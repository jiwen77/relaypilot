# Contributing

- Keep one primary user-facing script: `relaypilot.sh`; do not add duplicate old-name entrypoints.
- Keep runtime dependencies tiny: Bash, Go stdlib core, sing-box.
- Do not add SSH/fscarmen wrapper flows to this clean project.
- Endpoint JSON and Telegram config contain secrets; never print secrets except explicit export flows.
- Prefer install-time flags/env over long-running self-tuning logic on NAT boxes.
- Keep service defaults safe for constrained machines; document any higher CPU/RAM need.

Run checks:

```bash
make test
make release-check
```

If Go is not installed:

```bash
GO_BIN="$(scripts/install-local-go.sh)"
PATH="$(dirname "$GO_BIN"):$PATH" make test
```
