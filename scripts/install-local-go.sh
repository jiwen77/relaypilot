#!/usr/bin/env bash
set -euo pipefail

VERSION="${GO_VERSION:-1.22.12}"
ROOT="${ROOT:-/tmp/relaypilot-go-toolchain}"
TARBALL="$ROOT/go${VERSION}.tar.gz"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

mkdir -p "$ROOT"
if [[ ! -x "$ROOT/go/bin/go" ]]; then
  url="https://go.dev/dl/go${VERSION}.linux-${GOARCH}.tar.gz"
  curl -fsSL "$url" -o "$TARBALL"
  rm -rf "$ROOT/go"
  tar -C "$ROOT" -xzf "$TARBALL"
fi

printf '%s\n' "$ROOT/go/bin/go"
