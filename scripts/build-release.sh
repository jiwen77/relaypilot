#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"
VERSION="${VERSION:-$(cat "$ROOT/VERSION" 2>/dev/null || echo dev)}"
mkdir -p "$OUT_DIR"
cd "$ROOT"

targets=(
  linux/amd64
  linux/arm64
  linux/arm/7
  linux/arm/6
)

for target in "${targets[@]}"; do
  IFS=/ read -r goos goarch goarm <<<"$target"
  suffix="${goos}_${goarch}"
  env_args=("GOOS=$goos" "GOARCH=$goarch" "CGO_ENABLED=0")
  if [[ -n "${goarm:-}" ]]; then
    env_args+=("GOARM=$goarm")
    suffix="${suffix}v${goarm}"
  fi
  out="$OUT_DIR/relaypilot_${suffix}"
  env "${env_args[@]}" go build -trimpath -ldflags "-s -w -X main.buildVersion=$VERSION" -o "$out" ./cmd/relaypilot
  (cd "$OUT_DIR" && sha256sum "$(basename "$out")" > "$(basename "$out").sha256")
  echo "built $out"
done
