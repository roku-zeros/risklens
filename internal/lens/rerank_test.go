package lens

import (
	"context"
	"fmt"
	"testing"
)

type testReranker struct {
	fail  bool
	calls int
}

func (f *testReranker) Score(ctx context.Context, q string, docs []string) ([]float64, error) {
	f.calls++
	if f.fail {
		return nil, fmt.Errorf("missing local reranker")
	}
	scores := []float64{}
	for i := range docs {
		scores = append(scores, float64(i))
	}
	return scores, nil
}
func (f *testReranker) Close() error { return nil }
func TestRerankingIsOptionalAndDegradesSafely(t *testing.T) {
	root := fixture(t)
	e, err := New(Config{Root: root, RerankModelKey: "test-reranker", RerankCandidates: 4}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	f := &testReranker{}
	e.rerank = f
	ctx := context.Background()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := e.Call(ctx, "search_code", Args{Query: "Calculate", Mode: "hybrid"})
	if err != nil {
		t.Fatal(err)
	}
	hits := r.Items.([]any)
	if f.calls != 1 || len(hits) < 2 || hits[0].(Hit).RerankScore == nil || hits[0].(Hit).RerankModel != "test-reranker" {
		t.Fatal("rerank missing")
	}
	if *hits[0].(Hit).RerankScore < *hits[1].(Hit).RerankScore {
		t.Fatal("scores not ordered")
	}
	no := false
	r, err = e.Call(ctx, "search_code", Args{Query: "Calculate", Mode: "hybrid", Rerank: &no})
	if err != nil || f.calls != 1 || r.Items.([]any)[0].(Hit).RerankScore != nil {
		t.Fatal("cannot disable reranker", err)
	}
	f.fail = true
	r, err = e.Call(ctx, "search_code", Args{Query: "Calculate limits", Mode: "hybrid"})
	if err != nil || len(r.Items.([]any)) == 0 || len(r.Limitations) < 5 {
		t.Fatal("reranker failure lost search", err)
	}
}
