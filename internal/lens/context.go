package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ContextItem struct {
	Symbol string    `json:"symbol_id"`
	Chunk  string    `json:"chunk_id,omitempty"`
	File   string    `json:"file"`
	Start  int       `json:"start_line"`
	End    int       `json:"end_line"`
	Code   string    `json:"code"`
	Reason string    `json:"reason"`
	Via    *Relation `json:"via,omitempty"`
}

func (e *Engine) buildContext(ctx context.Context, s *Snapshot, a Args, r Response) (Response, error) {
	if a.Depth < 0 || a.Depth > 3 {
		return r, fmt.Errorf("depth must be 0..3")
	}
	budget := a.Budget
	if budget == 0 {
		budget = 16000
	}
	if budget < 128 || budget > e.C.MaxContextBytes {
		return r, fmt.Errorf("budget_bytes must be 128..%d", e.C.MaxContextBytes)
	}
	type node struct {
		id, reason string
		depth      int
		via        *Relation
	}
	queue := []node{}
	for _, id := range a.Symbols {
		if s.Symbols[id] == nil {
			return r, unknownSymbol(s, id, "")
		}
		queue = append(queue, node{id, "selected symbol", 0, nil})
	}
	selectedChunks := map[string]bool{}
	for _, id := range a.Chunks {
		found := false
		for _, c := range s.Chunks {
			if c.ID == id {
				selectedChunks[id] = true
				queue = append(queue, node{c.Symbol, "selected chunk", 0, nil})
				found = true
				break
			}
		}
		if !found {
			return r, fmt.Errorf("unknown chunk_id: %s", id)
		}
	}
	if a.Query != "" {
		hits, warnings, err := e.Search(ctx, s, a)
		if err != nil {
			return r, err
		}
		r.Limitations = append(r.Limitations, warnings...)
		for _, h := range hits[:min(5, len(hits))] {
			selectedChunks[h.Chunk.ID] = true
			queue = append(queue, node{h.Chunk.Symbol, "search result via " + strings.Join(h.Channels, ","), 0, nil})
		}
	}
	if len(queue) == 0 {
		return r, fmt.Errorf("provide symbol_ids, chunk_ids or query")
	}
	if len(queue) > e.C.MaxResults {
		return r, fmt.Errorf("too many context seeds")
	}
	// Expand only to a bounded frontier; omitted IDs allow explicit continuation.
	seen := map[string]bool{}
	seenChunk := map[string]bool{}
	items := []ContextItem{}
	omitted := []string{}
	used := 2
	ranges := map[string][][2]int{}
	overlap := func(file string, start, end int) bool {
		for _, r := range ranges[file] {
			if start < r[1] && end > r[0] {
				return true
			}
		}
		return false
	}
	expanded := 0
	queued := map[string]bool{}
	for _, n := range queue {
		queued[n.id] = true
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		n := queue[0]
		queue = queue[1:]
		if seen[n.id] {
			continue
		}
		seen[n.id] = true
		sym := s.Symbols[n.id]
		if sym == nil {
			continue
		}
		cs := []*Chunk{}
		for _, c := range s.Chunks {
			if c.Symbol == n.id {
				cs = append(cs, c)
			}
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].start < cs[j].start })
		for _, c := range cs {
			if seenChunk[c.ID] {
				continue
			}
			if n.depth == 0 && len(selectedChunks) > 0 && n.reason != "selected symbol" && !selectedChunks[c.ID] {
				omitted = append(omitted, c.ID)
				continue
			}
			if !match(sym, a) {
				continue
			}
			if overlap(c.File, c.start, c.end) {
				continue
			}
			item := ContextItem{c.Symbol, c.ID, c.File, c.Start, c.End, c.Code, n.reason, n.via}
			serialized, _ := json.Marshal(item)
			cost := len(serialized) + 1
			if used+cost > budget || len(items) >= a.Limit {
				omitted = append(omitted, c.ID)
				continue
			}
			seenChunk[c.ID] = true
			used += cost
			items = append(items, item)
			ranges[c.File] = append(ranges[c.File], [2]int{c.start, c.end})
		}
		if len(cs) == 0 && !overlap(sym.Position.File, sym.start, sym.end) {
			item := ContextItem{Symbol: sym.ID, File: sym.Position.File, Start: sym.Position.Line, End: sym.EndLine, Code: sym.Code, Reason: n.reason, Via: n.via}
			serialized, _ := json.Marshal(item)
			cost := len(serialized) + 1
			if used+cost <= budget && len(items) < a.Limit && match(sym, a) {
				used += cost
				items = append(items, item)
				ranges[sym.Position.File] = append(ranges[sym.Position.File], [2]int{sym.start, sym.end})
			} else {
				omitted = append(omitted, n.id)
			}
		}
		if n.depth >= a.Depth {
			continue
		}
		for _, rel := range s.Relations {
			if rel.Kind == "reference" && rel.Symbol != n.id {
				continue
			}
			if rel.From == n.id && rel.Kind == "call" && rel.Resolution == "candidates" {
				for _, candidate := range rel.Candidates {
					if seen[candidate] || queued[candidate] || s.Symbols[candidate] == nil {
						continue
					}
					if expanded >= e.C.MaxResults*10 {
						omitted = append(omitted, candidate)
						continue
					}
					expanded++
					queued[candidate] = true
					copy := rel
					queue = append(queue, node{candidate, "candidate interface target from " + n.id, n.depth + 1, &copy})
				}
			}
			next := ""
			if rel.From == n.id {
				next = rel.Symbol
			} else if rel.Symbol == n.id {
				next = rel.From
			}
			if next == "" || seen[next] || queued[next] || s.Symbols[next] == nil {
				continue
			}
			if expanded >= e.C.MaxResults*10 {
				omitted = append(omitted, next)
				continue
			}
			expanded++
			queued[next] = true
			copy := rel
			queue = append(queue, node{next, rel.Kind + " (" + rel.Resolution + ") from " + n.id, n.depth + 1, &copy})
		}
	}
	unique := []string{}
	omittedSet := map[string]bool{}
	for _, id := range omitted {
		if !omittedSet[id] {
			unique = append(unique, id)
			omittedSet[id] = true
		}
	}
	omitted = unique
	r.Items = items
	r.Truncated = len(omitted) > 0
	r.Meta = map[string]any{"budget_bytes": budget, "accounted_bytes": used, "budget_unit": "serialized JSON items array in UTF-8 bytes (envelope excluded); not Qwen tokens", "omitted": omitted[:min(len(omitted), e.C.MaxResults)], "omitted_count": len(omitted), "continuation": "build_context with omitted chunk_ids or symbol_ids and index_snapshot"}
	if _, fp, err := readFiles(ctx, e.C); err != nil || fp != s.Fingerprint {
		return r, fmt.Errorf("files changed during context assembly; refresh and retry")
	}
	return r, nil
}
