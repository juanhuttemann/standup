#!/bin/sh
# standup installer — Linux, macOS, WSL2, Termux.
# Downloads the latest release binary from GitHub into ~/.local/bin
# (/usr/local/bin as root, $PREFIX/bin on Termux).
set -eu

repo=juanhuttemann/standup

os=$(uname -s)
case "$os" in
  Linux*) os=linux ;;
  Darwin*) os=darwin ;;
  *) echo "unsupported OS: $os (on Windows use install.ps1)" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

bin_dir=${STANDUP_BIN_DIR:-$HOME/.local/bin}
if [ "$(id -u)" = 0 ]; then bin_dir=/usr/local/bin; fi
if [ -n "${TERMUX_VERSION:-}" ]; then bin_dir=${PREFIX:?}/bin; fi

url="https://github.com/$repo/releases/latest/download/standup_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $url"
curl -fsSL "$url" | tar -xzf - -C "$tmp"

mkdir -p "$bin_dir"
mv "$tmp/standup" "$bin_dir/standup"
chmod +x "$bin_dir/standup"

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH" >&2 ;;
esac

echo "installed $bin_dir/standup"
