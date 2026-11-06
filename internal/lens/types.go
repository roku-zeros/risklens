package lens

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

const Version = "0.4.0"
const ModelRevision = "614241f622f53c4eeff9890bdc4f31cfecc418b3"
const ModelKey = "intfloat/multilingual-e5-small@" + ModelRevision + ":fp32:384:mask-mean:l2:e5-prefix:window510:v2"

type Config struct {
	RerankCommand      []string `json:"reranker_command"`
	RerankModelKey     string   `json:"reranker_model_key"`
	RerankCandidates   int      `json:"rerank_candidates"`
	MaxSSAInstructions int      `json:"max_ssa_instructions"`
	CallAnalysis       string   `json:"call_analysis"`
	MaxSSAFunctions    int      `json:"max_ssa_functions"`
	Root               string   `json:"root"`
	Cache              string   `json:"cache"`
	EmbeddingCommand   []string `json:"embedding_command"`
	ModelKey           string   `json:"model_key"`
	Dimension          int      `json:"dimension"`
	BatchSize          int      `json:"batch_size"`
	ChunkBytes         int      `json:"chunk_bytes"`
	MaxResults         int      `json:"max_results"`
	MaxSourceBytes     int      `json:"max_source_bytes"`
	MaxContextBytes    int      `json:"max_context_bytes"`
	MaxFiles           int      `json:"max_files"`
	MaxProjectBytes    int64    `json:"max_project_bytes"`
	OperationSeconds   int      `json:"operation_seconds"`
	BuildFlags         []string `json:"build_flags"`
}

func (c *Config) Defaults() error {
	var err error
	if c.RerankCandidates == 0 {
		c.RerankCandidates = 30
	}
	if c.RerankCandidates < 1 || c.RerankCandidates > 100 {
		return fmt.Errorf("invalid rerank_candidates")
	}
	if len(c.RerankCommand) > 0 && c.RerankModelKey == "" {
		return fmt.Errorf("reranker_model_key required")
	}
	if c.CallAnalysis == "" {
		c.CallAnalysis = "vta"
	}
	if c.MaxSSAInstructions == 0 {
		c.MaxSSAInstructions = 300000
	}
	if c.MaxSSAInstructions < 1 {
		return fmt.Errorf("invalid max_ssa_instructions")
	}
	if c.MaxSSAFunctions == 0 {
		c.MaxSSAFunctions = 20000
	}
	if (c.CallAnalysis != "ast" && c.CallAnalysis != "vta") || c.MaxSSAFunctions < 1 {
		return fmt.Errorf("invalid call analysis configuration")
	}
	c.Root, err = filepath.Abs(c.Root)
	if err != nil {
		return err
	}
	c.Root, err = filepath.EvalSymlinks(c.Root)
	if err != nil {
		return err
	}
	if c.Cache == "" {
		c.Cache = filepath.Join(c.Root, ".risklens")
	}
	c.Cache, err = filepath.Abs(c.Cache)
	if err != nil {
		return err
	}
	if c.ModelKey == "" {
		c.ModelKey = ModelKey
	}
	if c.Dimension == 0 {
		c.Dimension = 384
	}
	if c.BatchSize == 0 {
		c.BatchSize = 8
	}
	if c.ChunkBytes == 0 {
		c.ChunkBytes = 1200
	}
	if c.MaxResults == 0 {
		c.MaxResults = 100
	}
	if c.MaxSourceBytes == 0 {
		c.MaxSourceBytes = 16000
	}
	if c.MaxContextBytes == 0 {
		c.MaxContextBytes = 48000
	}
	if c.MaxFiles == 0 {
		c.MaxFiles = 30000
	}
	if c.MaxProjectBytes == 0 {
		c.MaxProjectBytes = 128 << 20
	}
	if c.OperationSeconds == 0 {
		c.OperationSeconds = 300
	}
	if c.BatchSize < 1 || c.BatchSize > 128 || c.Dimension < 1 || c.Dimension > 4096 || c.ChunkBytes < 128 || c.ChunkBytes > 16000 || c.MaxResults < 1 || c.MaxResults > 1000 || c.MaxFiles < 1 || c.MaxProjectBytes < 1 || c.OperationSeconds < 1 || c.MaxSourceBytes < 128 || c.MaxContextBytes < 128 {
		return fmt.Errorf("invalid resource limits")
	}
	for _, f := range c.BuildFlags {
		if !strings.HasPrefix(f, "-tags=") {
			return fmt.Errorf("only -tags= build flags allowed")
		}
	}
	return nil
}

type Position struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}
type Symbol struct {
	AccessPath []string `json:"access_path,omitempty"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Package    string   `json:"package"`
	Receiver   string   `json:"receiver,omitempty"`
	Signature  string   `json:"signature"`
	Position   Position `json:"position"`
	EndLine    int      `json:"end_line"`
	Test       bool     `json:"test"`
	Code       string   `json:"code,omitempty"`
	Comment    string   `json:"comment,omitempty"`
	start, end int
}
type Relation struct {
	Conditions     []string `json:"conditions,omitempty"`
	Analysis       string   `json:"analysis,omitempty"`
	FlowCandidates []string `json:"flow_candidates,omitempty"`
	UnknownTargets bool     `json:"unknown_targets_possible,omitempty"`
	Symbol         string   `json:"symbol_id,omitempty"`
	From           string   `json:"from_symbol,omitempty"`
	Kind           string   `json:"kind"`
	Resolution     string   `json:"resolution"`
	Position       Position `json:"position"`
	Candidates     []string `json:"candidates,omitempty"`
	Code           string   `json:"code,omitempty"`
}
type Chunk struct {
	Facts      string `json:"static_facts,omitempty"`
	ID         string `json:"chunk_id"`
	Symbol     string `json:"symbol_id"`
	Package    string `json:"package_path"`
	File       string `json:"file_path"`
	Start      int    `json:"start_line"`
	End        int    `json:"end_line"`
	Hash       string `json:"content_hash"`
	Snapshot   string `json:"index_snapshot"`
	Model      string `json:"embedding_model_revision"`
	Code       string `json:"code"`
	Input      string `json:"-"`
	Test       bool   `json:"test"`
	start, end int
}
type Snapshot struct {
	Calls       CallAnalysis           `json:"call_analysis"`
	TypeFields  map[string][]FieldInfo `json:"-"`
	ID          string                 `json:"index_snapshot"`
	Fingerprint string                 `json:"-"`
	Symbols     map[string]*Symbol     `json:"-"`
	Relations   []Relation             `json:"-"`
	Chunks      []*Chunk               `json:"-"`
	Files       map[string][]byte      `json:"-"`
	Diagnostics []string               `json:"diagnostics"`
	Complete    bool                   `json:"complete"`
	Environment map[string]string      `json:"environment"`
	Module      string                 `json:"module"`
	Created     string                 `json:"created"`
	Text        *TextIndex             `json:"-"`
}
type Status struct {
	LastRefresh         map[string]any    `json:"last_refresh,omitempty"`
	Meta                map[string]any    `json:"meta,omitempty"`
	Calls               CallAnalysis      `json:"call_analysis"`
	Version             string            `json:"version"`
	GoHeapReservedBytes uint64            `json:"go_heap_reserved_bytes"`
	GoTotalAllocBytes   uint64            `json:"go_total_alloc_bytes"`
	GoGCCycles          uint32            `json:"go_gc_cycles"`
	FreshnessScope      string            `json:"freshness_scope"`
	GoHeapBytes         uint64            `json:"go_heap_bytes"`
	Snapshot            string            `json:"index_snapshot"`
	State               string            `json:"state"`
	Fresh               bool              `json:"fresh"`
	Loading             bool              `json:"loading"`
	Vectors             string            `json:"vectors"`
	VectorDone          int               `json:"vector_done"`
	VectorTotal         int               `json:"vector_total"`
	CacheHits           int               `json:"cache_hits"`
	LastError           string            `json:"last_error,omitempty"`
	EmbeddingError      string            `json:"embedding_error,omitempty"`
	Symbols             int               `json:"symbols"`
	Chunks              int               `json:"chunks"`
	Files               int               `json:"files"`
	Complete            bool              `json:"complete"`
	Diagnostics         []string          `json:"diagnostics"`
	Environment         map[string]string `json:"environment"`
	Module              string            `json:"module"`
}
type Args struct {
	Rerank            *bool           `json:"rerank"`
	Document          string          `json:"document"`
	Claims            []EvidenceClaim `json:"claims"`
	EntrySymbol       string          `json:"entry_symbol_id"`
	IncludeCandidates bool            `json:"include_candidates"`
	OwnerID           string          `json:"owner_id"`
	Paths             []string        `json:"paths"`
	ExcludePaths      []string        `json:"exclude_paths"`
	DistinctSymbols   *bool           `json:"distinct_symbols"`
	Query             string          `json:"query"`
	Symbol            string          `json:"symbol_id"`
	Symbols           []string        `json:"symbol_ids"`
	Chunks            []string        `json:"chunk_ids"`
	File              string          `json:"file"`
	Start             int             `json:"start_line"`
	End               int             `json:"end_line"`
	Path              string          `json:"path"`
	Package           string          `json:"package"`
	Kind              string          `json:"kind"`
	IncludeTests      *bool           `json:"include_tests"`
	Limit             int             `json:"limit"`
	Cursor            string          `json:"cursor"`
	Mode              string          `json:"mode"`
	Depth             int             `json:"depth"`
	Budget            int             `json:"budget_bytes"`
	Snapshot          string          `json:"index_snapshot"`
}
type Response struct {
	Snapshot    string         `json:"index_snapshot"`
	Fresh       bool           `json:"fresh"`
	Complete    bool           `json:"complete"`
	Items       any            `json:"items"`
	Truncated   bool           `json:"truncated"`
	Next        string         `json:"next_cursor,omitempty"`
	Limitations []string       `json:"limitations,omitempty"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

var Limitations = []string{"Static relationships are not execution order or proof of business correctness.", "Interface/function-variable targets are unresolved or candidates; graph is incomplete.", "Bounded SSA may recover some pointer/parameter/return aliases as candidate field accesses. This is not complete object/value history; whole-struct, reflection, container-element and external effects may be absent.", "Embedding similarity is retrieval relevance, not confidence in behavior. Freshness covers indexed Go files, not documentation claims or the execution order of product stages."}

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func digest(v any) string  { b, _ := json.Marshal(v); return hash(b) }
func baseEnvironment() map[string]string {
	return map[string]string{"GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH, "toolchain": runtime.Version(), "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=readonly"}
}
