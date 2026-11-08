package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"risklens/internal/lens"
)

type ArgumentError struct {
	Code     string         `json:"code"`
	Tool     string         `json:"tool"`
	Field    string         `json:"field,omitempty"`
	Message  string         `json:"message"`
	Allowed  []string       `json:"allowed_fields,omitempty"`
	Expected map[string]any `json:"expected,omitempty"`
	Example  map[string]any `json:"example_arguments"`
	Hint     string         `json:"hint"`
}

func (e *ArgumentError) Error() string { return e.Message + "; " + e.Hint }
func argumentExample(name string) map[string]any {
	switch name {
	case "batch_tools":
		return map[string]any{"calls": []any{map[string]any{"tool": "get_source", "arguments": map[string]any{"file": "main.go", "end_line": -1}}}}
	case "resolve_reference":
		return map[string]any{"query": "Request.Limit", "namespace": "go"}
	case "verify_evidence":
		return map[string]any{"claims": []any{map[string]any{"file": "main.go", "start_line": 1, "end_line": 1, "expected_text": "package"}}}
	case "audit_document":
		return map[string]any{"document": "docs/notes.md", "limit": 20}
	case "get_source":
		return map[string]any{"file": "main.go", "start_line": 1, "end_line": -1}
	case "search_code":
		return map[string]any{"query": "limit", "mode": "hybrid", "limit": 20}
	case "list_files", "search_symbols", "project_map":
		return map[string]any{"limit": 20}
	default:
		return map[string]any{"symbol_id": "copy canonical ID from search_symbols"}
	}
}
func Validate(name string, raw json.RawMessage) (lens.Args, error) {
	var a lens.Args
	var spec *Tool
	for _, tool := range Tools() {
		if tool.Name == name {
			copy := tool
			spec = &copy
			break
		}
	}
	if spec == nil {
		return a, &ArgumentError{Code: "unknown_tool", Tool: name, Message: "unknown tool " + name, Example: map[string]any{}, Hint: "Call tools/list for supported tools."}
	}
	fail := func(code, field, msg string, expected map[string]any, allowed []string) error {
		hint := "Use the documented field names and types; call tools/list for the current schema."
		if name == "verify_evidence" {
			hint = "claims must contain objects using symbol_id/file/start_line/end_line/expected_text/source_hash; symbol/line/text are not accepted aliases."
		}
		if name == "audit_document" {
			hint = "Use document=<root-relative Markdown path>, not file; limit is a page size, continue with next_cursor."
		}
		return &ArgumentError{code, name, field, msg, allowed, expected, argumentExample(name), hint}
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := validateValue(raw, spec.InputSchema, "", fail); err != nil {
		return a, err
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fail("invalid_arguments", "", err.Error(), nil, nil)
	}
	if len(a.Cursor) > 4096 {
		return a, fail("argument_too_long", "cursor", "cursor exceeds 4096 bytes", nil, nil)
	}
	return a, nil
}

type argumentFailure func(string, string, string, map[string]any, []string) error

func validateValue(raw json.RawMessage, schema map[string]any, path string, fail argumentFailure) error {
	typ, _ := schema["type"].(string)
	bad := func() error {
		return fail("invalid_argument_type", path, fmt.Sprintf("argument %s must be %s (not null)", path, typ), schema, nil)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return bad()
	}
	switch typ {
	case "object":
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return bad()
		}
		if schema["properties"] == nil && schema["additionalProperties"] == true {
			return nil
		}
		props := schema["properties"].(map[string]any)
		allowed := make([]string, 0, len(props))
		for k := range props {
			allowed = append(allowed, k)
		}
		sort.Strings(allowed)
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			field := k
			if path != "" {
				field = path + "." + k
			}
			child, ok := props[k]
			if !ok {
				return fail("unknown_argument", field, "unknown argument "+field+"; allowed fields: "+strings.Join(allowed, ", "), nil, allowed)
			}
			if err := validateValue(values[k], child.(map[string]any), field, fail); err != nil {
				return err
			}
		}
		if required, ok := schema["required"].([]string); ok {
			for _, k := range required {
				if _, ok := values[k]; !ok {
					return fail("missing_argument", k, "required argument "+k, props[k].(map[string]any), allowed)
				}
			}
		}
	case "array":
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return bad()
		}
		if n, ok := schema["minItems"].(int); ok && len(values) < n {
			return fail("invalid_argument_size", path, fmt.Sprintf("%s must have at least %d items", path, n), schema, nil)
		}
		if n, ok := schema["maxItems"].(int); ok && len(values) > n {
			return fail("invalid_argument_size", path, fmt.Sprintf("%s must have at most %d items", path, n), schema, nil)
		}
		for i, v := range values {
			if err := validateValue(v, schema["items"].(map[string]any), fmt.Sprintf("%s[%d]", path, i), fail); err != nil {
				return err
			}
		}
	case "string":
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return bad()
		}
		if n, ok := schema["maxLength"].(int); ok && utf8.RuneCountInString(v) > n {
			return fail("invalid_argument_size", path, fmt.Sprintf("%s exceeds %d characters", path, n), schema, nil)
		}
		if enums, ok := schema["enum"].([]string); ok {
			found := false
			for _, x := range enums {
				found = found || x == v
			}
			if !found {
				return fail("invalid_argument_value", path, "invalid "+path+"; expected "+strings.Join(enums, ", "), schema, nil)
			}
		}
	case "integer":
		var v int
		if json.Unmarshal(raw, &v) != nil {
			return bad()
		}
		if n, ok := schema["minimum"].(int); ok && v < n {
			return fail("invalid_argument_value", path, fmt.Sprintf("%s must be >= %d", path, n), schema, nil)
		}
		if n, ok := schema["maximum"].(int); ok && v > n {
			return fail("invalid_argument_value", path, fmt.Sprintf("%s must be <= %d", path, n), schema, nil)
		}
	case "boolean":
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			return bad()
		}
	}
	return nil
}
