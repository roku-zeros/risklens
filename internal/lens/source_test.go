package lens

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceEOFAndByteContinuation(t *testing.T) {
	for _, text := range []string{"", "one", "one\n", "one\ntwo", "one\ntwo\n"} {
		s := &Snapshot{Files: map[string][]byte{"x.go": []byte(text)}}
		e := &Engine{C: Config{MaxSourceBytes: 100}}
		for _, end := range []int{0, -1, 500} {
			r, err := e.source(s, Args{File: "x.go", End: end}, Response{})
			if err != nil {
				t.Fatal(text, end, err)
			}
			item := r.Items.(map[string]any)
			if item["eof"] != true || item["total_lines"] != sourceLineCount([]byte(text)) || r.Truncated {
				t.Fatal(item, r)
			}
			if strings.TrimSuffix(text, "\n") != item["code"] {
				t.Fatal("lost source", item)
			}
		}
	}
	s := &Snapshot{Files: map[string][]byte{"x.go": []byte("abc\ndef\nghi\n")}}
	e := &Engine{C: Config{MaxSourceBytes: 5}}
	r, err := e.source(s, Args{File: "x.go", End: -1}, Response{})
	if err != nil || !r.Truncated || r.Meta["next_line"] != 2 {
		t.Fatal(r, err)
	}
	item := r.Items.(map[string]any)
	if item["code"] != "abc" || item["eof"] != false {
		t.Fatal(item)
	}
	r, err = e.source(s, Args{File: "x.go", Start: 2, End: -1}, Response{})
	if err != nil || r.Items.(map[string]any)["code"] != "def" {
		t.Fatal(r, err)
	}
	e.C.MaxSourceBytes = 2
	blocked, blockErr := e.source(s, Args{File: "x.go", End: -1}, Response{})
	if blockErr != nil || !blocked.Truncated || blocked.Items.(map[string]any)["returned_lines"] != 0 || !strings.Contains(blocked.Meta["continuation"].(string), "increase") {
		t.Fatal("stalled byte limit is not explained", blocked, blockErr)
	}
	for _, a := range []Args{{File: "x.go", Start: 4, End: -1}, {File: "x.go", Start: 2, End: 1}, {File: "x.go", End: -2}} {
		_, err := e.source(s, a, Response{})
		var detail *InputError
		if !errors.As(err, &detail) || detail.Details["total_lines"] != 3 {
			t.Fatal("missing recovery", err)
		}
	}
}
func TestInventoryPaginationAndFreshnessTimings(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module inventory\ngo 1.26.0\n"), 0600)
	for i := 0; i < 105; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte(fmt.Sprintf("package inventory\nvar V%d = %d\n", i, i)), 0600)
	}
	e, err := New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	a := Args{Limit: 100}
	for {
		r, err := e.Call(ctx, "list_files", a)
		if err != nil {
			t.Fatal(err)
		}
		if r.Meta["total_results"] != 106 || r.Meta["freshness_scans"] != 2 {
			t.Fatal(r.Meta)
		}
		for _, raw := range r.Items.([]any) {
			item := raw.(map[string]any)
			name := item["file_path"].(string)
			if seen[name] {
				t.Fatal("duplicate file", name)
			}
			seen[name] = true
			if item["total_lines"] == 0 {
				t.Fatal(item)
			}
		}
		if r.Next == "" {
			if r.Truncated {
				t.Fatal("truncated without cursor")
			}
			break
		}
		if !r.Truncated || r.Meta["results_complete"] != false {
			t.Fatal("silent truncation", r)
		}
		a.Cursor = r.Next
		a.Snapshot = r.Snapshot
	}
	if len(seen) != 106 {
		t.Fatal("incomplete inventory", len(seen))
	}
	st := e.Status(ctx)
	if st.Meta["freshness_scans"] != 1 || !st.Fresh {
		t.Fatal(st)
	}
	r, err := e.Call(ctx, "get_source", Args{File: "f000.go", End: 999})
	if err != nil || r.Items.(map[string]any)["range_clamped"] != true {
		t.Fatal(r, err)
	}
	// Same-length edits with restored mtime must still be detected by content hashing.
	path := filepath.Join(root, "f000.go")
	info, _ := os.Stat(path)
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), "= 0", "= 9", 1))
	os.WriteFile(path, b, 0600)
	os.Chtimes(path, info.ModTime(), info.ModTime())
	if _, err = e.Call(ctx, "list_files", Args{}); err == nil {
		t.Fatal("same-length/mtime edit was not detected")
	}
}

func TestStructFieldTruncationIsVisibleAtTopLevel(t *testing.T) {
	e, _, _ := newTest(t)
	ctx := context.Background()
	s, err := e.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := sym(t, s, "example.com/strategy/state", "Application", "type")
	r, err := e.Call(ctx, "get_symbol", Args{Symbol: owner.ID, Limit: 1})
	if err != nil || !r.Truncated || r.Meta["fields_truncated"] != true || r.Meta["results_complete"] != false {
		t.Fatal("partial fields look complete", r, err)
	}
}
