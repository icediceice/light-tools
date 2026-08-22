#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root"

copy_tree() {
  module="$1"
  source_dir="$2"
  destination="$3"
  required="${4-}"
  module_root="$(go list -mod=mod -m -f '{{.Dir}}' "$module")"
  if [ ! -d "$module_root/$source_dir" ]; then
    echo "missing vendored C source: $module_root/$source_dir" >&2
    exit 1
  fi
  rm -rf "$destination"
  mkdir -p "$(dirname -- "$destination")"
  cp -R "$module_root/$source_dir" "$destination"
  chmod -R u+w "$destination"
  rm -f "$destination/grammar.json" "$destination/node-types.json"
  if [ -n "$required" ] && [ ! -f "$destination/$required" ]; then
    echo "vendored source is missing $required: $destination" >&2
    exit 1
  fi
}

copy_tree github.com/tree-sitter/go-tree-sitter include vendor/github.com/tree-sitter/go-tree-sitter/include
copy_tree github.com/tree-sitter/go-tree-sitter src vendor/github.com/tree-sitter/go-tree-sitter/src parser.c

copy_tree github.com/UserNobody14/tree-sitter-dart src vendor/github.com/UserNobody14/tree-sitter-dart/src parser.c
copy_tree github.com/tree-sitter-grammars/tree-sitter-kotlin src vendor/github.com/tree-sitter-grammars/tree-sitter-kotlin/src parser.c
copy_tree github.com/tree-sitter-grammars/tree-sitter-lua src vendor/github.com/tree-sitter-grammars/tree-sitter-lua/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-bash src vendor/github.com/tree-sitter/tree-sitter-bash/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-c src vendor/github.com/tree-sitter/tree-sitter-c/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-c-sharp src vendor/github.com/tree-sitter/tree-sitter-c-sharp/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-cpp src vendor/github.com/tree-sitter/tree-sitter-cpp/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-go src vendor/github.com/tree-sitter/tree-sitter-go/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-html src vendor/github.com/tree-sitter/tree-sitter-html/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-java src vendor/github.com/tree-sitter/tree-sitter-java/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-javascript src vendor/github.com/tree-sitter/tree-sitter-javascript/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-python src vendor/github.com/tree-sitter/tree-sitter-python/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-ruby src vendor/github.com/tree-sitter/tree-sitter-ruby/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-rust src vendor/github.com/tree-sitter/tree-sitter-rust/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-scala src vendor/github.com/tree-sitter/tree-sitter-scala/src parser.c

copy_tree github.com/tree-sitter/tree-sitter-typescript typescript/src vendor/github.com/tree-sitter/tree-sitter-typescript/typescript/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-typescript tsx/src vendor/github.com/tree-sitter/tree-sitter-typescript/tsx/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-typescript common vendor/github.com/tree-sitter/tree-sitter-typescript/common

copy_tree github.com/tree-sitter/tree-sitter-php php/src vendor/github.com/tree-sitter/tree-sitter-php/php/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-php php_only/src vendor/github.com/tree-sitter/tree-sitter-php/php_only/src parser.c
copy_tree github.com/tree-sitter/tree-sitter-php common vendor/github.com/tree-sitter/tree-sitter-php/common