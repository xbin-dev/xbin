# Push notifications — wire formats (v1)

> Status: implemented on the xbind and relay side (`internal/push`,
> `relay/`); the app's Notification Service Extension implements the device
> side against this page and [push-vectors.json](push-vectors.json).
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
generic state (running, waiting, elapsed) — not specified here.

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
