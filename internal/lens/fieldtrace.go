package lens

import (
	"context"
	"encoding/json"
	"fmt"
)

type FieldTraceItem struct {
	Access    Relation   `json:"access"`
	CallPath  []Relation `json:"call_path"`
	Certainty string     `json:"certainty"`
}

func (e *Engine) traceField(ctx context.Context, s *Snapshot, a Args, r Response) (Response, error) {
	field := s.Symbols[a.Symbol]
	entry := s.Symbols[a.EntrySymbol]
	if field == nil {
		return r, unknownSymbol(s, a.Symbol, "field")
	}
	if field.Kind != "field" {
		return r, fmt.Errorf("symbol_id must identify a field")
	}
	if entry == nil {
		return r, unknownSymbol(s, a.EntrySymbol, "")
	}
	if entry.Kind != "function" && entry.Kind != "method" && entry.Kind != "closure" {
		return r, fmt.Errorf("entry_symbol_id must identify a function/method/closure")
	}
	depth := a.Depth
	if depth == 0 {
		depth = 5
	}
	if depth < 1 || depth > 12 {
		return r, fmt.Errorf("depth must be 1..12")
	}
	budget := a.Budget
	if budget == 0 {
		budget = 16000
	}
	if budget < 128 || budget > e.C.MaxContextBytes {
		return r, fmt.Errorf("invalid budget_bytes")
	}
	type step struct {
		id        string
		path      []Relation
		candidate bool
	}
	queue := []step{{id: entry.ID, path: []Relation{}}}
	seen := map[string]bool{}
	queued := map[string]bool{entry.ID: true}
	items := []FieldTraceItem{}
	unresolved := []Relation{}
	frontier := []string{}
	used := 2
	truncated := false
	byFrom := map[string][]Relation{}
	for _, rel := range s.Relations {
		byFrom[rel.From] = append(byFrom[rel.From], rel)
	}
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		n := queue[0]
		queue = queue[1:]
		if seen[n.id] {
			continue
		}
		if len(seen) >= e.C.MaxResults*10 {
			frontier = append(frontier, n.id)
			truncated = true
			continue
		}
		seen[n.id] = true
		for _, rel := range byFrom[n.id] {
			if rel.Symbol == field.ID && rel.Kind != "reference" && rel.Kind != "call" && relMatch(rel, a, s) {
				certainty := "confirmed_call_path"
				if n.candidate {
					certainty = "candidate_call_path"
				} else if rel.Resolution != "confirmed" {
					certainty = "candidate_access"
				}
				item := FieldTraceItem{rel, n.path, certainty}
				b, _ := json.Marshal(item)
				if len(items) >= a.Limit || used+len(b)+1 > budget {
					truncated = true
					continue
				}
				used += len(b) + 1
				items = append(items, item)
			}
			if rel.Kind != "call" {
				continue
			}
			targets := []string{}
			candidate := n.candidate
			if rel.Resolution == "confirmed" {
				if s.Symbols[rel.Symbol] != nil {
					targets = append(targets, rel.Symbol)
				} else {
					if len(unresolved) < e.C.MaxResults {
						unresolved = append(unresolved, rel)
					}
				}
			} else {
				if len(unresolved) < e.C.MaxResults {
					unresolved = append(unresolved, rel)
				}
				if a.IncludeCandidates {
					candidate = true
					targets = append(targets, rel.Candidates...)
					targets = append(targets, rel.FlowCandidates...)
				}
			}
			for _, target := range targets {
				if seen[target] || queued[target] || s.Symbols[target] == nil {
					continue
				}
				if len(n.path) >= depth {
					frontier = append(frontier, target)
					truncated = true
					continue
				}
				if len(queue)+len(seen) >= e.C.MaxResults*10 {
					frontier = append(frontier, target)
					truncated = true
					continue
				}
				queued[target] = true
				p := append(append([]Relation{}, n.path...), rel)
				queue = append(queue, step{target, p, candidate})
			}
		}
	}
	r.Items = items
	r.Truncated = truncated
	r.Meta = map[string]any{"entry_symbol_id": entry.ID, "field_symbol_id": field.ID, "visited_functions": len(seen), "budget_bytes": budget, "accounted_bytes": used, "unresolved_or_external_calls": unresolved, "frontier": frontier[:min(len(frontier), e.C.MaxResults)], "call_analysis": s.Calls, "limitation": "Accesses to the same field declaration, not proof of the same runtime object. Paths are possible call relationships, not execution order or full value history. At most one discovered path per function is shown. Dynamic/external/alias/whole-struct effects may be absent."}
	if err := validateSnapshot(ctx, e.C, s); err != nil {
		return r, err
	}
	return r, nil
}
