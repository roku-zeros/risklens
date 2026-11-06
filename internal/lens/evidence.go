package lens

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type EvidenceClaim struct {
	Symbol   string `json:"symbol_id,omitempty"`
	File     string `json:"file,omitempty"`
	Start    int    `json:"start_line,omitempty"`
	End      int    `json:"end_line,omitempty"`
	Expected string `json:"expected_text,omitempty"`
	Hash     string `json:"source_hash,omitempty"`
}
type EvidenceCheck struct {
	Bindings        []ReferenceMatch          `json:"bindings,omitempty"`
	BindingCount    int                       `json:"binding_count,omitempty"`
	CandidateGroups []CandidateSignatureGroup `json:"candidate_signature_groups,omitempty"`
	Reference       string                    `json:"reference,omitempty"`
	DocumentLine    int                       `json:"document_line,omitempty"`
	Status          string                    `json:"status"`
	Claim           EvidenceClaim             `json:"claim"`
	Actual          *EvidenceClaim            `json:"actual,omitempty"`
	Excerpt         string                    `json:"source_excerpt,omitempty"`
	Candidates      []Symbol                  `json:"candidates,omitempty"`
	Note            string                    `json:"note"`
}

func sourceLines(b []byte, start, end int) string {
	lines := strings.Split(string(b), "\n")
	if start < 1 || end < start || end > len(lines) {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}
func checkEvidence(s *Snapshot, c EvidenceClaim) EvidenceCheck {
	out := EvidenceCheck{Claim: c, Status: "unbound", Note: "Source location/text only; business meaning, reachability and execution order are not verified."}
	var sym *Symbol
	if c.Symbol != "" {
		sym = s.Symbols[c.Symbol]
		if sym == nil {
			out.Status = "missing_symbol"
			out.Candidates = unknownSymbol(s, c.Symbol, "").Candidates
			return out
		}
		actual := EvidenceClaim{Symbol: sym.ID, File: sym.Position.File, Start: sym.Position.Line, End: sym.EndLine, Hash: hash(s.Files[sym.Position.File])}
		out.Actual = &actual
		if c.File != "" && path.Clean(c.File) != sym.Position.File {
			out.Status = "moved_symbol"
			return out
		}
		if c.File == "" {
			c.File = sym.Position.File
		}
		if c.Start == 0 {
			c.Start = sym.Position.Line
			c.End = sym.EndLine
		}
	}
	c.File = path.Clean(c.File)
	b, ok := s.Files[c.File]
	if !ok || !strings.HasSuffix(c.File, ".go") {
		out.Status = "missing_file"
		return out
	}
	if c.Hash != "" && c.Hash != hash(b) {
		out.Status = "source_changed"
		return out
	}
	if c.Start == 0 {
		out.Status = "file_exists_unbound"
		out.Note += " A file without a symbol/text anchor cannot establish that a documentation statement is current."
		return out
	}
	if c.End == 0 {
		c.End = c.Start
	}
	if c.Start < 1 || c.End < c.Start || c.End > 1+strings.Count(string(b), "\n") {
		out.Status = "invalid_range"
	} else {
		snippet := sourceLines(b, c.Start, c.End)
		out.Excerpt = clip(snippet, 800)
		if c.Expected != "" {
			if strings.Contains(snippet, c.Expected) {
				out.Status = "source_verified"
			} else {
				out.Status = "text_mismatch"
			}
		} else if sym != nil && c.Start >= sym.Position.Line && c.End <= sym.EndLine {
			out.Status = "symbol_location_verified"
		} else {
			out.Status = "range_exists_unbound"
		}
	}
	if c.Expected != "" && out.Status != "source_verified" && strings.Count(string(b), c.Expected) == 1 {
		offset := strings.Index(string(b), c.Expected)
		line := 1 + strings.Count(string(b[:offset]), "\n")
		actual := EvidenceClaim{File: c.File, Start: line, End: line + strings.Count(c.Expected, "\n"), Expected: c.Expected, Hash: hash(b)}
		out.Actual = &actual
		out.Status = "moved_text"
	}
	return out
}
func (e *Engine) verifyEvidence(ctx context.Context, s *Snapshot, a Args, r Response) (Response, error) {
	if len(a.Claims) == 0 || len(a.Claims) > 100 {
		return r, fmt.Errorf("provide 1..100 claims")
	}
	items := []EvidenceCheck{}
	for _, c := range a.Claims {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if len(c.Expected) > 4096 {
			return r, fmt.Errorf("expected_text exceeds 4096 bytes")
		}
		items = append(items, checkEvidence(s, c))
	}
	return e.evidencePage(ctx, s, a, r, "verify_evidence", items, nil)
}
func (e *Engine) evidencePage(ctx context.Context, s *Snapshot, a Args, r Response, name string, items []EvidenceCheck, meta map[string]any) (Response, error) {
	start, err := decodeCursor(a.Cursor, s.ID, name, a)
	if err != nil {
		return r, err
	}
	if start > len(items) {
		return r, fmt.Errorf("cursor outside evidence results")
	}
	end := min(start+a.Limit, len(items))
	r.Items = items[start:end]
	r.Truncated = end < len(items)
	if r.Truncated {
		r.Next = encodeCursor(s.ID, name, a, end)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	counts := map[string]int{}
	for _, c := range items {
		counts[c.Status]++
	}
	meta["total_checks"] = len(items)
	meta["total_results"] = len(items)
	meta["returned_results"] = end - start
	meta["offset"] = start
	meta["has_more"] = end < len(items)
	meta["status_counts"] = counts
	meta["semantic_verification"] = false
	r.Meta = meta
	if err := validateSnapshot(ctx, e.C, s); err != nil {
		return r, err
	}
	return r, nil
}

var markdownLinks = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
var inlineCode = regexp.MustCompile("`([^`\\n]+)`")
var sourceReference = regexp.MustCompile(`^(.+\.go)(?:(?::|#)L?([0-9]+)(?:-L?([0-9]+))?)?$`)
var docIdentifier = regexp.MustCompile(`^[\pL_][\pL\pN_]*(?:\.[\pL_][\pL\pN_]*)*(?:\(\))?$`)

func (e *Engine) auditDocument(ctx context.Context, s *Snapshot, a Args, r Response) (Response, error) {
	if a.Document == "" {
		return r, fmt.Errorf("document is required")
	}
	full, err := filepath.EvalSymlinks(filepath.Join(e.C.Root, filepath.FromSlash(a.Document)))
	if err != nil {
		return r, err
	}
	rel, err := filepath.Rel(e.C.Root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(a.Document) {
		return r, fmt.Errorf("document must be inside module root")
	}
	info, err := os.Stat(full)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return r, fmt.Errorf("document must be a regular UTF-8 text file up to 1 MiB")
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return r, err
	}
	if !utf8.Valid(raw) {
		return r, fmt.Errorf("document must be valid UTF-8")
	}
	text := string(raw)
	docHash := hash(raw)
	starts := []int{0}
	for i, b := range raw {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	docLine := func(offset int) int { return sort.Search(len(starts), func(i int) bool { return starts[i] > offset }) }
	checks := []EvidenceCheck{}
	seen := map[string]bool{}
	add := func(ref string, offset int) {
		key := fmt.Sprintf("%d:%s", offset, ref)
		if seen[key] {
			return
		}
		seen[key] = true
		line := docLine(offset)
		parsed := sourceReference.FindStringSubmatch(ref)
		if parsed != nil {
			var lo, hi int
			if parsed[2] != "" {
				fmt.Sscanf(parsed[2], "%d", &lo)
				hi = lo
				if parsed[3] != "" {
					fmt.Sscanf(parsed[3], "%d", &hi)
				}
			}
			file := parsed[1]
			u, ue := url.Parse(file)
			if ue == nil && u.Scheme != "" {
				checks = append(checks, EvidenceCheck{Reference: ref, DocumentLine: line, Status: "external_not_checked", Note: "External links are not fetched."})
				return
			}
			file, _ = url.PathUnescape(file)
			direct := path.Clean(file)
			relative := path.Clean(path.Join(path.Dir(filepath.ToSlash(rel)), file))
			_, hasDirect := s.Files[direct]
			_, hasRelative := s.Files[relative]
			if hasDirect && hasRelative && direct != relative {
				checks = append(checks, EvidenceCheck{Reference: ref, DocumentLine: line, Status: "ambiguous_path", Note: "Both module-relative and document-relative Go paths exist; select explicitly."})
				return
			}
			if hasRelative {
				file = relative
			} else {
				file = direct
			}
			c := checkEvidence(s, EvidenceClaim{File: file, Start: lo, End: hi})
			c.Reference = ref
			c.DocumentLine = line
			checks = append(checks, c)
			return
		}
		if strings.Contains(ref, ".go") {
			checks = append(checks, EvidenceCheck{Reference: ref, DocumentLine: line, Status: "unsupported_reference", Note: "Unsupported Go-reference format; provide a structured verify_evidence claim."})
			return
		}
		if check, ok := auditFieldReference(s, ref, line); ok {
			checks = append(checks, check)
			return
		}
		if !docIdentifier.MatchString(ref) {
			return
		}
		name := strings.TrimSuffix(ref, "()")
		if !strings.ContainsAny(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ.") && !strings.HasSuffix(ref, "()") && !strings.Contains(name, "_") {
			return
		}
		found := []*Symbol{}
		parts := strings.Split(name, ".")
		last := parts[len(parts)-1]
		for _, sym := range s.Symbols {
			if sym.Name != last {
				continue
			}
			if strings.HasSuffix(ref, "()") && sym.Kind != "function" && sym.Kind != "method" {
				continue
			}
			if len(parts) > 1 {
				ownerName := parts[len(parts)-2]
				if sym.Kind == "field" {
					continue
				} // An unresolved typed field path is not a verified source binding.
				if !strings.Contains(sym.Package, ownerName) && !strings.Contains(sym.Receiver, ownerName) {
					continue
				}
			}
			found = append(found, sym)
		}
		sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
		if len(found) == 1 {
			c := checkEvidence(s, EvidenceClaim{Symbol: found[0].ID})
			c.Reference = ref
			c.DocumentLine = line
			checks = append(checks, c)
		} else {
			c := EvidenceCheck{Reference: ref, DocumentLine: line, Status: "missing_symbol", Note: "Names are not semantic claims. Resolve and bind the intended canonical ID before changing a document."}
			if len(found) > 1 {
				c.Status = "ambiguous_symbol"
				c.CandidateGroups = signatureGroups(found)
				for _, sym := range found[:min(8, len(found))] {
					c.Candidates = append(c.Candidates, summary(sym))
				}
			} else {
				if strings.Contains(name, ".") {
					c.Status = "unbound_path"
					c.Note = "No typed path binding in scope. Specify the root owner with resolve_reference; this is not evidence that the document is wrong."
				} else if strings.Contains(name, "_") {
					c.Status = "unrecognized_token"
					c.Note = "Unbound contract/prose token, not a verified missing Go declaration. Use exact JSON aliases or explicit source claims."
				}
				c.Candidates = suggestSymbols(s, Args{Query: last}, 8)
			}
			checks = append(checks, c)
		}
	}
	for _, m := range markdownLinks.FindAllStringSubmatchIndex(text, -1) {
		target := text[m[2]:m[3]]
		if strings.Contains(target, ".go") {
			add(target, m[0])
		}
	}
	for _, m := range inlineCode.FindAllStringSubmatchIndex(text, -1) {
		add(text[m[2]:m[3]], m[0])
	}
	after, err := os.ReadFile(full)
	if err != nil || hash(after) != docHash {
		return r, fmt.Errorf("document changed during audit; retry")
	}
	return e.evidencePage(ctx, s, a, r, "audit_document:"+docHash, checks, map[string]any{"document": filepath.ToSlash(rel), "document_hash": docHash, "field_scope_truncated": s.FieldScopeTruncated, "parser_scope": "Markdown Go links, typed Go field paths, exact JSON declaration aliases and symbol references. Prose/business claims and JSON serialization paths are not verified."})
}
