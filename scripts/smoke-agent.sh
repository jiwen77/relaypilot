#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT="$(mktemp -d /tmp/relaypilot-smoke.XXXXXX)"
cleanup() { rm -rf "$ROOT"; }
trap cleanup EXIT
mkdir -p "$ROOT/bin" "$ROOT/state" "$ROOT/migrated-state" "$ROOT/transit-conf" "$ROOT/systemd"

if ! command -v go >/dev/null 2>&1; then
  echo "go is required for smoke test" >&2
  exit 1
fi
go build -trimpath -o "$ROOT/bin/relaypilot" ./cmd/relaypilot
export RELAYPILOT_GO_BIN="$ROOT/bin/relaypilot"

cat > "$ROOT/bin/sing-box" <<'STUB'
#!/usr/bin/env bash
printf 'stub sing-box %s\n' "$*" >> "${RELAYPILOT_STUB_LOG:?}"
exit 0
STUB
chmod +x "$ROOT/bin/sing-box"

cat > "$ROOT/landing.in" <<'EOF_INPUT'
hk
203.0.113.10
::
2443
443
2022-blake3-aes-128-gcm
ss-in
landing-hk-ss
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
bash ./relaypilot.sh landing-install-ss < "$ROOT/landing.in" > "$ROOT/landing.out" 2> "$ROOT/landing.err"

cat > "$ROOT/transit-init.in" <<EOF_INPUT
$ROOT/transit-conf
::
443
vless-in
www.cloudflare.com
www.cloudflare.com
443
0123456789abcdef
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
bash ./relaypilot.sh transit-init-reality < "$ROOT/transit-init.in" > "$ROOT/transit-init.out" 2> "$ROOT/transit-init.err"

cat > "$ROOT/transit.in" <<EOF_INPUT
$ROOT/state/endpoints/hk.json
$ROOT/transit-conf
vless-in
hk
44444444-4444-4444-8444-444444444444
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
bash ./relaypilot.sh transit-import-bind < "$ROOT/transit.in" > "$ROOT/transit.out" 2> "$ROOT/transit.err"

STATE_DIR="$ROOT/state" bash ./relaypilot.sh tg-config \
  --bot-token "123456:SMOKE_TOKEN" \
  --chat-id "987654" \
  --api-base "https://api.telegram.example" > "$ROOT/tg-config.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh tg-commands > "$ROOT/tg-commands.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh tg-commands --hub > "$ROOT/tg-hub-commands.out"
STATE_DIR="$ROOT/state" TG_DRY_RUN=1 bash ./relaypilot.sh tg-register-commands > "$ROOT/tg-register.out"
STATE_DIR="$ROOT/state" CONF_DIR="$ROOT/transit-conf" bash ./relaypilot.sh tg-dispatch "/endpoints" > "$ROOT/tg-dispatch.out"
STATE_DIR="$ROOT/state" TG_DRY_RUN=1 bash ./relaypilot.sh tg-send "hello" > "$ROOT/tg-send.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh bot commands > "$ROOT/bot-commands.out"

printf '2\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/agent-menu.out"
printf '1\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/hub-menu.out"
printf '3\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/service-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-direct-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh transit > "$ROOT/transit-menu.out"

STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-init-tls --host hub.example > "$ROOT/hub-tls.out"
printf '2\nsmoke-interactive\nhub.example\n10m\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh hub-enroll > "$ROOT/hub-enroll.out"
RELAYPILOT_PROFILE=tiny bash ./relaypilot.sh resource-profile > "$ROOT/profile-tiny.out"
bash ./relaypilot.sh migrate-state --from "$ROOT/state" --to "$ROOT/migrated-state" --dry-run > "$ROOT/migrate-dry.out"

SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
RELAYPILOT_NO_ROOT=1 \
RELAYPILOT_PROFILE=tiny \
bash ./relaypilot.sh install-bot-service > "$ROOT/bot-service.out" 2> "$ROOT/bot-service.err"
printf '\n\nn\n' | SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/quick-hub" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
RELAYPILOT_NO_ROOT=1 \
HUB_PUBLIC_HOST="hub.quick.example" \
RELAYPILOT_PROFILE=tiny \
bash ./relaypilot.sh hub-quick-setup > "$ROOT/hub-quick.out" 2> "$ROOT/hub-quick.err"
SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/state" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install-alert-timer \
  --interval 30min \
  --threshold-seconds 3600 \
  --snooze-seconds 7200 \
  --dry-run > "$ROOT/alert-timer.out" 2> "$ROOT/alert-timer.err"

STATE_DIR="$ROOT/state" CONF_DIR="$ROOT/transit-conf" bash ./relaypilot.sh hub-agent-export \
  --agent-id transit-hk \
  --role transit \
  --labels region=hk \
  --output "$ROOT/transit-hk.registration.json" > "$ROOT/hub-export.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-import-agent "$ROOT/transit-hk.registration.json" > "$ROOT/hub-import.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-issue-token --token smoke-token transit-hk > "$ROOT/hub-token.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-tokens > "$ROOT/hub-tokens.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-revoke-token transit-hk > "$ROOT/hub-token-revoke.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-dispatch "/status" > "$ROOT/hub-status.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-dispatch "/topology" > "$ROOT/hub-topology.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-dispatch "/status transit" > "$ROOT/hub-route.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-dispatch "/update transit v-local" > "$ROOT/hub-update.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-tasks > "$ROOT/hub-tasks-after-update.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-alert-offline --threshold-seconds 0 --dry-run > "$ROOT/hub-alert-dry.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-remove-agent transit-hk --reason smoke-uninstall > "$ROOT/hub-remove.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-removed-agents > "$ROOT/hub-removed.out"

SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
AGENT_SERVICE_NAME="relay-smoke-agent" \
RELAYPILOT_PROFILE=small \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install-agent-service \
  --hub-url http://127.0.0.1:8080 \
  --agent-id transit-hk \
  --role transit \
  --token-file "$ROOT/state/agent-token" \
  --conf "$ROOT/transit-conf" > "$ROOT/agent-service.out" 2> "$ROOT/agent-service.err"

SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
HUB_SERVICE_NAME="relay-smoke-hub" \
RELAYPILOT_PROFILE=small \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install-hub-service \
  --host 127.0.0.1 \
  --port 8080 > "$ROOT/hub-service.out" 2> "$ROOT/hub-service.err"

SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
TG_SERVICE_NAME="relay-smoke-tg" \
RELAYPILOT_PROFILE=small \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install-bot-service > "$ROOT/tg-service.out" 2> "$ROOT/tg-service.err"

RELAYPILOT_PROFILE=normal \
AGENT_SERVICE_MEMORY_MAX=77M \
HUB_SERVICE_CPU_QUOTA=33% \
bash ./relaypilot.sh resource-profile > "$ROOT/profile-override.out"

grep -q '/relaypilot_status' "$ROOT/tg-commands.out"
grep -q '/relaypilot_panel' "$ROOT/tg-hub-commands.out"
grep -q '/relaypilot_link' "$ROOT/tg-hub-commands.out"
grep -q '/relaypilot_update' "$ROOT/tg-hub-commands.out"
grep -q '/relaypilot_status' "$ROOT/bot-commands.out"
grep -q 'setMyCommands' "$ROOT/tg-register.out"
! grep -q 'SMOKE_TOKEN' "$ROOT/tg-register.out"
grep -q 'dry_run' "$ROOT/tg-send.out"
! grep -q 'SMOKE_TOKEN' "$ROOT/tg-send.out"
grep -q 'landing-hk-ss' "$ROOT/tg-dispatch.out"

grep -q 'RelayPilot' "$ROOT/agent-menu.out"
grep -q '1) Hub 模式' "$ROOT/agent-menu.out"
grep -q '2) Agent 模式' "$ROOT/agent-menu.out"
grep -q '1) 配置中转节点' "$ROOT/agent-menu.out"
grep -q '2) 配置落地节点' "$ROOT/agent-menu.out"
grep -q '3) 粘贴 Hub invite 并安装 Agent 服务' "$ROOT/agent-menu.out"
grep -q 'Hub 模式' "$ROOT/hub-menu.out"
grep -q '1) 初始化 Hub 服务' "$ROOT/hub-menu.out"
grep -q '2) 生成 Agent invite' "$ROOT/hub-menu.out"
grep -q '5) 串联中转/落地' "$ROOT/hub-menu.out"
grep -q '6) Telegram' "$ROOT/hub-menu.out"
grep -q '服务管理' "$ROOT/service-menu.out"
grep -q '5) 资源限制' "$ROOT/service-menu.out"
grep -q '6) 更新 RelayPilot' "$ROOT/service-menu.out"
grep -q 'relaypilot-agent' "$ROOT/service-menu.out"
grep -q 'Agent 模式' "$ROOT/agent-direct-menu.out"
grep -q '1) 配置中转节点' "$ROOT/agent-direct-menu.out"
grep -q '1) 初始化/更新 Reality 入口' "$ROOT/transit-menu.out"
grep -q '2) 绑定落地 endpoint' "$ROOT/transit-menu.out"
! grep -q 'Advanced' "$ROOT/hub-menu.out"
! grep -q '接入 Hub' "$ROOT/agent-menu.out"
! grep -q '不会 fanout' "$ROOT/hub-menu.out"

grep -q 'smoke-interactive' "$ROOT/hub-enroll.out"
grep -q 'hub.example' "$ROOT/hub-enroll.out"
grep -q -- '--enroll' "$ROOT/hub-enroll.out"
grep -q 'profile=tiny' "$ROOT/profile-tiny.out"
grep -q 'agent=64M/15%' "$ROOT/profile-tiny.out"
grep -q 'telegram=96M/20%' "$ROOT/profile-tiny.out"
grep -q 'profile=normal' "$ROOT/profile-override.out"
grep -q 'agent=77M/50%' "$ROOT/profile-override.out"
grep -q 'hub=256M/33%' "$ROOT/profile-override.out"
grep -q '"dry_run": true' "$ROOT/migrate-dry.out"
grep -q 'endpoints/hk.json' "$ROOT/migrate-dry.out"

grep -q 'relaypilot-bot.service' "$ROOT/bot-service.out"
grep -q 'bot-daemon' "$ROOT/systemd/relaypilot-bot.service"
grep -q 'MemoryMax=96M' "$ROOT/systemd/relaypilot-bot.service"
grep -q 'Hub URL 给 agent 使用：https://hub.quick.example:8443' "$ROOT/hub-quick.out"
grep -q 'hub-daemon' "$ROOT/systemd/relaypilot-hub.service"
grep -q -- '--require-client-cert' "$ROOT/systemd/relaypilot-hub.service"
[[ -f "$ROOT/quick-hub/hub-tls/ca.crt" ]]
grep -q 'relaypilot-alert-offline.timer' "$ROOT/alert-timer.out"
grep -q 'OnUnitActiveSec=30min' "$ROOT/systemd/relaypilot-alert-offline.timer"
grep -q -- '--threshold-seconds 3600' "$ROOT/systemd/relaypilot-alert-offline.service"
grep -q -- '--snooze-seconds 7200' "$ROOT/systemd/relaypilot-alert-offline.service"
grep -q -- '--dry-run' "$ROOT/systemd/relaypilot-alert-offline.service"

grep -q '"token": "smoke-token"' "$ROOT/hub-token.out"
grep -q 'transit-hk' "$ROOT/hub-tokens.out"
! grep -q 'smoke-token' "$ROOT/hub-tokens.out"
grep -q '"revoked": true' "$ROOT/hub-token-revoke.out"
grep -q '默认不广播' "$ROOT/hub-status.out"
grep -q '转发拓扑' "$ROOT/hub-topology.out"
grep -q 'transit-hk' "$ROOT/hub-route.out"
grep -q '已下发 RelayPilot 更新' "$ROOT/hub-update.out"
grep -q 'self_update' "$ROOT/hub-tasks-after-update.out"
grep -q '"dry_run": true' "$ROOT/hub-alert-dry.out"
grep -q 'transit-hk' "$ROOT/hub-remove.out"
grep -q 'smoke-uninstall' "$ROOT/hub-removed.out"
grep -q 'MemoryMax=96M' "$ROOT/systemd/relay-smoke-agent.service"
grep -q 'CPUQuota=25%' "$ROOT/systemd/relay-smoke-agent.service"
grep -q -- '--topology-interval 300' "$ROOT/systemd/relay-smoke-agent.service"
grep -q 'RestartSec=30s' "$ROOT/systemd/relay-smoke-hub.service"
grep -q 'MemoryMax=128M' "$ROOT/systemd/relay-smoke-hub.service"
grep -q 'bot-daemon' "$ROOT/systemd/relay-smoke-tg.service"
grep -q 'CPUQuota=25%' "$ROOT/systemd/relay-smoke-tg.service"

mkdir -p "$ROOT/release/v-local" "$ROOT/raw"
cp ./relaypilot.sh "$ROOT/raw/relaypilot.sh"
cp "$ROOT/bin/relaypilot" "$ROOT/release/v-local/relaypilot_linux_amd64"
(cd "$ROOT/release/v-local" && sha256sum relaypilot_linux_amd64 > relaypilot_linux_amd64.sha256)
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
INSTALL_DIR="$ROOT/update-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh update --version v-local --no-restart-services > "$ROOT/update.out" 2> "$ROOT/update.err"
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer" \
BIN_PATH="$ROOT/bin/relaypilot-installed" \
RELAYPILOT_NO_ROOT=1 \
bash ./install-relaypilot.sh > "$ROOT/installer-noninteractive.out" 2> "$ROOT/installer-noninteractive.err"
printf '0\n' | RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer-menu" \
BIN_PATH="$ROOT/bin/relaypilot-menu" \
RELAYPILOT_NO_ROOT=1 \
bash ./install-relaypilot.sh menu > "$ROOT/installer-menu.out" 2> "$ROOT/installer-menu.err"
INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install > "$ROOT/install.out" 2> "$ROOT/install.err"

grep -q '已更新 RelayPilot' "$ROOT/update.out"
[[ -x "$ROOT/update-dir/relaypilot.sh" ]]
[[ -x "$ROOT/update-dir/bin/relaypilot" ]]
[[ -L "$ROOT/bin/relaypilot-updated" || -x "$ROOT/bin/relaypilot-updated" ]]
grep -q 'Installed entrypoint' "$ROOT/installer-noninteractive.out"
! grep -q '^RelayPilot$' "$ROOT/installer-noninteractive.out"
grep -q 'RelayPilot' "$ROOT/installer-menu.out"
grep -q 'Hub 模式' "$ROOT/installer-menu.out"
[[ -x "$ROOT/install-dir/relaypilot.sh" ]]
[[ -L "$ROOT/bin/relaypilot-self" || -x "$ROOT/bin/relaypilot-self" ]]

INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
STATE_DIR="$ROOT/state" \
KEEP_STATE=1 \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh uninstall > "$ROOT/uninstall.out" 2> "$ROOT/uninstall.err"
[[ ! -e "$ROOT/install-dir" ]]
[[ ! -e "$ROOT/bin/relaypilot-self" ]]
[[ -d "$ROOT/state" ]]

[[ -f "$ROOT/config.json" ]]
grep -q '"protocol": "shadowsocks"' "$ROOT/state/endpoints/hk.json"
grep -q '"tag": "landing-hk-ss"' "$ROOT/state/endpoints/hk.json"
grep -q '44444444-4444-4444-8444-444444444444' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '"private_key"' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '0123456789abcdef' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '"type": "shadowsocks"' "$ROOT/transit-conf/90-relaypilot-outbounds.json"
grep -q 'landing-hk-ss' "$ROOT/transit-conf/90-relaypilot-outbounds.json"
grep -q '"auth_user"' "$ROOT/transit-conf/91-relaypilot-route.json"
grep -q '"hk"' "$ROOT/transit-conf/91-relaypilot-route.json"
! grep -q 'systemctl' "$ROOT/stub.log"
echo 'relaypilot smoke: OK'
