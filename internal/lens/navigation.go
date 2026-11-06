package lens

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"sort"
	"strings"
)

// FieldInfo describes a legal selector on a type, with the declaration's canonical ID.
// A promoted selector never creates a second field symbol owned by the outer type.
type FieldInfo struct {
	JSONName    string    `json:"json_name,omitempty"`
	JSONTag     string    `json:"json_tag,omitempty"`
	TargetScope string    `json:"target_scope_id,omitempty"`
	Symbol      string    `json:"symbol_id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	AccessPath  []string  `json:"access_path"`
	Promoted    bool      `json:"promoted"`
	Embedded    bool      `json:"embedded"`
	Available   bool      `json:"definition_indexed"`
	Position    *Position `json:"position,omitempty"`
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
	type scope struct {
		id  string
		t   types.Type
		pkg *types.Package
	}
	queue := []scope{}
	for obj, id := range a.objects {
		if tn, ok := obj.(*types.TypeName); ok && structOf(tn.Type()) != nil {
			queue = append(queue, scope{id, tn.Type(), tn.Pkg()})
		}
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		q := queue[0]
		queue = queue[1:]
		if seen[q.id] {
			continue
		}
		seen[q.id] = true
		if len(seen) > 50000 {
			a.s.FieldScopeTruncated = true
			break
		}
		names := map[string]bool{}
		visited := map[types.Type]bool{}
		embedded := []types.Type{q.t}
		for len(embedded) > 0 {
			t := types.Unalias(embedded[0])
			embedded = embedded[1:]
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
					embedded = append(embedded, f.Type())
				}
			}
		}
		fields := []FieldInfo{}
		for name := range names {
			obj, index, _ := types.LookupFieldOrMethod(q.t, false, q.pkg, name)
			f, ok := obj.(*types.Var)
			if !ok || !f.IsField() || len(index) == 0 {
				continue
			}
			path := []string{}
			t := q.t
			valid := true
			tag := ""
			for _, i := range index {
				st := structOf(t)
				if st == nil || i >= st.NumFields() {
					valid = false
					break
				}
				v := st.Field(i)
				path = append(path, v.Name())
				tag = st.Tag(i)
				t = v.Type()
			}
			if !valid {
				continue
			}
			fid := a.objectID(f)
			var pos *Position
			if def := a.s.Symbols[fid]; def != nil {
				copy := def.Position
				pos = &copy
			}
			wire, _ := reflect.StructTag(tag).Lookup("json")
			jsonName := strings.Split(wire, ",")[0]
			if jsonName == "" {
				jsonName = f.Name()
			}
			if jsonName == "-" || !ast.IsExported(f.Name()) {
				jsonName = ""
			}
			target := ""
			if structOf(t) != nil {
				unwrapped := types.Unalias(t)
				if ptr, ok := unwrapped.(*types.Pointer); ok {
					unwrapped = types.Unalias(ptr.Elem())
				}
				if named, ok := unwrapped.(*types.Named); ok {
					target = a.objectID(named.Obj())
					if named.TypeArgs() != nil && named.TypeArgs().Len() > 0 {
						target += "#inst:" + digest(types.TypeString(t, nil))[:12]
					}
				} else {
					target = fid + "#struct"
				}
				queue = append(queue, scope{target, t, f.Pkg()})
			}
			fields = append(fields, FieldInfo{Symbol: fid, Name: f.Name(), Type: types.TypeString(f.Type(), func(p *types.Package) string { return p.Path() }), AccessPath: path, Promoted: len(index) > 1, Embedded: f.Embedded(), Available: a.s.Symbols[fid] != nil, Position: pos, JSONName: jsonName, JSONTag: wire, TargetScope: target})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		a.s.TypeFields[q.id] = fields
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
