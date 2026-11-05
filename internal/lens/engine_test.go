package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEmbed struct {
	mu   sync.Mutex
	Docs int
	Fail bool
}

func (f *fakeEmbed) Embed(ctx context.Context, mode string, ss []string) (EmbeddingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return EmbeddingResult{}, err
	}
	if f.Fail {
		return EmbeddingResult{}, fmt.Errorf("offline runtime missing")
	}
	r := EmbeddingResult{}
	for range ss {
		v := make([]float32, 384)
		v[0] = 1
		r.Vectors = append(r.Vectors, v)
		r.Tokens = append(r.Tokens, 10)
		r.Windows = append(r.Windows, 1)
		if mode == "document" {
			f.Docs++
		}
	}
	return r, nil
}
func (f *fakeEmbed) Close() error { return nil }
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "..", "examples", "strategy")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(root, rel), 0700)
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(root, rel), b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func newTest(t *testing.T) (*Engine, *fakeEmbed, string) {
	t.Helper()
	root := fixture(t)
	f := &fakeEmbed{}
	e, err := New(Config{Root: root}, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	return e, f, root
}
func sym(t *testing.T, s *Snapshot, pkg, name, kind string) *Symbol {
	t.Helper()
	for _, v := range s.Symbols {
		if v.Package == pkg && v.Name == name && v.Kind == kind {
			return v
		}
	}
	t.Fatalf("missing symbol %s %s %s", pkg, name, kind)
	return nil
}
func TestTypedNavigation(t *testing.T) {
	e, _, _ := newTest(t)
	s, err := e.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Complete {
		t.Fatal(s.Diagnostics)
	}
	names := map[string]int{}
	for _, v := range s.Symbols {
		names[v.Package+":"+v.Name+":"+v.Kind]++
	}
	if names["example.com/strategy/consumer:Calculate:function"] != 1 {
		t.Fatal("test variants duplicated")
	}
	if names["example.com/strategy/consumer:limit:local"] != 2 {
		t.Fatal("shadowing not distinct")
	}
	field := sym(t, s, "example.com/strategy/state", "Limit", "field")
	kinds := map[string]int{}
	cross := 0
	for _, r := range s.Relations {
		if r.Symbol == field.ID {
			kinds[r.Kind]++
			if r.Position.File == "consumer/consumer.go" {
				cross++
			}
		}
	}
	for _, k := range []string{"read", "write", "read_write", "address"} {
		if kinds[k] == 0 {
			t.Fatalf("missing %s for %s: %+v", k, field.ID, kinds)
		}
	}
	if cross == 0 {
		t.Fatal("cross-package promoted field missing")
	}
	other := sym(t, s, "example.com/strategy/other", "Limit", "field")
	if other.ID == field.ID {
		t.Fatal("field collision")
	}
	final := sym(t, s, "example.com/strategy/state", "Finalize", "function")
	states := map[string]int{}
	for _, r := range s.Relations {
		if r.From == final.ID && r.Kind == "call" {
			states[r.Resolution]++
			if r.Resolution == "candidates" && r.Symbol != "" && len(r.Candidates) < 2 {
				t.Fatalf("expected consumer and refinance candidates: %+v", r)
			}
		}
	}
	for _, k := range []string{"confirmed", "candidates"} {
		if states[k] == 0 {
			t.Fatalf("missing call resolution %s: %+v", k, states)
		}
	}
	long := sym(t, s, "example.com/strategy/state", "LongPipeline", "function")
	var code strings.Builder
	count := 0
	for _, c := range s.Chunks {
		if c.Symbol == long.ID {
			code.WriteString(c.Code)
			count++
		}
	}
	if count < 2 || code.String() != long.Code {
		t.Fatal("chunk coverage is not exact")
	}
	tests := 0
	for _, v := range s.Symbols {
		if v.Kind == "function" && strings.HasPrefix(v.Name, "Test") {
			tests++
		}
	}
	if tests != 2 {
		t.Fatalf("test variant definitions = %d", tests)
	}
}
func TestRefreshDeleteCacheAndStalePagination(t *testing.T) {
	e, f, root := newTest(t)
	ctx := context.Background()
	old, _ := e.Current(ctx)
	r, err := e.Call(ctx, "search_symbols", Args{Limit: 1})
	if err != nil || r.Next == "" {
		t.Fatal(err, r)
	}
	before := f.Docs
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Docs != before {
		t.Fatal("unchanged embeddings recomputed")
	}
	p := filepath.Join(root, "consumer", "consumer.go")
	b, _ := os.ReadFile(p)
	b = append([]byte("\n\n"), b...)
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Call(ctx, "search_symbols", Args{}); err == nil {
		t.Fatal("stale answer accepted")
	}
	for i := 0; i < 3000; i++ {
		st := e.Status(ctx)
		if !st.Loading && st.Fresh {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cur, err := e.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cur.ID == old.ID {
		t.Fatal("snapshot unchanged")
	}
	if sym(t, cur, "example.com/strategy/consumer", "Calculate", "function").Position.Line != sym(t, old, "example.com/strategy/consumer", "Calculate", "function").Position.Line+2 {
		t.Fatal("positions not refreshed")
	}
	if _, err = e.Call(ctx, "search_symbols", Args{Cursor: r.Next, Limit: 1}); err == nil {
		t.Fatal("old cursor accepted")
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Docs != before {
		t.Fatal("line shifts changed embedding content")
	}
	if err = os.Remove(filepath.Join(root, "education", "education.go")); err != nil {
		t.Fatal(err)
	}
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Current(ctx)
	for _, v := range s.Symbols {
		if v.Name == "StudyGrace" {
			t.Fatal("deleted symbol remains")
		}
	}
	for _, c := range s.Chunks {
		if strings.HasPrefix(c.File, "education/") {
			t.Fatal("deleted chunk remains")
		}
	}
	for id := range e.vectors.Chunks {
		found := false
		for _, c := range s.Chunks {
			if c.ID == id {
				found = true
			}
		}
		if !found {
			t.Fatal("deleted vector remains")
		}
	}
	p = filepath.Join(root, "consumer", "consumer.go")
	b, _ = os.ReadFile(p)
	b = []byte(strings.Replace(string(b), "a.Income * 6", "a.Income * 7", 1))
	os.WriteFile(p, b, 0600)
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Docs != before+1 {
		t.Fatalf("changed function cache misses %d", f.Docs-before)
	}
}
func TestPartialAndFailedRefresh(t *testing.T) {
	e, _, root := newTest(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(root, "broken.go"), []byte("package main\nfunc Broken(){ var x int = \"bad\"; _ = x }\n"), 0600)
	if err := e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := e.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Complete || len(s.Diagnostics) == 0 {
		t.Fatal("type errors hidden")
	}
	os.WriteFile(filepath.Join(root, "broken.go"), []byte("package main\nfunc Broken( {\n"), 0600)
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = e.Current(ctx)
	if err != nil || s.Complete {
		t.Fatal("parse errors hidden", err)
	}
	os.Remove(filepath.Join(root, "go.mod"))
	if err = e.Refresh(ctx); err == nil {
		t.Fatal("expected failure")
	}
	if _, err = e.Current(ctx); err == nil {
		t.Fatal("old snapshot served after failure")
	}
}
func TestDegradedRuntimeAndModelMismatch(t *testing.T) {
	root := fixture(t)
	f := &fakeEmbed{Fail: true}
	e, err := New(Config{Root: root}, f)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err == nil {
		t.Fatal("expected unavailable runtime")
	}
	r, err := e.Call(ctx, "search_symbols", Args{Query: "Calculate"})
	if err != nil || r.Items == nil {
		t.Fatal("exact navigation must survive", err)
	}
	r, err = e.Call(ctx, "search_code", Args{Query: "Calculate"})
	if err != nil || len(r.Limitations) < 5 {
		t.Fatal("hybrid degradation not disclosed", err)
	}
	c := e.C
	c.ModelKey = "other-revision"
	d := vectorDisk{Version: 1, Model: ModelKey, Dimension: 384, Records: map[string]VectorRecord{}, Chunks: map[string]string{}}
	if err = saveVectors(c, d); err != nil {
		t.Fatal(err)
	}
	if _, err = loadVectors(c); err == nil {
		t.Fatal("incompatible persisted model accepted")
	}
}
func TestLimitsFiltersAndContext(t *testing.T) {
	e, _, _ := newTest(t)
	ctx := context.Background()
	s, _ := e.Current(ctx)
	no := false
	r, err := e.Call(ctx, "search_symbols", Args{Query: "Test", IncludeTests: &no})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items.([]any)) != 0 {
		t.Fatal("tests filter failed")
	}
	r, err = e.Call(ctx, "search_code", Args{Query: "Calculate", Path: "consumer/", Mode: "lexical"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Items.([]any) {
		if !strings.HasPrefix(v.(Hit).Chunk.File, "consumer/") {
			t.Fatal("path filter")
		}
	}
	if _, err = e.Call(ctx, "search_code", Args{Query: "x", Limit: 100000}); err == nil {
		t.Fatal("unbounded limit")
	}
	if _, err = e.Call(ctx, "get_source", Args{File: "../go.mod"}); err == nil {
		t.Fatal("path traversal")
	}
	long := sym(t, s, "example.com/strategy/state", "LongPipeline", "function")
	r, err = e.Call(ctx, "build_context", Args{Symbols: []string{long.ID}, Budget: 1500, Limit: 3, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || len(r.Items.([]ContextItem)) > 3 {
		t.Fatal("context unbounded")
	}
	r, err = e.Call(ctx, "get_source", Args{File: "state/state.go", Start: 1, End: 4})
	if err != nil || r.Snapshot != s.ID {
		t.Fatal(err)
	}
}
func TestPersistenceAndExclusiveCache(t *testing.T) {
	root := fixture(t)
	f := &fakeEmbed{}
	e, err := New(Config{Root: root}, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(Config{Root: root}, f); err == nil {
		t.Fatal("shared cache writer allowed")
	}
	ctx := context.Background()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.Docs
	e.Close()
	e, err = New(Config{Root: root}, f)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.WaitVectors(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Docs != before {
		t.Fatal("persisted embeddings not reused")
	}
	b, _ := json.Marshal(e.Status(ctx))
	if !strings.Contains(string(b), "cache_hits") {
		t.Fatal("missing progress")
	}
}
