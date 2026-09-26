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
| `-state` | `XBIN_RELAY_STATE` | `relay-state.json` | the state snapshot (handles, workspace key hashes), mode 0600, with its journal next to it (`<state>.log`) — see *State* |
| `-apns-key` | `XBIN_RELAY_APNS_KEY` | — | the APNs auth key (`.p8`); without it registration works and pushes answer 503 |
| `-apns-key-id` | `XBIN_RELAY_APNS_KEY_ID` | — | the key's id (JWT `kid`) |
| `-apns-team-id` | `XBIN_RELAY_APNS_TEAM_ID` | — | the Apple developer team (JWT `iss`) |
| `-topics` | `XBIN_RELAY_TOPICS` | any | comma-separated bundle ids a handle may name — **set it in production** |
| `-trust-proxy` | `XBIN_RELAY_TRUST_PROXY` | off | client IP = the last `X-Forwarded-For` hop (registration rate limits) |
| `-verify-tokens` | `XBIN_RELAY_NO_VERIFY_TOKENS` (set = off) | on | check each device token with APNs before storing a handle (needs `-apns-key`) |
| `-tls-cert`, `-tls-key` | `XBIN_RELAY_TLS_CERT`, `XBIN_RELAY_TLS_KEY` | — | serve TLS directly; otherwise run behind a TLS proxy |

APNs authentication is token-based: an ES256 JWT (`kid`, `iss`, `iat`) signed
with the `.p8` key, reused for 40 minutes (APNs rejects tokens older than an
hour and throttles more frequent refreshes), refreshed early when APNs answers
`ExpiredProviderToken`. Requests go over HTTP/2 to `api.push.apple.com`, or
`api.sandbox.push.apple.com` for `development` handles.

## API

All bodies are JSON; errors are `{"error": "…", "code": "…"}` — the `code`
is the contract (a client acts on it, never on a bare status a proxy could
have produced), the text is for people.

```
POST   /v1/handles            {apnsToken, topic, env} → {handle}
PUT    /v1/handles/<handle>   {apnsToken, topic, env} → {handle}
DELETE /v1/handles/<handle>   → 204 | 404
POST   /v1/workspaces         → {workspaceId, key}
GET    /v1/workspace          Authorization: Bearer <key> → {workspaceId}
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
at most 64 handles; creating more evicts the least recently used. With
`-verify-tokens` (the default when an APNs key is configured) the relay first
sends the token a silent background notification (`apns-push-type:
background`, `{"aps":{"content-available":1}}`) and refuses the handle when
APNs rejects the token (400 `bad_token`) — so the state holds real devices,
not whatever an unauthenticated caller makes up.

**Workspaces** (called once by xbind, when an admin turns push on). The relay
answers a random id and a key (`xbr_…`); it stores only the key's SHA-256. The
key is the bearer credential for `/v1/push`; `GET /v1/workspace` with it
answers the workspace id (xbind checks a key an admin hands it this way).
Every handle is bound to the workspace that first pushed to it, so **a new
workspace orphans the handles bound to the old one**: xbind keeps its key
across push off/on and registers anew only when an admin asks (rotate) —
after that the apps make fresh handles (xbind tells them: `needsNewHandle`,
native/spec/push.md §1).

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

| Answer | `code` | Meaning | xbind does |
|---|---|---|---|
| 200 | — | delivered to APNs | — |
| 400 | `bad_request`, `apns_refused` | malformed request, or APNs refused the payload | drop the notification |
| 401 | `bad_key` | unknown workspace key | drop; the admin re-registers (rotate) |
| 403 | `handle_bound` | the handle belongs to another workspace | mark the registration `needsNewHandle` |
| 404 | `handle_unknown` | unknown handle (deleted, expired, never here) | mark the registration `needsNewHandle` |
| 410 | `handle_gone` | APNs says the device token is dead; every handle of it is deleted | drop the registration |
| 429 | `rate_limited` | a rate limit (`Retry-After` seconds) | retry later |
| 500 | `internal` | the relay could not store its state | retry with backoff |
| 502 | `apns_unavailable` | APNs failed or is unreachable | retry with backoff |
| 503 | `no_apns`, `full` | the relay has no APNs key / is at capacity | retry later |

A 403 or 404 without one of these codes (a proxy, a WAF, a wrong URL) is
not the relay's word: xbind fails that notification and keeps the
registration.

## Limits

Token buckets, in memory (they reset when the relay restarts):

| What | Key | Default |
|---|---|---|
| pushes | workspace | 3600 / hour, burst 120 |
| pushes | handle | 600 / hour, burst 30 |
| `POST /v1/workspaces` | client (IPv6: /48) | 10 / hour, burst 3 |
| `POST /v1/workspaces` | everyone together | 600 / hour, burst 60 |
| `POST`/`PUT /v1/handles` | client (IPv6: /64) | 360 / hour, burst 60 |

A client is an IPv4 address or an IPv6 prefix (a /64 is one subscriber's
LAN: per address, one host would have 2^64 buckets; workspaces are servers,
so their prefix is wider). The handle limit is generous because carrier NAT
puts many phones behind one IPv4 address.

Request bodies are capped at 8 KiB and the APNs payload at 4 KiB.

## Capacity and retention

`POST /v1/handles` and `POST /v1/workspaces` are unauthenticated, so the state
is bounded rather than trusted to stay small:

| What | Default | `relay.Config` |
|---|---|---|
| handles at most | 2 000 000 (then 503 `full`) | `MaxHandles` |
| workspaces at most | 200 000 (then 503 `full`) | `MaxWorkspaces` |
| a handle no workspace ever pushed to is deleted after | 30 days | `UnboundHandleTTL` |
| a handle nothing pushed to is deleted after | 180 days | `IdleHandleTTL` |
| a workspace key never used (no push, no `GET /v1/workspace`) is deleted after | 30 days | `UnusedWorkspaceTTL` |

The sweep runs with registrations, at most every 10 minutes (every minute
while full). A deleted handle answers 404 `handle_unknown`, and the app makes a
new one.

## State

The state is a snapshot (`-state`) plus an append-only journal
(`<state>.log`). Every change — a handle, a binding, a workspace, a deletion,
a usage timestamp at most once a day per entry — appends one small record and
fsyncs it, outside the lock the push path takes; no request rewrites the whole
state. When the journal outgrows the snapshot (and 4 MiB), a background
compaction writes a new snapshot from a copy of the state and starts a fresh
journal. On start the journal is replayed onto the snapshot (a torn last line
is ignored) and folded into it; on shutdown (SIGTERM) the relay compacts once
more.

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
`BadDeviceToken` dropping handles, handle binding, repointing, error codes, the
key probe, rate limits (IPv6 per /64), token verification, capacity and
retention, and the journal (crash replay, a torn line, compaction under
concurrent writes).
