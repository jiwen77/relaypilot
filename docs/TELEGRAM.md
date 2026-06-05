# Telegram Management

For multiple transit/landing agents, use Hub mode: only the Hub receives
Telegram updates and agents do not reply to Telegram directly.

```text
Telegram -> Hub -> selected agents -> one aggregated reply
```

See [HUB.md](HUB.md) for the unified-management flow.

Hub-mode Telegram replies are human-first. RelayPilot only responds to
`/relaypilot` and `/relaypilot_*` commands in Telegram messages, so generic commands such as
`/start` or `/status` can remain owned by other services connected to the same
bot/chat. The panel gives the normal status and operation view; manual
commands such as `/relaypilot_status` and `/relaypilot_topology` remain
available for operators.

Hub can also push a 24h offline cleanup prompt with inline buttons:

```text
[删除节点] [继续观察]
```

`删除节点` removes the stale agent from active topology and keeps a tombstone;
`继续观察` keeps it and snoozes the alert for another 24 hours.

Standalone Telegram support is still available for single-machine debugging:

- stores bot config at `/etc/relaypilot/telegram.json` with mode `0600`;
- registers read-only commands;
- can push those commands to Telegram Bot API with `setMyCommands`;
- can inspect/delete remote Bot API commands with `getMyCommands` and
  `deleteMyCommands`;
- dispatches command text locally;
- can send messages, with dry-run support;
- starts Hub-mode Telegram long polling only when you explicitly install or run
  `bot daemon`.

## Configure

```bash
relaypilot bot setup
```

## Commands

```bash
relaypilot bot commands
```

Register them in Telegram's command menu:

```bash
TG_DRY_RUN=1 relaypilot bot register
relaypilot bot register
```

For Hub mode, bind Telegram from the Hub menu instead:

```text
relaypilot
1) Hub 模式
5) Telegram
1) 绑定/修改 Telegram
```

Binding writes the bot config, installs/updates the bounded Hub Telegram daemon,
registers `/relaypilot`, and restarts the service. Use the local menu to verify
delivery:

```text
RelayPilot -> Hub 模式 -> Telegram -> 发送测试
```

The daemon long-polls Telegram `getUpdates`, sends command text to
`hub-dispatch`, replies once with the queued targets, then sends one aggregate
result after the selected agents report back. It is designed for small boxes:
systemd units use the same `RELAYPILOT_PROFILE` resource profiles documented in
[HUB.md](HUB.md), Telegram/network errors use retry backoff, and pending result
batches are bounded.

For Hub mode, the normal Telegram entry point is:

```text
/relaypilot
```

`/relaypilot` opens the Telegram control panel. It shows Hub/node/task
status and exposes four primary buttons: `节点列表`, `拓扑`, `最近操作`, and
`更新中心`. `刷新节点详情` lives in `节点列表`; `链路检测` lives in `拓扑`. The node
list is clickable: each node opens a detail page with refresh, doctor,
related nodes, recent operations, and confirmed retirement actions. Per-node
updates stay in `更新中心` so update flow remains centralized. When a
public node IP is available, Hub caches a low-frequency GeoIP lookup and shows
it as `位置 美国·洛杉矶` or `位置 日本·东京` in the node list. Human-facing panels
mask IP display to the first half, for example `203.0.x.x`. GeoIP uses the
configured third-party lookup endpoint and can be disabled with
`RELAYPILOT_GEOIP=0`. The update center uses a button wizard: choose Hub or
Agent, choose a range/node, then confirm.
Unprefixed Telegram
messages are ignored by RelayPilot to avoid cross-service command conflicts.

Inspect or delete the remote command menu:

```bash
relaypilot bot get-commands
relaypilot bot delete-commands
```

Registered commands shown in Telegram's command menu stay panel-only:

```text
/relaypilot
```

Advanced commands, including `/relaypilot_panel` and `/relaypilot_status`, remain supported manually and
through panel buttons where applicable, but are not registered into Telegram's
global command menu. Manual update shortcuts:

```text
/relaypilot_uphub
/relaypilot_up transit-hk
/relaypilot_up transit
/relaypilot_up landing
/relaypilot_upall
```

Short update commands default to `latest` and restart the target service. You
can still pin a version or defer restart:

```text
/relaypilot_upall v0.1.11
/relaypilot_upall v0.1.11 --no-restart
```

Use an explicit tag for normal production operations. `latest` is convenient for
lab machines or canary nodes. Node updates are queued through Hub tasks and
reported back as an aggregate result.

## Test locally

```bash
relaypilot bot dispatch "/relaypilot_status"
TG_DRY_RUN=1 relaypilot bot send "test"
```
