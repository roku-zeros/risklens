package lens

import (
	"context"
	"fmt"
)

// Bounded process-local cache: never reuse vectors across model/preparation keys.
func (e *Engine) embedQuery(ctx context.Context, query string) (result EmbeddingResult, err error) {
	stage := map[string]any{"hit": false, "model_key": e.C.ModelKey, "executed": false}
	defer func() {
		if err != nil {
			stage["error"] = err.Error()
		}
		recordStage(ctx, "query_embedding_cache", stage)
	}()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	key := e.C.ModelKey + fmt.Sprintf(":%d:", e.C.Dimension) + hash([]byte(query))
	e.mu.RLock()
	v, ok := e.queryCache[key]
	e.mu.RUnlock()
	if ok {
		stage["hit"] = true
		return EmbeddingResult{Vectors: [][]float32{v}}, nil
	}
	result, err = e.embed.Embed(ctx, "query", []string{query})
	if err != nil {
		return result, err
	}
	if len(result.Vectors) != 1 {
		return result, fmt.Errorf("invalid query vector count")
	}
	if err = validVector(result.Vectors[0], e.C.Dimension); err != nil {
		return result, err
	}
	stage["executed"] = true
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.queryCache == nil {
		e.queryCache = map[string][]float32{}
	}
	if _, ok = e.queryCache[key]; !ok {
		e.queryOrder = append(e.queryOrder, key)
	}
	e.queryCache[key] = append([]float32{}, result.Vectors[0]...)
	for len(e.queryOrder) > 256 {
		delete(e.queryCache, e.queryOrder[0])
		e.queryOrder = e.queryOrder[1:]
	}
	return result, nil
}
