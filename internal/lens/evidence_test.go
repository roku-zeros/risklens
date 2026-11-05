package lens

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceAndMarkdownAudit(t *testing.T) {
	e, _, root := newTest(t)
	ctx := context.Background()
	s, _ := e.Current(ctx)
	fn := sym(t, s, "example.com/strategy/consumer", "Calculate", "function")
	claim := EvidenceClaim{Symbol: fn.ID, File: fn.Position.File, Start: fn.Position.Line, End: fn.EndLine, Expected: "func Calculate", Hash: hash(s.Files[fn.Position.File])}
	cases := []struct {
		c      EvidenceClaim
		status string
	}{{claim, "source_verified"}, {EvidenceClaim{File: fn.Position.File, Start: 1, Expected: "func Calculate"}, "moved_text"}, {EvidenceClaim{Symbol: fn.ID, File: "old/calc.go"}, "moved_symbol"}, {EvidenceClaim{File: "old/calc.go", Start: 1}, "missing_file"}, {EvidenceClaim{File: fn.Position.File, Start: 99999}, "invalid_range"}, {EvidenceClaim{File: fn.Position.File, Start: 1}, "range_exists_unbound"}, {EvidenceClaim{Symbol: "gone#function::Missing"}, "missing_symbol"}, {EvidenceClaim{File: fn.Position.File, Hash: "stale"}, "source_changed"}}
	for _, c := range cases {
		got := checkEvidence(s, c.c)
		if got.Status != c.status {
			t.Fatalf("%+v: %s != %s", c.c, got.Status, c.status)
		}
	}
	os.MkdirAll(filepath.Join(root, "docs"), 0700)
	doc := "# Audit\n[existing](../consumer/consumer.go#L1-L2)\n`missing/old.go:99`\n`Calculate()`\n`MissingBuilder()`\n`Application.Limit`\n"
	os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte(doc), 0600)
	r, err := e.Call(ctx, "audit_document", Args{Document: "docs/notes.md", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || r.Next == "" {
		t.Fatal("audit must paginate")
	}
	items := r.Items.([]EvidenceCheck)
	if items[0].Status != "range_exists_unbound" || items[0].DocumentLine != 2 {
		t.Fatal(items)
	}
	next, err := e.Call(ctx, "audit_document", Args{Document: "docs/notes.md", Cursor: r.Next, Limit: 2})
	if err != nil || len(next.Items.([]EvidenceCheck)) != 2 {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte(doc+"\nnew text\n"), 0600)
	if _, err = e.Call(ctx, "audit_document", Args{Document: "docs/notes.md", Cursor: r.Next, Limit: 2}); err == nil {
		t.Fatal("document change accepted old cursor")
	}
	outside := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(root, "outside.md"))
	if _, err = e.Call(ctx, "audit_document", Args{Document: "outside.md"}); err == nil {
		t.Fatal("document symlink escaped root")
	}
	r, err = e.Call(ctx, "verify_evidence", Args{Claims: []EvidenceClaim{claim}})
	if err != nil || r.Items.([]EvidenceCheck)[0].Status != "source_verified" || r.Meta["semantic_verification"] != false {
		t.Fatal(err, r)
	}
}
func TestVTAAndFieldTrace(t *testing.T) {
	e, _, _ := newTest(t)
	ctx := context.Background()
	s, _ := e.Current(ctx)
	if s.Calls.State != "ready" || s.Calls.RecoveredSites < 1 {
		t.Fatal("VTA did not run", s.Calls)
	}
	final := sym(t, s, "example.com/strategy/state", "Finalize", "function")
	callbackTarget := ""
	for _, r := range s.Relations {
		if r.From == final.ID && r.Kind == "call" && strings.Contains(r.Code, "callback(a)") {
			if r.Resolution != "candidates" || r.Analysis != "vta_module" || len(r.FlowCandidates) == 0 || !r.UnknownTargets {
				t.Fatal("callback not propagated", r)
			}
			callbackTarget = r.FlowCandidates[0]
		}
	}
	guards := 0
	for _, rel := range s.Relations {
		if rel.Kind == "call" && strings.Contains(rel.Position.File, "main.go") && len(rel.Conditions) > 0 {
			guards++
		}
	}
	if guards < 2 {
		t.Fatal("product branch guards missing")
	}
	if callbackTarget == "" {
		t.Fatal("callback site missing")
	}
	r, err := e.Call(ctx, "get_callers", Args{Symbol: callbackTarget})
	if err != nil || len(r.Items.([]any)) == 0 {
		t.Fatal("callback callers missing", err)
	}
	field := sym(t, s, "example.com/strategy/state", "Limit", "field")
	r, err = e.Call(ctx, "trace_field", Args{Symbol: field.ID, EntrySymbol: final.ID, IncludeCandidates: true, Depth: 5, Limit: 30, Budget: 16000})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, site := range r.Items.([]FieldTraceItem) {
		if site.Access.From == callbackTarget {
			found = true
			if site.Certainty != "candidate_call_path" || len(site.CallPath) == 0 {
				t.Fatal("candidate certainty lost", site)
			}
		}
	}
	if !found {
		t.Fatal("callback field mutation missing")
	}
	r, err = e.Call(ctx, "trace_field", Args{Symbol: field.ID, EntrySymbol: final.ID, Depth: 1, Limit: 1, Budget: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items.([]FieldTraceItem)) > 1 {
		t.Fatal("trace exceeded limits")
	}
}
func TestSSAStaticFunctionValueAndASTFallback(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module flow\ngo 1.26.0\n"), 0600)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package flow\ntype State struct{Value int}\nfunc Write(s *State){s.Value=1}\nfunc Entry(s *State){f:=Write;f(s)}\n"), 0600)
	for _, mode := range []string{"vta", "ast"} {
		e, err := New(Config{Root: root, Cache: filepath.Join(root, mode), CallAnalysis: mode}, &fakeEmbed{})
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		s, _ := e.Current(context.Background())
		entry := sym(t, s, "flow", "Entry", "function")
		found := false
		for _, rel := range s.Relations {
			if rel.From == entry.ID && rel.Kind == "call" {
				found = true
				if mode == "vta" && (rel.Resolution != "confirmed" || rel.Analysis != "ssa_static") {
					t.Fatal(rel, s.Calls)
				}
				if mode == "ast" && rel.Resolution != "unresolved" {
					t.Fatal(rel)
				}
			}
		}
		if !found {
			t.Fatal("missing function-value call")
		}
		e.Close()
	}
}

func TestSSAPointerAliasWrites(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module pointers\ngo 1.26.0\n"), 0600)
	src := `package pointers
type State struct{ Value int }
func Set(p *int){ *p = 9 }
func Address(s *State)*int{return &s.Value}
func Entry(s *State){
 alias:=&s.Value
 *alias=4
 Set(&s.Value)
 *Address(s)=5
}
`
	os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0600)
	e, err := New(Config{Root: root}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Current(ctx)
	field := sym(t, s, "pointers", "Value", "field")
	set := sym(t, s, "pointers", "Set", "function")
	found := map[int]bool{}
	for _, r := range s.Relations {
		if r.Symbol == field.ID && r.Kind == "write" && r.Analysis == "ssa_alias" {
			found[r.Position.Line] = true
			if r.Resolution != "candidates" {
				t.Fatal("alias treated as a runtime fact", r)
			}
			if r.From == set.ID {
				found[-1] = true
			}
		}
	}
	for _, line := range []int{7, 9, -1} {
		if !found[line] {
			t.Fatalf("alias access missing %d: %+v; analysis %+v", line, found, s.Calls)
		}
	}
}
