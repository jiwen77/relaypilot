# Hub 模式

The safe multi-agent model is hub-and-spoke:

```text
Telegram bot updates
  -> Hub only
  -> task queue
  -> selected transit / landing agents
  -> Hub aggregates one reply
```

Transit and landing agents should not run independent Telegram polling/webhook
loops. In a fleet, only the Hub stores the Telegram bot token.

## Why this avoids duplicate replies

- `/status` without a target is Hub-only.
- Agent fanout is opt-in: `/status all`, `/status transit`, `/status landing`,
  `/status transit-hk`, or `/status label:region=hk`.
- Agents never talk to Telegram directly in Hub mode; they report back to Hub.
- Hub owns deduplication, authorization, task queueing, and result aggregation.

## Register agents

Default production onboarding is agent-initiated. The Hub creates a short-lived,
single-use invite; the agent contacts the Hub over HTTPS, verifies the pinned
Hub CA inside the invite, then receives its long-term token and mTLS client
certificate. No registration JSON or certificate files need to be copied by
hand.

The interactive menu is split by machine role, not by every individual action:

```text
RelayPilot
1) Hub 模式
2) Agent 模式
3) 本机服务
0) 退出
```

So invite onboarding has two non-duplicated steps on two machines:

1. Hub machine: **Hub 模式 → 生成 Agent invite** and choose `transit` or `landing`.
2. Transit/Landing machine: **Agent 模式 → 配置中转/落地节点 → 粘贴 Hub invite 并安装 Agent 服务**.

The local data-plane role and the Hub invite role should match. A transit invite
registers the agent as transit in Hub commands/topology; a landing invite
registers it as landing. The invite does not magically turn a machine into a
transit or landing node; it connects the already-configured machine to the Hub
control plane.

```bash
# Recommended menu path on the Hub machine:
relaypilot
# 1) Hub 模式
#   1) 初始化 Hub 服务
#   2) 生成 Agent invite

# Concise command path:
relaypilot hub-quick-setup
relaypilot hub-enroll

# Non-interactive equivalent:
relaypilot hub-create-enroll-code \
  --agent-id transit-hk \
  --role transit \
  --labels region=hk,provider=example \
  --ttl 10m

# Human-readable output for manual copy/paste:
relaypilot hub-create-enroll-code \
  --agent-id transit-hk \
  --role transit \
  --ttl 10m \
  --text

# Optional domain/IP override:
relaypilot hub-init-tls --host hub.example
relaypilot hub-create-enroll-code \
  --public-host hub.example \
  --agent-id transit-hk \
  --role transit \
  --labels region=hk,provider=example \
  --ttl 10m
```

Invite creation writes the Agent to the Hub registry as `待接入` before the
target machine connects. If the invite expires or the install fails, generate a
new invite for the same `--agent-id`; pending role/name/labels are reused when
omitted, and the secret remains short-lived and single-use.

Paste the printed `install_command` on the agent host, or after RelayPilot is already installed use Agent mode. Enrolling the Agent joins the Hub control plane; the interactive Agent path also offers the local data-plane setup for the invite role:

```text
relaypilot
2) Agent 模式
1) 配置中转节点    # 初始化/更新 Reality 或手动绑定
2) 配置落地节点    # 安装/更新 Shadowsocks
3) 粘贴 Hub invite 并安装 Agent 服务
6) 公网入口        # 前置机房/端口转发时设置
```

Non-interactive equivalent:

```bash
relaypilot agent enroll --invite 'PASTE_INVITE' --install-service
relaypilot transit-init-reality   # transit host, if not done by menu
relaypilot landing-install-ss     # landing host, if not done by menu
relaypilot agent poll-once --enrollment-file /etc/relaypilot/agent-enrollment.json
```

Agent IP mode is chosen on the Agent machine. Use `static` when the node has a
stable IP or domain; it performs no extra public-IP probe. Use `dynamic` when
the provider may change the public IP; the Agent then reports a low-frequency
public-IP probe result in heartbeat data:

```bash
relaypilot agent ip-mode --mode static
relaypilot agent ip-mode --mode dynamic --public-ip-interval 600
```

The Hub also records the source IP it observes from each heartbeat. These fields
are for visibility and future automation only; changing static/dynamic mode does
not rewrite existing Reality, Shadowsocks, or WireGuard configuration.

For a node behind a fronting datacenter/static IP with port forwarding, configure
the Agent public entry instead of relying on IP mode:

```bash
# front.example:443 -> landing local :2443
relaypilot public-entry-set --use shadowsocks --name hk --host front.example --public-port 443 --local-port 2443

# Mesh/WireGuard only when UDP forwarding exists:
relaypilot public-entry-set --use wireguard --name hk --host front.example --public-port 51820 --local-port 50123 --network udp
```

Public entry changes exported service addresses and mesh peer endpoints. IP
mode only reports address metadata to Hub.

### Link transit to landing

After both agents are enrolled and polling, the Hub can link them without manual
endpoint-file copying:

```text
relaypilot
1) Hub 模式
5) 串联中转/落地
```

Equivalent command:

```bash
relaypilot hub-link transit-hk landing-hk hk
# same as:
relaypilot hub-dispatch "/link transit-hk landing-hk hk"
```

`direct` is the default link mode: transit connects to the landing endpoint
server/port exactly as exported by the landing agent. For private transit↔landing
transport, choose mesh mode:

```bash
relaypilot hub-link transit-hk landing-hk hk --mode mesh
# optional deterministic lab values:
relaypilot hub-dispatch "/link transit-hk landing-hk hk --mode mesh --mesh-cidr 10.88.1.0/30 --mesh-port 50123"
```

Mesh mode provisions one dedicated WireGuard interface on each agent, routes
only the peer /32, then rewrites the transit outbound server to the landing
overlay IP. Hub only coordinates keys/tasks; data does not relay through Hub.
Install `wireguard-tools`/`wg-quick` first on both agents.

If a landing Agent has a Shadowsocks public entry, Hub uses that forwarded
host/port for direct mode and preserves the original local address as
`local_server`. If it also has a WireGuard public entry, mesh mode uses that
forwarded UDP host/port as the transit peer endpoint. Without the WireGuard
entry, mesh falls back to the exported landing address and mesh port.

The flow is automatic but still safe and explicit:

1. Hub queues `export_endpoint` to the landing agent.
2. In mesh mode, the landing agent writes/starts its dedicated WireGuard peer before exporting.
3. The landing agent returns the full endpoint over the signed/mTLS control plane.
4. Hub queues `bind_endpoint` to the transit agent, using either the direct server or the mesh overlay IP.
5. The transit agent imports the endpoint, creates/updates the Shadowsocks outbound, adds the VLESS `auth_user`, and writes the route rule. Its Reality inbound should already exist; the Agent join wizard or `relaypilot transit-init-reality` creates it.

The endpoint secret is not printed in Telegram replies. Completed task records
store endpoint summaries without the password; the full endpoint only travels as
the protected task payload needed by the transit agent.

`agent-enroll` writes the token, pinned Hub CA, client certificate/key, and
`agent-enrollment.json` locally. The invite is sensitive until it is used or
expires: treat it like a one-time installation secret and do not post it in chat
logs or public issue trackers.

Compatibility fallback remains available when the Hub cannot accept an
unauthenticated bootstrap request on `/api/enroll`:

```bash
relaypilot hub-provision-agent \
  --hub-url https://hub.example:8443 \
  --agent-id transit-hk \
  --role transit
relaypilot agent enroll --bundle 'PASTE_BUNDLE'
```

### Compatibility: manual registration

Manual registration remains available when you want the agent to export its
current topology before provisioning. On each transit or landing host:

```bash
relaypilot hub-agent-export \
  --agent-id transit-hk \
  --role transit \
  --labels region=hk,provider=example \
  --output ./transit-hk.registration.json
```

Copy the registration file to the Hub host and import it:

```bash
relaypilot hub-import-agent ./transit-hk.registration.json
relaypilot hub-agents
```

Registration files do not contain Shadowsocks passwords, Telegram tokens, or
agent API tokens.

After import, issue a per-agent poll token on the Hub and install that token on
the agent host, or prefer `hub-provision-agent` to also package TLS files:

```bash
# Hub host
relaypilot hub-issue-token transit-hk
relaypilot hub-tokens

# Agent host; paste the printed token
umask 077
printf '%s\n' 'PASTE_PRINTED_TOKEN' > /etc/relaypilot/agent-token
```

The token is never sent over the wire. Each poll request carries only a
timestamp, nonce, agent id, and HMAC-SHA256 signature over method/path/body.
Hub rejects stale timestamps and reused nonces; keep `hub-agent-tokens.json`,
`hub-auth-nonces.json`, and each agent token file mode `0600`.

For production across the public Internet, use RelayPilot's native private
TLS/mTLS control plane. This is the same operational idea as systems that ship
their own node API TLS: no Caddy/Nginx/Let's Encrypt reverse proxy is required,
but the Hub port is still encrypted and mutually authenticated.

Token operations:

```bash
relaypilot hub-tokens
relaypilot hub-rotate-token transit-hk
relaypilot hub-revoke-token transit-hk
```

`hub-issue-token` and `hub-rotate-token` print the token once. Hub stores only
the SHA-256 verifier, and `hub-tokens` never prints plaintext tokens.

After `hub-init-tls`, start the Hub API with native HTTPS and required client
certificates:

```bash
relaypilot hub-daemon \
  --host 0.0.0.0 \
  --port 8443 \
  --tls-cert /etc/relaypilot/hub-tls/hub.crt \
  --tls-key /etc/relaypilot/hub-tls/hub.key \
  --client-ca /etc/relaypilot/hub-tls/ca.crt \
  --require-client-cert
```

Then run one poll cycle, or run the continuous loop from a service manager:

```bash
relaypilot agent enroll --invite 'PASTE_INVITE'

relaypilot agent poll-once \
  --enrollment-file /etc/relaypilot/agent-enrollment.json

relaypilot agent poll \
  --enrollment-file /etc/relaypilot/agent-enrollment.json \
  --interval 30 \
  --topology-interval 300
```

Plain HTTP Hub mode remains available for localhost, lab validation, or traffic
already protected by WireGuard/Tailscale/SSH tunnels:

```bash
relaypilot hub-daemon --host 127.0.0.1 --port 8080
```

Do not expose that plain HTTP port directly to the public Internet.

On RAM/CPU-limited NAT boxes, keep `--interval` at 30s or higher unless you
need faster response. The loop caches topology snapshots for 300s by default,
caps each task batch, limits JSON/HTTP body sizes, and exponentially backs off
when the Hub is unreachable, so outages do not create a busy retry loop.

For production, install bounded service units instead of hand-running loops:

```bash
relaypilot resource-profile

relaypilot agent install-service \
  --enrollment-file /etc/relaypilot/agent-enrollment.json

relaypilot install-hub-service --host 127.0.0.1 --port 8080
relaypilot install-bot-service
```

On systemd, the generated units include `MemoryMax`, `CPUQuota`, `TasksMax`,
`RestartSec`, and start-limit settings. The default `RELAYPILOT_PROFILE=auto`
selects a profile from detected RAM:

| Profile | Suggested host | Agent | Hub | Telegram daemon |
| --- | --- | --- | --- | --- |
| `tiny` | <384MB RAM | 64M / 15% | 96M / 25% | 96M / 20% |
| `small` | 384-1023MB RAM | 96M / 25% | 128M / 50% | 128M / 25% |
| `normal` | 1GB+ RAM | 128M / 50% | 256M / 75% | 192M / 50% |

Set `RELAYPILOT_PROFILE=tiny|small|normal|custom|ask` before installing a
service. Exact environment overrides always win:
`AGENT_SERVICE_MEMORY_MAX`, `AGENT_SERVICE_CPU_QUOTA`,
`HUB_SERVICE_MEMORY_MAX`, `HUB_SERVICE_CPU_QUOTA`,
`TG_SERVICE_MEMORY_MAX`, or `TG_SERVICE_CPU_QUOTA`.
OpenRC units include respawn backoff only; use cgroups/container limits for hard
CPU/RAM caps there.

## Human-facing Telegram view

Telegram replies are optimized for quick human inspection:

- normal `/status` does not include filesystem paths, task IDs, or config tags;
- `/topology` shows the forwarding chain as a Transit -> Landing tree;
- command routing replies show target count and node names only;
- technical detail is reserved for `/doctor` and CLI `--json` output.

The display tree does not change the control-plane architecture. Hub still
talks to transit and landing agents directly; the tree is only a presentation
model for the data-plane relationship.

Example:

```text
🌐 转发拓扑：1 个中转 / 1 个落地
└─ 🟢 🚦 transit-hk · HK Transit
   └─ 🟢 🎯 landing-hk · HK Landing ← user:hk
🟢 在线  🟡 可能掉线  🔴 离线
```

Registration automatically includes a local topology/health snapshot when the
agent can inspect its state:

- transit snapshot: `auth_user -> outbound tag -> endpoint/server`;
- landing snapshot: exported endpoint summaries and local inbound summary;
- health snapshot: `last_seen`, `checked_at`, and status.

Hub builds `/topology` from those snapshots first. Labels are only a fallback.
For manual grouping, the most explicit label form is:

```bash
relaypilot hub-agent-export \
  --agent-id landing-hk \
  --role landing \
  --labels transit=transit-hk,region=hk \
  --output ./landing-hk.registration.json
```

If no snapshot link and no explicit `transit=` label exists, Hub can infer
display grouping from matching `route`, `group`, `region`, or `site` labels.

Liveness is adaptive: every successful agent poll refreshes `last_seen` and the
topology snapshot. Nodes become
🟡 stale after 120 seconds without heartbeat and 🔴 offline after 600 seconds.

## Node deletion / uninstall

There are two cases:

1. **Graceful uninstall**: remove the agent from Hub before or during uninstall.
2. **Unexpected disappearance**: no unregister happens; Hub marks it 🟡/🔴 from
   heartbeat timeout, then an operator can remove it after confirmation.

Hub-side removal:

```bash
relaypilot hub-remove-agent landing-hk --reason uninstalled
relaypilot hub-removed-agents
```

Removal behavior:

- active registry entry is removed, so `/topology` no longer shows it;
- a tombstone is saved in `hub-removed-agents.json` for audit;
- queued/running tasks for that agent are marked `cancelled`;
- cleanup tasks are queued to remaining peers:
  - removing a landing queues `unbind_endpoint` on matching transit nodes;
  - removing a transit queues `teardown_mesh` on matching landing nodes when the
    link used mesh mode;
- cleanup only applies to RelayPilot-managed objects. WireGuard configs without
  the RelayPilot marker are never overwritten or deleted;
- historical task files are not deleted.

Use `--no-tombstone` only for lab cleanup. Use `--keep-tasks` if you do not
want queued tasks cancelled automatically.

If the removed machine is already offline or uninstalled, Hub cannot execute
cleanup on that machine itself; run local uninstall/cleanup there. Hub still
protects the other side from stale routes where it can.

On a node, `relaypilot leave-hub` only removes the RelayPilot Agent
service and Hub enrollment credentials. It keeps the installed data plane
intact: Reality, Shadowsocks, sing-box config fragments, and WireGuard files
are not deleted. Use the menu item `退出 Hub 托管（保留程序/代理）` for the same
operation.

## 24h offline Telegram cleanup prompt

Hub can scan for agents that have been offline for more than 24 hours and send
one human-facing Telegram alert with inline buttons:

```bash
TG_DRY_RUN=1 relaypilot hub-alert-offline
relaypilot hub-alert-offline
relaypilot hub-alerts
```

For unattended Hub hosts, install a systemd timer instead of running a resident
alert daemon:

```bash
relaypilot install-alert-timer
relaypilot install-alert-timer --interval 30min --threshold-seconds 86400
```

Default threshold:

```text
offline alert: 86400 seconds / 24h
snooze:        86400 seconds / 24h
```

Telegram text:

```text
🔴 节点长时间失联
落地：SG Landing (landing-sg)
失联：约 24 小时

请选择处理方式：删除节点，或继续观察 24 小时。
[删除节点] [继续观察]
```

Button behavior:

- **删除节点**: removes the agent from active topology, writes a tombstone, and
  cancels queued/running tasks for that node.
- **继续观察**: keeps the node and snoozes this alert for another 24 hours.

The CLI and the Go bot daemon both use the same callback handler:

```bash
relaypilot hub-alert-callback 'rp:obs:<token>'
relaypilot hub-alert-callback 'rp:rm:<token>'
```

## Register Hub commands with Telegram

Configure Telegram only on the Hub:

```bash
relaypilot bot setup
TG_DRY_RUN=1 relaypilot bot register --hub
relaypilot bot register --hub
```

Hub command set:

```text
/relaypilot_help
/relaypilot_agents
/relaypilot_topology
/relaypilot_status [hub|all|transit|landing|agent_id]
/relaypilot_doctor [hub|all|agent_id]
/relaypilot_endpoints [all|transit|landing|agent_id]
/relaypilot_show_endpoint <agent_id> <endpoint_name>
/relaypilot_inspect_conf <agent_id> [path]
/relaypilot_tasks
/relaypilot_results [batch_id]
/relaypilot_alerts
```

Short aliases like `/status` and `/topology` remain accepted for compatibility,
but the Telegram menu is namespaced with `relaypilot_`.

## Dispatch examples

```bash
relaypilot hub-dispatch "/status"
relaypilot hub-dispatch "/topology"
relaypilot hub-dispatch "/status all"
relaypilot hub-dispatch "/endpoints transit"
relaypilot hub-dispatch "/show_endpoint landing-hk hk"
relaypilot hub-dispatch "/update transit v0.1.1"
relaypilot hub-tasks
relaypilot hub-recover-tasks
relaypilot hub-results
```

Current implementation queues tasks locally on the Hub. Polling agents lease
queued tasks, execute them locally, and post command results back to the task
file:

```text
agent -> Hub: heartbeat
agent -> Hub: fetch queued tasks
agent -> Hub: post command result
Hub -> task result store
```

When a command is received through `relaypilot bot daemon`, the Hub tracks the
batch and sends one aggregate Telegram result after all target agents finish, or
after a bounded timeout. Manual CLI dispatch can inspect the same data with
`relaypilot hub-results [--batch-id ...]`.

If an agent crashes or loses network after leasing a task, Hub task recovery
prevents the task from staying `running` forever. `hub-recover-tasks` requeues
stale running leases first, then marks them failed after repeated timed-out
leases:

```bash
relaypilot hub-recover-tasks
relaypilot hub-recover-tasks --lease-timeout-seconds 120 --max-lease-count 3
```

Agents also trigger the same recovery check before leasing new tasks.

That polling model works behind NAT and does not require inbound ports on
transit or landing machines.
