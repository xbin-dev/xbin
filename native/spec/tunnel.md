# The app tunnel (D204): wire specification

How the xbin app reaches its workspace's xbind over iroh, with an xbin
encryption layer inside. This page is normative for both sides. xbind
(`internal/apptunnel`) generates the test vectors in `tunnel-vectors.json`
next to this file; the app (`Shared/NoiseTunnel.swift`,
`XbinCore/Client/Tunnel.swift`) must reproduce them byte for byte. The
builder-facing page is `/docs/app-tunnel.md`; device login itself is
`device-login.md`.

Tunnel version 1. Nothing here changes device login or the classic
`xbin://enroll?u&c` flow: an app that knows nothing of tunnels keeps
working with a workspace that doesn't require them.

## 1. Keys

| Who | Key | Where |
|---|---|---|
| xbind | iroh secret (Ed25519). Its public key is the server's **EndpointId** | `data/app-tunnel/keys.json` (0600, outside the vault so login works sealed) |
| xbind | Noise static (X25519) | the same file |
| device, per workspace | iroh secret (Ed25519). Its public key is the device's EndpointId | Keychain, `dev.xbin.tunnel`, `…ThisDeviceOnly` |
| device, per workspace | Noise static (X25519) | the same item |
| device, per workspace | device-login P-256 key (Secure Enclave), unchanged | `device-login.md` |

One iroh key and one Noise key per workspace, so two workspaces can't link
the device. The device's Noise public key and EndpointId are stored on its
device record when it pairs (`Device.TunnelKey`, `Device.Endpoint`).

## 2. The pairing link

```
xbin://enroll?u=<origin>&c=<code>&tv=1&t=<ticket>&k=<server key>&p=<psk>[&r=<relay token>][&rq=1]
```

- **`u`, `c`:** as in `device-login.md` §2. `c` is the enroll code; xbind
  mints it as a *tunnel* code, which never redeems over plain
  `POST /devices/enroll` (so the confirmation in §6 can't be skipped).
- **`tv`:** the tunnel version, `1`. An app given a `tv` it doesn't know
  refuses the link and says to update.
- **`t`:** an iroh EndpointTicket (`endpoint…`) holding the server's
  EndpointId and its relay URL only, with no direct addresses.
- **`k`:** the server's Noise static public key: 32 bytes, base64url without
  padding.
- **`p`:** the pairing psk: 32 random bytes, base64url without padding. It
  is single use, and valid as long as the code.
- **`r`:** the relay's access token, present only for a custom relay that
  has one. URL-encoded.
- **`rq=1`:** the workspace refuses this device's app traffic outside the
  tunnel.

If `t`, `k` or `p` is present but any of them is malformed, the app refuses
the whole link. It never quietly falls back to classic enrollment. The QR
code is rendered at error correction level M, at least 288 px wide.

## 3. iroh

- **ALPNs:** `xbin/pair/1` and `xbin/tunnel/1` (ASCII).
- **Device endpoint:**
  - preset **Minimal** with relay mode *custom*: exactly the ticket's relay
    URL, plus the token `r` if present;
  - no address publishing, no DNS discovery, no mDNS. The device only ever
    dials; it is never dialed.
  - It dials the ticket's address and refuses the connection unless
    `remoteId` equals the ticket's EndpointId.
  - A new endpoint is made each time the app becomes active (iroh #4289),
    and none is kept in the background.
- **Server endpoint:**
  - preset N0 when the relay is n0's; otherwise Minimal with the custom relay
    and its token.
  - It accepts `tunnel/1` only from EndpointIds on xbind's allowlist (paired
    devices), and `pair/1` only while a pairing is open; anything else is
    closed with error code 403 right after the handshake. It also lists its
    own probe ALPN, `xbin/selftest/1`, which it admits from itself alone.
  - Uni-streams and datagrams are refused; at most 128 bi-streams are open
    at once per connection.
  - A stream reaches xbind once its first bytes arrive (QUIC announces a
    stream with its first frame): the device writes first, as Noise's
    initiator does.
- **Streams:** one bi-stream per logical connection (on the device, one per
  TCP connection the loopback forwarder accepts). Every stream runs its own
  Noise handshake (§4).

## 4. The xbin layer: Noise

- **Patterns:** `Noise_IKpsk2_25519_ChaChaPoly_SHA256` on `pair/1`, and
  `Noise_IK_25519_ChaChaPoly_SHA256` on `tunnel/1` (Noise revision 34). The
  device is the initiator; xbind is the responder and holds the static key
  `k`.
- **Prologue:** ASCII `xbin-pair-v1` or `xbin-tunnel-v1` (no terminator),
  followed by the server EndpointId (32 bytes) and the device EndpointId (32
  bytes). The inner session is therefore bound to the iroh peers. Each side
  computes its own EndpointId from its own key; xbind takes the device's from
  iroh's authenticated `remoteId`.
- **Handshake messages:** each is framed as `len16(BE) ‖ message`. The device
  writes message 1, xbind answers with message 2, and xbind gives up after 10
  s.
- **Payloads:**
  - `tunnel/1`: both messages are empty.
  - `pair/1`: message 1 carries the **selector**,
    `SHA-256("xbin-pair-select-v1\n" ‖ code)[0:16]`, with `code` exactly as
    the link's `c` carries it, so xbind can find the pending pairing (and its
    psk) before reading message 2's tokens. The code itself never travels.
    Message 2 is empty.
  - No application data is ever sent in a handshake payload: message 1 is not
    forward-secret.
- **DH:** an X25519 result of all zeros (a low-order point) aborts the
  handshake on both sides.
- **Transport messages:** `len16(BE) ‖ ciphertext`, at most 65535 bytes
  including the 16-byte tag. The associated data is empty. The plaintext is
  `type(1) ‖ body`:

  | type | body | meaning |
  |---|---|---|
  | `0x00` | bytes | data |
  | `0x01` | empty | rekey: the sender calls `Rekey()` on its sending CipherState right after sending this message; the receiver calls it on its receiving CipherState right after decrypting it |
  | `0x02` | empty | close: an orderly end of the sender's direction (a half-close) |

  Any other type aborts the stream.
- **Rekey:** a sender rekeys after 2^20 messages, 2^30 plaintext bytes, or
  10 minutes since its last (re)key, whichever comes first. A receiver
  accepts a rekey at any time.
- **End of stream:** if the stream ends without a close message, that is
  truncation, an error. It must never be treated as an orderly EOF.

## 5. The confirmation code (`pair/1`)

```
okm = HKDF-SHA256(ikm = h, salt = "" (zero-length), info = "xbin-sas-v1", L = 8)
n   = big-endian uint64(okm) mod 1 000 000
```

`h` is the handshake hash once the handshake completes (Noise's channel
binding). Each side computes `n` itself; xbind never sends it. It is shown
as six digits, zero-padded and grouped: `012 345`.

- **The phone shows:** "Confirm on your computer: 012 345".
- **The computer** (the shell's add-device dialog or `/xbin/devices`, which
  minted the code) shows the same code and the device's name, with **Codes
  match** and **They don't**.
- **"They don't"** burns the code.

## 6. Pairing messages (`pair/1` data, one JSON object per data message)

1. **Device → xbind**, the first data message:
   `{"v":1,"name":"<device name>","platform":"ios","publicKey":"<base64url SPKI DER of the device-login P-256 key, as in device-login.md>"}`.
   The device's Noise key is the handshake's remote static key and its
   EndpointId is iroh's `remoteId`. Neither travels in the payload.
2. **xbind → device:** `{"type":"await","expires":"<RFC 3339>"}`. The
   computer now shows the code.
3. **xbind → device**, once the minting session confirms, then a close
   message:
   `{"type":"enrolled","deviceId","user":{"id","name"},"origin","name","tunnel":{"ticket","relay":{"url"?,"token"?},"required"}}`.
   - `origin` is the origin the device signs at device login
     (`device-login.md` §4). It is never a loopback address.
   - xbind sends this only after the iroh sidecar has acknowledged the
     device's EndpointId on the `tunnel/1` allowlist.
   - When the person was at their device limit, the confirmation named a
     device to replace, and the replacement happens in the same step.
4. **xbind → device**, instead of 3, then a close message:
   `{"type":"rejected","reason":"mismatch|expired|limit|refused","error":"<text>"}`.

If the stream ends before the confirmation, the pairing is *abandoned*:
the confirmation answers 409 and no device record is created.

After `enrolled`, the device opens `tunnel/1` and signs in with device login
over it.

## 7. HTTP over `tunnel/1`

- **Plain HTTP.** After the handshake the stream carries HTTP/1.1 (WebSocket
  upgrades included), exactly as a TCP connection to xbind would.
  - The device sends `Host: 127.0.0.1:<port>`, its loopback forwarder's
    address.
  - xbind treats tunnel requests as plain http: no `Secure` or `__Host-`
    cookies, and `xbin-ws-origin` is `ws://<Host>`.
  - It strips `X-Forwarded-*`, and keys client addresses (login throttle,
    session records) by device.
- **Credentials** are unchanged: `Authorization: Bearer <device session>`
  for the app's own requests, and cookies for web views that redeemed a web
  ticket.
- **One device per tunnel.** On device D's tunnel, xbind serves only:
  - requests carrying D's credentials: a bearer or cookie session opened by
    D, or a frame or asset token derived from one;
  - D's own device login (`/login/device/challenge`, `/login/device` for
    `deviceId` D);
  - web-ticket redemption (`GET /login?ticket=`, `POST /login/web-ticket`);
  - `GET /api/xbin/login/methods` and `/vendor/*`.

  Anything else answers 403 `{"error","tunnel":"device"}`. Other apps on the
  phone can reach its loopback port; this is why that gains them nothing.
- **Bound sessions.** A session opened over the tunnel, and every credential
  derived from it, works only on D's tunnel.
- **`GET /api/xbin/app-tunnel/client`** (D's session, over the tunnel)
  answers
  `{"endpointId","ticket","serverKey","relay":{"url"?,"token"?,"next"?:{"url"?,"token"?}},"required"}`.
  The device stores it after every connect, and tries `next` when the
  current relay refuses it, which is how a rotation reaches devices.

## 8. When the workspace requires the tunnel

The workspace setting "refuse app connections outside the iroh tunnel"
(only with tunnels on):

- **Refused off the tunnel:** a device's bearer, a web-ticket cookie a
  device opened, app sign-in by password, invite or SSO ticket
  (`POST /login/ticket`), and `POST /devices/enroll`. The answer is 403
  `{"error":"<text that names QR pairing>","tunnel":"required","docs":"/docs/app-tunnel.md"}`.
  It is never 401, so an old app doesn't loop re-signing, and never 404,
  which an old app reads as "removed".
- **Still allowed:** the SSO browser leg (`/login/sso?app=1`), so that
  device login over the tunnel can follow it.
- **`GET /api/xbin/login/methods`** carries `"tunnel":{"enabled","required"}`
  (absent before D204).
- **Browsers** are not affected.

## 9. Upgrading an enrolled device

`POST /api/xbin/devices/{id}/tunnel {"noiseKey":"<base64url X25519>","endpoint":"<hex EndpointId>"}`.

- **Who may call it:** the device's own session, signed in within the last
  10 minutes, over a direct connection. It answers 403 while the workspace
  requires the tunnel.
- **What it does:** registers the keys and answers as
  `/app-tunnel/client`. The device then uses `tunnel/1`; an unproven key
  simply never completes IK.

## 10. Test vectors (`tunnel-vectors.json`)

Generated by `go test ./internal/apptunnel/noise -run Vectors` with
`UPDATE_VECTORS=1`. For each case, the file holds:

- every private and public key, the ephemerals, and the psk, code and
  selector;
- both EndpointIds and the prologue;
- message 1 and message 2;
- `h` and the code;
- N transport messages in each direction, including a rekey and a close.

It also holds invalid cases (a wrong psk, a wrong prologue, a flipped byte,
a low-order point, an unknown frame type, and truncation), each reusing a
base vector with the fields it overrides and naming the step that must
fail, and the Noise community's (cacophony) `IK` and `IKpsk2` vectors for
this cipher suite. A rekey does not reset the nonce (Noise §11.3); the
vectors' frames after a rekey pin that.
