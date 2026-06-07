#!/usr/bin/env bash
set -euo pipefail

VERSION="${RELAYPILOT_VERSION:-0.1.12}"
REPO="${REPO:-jiwen77/relaypilot}"
RAW_REF="${RAW_REF:-main}"
RAW_BASE="${RAW_BASE:-https://github.com/${REPO}/raw/${RAW_REF}}"
RELEASE_BASE="${RELEASE_BASE:-https://github.com/${REPO}/releases/download}"
INSTALL_DIR="${INSTALL_DIR:-/opt/relaypilot}"
BIN_PATH="${BIN_PATH:-/usr/local/bin/relaypilot}"
HUB_BIN_PATH="${HUB_BIN_PATH:-$(dirname "$BIN_PATH")/relaypilot-hub}"
AGENT_BIN_PATH="${AGENT_BIN_PATH:-$(dirname "$BIN_PATH")/relaypilot-agent}"
INVOKED_NAME="$(basename "${0:-relaypilot}")"
SOURCE_PATH="${BASH_SOURCE[0]}"
while [[ -L "$SOURCE_PATH" ]]; do
  SOURCE_DIR="$(cd -P "$(dirname "$SOURCE_PATH")" && pwd 2>/dev/null || pwd)"
  SOURCE_PATH="$(readlink "$SOURCE_PATH")"
  [[ "$SOURCE_PATH" != /* ]] && SOURCE_PATH="${SOURCE_DIR}/${SOURCE_PATH}"
done
SCRIPT_DIR="$(cd -P "$(dirname "$SOURCE_PATH")" && pwd 2>/dev/null || pwd)"
GO_CORE="${RELAYPILOT_GO_BIN:-${SCRIPT_DIR}/bin/relaypilot}"
STATE_DIR="${STATE_DIR:-/etc/relaypilot}"
CONF_DIR="${CONF_DIR:-/etc/sing-box/conf}"
SINGBOX_CONFIG_PATH="${SINGBOX_CONFIG_PATH:-/etc/sing-box/config.json}"
MESH_CONFIG_DIR="${MESH_CONFIG_DIR:-/etc/wireguard}"
SERVICE_NAME="${SERVICE_NAME:-sing-box}"
AGENT_SERVICE_NAME="${AGENT_SERVICE_NAME:-relaypilot-agent}"
HUB_SERVICE_NAME="${HUB_SERVICE_NAME:-relaypilot-hub}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
OPENRC_DIR="${OPENRC_DIR:-/etc/init.d}"
RELAYPILOT_PROFILE="${RELAYPILOT_PROFILE:-auto}"
AGENT_SERVICE_MEMORY_MAX_USER_SET="${AGENT_SERVICE_MEMORY_MAX:+1}"
AGENT_SERVICE_CPU_QUOTA_USER_SET="${AGENT_SERVICE_CPU_QUOTA:+1}"
HUB_SERVICE_MEMORY_MAX_USER_SET="${HUB_SERVICE_MEMORY_MAX:+1}"
HUB_SERVICE_CPU_QUOTA_USER_SET="${HUB_SERVICE_CPU_QUOTA:+1}"
TG_SERVICE_MEMORY_MAX_USER_SET="${TG_SERVICE_MEMORY_MAX:+1}"
TG_SERVICE_CPU_QUOTA_USER_SET="${TG_SERVICE_CPU_QUOTA:+1}"
AGENT_SERVICE_MEMORY_MAX="${AGENT_SERVICE_MEMORY_MAX:-96M}"
AGENT_SERVICE_CPU_QUOTA="${AGENT_SERVICE_CPU_QUOTA:-25%}"
HUB_SERVICE_MEMORY_MAX="${HUB_SERVICE_MEMORY_MAX:-128M}"
HUB_SERVICE_CPU_QUOTA="${HUB_SERVICE_CPU_QUOTA:-50%}"
SERVICE_RESTART_SEC="${SERVICE_RESTART_SEC:-30}"
SERVICE_TASKS_MAX="${SERVICE_TASKS_MAX:-64}"
TG_SERVICE_NAME="${TG_SERVICE_NAME:-relaypilot-bot}"
TG_SERVICE_MEMORY_MAX="${TG_SERVICE_MEMORY_MAX:-128M}"
TG_SERVICE_CPU_QUOTA="${TG_SERVICE_CPU_QUOTA:-25%}"
HUB_ALERT_TIMER_NAME="${HUB_ALERT_TIMER_NAME:-relaypilot-alert-offline}"
HUB_ALERT_TIMER_INTERVAL="${HUB_ALERT_TIMER_INTERVAL:-1h}"
HUB_ALERT_THRESHOLD_SECONDS="${HUB_ALERT_THRESHOLD_SECONDS:-86400}"
HUB_ALERT_SNOOZE_SECONDS="${HUB_ALERT_SNOOZE_SECONDS:-86400}"
HUB_PUBLIC_CONFIG_NAME="${HUB_PUBLIC_CONFIG_NAME:-hub-public.env}"

if [[ -t 1 ]]; then
  GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; CYAN=$'\033[36m'; BOLD=$'\033[1m'; DIM=$'\033[2m'; NC=$'\033[0m'
else
  GREEN=''; YELLOW=''; RED=''; CYAN=''; BOLD=''; DIM=''; NC=''
fi
info() { printf "%s==>%s %s\n" "$GREEN" "$NC" "$*"; }
warn() { printf "%sWARN:%s %s\n" "$YELLOW" "$NC" "$*" >&2; }
err() { printf "%sERROR:%s %s\n" "$RED" "$NC" "$*" >&2; }
title() { printf "\n%s%s%s\n" "$BOLD" "$*" "$NC"; }

MENU_SCREEN_ACTIVE=0
MENU_PREFETCHED_INPUT=()
menu_can_control_screen() {
  [[ -t 0 && -t 1 ]] || return 1
  [[ "${TERM:-}" != "" && "${TERM:-}" != "dumb" ]] || return 1
  [[ "${RELAYPILOT_MENU_SCREEN:-1}" != "0" ]] || return 1
}
menu_prefetch_non_tty_input() {
  [[ -t 0 ]] && return 0
  local value timeout="${RELAYPILOT_MENU_INPUT_TIMEOUT:-0.2}"
  if IFS= read -r -t "$timeout" value; then
    MENU_PREFETCHED_INPUT+=("$value")
    return 0
  fi
  if [[ -n "${value:-}" ]]; then
    MENU_PREFETCHED_INPUT+=("$value")
    return 0
  fi
  return 1
}
menu_read_line() {
  local var_name="$1" line
  if (( ${#MENU_PREFETCHED_INPUT[@]} > 0 )); then
    line="${MENU_PREFETCHED_INPUT[0]}"
    MENU_PREFETCHED_INPUT=("${MENU_PREFETCHED_INPUT[@]:1}")
  else
    read -r line || true
  fi
  printf -v "$var_name" '%s' "$line"
}
menu_enter_screen() {
  [[ "$MENU_SCREEN_ACTIVE" == "1" ]] && return 0
  menu_can_control_screen || return 0
  if command -v tput >/dev/null 2>&1; then
    tput smcup 2>/dev/null || true
    tput clear 2>/dev/null || true
  else
    printf '\033[?1049h\033[H\033[2J'
  fi
  MENU_SCREEN_ACTIVE=1
  trap 'menu_leave_screen' EXIT
  trap 'menu_leave_screen; exit 130' INT TERM
}
menu_leave_screen() {
  [[ "$MENU_SCREEN_ACTIVE" == "1" ]] || return 0
  if command -v tput >/dev/null 2>&1; then
    tput rmcup 2>/dev/null || true
  else
    printf '\033[?1049l'
  fi
  MENU_SCREEN_ACTIVE=0
}
menu_clear() {
  [[ "$MENU_SCREEN_ACTIVE" == "1" ]] || return 0
  if command -v tput >/dev/null 2>&1; then
    tput clear 2>/dev/null || true
  else
    printf '\033[H\033[2J'
  fi
}
menu_pause() {
  [[ -t 0 && -t 1 ]] || return 0
  local _
  echo
  read -r -p "按 Enter 返回菜单..." _ || true
}
menu_action() {
  local rc
  menu_clear
  set +e
  "$@"
  rc=$?
  set -e
  (( rc == 0 )) || warn "操作未完成（退出码 ${rc}）。"
  menu_pause
  return 0
}
menu_invalid_choice() {
  warn "无效选择"
  menu_pause
}
menu_session() {
  local fn="$1"
  shift
  if ! menu_prefetch_non_tty_input; then
    warn "interactive menu requires a TTY or piped menu input; run 'relaypilot help' or pass a subcommand for automation."
    usage
    return 2
  fi
  menu_enter_screen
  "$fn" "$@"
  local rc=$?
  menu_leave_screen
  return "$rc"
}
menu_header() {
  local heading="$1" status_line="${2:-}" meta_line="${3:-}"
  menu_clear
  printf "\n%s================================================%s\n" "$CYAN" "$NC"
  printf "%s%s%*s%s\n" "$CYAN" "$BOLD" $(( (48 + ${#heading}) / 2 )) "$heading" "$NC"
  printf "%s================================================%s\n" "$CYAN" "$NC"
  [[ -n "$meta_line" ]] && printf "%s%s%s\n" "$DIM" "$meta_line" "$NC"
  [[ -n "$status_line" ]] && printf "%s\n" "$status_line"
  echo
}
menu_item() {
  local key="$1" label="$2" desc="${3:-}"
  if [[ -n "$desc" ]]; then
    printf "  %s%2s%s  %s  %s%s%s\n" "$CYAN" "$key" "$NC" "$label" "$DIM" "$desc" "$NC"
  else
    printf "  %s%2s%s  %s\n" "$CYAN" "$key" "$NC" "$label"
  fi
}
menu_back() {
  local label="${1:-返回}"
  echo
  menu_item 0 "$label"
}
menu_prompt() {
  local var_name="$1" range="$2" value
  echo
  printf "选择 [%s]: " "$range"
  menu_read_line value
  printf -v "$var_name" '%s' "$value"
}

usage() {
  cat <<EOF
RelayPilot v${VERSION}

Role entrypoints:
  relaypilot-hub      # 直接进入 Hub 面板
  relaypilot-agent    # 直接进入 Agent 面板
  relaypilot          # 安装/初始化入口

Usage:
  bash relaypilot.sh
  bash relaypilot.sh menu
  relaypilot hub      # 兼容旧入口
  relaypilot agent    # 兼容旧入口
  relaypilot-hub install
  relaypilot-agent install
  bash relaypilot.sh landing
  bash relaypilot.sh transit
  bash relaypilot.sh hub
  bash relaypilot.sh bot commands
  bash relaypilot.sh landing-install-ss
  bash relaypilot.sh landing-install-socks
  bash relaypilot.sh transit-init-reality
  bash relaypilot.sh transit-import-bind
  bash relaypilot.sh connection-info
  bash relaypilot.sh agent connection-info
  bash relaypilot.sh hub-agent-export --agent-id hk-transit --role transit
  bash relaypilot.sh hub-import-agent /path/to/agent.json
  bash relaypilot.sh hub-issue-token transit-hk
  bash relaypilot.sh hub-init-tls --host hub.example
  bash relaypilot.sh hub-quick-setup
  bash relaypilot.sh hub-enroll  # interactive invite wizard
  bash relaypilot.sh hub-create-enroll-code --agent-id transit-hk --role transit  # JSON for automation
  bash relaypilot.sh hub-create-enroll-code --public-host hub.example --agent-id transit-hk --role transit --text
  bash relaypilot.sh hub-provision-agent --hub-url https://hub.example:8443 --agent-id transit-hk --role transit
  bash relaypilot.sh hub-tokens
  bash relaypilot.sh hub-revoke-token transit-hk
  bash relaypilot.sh hub-daemon --host 0.0.0.0 --port 8443 --tls-cert /etc/relaypilot/hub-tls/hub.crt --tls-key /etc/relaypilot/hub-tls/hub.key --client-ca /etc/relaypilot/hub-tls/ca.crt --require-client-cert
  bash relaypilot.sh agent enroll --invite 'PASTE_INVITE' --install-service
  bash relaypilot.sh agent enroll --bundle 'PASTE_BUNDLE'
  bash relaypilot.sh agent join
  bash relaypilot.sh agent ip-mode
  bash relaypilot.sh agent remote-decommission enable|disable|status
  bash relaypilot.sh agent public-entry
  bash relaypilot.sh agent poll-once --enrollment-file /etc/relaypilot/agent-enrollment.json
  bash relaypilot.sh agent install-service --enrollment-file /etc/relaypilot/agent-enrollment.json
  bash relaypilot.sh public-entry-set --use shadowsocks --name jp --host front.example --public-port 443 --local-port 2443
  bash relaypilot.sh public-entry-list
  bash relaypilot.sh install-hub-service --host 0.0.0.0 --port 8443 --tls-cert /etc/relaypilot/hub-tls/hub.crt --tls-key /etc/relaypilot/hub-tls/hub.key --client-ca /etc/relaypilot/hub-tls/ca.crt --require-client-cert
  bash relaypilot.sh install-bot-service
  bash relaypilot.sh install-alert-timer
  bash relaypilot.sh resource-profile
  bash relaypilot.sh services
  bash relaypilot.sh hub-remove-agent transit-hk --reason uninstalled
  bash relaypilot.sh hub-dispatch "/decommission transit-hk --mode uninstall"
  bash relaypilot.sh hub-dispatch "/decommission transit-hk --mode uninstall --confirm transit-hk"
  bash relaypilot.sh hub-alert-offline --dry-run
  bash relaypilot.sh hub-dispatch "/status all"
  bash relaypilot.sh hub-link transit-hk landing-hk [auth_user] [endpoint_name] [--mode direct|mesh]
  bash relaypilot.sh hub-results
  bash relaypilot.sh bot register
  bash relaypilot.sh install
  bash relaypilot.sh update
  bash relaypilot.sh update --version v0.1.12 --restart-services
  bash relaypilot.sh update --version v0.1.12 --force
  bash relaypilot.sh leave-hub  # remove Agent service/Hub credentials, keep Reality/SS/sing-box
  bash relaypilot.sh uninstall --dry-run
  bash relaypilot.sh uninstall --yes
  bash relaypilot.sh uninstall --yes --full --purge-proxy-config
  bash relaypilot.sh doctor
  bash relaypilot.sh migrate-state --from /path/to/old-state --to /etc/relaypilot --dry-run

Environment:
  STATE_DIR=/etc/relaypilot
  CONF_DIR=/etc/sing-box/conf
  SINGBOX_CONFIG_PATH=/etc/sing-box/config.json
  INSTALL_DIR=/opt/relaypilot
  BIN_PATH=/usr/local/bin/relaypilot
  NO_RESTART=1
  RELAYPILOT_NO_ROOT=1        # tests/dev only
  SKIP_SINGBOX_INSTALL=1     # do not invoke upstream installer
  RELAYPILOT_PROFILE=auto    # auto|ask|tiny|small|normal|custom
  RELAYPILOT_NONINTERACTIVE=1 # never ask service profile questions
  AGENT_SERVICE_MEMORY_MAX=96M
  AGENT_SERVICE_CPU_QUOTA=25%
  HUB_SERVICE_MEMORY_MAX=128M
  HUB_SERVICE_CPU_QUOTA=50%
  TG_SERVICE_MEMORY_MAX=128M
  TG_SERVICE_CPU_QUOTA=25%
  HUB_ALERT_TIMER_INTERVAL=1h
  HUB_ALERT_THRESHOLD_SECONDS=86400
EOF
}

prompt() {
  local var_name="$1" label="$2" default="${3:-}" value
  if [[ -n "$default" ]]; then
    printf "%s [默认：%s%s%s]: " "$label" "$BOLD$CYAN" "$default" "$NC"
    read -r value || true
    value="${value:-$default}"
  else
    printf "%s: " "$label"
    read -r value || true
  fi
  printf -v "$var_name" '%s' "$value"
}

trim_url_scheme() {
  local value="$1"
  value="${value#https://}"
  value="${value#http://}"
  value="${value%%/*}"
  printf '%s' "$value"
}

host_only() {
  local value colons
  value="$(trim_url_scheme "$1")"
  if [[ "$value" == \[*\]* ]]; then
    value="${value#\[}"
    value="${value%%\]*}"
  else
    colons="${value//[^:]/}"
    if [[ ${#colons} -eq 1 ]]; then
      value="${value%%:*}"
    fi
  fi
  printf '%s' "$value"
}

port_from_host_input() {
  local value colons
  value="$(trim_url_scheme "$1")"
  if [[ "$value" == \[*\]:* ]]; then
    value="${value##*\]:}"
  else
    colons="${value//[^:]/}"
    if [[ ${#colons} -eq 1 ]]; then
      value="${value##*:}"
    else
      value=""
    fi
  fi
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    printf '%s' "$value"
  fi
  return 0
}

valid_port() {
  local value="$1"
  [[ "$value" =~ ^[0-9]+$ ]] && (( value >= 1 && value <= 65535 ))
}

random_hex() {
  local bytes="${1:-8}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
  else
    od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

random_token() {
  local bytes="${1:-18}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$bytes" | tr '+/' '-_' | tr -d '='
  else
    od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

random_reality_private_key() {
  random_token 32
}

random_uuid() {
  if command -v uuidgen >/dev/null 2>&1; then
    uuidgen | tr '[:upper:]' '[:lower:]'
    return
  fi
  local hex
  hex="$(random_hex 16)"
  printf '%s-%s-4%s-%s%s-%s\n' \
    "${hex:0:8}" "${hex:8:4}" "${hex:13:3}" \
    "$(printf '%x' $(( (0x${hex:16:1} & 3) | 8 )))" "${hex:17:3}" "${hex:20:12}"
}

config_json_files() {
  local conf="$1"
  if [[ -f "$conf" ]]; then
    printf '%s\n' "$conf"
  elif [[ -d "$conf" ]]; then
    find "$conf" -maxdepth 1 -type f -name '*.json' | sort
  fi
}

json_string_field_from_conf() {
  local conf="$1" key="$2" file value
  while IFS= read -r file; do
    [[ -n "$file" ]] || continue
    value="$(
      awk -v field="$key" '
        {
          pattern="^[[:space:]]*\"" field "\"[[:space:]]*:[[:space:]]*\""
          field_pattern="^[[:space:]]*\"" field "\"[[:space:]]*:"
        }
        $0 ~ pattern {
          line=$0
          sub(pattern, "", line)
          sub(/".*$/, "", line)
          print line
          exit
        }
        $0 ~ field_pattern {
          pending=1
          next
        }
        pending && $0 ~ /"/ {
          line=$0
          sub(/^[^"]*"/, "", line)
          sub(/".*$/, "", line)
          print line
          exit
        }
      ' "$file"
    )"
    if [[ -n "$value" ]]; then
      printf '%s\n' "$value"
      return 0
    fi
  done < <(config_json_files "$conf")
}

json_number_field_from_conf() {
  local conf="$1" key="$2" file value
  while IFS= read -r file; do
    [[ -n "$file" ]] || continue
    value="$(
      awk -v field="$key" '
        {
          pattern="^[[:space:]]*\"" field "\"[[:space:]]*:[[:space:]]*"
        }
        $0 ~ pattern {
          line=$0
          sub(pattern, "", line)
          sub(/[,}].*$/, "", line)
          gsub(/[[:space:]]/, "", line)
          if (line ~ /^[0-9]+$/) {
            print line
            exit
          }
        }
      ' "$file"
    )"
    if [[ -n "$value" ]]; then
      printf '%s\n' "$value"
      return 0
    fi
  done < <(config_json_files "$conf")
}

endpoint_json_files() {
  local dir="$STATE_DIR/endpoints"
  [[ -d "$dir" ]] || return 0
  find "$dir" -maxdepth 1 -type f -name '*.json' | sort
}

json_string_field_from_endpoint() {
  local protocol="$1" key="$2" file
  while IFS= read -r file; do
    [[ -n "$file" ]] || continue
    [[ "$(json_string_field_from_conf "$file" protocol)" == "$protocol" ]] || continue
    json_string_field_from_conf "$file" "$key"
    return 0
  done < <(endpoint_json_files)
}

json_number_field_from_endpoint() {
  local protocol="$1" key="$2" file
  while IFS= read -r file; do
    [[ -n "$file" ]] || continue
    [[ "$(json_string_field_from_conf "$file" protocol)" == "$protocol" ]] || continue
    json_number_field_from_conf "$file" "$key"
    return 0
  done < <(endpoint_json_files)
}

public_entry_string_field() {
  local use="$1" name="$2" key="$3" file="$STATE_DIR/public-entries.json" value
  [[ -f "$file" ]] || return 0
  value="$(
    awk -v entry="\"${use}:${name}\"" -v field="$key" '
      $0 ~ entry { in_entry=1; next }
      in_entry && $0 ~ /^[[:space:]]*}/ { exit }
      in_entry {
        pattern="^[[:space:]]*\"" field "\"[[:space:]]*:[[:space:]]*\""
      }
      in_entry && $0 ~ pattern {
        line=$0
        sub(pattern, "", line)
        sub(/".*$/, "", line)
        print line
        exit
      }
    ' "$file"
  )"
  [[ -n "$value" ]] && printf '%s\n' "$value"
  return 0
}

public_entry_number_field() {
  local use="$1" name="$2" key="$3" file="$STATE_DIR/public-entries.json" value
  [[ -f "$file" ]] || return 0
  value="$(
    awk -v entry="\"${use}:${name}\"" -v field="$key" '
      $0 ~ entry { in_entry=1; next }
      in_entry && $0 ~ /^[[:space:]]*}/ { exit }
      in_entry {
        pattern="^[[:space:]]*\"" field "\"[[:space:]]*:[[:space:]]*"
      }
      in_entry && $0 ~ pattern {
        line=$0
        sub(pattern, "", line)
        sub(/[,}].*$/, "", line)
        gsub(/[[:space:]]/, "", line)
        if (line ~ /^[0-9]+$/) {
          print line
          exit
        }
      }
    ' "$file"
  )"
  [[ -n "$value" ]] && printf '%s\n' "$value"
  return 0
}

first_endpoint_json_file() {
  local file
  while IFS= read -r file; do
    [[ -n "$file" ]] || continue
    printf '%s\n' "$file"
    return 0
  done < <(endpoint_json_files)
}

vless_user_uuid_from_conf() {
  local conf="$1" name="$2" file value
  [[ -n "$name" ]] || return 0
  while IFS= read -r file; do
    [[ -n "$file" ]] || continue
    value="$(
      awk -v target="$name" '
        /"users"[[:space:]]*:/ { in_users=1 }
        in_users && /}/ { if (in_user) in_user=0 }
        in_users && /"name"[[:space:]]*:/ {
          line=$0
          sub(/^.*"name"[[:space:]]*:[[:space:]]*"/, "", line)
          sub(/".*$/, "", line)
          if (line == target) in_user=1
        }
        in_user && /"uuid"[[:space:]]*:/ {
          line=$0
          sub(/^.*"uuid"[[:space:]]*:[[:space:]]*"/, "", line)
          sub(/".*$/, "", line)
          print line
          exit
        }
      ' "$file"
    )"
    if [[ -n "$value" ]]; then
      printf '%s\n' "$value"
      return 0
    fi
  done < <(config_json_files "$conf")
}

duration_to_minutes() {
  local value="$1"
  value="${value//[[:space:]]/}"
  if [[ "$value" =~ ^([0-9]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  elif [[ "$value" =~ ^([0-9]+)[mM]$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  elif [[ "$value" =~ ^([0-9]+)[hH]$ ]]; then
    printf '%s\n' "$(( BASH_REMATCH[1] * 60 ))"
  elif [[ "$value" =~ ^([0-9]+)[dD]$ ]]; then
    printf '%s\n' "$(( BASH_REMATCH[1] * 1440 ))"
  else
    return 1
  fi
}

url_host() {
  local value
  value="$(host_only "$1")"
  if [[ "$value" == *:* ]]; then
    printf '[%s]' "$value"
  else
    printf '%s' "$value"
  fi
}

hub_tls_cert_matches_host() {
  local cert="$1" host="$2" sans
  [[ -f "$cert" ]] || return 1
  command -v openssl >/dev/null 2>&1 || return 1
  sans="$(openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null || true)"
  grep -Fq "DNS:${host}" <<<"$sans" || grep -Fq "IP Address:${host}" <<<"$sans"
}

select_option() {
  local var_name="$1" title_text="$2" default="${3:-}" choice idx raw value label desc default_choice=""
  shift 3
  echo
  title "$title_text"
  local values=() labels=() descs=()
  while [[ $# -gt 0 ]]; do
    IFS='|' read -r value label desc <<< "$1"
    values+=("$value")
    labels+=("${label:-$value}")
    descs+=("${desc:-}")
    shift
  done
  for idx in "${!values[@]}"; do
    local mark=""
    if [[ "${values[$idx]}" == "$default" ]]; then
      mark=" *"
      default_choice="$((idx + 1))"
    fi
    if [[ -n "${descs[$idx]}" ]]; then
      printf "  %d) %s - %s%s\n" "$((idx + 1))" "${labels[$idx]}" "${descs[$idx]}" "$mark"
    else
      printf "  %d) %s%s\n" "$((idx + 1))" "${labels[$idx]}" "$mark"
    fi
  done
  if [[ -n "$default" ]]; then
    printf "  选择序号 [默认：%s%s%s]: " "$BOLD$CYAN" "${default_choice:-$default}" "$NC"
    read -r choice || true
    choice="${choice:-${default_choice:-$default}}"
  else
    printf "  选择序号: "
    read -r choice || true
  fi
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#values[@]} )); then
    printf -v "$var_name" '%s' "${values[$((choice - 1))]}"
  else
    printf -v "$var_name" '%s' "$choice"
  fi
}

confirm() {
  local label="$1" default="${2:-y}" value
  default="$(printf '%s' "$default" | tr '[:upper:]' '[:lower:]')"
  if [[ "$default" == "n" ]]; then
    printf "%s [y/%sN%s]: " "$label" "$BOLD$CYAN" "$NC"
  else
    printf "%s [%sY%s/n]: " "$label" "$BOLD$CYAN" "$NC"
  fi
  read -r value || true
  value="${value:-$default}"
  case "$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')" in
    y|yes) return 0 ;;
    *) return 1 ;;
  esac
}

fetch() {
  local url="$1" output="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$output" "$url"
  else
    err "缺少 curl/wget，无法下载：$url"
    return 1
  fi
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

normalize_version() {
  local value="$1"
  value="${value#v}"
  printf '%s' "$value"
}

installed_core_version() {
  local core="${INSTALL_DIR}/bin/relaypilot" out
  [[ -x "$core" ]] || return 1
  out="$("$core" version 2>/dev/null || true)"
  sed -nE 's/^RelayPilot Go core[[:space:]]+(.+)$/\1/p' <<<"$out" | head -n 1
}

resolve_latest_update_version() {
  local raw_base="$1" tmp resolved=""
  if [[ -n "${RELAYPILOT_REMOTE_VERSION:-}" ]]; then
    printf '%s' "$RELAYPILOT_REMOTE_VERSION"
    return 0
  fi
  tmp="$(mktemp -d /tmp/relaypilot-version.XXXXXX)"
  if fetch "https://api.github.com/repos/${REPO}/releases/latest" "${tmp}/latest.json" >/dev/null 2>&1; then
    resolved="$(sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "${tmp}/latest.json" | head -n 1)"
  fi
  if [[ -z "$resolved" ]] && fetch "${raw_base}/VERSION" "${tmp}/VERSION" >/dev/null 2>&1; then
    resolved="$(head -n 1 "${tmp}/VERSION" | tr -d '[:space:]')"
  fi
  rm -rf "$tmp"
  printf '%s' "$resolved"
}

select_agent_role() {
  local var_name="$1" default="${2:-transit}"
  select_option "$var_name" "Agent 角色" "$default" \
    "transit|中转节点|接入用户，转发到落地" \
    "landing|落地节点|提供出口" \
    "hub|Hub|控制面/管理端"
}

select_ip_mode() {
  local var_name="$1" default="${2:-static}"
  select_option "$var_name" "节点 IP 模式" "$default" \
    "static|静态 IP/DDNS|不额外检测公网 IP" \
    "dynamic|动态 IP|低频上报公网 IP"
}

require_root() {
  if [[ "${RELAYPILOT_NO_ROOT:-}" == "1" ]]; then return 0; fi
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    err "请用 root 运行，或在测试环境设置 RELAYPILOT_NO_ROOT=1。"
    exit 1
  fi
}

singbox_bin() {
  if [[ -x /etc/sing-box/sing-box ]]; then
    printf '%s\n' /etc/sing-box/sing-box
  elif command -v sing-box >/dev/null 2>&1; then
    command -v sing-box
  else
    printf '\n'
  fi
}


go_core_supports() {
  case "$1" in
    generate-ss-password|migrate-state|tg-config|tg-status|tg-commands|tg-register-commands|tg-get-commands|tg-delete-commands|tg-dispatch|tg-send|render-landing-ss|render-landing-socks|ensure-transit-reality|validate-endpoint|render-outbound|import-endpoint|export-endpoint|agent-connection-info|public-entry-set|public-entry-list|bind-transit|list-endpoints|inspect-conf|hub-agent-export|hub-import-agent|hub-agents|hub-remove-agent|hub-removed-agents|hub-alert-offline|hub-alerts|hub-alert-callback|hub-recover-tasks|hub-issue-token|hub-init-tls|hub-issue-agent-cert|hub-provision-agent|hub-create-enroll-code|hub-enroll-code|agent-enroll|agent-set-ip-mode|hub-rotate-token|hub-revoke-token|hub-tokens|hub-dispatch|hub-tasks|hub-results|hub-export-client|hub-export-landing|hub-sync-agent|hub-sync-all|hub-daemon|bot-daemon|agent-poll-once|agent-poll-loop) return 0 ;;
    *) return 1 ;;
  esac
}

core_cmd() {
  local cmd="$1"; shift || true
  if [[ ! -x "$GO_CORE" ]]; then
    err "缺少 Go core: $GO_CORE。请安装 release 二进制或设置 RELAYPILOT_GO_BIN。"
    return 1
  fi
  if ! go_core_supports "$cmd"; then
    err "Go core 暂不支持命令：$cmd"
    return 1
  fi
  "$GO_CORE" "$cmd" "$@"
}

ensure_singbox() {
  if [[ -n "$(singbox_bin)" ]]; then return 0; fi
  warn "未找到 sing-box。"
  if [[ "${SKIP_SINGBOX_INSTALL:-}" == "1" ]]; then
    warn "SKIP_SINGBOX_INSTALL=1，跳过安装。"
    return 0
  fi
  if confirm "是否通过 sing-box 官方 install.sh 安装" y; then
    bash <(curl -fsSL https://sing-box.app/install.sh)
  else
    return 1
  fi
}

service_restart() {
  local svc="$1"
  if command -v rc-service >/dev/null 2>&1; then
    rc-service "$svc" restart
  elif command -v systemctl >/dev/null 2>&1; then
    systemctl restart "$svc"
  else
    warn "找不到 rc-service/systemctl，请手动重启 $svc。"
  fi
}

detect_mem_mb() {
  if [[ -r /proc/meminfo ]]; then
    awk '/^MemTotal:/ {print int($2 / 1024); exit}' /proc/meminfo
  else
    printf '0\n'
  fi
}

detect_cpu_count() {
  local count=""
  if command -v nproc >/dev/null 2>&1; then
    count="$(nproc 2>/dev/null || true)"
  fi
  if [[ -z "$count" && -r /proc/cpuinfo ]]; then
    count="$(grep -c '^processor' /proc/cpuinfo 2>/dev/null || true)"
  fi
  if [[ -z "$count" || "$count" -lt 1 ]]; then count=1; fi
  printf '%s\n' "$count"
}

recommended_profile() {
  local mem_mb="${1:-$(detect_mem_mb)}"
  case "$mem_mb" in ''|*[!0-9]*) mem_mb=0 ;; esac
  if [[ "$mem_mb" -eq 0 ]]; then
    printf 'small\n'
  elif [[ "$mem_mb" -lt 384 ]]; then
    printf 'tiny\n'
  elif [[ "$mem_mb" -lt 1024 ]]; then
    printf 'small\n'
  else
    printf 'normal\n'
  fi
}

profile_description() {
  case "$1" in
    tiny) printf 'agent 64M/15%%, hub 96M/25%%, telegram 96M/20%%\n' ;;
    small) printf 'agent 96M/25%%, hub 128M/50%%, telegram 128M/25%%\n' ;;
    normal) printf 'agent 128M/50%%, hub 256M/75%%, telegram 192M/50%%\n' ;;
    custom) printf 'keep AGENT/HUB/TG_SERVICE_* environment overrides\n' ;;
    *) printf 'auto-select by detected RAM\n' ;;
  esac
}

set_if_not_user_override() {
  local var_name="$1" user_set_name="$2" value="$3"
  if [[ -z "${!user_set_name:-}" ]]; then
    printf -v "$var_name" '%s' "$value"
  fi
}

apply_service_profile() {
  local profile="$1"
  case "$profile" in
    tiny)
      set_if_not_user_override AGENT_SERVICE_MEMORY_MAX AGENT_SERVICE_MEMORY_MAX_USER_SET 64M
      set_if_not_user_override AGENT_SERVICE_CPU_QUOTA AGENT_SERVICE_CPU_QUOTA_USER_SET 15%
      set_if_not_user_override HUB_SERVICE_MEMORY_MAX HUB_SERVICE_MEMORY_MAX_USER_SET 96M
      set_if_not_user_override HUB_SERVICE_CPU_QUOTA HUB_SERVICE_CPU_QUOTA_USER_SET 25%
      set_if_not_user_override TG_SERVICE_MEMORY_MAX TG_SERVICE_MEMORY_MAX_USER_SET 96M
      set_if_not_user_override TG_SERVICE_CPU_QUOTA TG_SERVICE_CPU_QUOTA_USER_SET 20%
      ;;
    small)
      set_if_not_user_override AGENT_SERVICE_MEMORY_MAX AGENT_SERVICE_MEMORY_MAX_USER_SET 96M
      set_if_not_user_override AGENT_SERVICE_CPU_QUOTA AGENT_SERVICE_CPU_QUOTA_USER_SET 25%
      set_if_not_user_override HUB_SERVICE_MEMORY_MAX HUB_SERVICE_MEMORY_MAX_USER_SET 128M
      set_if_not_user_override HUB_SERVICE_CPU_QUOTA HUB_SERVICE_CPU_QUOTA_USER_SET 50%
      set_if_not_user_override TG_SERVICE_MEMORY_MAX TG_SERVICE_MEMORY_MAX_USER_SET 128M
      set_if_not_user_override TG_SERVICE_CPU_QUOTA TG_SERVICE_CPU_QUOTA_USER_SET 25%
      ;;
    normal)
      set_if_not_user_override AGENT_SERVICE_MEMORY_MAX AGENT_SERVICE_MEMORY_MAX_USER_SET 128M
      set_if_not_user_override AGENT_SERVICE_CPU_QUOTA AGENT_SERVICE_CPU_QUOTA_USER_SET 50%
      set_if_not_user_override HUB_SERVICE_MEMORY_MAX HUB_SERVICE_MEMORY_MAX_USER_SET 256M
      set_if_not_user_override HUB_SERVICE_CPU_QUOTA HUB_SERVICE_CPU_QUOTA_USER_SET 75%
      set_if_not_user_override TG_SERVICE_MEMORY_MAX TG_SERVICE_MEMORY_MAX_USER_SET 192M
      set_if_not_user_override TG_SERVICE_CPU_QUOTA TG_SERVICE_CPU_QUOTA_USER_SET 50%
      ;;
    custom) ;;
    *)
      warn "未知资源 profile：$profile，使用 small。"
      apply_service_profile small
      return
      ;;
  esac
  RELAYPILOT_PROFILE_EFFECTIVE="$profile"
}

service_profile_ready=0
prepare_service_profile() {
  if [[ "$service_profile_ready" == "1" ]]; then return 0; fi
  local detected_mem detected_cpu recommended profile answer
  detected_mem="$(detect_mem_mb)"
  detected_cpu="$(detect_cpu_count)"
  recommended="$(recommended_profile "$detected_mem")"
  profile="$RELAYPILOT_PROFILE"
  if [[ "$profile" == "auto" ]]; then
    profile="$recommended"
  elif [[ "$profile" == "ask" ]]; then
    if [[ "${RELAYPILOT_NONINTERACTIVE:-}" == "1" || "${RELAYPILOT_NO_ROOT:-}" == "1" || ! -t 0 ]]; then
      profile="$recommended"
    else
      title "资源配置"
      echo "检测：RAM ${detected_mem:-0}MB，CPU ${detected_cpu}"
      echo "1) ${recommended}（推荐）：$(profile_description "$recommended")"
      echo "2) tiny: $(profile_description tiny)"
      echo "3) small: $(profile_description small)"
      echo "4) normal: $(profile_description normal)"
      echo "5) custom/env: $(profile_description custom)"
      printf "选择配置 [%s1%s]: " "$BOLD$CYAN" "$NC"
      read -r answer || true
      case "${answer:-1}" in
        1|"") profile="$recommended" ;;
        2) profile=tiny ;;
        3) profile=small ;;
        4) profile=normal ;;
        5) profile=custom ;;
        tiny|small|normal|custom) profile="$answer" ;;
        *) warn "无效选择，使用推荐 profile：$recommended"; profile="$recommended" ;;
      esac
    fi
  fi
  apply_service_profile "$profile"
  service_profile_ready=1
}

resource_profile() {
  prepare_service_profile
  local detected_mem detected_cpu
  detected_mem="$(detect_mem_mb)"
  detected_cpu="$(detect_cpu_count)"
  cat <<EOF
detected_memory_mb=${detected_mem}
detected_cpu_count=${detected_cpu}
profile=${RELAYPILOT_PROFILE_EFFECTIVE:-$RELAYPILOT_PROFILE}
agent=${AGENT_SERVICE_MEMORY_MAX}/${AGENT_SERVICE_CPU_QUOTA}
hub=${HUB_SERVICE_MEMORY_MAX}/${HUB_SERVICE_CPU_QUOTA}
telegram=${TG_SERVICE_MEMORY_MAX}/${TG_SERVICE_CPU_QUOTA}
tasks_max=${SERVICE_TASKS_MAX}
restart_sec=${SERVICE_RESTART_SEC}
EOF
}

backup_file_if_exists() {
  local path="$1"
  if [[ -e "$path" ]]; then
    local backup="${path}.bak.$(date +%Y%m%d-%H%M%S)"
    cp -a "$path" "$backup"
    info "已备份：$backup"
  fi
}

hub_public_config_path() {
  printf '%s/%s\n' "$STATE_DIR" "$HUB_PUBLIC_CONFIG_NAME"
}

hub_public_config_get() {
  local key="$1" file
  file="$(hub_public_config_path)"
  [[ -f "$file" ]] || return 1
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

hub_tls_first_san() {
  local cert="${STATE_DIR}/hub-tls/hub.crt" sans
  [[ -f "$cert" ]] || return 1
  command -v openssl >/dev/null 2>&1 || return 1
  sans="$(openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null || true)"
  awk '
    {
      for (i = 1; i <= NF; i++) {
        gsub(/,/, "", $i)
        if ($i ~ /^DNS:/) {
          sub(/^DNS:/, "", $i)
          if ($i != "localhost") { print $i; found = 1; exit }
          local = $i
        }
      }
    }
    END { if (!found && local != "") print local }
  ' <<<"$sans"
}

hub_tls_first_ip_san() {
  local cert="${STATE_DIR}/hub-tls/hub.crt" sans
  [[ -f "$cert" ]] || return 1
  command -v openssl >/dev/null 2>&1 || return 1
  sans="$(openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null || true)"
  awk '
    {
      for (i = 1; i <= NF; i++) {
        gsub(/,/, "", $i)
        if ($i ~ /^Address:/) {
          sub(/^Address:/, "", $i)
          if ($i != "127.0.0.1" && $i != "::1") { print $i; found = 1; exit }
          local = $i
        }
      }
    }
    END { if (!found && local != "") print local }
  ' <<<"$sans"
}

hub_service_arg() {
  local flag="$1" file="${SYSTEMD_DIR}/${HUB_SERVICE_NAME}.service"
  [[ -f "$file" ]] || return 1
  awk -v flag="$flag" '
    /^ExecStart=/ {
      for (i = 1; i <= NF; i++) {
        if ($i == flag && (i + 1) <= NF) { print $(i + 1); exit }
      }
    }
  ' "$file"
}

hub_public_host_default() {
  local value
  value="$(hub_public_config_get HUB_PUBLIC_HOST 2>/dev/null || true)"
  [[ -n "$value" ]] && { printf '%s\n' "$value"; return 0; }
  value="$(hub_tls_first_san 2>/dev/null || true)"
  [[ -n "$value" ]] && { printf '%s\n' "$value"; return 0; }
  value="$(hub_tls_first_ip_san 2>/dev/null || true)"
  [[ -n "$value" ]] && { printf '%s\n' "$value"; return 0; }
  return 1
}

hub_public_port_default() {
  local value
  value="$(hub_public_config_get HUB_PUBLIC_PORT 2>/dev/null || true)"
  [[ -n "$value" ]] && { printf '%s\n' "$value"; return 0; }
  value="$(hub_service_arg --port 2>/dev/null || true)"
  [[ -n "$value" ]] && { printf '%s\n' "$value"; return 0; }
  return 1
}

save_hub_public_config() {
  local public_host="$1" cert_host="$2" port="$3" listen_host="$4" file
  file="$(hub_public_config_path)"
  mkdir -p "$STATE_DIR"
  cat > "$file" <<EOF_HUB_PUBLIC
HUB_PUBLIC_HOST=${public_host}
HUB_PUBLIC_CERT_HOST=${cert_host}
HUB_PUBLIC_PORT=${port}
HUB_LISTEN_HOST=${listen_host}
HUB_PUBLIC_URL=https://${public_host}:${port}
EOF_HUB_PUBLIC
  chmod 0644 "$file" 2>/dev/null || true
}

append_unique_path() {
  local var_name="$1" candidate="$2" existing
  [[ -n "$candidate" && -d "$candidate" ]] || return 0
  eval "existing=\" \${${var_name}[*]:-} \""
  if [[ "$existing" != *" $candidate "* ]]; then
    eval "${var_name}+=(\"\$candidate\")"
  fi
}

systemd_read_write_paths() {
  local paths=() config_parent
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  append_unique_path paths "$STATE_DIR"
  append_unique_path paths "$CONF_DIR"
  config_parent="$(dirname "$SINGBOX_CONFIG_PATH")"
  append_unique_path paths "$config_parent"
  printf '%s' "${paths[*]}"
}

install_managed_service() {
  require_root
  local name="$1" description="$2" exec_cmd="$3" memory_max="$4" cpu_quota="$5" service_path read_write_paths
  if command -v systemctl >/dev/null 2>&1 && [[ -d "$SYSTEMD_DIR" ]]; then
    service_path="${SYSTEMD_DIR}/${name}.service"
    if [[ -e "$service_path" && "${FORCE_SERVICE_FILE:-}" != "1" ]]; then
      info "服务文件已存在，未覆盖：$service_path"
      return 0
    fi
    backup_file_if_exists "$service_path"
    read_write_paths="$(systemd_read_write_paths)"
    cat > "$service_path" <<EOF_SERVICE
[Unit]
Description=${description}
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
ExecStart=${exec_cmd}
Restart=on-failure
RestartSec=${SERVICE_RESTART_SEC}s
MemoryMax=${memory_max}
CPUQuota=${cpu_quota}
TasksMax=${SERVICE_TASKS_MAX}
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=${read_write_paths}

[Install]
WantedBy=multi-user.target
EOF_SERVICE
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl enable "$name" >/dev/null 2>&1 || true
    info "已写入 systemd 服务：$service_path"
    info "启动：systemctl start $name"
  elif command -v rc-update >/dev/null 2>&1 && [[ -d "$OPENRC_DIR" ]]; then
    service_path="${OPENRC_DIR}/${name}"
    if [[ -e "$service_path" && "${FORCE_SERVICE_FILE:-}" != "1" ]]; then
      info "服务文件已存在，未覆盖：$service_path"
      return 0
    fi
    backup_file_if_exists "$service_path"
    local openrc_command openrc_args
    openrc_command="${exec_cmd%% *}"
    if [[ "$exec_cmd" == *" "* ]]; then
      openrc_args="${exec_cmd#* }"
    else
      openrc_args=""
    fi
    cat > "$service_path" <<EOF_SERVICE
#!/sbin/openrc-run
name="${description}"
command="${openrc_command}"
command_args="${openrc_args}"
command_background="yes"
pidfile="/run/${name}.pid"
supervisor=supervise-daemon
supervise_daemon_args="--respawn-delay ${SERVICE_RESTART_SEC} --respawn-max 5 --respawn-period 300"
depend() { need net; }
EOF_SERVICE
    chmod +x "$service_path"
    rc-update add "$name" default >/dev/null 2>&1 || true
    info "已写入 OpenRC 服务：$service_path"
    info "启动：rc-service $name start"
    warn "OpenRC 仅配置重启退避；内存/CPU 硬限制需由 cgroup/容器层设置。"
  else
    warn "未检测到可写的 systemd/OpenRC 服务目录，请手动配置服务。"
  fi
}

ensure_service_file() {
  if [[ "${RELAYPILOT_NO_ROOT:-}" == "1" ]]; then
    warn "RELAYPILOT_NO_ROOT=1，跳过服务文件写入。"
    return 0
  fi
  local config_path="$1" sb service_path
  sb="$(singbox_bin)"
  if [[ -z "$sb" ]]; then return 0; fi
  if command -v rc-update >/dev/null 2>&1 && [[ -d /etc/init.d ]]; then
    service_path="/etc/init.d/${SERVICE_NAME}"
    if [[ -e "$service_path" && "${FORCE_SERVICE_FILE:-}" != "1" ]]; then
      info "服务文件已存在，未覆盖：$service_path"
      return 0
    fi
    backup_file_if_exists "$service_path"
    cat > "$service_path" <<EOF_SERVICE
#!/sbin/openrc-run
name="${SERVICE_NAME}"
command="${sb}"
command_args="run -c ${config_path}"
command_background="yes"
pidfile="/run/${SERVICE_NAME}.pid"
supervisor=supervise-daemon
supervise_daemon_args="--respawn-max 0 --respawn-delay 5"
depend() { need net; }
EOF_SERVICE
    chmod +x "$service_path"
    rc-update add "$SERVICE_NAME" default >/dev/null 2>&1 || true
    info "已写入 OpenRC 服务：$service_path"
  elif command -v systemctl >/dev/null 2>&1 && [[ -d /etc/systemd/system ]]; then
    service_path="/etc/systemd/system/${SERVICE_NAME}.service"
    if [[ -e "$service_path" && "${FORCE_SERVICE_FILE:-}" != "1" ]]; then
      info "服务文件已存在，未覆盖：$service_path"
      return 0
    fi
    backup_file_if_exists "$service_path"
    cat > "$service_path" <<EOF_SERVICE
[Unit]
Description=sing-box service managed by RelayPilot
Documentation=https://sing-box.sagernet.org
After=network.target nss-lookup.target
Wants=network.target

[Service]
WorkingDirectory=/etc/sing-box
ExecStart=${sb} run -c ${config_path}
Restart=on-failure
RestartSec=10s
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
EOF_SERVICE
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
    info "已写入 systemd 服务：$service_path"
  else
    warn "未检测到可写的 systemd/OpenRC 服务目录，请手动配置 ${SERVICE_NAME} 服务。"
  fi
}

service_check() {
  local config="$1" sb
  sb="$(singbox_bin)"
  if [[ -z "$sb" ]]; then
    warn "未找到 sing-box，跳过真实 check。"
    return 0
  fi
  if [[ -d "$config" ]]; then
    "$sb" check -C "$config"
  else
    "$sb" check -c "$config"
  fi
}

install_cli_links() {
  local script_dest="$1"
  mkdir -p "$(dirname "$BIN_PATH")" "$(dirname "$HUB_BIN_PATH")" "$(dirname "$AGENT_BIN_PATH")"
  ln -sf "$script_dest" "$BIN_PATH"
  ln -sf "$script_dest" "$HUB_BIN_PATH"
  ln -sf "$script_dest" "$AGENT_BIN_PATH"
}

install_self() {
  require_root
  mkdir -p "$INSTALL_DIR/bin"
  if [[ -f "$SCRIPT_DIR/relaypilot.sh" ]]; then
    cp -a "$SCRIPT_DIR/relaypilot.sh" "$INSTALL_DIR/relaypilot.sh"
  else
    fetch "$RAW_BASE/relaypilot.sh" "$INSTALL_DIR/relaypilot.sh"
  fi
  if [[ -x "$GO_CORE" ]]; then
    cp -a "$GO_CORE" "$INSTALL_DIR/bin/relaypilot"
  elif [[ -x "$SCRIPT_DIR/bin/relaypilot" ]]; then
    cp -a "$SCRIPT_DIR/bin/relaypilot" "$INSTALL_DIR/bin/relaypilot"
  fi
  chmod +x "$INSTALL_DIR/relaypilot.sh"
  if [[ ! -x "$INSTALL_DIR/bin/relaypilot" ]]; then
    err "缺少 Go core，无法安装 Go-only RelayPilot。请先安装 release 二进制或设置 RELAYPILOT_GO_BIN。"
    return 1
  fi
  install_cli_links "$INSTALL_DIR/relaypilot.sh"
  info "已安装到：$INSTALL_DIR"
  info "CLI：$BIN_PATH"
  info "Hub 面板：$HUB_BIN_PATH"
  info "Agent 面板：$AGENT_BIN_PATH"
}

self_update() {
  require_root
  local version="${UPDATE_VERSION:-latest}" restart_services="${RELAYPILOT_UPDATE_RESTART:-ask}" raw_base="$RAW_BASE" release_base="$RELEASE_BASE" force=0
  RELAYPILOT_UPDATE_NOOP=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) version="$2"; shift 2 ;;
      --raw-base) raw_base="$2"; shift 2 ;;
      --release-base) release_base="$2"; shift 2 ;;
      --restart-services) restart_services=yes; shift ;;
      --no-restart-services) restart_services=no; shift ;;
      --force) force=1; shift ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done

  local target_version current_version
  if [[ "$version" == "latest" ]]; then
    target_version="$(resolve_latest_update_version "$raw_base" || true)"
  else
    target_version="$version"
  fi
  current_version="$(installed_core_version || true)"
  if [[ "$force" != "1" && -n "$target_version" && -n "$current_version" ]]; then
    if [[ "$(normalize_version "$target_version")" == "$(normalize_version "$current_version")" ]]; then
      RELAYPILOT_UPDATE_NOOP=1
      info "已是最新版本：$target_version"
      info "如需重新安装当前版本，请添加 --force。"
      return 0
    fi
  fi

  local platform asset url tmp script_tmp asset_tmp checksum
  platform="$(arch_name)"
  asset="relaypilot_${platform}"
  if [[ "$version" != latest ]]; then
    url="${release_base}/${version}/${asset}"
  else
    url="https://github.com/${REPO}/releases/latest/download/${asset}"
  fi

  tmp="$(mktemp -d /tmp/relaypilot-update.XXXXXX)"
  RELAYPILOT_UPDATE_TMP="$tmp"
  trap 'rm -rf "${RELAYPILOT_UPDATE_TMP:-}"' RETURN EXIT
  script_tmp="${tmp}/relaypilot.sh"
  asset_tmp="${tmp}/${asset}"
  checksum="${tmp}/${asset}.sha256"

  info "下载入口脚本：${raw_base}/relaypilot.sh"
  fetch "${raw_base}/relaypilot.sh" "$script_tmp"
  info "下载 Go core：$url"
  fetch "$url" "$asset_tmp"
  chmod +x "$script_tmp" "$asset_tmp"

  if fetch "${url}.sha256" "$checksum" 2>/dev/null; then
    if ! command -v sha256sum >/dev/null 2>&1; then
      err "缺少 sha256sum，无法校验 release。"
      return 1
    fi
    (cd "$tmp" && sha256sum -c "$(basename "$checksum")")
  else
    warn "未找到 sha256 文件，跳过校验。"
  fi

  mkdir -p "$INSTALL_DIR/bin" "$(dirname "$BIN_PATH")" "$(dirname "$HUB_BIN_PATH")" "$(dirname "$AGENT_BIN_PATH")"
  [[ -f "$INSTALL_DIR/relaypilot.sh" ]] && cp -a "$INSTALL_DIR/relaypilot.sh" "$INSTALL_DIR/relaypilot.sh.prev"
  [[ -f "$INSTALL_DIR/bin/relaypilot" ]] && cp -a "$INSTALL_DIR/bin/relaypilot" "$INSTALL_DIR/bin/relaypilot.prev"
  local script_dest core_dest script_new core_new
  script_dest="$INSTALL_DIR/relaypilot.sh"
  core_dest="$INSTALL_DIR/bin/relaypilot"
  script_new="${script_dest}.new.$$"
  core_new="${core_dest}.new.$$"
  rm -f "$script_new" "$core_new"
  cp -a "$script_tmp" "$script_new"
  cp -a "$asset_tmp" "$core_new"
  chmod +x "$script_new" "$core_new"
  mv -f "$script_new" "$script_dest"
  mv -f "$core_new" "$core_dest"
  install_cli_links "$script_dest"
  rm -rf "$tmp"
  unset RELAYPILOT_UPDATE_TMP

  info "已更新 RelayPilot：$BIN_PATH"
  info "Hub 面板：$HUB_BIN_PATH"
  info "Agent 面板：$AGENT_BIN_PATH"
  "$INSTALL_DIR/bin/relaypilot" version || true

  if [[ "$restart_services" == "ask" ]]; then
    if confirm "是否重启已安装的 RelayPilot 服务以应用新版本" y; then
      restart_services=yes
    else
      restart_services=no
    fi
  fi
  if [[ "$restart_services" == "yes" ]]; then
    restart_relaypilot_services
  else
    info "常驻服务会在下次重启后使用新版本。"
  fi
}

remove_path() {
  local path="$1"
  [[ -n "$path" && "$path" != "/" ]] || return 0
  case "$path" in
    /etc|/etc/|/opt|/opt/|/usr|/usr/|/usr/local|/usr/local/|/var|/var/|/home|/home/|/root|/root/|/tmp|/tmp/)
      err "拒绝删除高危路径：$path"
      return 1
      ;;
  esac
  if [[ ! -e "$path" && ! -L "$path" ]]; then return 0; fi
  if [[ "${UNINSTALL_DRY_RUN:-0}" == "1" ]]; then
    info "DRY-RUN 删除：$path"
  else
    rm -rf "$path"
    info "已删除：$path"
  fi
}

stop_disable_systemd_unit() {
  local unit="$1"
  if [[ "${UNINSTALL_DRY_RUN:-0}" == "1" || "${RELAYPILOT_NO_ROOT:-}" == "1" ]]; then
    info "DRY-RUN 停用服务：$unit"
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop "$unit" >/dev/null 2>&1 || true
    systemctl disable "$unit" >/dev/null 2>&1 || true
    systemctl reset-failed "$unit" >/dev/null 2>&1 || true
  fi
}

remove_systemd_unit_file() {
  local unit="$1"
  stop_disable_systemd_unit "$unit"
  remove_path "${SYSTEMD_DIR}/${unit}"
}

remove_openrc_service_file() {
  local name="$1"
  if [[ "${UNINSTALL_DRY_RUN:-0}" != "1" && "${RELAYPILOT_NO_ROOT:-}" != "1" ]] && command -v rc-service >/dev/null 2>&1; then
    rc-service "$name" stop >/dev/null 2>&1 || true
    rc-update del "$name" default >/dev/null 2>&1 || true
  fi
  remove_path "${OPENRC_DIR}/${name}"
}

remove_service_files() {
  local name="$1"
  remove_systemd_unit_file "${name}.service"
  remove_openrc_service_file "$name"
}

remove_timer_files() {
  local name="$1"
  remove_systemd_unit_file "${name}.timer"
  remove_systemd_unit_file "${name}.service"
  remove_openrc_service_file "$name"
}

reload_service_manager() {
  if [[ "${UNINSTALL_DRY_RUN:-0}" == "1" || "${RELAYPILOT_NO_ROOT:-}" == "1" ]]; then
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
}

reset_hub_state() {
  remove_service_files "$HUB_SERVICE_NAME"
  remove_service_files "$TG_SERVICE_NAME"
  remove_timer_files "$HUB_ALERT_TIMER_NAME"
  remove_path "$STATE_DIR/hub-tls"
  remove_path "$STATE_DIR/hub-agents.json"
  remove_path "$STATE_DIR/hub-removed-agents.json"
  remove_path "$STATE_DIR/hub-alerts.json"
  remove_path "$STATE_DIR/hub-tasks"
  remove_path "$STATE_DIR/hub-agent-tokens.json"
  remove_path "$STATE_DIR/hub-auth-nonces.json"
  remove_path "$STATE_DIR/hub-enroll-codes.json"
  remove_path "$(hub_public_config_path)"
  reload_service_manager
}

remove_relaypilot_proxy_fragments() {
  local path
  remove_service_files "$SERVICE_NAME"
  if [[ -d "$CONF_DIR" ]]; then
    while IFS= read -r -d '' path; do
      remove_path "$path"
    done < <(find "$CONF_DIR" -maxdepth 1 -type f -name '*relaypilot*.json' -print0 2>/dev/null || true)
  fi
  if [[ -d "$MESH_CONFIG_DIR" ]]; then
    while IFS= read -r -d '' path; do
      if grep -q 'RelayPilot managed WireGuard mesh' "$path" 2>/dev/null; then
        remove_path "$path"
      fi
    done < <(find "$MESH_CONFIG_DIR" -maxdepth 1 -type f -name '*.conf' -print0 2>/dev/null || true)
  fi
  reload_service_manager
}

reset_agent_state() {
  reset_agent_control_state
  remove_path "$STATE_DIR/endpoints"
  remove_path "$STATE_DIR/public-entries.json"
  remove_relaypilot_proxy_fragments
  reload_service_manager
}

reset_agent_control_state() {
  remove_service_files "$AGENT_SERVICE_NAME"
  remove_path "$STATE_DIR/agent-enrollment.json"
  remove_path "$STATE_DIR/agent-token"
  remove_path "$STATE_DIR/hub-ca.crt"
  remove_path "$STATE_DIR/agent.crt"
  remove_path "$STATE_DIR/agent.key"
  reload_service_manager
}

uninstall_preview() {
  local purge_state="$1" purge_proxy="$2" dry_run="$3"
  title "卸载预览"
  printf "  程序目录：     %s\n" "$INSTALL_DIR"
  printf "  命令入口：     %s\n" "$BIN_PATH"
  printf "  Hub 入口：     %s\n" "$HUB_BIN_PATH"
  printf "  Agent 入口：   %s\n" "$AGENT_BIN_PATH"
  printf "  RelayPilot 服务：%s, %s, %s, %s\n" "$HUB_SERVICE_NAME" "$AGENT_SERVICE_NAME" "$TG_SERVICE_NAME" "$HUB_ALERT_TIMER_NAME"
  if [[ "$purge_state" == "1" ]]; then
    printf "  状态目录：     删除 %s\n" "$STATE_DIR"
  else
    printf "  状态目录：     保留 %s\n" "$STATE_DIR"
  fi
  if [[ "$purge_proxy" == "1" ]]; then
    printf "  代理配置：     删除 %s 内 RelayPilot 片段；删除 WireGuard RelayPilot mesh\n" "$CONF_DIR"
    printf "  sing-box 主配置：保留 %s\n" "$SINGBOX_CONFIG_PATH"
  else
    printf "  代理配置：     保留\n"
  fi
  if [[ "$dry_run" == "1" ]]; then
    printf "  模式：         DRY-RUN，只预览不删除\n"
  fi
  return 0
}

uninstall_self() {
  require_root
  local purge_state=0 purge_proxy=0 dry_run=0 force=0
  [[ "${KEEP_STATE:-1}" == "0" ]] && purge_state=1
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --full|--purge-state|--delete-state) purge_state=1; shift ;;
      --keep-state) purge_state=0; shift ;;
      --purge-proxy-config|--delete-proxy-config) purge_proxy=1; shift ;;
      --dry-run) dry_run=1; shift ;;
      -y|--yes) force=1; shift ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done

  uninstall_preview "$purge_state" "$purge_proxy" "$dry_run"
  echo
  if [[ "$force" != "1" ]]; then
    if [[ ! -t 0 ]]; then
      err "非交互卸载需要 --yes；建议先加 --dry-run 预览。"
      return 1
    fi
    if ! confirm "确认执行卸载/清理" n; then
      info "已取消，未删除任何内容。"
      return 0
    fi
  fi

  UNINSTALL_DRY_RUN="$dry_run"
  remove_service_files "$HUB_SERVICE_NAME"
  remove_service_files "$AGENT_SERVICE_NAME"
  remove_service_files "$TG_SERVICE_NAME"
  remove_timer_files "$HUB_ALERT_TIMER_NAME"
  [[ "$purge_proxy" == "1" ]] && remove_relaypilot_proxy_fragments
  remove_path "$BIN_PATH"
  remove_path "$HUB_BIN_PATH"
  remove_path "$AGENT_BIN_PATH"
  remove_path "$INSTALL_DIR"
  if [[ "$purge_state" == "1" ]]; then
    remove_path "$STATE_DIR"
  elif [[ -d "$STATE_DIR" ]]; then
    info "已保留状态目录：$STATE_DIR"
  fi
  reload_service_manager
}

status() {
  title "RelayPilot status"
  echo "version: $VERSION"
  echo "state: $STATE_DIR"
  echo "conf dir: $CONF_DIR"
  echo "config file: $SINGBOX_CONFIG_PATH"
  local sb; sb="$(singbox_bin)"
  echo "sing-box: ${sb:-not found}"
  echo
  title "endpoints"
  list_endpoints || true
  echo
  if [[ -n "$sb" ]]; then
    if [[ -d "$CONF_DIR" ]]; then service_check "$CONF_DIR" || true
    elif [[ -f "$SINGBOX_CONFIG_PATH" ]]; then service_check "$SINGBOX_CONFIG_PATH" || true
    fi
  fi
}

doctor() {
  title "RelayPilot doctor"
  local ok=0
  echo "go core: $GO_CORE"
  if [[ -x "$GO_CORE" ]]; then echo "go core: OK"; else echo "go core: MISSING"; ok=1; fi
  local sb; sb="$(singbox_bin)"
  if [[ -n "$sb" ]]; then echo "sing-box: OK ($sb)"; else echo "sing-box: not found"; fi
  [[ -d "$STATE_DIR" ]] && echo "state dir: exists $STATE_DIR" || echo "state dir: absent $STATE_DIR"
  [[ -d "$CONF_DIR" ]] && echo "conf dir: exists $CONF_DIR" || echo "conf dir: absent $CONF_DIR"
  [[ -f "$SINGBOX_CONFIG_PATH" ]] && echo "config file: exists $SINGBOX_CONFIG_PATH" || echo "config file: absent $SINGBOX_CONFIG_PATH"
  return "$ok"
}

list_endpoints() { core_cmd list-endpoints --state-dir "$STATE_DIR"; }
show_endpoint() { local name="${1:-}"; [[ -z "$name" ]] && prompt name "出口名称" "${ENDPOINT_NAME:-landing}"; core_cmd export-endpoint --state-dir "$STATE_DIR" "$name"; }
inspect_conf() { local conf="${1:-}"; [[ -z "$conf" ]] && { if [[ -d "$CONF_DIR" ]]; then conf="$CONF_DIR"; else conf="$SINGBOX_CONFIG_PATH"; fi; }; core_cmd inspect-conf --conf "$conf"; }
connection_info() { local conf="${1:-}"; [[ -z "$conf" ]] && { if [[ -d "$CONF_DIR" ]]; then conf="$CONF_DIR"; else conf="$SINGBOX_CONFIG_PATH"; fi; }; core_cmd agent-connection-info --state-dir "$STATE_DIR" --conf "$conf"; }
migrate_state() { core_cmd migrate-state "$@"; }
public_entry_set() { core_cmd public-entry-set --state-dir "$STATE_DIR" "$@"; }
public_entry_list() { core_cmd public-entry-list --state-dir "$STATE_DIR" "$@"; }

json_file_string_value() {
  local file="$1" key="$2"
  [[ -f "$file" ]] || return 0
  sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$file" | head -n1
}

json_file_number_value() {
  local file="$1" key="$2"
  [[ -f "$file" ]] || return 0
  sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$file" | head -n1
}

config_inbound_listen_port() {
  local file="$1" tag="$2"
  [[ -f "$file" ]] || return 0
  awk -v tag="$tag" '
    $0 ~ "\"tag\"[[:space:]]*:[[:space:]]*\"" tag "\"" { seen = 1 }
    seen && $0 ~ "\"listen_port\"[[:space:]]*:" {
      gsub(/[^0-9]/, "", $0)
      print
      exit
    }
  ' "$file"
}

first_endpoint_file() {
  local file
  [[ -d "$STATE_DIR/endpoints" ]] || return 0
  for file in "$STATE_DIR"/endpoints/*.json; do
    [[ -f "$file" ]] || continue
    printf '%s\n' "$file"
    return 0
  done
}

default_endpoint_name() {
  local file
  file="$(first_endpoint_file)"
  [[ -n "$file" ]] && basename "$file" .json
}

public_entry_wizard() {
  require_root
  local use="$1" label="$2" network="$3" name host public_port local_port endpoint_file input_port default_name default_host default_public_port default_local_port
  endpoint_file="$(first_endpoint_file)"
  case "$use" in
    reality)
      default_name="${TRANSIT_INBOUND_TAG:-vless-in}"
      default_host="${REALITY_PUBLIC_HOST:-${PUBLIC_ENTRY_HOST:-}}"
      default_local_port="${REALITY_LOCAL_PORT:-${TRANSIT_PORT:-$(config_inbound_listen_port "$CONF_DIR/00-relaypilot-reality.json" "$default_name")}}"
      default_public_port="${REALITY_PUBLIC_PORT:-${default_local_port:-443}}"
      ;;
    shadowsocks)
      default_name="${PUBLIC_ENTRY_NAME:-${LANDING_NAME:-$(default_endpoint_name)}}"
      default_host="${PUBLIC_ENTRY_HOST:-$(json_file_string_value "$endpoint_file" server)}"
      default_public_port="${PUBLIC_ENTRY_PUBLIC_PORT:-$(json_file_number_value "$endpoint_file" server_port)}"
      default_local_port="${PUBLIC_ENTRY_LOCAL_PORT:-${LANDING_PORT:-$default_public_port}}"
      ;;
    wireguard)
      default_name="${PUBLIC_ENTRY_NAME:-$(default_endpoint_name)}"
      [[ -z "$default_name" ]] && default_name="default"
      default_host="${PUBLIC_ENTRY_HOST:-}"
      default_public_port="${PUBLIC_ENTRY_PUBLIC_PORT:-${MESH_PUBLIC_PORT:-${MESH_PORT:-}}}"
      default_local_port="${PUBLIC_ENTRY_LOCAL_PORT:-${MESH_LOCAL_PORT:-${MESH_PORT:-$default_public_port}}}"
      ;;
  esac
  [[ -z "$default_name" ]] && default_name="default"
  title "配置${label}公网入口"
  prompt name "名称" "$default_name"
  prompt host "对外 IP/域名" "$default_host"
  input_port="$(port_from_host_input "$host")"
  if [[ -n "$input_port" ]]; then
    default_public_port="$input_port"
    host="$(host_only "$host")"
  fi
  prompt public_port "对外端口" "$default_public_port"
  if ! valid_port "$public_port"; then
    err "对外端口必须是 1-65535：$public_port"
    return 1
  fi
  prompt local_port "本地端口" "${default_local_port:-$public_port}"
  if [[ -n "$local_port" ]] && ! valid_port "$local_port"; then
    err "本地端口必须是 1-65535：$local_port"
    return 1
  fi
  public_entry_set --use "$use" --name "$name" --host "$(host_only "$host")" --public-port "$public_port" --local-port "${local_port:-0}" --network "$network"
  info "公网入口已保存：${name} ${host}:${public_port} -> 本地:${local_port:-0}/${network}"
}

public_entry_menu() {
  require_root
  while true; do
    menu_title "公网入口"
    menu_item 1 "配置 Reality 入口"
    menu_item 2 "配置 Shadowsocks 入口"
    menu_item 3 "配置 WireGuard 入口"
    menu_item 4 "查看公网入口"
    menu_back
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) menu_action public_entry_wizard reality "Reality" tcp ;;
      2) menu_action public_entry_wizard shadowsocks "Shadowsocks" tcp ;;
      3) menu_action public_entry_wizard wireguard "WireGuard" udp ;;
      4) menu_action public_entry_list ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

transit_init_reality() {
  require_root
  ensure_singbox || true
  title "中转节点：初始化 Reality 入口"
  local conf="${TRANSIT_CONF:-$CONF_DIR}" listen listen_port inbound_tag="${TRANSIT_INBOUND_TAG:-vless-in}" server_name handshake_server handshake_port private_key short_id access_host access_port detected_access_host="" input_port args existing_listen existing_listen_port existing_server_name existing_access_host existing_access_port existing_local_port
  existing_listen="$(json_string_field_from_conf "$conf" listen)"
  existing_listen_port="$(json_number_field_from_conf "$conf" listen_port)"
  existing_server_name="$(json_string_field_from_conf "$conf" server_name)"
  existing_access_host="$(public_entry_string_field reality "$inbound_tag" host)"
  existing_access_port="$(public_entry_number_field reality "$inbound_tag" public_port)"
  existing_local_port="$(public_entry_number_field reality "$inbound_tag" local_port)"
  prompt listen "本地地址/IP" "${TRANSIT_LISTEN:-${existing_listen:-::}}"
  prompt listen_port "本地端口" "${TRANSIT_PORT:-${existing_listen_port:-${existing_local_port:-443}}}"
  prompt server_name "伪装域名/SNI" "${TRANSIT_SERVER_NAME:-${existing_server_name:-www.cloudflare.com}}"
  private_key="${TRANSIT_PRIVATE_KEY:-$(json_string_field_from_conf "$conf" private_key)}"
  private_key="${private_key:-$(random_reality_private_key)}"
  prompt private_key "Reality 私钥" "$private_key"
  short_id="${TRANSIT_SHORT_ID:-$(json_string_field_from_conf "$conf" short_id)}"
  short_id="${short_id:-$(random_hex 8)}"
  prompt short_id "Reality short_id" "$short_id"
  if [[ -z "${REALITY_PUBLIC_HOST:-${PUBLIC_ENTRY_HOST:-${TRANSIT_PUBLIC_HOST:-}}}" && -t 0 ]]; then
    detected_access_host="$(detect_public_ip || true)"
  fi
  prompt access_host "访问地址/IP（空则自动检测）" "${REALITY_PUBLIC_HOST:-${PUBLIC_ENTRY_HOST:-${TRANSIT_PUBLIC_HOST:-${existing_access_host:-$detected_access_host}}}}"
  input_port="$(port_from_host_input "$access_host")"
  if [[ -n "$input_port" ]]; then
    access_port="$input_port"
    access_host="$(host_only "$access_host")"
  fi
  prompt access_port "访问端口" "${REALITY_PUBLIC_PORT:-${PUBLIC_ENTRY_PUBLIC_PORT:-${TRANSIT_PUBLIC_PORT:-${access_port:-${existing_access_port:-$listen_port}}}}}"
  if [[ -n "$access_port" ]] && ! valid_port "$access_port"; then
    err "访问端口必须是 1-65535：$access_port"
    return 1
  fi
  handshake_server="${TRANSIT_HANDSHAKE_SERVER:-$server_name}"
  handshake_port="${TRANSIT_HANDSHAKE_PORT:-443}"
  args=(ensure-transit-reality --conf "$conf" --state-dir "$STATE_DIR" --listen "$listen" --listen-port "$listen_port" --inbound-tag "$inbound_tag" --server-name "$server_name" --handshake-server "$handshake_server" --handshake-port "$handshake_port")
  [[ -n "$private_key" ]] && args+=(--private-key "$private_key")
  [[ -n "$short_id" ]] && args+=(--short-id "$short_id")
  if ! core_cmd "${args[@]}" >/dev/null; then
    return 1
  fi
  if [[ -n "$access_host" ]]; then
    if public_entry_set --use reality --name "$inbound_tag" --host "$access_host" --public-port "$access_port" --local-port "$listen_port" --network tcp >/dev/null; then
      info "公网入口已记录：${access_host}:${access_port} -> 本地:${listen_port}"
    else
      warn "公网入口记录失败，可稍后在 Agent 模式 -> 公网入口 中重设。"
    fi
  fi
  info "Reality 入口已更新：${listen}:${listen_port}"
  service_check "$conf"
  ensure_service_file "$conf"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
}

landing_install_ss() {
  require_root
  ensure_singbox || true
  title "安装/更新 Shadowsocks"
  local name server listen listen_port server_port method password inbound_tag endpoint_tag endpoint_file detected_server="" existing_name existing_server existing_server_port existing_listen existing_listen_port existing_method existing_password
  existing_name="$(json_string_field_from_endpoint shadowsocks name)"
  existing_server="$(json_string_field_from_endpoint shadowsocks server)"
  existing_server_port="$(json_number_field_from_endpoint shadowsocks server_port)"
  existing_listen="$(json_string_field_from_conf "$SINGBOX_CONFIG_PATH" listen)"
  existing_listen_port="$(json_number_field_from_conf "$SINGBOX_CONFIG_PATH" listen_port)"
  existing_method="$(json_string_field_from_conf "$SINGBOX_CONFIG_PATH" method)"
  existing_method="${existing_method:-$(json_string_field_from_endpoint shadowsocks method)}"
  existing_password="$(json_string_field_from_conf "$SINGBOX_CONFIG_PATH" password)"
  existing_password="${existing_password:-$(json_string_field_from_endpoint shadowsocks password)}"
  prompt name "名称" "${LANDING_NAME:-${existing_name:-landing}}"
  prompt listen "本地地址/IP" "${LANDING_LISTEN:-${existing_listen:-::}}"
  prompt listen_port "本地端口" "${LANDING_PORT:-${existing_listen_port:-443}}"
  select_option method "Shadowsocks 加密方式" "${LANDING_METHOD:-${existing_method:-2022-blake3-aes-128-gcm}}" \
    "2022-blake3-aes-128-gcm|2022 AES-128-GCM|推荐，轻量安全" \
    "2022-blake3-aes-256-gcm|2022 AES-256-GCM|更强，稍重" \
    "chacha20-ietf-poly1305|ChaCha20-Poly1305|兼容老环境"
  if [[ -n "${LANDING_PASSWORD:-}" ]]; then
    password="$LANDING_PASSWORD"
  elif [[ -n "$existing_password" && "$existing_method" == "$method" ]]; then
    password="$existing_password"
  else
    password="$(core_cmd generate-ss-password --method "$method")"
  fi
  prompt password "Shadowsocks 密码" "$password"
  if [[ -z "${LANDING_SERVER:-}" && -t 0 ]]; then
    detected_server="$(detect_public_ip || true)"
  fi
  prompt server "访问地址/IP（空则自动检测）" "${LANDING_SERVER:-${existing_server:-$detected_server}}"
  prompt server_port "访问端口" "${LANDING_SERVER_PORT:-${existing_server_port:-$listen_port}}"
  inbound_tag="${LANDING_INBOUND_TAG:-ss-in}"
  endpoint_tag="${LANDING_ENDPOINT_TAG:-landing-${name}-ss}"
  mkdir -p "$STATE_DIR/endpoints"
  endpoint_file="$STATE_DIR/endpoints/${name}.json"
  backup_file_if_exists "$SINGBOX_CONFIG_PATH"
  backup_file_if_exists "$endpoint_file"
  if ! core_cmd render-landing-ss \
    --name "$name" --server "$server" --listen "$listen" \
    --listen-port "$listen_port" --server-port "$server_port" \
    --method "$method" --password "$password" \
    --inbound-tag "$inbound_tag" --endpoint-tag "$endpoint_tag" \
    --config-output "$SINGBOX_CONFIG_PATH" --endpoint-output "$endpoint_file" >/dev/null; then
    return 1
  fi
  if public_entry_set --use shadowsocks --name "$name" --host "$server" --public-port "$server_port" --local-port "$listen_port" --network tcp >/dev/null; then
    info "公网入口已记录：${server}:${server_port} -> 本地:${listen_port}"
  else
    warn "公网入口记录失败，可稍后在 Agent 模式 -> 公网入口 中重设。"
  fi
  info "落地配置已写入：$SINGBOX_CONFIG_PATH"
  info "出口 JSON 已写入：$endpoint_file"
  service_check "$SINGBOX_CONFIG_PATH"
  ensure_service_file "$SINGBOX_CONFIG_PATH"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
  echo; title "复制给中转机导入的出口 JSON"; cat "$endpoint_file"
}

landing_install_socks() {
  require_root
  ensure_singbox || true
  title "安装/更新 SOCKS5"
  local name server listen listen_port server_port username password inbound_tag endpoint_tag endpoint_file detected_server="" existing_name existing_server existing_server_port existing_listen existing_listen_port existing_username existing_password
  existing_name="$(json_string_field_from_endpoint socks name)"
  existing_server="$(json_string_field_from_endpoint socks server)"
  existing_server_port="$(json_number_field_from_endpoint socks server_port)"
  existing_listen="$(json_string_field_from_conf "$SINGBOX_CONFIG_PATH" listen)"
  existing_listen_port="$(json_number_field_from_conf "$SINGBOX_CONFIG_PATH" listen_port)"
  existing_username="$(json_string_field_from_conf "$SINGBOX_CONFIG_PATH" username)"
  existing_username="${existing_username:-$(json_string_field_from_endpoint socks username)}"
  existing_password="$(json_string_field_from_conf "$SINGBOX_CONFIG_PATH" password)"
  existing_password="${existing_password:-$(json_string_field_from_endpoint socks password)}"
  prompt name "名称" "${LANDING_SOCKS_NAME:-${existing_name:-landing-direct}}"
  prompt listen "本地地址/IP" "${LANDING_SOCKS_LISTEN:-${existing_listen:-::}}"
  prompt listen_port "本地端口" "${LANDING_SOCKS_PORT:-${existing_listen_port:-1080}}"
  prompt username "SOCKS 用户名（留空则不鉴权）" "${LANDING_SOCKS_USERNAME:-$existing_username}"
  if [[ -n "$username" ]]; then
    if [[ -n "${LANDING_SOCKS_PASSWORD:-}" ]]; then
      password="$LANDING_SOCKS_PASSWORD"
    elif [[ "$username" == "$existing_username" && -n "$existing_password" ]]; then
      password="$existing_password"
    else
      password="$(random_token 18)"
    fi
    prompt password "SOCKS 密码" "$password"
  else
    password=""
  fi
  if [[ -z "${LANDING_SOCKS_SERVER:-}" && -t 0 ]]; then
    detected_server="$(detect_public_ip || true)"
  fi
  prompt server "访问地址/IP（空则自动检测）" "${LANDING_SOCKS_SERVER:-${existing_server:-$detected_server}}"
  prompt server_port "访问端口" "${LANDING_SOCKS_SERVER_PORT:-${existing_server_port:-$listen_port}}"
  inbound_tag="${LANDING_SOCKS_INBOUND_TAG:-socks-in}"
  endpoint_tag="${LANDING_SOCKS_ENDPOINT_TAG:-landing-${name}-socks}"
  mkdir -p "$STATE_DIR/endpoints"
  endpoint_file="$STATE_DIR/endpoints/${name}.json"
  backup_file_if_exists "$SINGBOX_CONFIG_PATH"
  backup_file_if_exists "$endpoint_file"
  if ! core_cmd render-landing-socks \
    --name "$name" --server "$server" --listen "$listen" \
    --listen-port "$listen_port" --server-port "$server_port" \
    --username "$username" --password "$password" \
    --inbound-tag "$inbound_tag" --endpoint-tag "$endpoint_tag" \
    --config-output "$SINGBOX_CONFIG_PATH" --endpoint-output "$endpoint_file" >/dev/null; then
    return 1
  fi
  info "落地配置已写入：$SINGBOX_CONFIG_PATH"
  info "出口 JSON 已写入：$endpoint_file"
  if [[ -n "$username" ]]; then
    info "SOCKS5 连接信息：${server}:${server_port}（用户名：${username}）"
  else
    info "SOCKS5 连接信息：${server}:${server_port}（无鉴权）"
  fi
  service_check "$SINGBOX_CONFIG_PATH"
  ensure_service_file "$SINGBOX_CONFIG_PATH"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
}

transit_import_bind() {
  require_root
  title "中转节点：绑定出口"
  local endpoint_file conf="${TRANSIT_CONF:-$CONF_DIR}" inbound_tag="${TRANSIT_INBOUND_TAG:-}" auth_user client_uuid imported existing_client_uuid
  prompt endpoint_file "落地出口 JSON 文件路径" "${ENDPOINT_FILE:-$(first_endpoint_json_file)}"
  [[ ! -f "$endpoint_file" ]] && { err "endpoint 文件不存在：$endpoint_file"; return 1; }
  mkdir -p "$STATE_DIR/endpoints"
  imported="$(core_cmd import-endpoint --state-dir "$STATE_DIR" "$endpoint_file")"
  info "已导入出口：$imported"
  prompt auth_user "客户端名称" "${TRANSIT_AUTH_USER:-$(basename "$endpoint_file" .json)}"
  existing_client_uuid="$(vless_user_uuid_from_conf "$conf" "$auth_user")"
  prompt client_uuid "客户端 UUID" "${TRANSIT_UUID:-${existing_client_uuid:-$(random_uuid)}}"
  local args=(bind-transit --conf "$conf" --endpoint "$endpoint_file" --state-dir "$STATE_DIR" --auth-user "$auth_user")
  [[ -n "$inbound_tag" ]] && args+=(--inbound-tag "$inbound_tag")
  [[ -n "$client_uuid" ]] && args+=(--uuid "$client_uuid")
  if ! core_cmd "${args[@]}" >/dev/null; then
    return 1
  fi
  info "出口已绑定：${auth_user}"
  service_check "$conf"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
}

tg_setup() {
  require_root
  local token chat_id api_base
  prompt token "Telegram Bot Token" "${TG_BOT_TOKEN:-}"
  prompt chat_id "Telegram Chat ID" "${TG_CHAT_ID:-}"
  prompt api_base "Telegram API 地址" "${TG_API_BASE:-https://api.telegram.org}"
  core_cmd tg-config --state-dir "$STATE_DIR" --bot-token "$token" --chat-id "$chat_id" --api-base "$api_base"
  info "Telegram 配置已写入：$STATE_DIR/telegram.json"
}

tg_status() { core_cmd tg-status --state-dir "$STATE_DIR"; }
tg_commands() { core_cmd tg-commands "$@"; }
tg_register_commands() {
  local dry_arg=()
  [[ "${TG_DRY_RUN:-}" == "1" ]] && dry_arg+=(--dry-run)
  core_cmd tg-register-commands --state-dir "$STATE_DIR" "${dry_arg[@]}" "$@"
}
tg_get_commands() {
  local dry_arg=()
  [[ "${TG_DRY_RUN:-}" == "1" ]] && dry_arg+=(--dry-run)
  core_cmd tg-get-commands --state-dir "$STATE_DIR" "${dry_arg[@]}" "$@"
}
tg_delete_commands() {
  local dry_arg=()
  [[ "${TG_DRY_RUN:-}" == "1" ]] && dry_arg+=(--dry-run)
  core_cmd tg-delete-commands --state-dir "$STATE_DIR" "${dry_arg[@]}" "$@"
}
tg_dispatch() { local text="${1:-}"; [[ -z "$text" ]] && prompt text "Telegram command text" "/status"; core_cmd tg-dispatch --text "$text" --state-dir "$STATE_DIR" --conf "$CONF_DIR"; }
tg_hub_dispatch() { local text="${1:-}"; [[ -z "$text" ]] && prompt text "Hub Telegram command text" "/status"; core_cmd tg-dispatch --hub --text "$text" --state-dir "$STATE_DIR"; }
tg_send() { local text="${1:-}"; [[ -z "$text" ]] && prompt text "Telegram message text" "RelayPilot test"; local dry_arg=(); [[ "${TG_DRY_RUN:-}" == "1" ]] && dry_arg+=(--dry-run); core_cmd tg-send --text "$text" --state-dir "$STATE_DIR" "${dry_arg[@]}"; }

hub_agent_export() {
  local agent_id role name endpoint labels output
  agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "${AGENT_ID:-$(hostname 2>/dev/null || echo agent)}"
  role="${2:-}"
  [[ -z "$role" ]] && select_agent_role role "${AGENT_ROLE:-transit}"
  prompt name "显示名称" "${AGENT_NAME:-$agent_id}"
  prompt endpoint "节点回连地址（可留空，默认主动拉取任务）" "${AGENT_ENDPOINT:-}"
  prompt labels "labels，逗号分隔 key=value（可留空）" "${AGENT_LABELS:-}"
  prompt output "输出注册文件路径" "${AGENT_REG_OUTPUT:-${STATE_DIR}/${agent_id}.registration.json}"
  local snapshot_conf=""
  if [[ "$role" == "transit" ]]; then
    snapshot_conf="${TRANSIT_CONF:-$CONF_DIR}"
  elif [[ "$role" == "landing" ]]; then
    snapshot_conf="${SINGBOX_CONFIG_PATH:-}"
  fi
  local args=(hub-agent-export --agent-id "$agent_id" --role "$role" --name "$name" --state-dir "$STATE_DIR" --output "$output")
  [[ -n "$endpoint" ]] && args+=(--endpoint "$endpoint")
  [[ -n "$labels" ]] && args+=(--labels "$labels")
  [[ -n "$snapshot_conf" ]] && args+=(--conf "$snapshot_conf")
  core_cmd "${args[@]}"
  info "把该 registration JSON 导入 Hub：$output"
}

hub_import_agent() {
  local registration="${1:-}"
  [[ -z "$registration" ]] && prompt registration "节点注册 JSON 路径" "${AGENT_REGISTRATION:-}"
  core_cmd hub-import-agent --state-dir "$STATE_DIR" "$registration"
}

hub_issue_token() {
  if [[ "${1:-}" == --* ]]; then
    core_cmd hub-issue-token --state-dir "$STATE_DIR" "$@"
    return
  fi
  local agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "${AGENT_ID:-}"
  core_cmd hub-issue-token --state-dir "$STATE_DIR" "${@:2}" "$agent_id"
}

hub_rotate_token() {
  if [[ "${1:-}" == --* ]]; then
    core_cmd hub-rotate-token --state-dir "$STATE_DIR" "$@"
    return
  fi
  local agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "${AGENT_ID:-}"
  core_cmd hub-rotate-token --state-dir "$STATE_DIR" "${@:2}" "$agent_id"
}

hub_revoke_token() {
  local agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "${AGENT_ID:-}"
  shift || true
  core_cmd hub-revoke-token --state-dir "$STATE_DIR" "$@" "$agent_id"
}

hub_tokens() { core_cmd hub-tokens --state-dir "$STATE_DIR" "$@"; }

hub_daemon() {
  local host="${HUB_HOST:-127.0.0.1}"
  local port="${HUB_PORT:-8080}"
  core_cmd hub-daemon --state-dir "$STATE_DIR" --host "$host" --port "$port" "$@"
}

tg_hub_daemon() {
  local interval="${TG_POLL_INTERVAL:-2}"
  local timeout="${TG_POLL_TIMEOUT:-25}"
  core_cmd bot-daemon --state-dir "$STATE_DIR" --interval "$interval" --timeout "$timeout" "$@"
}

hub_agents() { core_cmd hub-agents --state-dir "$STATE_DIR" "$@"; }
hub_sync_agent() { local agent_id="${1:-}"; [[ -z "$agent_id" ]] && prompt agent_id "要刷新的节点 ID" "${AGENT_ID:-}"; core_cmd hub-sync-agent --state-dir "$STATE_DIR" --agent-id "$agent_id"; }
hub_sync_all() { core_cmd hub-sync-all --state-dir "$STATE_DIR"; }
hub_export_client() { core_cmd hub-export-client --state-dir "$STATE_DIR" "$@"; }
hub_export_landing() { core_cmd hub-export-landing --state-dir "$STATE_DIR" "$@"; }
hub_remove_agent() { local agent_id="${1:-}"; [[ -z "$agent_id" ]] && prompt agent_id "要移除的节点 ID" "${AGENT_ID:-}"; shift || true; core_cmd hub-remove-agent --state-dir "$STATE_DIR" "$@" "$agent_id"; }
hub_removed_agents() { core_cmd hub-removed-agents --state-dir "$STATE_DIR" "$@"; }
hub_decommission_agent() {
  require_root
  local agent_id="${1:-${AGENT_ID:-}}" mode="${DECOMMISSION_MODE:-uninstall}" action="${DECOMMISSION_ACTION:-preview}" text
  [[ -z "$agent_id" ]] && prompt agent_id "要远程退役的节点 ID" ""
  select_option mode "退役模式" "$mode" \
    "detach|退出 Hub 托管|删除接入凭证，保留代理配置" \
    "purge-managed-proxy|清理托管代理配置|删除 RelayPilot 代理片段，保留程序" \
    "uninstall|彻底卸载|删除 RelayPilot、状态和托管代理片段"
  select_option action "执行方式" "$action" \
    "preview|预览|只下发 dry-run，不删除节点文件" \
    "execute|确认执行|节点需本机已开启远程退役授权"
  text="/decommission ${agent_id} --mode ${mode}"
  if [[ "$action" == "execute" ]]; then
    title "远程退役节点"
    printf "  目标：%s\n" "$agent_id"
    printf "  模式：%s\n" "$mode"
    printf "  要求：节点本机已开启远程退役授权。\n"
    printf "  范围：只执行 RelayPilot 白名单退役动作，不执行任意 shell。\n"
    echo
    if ! confirm "确认远程退役该节点" n; then
      info "已取消，未下发退役任务。"
      return 0
    fi
    text+=" --confirm ${agent_id}"
  fi
  hub_dispatch "$text"
}
hub_alert_offline() { core_cmd hub-alert-offline --state-dir "$STATE_DIR" "$@"; }
hub_alerts() { core_cmd hub-alerts --state-dir "$STATE_DIR" "$@"; }
hub_recover_tasks() { core_cmd hub-recover-tasks --state-dir "$STATE_DIR" "$@"; }
hub_init_tls() { core_cmd hub-init-tls --state-dir "$STATE_DIR" "$@"; }
hub_issue_agent_cert() { core_cmd hub-issue-agent-cert --state-dir "$STATE_DIR" "$@"; }
hub_provision_agent() { core_cmd hub-provision-agent --state-dir "$STATE_DIR" "$@"; }
hub_enroll_code() { core_cmd hub-create-enroll-code --state-dir "$STATE_DIR" "$@"; }

hub_enroll_wizard() {
  require_root
  local agent_id="${AGENT_ID:-}" role="${AGENT_ROLE:-transit}" public_host="${HUB_PUBLIC_HOST:-}" ttl_minutes port="${HUB_PORT:-8443}"
  local default_public_host default_port public_host_locked=0 parsed_minutes
  ttl_minutes="$(duration_to_minutes "${HUB_ENROLL_MINUTES:-${HUB_ENROLL_TTL:-10m}}" 2>/dev/null || printf '10\n')"
  default_public_host="$(hub_public_host_default 2>/dev/null || true)"
  default_port="$(hub_public_port_default 2>/dev/null || true)"
  if [[ -z "$public_host" && -n "$default_public_host" ]]; then
    public_host="$default_public_host"
    public_host_locked=1
  elif [[ -n "$public_host" ]]; then
    public_host_locked=1
  fi
  [[ "${HUB_PORT:-}" == "" && -n "$default_port" ]] && port="$default_port"
  title "生成节点邀请码"
  select_option role "节点角色" "$role" \
    "transit|中转节点|接入用户，转发到落地" \
    "landing|落地节点|提供出口"
  local default_agent_id
  if [[ "$role" == "landing" ]]; then
    default_agent_id="landing-$(hostname 2>/dev/null || echo node)"
  else
    default_agent_id="transit-$(hostname 2>/dev/null || echo node)"
  fi
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID（英文数字短横线，例如 ${default_agent_id}）" "$default_agent_id"
  local input_port
  if [[ "$public_host_locked" == "1" ]]; then
    input_port="$(port_from_host_input "$public_host")"
    [[ -n "$input_port" ]] && port="$input_port"
    [[ -n "$public_host" ]] && public_host="$(host_only "$public_host")"
    printf "Hub URL： %s%s%s\n" "$BOLD" "https://${public_host}:${port}" "$NC"
  else
    prompt public_host "Hub 访问地址/IP（空则自动检测）" "$public_host"
    input_port="$(port_from_host_input "$public_host")"
    [[ -n "$input_port" ]] && port="$input_port"
    [[ -n "$public_host" ]] && public_host="$(host_only "$public_host")"
    prompt port "Hub 访问端口" "$port"
  fi
  if ! valid_port "$port"; then
    err "Hub HTTPS 端口必须是 1-65535：$port"
    return 1
  fi
  prompt ttl_minutes "邀请码有效期（分钟）" "$ttl_minutes"
  if ! parsed_minutes="$(duration_to_minutes "$ttl_minutes" 2>/dev/null)" || (( parsed_minutes < 1 )); then
    err "邀请码有效期请输入分钟数，例如 10、30、60。"
    return 1
  fi
  local args=(--agent-id "$agent_id" --role "$role" --ttl "${parsed_minutes}m" --port "$port" --text)
  [[ -n "$public_host" ]] && args+=(--public-host "$public_host")
  info "Generating invite..."
  hub_enroll_code "${args[@]}"
}

agent_enroll() {
  local install_service=0 args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --install-service) install_service=1; shift ;;
      *) args+=("$1"); shift ;;
    esac
  done
  core_cmd agent-enroll --state-dir "$STATE_DIR" "${args[@]}"
  if [[ "$install_service" == "1" ]]; then
    install_agent_service --enrollment-file "${STATE_DIR}/agent-enrollment.json"
  fi
}

hub_alert_callback() {
  if [[ "${1:-}" == --* ]]; then
    core_cmd hub-alert-callback --state-dir "$STATE_DIR" "$@"
    return
  fi
  local data="${1:-}"
  [[ -z "$data" ]] && prompt data "Telegram 回调数据" "${CALLBACK_DATA:-}"
  core_cmd hub-alert-callback --state-dir "$STATE_DIR" --data "$data"
}
hub_dispatch() { local text="${1:-}"; [[ -z "$text" ]] && prompt text "Hub command text" "/status"; core_cmd hub-dispatch --state-dir "$STATE_DIR" --text "$text"; }

select_hub_agent_by_role() {
  local var_name="$1" role="$2" label="$3" default="${4:-}" output line id row_role name transport choice idx
  local rows=() ids=()
  output="$(hub_agents 2>/dev/null || true)"
  while IFS=$'\t' read -r id row_role name transport; do
    [[ "$row_role" == "$role" && -n "$id" ]] || continue
    ids+=("$id")
    rows+=("$id"$'\t'"${name:-$id}"$'\t'"${transport:-poll}")
  done <<< "$output"

  if [[ "${#ids[@]}" -eq 0 ]]; then
    warn "没有找到${label}节点，请先生成邀请码并让 Agent 接入 Hub。"
    prompt "$var_name" "${label}节点 ID" "$default"
    return
  fi

  echo
  title "选择${label}节点"
  for idx in "${!rows[@]}"; do
    IFS=$'\t' read -r id name transport <<< "${rows[$idx]}"
    printf "%2d) %s · %s · %s\n" "$((idx + 1))" "$id" "$name" "$transport"
  done
  if [[ -n "$default" ]]; then
    printf "选择序号，或输入节点 ID [%s%s%s]: " "$BOLD$CYAN" "$default" "$NC"
    read -r choice || true
    choice="${choice:-$default}"
  else
    read -r -p "选择序号，或输入节点 ID: " choice || true
  fi
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#ids[@]} )); then
    printf -v "$var_name" '%s' "${ids[$((choice - 1))]}"
  else
    printf -v "$var_name" '%s' "$choice"
  fi
}

hub_link_wizard() {
  require_root
  local transit_id="${TRANSIT_AGENT_ID:-}" landing_id="${LANDING_AGENT_ID:-}" auth_user="${TRANSIT_AUTH_USER:-}" endpoint_name="${ENDPOINT_NAME:-}" inbound_tag="${TRANSIT_INBOUND_TAG:-}" link_mode="${RELAYPILOT_LINK_MODE:-${LINK_MODE:-direct}}" mesh_cidr="${MESH_CIDR:-}" mesh_port="${MESH_PORT:-}" mesh_endpoint="${MESH_ENDPOINT:-}"
  title "串联中转/落地"
  select_hub_agent_by_role transit_id "transit" "中转" "$transit_id"
  select_hub_agent_by_role landing_id "landing" "落地" "$landing_id"
  select_option link_mode "链路模式" "$link_mode" \
    "direct|直连 TCP|兼容 NAT/禁 UDP，默认推荐" \
    "mesh|自动组网 UDP|WireGuard，需 transit 可发 UDP 且 landing 可收 UDP"
  prompt auth_user "客户端名称（空=自动）" "$auth_user"
  prompt endpoint_name "出口名称（空=自动）" "$endpoint_name"
  local text="/link ${transit_id} ${landing_id}"
  [[ -n "$link_mode" ]] && text+=" --mode ${link_mode}"
  if [[ "$link_mode" =~ ^(mesh|auto|overlay|wg|wireguard|自动组网|组网)$ ]]; then
    prompt mesh_cidr "WireGuard /30 网段（空=自动生成）" "$mesh_cidr"
    prompt mesh_port "落地 WireGuard UDP 端口（空=自动生成）" "$mesh_port"
    prompt mesh_endpoint "落地 WireGuard 地址:端口（空=沿用出口）" "$mesh_endpoint"
    [[ -n "$mesh_cidr" ]] && text+=" --mesh-cidr ${mesh_cidr}"
    [[ -n "$mesh_port" ]] && text+=" --mesh-port ${mesh_port}"
    [[ -n "$mesh_endpoint" ]] && text+=" --mesh-endpoint ${mesh_endpoint}"
  fi
  [[ -n "$auth_user" ]] && text+=" --auth-user ${auth_user}"
  [[ -n "$endpoint_name" ]] && text+=" --endpoint-name ${endpoint_name}"
  [[ -n "$inbound_tag" ]] && text+=" --inbound-tag ${inbound_tag}"
  hub_dispatch "$text"
}
hub_tasks() { core_cmd hub-tasks --state-dir "$STATE_DIR" "$@"; }
hub_results() { core_cmd hub-results --state-dir "$STATE_DIR" "$@"; }

agent_poll_once() {
  if [[ $# -gt 0 ]]; then
    core_cmd agent-poll-once --state-dir "$STATE_DIR" "$@"
    return
  fi
  local hub_url="${HUB_URL:-}" agent_id="${AGENT_ID:-}" token_file="${AGENT_TOKEN_FILE:-}" role="${AGENT_ROLE:-}" name="${AGENT_NAME:-}" labels="${AGENT_LABELS:-}" conf="${AGENT_CONF:-$CONF_DIR}" ca_cert="${AGENT_CA_CERT:-}" client_cert="${AGENT_CLIENT_CERT:-}" client_key="${AGENT_CLIENT_KEY:-}" tls_server_name="${AGENT_TLS_SERVER_NAME:-}" ip_mode="${AGENT_IP_MODE:-}" public_ip_interval="${AGENT_PUBLIC_IP_INTERVAL:-}"
  [[ -z "$hub_url" ]] && prompt hub_url "Hub URL" "http://127.0.0.1:8080"
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "$(hostname 2>/dev/null || echo agent)"
  [[ -z "$role" ]] && select_agent_role role "transit"
  local args=(--hub-url "$hub_url" --agent-id "$agent_id" --role "$role" --state-dir "$STATE_DIR" --conf "$conf")
  [[ -n "$name" ]] && args+=(--name "$name")
  [[ -n "$labels" ]] && args+=(--labels "$labels")
  [[ -n "$ca_cert" ]] && args+=(--ca-cert "$ca_cert")
  [[ -n "$client_cert" ]] && args+=(--client-cert "$client_cert")
  [[ -n "$client_key" ]] && args+=(--client-key "$client_key")
  [[ -n "$tls_server_name" ]] && args+=(--tls-server-name "$tls_server_name")
  [[ -n "$ip_mode" ]] && args+=(--ip-mode "$ip_mode")
  [[ -n "$public_ip_interval" ]] && args+=(--public-ip-interval "$public_ip_interval")
  if [[ -n "$token_file" ]]; then
    args+=(--token-file "$token_file")
  elif [[ -z "${AGENT_TOKEN:-}" ]]; then
    prompt token_file "节点 token 文件" "${STATE_DIR}/agent-token"
    args+=(--token-file "$token_file")
  fi
  core_cmd agent-poll-once "${args[@]}"
}

agent_poll_loop() {
  if [[ $# -gt 0 ]]; then
    core_cmd agent-poll-loop --state-dir "$STATE_DIR" "$@"
    return
  fi
  local hub_url="${HUB_URL:-}" agent_id="${AGENT_ID:-}" token_file="${AGENT_TOKEN_FILE:-}" role="${AGENT_ROLE:-}" name="${AGENT_NAME:-}" labels="${AGENT_LABELS:-}" conf="${AGENT_CONF:-$CONF_DIR}" interval="${AGENT_POLL_INTERVAL:-30}" topology_interval="${AGENT_TOPOLOGY_INTERVAL:-300}" ca_cert="${AGENT_CA_CERT:-}" client_cert="${AGENT_CLIENT_CERT:-}" client_key="${AGENT_CLIENT_KEY:-}" tls_server_name="${AGENT_TLS_SERVER_NAME:-}" ip_mode="${AGENT_IP_MODE:-}" public_ip_interval="${AGENT_PUBLIC_IP_INTERVAL:-}"
  [[ -z "$hub_url" ]] && prompt hub_url "Hub URL" "http://127.0.0.1:8080"
  [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "$(hostname 2>/dev/null || echo agent)"
  [[ -z "$role" ]] && select_agent_role role "transit"
  local args=(--hub-url "$hub_url" --agent-id "$agent_id" --role "$role" --state-dir "$STATE_DIR" --conf "$conf" --interval "$interval" --topology-interval "$topology_interval")
  [[ -n "$name" ]] && args+=(--name "$name")
  [[ -n "$labels" ]] && args+=(--labels "$labels")
  [[ -n "$ca_cert" ]] && args+=(--ca-cert "$ca_cert")
  [[ -n "$client_cert" ]] && args+=(--client-cert "$client_cert")
  [[ -n "$client_key" ]] && args+=(--client-key "$client_key")
  [[ -n "$tls_server_name" ]] && args+=(--tls-server-name "$tls_server_name")
  [[ -n "$ip_mode" ]] && args+=(--ip-mode "$ip_mode")
  [[ -n "$public_ip_interval" ]] && args+=(--public-ip-interval "$public_ip_interval")
  if [[ -n "$token_file" ]]; then
    args+=(--token-file "$token_file")
  elif [[ -z "${AGENT_TOKEN:-}" ]]; then
    prompt token_file "节点 token 文件" "${STATE_DIR}/agent-token"
    args+=(--token-file "$token_file")
  fi
  core_cmd agent-poll-loop "${args[@]}"
}

install_agent_service() {
  local hub_url="${HUB_URL:-}" agent_id="${AGENT_ID:-}" token_file="${AGENT_TOKEN_FILE:-}" role="${AGENT_ROLE:-}" conf="${AGENT_CONF:-$CONF_DIR}" interval="${AGENT_POLL_INTERVAL:-30}" topology_interval="${AGENT_TOPOLOGY_INTERVAL:-300}" ca_cert="${AGENT_CA_CERT:-}" client_cert="${AGENT_CLIENT_CERT:-}" client_key="${AGENT_CLIENT_KEY:-}" tls_server_name="${AGENT_TLS_SERVER_NAME:-}" enrollment_file="${AGENT_ENROLLMENT_FILE:-}" ip_mode="${AGENT_IP_MODE:-}" public_ip_interval="${AGENT_PUBLIC_IP_INTERVAL:-}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --enrollment-file) enrollment_file="$2"; shift 2 ;;
      --hub-url) hub_url="$2"; shift 2 ;;
      --agent-id) agent_id="$2"; shift 2 ;;
      --role) role="$2"; shift 2 ;;
      --token-file) token_file="$2"; shift 2 ;;
      --conf) conf="$2"; shift 2 ;;
      --ca-cert) ca_cert="$2"; shift 2 ;;
      --client-cert) client_cert="$2"; shift 2 ;;
      --client-key) client_key="$2"; shift 2 ;;
      --tls-server-name) tls_server_name="$2"; shift 2 ;;
      --interval) interval="$2"; shift 2 ;;
      --topology-interval) topology_interval="$2"; shift 2 ;;
      --ip-mode) ip_mode="$2"; shift 2 ;;
      --public-ip-interval) public_ip_interval="$2"; shift 2 ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done
  if [[ -z "$enrollment_file" && -f "${STATE_DIR}/agent-enrollment.json" ]]; then
    enrollment_file="${STATE_DIR}/agent-enrollment.json"
  fi
  if [[ -n "$enrollment_file" && ! -f "$enrollment_file" ]]; then
    err "enrollment file 不存在：$enrollment_file。请先在 Agent 菜单粘贴 Hub invite 接入。"
    return 1
  fi
  if [[ -z "$enrollment_file" ]]; then
    [[ -z "$hub_url" ]] && prompt hub_url "Hub URL" "http://127.0.0.1:8080"
    [[ -z "$agent_id" ]] && prompt agent_id "节点 ID" "$(hostname 2>/dev/null || echo agent)"
    [[ -z "$role" ]] && select_agent_role role "transit"
    [[ -z "$token_file" ]] && prompt token_file "节点 token 文件" "${STATE_DIR}/agent-token"
  fi
  prepare_service_profile
  local exec_cmd
  if [[ -n "$enrollment_file" ]]; then
    exec_cmd="${GO_CORE} agent-poll-loop --state-dir ${STATE_DIR} --enrollment-file ${enrollment_file} --conf ${conf} --interval ${interval} --topology-interval ${topology_interval}"
  else
    exec_cmd="${GO_CORE} agent-poll-loop --state-dir ${STATE_DIR} --hub-url ${hub_url} --agent-id ${agent_id} --role ${role} --token-file ${token_file} --conf ${conf} --interval ${interval} --topology-interval ${topology_interval}"
    [[ -n "$ca_cert" ]] && exec_cmd+=" --ca-cert ${ca_cert}"
    [[ -n "$client_cert" ]] && exec_cmd+=" --client-cert ${client_cert}"
    [[ -n "$client_key" ]] && exec_cmd+=" --client-key ${client_key}"
    [[ -n "$tls_server_name" ]] && exec_cmd+=" --tls-server-name ${tls_server_name}"
  fi
  [[ -n "$ip_mode" ]] && exec_cmd+=" --ip-mode ${ip_mode}"
  [[ -n "$public_ip_interval" ]] && exec_cmd+=" --public-ip-interval ${public_ip_interval}"
  install_managed_service "$AGENT_SERVICE_NAME" "RelayPilot agent poll loop" "$exec_cmd" "$AGENT_SERVICE_MEMORY_MAX" "$AGENT_SERVICE_CPU_QUOTA"
}

install_hub_service() {
  local host="${HUB_HOST:-127.0.0.1}" port="${HUB_PORT:-8080}" tls_cert="${HUB_TLS_CERT:-}" tls_key="${HUB_TLS_KEY:-}" client_ca="${HUB_CLIENT_CA:-}" require_client_cert="${HUB_REQUIRE_CLIENT_CERT:-false}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --host) host="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      --tls-cert) tls_cert="$2"; shift 2 ;;
      --tls-key) tls_key="$2"; shift 2 ;;
      --client-ca) client_ca="$2"; shift 2 ;;
      --require-client-cert) require_client_cert="true"; shift ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done
  prepare_service_profile
  local exec_cmd
  exec_cmd="${GO_CORE} hub-daemon --state-dir ${STATE_DIR} --host ${host} --port ${port} --quiet"
  [[ -n "$tls_cert" ]] && exec_cmd+=" --tls-cert ${tls_cert}"
  [[ -n "$tls_key" ]] && exec_cmd+=" --tls-key ${tls_key}"
  [[ -n "$client_ca" ]] && exec_cmd+=" --client-ca ${client_ca}"
  [[ "$require_client_cert" == "true" ]] && exec_cmd+=" --require-client-cert"
  install_managed_service "$HUB_SERVICE_NAME" "RelayPilot hub api" "$exec_cmd" "$HUB_SERVICE_MEMORY_MAX" "$HUB_SERVICE_CPU_QUOTA"
}

install_tg_hub_service() {
  local interval="${TG_POLL_INTERVAL:-2}" timeout="${TG_POLL_TIMEOUT:-25}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --interval) interval="$2"; shift 2 ;;
      --timeout) timeout="$2"; shift 2 ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done
  prepare_service_profile
  local exec_cmd
  exec_cmd="${GO_CORE} bot-daemon --state-dir ${STATE_DIR} --interval ${interval} --timeout ${timeout} --quiet"
  install_managed_service "$TG_SERVICE_NAME" "RelayPilot bot hub daemon" "$exec_cmd" "$TG_SERVICE_MEMORY_MAX" "$TG_SERVICE_CPU_QUOTA"
}

hub_telegram_quick_setup() {
  require_root
  if ! confirm "绑定 Telegram 并启用面板" n; then
    return 0
  fi
  if ! hub_telegram_setup; then
    warn "Telegram 面板未启用，可稍后在 Hub → Telegram 中重试。"
  fi
}

install_alert_timer() {
  require_root
  local name="${HUB_ALERT_TIMER_NAME}" interval="${HUB_ALERT_TIMER_INTERVAL}" threshold="${HUB_ALERT_THRESHOLD_SECONDS}" snooze="${HUB_ALERT_SNOOZE_SECONDS}" dry_run="" service_path timer_path exec_cmd
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name) name="$2"; shift 2 ;;
      --interval) interval="$2"; shift 2 ;;
      --threshold-seconds) threshold="$2"; shift 2 ;;
      --snooze-seconds) snooze="$2"; shift 2 ;;
      --dry-run) dry_run=" --dry-run"; shift ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done
  if ! command -v systemctl >/dev/null 2>&1 || [[ ! -d "$SYSTEMD_DIR" ]]; then
    warn "未检测到 systemd；请用 cron/OpenRC 手动定期运行：${BIN_PATH} hub-alert-offline --state-dir ${STATE_DIR}"
    return 1
  fi
  service_path="${SYSTEMD_DIR}/${name}.service"
  timer_path="${SYSTEMD_DIR}/${name}.timer"
  if [[ ( -e "$service_path" || -e "$timer_path" ) && "${FORCE_SERVICE_FILE:-}" != "1" ]]; then
    info "timer/service 已存在，未覆盖：$service_path / $timer_path"
    return 0
  fi
  backup_file_if_exists "$service_path"
  backup_file_if_exists "$timer_path"
  exec_cmd="${BIN_PATH} hub-alert-offline --state-dir ${STATE_DIR} --threshold-seconds ${threshold} --snooze-seconds ${snooze}${dry_run}"
  cat > "$service_path" <<EOF_SERVICE
[Unit]
Description=RelayPilot offline agent alert scan
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${exec_cmd}
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=${STATE_DIR}
EOF_SERVICE
  cat > "$timer_path" <<EOF_TIMER
[Unit]
Description=Run RelayPilot offline alert scan periodically

[Timer]
OnBootSec=5min
OnUnitActiveSec=${interval}
RandomizedDelaySec=60s
Persistent=true
Unit=${name}.service

[Install]
WantedBy=timers.target
EOF_TIMER
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl enable "${name}.timer" >/dev/null 2>&1 || true
  info "已写入 systemd timer：$timer_path"
  info "启动：systemctl start ${name}.timer"
}

tg_register_hub_commands() {
  local dry_arg=()
  [[ "${TG_DRY_RUN:-}" == "1" ]] && dry_arg+=(--dry-run)
  core_cmd tg-register-commands --hub --state-dir "$STATE_DIR" "${dry_arg[@]}"
}

hub_telegram_setup() {
  require_root
  tg_setup || return 1
  hub_telegram_repair
}

hub_telegram_repair() {
  require_root
  if [[ ! -s "${STATE_DIR}/telegram.json" ]]; then
    warn "尚未绑定 Telegram，请先选择“绑定/修改 Telegram”。"
    return 1
  fi
  if FORCE_SERVICE_FILE=1 install_tg_hub_service; then
    info "Telegram 服务已安装/更新：${TG_SERVICE_NAME}"
  else
    warn "Telegram 服务安装失败。"
    return 1
  fi
  if tg_register_hub_commands; then
    info "Telegram 面板命令已注册：/relaypilot"
  else
    warn "Telegram 命令注册失败，可稍后选择“修复 Telegram 面板”重试。"
    return 1
  fi
  if service_action "$TG_SERVICE_NAME" restart; then
    info "Telegram 面板已启用。"
  else
    warn "${TG_SERVICE_NAME} 重启失败，请查看日志。"
    return 1
  fi
}

detect_public_ip() {
  command -v curl >/dev/null 2>&1 || return 1
  curl -fsSL --max-time 4 https://api.ipify.org 2>/dev/null || \
    curl -fsSL --max-time 4 https://ifconfig.me/ip 2>/dev/null || \
    curl -fsSL --max-time 4 https://ipinfo.io/ip 2>/dev/null
}

hub_quick_setup() {
  require_root
  local public_host="${HUB_PUBLIC_HOST:-}" port="${HUB_PORT:-8443}" listen_host="${HUB_LISTEN_HOST:-0.0.0.0}"
  local saved_public_host saved_port saved_listen_host
  saved_public_host="$(hub_public_host_default 2>/dev/null || true)"
  saved_port="$(hub_public_port_default 2>/dev/null || true)"
  saved_listen_host="$(hub_public_config_get HUB_LISTEN_HOST 2>/dev/null || true)"
  [[ -z "$public_host" && -n "$saved_public_host" ]] && public_host="$saved_public_host"
  [[ "${HUB_PORT:-}" == "" && -n "$saved_port" ]] && port="$saved_port"
  [[ "${HUB_LISTEN_HOST:-}" == "" && -n "$saved_listen_host" ]] && listen_host="$saved_listen_host"
  local detected="" tls_args=()
  title "初始化 Hub"
  if [[ -z "$public_host" ]]; then
    detected="$(detect_public_ip || true)"
  fi
  prompt public_host "Hub 访问地址/IP（空则自动检测）" "${public_host:-$detected}"
  [[ -z "$public_host" ]] && { err "需要 Hub 访问地址/IP；也可以稍后运行 hub-init-tls --host <IP_OR_DOMAIN>。"; return 1; }
  local input_port cert_host public_host_for_url
  input_port="$(port_from_host_input "$public_host")"
  [[ -n "$input_port" ]] && port="$input_port"
  cert_host="$(host_only "$public_host")"
  public_host_for_url="$(url_host "$public_host")"
  prompt port "Hub 访问端口" "$port"
  if ! valid_port "$port"; then
    err "Hub 访问端口必须是 1-65535：$port"
    return 1
  fi
  prompt listen_host "Hub 本地地址/IP" "$listen_host"
  [[ -z "$listen_host" ]] && listen_host="0.0.0.0"

  title "Hub 配置预览"
  printf "  Hub URL：      https://%s:%s\n" "$public_host_for_url" "$port"
  printf "  本地监听：     %s:%s\n" "$listen_host" "$port"
  printf "  证书 SAN：     %s\n" "$cert_host"
  printf "  状态目录：     %s\n" "$STATE_DIR"
  printf "  systemd 服务： %s\n" "$HUB_SERVICE_NAME"
  echo
  if ! confirm "确认写入/更新 Hub 配置" y; then
    info "未写入任何 Hub 配置。"
    return 0
  fi

  if [[ -f "$STATE_DIR/hub-tls/hub.crt" && -f "$STATE_DIR/hub-tls/hub.key" && -f "$STATE_DIR/hub-tls/ca.crt" ]] && hub_tls_cert_matches_host "$STATE_DIR/hub-tls/hub.crt" "$cert_host"; then
    info "检测到已有 Hub TLS 文件，跳过证书初始化：$STATE_DIR/hub-tls"
  elif [[ -f "$STATE_DIR/hub-tls/hub.crt" || -f "$STATE_DIR/hub-tls/hub.key" || -f "$STATE_DIR/hub-tls/ca.crt" ]]; then
    tls_args=(--host "$cert_host" --force)
    info "检测到 Hub TLS 与当前地址不匹配，重新生成证书 SAN：$cert_host"
    hub_init_tls "${tls_args[@]}"
  else
    tls_args=(--host "$cert_host")
    info "初始化 Hub TLS，证书 SAN 包含：$cert_host"
    hub_init_tls "${tls_args[@]}"
  fi

  FORCE_SERVICE_FILE=1 install_hub_service \
    --host "$listen_host" \
    --port "$port" \
    --tls-cert "$STATE_DIR/hub-tls/hub.crt" \
    --tls-key "$STATE_DIR/hub-tls/hub.key" \
    --client-ca "$STATE_DIR/hub-tls/ca.crt" \
    --require-client-cert
  save_hub_public_config "$public_host_for_url" "$cert_host" "$port" "$listen_host"
  echo
  info "Hub URL 给 agent 使用：https://${public_host_for_url}:${port}"
  info "继续：在 Hub 菜单生成节点邀请码；Agent 端粘贴邀请码即可连接。"
  if confirm "是否现在启动 ${HUB_SERVICE_NAME}" y; then
    if service_action "$HUB_SERVICE_NAME" restart; then
      if command -v systemctl >/dev/null 2>&1 && ! systemctl is-active --quiet "$HUB_SERVICE_NAME"; then
        warn "${HUB_SERVICE_NAME} 未处于 active 状态，请查看：journalctl -u ${HUB_SERVICE_NAME} -n 80 --no-pager"
      fi
    else
      warn "${HUB_SERVICE_NAME} 启动失败，请查看：journalctl -u ${HUB_SERVICE_NAME} -n 80 --no-pager"
    fi
  fi
  hub_telegram_quick_setup
}

agent_join_wizard() {
  require_root
  local invite="${AGENT_INVITE:-}" role ip_mode="${AGENT_IP_MODE:-static}" ip_check_minutes ip_check_seconds
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --invite|--enroll)
        invite="$2"; shift 2 ;;
      --ip-mode)
        ip_mode="$2"; shift 2 ;;
      --public-ip-interval)
        ip_check_seconds="$2"; shift 2 ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done
  title "Agent 模式"
  if [[ -z "$invite" ]]; then
    prompt invite "Hub 邀请码" ""
  else
    info "已读取 Hub invite。"
  fi
  [[ -z "$invite" ]] && { warn "邀请码为空。"; return 0; }
  select_ip_mode ip_mode "$ip_mode"
  ip_check_seconds="${ip_check_seconds:-${AGENT_PUBLIC_IP_INTERVAL:-600}}"
  if [[ "$ip_mode" == "dynamic" ]]; then
    ip_check_minutes="$(duration_to_minutes "${AGENT_IP_CHECK_MINUTES:-$(( ip_check_seconds / 60 ))}" 2>/dev/null || printf '10\n')"
    prompt ip_check_minutes "公网 IP 检测间隔（分钟）" "$ip_check_minutes"
    if ! ip_check_minutes="$(duration_to_minutes "$ip_check_minutes" 2>/dev/null)" || (( ip_check_minutes < 1 )); then
      err "公网 IP 检测间隔请输入分钟数，例如 10、30、60。"
      return 1
    fi
    ip_check_seconds="$(( ip_check_minutes * 60 ))"
  fi
  agent_enroll --invite "$invite" --ip-mode "$ip_mode" --public-ip-interval "$ip_check_seconds"
  role="$(agent_enrollment_role)"
  case "$role" in
    transit)
      if confirm "检测到中转节点，是否初始化/更新 Reality 入口" y; then
        transit_init_reality
      fi
      ;;
    landing)
      if confirm "检测到落地节点，是否安装/更新 Shadowsocks" y; then
        landing_install_ss
      fi
      ;;
    *)
      warn "未识别 Agent role：${role:-空}，跳过数据面配置。"
      ;;
  esac
  install_agent_service --enrollment-file "${STATE_DIR}/agent-enrollment.json"
  info "Agent 已连接 Hub：${AGENT_SERVICE_NAME}"
}

agent_enrollment_role() {
  local file="${STATE_DIR}/agent-enrollment.json"
  [[ -f "$file" ]] || return 0
  sed -n 's/.*"role"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$file" | head -n1
}

agent_enrollment_value() {
  local key="$1" file="${STATE_DIR}/agent-enrollment.json"
  [[ -f "$file" ]] || return 0
  sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$file" | head -n1
}

agent_enrollment_number() {
  local key="$1" file="${STATE_DIR}/agent-enrollment.json"
  [[ -f "$file" ]] || return 0
  sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$file" | head -n1
}

agent_ip_mode_wizard() {
  require_root
  if [[ $# -gt 0 ]]; then
    core_cmd agent-set-ip-mode --state-dir "$STATE_DIR" "$@"
    return
  fi
  if [[ ! -f "${STATE_DIR}/agent-enrollment.json" ]]; then
    warn "Agent 尚未连接 Hub。请先接入 Hub。"
    return 0
  fi
  local current_mode ip_mode current_seconds ip_check_minutes ip_check_seconds
  current_mode="$(agent_enrollment_value ip_mode)"
  [[ -z "$current_mode" ]] && current_mode="static"
  current_seconds="$(agent_enrollment_number public_ip_interval_seconds)"
  [[ -z "$current_seconds" || "$current_seconds" -le 0 ]] && current_seconds=600
  title "Agent IP 模式"
  select_ip_mode ip_mode "$current_mode"
  ip_check_seconds="$current_seconds"
  if [[ "$ip_mode" == "dynamic" ]]; then
    ip_check_minutes="$(duration_to_minutes "$(( current_seconds / 60 ))" 2>/dev/null || printf '10\n')"
    [[ "$ip_check_minutes" -lt 1 ]] && ip_check_minutes=10
    prompt ip_check_minutes "公网 IP 检测间隔（分钟）" "$ip_check_minutes"
    if ! ip_check_minutes="$(duration_to_minutes "$ip_check_minutes" 2>/dev/null)" || (( ip_check_minutes < 1 )); then
      err "公网 IP 检测间隔请输入分钟数，例如 10、30、60。"
      return 1
    fi
    ip_check_seconds="$(( ip_check_minutes * 60 ))"
  fi
  core_cmd agent-set-ip-mode --state-dir "$STATE_DIR" --mode "$ip_mode" --public-ip-interval "$ip_check_seconds"
  info "已更新 Agent IP 模式：$ip_mode（下次轮询生效）"
}

show_agent_enrollment() {
  if [[ -f "${STATE_DIR}/agent-enrollment.json" ]]; then
    local hub_url agent_id role_label token_file ca_cert client_cert client_key ip_mode interval
    hub_url="$(agent_enrollment_value hub_url)"
    agent_id="$(agent_enrollment_value agent_id)"
    role_label="$(agent_role_label)"
    token_file="$(agent_enrollment_value token_file)"
    ca_cert="$(agent_enrollment_value ca_cert)"
    client_cert="$(agent_enrollment_value client_cert)"
    client_key="$(agent_enrollment_value client_key)"
    ip_mode="$(agent_enrollment_value ip_mode)"
    interval="$(agent_enrollment_number public_ip_interval_seconds)"
    printf "Hub：%s\n" "${hub_url:-未配置}"
    printf "Agent ID：%s\n" "${agent_id:-未配置}"
    printf "角色：%s\n" "${role_label:-Agent}"
    [[ -n "$ip_mode" ]] && printf "IP 模式：%s\n" "$ip_mode"
    [[ -n "$interval" ]] && printf "公网 IP 检测间隔：%s 秒\n" "$interval"
    [[ -n "$token_file" ]] && printf "Token 文件：%s\n" "$token_file"
    [[ -n "$ca_cert" ]] && printf "CA 证书：%s\n" "$ca_cert"
    [[ -n "$client_cert" ]] && printf "客户端证书：%s\n" "$client_cert"
    [[ -n "$client_key" ]] && printf "客户端私钥：%s\n" "$client_key"
  else
    warn "Agent 尚未连接 Hub。"
  fi
}

agent_enrolled() {
  [[ -f "${STATE_DIR}/agent-enrollment.json" ]]
}

agent_role_config_label() {
  printf '代理配置'
}

agent_role_config_menu() {
  case "$(agent_enrollment_role)" in
    transit) transit_menu ;;
    landing) landing_menu ;;
    *)
      while true; do
        menu_title "代理配置"
        menu_item 1 "配置中转 Reality"
        menu_item 2 "配置落地出口"
        menu_back
        menu_prompt choice "0-2"
        case "${choice:-}" in
          1) menu_action transit_init_reality ;;
          2) landing_menu ;;
          0) return 0 ;;
          *) menu_invalid_choice ;;
        esac
      done
      ;;
  esac
}

agent_remote_decommission_enabled() {
  local file="${STATE_DIR}/agent-policy.json"
  [[ -f "$file" ]] || return 1
  grep -Eq '"allow_remote_decommission"[[:space:]]*:[[:space:]]*true' "$file"
}

write_agent_remote_decommission_policy() {
  local enabled="$1"
  mkdir -p "$STATE_DIR"
  if [[ "$enabled" == "1" ]]; then
    cat > "${STATE_DIR}/agent-policy.json" <<'EOF_POLICY'
{
  "allow_remote_decommission": true
}
EOF_POLICY
    chmod 0600 "${STATE_DIR}/agent-policy.json" 2>/dev/null || true
  else
    cat > "${STATE_DIR}/agent-policy.json" <<'EOF_POLICY'
{
  "allow_remote_decommission": false
}
EOF_POLICY
    chmod 0600 "${STATE_DIR}/agent-policy.json" 2>/dev/null || true
  fi
}

agent_remote_decommission_policy_menu() {
  require_root
  while true; do
    menu_title "远程退役授权"
    if agent_remote_decommission_enabled; then
      printf "  状态：已启用\n"
    else
      printf "  状态：未启用\n"
    fi
    printf "  用途：允许 Hub 下发白名单退役任务。\n"
    printf "  范围：退出托管、清理 RelayPilot 代理片段、卸载 RelayPilot。\n"
    echo
    menu_item 1 "启用远程退役授权"
    menu_item 2 "关闭远程退役授权"
    menu_back
    menu_prompt choice "0-2"
    case "${choice:-}" in
      1)
        if confirm "启用后 Hub 可远程退役本节点" n; then
          write_agent_remote_decommission_policy 1
          info "已启用远程退役授权。"
        fi
        ;;
      2)
        write_agent_remote_decommission_policy 0
        info "已关闭远程退役授权。"
        ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

agent_network_menu() {
  require_root
  while true; do
    menu_title "Agent 网络设置"
    menu_item 1 "IP 模式"
    menu_item 2 "公网入口"
    menu_back
    menu_prompt choice "0-2"
    case "${choice:-}" in
      1) menu_action agent_ip_mode_wizard ;;
      2) public_entry_menu ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

agent_advanced_menu() {
  require_root
  while true; do
    menu_title "Agent 高级操作"
    menu_item 1 "Hub 接入信息"
    menu_item 2 "远程退役授权"
    menu_item 3 "退出 Hub 托管"
    menu_item 4 "重置 Agent"
    menu_back
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) menu_action show_agent_enrollment ;;
      2) agent_remote_decommission_policy_menu ;;
      3) menu_action reset_agent_control_menu_action ;;
      4) menu_action reset_agent_menu_action ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

agent_unenrolled_menu() {
  require_root
  while ! agent_enrolled; do
    menu_title "Agent 模式"
    printf "  Agent 尚未接入 Hub。\n"
    echo
    menu_item 1 "接入 Hub"
    menu_item 2 "代理配置"
    menu_item 3 "连接信息"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action agent_join_wizard ;;
      2) agent_role_config_menu ;;
      3) menu_action connection_info ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

agent_mode_menu() {
  require_root
  if ! agent_enrolled; then
    agent_unenrolled_menu
    agent_enrolled || return 0
  fi
  local role_label agent_id config_label
  while true; do
    role_label="$(agent_role_label)"
    agent_id="$(agent_enrollment_value agent_id)"
    config_label="$(agent_role_config_label)"
    menu_title "Agent 模式"
    printf "  Agent 已接入：%s · %s\n" "${agent_id:-unknown}" "${role_label:-Agent}"
    echo
    menu_item 1 "$config_label"
    menu_item 2 "连接信息"
    menu_item 3 "网络设置"
    menu_item 4 "Agent 服务"
    menu_item 5 "高级操作"
    menu_back
    menu_prompt choice "0-5"
    case "${choice:-}" in
      1) agent_role_config_menu ;;
      2) menu_action connection_info ;;
      3) agent_network_menu ;;
      4) service_control_menu "$AGENT_SERVICE_NAME" "Agent 服务" ;;
      5) agent_advanced_menu ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

service_action() {
  local name="$1" action="$2"
  if command -v systemctl >/dev/null 2>&1; then
    case "$action" in
      start|restart|stop)
        systemctl reset-failed "$name" >/dev/null 2>&1 || true
        ;;
    esac
    systemctl "$action" "$name"
  elif command -v rc-service >/dev/null 2>&1; then
    rc-service "$name" "$action"
  else
    warn "找不到 systemctl/rc-service，请手动执行 $action $name。"
    return 1
  fi
}

service_exists() {
  local name="$1"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl list-unit-files "${name}.service" >/dev/null 2>&1 || systemctl status "$name" >/dev/null 2>&1
  elif command -v rc-service >/dev/null 2>&1; then
    rc-service "$name" status >/dev/null 2>&1
  else
    return 1
  fi
}

service_unit_pattern() {
  local unit="$1"
  if [[ "$unit" == *.* ]]; then
    printf '%s' "$unit"
  else
    printf '%s.service' "$unit"
  fi
}

service_unit_installed() {
  local name="$1" unit="${2:-$1}" pattern
  if command -v systemctl >/dev/null 2>&1; then
    pattern="$(service_unit_pattern "$unit")"
    systemctl list-unit-files "$pattern" --no-legend 2>/dev/null | grep -q .
  elif command -v rc-service >/dev/null 2>&1; then
    rc-service "$name" status >/dev/null 2>&1
  else
    return 1
  fi
}

service_active_label() {
  local name="$1" unit="${2:-$1}" active installed=0
  service_unit_installed "$name" "$unit" && installed=1
  if command -v systemctl >/dev/null 2>&1; then
    active="$(systemctl is-active "$unit" 2>/dev/null || true)"
  elif command -v rc-service >/dev/null 2>&1; then
    active="$(rc-service "$name" status 2>/dev/null | head -n1 || true)"
  else
    active="unknown"
  fi
  case "$active" in
    active|*started*|*running*) printf '运行中' ;;
    failed|*crashed*) printf '失败' ;;
    activating|deactivating) printf '处理中' ;;
    *)
      if [[ "$installed" == "1" ]]; then
        printf '未运行'
      else
        printf '未安装'
      fi
      ;;
  esac
}

service_enabled_label() {
  local name="$1" unit="${2:-$1}" enabled installed=0
  service_unit_installed "$name" "$unit" && installed=1
  if [[ "$installed" != "1" ]]; then
    printf '未安装'
    return
  fi
  if command -v systemctl >/dev/null 2>&1; then
    enabled="$(systemctl is-enabled "$unit" 2>/dev/null || true)"
  elif command -v rc-service >/dev/null 2>&1; then
    enabled="openrc"
  else
    enabled="unknown"
  fi
  case "$enabled" in
    enabled) printf '开机启动' ;;
    disabled) printf '未启用' ;;
    static) printf '静态' ;;
    openrc) printf 'OpenRC' ;;
    *) printf '未知' ;;
  esac
}

menu_color_status() {
  local value="$1"
  case "$value" in
    *运行中|*开机启动) printf '%s%s● %s%s' "$BOLD" "$GREEN" "$value" "$NC" ;;
    *失败|*异常) printf '%s%s✕ %s%s' "$BOLD" "$RED" "$value" "$NC" ;;
    *处理中|*混合部署) printf '%s%s● %s%s' "$BOLD" "$YELLOW" "$value" "$NC" ;;
    *未启用|*未安装|*未运行|*未知) printf '%s○ %s%s' "$DIM" "$value" "$NC" ;;
    *) printf '%s' "$value" ;;
  esac
}

agent_role_label() {
  case "$(agent_enrollment_role)" in
    transit) printf '中转' ;;
    landing) printf '落地' ;;
    hub) printf 'Hub' ;;
    "") printf '' ;;
    *) printf 'Agent' ;;
  esac
}

proxy_config_present() {
  [[ -f "$SINGBOX_CONFIG_PATH" ]] && return 0
  if [[ -d "$CONF_DIR" ]]; then
    find "$CONF_DIR" -maxdepth 1 -type f -name '*relaypilot*.json' -print -quit 2>/dev/null | grep -q .
  else
    return 1
  fi
}

proxy_present() {
  service_unit_installed "$SERVICE_NAME" && return 0
  proxy_config_present
}

append_unique_value() {
  local var_name="$1" candidate="$2" existing
  [[ -n "$candidate" ]] || return 0
  eval "existing=\" \${${var_name}[*]:-} \""
  if [[ "$existing" != *" $candidate "* ]]; then
    eval "${var_name}+=(\"\$candidate\")"
  fi
}

collect_proxy_types_from_file() {
  local file="$1" var_name="$2"
  [[ -f "$file" ]] || return 0
  if grep -Eq '"(protocol|type)"[[:space:]]*:[[:space:]]*"socks"' "$file"; then
    append_unique_value "$var_name" "SOCKS5"
  fi
  if grep -Eq '"(protocol|type)"[[:space:]]*:[[:space:]]*"shadowsocks"' "$file"; then
    append_unique_value "$var_name" "Shadowsocks"
  fi
  if grep -Eq '"(protocol|type)"[[:space:]]*:[[:space:]]*"wireguard"' "$file"; then
    append_unique_value "$var_name" "WireGuard"
  fi
  if grep -Eq '"type"[[:space:]]*:[[:space:]]*"vless"|"reality"' "$file"; then
    append_unique_value "$var_name" "Reality"
  fi
}

proxy_type_summary() {
  local types=() file
  if [[ -d "${STATE_DIR}/endpoints" ]]; then
    while IFS= read -r -d '' file; do
      collect_proxy_types_from_file "$file" types
    done < <(find "${STATE_DIR}/endpoints" -maxdepth 1 -type f -name '*.json' -print0 2>/dev/null || true)
  fi
  collect_proxy_types_from_file "$SINGBOX_CONFIG_PATH" types
  if [[ -d "$CONF_DIR" ]]; then
    while IFS= read -r -d '' file; do
      collect_proxy_types_from_file "$file" types
    done < <(find "$CONF_DIR" -maxdepth 1 -type f -name '*relaypilot*.json' -print0 2>/dev/null || true)
  fi
  if (( ${#types[@]} > 0 )); then
    local IFS='/'
    printf '%s' "${types[*]}"
  fi
}

proxy_status_label() {
  local status summary
  if service_unit_installed "$SERVICE_NAME"; then
    status="$(service_active_label "$SERVICE_NAME")"
  elif proxy_config_present; then
    status="未安装"
  else
    status="未启用"
  fi
  summary="$(proxy_type_summary)"
  if [[ -z "$summary" && "$status" != "未启用" ]]; then
    summary="sing-box"
  fi
  if [[ -n "$summary" && "$status" != "未启用" ]]; then
    printf '%s %s' "$summary" "$status"
  else
    printf '%s' "$status"
  fi
}

machine_mode_label() {
  local hub_present=0 agent_present=0 proxy_present_flag=0 role_label
  service_unit_installed "$HUB_SERVICE_NAME" && hub_present=1
  service_unit_installed "$AGENT_SERVICE_NAME" && agent_present=1
  proxy_present && proxy_present_flag=1
  [[ -f "${STATE_DIR}/agent-enrollment.json" ]] && agent_present=1
  role_label="$(agent_role_label)"
  if [[ "$hub_present" == "1" && "$agent_present" == "1" ]]; then
    if [[ -n "$role_label" ]]; then
      printf 'Hub + Agent / %s' "$role_label"
    else
      printf 'Hub + Agent'
    fi
  elif [[ "$hub_present" == "1" ]]; then
    printf 'Hub'
  elif [[ "$agent_present" == "1" ]]; then
    if [[ -n "$role_label" ]]; then
      printf 'Agent / %s' "$role_label"
    else
      printf 'Agent'
    fi
  elif [[ "$proxy_present_flag" == "1" ]]; then
    printf '本机代理'
  else
    printf '未配置'
  fi
}

menu_status_line() {
  local hub agent proxy
  if service_unit_installed "$HUB_SERVICE_NAME"; then
    hub="$(service_active_label "$HUB_SERVICE_NAME")"
  else
    hub="未启用"
  fi
  agent="$(agent_status_label)"
  proxy="$(proxy_status_label)"
  printf '%sHub：%s%s   %sAgent：%s%s   %s代理：%s%s' \
    "$DIM" "$NC" "$(menu_color_status "$hub")" \
    "$DIM" "$NC" "$(menu_color_status "$agent")" \
    "$DIM" "$NC" "$(menu_color_status "$proxy")"
}

agent_status_label() {
  local service_status
  if agent_enrolled; then
    if service_unit_installed "$AGENT_SERVICE_NAME"; then
      service_status="$(service_active_label "$AGENT_SERVICE_NAME")"
      case "$service_status" in
        运行中) printf '已接入/运行中' ;;
        未运行) printf '已接入/服务未运行' ;;
        未安装) printf '已接入/服务未安装' ;;
        *) printf '已接入/%s' "$service_status" ;;
      esac
    else
      printf '已接入/服务未安装'
    fi
    return
  fi
  if service_unit_installed "$AGENT_SERVICE_NAME"; then
    service_active_label "$AGENT_SERVICE_NAME"
  else
    printf '未启用'
  fi
}

menu_title() {
  local section="$1" mode status meta heading
  mode="$(machine_mode_label)"
  status="$(menu_status_line)"
  heading="${section} v${VERSION} · 当前：${mode}"
  if [[ "$section" == "RelayPilot" ]]; then
    meta=$'安装目录：'"${INSTALL_DIR}"$'\n状态目录：'"${STATE_DIR}"
  else
    meta="状态目录：${STATE_DIR}"
  fi
  menu_header "$heading" "$status" "$meta"
}

restart_relaypilot_services() {
  local svc
  for svc in "$AGENT_SERVICE_NAME" "$HUB_SERVICE_NAME" "$TG_SERVICE_NAME"; do
    if service_exists "$svc"; then
      info "重启服务：$svc"
      service_action "$svc" restart || warn "重启失败：$svc"
    fi
  done
}

service_status_line() {
  local name="$1" unit="${2:-$1}" active enabled
  active="$(service_active_label "$name" "$unit")"
  enabled="$(service_enabled_label "$name" "$unit")"
  printf "  %-28s %s / %s\n" "$unit" "$(menu_color_status "$active")" "$(menu_color_status "$enabled")"
}

service_status_overview() {
  menu_title "本机服务"
  printf "  %-28s %s\n" "服务" "状态 / 启动"
  service_status_line "$AGENT_SERVICE_NAME"
  service_status_line "$HUB_SERVICE_NAME"
  service_status_line "$TG_SERVICE_NAME"
  service_status_line "$HUB_ALERT_TIMER_NAME" "${HUB_ALERT_TIMER_NAME}.timer"
  service_status_line "$SERVICE_NAME"
}

service_control_menu() {
  local name="$1" label="$2"
  while true; do
    menu_title "$label"
    local start_label="启动"
    if [[ "$name" == "$AGENT_SERVICE_NAME" ]] && agent_enrolled && ! service_unit_installed "$AGENT_SERVICE_NAME"; then
      printf "  Agent 已接入 Hub，但后台服务未安装。\n"
      printf "  选择“安装/修复”会创建 %s 并开始轮询 Hub。\n" "$AGENT_SERVICE_NAME"
      echo
      start_label="安装/修复 Agent 服务"
    fi
    menu_item 1 "状态"
    menu_item 2 "$start_label"
    menu_item 3 "重启"
    menu_item 4 "停止"
    menu_item 5 "日志"
    menu_back
    menu_prompt choice "0-5"
    case "${choice:-}" in
      1) menu_action service_action "$name" status ;;
      2)
        if [[ "$name" == "$AGENT_SERVICE_NAME" ]] && agent_enrolled && ! service_unit_installed "$AGENT_SERVICE_NAME"; then
          menu_action install_agent_service --enrollment-file "${STATE_DIR}/agent-enrollment.json"
        else
          menu_action service_action "$name" start
        fi
        ;;
      3) menu_action service_action "$name" restart ;;
      4) menu_action service_action "$name" stop ;;
      5)
        menu_action show_service_logs "$name"
        ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

show_service_logs() {
  local name="$1"
  if command -v journalctl >/dev/null 2>&1; then
    journalctl -u "$name" -n 80 --no-pager
  else
    warn "未找到 journalctl。"
  fi
}

menu_update_and_reload() {
  local rc
  menu_clear
  set +e
  self_update "$@"
  rc=$?
  set -e
  if (( rc != 0 )); then
    warn "更新未完成（退出码 ${rc}）。"
    menu_pause
    return 0
  fi
  if [[ "${RELAYPILOT_UPDATE_NOOP:-0}" == "1" ]]; then
    menu_pause
    return 0
  fi
  echo
  info "已更新，正在重新打开新版面板..."
  sleep 1
  menu_leave_screen
  exec "$BIN_PATH" menu
}

services_menu() {
  require_root
  while true; do
    service_status_overview
    echo
    menu_item 1 "Agent 服务"
    menu_item 2 "Hub 服务"
    menu_item 3 "Telegram 服务"
    menu_item 4 "sing-box 服务"
    menu_item 5 "资源限制"
    menu_item 6 "更新 RelayPilot"
    menu_back
    menu_prompt choice "0-6"
    case "${choice:-}" in
      1) service_control_menu "$AGENT_SERVICE_NAME" "Agent 服务" ;;
      2) service_control_menu "$HUB_SERVICE_NAME" "Hub 服务" ;;
      3) service_control_menu "$TG_SERVICE_NAME" "Telegram 服务" ;;
      4) service_control_menu "$SERVICE_NAME" "sing-box 服务" ;;
      5) menu_action resource_profile ;;
      6) menu_update_and_reload ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_advanced_menu() {
  require_root
  while true; do
    menu_title "Hub 高级操作"
    menu_item 1 "初始化/修改 Hub 配置"
    menu_item 2 "任务队列"
    menu_item 3 "恢复超时任务"
    menu_item 4 "远程退役节点"
    menu_item 5 "移除节点"
    menu_item 6 "重置 Hub"
    menu_back
    menu_prompt choice "0-6"
    case "${choice:-}" in
      1) menu_action hub_quick_setup ;;
      2) menu_action hub_tasks ;;
      3) menu_action hub_recover_tasks ;;
      4) menu_action hub_decommission_agent ;;
      5) menu_action hub_remove_agent ;;
      6) menu_action reset_hub_menu_action ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_telegram_menu() {
  require_root
  while true; do
    menu_title "Telegram"
    menu_item 1 "绑定/修改 Telegram"
    menu_item 2 "发送测试"
    menu_item 3 "修复 Telegram 面板"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action hub_telegram_setup ;;
      2) menu_action tg_send ;;
      3) menu_action hub_telegram_repair ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_alerts_menu() {
  require_root
  while true; do
    menu_title "离线告警"
    menu_item 1 "扫描离线节点"
    menu_item 2 "查看告警"
    menu_item 3 "安装定时扫描"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action hub_alert_offline ;;
      2) menu_action hub_alerts ;;
      3) menu_action install_alert_timer ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_agents_menu() {
  require_root
  while true; do
    menu_title "节点列表"
    menu_item 1 "查看节点"
    menu_item 2 "刷新单个节点详情"
    menu_item 3 "刷新全部节点详情"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action hub_agents ;;
      2) menu_action hub_sync_agent ;;
      3) menu_action hub_sync_all ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_tls_ready() {
  [[ -f "$STATE_DIR/hub-tls/hub.crt" && -f "$STATE_DIR/hub-tls/hub.key" && -f "$STATE_DIR/hub-tls/ca.crt" ]]
}

hub_public_config_ready() {
  local host port
  host="$(hub_public_config_get HUB_PUBLIC_HOST 2>/dev/null || true)"
  port="$(hub_public_config_get HUB_PUBLIC_PORT 2>/dev/null || true)"
  [[ -n "$host" && -n "$port" ]]
}

hub_service_configured_for_state() {
  local service_file="${SYSTEMD_DIR}/${HUB_SERVICE_NAME}.service"
  [[ -f "$service_file" ]] || return 1
  grep -q 'hub-daemon' "$service_file" || return 1
  grep -q -- "--state-dir ${STATE_DIR}" "$service_file"
}

hub_initialized() {
  hub_service_configured_for_state || { hub_tls_ready && hub_public_config_ready; }
}

hub_bootstrap_menu() {
  require_root
  while ! hub_initialized; do
    menu_title "Hub 模式"
    printf "  Hub 尚未初始化。\n"
    echo
    printf "  当前没有检测到可用的 Hub 配置，因此先不展示邀请码、串联、任务等二级操作。\n"
    printf "  初始化会配置 Hub 对外地址、HTTPS/mTLS 证书，并安装 Hub 服务。\n"
    echo
    menu_item 1 "初始化 Hub"
    menu_back
    menu_prompt choice "0-1"
    case "${choice:-}" in
      1) menu_action hub_quick_setup ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_menu() {
  require_root
  if ! hub_initialized; then
    hub_bootstrap_menu
    hub_initialized || return 0
  fi
  while true; do
    menu_title "Hub 模式"
    menu_item 1 "生成邀请码"
    menu_item 2 "串联节点"
    menu_item 3 "Hub 状态"
    menu_item 4 "节点列表"
    menu_item 5 "Telegram"
    menu_item 6 "最近操作"
    menu_item 7 "离线告警"
    menu_item 8 "高级操作"
    menu_back
    menu_prompt choice "0-8"
    case "${choice:-}" in
      1) menu_action hub_enroll_wizard ;;
      2) menu_action hub_link_wizard ;;
      3) menu_action hub_dispatch "/status" ;;
      4) hub_agents_menu ;;
      5) hub_telegram_menu ;;
      6) menu_action hub_results ;;
      7) hub_alerts_menu ;;
      8) hub_advanced_menu ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

telegram_menu() {
  require_root
  while true; do
    menu_title "Telegram"
    menu_item 1 "配置 bot"
    menu_item 2 "配置状态"
    menu_item 3 "注册命令"
    menu_item 4 "删除命令"
    menu_item 5 "发送测试"
    menu_back
    menu_prompt choice "0-5"
    case "${choice:-}" in
      1) menu_action tg_setup ;;
      2) menu_action tg_status ;;
      3) menu_action tg_register_commands ;;
      4) menu_action tg_delete_commands ;;
      5) menu_action tg_send ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

landing_menu() {
  require_root
  while true; do
    menu_title "落地节点"
    menu_item 1 "安装/更新 Shadowsocks"
    menu_item 2 "安装/更新 SOCKS5"
    menu_item 3 "运行状态"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action landing_install_ss ;;
      2) menu_action landing_install_socks ;;
      3) menu_action status ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

transit_menu() {
  require_root
  while true; do
    menu_title "中转节点"
    menu_item 1 "初始化/更新 Reality"
    menu_item 2 "绑定出口"
    menu_item 3 "运行状态"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action transit_init_reality ;;
      2) menu_action transit_import_bind ;;
      3) menu_action status ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

reset_hub_menu_action() {
  title "重置 Hub 配置"
  printf "  将停止并移除：%s, %s, %s\n" "$HUB_SERVICE_NAME" "$TG_SERVICE_NAME" "$HUB_ALERT_TIMER_NAME"
  printf "  将删除 Hub 状态：%s/hub-*\n" "$STATE_DIR"
  printf "  不删除程序目录：%s\n" "$INSTALL_DIR"
  echo
  if confirm "确认重置 Hub 配置" n; then
    reset_hub_state
  else
    info "已取消，未删除任何内容。"
  fi
}

reset_agent_menu_action() {
  title "重置 Agent 和代理配置"
  printf "  将停止并移除：%s, %s\n" "$AGENT_SERVICE_NAME" "$SERVICE_NAME"
  printf "  将删除 Agent 接入状态：%s/agent-*, %s/endpoints, %s/public-entries.json\n" "$STATE_DIR" "$STATE_DIR" "$STATE_DIR"
  printf "  将删除代理片段：%s/*relaypilot*.json\n" "$CONF_DIR"
  printf "  将删除 WireGuard mesh：%s 内 RelayPilot 标记配置\n" "$MESH_CONFIG_DIR"
  printf "  不删除 sing-box 主配置：%s\n" "$SINGBOX_CONFIG_PATH"
  echo
  if confirm "确认重置 Agent/代理配置" n; then
    reset_agent_state
  else
    info "已取消，未删除任何内容。"
  fi
}

reset_agent_control_menu_action() {
  title "退出 Hub 托管"
  printf "  将停止并移除：%s\n" "$AGENT_SERVICE_NAME"
  printf "  将删除 Hub 接入凭证：%s/agent-enrollment.json, agent-token, hub-ca.crt, agent.crt, agent.key\n" "$STATE_DIR"
  printf "  保留代理配置：Reality / Shadowsocks / sing-box / WireGuard\n"
  printf "  保留程序目录：%s\n" "$INSTALL_DIR"
  echo
  if confirm "确认退出 Hub 托管并保留程序/代理配置" n; then
    reset_agent_control_state
    info "已退出 Hub 托管，保留程序和代理配置。"
  else
    info "已取消，未删除任何内容。"
  fi
}

uninstall_from_menu() {
  local mode="$1"
  case "$mode" in
    keep)
      if confirm "卸载 RelayPilot 程序并保留状态/代理配置" n; then
        uninstall_self --keep-state --yes
        info "RelayPilot 已卸载。"
        menu_pause
        menu_leave_screen
        exit 0
      fi
      ;;
    full)
      if confirm "彻底卸载：删除程序、状态和 RelayPilot 代理片段" n; then
        uninstall_self --full --purge-proxy-config --yes
        info "RelayPilot 已完全卸载。"
        menu_pause
        menu_leave_screen
        exit 0
      fi
      ;;
  esac
}

uninstall_relaypilot_menu() {
  require_root
  while true; do
    menu_title "卸载 RelayPilot"
    menu_item 1 "卸载 RelayPilot（保留状态/代理）"
    menu_item 2 "彻底卸载（含状态/代理）"
    menu_back
    menu_prompt choice "0-2"
    case "${choice:-}" in
      1) menu_clear; uninstall_from_menu keep ;;
      2) menu_clear; uninstall_from_menu full ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

main_menu() {
  require_root
  while true; do
    menu_title "RelayPilot 安装"
    menu_item 1 "安装/进入 Hub"
    menu_item 2 "安装/进入 Agent"
    menu_item 3 "卸载 RelayPilot"
    menu_back "退出"
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) hub_install_wizard ;;
      2) agent_install_wizard ;;
      3) uninstall_relaypilot_menu ;;
      0) exit 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_install_wizard() {
  require_root
  if ! hub_initialized; then
    hub_quick_setup
  fi
  hub_initialized && hub_menu
}

agent_install_wizard() {
  require_root
  if agent_enrolled; then
    agent_mode_menu
    return
  fi
  local invite="${AGENT_INVITE:-}"
  title "安装/接入 Agent"
  prompt invite "Hub 邀请码（留空=单机模式）" "$invite"
  if [[ -n "$invite" ]]; then
    agent_join_wizard --invite "$invite"
    agent_enrolled && agent_mode_menu
    return
  fi
  info "已跳过 Hub 接入，进入单机 Agent 面板。"
  agent_mode_menu
}

hub_applet_main() {
  case "${1:-menu}" in
    menu|interactive|"") shift || true; menu_session hub_menu "$@" ;;
    install) shift; hub_install_wizard "$@" ;;
    setup|quick-setup|init) shift; hub_quick_setup "$@" ;;
    invite|enroll) shift; hub_enroll_wizard "$@" ;;
    link) shift; hub_link_wizard "$@" ;;
    agents) shift; hub_agents "$@" ;;
    status) shift; hub_dispatch "/status" ;;
    dispatch) shift; hub_dispatch "${1:-}" ;;
    telegram|tg) shift; menu_session hub_telegram_menu "$@" ;;
    service|services) shift; menu_session service_control_menu "$HUB_SERVICE_NAME" "Hub 服务" ;;
    advanced) shift; menu_session hub_advanced_menu "$@" ;;
    update|self-update|upgrade) shift; self_update "$@" ;;
    uninstall) shift; uninstall_self "$@" ;;
    doctor) doctor ;;
    -h|--help|help) usage ;;
    *) err "未知 Hub 命令：${1:-}"; usage; exit 1 ;;
  esac
}

agent_applet_main() {
  case "${1:-menu}" in
    menu|interactive|"") shift || true; menu_session agent_mode_menu "$@" ;;
    install|setup|init) shift; agent_install_wizard "$@" ;;
    service|services) shift; menu_session service_control_menu "$AGENT_SERVICE_NAME" "Agent 服务" ;;
    update|self-update|upgrade) shift; self_update "$@" ;;
    uninstall) shift; uninstall_self "$@" ;;
    doctor) doctor ;;
    -h|--help|help) usage ;;
    *)
      local old_invoked="$INVOKED_NAME"
      INVOKED_NAME="relaypilot"
      main agent "$@"
      INVOKED_NAME="$old_invoked"
      ;;
  esac
}

main() {
  if [[ "$INVOKED_NAME" == "relaypilot-hub" ]]; then
    hub_applet_main "$@"
    return
  fi
  if [[ "$INVOKED_NAME" == "relaypilot-agent" ]]; then
    agent_applet_main "$@"
    return
  fi
  if [[ "${1:-}" == "bot" ]]; then
    shift
    case "${1:-}" in
      setup|config) shift; tg_setup "$@" ;;
      status) shift; tg_status "$@" ;;
      commands) shift; tg_commands "$@" ;;
      register) shift; tg_register_commands "$@" ;;
      get-commands) shift; tg_get_commands "$@" ;;
      delete-commands) shift; tg_delete_commands "$@" ;;
      dispatch) shift; tg_dispatch "${1:-}" ;;
      send) shift; tg_send "${1:-}" ;;
      daemon) shift; tg_hub_daemon "$@" ;;
      *) err "未知 bot 命令：${1:-}"; usage; exit 1 ;;
    esac
    return
  fi
  if [[ "${1:-}" == "agent" ]]; then
    shift
    case "${1:-}" in
      enroll) shift; agent_enroll "$@" ;;
      join) shift; agent_join_wizard "$@" ;;
      ip-mode|set-ip-mode) shift; agent_ip_mode_wizard "$@" ;;
      remote-decommission|decommission-policy)
        shift
        require_root
        case "${1:-menu}" in
          enable) write_agent_remote_decommission_policy 1; info "已启用远程退役授权。" ;;
          disable) write_agent_remote_decommission_policy 0; info "已关闭远程退役授权。" ;;
          status)
            if agent_remote_decommission_enabled; then info "远程退役授权：已启用"; else info "远程退役授权：未启用"; fi
            ;;
          menu|"") menu_session agent_remote_decommission_policy_menu ;;
          *) err "未知远程退役授权命令：${1:-}"; exit 1 ;;
        esac
        ;;
      connection-info|connect-info|client-info) shift; connection_info "${1:-}" ;;
      public-entry|entry) shift; menu_session public_entry_menu "$@" ;;
      poll-once) shift; agent_poll_once "$@" ;;
      poll|poll-loop) shift; agent_poll_loop "$@" ;;
      install-service) shift; install_agent_service "$@" ;;
      menu|"") menu_session agent_mode_menu ;;
      *) err "未知 agent 命令：${1:-}"; usage; exit 1 ;;
    esac
    return
  fi
  case "${1:-}" in
    menu|interactive) menu_session main_menu ;;
    landing) menu_session landing_menu ;;
    transit) menu_session transit_menu ;;
    hub) menu_session hub_menu ;;
    telegram|tg) menu_session telegram_menu ;;
    landing-install-ss) landing_install_ss ;;
    landing-install-socks) landing_install_socks ;;
    transit-init-reality|ensure-transit-reality) transit_init_reality ;;
    transit-import-bind) transit_import_bind ;;
    hub-agent-export) shift; core_cmd hub-agent-export --state-dir "$STATE_DIR" "$@" ;;
    hub-import-agent) shift; core_cmd hub-import-agent --state-dir "$STATE_DIR" "$@" ;;
    hub-issue-token) shift; hub_issue_token "${1:-}" "${@:2}" ;;
    hub-init-tls) shift; hub_init_tls "$@" ;;
    hub-quick-setup|hub-setup) shift; hub_quick_setup "$@" ;;
    hub-issue-agent-cert) shift; hub_issue_agent_cert "$@" ;;
    hub-provision-agent) shift; hub_provision_agent "$@" ;;
    hub-create-enroll-code|hub-enroll-code) shift; hub_enroll_code "$@" ;;
    hub-enroll|hub-invite) shift; hub_enroll_wizard "$@" ;;
    agent-enroll) shift; agent_enroll "$@" ;;
    agent-ip-mode|agent-set-ip-mode) shift; agent_ip_mode_wizard "$@" ;;
    hub-rotate-token) shift; hub_rotate_token "${1:-}" "${@:2}" ;;
    hub-revoke-token) shift; hub_revoke_token "${1:-}" "${@:2}" ;;
    hub-tokens) shift; hub_tokens "$@" ;;
    hub-daemon) shift; hub_daemon "$@" ;;
    bot-daemon) shift; tg_hub_daemon "$@" ;;
    install-agent-service) shift; install_agent_service "$@" ;;
    install-hub-service) shift; install_hub_service "$@" ;;
    install-bot-service) shift; install_tg_hub_service "$@" ;;
    hub-agents) shift; hub_agents "$@" ;;
    hub-remove-agent) shift; hub_remove_agent "${1:-}" "${@:2}" ;;
    hub-decommission-agent|hub-retire-agent) shift; hub_decommission_agent "${1:-}" ;;
    hub-removed-agents) shift; hub_removed_agents "$@" ;;
    hub-alert-offline) shift; hub_alert_offline "$@" ;;
    hub-alerts) shift; hub_alerts "$@" ;;
    hub-alert-callback) shift; hub_alert_callback "$@" ;;
    hub-recover-tasks) shift; hub_recover_tasks "$@" ;;
    install-alert-timer|install-hub-alert-timer) shift; install_alert_timer "$@" ;;
    hub-link) shift; hub_dispatch "/link $*" ;;
    hub-dispatch) shift; hub_dispatch "${1:-}" ;;
    hub-tasks) shift; hub_tasks "$@" ;;
    hub-results) shift; hub_results "$@" ;;
    hub-sync-agent) shift; hub_sync_agent "${1:-}" ;;
    hub-sync-all) shift; hub_sync_all "$@" ;;
    hub-export-client) shift; hub_export_client "$@" ;;
    hub-export-landing) shift; hub_export_landing "$@" ;;
    agent-poll-once) shift; agent_poll_once "$@" ;;
    agent-poll-loop) shift; agent_poll_loop "$@" ;;
    generate-ss-password) shift; core_cmd generate-ss-password "$@" ;;
    render-landing-ss) shift; core_cmd render-landing-ss "$@" ;;
    render-landing-socks) shift; core_cmd render-landing-socks "$@" ;;
    render-transit-reality|core-ensure-transit-reality) shift; core_cmd ensure-transit-reality --state-dir "$STATE_DIR" "$@" ;;
    validate-endpoint) shift; core_cmd validate-endpoint "$@" ;;
    render-outbound) shift; core_cmd render-outbound "$@" ;;
    migrate-state) shift; migrate_state "$@" ;;
    import-endpoint) shift; core_cmd import-endpoint --state-dir "$STATE_DIR" "$@" ;;
    export-endpoint) shift; core_cmd export-endpoint --state-dir "$STATE_DIR" "$@" ;;
    connection-info|connect-info|client-info) shift; connection_info "${1:-}" ;;
    public-entry-set) shift; public_entry_set "$@" ;;
    public-entry-list) shift; public_entry_list "$@" ;;
    public-entry|entry) shift; menu_session public_entry_menu "$@" ;;
    bind-transit) shift; core_cmd bind-transit --state-dir "$STATE_DIR" "$@" ;;
    list-endpoints) list_endpoints ;;
    show-endpoint) shift; show_endpoint "${1:-}" ;;
    inspect-conf) shift; inspect_conf "${1:-}" ;;
    tg-config) shift; core_cmd tg-config --state-dir "$STATE_DIR" "$@" ;;
    tg-setup|bot-setup) tg_setup ;;
    tg-status|bot-status) tg_status ;;
    tg-commands|bot-commands) shift; tg_commands "$@" ;;
    tg-register-commands|bot-register) shift; tg_register_commands "$@" ;;
    tg-get-commands|bot-get-commands) shift; tg_get_commands "$@" ;;
    tg-delete-commands|bot-delete-commands) shift; tg_delete_commands "$@" ;;
    tg-dispatch|bot-dispatch) shift; tg_dispatch "${1:-}" ;;
    tg-send|bot-send) shift; tg_send "${1:-}" ;;
    resource-profile|service-profile) shift; resource_profile "$@" ;;
    services|service|service-menu) shift; menu_session services_menu "$@" ;;
    install) install_self ;;
    update|self-update|upgrade) shift; self_update "$@" ;;
    reset-hub) reset_hub_state ;;
    leave-hub|reset-agent-control|detach-agent) reset_agent_control_state; info "已退出 Hub 托管，保留程序和代理配置。" ;;
    reset-agent|reset-proxy) reset_agent_state ;;
    uninstall) shift; uninstall_self "$@" ;;
    doctor) doctor ;;
    status) status ;;
    -h|--help|help) usage ;;
    "") menu_session main_menu ;;
    *) err "未知命令：$1"; usage; exit 1 ;;
  esac
}

main "$@"
