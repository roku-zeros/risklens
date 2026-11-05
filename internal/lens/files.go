package lens

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func readFiles(ctx context.Context, c Config) (map[string][]byte, string, error) {
	if _, err := os.Stat(filepath.Join(c.Root, "go.mod")); err != nil {
		return nil, "", fmt.Errorf("root must contain go.mod: %w", err)
	}
	files := map[string][]byte{}
	var size int64
	err := filepath.WalkDir(c.Root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != c.Root && (d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") || path == c.Cache) {
				return filepath.SkipDir
			}
			if path != c.Root {
				if _, e := os.Stat(filepath.Join(path, "go.mod")); e == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, _ := filepath.Rel(c.Root, path)
		rel = filepath.ToSlash(rel)
		if !(strings.HasSuffix(rel, ".go") || rel == "go.mod" || rel == "go.sum") {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source symlink not supported: %s", rel)
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		size += info.Size()
		if len(files) >= c.MaxFiles || size > c.MaxProjectBytes {
			return fmt.Errorf("project exceeds configured file/byte limit")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		files[rel] = b
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := []string{Version, "snapshot-v1", digest([]any{c.ChunkBytes, c.ModelKey, c.Dimension}), digest(c.BuildFlags), digest(baseEnvironment()), os.Getenv("GOOS"), os.Getenv("GOARCH"), os.Getenv("CGO_ENABLED"), os.Getenv("GOEXPERIMENT")}
	for _, n := range names {
		parts = append(parts, n, hash(files[n]))
	}
	return files, digest(parts), nil
}
