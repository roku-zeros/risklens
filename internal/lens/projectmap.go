package lens

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"path"
	"sort"
	"strings"
)

type MapNode struct {
	Summary  *PackageSummary `json:"summary,omitempty"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Package  string          `json:"package"`
	Position *Position       `json:"position,omitempty"`
}

type MapEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Form string `json:"form,omitempty"`
}

type projectMap struct {
	nodes         map[string]MapNode
	packages      []string
	out, in       map[string][]MapEdge
	implTruncated bool
}

const implementationCheckLimit = 250_000

func (g *projectMap) addEdge(edge MapEdge) {
	g.out[edge.From] = append(g.out[edge.From], edge)
	g.in[edge.To] = append(g.in[edge.To], edge)
}

func (a *analyzer) buildProjectMap(ctx context.Context, pkgImports map[string]map[string]bool, declarations []types.Object) (*projectMap, error) {
	g := &projectMap{nodes: map[string]MapNode{}, out: map[string][]MapEdge{}, in: map[string][]MapEdge{}}
	packageID := func(pkg string) string { return "package:" + pkg }
	for pkg := range pkgImports {
		id := packageID(pkg)
		g.packages = append(g.packages, id)
		g.nodes[id] = MapNode{ID: id, Name: path.Base(pkg), Kind: "package", Package: pkg}
	}
	sort.Strings(g.packages)
	for _, id := range g.packages {
		for target := range pkgImports[g.nodes[id].Package] {
			if _, ok := g.nodes[packageID(target)]; ok {
				g.addEdge(MapEdge{From: id, To: packageID(target), Kind: "imports"})
			}
		}
	}
	type declaration struct {
		node MapNode
		typ  types.Type
	}
	var interfaces, concrete []declaration
	for _, obj := range declarations {
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		pkg := obj.Pkg().Path()
		kind := ""
		switch obj := obj.(type) {
		case *types.Func:
			kind = "entry_point"
		case *types.TypeName:
			if obj.IsAlias() {
				continue
			}
			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, ok := named.Underlying().(*types.Interface); ok {
				kind = "interface"
			} else if named.TypeParams().Len() != 0 {
				continue
			}
		}
		id, pos := a.objectID(obj), a.position(obj.Pos())
		node := MapNode{ID: id, Name: obj.Name(), Kind: kind, Package: pkg, Position: &pos}
		if kind == "" {
			if _, ok := obj.(*types.TypeName); ok {
				node.Kind = "type"
				concrete = append(concrete, declaration{node, obj.Type()})
			}
			continue
		}
		g.nodes[id] = node
		g.addEdge(MapEdge{From: packageID(pkg), To: id, Kind: "contains"})
		if kind == "interface" {
			named := obj.Type().(*types.Named)
			iface := named.Underlying().(*types.Interface)
			if named.TypeParams().Len() == 0 && iface.IsMethodSet() && iface.NumMethods() > 0 {
				interfaces = append(interfaces, declaration{node, named})
			}
		}
	}

	// A candidate must have every interface method. Index pointer method sets
	// once and test candidates from the smallest matching bucket.
	sort.Slice(concrete, func(i, j int) bool { return concrete[i].node.ID < concrete[j].node.ID })
	sort.Slice(interfaces, func(i, j int) bool { return interfaces[i].node.ID < interfaces[j].node.ID })
	byMethod := map[string][]int{}
	for i, d := range concrete {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		methods := types.NewMethodSet(types.NewPointer(d.typ))
		for j := 0; j < methods.Len(); j++ {
			key := methods.At(j).Obj().Id()
			byMethod[key] = append(byMethod[key], i)
		}
	}
	checks := 0
	for _, d := range interfaces {
		iface := d.typ.Underlying().(*types.Interface)
		candidates := byMethod[iface.Method(0).Id()]
		for j := 1; j < iface.NumMethods(); j++ {
			bucket := byMethod[iface.Method(j).Id()]
			if len(bucket) < len(candidates) {
				candidates = bucket
			}
		}
		for _, i := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if checks == implementationCheckLimit {
				g.implTruncated = true
				break
			}
			checks++
			impl := concrete[i]
			form := "pointer"
			if types.Implements(impl.typ, iface) {
				form = "value_and_pointer"
			} else if !types.Implements(types.NewPointer(impl.typ), iface) {
				continue
			}
			if _, exists := g.nodes[impl.node.ID]; !exists {
				g.nodes[impl.node.ID] = impl.node
				g.addEdge(MapEdge{From: packageID(impl.node.Package), To: impl.node.ID, Kind: "contains"})
			}
			g.addEdge(MapEdge{From: impl.node.ID, To: d.node.ID, Kind: "implements", Form: form})
		}
		if g.implTruncated {
			break
		}
	}
	for _, adj := range []map[string][]MapEdge{g.in, g.out} {
		for _, edges := range adj {
			sort.Slice(edges, func(i, j int) bool {
				a, b := edges[i], edges[j]
				if a.Kind != b.Kind {
					return a.Kind < b.Kind
				}
				if a.From != b.From {
					return a.From < b.From
				}
				return a.To < b.To
			})
		}
	}
	return g, ctx.Err()
}

func mapDeclarations(file *ast.File, info *types.Info) []types.Object {
	var out []types.Object
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Recv == nil && (decl.Name.Name == "init" || (file.Name.Name == "main" && decl.Name.Name == "main")) {
				if obj, ok := info.Defs[decl.Name].(*types.Func); ok {
					sig := obj.Type().(*types.Signature)
					if sig.Params().Len() == 0 && sig.Results().Len() == 0 && sig.TypeParams().Len() == 0 {
						out = append(out, obj)
					}
				}
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				if spec, ok := spec.(*ast.TypeSpec); ok {
					out = append(out, info.Defs[spec.Name])
				}
			}
		}
	}
	return out
}

const mapScope = "Production files selected by Go build configuration. In-module imports, main/init and named interface declarations. Implementation edges are method-set matches between local named non-generic concrete types and non-empty named runtime interfaces. Implementation edges do not identify runtime dispatch."

func mapPage(ctx context.Context, s *Snapshot, a Args, r Response) (Response, error) {
	g := s.ProjectMap
	if g == nil {
		return r, fmt.Errorf("project map unavailable; refresh the index")
	}
	r.Meta["map_scope"] = mapScope
	r.Meta["implementation_scan_truncated"] = g.implTruncated
	r.Complete = r.Complete && !g.implTruncated
	nodes := []MapNode{}
	if a.NodeID == "" {
		ids := g.packages
		if a.Query != "" {
			ids = nil
			query := strings.ToLower(a.Query)
			for _, id := range g.packages {
				if strings.Contains(strings.ToLower(g.nodes[id].Package), query) {
					ids = append(ids, id)
				}
			}
		}
		start, err := decodeCursor(a.Cursor, s.ID, "project_map", a)
		if err != nil {
			return r, err
		}
		if start > len(ids) {
			return r, fmt.Errorf("cursor offset outside map results")
		}
		end := min(start+a.Limit, len(ids))
		for _, id := range ids[start:end] {
			nodes = append(nodes, g.nodes[id])
		}
		r.Items = nodes
		r.Meta["total_results"] = len(ids)
		r.Truncated = end < len(ids)
		if r.Truncated {
			r.Next = encodeCursor(s.ID, "project_map", a, end)
		}
		return r, ctx.Err()
	}
	root, ok := g.nodes[a.NodeID]
	if !ok {
		return r, fmt.Errorf("unknown map node %q; discover node IDs with project_map", a.NodeID)
	}
	nodes = append(nodes, root)
	depths := map[string]int{root.ID: 0}
	edges := []MapEdge{}
	seen := map[MapEdge]bool{}
	edgeLimit := 3 * a.Limit
walk:
	for i := 0; i < len(nodes); i++ {
		id := nodes[i].ID
		if depths[id] >= a.Depth {
			continue
		}
		for direction, adjacent := range [][]MapEdge{g.out[id], g.in[id]} {
			if (direction == 0 && a.Direction == "in") || (direction == 1 && a.Direction == "out") {
				continue
			}
			for _, edge := range adjacent {
				if err := ctx.Err(); err != nil {
					return r, err
				}
				if seen[edge] || (a.EdgeKind != "" && edge.Kind != a.EdgeKind) {
					continue
				}
				if len(edges) == edgeLimit {
					r.Truncated = true
					break walk
				}
				target := edge.To
				if direction == 1 {
					target = edge.From
				}
				if _, exists := depths[target]; !exists {
					if len(nodes) == a.Limit {
						r.Truncated = true
						break walk
					}
					depths[target] = depths[id] + 1
					nodes = append(nodes, g.nodes[target])
				}
				seen[edge] = true
				edges = append(edges, edge)
			}
		}
	}
	r.Items = nodes
	r.Meta["edges"] = edges
	r.Meta["edge_limit"] = edgeLimit
	if r.Truncated {
		r.Meta["continuation"] = "Narrow by edge_kind/direction or query a returned node; increase limit if needed."
	}
	return r, nil
}

func validateMapArgs(a *Args) error {
	if a.NodeID == "" {
		if a.Depth != 0 || a.Direction != "" || a.EdgeKind != "" {
			return inputError("map_node_required", "node_id", "depth, direction and edge_kind require a map node ID", nil)
		}
		return nil
	}
	if a.Query != "" || a.Cursor != "" {
		return inputError("invalid_map_scope", "node_id", "query and cursor are for package discovery without node_id", nil)
	}
	if a.Depth == 0 {
		a.Depth = 1
	}
	if a.Depth < 1 || a.Depth > 3 {
		return inputError("invalid_map_depth", "depth", "subgraph depth must be 1..3", nil)
	}
	switch a.Direction {
	case "", "in", "out", "both":
	default:
		return inputError("invalid_map_direction", "direction", "direction must be in, out or both", nil)
	}
	switch a.EdgeKind {
	case "", "imports", "contains", "implements":
	default:
		return inputError("invalid_map_edge_kind", "edge_kind", "edge_kind must be imports, contains or implements", nil)
	}
	return nil
}
