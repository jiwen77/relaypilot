# Security

RelayPilot endpoint JSON files and Telegram config contain secrets.

- Endpoint files are written with mode `0600`.
- Telegram config is written with mode `0600`.
- Backups may contain secrets.
- Telegram commands are read-only in the MVP.
- Hub is the only component that should hold the Telegram bot token.
- Agent poll auth uses HMAC timestamp/nonce headers plus optional native HTTPS/mTLS; do not expose Hub over plain HTTP on untrusted networks.
- Agent enrollment invites contain a short-lived single-use code and pinned Hub CA; treat invites as temporary secrets until redeemed or expired.
- A stable public IP is sufficient for Hub TLS when included in `hub-init-tls --host`; a domain is optional for operability, not a security requirement.
- Prefer systemd resource profiles or external cgroup/container limits on small boxes.
- Use `NO_RESTART=1` for dry runs.
