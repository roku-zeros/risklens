package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

type Reranker interface {
	Score(context.Context, string, []string) ([]float64, error)
	Close() error
}
type processReranker struct{ worker *processEmbedder }

func newReranker(c Config) Reranker {
	c.EmbeddingCommand = c.RerankCommand
	c.ModelKey = c.RerankModelKey
	c.Dimension = 1
	return &processReranker{&processEmbedder{cfg: c}}
}
func (p *processReranker) Close() error { return p.worker.Close() }
func (p *processReranker) Score(ctx context.Context, query string, docs []string) ([]float64, error) {
	w := p.worker
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd == nil {
		if err := w.start(ctx); err != nil {
			return nil, err
		}
	}
	finished := make(chan struct{})
	defer close(finished)
	process := w.cmd.Process
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-finished:
		}
	}()
	b, _ := json.Marshal(map[string]any{"model_key": w.cfg.ModelKey, "query": query, "documents": docs})
	if _, err := w.in.Write(append(b, '\n')); err != nil {
		w.closeLocked()
		return nil, err
	}
	var result struct {
		Key    string    `json:"model_key"`
		Scores []float64 `json:"scores"`
		Error  string    `json:"error"`
	}
	if err := w.read(ctx, &result); err != nil {
		w.closeLocked()
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("reranker: %s", result.Error)
	}
	if result.Key != w.cfg.ModelKey || len(result.Scores) != len(docs) {
		return nil, fmt.Errorf("incompatible reranker response")
	}
	for _, s := range result.Scores {
		if math.IsNaN(s) || math.IsInf(s, 0) {
			return nil, fmt.Errorf("non-finite reranker score")
		}
	}
	return result.Scores, nil
}
func (e *Engine) rerankHits(ctx context.Context, a Args, hits []Hit) ([]Hit, string) {
	if e.rerank == nil || a.Mode != "hybrid" || (a.Rerank != nil && !*a.Rerank) || len(hits) == 0 {
		return hits, ""
	}
	n := min(e.C.RerankCandidates, len(hits))
	scores := make([]float64, n)
	keys := make([]string, n)
	missing := []int{}
	docs := []string{}
	queryHash := hash([]byte(a.Query))
	e.mu.RLock()
	for i, h := range hits[:n] {
		keys[i] = e.C.RerankModelKey + ":" + queryHash + ":" + h.Chunk.Hash
		if value, ok := e.rerankCache[keys[i]]; ok {
			scores[i] = value
		} else {
			missing = append(missing, i)
			docs = append(docs, h.Chunk.Input)
		}
	}
	e.mu.RUnlock()
	if len(missing) > 0 {
		rctx, cancel := context.WithTimeout(ctx, time.Duration(e.C.OperationSeconds)*time.Second)
		values, err := e.rerank.Score(rctx, a.Query, docs)
		cancel()
		if err != nil {
			return hits, "reranker unavailable; RRF retained: " + err.Error()
		}
		if len(values) != len(missing) {
			return hits, "reranker invalid count; RRF retained"
		}
		e.mu.Lock()
		if e.rerankCache == nil {
			e.rerankCache = map[string]float64{}
		}
		for j, i := range missing {
			scores[i] = values[j]
			if _, exists := e.rerankCache[keys[i]]; !exists {
				e.rerankOrder = append(e.rerankOrder, keys[i])
			}
			e.rerankCache[keys[i]] = values[j]
		}
		for len(e.rerankOrder) > 2048 {
			delete(e.rerankCache, e.rerankOrder[0])
			e.rerankOrder = e.rerankOrder[1:]
		}
		e.mu.Unlock()
	}
	for i, score := range scores {
		x := score
		hits[i].RerankScore = &x
		hits[i].RerankModel = e.C.RerankModelKey
	}
	sort.SliceStable(hits[:n], func(i, j int) bool { return *hits[i].RerankScore > *hits[j].RerankScore })
	return hits, ""
}
