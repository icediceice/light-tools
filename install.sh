#!/bin/sh
set -eu

repo="${LIGHT_TOOLS_REPO:-icediceice/light-tools}"
install_dir="${LIGHT_TOOLS_INSTALL_DIR:-$HOME/.local/bin}"
version="${LIGHT_TOOLS_VERSION:-}"

if [ -z "$version" ]; then
  tag="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  version="${tag#v}"
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW*|MSYS*|CYGWIN*) os=windows ;;
  *) echo "unsupported OS" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac

if [ "$os" = windows ] && [ "$arch" = arm64 ]; then
  echo "windows/arm64 is not published" >&2
  exit 1
fi

extension=tar.gz
[ "$os" = windows ] && extension=zip
asset="light-tools_${version}_${os}_${arch}.${extension}"
base="https://github.com/$repo/releases/download/v$version"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

curl -fsSL "$base/$asset" -o "$temp_dir/$asset"
curl -fsSL "$base/checksums.txt" -o "$temp_dir/checksums.txt"
(
  cd "$temp_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    grep "  $asset\$" checksums.txt | sha256sum -c -
  else
    expected="$(grep "  $asset\$" checksums.txt | awk '{print $1}')"
    actual="$(shasum -a 256 "$asset" | awk '{print $1}')"
    [ "$expected" = "$actual" ]
  fi
)

mkdir -p "$install_dir"
if [ "$extension" = zip ]; then
  unzip -q "$temp_dir/$asset" -d "$temp_dir/unpacked"
  install -m 0755 "$temp_dir/unpacked/light-tools.exe" "$install_dir/light-tools.exe"
  installed="$install_dir/light-tools.exe"
else
  tar -xzf "$temp_dir/$asset" -C "$temp_dir"
  install -m 0755 "$temp_dir/light-tools" "$install_dir/light-tools"
  installed="$install_dir/light-tools"
fi

echo "installed $installed"
echo "next: light-tools init"