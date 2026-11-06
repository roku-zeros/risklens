package lens

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestContractAliasesTypedPathsAndAudit(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module contracts\ngo 1.26.0\n"), 0600)
	code := "package contracts\ntype Request struct { Rules struct { Age struct { Min int `json:\"age_min\"` } }; PTI int `json:\"pti_max\"`; Hidden int `json:\"-\"` }\ntype Other struct { PTI int `json:\"pti_max\"` }\n"
	os.WriteFile(filepath.Join(root, "main.go"), []byte(code), 0600)
	os.WriteFile(filepath.Join(root, "notes.md"), []byte("`Request.Rules.Age.Min` `age_min` `pti_max` `APP_UNKNOWN` `Unknown.Path` `Ghost.Request.PTI` `MissingBuilder()`"), 0600)
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
	owner := sym(t, s, "contracts", "Request", "type")
	for _, q := range []struct {
		query, namespace string
		count            int
	}{{"Request.Rules.Age.Min", "go", 1}, {"age_min", "json", 1}, {"Request.Rules.Age.Min()", "go", 0}, {"pti_max", "json", 2}, {"PTI_MAX", "json", 0}, {"-", "json", 0}} {
		r, err := e.Call(ctx, "resolve_reference", Args{Query: q.query, Namespace: q.namespace})
		if err != nil || len(r.Items.([]any)) != q.count {
			t.Fatal(q, r, err)
		}
	}
	r, err := e.Call(ctx, "resolve_reference", Args{Query: "pti_max", Namespace: "json", OwnerID: owner.ID})
	if err != nil || len(r.Items.([]any)) != 1 {
		t.Fatal("owner filter failed", r, err)
	}
	r, err = e.Call(ctx, "audit_document", Args{Document: "notes.md"})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, check := range r.Items.([]EvidenceCheck) {
		statuses[check.Reference] = check.Status
		if check.Reference == "pti_max" && len(check.CandidateGroups) == 0 {
			t.Fatal("missing groups", check)
		}
	}
	for ref, status := range map[string]string{"Request.Rules.Age.Min": "field_path_verified", "age_min": "contract_name_verified", "pti_max": "ambiguous_symbol", "APP_UNKNOWN": "unrecognized_token", "Unknown.Path": "unbound_path", "Ghost.Request.PTI": "unbound_path", "MissingBuilder()": "missing_symbol"} {
		if statuses[ref] != status {
			t.Fatal(ref, statuses)
		}
	}
}
func TestUnresolvedCallCoverageIsExplicit(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module calls\ngo 1.26.0\n"), 0600)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package calls\nfunc Entry(f func()){f()}\n"), 0600)
	e, err := New(Config{Root: root, CallAnalysis: "ast"}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	if err = e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Current(ctx)
	entry := sym(t, s, "calls", "Entry", "function")
	r, err := e.Call(ctx, "get_callees", Args{Symbol: entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	cov := r.Meta["call_coverage"].(map[string]any)
	if cov["counts"].(map[string]int)["unresolved"] != 1 {
		t.Fatal(cov)
	}
	site := r.Items.([]any)[0].(Relation)
	if site.Resolution != "unresolved" || !site.UnknownTargets {
		t.Fatal(site)
	}
}
