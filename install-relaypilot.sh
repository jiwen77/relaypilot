#!/usr/bin/env bash
set -euo pipefail

REPO="${REPO:-jiwen77/relaypilot}"
RAW_REF="${RAW_REF:-main}"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/opt/relaypilot}"
BIN_PATH="${BIN_PATH:-/usr/local/bin/relaypilot}"
HUB_BIN_PATH="${HUB_BIN_PATH:-$(dirname "$BIN_PATH")/relaypilot-hub}"
AGENT_BIN_PATH="${AGENT_BIN_PATH:-$(dirname "$BIN_PATH")/relaypilot-agent}"
RAW_BASE="${RAW_BASE:-https://github.com/${REPO}/raw/${RAW_REF}}"
RELEASE_BASE="${RELEASE_BASE:-https://github.com/${REPO}/releases/download}"

need_root() {
  if [[ "${RELAYPILOT_NO_ROOT:-}" == "1" ]]; then return 0; fi
  if [[ "${EUID:-$(id -u)}" != 0 ]]; then echo "ERROR: run as root" >&2; exit 1; fi
}
arch_name() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    armv7l|armv7*) arch=armv7 ;;
    armv6l|armv6*) arch=armv6 ;;
    mips64el) arch=mips64le ;;
    mipsel) arch=mipsle ;;
  esac
  printf '%s_%s' "$os" "$arch"
}
fetch() {
  local url="$1" output="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$output" "$url"
  else
    echo "ERROR: curl or wget is required" >&2
    return 1
  fi
}

need_root
mkdir -p "$INSTALL_DIR/bin"

tmp="$(mktemp -d /tmp/relaypilot-install.XXXXXX)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

entrypoint="$tmp/relaypilot.sh"
fetch "$RAW_BASE/relaypilot.sh" "$entrypoint"
chmod +x "$entrypoint"

platform="$(arch_name)"
asset="relaypilot_${platform}"
if [[ "$VERSION" != latest ]]; then
  url="$RELEASE_BASE/$VERSION/$asset"
else
  url="https://github.com/${REPO}/releases/latest/download/$asset"
fi

core="$tmp/$asset"
if fetch "$url" "$core"; then
  chmod +x "$core"
  checksum="$tmp/$asset.sha256"
  if fetch "$url.sha256" "$checksum" 2>/dev/null; then
    if ! command -v sha256sum >/dev/null 2>&1; then
      echo "ERROR: sha256sum is required to verify release checksum" >&2
      exit 1
    fi
    (cd "$tmp" && sha256sum -c "$(basename "$checksum")")
  fi
  cp -a "$entrypoint" "$INSTALL_DIR/relaypilot.sh"
  cp -a "$core" "$INSTALL_DIR/bin/relaypilot"
  echo "Installed Go core: $INSTALL_DIR/bin/relaypilot"
else
  echo "ERROR: Go release binary not available for $platform; RelayPilot is Go-only." >&2
  exit 1
fi

mkdir -p "$(dirname "$BIN_PATH")"
ln -sf "$INSTALL_DIR/relaypilot.sh" "$BIN_PATH"
echo "Installed entrypoint: $BIN_PATH"
echo "Run: relaypilot        # 总面板"

install_hub_entrypoint() {
  mkdir -p "$(dirname "$HUB_BIN_PATH")"
  ln -sf "$INSTALL_DIR/relaypilot.sh" "$HUB_BIN_PATH"
  echo "Installed Hub entrypoint: $HUB_BIN_PATH"
  echo "Run: relaypilot-hub    # Hub 面板"
}

install_agent_entrypoint() {
  mkdir -p "$(dirname "$AGENT_BIN_PATH")"
  ln -sf "$INSTALL_DIR/relaypilot.sh" "$AGENT_BIN_PATH"
  echo "Installed Agent entrypoint: $AGENT_BIN_PATH"
  echo "Run: relaypilot-agent  # Agent 面板"
}

if [[ $# -gt 0 ]]; then
  case "${1:-}" in
    hub|--hub)
      shift || true
      install_hub_entrypoint
      "$HUB_BIN_PATH" install "$@"
      ;;
    agent|--agent)
      shift || true
      install_agent_entrypoint
      "$AGENT_BIN_PATH" install "$@"
      ;;
    menu|--menu|interactive|--interactive)
      shift || true
      "$BIN_PATH"
      ;;
    --enroll|--invite)
      if [[ $# -lt 2 ]]; then echo "ERROR: $1 requires an invite value" >&2; exit 1; fi
      invite="$2"; shift 2
      install_agent_entrypoint
      case "${RELAYPILOT_INSTALL_ENROLL_MODE:-}" in
        join|interactive)
          "$AGENT_BIN_PATH" join --invite "$invite" "$@"
          ;;
        auto|noninteractive)
          "$AGENT_BIN_PATH" enroll --invite "$invite" --install-service "$@"
          ;;
        *)
          if [[ -t 0 && -t 1 ]]; then
            "$AGENT_BIN_PATH" join --invite "$invite" "$@"
          else
            "$AGENT_BIN_PATH" enroll --invite "$invite" --install-service "$@"
          fi
          ;;
      esac
      ;;
    --bundle)
      if [[ $# -lt 2 ]]; then echo "ERROR: --bundle requires a value" >&2; exit 1; fi
      bundle="$2"; shift 2
      install_agent_entrypoint
      "$AGENT_BIN_PATH" enroll --bundle "$bundle" --install-service "$@"
      ;;
    *)
      "$BIN_PATH" "$@"
      ;;
  esac
elif [[ -t 0 && -t 1 ]]; then
  echo "Launching interactive menu..."
  "$BIN_PATH"
fi
