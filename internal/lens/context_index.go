package lens

import "sort"

type contextIndex struct {
	byChunk  map[string]*Chunk
	bySymbol map[string][]*Chunk
	adjacent map[string][]int
}

func newContextIndex(s *Snapshot) *contextIndex {
	idx := &contextIndex{
		byChunk: map[string]*Chunk{}, bySymbol: map[string][]*Chunk{},
		adjacent: map[string][]int{},
	}
	for _, c := range s.Chunks {
		idx.byChunk[c.ID] = c
		idx.bySymbol[c.Symbol] = append(idx.bySymbol[c.Symbol], c)
	}
	for _, chunks := range idx.bySymbol {
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].start < chunks[j].start })
	}
	for i, rel := range s.Relations {
		if rel.From != "" && (rel.Kind != "reference" || rel.From == rel.Symbol) {
			idx.adjacent[rel.From] = append(idx.adjacent[rel.From], i)
		}
		if rel.Symbol != "" && rel.Symbol != rel.From {
			idx.adjacent[rel.Symbol] = append(idx.adjacent[rel.Symbol], i)
		}
	}
	return idx
}
