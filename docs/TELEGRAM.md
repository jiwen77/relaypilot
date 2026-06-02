# Telegram Management

For multiple transit/landing agents, use Hub mode: only the Hub receives
Telegram updates and agents do not reply to Telegram directly.

```text
Telegram -> Hub -> selected agents -> one aggregated reply
```

See [HUB.md](HUB.md) for the unified-management flow.

Hub-mode Telegram replies are human-first. `/status` gives a short health
summary, `/topology` shows the Transit -> Landing display tree, and technical
details are kept out of normal push text.

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

For Hub mode, register the Hub command set instead:

```bash
TG_DRY_RUN=1 relaypilot bot register --hub
relaypilot bot register --hub
```

Then install the bounded Hub Telegram daemon:

```bash
relaypilot install-bot-service
```

The daemon long-polls Telegram `getUpdates`, sends command text to
`hub-dispatch`, replies once with the queued targets, then sends one aggregate
result after the selected agents report back. It is designed for small boxes:
systemd units use the same `RELAYPILOT_PROFILE` resource profiles documented in
[HUB.md](HUB.md), Telegram/network errors use retry backoff, and pending result
batches are bounded.

For Hub mode, the useful runtime commands are:

```text
/start
/relaypilot_panel
/relaypilot_status
/relaypilot_topology
/relaypilot_status all
/relaypilot_status transit
/relaypilot_endpoints transit
/relaypilot_update transit v0.1.3
/relaypilot_results [batch_id]
```

`/start` and `/relaypilot_panel` open the Telegram control panel. Its update
center prints update commands as Telegram code blocks, so they can be tapped or
long-pressed and copied before sending. The update buttons do not execute an
OTA operation by themselves.

Short forms like `/status` still work; the registered menu uses `relaypilot_`
so it is easy to distinguish from other projects.

Inspect or delete the remote command menu:

```bash
relaypilot bot get-commands
relaypilot bot delete-commands
```

Registered commands:

```text
/relaypilot_panel
/relaypilot_help
/relaypilot_status
/relaypilot_doctor
/relaypilot_endpoints
/relaypilot_show_endpoint <name>
/relaypilot_inspect_conf [path]
```

Hub update commands:

```text
/relaypilot_update hub v0.1.3
/relaypilot_update transit v0.1.3
/relaypilot_update landing v0.1.3
/relaypilot_update all v0.1.3
/relaypilot_update transit-hk v0.1.3 --restart
```

Use an explicit tag for normal operations. `latest` is accepted, but is best kept
for lab machines or canary nodes. Node updates are queued through Hub tasks and
reported back as an aggregate result.

## Test locally

```bash
relaypilot bot dispatch "/status"
TG_DRY_RUN=1 relaypilot bot send "test"
```
