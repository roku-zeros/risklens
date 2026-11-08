package lens

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	queryCache   map[string][]float32
	queryOrder   []string
	rerankCache  map[string]float64
	rerankOrder  []string
	rerank       Reranker
	C            Config
	mu           sync.RWMutex
	refreshMu    sync.Mutex
	diskMu       sync.Mutex
	snap         *Snapshot
	status       Status
	vectors      vectorDisk
	embed        Embedder
	ctx          context.Context
	cancel       context.CancelFunc
	vectorCancel context.CancelFunc
	wg           sync.WaitGroup
	lockfile     string
	cacheLock    *os.File
	closeOnce    sync.Once
}

func New(c Config, provider Embedder) (*Engine, error) {
	if e := c.Defaults(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(c.Cache, 0700); e != nil {
		return nil, e
	}
	lock := filepath.Join(c.Cache, "server.lock")
	f, e := acquireCacheLock(lock)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	eng := &Engine{C: c, ctx: ctx, cancel: cancel, lockfile: lock, cacheLock: f}
	if provider == nil {
		provider = NewProcessEmbedder(c)
	}
	eng.embed = provider
	if len(c.RerankCommand) > 0 {
		eng.rerank = newReranker(c)
	}
	eng.vectors, e = loadVectors(c)
	eng.status.State = "unloaded"
	eng.status.Vectors = "not_ready"
	if e != nil {
		eng.status.EmbeddingError = e.Error()
	}
	return eng, nil
}
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		e.cancel()
		e.wg.Wait()
		e.embed.Close()
		if e.rerank != nil {
			e.rerank.Close()
		}
		if e.cacheLock != nil {
			e.cacheLock.Close()
		}
	})
}
func (e *Engine) Refresh(ctx context.Context) error {
	start := time.Now()
	ctx, timings := startTimings(ctx)
	defer func() { e.mu.Lock(); e.status.LastRefresh = timings.metadata(start); e.mu.Unlock() }()
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	e.status.Loading = true
	e.status.State = "loading"
	e.status.LastError = ""
	if e.vectorCancel != nil {
		e.vectorCancel()
	}
	e.mu.Unlock()
	var s *Snapshot
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var files map[string][]byte
		var fp, after string
		files, fp, err = readFiles(ctx, e.C)
		if err != nil {
			break
		}
		phase := time.Now()
		s, err = analyze(ctx, e.C, files, fp)
		recordTime(ctx, "load_analysis", phase)
		if err != nil {
			break
		}
		after, err = fingerprintFiles(ctx, e.C)
		if err != nil {
			break
		}
		if after == fp {
			break
		}
		s = nil
		err = fmt.Errorf("source files changed during analysis; retry refresh_index")
	}
	// The loader/type-checker graph is not retained by Snapshot. Collect it once before vectorization, never per query.
	if err == nil && s != nil {
		runtime.GC()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.Loading = false
	if err != nil || s == nil {
		e.status.State = "failed"
		e.status.Fresh = false
		if err == nil {
			err = fmt.Errorf("analysis unavailable")
		}
		e.status.LastError = err.Error()
		return err
	}
	e.snap = s
	e.status.State = "ready"
	e.status.Fresh = true
	e.status.Snapshot = s.ID
	e.status.Symbols = len(s.Symbols)
	e.status.Chunks = len(s.Chunks)
	e.status.Files = len(s.Files)
	e.status.Complete = s.Complete
	e.status.Diagnostics = s.Diagnostics
	e.status.Environment = s.Environment
	e.status.Module = s.Module
	e.status.Calls = s.Calls
	e.status.VectorTotal = len(s.Chunks)
	e.status.VectorDone = 0
	e.status.CacheHits = 0
	e.status.Vectors = "building"
	vctx, cancel := context.WithCancel(e.ctx)
	e.vectorCancel = cancel
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.updateVectors(vctx, s) }()
	return nil
}
func (e *Engine) StartRefresh() bool {
	e.mu.Lock()
	if e.status.Loading {
		e.mu.Unlock()
		return false
	}
	e.status.Loading = true
	e.mu.Unlock()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(e.ctx, time.Duration(e.C.OperationSeconds)*time.Second)
		defer cancel()
		_ = e.Refresh(ctx)
	}()
	return true
}
func (e *Engine) CachedStatus() Status {
	e.mu.RLock()
	st := e.status
	st.Version = Version
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	st.GoHeapBytes = mem.HeapAlloc
	st.GoHeapReservedBytes = mem.HeapSys
	st.GoTotalAllocBytes = mem.TotalAlloc
	st.GoGCCycles = mem.NumGC
	st.FreshnessScope = "Saved Go sources/go.mod/go.sum in this module and load configuration; not documentation claims or business correctness."
	e.mu.RUnlock()
	st.Fresh = false
	return st
}
func (e *Engine) Status(ctx context.Context) (st Status) {
	start := time.Now()
	ctx, timings := startTimings(ctx)
	defer func() { st.Meta = timings.metadata(start) }()
	st = e.CachedStatus()
	e.mu.RLock()
	s := e.snap
	e.mu.RUnlock()
	if s != nil {
		fp, err := fingerprintFiles(ctx, e.C)
		st.Fresh = err == nil && fp == s.Fingerprint && st.State == "ready"
		if err != nil {
			st.LastError = err.Error()
		}
	}
	return st
}
func (e *Engine) Current(ctx context.Context) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s := pinnedSnapshot(ctx); s != nil {
		return s, nil
	}
	s, err := e.readySnapshot()
	if err != nil {
		return nil, err
	}
	fp, err := fingerprintFiles(ctx, e.C)
	if err != nil {
		return nil, err
	}
	if fp != s.Fingerprint {
		e.StartRefresh()
		return nil, fmt.Errorf("index stale; automatic refresh started; poll index_status and retry")
	}
	return s, nil
}
func (e *Engine) readySnapshot() (*Snapshot, error) {
	e.mu.RLock()
	s := e.snap
	state := e.status.State
	e.mu.RUnlock()
	if s == nil || state != "ready" {
		return nil, fmt.Errorf("index is %s; call refresh_index then poll index_status", state)
	}
	return s, nil
}
func (e *Engine) updateVectors(ctx context.Context, s *Snapshot) {
	e.mu.RLock()
	old := e.vectors
	e.mu.RUnlock()
	d := vectorDisk{Version: 1, Model: e.C.ModelKey, Dimension: e.C.Dimension, Snapshot: s.ID, Records: map[string]VectorRecord{}, Chunks: map[string]string{}}
	missing := []*Chunk{}
	for _, c := range s.Chunks {
		d.Chunks[c.ID] = c.Hash
		if r, ok := old.Records[c.Hash]; ok {
			d.Records[c.Hash] = r
		} else {
			missing = append(missing, c)
		}
	}
	done := len(s.Chunks) - len(missing)
	e.mu.Lock()
	if e.snap == s {
		e.status.VectorDone = done
		e.status.CacheHits = done
	}
	e.mu.Unlock()
	for i := 0; i < len(missing); i += e.C.BatchSize {
		if ctx.Err() != nil {
			return
		}
		end := min(i+e.C.BatchSize, len(missing))
		texts := []string{}
		for _, c := range missing[i:end] {
			texts = append(texts, c.Input)
		}
		bctx, cancel := context.WithTimeout(ctx, time.Duration(e.C.OperationSeconds)*time.Second)
		r, err := e.embed.Embed(bctx, "document", texts)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.mu.Lock()
			if e.snap == s {
				e.status.Vectors = "degraded"
				e.status.EmbeddingError = err.Error()
			}
			e.mu.Unlock()
			return
		}
		if len(r.Vectors) != len(texts) || len(r.Tokens) != len(texts) || len(r.Windows) != len(texts) {
			e.mu.Lock()
			e.status.Vectors = "degraded"
			e.status.EmbeddingError = "invalid provider batch"
			e.mu.Unlock()
			return
		}
		for j, c := range missing[i:end] {
			if err := validVector(r.Vectors[j], e.C.Dimension); err != nil {
				e.mu.Lock()
				e.status.Vectors = "degraded"
				e.status.EmbeddingError = err.Error()
				e.mu.Unlock()
				return
			}
			d.Records[c.Hash] = VectorRecord{r.Vectors[j], r.Tokens[j], r.Windows[j]}
		}
		done += end - i
		e.mu.Lock()
		if e.snap == s {
			e.status.VectorDone = done
		}
		e.mu.Unlock()
	}
	if ctx.Err() != nil {
		return
	}
	e.diskMu.Lock()
	defer e.diskMu.Unlock()
	e.mu.RLock()
	current := e.snap == s
	e.mu.RUnlock()
	if !current || ctx.Err() != nil {
		return
	}
	err := saveVectors(e.C, d)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snap != s || ctx.Err() != nil {
		return
	}
	if err != nil {
		e.status.Vectors = "degraded"
		e.status.EmbeddingError = err.Error()
		return
	}
	e.vectors = d
	e.status.Vectors = "ready"
	e.status.EmbeddingError = ""
}

func (e *Engine) WaitVectors(ctx context.Context) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		e.mu.RLock()
		st := e.status
		st.Version = Version
		e.mu.RUnlock()
		if st.Vectors == "ready" {
			return nil
		}
		if st.Vectors == "degraded" {
			return errors.New(st.EmbeddingError)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func (e *Engine) Search(ctx context.Context, s *Snapshot, a Args) ([]Hit, []string, error) {
	if strings.TrimSpace(a.Query) == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	if len(a.Query) > 16000 {
		return nil, nil, fmt.Errorf("query exceeds 16000 bytes")
	}
	if a.Mode == "" {
		a.Mode = "hybrid"
	}
	lists := map[string][]rank{}
	candidates := s.searchChunks(a.Package)
	recordStage(ctx, "search_scope", map[string]any{"package": a.Package, "candidate_chunks": len(candidates), "index_chunks": len(s.Chunks)})
	warnings := []string{}
	switch a.Mode {
	case "lexical", "hybrid":
		phase := time.Now()
		lists["symbol"] = symbolRanks(s, a.Query, candidates)
		lists["text"] = s.Text.search(a.Query, s, a.Package, candidates)
		if s.PackageSearch != nil && (a.PackageContext == nil || *a.PackageContext) {
			lists["package_context"] = s.PackageSearch.rank(s, a, lists["symbol"], lists["text"])
		}
		recordTime(ctx, "symbol_text", phase)
	case "vector":
	default:
		return nil, nil, fmt.Errorf("mode must be lexical, vector or hybrid")
	}
	if a.Mode != "lexical" {
		e.mu.RLock()
		d := e.vectors
		ready := d.Snapshot == s.ID && e.status.Vectors == "ready"
		e.mu.RUnlock()
		if !ready {
			warnings = append(warnings, "vectors not ready for this snapshot; using available lexical channels")
		} else {
			bctx, cancel := context.WithTimeout(ctx, time.Duration(e.C.OperationSeconds)*time.Second)
			phase := time.Now()
			r, err := e.embedQuery(bctx, a.Query)
			recordTime(ctx, "query_embedding", phase)
			cancel()
			if err != nil {
				warnings = append(warnings, "embedding runtime unavailable: "+err.Error())
			} else if len(r.Vectors) != 1 {
				warnings = append(warnings, "invalid query vector count")
			} else {
				phase := time.Now()
				scores := map[int]float64{}
				q := r.Vectors[0]
				if err = validVector(q, e.C.Dimension); err != nil {
					return nil, nil, err
				}
				for _, i := range candidates {
					c := s.Chunks[i]
					if !match(s.Symbols[c.Symbol], a) {
						continue
					}
					record, ok := d.Records[c.Hash]
					if !ok {
						continue
					}
					score := 0.
					for j, v := range q {
						score += float64(v * record.Vector[j])
					}
					if !math.IsNaN(score) {
						scores[i] = score
					}
				}
				lists["vector"] = sortedRanks(scores)
				recordTime(ctx, "vector_scan_sort", phase)
				recordStage(ctx, "vector_candidates", len(scores))
			}
		}
	}
	if a.Mode == "vector" && lists["vector"] == nil {
		return nil, warnings, fmt.Errorf("vector search unavailable: %s", strings.Join(warnings, "; "))
	}
	phase := time.Now()
	hits := fuse(s, a, lists)
	recordTime(ctx, "fusion", phase)
	phase = time.Now()
	hits, warning := e.rerankHits(ctx, a, hits)
	recordTime(ctx, "rerank", phase)
	if warning != "" {
		warnings = append(warnings, warning)
	}
	return hits, warnings, nil
}
func (e *Engine) Call(ctx context.Context, name string, a Args) (r Response, err error) {
	started := time.Now()
	ctx, timings := startTimings(ctx)
	fast := false
	defer func() {
		if r.Meta == nil {
			r.Meta = map[string]any{}
		}
		for k, v := range timings.metadata(started) {
			r.Meta[k] = v
		}
		if fast {
			r.Fresh = false
			r.Meta["freshness_policy"] = "snapshot; saved files were not checked. Use freshness=strict or batch_tools to validate against disk."
		}
		r.Meta["results_complete"] = err == nil && !r.Truncated
		r.Meta["effective_limit"] = a.Limit
		if r.Next != "" {
			r.Meta["has_more"] = true
			r.Meta["continuation"] = "Repeat the same tool and arguments with cursor=next_cursor and index_snapshot; this is a partial result page."
		}
		if err != nil {
			r.Fresh = false
		}
	}()
	if name == "audit_document" && !validSourcePath(a.Document) {
		return r, inputError("invalid_document_path", "document", "document must be a root-relative path with forward slashes", map[string]any{"hint": "Use document relative to config.root, not an absolute OS path."})
	}
	if name == "get_source" && !validSourcePath(a.File) {
		return r, inputError("invalid_file_path", "file", "file must be root-relative; use list_files and copy file_path", map[string]any{"hint": "Use forward slashes and a path relative to config.root, not an absolute path."})
	}
	if a.Limit < 0 || a.Limit > e.C.MaxResults {
		return r, inputError("invalid_limit", "limit", fmt.Sprintf("limit must be 1..%d (or 0 for default); paginate with next_cursor", e.C.MaxResults), map[string]any{"minimum": 1, "maximum": e.C.MaxResults, "default": min(20, e.C.MaxResults), "hint": "Use the returned next_cursor to read additional results."})
	}
	if a.Limit == 0 {
		a.Limit = min(20, e.C.MaxResults)
	}
	if a.Freshness != "" && a.Freshness != "snapshot" && a.Freshness != "strict" {
		return r, inputError("invalid_freshness", "freshness", "freshness must be snapshot or strict", nil)
	}
	if a.Freshness != "" && name != "project_map" && name != "get_source" && name != "get_symbol" && name != "search_code" && name != "build_context" {
		return r, inputError("invalid_freshness", "freshness", "this tool does not support freshness selection", nil)
	}
	if a.Freshness == "snapshot" && name != "project_map" && a.Snapshot == "" && pinnedSnapshot(ctx) == nil {
		return r, inputError("snapshot_required", "index_snapshot", "copy index_snapshot from project_map for snapshot reads", nil)
	}
	if name == "project_map" {
		if err := validateMapArgs(&a); err != nil {
			return r, err
		}
	}
	fast = pinnedSnapshot(ctx) == nil && (a.Freshness == "snapshot" || (name == "project_map" && a.Freshness == ""))
	var s *Snapshot
	if fast {
		s, err = e.readySnapshot()
	} else {
		s, err = e.Current(ctx)
	}
	if err != nil {
		return r, err
	}
	r = Response{Snapshot: s.ID, Fresh: true, Complete: s.Complete, Limitations: append([]string{}, Limitations...), Diagnostics: s.Diagnostics}
	if a.Snapshot != "" && a.Snapshot != s.ID {
		return r, fmt.Errorf("snapshot changed: requested %s, current %s; repeat discovery and use returned IDs", a.Snapshot, s.ID)
	}
	if fast {
		ctx = context.WithValue(ctx, pinnedSnapshotKey{}, s)
	}
	if name == "project_map" {
		if r.Meta == nil {
			r.Meta = map[string]any{}
		}
		r, err = mapPage(ctx, s, a, r)
		if err != nil {
			return r, err
		}
		return r, validateSnapshot(ctx, e.C, s)
	}
	pageName := name
	if name == "search_code" && a.Mode != "lexical" {
		e.mu.RLock()
		pageName += ":" + e.status.Vectors + ":" + e.vectors.Snapshot + ":" + e.C.ModelKey
		e.mu.RUnlock()
	}
	var list []any
	switch name {
	case "search_symbols":
		syms := []*Symbol{}
		scoped := map[string][]string{}
		if a.OwnerID != "" {
			owner := s.Symbols[a.OwnerID]
			if owner == nil {
				return r, unknownSymbol(s, a.OwnerID, "type")
			}
			if owner.Kind != "type" {
				return r, fmt.Errorf("owner_id must identify a type")
			}
			for _, f := range s.TypeFields[a.OwnerID] {
				if f.Available {
					scoped[f.Symbol] = f.AccessPath
				}
			}
		}
		for _, sym := range s.Symbols {
			if a.OwnerID != "" {
				if _, ok := scoped[sym.ID]; !ok {
					continue
				}
			}
			if match(sym, a) && (a.Query == "" || strings.Contains(strings.ToLower(sym.Name+" "+sym.Signature), strings.ToLower(a.Query))) {
				syms = append(syms, sym)
			}
		}
		sort.Slice(syms, func(i, j int) bool {
			ei, ej := strings.EqualFold(syms[i].Name, a.Query), strings.EqualFold(syms[j].Name, a.Query)
			if ei != ej {
				return ei
			}
			return syms[i].ID < syms[j].ID
		})
		for _, v := range syms {
			x := *v
			x.AccessPath = scoped[v.ID]
			x.Code = ""
			x.Comment = ""
			list = append(list, x)
		}
		if len(syms) == 0 && a.Query != "" {
			r.Meta = map[string]any{"exact_match": false, "suggestions": suggestSymbols(s, a, 8), "hint": "No declaration matched. Suggestions are real symbols, not verified replacements. Validate documentation references against get_symbol/get_source in this snapshot."}
		}
	case "get_symbol":
		sym := s.Symbols[a.Symbol]
		if sym == nil {
			return r, unknownSymbol(s, a.Symbol, "")
		}
		x := *sym
		if len(x.Code) > e.C.MaxSourceBytes {
			x.Code = clip(x.Code, e.C.MaxSourceBytes)
			r.Truncated = true
			r.Meta = map[string]any{"continuation": "get_source using position.file, position.line and end_line"}
		}
		if fields, ok := s.TypeFields[a.Symbol]; ok {
			if r.Meta == nil {
				r.Meta = map[string]any{}
			}
			r.Meta["fields"] = fields[:min(a.Limit, len(fields))]
			r.Meta["field_count"] = len(fields)
			r.Meta["fields_truncated"] = len(fields) > a.Limit
			r.Truncated = r.Truncated || len(fields) > a.Limit
			r.Meta["field_continuation"] = "search_symbols with owner_id equal to this returned type ID, kind=field, and optional query/cursor"
		}
		if r.Meta == nil {
			r.Meta = map[string]any{}
		}
		r.Meta["source_hash"] = hash(s.Files[x.Position.File])
		r.Items = x
	case "find_references", "get_callers", "get_callees", "find_field_accesses":
		if name == "find_field_accesses" && s.Symbols[a.Symbol] != nil && s.Symbols[a.Symbol].Kind != "field" {
			return r, fmt.Errorf("symbol_id must identify a field")
		}
		if s.Symbols[a.Symbol] == nil {
			return r, unknownSymbol(s, a.Symbol, "")
		}
		if name == "get_callees" {
			r.Meta = map[string]any{"call_coverage": callCoverage(s, a.Symbol, a)}
		}
		if name == "get_callers" || name == "find_references" {
			r.Meta = map[string]any{"absence_is_not_proven": true, "coverage_note": "Unknown call targets cannot be attributed to this symbol. Inspect get_callees at entry functions for unresolved/external boundaries."}
		}
		for _, rel := range s.Relations {
			yes := false
			switch name {
			case "find_references":
				yes = rel.Symbol == a.Symbol && rel.Kind == "reference"
			case "get_callers":
				yes = rel.Kind == "call" && (rel.Symbol == a.Symbol || contains(rel.Candidates, a.Symbol) || contains(rel.FlowCandidates, a.Symbol))
			case "get_callees":
				yes = rel.Kind == "call" && rel.From == a.Symbol
			case "find_field_accesses":
				yes = rel.Symbol == a.Symbol && rel.Kind != "reference" && rel.Kind != "call"
			}
			if yes && relMatch(rel, a, s) {
				list = append(list, rel)
			}
		}
	case "rule_membership":
		if s.Symbols[a.Symbol] == nil {
			return r, unknownSymbol(s, a.Symbol, "")
		}
		facts, supported := s.Membership[a.Symbol]
		list = make([]any, 0, len(facts.Rows))
		for _, fact := range facts.Rows {
			list = append(list, fact)
		}
		r.Meta = map[string]any{
			"adapter":            "explicit_go_slice_return",
			"source_binding":     "indexed_declaration",
			"source_hash":        hash(s.Files[s.Symbols[a.Symbol].Position.File]),
			"execution":          "unverified",
			"requirement":        "unreviewed",
			"coverage":           "unknown",
			"analysis_truncated": facts.Truncated,
		}
		if supported && len(facts.Rows) > 0 {
			r.Meta["coverage"] = "observed_return_expressions"
			for _, fact := range facts.Rows {
				if fact.Status == "unknown" {
					r.Meta["coverage"] = "partial"
					break
				}
			}
		}
		if facts.Truncated {
			r.Meta["coverage"] = "partial"
			r.Complete = false
		}
		r.Limitations = append(r.Limitations, "Explicit return expressions only. Lexical guards omit implicit path predicates, reachability and inner rule conditions. Unknown results cannot establish absence.")
	case "resolve_reference":
		if strings.TrimSpace(a.Query) == "" {
			return r, fmt.Errorf("query is required")
		}
		if a.Namespace != "" && a.Namespace != "auto" && a.Namespace != "go" && a.Namespace != "json" {
			return r, fmt.Errorf("namespace must be auto, go or json")
		}
		if a.OwnerID != "" {
			if _, ok := s.TypeFields[a.OwnerID]; !ok {
				return r, unknownSymbol(s, a.OwnerID, "type")
			}
		}
		resolved := resolveFields(s, a)
		for _, m := range resolved {
			list = append(list, m)
		}
		r.Meta = map[string]any{"scope_truncated": s.FieldScopeTruncated, "case_sensitive": true, "semantic_verification": false, "limitation": "Declaration aliases and typed Go field paths. JSON names are leaf declaration aliases, not proof of serialization paths, execution or the same runtime object. owner_id may be an opaque returned target_scope_id."}
	case "list_files":
		names := make([]string, 0, len(s.Files))
		for file := range s.Files {
			names = append(names, file)
		}
		sort.Strings(names)
		for _, file := range names {
			if !matchPath(file, a) || (a.IncludeTests != nil && !*a.IncludeTests && strings.HasSuffix(file, "_test.go")) {
				continue
			}
			b := s.Files[file]
			list = append(list, map[string]any{"file_path": file, "total_lines": sourceLineCount(b), "bytes": len(b), "source_hash": hash(b)})
		}
	case "get_source":
		r, err = e.source(s, a, r)
		if err != nil {
			return r, err
		}
	case "search_code":
		hits, warnings, e2 := e.Search(ctx, s, a)
		if e2 != nil {
			return r, e2
		}
		r.Limitations = append(r.Limitations, warnings...)
		pageName += ":" + digest(warnings)
		for _, v := range hits {
			list = append(list, v)
		}
	case "audit_document":
		return e.auditDocument(ctx, s, a, r)
	case "verify_evidence":
		return e.verifyEvidence(ctx, s, a, r)
	case "trace_field":
		return e.traceField(ctx, s, a, r)
	case "build_context":
		return e.buildContext(ctx, s, a, r)
	default:
		return r, fmt.Errorf("unknown tool: %s", name)
	}
	if list != nil || name == "list_files" || name == "resolve_reference" || name == "search_symbols" || name == "search_code" || strings.HasPrefix(name, "get_call") || name == "find_references" || name == "find_field_accesses" {
		if list == nil {
			list = []any{}
		}
		start, e2 := decodeCursor(a.Cursor, s.ID, pageName, a)
		if e2 != nil {
			return r, e2
		}
		if start > len(list) {
			return r, fmt.Errorf("cursor offset outside results")
		}
		end := min(start+a.Limit, len(list))
		if r.Meta == nil {
			r.Meta = map[string]any{}
		}
		r.Meta["total_results"] = len(list)
		r.Meta["returned_results"] = end - start
		r.Meta["offset"] = start
		r.Meta["has_more"] = end < len(list)
		r.Items = list[start:end]
		if r.Items == nil {
			r.Items = []any{}
		}
		r.Truncated = end < len(list)
		if r.Truncated {
			r.Next = encodeCursor(s.ID, pageName, a, end)
		}
	}
	// Revalidate after slower inference/context work. Positions and source always come from s.
	if err := validateSnapshot(ctx, e.C, s); err != nil {
		return r, err
	}
	return r, nil
}
func relMatch(r Relation, a Args, s *Snapshot) bool {
	if !matchPath(r.Position.File, a) {
		return false
	}
	if a.IncludeTests != nil && !*a.IncludeTests && strings.HasSuffix(r.Position.File, "_test.go") {
		return false
	}
	if a.Package != "" {
		if sym := s.Symbols[r.From]; sym != nil && sym.Package != a.Package {
			return false
		}
	}
	return true
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}

type cursor struct {
	Snapshot string `json:"snapshot"`
	Query    string `json:"query"`
	Offset   int    `json:"offset"`
}

func queryKey(name string, a Args) string {
	a.Cursor = ""
	a.Limit = 0
	a.Snapshot = ""
	return digest([]any{name, a})
}
func encodeCursor(s, name string, a Args, offset int) string {
	b, _ := json.Marshal(cursor{s, queryKey(name, a), offset})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(raw, s, name string, a Args) (int, error) {
	if raw == "" {
		return 0, nil
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Offset < 0 || c.Query != queryKey(name, a) {
		return 0, fmt.Errorf("cursor does not match query")
	}
	if c.Snapshot != s {
		return 0, fmt.Errorf("cursor snapshot changed; restart pagination")
	}
	return c.Offset, nil
}
