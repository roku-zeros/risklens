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
	stage := map[string]any{"requested": a.Mode == "hybrid" && (a.Rerank == nil || *a.Rerank), "explicitly_requested": a.Rerank != nil && *a.Rerank, "configured": e.rerank != nil, "attempted": false, "executed": false, "model_key": e.C.RerankModelKey, "scored_items": 0, "cache_hits": 0, "reranked_items": 0, "status": "disabled"}
	defer func() { recordStage(ctx, "reranker", stage) }()
	if a.Mode != "hybrid" {
		stage["status"] = "not_applicable"
		return hits, ""
	}
	if a.Rerank != nil && !*a.Rerank {
		stage["reason"] = "disabled_by_request"
		return hits, ""
	}
	if e.rerank == nil {
		stage["reason"] = "not_configured"
		if a.Rerank != nil && *a.Rerank {
			return hits, "reranker explicitly requested but not configured; RRF retained"
		}
		return hits, ""
	}
	if len(hits) == 0 {
		stage["status"] = "empty"
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
	stage["candidate_items"] = n
	stage["cache_hits"] = n - len(missing)
	if len(missing) > 0 {
		stage["attempted"] = true
		rctx, cancel := context.WithTimeout(ctx, time.Duration(e.C.OperationSeconds)*time.Second)
		values, err := e.rerank.Score(rctx, a.Query, docs)
		cancel()
		if err != nil {
			stage["status"] = "fallback"
			stage["fallback_reason"] = err.Error()
			return hits, "reranker unavailable; RRF retained: " + err.Error()
		}
		if len(values) != len(missing) {
			stage["status"] = "fallback"
			stage["fallback_reason"] = "invalid score count"
			return hits, "reranker invalid count; RRF retained"
		}
		stage["executed"] = true
		stage["scored_items"] = len(missing)
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
	stage["reranked_items"] = n
	stage["status"] = "completed"
	if len(missing) == 0 {
		stage["status"] = "cache_hit"
	}
	for i, score := range scores {
		x := score
		hits[i].RerankScore = &x
		hits[i].RerankModel = e.C.RerankModelKey
	}
	sort.SliceStable(hits[:n], func(i, j int) bool { return *hits[i].RerankScore > *hits[j].RerankScore })
	return hits, ""
}
