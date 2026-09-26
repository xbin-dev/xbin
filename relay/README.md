# xbin push relay

Self-hosted xbind instances cannot send iOS push notifications: the APNs key
belongs to whoever publishes the app. The relay is the one small service that
holds that key. It maps opaque **handles** to APNs device tokens and forwards
**sealed envelopes** — it never sees notification content (design:
[plans/native.md §14](../plans/native.md); wire formats:
[native/spec/push.md](../native/spec/push.md)).

```
xbin app ──(APNs token)──▶ relay: POST /v1/handles ─▶ handle
xbin app ──(handle, X25519 public key)──▶ xbind: POST /api/xbin/devices/push
xbind ──(sealed envelope, workspace key)──▶ relay: POST /v1/push ─▶ APNs ─▶ device
device: Notification Service Extension opens the envelope, shows the real text
```

Go, standard library only (its own module in `relay/`, part of the repo's
`go.work`). Who operates the public relay, and at which domain, is not yet
decided (plans/native.md decision 13) — relay URLs are configuration
everywhere; nothing here assumes one.

## Running

```sh
go build -o xbin-relay ./relay/cmd/xbin-relay
./xbin-relay -listen 127.0.0.1:8650 -state /var/lib/xbin-relay/state.json \
  -apns-key AuthKey_ABC123.p8 -apns-key-id ABC123 -apns-team-id TEAM456 \
  -topics dev.xbin.app -trust-proxy
```

| Flag | Environment | Default | What |
|---|---|---|---|
| `-listen` | `XBIN_RELAY_LISTEN` | `127.0.0.1:8650` | listen address |
| `-state` | `XBIN_RELAY_STATE` | `relay-state.json` | the state file (handles, workspace key hashes); rewritten atomically, mode 0600 |
| `-apns-key` | `XBIN_RELAY_APNS_KEY` | — | the APNs auth key (`.p8`); without it registration works and pushes answer 503 |
| `-apns-key-id` | `XBIN_RELAY_APNS_KEY_ID` | — | the key's id (JWT `kid`) |
| `-apns-team-id` | `XBIN_RELAY_APNS_TEAM_ID` | — | the Apple developer team (JWT `iss`) |
| `-topics` | `XBIN_RELAY_TOPICS` | any | comma-separated bundle ids a handle may name — **set it in production** |
| `-trust-proxy` | `XBIN_RELAY_TRUST_PROXY` | off | client IP = the last `X-Forwarded-For` hop (registration rate limits) |
| `-tls-cert`, `-tls-key` | `XBIN_RELAY_TLS_CERT`, `XBIN_RELAY_TLS_KEY` | — | serve TLS directly; otherwise run behind a TLS proxy |

APNs authentication is token-based: an ES256 JWT (`kid`, `iss`, `iat`) signed
with the `.p8` key, reused for 40 minutes (APNs rejects tokens older than an
hour and throttles more frequent refreshes), refreshed early when APNs answers
`ExpiredProviderToken`. Requests go over HTTP/2 to `api.push.apple.com`, or
`api.sandbox.push.apple.com` for `development` handles.

## API

All bodies are JSON; errors are `{"error": "…"}`.

```
POST   /v1/handles            {apnsToken, topic, env} → {handle}
PUT    /v1/handles/<handle>   {apnsToken, topic, env} → {handle}
DELETE /v1/handles/<handle>   → 204 | 404
POST   /v1/workspaces         → {workspaceId, key}
POST   /v1/push               Authorization: Bearer <key>
                              {handle, envelope, collapseId?, priority?}
                              → {ok: true, apnsId}
GET    /healthz               → ok
```

**Handles** (called by the app). `apnsToken` is the device token in hex (64–200
characters, case-insensitive), `topic` the app's bundle id (one of `-topics`),
`env` `production` | `development` (`sandbox` is accepted as an alias). A
handle is 16 random bytes, base64url. **The app asks for one handle per
workspace**: the first workspace that pushes to a handle owns it, and any other
workspace gets 403 — so removing a workspace from the app (`DELETE` its handle)
cuts that workspace off for good, even if it keeps trying. `PUT` points an
existing handle at a new device token (APNs tokens change after a restore or
reinstall) without re-registering with every workspace. One device token holds
at most 64 handles; creating more evicts the least recently used.

**Workspaces** (called once by xbind, when an admin turns push on). The relay
answers a random id and a key (`xbr_…`); it stores only the key's SHA-256. The
key is the bearer credential for `/v1/push`.

**Push** (called by xbind). `envelope` is `{v: 1, epk, n, ct}` exactly as
native/spec/push.md defines it (base64url, no padding; `epk` 32 bytes, `n` 12
bytes, `ct` at most 3200 characters). The relay re-encodes it (no other keys
pass through) and sends APNs:

```json
{"aps": {"alert": "New activity", "mutable-content": 1, "sound": "default"},
 "xbin": {"v": 1, "epk": "…", "n": "…", "ct": "…"}}
```

with `apns-push-type: alert`, `apns-priority` 10 (or 5 when `priority` is 5),
`apns-expiration` now + 24 h, and `apns-collapse-id` when `collapseId` is given
(at most 64 bytes; xbind sends a hash, not its own id).

| Answer | Meaning | xbind does |
|---|---|---|
| 200 | delivered to APNs | — |
| 400 | malformed request, or APNs refused the payload | drop the notification |
| 401 | unknown workspace key | drop; the admin must re-register |
| 403 | the handle belongs to another workspace | drop the registration |
| 404 | unknown handle | drop the registration |
| 410 | APNs says the device token is dead; every handle of it is deleted | drop the registration |
| 429 | a rate limit (`Retry-After` seconds) | retry later |
| 502 | APNs failed or is unreachable | retry with backoff |
| 503 | the relay has no APNs key | retry later |

## Limits

Token buckets, in memory (they reset when the relay restarts):

| What | Key | Default |
|---|---|---|
| pushes | workspace | 3600 / hour, burst 120 |
| pushes | handle | 600 / hour, burst 30 |
| `POST /v1/workspaces` | client IP | 10 / hour, burst 3 |
| `POST`/`PUT /v1/handles` | client IP | 120 / hour, burst 20 |

Request bodies are capped at 8 KiB and the APNs payload at 4 KiB.

## What the relay knows

Device tokens, which handle belongs to which workspace id, when each was last
used, and the SHA-256 of each workspace key. It does not know workspace URLs,
users, or anything a notification says: the envelope is sealed on the xbind
side to a key that lives only in the app (X25519 + HKDF-SHA256 + AES-256-GCM).
Logs never carry envelopes.

## Tests

`go test ./relay/...` runs the relay against a fake APNs (an `httptest` TLS
server speaking HTTP/2) that verifies the ES256 provider token and records each
notification: delivery and headers, token caching and refresh, `410`/
`BadDeviceToken` dropping handles, handle binding, repointing, rate limits and
persistence.
