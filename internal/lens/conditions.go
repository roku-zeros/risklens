package lens

import (
	"go/ast"
)

// These are lexical guards from source, not evaluated path predicates.
func (a *analyzer) conditions(n ast.Node, parents map[ast.Node]ast.Node) []string {
	text := func(x ast.Node) string {
		if x == nil {
			return ""
		}
		p := a.position(x.Pos())
		b := a.s.Files[p.File]
		start, end := a.fs.Position(x.Pos()).Offset, a.fs.Position(x.End()).Offset
		if start < 0 || end < start || end > len(b) {
			return ""
		}
		s := string(b[start:end])
		if len(s) > 240 {
			s = clip(s, 240) + "..."
		}
		return s
	}
	out := []string{}
	for p := parents[n]; p != nil; p = parents[p] {
		condition := ""
		switch v := p.(type) {
		case *ast.IfStmt:
			if n.Pos() >= v.Body.Pos() && n.End() <= v.Body.End() {
				condition = "if " + text(v.Cond)
			} else if v.Else != nil && n.Pos() >= v.Else.Pos() && n.End() <= v.Else.End() {
				condition = "else of if " + text(v.Cond)
			}
		case *ast.ForStmt:
			if n.Pos() >= v.Body.Pos() && n.End() <= v.Body.End() {
				condition = "for " + text(v.Cond)
			}
		case *ast.RangeStmt:
			if n.Pos() >= v.Body.Pos() && n.End() <= v.Body.End() {
				condition = "range " + text(v.X)
			}
		case *ast.CaseClause:
			if n.Pos() > v.Colon {
				condition = "default case"
				if len(v.List) > 0 {
					condition = "case "
					for i, x := range v.List {
						if i > 0 {
							condition += ", "
						}
						condition += text(x)
					}
				}
			}
		}
		if condition != "" {
			out = append(out, condition)
			if len(out) >= 8 {
				break
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
