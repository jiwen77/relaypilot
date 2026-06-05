# Changelog

## Unreleased

## 0.1.11 - 2026-06-06

- Collapse Hub Telegram setup into one binding flow that configures the bot, installs the daemon, registers `/relaypilot`, and restarts the service.
- Move Telegram service and command internals out of the Hub Telegram main menu and into advanced operations.

## 0.1.10 - 2026-06-05

- Add update version checks so RelayPilot skips same-version updates by default and uses `--force` for intentional reinstall.

## 0.1.9 - 2026-06-05

- Highlight only the default character in interactive yes/no prompts, so `[Y/n]` no longer makes the non-default choice look selected.

## 0.1.8 - 2026-06-05

- Move Telegram Hub update operations behind panel buttons with a final confirmation step; namespaced short commands remain as manual fallback.
- Stop handling generic Telegram message commands such as `/start`, `/status`, and `/up` so other services connected to the same bot/chat can own them.
- Keep the Telegram command menu to `/relaypilot`; advanced operations stay in the panel or manual namespaced commands.
- Simplify Telegram panel copy and button labels, remove utility/external buttons from the main operation panel, and show update targets on confirmation.
- Add clickable Telegram node rows with single-node detail, refresh, doctor, related-node, recent-operation, and confirmed retirement actions.
- Add cached Hub-side GeoIP location enrichment for node lists, shown as fields such as `美国·洛杉矶` or `日本·东京` when a public node IP is available.
- Mask human-facing IP display to the first half, for example `203.0.x.x`.
- Document GeoIP privacy behavior and the `RELAYPILOT_GEOIP=0` opt-out.
- Add mtime-invalidated in-process caches for Hub agent/task reads and topology relation views to avoid rebuilding unchanged Telegram/Hub panels.
- Add passive max-age expiry for Hub view caches: 10s for agent/topology views and 5s for task views.
- Split the Go command implementation into broader domain files while keeping the same package and behavior.

## 0.1.7

- Default the interactive self-update prompt to restarting installed RelayPilot services so Hub/Agent/Bot daemons use the refreshed version immediately.
- Document `--no-restart-services` as the explicit opt-out for deferred service restarts.

## 0.1.6

- Refine the Hub/Agent interactive flow so menus are state-aware and advanced/destructive operations stay out of the primary path.
- Add one-command Hub invites that enroll Agents interactively and offer role-specific local data-plane setup.
- Add Telegram Hub panel improvements focused on status, recent operations, link checks, update commands, and test delivery.
- Add on-demand low-resource link probes and rename manual detail sync to refresh node details.
- Add safe remote decommission tasks with Agent-side opt-in policy and dry-run-by-default behavior.
- Apply sing-box changes with check plus hot reload first, falling back to restart only when needed.
- Hot-read Agent enrollment changes during polling so IP mode and public-IP interval updates take effect without restarting the Agent service.
- Add concise default prompt styling and cleanup of operator-facing wording.

## 0.1.5

- Refuse interactive menus without a TTY so automation probes cannot leave stuck menu processes on remote hosts.

## 0.1.4

- Install managed Hub/Agent/Bot services against the Go core directly so OpenRC restarts do not leave orphaned Bash/core child processes.

## 0.1.3

- Omit the sing-box Shadowsocks outbound `network` field for dual `tcp,udp` endpoints so transit binding remains compatible with sing-box 1.12.

## 0.1.2

- Make self-update replace the Bash entrypoint and Go core atomically so running services do not fail with `Text file busy`.

## 0.1.1

- Add short-lived visible Hub invites with remembered public Hub URL defaults.
- Add Agent static/dynamic public IP reporting mode.
- Add public service entries for forwarded Reality/Shadowsocks/WireGuard endpoints.
- Harden Hub setup and service reset flows for live deployment.
- Improve interactive Hub/Agent menus for role-first deployment.

## 0.1.0

- Start clean single-entry RelayPilot project.
- Add landing Shadowsocks endpoint generation.
- Add transit endpoint import and `auth_user` binding.
- Add Telegram read-only management MVP and Bot API command registration.
- Add Hub registry, targeted Telegram command routing, and task queue foundation.
- Add human-facing Hub Telegram output and Transit -> Landing topology view.
- Add registration-time topology snapshots and heartbeat-based liveness display.
- Add Hub agent removal with tombstone audit and queued-task cancellation.
- Add 24h offline Telegram alert flow with delete/observe inline buttons.
- Add stdlib Hub HTTP API, per-agent HMAC poll tokens, replay nonce checks, and agent task result posting.
- Add initial Go core binary, Go tests, release build script, and one-line installer.
- Migrate landing endpoint rendering, endpoint import/export, outbound rendering, and transit binding to the Go core.
- Migrate Hub agent registration export, agent poll loop, and Hub topology/help/status dispatch reads to the Go core.
- Add low-resource safeguards: topology cache, bounded task batches, JSON/HTTP body limits, Hub server timeouts, and exponential poll retry backoff.
- Add bounded systemd/OpenRC service installers for Hub and agent poll daemons.
- Add Go Hub Telegram long-poll daemon and bounded service installer.
- Add service resource profiles (`auto`, `tiny`, `small`, `normal`, `custom`, `ask`) with exact env override support.
- Add GitHub Release workflow for reproducible static Go core assets.
- Migrate Hub agent removal, removed-agent tombstones, offline-alert scanning, and Telegram alert callbacks to the Go core.
- Add `relaypilot.sh` / `install-relaypilot.sh` as the only user-facing install and CLI entrypoints.
- Add a safe generic state migration helper.
- Add Telegram Hub daemon batch-result aggregation for queued multi-agent commands.
- Add native Hub HTTPS/mTLS enrollment bundles and agent enrollment files.
- Add agent-initiated short-lived invite enrollment for one-command deployment.
- Add kejilion-style interactive management entrypoints while preserving one-line automation.
- Add local self-update command/menu entry for GitHub release updates.
- Add non-invasive tests, smoke checks, and CI.
