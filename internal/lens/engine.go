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
}

func New(c Config, provider Embedder) (*Engine, error) {
	if e := c.Defaults(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(c.Cache, 0700); e != nil {
		return nil, e
	}
	lock := filepath.Join(c.Cache, "server.lock")
	f, e := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, fmt.Errorf("cache already owned; if previous server crashed remove %s after checking its PID: %w", lock, e)
	}
	fmt.Fprintln(f, os.Getpid())
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	eng := &Engine{C: c, ctx: ctx, cancel: cancel, lockfile: lock}
	if provider == nil {
		provider = NewProcessEmbedder(c)
	}
	eng.embed = provider
	eng.vectors, e = loadVectors(c)
	eng.status.State = "unloaded"
	eng.status.Vectors = "not_ready"
	if e != nil {
		eng.status.EmbeddingError = e.Error()
	}
	return eng, nil
}
func (e *Engine) Close() { e.cancel(); e.wg.Wait(); e.embed.Close(); os.Remove(e.lockfile) }
func (e *Engine) Refresh(ctx context.Context) error {
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
		s, err = analyze(ctx, e.C, files, fp)
		if err != nil {
			break
		}
		_, after, err = readFiles(ctx, e.C)
		if err != nil {
			break
		}
		if after == fp {
			break
		}
		s = nil
		err = fmt.Errorf("source files changed during analysis; retry refresh_index")
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
func (e *Engine) Status(ctx context.Context) Status {
	e.mu.RLock()
	st := e.status
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	st.GoHeapBytes = mem.HeapAlloc
	s := e.snap
	e.mu.RUnlock()
	if s != nil {
		_, fp, err := readFiles(ctx, e.C)
		st.Fresh = err == nil && fp == s.Fingerprint && st.State == "ready"
		if err != nil {
			st.LastError = err.Error()
		}
	}
	return st
}
func (e *Engine) Current(ctx context.Context) (*Snapshot, error) {
	e.mu.RLock()
	s := e.snap
	state := e.status.State
	e.mu.RUnlock()
	if s == nil || state != "ready" {
		return nil, fmt.Errorf("index is %s; call refresh_index then poll index_status", state)
	}
	_, fp, err := readFiles(ctx, e.C)
	if err != nil {
		return nil, err
	}
	if fp != s.Fingerprint {
		e.StartRefresh()
		return nil, fmt.Errorf("index stale; automatic refresh started; poll index_status and retry")
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
	warnings := []string{}
	switch a.Mode {
	case "lexical", "hybrid":
		lists["symbol"] = symbolRanks(s, a.Query)
		lists["text"] = s.Text.Search(a.Query)
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
			warnings = append(warnings, "vectors not ready for this snapshot; symbol/text only")
		} else {
			bctx, cancel := context.WithTimeout(ctx, time.Duration(e.C.OperationSeconds)*time.Second)
			r, err := e.embed.Embed(bctx, "query", []string{a.Query})
			cancel()
			if err != nil {
				warnings = append(warnings, "embedding runtime unavailable: "+err.Error())
			} else if len(r.Vectors) != 1 {
				warnings = append(warnings, "invalid query vector count")
			} else {
				scores := map[int]float64{}
				q := r.Vectors[0]
				if err = validVector(q, e.C.Dimension); err != nil {
					return nil, nil, err
				}
				for i, c := range s.Chunks {
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
			}
		}
	}
	if a.Mode == "vector" && lists["vector"] == nil {
		return nil, warnings, fmt.Errorf("vector search unavailable: %s", strings.Join(warnings, "; "))
	}
	return fuse(s, a, lists), warnings, nil
}
func (e *Engine) Call(ctx context.Context, name string, a Args) (Response, error) {
	if a.Limit < 0 || a.Limit > e.C.MaxResults {
		return Response{}, fmt.Errorf("limit must be 1..%d (or 0 for default)", e.C.MaxResults)
	}
	if a.Limit == 0 {
		a.Limit = min(20, e.C.MaxResults)
	}
	s, err := e.Current(ctx)
	if err != nil {
		return Response{}, err
	}
	r := Response{Snapshot: s.ID, Fresh: true, Complete: s.Complete, Limitations: append([]string{}, Limitations...), Diagnostics: s.Diagnostics}
	if a.Snapshot != "" && a.Snapshot != s.ID {
		return r, fmt.Errorf("snapshot changed; repeat search")
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
		for _, sym := range s.Symbols {
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
			x.Code = ""
			x.Comment = ""
			list = append(list, x)
		}
	case "get_symbol":
		sym := s.Symbols[a.Symbol]
		if sym == nil {
			return r, fmt.Errorf("unknown symbol_id")
		}
		x := *sym
		if len(x.Code) > e.C.MaxSourceBytes {
			x.Code = clip(x.Code, e.C.MaxSourceBytes)
			r.Truncated = true
			r.Meta = map[string]any{"continuation": "get_source using position.file, position.line and end_line"}
		}
		r.Items = x
	case "find_references", "get_callers", "get_callees", "find_field_accesses":
		if name == "find_field_accesses" && s.Symbols[a.Symbol] != nil && s.Symbols[a.Symbol].Kind != "field" {
			return r, fmt.Errorf("symbol_id must identify a field")
		}
		if s.Symbols[a.Symbol] == nil {
			return r, fmt.Errorf("unknown symbol_id")
		}
		for _, rel := range s.Relations {
			yes := false
			switch name {
			case "find_references":
				yes = rel.Symbol == a.Symbol && rel.Kind == "reference"
			case "get_callers":
				yes = rel.Kind == "call" && (rel.Symbol == a.Symbol || contains(rel.Candidates, a.Symbol))
			case "get_callees":
				yes = rel.Kind == "call" && rel.From == a.Symbol
			case "find_field_accesses":
				yes = rel.Symbol == a.Symbol && rel.Kind != "reference" && rel.Kind != "call"
			}
			if yes && relMatch(rel, a, s) {
				list = append(list, rel)
			}
		}
	case "get_source":
		file := filepath.ToSlash(a.File)
		b, ok := s.Files[file]
		if !ok || filepath.IsAbs(file) || strings.Contains(file, "../") {
			return r, fmt.Errorf("file must be an indexed root-relative path")
		}
		lines := strings.Split(string(b), "\n")
		start := a.Start
		if start == 0 {
			start = 1
		}
		end := a.End
		if end == 0 {
			end = min(start+39, len(lines))
		}
		if start < 1 || end < start || start > len(lines) || end > len(lines) {
			return r, fmt.Errorf("invalid 1-based line range")
		}
		used := 0
		last := start - 1
		out := []string{}
		for i := start - 1; i < end; i++ {
			if used+len(lines[i])+1 > e.C.MaxSourceBytes {
				break
			}
			out = append(out, lines[i])
			used += len(lines[i]) + 1
			last = i + 1
		}
		if last < end {
			r.Truncated = true
			r.Meta = map[string]any{"next_line": last + 1, "requested_end": end, "continuation": "get_source; a line exceeding max_source_bytes needs a larger server limit"}
		}
		r.Items = map[string]any{"file": file, "start_line": start, "end_line": last, "code": strings.Join(out, "\n")}
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
	case "build_context":
		return e.buildContext(ctx, s, a, r)
	default:
		return r, fmt.Errorf("unknown tool: %s", name)
	}
	if list != nil || name == "search_symbols" || name == "search_code" || strings.HasPrefix(name, "get_call") || name == "find_references" || name == "find_field_accesses" {
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
	if _, fp, e2 := readFiles(ctx, e.C); e2 != nil || fp != s.Fingerprint {
		return r, fmt.Errorf("files changed during query; refresh and retry")
	}
	return r, nil
}
func relMatch(r Relation, a Args, s *Snapshot) bool {
	if a.Path != "" && !strings.HasPrefix(r.Position.File, a.Path) {
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

func queryKey(name string, a Args) string { a.Cursor = ""; a.Limit = 0; return digest([]any{name, a}) }
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
