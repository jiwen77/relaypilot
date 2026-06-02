# Protocol Contract

Landing endpoints are JSON files exported by landing hosts and imported by
transit hosts.

## Shadowsocks endpoint v1

```json
{
  "kind": "relaypilot/landing-endpoint",
  "version": 1,
  "name": "hk",
  "protocol": "shadowsocks",
  "server": "203.0.113.10",
  "server_port": 443,
  "method": "2022-blake3-aes-128-gcm",
  "password": "BASE64_KEY",
  "network": "tcp,udp",
  "tag": "landing-hk-ss"
}
```

Transit renders it into a sing-box Shadowsocks outbound and binds a VLESS
`users.name` to that outbound through `route.rules[].auth_user`.

## Agent registration v1

Agents register with the Hub through a non-secret JSON document:

```json
{
  "kind": "relaypilot/agent-registration",
  "version": 1,
  "id": "transit-hk",
  "role": "transit",
  "name": "HK Transit",
  "transport": "poll",
  "labels": {
    "region": "hk"
  },
  "capabilities": ["status", "doctor", "endpoints", "inspect_conf"],
  "health": {
    "status": "ok",
    "checked_at": 1780315200,
    "errors": []
  },
  "last_seen": 1780315200,
  "topology": {
    "captured_at": 1780315200,
    "links": [
      {
        "auth_user": "hk",
        "outbound_tag": "landing-hk-ss",
        "endpoint_name": "hk",
        "link_mode": "direct",
        "server": "203.0.113.10",
        "server_port": 443
      }
    ],
    "endpoints": [],
    "inbounds": []
  },
  "updated_at": 1780315200
}
```

The registration file must not contain Shadowsocks passwords, Telegram tokens,
or per-agent shared secrets. Runtime authentication uses a Hub-local token store
(`hub-agent-tokens.json`) populated by `hub-issue-token`; the token itself is
printed once for installation on the agent host and a SHA-256-derived verifier
is kept on the Hub with file mode `0600`.

Hub topology rendering uses `topology.links` first, then falls back to labels
such as `transit=...`, `group=...`, or `region=...`. `last_seen` drives the
human-facing liveness icons in Telegram.

`link_mode` is `direct` when transit dials the landing endpoint server/port
directly. In `mesh` mode Hub provisions a dedicated WireGuard transit↔landing
/30 and the transit endpoint `server` becomes the landing overlay IP; the
original landing address may be retained in endpoint state as `direct_server`.

## Removed agent tombstone v1

When a node is intentionally removed from Hub, the active registry entry is
deleted and a tombstone is stored:

```json
{
  "kind": "relaypilot/hub-removed-agents",
  "version": 1,
  "agents": {
    "landing-hk": {
      "id": "landing-hk",
      "removed_at": 1780315800,
      "reason": "uninstalled",
      "agent": {
        "id": "landing-hk",
        "role": "landing",
        "name": "HK Landing"
      },
      "cancelled_tasks": 1
    }
  }
}
```

Tombstones are for audit and display; they are not part of active topology.

## Hub task lease recovery v1

Hub task files move through this lifecycle:

```text
queued -> running -> done|failed|cancelled
```

When an agent leases a task, Hub records `leased_at` and increments
`lease_count`. If a `running` task exceeds the lease timeout, recovery either:

- moves it back to `queued` with `requeue_reason: "lease_timeout"` when
  `lease_count` is below the retry cap; or
- marks it `failed` with a failure `result` after repeated timed-out leases.

The default recovery settings are:

```text
lease timeout: 120 seconds
max leases:    3
```

Operators can run recovery explicitly with `hub-recover-tasks`; polling agents
also run recovery before they lease more work.

## Offline alert state v1

Hub stores offline alert state separately from active topology:

```json
{
  "kind": "relaypilot/hub-alerts",
  "version": 1,
  "alerts": {
    "landing-sg": {
      "agent_id": "landing-sg",
      "status": "alerted",
      "offline_age_seconds": 86410,
      "threshold_seconds": 86400,
      "alerted_at": 1780315900,
      "next_alert_at": 1780402300,
      "remove_token": "short-token",
      "observe_token": "short-token"
    }
  }
}
```

The tokens are short Telegram `callback_data` handles. Button actions are:

- `rp:rm:<token>`: remove agent, tombstone it, cancel queued/running tasks;
- `rp:obs:<token>`: keep agent and snooze the alert window.

## Hub polling API v1

The API paths below are HTTP semantics. For production across an untrusted
network, RelayPilot runs them over native HTTPS/mTLS:

- Hub server cert is signed by a RelayPilot private CA (`hub-init-tls`).
- Agents pin that CA with `--ca-cert`.
- Hub can require agent client certificates with `--client-ca` and
  `--require-client-cert`.
- HMAC token signing remains required even when mTLS is enabled.

Operator onboarding can avoid manual file shuffling with agent-initiated
enrollment:

1. `hub-create-enroll-code` stores a SHA-256 verifier for a short-lived,
   single-use code and prints an invite containing Hub URL, code id, code, agent
   metadata, expiry, and the Hub CA PEM. The Hub URL can be explicit
   (`--hub-url`), built from `--public-host`, or defaulted from detected public
   IP plus port 8443. Invite creation also writes/updates the bound Agent in
   the Hub registry as pending (`待接入`) without setting `last_seen`; a fresh
   invite can be regenerated for the same Agent ID without reusing any old
   plaintext secret.
2. `agent-enroll --invite ...` verifies the Hub certificate with that pinned CA
   and posts to unauthenticated bootstrap endpoint `POST /api/enroll`.
3. The Hub consumes the code, writes/updates the registry entry, stores only the
   long-term token verifier, signs an agent client certificate, and returns one
   sensitive enrollment payload.
4. The agent writes:

```text
agent-enrollment.json
agent-token
hub-ca.crt
agent.crt
agent.key
```

Runtime pollers then use `--enrollment-file` instead of passing individual token
and certificate paths. The manual `hub-provision-agent` bundle flow remains
available for environments that cannot expose `/api/enroll`.


### Bootstrap endpoint

`POST /api/enroll` is intentionally unauthenticated by client certificate so a
new agent can bootstrap on the same HTTPS port used by runtime polling. Security
comes from the pinned Hub CA plus the high-entropy, single-use, short-lived code.
When `hub-daemon --require-client-cert` is enabled, RelayPilot verifies any
presented client certificate at the TLS layer and enforces client-certificate
presence in application auth for runtime poll endpoints. `/api/enroll` and
`/healthz` remain reachable without a client certificate.

Agents do not send the token itself. Each authenticated request carries:

```text
X-Agent-Id: <agent_id>
X-Agent-Timestamp: <unix_seconds>
X-Agent-Nonce: <random_nonce>
X-Agent-Signature: <hex_hmac_sha256>
```

The signature key is `sha256(agent-token)`. The signed message is:

```text
METHOD
PATH_WITH_QUERY
SHA256_HEX(BODY)
X-Agent-Timestamp
X-Agent-Nonce
X-Agent-Id
```

Hub rejects signatures with stale timestamps or reused nonces.

```text
POST /api/agents/register
POST /api/agents/{id}/heartbeat
GET  /api/agents/{id}/tasks?limit=5
POST /api/tasks/{task_id}/result
GET  /healthz
```

`register` accepts an agent registration document and refreshes `last_seen`.
`heartbeat` accepts optional `topology` and `health` objects. Fetching tasks
leases queued tasks by marking them `running`; posting a result marks each task
`done` or `failed` and stores the command output/error under `result`.

Hub token store entries are verifier-only:

```json
{
  "kind": "relaypilot/hub-agent-tokens",
  "version": 1,
  "agents": {
    "transit-hk": {
      "agent_id": "transit-hk",
      "token_sha256": "hex-sha256",
      "issued_at": 1780316000
    }
  }
}
```

`hub-tokens` must not print plaintext tokens. `hub-revoke-token` removes the
entry, causing subsequent signed poll requests for that agent to fail.

## Telegram pending batch state v1

The Hub bot daemon keeps only a small local index of Telegram-originated batches
that still need an aggregate result reply:

```json
{
  "kind": "relaypilot/telegram-pending-batches",
  "version": 1,
  "batches": {
    "1780316000-abcd": {
      "batch_id": "1780316000-abcd",
      "origin_text": "/status all",
      "chat_id": 123456,
      "created_at": 1780316000,
      "timeout_seconds": 120
    }
  }
}
```

The index is bounded and references Hub task files by `batch_id`; command
outputs remain in `hub-tasks/*.json`.
