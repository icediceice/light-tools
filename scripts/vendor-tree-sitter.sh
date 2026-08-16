#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root"

copy_tree() {
  module="$1"
  source_dir="$2"
  destination="$3"
  module_root="$(go list -mod=mod -m -f '{{.Dir}}' "$module")"
  if [ ! -d "$module_root/$source_dir" ]; then
    echo "missing vendored C source: $module_root/$source_dir" >&2
    exit 1
  fi
  rm -rf "$destination"
  mkdir -p "$(dirname -- "$destination")"
  cp -R "$module_root/$source_dir" "$destination"
}

copy_tree github.com/tree-sitter/go-tree-sitter include vendor/github.com/tree-sitter/go-tree-sitter/include
copy_tree github.com/tree-sitter/go-tree-sitter src vendor/github.com/tree-sitter/go-tree-sitter/src
copy_tree github.com/tree-sitter/tree-sitter-go src vendor/github.com/tree-sitter/tree-sitter-go/src
copy_tree github.com/tree-sitter/tree-sitter-javascript src vendor/github.com/tree-sitter/tree-sitter-javascript/src
copy_tree github.com/tree-sitter/tree-sitter-python src vendor/github.com/tree-sitter/tree-sitter-python/src
