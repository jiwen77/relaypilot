#!/usr/bin/env bash
set -euo pipefail

REPO="${REPO:-jiwen77/relaypilot}"
RAW_REF="${RAW_REF:-main}"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/opt/relaypilot}"
BIN_PATH="${BIN_PATH:-/usr/local/bin/relaypilot}"
HUB_BIN_PATH="${HUB_BIN_PATH:-$(dirname "$BIN_PATH")/relaypilot-hub}"
AGENT_BIN_PATH="${AGENT_BIN_PATH:-$(dirname "$BIN_PATH")/relaypilot-agent}"
RAW_BASE_USER_SET="${RAW_BASE:+1}"
RAW_BASE="${RAW_BASE:-https://github.com/${REPO}/raw/${RAW_REF}}"
RELEASE_BASE="${RELEASE_BASE:-https://github.com/${REPO}/releases/download}"
SCRIPT_REF="${SCRIPT_REF:-}"

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

normalize_version() {
  local value="$1"
  value="${value#v}"
  printf '%s' "$value"
}

script_default_version() {
  local script="$1"
  [[ -f "$script" ]] || return 1
  sed -nE 's/^VERSION="\$\{RELAYPILOT_VERSION:-([^}]+)\}"$/\1/p' "$script" | head -n 1
}

core_file_version() {
  local core="$1" out
  [[ -x "$core" ]] || return 1
  out="$("$core" version 2>/dev/null || true)"
  sed -nE 's/^RelayPilot Go core[[:space:]]+(.+)$/\1/p' <<<"$out" | head -n 1
}

verify_release_pair_versions() {
  local script="$1" core="$2" script_version core_version
  script_version="$(script_default_version "$script" || true)"
  core_version="$(core_file_version "$core" || true)"
  if [[ -z "$script_version" || -z "$core_version" ]]; then
    echo "ERROR: cannot verify version consistency: script=${script_version:-unknown}, core=${core_version:-unknown}" >&2
    return 1
  fi
  if [[ "$(normalize_version "$script_version")" != "$(normalize_version "$core_version")" ]]; then
    echo "ERROR: refusing mismatched install: panel script $script_version, Go core $core_version" >&2
    return 1
  fi
  echo "Version match: panel $script_version / Go core $core_version"
}

resolve_latest_release_tag() {
  local output=""
  if command -v curl >/dev/null 2>&1; then
    output="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || true)"
  elif command -v wget >/dev/null 2>&1; then
    output="$(wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || true)"
  fi
  sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' <<<"$output" | head -n 1
}

entrypoint_raw_base() {
  local ref="$SCRIPT_REF"
  if [[ -z "$ref" && "${RAW_BASE_USER_SET:-0}" == "1" ]]; then
    printf '%s' "$RAW_BASE"
    return
  fi
  if [[ -z "$ref" ]]; then
    if [[ "$VERSION" == latest ]]; then
      ref="$(resolve_latest_release_tag || true)"
    else
      ref="$VERSION"
    fi
  fi
  if [[ -n "$ref" ]]; then
    printf 'https://github.com/%s/raw/%s' "$REPO" "$ref"
  else
    printf '%s' "$RAW_BASE"
  fi
}

need_root
mkdir -p "$INSTALL_DIR/bin"

tmp="$(mktemp -d /tmp/relaypilot-install.XXXXXX)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

platform="$(arch_name)"
asset="relaypilot_${platform}"
if [[ "$VERSION" != latest ]]; then
  url="$RELEASE_BASE/$VERSION/$asset"
else
  url="https://github.com/${REPO}/releases/latest/download/$asset"
fi

script_base="$(entrypoint_raw_base)"
entrypoint="$tmp/relaypilot.sh"
fetch "$script_base/relaypilot.sh" "$entrypoint"
chmod +x "$entrypoint"

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
  verify_release_pair_versions "$entrypoint" "$core"
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
