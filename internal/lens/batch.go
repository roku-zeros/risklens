package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type pinnedSnapshotKey struct{}

func pinnedSnapshot(ctx context.Context) *Snapshot {
	s, _ := ctx.Value(pinnedSnapshotKey{}).(*Snapshot)
	return s
}
func validateSnapshot(ctx context.Context, c Config, s *Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pinnedSnapshot(ctx) == s {
		return nil
	}
	fp, err := fingerprintFiles(ctx, c)
	if err != nil {
		return err
	}
	if fp != s.Fingerprint {
		return fmt.Errorf("files changed during query; refresh and retry")
	}
	return nil
}

type BatchCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}
type BatchRequest struct {
	Tool            string
	Args            Args
	ValidationError error
}
type BatchItem struct {
	Index   int    `json:"index"`
	Tool    string `json:"tool"`
	IsError bool   `json:"is_error"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

func (e *Engine) Batch(ctx context.Context, requests []BatchRequest, expected ...string) (r Response, err error) {
	start := time.Now()
	ctx, timings := startTimings(ctx)
	defer func() {
		if r.Meta == nil {
			r.Meta = map[string]any{}
		}
		for k, v := range timings.metadata(start) {
			r.Meta[k] = v
		}
		if err != nil {
			r.Fresh = false
		}
	}()
	if len(requests) < 1 || len(requests) > 20 {
		return r, inputError("invalid_batch_size", "calls", "batch must have 1..20 read-only calls", nil)
	}
	s, err := e.Current(ctx)
	if err != nil {
		return r, err
	}
	r.Snapshot = s.ID
	if len(expected) > 0 && expected[0] != "" && expected[0] != s.ID {
		return r, fmt.Errorf("snapshot changed: requested %s, current %s", expected[0], s.ID)
	}
	ctx = context.WithValue(ctx, pinnedSnapshotKey{}, s)
	r = Response{Snapshot: s.ID, Complete: s.Complete, Meta: map[string]any{"snapshot_policy": "One immutable Go snapshot. Full content checks before/after the entire batch; individual items do not rescan. Document hashes are validated per audit item.", "byte_limit": 512 * 1024}}
	items := []BatchItem{}
	used := 2
	omitted := -1
	for i, q := range requests {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		item := BatchItem{Index: i, Tool: q.Tool}
		itemErr := q.ValidationError
		switch q.Tool {
		case "batch_tools", "project_status", "index_status", "refresh_index":
			itemErr = fmt.Errorf("batch permits navigation/search/verification tools only; nested/control tools are not allowed")
		}
		if itemErr == nil {
			result, callErr := e.Call(ctx, q.Tool, q.Args)
			itemErr = callErr
			if itemErr == nil {
				result.Fresh = false
				result.Meta["freshness_policy"] = "covered by outer batch validation; inspect batch fresh/index_snapshot"
				item.Result = result
			}
		}
		if itemErr != nil {
			item.IsError = true
			details := any(itemErr)
			if rich, ok := itemErr.(interface{ ErrorDetails() map[string]any }); ok {
				details = rich.ErrorDetails()
			}
			item.Error = map[string]any{"message": itemErr.Error(), "details": details}
		}
		encoded, _ := json.Marshal(item)
		if used+len(encoded)+1 > 512*1024 {
			omitted = i
			break
		}
		used += len(encoded) + 1
		items = append(items, item)
	}
	// Always execute the post-check: the batch's private pinned context must not bypass it.
	checkCtx := context.WithValue(ctx, pinnedSnapshotKey{}, (*Snapshot)(nil))
	if err = validateSnapshot(checkCtx, e.C, s); err != nil {
		return r, err
	}
	r.Fresh = true
	r.Items = items
	r.Truncated = omitted >= 0
	r.Meta["returned_calls"] = len(items)
	r.Meta["requested_calls"] = len(requests)
	r.Meta["accounted_bytes"] = used
	r.Meta["results_complete"] = !r.Truncated
	if omitted >= 0 {
		r.Meta["remaining_from"] = omitted
		r.Meta["continuation"] = "Submit remaining calls in a new batch using returned index_snapshot, or call an oversized item individually."
	}
	return r, nil
}
