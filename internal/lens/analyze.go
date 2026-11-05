package lens

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

type analyzer struct {
	s          *Snapshot
	fs         *token.FileSet
	objects    map[types.Object]string
	owner      map[*types.Var]string
	ownerPos   map[Position]string
	lineStarts map[string][]int
	root       string
	funcs      map[*ast.FuncDecl]string
	literals   map[*ast.FuncLit]string
	pkg        *packages.Package
	file       *ast.File
}

func analyze(ctx context.Context, c Config, files map[string][]byte, fp string) (*Snapshot, error) {
	fs := token.NewFileSet()
	overlay := map[string][]byte{}
	for n, b := range files {
		if strings.HasSuffix(n, ".go") {
			overlay[filepath.Join(c.Root, filepath.FromSlash(n))] = b
		}
	}
	env := append([]string{}, os.Environ()...)
	env = append(env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOPACKAGESDRIVER=off", "GONOPROXY=none", "GONOSUMDB=*", "GOPRIVATE=", "GOVCS=*:off")
	ps, err := packages.Load(&packages.Config{Context: ctx, Dir: c.Root, Env: env, Fset: fs, Tests: true, Overlay: overlay, BuildFlags: c.BuildFlags, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule}, "./...")
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s := &Snapshot{ID: fp[:24], Fingerprint: fp, Symbols: map[string]*Symbol{}, Files: files, Complete: true, Environment: baseEnvironment(), Created: time.Now().UTC().Format(time.RFC3339)}
	for _, k := range []string{"GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT"} {
		if v := os.Getenv(k); v != "" {
			s.Environment[k] = v
		}
	}
	s.Environment["build_flags"] = strings.Join(c.BuildFlags, " ")
	cmd := exec.CommandContext(ctx, "go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "GOVERSION", "GOTOOLCHAIN", "GOEXPERIMENT")
	cmd.Dir = c.Root
	cmd.Env = env
	if b, err := cmd.Output(); err == nil {
		var settings map[string]string
		if json.Unmarshal(b, &settings) == nil {
			for k, v := range settings {
				s.Environment[k] = v
			}
		}
	} else {
		s.Complete = false
		s.Diagnostics = append(s.Diagnostics, "cannot inspect active go command: "+err.Error())
	}
	s.Environment["GONOPROXY"] = "none"
	s.Environment["GOVCS"] = "*:off"
	// Prefer augmented internal-test variants. Each physical AST is indexed only once.
	sort.Slice(ps, func(i, j int) bool { return len(ps[i].CompiledGoFiles) > len(ps[j].CompiledGoFiles) })
	type unit struct {
		p *packages.Package
		f *ast.File
	}
	var units []unit
	seen := map[string]bool{}
	diags := map[string]bool{}
	a := &analyzer{s: s, fs: fs, root: c.Root, ownerPos: map[Position]string{}, lineStarts: map[string][]int{}, objects: map[types.Object]string{}, owner: map[*types.Var]string{}, funcs: map[*ast.FuncDecl]string{}, literals: map[*ast.FuncLit]string{}}
	for _, p := range ps {
		for _, e := range p.Errors {
			diags[e.Error()] = true
			s.Complete = false
		}
		if p.Module != nil && p.Module.Main {
			s.Module = p.Module.Path
		}
		for _, f := range p.Syntax {
			rel, e := filepath.Rel(c.Root, fs.Position(f.Pos()).Filename)
			if e != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if _, ok := files[rel]; !ok || seen[rel] {
				continue
			}
			seen[rel] = true
			units = append(units, unit{p, f})
		}
	}
	if len(diags) > 0 {
		s.Diagnostics = append(s.Diagnostics, "Partial analysis. If dependencies are missing, prepare with go mod download in an online setup step; the analyzer uses GOPROXY=off and -mod=readonly.")
	}
	for d := range diags {
		s.Diagnostics = append(s.Diagnostics, d)
	}
	sort.Strings(s.Diagnostics)
	if len(units) == 0 {
		s.Complete = false
		s.Diagnostics = append(s.Diagnostics, "No buildable Go sources; prepare dependencies with go mod download online, then retry offline.")
	}
	for name, b := range files {
		starts := []int{0}
		for i, v := range b {
			if v == '\n' {
				starts = append(starts, i+1)
			}
		}
		a.lineStarts[name] = starts
	}
	// Synthetic file containers cover package/import text and otherwise unattached comments.
	for _, u := range units {
		a.pkg = u.p
		a.file = u.f
		pos := a.position(u.f.Pos())
		b := files[pos.File]
		id := a.pkgPath() + "#file:" + pos.File
		s.Symbols[id] = &Symbol{ID: id, Name: filepath.Base(pos.File), Kind: "file", Package: a.pkgPath(), Signature: "file " + pos.File, Position: Position{pos.File, 1, 1}, EndLine: 1 + strings.Count(string(b), "\n"), Test: strings.HasSuffix(pos.File, "_test.go"), Code: string(b), start: 0, end: len(b)}
	}
	// Establish field owners before resolving definitions, including anonymous fields.
	for _, u := range units {
		a.pkg = u.p
		a.file = u.f
		ast.Inspect(u.f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := u.p.TypesInfo.TypeOf(ts.Type).(*types.Struct); ok {
				for i := 0; i < st.NumFields(); i++ {
					own := a.pkgPath() + "." + ts.Name.Name
					if obj := u.p.TypesInfo.Defs[ts.Name]; obj != nil && obj.Parent() != u.p.Types.Scope() {
						own = a.objectID(obj)
					}
					a.owner[st.Field(i)] = own
					a.ownerPos[a.position(st.Field(i).Pos())] = own
				}
			}
			return true
		})
	}
	// All declarations and lexical definitions (type parameters, receivers, locals included).
	for _, u := range units {
		a.pkg = u.p
		a.file = u.f
		parents := parentMap(u.f)
		for ident, obj := range u.p.TypesInfo.Defs {
			if obj == nil || !inside(ident, u.f) {
				continue
			}
			id := a.objectID(obj)
			if id == "" {
				continue
			}
			a.objects[obj] = id
			n := ast.Node(ident)
			comment := ""
			if par := parents[ident]; par != nil {
				switch v := par.(type) {
				case *ast.FuncDecl:
					n = v
					if v.Doc != nil {
						comment = v.Doc.Text()
					}
				case *ast.TypeSpec:
					n = v
					if v.Doc != nil {
						comment = v.Doc.Text()
					}
					if gd, ok := parents[v].(*ast.GenDecl); ok {
						if gd.Doc != nil {
							comment = gd.Doc.Text() + comment
						}
						if len(gd.Specs) == 1 {
							n = gd
						}
					}
				case *ast.ValueSpec:
					n = v
					if v.Doc != nil {
						comment = v.Doc.Text()
					}
					if gd, ok := parents[v].(*ast.GenDecl); ok {
						if gd.Doc != nil {
							comment = gd.Doc.Text() + comment
						}
						if len(gd.Specs) == 1 {
							n = gd
						}
					}
				case *ast.Field:
					n = v
					if v.Doc != nil {
						comment = v.Doc.Text()
					}
					if v.Comment != nil {
						comment += v.Comment.Text()
					}
				}
			}
			pos := a.position(ident.Pos())
			start := fs.Position(n.Pos()).Offset
			end := fs.Position(n.End()).Offset
			b := files[pos.File]
			if start < 0 || end < start || end > len(b) {
				continue
			}
			kind, recv := objectKind(obj)
			sig := types.ObjectString(obj, func(p *types.Package) string { return p.Path() })
			sym := &Symbol{ID: id, Name: obj.Name(), Kind: kind, Receiver: recv, Package: a.pkgPath(), Signature: sig, Position: pos, EndLine: fs.Position(n.End()).Line, Test: strings.HasSuffix(pos.File, "_test.go"), Code: string(b[start:end]), Comment: comment, start: start, end: end}
			if old := s.Symbols[id]; old == nil || len(sym.Code) > len(old.Code) {
				s.Symbols[id] = sym
			}
		}
		for _, d := range u.f.Decls {
			if f, ok := d.(*ast.FuncDecl); ok {
				a.funcs[f] = a.objectID(u.p.TypesInfo.Defs[f.Name])
			}
		}
	}
	for _, u := range units {
		a.pkg = u.p
		a.file = u.f
		ast.Inspect(u.f, func(n ast.Node) bool {
			f, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			pos := a.position(f.Pos())
			start, end := fs.Position(f.Pos()).Offset, fs.Position(f.End()).Offset
			b := files[pos.File]
			if start < 0 || end < start || end > len(b) {
				return true
			}
			id := fmt.Sprintf("%s#closure@%s:%d:%d:%s", a.pkgPath(), pos.File, pos.Line, pos.Column, s.ID)
			a.literals[f] = id
			s.Symbols[id] = &Symbol{ID: id, Name: "func literal", Kind: "closure", Package: a.pkgPath(), Signature: types.TypeString(u.p.TypesInfo.TypeOf(f), func(p *types.Package) string { return p.Path() }), Position: pos, EndLine: fs.Position(f.End()).Line, Test: strings.HasSuffix(pos.File, "_test.go"), Code: string(b[start:end]), start: start, end: end}
			return true
		})
	}
	for _, u := range units {
		a.pkg = u.p
		a.file = u.f
		a.relations(parentMap(u.f))
	}
	// Stable source order without repeatedly serializing relations inside a sort comparator.
	type key struct {
		symbol, from, kind, resolution, file, candidates string
		line, column                                     int
	}
	unique := map[key]bool{}
	out := s.Relations[:0]
	for _, r := range s.Relations {
		k := key{r.Symbol, r.From, r.Kind, r.Resolution, r.Position.File, strings.Join(r.Candidates, "\x00"), r.Position.Line, r.Position.Column}
		if !unique[k] {
			unique[k] = true
			out = append(out, r)
		}
	}
	s.Relations = out
	sort.Slice(s.Relations, func(i, j int) bool {
		a, b := s.Relations[i], s.Relations[j]
		if a.Position.File != b.Position.File {
			return a.Position.File < b.Position.File
		}
		if a.Position.Line != b.Position.Line {
			return a.Position.Line < b.Position.Line
		}
		if a.Position.Column != b.Position.Column {
			return a.Position.Column < b.Position.Column
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.From != b.From {
			return a.From < b.From
		}
		if a.Resolution != b.Resolution {
			return a.Resolution < b.Resolution
		}
		return strings.Join(a.Candidates, "\x00") < strings.Join(b.Candidates, "\x00")
	})
	a.collectTypeFields()

	makeChunks(s, c.ChunkBytes, c.ModelKey)
	s.Text = NewTextIndex(s.Chunks)
	return s, nil
}
func inside(n ast.Node, f *ast.File) bool { return n.Pos() >= f.Pos() && n.End() <= f.End() }
func parentMap(f ast.Node) map[ast.Node]ast.Node {
	m := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			m[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return m
}
func (a *analyzer) pkgPath() string {
	p := a.pkg.PkgPath
	if a.pkg.Types != nil {
		p = a.pkg.Types.Path()
	}
	if i := strings.Index(p, " ["); i >= 0 {
		p = p[:i]
	}
	return p
}
func (a *analyzer) position(p token.Pos) Position {
	v := a.fs.Position(p)
	rel, err := filepath.Rel(a.root, v.Filename)
	if err != nil {
		rel = v.Filename
	}
	return Position{filepath.ToSlash(rel), v.Line, v.Column}
}

func objectKind(o types.Object) (string, string) {
	switch v := o.(type) {
	case *types.Func:
		if sig, ok := v.Type().(*types.Signature); ok && sig.Recv() != nil {
			return "method", types.TypeString(sig.Recv().Type(), func(p *types.Package) string { return p.Path() })
		}
		return "function", ""
	case *types.TypeName:
		return "type", ""
	case *types.Const:
		return "constant", ""
	case *types.Var:
		if v.IsField() {
			return "field", ""
		}
		if v.Pkg() != nil && v.Parent() == v.Pkg().Scope() {
			return "variable", ""
		}
		return "local", ""
	case *types.PkgName:
		return "import", ""
	default:
		return "local", ""
	}
}
func (a *analyzer) objectID(o types.Object) string {
	if f, ok := o.(*types.Func); ok {
		o = f.Origin()
	}
	if o == nil {
		return ""
	}
	if id := a.objects[o]; id != "" {
		return id
	}
	kind, recv := objectKind(o)
	pkg := "builtin"
	if o.Pkg() != nil {
		pkg = o.Pkg().Path()
		if i := strings.Index(pkg, " ["); i >= 0 {
			pkg = pkg[:i]
		}
	}
	if v, ok := o.(*types.Var); ok && v.IsField() {
		own := a.owner[v]
		if own == "" {
			own = a.ownerPos[a.position(o.Pos())]
		}
		if own != "" {
			return own + "#field:" + o.Name()
		}
		p := a.position(o.Pos())
		return fmt.Sprintf("%s#field:%s@%s:%d:%d", pkg, o.Name(), p.File, p.Line, p.Column)
	}
	if kind == "local" || kind == "import" || (kind == "type" && o.Pkg() != nil && o.Parent() != o.Pkg().Scope()) || (kind == "constant" && o.Pkg() != nil && o.Parent() != o.Pkg().Scope()) {
		p := a.position(o.Pos())
		return fmt.Sprintf("%s#%s:%s@%s:%d:%d:%s", pkg, kind, o.Name(), p.File, p.Line, p.Column, a.s.ID)
	}
	// init declarations need a position to distinguish multiple init functions.
	if o.Name() == "init" {
		p := a.position(o.Pos())
		return fmt.Sprintf("%s#function:init@%s:%d", pkg, p.File, p.Line)
	}
	return pkg + "#" + kind + ":" + recv + ":" + o.Name()
}
func (a *analyzer) from(n ast.Node, parents map[ast.Node]ast.Node) string {
	for p := n; p != nil; p = parents[p] {
		if f, ok := p.(*ast.FuncLit); ok {
			return a.literals[f]
		}
		if f, ok := p.(*ast.FuncDecl); ok {
			return a.funcs[f]
		}
	}
	return ""
}
func (a *analyzer) line(p Position) string {
	starts := a.lineStarts[p.File]
	b := a.s.Files[p.File]
	if p.Line < 1 || p.Line > len(starts) {
		return ""
	}
	start := starts[p.Line-1]
	end := len(b)
	if p.Line < len(starts) {
		end = starts[p.Line] - 1
	}
	return clip(string(bytes.TrimSuffix(b[start:min(end, start+304)], []byte{'\r'})), 300)
}

func (a *analyzer) relations(parents map[ast.Node]ast.Node) {
	ast.Inspect(a.file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		from := a.from(n, parents)
		if id, ok := n.(*ast.Ident); ok {
			if o := a.pkg.TypesInfo.Uses[id]; o != nil {
				if v, ok := o.(*types.Var); ok && v.IsField() {
					if kv, ok := parents[id].(*ast.KeyValueExpr); ok && kv.Key == id {
						p := a.position(id.Pos())
						a.s.Relations = append(a.s.Relations, Relation{Symbol: a.objectID(v), From: from, Kind: "write", Resolution: "confirmed", Position: p, Code: a.line(p)})
					}
				}
				p := a.position(id.Pos())
				a.s.Relations = append(a.s.Relations, Relation{Symbol: a.objectID(o), From: from, Kind: "reference", Resolution: "confirmed", Position: p, Code: a.line(p)})
			}
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if v, ok := a.pkg.TypesInfo.ObjectOf(sel.Sel).(*types.Var); ok && v.IsField() {
				p := a.position(sel.Sel.Pos())
				k := fieldKind(sel, parents)
				a.s.Relations = append(a.s.Relations, Relation{Symbol: a.objectID(v), From: from, Kind: k, Resolution: "confirmed", Position: p, Code: a.line(p)})
			}
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if tv, ok := a.pkg.TypesInfo.Types[call.Fun]; ok && tv.IsType() {
				return true
			}
			target, res, candidates := a.callTarget(call.Fun)
			p := a.position(call.Pos())
			a.s.Relations = append(a.s.Relations, Relation{Symbol: target, From: from, Kind: "call", Resolution: res, Position: p, Candidates: candidates, Code: a.line(p)})
		}
		return true
	})
}
func fieldKind(sel *ast.SelectorExpr, parents map[ast.Node]ast.Node) string {
	var node ast.Node = sel
	for {
		p := parents[node]
		switch v := p.(type) {
		case *ast.ParenExpr:
			node = p
			continue
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				return "address"
			}
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if lhs == node {
					if v.Tok == token.ASSIGN || v.Tok == token.DEFINE {
						return "write"
					}
					return "read_write"
				}
			}
		case *ast.RangeStmt:
			if v.Key == node || v.Value == node {
				return "write"
			}
		case *ast.IncDecStmt:
			if v.X == node {
				return "read_write"
			}
		}
		return "read"
	}
}
func (a *analyzer) callTarget(e ast.Expr) (string, string, []string) {
	switch v := e.(type) {
	case *ast.FuncLit:
		return a.literals[v], "confirmed", nil
	case *ast.ParenExpr:
		return a.callTarget(v.X)
	case *ast.IndexExpr:
		return a.callTarget(v.X)
	case *ast.IndexListExpr:
		return a.callTarget(v.X)
	case *ast.Ident:
		o := a.pkg.TypesInfo.ObjectOf(v)
		if f, ok := o.(*types.Func); ok {
			return a.objectID(f), "confirmed", nil
		}
		if _, ok := o.(*types.Builtin); ok {
			return a.objectID(o), "confirmed", nil
		}
	case *ast.SelectorExpr:
		if sel := a.pkg.TypesInfo.Selections[v]; sel != nil {
			if f, ok := sel.Obj().(*types.Func); ok {
				t := sel.Recv()
				if ptr, ok := t.(*types.Pointer); ok {
					t = ptr.Elem()
				}
				if iface, ok := t.Underlying().(*types.Interface); ok {
					var candidates []string
					dedup := map[string]bool{}
					for obj, id := range a.objects {
						fn, ok := obj.(*types.Func)
						if !ok || fn.Name() != f.Name() {
							continue
						}
						sig, ok := fn.Type().(*types.Signature)
						if !ok || sig.Recv() == nil {
							continue
						}
						if _, abstract := sig.Recv().Type().Underlying().(*types.Interface); abstract {
							continue
						}
						if !dedup[id] && types.Implements(sig.Recv().Type(), iface) {
							candidates = append(candidates, id)
							dedup[id] = true
						}
					}
					sort.Strings(candidates)
					return a.objectID(f), "candidates", candidates
				}
				return a.objectID(f), "confirmed", nil
			}
		}
		if f, ok := a.pkg.TypesInfo.ObjectOf(v.Sel).(*types.Func); ok {
			return a.objectID(f), "confirmed", nil
		}
	}
	return "", "unresolved", nil
}
