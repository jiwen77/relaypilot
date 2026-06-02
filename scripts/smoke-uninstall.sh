#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d /tmp/relaypilot-uninstall-smoke.XXXXXX)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

make_fixture() {
  rm -rf "$TMP/fixture"
  INSTALL_DIR="$TMP/fixture/opt/relaypilot"
  BIN_DIR="$TMP/fixture/usr/local/bin"
  STATE_DIR="$TMP/fixture/etc/relaypilot"
  CONF_DIR="$TMP/fixture/etc/sing-box/conf"
  CONFIG_PATH="$TMP/fixture/etc/sing-box/config.json"
  SYSTEMD_DIR="$TMP/fixture/etc/systemd/system"
  OPENRC_DIR="$TMP/fixture/etc/init.d"
  MESH_DIR="$TMP/fixture/etc/wireguard"
  mkdir -p "$INSTALL_DIR/bin" "$BIN_DIR" "$STATE_DIR/hub-tls" "$STATE_DIR/endpoints" \
    "$CONF_DIR" "$(dirname "$CONFIG_PATH")" "$SYSTEMD_DIR" "$OPENRC_DIR" "$MESH_DIR"
  : > "$INSTALL_DIR/relaypilot.sh"
  : > "$INSTALL_DIR/bin/relaypilot"
  ln -s "$INSTALL_DIR/relaypilot.sh" "$BIN_DIR/relaypilot"
  printf '{"agents":{}}\n' > "$STATE_DIR/hub-agents.json"
  printf '{"agent_id":"a"}\n' > "$STATE_DIR/agent-enrollment.json"
  printf '{}\n' > "$CONF_DIR/00-relaypilot-reality.json"
  printf '{}\n' > "$CONF_DIR/90-relaypilot-outbounds.json"
  printf '{}\n' > "$CONF_DIR/other.json"
  printf '{"external":true}\n' > "$CONFIG_PATH"
  printf '# RelayPilot managed WireGuard mesh\n' > "$MESH_DIR/rp-test.conf"
  printf '# external wg\n' > "$MESH_DIR/wg0.conf"
  for unit in "$AGENT_SERVICE_NAME" "$HUB_SERVICE_NAME" "$TG_SERVICE_NAME" "$HUB_ALERT_TIMER_NAME"; do
    : > "$SYSTEMD_DIR/${unit}.service"
  done
  : > "$SYSTEMD_DIR/${HUB_ALERT_TIMER_NAME}.timer"
  : > "$SYSTEMD_DIR/${SERVICE_NAME}.service"
}

run_uninstall() {
  RELAYPILOT_NO_ROOT=1 \
  INSTALL_DIR="$INSTALL_DIR" \
  BIN_PATH="$BIN_DIR/relaypilot" \
  STATE_DIR="$STATE_DIR" \
  CONF_DIR="$CONF_DIR" \
  SINGBOX_CONFIG_PATH="$CONFIG_PATH" \
  SYSTEMD_DIR="$SYSTEMD_DIR" \
  OPENRC_DIR="$OPENRC_DIR" \
  MESH_CONFIG_DIR="$MESH_DIR" \
  AGENT_SERVICE_NAME="$AGENT_SERVICE_NAME" \
  HUB_SERVICE_NAME="$HUB_SERVICE_NAME" \
  TG_SERVICE_NAME="$TG_SERVICE_NAME" \
  HUB_ALERT_TIMER_NAME="$HUB_ALERT_TIMER_NAME" \
  SERVICE_NAME="$SERVICE_NAME" \
  bash "$ROOT/relaypilot.sh" uninstall "$@"
}

AGENT_SERVICE_NAME="rp-test-agent"
HUB_SERVICE_NAME="rp-test-hub"
TG_SERVICE_NAME="rp-test-bot"
HUB_ALERT_TIMER_NAME="rp-test-alert"
SERVICE_NAME="rp-test-sing-box"

make_fixture
run_uninstall --full --purge-proxy-config --dry-run --yes >/tmp/relaypilot-uninstall-dry.out
[[ -d "$INSTALL_DIR" ]]
[[ -d "$STATE_DIR" ]]
[[ -e "$CONF_DIR/00-relaypilot-reality.json" ]]
[[ -e "$SYSTEMD_DIR/${HUB_SERVICE_NAME}.service" ]]
grep -q "DRY-RUN" /tmp/relaypilot-uninstall-dry.out

make_fixture
run_uninstall --full --purge-proxy-config --yes >/tmp/relaypilot-uninstall-full.out
[[ ! -e "$BIN_DIR/relaypilot" ]]
[[ ! -d "$INSTALL_DIR" ]]
[[ ! -d "$STATE_DIR" ]]
[[ ! -e "$SYSTEMD_DIR/${HUB_SERVICE_NAME}.service" ]]
[[ ! -e "$SYSTEMD_DIR/${AGENT_SERVICE_NAME}.service" ]]
[[ ! -e "$SYSTEMD_DIR/${TG_SERVICE_NAME}.service" ]]
[[ ! -e "$SYSTEMD_DIR/${HUB_ALERT_TIMER_NAME}.timer" ]]
[[ ! -e "$CONF_DIR/00-relaypilot-reality.json" ]]
[[ ! -e "$CONF_DIR/90-relaypilot-outbounds.json" ]]
[[ -e "$CONF_DIR/other.json" ]]
[[ -e "$CONFIG_PATH" ]]
[[ ! -e "$MESH_DIR/rp-test.conf" ]]
[[ -e "$MESH_DIR/wg0.conf" ]]

echo "uninstall smoke: OK"
