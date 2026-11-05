package lens

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromotedFieldDiscoveryAndUnknownIDRecovery(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.org/navigation\ngo 1.26.0\n"), 0600)
	src := `package navigation
 type RiskParams struct { Limit int }
 type Params struct { *RiskParams; Code string }
 type Other struct { Limit string }
 type Ambiguous struct { RiskParams; Other }
 type Shadow struct { RiskParams; Limit string }
 type Recursive struct { *Recursive; Count int }
 type Inner[T any] struct { Value T }
 type Outer struct { Inner[int] }
 func Update(p *Params) { p.Limit = 10; p.Limit++ }
 `
	os.WriteFile(filepath.Join(root, "fields.go"), []byte(src), 0600)
	os.MkdirAll(filepath.Join(root, "external"), 0700)
	os.WriteFile(filepath.Join(root, "external", "ext.go"), []byte("package external\nimport nav \"example.org/navigation\"\ntype Wrapper struct { *nav.Params }\n"), 0600)
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
	outer := sym(t, s, "example.org/navigation", "Params", "type")
	canonical := "example.org/navigation.RiskParams#field:Limit"
	found := false
	for _, f := range s.TypeFields[outer.ID] {
		if f.Name == "Limit" {
			found = true
			if f.Symbol != canonical || !f.Promoted || !f.Available || strings.Join(f.AccessPath, ".") != "RiskParams.Limit" {
				t.Fatalf("incorrect canonical field: %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("promoted field not discoverable")
	}
	r, err := e.Call(ctx, "get_symbol", Args{Symbol: outer.ID, Limit: 1})
	if err != nil || r.Meta["fields_truncated"] != true {
		t.Fatal("type field budget", err, r.Meta)
	}
	r, err = e.Call(ctx, "search_symbols", Args{OwnerID: outer.ID, Kind: "field", Query: "Limit"})
	if err != nil {
		t.Fatal(err)
	}
	items := r.Items.([]any)
	if len(items) != 1 || items[0].(Symbol).ID != canonical || strings.Join(items[0].(Symbol).AccessPath, ".") != "RiskParams.Limit" {
		t.Fatal(items)
	}
	r, err = e.Call(ctx, "find_field_accesses", Args{Symbol: canonical})
	if err != nil || len(r.Items.([]any)) != 2 {
		t.Fatal("canonical field access failed", err, r.Items)
	}
	_, err = e.Call(ctx, "find_field_accesses", Args{Symbol: "example.org/navigation#field::Params.Limit"})
	var lookup *LookupError
	if !errors.As(err, &lookup) || lookup.Code != "unknown_symbol_id" {
		t.Fatal("missing actionable error", err)
	}
	ok := false
	for _, v := range lookup.Candidates {
		if v.ID == canonical {
			ok = true
		}
	}
	if !ok {
		t.Fatal("canonical field missing from suggestions", lookup)
	}
	external := sym(t, s, "example.org/navigation/external", "Wrapper", "type")
	externalFound := false
	for _, f := range s.TypeFields[external.ID] {
		if f.Name == "Limit" {
			externalFound = true
			if f.Symbol != canonical || !f.Available || strings.Join(f.AccessPath, ".") != "Params.RiskParams.Limit" {
				t.Fatal("cross-package field identity", f)
			}
		}
	}
	if !externalFound {
		t.Fatal("cross-package promoted field missing")
	}
	ambiguous := sym(t, s, "example.org/navigation", "Ambiguous", "type")
	for _, f := range s.TypeFields[ambiguous.ID] {
		if f.Name == "Limit" {
			t.Fatal("ambiguous promoted field falsely resolved", f)
		}
	}
	shadow := sym(t, s, "example.org/navigation", "Shadow", "type")
	for _, f := range s.TypeFields[shadow.ID] {
		if f.Name == "Limit" && (f.Promoted || f.Symbol == canonical) {
			t.Fatal("shadowing ignored", f)
		}
	}
	generic := sym(t, s, "example.org/navigation", "Outer", "type")
	for _, f := range s.TypeFields[generic.ID] {
		if f.Name == "Value" && (!f.Available || f.Type != "int") {
			t.Fatal("generic field origin", f)
		}
	}
}
func TestCrossPackagePromotedDiscoveryAndDistinctSearch(t *testing.T) {
	e, _, _ := newTest(t)
	ctx := context.Background()
	s, _ := e.Current(ctx)
	app := sym(t, s, "example.com/strategy/state", "Application", "type")
	field := sym(t, s, "example.com/strategy/state", "Limit", "field")
	r, err := e.Call(ctx, "search_symbols", Args{OwnerID: app.ID, Query: "Limit", Kind: "field"})
	if err != nil || r.Items.([]any)[0].(Symbol).ID != field.ID {
		t.Fatal(err, r.Items)
	}
	long := sym(t, s, "example.com/strategy/state", "LongPipeline", "function")
	r, err = e.Call(ctx, "search_code", Args{Query: "LongPipeline", Mode: "lexical", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, raw := range r.Items.([]any) {
		h := raw.(Hit)
		if !h.VerifySource {
			t.Fatal("missing evidence warning")
		}
		if h.Chunk.Symbol == long.ID {
			n++
			if h.OtherChunks == 0 {
				t.Fatal("grouped siblings not reported")
			}
		}
	}
	if n != 1 {
		t.Fatal("duplicate parent symbols", n)
	}
	no := false
	r, err = e.Call(ctx, "search_code", Args{Query: "LongPipeline", Mode: "lexical", Limit: 100, DistinctSymbols: &no})
	if err != nil {
		t.Fatal(err)
	}
	n = 0
	for _, v := range r.Items.([]any) {
		if v.(Hit).Chunk.Symbol == long.ID {
			n++
		}
	}
	if n < 2 {
		t.Fatal("raw chunk mode unavailable")
	}
	r, err = e.Call(ctx, "search_code", Args{Query: "Calculate", Mode: "lexical", Paths: []string{"consumer/", "refinance/"}, ExcludePaths: []string{"refinance/"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Items.([]any) {
		if !strings.HasPrefix(v.(Hit).Chunk.File, "consumer/") {
			t.Fatal("scope escaped")
		}
	}
}
func TestUnknownNameSuggestionsAreNotRenames(t *testing.T) {
	e, _, _ := newTest(t)
	r, err := e.Call(context.Background(), "search_symbols", Args{Query: "CalculateChanged"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items.([]any)) != 0 || r.Meta["exact_match"] != false {
		t.Fatal("missing declaration masquerades as existing", r)
	}
	ts := queryTerms("лимит по доходу")
	if contains(ts, "по") || !contains(ts, "доходу") {
		t.Fatal("stop-word filtering", ts)
	}
	s := &Snapshot{Symbols: map[string]*Symbol{"f": {ID: "f", Name: "Fn", Kind: "function"}}, Chunks: []*Chunk{{ID: "c", Symbol: "f"}}}
	hits := fuse(s, Args{}, map[string][]rank{"vector": {{I: 0, Score: 1}}})
	if hits[0].Evidence != "semantic_only" || !hits[0].VerifySource {
		t.Fatal("semantic-only result must require verification")
	}
}
