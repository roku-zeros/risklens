package lens

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func readFiles(ctx context.Context, c Config) (map[string][]byte, string, error) {
	return scanFiles(ctx, c, true)
}
func fingerprintFiles(ctx context.Context, c Config) (string, error) {
	_, fp, err := scanFiles(ctx, c, false)
	return fp, err
}
func scanFiles(ctx context.Context, c Config, keepContents bool) (map[string][]byte, string, error) {
	start := time.Now()
	defer recordTime(ctx, "freshness", start)

	if _, err := os.Stat(filepath.Join(c.Root, "go.mod")); err != nil {
		return nil, "", fmt.Errorf("root must contain go.mod: %w", err)
	}
	files := map[string][]byte{}
	hashes := map[string]string{}
	buffer := make([]byte, 64*1024)
	readHashTime := time.Duration(0)
	walkStart := time.Now()
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
		if len(hashes) >= c.MaxFiles || size > c.MaxProjectBytes {
			return fmt.Errorf("project exceeds configured file/byte limit")
		}
		readStart := time.Now()
		defer func() { readHashTime += time.Since(readStart) }()
		if keepContents {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[rel] = b
			hashes[rel] = hash(b)
		} else {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, copyErr := io.CopyBuffer(h, scanReader{ctx, f}, buffer)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			hashes[rel] = hex.EncodeToString(h.Sum(nil))
		}
		return nil
	})
	recordDuration(ctx, "freshness_read_hash", readHashTime)
	recordDuration(ctx, "freshness_walk_metadata", time.Since(walkStart)-readHashTime)
	recordScanStats(ctx, len(hashes), size)
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(hashes))
	for n := range hashes {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := []string{Version, "snapshot-v1", digest([]any{c.ChunkBytes, c.ModelKey, c.Dimension, c.CallAnalysis, c.MaxSSAFunctions, c.MaxSSAInstructions, c.RerankModelKey, c.RerankCandidates}), digest(c.BuildFlags), digest(baseEnvironment()), os.Getenv("GOOS"), os.Getenv("GOARCH"), os.Getenv("CGO_ENABLED"), os.Getenv("GOEXPERIMENT")}
	for _, n := range names {
		parts = append(parts, n, hashes[n])
	}
	return files, digest(parts), nil
}

// Hide os.File.WriteTo so CopyBuffer reuses the bounded buffer and checks cancellation.
type scanReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r scanReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
