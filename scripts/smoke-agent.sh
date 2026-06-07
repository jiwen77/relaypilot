#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT="$(mktemp -d /tmp/relaypilot-smoke.XXXXXX)"
cleanup() { rm -rf "$ROOT"; }
trap cleanup EXIT
mkdir -p "$ROOT/bin" "$ROOT/state" "$ROOT/migrated-state" "$ROOT/transit-conf" "$ROOT/systemd"

text_offset() {
  local pattern="$1" file="$2"
  grep -abo "$pattern" "$file" | head -n1 | cut -d: -f1
}

assert_text_before() {
  local first="$1" second="$2" file="$3" first_offset second_offset
  first_offset="$(text_offset "$first" "$file")"
  second_offset="$(text_offset "$second" "$file")"
  if [[ -z "$first_offset" || -z "$second_offset" || "$first_offset" -ge "$second_offset" ]]; then
    echo "expected '$first' before '$second' in $file" >&2
    cat "$file" >&2
    exit 1
  fi
}

assert_path_absent() {
  local path="$1"
  if [[ -e "$path" || -L "$path" ]]; then
    echo "expected path to be absent: $path" >&2
    ls -l "$path" >&2 || true
    exit 1
  fi
}

json_string_field() {
  local key="$1" file="$2"
  grep -m1 "\"$key\"" "$file" | sed -E 's/^[^:]+:[[:space:]]*"([^"]*)".*$/\1/'
}

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
      exit 0
    fi
    exit 0
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
  daemon-reload|enable|restart|start|stop)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
STUB
chmod +x "$ROOT/bin/systemctl"

cat > "$ROOT/landing.in" <<'EOF_INPUT'
hk
::
2443
1

203.0.113.10
443
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
bash ./relaypilot.sh landing-install-ss < "$ROOT/landing.in" > "$ROOT/landing.out" 2> "$ROOT/landing.err"
cp "$ROOT/config.json" "$ROOT/config.before.json"
cp "$ROOT/state/endpoints/hk.json" "$ROOT/hk.endpoint.before.json"

printf '\n\n\n\n\n\n\n' > "$ROOT/landing-update.in"

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
SINGBOX_CONFIG_PATH="$ROOT/config.json" \
bash ./relaypilot.sh landing-install-ss < "$ROOT/landing-update.in" > "$ROOT/landing-update.out" 2> "$ROOT/landing-update.err"
if ! cmp -s "$ROOT/config.before.json" "$ROOT/config.json" || ! cmp -s "$ROOT/hk.endpoint.before.json" "$ROOT/state/endpoints/hk.json" || [[ -e "$ROOT/state/endpoints/landing.json" ]]; then
  echo "Shadowsocks update should reuse existing defaults when accepted" >&2
  exit 1
fi

cat > "$ROOT/landing-socks.in" <<'EOF_INPUT'
la-direct
::
1080
sub2api
secret-pass
198.51.100.20
2080
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/socks-state" \
SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
bash ./relaypilot.sh landing-install-socks < "$ROOT/landing-socks.in" > "$ROOT/landing-socks.out" 2> "$ROOT/landing-socks.err"
cp "$ROOT/socks-config.json" "$ROOT/socks-config.before.json"
cp "$ROOT/socks-state/endpoints/la-direct.json" "$ROOT/la-direct.endpoint.before.json"

printf '\n\n\n\n\n\n\n' > "$ROOT/landing-socks-update.in"

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/socks-state" \
SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
bash ./relaypilot.sh landing-install-socks < "$ROOT/landing-socks-update.in" > "$ROOT/landing-socks-update.out" 2> "$ROOT/landing-socks-update.err"
if ! cmp -s "$ROOT/socks-config.before.json" "$ROOT/socks-config.json" || ! cmp -s "$ROOT/la-direct.endpoint.before.json" "$ROOT/socks-state/endpoints/la-direct.json" || [[ -e "$ROOT/socks-state/endpoints/landing-direct.json" ]]; then
  echo "SOCKS update should reuse existing defaults when accepted" >&2
  exit 1
fi

TRANSIT_PRIVATE_KEY_SMOKE="uBQcxiMI5t1rHtw2iWO2ldhclClTHFWb0ppUIK0vce8"
cat > "$ROOT/transit-init.in" <<EOF_INPUT
0.0.0.0
8443
www.example.com


203.0.113.30
443
EOF_INPUT

RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
TRANSIT_PRIVATE_KEY="$TRANSIT_PRIVATE_KEY_SMOKE" \
TRANSIT_SHORT_ID=0123456789abcdef \
bash ./relaypilot.sh transit-init-reality < "$ROOT/transit-init.in" > "$ROOT/transit-init.out" 2> "$ROOT/transit-init.err"
cp "$ROOT/transit-conf/00-relaypilot-reality.json" "$ROOT/reality.before.json"
printf '\n\n\n\n\n\n\n' > "$ROOT/transit-init-update.in"
RELAYPILOT_NO_ROOT=1 \
SKIP_SINGBOX_INSTALL=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
bash ./relaypilot.sh transit-init-reality < "$ROOT/transit-init-update.in" > "$ROOT/transit-init-update.out" 2> "$ROOT/transit-init-update.err"
if ! cmp -s "$ROOT/reality.before.json" "$ROOT/transit-conf/00-relaypilot-reality.json"; then
  echo "Reality update should reuse existing defaults when accepted" >&2
  exit 1
fi

cat > "$ROOT/transit.in" <<EOF_INPUT
$ROOT/state/endpoints/hk.json
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
cp "$ROOT/transit-conf/00-relaypilot-reality.json" "$ROOT/reality.bound.before.json"
cp "$ROOT/transit-conf/91-relaypilot-route.json" "$ROOT/route.bound.before.json"
printf '\n\n\n' > "$ROOT/transit-update.in"
RELAYPILOT_NO_ROOT=1 \
NO_RESTART=1 \
PATH="$ROOT/bin:$PATH" \
RELAYPILOT_STUB_LOG="$ROOT/stub.log" \
STATE_DIR="$ROOT/state" \
CONF_DIR="$ROOT/transit-conf" \
bash ./relaypilot.sh transit-import-bind < "$ROOT/transit-update.in" > "$ROOT/transit-update.out" 2> "$ROOT/transit-update.err"
if ! cmp -s "$ROOT/reality.bound.before.json" "$ROOT/transit-conf/00-relaypilot-reality.json" || ! cmp -s "$ROOT/route.bound.before.json" "$ROOT/transit-conf/91-relaypilot-route.json"; then
  echo "Transit bind update should reuse existing endpoint/client defaults when accepted" >&2
  exit 1
fi

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
STATE_DIR="$ROOT/state" CONF_DIR="$ROOT/transit-conf" bash ./relaypilot.sh connection-info > "$ROOT/connection-info.out"
STATE_DIR="$ROOT/socks-state" SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" bash ./relaypilot.sh connection-info "$ROOT/socks-config.json" > "$ROOT/socks-connection-info.out"

printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/install-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-menu.out"
printf '2\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-proxy-config-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh hub > "$ROOT/hub-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh services > "$ROOT/service-menu.out"
printf '2\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh services > "$ROOT/hub-service-menu.out"
printf '3\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh > "$ROOT/uninstall-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-direct-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-menu-connected.out"
printf '3\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-network-menu.out"
printf '4\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-service-missing-menu.out"
printf '5\n1\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-advanced-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/ip-state" \
  bash ./relaypilot.sh agent > "$ROOT/agent-direct-connected.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 \
  PATH="$ROOT/bin:$PATH" \
  RELAYPILOT_STUB_SYSTEMCTL_UNITS="sing-box.service" \
  RELAYPILOT_STUB_SYSTEMCTL_ACTIVE_UNITS="sing-box.service sing-box" \
  STATE_DIR="$ROOT/socks-state" \
  SINGBOX_CONFIG_PATH="$ROOT/socks-config.json" \
  bash ./relaypilot.sh agent > "$ROOT/agent-standalone-proxy-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh landing > "$ROOT/landing-menu.out"
printf '0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh transit > "$ROOT/transit-menu.out"
printf '2\nhk-entry\nfront.example\n443\n2443\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/state" \
  bash ./relaypilot.sh public-entry > "$ROOT/public-entry-wizard.out"

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
STATE_DIR="$ROOT/state" CONF_DIR="$ROOT/transit-conf" bash ./relaypilot.sh hub-agent-export \
  --agent-id transit-hk \
  --role transit \
  --name "HK Transit" \
  --conf "$ROOT/transit-conf" \
  --output "$ROOT/quick-transit.registration.json" > "$ROOT/quick-transit-export.out"
STATE_DIR="$ROOT/state" bash ./relaypilot.sh hub-agent-export \
  --agent-id landing-hk \
  --role landing \
  --name "HK Landing" \
  --conf "$ROOT/config.json" \
  --output "$ROOT/quick-landing.registration.json" > "$ROOT/quick-landing-export.out"
STATE_DIR="$ROOT/quick-hub" bash ./relaypilot.sh hub-import-agent "$ROOT/quick-transit.registration.json" > "$ROOT/quick-transit-import.out"
STATE_DIR="$ROOT/quick-hub" bash ./relaypilot.sh hub-import-agent "$ROOT/quick-landing.registration.json" > "$ROOT/quick-landing-import.out"
printf '1\n0\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh > "$ROOT/hub-menu-ready.out"
printf '2\n1\n1\n1\n\n\n0\n' | RELAYPILOT_NO_ROOT=1 STATE_DIR="$ROOT/quick-hub" \
  bash ./relaypilot.sh hub > "$ROOT/hub-link-wizard.out"
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

grep -q 'RelayPilot 安装 v.* · 当前：未配置' "$ROOT/install-menu.out"
grep -q '安装/进入 Hub' "$ROOT/install-menu.out"
grep -q '安装/进入 Agent' "$ROOT/install-menu.out"
grep -q '卸载 RelayPilot' "$ROOT/install-menu.out"
if grep -q '本机服务' "$ROOT/install-menu.out"; then
  echo "relaypilot install menu should not expose service management" >&2
  exit 1
fi
if grep -q '^ *[0-9]) Hub 模式\|^ *[0-9]) Agent 模式' "$ROOT/install-menu.out"; then
  echo "relaypilot default menu should be install-focused, not role mode navigation" >&2
  exit 1
fi
grep -q 'Agent 模式 v.* · 当前：未配置' "$ROOT/agent-menu.out"
grep -q 'Hub：○ 未启用.*Agent：○ 未启用.*代理：○ 未启用' "$ROOT/agent-menu.out"
grep -q '卸载 RelayPilot（保留状态/代理）' "$ROOT/uninstall-menu.out"
grep -q '彻底卸载（含状态/代理）' "$ROOT/uninstall-menu.out"
grep -q 'Agent 尚未接入 Hub' "$ROOT/agent-menu.out"
grep -q '接入 Hub' "$ROOT/agent-menu.out"
grep -q '代理配置' "$ROOT/agent-menu.out"
grep -q '连接信息' "$ROOT/agent-menu.out"
if grep -q '接入信息\|退出 Hub 托管\|重置 Agent\|配置中转 Reality\|配置落地出口' "$ROOT/agent-menu.out"; then
  echo "unenrolled Agent mode should hide connected-only and destructive actions" >&2
  exit 1
fi
grep -q '代理配置' "$ROOT/agent-proxy-config-menu.out"
grep -q '配置中转 Reality' "$ROOT/agent-proxy-config-menu.out"
grep -q '配置落地出口' "$ROOT/agent-proxy-config-menu.out"
grep -q 'Agent 已接入：transit-hk · 中转' "$ROOT/agent-menu-connected.out"
grep -q 'Agent：○ 已接入/服务未安装' "$ROOT/agent-menu-connected.out"
grep -q '代理配置' "$ROOT/agent-menu-connected.out"
grep -q '网络设置' "$ROOT/agent-menu-connected.out"
grep -q '连接信息' "$ROOT/agent-menu-connected.out"
grep -q '高级操作' "$ROOT/agent-menu-connected.out"
if grep -q 'Hub 接入信息\|IP 模式\|公网入口' "$ROOT/agent-menu-connected.out"; then
  echo "Hub enrollment and detailed network settings should not live in the Agent main menu" >&2
  exit 1
fi
grep -q 'Agent 网络设置' "$ROOT/agent-network-menu.out"
grep -q 'IP 模式' "$ROOT/agent-network-menu.out"
grep -q '公网入口' "$ROOT/agent-network-menu.out"
grep -q 'Agent 已接入 Hub，但后台服务未安装' "$ROOT/agent-service-missing-menu.out"
grep -q '安装/修复 Agent 服务' "$ROOT/agent-service-missing-menu.out"
if grep -q 'Agent：○ 未安装' "$ROOT/agent-menu-connected.out" "$ROOT/agent-direct-connected.out" "$ROOT/agent-service-missing-menu.out"; then
  echo "enrolled Agent should not be summarized as simply 未安装" >&2
  exit 1
fi
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
grep -q '"host": "203.0.113.30"' "$ROOT/state/public-entries.json"
grep -q '"public_port": 443' "$ROOT/state/public-entries.json"
grep -q '"local_port": 8443' "$ROOT/state/public-entries.json"
grep -q 'reality.*vless-in.*203.0.113.30:443' "$ROOT/public-entry-list.out"
grep -q 'shadowsocks.*hk.*front.example:443' "$ROOT/public-entry-list.out"
grep -q 'wireguard.*hk.*front.example:51820' "$ROOT/public-entry-list.out"
grep -q 'Reality / VLESS' "$ROOT/connection-info.out"
grep -q '^==================== Reality / VLESS ====================$' "$ROOT/connection-info.out"
grep -q '客户端：hk' "$ROOT/connection-info.out"
grep -q '地址：203.0.113.30:443' "$ROOT/connection-info.out"
grep -q 'UUID：44444444-4444-4444-8444-444444444444' "$ROOT/connection-info.out"
grep -q 'SNI：www.example.com' "$ROOT/connection-info.out"
grep -q 'Public Key：' "$ROOT/connection-info.out"
grep -q 'Short ID：0123456789abcdef' "$ROOT/connection-info.out"
grep -q '^==================== VLESS 分享链接 ====================$' "$ROOT/connection-info.out"
grep -q 'VLESS 分享链接' "$ROOT/connection-info.out"
grep -q '^客户端：hk$' "$ROOT/connection-info.out"
grep -q '^vless://44444444-4444-4444-8444-444444444444@203.0.113.30:443?' "$ROOT/connection-info.out"
if grep -q 'VLESS 链接：' "$ROOT/connection-info.out"; then
  echo "VLESS URI should live in a separate share-link block, not inline with parameters" >&2
  exit 1
fi
grep -q 'security=reality' "$ROOT/connection-info.out"
grep -q 'pbk=' "$ROOT/connection-info.out"
grep -q 'sid=0123456789abcdef' "$ROOT/connection-info.out"
grep -q 'sni=www.example.com' "$ROOT/connection-info.out"
grep -q '^==================== sing-box 出站 JSON ====================$' "$ROOT/connection-info.out"
grep -q '"type": "vless"' "$ROOT/connection-info.out"
grep -q '"server": "203.0.113.30"' "$ROOT/connection-info.out"
grep -q '"uuid": "44444444-4444-4444-8444-444444444444"' "$ROOT/connection-info.out"
grep -q '"reality": {' "$ROOT/connection-info.out"
grep -q '"short_id": "0123456789abcdef"' "$ROOT/connection-info.out"
grep -q '^==================== mihomo proxies YAML ====================$' "$ROOT/connection-info.out"
grep -q '^proxies:$' "$ROOT/connection-info.out"
grep -q 'name: "hk-vless"' "$ROOT/connection-info.out"
grep -q 'type: vless' "$ROOT/connection-info.out"
grep -q 'servername: "www.example.com"' "$ROOT/connection-info.out"
grep -q 'client-fingerprint: chrome' "$ROOT/connection-info.out"
grep -q 'reality-opts:' "$ROOT/connection-info.out"
grep -q 'public-key: "' "$ROOT/connection-info.out"
grep -q 'short-id: "0123456789abcdef"' "$ROOT/connection-info.out"
grep -q 'encryption: ""' "$ROOT/connection-info.out"
grep -q 'name: "hk-ss"' "$ROOT/connection-info.out"
grep -q 'type: ss' "$ROOT/connection-info.out"
grep -q 'cipher: "2022-blake3-aes-128-gcm"' "$ROOT/connection-info.out"
grep -q '^==================== 落地出口 ====================$' "$ROOT/connection-info.out"
grep -q 'Shadowsocks' "$ROOT/connection-info.out"
grep -q '密码：' "$ROOT/connection-info.out"
grep -q 'SOCKS5' "$ROOT/socks-connection-info.out"
grep -q '地址：198.51.100.20:2080' "$ROOT/socks-connection-info.out"
grep -q '用户名：sub2api' "$ROOT/socks-connection-info.out"
grep -q '密码：secret-pass' "$ROOT/socks-connection-info.out"
grep -q '^==================== mihomo proxies YAML ====================$' "$ROOT/socks-connection-info.out"
grep -q 'name: "la-direct-socks"' "$ROOT/socks-connection-info.out"
grep -q 'type: socks5' "$ROOT/socks-connection-info.out"
grep -q 'username: "sub2api"' "$ROOT/socks-connection-info.out"
grep -q 'password: "secret-pass"' "$ROOT/socks-connection-info.out"
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
grep -q '绑定/修改 Telegram' "$ROOT/hub-telegram-menu.out"
grep -q '发送测试' "$ROOT/hub-telegram-menu.out"
grep -q '修复 Telegram 面板' "$ROOT/hub-telegram-menu.out"
if grep -q '安装服务' "$ROOT/hub-telegram-menu.out" || grep -q '注册命令' "$ROOT/hub-telegram-menu.out" || grep -q '命令列表' "$ROOT/hub-telegram-menu.out" || grep -q '删除远端命令' "$ROOT/hub-telegram-menu.out" || grep -q '配置状态' "$ROOT/hub-telegram-menu.out"; then
  echo "Hub Telegram main menu should hide service/command implementation details" >&2
  exit 1
fi
grep -q 'Agent 高级操作' "$ROOT/agent-advanced-menu.out"
grep -q 'Hub 接入信息' "$ROOT/agent-advanced-menu.out"
grep -q 'Hub：https://hub.example:8443' "$ROOT/agent-advanced-menu.out"
grep -q 'Agent ID：transit-hk' "$ROOT/agent-advanced-menu.out"
grep -q '角色：中转' "$ROOT/agent-advanced-menu.out"
if grep -q '"hub_url"\|"agent_id"\|"token_file"' "$ROOT/agent-advanced-menu.out"; then
  echo "Hub enrollment info should be a readable summary, not raw JSON" >&2
  exit 1
fi
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
grep -q '代理配置' "$ROOT/agent-direct-menu.out"
grep -q '接入 Hub' "$ROOT/agent-direct-menu.out"
grep -q 'Agent 模式' "$ROOT/agent-direct-connected.out"
grep -q 'Agent 已接入：transit-hk · 中转' "$ROOT/agent-direct-connected.out"
grep -q '代理配置' "$ROOT/agent-direct-connected.out"
grep -q 'Agent 模式 v.* · 当前：本机代理' "$ROOT/agent-standalone-proxy-menu.out"
grep -q 'Hub：○ 未启用.*Agent：○ 未启用.*代理：● SOCKS5 运行中' "$ROOT/agent-standalone-proxy-menu.out"
grep -q 'Agent 尚未接入 Hub' "$ROOT/agent-standalone-proxy-menu.out"
grep -q '连接信息' "$ROOT/agent-standalone-proxy-menu.out"
grep -q '安装/更新 SOCKS5' "$ROOT/landing-menu.out"
grep -q '安装/更新 Shadowsocks' "$ROOT/landing-menu.out"
if grep -q '本机直连\|中转出口\|接入 Hub 托管' "$ROOT/agent-menu.out" "$ROOT/agent-direct-menu.out" "$ROOT/landing-menu.out"; then
  echo "menus should stay concise without explanatory suffixes" >&2
  exit 1
fi
assert_text_before '本地地址/IP' '本地端口' "$ROOT/landing-socks.out"
assert_text_before '本地端口' 'SOCKS 用户名' "$ROOT/landing-socks.out"
assert_text_before 'SOCKS 密码' '访问地址/IP' "$ROOT/landing-socks.out"
assert_text_before 'Shadowsocks 加密方式' 'Shadowsocks 密码' "$ROOT/landing.out"
assert_text_before 'Shadowsocks 密码' '访问地址/IP' "$ROOT/landing.out"
if grep -q 'inbound tag\|endpoint tag' "$ROOT/landing-socks.out" "$ROOT/landing.out"; then
  echo "normal landing install wizards should not ask for sing-box tag internals" >&2
  exit 1
fi
assert_text_before '本地地址/IP' '本地端口' "$ROOT/transit-init.out"
assert_text_before '本地端口' '伪装域名/SNI' "$ROOT/transit-init.out"
assert_text_before '伪装域名/SNI' 'Reality 私钥' "$ROOT/transit-init.out"
assert_text_before 'Reality 私钥' 'Reality short_id' "$ROOT/transit-init.out"
assert_text_before 'Reality short_id' '访问地址/IP' "$ROOT/transit-init.out"
assert_text_before '访问地址/IP' '访问端口' "$ROOT/transit-init.out"
if grep -q 'sing-box 配置目录\|Reality inbound tag\|VLESS Reality inbound tag\|Reality 握手目标\|Reality 握手端口\|Transit agent\|auth_user\|server_name' "$ROOT/transit-init.out" "$ROOT/transit.out"; then
  echo "normal transit wizards should not ask for sing-box path/tag/handshake internals" >&2
  exit 1
fi
assert_text_before '落地出口 JSON 文件路径' '客户端名称' "$ROOT/transit.out"
assert_text_before '客户端名称' '客户端 UUID' "$ROOT/transit.out"
assert_text_before '对外 IP/域名' '对外端口' "$ROOT/public-entry-wizard.out"
assert_text_before '对外端口' '本地端口' "$ROOT/public-entry-wizard.out"
if grep -q '本机监听端口' "$ROOT/public-entry-wizard.out"; then
  echo "public entry wizard should use 本地端口 consistently" >&2
  exit 1
fi
assert_text_before '选择中转节点' '选择落地节点' "$ROOT/hub-link-wizard.out"
assert_text_before '链路模式' '客户端名称' "$ROOT/hub-link-wizard.out"
assert_text_before '客户端名称' '出口名称' "$ROOT/hub-link-wizard.out"
if grep -q 'auth_user\|endpoint 名\|inbound tag\|endpoint tag\|Reality inbound tag' "$ROOT/hub-link-wizard.out"; then
  echo "Hub link wizard should hide auth_user/endpoint/tag internals" >&2
  exit 1
fi
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
grep -q 'Hub 访问地址/IP' "$ROOT/hub-quick.out"
grep -q 'Hub 访问端口' "$ROOT/hub-quick.out"
grep -q 'Hub 本地地址/IP' "$ROOT/hub-quick.out"
grep -q '证书 SAN 包含：hub.quick.example' "$ROOT/hub-quick.out"
grep -q '是否现在启动 relaypilot-hub \[Y/n\]' "$ROOT/hub-quick.out"
grep -q '绑定 Telegram 并启用面板 \[y/N\]' "$ROOT/hub-quick.out"
if grep -q 'Hub public IP/domain\|Hub HTTPS port\|Hub listen address' "$ROOT/hub-quick.out" "$ROOT/hub-quick-cancel.out"; then
  echo "Hub setup wizard should use concise Chinese labels" >&2
  exit 1
fi
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
go build -trimpath -ldflags "-X main.buildVersion=v-local" -o "$ROOT/release/v-local/relaypilot_linux_amd64" ./cmd/relaypilot
(cd "$ROOT/release/v-local" && sha256sum relaypilot_linux_amd64 > relaypilot_linux_amd64.sha256)
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
INSTALL_DIR="$ROOT/update-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh update --version v-local --no-restart-services > "$ROOT/update.out" 2> "$ROOT/update.err"
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
INSTALL_DIR="$ROOT/update-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh update --version v-local --no-restart-services > "$ROOT/update-skip.out" 2> "$ROOT/update-skip.err"
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
RELAYPILOT_REMOTE_VERSION="v-local" \
INSTALL_DIR="$ROOT/update-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh update --no-restart-services > "$ROOT/update-latest-skip.out" 2> "$ROOT/update-latest-skip.err"
printf '6\n0\n' | RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
RELAYPILOT_REMOTE_VERSION="v-local" \
INSTALL_DIR="$ROOT/update-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh services > "$ROOT/update-menu-skip.out" 2> "$ROOT/update-menu-skip.err"
ln -sf "$ROOT/update-dir/relaypilot.sh" "$ROOT/bin/relaypilot-hub"
ln -sf "$ROOT/update-dir/relaypilot.sh" "$ROOT/bin/relaypilot-agent"
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
INSTALL_DIR="$ROOT/update-dir" \
BIN_PATH="$ROOT/bin/relaypilot-updated" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh update --version v-local --force --no-restart-services > "$ROOT/update-force.out" 2> "$ROOT/update-force.err"
assert_path_absent "$ROOT/bin/relaypilot-hub"
assert_path_absent "$ROOT/bin/relaypilot-agent"
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
if command -v script >/dev/null 2>&1; then
  printf '\n' | script -qec "RAW_BASE=file://$ROOT/raw RELEASE_BASE=file://$ROOT/release INSTALL_DIR=$ROOT/update-confirm-tty-dir BIN_PATH=$ROOT/bin/relaypilot-updated-confirm-tty RELAYPILOT_NO_ROOT=1 RELAYPILOT_FAKE_SYSTEMCTL_LOG=$ROOT/update-confirm-tty-systemctl.log PATH=$ROOT/fake-update-bin:\$PATH bash ./relaypilot.sh update --version v-local" "$ROOT/update-confirm-tty.out" >/dev/null
  printf '\n' | script -qec "INSTALL_DIR=$ROOT/uninstall-confirm-tty-dir BIN_PATH=$ROOT/bin/relaypilot-uninstall-confirm-tty STATE_DIR=$ROOT/state RELAYPILOT_NO_ROOT=1 bash ./relaypilot.sh uninstall --dry-run" "$ROOT/uninstall-confirm-tty.out" >/dev/null
  confirm_yes_default=$'\033[1m\033[36mY\033[0m/n'
  confirm_yes_bad=$'\033[1m\033[36mY/n\033[0m'
  confirm_no_default=$'y/\033[1m\033[36mN\033[0m'
  confirm_no_bad=$'\033[1m\033[36my/N\033[0m'
  grep -Fq "$confirm_yes_default" "$ROOT/update-confirm-tty.out"
  ! grep -Fq "$confirm_yes_bad" "$ROOT/update-confirm-tty.out"
  grep -Fq "$confirm_no_default" "$ROOT/uninstall-confirm-tty.out"
  ! grep -Fq "$confirm_no_bad" "$ROOT/uninstall-confirm-tty.out"
fi
RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer" \
BIN_PATH="$ROOT/bin/relaypilot-installed" \
RELAYPILOT_NO_ROOT=1 \
bash ./install-relaypilot.sh > "$ROOT/installer-noninteractive.out" 2> "$ROOT/installer-noninteractive.err"
assert_path_absent "$ROOT/bin/relaypilot-hub"
assert_path_absent "$ROOT/bin/relaypilot-agent"

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
RAW_BASE="file://$ROOT/raw-enroll" \
RELEASE_BASE="file://$ROOT/release-enroll" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer-hub-mode" \
BIN_PATH="$ROOT/bin/relaypilot-hub-mode" \
RELAYPILOT_NO_ROOT=1 \
RELAYPILOT_INSTALL_DISPATCH_LOG="$ROOT/install-hub-mode.log" \
bash ./install-relaypilot.sh hub > "$ROOT/installer-hub-mode.out" 2> "$ROOT/installer-hub-mode.err"
RAW_BASE="file://$ROOT/raw-enroll" \
RELEASE_BASE="file://$ROOT/release-enroll" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer-agent-mode" \
BIN_PATH="$ROOT/bin/relaypilot-agent-mode" \
RELAYPILOT_NO_ROOT=1 \
RELAYPILOT_INSTALL_DISPATCH_LOG="$ROOT/install-agent-mode.log" \
bash ./install-relaypilot.sh agent > "$ROOT/installer-agent-mode.out" 2> "$ROOT/installer-agent-mode.err"

printf '0\n' | RAW_BASE="file://$ROOT/raw" \
RELEASE_BASE="file://$ROOT/release" \
VERSION="v-local" \
INSTALL_DIR="$ROOT/relay-installer-menu" \
BIN_PATH="$ROOT/bin/relaypilot-menu" \
RELAYPILOT_NO_ROOT=1 \
bash ./install-relaypilot.sh menu > "$ROOT/installer-menu.out" 2> "$ROOT/installer-menu.err"
INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
HUB_BIN_PATH="$ROOT/bin/relaypilot-self-hub" \
AGENT_BIN_PATH="$ROOT/bin/relaypilot-self-agent" \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh install > "$ROOT/install.out" 2> "$ROOT/install.err"

grep -q '已更新 RelayPilot' "$ROOT/update.out"
grep -q 'RelayPilot Go core v-local' "$ROOT/update.out"
grep -q '已是最新版本：v-local' "$ROOT/update-skip.out"
! grep -q '下载 Go core' "$ROOT/update-skip.out"
grep -q '已是最新版本：v-local' "$ROOT/update-latest-skip.out"
! grep -q '下载 Go core' "$ROOT/update-latest-skip.out"
grep -q '已是最新版本：v-local' "$ROOT/update-menu-skip.out"
! grep -q '已更新，正在重新打开新版面板' "$ROOT/update-menu-skip.out"
grep -q '下载 Go core' "$ROOT/update-force.out"
[[ -x "$ROOT/update-dir/relaypilot.sh" ]]
[[ -x "$ROOT/update-dir/bin/relaypilot" ]]
[[ -L "$ROOT/bin/relaypilot-updated" || -x "$ROOT/bin/relaypilot-updated" ]]
grep -q 'Installed entrypoint' "$ROOT/installer-noninteractive.out"
! grep -q 'Installed Hub entrypoint' "$ROOT/installer-noninteractive.out"
! grep -q 'Installed Agent entrypoint' "$ROOT/installer-noninteractive.out"
! grep -q 'Run: relaypilot-hub' "$ROOT/installer-noninteractive.out"
! grep -q 'Run: relaypilot-agent' "$ROOT/installer-noninteractive.out"
! grep -q '^RelayPilot$' "$ROOT/installer-noninteractive.out"
grep -q '^enroll --invite INVITE_SMOKE --install-service$' "$ROOT/install-enroll-auto.log"
grep -q '^join --invite INVITE_SMOKE$' "$ROOT/install-enroll-join.log"
grep -q '^install$' "$ROOT/install-hub-mode.log"
grep -q '^install$' "$ROOT/install-agent-mode.log"
[[ -L "$ROOT/bin/relaypilot-hub" || -x "$ROOT/bin/relaypilot-hub" ]]
[[ -L "$ROOT/bin/relaypilot-agent" || -x "$ROOT/bin/relaypilot-agent" ]]
grep -q 'RelayPilot 安装' "$ROOT/installer-menu.out"
grep -q '安装/进入 Hub' "$ROOT/installer-menu.out"
grep -q '安装/进入 Agent' "$ROOT/installer-menu.out"
[[ -x "$ROOT/install-dir/relaypilot.sh" ]]
[[ -L "$ROOT/bin/relaypilot-self" || -x "$ROOT/bin/relaypilot-self" ]]
assert_path_absent "$ROOT/bin/relaypilot-self-hub"
assert_path_absent "$ROOT/bin/relaypilot-self-agent"
printf '0\n' | INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
HUB_BIN_PATH="$ROOT/bin/relaypilot-self-hub" \
AGENT_BIN_PATH="$ROOT/bin/relaypilot-self-agent" \
RELAYPILOT_NO_ROOT=1 \
bash "$ROOT/install-dir/relaypilot.sh" hub > "$ROOT/install-self-hub.out" 2> "$ROOT/install-self-hub.err"
printf '0\n' | INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
HUB_BIN_PATH="$ROOT/bin/relaypilot-self-hub" \
AGENT_BIN_PATH="$ROOT/bin/relaypilot-self-agent" \
RELAYPILOT_NO_ROOT=1 \
bash "$ROOT/install-dir/relaypilot.sh" agent > "$ROOT/install-self-agent.out" 2> "$ROOT/install-self-agent.err"
[[ -L "$ROOT/bin/relaypilot-self-hub" || -x "$ROOT/bin/relaypilot-self-hub" ]]
[[ -L "$ROOT/bin/relaypilot-self-agent" || -x "$ROOT/bin/relaypilot-self-agent" ]]

INSTALL_DIR="$ROOT/install-dir" \
BIN_PATH="$ROOT/bin/relaypilot-self" \
HUB_BIN_PATH="$ROOT/bin/relaypilot-self-hub" \
AGENT_BIN_PATH="$ROOT/bin/relaypilot-self-agent" \
STATE_DIR="$ROOT/state" \
KEEP_STATE=1 \
RELAYPILOT_NO_ROOT=1 \
bash ./relaypilot.sh uninstall --yes > "$ROOT/uninstall.out" 2> "$ROOT/uninstall.err"
[[ ! -e "$ROOT/install-dir" ]]
[[ ! -e "$ROOT/bin/relaypilot-self" ]]
assert_path_absent "$ROOT/bin/relaypilot-self-hub"
assert_path_absent "$ROOT/bin/relaypilot-self-agent"
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
grep -q "$TRANSIT_PRIVATE_KEY_SMOKE" "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '0123456789abcdef' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q 'www.example.com' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '"listen_port": 8443' "$ROOT/transit-conf/00-relaypilot-reality.json"
grep -q '"type": "shadowsocks"' "$ROOT/transit-conf/90-relaypilot-outbounds.json"
grep -q 'landing-hk-ss' "$ROOT/transit-conf/90-relaypilot-outbounds.json"
grep -q '"auth_user"' "$ROOT/transit-conf/91-relaypilot-route.json"
grep -q '"hk"' "$ROOT/transit-conf/91-relaypilot-route.json"
! grep -q 'systemctl' "$ROOT/stub.log"
echo 'relaypilot smoke: OK'
