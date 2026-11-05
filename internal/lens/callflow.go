package lens

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

type CallAnalysis struct {
	Instructions   int      `json:"instructions"`
	Mode           string   `json:"mode"`
	State          string   `json:"state"`
	Functions      int      `json:"functions"`
	DynamicSites   int      `json:"dynamic_sites"`
	RecoveredSites int      `json:"recovered_sites"`
	AliasAccesses  int      `json:"alias_accesses"`
	Limitations    []string `json:"limitations"`
	Error          string   `json:"error,omitempty"`
}

func (a *analyzer) refineCalls(ctx context.Context, ps []*packages.Package, c Config) {
	report := CallAnalysis{Mode: c.CallAnalysis, State: "ast_only", Limitations: []string{"Module-scoped static analysis; external function bodies, reflection, unsafe, runtime reachability and execution order are not proven.", "VTA candidates describe possible function/type flow; they do not identify a specific runtime object."}}
	a.s.Calls = report
	hasDynamic := false
	for _, r := range a.s.Relations {
		if (r.Kind == "call" && r.Resolution != "confirmed") || r.Kind == "address" {
			hasDynamic = true
			break
		}
	}
	if !hasDynamic {
		a.s.Calls.State = "no_dynamic_sites"
		return
	}
	if c.CallAnalysis == "ast" {
		return
	}
	if !a.s.Complete {
		a.s.Calls.State = "skipped_type_errors"
		return
	}
	defer func() {
		if v := recover(); v != nil {
			a.s.Calls.State = "failed"
			a.s.Calls.Error = fmt.Sprint(v)
		}
	}()
	count := 0
	for _, s := range a.s.Symbols {
		if s.Kind == "function" || s.Kind == "method" || s.Kind == "closure" {
			count++
		}
	}
	if count > c.MaxSSAFunctions {
		a.s.Calls.State = "skipped_budget"
		a.s.Calls.Error = "module exceeds max_ssa_functions; AST navigation remains available"
		return
	}
	if ctx.Err() != nil {
		a.s.Calls.State = "cancelled"
		return
	}
	roots := map[*types.Package]*packages.Package{}
	all := map[*types.Package]bool{}
	var visit func(*types.Package)
	visit = func(p *types.Package) {
		if p == nil || all[p] {
			return
		}
		all[p] = true
		for _, dep := range p.Imports() {
			visit(dep)
		}
	}
	for _, p := range ps {
		if p.Types == nil || p.IllTyped || p.TypesInfo == nil {
			continue
		}
		local := false
		for _, f := range p.Syntax {
			if _, ok := a.s.Files[a.position(f.Pos()).File]; ok {
				local = true
				break
			}
		}
		if local {
			roots[p.Types] = p
			visit(p.Types)
		}
	}
	prog := ssa.NewProgram(a.fs, ssa.InstantiateGenerics|ssa.BuildSerially)
	built := []*ssa.Package{}
	for typ := range all {
		p := roots[typ]
		if p != nil {
			built = append(built, prog.CreatePackage(typ, p.Syntax, p.TypesInfo, true))
		} else {
			prog.CreatePackage(typ, nil, nil, true)
		}
	}
	for _, p := range built {
		if ctx.Err() != nil {
			a.s.Calls.State = "cancelled"
			return
		}
		p.Build()
	}
	funcs := map[*ssa.Function]bool{}
	for f := range ssautil.AllFunctions(prog) {
		if len(f.Blocks) > 0 {
			funcs[f] = true
		}
	}
	a.s.Calls.Functions = len(funcs)
	for f := range funcs {
		for _, b := range f.Blocks {
			a.s.Calls.Instructions += len(b.Instrs)
		}
	}
	if a.s.Calls.Instructions > c.MaxSSAInstructions {
		a.s.Calls.State = "skipped_budget"
		a.s.Calls.Error = "SSA instruction budget exceeded; AST navigation retained"
		return
	}
	if len(funcs) > c.MaxSSAFunctions {
		a.s.Calls.State = "skipped_budget"
		return
	}
	closures := map[Position]string{}
	for id, s := range a.s.Symbols {
		if s.Kind == "closure" {
			closures[s.Position] = id
		}
	}
	functionID := func(f *ssa.Function) string {
		if f == nil {
			return ""
		}
		if obj := f.Object(); obj != nil {
			if id := a.objectID(obj); a.s.Symbols[id] != nil {
				return id
			}
		}
		if lit, ok := f.Syntax().(*ast.FuncLit); ok {
			return closures[a.position(lit.Pos())]
		}
		return ""
	}
	callPos := func(p token.Pos) Position {
		pos := a.position(p)
		if astPos, ok := a.callPositions[pos]; ok {
			return astPos
		}
		return pos
	}
	type siteKey struct {
		from string
		pos  Position
	}
	sites := map[siteKey][]int{}
	for i, r := range a.s.Relations {
		if r.Kind == "call" {
			sites[siteKey{r.From, r.Position}] = append(sites[siteKey{r.From, r.Position}], i)
		}
	}
	// Accumulate SSA facts before mutating the snapshot, so recovery from a builder failure is atomic.
	statics := map[siteKey]string{}
	targets := map[siteKey]map[string]bool{}
	for f := range funcs {
		from := functionID(f)
		if from == "" {
			continue
		}
		for _, b := range f.Blocks {
			for _, ins := range b.Instrs {
				call, ok := ins.(ssa.CallInstruction)
				if !ok {
					continue
				}
				key := siteKey{from, callPos(call.Common().Pos())}
				if id := functionID(call.Common().StaticCallee()); id != "" {
					statics[key] = id
				}
			}
		}
	}
	if ctx.Err() != nil {
		a.s.Calls.State = "cancelled"
		return
	}
	graph := vta.CallGraph(funcs, nil)
	for f, node := range graph.Nodes {
		from := functionID(f)
		if from == "" {
			continue
		}
		for _, edge := range node.Out {
			if edge.Site == nil {
				continue
			}
			id := functionID(edge.Callee.Func)
			if id == "" {
				continue
			}
			key := siteKey{from, callPos(edge.Site.Common().Pos())}
			if targets[key] == nil {
				targets[key] = map[string]bool{}
			}
			targets[key][id] = true
		}
	}
	if ctx.Err() != nil {
		a.s.Calls.State = "cancelled"
		return
	}
	aliases := a.aliasAccesses(funcs, graph, functionID)
	a.s.Calls.AliasAccesses = len(aliases)
	a.s.Relations = append(a.s.Relations, aliases...)
	for key, indexes := range sites {
		for _, i := range indexes {
			r := &a.s.Relations[i]
			if r.Resolution == "confirmed" {
				continue
			}
			a.s.Calls.DynamicSites++
			if id := statics[key]; id != "" {
				r.Symbol = id
				r.Resolution = "confirmed"
				r.Analysis = "ssa_static"
				r.Candidates = nil
				r.UnknownTargets = false
				a.s.Calls.RecoveredSites++
				continue
			}
			ids := []string{}
			for id := range targets[key] {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				r.FlowCandidates = ids
				r.Analysis = "vta_module"
				r.UnknownTargets = true
				if r.Resolution == "unresolved" {
					r.Resolution = "candidates"
					r.Candidates = append([]string{}, ids...)
				}
				a.s.Calls.RecoveredSites++
			}
		}
	}
	a.s.Calls.State = "ready"
}
