package lens

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func makeChunks(s *Snapshot, max int, model string) {
	syms := make([]*Symbol, 0, len(s.Symbols))
	for _, v := range s.Symbols {
		if v.Kind == "function" || v.Kind == "method" || v.Kind == "type" || v.Kind == "constant" || v.Kind == "variable" {
			syms = append(syms, v)
		}
	}
	sort.Slice(syms, func(i, j int) bool {
		a, b := syms[i], syms[j]
		if a.Position.File != b.Position.File {
			return a.Position.File < b.Position.File
		}
		if a.start != b.start {
			return a.start < b.start
		}
		return a.ID < b.ID
	})
	seen := map[string]bool{}
	fileBoundaries := map[string][]int{}
	for name, b := range s.Files {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		fs := token.NewFileSet()
		f, _ := parser.ParseFile(fs, name, b, parser.ParseComments|parser.AllErrors)
		if f == nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				return true
			}
			switch n.(type) {
			case ast.Stmt, *ast.Field, *ast.ValueSpec:
				off := fs.Position(n.End()).Offset
				if off > 0 && off <= len(b) {
					fileBoundaries[name] = append(fileBoundaries[name], off)
				}
			}
			return true
		})
		sort.Ints(fileBoundaries[name])
	}
	for _, sym := range syms {
		// Local declarations already occur inside their containing function chunk.
		nested := false
		for _, parent := range syms {
			if (parent.Kind == "function" || parent.Kind == "method" || parent.Kind == "type") && parent.Position.File == sym.Position.File && parent.start < sym.start && parent.end >= sym.end {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		file := sym.Position.File
		b := s.Files[file]
		if sym.end > len(b) {
			continue
		}
		key := fmt.Sprintf("%s:%d:%d", file, sym.start, sym.end)
		if seen[key] {
			continue
		}
		seen[key] = true
		boundaries := []int{sym.start, sym.end}
		for _, off := range fileBoundaries[file] {
			if off > sym.start && off < sym.end {
				boundaries = append(boundaries, off)
			}
		}
		sort.Ints(boundaries)
		start := sym.start
		for start < sym.end {
			end := sym.end
			if end-start > max {
				end = 0
				for _, v := range boundaries {
					if v > start && v-start <= max {
						end = v
					}
				}
				if end == 0 {
					end = start + max
					for end > start && !utf8.RuneStart(b[end]) {
						end--
					}
					if j := strings.LastIndex(string(b[start:end]), "\n"); j > max/3 {
						end = start + j + 1
					}
				}
			}
			code := string(b[start:end])
			line := 1 + strings.Count(string(b[:start]), "\n")
			last := line + strings.Count(code, "\n")
			input := sym.Package + "\n" + sym.Name + "\n" + sym.Signature + "\n" + sym.Comment + "\n" + code
			c := &Chunk{Symbol: sym.ID, Package: sym.Package, File: file, Start: line, End: last, Hash: hash([]byte(input)), Snapshot: s.ID, Model: model, Code: code, Input: input, Test: sym.Test, start: start, end: end}
			c.ID = hash([]byte(fmt.Sprintf("%s:%d:%d:%s", file, start, end, c.Hash)))[:24]
			s.Chunks = append(s.Chunks, c)
			start = end
		}
	}

	// Index every remaining non-whitespace source span without duplicating declaration code.
	fileSyms := []*Symbol{}
	for _, v := range s.Symbols {
		if v.Kind == "file" {
			fileSyms = append(fileSyms, v)
		}
	}
	sort.Slice(fileSyms, func(i, j int) bool { return fileSyms[i].ID < fileSyms[j].ID })
	for _, sym := range fileSyms {
		b := s.Files[sym.Position.File]
		ranges := [][2]int{}
		for _, c := range s.Chunks {
			if c.File == sym.Position.File {
				ranges = append(ranges, [2]int{c.start, c.end})
			}
		}
		sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
		gaps := [][2]int{}
		last := 0
		for _, r := range ranges {
			if r[0] > last {
				gaps = append(gaps, [2]int{last, r[0]})
			}
			last = maxInt(last, r[1])
		}
		if last < len(b) {
			gaps = append(gaps, [2]int{last, len(b)})
		}
		for _, gap := range gaps {
			start := gap[0]
			for start < gap[1] {
				end := minInt(gap[1], start+max)
				for end > start && end < len(b) && !utf8.RuneStart(b[end]) {
					end--
				}
				code := string(b[start:end])
				if strings.TrimSpace(code) != "" {
					input := sym.Package + "\nfile context\n" + strings.TrimSpace(code)
					c := &Chunk{Symbol: sym.ID, Package: sym.Package, File: sym.Position.File, Start: 1 + strings.Count(string(b[:start]), "\n"), End: 1 + strings.Count(string(b[:end]), "\n"), Hash: hash([]byte(input)), Snapshot: s.ID, Model: model, Code: code, Input: input, Test: sym.Test, start: start, end: end}
					c.ID = hash([]byte(fmt.Sprintf("%s:%d:%d:%s", c.File, start, end, c.Hash)))[:24]
					s.Chunks = append(s.Chunks, c)
				}
				start = end
			}
		}
	}
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func terms(s string) []string {
	// Preserve exact identifiers while also indexing CamelCase components.
	var b strings.Builder
	var prev rune
	for _, r := range s {
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
		prev = r
	}
	words := strings.FieldsFunc(strings.ToLower(s+" "+b.String()), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	out := []string{}
	seen := map[string]bool{}
	for _, w := range words {
		if !seen[w] {
			out = append(out, w)
			seen[w] = true
		}
	}
	return out
}

type TextIndex struct {
	Postings map[string]map[int]int
	Length   []int
	Average  float64
}

func NewTextIndex(cs []*Chunk) *TextIndex {
	t := &TextIndex{Postings: map[string]map[int]int{}, Length: make([]int, len(cs))}
	for i, c := range cs {
		for _, w := range terms(c.Input) {
			if t.Postings[w] == nil {
				t.Postings[w] = map[int]int{}
			}
			t.Postings[w][i]++
			t.Length[i]++
		}
		t.Average += float64(t.Length[i])
	}
	if len(cs) > 0 {
		t.Average /= float64(len(cs))
	}
	return t
}

type rank struct {
	I     int
	Score float64
}

func sortedRanks(m map[int]float64) []rank {
	out := []rank{}
	for i, v := range m {
		out = append(out, rank{i, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].I < out[j].I
		}
		return out[i].Score > out[j].Score
	})
	return out
}
func (t *TextIndex) Search(q string) []rank {
	scores := map[int]float64{}
	for _, w := range terms(q) {
		p := t.Postings[w]
		idf := math.Log(1 + (float64(len(t.Length)-len(p))+.5)/(float64(len(p))+.5))
		for i, tf := range p {
			f := float64(tf)
			avg := math.Max(t.Average, 1)
			scores[i] += idf * (f * 2.2) / (f + 1.2*(.25+.75*float64(t.Length[i])/avg))
		}
	}
	return sortedRanks(scores)
}
func symbolRanks(s *Snapshot, q string) []rank {
	m := map[int]float64{}
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	ts := terms(q)
	for i, c := range s.Chunks {
		sym := s.Symbols[c.Symbol]
		score := 0.
		if strings.EqualFold(sym.Name, q) {
			score += 100
		}
		if strings.Contains(strings.ToLower(sym.Name), q) {
			score += 10
		}
		txt := strings.ToLower(sym.Name + " " + sym.Signature + " " + sym.Package)
		for _, t := range ts {
			if strings.Contains(txt, t) {
				score++
			}
		}
		if score > 0 {
			m[i] = score
		}
	}
	return sortedRanks(m)
}
func match(s *Symbol, a Args) bool {
	if a.IncludeTests != nil && !*a.IncludeTests && s.Test {
		return false
	}
	return (a.Path == "" || strings.HasPrefix(s.Position.File, a.Path)) && (a.Package == "" || s.Package == a.Package) && (a.Kind == "" || s.Kind == a.Kind)
}

type Hit struct {
	Chunk    *Chunk         `json:"chunk"`
	Score    float64        `json:"rrf_score"`
	Channels []string       `json:"channels"`
	Ranks    map[string]int `json:"ranks"`
}

func fuse(s *Snapshot, a Args, lists map[string][]rank) []Hit {
	hits := map[int]*Hit{}
	for _, name := range []string{"symbol", "text", "vector"} {
		list := lists[name]
		r := 0
		for _, v := range list {
			c := s.Chunks[v.I]
			if !match(s.Symbols[c.Symbol], a) {
				continue
			}
			r++
			if r > 1000 {
				break
			}
			h := hits[v.I]
			if h == nil {
				h = &Hit{Chunk: c, Ranks: map[string]int{}}
				hits[v.I] = h
			}
			h.Score += 1 / (60 + float64(r))
			h.Channels = append(h.Channels, name)
			h.Ranks[name] = r
		}
	}
	out := []Hit{}
	for _, v := range hits {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Chunk.ID < out[j].Chunk.ID
		}
		return out[i].Score > out[j].Score
	})
	return out
}
