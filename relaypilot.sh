#!/usr/bin/env bash
set -euo pipefail

VERSION="${RELAYPILOT_VERSION:-0.1.0}"
REPO="${REPO:-jiwen77/relaypilot}"
RAW_REF="${RAW_REF:-main}"
RAW_BASE="${RAW_BASE:-https://github.com/${REPO}/raw/${RAW_REF}}"
RELEASE_BASE="${RELEASE_BASE:-https://github.com/${REPO}/releases/download}"
INSTALL_DIR="${INSTALL_DIR:-/opt/relaypilot}"
BIN_PATH="${BIN_PATH:-/usr/local/bin/relaypilot}"
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
menu_can_control_screen() {
  [[ -t 0 && -t 1 ]] || return 1
  [[ "${TERM:-}" != "" && "${TERM:-}" != "dumb" ]] || return 1
  [[ "${RELAYPILOT_MENU_SCREEN:-1}" != "0" ]] || return 1
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
  read -r -p "选择 [${range}]: " value || true
  printf -v "$var_name" '%s' "$value"
}

usage() {
  cat <<EOF
RelayPilot v${VERSION}

Single entrypoint. Run without arguments to choose a mode.

Usage:
  bash relaypilot.sh
  bash relaypilot.sh menu
  bash relaypilot.sh landing
  bash relaypilot.sh transit
  bash relaypilot.sh hub
  bash relaypilot.sh bot commands
  bash relaypilot.sh landing-install-ss
  bash relaypilot.sh transit-init-reality
  bash relaypilot.sh transit-import-bind
  bash relaypilot.sh hub-agent-export --agent-id hk-transit --role transit
  bash relaypilot.sh hub-import-agent /path/to/agent.json
  bash relaypilot.sh hub-issue-token transit-hk
  bash relaypilot.sh hub-init-tls --host hub.example
  bash relaypilot.sh hub-quick-setup
  bash relaypilot.sh hub-enroll  # interactive invite wizard
  bash relaypilot.sh hub-create-enroll-code --agent-id transit-hk --role transit  # defaults to current public IP
  bash relaypilot.sh hub-create-enroll-code --public-host hub.example --agent-id transit-hk --role transit
  bash relaypilot.sh hub-provision-agent --hub-url https://hub.example:8443 --agent-id transit-hk --role transit
  bash relaypilot.sh hub-tokens
  bash relaypilot.sh hub-revoke-token transit-hk
  bash relaypilot.sh hub-daemon --host 0.0.0.0 --port 8443 --tls-cert /etc/relaypilot/hub-tls/hub.crt --tls-key /etc/relaypilot/hub-tls/hub.key --client-ca /etc/relaypilot/hub-tls/ca.crt --require-client-cert
  bash relaypilot.sh agent enroll --invite 'PASTE_INVITE' --install-service
  bash relaypilot.sh agent enroll --bundle 'PASTE_BUNDLE'
  bash relaypilot.sh agent join
  bash relaypilot.sh agent poll-once --enrollment-file /etc/relaypilot/agent-enrollment.json
  bash relaypilot.sh agent install-service --enrollment-file /etc/relaypilot/agent-enrollment.json
  bash relaypilot.sh install-hub-service --host 0.0.0.0 --port 8443 --tls-cert /etc/relaypilot/hub-tls/hub.crt --tls-key /etc/relaypilot/hub-tls/hub.key --client-ca /etc/relaypilot/hub-tls/ca.crt --require-client-cert
  bash relaypilot.sh install-bot-service
  bash relaypilot.sh install-alert-timer
  bash relaypilot.sh resource-profile
  bash relaypilot.sh services
  bash relaypilot.sh hub-remove-agent transit-hk --reason uninstalled
  bash relaypilot.sh hub-alert-offline --dry-run
  bash relaypilot.sh hub-dispatch "/status all"
  bash relaypilot.sh hub-link transit-hk landing-hk [auth_user] [endpoint_name] [--mode direct|mesh]
  bash relaypilot.sh hub-results
  bash relaypilot.sh bot register
  bash relaypilot.sh install
  bash relaypilot.sh update
  bash relaypilot.sh update --version v0.1.0 --restart-services
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
    read -r -p "$label [$default]: " value || true
    value="${value:-$default}"
  else
    read -r -p "$label: " value || true
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
  local var_name="$1" title_text="$2" default="${3:-}" choice idx raw value label desc
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
    [[ "${values[$idx]}" == "$default" ]] && mark=" *"
    if [[ -n "${descs[$idx]}" ]]; then
      printf "%2d) %s - %s%s\n" "$((idx + 1))" "${labels[$idx]}" "${descs[$idx]}" "$mark"
    else
      printf "%2d) %s%s\n" "$((idx + 1))" "${labels[$idx]}" "$mark"
    fi
  done
  if [[ -n "$default" ]]; then
    read -r -p "选择序号 [$default]: " choice || true
    choice="${choice:-$default}"
  else
    read -r -p "选择序号: " choice || true
  fi
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#values[@]} )); then
    printf -v "$var_name" '%s' "${values[$((choice - 1))]}"
  else
    printf -v "$var_name" '%s' "$choice"
  fi
}

confirm() {
  local label="$1" default="${2:-y}" value suffix
  [[ "$default" =~ ^[Yy]$ ]] && suffix="Y/n" || suffix="y/N"
  read -r -p "$label [$suffix]: " value || true
  value="${value:-$default}"
  [[ "$value" =~ ^[Yy]$ ]]
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

select_agent_role() {
  local var_name="$1" default="${2:-transit}"
  select_option "$var_name" "Agent 角色" "$default" \
    "transit|中转节点|接入用户，转发到落地" \
    "landing|落地节点|提供出口" \
    "hub|Hub|控制面/管理端"
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
    generate-ss-password|migrate-state|tg-config|tg-status|tg-commands|tg-register-commands|tg-get-commands|tg-delete-commands|tg-dispatch|tg-send|render-landing-ss|ensure-transit-reality|validate-endpoint|render-outbound|import-endpoint|export-endpoint|bind-transit|list-endpoints|inspect-conf|hub-agent-export|hub-import-agent|hub-agents|hub-remove-agent|hub-removed-agents|hub-alert-offline|hub-alerts|hub-alert-callback|hub-recover-tasks|hub-issue-token|hub-init-tls|hub-issue-agent-cert|hub-provision-agent|hub-create-enroll-code|hub-enroll-code|agent-enroll|hub-rotate-token|hub-revoke-token|hub-tokens|hub-dispatch|hub-tasks|hub-results|hub-daemon|bot-daemon|agent-poll-once|agent-poll-loop) return 0 ;;
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
      title "Service resource profile"
      echo "Detected: RAM ${detected_mem:-0}MB, CPU ${detected_cpu}"
      echo "1) ${recommended} (recommended): $(profile_description "$recommended")"
      echo "2) tiny: $(profile_description tiny)"
      echo "3) small: $(profile_description small)"
      echo "4) normal: $(profile_description normal)"
      echo "5) custom/env: $(profile_description custom)"
      read -r -p "Choose profile [1]: " answer || true
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
    cat > "$service_path" <<EOF_SERVICE
#!/sbin/openrc-run
name="${description}"
command="/bin/sh"
command_args="-c 'exec ${exec_cmd}'"
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
  mkdir -p "$(dirname "$BIN_PATH")"
  ln -sf "$INSTALL_DIR/relaypilot.sh" "$BIN_PATH"
  info "已安装到：$INSTALL_DIR"
  info "CLI：$BIN_PATH"
}

self_update() {
  require_root
  local version="${UPDATE_VERSION:-latest}" restart_services="${RELAYPILOT_UPDATE_RESTART:-ask}" raw_base="$RAW_BASE" release_base="$RELEASE_BASE"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) version="$2"; shift 2 ;;
      --raw-base) raw_base="$2"; shift 2 ;;
      --release-base) release_base="$2"; shift 2 ;;
      --restart-services) restart_services=yes; shift ;;
      --no-restart-services) restart_services=no; shift ;;
      *) err "未知参数：$1"; return 1 ;;
    esac
  done

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

  mkdir -p "$INSTALL_DIR/bin" "$(dirname "$BIN_PATH")"
  [[ -f "$INSTALL_DIR/relaypilot.sh" ]] && cp -a "$INSTALL_DIR/relaypilot.sh" "$INSTALL_DIR/relaypilot.sh.prev"
  [[ -f "$INSTALL_DIR/bin/relaypilot" ]] && cp -a "$INSTALL_DIR/bin/relaypilot" "$INSTALL_DIR/bin/relaypilot.prev"
  cp -a "$script_tmp" "$INSTALL_DIR/relaypilot.sh"
  cp -a "$asset_tmp" "$INSTALL_DIR/bin/relaypilot"
  ln -sf "$INSTALL_DIR/relaypilot.sh" "$BIN_PATH"
  rm -rf "$tmp"
  unset RELAYPILOT_UPDATE_TMP

  info "已更新 RelayPilot：$BIN_PATH"
  "$INSTALL_DIR/bin/relaypilot" version || true

  if [[ "$restart_services" == "ask" ]]; then
    if confirm "是否重启已安装的 RelayPilot 服务以应用新版本" n; then
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
  remove_service_files "$AGENT_SERVICE_NAME"
  remove_path "$STATE_DIR/agent-enrollment.json"
  remove_path "$STATE_DIR/agent-token"
  remove_path "$STATE_DIR/hub-ca.crt"
  remove_path "$STATE_DIR/agent.crt"
  remove_path "$STATE_DIR/agent.key"
  remove_path "$STATE_DIR/endpoints"
  remove_relaypilot_proxy_fragments
  reload_service_manager
}

uninstall_preview() {
  local purge_state="$1" purge_proxy="$2" dry_run="$3"
  title "卸载预览"
  printf "  程序目录：     %s\n" "$INSTALL_DIR"
  printf "  命令入口：     %s\n" "$BIN_PATH"
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
show_endpoint() { local name="${1:-}"; [[ -z "$name" ]] && prompt name "endpoint 名称" "${ENDPOINT_NAME:-landing}"; core_cmd export-endpoint --state-dir "$STATE_DIR" "$name"; }
inspect_conf() { local conf="${1:-}"; [[ -z "$conf" ]] && { if [[ -d "$CONF_DIR" ]]; then conf="$CONF_DIR"; else conf="$SINGBOX_CONFIG_PATH"; fi; }; core_cmd inspect-conf --conf "$conf"; }
migrate_state() { core_cmd migrate-state "$@"; }

transit_init_reality() {
  require_root
  ensure_singbox || true
  title "中转节点：初始化 Reality 入口"
  local conf listen listen_port inbound_tag server_name handshake_server handshake_port short_id args
  prompt conf "sing-box 配置目录或 config.json" "${TRANSIT_CONF:-$CONF_DIR}"
  prompt listen "监听地址" "${TRANSIT_LISTEN:-::}"
  prompt listen_port "监听端口" "${TRANSIT_PORT:-443}"
  prompt inbound_tag "Reality inbound tag（默认即可）" "${TRANSIT_INBOUND_TAG:-vless-in}"
  prompt server_name "客户端 SNI/server_name（伪装域名，默认即可）" "${TRANSIT_SERVER_NAME:-www.cloudflare.com}"
  prompt handshake_server "Reality 握手目标" "${TRANSIT_HANDSHAKE_SERVER:-$server_name}"
  prompt handshake_port "Reality 握手端口" "${TRANSIT_HANDSHAKE_PORT:-443}"
  prompt short_id "Reality short_id（空则自动生成）" "${TRANSIT_SHORT_ID:-}"
  args=(ensure-transit-reality --conf "$conf" --state-dir "$STATE_DIR" --listen "$listen" --listen-port "$listen_port" --inbound-tag "$inbound_tag" --server-name "$server_name" --handshake-server "$handshake_server" --handshake-port "$handshake_port")
  [[ -n "$short_id" ]] && args+=(--short-id "$short_id")
  core_cmd "${args[@]}"
  service_check "$conf"
  ensure_service_file "$conf"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
}

landing_install_ss() {
  require_root
  ensure_singbox || true
  title "Landing agent: install Shadowsocks endpoint"
  local name server listen listen_port server_port method password inbound_tag endpoint_tag endpoint_file detected_server=""
  prompt name "落地名称/endpoint 名（英文数字短横线，例如 jp-biglobe）" "${LANDING_NAME:-landing}"
  if [[ -z "${LANDING_SERVER:-}" && -t 0 ]]; then
    detected_server="$(detect_public_ip || true)"
  fi
  prompt server "中转机连接此落地的 IP/域名（空则用检测值）" "${LANDING_SERVER:-$detected_server}"
  prompt listen "落地 sing-box 监听地址" "${LANDING_LISTEN:-::}"
  prompt listen_port "落地 sing-box 监听端口" "${LANDING_PORT:-443}"
  prompt server_port "中转机连接端口（NAT 映射时可不同）" "${LANDING_SERVER_PORT:-$listen_port}"
  select_option method "Shadowsocks 加密方式" "${LANDING_METHOD:-2022-blake3-aes-128-gcm}" \
    "2022-blake3-aes-128-gcm|2022 AES-128-GCM|推荐，轻量安全" \
    "2022-blake3-aes-256-gcm|2022 AES-256-GCM|更强，稍重" \
    "chacha20-ietf-poly1305|ChaCha20-Poly1305|兼容老环境"
  password="${LANDING_PASSWORD:-}"
  [[ -z "$password" ]] && password="$(core_cmd generate-ss-password --method "$method")"
  prompt inbound_tag "落地 inbound tag（默认即可）" "${LANDING_INBOUND_TAG:-ss-in}"
  prompt endpoint_tag "导出给中转的 outbound tag（默认即可）" "${LANDING_ENDPOINT_TAG:-landing-${name}-ss}"
  mkdir -p "$STATE_DIR/endpoints"
  endpoint_file="$STATE_DIR/endpoints/${name}.json"
  backup_file_if_exists "$SINGBOX_CONFIG_PATH"
  backup_file_if_exists "$endpoint_file"
  core_cmd render-landing-ss \
    --name "$name" --server "$server" --listen "$listen" \
    --listen-port "$listen_port" --server-port "$server_port" \
    --method "$method" --password "$password" \
    --inbound-tag "$inbound_tag" --endpoint-tag "$endpoint_tag" \
    --config-output "$SINGBOX_CONFIG_PATH" --endpoint-output "$endpoint_file"
  info "落地配置已写入：$SINGBOX_CONFIG_PATH"
  info "endpoint 已写入：$endpoint_file"
  service_check "$SINGBOX_CONFIG_PATH"
  ensure_service_file "$SINGBOX_CONFIG_PATH"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
  echo; title "复制给中转机导入的 endpoint JSON"; cat "$endpoint_file"
}

transit_import_bind() {
  require_root
  title "Transit agent: import endpoint and bind auth_user"
  local endpoint_file conf inbound_tag auth_user client_uuid imported
  prompt endpoint_file "落地 endpoint JSON 文件路径" "${ENDPOINT_FILE:-}"
  [[ ! -f "$endpoint_file" ]] && { err "endpoint 文件不存在：$endpoint_file"; return 1; }
  mkdir -p "$STATE_DIR/endpoints"
  imported="$(core_cmd import-endpoint --state-dir "$STATE_DIR" "$endpoint_file")"
  info "已导入 endpoint：$imported"
  prompt conf "sing-box 配置目录或 config.json" "${TRANSIT_CONF:-$CONF_DIR}"
  prompt inbound_tag "VLESS Reality inbound tag（空则自动选第一个）" "${TRANSIT_INBOUND_TAG:-}"
  prompt auth_user "客户端 auth_user/users.name" "${TRANSIT_AUTH_USER:-}"
  if [[ -z "$auth_user" ]]; then
    auth_user="$(basename "$endpoint_file" .json)"
  fi
  prompt client_uuid "客户端 UUID（空则自动生成）" "${TRANSIT_UUID:-}"
  local args=(bind-transit --conf "$conf" --endpoint "$endpoint_file" --state-dir "$STATE_DIR" --auth-user "$auth_user")
  [[ -n "$inbound_tag" ]] && args+=(--inbound-tag "$inbound_tag")
  [[ -n "$client_uuid" ]] && args+=(--uuid "$client_uuid")
  core_cmd "${args[@]}"
  service_check "$conf"
  if [[ "${NO_RESTART:-}" != "1" ]] && confirm "是否现在重启 $SERVICE_NAME" y; then service_restart "$SERVICE_NAME"; fi
}

tg_setup() {
  require_root
  local token chat_id api_base
  prompt token "Telegram bot token" "${TG_BOT_TOKEN:-}"
  prompt chat_id "Telegram chat id" "${TG_CHAT_ID:-}"
  prompt api_base "Telegram API base" "${TG_API_BASE:-https://api.telegram.org}"
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
  [[ -z "$agent_id" ]] && prompt agent_id "agent id" "${AGENT_ID:-$(hostname 2>/dev/null || echo agent)}"
  role="${2:-}"
  [[ -z "$role" ]] && select_agent_role role "${AGENT_ROLE:-transit}"
  prompt name "display name" "${AGENT_NAME:-$agent_id}"
  prompt endpoint "agent hub endpoint（可留空，推荐未来使用 agent 主动 poll）" "${AGENT_ENDPOINT:-}"
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
  [[ -z "$registration" ]] && prompt registration "agent registration JSON 路径" "${AGENT_REGISTRATION:-}"
  core_cmd hub-import-agent --state-dir "$STATE_DIR" "$registration"
}

hub_issue_token() {
  if [[ "${1:-}" == --* ]]; then
    core_cmd hub-issue-token --state-dir "$STATE_DIR" "$@"
    return
  fi
  local agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "agent id" "${AGENT_ID:-}"
  core_cmd hub-issue-token --state-dir "$STATE_DIR" "${@:2}" "$agent_id"
}

hub_rotate_token() {
  if [[ "${1:-}" == --* ]]; then
    core_cmd hub-rotate-token --state-dir "$STATE_DIR" "$@"
    return
  fi
  local agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "agent id" "${AGENT_ID:-}"
  core_cmd hub-rotate-token --state-dir "$STATE_DIR" "${@:2}" "$agent_id"
}

hub_revoke_token() {
  local agent_id="${1:-}"
  [[ -z "$agent_id" ]] && prompt agent_id "agent id" "${AGENT_ID:-}"
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
hub_remove_agent() { local agent_id="${1:-}"; [[ -z "$agent_id" ]] && prompt agent_id "要移除的 agent id" "${AGENT_ID:-}"; shift || true; core_cmd hub-remove-agent --state-dir "$STATE_DIR" "$@" "$agent_id"; }
hub_removed_agents() { core_cmd hub-removed-agents --state-dir "$STATE_DIR" "$@"; }
hub_alert_offline() { core_cmd hub-alert-offline --state-dir "$STATE_DIR" "$@"; }
hub_alerts() { core_cmd hub-alerts --state-dir "$STATE_DIR" "$@"; }
hub_recover_tasks() { core_cmd hub-recover-tasks --state-dir "$STATE_DIR" "$@"; }
hub_init_tls() { core_cmd hub-init-tls --state-dir "$STATE_DIR" "$@"; }
hub_issue_agent_cert() { core_cmd hub-issue-agent-cert --state-dir "$STATE_DIR" "$@"; }
hub_provision_agent() { core_cmd hub-provision-agent --state-dir "$STATE_DIR" "$@"; }
hub_enroll_code() { core_cmd hub-create-enroll-code --state-dir "$STATE_DIR" "$@"; }

hub_enroll_wizard() {
  require_root
  local agent_id="${AGENT_ID:-}" role="${AGENT_ROLE:-transit}" public_host="${HUB_PUBLIC_HOST:-}" ttl="${HUB_ENROLL_TTL:-10m}" port="${HUB_PORT:-8443}"
  local saved_public_host saved_port
  saved_public_host="$(hub_public_config_get HUB_PUBLIC_HOST 2>/dev/null || true)"
  saved_port="$(hub_public_config_get HUB_PUBLIC_PORT 2>/dev/null || true)"
  [[ -z "$public_host" && -n "$saved_public_host" ]] && public_host="$saved_public_host"
  [[ "${HUB_PORT:-}" == "" && -n "$saved_port" ]] && port="$saved_port"
  title "Create agent invite"
  select_option role "节点角色" "$role" \
    "transit|中转节点|接入用户，转发到落地" \
    "landing|落地节点|提供出口"
  local default_agent_id
  if [[ "$role" == "landing" ]]; then
    default_agent_id="landing-$(hostname 2>/dev/null || echo node)"
  else
    default_agent_id="transit-$(hostname 2>/dev/null || echo node)"
  fi
  [[ -z "$agent_id" ]] && prompt agent_id "Agent id（英文数字短横线，例如 ${default_agent_id}）" "$default_agent_id"
  prompt public_host "Hub 公网 IP/域名（空=自动检测当前公网 IP）" "$public_host"
  local input_port
  input_port="$(port_from_host_input "$public_host")"
  [[ -n "$input_port" ]] && port="$input_port"
  [[ -n "$public_host" ]] && public_host="$(host_only "$public_host")"
  prompt port "Hub HTTPS 端口" "$port"
  if ! valid_port "$port"; then
    err "Hub HTTPS 端口必须是 1-65535：$port"
    return 1
  fi
  prompt ttl "Invite TTL" "$ttl"
  local args=(--agent-id "$agent_id" --role "$role" --ttl "$ttl" --port "$port")
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
  [[ -z "$data" ]] && prompt data "Telegram callback_data" "${CALLBACK_DATA:-}"
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
    warn "没有找到 ${label} 节点，请先生成 invite 并让 Agent 连接 Hub。"
    prompt "$var_name" "${label} agent id" "$default"
    return
  fi

  echo
  title "选择${label}节点"
  for idx in "${!rows[@]}"; do
    IFS=$'\t' read -r id name transport <<< "${rows[$idx]}"
    printf "%2d) %s · %s · %s\n" "$((idx + 1))" "$id" "$name" "$transport"
  done
  if [[ -n "$default" ]]; then
    read -r -p "选择序号，或输入 agent id [$default]: " choice || true
    choice="${choice:-$default}"
  else
    read -r -p "选择序号，或输入 agent id: " choice || true
  fi
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#ids[@]} )); then
    printf -v "$var_name" '%s' "${ids[$((choice - 1))]}"
  else
    printf -v "$var_name" '%s' "$choice"
  fi
}

hub_link_wizard() {
  require_root
  local transit_id="${TRANSIT_AGENT_ID:-}" landing_id="${LANDING_AGENT_ID:-}" auth_user="${TRANSIT_AUTH_USER:-}" endpoint_name="${ENDPOINT_NAME:-}" inbound_tag="${TRANSIT_INBOUND_TAG:-vless-in}" link_mode="${RELAYPILOT_LINK_MODE:-${LINK_MODE:-direct}}" mesh_cidr="${MESH_CIDR:-}" mesh_port="${MESH_PORT:-}" mesh_endpoint="${MESH_ENDPOINT:-}"
  title "串联中转/落地"
  select_hub_agent_by_role transit_id "transit" "中转" "$transit_id"
  select_hub_agent_by_role landing_id "landing" "落地" "$landing_id"
  select_option link_mode "链路模式" "$link_mode" \
    "direct|直连 TCP|兼容 NAT/禁 UDP，默认推荐" \
    "mesh|自动组网 UDP|WireGuard，需 transit 可发 UDP 且 landing 可收 UDP"
  prompt auth_user "客户端 auth_user（空=自动用 endpoint 名）" "$auth_user"
  prompt endpoint_name "落地 endpoint 名（空=自动选第一个）" "$endpoint_name"
  prompt inbound_tag "中转 Reality inbound tag（默认即可）" "$inbound_tag"
  local text="/link ${transit_id} ${landing_id}"
  [[ -n "$link_mode" ]] && text+=" --mode ${link_mode}"
  if [[ "$link_mode" =~ ^(mesh|auto|overlay|wg|wireguard|自动组网|组网)$ ]]; then
    prompt mesh_cidr "WireGuard /30 网段（空=自动生成）" "$mesh_cidr"
    prompt mesh_port "Landing WireGuard UDP 端口（空=自动生成）" "$mesh_port"
    prompt mesh_endpoint "Landing WireGuard 地址:端口（空=沿用落地 endpoint）" "$mesh_endpoint"
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
  local hub_url="${HUB_URL:-}" agent_id="${AGENT_ID:-}" token_file="${AGENT_TOKEN_FILE:-}" role="${AGENT_ROLE:-}" name="${AGENT_NAME:-}" labels="${AGENT_LABELS:-}" conf="${AGENT_CONF:-$CONF_DIR}" ca_cert="${AGENT_CA_CERT:-}" client_cert="${AGENT_CLIENT_CERT:-}" client_key="${AGENT_CLIENT_KEY:-}" tls_server_name="${AGENT_TLS_SERVER_NAME:-}"
  [[ -z "$hub_url" ]] && prompt hub_url "Hub API URL" "http://127.0.0.1:8080"
  [[ -z "$agent_id" ]] && prompt agent_id "agent id" "$(hostname 2>/dev/null || echo agent)"
  [[ -z "$role" ]] && select_agent_role role "transit"
  local args=(--hub-url "$hub_url" --agent-id "$agent_id" --role "$role" --state-dir "$STATE_DIR" --conf "$conf")
  [[ -n "$name" ]] && args+=(--name "$name")
  [[ -n "$labels" ]] && args+=(--labels "$labels")
  [[ -n "$ca_cert" ]] && args+=(--ca-cert "$ca_cert")
  [[ -n "$client_cert" ]] && args+=(--client-cert "$client_cert")
  [[ -n "$client_key" ]] && args+=(--client-key "$client_key")
  [[ -n "$tls_server_name" ]] && args+=(--tls-server-name "$tls_server_name")
  if [[ -n "$token_file" ]]; then
    args+=(--token-file "$token_file")
  elif [[ -z "${AGENT_TOKEN:-}" ]]; then
    prompt token_file "agent token file" "${STATE_DIR}/agent-token"
    args+=(--token-file "$token_file")
  fi
  core_cmd agent-poll-once "${args[@]}"
}

agent_poll_loop() {
  if [[ $# -gt 0 ]]; then
    core_cmd agent-poll-loop --state-dir "$STATE_DIR" "$@"
    return
  fi
  local hub_url="${HUB_URL:-}" agent_id="${AGENT_ID:-}" token_file="${AGENT_TOKEN_FILE:-}" role="${AGENT_ROLE:-}" name="${AGENT_NAME:-}" labels="${AGENT_LABELS:-}" conf="${AGENT_CONF:-$CONF_DIR}" interval="${AGENT_POLL_INTERVAL:-30}" topology_interval="${AGENT_TOPOLOGY_INTERVAL:-300}" ca_cert="${AGENT_CA_CERT:-}" client_cert="${AGENT_CLIENT_CERT:-}" client_key="${AGENT_CLIENT_KEY:-}" tls_server_name="${AGENT_TLS_SERVER_NAME:-}"
  [[ -z "$hub_url" ]] && prompt hub_url "Hub API URL" "http://127.0.0.1:8080"
  [[ -z "$agent_id" ]] && prompt agent_id "agent id" "$(hostname 2>/dev/null || echo agent)"
  [[ -z "$role" ]] && select_agent_role role "transit"
  local args=(--hub-url "$hub_url" --agent-id "$agent_id" --role "$role" --state-dir "$STATE_DIR" --conf "$conf" --interval "$interval" --topology-interval "$topology_interval")
  [[ -n "$name" ]] && args+=(--name "$name")
  [[ -n "$labels" ]] && args+=(--labels "$labels")
  [[ -n "$ca_cert" ]] && args+=(--ca-cert "$ca_cert")
  [[ -n "$client_cert" ]] && args+=(--client-cert "$client_cert")
  [[ -n "$client_key" ]] && args+=(--client-key "$client_key")
  [[ -n "$tls_server_name" ]] && args+=(--tls-server-name "$tls_server_name")
  if [[ -n "$token_file" ]]; then
    args+=(--token-file "$token_file")
  elif [[ -z "${AGENT_TOKEN:-}" ]]; then
    prompt token_file "agent token file" "${STATE_DIR}/agent-token"
    args+=(--token-file "$token_file")
  fi
  core_cmd agent-poll-loop "${args[@]}"
}

install_agent_service() {
  local hub_url="${HUB_URL:-}" agent_id="${AGENT_ID:-}" token_file="${AGENT_TOKEN_FILE:-}" role="${AGENT_ROLE:-}" conf="${AGENT_CONF:-$CONF_DIR}" interval="${AGENT_POLL_INTERVAL:-30}" topology_interval="${AGENT_TOPOLOGY_INTERVAL:-300}" ca_cert="${AGENT_CA_CERT:-}" client_cert="${AGENT_CLIENT_CERT:-}" client_key="${AGENT_CLIENT_KEY:-}" tls_server_name="${AGENT_TLS_SERVER_NAME:-}" enrollment_file="${AGENT_ENROLLMENT_FILE:-}"
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
    [[ -z "$hub_url" ]] && prompt hub_url "Hub API URL" "http://127.0.0.1:8080"
    [[ -z "$agent_id" ]] && prompt agent_id "agent id" "$(hostname 2>/dev/null || echo agent)"
    [[ -z "$role" ]] && select_agent_role role "transit"
    [[ -z "$token_file" ]] && prompt token_file "agent token file" "${STATE_DIR}/agent-token"
  fi
  prepare_service_profile
  local exec_cmd
  if [[ -n "$enrollment_file" ]]; then
    exec_cmd="${BIN_PATH} agent-poll-loop --state-dir ${STATE_DIR} --enrollment-file ${enrollment_file} --conf ${conf} --interval ${interval} --topology-interval ${topology_interval}"
  else
    exec_cmd="${BIN_PATH} agent-poll-loop --state-dir ${STATE_DIR} --hub-url ${hub_url} --agent-id ${agent_id} --role ${role} --token-file ${token_file} --conf ${conf} --interval ${interval} --topology-interval ${topology_interval}"
    [[ -n "$ca_cert" ]] && exec_cmd+=" --ca-cert ${ca_cert}"
    [[ -n "$client_cert" ]] && exec_cmd+=" --client-cert ${client_cert}"
    [[ -n "$client_key" ]] && exec_cmd+=" --client-key ${client_key}"
    [[ -n "$tls_server_name" ]] && exec_cmd+=" --tls-server-name ${tls_server_name}"
  fi
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
  exec_cmd="${BIN_PATH} hub-daemon --state-dir ${STATE_DIR} --host ${host} --port ${port} --quiet"
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
  exec_cmd="${BIN_PATH} bot-daemon --state-dir ${STATE_DIR} --interval ${interval} --timeout ${timeout} --quiet"
  install_managed_service "$TG_SERVICE_NAME" "RelayPilot bot hub daemon" "$exec_cmd" "$TG_SERVICE_MEMORY_MAX" "$TG_SERVICE_CPU_QUOTA"
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
  saved_public_host="$(hub_public_config_get HUB_PUBLIC_HOST 2>/dev/null || true)"
  saved_port="$(hub_public_config_get HUB_PUBLIC_PORT 2>/dev/null || true)"
  saved_listen_host="$(hub_public_config_get HUB_LISTEN_HOST 2>/dev/null || true)"
  [[ -z "$public_host" && -n "$saved_public_host" ]] && public_host="$saved_public_host"
  [[ "${HUB_PORT:-}" == "" && -n "$saved_port" ]] && port="$saved_port"
  [[ "${HUB_LISTEN_HOST:-}" == "" && -n "$saved_listen_host" ]] && listen_host="$saved_listen_host"
  local detected="" tls_args=()
  title "Hub HTTPS/mTLS quick setup"
  if [[ -z "$public_host" ]]; then
    detected="$(detect_public_ip || true)"
  fi
  prompt public_host "Hub public IP/domain used by agents (empty = detected current IP)" "${public_host:-$detected}"
  [[ -z "$public_host" ]] && { err "需要 Hub public IP/domain；也可以稍后运行 hub-init-tls --host <IP_OR_DOMAIN>。"; return 1; }
  local input_port cert_host public_host_for_url
  input_port="$(port_from_host_input "$public_host")"
  [[ -n "$input_port" ]] && port="$input_port"
  cert_host="$(host_only "$public_host")"
  public_host_for_url="$(url_host "$public_host")"
  prompt port "Hub HTTPS port" "$port"
  if ! valid_port "$port"; then
    err "Hub HTTPS port 必须是 1-65535：$port"
    return 1
  fi
  prompt listen_host "Hub listen address" "$listen_host"
  [[ -z "$listen_host" ]] && listen_host="0.0.0.0"

  title "Hub 配置预览"
  printf "  Hub URL：      https://%s:%s\n" "$public_host_for_url" "$port"
  printf "  监听地址：     %s:%s\n" "$listen_host" "$port"
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
  info "下一步：在 Hub 菜单生成 agent invite；agent 端粘贴 invite 即可连接。"
  if confirm "是否现在启动 ${HUB_SERVICE_NAME}" n; then
    if service_action "$HUB_SERVICE_NAME" restart; then
      if command -v systemctl >/dev/null 2>&1 && ! systemctl is-active --quiet "$HUB_SERVICE_NAME"; then
        warn "${HUB_SERVICE_NAME} 未处于 active 状态，请查看：journalctl -u ${HUB_SERVICE_NAME} -n 80 --no-pager"
      fi
    else
      warn "${HUB_SERVICE_NAME} 启动失败，请查看：journalctl -u ${HUB_SERVICE_NAME} -n 80 --no-pager"
    fi
  fi
}

agent_join_wizard() {
  require_root
  local invite role
  title "Agent 模式"
  prompt invite "invite" "${AGENT_INVITE:-}"
  [[ -z "$invite" ]] && { warn "invite 为空。"; return 0; }
  agent_enroll --invite "$invite"
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

show_agent_enrollment() {
  if [[ -f "${STATE_DIR}/agent-enrollment.json" ]]; then
    cat "${STATE_DIR}/agent-enrollment.json"
  else
    warn "Agent 尚未连接 Hub。"
  fi
}

agent_mode_menu() {
  require_root
  while true; do
    menu_title "Agent 模式"
    menu_item 1 "配置中转"
    menu_item 2 "配置落地"
    menu_item 3 "粘贴 invite"
    menu_item 4 "接入信息"
    menu_back
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) transit_menu ;;
      2) landing_menu ;;
      3) menu_action agent_join_wizard ;;
      4)
        menu_action show_agent_enrollment
        ;;
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
    运行中|开机启动) printf '%s%s%s' "$GREEN" "$value" "$NC" ;;
    失败|异常) printf '%s%s%s' "$RED" "$value" "$NC" ;;
    处理中|混合部署) printf '%s%s%s' "$YELLOW" "$value" "$NC" ;;
    未启用|未安装|未运行|未知) printf '%s%s%s' "$DIM" "$value" "$NC" ;;
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

machine_mode_label() {
  local hub_present=0 agent_present=0 role_label
  service_unit_installed "$HUB_SERVICE_NAME" && hub_present=1
  service_unit_installed "$AGENT_SERVICE_NAME" && agent_present=1
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
  else
    printf '未配置'
  fi
}

menu_status_line() {
  local hub agent proxy agent_present=0
  [[ -f "${STATE_DIR}/agent-enrollment.json" ]] && agent_present=1
  service_unit_installed "$AGENT_SERVICE_NAME" && agent_present=1
  if service_unit_installed "$HUB_SERVICE_NAME"; then
    hub="$(service_active_label "$HUB_SERVICE_NAME")"
  else
    hub="未启用"
  fi
  if [[ "$agent_present" == "1" ]]; then
    agent="$(service_active_label "$AGENT_SERVICE_NAME")"
    proxy="$(service_active_label "$SERVICE_NAME")"
  else
    agent="未启用"
    proxy="未启用"
  fi
  printf '%sHub：%s%s   %sAgent：%s%s   %s代理：%s%s' \
    "$DIM" "$NC" "$(menu_color_status "$hub")" \
    "$DIM" "$NC" "$(menu_color_status "$agent")" \
    "$DIM" "$NC" "$(menu_color_status "$proxy")"
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
    menu_item 1 "状态"
    menu_item 2 "启动"
    menu_item 3 "重启"
    menu_item 4 "停止"
    menu_item 5 "日志"
    menu_back
    menu_prompt choice "0-5"
    case "${choice:-}" in
      1) menu_action service_action "$name" status ;;
      2) menu_action service_action "$name" start ;;
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

hub_tasks_menu() {
  require_root
  while true; do
    menu_title "任务"
    menu_item 1 "任务队列"
    menu_item 2 "执行结果"
    menu_item 3 "恢复超时任务"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action hub_tasks ;;
      2) menu_action hub_results ;;
      3) menu_action hub_recover_tasks ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

hub_telegram_menu() {
  require_root
  while true; do
    menu_title "Telegram"
    menu_item 1 "配置 bot"
    menu_item 2 "安装服务"
    menu_item 3 "注册命令"
    menu_item 4 "命令列表"
    menu_back
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) menu_action tg_setup ;;
      2) menu_action install_tg_hub_service ;;
      3) menu_action tg_register_hub_commands ;;
      4) menu_action tg_commands --hub ;;
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

hub_menu() {
  require_root
  while true; do
    menu_title "Hub 模式"
    menu_item 1 "初始化 Hub"
    menu_item 2 "生成 invite"
    menu_item 3 "Hub 状态"
    menu_item 4 "节点列表"
    menu_item 5 "串联节点"
    menu_item 6 "Telegram"
    menu_item 7 "任务"
    menu_item 8 "离线告警"
    menu_item 9 "移除节点"
    menu_back
    menu_prompt choice "0-9"
    case "${choice:-}" in
      1) menu_action hub_quick_setup ;;
      2) menu_action hub_enroll_wizard ;;
      3) menu_action hub_dispatch "/status" ;;
      4) menu_action hub_agents ;;
      5) menu_action hub_link_wizard ;;
      6) hub_telegram_menu ;;
      7) hub_tasks_menu ;;
      8) hub_alerts_menu ;;
      9) menu_action hub_remove_agent ;;
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
    menu_item 2 "状态检查"
    menu_item 3 "Endpoints"
    menu_back
    menu_prompt choice "0-3"
    case "${choice:-}" in
      1) menu_action landing_install_ss ;;
      2) menu_action status ;;
      3) menu_action list_endpoints ;;
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
    menu_item 2 "绑定落地 endpoint"
    menu_item 3 "状态检查"
    menu_item 4 "Endpoints"
    menu_back
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) menu_action transit_init_reality ;;
      2) menu_action transit_import_bind ;;
      3) menu_action status ;;
      4) menu_action list_endpoints ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

reset_hub_menu_action() {
  title "重置 Hub"
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
  title "重置 Agent/代理"
  printf "  将停止并移除：%s, %s\n" "$AGENT_SERVICE_NAME" "$SERVICE_NAME"
  printf "  将删除 Agent 接入状态：%s/agent-*, %s/endpoints\n" "$STATE_DIR" "$STATE_DIR"
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

uninstall_from_menu() {
  local mode="$1"
  case "$mode" in
    keep)
      if confirm "卸载 RelayPilot 程序并保留状态" n; then
        uninstall_self --keep-state --yes
        info "RelayPilot 已卸载。"
        menu_pause
        menu_leave_screen
        exit 0
      fi
      ;;
    full)
      if confirm "完全卸载：删除程序、状态和 RelayPilot 代理片段" n; then
        uninstall_self --full --purge-proxy-config --yes
        info "RelayPilot 已完全卸载。"
        menu_pause
        menu_leave_screen
        exit 0
      fi
      ;;
  esac
}

uninstall_reset_menu() {
  require_root
  while true; do
    menu_title "卸载/重置"
    menu_item 1 "重置 Hub"
    menu_item 2 "重置 Agent/代理"
    menu_item 3 "卸载程序（保留状态）"
    menu_item 4 "完全卸载"
    menu_back
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) menu_action reset_hub_menu_action ;;
      2) menu_action reset_agent_menu_action ;;
      3) menu_clear; uninstall_from_menu keep ;;
      4) menu_clear; uninstall_from_menu full ;;
      0) return 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

main_menu() {
  require_root
  while true; do
    menu_title "RelayPilot"
    menu_item 1 "Hub 模式"
    menu_item 2 "Agent 模式"
    menu_item 3 "本机服务"
    menu_item 4 "卸载/重置"
    menu_back "退出"
    menu_prompt choice "0-4"
    case "${choice:-}" in
      1) hub_menu ;;
      2) agent_mode_menu ;;
      3) services_menu ;;
      4) uninstall_reset_menu ;;
      0) exit 0 ;;
      *) menu_invalid_choice ;;
    esac
  done
}

main() {
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
    agent-poll-once) shift; agent_poll_once "$@" ;;
    agent-poll-loop) shift; agent_poll_loop "$@" ;;
    generate-ss-password) shift; core_cmd generate-ss-password "$@" ;;
    render-landing-ss) shift; core_cmd render-landing-ss "$@" ;;
    render-transit-reality|core-ensure-transit-reality) shift; core_cmd ensure-transit-reality --state-dir "$STATE_DIR" "$@" ;;
    validate-endpoint) shift; core_cmd validate-endpoint "$@" ;;
    render-outbound) shift; core_cmd render-outbound "$@" ;;
    migrate-state) shift; migrate_state "$@" ;;
    import-endpoint) shift; core_cmd import-endpoint --state-dir "$STATE_DIR" "$@" ;;
    export-endpoint) shift; core_cmd export-endpoint --state-dir "$STATE_DIR" "$@" ;;
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
