package lens

import (
	"context"
	"testing"
)

func TestQueryCacheIsBoundedAndModelScoped(t *testing.T) {
	e, f, _ := newTest(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		r, err := e.Call(ctx, "search_code", Args{Query: "Calculate", Mode: "vector"})
		if err != nil {
			t.Fatal(err)
		}
		stage := r.Meta["query_embedding_cache"].(map[string]any)
		if stage["hit"] != (i == 1) {
			t.Fatal(stage)
		}
	}
	if f.Queries != 1 {
		t.Fatal("recomputed query", f.Queries)
	}
	old := e.C.ModelKey
	e.C.ModelKey = "another-model-key"
	if _, err := e.embedQuery(ctx, "Calculate"); err != nil {
		t.Fatal(err)
	}
	e.C.ModelKey = old
	if f.Queries != 2 {
		t.Fatal("mixed model keys")
	}
	for i := 0; i < 260; i++ {
		if _, err := e.embedQuery(ctx, string(rune(1000+i))); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.queryCache) > 256 || len(e.queryOrder) > 256 {
		t.Fatal("unbounded cache")
	}
}
