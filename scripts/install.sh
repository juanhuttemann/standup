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
sum_url="https://github.com/$repo/releases/latest/download/standup_checksums.txt"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $url"
curl -fsSL "$url" -o "$tmp/standup.tar.gz"

# Verify the archive checksum when this release publishes one (older
# releases have only a versioned checksums file — skip with a note).
if curl -fsSL "$sum_url" -o "$tmp/checksums.txt" 2>/dev/null; then
  line=$(grep "standup_${os}_${arch}.tar.gz" "$tmp/checksums.txt" || true)
  if [ -n "$line" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      sum=sha256sum
    else
      sum="shasum -a 256"
    fi
    (cd "$tmp" && echo "$line" | $sum -c -) || { echo "checksum mismatch — aborting" >&2; exit 1; }
  fi
else
  echo "note: no checksums file on this release, skipping verification" >&2
fi

tar -xzf "$tmp/standup.tar.gz" -C "$tmp"

mkdir -p "$bin_dir"
mv "$tmp/standup" "$bin_dir/standup"
chmod +x "$bin_dir/standup"

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH" >&2 ;;
esac

echo "installed $bin_dir/standup"
