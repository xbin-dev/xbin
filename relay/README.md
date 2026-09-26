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

xbin app ──(ActivityKit token, device handle)──▶ relay: POST /v1/handles {pushType: liveactivity} ─▶ handle
xbind ──(phase, since, pending — no text)──▶ relay: POST /v1/push {type: liveactivity} ─▶ APNs ─▶ Live Activity
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
| `-registration-tokens` | `XBIN_RELAY_REGISTRATION_TOKENS` | open | comma-separated tokens `POST /v1/workspaces` then requires (see *Capacity and retention*) |
| `-registration-pow` | `XBIN_RELAY_REGISTRATION_POW` | 0 (none) | bits of proof of work an anonymous `POST /v1/workspaces` must carry, 0–32 (see *Proof of work*) |
| `-max-workspaces`, `-max-handles` | `XBIN_RELAY_MAX_WORKSPACES`, `XBIN_RELAY_MAX_HANDLES` | 200 000, 2 000 000 | capacity |
| `-unused-workspace-ttl`, `-idle-workspace-ttl`, `-unbound-handle-ttl`, `-idle-handle-ttl` | `XBIN_RELAY_UNUSED_WORKSPACE_TTL`, … (Go durations) | 720h, 4320h, 720h, 4320h | retention |
| `-workspace-rate`, `-handle-rate`, `-refused-push-rate`, `-new-workspace-rate`, `-all-new-workspaces-rate`, `-new-handle-rate` | `XBIN_RELAY_WORKSPACE_RATE`, … | see *Limits* | `PER_HOUR/BURST`, e.g. `10/3` |

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
POST   /v1/handles            {apnsToken, topic, env, pushType?, parent?} → {handle}
PUT    /v1/handles/<handle>   {apnsToken, topic, env, pushType?} → {handle}
DELETE /v1/handles/<handle>   → 204 | 404
GET    /v1/workspaces/challenge → {challenge, bits, expires} | {bits: 0}
POST   /v1/workspaces         {pow?: {challenge, nonce}} → {workspaceId, key}
GET    /v1/workspace          Authorization: Bearer <key> → {workspaceId}
POST   /v1/push               Authorization: Bearer <key>
                              {handle, envelope, collapseId?, priority?}
                              {handle, type: "liveactivity", activity, priority?}
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
| 401 | `bad_key` | unknown workspace key | drop; the admin re-registers (rotate), or sets a new `XBIN_PUSH_RELAY_KEY` |
| 401 | `registration` | `POST /v1/workspaces` on a relay closed to its operator's tokens | the admin gets a key from the operator |
| 401 | `pow_required`, `pow_invalid` | `POST /v1/workspaces` without a proof of work, or with a wrong, expired, too easy or spent one; the body carries a fresh `challenge`, `bits`, `expires` | solve it and post again |
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

## Live Activities

A Live Activity (the app's agent-turn card on the lock screen and in the
Dynamic Island) is updated by pushes of its own: ActivityKit hands the app
one push token per activity (its updates) and one per app (push-to-start),
and APNs takes them with `apns-push-type: liveactivity` on the topic
`<bundle id>.push-type.liveactivity`. The app registers each token as a
**Live Activity handle** under the device handle it made for the same
workspace:

```
POST /v1/handles {apnsToken, topic, env, pushType: "liveactivity", parent: <device handle>} → {handle}
```

- `parent` must be a device handle of the same `topic` and `env` (400
  `bad_request` otherwise, 404 `handle_unknown` when there is none). The
  same token under the same parent answers the same handle.
- A Live Activity handle **lives under its parent**: deleting the parent
  (the app removing the workspace), APNs killing the parent's device token,
  or retention removing it takes its Live Activity handles along. A parent
  holds at most 16; a new one evicts the least recently used.
- It is **bound with its parent**: one made under a bound device handle is
  born bound to that workspace (so retention's rule for handles nothing
  ever pushed to leaves a push-to-start handle waiting for its first long
  turn alone); otherwise the first push to either binds both to the
  pushing workspace. Another workspace gets 403 `handle_bound`.
- Its token is **not** checked with a silent push (an ActivityKit token
  takes only Live Activity pushes); the parent's was, and bounds how many
  it may hold. `PUT /v1/handles/<handle>` with `pushType: "liveactivity"`
  repoints it; a handle never changes kind (400).

xbind pushes a content state, never a payload of its own — an ActivityKit
payload can't be sealed (the system draws it before any of the app's code
runs), so the relay builds the APNs body from enumerated and numeric fields
and lets no text through:

```
POST /v1/push  Authorization: Bearer <key>
{"handle": "<Live Activity handle>", "type": "liveactivity", "priority": 5 | 10,
 "activity": {"event": "update" | "end" | "start",
              "timestamp": <unix s>,                 (not in the future; the device drops older ones)
              "state": {"phase": "running" | "waiting" | "idle",
                        "since": <unix s, the turn's start; 0 = unknown>,
                        "pending": <0–99, requests waiting for the user>},
              "staleDate"?: <unix s, within a day>,
              "dismissalDate"?: <unix s, within a day; end only>,
              "ws"?, "ref"?: <start only: 1–64 of A–Z a–z 0–9 _ ->}}
```

becomes (`apns-push-type: liveactivity`, `apns-priority` as asked, default
10, `apns-expiration` now + 24 h, no collapse id):

```json
{"aps": {"timestamp": 1790000000, "event": "update",
         "content-state": {"phase": "waiting", "since": 1789999958, "pending": 1},
         "stale-date": 1790003600}}
```

and for `start` (push-to-start, to the app's push-to-start handle) also
`"attributes-type": "AgentActivityAttributes"`, `"attributes": {"ws", "ref",
"workspace": "", "session": "", "appWorkspace": "", "sessionID": ""}` (the
app fills the names in from its own records of `ws`), `"input-push-token": 1`
and a generic alert chosen by phase (`{"title": "xbin", "body": "An agent is
working."}`, `… is waiting for you.`, `… finished.`). Anything else — an
unknown phase, a string where a number goes, a field of the wrong event, an
envelope or collapse id, a device handle — is 400 `bad_request` before APNs
sees it. A dead Live Activity token (APNs `410`/`Unregistered`: the
activity ended, the app was reinstalled) answers 410 `handle_gone` and
deletes that handle only. The push limits (per handle, per workspace) are
the same as for alerts.

## Limits

Token buckets, in memory (they reset when the relay restarts):

| What | Key | Default |
|---|---|---|
| pushes | workspace | 3600 / hour, burst 120 |
| pushes | handle | 600 / hour, burst 30 |
| pushes to an unknown or another workspace's handle | workspace | 600 / hour, burst 60 |
| `POST /v1/workspaces` | client (IPv6: /48) | 10 / hour, burst 3 |
| `POST /v1/workspaces` | everyone together | 600 / hour, burst 60 |
| `POST`/`PUT /v1/handles` | client (IPv6: /64) | 360 / hour, burst 60 |

A push is checked in this order: the key, the request, the handle (unknown
or another workspace's: `404`/`403`, charged to the refused-push bucket
only), the handle's bucket, then the workspace's — so the workspace's
delivery budget pays only for pushes that can reach a device, and a push a
handle's own limit refuses costs it nothing.

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
| a workspace key unused for (no push, no `GET /v1/workspace`) is deleted after | 180 days | `IdleWorkspaceTTL` |

xbind checks its key (`GET /v1/workspace`) at start and daily, so a
workspace in use never idles; one that stops running for half a year loses
its key and registers again. Every limit, cap and TTL is a flag of
`xbin-relay` too (`-max-workspaces`, `-idle-workspace-ttl`,
`-new-workspace-rate 10/3`, …; `-h` lists them), so an operator under a
registration flood can tighten them without a rebuild, make each anonymous
registration cost CPU time (`-registration-pow`, below), or close
registration: `-registration-tokens a,b` makes `POST /v1/workspaces` require
`Authorization: Bearer` one of them (`401 {code:"registration"}` otherwise;
a token skips the proof of work); the operator then mints workspace keys
and hands them to admins (`PUT /api/xbin/push/config {key}` or
`XBIN_PUSH_RELAY_KEY`). While a flood lasts, anonymous opt-ins get 429;
keys already issued keep working.

### Proof of work

`-registration-pow <bits>` (0–32; 0 = off, the default) makes an anonymous
`POST /v1/workspaces` carry a hashcash-style proof of work. 20 bits is about
a million SHA-256 hashes on average — well under a second for xbind once,
and a real cost for someone minting workspaces by the thousand.

```
GET  /v1/workspaces/challenge → {"challenge": "<base64url>", "bits": 20, "expires": <unix s>}
                                ({"bits": 0} when the relay asks for no work)
find a nonce (1–64 of A–Z a–z 0–9 _ -) such that
     SHA-256(ASCII challenge ":" nonce) starts with `bits` zero bits
POST /v1/workspaces {"pow": {"challenge": "…", "nonce": "…"}}
```

- The challenge is **stateless**: version, difficulty, expiry (10 minutes)
  and 16 random bytes under an HMAC with a key the relay makes at start.
  Handing them out stores nothing; a restart makes the outstanding ones
  invalid.
- A proof is **spent** once it registers a workspace (replay: 401
  `pow_invalid`); the relay remembers spent challenges until they expire —
  the registration limits bound how many. A refusal by a rate limit spends
  nothing: retry the same proof after `Retry-After`.
- A refusal (401 `pow_required` / `pow_invalid`: missing, wrong, expired,
  easier than the relay's current difficulty, or spent) carries a fresh
  `{challenge, bits, expires}`, so a client solves and posts again.
- xbind solves up to 28 bits (an admin's `PUT /api/xbin/push/config`
  waits for it) and asks for a challenge only when the relay has the
  endpoint (an older relay: none).

Test vector: for the challenge string `xbin-pow-vector-1`, the first
decimal nonce (counting from 0) that solves 8, 16 and 20 bits is `148`,
`4813` and `268594` (`relay/pow_test.go`, `internal/push/pow_test.go`).

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
Logs never carry envelopes. Live Activity pushes are the exception to
sealing, and carry what the relay can see anyway: whether an agent turn is
running, waiting or done, since when, and how many requests wait — never
a session's name, a title or text.

## Tests

`go test ./relay/...` runs the relay against a fake APNs (an `httptest` TLS
server speaking HTTP/2) that verifies the ES256 provider token and records each
notification: delivery and headers, token caching and refresh, `410`/
`BadDeviceToken` dropping handles, handle binding, repointing, error codes, the
key probe, rate limits (IPv6 per /64), token verification, capacity and
retention, and the journal (crash replay, a torn line, compaction under
concurrent writes); Live Activities (the exact APNs body of update, end and
start, every refusal, binding with the parent, cascades, the cap, a restart,
repointing) and the proof of work (the vector, spent and expired and
tampered and foreign challenges, a raised difficulty, rate limits spending
nothing, registration tokens skipping it).
