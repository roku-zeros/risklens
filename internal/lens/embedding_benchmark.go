package lens

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"time"
)

type EmbeddingTrial struct {
	Threads        int     `json:"threads"`
	BatchSize      int     `json:"batch_size"`
	WorkerBatch    int     `json:"worker_batch_size"`
	StartupSeconds float64 `json:"startup_seconds"`
	SampleSeconds  float64 `json:"sample_seconds"`
	MinimumCosine  float64 `json:"minimum_cosine"`
	Equivalent     bool    `json:"equivalent"`
	Error          string  `json:"error,omitempty"`
}

type EmbeddingBenchmark struct {
	Architecture string           `json:"go_arch"`
	Samples      int              `json:"samples"`
	Chunks       int              `json:"chunks"`
	GoSeconds    float64          `json:"go_analysis_seconds"`
	Trials       []EmbeddingTrial `json:"trials"`
	Fastest      EmbeddingTrial   `json:"fastest_equivalent_trial"`
}

func BenchmarkEmbeddings(ctx context.Context, c Config, samples int) (EmbeddingBenchmark, error) {
	report := EmbeddingBenchmark{Architecture: runtime.GOARCH}
	if err := c.Defaults(); err != nil {
		return report, err
	}
	if samples < 8 || samples > 256 {
		return report, fmt.Errorf("benchmark samples must be 8..256")
	}
	if len(c.EmbeddingCommand) < 2 || filepath.Base(c.EmbeddingCommand[1]) != "worker.py" {
		return report, fmt.Errorf("benchmark requires the bundled runtime/worker.py in embedding_command")
	}
	timeout := time.Duration(c.OperationSeconds) * time.Second
	start := time.Now()
	loadCtx, cancel := context.WithTimeout(ctx, timeout)
	files, fp, err := readFiles(loadCtx, c)
	if err != nil {
		cancel()
		return report, err
	}
	s, err := analyze(loadCtx, c, files, fp)
	cancel()
	if err != nil {
		return report, err
	}
	report.GoSeconds = time.Since(start).Seconds()
	report.Chunks = len(s.Chunks)
	seen := map[string]bool{}
	var unique []string
	for _, chunk := range s.Chunks {
		if !seen[chunk.Hash] {
			unique = append(unique, chunk.Input)
			seen[chunk.Hash] = true
		}
	}
	if len(unique) == 0 {
		return report, fmt.Errorf("no indexed chunks to benchmark")
	}
	sort.SliceStable(unique, func(i, j int) bool { return len(unique[i]) < len(unique[j]) })
	report.Samples = min(samples, len(unique))
	texts := make([]string, report.Samples)
	for i := range texts {
		texts[i] = unique[i*len(unique)/len(texts)]
	}
	var baseline EmbeddingResult
	for i, trial := range []EmbeddingTrial{
		{Threads: 2, BatchSize: 8, WorkerBatch: 4},
		{Threads: 4, BatchSize: 8, WorkerBatch: 4},
		{Threads: 4, BatchSize: 16, WorkerBatch: 8},
		{Threads: 8, BatchSize: 16, WorkerBatch: 8},
	} {
		cfg := c
		cfg.EmbeddingCommand = append(append([]string(nil), c.EmbeddingCommand...),
			"--threads", strconv.Itoa(trial.Threads), "--batch-size", strconv.Itoa(trial.WorkerBatch))
		result, err := measureEmbeddings(ctx, cfg, texts, &trial)
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if i == 0 {
			if err != nil {
				return report, fmt.Errorf("baseline embedding trial: %w", err)
			}
			baseline = result
		}
		if err != nil {
			trial.Error = err.Error()
		} else {
			trial.MinimumCosine, trial.Equivalent = compareEmbeddings(baseline, result)
			if trial.Equivalent && (!report.Fastest.Equivalent || trial.SampleSeconds < report.Fastest.SampleSeconds) {
				report.Fastest = trial
			}
		}
		report.Trials = append(report.Trials, trial)
	}
	return report, nil
}

func measureEmbeddings(ctx context.Context, c Config, texts []string, trial *EmbeddingTrial) (EmbeddingResult, error) {
	provider := NewProcessEmbedder(c)
	defer provider.Close()
	timeout := time.Duration(c.OperationSeconds) * time.Second
	start := time.Now()
	warm, cancel := context.WithTimeout(ctx, timeout)
	_, err := provider.Embed(warm, "document", texts[:1])
	cancel()
	trial.StartupSeconds = time.Since(start).Seconds()
	var result EmbeddingResult
	if err != nil {
		return result, err
	}
	start = time.Now()
	for i := 0; i < len(texts); i += trial.BatchSize {
		batch, cancel := context.WithTimeout(ctx, timeout)
		res, err := provider.Embed(batch, "document", texts[i:min(i+trial.BatchSize, len(texts))])
		cancel()
		if err != nil {
			return result, err
		}
		result.Vectors = append(result.Vectors, res.Vectors...)
		result.Tokens = append(result.Tokens, res.Tokens...)
		result.Windows = append(result.Windows, res.Windows...)
	}
	trial.SampleSeconds = time.Since(start).Seconds()
	return result, nil
}

func compareEmbeddings(a, b EmbeddingResult) (float64, bool) {
	minimum := 1.0
	for i, v := range a.Vectors {
		if a.Tokens[i] != b.Tokens[i] || a.Windows[i] != b.Windows[i] {
			return 0, false
		}
		var dot, aa, bb float64
		for j, x := range v {
			u, w := float64(x), float64(b.Vectors[i][j])
			dot += u * w
			aa += u * u
			bb += w * w
		}
		minimum = min(minimum, dot/math.Sqrt(aa*bb))
	}
	return minimum, minimum >= 0.99999
}
