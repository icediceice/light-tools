package filetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	fileop "github.com/icediceice/light-tools/internal/file"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/payload"
	"github.com/icediceice/light-tools/internal/portable"
	"github.com/icediceice/light-tools/internal/readcache"
	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/snapshot"
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
	Spills       spillStore
}

type Handler struct {
	confiner  *security.Confiner
	vault     *snapshot.Vault
	cache     *readcache.Ledger
	assembler *payload.Assembler
	spills    spillStore
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
	return &Handler{
		confiner: options.Confiner, vault: snapshot.New(options.SnapshotRoot),
		cache: readcache.New(10*time.Minute, 512), assembler: payload.NewAssembler(),
		spills: options.Spills,
	}, nil
}

func (h *Handler) Portable() portable.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request Request
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, &portable.DiagnosticError{Code: "E_SCHEMA", Message: err.Error()}
		}
		request.normalize()
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
			return h.mutate(ctx, request.mutation())
		default:
			return nil, fmt.Errorf("unsupported light_file verb %q", request.Verb)
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
