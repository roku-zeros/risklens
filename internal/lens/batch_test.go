package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchAmortizesStrictScans(t *testing.T) {
	e, _, _ := newTest(t)
	ctx := context.Background()
	requests := []BatchRequest{{Tool: "list_files", Args: Args{Limit: 2}}, {Tool: "get_source", Args: Args{File: "main.go", End: -1}}, {Tool: "get_source", Args: Args{File: "missing.go"}}, {Tool: "refresh_index"}, {Tool: "search_symbols", Args: Args{Query: "Calculate"}}}
	r, err := e.Batch(ctx, requests)
	if err != nil {
		t.Fatal(err)
	}
	items := r.Items.([]BatchItem)
	if !r.Fresh || r.Meta["freshness_scans"] != 2 || len(items) != 5 || !items[2].IsError || !items[3].IsError || items[4].IsError {
		t.Fatal("bad batch", r)
	}
	for _, i := range []int{0, 1, 4} {
		result := items[i].Result.(Response)
		if result.Meta["freshness_scans"] != 0 || result.Fresh {
			t.Fatal("item advertised independent freshness", result)
		}
	}
	if _, err = e.Batch(ctx, requests, "wrong-snapshot"); err == nil {
		t.Fatal("ignored outer snapshot")
	}
}

type mutateReranker struct{ file string }

func (f *mutateReranker) Close() error { return nil }
func (f *mutateReranker) Score(ctx context.Context, q string, docs []string) ([]float64, error) {
	b, err := os.ReadFile(f.file)
	if err != nil {
		return nil, err
	}
	err = os.WriteFile(f.file, append(b, []byte("\n// modified during batch\n")...), 0600)
	return make([]float64, len(docs)), err
}
func TestBatchDiscardsResultsAfterSourceChange(t *testing.T) {
	e, _, root := newTest(t)
	e.rerank = &mutateReranker{filepath.Join(root, "main.go")}
	yes := true
	r, err := e.Batch(context.Background(), []BatchRequest{{Tool: "get_source", Args: Args{File: "main.go"}}, {Tool: "search_code", Args: Args{Query: "Calculate", Mode: "hybrid", Rerank: &yes}}})
	if err == nil || r.Fresh || r.Items != nil {
		t.Fatal("published results after mutation", r, err)
	}
}

func TestBatchByteBudgetHasExplicitContinuation(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module budget\ngo 1.26.0\n"), 0600)
	var source strings.Builder
	source.WriteString("package budget\n")
	for i := 0; i < 1200; i++ {
		fmt.Fprintf(&source, "var Value%04d = %d\n", i, i)
	}
	os.WriteFile(filepath.Join(root, "main.go"), []byte(source.String()), 0600)
	e, err := New(Config{Root: root, MaxResults: 1000, CallAnalysis: "ast"}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := []BatchRequest{}
	for i := 0; i < 5; i++ {
		calls = append(calls, BatchRequest{Tool: "search_symbols", Args: Args{Limit: 1000}})
	}
	r, err := e.Batch(context.Background(), calls)
	if err != nil || !r.Truncated || r.Meta["remaining_from"] == nil || !r.Fresh || r.Meta["freshness_scans"] != 2 {
		t.Fatal("missing batch continuation", r.Meta, err)
	}
	b, _ := json.Marshal(r.Items)
	if len(b) > 512*1024 {
		t.Fatal("batch exceeded items budget", len(b))
	}
}
