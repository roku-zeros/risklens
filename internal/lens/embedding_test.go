package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEmbeddingHelper(t *testing.T) {
	if os.Getenv("RISKLENS_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "bad" {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "model_key": "wrong", "dimension": 384})
		os.Exit(0)
	}
	if mode == "ready-hang" {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "model_key": ModelKey, "dimension": 384})
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if mode == "stderr" {
		fmt.Fprintln(os.Stderr, "missing model weights")
		os.Exit(2)
	}
	if mode == "hang" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}
func TestProcessRevisionAndCancellation(t *testing.T) {
	t.Setenv("RISKLENS_HELPER", "1")
	c := Config{Root: t.TempDir()}
	if err := c.Defaults(); err != nil {
		t.Fatal(err)
	}
	c.EmbeddingCommand = []string{os.Args[0], "-test.run=TestEmbeddingHelper", "--", "bad"}
	p := NewProcessEmbedder(c)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.Embed(ctx, "query", []string{"x"}); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("wrong revision: %v", err)
	}
	c.EmbeddingCommand[len(c.EmbeddingCommand)-1] = "hang"
	p = NewProcessEmbedder(c)
	defer p.Close()
	short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := p.Embed(short, "query", []string{"x"}); err == nil {
		t.Fatal("hanging worker did not cancel")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation did not kill worker promptly")
	}
}

func TestWorkerBlockedWriteAndDiagnosticTail(t *testing.T) {
	t.Setenv("RISKLENS_HELPER", "1")
	c := Config{Root: t.TempDir()}
	if err := c.Defaults(); err != nil {
		t.Fatal(err)
	}
	c.EmbeddingCommand = []string{os.Args[0], "-test.run=TestEmbeddingHelper", "--", "ready-hang"}
	p := NewProcessEmbedder(c)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := p.Embed(ctx, "document", []string{strings.Repeat("x", 2<<20)})
	p.Close()
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("blocked write did not cancel", err)
	}
	c.EmbeddingCommand[len(c.EmbeddingCommand)-1] = "stderr"
	p = NewProcessEmbedder(c)
	defer p.Close()
	ctx2, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if _, err = p.Embed(ctx2, "query", []string{"x"}); err == nil || !strings.Contains(err.Error(), "missing model weights") {
		t.Fatal("startup diagnostics lost", err)
	}
}
