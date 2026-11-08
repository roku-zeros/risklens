package lens

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectMapNavigation(t *testing.T) {
	root := t.TempDir()
	for file, contents := range map[string]string{
		"00.go":             "package main\n// Runner manages a calibration calibration calibration counter.\ntype Runner struct{}\n",
		"go.mod":            "module example.test/map\ngo 1.26.0\n",
		"main.go":           "package main\nimport \"example.test/map/impl\"\nfunc main() { _ = impl.Value{} }\nfunc init() {}\n",
		"api/api.go":        "// Package api handles monetary calibration.\npackage api\ntype Runner interface { Run() }\ntype Empty interface{}\n",
		"impl/impl.go":      "package impl\nimport \"example.test/map/api\"\ntype Value struct{}\nfunc (Value) Run() {}\ntype Pointer struct{}\nfunc (*Pointer) Run() {}\ntype Other struct{}\nfunc (Other) Run(int) {}\nvar _ api.Runner = Value{}\n",
		"impl/impl_test.go": "package impl\ntype TestOnly struct{}\nfunc (TestOnly) Run() {}\n",
	} {
		full := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := Config{Root: root, CallAnalysis: "ast"}
	e, err := New(c, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	if err := e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	call := func(tool string, a Args) Response {
		t.Helper()
		r, err := e.Call(ctx, tool, a)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	page := call("project_map", Args{Limit: 2})
	if page.Fresh || page.Meta["freshness_scans"] != 0 || !page.Truncated || page.Next == "" {
		t.Fatalf("bad first page: %+v", page)
	}
	next := call("project_map", Args{Limit: 2, Cursor: page.Next, Snapshot: page.Snapshot})
	if next.Truncated || len(next.Items.([]MapNode)) != 1 {
		t.Fatalf("bad continuation: %+v", next)
	}
	pkg := page.Items.([]MapNode)[0]
	overview := call("project_map", Args{NodeID: pkg.ID, EdgeKind: "imports", Direction: "out", Depth: 2, Freshness: "strict"})
	if !overview.Fresh || overview.Meta["freshness_scans"] != 2 || len(overview.Items.([]MapNode)) != 3 {
		t.Fatalf("bad import chain: %+v", overview)
	}
	entries := call("project_map", Args{NodeID: pkg.ID, EdgeKind: "contains", Direction: "out"})
	entry := entries.Items.([]MapNode)[1]
	if entry.Kind != "entry_point" || entry.Position.File != "main.go" || len(entries.Items.([]MapNode)) != 3 {
		t.Fatalf("bad entry points: %+v", entries)
	}
	api := page.Items.([]MapNode)[1]
	members := call("project_map", Args{NodeID: api.ID, EdgeKind: "contains", Direction: "out"})
	iface := members.Items.([]MapNode)[2]
	if iface.Name != "Runner" {
		t.Fatalf("bad interface node: %+v", iface)
	}
	result := call("project_map", Args{NodeID: iface.ID, EdgeKind: "implements", Direction: "in"})
	edges := result.Meta["edges"].([]MapEdge)
	if len(edges) != 2 || edges[0].Form != "pointer" || edges[1].Form != "value_and_pointer" {
		t.Fatalf("bad implementations: %+v", result)
	}
	truncated := call("project_map", Args{NodeID: iface.ID, EdgeKind: "implements", Limit: 2})
	if !truncated.Truncated || len(truncated.Items.([]MapNode)) != 2 {
		t.Fatalf("subgraph ignored the node limit: %+v", truncated)
	}
	if got := call("get_symbol", Args{Symbol: iface.ID, Snapshot: page.Snapshot, Freshness: "snapshot"}); got.Fresh || got.Meta["freshness_scans"] != 0 {
		t.Fatalf("unexpected symbol freshness: %+v", got)
	}
	if api.Summary == nil || api.Summary.GoDoc != "Package api handles monetary calibration." || api.Summary.DocPosition.File != "api/api.go" {
		t.Fatalf("missing package summary: %+v", api.Summary)
	}
	a := Args{Query: "monetary calibration Runner", Mode: "lexical", Kind: "type", Freshness: "snapshot", Snapshot: page.Snapshot}
	search := call("search_code", a)
	hits := search.Items.([]any)
	first := hits[0].(Hit)
	if first.Chunk.Symbol != iface.ID || first.Ranks["package_context"] != 1 || search.Fresh || search.Meta["freshness_scans"] != 0 {
		t.Fatalf("package context did not promote the typed source: %+v", search)
	}
	off := false
	a.PackageContext = &off
	baseline := call("search_code", a)
	if baseline.Items.([]any)[0].(Hit).Chunk.Package != pkg.Package {
		t.Fatalf("expected the lexical distractor to win without GoDoc context: %+v", baseline)
	}
	for _, hit := range baseline.Items.([]any) {
		if hit.(Hit).Ranks["package_context"] != 0 {
			t.Fatalf("package context was not disabled: %+v", hit)
		}
	}
	a.Package = api.Package
	scoped := call("search_code", a)
	scope := scoped.Meta["search_scope"].(map[string]any)
	if scope["candidate_chunks"].(int) >= scope["index_chunks"].(int) {
		t.Fatalf("package scope did not narrow the candidates: %+v", scope)
	}
	for _, hit := range scoped.Items.([]any) {
		if hit.(Hit).Chunk.Package != api.Package {
			t.Fatalf("package scope leaked another package: %+v", hit)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := call("get_source", Args{File: "main.go", Freshness: "snapshot", Snapshot: page.Snapshot})
	if source.Fresh || source.Meta["freshness_scans"] != 0 || source.Snapshot != page.Snapshot {
		t.Fatalf("bad cached source: %+v", source)
	}
	if _, err := e.Call(ctx, "get_source", Args{File: "main.go", Freshness: "snapshot", Snapshot: "wrong"}); err == nil {
		t.Fatal("snapshot mismatch accepted")
	}
	if _, err := e.Call(ctx, "get_source", Args{File: "main.go"}); err == nil {
		t.Fatal("strict source accepted stale data")
	}
}
