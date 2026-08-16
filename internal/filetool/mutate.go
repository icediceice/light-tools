package filetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	fileop "github.com/icediceice/light-tools/internal/file"
	"github.com/icediceice/light-tools/internal/security"
)

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
		return map[string]any{"path": path, "dry_run": true, "diff": transformed.Diff, "replacements": transformed.Replacements}, nil
	}
	result, err := fileop.Commit(ctx, fileop.CommitRequest{
		Path: path, Data: transformed.Data, ExpectedSHA: mutation.ExpectedSHA,
		AllowedRoots: h.roots, Snapshotter: h.vault,
	})
	if err != nil {
		return nil, err
	}
	h.cache.Invalidate(path)
	return map[string]any{"commit": result, "replacements": transformed.Replacements}, nil
}

func (h *Handler) rewrite(ctx context.Context, mutation fileop.Mutation) (any, error) {
	path, err := h.resolve(mutation.Path)
	if err != nil {
		return nil, err
	}
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
	result, err := fileop.Commit(ctx, fileop.CommitRequest{
		Path: path, Data: transformed.Data, ExpectedSHA: hashBytes(current),
		AllowedRoots: h.roots, Snapshotter: h.vault, Mode: mode,
	})
	if err == nil {
		h.cache.Invalidate(path)
	}
	return result, err
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
	if _, err := os.Lstat(target); err == nil && !mutation.Overwrite {
		return nil, fmt.Errorf("rename target exists")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	if err := security.Recheck(filepath.Dir(target), filepath.Dir(target), h.roots); err != nil {
		return nil, err
	}
	if mutation.Overwrite {
		_ = os.Remove(target)
	}
	if err := os.Rename(source, target); err != nil {
		return nil, err
	}
	h.cache.Invalidate(source)
	h.cache.Invalidate(target)
	return map[string]any{"from": source, "to": target}, nil
}
