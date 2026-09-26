# Push notifications — wire formats (v1)

> Status: implemented on the xbind and relay side (`internal/push`,
> `relay/`); the app's Notification Service Extension implements the device
> side against this page and [push-vectors.json](push-vectors.json). Live
> Activities (§7): xbind and relay implemented and tested; the app side
> (`native/ios/App/Push/LiveActivities.swift`, `native/ios/Widgets`) is
> written against this page and compiles only on the Apple CI.
> Design: plans/native.md §14. Relay operations: relay/README.md.

Three parties:

| Party | Holds | Sees |
|---|---|---|
| **app** (per workspace) | an X25519 key pair; the relay handle | everything, after decrypting |
| **relay** (run by the xbin project) | the APNs key; handle → device token | handles, tokens, ciphertext sizes, timing |
| **xbind** (self-hosted) | registrations: user, deviceId, handle, device public key | what it notifies about |

## 1. Keys and registration

1. **Device key.** Per workspace the app generates an X25519 key pair
   (CryptoKit `Curve25519.KeyAgreement.PrivateKey`). It is *not* the device-login
   key (that is a P-256 Secure Enclave key; the enclave has no X25519). Store the
   private key in the Keychain in an access group shared with the Notification
   Service Extension, accessible `AfterFirstUnlock` (the extension runs while the
   phone is locked). Rotating it = registering again with the new public key.
2. **Handle.** The app registers its APNs device token with the relay, **one
   handle per workspace**: `POST <relay>/v1/handles {apnsToken, topic, env}` →
   `{handle}` (`topic` = the app's bundle id; `env` = `production` |
   `development`). The first workspace that pushes to a handle owns it; another
   workspace gets 403 `handle_bound`. When APNs issues a new token: `PUT
   <relay>/v1/handles/<handle>` with the new token (the handle — and every
   workspace registration — stays; a 404 means the handle is gone: make a new
   one). When the user removes a workspace: `DELETE
   <relay>/v1/handles/<handle>` (cuts that workspace off for good) and `DELETE
   /api/xbin/devices/push/<deviceId>` on the workspace if it is reachable.
   The relay may check a new token with APNs by sending it a silent background
   notification (`{"aps":{"content-available":1}}`, no `xbin` key): ignore it.
   Relay errors are `{"error", "code"}`; the codes: relay/README.md.
3. **Registration with xbind**, with the device session (a human principal; a
   tile is refused). A device session registers under **its own device-login
   id** only (`403` for any other); a registration made with another session
   (the app's password or SSO session before the device is enrolled) lasts
   only as long as that session, and can't take over an enrolled device's id
   (`409`):

   ```
   POST /api/xbin/devices/push
   {"deviceId": "<the app's device id>", "handle": "<relay handle>",
    "publicKey": "<X25519 public key, base64url, 32 bytes>",
    "kinds": ["agent", "tile"]}                         (optional; none = all)
   → {"device": {"deviceId", "kinds", "created", "updated", "lastSent"?,
                 "needsNewHandle"?, "relayError"?},
      "workspace": "<ws id>", "enabled": true|false}
   ```

   Keep `workspace`: every payload of this workspace carries it as `ws`.
   `enabled` false = the admin has not turned push on yet (registration still
   succeeds and starts working when they do). Re-register on every launch that
   changes anything (token, key, kinds); it is idempotent per (user, deviceId).
   `GET /api/xbin/devices/push` lists the user's registrations, `POST
   /api/xbin/push/test` sends a test push, `GET`/`PUT /api/xbin/push/prefs`
   read and set `{mutedTiles: [tile path]}`.
4. **Keeping it working.** On launch and when coming to the foreground, per
   workspace, `GET /api/xbin/devices/push` and find this `deviceId`:
   - **missing** → register again (step 3). Registrations go when the user is
     signed out everywhere, disabled or deleted, when the device is removed
     (revoked on its own, `?devices=1` on sign-out-everywhere, a password
     change with `removeDevices`), when its device session signs out
     (`POST /logout`), when the owner token it registered with is rotated,
     when the session that registered it ends (one made before enrolling),
     when an admin revokes the registration, and when APNs
     reports the device token dead. Register with the device-login
     `deviceId`, from the device session, once the device is enrolled —
     that is the id revocation matches, and the only id that session may
     register;
   - **`needsNewHandle: true`** → the relay will not deliver to this handle for
     this workspace any more (`relayError`: `handle_bound` — the workspace
     re-registered with the relay, e.g. after an admin rotated its relay key;
     `handle_unknown` — the relay no longer knows the handle). Make a fresh
     handle (`POST <relay>/v1/handles`, and `DELETE` the old one) and register
     again with it. xbind skips such a registration until then; it never
     deletes it for this.

## 2. Payload

The plaintext is the UTF-8 JSON of:

| Field | Type | Meaning |
|---|---|---|
| `v` | int | `1` |
| `ws` | string | the workspace's push id (`workspace` from the registration) |
| `kind` | string | `agent.permission`, `agent.question`, `agent.turn`, `tile`, `tile.<kind>`, `test` |
| `title` | string | plain text, at most 120 characters |
| `body` | string | plain text, may contain `\n`, at most 1000 characters |
| `link` | string | workspace-relative: `c/<tile>/[path][?query][#fragment]` for a tile, `agent/<session id>` for an agent session, `""` = none |
| `collapseId` | string | notifications with the same one replace each other (`""` = none) |

Kinds (registration `kinds` match a kind and everything under it: `agent`
matches `agent.turn`; `test` is always delivered):

| kind | raised when | link |
|---|---|---|
| `agent.permission` | an ACP session's `permission.request` is still unanswered 3 s later | `agent/<session>` |
| `agent.question` | an ACP `elicitation.request`, likewise | `agent/<session>` |
| `agent.turn` | a turn ends (`turn.end`), except when the user cancelled it | `agent/<session>` |
| `tile`, `tile.<kind>` | a tile called `POST /api/xbin/notify` | `c/<tile>/…` |
| `test` | the user called `POST /api/xbin/push/test` | `""` |

The deep link the app opens is `xbin://<app's workspace id>/<link>`
(plans/native.md §4). Decoders ignore unknown fields; new kinds may appear
(show them like `tile`).

## 3. Sealing

Given the device's public key `R` (32 bytes) and the payload bytes `P`:

```
e, E   = a fresh X25519 key pair (per notification)
shared = X25519(e, R)                                  32 bytes; refuse all-zero
key    = HKDF-SHA256(ikm = shared,
                     salt = E || R,                    64 bytes
                     info = "xbin-push-v1",            ASCII
                     length = 32)
n      = 12 random bytes
ct     = AES-256-GCM-Seal(key, nonce = n, plaintext = P, aad = none)
                                                       ciphertext || 16-byte tag
```

The envelope:

```json
{"v": 1, "epk": "<base64url E>", "n": "<base64url n>", "ct": "<base64url ct>"}
```

base64url is RFC 4648 §5 **without padding**. `ct` is at most 3200 characters
(the relay refuses more); xbind trims `body`, then `title`, until the payload
fits 2300 bytes.

Opening (the extension), with the device private key `r`:

```
shared = X25519(r, E);  key = HKDF-SHA256(shared, salt = E || R, info, 32)
P      = AES-256-GCM-Open(key, n, ct)   — failure = not for this key
```

In CryptoKit:

```swift
let epk = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: e.epk)
let shared = try priv.sharedSecretFromKeyAgreement(with: epk)
let key = shared.hkdfDerivedSymmetricKey(using: SHA256.self,
    salt: e.epk + priv.publicKey.rawRepresentation,
    sharedInfo: Data("xbin-push-v1".utf8), outputByteCount: 32)
let box = try AES.GCM.SealedBox(nonce: AES.GCM.Nonce(data: e.n),
    ciphertext: e.ct.dropLast(16), tag: e.ct.suffix(16))
let payload = try AES.GCM.open(box, using: key)
```

## 4. What the device receives

The relay sends APNs (`apns-push-type: alert`, priority 10 unless xbind asked
for 5, expiration 24 h, `apns-collapse-id` = a hash of `ws` and the
`collapseId` when there is one):

```json
{"aps": {"alert": "New activity", "mutable-content": 1, "sound": "default"},
 "xbin": {"v": 1, "epk": "…", "n": "…", "ct": "…"}}
```

The Notification Service Extension:

1. reads `userInfo["xbin"]`; absent or `v` ≠ 1 → leave the notification as is;
2. tries each workspace's private key (a few at most; the GCM tag tells the
   right one) — none opens it → leave "New activity";
3. checks the payload's `v` = 1 and `ws` = the workspace whose key opened it
   (else treat as unopened);
4. sets `title`, `body`, `threadIdentifier` = `ws` (grouping per workspace),
   `userInfo` = `{ws, kind, link}`, and a category per kind (actions such as
   "Mute this tile" call `PUT /api/xbin/push/prefs` from the app);
5. delivers. The app, when foregrounded on the very surface the link names,
   suppresses the banner (`willPresent`).

Live Activity updates cannot be decrypted by an extension, so they carry only
generic state (running, waiting, elapsed): §7.

## 5. Test vectors

[push-vectors.json](push-vectors.json) (generated by
`internal/push/seal_test.go`, checked by it and by an independent Node
implementation): each vector gives the recipient and ephemeral private keys,
the nonce, the intermediate `sharedSecret` and `key`, the exact `plaintext`, and
the resulting `envelope`. A device implementation must (a) open every
`vectors[i].envelope` with `recipientPrivate` to exactly `plaintext`, (b)
reproduce `envelope.ct` from `ephemeralPrivate` + `nonce`, and (c) fail on every
entry of `invalid`.

## 6. Versioning and security notes

- The envelope and payload `v` are 1. A new format changes `v` and the HKDF
  `info`; xbind will only send it to devices that registered for it.
- Each notification uses a fresh ephemeral key, but the device key is long-lived:
  someone who later steals it can read notifications they recorded. Rotate by
  registering a new public key (e.g. on every app update).
- The relay learns which handles a workspace notifies and when — not what. Its
  collapse ids are hashes; xbind never sends tile paths or session ids in
  clear.
- Live Activity pushes (§7) are the one unsealed channel: the relay and APNs
  see that an agent turn runs, waits or ended, since when and how many
  requests wait — never a name, a title or text (the relay refuses any) —
  and a push-to-start's `ws` and random `ref`.

## 7. Live Activities

The app shows an agent turn as a **Live Activity** (the lock screen and the
Dynamic Island): which session, running or waiting for you, since when, how
many requests wait. It is **generic on purpose**. An ActivityKit payload is
decoded and drawn by the system before any code of the app runs, so it can't
be sealed; xbind sends only the state below and the relay builds the APNs
body from it, refusing anything else (relay/README.md §Live Activities).
Names — the workspace's, the session's — exist only on the device.

### 7.1 The card

ActivityKit attributes type `AgentActivityAttributes` (XbinAgent), fixed for
the activity's life; all strings, a missing one reads as `""`:

| Field | Set by the app | In a push-to-start |
|---|---|---|
| `ws` | xbind's push id of the workspace (`workspace` of §1.3), or `""` | the same |
| `ref` | `""` | the reference the app registers the token under (§7.3) |
| `workspace`, `session` | display names | `""` (the widget looks `ws` up in the app's push keyring) |
| `appWorkspace`, `sessionID` | the app's workspace id and the agent session id — the card's link `xbin://<appWorkspace>/agent/<sessionID>` | `""` (the link opens the workspace) |

Content state (`ContentState`, `AgentActivityState`):

```json
{"phase": "running" | "waiting" | "idle", "since": <unix s, the turn's start; 0 = unknown>, "pending": <0–99>}
```

`waiting` = a permission request or a question is unanswered (or the agent
says `waiting_permission`); `pending` counts both; `idle` = the turn is
over (the last state, shown until dismissed). Decoders are lenient: an
unknown phase reads as `running`, a missing number as 0.

A card is **one turn**: it starts when a turn has run 10 s (quick turns at
the desk never flash one), at once when it waits for the user, or when the
app leaves the foreground mid-turn (ActivityKit starts activities only from
the foreground); it ends at the turn's `turn.end` (idle, dismissed 10
minutes later). A card the user swiped away stays away for that turn.

### 7.2 Tokens and handles

ActivityKit gives the app one push token per activity (its updates) and one
per app (push-to-start). Each goes to the relay as a **Live Activity handle
under the workspace's device handle** (§1.2):

```
POST <relay>/v1/handles {"apnsToken": "<hex>", "topic": "<bundle id>", "env": …,
                         "pushType": "liveactivity", "parent": "<the workspace's device handle>"}
→ {"handle"}
```

It lives, binds and goes with its parent: deleting the parent (removing the
workspace from the app), a rotation that orphans it (`needsNewHandle`), or
APNs killing the device token takes the Live Activity handles along — the
app makes new ones under its new device handle. The same token under the
same parent answers the same handle.

### 7.3 Registration with xbind

- **Push-to-start**: the device's registration carries it —
  `POST /api/xbin/devices/push {…, "startHandle": "<Live Activity handle>"}`
  (absent keeps the one registered, `""` removes it: the user turned
  push-started cards off). A new device `handle` drops it and every
  activity registration (they hang off the old handle at the relay);
  `GET /api/xbin/devices/push` shows `pushToStart: true` and
  `activities: [session]` per device.
- **An activity**: once ActivityKit hands its update token,
  ```
  POST /api/xbin/devices/push/activities {"deviceId", "session": "<agent session id>", "handle", "since"?}   (one the app started)
  POST /api/xbin/devices/push/activities {"deviceId", "ref": "<attributes.ref>", "handle"}                   (one xbind started)
  → {"activity": {"session", "created"}}
  ```
  `since` is the turn's start the card shows (unix seconds); xbind takes it
  only for a turn it did not see begin (the user had no device then), so
  its updates keep the card's clock.
  The session must be the caller's (404 otherwise); the registration is the
  caller's own (the device session: its own `deviceId`, 403 otherwise).
  `DELETE /api/xbin/devices/push/<deviceId>/activities/<session>` when the
  user dismissed the card.

### 7.4 What xbind sends

xbind follows the agent sessions of users with a registered device from
their events and posts to the relay (`POST /v1/push {handle, type:
"liveactivity", activity, priority}`, relay/README.md):

| When | `event` | To | Priority |
|---|---|---|---|
| the phase or the pending count changes | `update` (+ `staleDate` now + 4 h) | each registered activity of the session | 10 for `waiting`, else 5 |
| `turn.end`, the session going busy → idle, error, exited, or closing | `end` (idle, + `dismissalDate` now + 10 min); the registrations go | the same | 10 |
| a turn still busy after 30 s, on a device with a `startHandle`, `agent` kinds, and no card for the session | `start` (`ws`, a fresh random `ref`, the state) | the device's `startHandle` | 10 |
| xbind starts (agent sessions do not outlive it) | `end` | every registered activity | 5 |

`timestamp` is unix seconds, strictly increasing per session (the device
drops an update older than what it shows). The relay's 410, `handle_bound`
or `handle_unknown` for an activity's handle drops that registration (for
the `startHandle`: the start handle); the app registers the next one — no
`needsNewHandle` round for these. Limits: 240 updates/hour per activity
(burst 30), 240 activity registrations/hour per person (burst 30).

A push-started card's token reaches the app in the background
(ActivityKit's `activityUpdates` / `pushTokenUpdates`); the app registers it
by `ref`, and xbind answers with the session and sends what changed since
the start.

## 8. Relay registration and proof of work

xbind mints the workspace's relay key with `POST <relay>/v1/workspaces`.
A relay may ask for a proof of work (`-registration-pow`,
relay/README.md §Proof of work): xbind reads `GET
/v1/workspaces/challenge`, finds the first decimal nonce with SHA-256
(`challenge ":" nonce`) starting with `bits` zero bits (at most 28), and
posts `{"pow": {"challenge", "nonce"}}`; a 401 `pow_required` /
`pow_invalid` carries a fresh challenge, solved once more. The app is not
involved.
