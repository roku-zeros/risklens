package lens

import (
	"context"
	"testing"
)

func TestVectorBuildReusesDuplicateInputs(t *testing.T) {
	provider := &fakeEmbed{}
	e, err := New(Config{Root: t.TempDir(), BatchSize: 1}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	s := &Snapshot{ID: "test", Chunks: []*Chunk{
		{ID: "first", Input: "same input", Hash: hash([]byte("same input"))},
		{ID: "second", Input: "same input", Hash: hash([]byte("same input"))},
		{ID: "third", Input: "different input", Hash: hash([]byte("different input"))},
	}}
	e.snap = s
	e.updateVectors(context.Background(), s)
	if provider.Docs != 2 || e.status.VectorDone != 3 || e.status.Vectors != "ready" {
		t.Fatalf("unexpected build: %d inputs, status %+v", provider.Docs, e.status)
	}
	saved, err := loadVectors(e.C)
	if err != nil || len(saved.Records) != 2 || len(saved.Chunks) != 3 || saved.Chunks["first"] != saved.Chunks["second"] {
		t.Fatalf("unexpected persisted index: %+v, %v", saved.Chunks, err)
	}
	e.updateVectors(context.Background(), s)
	if provider.Docs != 2 || e.status.CacheHits != 3 || e.status.VectorDone != 3 {
		t.Fatalf("unexpected cache reuse: %d inputs, status %+v", provider.Docs, e.status)
	}
}
