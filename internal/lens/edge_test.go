package lens

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenericOriginsLocalFieldsAndClosureCallers(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module edge\ngo 1.26.0\n"), 0600)
	src := `package edge
type Box[T any] struct { Value T }
func (b *Box[T]) Put(v T) { b.Value = v }
func Run() {
 b := &Box[int]{}; b.Put(1)
 type Local struct { Value int }; x := Local{}; x.Value = 1
 { type Local struct { Value int }; y := Local{}; y.Value = 2 }
 f := func() { b.Put(3) }; f()
 func() { b.Put(4) }()
}
`
	os.WriteFile(filepath.Join(root, "edge.go"), []byte(src), 0600)
	e, err := New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Current(context.Background())
	if !s.Complete {
		t.Fatal(s.Diagnostics)
	}
	put := sym(t, s, "edge", "Put", "method")
	run := sym(t, s, "edge", "Run", "function")
	fields := map[string]bool{}
	closures := 0
	for id, v := range s.Symbols {
		if v.Kind == "field" && v.Name == "Value" {
			fields[id] = true
		}
		if v.Kind == "closure" {
			closures++
		}
	}
	if len(fields) != 3 || closures != 2 {
		t.Fatal("local identity or closures", fields, closures)
	}
	calls := 0
	fromRun := 0
	for _, r := range s.Relations {
		if r.Kind == "call" && r.Symbol == put.ID {
			calls++
			if r.From == run.ID {
				fromRun++
			}
		}
	}
	if calls != 3 || fromRun != 1 {
		t.Fatalf("generic origin/closure call attribution: %d total, %d from Run", calls, fromRun)
	}
}
func TestExactContextBudgetAndEmptyJSON(t *testing.T) {
	e, _, _ := newTest(t)
	ctx := context.Background()
	r, err := e.Call(ctx, "search_code", Args{Query: "UNMATCHED_1Z9", Mode: "lexical"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"items":[]`) {
		t.Fatal("empty results must be array", string(b))
	}
	s, _ := e.Current(ctx)
	f := sym(t, s, "example.com/strategy/state", "LongPipeline", "function")
	r, err = e.Call(ctx, "build_context", Args{Symbols: []string{f.ID}, Budget: 1500, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(r.Items)
	if len(b) > 1500 {
		t.Fatalf("context array is %d bytes", len(b))
	}
}

func TestSourceCoverageDoesNotOverlap(t *testing.T) {
	e, _, _ := newTest(t)
	s, _ := e.Current(context.Background())
	coverage := map[string][]bool{}
	for name, b := range s.Files {
		if strings.HasSuffix(name, ".go") {
			coverage[name] = make([]bool, len(b))
		}
	}
	for _, c := range s.Chunks {
		for i := c.start; i < c.end; i++ {
			if coverage[c.File][i] {
				t.Fatalf("overlapping source %s:%d", c.File, i)
			}
			coverage[c.File][i] = true
		}
	}
	for name, covered := range coverage {
		for i, yes := range covered {
			b := s.Files[name][i]
			if !yes && b != ' ' && b != '\n' && b != '\t' && b != '\r' {
				t.Fatalf("unindexed source %s byte %d", name, i)
			}
		}
	}
}
func TestChangingFilesDuringAnalysisIsRejected(t *testing.T) {
	e, _, root := newTest(t)
	p := filepath.Join(root, "consumer", "consumer.go")
	b, _ := os.ReadFile(p)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			_ = os.WriteFile(p, append(append([]byte{}, b...), []byte(strings.Repeat("\n", i))...), 0600)
			time.Sleep(time.Millisecond)
		}
	}()
	err := e.Refresh(context.Background())
	close(stop)
	<-done
	if err == nil {
		t.Fatal("published a snapshot while files kept changing")
	}
	if _, err = e.Current(context.Background()); err == nil {
		t.Fatal("served previous snapshot as current")
	}
}

func TestSnapshotIncludesIndexConfiguration(t *testing.T) {
	root := fixture(t)
	a := Config{Root: root}
	if err := a.Defaults(); err != nil {
		t.Fatal(err)
	}
	_, first, err := readFiles(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	b := a
	b.ChunkBytes = 240
	_, second, err := readFiles(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("index configuration must invalidate snapshot cursors")
	}
}
