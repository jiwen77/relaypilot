# RelayPilot

[![Tests](https://github.com/jiwen77/relaypilot/actions/workflows/test.yml/badge.svg)](https://github.com/jiwen77/relaypilot/actions/workflows/test.yml)
[![Release](https://github.com/jiwen77/relaypilot/actions/workflows/release.yml/badge.svg)](https://github.com/jiwen77/relaypilot/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Runtime: Go](https://img.shields.io/badge/runtime-Go%20stdlib-blue)](go.mod)

RelayPilot is a secure, lightweight control plane for Hub-managed
[sing-box](https://sing-box.sagernet.org/) transit and landing relays. It keeps
the normal operator workflow simple: install one CLI, create short-lived Hub
invites, enroll Agents, link transit → landing, and manage status from one
Telegram panel.

```bash
bash <(curl -fsSL https://github.com/jiwen77/relaypilot/raw/refs/heads/main/install-relaypilot.sh)
```

```text
RelayPilot
1) Hub 模式
2) Agent 模式
3) 本机服务
0) 退出
```

## Highlights

- **One operational surface** — Hub, Agent, local services, update, uninstall,
  and diagnostics are available from `relaypilot`.
- **Agent-initiated onboarding** — the Hub generates a short-lived, single-use
  invite; the Agent pastes it and receives its long-term credentials over the
  protected enrollment flow.
- **Secure control plane** — native HTTPS/mTLS protects transport; every Agent
  poll is additionally HMAC-SHA256 signed with timestamp and nonce replay
  checks.
- **Low-resource defaults** — bounded systemd/OpenRC units, small JSON/HTTP
  limits, passive Hub view caches, poll batching, and backoff keep NAT boxes
  responsive.
- **Human-first Telegram panel** — only `/relaypilot` is registered; status,
  topology, update center, node detail, and retirement flows stay behind
  buttons to avoid command clutter.
- **Safe lifecycle operations** — update defaults to restarting RelayPilot
  services, data-plane changes hot reload sing-box first, and remote destructive
  cleanup requires Agent-side opt-in plus confirmation.

## Architecture

RelayPilot separates the control plane from the data plane:

```text
Telegram bot
  -> Hub
  -> registered transit / landing Agents

client
  -> transit VLESS Reality inbound
  -> auth_user route rule
  -> Shadowsocks outbound
  -> landing Shadowsocks inbound
  -> direct egress
```

The Hub never relays user traffic. It stores registry/task state, issues
invites, queues work for Agents, aggregates results, and optionally runs the
Telegram daemon. Transit and landing nodes only poll the Hub and apply local
sing-box/WireGuard changes.

Supported link modes:

- `direct` (default): transit dials the landing endpoint directly.
- `mesh`: Hub provisions a dedicated WireGuard /30 between the transit and
  landing Agents, then binds the transit outbound to the landing overlay IP.

## Quick start

### 1. Install or update the CLI

```bash
bash <(curl -fsSL https://github.com/jiwen77/relaypilot/raw/refs/heads/main/install-relaypilot.sh)
relaypilot
```

The installer downloads the Bash entrypoint and the matching static Go core
binary from GitHub Releases. RelayPilot is Go-only at runtime; Python is not
installed on target hosts.

Automation remains one-line when an invite is already available:

```bash
bash <(curl -fsSL https://github.com/jiwen77/relaypilot/raw/refs/heads/main/install-relaypilot.sh) \
  --enroll 'PASTE_INVITE'
```

### 2. Initialize the Hub

On the Hub machine:

```text
relaypilot
1) Hub 模式
  1) 初始化 Hub 服务
  2) 生成 Agent invite
```

Command equivalents:

```bash
relaypilot hub-quick-setup
relaypilot hub-create-enroll-code \
  --agent-id transit-hk \
  --role transit \
  --ttl 10m \
  --text
```

Use `--public-host hub.example` when the Hub should advertise a domain or a
specific public IP. Creating an invite also creates or updates the Agent as
`待接入` in the Hub registry. If the invite expires, rerun the same `agent-id`;
pending role/name/labels are reused when omitted.

### 3. Enroll transit and landing Agents

On each transit or landing node, paste the invite from the Hub:

```text
relaypilot
2) Agent 模式
  3) 粘贴 Hub invite 并安装 Agent 服务
```

The join wizard reads the invite role and offers the matching local data-plane
setup before installing the Agent poll service:

```bash
relaypilot transit-init-reality   # transit node
relaypilot landing-install-ss     # landing node
```

If a landing service is reached through a forwarded IP/domain, store the real
externally reachable address on the Agent:

```bash
relaypilot public-entry-set --use shadowsocks --name jp \
  --host front.example --public-port 443 --local-port 2443

# Mesh/WireGuard only when UDP is forwarded too.
relaypilot public-entry-set --use wireguard --name jp \
  --host front.example --public-port 51820 --local-port 50123 --network udp
```

Agent IP mode is only visibility metadata for Hub panels. Keep the default
`static` mode for stable IP/domain nodes; use `dynamic` when the provider may
change the public IP:

```bash
relaypilot agent ip-mode --mode dynamic --public-ip-interval 600
```

### 4. Link transit → landing

After both Agents are online:

```bash
relaypilot hub-link transit-hk landing-jp jp
# or:
relaypilot hub-link transit-hk landing-jp jp --mode mesh
```

The Hub asks the landing Agent for its endpoint over the protected control
plane, then queues a bind task to the transit Agent. No endpoint JSON or secret
needs to be copied by hand.

### 5. Bind Telegram on the Hub

Only the Hub should receive Telegram updates. Bind it from the Hub menu:

```text
relaypilot
1) Hub 模式
5) Telegram
1) 绑定/修改 Telegram
```

Binding writes the bot config, installs/updates the Telegram daemon, registers
`/relaypilot`, and restarts the service. After binding, send `/relaypilot` to
open the panel. The registered command menu contains only:

```text
/relaypilot
```

RelayPilot ignores generic Telegram commands such as `/start`, `/status`, and
`/up`, so the same bot/chat can be shared with other services. The panel exposes
four primary entries: `节点列表`, `拓扑`, `最近操作`, and `更新中心`.

## Update and service refresh

Interactive update path:

```text
relaypilot
3) 本机服务
6) 更新 RelayPilot
```

Automation:

```bash
relaypilot update
relaypilot update --version v0.1.12 --restart-services
```

The updater checks the installed Go core version before downloading. If the
target version is already installed, it skips the update by default; use
`--force` when you intentionally want to reinstall the same version. Interactive
update defaults to restarting installed RelayPilot services so Hub/Agent/Bot
daemons use the new version immediately. Use `--no-restart-services` only when
you intentionally want running services to keep the old process until the next
restart.

Telegram update operations are centralized in `更新中心`: choose Hub or Agent,
choose the range/node, then confirm.

## Security model

- Invites are short-lived, single-use installation secrets. Do not paste them in
  public chat logs, issues, or release notes.
- Runtime Agent traffic uses HTTPS/mTLS plus HMAC request signatures. Plain HTTP
  is for localhost, lab, or private-tunnel diagnostics only.
- Agent tokens are not sent as raw HTTP headers; Hub stores verifier material in
  Hub-local token state with restrictive file modes.
- Telegram bot tokens are stored only on the Hub at `/etc/relaypilot` and should
  not exist on transit/landing Agents.
- Endpoint passwords are not printed in Telegram replies; task records keep
  summaries and only protected task payloads carry full endpoint material.
- Human-facing panels mask public IP display to the first half, for example
  `203.0.*.*`. Stored state keeps original values for routing and diagnostics.
- Hub-side GeoIP enrichment is cached and optional. Disable third-party lookups
  with `RELAYPILOT_GEOIP=0`, or point `RELAYPILOT_GEOIP_URL` at your own
  lookup service.

## Resource model

RelayPilot is designed for small VPS/NAT hosts:

- `auto` profile chooses service limits by detected RAM.
- `tiny`: Agent 64M/15% CPU, Hub 96M/25%, Telegram daemon 96M/20%.
- `small`: Agent 96M/25%, Hub 128M/50%, Telegram daemon 128M/25%.
- `normal`: Agent 128M/50%, Hub 256M/75%, Telegram daemon 192M/50%.

Use `RELAYPILOT_PROFILE=tiny|small|normal|custom|ask`, or exact overrides such
as `AGENT_SERVICE_MEMORY_MAX`, `HUB_SERVICE_CPU_QUOTA`, and
`TG_SERVICE_MEMORY_MAX`.

Hub panel data is cached only in process and only passively: agent/topology
views expire after 10 seconds, task views after 5 seconds, and file changes
invalidate the cache immediately. There are no background cache-refresh timers.

## State, migration, and cleanup

RelayPilot stores state in `/etc/relaypilot` by default.

```bash
relaypilot migrate-state --from /path/to/old-state --to /etc/relaypilot --dry-run
relaypilot migrate-state --from /path/to/old-state --to /etc/relaypilot
```

Graceful local exit from Hub management keeps the already deployed data plane:

```bash
relaypilot leave-hub
```

Remote destructive retirement is intentionally gated. A Hub can request
`退出 Hub 托管`, `清理托管代理`, or `彻底卸载`, but the Agent must enable
`allow_remote_decommission=true`, and Hub/TG flows require explicit
confirmation. Use preview/dry-run first when validating a fleet.

## Documentation

- [Quickstart](docs/QUICKSTART.md) — minimal install and first deployment flow.
- [Hub mode](docs/HUB.md) — enrollment, linking, Telegram, task recovery, and
  lifecycle operations.
- [Telegram management](docs/TELEGRAM.md) — Hub panel behavior and command
  registration.
- [Protocol contract](docs/PROTOCOL_CONTRACT.md) — endpoint, registration,
  public-entry, and task-state JSON contracts.
- [Security policy](SECURITY.md) — supported versions and vulnerability reports.

## Development

```bash
make test
make release-check
```

If the host does not ship Go:

```bash
GO_BIN="$(scripts/install-local-go.sh)"
PATH="$(dirname "$GO_BIN"):$PATH" make release-check
```

Release assets are built by `scripts/build-release.sh` and published by the
GitHub Actions release workflow when a `v*` tag is pushed. Before tagging, run a
local sensitive-info check and keep runtime state, tokens, certificates, and
`.omx/` files out of commits.
