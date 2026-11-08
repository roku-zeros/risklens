package lens

import (
	"go/ast"
	"go/types"
)

// MembershipFact describes the syntax of one return path. An element's
// presence in the expression says nothing about whether the path executes.
type MembershipFact struct {
	Return     Position `json:"return_position"`
	Position   Position `json:"position"`
	Conditions []string `json:"lexical_guards,omitempty"`
	Status     string   `json:"membership"`
	Symbol     string   `json:"element_symbol_id,omitempty"`
	Expression string   `json:"expression,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

type membershipFacts struct {
	Rows      []MembershipFact
	Truncated bool
}

func (a *analyzer) collectMembership(parents map[ast.Node]ast.Node) {
	info := a.pkg.TypesInfo
	if info == nil {
		return
	}
	for _, decl := range a.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		id := a.funcs[fn]
		obj, ok := info.Defs[fn.Name].(*types.Func)
		if !ok || a.s.Symbols[id] == nil {
			continue
		}
		results := obj.Type().(*types.Signature).Results()
		if results.Len() != 1 {
			continue
		}
		if _, ok := results.At(0).Type().Underlying().(*types.Slice); !ok {
			continue
		}
		facts := membershipFacts{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || facts.Truncated {
				return !facts.Truncated
			}
			pos := a.position(ret.Pos())
			guards := a.conditions(ret, parents)
			add := func(n ast.Node, status, symbol, reason string) {
				if len(facts.Rows) >= 128 {
					facts.Truncated = true
					return
				}
				start, end := a.fs.Position(n.Pos()).Offset, a.fs.Position(n.End()).Offset
				var expression string
				b := a.s.Files[pos.File]
				if start >= 0 && end >= start && end <= len(b) {
					expression = clip(string(b[start:end]), 320)
				}
				facts.Rows = append(facts.Rows, MembershipFact{pos, a.position(n.Pos()), guards, status, symbol, expression, reason})
			}
			if len(ret.Results) == 1 {
				a.membershipExpression(ret.Results[0], false, 0, add)
			} else {
				add(ret, "unknown", "", "implicit or unsupported return")
			}
			return false
		})
		a.s.Membership[id] = facts
	}
}

func (a *analyzer) membershipExpression(expr ast.Expr, appending bool, depth int, add func(ast.Node, string, string, string)) {
	info := a.pkg.TypesInfo
	expr = ast.Unparen(expr)
	if depth >= 16 {
		add(expr, "unknown", "", "append nesting limit")
		return
	}
	switch x := expr.(type) {
	case *ast.Ident:
		if info.Uses[x] == types.Universe.Lookup("nil") {
			if !appending {
				add(x, "explicit_empty", "", "")
			}
			return
		}
	case *ast.CallExpr:
		name, ok := ast.Unparen(x.Fun).(*ast.Ident)
		if ok && info.Uses[name] == types.Universe.Lookup("append") && len(x.Args) >= 1 && !x.Ellipsis.IsValid() {
			a.membershipExpression(x.Args[0], appending || len(x.Args) > 1, depth+1, add)
			for _, item := range x.Args[1:] {
				a.membershipElement(item, add)
			}
			return
		}
	case *ast.CompositeLit:
		if t := info.TypeOf(x); t != nil {
			if _, ok := t.Underlying().(*types.Slice); ok {
				for _, item := range x.Elts {
					if _, keyed := item.(*ast.KeyValueExpr); keyed {
						add(x, "unknown", "", "keyed slice literal may contain implicit elements")
						return
					}
				}
				if len(x.Elts) == 0 && !appending {
					add(x, "explicit_empty", "", "")
				}
				for _, item := range x.Elts {
					a.membershipElement(item, add)
				}
				return
			}
		}
	}
	add(expr, "unknown", "", "return value requires data flow or composition analysis")
}

func (a *analyzer) membershipElement(item ast.Expr, add func(ast.Node, string, string, string)) {
	info := a.pkg.TypesInfo
	var obj types.Object
	switch element := ast.Unparen(item).(type) {
	case *ast.Ident:
		obj = info.Uses[element]
	case *ast.SelectorExpr:
		if info.Selections[element] == nil {
			obj = info.Uses[element.Sel]
		}
	}
	if fn, ok := obj.(*types.Func); ok {
		if id := a.objectID(fn); a.s.Symbols[id] != nil {
			add(item, "explicit_element", id, "")
			return
		}
	}
	add(item, "unknown", "", "element value requires analysis beyond a named local function")
}
