package lens

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type EmbeddingResult struct {
	Vectors [][]float32 `json:"vectors"`
	Tokens  []int       `json:"token_counts"`
	Windows []int       `json:"windows"`
}
type Embedder interface {
	Embed(context.Context, string, []string) (EmbeddingResult, error)
	Close() error
}
type processEmbedder struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	cfg  Config
	tail *tailBuffer
}

func NewProcessEmbedder(c Config) Embedder { return &processEmbedder{cfg: c} }
func (p *processEmbedder) closeLocked() error {
	if p.cmd == nil {
		return nil
	}
	p.in.Close()
	p.cmd.Process.Kill()
	e := p.cmd.Wait()
	p.cmd = nil
	return e
}
func (p *processEmbedder) Close() error { p.mu.Lock(); defer p.mu.Unlock(); return p.closeLocked() }
func (p *processEmbedder) start(ctx context.Context) error {
	if len(p.cfg.EmbeddingCommand) == 0 {
		return fmt.Errorf("embedding runtime not configured; set embedding_command after installing model")
	}
	cmd := exec.Command(p.cfg.EmbeddingCommand[0], p.cfg.EmbeddingCommand[1:]...)
	p.tail = &tailBuffer{}
	cmd.Stderr = io.MultiWriter(os.Stderr, p.tail)
	in, e := cmd.StdinPipe()
	if e != nil {
		return e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	p.cmd = cmd
	p.in = in
	p.out = bufio.NewReaderSize(out, 65536)
	var ready struct {
		Ready     bool   `json:"ready"`
		Key       string `json:"model_key"`
		Dimension int    `json:"dimension"`
	}
	if e = p.read(ctx, &ready); e != nil {
		p.closeLocked()
		return fmt.Errorf("embedding worker startup: %w; stderr: %s", e, p.tail.String())
	}
	if !ready.Ready || ready.Key != p.cfg.ModelKey || ready.Dimension != p.cfg.Dimension {
		p.closeLocked()
		return fmt.Errorf("incompatible embedding runtime/model revision or dimension")
	}
	return nil
}
func (p *processEmbedder) read(ctx context.Context, v any) error {
	type reply struct {
		b []byte
		e error
	}
	ch := make(chan reply, 1)
	reader := p.out
	go func() { b, e := readBoundedLine(reader, 32<<20); ch <- reply{b, e} }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		if r.e != nil {
			return r.e
		}
		return json.Unmarshal(r.b, v)
	}
}
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	b := []byte{}
	for {
		part, prefix, e := r.ReadLine()
		if e != nil {
			return nil, e
		}
		if len(b)+len(part) > max {
			return nil, fmt.Errorf("runtime message exceeds limit")
		}
		b = append(b, part...)
		if !prefix {
			return b, nil
		}
	}
}
func (p *processEmbedder) Embed(ctx context.Context, mode string, texts []string) (EmbeddingResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result EmbeddingResult
	if p.cmd == nil {
		if e := p.start(ctx); e != nil {
			return result, e
		}
	}
	if err := ctx.Err(); err != nil {
		p.closeLocked()
		return result, err
	}
	// Cancellation must also interrupt a blocked stdin write, before a response can be read.
	process := p.cmd.Process
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-finished:
		}
	}()
	req := map[string]any{"mode": mode, "texts": texts, "model_key": p.cfg.ModelKey}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, e := p.in.Write(b); e != nil {
		p.closeLocked()
		return result, e
	}
	var r struct {
		EmbeddingResult
		Error     string `json:"error"`
		Key       string `json:"model_key"`
		Dimension int    `json:"dimension"`
	}
	if e := p.read(ctx, &r); e != nil {
		p.closeLocked()
		return result, e
	}
	if r.Error != "" {
		return result, fmt.Errorf("embedding runtime: %s", r.Error)
	}
	if r.Key != p.cfg.ModelKey || r.Dimension != p.cfg.Dimension {
		return result, fmt.Errorf("incompatible embedding response")
	}
	if len(r.Vectors) != len(texts) || len(r.Tokens) != len(texts) || len(r.Windows) != len(texts) {
		return result, fmt.Errorf("invalid embedding batch length")
	}
	for _, v := range r.Vectors {
		if e := validVector(v, p.cfg.Dimension); e != nil {
			return result, e
		}
	}
	return r.EmbeddingResult, nil
}
func validVector(v []float32, d int) error {
	if len(v) != d {
		return fmt.Errorf("incompatible vector dimension")
	}
	n := 0.
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("non-finite vector")
		}
		n += f * f
	}
	if math.Abs(n-1) > .05 {
		return fmt.Errorf("embedding must be L2 normalized")
	}
	return nil
}

type VectorRecord struct {
	Vector  []float32 `json:"vector"`
	Tokens  int       `json:"tokens"`
	Windows int       `json:"windows"`
}
type vectorDisk struct {
	Version   int                     `json:"version"`
	Model     string                  `json:"model_key"`
	Dimension int                     `json:"dimension"`
	Snapshot  string                  `json:"index_snapshot"`
	Records   map[string]VectorRecord `json:"records"`
	Chunks    map[string]string       `json:"chunks"`
}

func loadVectors(c Config) (vectorDisk, error) {
	d := vectorDisk{Version: 1, Model: c.ModelKey, Dimension: c.Dimension, Records: map[string]VectorRecord{}, Chunks: map[string]string{}}
	b, e := os.ReadFile(filepath.Join(c.Cache, "vectors.json"))
	if os.IsNotExist(e) {
		return d, nil
	}
	if e != nil {
		return d, e
	}
	var old vectorDisk
	if e = json.Unmarshal(b, &old); e != nil {
		return d, e
	}
	if old.Version != 1 || old.Model != c.ModelKey || old.Dimension != c.Dimension {
		return d, fmt.Errorf("incompatible persisted vector index; rebuild with refresh_index")
	}
	for _, v := range old.Records {
		if e = validVector(v.Vector, c.Dimension); e != nil {
			return d, e
		}
	}
	if old.Records == nil || old.Chunks == nil {
		return d, fmt.Errorf("invalid vector index maps")
	}
	return old, nil
}
func saveVectors(c Config, d vectorDisk) error {
	if e := os.MkdirAll(c.Cache, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(d)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(c.Cache, "vectors-*.tmp")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	target := filepath.Join(c.Cache, "vectors.json")
	if e = os.Rename(name, target); e != nil {
		return fmt.Errorf("replace vector index (ensure a single server owns cache): %w", e)
	}
	return nil
}

// tailBuffer keeps bounded child diagnostics while stderr remains available to the MCP client.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = append([]byte{}, t.b[len(t.b)-4096:]...)
	}
	return n, nil
}
func (t *tailBuffer) String() string { t.mu.Lock(); defer t.mu.Unlock(); return string(t.b) }
