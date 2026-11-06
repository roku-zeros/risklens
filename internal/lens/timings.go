package lens

import (
	"context"
	"sync"
	"time"
)

type timingKey struct{}
type requestTimings struct {
	mu     sync.Mutex
	phases map[string]float64
	stages map[string]any
	scans  int
	files  int
	bytes  int64
}

func startTimings(ctx context.Context) (context.Context, *requestTimings) {
	t := &requestTimings{phases: map[string]float64{}, stages: map[string]any{}}
	return context.WithValue(ctx, timingKey{}, t), t
}
func recordTime(ctx context.Context, phase string, start time.Time) {
	recordDuration(ctx, phase, time.Since(start))
}
func recordDuration(ctx context.Context, phase string, duration time.Duration) {
	if t, ok := ctx.Value(timingKey{}).(*requestTimings); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.phases[phase] += float64(duration) / float64(time.Millisecond)
		if phase == "freshness" {
			t.scans++
		}
	}
}
func recordScanStats(ctx context.Context, files int, bytes int64) {
	if t, ok := ctx.Value(timingKey{}).(*requestTimings); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.files += files
		t.bytes += bytes
	}
}
func (t *requestTimings) metadata(start time.Time) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	phases := map[string]float64{}
	for k, v := range t.phases {
		phases[k] = v
	}
	phases["total"] = float64(time.Since(start)) / float64(time.Millisecond)
	meta := map[string]any{"timings_ms": phases, "freshness_scans": t.scans, "freshness_file_reads": t.files, "freshness_source_bytes": t.bytes, "timing_scope": "server processing before JSON serialization; freshness is aggregate full-file scans; phases may overlap; excludes client queue/startup"}
	for k, v := range t.stages {
		meta[k] = v
	}
	return meta
}

func recordStage(ctx context.Context, name string, value any) {
	if t, ok := ctx.Value(timingKey{}).(*requestTimings); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.stages[name] = value
	}
}
