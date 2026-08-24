package filetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"time"

	fileop "github.com/icediceice/light-tools/internal/file"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/payload"
	"github.com/icediceice/light-tools/internal/portable"
	"github.com/icediceice/light-tools/internal/readcache"
	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/snapshot"
	"github.com/icediceice/light-tools/internal/telemetry"
)

// spillStore is the subset of the shared spill store filetool needs. Declaring
// it here (rather than importing the bash package) keeps the dependency
// pointing inward, while main.go passes the SAME store both tools use — so a
// spill_id from light_file is readable through light_bash read_block.
type spillStore interface {
	Store(data []byte) (string, error)
}

type Options struct {
	Confiner     *security.Confiner
	SnapshotRoot string
	// Vault lets the caller supply the SAME snapshot store light_bash captures
	// into, so a capture_id minted there resolves here. Nil falls back to a
	// vault rooted at SnapshotRoot.
	Vault  *snapshot.Vault
	Spills spillStore
	// Recorder receives local-only savings aggregates. Nil records nothing.
	Recorder telemetry.Recorder
	// DefaultEpoch scopes the read-dedup ledger for requests that do not carry
	// their own context_epoch. Dedup used to be off unless the CLIENT thought to
	// send an epoch, which made a headline saving opt-in behind a parameter no
	// model knows to invent. main mints one per process; Serve is a single-client
	// stdio loop, so process lifetime is connection lifetime. Empty keeps the old
	// client-gated behaviour.
	DefaultEpoch string
}

type Handler struct {
	confiner  *security.Confiner
	vault     *snapshot.Vault
	cache     *readcache.Ledger
	assembler *payload.Assembler
	spills    spillStore
	recorder  telemetry.Recorder
	// defaultEpoch is used when a request omits context_epoch. See Options.
	defaultEpoch string
}

type Item struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Name   string `json:"name,omitempty"`
}

type Request struct {
	Verb            string            `json:"verb"`
	Path            string            `json:"path,omitempty"`
	Target          string            `json:"target,omitempty"`
	From            string            `json:"from,omitempty"`
	To              string            `json:"to,omitempty"`
	Payload         string            `json:"payload,omitempty"`
	Patch           string            `json:"patch,omitempty"`
	PatchPath       string            `json:"patch_path,omitempty"`
	Fuzz            int               `json:"fuzz,omitempty"`
	Spans           []fileop.EditSpan `json:"spans,omitempty"`
	Content         *string           `json:"content,omitempty"`
	NewString       *string           `json:"new_string,omitempty"`
	Find            *string           `json:"find,omitempty"`
	Replace         *string           `json:"replace,omitempty"`
	StartLine       int               `json:"start_line,omitempty"`
	EndLine         int               `json:"end_line,omitempty"`
	StartGuard      string            `json:"start_guard,omitempty"`
	EndGuard        string            `json:"end_guard,omitempty"`
	Offset          int               `json:"offset,omitempty"`
	Limit           int               `json:"limit,omitempty"`
	Cursor          string            `json:"cursor,omitempty"`
	Name            string            `json:"name,omitempty"`
	Symbol          string            `json:"symbol,omitempty"`
	Pattern         string            `json:"pattern,omitempty"`
	Context         int               `json:"context,omitempty"`
	A               string            `json:"a,omitempty"`
	B               string            `json:"b,omitempty"`
	DiffContext     int               `json:"diff_context,omitempty"`
	All             bool              `json:"all,omitempty"`
	Count           int               `json:"count,omitempty"`
	Regex           bool              `json:"regex,omitempty"`
	DryRun          bool              `json:"dry_run,omitempty"`
	Overwrite       bool              `json:"overwrite,omitempty"`
	AllowUnbalanced bool              `json:"allow_unbalanced,omitempty"`
	ExpectedSHA     string            `json:"expected_sha,omitempty"`
	Version         int               `json:"version,omitempty"`
	Force           bool              `json:"force,omitempty"`
	ContextEpoch    string            `json:"context_epoch,omitempty"`
	// CaptureID addresses a whole light_bash capture rather than one path.
	CaptureID string `json:"capture_id,omitempty"`
	Items           []Item            `json:"items,omitempty"`
	Reads           []Item            `json:"reads,omitempty"`
}

func New(options Options) (*Handler, error) {
	if options.Confiner == nil {
		return nil, fmt.Errorf("path confiner is required")
	}
	if options.SnapshotRoot == "" {
		return nil, fmt.Errorf("snapshot root is required")
	}
	vault := options.Vault
	if vault == nil {
		vault = snapshot.New(options.SnapshotRoot)
	}
	return &Handler{
		confiner: options.Confiner, vault: vault,
		cache: readcache.New(10*time.Minute, 512), assembler: payload.NewAssembler(),
		spills: options.Spills, recorder: options.Recorder,
		defaultEpoch: options.DefaultEpoch,
	}, nil
}

// observe runs one recorder callback behind a panic boundary, mirroring the
// mcp server's helper: savings recording must never be able to alter or fail a
// tool result. A nil recorder records nothing.
func (h *Handler) observe(record func(telemetry.Recorder)) {
	defer func() { _ = recover() }()
	if h.recorder == nil {
		return
	}
	record(h.recorder)
}

// carriedBytes is the wire payload one mutation transmits for its file: the
// string fields its verb actually carries, nothing more.
func carriedBytes(mutation fileop.Mutation) int {
	total := 0
	if mutation.Content != nil {
		total += len(*mutation.Content)
	}
	if mutation.NewString != nil {
		total += len(*mutation.NewString)
	}
	if mutation.Find != nil {
		total += len(*mutation.Find)
	}
	if mutation.Replace != nil {
		total += len(*mutation.Replace)
	}
	for _, span := range mutation.Spans {
		total += len(span.NewString)
	}
	return total
}

// recordWriteSavings credits one COMMIT with the bytes a full rewrite would
// have had to carry to reach the same on-disk state, minus the payload this
// call actually transmitted. Once per commit, never per assembled mutation.
func (h *Handler) recordWriteSavings(carried int, postimage []byte) {
	if saved := len(postimage) - carried; saved > 0 {
		h.observe(func(recorder telemetry.Recorder) { recorder.RecordWriteBytes(saved) })
	}
}

// noReadDedup is the operator kill switch, mirroring the DO_NOT_TRACK /
// LIGHT_NO_TELEMETRY opt-out already used for telemetry. It exists because this
// changes a default for every existing client, and an operator needs a way back
// that is not a version downgrade.
const noReadDedup = "LIGHT_NO_READ_DEDUP"

// resolveEpoch decides the dedup scope for one request. The kill switch wins
// over an explicit client epoch too: an operator turning dedup off means off,
// not "off unless the caller asks for it". ShouldElide already treats an empty
// epoch as disabled, so returning "" is the whole mechanism.
func (h *Handler) resolveEpoch(requested string) string {
	if os.Getenv(noReadDedup) != "" {
		return ""
	}
	if requested != "" {
		return requested
	}
	return h.defaultEpoch
}

func (h *Handler) Portable() portable.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request Request
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, &portable.DiagnosticError{Code: "E_SCHEMA", Message: err.Error()}
		}
		request.normalize()
		request.ContextEpoch = h.resolveEpoch(request.ContextEpoch)
		if request.Payload != "" {
			mutations, partial, err := h.assembler.Assemble(request.Payload)
			if err != nil {
				return nil, err
			}
			if partial != nil {
				return textJSON(partial)
			}
			return h.mutateBatch(ctx, mutations)
		}
		switch request.Verb {
		case "read":
			return h.read(ctx, request)
		case "list":
			return h.list(request)
		case "symbol":
			return h.symbol(request)
		case "outline":
			return h.outline(request)
		case "locate":
			return h.locate(ctx, request)
		case "diff":
			return h.diff(request)
		case "identity":
			return h.identity(request)
		case "vault_list":
			return h.vaultList(request)
		case "write", "edit", "sed", "rename", "rewrite", "vault_restore":
			// A capture spans many paths, so it cannot travel as a per-path
			// Mutation; it is dispatched before the single-path lane.
			if request.Verb == "vault_restore" && request.CaptureID != "" {
				return h.restoreCapture(request)
			}
			return h.mutate(ctx, request.mutation())
		default:
			// Names the closest match AND the whole vocabulary: a verb this far
			// off was refused by the repair pass precisely because guessing
			// would be unsafe, so the diagnostic has to be self-sufficient.
			return nil, &portable.DiagnosticError{Code: "E_VERB", Message: portable.UnknownVerbMessage("light_file", request.Verb)}
		}
	}
}

func (r *Request) normalize() {
	if r.Path == "" {
		r.Path = r.From
	}
	if r.Target == "" {
		r.Target = r.To
	}
	if r.Name == "" {
		r.Name = r.Symbol
	}
	if len(r.Items) == 0 {
		r.Items = r.Reads
	}
}

func (r Request) mutation() fileop.Mutation {
	return fileop.Mutation{
		Verb: fileop.Verb(r.Verb), Path: r.Path, Target: r.Target, Spans: r.Spans,
		Content: r.Content, NewString: r.NewString, Find: r.Find, Replace: r.Replace,
		StartLine: r.StartLine, EndLine: r.EndLine, StartGuard: r.StartGuard, EndGuard: r.EndGuard,
		All: r.All, Count: r.Count, Regex: r.Regex, DryRun: r.DryRun, Overwrite: r.Overwrite,
		AllowUnbalanced: r.AllowUnbalanced, ExpectedSHA: r.ExpectedSHA, Version: r.Version, Force: r.Force,
	}
}

func textJSON(value any) (mcp.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{Content: []mcp.Content{mcp.Text(string(data))}}, nil
}
