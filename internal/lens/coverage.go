package lens

// callCoverage describes indexed direct sites, never runtime reachability.
func callCoverage(s *Snapshot, from string, a Args) map[string]any {
	counts := map[string]int{"confirmed_internal": 0, "confirmed_external": 0, "candidate": 0, "unresolved": 0}
	unknown := []Relation{}
	total := 0
	for _, r := range s.Relations {
		if r.Kind != "call" || (from != "" && r.From != from) || !relMatch(r, a, s) {
			continue
		}
		total++
		key := "unresolved"
		if r.Resolution == "confirmed" {
			key = "confirmed_internal"
			if s.Symbols[r.Symbol] == nil {
				key = "confirmed_external"
			}
		} else if len(r.Candidates) > 0 || len(r.FlowCandidates) > 0 {
			key = "candidate"
		}
		counts[key]++
		if key != "confirmed_internal" && len(unknown) < 20 {
			unknown = append(unknown, r)
		}
	}
	return map[string]any{"indexed_direct_sites": total, "counts": counts, "unknown_or_external_examples": unknown, "examples_truncated": counts["candidate"]+counts["unresolved"]+counts["confirmed_external"] > len(unknown), "scope": "Indexed direct calls, excluding type conversions; no proof of runtime execution, absence, transitive completeness or same object.", "analysis_state": s.Calls.State}
}
