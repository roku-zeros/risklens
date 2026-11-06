package lens

import (
	"sort"
	"strings"
)

type ReferenceMatch struct {
	Symbol     string    `json:"symbol_id"`
	Owner      string    `json:"owner_id"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"field_name"`
	JSONName   string    `json:"json_name,omitempty"`
	Type       string    `json:"type"`
	AccessPath []string  `json:"access_path"`
	Position   *Position `json:"position,omitempty"`
}

// Resolve declarations and typed Go selectors, not JSON wire paths or runtime objects.
func resolveFields(s *Snapshot, a Args) []ReferenceMatch {
	query := strings.TrimSpace(a.Query)
	if a.Namespace == "json" {
		query = a.Query
	}
	ns := a.Namespace
	if ns == "" {
		ns = "auto"
	}
	matches := []ReferenceMatch{}
	seen := map[string]bool{}
	add := func(owner string, f FieldInfo, namespace string, path []string) {
		if !f.Available || !match(s.Symbols[f.Symbol], a) {
			return
		}
		key := f.Symbol + ":" + owner
		if a.OwnerID == "" && !strings.Contains(query, ".") {
			key = f.Symbol
		}
		if seen[key] {
			return
		}
		seen[key] = true
		matches = append(matches, ReferenceMatch{f.Symbol, owner, namespace, f.Name, f.JSONName, f.Type, path, f.Position})
	}
	owners := []string{}
	if a.OwnerID != "" {
		owners = append(owners, a.OwnerID)
	} else {
		for id := range s.TypeFields {
			owners = append(owners, id)
		}
	}
	sort.Strings(owners)
	if ns != "json" {
		for _, owner := range owners {
			parts := strings.Split(query, ".")
			root := s.Symbols[owner]
			if len(parts) > 1 && a.OwnerID == "" {
				if root == nil || root.Kind != "type" || root.Name != parts[0] {
					continue
				}
				parts = parts[1:]
			} else if root != nil && len(parts) > 1 && parts[0] == root.Name {
				parts = parts[1:]
			}
			if len(parts) == 0 {
				continue
			}
			scope := owner
			path := []string{}
			for i, part := range parts {
				var found *FieldInfo
				for _, f := range s.TypeFields[scope] {
					if f.Name == part {
						copy := f
						found = &copy
						break
					}
				}
				if found == nil {
					break
				}
				path = append(path, found.AccessPath...)
				if i == len(parts)-1 {
					if a.OwnerID != "" || len(parts) > 1 || !found.Promoted {
						add(owner, *found, "go", path)
					}
				} else {
					if found.TargetScope == "" {
						break
					}
					scope = found.TargetScope
				}
			}
		}
	}
	if ns != "go" {
		for _, owner := range owners {
			for _, f := range s.TypeFields[owner] {
				if !f.Promoted && f.JSONName == query {
					add(owner, f, "json", f.AccessPath)
				}
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Symbol != matches[j].Symbol {
			return matches[i].Symbol < matches[j].Symbol
		}
		return matches[i].Owner < matches[j].Owner
	})
	return matches
}

type CandidateSignatureGroup struct {
	Signature string   `json:"signature"`
	Count     int      `json:"count"`
	Symbols   []string `json:"symbol_ids"`
}

func signatureGroups(found []*Symbol) []CandidateSignatureGroup {
	groups := map[string]*CandidateSignatureGroup{}
	for _, sym := range found {
		key := sym.Signature
		g := groups[key]
		if g == nil {
			g = &CandidateSignatureGroup{Signature: key, Symbols: []string{}}
			groups[key] = g
		}
		g.Count++
		if len(g.Symbols) < 8 {
			g.Symbols = append(g.Symbols, sym.ID)
		}
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []CandidateSignatureGroup{}
	for _, k := range keys {
		if len(out) >= 8 {
			break
		}
		out = append(out, *groups[k])
	}
	return out
}
func auditFieldReference(s *Snapshot, ref string, line int) (EvidenceCheck, bool) {
	if strings.HasSuffix(ref, "()") {
		return EvidenceCheck{}, false
	}
	matches := resolveFields(s, Args{Query: ref})
	if len(matches) == 0 {
		return EvidenceCheck{}, false
	}
	unique := map[string]*Symbol{}
	for _, m := range matches {
		unique[m.Symbol] = s.Symbols[m.Symbol]
	}
	found := []*Symbol{}
	for _, v := range unique {
		found = append(found, v)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	c := EvidenceCheck{Reference: ref, DocumentLine: line, Bindings: matches[:min(8, len(matches))], BindingCount: len(matches)}
	if len(found) == 1 {
		c = checkEvidence(s, EvidenceClaim{Symbol: found[0].ID})
		c.Reference = ref
		c.DocumentLine = line
		c.Bindings = matches[:min(8, len(matches))]
		c.BindingCount = len(matches)
		if matches[0].Namespace == "json" {
			c.Status = "contract_name_verified"
			c.Note += " Exact JSON declaration alias, not a serialization-path or business-claim proof."
		} else if strings.Contains(ref, ".") {
			c.Status = "field_path_verified"
			c.Note += " Typed Go field path; not proof of the same runtime object."
		}
	} else {
		c.Status = "ambiguous_symbol"
		c.Note = "Multiple declarations match; identical signatures do not establish equivalent business meaning. Resolve owner_id with resolve_reference."
		c.CandidateGroups = signatureGroups(found)
		for _, v := range found[:min(8, len(found))] {
			c.Candidates = append(c.Candidates, summary(v))
		}
	}
	return c, true
}
