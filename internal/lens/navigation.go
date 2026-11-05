package lens

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// FieldInfo describes a legal selector on a type, with the declaration's canonical ID.
// A promoted selector never creates a second field symbol owned by the outer type.
type FieldInfo struct {
	Symbol     string    `json:"symbol_id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	AccessPath []string  `json:"access_path"`
	Promoted   bool      `json:"promoted"`
	Embedded   bool      `json:"embedded"`
	Available  bool      `json:"definition_indexed"`
	Position   *Position `json:"position,omitempty"`
}

func structOf(t types.Type) *types.Struct {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	s, _ := t.Underlying().(*types.Struct)
	return s
}
func (a *analyzer) collectTypeFields() {
	a.s.TypeFields = map[string][]FieldInfo{}
	for obj, id := range a.objects {
		tn, ok := obj.(*types.TypeName)
		if !ok || structOf(tn.Type()) == nil {
			continue
		}
		names := map[string]bool{}
		visited := map[types.Type]bool{}
		queue := []types.Type{tn.Type()}
		for len(queue) > 0 {
			t := queue[0]
			queue = queue[1:]
			t = types.Unalias(t)
			if visited[t] {
				continue
			}
			visited[t] = true
			st := structOf(t)
			if st == nil {
				continue
			}
			for i := 0; i < st.NumFields(); i++ {
				f := st.Field(i)
				names[f.Name()] = true
				if f.Embedded() {
					queue = append(queue, f.Type())
				}
			}
		}
		fields := []FieldInfo{}
		for name := range names {
			o, index, _ := types.LookupFieldOrMethod(tn.Type(), false, tn.Pkg(), name)
			f, ok := o.(*types.Var)
			if !ok || !f.IsField() || len(index) == 0 {
				continue
			}
			path := []string{}
			t := tn.Type()
			valid := true
			for _, i := range index {
				st := structOf(t)
				if st == nil || i >= st.NumFields() {
					valid = false
					break
				}
				v := st.Field(i)
				path = append(path, v.Name())
				t = v.Type()
			}
			if !valid {
				continue
			}
			fid := a.objectID(f)
			var position *Position
			if definition := a.s.Symbols[fid]; definition != nil {
				copy := definition.Position
				position = &copy
			}
			fields = append(fields, FieldInfo{fid, f.Name(), types.TypeString(f.Type(), func(p *types.Package) string { return p.Path() }), path, len(index) > 1, f.Embedded(), a.s.Symbols[fid] != nil, position})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		a.s.TypeFields[id] = fields
	}
}

type LookupError struct {
	Code       string   `json:"code"`
	Requested  string   `json:"requested_symbol_id"`
	Hint       string   `json:"hint"`
	Candidates []Symbol `json:"candidates"`
}

func (e *LookupError) Error() string {
	return fmt.Sprintf("unknown symbol_id %q. %s", e.Requested, e.Hint)
}
func unknownSymbol(s *Snapshot, id, kind string) *LookupError {
	name := id
	if i := strings.LastIndex(name, "#"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	query := Args{Query: name, Kind: kind}
	if query.Kind == "" && strings.Contains(id, "#field") {
		query.Kind = "field"
	}
	candidates := suggestSymbols(s, query, 8)
	return &LookupError{"unknown_symbol_id", id, "Use IDs returned by search_symbols; do not construct IDs. For promoted fields, get_symbol on the outer type lists canonical fields; search_symbols owner_id=<returned type ID> enumerates them. A stale name is not proof that its declaration exists.", candidates}
}
func summary(sym *Symbol) Symbol {
	x := *sym
	x.Code = ""
	x.Comment = ""
	x.Signature = clip(x.Signature, 512)
	return x
}
func suggestSymbols(s *Snapshot, a Args, limit int) []Symbol {
	type candidate struct {
		sym   *Symbol
		score int
	}
	cs := []candidate{}
	q := strings.ToLower(a.Query)
	qt := terms(a.Query)
	for _, sym := range s.Symbols {
		if a.OwnerID != "" {
			owned := false
			for _, f := range s.TypeFields[a.OwnerID] {
				if f.Symbol == sym.ID && f.Available {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
		}
		if !match(sym, a) {
			continue
		}
		score := 0
		n := strings.ToLower(sym.Name)
		if n == q {
			score = 100
		} else {
			for _, term := range qt {
				if len([]rune(term)) < 3 {
					continue
				}
				if strings.Contains(n, term) {
					score += len(term)
				}
			}
		}
		if score > 0 {
			cs = append(cs, candidate{sym, score})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].score == cs[j].score {
			return cs[i].sym.ID < cs[j].sym.ID
		}
		return cs[i].score > cs[j].score
	})
	out := []Symbol{}
	for _, c := range cs[:min(limit, len(cs))] {
		out = append(out, summary(c.sym))
	}
	return out
}
