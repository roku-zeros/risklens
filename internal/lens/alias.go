package lens

import (
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"
)

// aliasAccesses follows bounded SSA pointer flow to field declarations. It does
// not identify runtime instances or perform strong updates/temporal reasoning.
func (a *analyzer) aliasAccesses(funcs map[*ssa.Function]bool, graph *callgraph.Graph, functionID func(*ssa.Function) string) []Relation {
	bindings := map[ssa.Value][]ssa.Value{}
	stores := map[ssa.Value][]ssa.Value{}

	for f := range funcs {
		for _, block := range f.Blocks {
			for _, instruction := range block.Instrs {
				switch v := instruction.(type) {
				case *ssa.Store:
					stores[v.Addr] = append(stores[v.Addr], v.Val)
				case *ssa.MakeClosure:
					if fn, ok := v.Fn.(*ssa.Function); ok {
						for i, binding := range v.Bindings {
							if i < len(fn.FreeVars) {
								bindings[fn.FreeVars[i]] = append(bindings[fn.FreeVars[i]], binding)
							}
						}
					}
				}
			}
		}
	}

	for _, node := range graph.Nodes {
		for _, edge := range node.Out {
			if edge.Site == nil {
				continue
			}
			args := append([]ssa.Value{}, edge.Site.Common().Args...)
			if edge.Site.Common().IsInvoke() {
				args = append([]ssa.Value{edge.Site.Common().Value}, args...)
			}
			for i, p := range edge.Callee.Func.Params {
				if i < len(args) {
					bindings[p] = append(bindings[p], args[i])
				}
			}
		}
	}
	var resolve func(ssa.Value, int, map[ssa.Value]bool, map[string]bool)
	resolve = func(v ssa.Value, depth int, seen map[ssa.Value]bool, out map[string]bool) {
		if v == nil || depth > 12 || seen[v] || len(seen) >= 1000 {
			return
		}
		seen[v] = true
		switch x := v.(type) {
		case *ssa.FieldAddr:
			t := x.X.Type().Underlying()
			if p, ok := t.(*types.Pointer); ok {
				if st, ok := p.Elem().Underlying().(*types.Struct); ok && x.Field < st.NumFields() {
					id := a.objectID(st.Field(x.Field))
					if a.s.Symbols[id] != nil {
						out[id] = true
					}
				}
			}
		case *ssa.Phi:
			for _, value := range x.Edges {
				resolve(value, depth+1, seen, out)
			}
		case *ssa.ChangeType:
			resolve(x.X, depth+1, seen, out)
		case *ssa.Convert:
			resolve(x.X, depth+1, seen, out)
		case *ssa.UnOp:
			if x.Op == token.MUL {
				for _, value := range stores[x.X] {
					resolve(value, depth+1, seen, out)
				}
				for _, value := range bindings[x.X] {
					resolve(value, depth+1, seen, out)
				}
			}
		case *ssa.Call:
			if fn := x.Common().StaticCallee(); fn != nil {
				for _, block := range fn.Blocks {
					for _, ins := range block.Instrs {
						if ret, ok := ins.(*ssa.Return); ok && len(ret.Results) == 1 {
							resolve(ret.Results[0], depth+1, seen, out)
						}
					}
				}
			}
		default:
			for _, value := range bindings[v] {
				resolve(value, depth+1, seen, out)
			}
			for _, value := range stores[v] {
				resolve(value, depth+1, seen, out)
			}
		}
	}
	out := []Relation{}
	for f := range funcs {
		from := functionID(f)
		if from == "" {
			continue
		}
		for _, b := range f.Blocks {
			for _, ins := range b.Instrs {
				var ptr ssa.Value
				kind := ""
				switch x := ins.(type) {
				case *ssa.Store:
					ptr = x.Addr
					kind = "write"
				case *ssa.UnOp:
					if x.Op == token.MUL {
						ptr = x.X
						kind = "read"
					}
				}
				if ptr == nil {
					continue
				}
				pos := a.position(ins.Pos())
				if _, ok := a.s.Files[pos.File]; !ok || pos.Line < 1 {
					continue
				}
				ids := map[string]bool{}
				resolve(ptr, 0, map[ssa.Value]bool{}, ids)
				candidates := []string{}
				for id := range ids {
					candidates = append(candidates, id)
				}
				sort.Strings(candidates)
				for _, id := range candidates {
					if a.fieldPositions[pos][id] {
						continue
					}
					out = append(out, Relation{Symbol: id, From: from, Kind: kind, Resolution: "candidates", Position: pos, Code: a.line(pos), Analysis: "ssa_alias", Conditions: a.siteContexts[pos], Candidates: candidates, UnknownTargets: true})
				}
			}
		}
	}
	return out
}
