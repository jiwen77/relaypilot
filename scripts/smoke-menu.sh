#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT="$(mktemp -d /tmp/relaypilot-menu-smoke.XXXXXX)"
cleanup() { rm -rf "$ROOT"; }
trap cleanup EXIT
mkdir -p "$ROOT/bin" "$ROOT/socks-state"
printf '{"inbounds":[{"type":"socks","tag":"socks-in"}]}\n' > "$ROOT/socks-config.json"
ln -sf "$PWD/relaypilot.sh" "$ROOT/bin/relaypilot-agent"
ln -sf "$PWD/relaypilot.sh" "$ROOT/bin/relaypilot-hub"

cat > "$ROOT/bin/systemctl" <<'STUB'
#!/usr/bin/env bash
cmd="${1:-}"
shift || true
unit_is_listed() {
  local needle="$1" unit
  for unit in ${RELAYPILOT_STUB_SYSTEMCTL_UNITS:-}; do
    [[ "$unit" == "$needle" ]] && return 0
  done
  return 1
}
unit_is_active() {
  local needle="$1" unit
  for unit in ${RELAYPILOT_STUB_SYSTEMCTL_ACTIVE_UNITS:-}; do
    [[ "$unit" == "$needle" ]] && return 0
  done
  return 1
}
case "$cmd" in
  list-unit-files)
    pattern="${1:-}"
    if unit_is_listed "$pattern"; then
      printf '%s enabled\n' "$pattern"
    fi
    ;;
  is-active)
    unit="${1:-}"
    if unit_is_active "$unit"; then
      printf 'active\n'
      exit 0
    fi
    printf 'inactive\n'
    exit 3
    ;;
  is-enabled)
    unit="${1:-}"
    if unit_is_listed "$unit"; then
      printf 'enabled\n'
      exit 0
    fi
    printf 'disabled\n'
    exit 1
    ;;
  status)
    unit="${1:-}"
    unit_is_active "$unit" && { printf '%s active\n' "$unit"; exit 0; }
    printf '%s inactive\n' "$unit"
    exit 3
    ;;
  *)
    ;;
esac
STUB
chmod +x "$ROOT/bin/systemctl"

printf '0\n' | RELAYPILOT_NO_ROOT=1 \
  PATH="$ROOT/bin:$PATH" \
  RELAYPILOT_STUB_SYSTEMCTL_UNITS="sing-box.service" \
  RELAYPILOT_STUB_SYSTEMCTL_ACTIVE_UNITS="sing-box.service sing-box" \
  STATE_DIR="$ROOT/socks-state" \
  SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
  bash ./relaypilot.sh agent > "$ROOT/agent-standalone-proxy-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 \
  PATH="$ROOT/bin:$PATH" \
  RELAYPILOT_STUB_SYSTEMCTL_UNITS="sing-box.service" \
  RELAYPILOT_STUB_SYSTEMCTL_ACTIVE_UNITS="sing-box.service sing-box" \
  STATE_DIR="$ROOT/socks-state" \
  SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
  "$ROOT/bin/relaypilot-agent" > "$ROOT/agent-applet-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 \
  PATH="$ROOT/bin:$PATH" \
  STATE_DIR="$ROOT/hub-state" \
  "$ROOT/bin/relaypilot-hub" > "$ROOT/hub-applet-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 \
  STATE_DIR="$ROOT/socks-state" \
  SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
  bash ./relaypilot.sh landing > "$ROOT/landing-menu.out"

grep -q 'Agent 模式 v.* · 当前：本机代理' "$ROOT/agent-standalone-proxy-menu.out"
grep -q 'Agent 模式 v.* · 当前：本机代理' "$ROOT/agent-applet-menu.out"
grep -q 'Hub 模式' "$ROOT/hub-applet-menu.out"
grep -q 'Hub 尚未初始化' "$ROOT/hub-applet-menu.out"
grep -q 'Hub：○ 未启用.*Agent：○ 未启用.*代理：● SOCKS5 运行中' "$ROOT/agent-standalone-proxy-menu.out"
grep -q 'Agent 尚未接入 Hub' "$ROOT/agent-standalone-proxy-menu.out"
grep -q '接入 Hub' "$ROOT/agent-standalone-proxy-menu.out"
grep -q '安装/更新 SOCKS5' "$ROOT/landing-menu.out"
grep -q '安装/更新 Shadowsocks' "$ROOT/landing-menu.out"
if grep -q '本机直连\|中转出口\|接入 Hub 托管' "$ROOT/agent-standalone-proxy-menu.out" "$ROOT/landing-menu.out"; then
  echo "menus should stay concise without explanatory suffixes" >&2
  exit 1
fi

echo "menu smoke: OK"
