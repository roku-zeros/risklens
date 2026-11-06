package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"risklens/internal/lens"
)

func TestSchemaValidation(t *testing.T) {
	for _, x := range []string{`{"symbol_id":null}`, `{"symbol_id":123}`, `{"symbol_id":"x","extra":1}`, `{}`} {
		if _, err := Validate("get_symbol", json.RawMessage(x)); err == nil {
			t.Fatal("accepted", x)
		}
	}
	if _, err := Validate("search_code", json.RawMessage(`{"query":"x","limit":2}`)); err != nil {
		t.Fatal(err)
	}
}
func TestStdioLifecycleAndResponsiveStatus(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\ngo 1.26.0\n"), 0600)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package demo\nfunc Hello() {}\n"), 0600)
	e, err := lens.New(lens.Config{Root: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	inr, inw := io.Pipe()
	outr, outw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := Server{Engine: e}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, inr, outw); outw.Close() }()
	reader := bufio.NewReader(outr)
	call := func(id int, method string, params any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		fmt.Fprintln(inw, string(b))
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err = json.Unmarshal(line, &v); err != nil {
			t.Fatal("non-protocol stdout", string(line))
		}
		if v["error"] != nil {
			t.Fatal(v)
		}
		return v["result"].(map[string]any)
	}
	init := call(1, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if init["protocolVersion"] != "2025-11-25" {
		t.Fatal(init)
	}
	fmt.Fprintln(inw, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	ls := call(2, "tools/list", map[string]any{})
	if len(ls["tools"].([]any)) != 18 {
		t.Fatal(ls)
	}
	call(3, "tools/call", map[string]any{"name": "refresh_index", "arguments": map[string]any{}})
	st := call(4, "tools/call", map[string]any{"name": "index_status", "arguments": map[string]any{}})
	if st["isError"] != false {
		t.Fatal(st)
	}
	for i := 5; i < 200; i++ {
		st = call(i, "tools/call", map[string]any{"name": "index_status", "arguments": map[string]any{}})
		data := st["structuredContent"].(map[string]any)
		if data["fresh"] == true {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := call(300, "tools/call", map[string]any{"name": "search_symbols", "arguments": map[string]any{"query": "Hello"}})
	if r["isError"] != false {
		t.Fatal(r)
	}
	data := r["structuredContent"].(map[string]any)
	if len(data["items"].([]any)) != 1 {
		t.Fatal(data)
	}
	bad := call(301, "tools/call", map[string]any{"name": "find_field_accesses", "arguments": map[string]any{"symbol_id": "demo#field::Params.Limit"}})
	if bad["isError"] != true {
		t.Fatal("unknown ID was silently accepted", bad)
	}
	details := bad["structuredContent"].(map[string]any)["details"].(map[string]any)
	if details["code"] != "unknown_symbol_id" {
		t.Fatal(details)
	}
	badData := bad["structuredContent"].(map[string]any)
	if badData["meta"].(map[string]any)["freshness_scans"] != float64(1) || badData["freshness_checked"] != false {
		t.Fatal("error caused an additional freshness scan", badData)
	}
	fmt.Fprintln(inw, `{"jsonrpc":"2.0","id":302,"method":"tools/call","params":{"name":"audit_document","arguments":{"file":"x.md"}}}`)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var invalid map[string]any
	if err = json.Unmarshal(line, &invalid); err != nil {
		t.Fatal(err)
	}
	failure := invalid["error"].(map[string]any)
	dataError := failure["data"].(map[string]any)
	if failure["code"] != float64(-32602) || dataError["field"] != "file" || dataError["example_arguments"].(map[string]any)["document"] == nil {
		t.Fatal("missing machine recovery", invalid)
	}

	inw.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNavigationArgumentsAndStructuredLookupError(t *testing.T) {
	for _, raw := range []string{`{"owner_id":"p#type::Params","query":"Limit","kind":"field"}`, `{"paths":["product/","common/"],"exclude_paths":["other/"],"query":"limiting","distinct_symbols":true}`} {
		name := "search_symbols"
		if strings.Contains(raw, "distinct_symbols") {
			name = "search_code"
		}
		if _, err := Validate(name, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActionableArgumentErrors(t *testing.T) {
	cases := []struct{ name, raw, code, field string }{
		{"audit_document", `{"file":"x.md"}`, "unknown_argument", "file"},
		{"verify_evidence", `{"claims":["x"]}`, "invalid_argument_type", "claims[0]"},
		{"verify_evidence", `{"claims":[{"symbol":"x"}]}`, "unknown_argument", "claims[0].symbol"},
		{"get_source", `{"file":"x.go","end_line":-2}`, "invalid_argument_value", "end_line"},
		{"search_code", `{"query":"x","mode":"wrong"}`, "invalid_argument_value", "mode"},
	}
	for _, c := range cases {
		_, err := Validate(c.name, json.RawMessage(c.raw))
		var detail *ArgumentError
		if !errors.As(err, &detail) || detail.Code != c.code || detail.Field != c.field || len(detail.Example) == 0 || detail.Hint == "" {
			t.Fatal(c, err)
		}
	}
	if _, err := Validate("get_source", json.RawMessage(`{"file":"x.go","end_line":-1}`)); err != nil {
		t.Fatal(err)
	}
}

func TestNewToolRecoveryExamplesAreValid(t *testing.T) {
	for name, raw := range map[string]string{"batch_tools": `{"calls":[]}`, "resolve_reference": `{"query":"x","namespace":"wrong"}`} {
		_, err := Validate(name, json.RawMessage(raw))
		var detail *ArgumentError
		if !errors.As(err, &detail) {
			t.Fatal(err)
		}
		example, _ := json.Marshal(detail.Example)
		if _, err = Validate(name, example); err != nil {
			t.Fatal("recovery example violates the tool schema", name, err)
		}
	}
}
