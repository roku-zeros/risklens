package lens

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// InputError describes a rejected runtime argument without silently guessing intent.
type InputError struct {
	Code, Field, Message string
	Details              map[string]any
}

func (e *InputError) Error() string { return e.Message }
func (e *InputError) ErrorDetails() map[string]any {
	d := map[string]any{"code": e.Code, "field": e.Field}
	for k, v := range e.Details {
		d[k] = v
	}
	return d
}
func inputError(code, field, message string, details map[string]any) error {
	return &InputError{code, field, message, details}
}
func validSourcePath(file string) bool {
	if file == "" || filepath.IsAbs(file) || strings.ContainsAny(file, "\\:") {
		return false
	}
	for _, part := range strings.Split(file, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func sourceLineCount(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := 1 + bytes.Count(b, []byte("\n"))
	if b[len(b)-1] == '\n' {
		n--
	}
	return n
}
func (e *Engine) source(s *Snapshot, a Args, r Response) (Response, error) {
	b, ok := s.Files[a.File]
	if !ok {
		return r, inputError("file_not_indexed", "file", "file is not indexed; use list_files and copy a root-relative file_path", map[string]any{"file": a.File, "recovery_tool": "list_files"})
	}
	total := sourceLineCount(b)
	start := a.Start
	if start == 0 {
		start = 1
	}
	end := a.End
	if end == 0 {
		end = start + 39
	}
	if start < 1 || a.End < -1 || (end != -1 && end < start) || (total > 0 && start > total) || (total == 0 && start != 1) {
		return r, inputError("invalid_line_range", "start_line/end_line", fmt.Sprintf("invalid 1-based line range; total_lines=%d; end_line=-1 reads to EOF", total), map[string]any{"total_lines": total, "start_line": a.Start, "end_line": a.End, "hint": "Use start_line>=1 within the file and end_line>=start_line, or end_line=-1 for EOF."})
	}
	requestedEnd := end
	if end == -1 || end > total {
		end = total
	}
	lines := strings.Split(string(b), "\n")
	used := 0
	last := start - 1
	out := []string{}
	for i := start - 1; i < end; i++ {
		if used+len(lines[i])+1 > e.C.MaxSourceBytes {
			break
		}
		out = append(out, lines[i])
		used += len(lines[i]) + 1
		last = i + 1
	}
	r.Truncated = last < end
	r.Meta = map[string]any{"requested_start_line": start, "requested_end_line": requestedEnd, "range_clamped": requestedEnd > total, "total_lines": total, "byte_limit": e.C.MaxSourceBytes}
	if last < total {
		r.Meta["next_line"] = last + 1
		r.Meta["continuation"] = "get_source with start_line=next_line, end_line=-1 and index_snapshot"
	}
	if r.Truncated && len(out) == 0 {
		r.Meta["continuation"] = "A source line exceeds max_source_bytes; increase the configured byte limit. Retrying the same range cannot make progress."
	}
	r.Items = map[string]any{"file": a.File, "start_line": start, "end_line": last, "code": strings.Join(out, "\n"), "source_hash": hash(b), "total_lines": total, "returned_lines": len(out), "eof": last >= total, "range_clamped": requestedEnd > total}
	return r, nil
}
