package lens

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamingFingerprintMatchesLoadedSnapshot(t *testing.T) {
	root := fixture(t)
	c := Config{Root: root}
	if err := c.Defaults(); err != nil {
		t.Fatal(err)
	}
	files, loaded, err := readFiles(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	streamed, err := fingerprintFiles(context.Background(), c)
	if err != nil || loaded != streamed {
		t.Fatal(loaded, streamed, err)
	}
	// A chunk larger than the stream buffer must not be truncated.
	b := []byte("package strategy\n//" + strings.Repeat("x", 160000) + "\n")
	os.WriteFile(filepath.Join(root, "stream.go"), b, 0600)
	_, loaded, err = readFiles(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	streamed, err = fingerprintFiles(context.Background(), c)
	if err != nil || loaded != streamed {
		t.Fatal("stream differs", err)
	}
	if len(files) == 0 {
		t.Fatal("missing baseline")
	}
	limited := c
	limited.MaxFiles = 1
	if _, err = fingerprintFiles(context.Background(), limited); err == nil {
		t.Fatal("stream scan bypassed file limit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = fingerprintFiles(ctx, c); err == nil {
		t.Fatal("stream scan ignored cancellation")
	}
}
func BenchmarkFreshnessScanAllocation(b *testing.B) {
	root := b.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module bench\ngo 1.26.0\n"), 0600)
	content := []byte("package bench\n//" + strings.Repeat("x", 128*1024) + "\n")
	for i := 0; i < 64; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.go", i)), content, 0600)
	}
	c := Config{Root: root}
	if err := c.Defaults(); err != nil {
		b.Fatal(err)
	}
	b.Run("retain_source_contents", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := readFiles(context.Background(), c); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stream_fingerprint_only", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := fingerprintFiles(context.Background(), c); err != nil {
				b.Fatal(err)
			}
		}
	})
}
