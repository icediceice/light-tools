package filetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	fileop "github.com/icediceice/light-tools/internal/file"

)

func (h *Handler) mutateBatch(ctx context.Context, mutations []fileop.Mutation) (any, error) {
	if len(mutations) == 0 {
		return nil, fmt.Errorf("empty mutation batch")
	}
	type group struct {
		path      string
		mutations []fileop.Mutation
	}
	var groups []*group
	byPath := make(map[string]*group)
	for _, mutation := range mutations {
		if err := mutation.Validate(); err != nil {
			return nil, err // whole payload validates before the first write
		}
		resolved, err := h.resolve(mutation.Path)
		if err != nil {
			return nil, err
		}
		mutation.Path = resolved
		item := byPath[resolved]
		if item == nil {
			item = &group{path: resolved}
			byPath[resolved] = item
			groups = append(groups, item)
		}
		item.mutations = append(item.mutations, mutation)
	}
	for _, item := range groups {
		if len(item.mutations) < 2 {
			continue
		}
		verb := item.mutations[0].Verb
		for _, mutation := range item.mutations[1:] {
			if mutation.Verb != verb || verb != fileop.VerbEdit && verb != fileop.VerbSed {
				return nil, fmt.Errorf("same-path batch %s mixes mutation kinds; use only edit or only sed", item.path)
			}
		}
	}

	files := make([]map[string]any, 0, len(groups))
	for _, item := range groups {
		fileResult := map[string]any{"path": item.path, "verb": item.mutations[0].Verb}
		if len(item.mutations) > 1 && item.mutations[0].Verb == fileop.VerbEdit {
			result, err := h.batchEdit(ctx, item.mutations)
			if err != nil {
				fileResult["status"], fileResult["error"] = "error", err.Error()
			} else {
				fileResult["status"], fileResult["result"] = "ok", result
			}
			files = append(files, fileResult)
			continue
		}
		var operations []map[string]any
		for index, mutation := range item.mutations {
			result, err := h.mutate(ctx, mutation)
			if err != nil {
				operations = append(operations, map[string]any{"op": index, "status": "error", "error": err.Error()})
				fileResult["status"], fileResult["error"], fileResult["ops"] = "error", fmt.Sprintf("operation %d failed", index), operations
				break
			}
			operations = append(operations, map[string]any{"op": index, "status": "ok", "result": result})
		}
		if _, failed := fileResult["error"]; !failed {
			fileResult["status"] = "ok"
			if len(operations) == 1 {
				fileResult["result"] = operations[0]["result"]
			} else {
				fileResult["ops"] = operations
			}
		}
		files = append(files, fileResult)
	}
	return textJSON(map[string]any{"files": files})
}

func (h *Handler) batchEdit(ctx context.Context, mutations []fileop.Mutation) (any, error) {
	path := mutations[0].Path
	preimage, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	expected := ""
	dryRun := mutations[0].DryRun
	for _, mutation := range mutations {
		if mutation.DryRun != dryRun {
			return nil, fmt.Errorf("same-path edit batch cannot mix dry_run and live edits")
		}
		if mutation.ExpectedSHA != "" {
			if expected != "" && expected != mutation.ExpectedSHA {
				return nil, fmt.Errorf("same-path edit batch has conflicting expected_sha values")
			}
			expected = mutation.ExpectedSHA
		}
	}
	transformed, err := fileop.TransformEdits(mutations, preimage)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return map[string]any{"path": path, "dry_run": true, "diff": transformed.Diff, "spans": transformed.Spans}, nil
	}
	result, err := fileop.Commit(ctx, fileop.CommitRequest{
		Path: path, Data: transformed.Data, ExpectedSHA: expected,
		AllowedRoots: h.roots, Snapshotter: h.vault,
	})
	if err != nil {
		return nil, err
	}
	h.cache.Invalidate(path)
	return map[string]any{"commit": result, "spans": transformed.Spans, "numbered": numbered(transformed.Data)}, nil
}

func (h *Handler) mutate(ctx context.Context, mutation fileop.Mutation) (any, error) {
	if err := mutation.Validate(); err != nil {
		return nil, err
	}
	switch mutation.Verb {
	case fileop.VerbRename:
		return h.rename(mutation)
	case fileop.VerbRestore:
		return h.restore(ctx, mutation)
	case fileop.VerbRewrite:
		return h.rewrite(ctx, mutation)
	}
	path, err := h.resolve(mutation.Path)
	if err != nil {
		return nil, err
	}
	mutation.Path = path
	preimage, err := os.ReadFile(path)
	if os.IsNotExist(err) && mutation.Verb == fileop.VerbWrite {
		preimage = nil
	} else if err != nil {
		return nil, err
	}
	transformed, err := fileop.Transform(mutation, preimage)
	if err != nil {
		return nil, err
	}
	if mutation.DryRun {
		return map[string]any{"path": path, "dry_run": true, "diff": transformed.Diff, "replacements": transformed.Replacements, "spans": transformed.Spans}, nil
	}
	result, err := fileop.Commit(ctx, fileop.CommitRequest{
		Path: path, Data: transformed.Data, ExpectedSHA: mutation.ExpectedSHA,
		AllowedRoots: h.roots, Snapshotter: h.vault,
	})
	if err != nil {
		return nil, err
	}
	h.cache.Invalidate(path)
	response := map[string]any{"commit": result, "replacements": transformed.Replacements, "spans": transformed.Spans}
	if mutation.Verb == fileop.VerbEdit {
		response["numbered"] = numbered(transformed.Data)
	}
	return response, nil
}

func (h *Handler) rewrite(ctx context.Context, mutation fileop.Mutation) (any, error) {
	path, err := h.resolve(mutation.Path)
	if err != nil {
		return nil, err
	}
	mutation.Path = path
	current, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	base, mode, err := h.vault.Load(path, 1)
	if err != nil {
		return nil, err
	}
	transformed, err := fileop.Transform(mutation, base)
	if err != nil {
		return nil, err
	}
	expected := hashBytes(current)
	if mutation.Force {
		expected = ""
	}
	result, err := fileop.Commit(ctx, fileop.CommitRequest{
		Path: path, Data: transformed.Data, ExpectedSHA: expected,
		AllowedRoots: h.roots, Snapshotter: nil, Mode: mode,
	})
	if err == nil {
		h.cache.Invalidate(path)
	}
	return map[string]any{"commit": result, "spans": transformed.Spans, "numbered": numbered(transformed.Data)}, err
}

func (h *Handler) restore(ctx context.Context, mutation fileop.Mutation) (any, error) {
	path, err := h.resolve(mutation.Path)
	if err != nil {
		return nil, err
	}
	data, mode, err := h.vault.Load(path, mutation.Version)
	if err != nil {
		return nil, err
	}
	expected := mutation.ExpectedSHA
	if !mutation.Force && expected == "" {
		current, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		expected = hashBytes(current)
	}
	result, err := fileop.Commit(ctx, fileop.CommitRequest{
		Path: path, Data: data, ExpectedSHA: expected, AllowedRoots: h.roots, Snapshotter: h.vault, Mode: mode,
	})
	if err == nil {
		h.cache.Invalidate(path)
	}
	return result, err
}

func (h *Handler) vaultList(request Request) (any, error) {
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	entries, err := h.vault.List(path)
	if err != nil {
		return nil, err
	}
	return textJSON(map[string]any{"path": path, "entries": entries})
}

func (h *Handler) rename(mutation fileop.Mutation) (any, error) {
	source, err := h.resolve(mutation.Path)
	if err != nil {
		return nil, err
	}
	target, err := security.ResolveBeneath(mutation.Target, h.roots)
	if err != nil {
		return nil, err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to rename symlink")
	}
	sourceData, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	if err := h.vault.Capture(source, sourceData, sourceInfo.Mode()); err != nil {
		return nil, err
	}
	if targetInfo, err := os.Lstat(target); err == nil {
		if !mutation.Overwrite {
			return nil, fmt.Errorf("rename target exists")
		}
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to overwrite symlink target")
		}
		targetData, err := os.ReadFile(target)
		if err != nil {
			return nil, err
		}
		if err := h.vault.Capture(target, targetData, targetInfo.Mode()); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	if err := security.Recheck(filepath.Dir(target), filepath.Dir(target), h.roots); err != nil {
		return nil, err
	}
	if mutation.Overwrite && runtime.GOOS == "windows" {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err := os.Rename(source, target); err != nil {
		return nil, err
	}
	h.cache.Invalidate(source)
	h.cache.Invalidate(target)
	return map[string]any{"from": source, "to": target, "sha256": hashBytes(sourceData)}, nil
}

func numbered(data []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var builder strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&builder, "%6d\t%s\n", index+1, line)
	}
	return builder.String()
}
