package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"risklens/internal/lens"
	"risklens/internal/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "RiskLens:", err)
		os.Exit(1)
	}
}
func run() error {
	clearCache := flag.Bool("clear-cache", false, "remove owned vector files while holding the cache OS lock; then exit")
	config := flag.String("config", "", "JSON configuration file")
	root := flag.String("root", "", "Go module root")
	cache := flag.String("cache", "", "cache directory override")
	tool := flag.String("tool", "", "one-shot tool name; otherwise MCP stdio")
	args := flag.String("args", "{}", "one-shot arguments JSON")
	wait := flag.Bool("wait-vectors", false, "wait for real embeddings before one-shot request")
	schema := flag.Bool("schema", false, "print MCP tool schemas and exit")
	version := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *version {
		fmt.Println("RiskLens", lens.Version)
		return nil
	}
	if *schema {
		return json.NewEncoder(os.Stdout).Encode(mcp.Tools())
	}
	var c lens.Config
	if *config != "" {
		b, e := os.ReadFile(*config)
		if e != nil {
			return e
		}
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&c); e != nil {
			return e
		}
	}
	if *root != "" {
		c.Root = *root
	}
	if c.Root == "" {
		return fmt.Errorf("--root or config.root is required")
	}
	if *cache != "" {
		c.Cache = *cache
	}
	if *clearCache {
		removed, err := lens.ClearCache(c)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"removed": removed})
	}
	e, err := lens.New(c, nil)
	if err != nil {
		return err
	}
	defer e.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *tool != "" {
		op, stop := context.WithTimeout(ctx, time.Duration(e.C.OperationSeconds)*time.Second)
		defer stop()
		if err = e.Refresh(op); err != nil {
			return err
		}
		if *wait {
			if err = e.WaitVectors(op); err != nil {
				return err
			}
		}
		var result any
		if *tool == "project_status" || *tool == "index_status" || *tool == "refresh_index" {
			result = e.Status(op)
		} else {
			a, er := mcp.Validate(*tool, json.RawMessage(*args))
			if er != nil {
				return er
			}
			result, err = e.Call(op, *tool, a)
			if err != nil {
				return err
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	e.StartRefresh()
	s := mcp.Server{Engine: e}
	return s.Serve(ctx, os.Stdin, os.Stdout)
}
