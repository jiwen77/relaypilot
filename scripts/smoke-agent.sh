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

cat > "$ROOT/landing-socks.in" <<'EOF_INPUT'
la-direct
198.51.100.20
::
1080
2080
sub2api
secret-pass
socks-in
landing-la-direct-socks
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/socks-state" \
SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
bash ./relaypilot.sh landing-install-socks < "$ROOT/landing-socks.in" > "$ROOT/landing-socks.out" 2> "$ROOT/landing-socks.err"

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
mkdir -p "$ROOT/ip-state"
cat > "$ROOT/ip-state/agent-enrollment.json" <<EOF_AGENT_ENROLLMENT
{
  "hub_url": "https://hub.example:8443",
  "agent_id": "transit-hk",
  "role": "transit",
  "token_file": "$ROOT/ip-state/agent-token",
  "ca_cert": "$ROOT/ip-state/hub-ca.crt",
  "client_cert": "$ROOT/ip-state/agent.crt",
  "client_key": "$ROOT/ip-state/agent.key",
  "created_at": 1
}
EOF_AGENT_ENROLLMENT
STATE_DIR="$ROOT/ip-state" RELAYPILOT_NO_ROOT=1 bash ./relaypilot.sh agent ip-mode \
  --mode dynamic \
  --public-ip-interval 1800 > "$ROOT/agent-ip-mode.out"

STATE_DIR="$ROOT/state" bash ./relaypilot.sh public-entry-set \
  --use shadowsocks \
  --name hk \
  --host front.example \
  --public-port 443 \
  --local-port 2443 > "$ROOT/public-entry-set.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh public-entry-set \
  --use wireguard \
  --name hk \
  --host front.example \
  --public-port 51820 \
  --local-port 50123 \
  --network udp > "$ROOT/public-entry-wg.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh public-entry-list > "$ROOT/public-entry-list.out"

printf '2\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/agent-menu.out"
printf '1\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/hub-menu.out"
printf '3\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/service-menu.out"
printf '3\n2\n0\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/hub-service-menu.out"
printf '4\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/uninstall-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-direct-menu.out"
printf '2\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh > "$ROOT/agent-menu-connected.out"
printf '2\n6\n0\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh > "$ROOT/agent-advanced-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-direct-connected.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh landing > "$ROOT/landing-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh transit > "$ROOT/transit-menu.out"

STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-init-tls --host hub.example > "$ROOT/hub-tls.out"
printf '2\nsmoke-interactive\n10\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh hub-enroll > "$ROOT/hub-enroll.out"
printf '1\nsmoke-url\n10\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" HUB_PUBLIC_HOST="https://hub.example:9443" \
  bash ./relaypilot.sh hub-enroll > "$ROOT/hub-enroll-url.out"
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
printf '\n\n\nn\n' | SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/quick-hub-cancel" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
RELAYPILOT_NO_ROOT=1 \
HUB_PUBLIC_HOST="https://hub.cancel.example:9443" \
RELAYPILOT_PROFILE=tiny \
bash ./relaypilot.sh hub-quick-setup > "$ROOT/hub-quick-cancel.out" 2> "$ROOT/hub-quick-cancel.err"
printf '\n\n\ny\nn\n' | SYSTEMD_DIR="$ROOT/systemd" \
BIN_PATH="$ROOT/bin/relaypilot" \
STATE_DIR="$ROOT/quick-hub" \
CONF_DIR="$ROOT/transit-conf" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
RELAYPILOT_NO_ROOT=1 \
HUB_PUBLIC_HOST="https://hub.quick.example:9443" \
RELAYPILOT_PROFILE=tiny \
bash ./relaypilot.sh hub-quick-setup > "$ROOT/hub-quick.out" 2> "$ROOT/hub-quick.err"
printf '1\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh > "$ROOT/hub-menu-ready.out"
printf '1\n4\n0\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh > "$ROOT/hub-agents-menu.out"
printf '1\n8\n0\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh > "$ROOT/hub-advanced-menu.out"
printf '1\n5\n0\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh > "$ROOT/hub-telegram-menu.out"
printf '\nstored-transit\n\n\n10m\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh hub-enroll > "$ROOT/hub-enroll-stored-default.out" 2>&1
rm -f "$ROOT/quick-hub/hub-public.env"
printf '\ninferred-transit\n\n\n10m\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" SYSTEMD_DIR="$ROOT/systemd" \
  bash ./relaypilot.sh hub-enroll > "$ROOT/hub-enroll-inferred-default.out" 2>&1
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
  --conf "$ROOT/transit-conf" \
  --ip-mode dynamic \
  --public-ip-interval 600 > "$ROOT/agent-service.out" 2> "$ROOT/agent-service.err"

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
STATE_DIR="$ROOT/hub-only-state" \
CONF_DIR="$ROOT/missing-sing-box/conf" \
SINGBOX_CONFIG_PATH="$ROOT/missing-sing-box/config.json" \
HUB_SERVICE_NAME="relay-smoke-hub-no-singbox" \
RELAYPILOT_PROFILE=small \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install-hub-service \
  --host 127.0.0.1 \
  --port 18080 > "$ROOT/hub-service-no-singbox.out" 2> "$ROOT/hub-service-no-singbox.err"

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
grep -q '/relaypilot' "$ROOT/tg-hub-commands.out"
if grep -q '/relaypilot_panel\|/relaypilot_status\|/relaypilot_up\|/relaypilot_link\|/relaypilot_update\|/relaypilot_decommission\|/relaypilot_tasks' "$ROOT/tg-hub-commands.out"; then
  echo "Hub Telegram command menu should stay minimal; advanced actions belong in panel/manual commands" >&2
  exit 1
fi
grep -q '/relaypilot_status' "$ROOT/bot-commands.out"
grep -q 'setMyCommands' "$ROOT/tg-register.out"
! grep -q 'SMOKE_TOKEN' "$ROOT/tg-register.out"
grep -q 'dry_run' "$ROOT/tg-send.out"
! grep -q 'SMOKE_TOKEN' "$ROOT/tg-send.out"
grep -q 'landing-hk-ss' "$ROOT/tg-dispatch.out"

grep -q 'RelayPilot v.* · 当前：未配置' "$ROOT/agent-menu.out"
grep -q 'Hub：○ 未启用.*Agent：○ 未启用.*代理：○ 未启用' "$ROOT/agent-menu.out"
grep -q 'Hub 模式' "$ROOT/agent-menu.out"
grep -q 'Agent 模式' "$ROOT/agent-menu.out"
grep -q '卸载 RelayPilot' "$ROOT/agent-menu.out"
grep -q '卸载 RelayPilot（保留状态/代理）' "$ROOT/uninstall-menu.out"
grep -q '彻底卸载（含状态/代理）' "$ROOT/uninstall-menu.out"
grep -q 'Agent 尚未接入 Hub' "$ROOT/agent-menu.out"
grep -q '接入 Hub' "$ROOT/agent-menu.out"
grep -q '配置中转 Reality' "$ROOT/agent-menu.out"
grep -q '配置落地出口' "$ROOT/agent-menu.out"
if grep -q '接入信息' "$ROOT/agent-menu.out" || grep -q '退出 Hub 托管' "$ROOT/agent-menu.out" || grep -q '重置 Agent' "$ROOT/agent-menu.out"; then
  echo "unenrolled Agent mode should hide connected-only and destructive actions" >&2
  exit 1
fi
grep -q 'Agent 已接入：transit-hk · 中转' "$ROOT/agent-menu-connected.out"
grep -q '中转节点' "$ROOT/agent-menu-connected.out"
grep -q '接入信息' "$ROOT/agent-menu-connected.out"
grep -q 'IP 模式' "$ROOT/agent-menu-connected.out"
grep -q '公网入口' "$ROOT/agent-menu-connected.out"
grep -q '高级操作' "$ROOT/agent-menu-connected.out"
if grep -q '退出 Hub 托管' "$ROOT/agent-menu-connected.out" || grep -q '重置 Agent' "$ROOT/agent-menu-connected.out"; then
  echo "connected Agent mode should keep destructive actions under Advanced" >&2
  exit 1
fi
grep -q 'Hub 尚未初始化' "$ROOT/hub-menu.out"
grep -q '初始化 Hub' "$ROOT/hub-menu.out"
if grep -q '生成邀请码' "$ROOT/hub-menu.out" || grep -q '串联节点' "$ROOT/hub-menu.out"; then
  echo "uninitialized Hub mode should only offer initialization/back, not operational actions" >&2
  exit 1
fi
grep -q '"ip_mode": "dynamic"' "$ROOT/ip-state/agent-enrollment.json"
grep -q '"public_ip_interval_seconds": 1800' "$ROOT/ip-state/agent-enrollment.json"
grep -q '"host": "front.example"' "$ROOT/state/public-entries.json"
grep -q '"public_port": 51820' "$ROOT/state/public-entries.json"
grep -q 'shadowsocks.*hk.*front.example:443' "$ROOT/public-entry-list.out"
grep -q 'wireguard.*hk.*front.example:51820' "$ROOT/public-entry-list.out"
grep -q 'Hub 模式' "$ROOT/hub-menu-ready.out"
grep -q '生成邀请码' "$ROOT/hub-menu-ready.out"
grep -q '串联节点' "$ROOT/hub-menu-ready.out"
grep -q '最近操作' "$ROOT/hub-menu-ready.out"
grep -q 'Telegram' "$ROOT/hub-menu-ready.out"
grep -q '高级操作' "$ROOT/hub-menu-ready.out"
if grep -q '任务队列' "$ROOT/hub-menu-ready.out" || grep -q '恢复超时任务' "$ROOT/hub-menu-ready.out" || grep -q '重置 Hub' "$ROOT/hub-menu-ready.out"; then
  echo "Hub main menu should expose results, not raw task internals" >&2
  exit 1
fi
grep -q 'Hub 高级操作' "$ROOT/hub-advanced-menu.out"
grep -q '初始化/修改 Hub 配置' "$ROOT/hub-advanced-menu.out"
grep -q '任务队列' "$ROOT/hub-advanced-menu.out"
grep -q '恢复超时任务' "$ROOT/hub-advanced-menu.out"
grep -q '远程退役节点' "$ROOT/hub-advanced-menu.out"
grep -q '重置 Hub' "$ROOT/hub-advanced-menu.out"
grep -q '发送测试' "$ROOT/hub-telegram-menu.out"
grep -q 'Agent 高级操作' "$ROOT/agent-advanced-menu.out"
grep -q '远程退役授权' "$ROOT/agent-advanced-menu.out"
grep -q '退出 Hub 托管' "$ROOT/agent-advanced-menu.out"
grep -q '重置 Agent' "$ROOT/agent-advanced-menu.out"
grep -q '节点列表' "$ROOT/hub-agents-menu.out"
grep -q '刷新单个节点详情' "$ROOT/hub-agents-menu.out"
grep -q '刷新全部节点详情' "$ROOT/hub-agents-menu.out"
grep -q '本机服务' "$ROOT/service-menu.out"
grep -q '状态 / 启动' "$ROOT/service-menu.out"
grep -q '资源限制' "$ROOT/service-menu.out"
grep -q '更新 RelayPilot' "$ROOT/service-menu.out"
grep -q 'relaypilot-agent' "$ROOT/service-menu.out"
grep -q 'Hub 服务' "$ROOT/hub-service-menu.out"
if grep -q '清除失败状态' "$ROOT/hub-service-menu.out"; then
  echo "service menu should hide systemd reset-failed as an automatic recovery detail" >&2
  exit 1
fi
grep -q 'Agent 模式' "$ROOT/agent-direct-menu.out"
grep -q 'Agent 尚未接入 Hub' "$ROOT/agent-direct-menu.out"
grep -q '配置中转 Reality' "$ROOT/agent-direct-menu.out"
grep -q 'Agent 模式' "$ROOT/agent-direct-connected.out"
grep -q 'Agent 已接入：transit-hk · 中转' "$ROOT/agent-direct-connected.out"
grep -q '中转节点' "$ROOT/agent-direct-connected.out"
grep -q '安装/更新 SOCKS5' "$ROOT/landing-menu.out"
grep -q '运行状态' "$ROOT/landing-menu.out"
if grep -q 'Endpoints' "$ROOT/landing-menu.out"; then
  echo "landing menu should not expose debug-only Endpoints entry" >&2
  exit 1
fi
grep -q '初始化/更新 Reality' "$ROOT/transit-menu.out"
grep -q '绑定出口' "$ROOT/transit-menu.out"
grep -q '运行状态' "$ROOT/transit-menu.out"
if grep -q 'Endpoints' "$ROOT/transit-menu.out"; then
  echo "transit menu should not expose debug-only Endpoints entry" >&2
  exit 1
fi
! grep -q 'Advanced' "$ROOT/hub-menu.out"
! grep -q '不会 fanout' "$ROOT/hub-menu.out"

grep -q 'smoke-interactive' "$ROOT/hub-enroll.out"
grep -q 'hub.example' "$ROOT/hub-enroll.out"
grep -q '^  1) 中转节点' "$ROOT/hub-enroll.out"
grep -q '^  2) 落地节点' "$ROOT/hub-enroll.out"
grep -q '^  选择序号 \[默认：1\]:' "$ROOT/hub-enroll.out"
! grep -q '^选择序号 \[默认：transit\]:' "$ROOT/hub-enroll.out"
grep -q '邀请码有效期（分钟）' "$ROOT/hub-enroll.out"
grep -q 'Agent 邀请码已生成' "$ROOT/hub-enroll.out"
grep -q '有效期：10 分钟' "$ROOT/hub-enroll.out"
grep -q '待接入' "$ROOT/hub-enroll.out"
grep -q '安装命令' "$ROOT/hub-enroll.out"
grep -q -- '--enroll' "$ROOT/hub-enroll.out"
! grep -q '"invite"' "$ROOT/hub-enroll.out"
grep -q 'Hub：https://hub.example:9443' "$ROOT/hub-enroll-url.out"
! grep -q 'https://https://' "$ROOT/hub-enroll-url.out"
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
grep -q 'Hub 配置预览' "$ROOT/hub-quick-cancel.out"
grep -q '未写入任何 Hub 配置' "$ROOT/hub-quick-cancel.out"
[[ ! -e "$ROOT/quick-hub-cancel/hub-tls/ca.crt" ]]
grep -q 'Hub 配置预览' "$ROOT/hub-quick.out"
grep -q 'Hub URL 给 agent 使用：https://hub.quick.example:9443' "$ROOT/hub-quick.out"
grep -q '证书 SAN 包含：hub.quick.example' "$ROOT/hub-quick.out"
grep -q '是否现在启动 relaypilot-hub \[Y/n\]' "$ROOT/hub-quick.out"
grep -q '配置 Telegram 状态面板 \[y/N\]' "$ROOT/hub-quick.out"
grep -q 'Hub URL： https://hub.quick.example:9443' "$ROOT/hub-enroll-stored-default.out"
grep -q '默认：' "$ROOT/hub-enroll-stored-default.out"
grep -q 'Hub：https://hub.quick.example:9443' "$ROOT/hub-enroll-stored-default.out"
if grep -q 'Hub 公网 IP/域名' "$ROOT/hub-enroll-stored-default.out" || grep -q 'Hub HTTPS 端口' "$ROOT/hub-enroll-stored-default.out"; then
  echo "stored Hub public URL should be reused without prompting for host/port" >&2
  exit 1
fi
grep -q 'Hub URL： https://hub.quick.example:9443' "$ROOT/hub-enroll-inferred-default.out"
grep -q 'Hub：https://hub.quick.example:9443' "$ROOT/hub-enroll-inferred-default.out"
if grep -q 'Hub 公网 IP/域名' "$ROOT/hub-enroll-inferred-default.out" || grep -q 'Hub HTTPS 端口' "$ROOT/hub-enroll-inferred-default.out"; then
  echo "existing Hub TLS/service should infer Hub public URL without prompting for host/port" >&2
  exit 1
fi
grep -q 'hub-daemon' "$ROOT/systemd/relaypilot-hub.service"
grep -q -- '--port 9443' "$ROOT/systemd/relaypilot-hub.service"
grep -q -- '--host 0.0.0.0' "$ROOT/systemd/relaypilot-hub.service"
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
grep -q '巡检：面板 → 刷新节点详情' "$ROOT/hub-status.out"
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
grep -q -- '--ip-mode dynamic' "$ROOT/systemd/relay-smoke-agent.service"
grep -q -- '--public-ip-interval 600' "$ROOT/systemd/relay-smoke-agent.service"
grep -q 'RestartSec=30s' "$ROOT/systemd/relay-smoke-hub.service"
grep -q 'MemoryMax=128M' "$ROOT/systemd/relay-smoke-hub.service"
grep -q 'relay-smoke-hub-no-singbox.service' "$ROOT/hub-service-no-singbox.out"
if grep -q "$ROOT/missing-sing-box" "$ROOT/systemd/relay-smoke-hub-no-singbox.service"; then
  echo "hub-only service should not include missing sing-box paths in ReadWritePaths" >&2
  exit 1
fi
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
mkdir -p "$ROOT/fake-update-bin"
cat > "$ROOT/fake-update-bin/systemctl" <<'EOF_SYSTEMCTL'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "${RELAYPILOT_FAKE_SYSTEMCTL_LOG:?}"
exit 0
EOF_SYSTEMCTL
chmod +x "$ROOT/fake-update-bin/systemctl"
printf '\n' | RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
INSTALL_DIR="$ROOT/update-default-restart-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated-default-restart" \
RELAYPILOT_NO_ROOT=1 \
RELAYPILOT_FAKE_SYSTEMCTL_LOG="$ROOT/update-default-restart-systemctl.log" \
PATH="$ROOT/fake-update-bin:$PATH" \
bash ./relaypilot.sh update --version v-local > "$ROOT/update-default-restart.out" 2> "$ROOT/update-default-restart.err"
grep -q '是否重启已安装的 RelayPilot 服务以应用新版本 \[Y/n\]' "$ROOT/update-default-restart.out"
grep -q 'systemctl restart relaypilot-agent' "$ROOT/update-default-restart-systemctl.log"
grep -q 'systemctl restart relaypilot-hub' "$ROOT/update-default-restart-systemctl.log"
grep -q 'systemctl restart relaypilot-bot' "$ROOT/update-default-restart-systemctl.log"
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer" \
BIN_PATH="$ROOT/bin/relaypilot-installed" \
RELAYPILOT_NO_ROOT=1 \
bash ./install-relaypilot.sh > "$ROOT/installer-noninteractive.out" 2> "$ROOT/installer-noninteractive.err"

mkdir -p "$ROOT/raw-enroll" "$ROOT/release-enroll/v-local"
cat > "$ROOT/raw-enroll/relaypilot.sh" <<'EOF_STUB_ENTRYPOINT'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${RELAYPILOT_INSTALL_DISPATCH_LOG:?}"
EOF_STUB_ENTRYPOINT
chmod +x "$ROOT/raw-enroll/relaypilot.sh"
cat > "$ROOT/release-enroll/v-local/relaypilot_linux_amd64" <<'EOF_STUB_CORE'
#!/usr/bin/env bash
exit 0
EOF_STUB_CORE
chmod +x "$ROOT/release-enroll/v-local/relaypilot_linux_amd64"
(cd "$ROOT/release-enroll/v-local" && sha256sum relaypilot_linux_amd64 > relaypilot_linux_amd64.sha256)
RAW_BASE="file://$ROOT/raw-enroll" \
RELEASE_BASE="file://$ROOT/release-enroll" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer-enroll-auto" \
BIN_PATH="$ROOT/bin/relaypilot-enroll-auto" \
RELAYPILOT_NO_ROOT=1 \
RELAYPILOT_INSTALL_DISPATCH_LOG="$ROOT/install-enroll-auto.log" \
RELAYPILOT_INSTALL_ENROLL_MODE=auto \
bash ./install-relaypilot.sh --enroll 'INVITE_SMOKE' > "$ROOT/installer-enroll-auto.out" 2> "$ROOT/installer-enroll-auto.err"
RAW_BASE="file://$ROOT/raw-enroll" \
RELEASE_BASE="file://$ROOT/release-enroll" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer-enroll-join" \
BIN_PATH="$ROOT/bin/relaypilot-enroll-join" \
RELAYPILOT_NO_ROOT=1 \
RELAYPILOT_INSTALL_DISPATCH_LOG="$ROOT/install-enroll-join.log" \
RELAYPILOT_INSTALL_ENROLL_MODE=join \
bash ./install-relaypilot.sh --enroll 'INVITE_SMOKE' > "$ROOT/installer-enroll-join.out" 2> "$ROOT/installer-enroll-join.err"

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
grep -q '^agent enroll --invite INVITE_SMOKE --install-service$' "$ROOT/install-enroll-auto.log"
grep -q '^agent join --invite INVITE_SMOKE$' "$ROOT/install-enroll-join.log"
grep -q 'RelayPilot' "$ROOT/installer-menu.out"
grep -q 'Hub 模式' "$ROOT/installer-menu.out"
[[ -x "$ROOT/install-dir/relaypilot.sh" ]]
[[ -L "$ROOT/bin/relaypilot-self" || -x "$ROOT/bin/relaypilot-self" ]]

INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
STATE_DIR="$ROOT/state" \
KEEP_STATE=1 \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh uninstall --yes > "$ROOT/uninstall.out" 2> "$ROOT/uninstall.err"
[[ ! -e "$ROOT/install-dir" ]]
[[ ! -e "$ROOT/bin/relaypilot-self" ]]
[[ -d "$ROOT/state" ]]

[[ -f "$ROOT/config.json" ]]
grep -q '"protocol": "shadowsocks"' "$ROOT/state/endpoints/hk.json"
grep -q '"tag": "landing-hk-ss"' "$ROOT/state/endpoints/hk.json"
[[ -f "$ROOT/socks-config.json" ]]
grep -q '"type": "socks"' "$ROOT/socks-config.json"
grep -q '"protocol": "socks"' "$ROOT/socks-state/endpoints/la-direct.json"
grep -q '"tag": "landing-la-direct-socks"' "$ROOT/socks-state/endpoints/la-direct.json"
grep -q '44444444-4444-4444-8444-444444444444' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '"private_key"' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '0123456789abcdef' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '"type": "shadowsocks"' "$ROOT/transit-conf/90-relaypilot-outbounds.json"
grep -q 'landing-hk-ss' "$ROOT/transit-conf/90-relaypilot-outbounds.json"
grep -q '"auth_user"' "$ROOT/transit-conf/91-relaypilot-route.json"
grep -q '"hk"' "$ROOT/transit-conf/91-relaypilot-route.json"
! grep -q 'systemctl' "$ROOT/stub.log"
echo 'relaypilot smoke: OK'
