# RelayPilot

Lightweight relay and landing node orchestration, starting with sing-box. It works on NAT boxes, VPS, VDS, and dedicated servers.

Single user-facing script:

```bash
bash relaypilot.sh
```

Then choose by machine role:

```text
RelayPilot
1) Hub 模式
2) Agent 模式
3) 本机服务
0) 退出
```

- Hub machine: initialize Hub, generate Agent invites, manage Telegram/tasks.
- Transit/Landing machine: choose Agent mode and paste the matching Hub invite. The join wizard then guides the local data-plane setup for that role.
- Local services: inspect/start/restart bounded system services.

## What it does

Target topology:

```text
client
  -> transit VLESS Reality inbound
  -> route.rules auth_user match
  -> Shadowsocks outbound
  -> landing Shadowsocks inbound
  -> direct egress
```

## Quick start

### Install local CLI

```bash
bash <(curl -fsSL https://github.com/jiwen77/relaypilot/raw/refs/heads/main/install-relaypilot.sh)
```

In an interactive terminal this installs/updates RelayPilot and opens the
management menu automatically. Use `relaypilot` or `relaypilot menu` later to
return to the same panel. Automation remains one-line:

```bash
bash <(curl -fsSL https://github.com/jiwen77/relaypilot/raw/refs/heads/main/install-relaypilot.sh) \
  --enroll 'PASTE_INVITE'
```

The installer downloads the Bash entrypoint plus the matching Go core binary.
Release builds publish only `relaypilot_*` assets. The Go core covers landing/transit writes, agent registration export, Hub task polling,
HMAC-signed Hub API, Hub Telegram long polling, offline-alert button callbacks,
Telegram command helpers, task recovery, and token operations. RelayPilot is
Go-only at runtime; Python is not installed on target hosts.

### State migration

RelayPilot stores state in `/etc/relaypilot` by default. To move state between
hosts or directories:

```bash
relaypilot migrate-state --from /path/to/old-state --to /etc/relaypilot --dry-run
relaypilot migrate-state --from /path/to/old-state --to /etc/relaypilot
```

The migration copies state and preserves file modes; it does not delete the source directory. Use `--force` only if you intentionally want to overwrite conflicts.

Then:

```bash
relaypilot
```

### Update

After publishing RelayPilot on GitHub, each machine can update itself from the
local panel:

```text
RelayPilot -> 本机服务 -> 更新 RelayPilot
```

Automation:

```bash
relaypilot update
relaypilot update --version v0.1.4 --restart-services
```

The update replaces the Bash entrypoint and Go core binary. Running services keep
using the old process until restarted; choose `--restart-services` or restart
Hub/Agent/Bot services from the same panel to apply it immediately.

### Data-plane roles

Agent enrollment connects a node to the Hub control plane. Data-plane setup is the local sing-box config that actually carries traffic. The interactive join flow combines both: paste the invite, RelayPilot reads the invite role, then guides the matching local setup.

Transit role writes/updates a VLESS Reality inbound:

```bash
relaypilot transit-init-reality
```

It generates the Reality private key and short_id when omitted, preserves existing users on updates, runs `sing-box check`, and optionally restarts sing-box.

Landing role writes a Shadowsocks inbound plus the endpoint secret stored under `/etc/relaypilot/endpoints/<name>.json`:

```bash
relaypilot landing-install-ss
```

After both roles are online, link them from the Hub. Hub fetches the landing endpoint over the protected control plane and queues a bind task to the transit, so normal Hub mode no longer requires copying endpoint JSON by hand.

Link modes:

- `direct` (default): transit dials the landing endpoint server/port directly.
- `mesh`: Hub provisions a dedicated WireGuard /30 between transit and landing, then binds the Shadowsocks outbound to the landing overlay IP. It does not route default traffic and does not relay data through Hub; `wg-quick` must already be available on both agents.

If a node is behind a fronting datacenter/static IP with port forwarding, set
the Agent's **public entry**. This is different from IP reporting: IP mode is
only Hub visibility; public entry is the real externally reachable service
address that RelayPilot exports to other nodes.

```bash
# front.example:443 -> landing local :2443
relaypilot public-entry-set --use shadowsocks --name jp --host front.example --public-port 443 --local-port 2443

# Only needed for mesh when WireGuard UDP is also forwarded.
relaypilot public-entry-set --use wireguard --name jp --host front.example --public-port 51820 --local-port 50123 --network udp
```

`landing-install-ss` records the Shadowsocks public entry from its prompts
automatically. Use Agent mode -> 公网入口 when the forwarded address changes or
when you need to add the WireGuard entry for mesh.

Manual import remains available for standalone/lab use:

```bash
relaypilot transit-import-bind
```

The bind step imports endpoint state, creates/updates the Shadowsocks outbound, adds/updates the VLESS user, prepends `route.rules` with `auth_user -> outbound`, validates sing-box, and optionally restarts sing-box.

## Hub 模式

Recommended multi-agent model:

```text
Telegram bot
  -> one Hub
  -> registered transit / landing agents
```

Only the Hub should hold the Telegram bot token and receive Telegram updates.
Transit/Landing agents talk to the Hub as agents and do not reply to Telegram
directly, so one command cannot trigger every machine to answer at once.

Agent onboarding is one flow with two sides: the Hub generates a short-lived
invite with a bound role (`transit` or `landing`), and the Agent pastes that
invite. Pasting the invite joins the Hub control plane; the same wizard then
guides the local data-plane setup for that role: transit initializes Reality,
landing initializes Shadowsocks.

```bash
# On the Hub host, recommended menu path:
relaypilot
# 1) Hub 模式
#   1) 初始化 Hub 服务
#   2) 生成 Agent invite

# Equivalent concise commands:
relaypilot hub-quick-setup
relaypilot hub-enroll

# Non-interactive/automation path. If --hub-url/--public-host is omitted,
# RelayPilot detects the Hub public IP.
relaypilot hub-create-enroll-code \
  --agent-id transit-hk \
  --role transit \
  --ttl 10m

# Add --text for a clean copy-ready install command instead of JSON.
relaypilot hub-create-enroll-code \
  --agent-id transit-hk \
  --role transit \
  --ttl 10m \
  --text

# Optional: use a domain or explicit IP instead of auto-detected public IP.
relaypilot hub-create-enroll-code \
  --public-host hub.example \
  --agent-id transit-hk \
  --role transit \
  --ttl 10m

# Creating an invite also creates/updates the Agent as "待接入" on the Hub.
# If the install fails or the invite expires, rerun the same agent id to get a
# fresh short-lived single-use invite; existing pending role/name/labels are reused.

# On the agent host, paste the invite. The wizard detects the role and
# offers the matching local setup before installing the poll service:
relaypilot
# 2) Agent 模式
#   3) 粘贴 Hub invite 并安装 Agent 服务

# Non-interactive control-plane enrollment remains available; pair it with
# transit-init-reality or landing-install-ss for data-plane automation:
relaypilot agent enroll --invite 'PASTE_INVITE' --install-service
relaypilot transit-init-reality      # transit host
relaypilot landing-install-ss        # landing host

# Agent IP mode defaults to static: no extra public-IP probe.
# If a node's public IP may change, enable low-frequency dynamic reporting:
relaypilot agent ip-mode --mode dynamic --public-ip-interval 600

# If a fronting static IP/domain forwards to the actual landing box,
# declare the external service address on that Agent:
relaypilot public-entry-set --use shadowsocks --name hk --host front.example --public-port 443 --local-port 2443
relaypilot public-entry-set --use wireguard --name hk --host front.example --public-port 51820 --local-port 50123 --network udp

# After both agents are online, link transit -> landing from the Hub.
# Hub asks the landing agent for the full endpoint over the signed control plane,
# then queues a bind task to the transit agent. No endpoint JSON copy is needed.
relaypilot hub-link transit-hk landing-hk hk
# Or build a dedicated transit↔landing WireGuard link first:
relaypilot hub-link transit-hk landing-hk hk --mode mesh
# or from Telegram/Hub dispatch:
relaypilot hub-dispatch "/link transit-hk landing-hk hk"

# Hub command examples
TG_DRY_RUN=1 relaypilot bot register --hub
relaypilot hub-dispatch "/topology"
relaypilot hub-dispatch "/status transit-hk"
relaypilot hub-dispatch "/status all"
relaypilot hub-results
```

When commands come from `relaypilot bot daemon`, the Hub records the batch ID,
waits for all targeted agents to finish, then sends one aggregate Telegram
result message. CLI users can still inspect results manually with
`relaypilot hub-results [--batch-id ...]`.

Agent onboarding is agent-initiated: the invite contains Hub URL + a short-lived
single-use code + the pinned Hub CA, and the agent exchanges it over HTTPS for
its long-term HMAC token plus mTLS client certificate. Runtime poll traffic is
then protected in two layers: native HTTPS/mTLS encrypts and authenticates the
transport, while HMAC-SHA256 signs each request with timestamp + nonce. The
agent token is not sent in HTTP headers. Plain HTTP is
for localhost, lab, or private-tunnel diagnostics only; use the native mTLS
mode above when the Hub API crosses an untrusted network. Firewall the Hub
control-plane port to known agent IPs when possible.
For small NAT boxes, the poll loop reuses topology snapshots by default,
caps each poll batch, limits JSON/HTTP body sizes, and backs off on Hub/network
errors so it does not spin CPU or grow memory during outages.
Agent IP reporting is also lightweight: `static` mode sends only normal
heartbeat metadata, while `dynamic` mode adds a short public-IP HTTPS probe
every 10 minutes by default, with a fallback endpoint only on failure. Hub
records the reported/observed IP for visibility; it does not automatically
rewrite Reality/Shadowsocks/WireGuard configs.

Public entry is the separate reachability setting for forwarded nodes. Example:
if `front.example:443` forwards to a landing machine's local `:2443`, the
landing Agent should store a Shadowsocks public entry; Hub linking will export
`front.example:443` instead of the local address. Mesh mode can also use a
WireGuard public entry when UDP forwarding exists; if UDP is blocked, keep
`direct` mode.

To install or inspect bounded services, use the menu path:

```bash
relaypilot services
```

Equivalent automation remains available:

```bash
relaypilot resource-profile
relaypilot agent install-service \
  --enrollment-file /etc/relaypilot/agent-enrollment.json
relaypilot install-hub-service \
  --host 0.0.0.0 \
  --port 8443 \
  --tls-cert /etc/relaypilot/hub-tls/hub.crt \
  --tls-key /etc/relaypilot/hub-tls/hub.key \
  --client-ca /etc/relaypilot/hub-tls/ca.crt \
  --require-client-cert
```

systemd services use a resource profile selected at install time:

- `auto` (default): choose by detected RAM.
- `tiny`: agent 64M/15% CPU, Hub 96M/25%, Telegram daemon 96M/20%.
- `small`: agent 96M/25%, Hub 128M/50%, Telegram daemon 128M/25%.
- `normal`: agent 128M/50%, Hub 256M/75%, Telegram daemon 192M/50%.

Use `RELAYPILOT_PROFILE` with `tiny|small|normal|custom|ask`, or keep exact overrides:
`AGENT_SERVICE_MEMORY_MAX`, `AGENT_SERVICE_CPU_QUOTA`,
`HUB_SERVICE_MEMORY_MAX`, `HUB_SERVICE_CPU_QUOTA`, `TG_SERVICE_MEMORY_MAX`,
or `TG_SERVICE_CPU_QUOTA`. OpenRC gets restart backoff; hard CPU/RAM caps should
come from cgroups or the container/VM layer.

Default `/status` is Hub-only; fanout requires an explicit selector such as
`all`, `transit`, `landing`, `role:transit`, `label:region=hk`, or an `agent_id`.
Telegram text is human-first: normal replies hide paths/task IDs and show a
Transit -> Landing tree so failures are easy to locate visually. Technical
details belong in `/doctor` or `--json` CLI output.
The tree is built from agent registration snapshots first, then labels as a
fallback. Liveness icons are based on agent `last_seen`.
If a node is uninstalled gracefully, remove it from Hub with
`relaypilot hub-remove-agent <agent_id> --reason uninstalled`; if it
disappears unexpectedly, Hub will mark it stale/offline by heartbeat timeout
until you remove it.
If you only want a node to leave Hub management but keep the already deployed
Reality/Shadowsocks/sing-box/WireGuard data plane, use the menu item
`退出 Hub 托管（保留程序/代理）` or run `relaypilot leave-hub`.
Hub removal also queues safe cleanup on remaining peers: removing a landing
unbinds matching transit routes/users/outbounds; removing a transit tears down
matching landing mesh interfaces. The removed machine itself can only be
cleaned remotely while it is still enrolled and polling; if it is already
offline, clean its local sing-box/WireGuard files during uninstall.
If a poll token is exposed or no longer needed, rotate or revoke it from the
Hub without deleting the agent:

```bash
relaypilot hub-rotate-token transit-hk
relaypilot hub-revoke-token transit-hk
```
Hub can also send a 24h offline Telegram alert with buttons:
`relaypilot hub-alert-offline` sends `[删除节点] [继续观察]`.
Install a low-overhead systemd timer if you want this scan to run
periodically without keeping another daemon resident:

```bash
relaypilot install-alert-timer
```

To let Telegram messages drive Hub commands automatically, install the bounded
Telegram Hub daemon after `bot setup` and `bot register --hub`:

```bash
relaypilot install-bot-service
```

It long-polls Telegram `getUpdates`, runs `hub-dispatch`, sends one Hub reply,
and inherits service CPU/RAM limits.

Standalone/single-machine Telegram commands are still available for debugging:

```bash
relaypilot bot setup
relaypilot bot commands
relaypilot bot register
relaypilot bot dispatch "/status"
TG_DRY_RUN=1 relaypilot bot send "relaypilot test"
```

On the Hub bot, send `/start` or `/relaypilot_panel` to open the Telegram
control panel. The update center shows copyable Telegram code blocks such as
`/relaypilot_update transit-hk v0.1.4 --restart`; buttons do not trigger
high-risk update actions directly.

Registered read-only commands:

```text
/relaypilot_panel
/relaypilot_help
/relaypilot_status
/relaypilot_doctor
/relaypilot_endpoints
/relaypilot_show_endpoint <name>
/relaypilot_inspect_conf [path]
```

Short commands such as `/status` still work, but the registered Telegram
menu uses the `relaypilot_` prefix to avoid collisions with other bots/projects.

`bot register` calls Telegram Bot API `setMyCommands`. Use
`TG_DRY_RUN=1` first if you only want to inspect the API payload.

## Development

```bash
make test
go test ./...
scripts/build-release.sh
```

If your NAT box or CI image does not ship Go, install a local toolchain for
development only:

```bash
GO_BIN="$(scripts/install-local-go.sh)"
PATH="$(dirname "$GO_BIN"):$PATH" make test
```

Tests are non-invasive: temp directories and a stub `sing-box` are used.
