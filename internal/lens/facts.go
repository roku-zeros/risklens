package lens

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

type syntaxFact struct {
	offset int
	text   string
}

func syntaxFacts(fs *token.FileSet, f *ast.File) []syntaxFact {
	out := []syntaxFact{}
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		fact := ""
		switch v := n.(type) {
		case *ast.AssignStmt:
			switch v.Tok {
			case token.SUB_ASSIGN:
				fact = "subtraction assignment decreases a value (-=)"
			case token.ADD_ASSIGN:
				fact = "addition assignment increases a value (+=)"
			case token.MUL_ASSIGN:
				fact = "multiplication assignment (*=)"
			case token.QUO_ASSIGN:
				fact = "division assignment (/=)"
			}
		case *ast.IncDecStmt:
			if v.Tok == token.INC {
				fact = "increment operator (++)"
			} else {
				fact = "decrement operator (--)"
			}
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				fact = "address taking, pointer alias (&)"
			}
		}
		if fact != "" {
			out = append(out, syntaxFact{fs.Position(n.Pos()).Offset, fact})
		}
		return true
	})
	return out
}
func chunkFacts(s *Snapshot, sym *Symbol, start, end int, syntax []syntaxFact, relations []Relation) string {
	facts := map[string]bool{}
	if strings.Contains(sym.Signature, "*") {
		facts["signature includes pointer types"] = true
	}
	for _, f := range syntax {
		if f.offset >= start && f.offset < end {
			facts["syntax contains "+f.text] = true
		}
	}
	for _, r := range relations {
		if r.From != sym.ID || r.Position.File != sym.Position.File {
			continue
		}
		if r.Kind == "call" && r.Resolution == "confirmed" {
			if callee := s.Symbols[r.Symbol]; callee != nil {
				facts["lexical call site: "+callee.Package+"."+callee.Name+" (not execution order)"] = true
			}
		} else if field := s.Symbols[r.Symbol]; field != nil && field.Kind == "field" && r.Kind != "reference" {
			facts["field "+r.Kind+": "+field.Name+" ("+r.Resolution+")"] = true
		}
	}
	ordered := []string{}
	for f := range facts {
		ordered = append(ordered, f)
	}
	sort.Strings(ordered)
	return clip(strings.Join(ordered, "; "), 800)
}
