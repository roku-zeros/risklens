package lens

import (
	"go/ast"
	"sort"
	"strings"
)

// PackageSummary contains facts extracted from the active production build.
type PackageSummary struct {
	Name        string    `json:"name"`
	GoDoc       string    `json:"go_doc,omitempty"`
	DocPosition *Position `json:"doc_position,omitempty"`
	Exports     []string  `json:"exports,omitempty"`
	Files       int       `json:"files"`
}

func (p *PackageSummary) addFile(a *analyzer, f *ast.File) {
	p.Files++
	if f.Doc != nil {
		pos := a.position(f.Doc.Pos())
		doc := strings.Join(strings.Fields(f.Doc.Text()), " ")
		if doc != "" && (p.DocPosition == nil || pos.File < p.DocPosition.File) {
			p.GoDoc, p.DocPosition = clip(doc, 640), &pos
		}
	}
	add := func(id *ast.Ident) {
		if id.IsExported() {
			p.Exports = append(p.Exports, id.Name)
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				add(d.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(s.Name)
				case *ast.ValueSpec:
					for _, name := range s.Names {
						add(name)
					}
				}
			}
		}
	}
}

type packageSearch struct {
	chunks    map[string][]int
	all       []int
	docTerms  map[string][]string
	docChunks map[string]int
}

func newPackageSearch(s *Snapshot, profiles map[string]*PackageSummary) *packageSearch {
	idx := &packageSearch{chunks: map[string][]int{}, docTerms: map[string][]string{}, docChunks: map[string]int{}}
	for pkg, summary := range profiles {
		sort.Strings(summary.Exports)
		if len(summary.Exports) > 12 {
			summary.Exports = append([]string(nil), summary.Exports[:12]...)
		}
		id := "package:" + pkg
		node := s.ProjectMap.nodes[id]
		node.Summary = summary
		s.ProjectMap.nodes[id] = node
		for _, term := range terms(summary.GoDoc) {
			if !stopWords[term] {
				idx.docTerms[term] = append(idx.docTerms[term], pkg)
			}
		}
	}
	for i, c := range s.Chunks {
		idx.all = append(idx.all, i)
		idx.chunks[c.Package] = append(idx.chunks[c.Package], i)
		summary := profiles[c.Package]
		if summary == nil || summary.DocPosition == nil {
			continue
		}
		pos := summary.DocPosition
		if pos.File == c.File && c.Start <= pos.Line && pos.Line <= c.End && s.Symbols[c.Symbol].Kind == "file" {
			if _, exists := idx.docChunks[c.Package]; !exists {
				idx.docChunks[c.Package] = i
			}
		}
	}
	return idx
}

func (s *Snapshot) searchChunks(pkg string) []int {
	if s.PackageSearch != nil {
		if pkg != "" {
			return s.PackageSearch.chunks[pkg]
		}
		return s.PackageSearch.all
	}
	var indices []int
	for i, c := range s.Chunks {
		if pkg == "" || c.Package == pkg {
			indices = append(indices, i)
		}
	}
	return indices
}

// Route only when two distinct terms point to a uniquely best GoDoc.
// Keep the ordinary lexical and vector candidate sets available to fusion.
func (p *packageSearch) rank(s *Snapshot, a Args, symbols, text []rank) []rank {
	query := queryTerms(a.Query)
	if len(query) < 2 {
		return nil
	}
	counts := map[string]int{}
	for _, term := range query {
		for _, pkg := range p.docTerms[term] {
			if a.Package == "" || pkg == a.Package {
				counts[pkg]++
			}
		}
	}
	best, high, tied := "", 0, false
	for pkg, count := range counts {
		if count > high {
			best, high, tied = pkg, count, false
		} else if count == high {
			tied = true
		}
	}
	if tied || high < 2 || high < (len(query)+1)/2 {
		return nil
	}
	allowed := func(i int) bool {
		c := s.Chunks[i]
		return c.Package == best && !c.Test && match(s.Symbols[c.Symbol], a)
	}
	var results []rank
	// Prefer a matching declaration and keep the actual GoDoc as a second
	// source anchor. Never guess a representative when there is no local hit.
	for _, list := range [][]rank{symbols, text} {
		for _, item := range list {
			if allowed(item.I) && s.Symbols[s.Chunks[item.I].Symbol].Kind != "file" {
				results = append(results, item)
				break
			}
		}
		if len(results) != 0 {
			break
		}
	}
	if i, ok := p.docChunks[best]; ok && allowed(i) {
		results = append(results, rank{I: i})
	}
	return results
}
