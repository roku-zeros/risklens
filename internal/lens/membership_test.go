package lens

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRuleMembershipSeparatesSourceFromExecution(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example.test/membership\ngo 1.26.0\n",
		"rules.go": `package membership

type Rule func()
func Basic() {}
func Extra() {}
func Process(kind string, enabled bool, external func() []Rule) []Rule {
	callback := func() []Rule { return []Rule{Extra} }
	_ = callback
	switch kind {
	case "refinance":
		if enabled { return []Rule{Basic} }
		return nil
	case "shared":
		return []Rule{Basic}
	case "append":
		return append([]Rule{Basic}, Extra)
	default:
		return external()
	}
}
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e, err := New(Config{Root: root, CallAnalysis: "ast"}, &fakeEmbed{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	if err := e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := e.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fn := sym(t, s, "example.test/membership", "Process", "function")
	r, err := e.Call(ctx, "rule_membership", Args{Symbol: fn.ID, Limit: 2})
	if err != nil || !r.Fresh || !r.Truncated || r.Meta["coverage"] != "partial" || r.Meta["execution"] != "unverified" || r.Meta["requirement"] != "unreviewed" {
		t.Fatalf("unexpected membership metadata: %+v, %v", r, err)
	}
	first := r.Items.([]any)
	explicit := first[0].(MembershipFact)
	basic := sym(t, s, fn.Package, "Basic", "function")
	if explicit.Symbol != basic.ID || explicit.Status != "explicit_element" || explicit.Expression != "Basic" || len(explicit.Conditions) != 2 || explicit.Conditions[1] != "if enabled" || first[1].(MembershipFact).Status != "explicit_empty" {
		t.Fatalf("wrong branch evidence: %+v", first)
	}
	next, err := e.Call(ctx, "rule_membership", Args{Symbol: fn.ID, Cursor: r.Next, Snapshot: r.Snapshot, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	rows := next.Items.([]any)
	extra := sym(t, s, fn.Package, "Extra", "function")
	if len(rows) != 4 || rows[0].(MembershipFact).Symbol != basic.ID || rows[1].(MembershipFact).Symbol != basic.ID || rows[2].(MembershipFact).Symbol != extra.ID || rows[3].(MembershipFact).Status != "unknown" {
		t.Fatalf("callback leaked or unsupported paths claimed as proof: %+v", next)
	}
}
