# Changelog

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
