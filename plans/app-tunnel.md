# App tunnels: iroh for the xbin app (D204)

> Status: live — being built (D204). The wire contract is `native/spec/tunnel.md`; where this page and the spec differ, the spec wins.

## The ask and the owner's decisions (2026-10-27)

The owner: "Iroh ios tunnels. Workspace admin may enable "Iroh tunneling" support in admin workspace (admin tile->workspace->app) settings, this would also allow setting custom iroh relay (+ have short instructions how to setup a selfhosted iroh relay properly, with auth of some sort); inside iroh tunnels we still must have xbin layer of encryption in addition to iroh encryption - traffic must be auntenticated between xbind and user device, with PFS. When iroh tunneling is enabled, the 'add device' flow gives a larger QR code with additional iroh connect string + ws auth code (pairing code pop-up just to be sure, anti-phishing?). App settings - limit for connected apps, default 1 (possible to set workspace-wide), overrides per user. In app settings also possible to disable non-iroh app connections (prevent leaking cookies eg. when device not on vpn)"

- **Server iroh:** a separate Rust `xbin-iroh` process (iroh 1.4.x, dumbpipe as the template) embedded in xbind, self-extracted (memfd) and run in a light namespace sandbox with the host network. xbind stays pure Go, `CGO_ENABLED=0`.
- **Relay:** n0's public relays by default (hobby-scale, which the admin UI says); a self-hosted `iroh-relay` with `access.shared_token` as the option.
- **Limit:** paired devices per user; workspace default 1, per-user overrides; existing devices grandfathered (enforced when a device is added).
- **iOS:** n0's IrohLib (iroh-ffi 1.2.x), pinned `exactVersion`.
- **Confirmation code:** compare and click — "Codes match" / "They don't" — not typed.
- **Verification:** simulator and Linux during the build; then a release with a TestFlight build the owner verifies on a device (bugs fixed in follow-up releases).
- **App Store** (audited 2026-10-27):
  - not a VPN (5.4), no background modes (2.5.4), no downloaded code (2.5.2);
  - IPv6-only/NAT64 (2.5.5): the relay path, with DNS64 resolution of the relay, a dual-stack self-hosted relay, and a NAT64 run in the owner's checklist;
  - the phone uses iroh's Minimal preset always (no n0 DNS, no mDNS, no multicast entitlement);
  - `privacy.html` and `PrivacyInfo.xcprivacy`, with required-reason APIs from `nm -u`;
  - the Local Network purpose string; Review Notes; an open-source licences screen;
  - `ITSAppUsesNonExemptEncryption = true` (the owner answers export compliance).

## Where the two design passes disagreed, and what was decided

1. **Plain HTTP over the tunnel, no synthetic TLS** (iOS side). Tunnel requests reach xbind with the loopback Host the app's forwarder listens on, and xbind treats them as plain http. iOS 26 WebKit drops `Secure`/`__Host-` cookies on http loopback, and `xbin-ws-origin` must be `ws://127.0.0.1:port`. xbind still strips `X-Forwarded-*` and keys addresses per device. `EnrollOrigin` on the tunnel is the recorded or external origin, never loopback.
2. **The pairing payloads follow the server pass:** a selector (not the code) in message 1, keys taken from the handshake and iroh's `remoteId` (not from JSON), and an in-band rekey message.
3. **One device per tunnel** (iOS pass §2.1): a device's tunnel serves only that device's credentials and sign-in steps, because other apps can reach the phone's loopback port.
4. **Compare and click** (the owner), not typing (the iOS pass's suggestion).
5. **Re-pairing at the limit:** the confirmation names a device to replace.
6. **Existing devices upgrade without re-pairing:** `POST /devices/{id}/tunnel` (both passes recommended it).
7. **The bearer stays and tunnel sessions are always bound** (the server pass).
8. **The SSO browser leg stays allowed under iroh-only;** only `POST /login/ticket` is refused off the tunnel.
9. **Naming:** "app tunnel" in code, routes and docs (`internal/apptunnel`, `/api/xbin/app-tunnel`), because "tunnel" already means SSH tunnels here. The UI says "Iroh tunneling".

The two passes and the research follow verbatim, for reference: file and line pointers verified against master 60249dd0.


## Appendix A — server, sidecar, protocol (design pass)

## D204: Iroh tunnels for the xbin app, server-side implementation plan

Every insertion point below comes from reading the code. Facts about iroh and iroh-relay come from upstream docs and source as of today. Where the owner's design needs to change, the change is flagged **⚑** and gathered again in §15.

### 0. Corrections the plan depends on

1. **"tunnel" already means something else in this codebase.** It names SSH tunnels and proxies in three places: `ExternalURL` ("tunnel-only deployments", `internal/server/server.go`), `EnrollOrigin`, and the "address your phone uses" hint in `bx-devices.js`. Call the new feature **"app tunnel"** everywhere: package `internal/apptunnel`, `/api/xbin/app-tunnel`, `docs/app-tunnel.md`.
2. **Never call `srv.Handler()` a second time.** It runs `s.loginThrottle = newLoginThrottle()`. The tunnel's `http.Server` must reuse the `handler` that `serve()` already built (`internal/boot/serve.go:33`). The `cfg.Listener` seam is the console listener only, so the tunnel gets its own `http.Server`.
3. **`ClientIP` will merge all tunnel clients into one key unless handled.** `ClientIP` (`internal/server/login.go:80`) runs `net.SplitHostPort(RemoteAddr)`. That call does not validate the port, so a RemoteAddr like `tunnel:abc` becomes host `tunnel` for every device. A conn built with `net.FileConn` has an empty RemoteAddr, which is no better. Fix it in two places:
   - an explicit transport check at the top of `ClientIP`;
   - a wrapped `RemoteAddr` with no colon (`tunnel/<deviceID>`).
4. **Scheme detection and spoofable headers.** Four sites decide http vs https with `r.TLS != nil || X-Forwarded-Proto` and trust that header from any peer: `devicelogin.go:165`, `native.go:152`, `sso.go:253`, and `auth/tilebinding.go:67,100`. The tunnel handler wrapper should set a synthetic `r.TLS` (TLS 1.3, `HandshakeComplete`) and delete `X-Forwarded-*`. **⚑** `appWSOriginMeta` would then emit `wss://<Host>`, which is wrong if the app bridges WebSockets on loopback. This has to be agreed with the iOS plan (§15.3).
5. **Reuse the existing fd-passing factory.** `internal/sandbox/factory_linux.go` already does SOCK_SEQPACKET plus SCM_RIGHTS passing of a fresh SOCK_STREAM socketpair end, and `AcceptFrom` receives with `MSG_CMSG_CLOEXEC`. The sidecar sends; xbind accepts. No socket path, no abstract socket, nothing anyone else can dial. The sandbox hands the child end to the entry as `--fd N` (`Spec.Agent`, `entryArgv`). Needed: a 3-line `(*Factory).Accept()`, plus a stub in `launch_other.go`.
6. **`confine.Run` can't host a daemon.** It is one-shot, captures output, and has a 2-minute default timeout. Use `sandbox.Launch` directly. Today it requires a rootfs (`mountRoot`: "spec has no rootfs lowerdir"), so `internal/sandbox` needs two small additions (§5.6).
7. **`brk.AdminAlerts` is a single func that gets overwritten** (`internal/boot/goversions.go`), not a chain. The tunnel alert must wrap the previous value.
8. **Size budgets are tight.** `openapi.go` is at the 800-line cap for unlisted files. `usersapi.go` is at 788 of 789. `auth.go` is at 721 of 732. Moves are in §9.

### 1. Architecture

```
phone (IrohLib) ──QUIC/TLS (iroh)──▶ xbin-iroh sidecar (Rust; bare sandbox, host net)
                                   │ per accepted bi-stream: new socketpair, end sent with
                                   │ 'c'+SCM_RIGHTS over the inherited SEQPACKET (--fd N)
                                   ▼
xbind: sandbox.(*Factory).Accept ─▶ 48-byte header ─▶ Noise IK / IKpsk2 (Go) ─┬─▶ pair/1: pairing state machine
                                                                              └─▶ tunnel/1: noise.Conn ─▶ tunnel http.Server
                                                                                  (same handler, ConnContext = auth.Transport)
control plane: stdin/stdout JSON lines; stderr → slog; the secret key is the first stdin line
```

Noise terminates in xbind, so a compromised sidecar sees ciphertext only. This is the concrete reason for the owner's second encryption layer, and it should be stated in D204.

### 2. Wire contracts (published as `native/spec/tunnel.md` plus `tunnel-vectors.json`)

#### 2.1 Sidecar ↔ xbind

- **Launch.** `xbin-iroh serve --fd N`, run as PID 1 of its own pid namespace. It must install a SIGTERM handler, because PID 1 ignores default-action signals. stdin, stdout and stderr are pipes.
- **Input, JSON lines:**
  - first line: `{"t":"init","v":1,"secretKey":"<hex32>","relay":{"mode":"n0"|"custom"|"disabled","url":…,"token":…},"allow":[…],"pairing":false}`. `disabled` exists only for tests (env `XBIN_IROH_RELAY`).
  - after that: `allow` (full replacement, with `seq`), `pairing {open}`, `close {endpoint}` (the device was revoked), `selftest`, `status`.
- **Output:**
  - `ready {endpointId, ticket, relayTicket, homeRelay, version}`
  - `ack {seq}`
  - `status {homeRelay, relayConnected, conns:[{endpoint, conn, path, rttMs}]}`
  - `conn {open|close}`
  - `reject {endpoint, alpn}`, rate-limited
  - `error {msg, fatal}`
- **Per-stream header,** in cleartext on the passed socket, then a raw splice:

```
0  4  magic "XBI1"   4 1 version=1   5 1 alpn: 1=xbin/tunnel/1, 2=xbin/pair/1
6  2  reserved=0     8 32 remote EndpointId (ed25519 pub)   40 8 connection serial (BE)
```

  xbind treats the EndpointId as a **claim**. It feeds the Noise prologue and is cross-checked against the device record. It is never an identity on its own.
- **Iroh setup:**
  - `presets::N0` for n0 relays.
  - `presets::Minimal` plus `RelayMode::Custom` with exactly one relay, `RelayConfig::with_auth_token`, and QUIC address discovery for a custom relay. It has to be one relay because without discovery the phone can only reach the server's home relay.
  - An `EndpointHooks::after_handshake` hook, server side only. ALPN `tunnel/1` is accepted only if the remote id is on the allowlist; `pair/1` only while pairing is open; anything else is rejected with error code 403.
  - Uni-streams and datagrams are refused. `max_concurrent_bidi_streams` = 128.

#### 2.2 The xbin layer

- **Library:** flynn/noise v1.1.0 (§3.4), wrapped in `internal/apptunnel/noise`.
- **Handshakes.** `tunnel/1` uses `Noise_IK_25519_ChaChaPoly_SHA256`. `pair/1` uses `Noise_IKpsk2_25519_ChaChaPoly_SHA256`.
  - Prologues: ASCII `xbin-tunnel-v1` or `xbin-pair-v1`, followed by server EndpointId (32 bytes) and device EndpointId (32 bytes).
  - The server computes its own EndpointId from its key with `crypto/ed25519`, so it never takes that value from the sidecar either.
- **Handshake payloads are empty,** with one exception: pair/1 message 1 carries a 16-byte selector, `SHA-256("xbin-pair-select-v1\n"‖code)[:16]`. So the code itself never travels, not even in the non-forward-secret message 1.
- **IKpsk2 detail.** The server learns that the initiator knows the psk only from the first transport message. So a pairing reaches "confirm" only after that message (the enroll payload) decrypts. A replayed message 1 costs the server some DH work and changes nothing.
- **Frames:** `len(2, BE) ‖ ciphertext (≤ 65535)`. The plaintext is `type(1) ‖ body`, with `0x00 data`, `0x01 rekey`, `0x02 close`.
  - **⚑** Rekey works through an in-band frame. After sending it, the sender calls `Rekey()` on its send CipherState; the receiver does the same after decrypting it. A time-based policy is then safe: rekey after 2^20 frames, 1 GiB, or 10 minutes, whichever comes first. Counting on both sides alone can't express time.
  - An authenticated `close` maps to `io.EOF`. A bare EOF maps to `io.ErrUnexpectedEOF`, which detects truncation by the sidecar.
- **SAS:** `HKDF-SHA256(ikm = final h (ChannelBinding()), salt = "", info = "xbin-sas-v1", L = 8)`, read as BE uint64, `mod 1e6`, zero-padded and shown as `123 456`. Each side computes it on its own; the server never sends it.

#### 2.3 QR and pairing

- **URL:** `xbin://enroll?u=<origin>&c=<code>&tv=1&t=<relay-only EndpointTicket>&k=<server noise pub b64url>&p=<psk b64url>[&r=<relay token>][&rq=1]`.
  - The ticket carries only the relay URL, no direct addresses, which keeps it at about 110 characters.
  - `rq=1` means "tunnel required". The app must pin it and never fall back to HTTP for this workspace.
- **Message sequence:**
  1. Device → server, first transport message: `{"v":1,"name","platform","publicKey":<SPKI P-256>}`.
  2. Server → device: `{"type":"await","expires"}`.
  3. On confirm, server → device: `{"type":"enrolled","deviceId","user","origin","name","ticket","relay":{…},"required"}`, then close. Before sending it, xbind waits for the sidecar's `ack` on the new allowlist, so the device's first `tunnel/1` connection is not rejected.
  4. On "They don't": `{"type":"rejected"}`, and the code is burned.
  5. If the stream drops before confirmation, the state becomes `abandoned` and confirm answers 409. This avoids orphan device records.
- **Device record fields:**
  - `TunnelKey` comes from `hs.PeerStatic()`, never from the payload. It must be unique across the store.
  - `Endpoint` (hex) comes from the header. It may repeat, because one phone can hold two accounts on one server.
- **Code TTL:** the existing 5 minutes from minting, plus a 2-minute confirm window that starts at the handshake.

### 3. Pushback and recommendations on open points

1. **Should the tunnel's HTTP still carry the app's bearer token? Recommend yes.** The tunnel authenticates the transport and binds the channel; the bearer from device login authenticates the person. Reasons:
   - The Noise X25519 key can't live in the Secure Enclave, which does P-256 only. Only the enclave key is gated by Face ID.
   - SSO-bound windows (D93, `deviceSSOBound`), session TTLs, revocation and frame-token generations all key on sessions.
   - Identity-only would turn every stream into an unbounded session.
2. **Bind tunnel sessions always, not only under iroh-only.** A session minted over the tunnel is accepted only over the tunnel and only from that device. A leaked token becomes useless off-tunnel. The cost: an app that falls back to HTTPS signs in again.
3. **Old apps and SAS.** A tunnel code must not be redeemable over plain `POST /devices/enroll`. Otherwise a leaked QR bypasses the SAS step. So old apps can't use tunnel QR codes. While iroh-only is off, the dialog offers "Older app? Show a classic code".
4. **⚑ Don't refuse `/login/sso?app=1` under iroh-only.** It is a browser leg (ASWebAuthenticationSession) and carries no xbin credential. The callback already stamps `LastSSO` before the ticket is minted (`TouchLogin(…,"sso")` in `sso.go`). An SSO-bound app can therefore run the SSO leg, skip redeeming the ticket, and retry device login over the tunnel. Refuse only `POST /login/ticket`. As written, iroh-only would lock SSO-only accounts out of the app every session-max-TTL.
5. **Noise library: flynn/noise v1.1.0.**
   - It is pure Go, uses only x/crypto (already in `go.mod`), and is what go-libp2p uses.
   - It provides psk placement, `ChannelBinding`, `PeerStatic`, `Rekey`, and an injectable `Random` for deterministic test vectors. Verify these exact API names when pinning.
   - The repo's "no new dependency" precedents (push and backup seals) are single-shot constructions. A handshake state machine is where hand-rolled code fails silently.
   - The wrapper keeps the library swappable, and the cacophony IK/IKpsk2 vectors run against it.
6. **Settings storage.**
   - Tunnel settings (enabled, relay, tokens, iroh-only) go in a store of their own, `data/app-tunnel/config.json`, following the push precedent. `wssettings` stays bool-only.
   - Keys go in `data/app-tunnel/keys.json` (0600: the iroh seed and the Noise static key). They stay unsealed so login works while the vault is sealed.
   - The device-limit default goes in `users.json` (`deviceLimit`) and the per-user override on the `User` row, because `AddDevice` enforces the limit under the users store's lock.
7. **Relay token and URL changes need a staged "next" slot holding {url, token}.**
   - Devices sync via `GET /app-tunnel/client` on every tunnel connection, and xbind records the generation each device has synced.
   - Upstream confirms iroh-relay accepts a list for `access.shared_token`, and the client presents one token.
   - Rotation:
     1. Add the new token to the relay config and restart the relay.
     2. Set it as "next" in xbin.
     3. Promote it: the sidecar restarts with it.
     4. Once the admin UI shows every device synced, drop the old token from the relay.
8. **Upgrading existing devices to the tunnel. ⚑ Recommend adding `POST /devices/{id}/tunnel`.** The device's own fresh device session, over HTTPS, registers its `{noiseKey, endpoint}`. It is refused under iroh-only. Without this, turning iroh-only on forces every user to remove and re-pair. With a limit of 1, that is remove-then-add for everyone.

### 4. Credential and transport rules (`internal/auth/transport.go`, one gate)

The gate runs at the end of `FromRequest` (`auth.go:557`). It keys on the principal's `Gen`: `s.<handle>` resolves to the live or orphan session (`genSessionLocked`), which carries `deviceID` and `tunnel`. Every derived credential carries a generation, so the same function also covers:
- the frame-token path (`fromRequest`);
- path tickets (`servePathTicket`);
- asset tokens and tile-origin grants (`grantLive` callers in `static.go` and `tileorigin.go`).

Audit each of those.

| Credential | Off tunnel | Off tunnel, iroh-only | Tunnel, same device | Tunnel, other device or user |
|---|---|---|---|---|
| Browser cookie (password/SSO/invite), owner/instance/terminal tokens | ok | ok | refuse | refuse |
| App session without a device (app password / SSO ticket / invite) | ok | refuse | (can't be minted) | refuse |
| Device bearer minted off tunnel | ok | refuse | ok | refuse |
| Device bearer minted over the tunnel (bound) | refuse | refuse | ok | refuse |
| Web-ticket cookie (device-opened) | follows its opening transport | refuse unless bound and on the tunnel | ok | refuse |
| `u.` frame grants | ok | ok | user must match | refuse |
| `o.` / `t.` grants | ok | ok | refuse | refuse |

Login routes, in `internal/server/tunnelgate.go`, return a 403 or 401 body of the form `{error, tunnel: "required"|"bound", docs: "/docs/app-tunnel.md"}`. The error text names QR pairing.
- `POST /api/xbin/login`, `/invite/redeem`, `/login/ticket`: refused under iroh-only when off-tunnel, and always refused over the tunnel ("sign in with the device key").
- `POST /devices/enroll`: refused under iroh-only and over the tunnel; tunnel codes always answer 401.
- `/login/device/challenge` and `/login/device`: over the tunnel, `deviceId` must equal the tunnel's device, and the resulting session is bound. Under iroh-only, refused off-tunnel.
- `GET /login?ticket=` and `POST /login/web-ticket`: the cookie binds to the transport it is redeemed on. Refused off-tunnel under iroh-only.
- `POST /devices/enroll-code`: under iroh-only, a request without `tunnel:true` gets 409 with "open <origin>/xbin/devices".

`authed` (`server.go:231`) returns a JSON 401 with the reason, so the app can tell "sign in over the tunnel" from an ordinary expiry.

### 5. Server Go: files

#### 5.1 New package `internal/apptunnel` (follows the push service pattern)

- **`service.go`**: `New(Options{Dir, Users, Auth, Hub, IsAdmin, RunDir, Isolated, Binary})`. It owns the store, keys, supervisor, listener and pairing.
- **`config.go`**: the config store and admin view. Tokens appear only as hints (`…ab12`); a generated token is shown once. Validation:
  - relay URL must be https, or http on loopback only;
  - tokens are 16–256 printable characters with no whitespace;
  - `tunnelOnly` requires `enabled`, and disabling the tunnel clears it (like SSO-only).
- **`keys.go`**: generate and load the keys; derive the EndpointId.
- **`listener.go`**: implements `net.Listener`.
  - An accept loop calls `Factory.Accept`, reads the header (5 s deadline), then runs the handshake per conn (10 s), never blocking `Accept`.
  - Limits: 64 conns per device, 1024 total.
  - It looks up the device by Noise static key in an in-memory index rebuilt on `users` events and once a minute.
  - It requires `device.Endpoint == header id` and the user not disabled.
  - Each conn carries a `*liveDevice` with an atomic `revoked` flag.
- **`pairing.go`**: the state machine (minted → handshaken → confirm → done | rejected | abandoned | expired), selector index, per-endpoint throttle, and opening and closing pairing on the sidecar.
- **`api.go`**: routes (§6).
- **`clientcfg.go`**: `GET /app-tunnel/client` and sync records.
- **`status.go`**: health and the alerts chained into `AdminAlerts`:
  - `warn` after 3 failed starts or 2 minutes down;
  - `crit` when iroh-only is on and the sidecar is down, since apps are then locked out.
- **`policy.go`**: what `server.AppTunnel` and `auth.SetTunnelPolicy` read.

#### 5.2 Subpackages

- **`internal/apptunnel/noise`**: `Responder`/`Initiator` wrappers, `Conn` (a `net.Conn` with separate send and receive CipherStates and a mutex each, plus deadline passthrough), and `SAS()`. Tests include `vectors_test.go`, which writes `native/spec/tunnel-vectors.json` with `UPDATE_VECTORS=1`, the same pattern as `internal/push/seal_test.go:109`.
- **`internal/apptunnel/wire`**: header encode/parse (strict on magic, version, ALPN and reserved bytes) and the control message types. Shared with the fake sidecar.
- **`internal/apptunnel/devclient`**: a Go implementation of the device side (pairing, IK, an `http.RoundTripper` over a dialer). Used by tests, by `hack/fakeiroh client`, and as the reference for iOS.
- **`internal/apptunnel/sidecar`**:
  - **Binary source:** `XBIN_IROH` (a file), otherwise the embedded gzip (`embed_iroh.go`, `//go:build xbin_iroh && linux`), otherwise none (`embed_none.go`).
  - **Extraction:** `memfd_create(MFD_CLOEXEC|MFD_ALLOW_SEALING|MFD_EXEC)`, retried without `MFD_EXEC` on EINVAL, then all four seals. If memfd fails or exec from it is denied, fall back to `<RunDir>/iroh/` (0700). Debian mounts `/run` noexec, so the last fallback is `<ws>/.xbin/iroh/`. Status reports which path was used.
  - **Launch:** if `sandbox.Available()`, sandbox it even when `--isolate` is off. With isolation off and no user namespaces, run `exec.Command("/proc/self/fd/N") // exec-ok: xbind's own embedded binary; no sandbox exists with isolation off`. With isolation on and a sandbox that won't start, the result is the `failed` state (D78: no host fallback).
  - **Supervision:** restart with backoff 1 s doubling to 60 s, reset after 5 minutes healthy. After `ready`, re-push the allowlist and pairing state. Shutdown: SIGTERM, then SIGKILL after 3 s. The factory's EOF also stops the sidecar.

#### 5.3 `internal/auth`

- **`transport.go` (new):** `Transport{Tunnel bool; DeviceID, UserID, Endpoint string}`, `WithTransport`, `TransportOf`, `SetTunnelPolicy`, and the gate. The derived-credential helper `GenAllowedOn(r, gen, uid)` lives here.
- **`sessions.go`:** add `session.tunnel`, `sessionView.tunnel`, and `NewBearerSessionOpts`. Keep the existing constructors as wrappers.
- **`framegens.go`:** add `genRecord.T bool \`json:"t,omitempty"\`` so orphans keep their binding.
- **`webticket.go`:** add a `tunnel` parameter to `OpenWebSession`.
- **`devices.go`:** add `enrollCode.tunnelOnly` and `MintEnrollCodeTunnel`. `RedeemEnrollCode` refuses tunnel codes.
- **`auth.go`:** add `Principal.TunnelDevice`, the policy field, and the gate call. Move the warm-IP block (`authedIPTTL` through `sweepWarmLocked`) into `warmip.go`. `warmLocked` should also accept `tunnel/<dev>` keys (key per device, not "IPs only").

#### 5.4 `internal/server`

- `devicelogin.go`:
  - `apiEnrollCode` gets a `{tunnel}` branch that calls `s.AppTunnel.MintPairing`, plus an early device-limit check that answers 409 `{error, limit, count}`.
  - The gates go into each login handler.
  - `writeAppSession` gets a tunnel flag.
- `login.go`: the `ClientIP` transport check.
- `server.go`: a `Server.AppTunnel` interface field and the JSON 401.
- `loginmethods.go`: `tunnel: {enabled, required}`.
- `webticket.go`: the gates.
- `native.go`: the tunnel behaviour of `appWSOriginMeta`, to agree with iOS.
- New `tunnelgate.go`, `openapi_tunnel.go`, and `devicespage.go`. The last serves `/xbin/devices` from `web/devices.html`, copying `limitspage.go` (CSP, framing headers).
- `deployclass.go` / `partitionclass.go` rows:
  - admin routes: `PrimaryOnly` / `GlobalOnlyRefused`, like push/config;
  - device and pairing routes: `Neutral` / `PartitionNeutral`, like devices/enroll.

#### 5.5 Users, broker and boot

- **`internal/users`:**
  - `devices.go`: new `Device.TunnelKey` and `Device.Endpoint`. `AddDevice` enforces `min(effective limit, 32)` under the lock and refuses a duplicate `TunnelKey`. New error text: "you have N devices; this workspace allows M per person — remove one first (an admin can raise your limit)".
  - New `devicelimit.go`.
  - `users.go`: add `User.MaxDevices *int`, `deviceLimit` in `Open` and `persistLocked`, and keep the override in `Upsert`.
  - `userjson.go`: add the field to both the raw struct **and** the literal (the 2026-09-05 email incident).
- **`internal/broker`:**
  - `devicesapi.go`: `deviceView.tunnel`; `GET /devices` adds `{limit, count}`; `GET /users/{id}/devices` adds `{limit, override}`; new `GET/PUT /devices/limits`. Chain the device removal into `OnDeviceRemoved`, which drops the device from the allowlist and closes its conns.
  - New `usersapi_devices.go`: an embedded `deviceLimitBody{MaxDevices json.RawMessage}` with D88 presence semantics (absent keeps, `null` clears).
  - `usersapi_personal.go`: add `userListRow.DeviceLimit`.
- **`internal/boot`:**
  - New `apptunnel.go` with `setupAppTunnel(srv)`, called after `setupPush` in `stepServer`.
  - `serveAppTunnel(ctx, handler)` lives in `serve.go`. It wraps `handler` with the synthetic TLS and header stripping, sets `ConnContext` to `auth.WithTransport`, and closes before `brk.Close()`.
  - `config.go` gets `XBIN_IROH` and `XBIN_IROH_RELAY` (`disabled` is test-only). Regenerate `docs/config.md` with `UPDATE_DOCS=1`.

#### 5.6 `internal/sandbox`

- `Spec.Bare`: the root is a tmpfs (generalising `vmRoot`). After the binds, remount it read-only before `pivot_root`. Binds are read-only `/etc/resolv.conf` (resolve its symlink) and `/etc/hosts`. CA roots are compiled into the sidecar (webpki-roots).
- `Spec.Exe *os.File`: passed like Agent/Lock. The init runs `unix.Exec("/proc/self/fd/N", …)` after mounting `/proc`. `internal/sandbox` is exempt from `TestNoDirectExec`.
- `(*Factory).Accept()`.
- The spec for the sidecar: `HostNet: true`, `Unprivileged: true` (all capabilities dropped, backend seccomp block-list), user/mount/pid/ipc/uts namespaces.

### 6. Routes (all documented in `docs/protocol.md` and `openapi_tunnel.go`)

| Route | Caller | Purpose |
|---|---|---|
| `GET /app-tunnel` | admin | config view, sidecar state (`disabled\|unavailable\|starting\|running\|degraded\|failed`, `confined`, `restarts`, `error`), endpointId, homeRelay, `devices {total, tunnelPaired, classic, connected, synced}`, live conns |
| `PUT /app-tunnel` | admin | `{enabled?, relay?, tunnelOnly?}`; changing the relay restarts the sidecar; enabling `tunnelOnly` reports the number of classic devices it will lock out |
| `POST /app-tunnel/relay-tokens` | admin | `{action: generate-next\|set-next\|promote\|drop-next\|set-current\|clear, token?}` |
| `POST /app-tunnel/test` | admin | sidecar selftest: an ephemeral endpoint dials the server through the relay; returns `{reachable, path, rttMs}` |
| `GET /app-tunnel/client` | own device session, over the tunnel | `{endpointId, ticket, relay {current, next?}, required, serverKey}` |
| `GET /devices/pairing/{id}` | the minting session (same user **and** same generation) | `{state, sas?, device {name, platform}, expires}` |
| `POST /devices/pairing/{id}/confirm` | same | `{match: bool}` |
| `GET` / `PUT /devices/limits` | admin / `xbin:users` | `{default, max: 32, atLimit, overrides[]}` |
| `PATCH /users/{id}` `{maxDevices: n\|null}` | admin / `xbin:users` | per-user override, range 0–32 (0 = none) |

Additive changes to existing routes:
- `enroll-code` accepts an optional request field `tunnel` and returns `tunnel {pairing, ticket, serverKey, required}` plus a new 409.
- `/login/methods` gains `tunnel`.
- `/users` rows gain `maxDevices` and `deviceLimit`.
- Device views gain `tunnel`.
- A bodiless `app-tunnel` event is published on changes.

### 7. Web UI (worker C, working against mocked routes until the API lands)

- **`web/bx-pair.js`** (new `/vendor/` module): the tunnel QR at 4 or more px per module (at least 288 px) and the SAS confirmation step. The confirm step shows the code large, the device's name, "Only confirm if YOUR phone shows this code", **Codes match** and **They don't**, plus the reject and abandoned copy.
- **`web/devices.html`** with `devices-page.js`, served at `/xbin/devices`. This is the D163/D203 guaranteed surface, needed because old shells can't show the SAS step. Old-shell error strings point to it.
- **`bx-devices.js`:**
  - sends `{tunnel:true}`;
  - dynamically imports `/vendor/bx-pair.js` and falls back to the classic flow;
  - shows "N of M devices"; at the limit, the add button is replaced by inline remove;
  - offers the "classic code" link when iroh-only is off;
  - the "address your phone uses" override only rewrites `u=` and keeps the tunnel params.
- **`tabs/nativeapp.js`** imports a new sibling `tabs/apptunnel.js`, relatively (compat rule 4). That tab holds:
  - the enable switch and relay choice. The n0 option carries the note "hobby-scale; publishes this server's endpoint and addresses to n0 DNS". The custom option has a URL field and the current/next token slots with generate, promote and drop.
  - the iroh-only switch with "N devices aren't tunnel-paired and will be locked out";
  - the status card (endpoint id, home relay, reachable with a Test button, connected devices, sync counts);
  - the default device limit;
  - a link to `/docs/app-tunnel.md`.
- **`tabs/users.js`:** a limit control in the devices… expansion. Show the button even when the user has 0 devices.
- **Harness:** new passes registered in `passes-more.js`, not `shots.js` (868 of 877).
  - `apptunnel`: admin tab states with `page.route` mocks.
  - `pairing`: the bigger QR, the SAS state, "They don't", and the `/xbin/devices` page.
  - Later, a real pass against a harness workspace running `XBIN_IROH=bin/fakeiroh` and a `devclient` stand-in, extending `passes/devices.js`.

### 8. Sidecar, build and embed (worker B)

- **Crate `sidecar/xbin-iroh/`:** `Cargo.toml` pinning iroh `=1.4.x` with `tls-ring` and minimal features; `Cargo.lock` committed. Sources:
  - `main.rs`: `serve | client | version`
  - `control.rs`, `factory.rs` (mirrors `Factory.send`), `serve.rs` (dumbpipe-derived: router, `copy_bidirectional`, header write), `hooks.rs`, `status.rs`
  - `client.rs`: test mode. Each local unix connection becomes one bi-stream to a ticket, with the relay token read from stdin.
  - NOTICE carrying dumbpipe's MIT/Apache attribution, and `cargo test` covering header, control parsing, fd-pass round trip and hooks.
  - **Status:** I did not check whether iroh 1.4.x has shipped; `iroh-relay` on docs.rs showed 1.0.3 when crawled. Confirm both pins before writing the crate.
- **`hack/build-xbin-iroh.sh`:** a docker builder with a digest-pinned Rust image and cargo-zigbuild, building `--locked` for `{x86_64,aarch64}-unknown-linux-musl` natively. Rust under qemu emulation would take hours. Output is a stripped `bin/xbin-iroh`.
- **Helpers:** in `hack/helpers-lib.sh`, a new `iroh` group: files `xbin-iroh`, arches amd64 and arm64, inputs = the script plus every file under `sidecar/xbin-iroh` except `target/`. Then `hack/helpers.sha256` once a maintainer publishes it.
- **Makefile:**
  - an `XBIN_IROH` file target;
  - `build:` depends on it, runs `hack/embed-iroh.sh` (gzip into the gitignored `internal/apptunnel/sidecar/embedded/xbin-iroh.linux-<arch>.gz`), then `go build -tags xbin_iroh`;
  - `dev*` passes `XBIN_IROH` only when `bin/xbin-iroh` exists;
  - `integration-deps` includes `HELPERS=iroh`;
  - a new `sidecar-check` target (cargo test), outside `check`, run by CI like `tile-check`.
- **Other build files:**
  - `deploy/publish-release.sh`: add `bin/xbin-iroh` and the embed file to the per-arch `rm -f` list. `make build` does the rest.
  - `.gitignore`: the embed path and `sidecar/xbin-iroh/target/`.
  - `ci.yml`: helpers `iroh` in the integration shards, plus a sidecar-check job.
  - `hack/integration.jsonc`: an `apptunnel` suite.
- **Vulnerability scanning. ⚑** `make vulncheck` doesn't scan the sidecar. Add `cargo deny check advisories` to `make release`.

### 9. Size-budget moves (`hack/size-budget.txt`)

- `internal/auth/auth.go` (721 of 732): move the warm-IP block (about 85 lines) to `warmip.go`, then add about 10 lines. The file lands near 650. That is under 90% of the budget, so the ratchet forces lowering the budget to the new size, with at most 5% headroom.
- `internal/broker/usersapi.go` (788 of 789): move `apiAccountPassword` and `weakPassword`/`minPasswordLen` (about 50 lines) to `usersapi_password.go`, then embed `deviceLimitBody` and apply it in create and update (+6). Lower the budget optionally.
- `internal/server/openapi.go` (800, unlisted cap 800): add `appTunnelEndpoints()` to the existing `joinEndpoints` line (630). Keep edits to the Devices rows line-neutral. If they grow, move the Devices group to `openapi_devices.go` first.
- No change needed for: `users.go` (799 to about 806 of 840), `server.go` (768 to about 780), `boot.go` (741 to 744), `cmd/bx/main.go` (+3 of 1068), `bx-devices.js` (stays under 600 because of `bx-pair.js`), `shots.js` (passes go in `passes-more.js`).

### 10. bx

New `cmd/bx/app.go`:
- `bx app tunnel [status] | on | off | relay n0 | relay <url> | token generate-next | set-next <t> | promote | drop-next | only on|off | test`
- `bx app limit [<n>]`

`bx user set <id> --max-devices N|default`. Update `docs/bx.md` and the usage strings.

### 11. Phases and work-package split

- **P0, contracts. Blocks everything; also unblocks the iOS agent.**
  - `native/spec/tunnel.md`, control-protocol section, API table, D204 draft, `plans/app-tunnel.md`.
  - Owner answers on §15.
- **P1, in parallel:**
  - **A1** `noise`, `wire`, `devclient`, vectors. This is the first deliverable to the iOS team.
  - **A2** device limit: users, broker, bx, tests. Independent; can ship alone.
  - **A3** auth transport layer and session binding, plus the warm-IP move.
  - **B1** sandbox `Bare`, `Exe`, `Factory.Accept`, with tests in `sandbox_linux_test.go`.
  - **B2** the Rust crate.
  - **B3** build, embed and helpers.
  - **B4** `hack/fakeiroh` in Go: a serve mode over loopback TCP and a client mode. Needs A1's `wire` package.
  - **C1** all UI against mocks, plus harness mock passes.
- **P2, server integration (A, needs B1 and B4):**
  - **A4** service, supervisor, listener, boot wiring, ConnContext, ClientIP.
  - **A5** pairing and enroll-code.
  - **A6** login-route gates, web tickets, login methods, JSON 401.
  - **A7** routes, openapi, protocol.md, route classes, events, alerts.
- **P3, end to end:**
  - integration with fakeiroh (in CI);
  - integration with the real sidecar (relay disabled, loopback direct, client mode);
  - the real harness pass;
  - docs and changelog.
- **P4, follow-ups:** the device upgrade route (if the owner accepts it), seccomp allowlist inside the sidecar, a cgroup leaf with `memory.max` 256 MiB, key rotation (`bx app tunnel reset-keys`, which re-pairs every device).

Ship tunnelling off by default. Ship iroh-only last, and only after iOS confirms it routes web-view traffic (§15.3).

### 12. Compatibility (`docs/compat.md`)

- **Rule 1.** No boot migration. `data/app-tunnel/` appears only on enable. Users missing `deviceLimit` read as 1. **⚑** Seeding a grandfathered default instead would be a migration and need a fixture test (rule 9). I advise against it.
- **Rule 2.** Every API change is additive (§6). Old shell plus new xbind: classic codes keep working. Under iroh-only, the old shell gets a 409 whose text names `/xbin/devices`. New shell plus old xbind: no `tunnel` in the response, so the classic flow runs, and the dynamic import is guarded.
- **Rules 3 and 4.** New `/vendor/bx-pair.js` and `/xbin/devices`; new siblings are imported relatively; nothing is renamed.
- **Rule 6.** New `bx` subcommands only.
- **Rule 10.** Additive, through a new spec file. Old apps lose access only when an admin turns on iroh-only, by design. That goes in the changelog and in `docs/app-tunnel.md`.
- **Downgrade.** An older xbind drops unknown `Device` fields and `deviceLimit`/`maxDevices` on its first `users.json` write, so tunnel pairings are lost and devices re-pair. `frame-gens.json`'s `t` is ignored. Document this.
- **⚑ The default limit of 1 on upgrade** blocks a user who already has a phone from adding a second device. Write a changelog entry and recommend a `docs/changes/` note ("raise it: `bx app limit 3`"). The owner decides whether to mark it **BREAKING**.

### 13. Threat model (summary for `docs/app-tunnel.md`)

- **Network or relay operator, including n0:** sees QUIC/TLS plus Noise, so only metadata (endpoint ids, timing). The n0 preset publishes the server's EndpointId, home relay and direct IPs to n0 DNS; a custom relay with `Minimal` avoids that.
- **Compromised sidecar:** holds the iroh key and can impersonate the endpoint, forge header claims, and cause DoS. It can't read or forge tunnel traffic, because the Noise key and handshakes stay in xbind. **⚑** On the host network it can reach LAN and cloud-metadata services; document this residual risk, with a seccomp socket-family allowlist as the P4 mitigation.
- **Leaked QR:** an attacker can pair, but only with the victim's SAS confirmation, and tunnel codes don't redeem over HTTP. The QR also leaks the relay token (bandwidth abuse) until it is rotated.
- **Stolen phone:** can open the tunnel, but a session still needs Face ID through the enclave key. Revocation drops the device from the allowlist, closes its conns, and ends its sessions.
- **Leaked bearer token:** useless off-tunnel when bound (§4).
- **Downgrade to HTTPS by an active attacker:** the app pins `rq` from the QR and the client config.
- **Stolen server keys (`keys.json`):** the attacker can impersonate xbind to devices and read their traffic. Tunnel-bound bearers still can't be replayed without the device's Noise key. Recovery is key rotation plus re-pairing everything.
- **Out of scope:** reverse phishing, where an attacker's QR pairs the victim's phone into the attacker's workspace. The app should show the workspace's identity before pairing.

### 14. Risks

- iOS must route WKWebView WebSockets and chrome pages through the tunnel (§15.3).
- IrohLib's docs show iroh 1.2 bindings, while the server targets 1.4.x. Verify cross-version wire compatibility and that IrohLib exposes `RelayConfig` auth tokens. Upstream says iroh-relay also accepts `?token=`.
- A fresh handshake per stream costs latency. HTTP/1.1 only, so the app must reuse streams with keep-alive.
- memfd exec can be blocked by `vm.memfd_noexec` or an LSM; there are fallback paths.
- `iroh`'s interface enumeration may want `/sys`, which the bare root lacks. Verify inside the sandbox.
- xbind grows by about 6–8 MB compressed.
- Releases now need a Rust docker build for both arches.
- The n0 public relays are hobby-scale.
- Sessions binding to a transport costs Face ID prompts when the app switches transports.

### 15. Owner decisions

1. The bearer stays (recommended) vs. the tunnel identity alone.
2. Tunnel sessions always bound, vs. bound only under iroh-only.
3. **Web-view traffic under iroh-only:** frame tokens and web-ticket cookies minted under tunnel sessions are tunnel-bound, strictly. That breaks tile WebSockets and chrome pages unless the app proxies them through the tunnel. Agree the `appWSOriginMeta` behaviour over the tunnel with the iOS plan.
4. Allow `/login/sso?app=1` under iroh-only and refuse only `/login/ticket` (§3.4).
5. Tunnel codes are tunnel-only, which breaks old apps scanning them, plus the "classic code" fallback.
6. Default limit 1 on upgrade, and whether that is BREAKING. Ranges: workspace default 1–32, override 0–32.
7. The device upgrade route (§3.8).
8. Staged relay changes and tokens through a "next" slot.
9. flynn/noise vs. a hand-rolled Noise implementation.
10. Accept the host-network risk, and whether P4 seccomp hardening is in v1.
11. `cargo deny` in `make release`.
12. Server key rotation in v1.

### 16. Verification

- **Unit tests** (`go test ./internal/apptunnel/... ./internal/auth/... ./internal/users/... ./internal/broker/... ./internal/server/... ./internal/sandbox/...`):
  - Noise handshakes and vectors (`UPDATE_VECTORS=1`), plus cacophony IK/IKpsk2;
  - rekey and close frames, truncation giving `ErrUnexpectedEOF`, SAS, header parse fuzzing;
  - the pairing state machine: expiry, abandoned, reject burns the code, a single claimant, the gen-bound confirmer;
  - the transport gate matrix (§4) × iroh-only, with derived credentials (frame, path ticket, asset);
  - ClientIP and throttle keys distinct per device; synthetic TLS gives Secure cookies and an https `EnrollOrigin`;
  - device limit with grandfathering and a concurrent `AddDevice` race;
  - config validation, token masking, and the supervisor with an in-process fake (restart backoff, re-push after ready, alerts).
- **Guards:** `make check`, covering `TestNoDirectExec`, size budgets, apicheck (routes vs. openapi vs. protocol.md), `TestConfigDoc`, the route-class tables, `js-check`, `theme-check`, `large-files`. `git status` must be clean after `make build`.
- **Integration** (`make integration`, suite `apptunnel`):
  - (a) `XBIN_IROH=bin/fakeiroh`: memfd + bare sandbox launch, pairing → SAS confirm over the API → IK → device login → a tunnel-bound bearer that is refused over HTTPS; iroh-only refusals; revoke closes the stream.
  - (b) the same with the real `bin/xbin-iroh` and `XBIN_IROH_RELAY=disabled`, using `xbin-iroh client` plus `devclient`; skipped with instructions when the helper is missing.
- **Sidecar:** `make sidecar-check`; `make build` produces a tagged xbind that starts the embedded sidecar (`/app-tunnel` shows `confined:true`).
- **Harness:** `hack/ui-harness/run.sh --shots apptunnel` and `--shots pairing`.
- **Manual:** a docker iroh-relay with LetsEncrypt, `shared_token` and metrics on 127.0.0.1, the selftest, a real phone; and token rotation end to end.

### 17. Documentation

- **New:** `docs/app-tunnel.md` (setup, relay self-hosting, security and threat model, what iroh-only does; cites D204, no `plans/` links since it's embedded), `native/spec/tunnel.md` and `tunnel-vectors.json`, `plans/app-tunnel.md`, D204 in `plans/DECISIONS.md`.
- **Updated:** `docs/protocol.md`, `docs/auth.md` §Device login, `docs/config.md` (generated), `docs/compat.md`, `docs/changelog.md` (plus an optional `docs/changes/` note), `docs/bx.md`, `docs/maintenance.md` (helpers group, embedded sidecar, harness passes), `docs/index.md`, and `native/spec/device-login.md` (the 409 limit text and a pointer to the tunnel spec).
- **Relay self-hosting section**, verified against `iroh-relay/src/main.rs`:
  - top-level keys `enable_quic_addr_discovery = true`, `metrics_bind_addr = "127.0.0.1:9090"` (metrics default to on and public), and `access.shared_token = ["cur", "next"]`, all placed before the `[tls]` table;
  - `[tls] cert_mode = "LetsEncrypt"`, `hostname`, `contact` (required for LetsEncrypt), `cert_dir`;
  - `[limits.client.rx] bytes_per_second / max_burst_bytes`;
  - ports TCP 80 and 443, UDP 7842;
  - systemd (`DynamicUser`, `CAP_NET_BIND_SERVICE`) or docker (verify the image name and tag);
  - token changes require a relay restart; patch promptly.

#### Critical Files for Implementation
- /home/magik6k/buxon/internal/server/devicelogin.go
- /home/magik6k/buxon/internal/auth/auth.go (with /home/magik6k/buxon/internal/auth/sessions.go)
- /home/magik6k/buxon/internal/sandbox/factory_linux.go (with /home/magik6k/buxon/internal/sandbox/init_linux.go)
- /home/magik6k/buxon/internal/boot/serve.go
- /home/magik6k/buxon/internal/users/devices.go


## Appendix B — iOS app (design pass)

## iOS implementation plan for iroh tunnels in the xbin app (D204)

**Recommendation:** use option (A), a per-workspace loopback forwarder, with these choices:
- The forwarder listens on `http://127.0.0.1:<port>`.
- Each workspace keeps the same port across launches (stored in its record).
- It listens only while the app is in the foreground.
- The real https origin stays the workspace's identity: signing, links, Handoff and display.

Two rules on the xbind side make it safe:
1. Tunnel requests never get `Secure`/`__Host-` cookies.
2. The tunnel ALPN serves nothing anonymous except the paired device's own sign-in steps, so another app that reaches the port gets nothing.

**Why not (B), the proxy option:** WKWebView cannot accept a pinned or self-signed certificate on WebSocket connections. And xbind normally runs plain HTTP behind someone else's TLS: `internal/boot/serve.go` only terminates TLS for ingress.

**Key point:** the transport refactor (Phase 1) is needed whichever way the spike goes, so it runs in parallel with Phase 0.

---

### 1. What I checked in the code, and corrections to the brief

**The request sites that must stop assuming the origin (all checked):**
- `WorkspaceModel` stores the concrete `AppTransport`:
  - the property at `/home/magik6k/buxon/native/ios/App/Model/WorkspaceModel.swift:18`;
  - the init at `:76-90`, which builds `WorkspaceAuth` with it;
  - `loadLayout` at `:210`, `agents` at `:131`, `chromeURL` at `:534-537`, `pageDestination` at `:501-508`;
  - `makeEvents` at `:296-300`, which uses `URLSessionEventSocket`.
- `WorkspaceAuth.origin` is `record.server` (`/home/magik6k/buxon/native/ios/Packages/XbinCore/Sources/XbinCore/Client/Auth.swift:148`). Every `send` goes `transport.send(…, to: origin)` (`:197`, `:206`, `:224`).
- `WorkspaceEvents.connect` builds its URL from `await auth.origin` (`/home/magik6k/buxon/native/ios/App/Model/WorkspaceEvents.swift:247`).
- `AgentSessionTransport.open` does the same (`/home/magik6k/buxon/native/ios/App/Model/AppTransport.swift:141-145`).
- `TileSchemeHandler` has three hard-wired spots in `/home/magik6k/buxon/native/ios/App/Tiles/TileSchemeHandler.swift`:
  - the URL at `:76`;
  - the redirect rule at `:86`;
  - `AppTransport.shared.stream` at `:89`.
- `TileUploader` (`/home/magik6k/buxon/native/ios/App/Tiles/TileAttach.swift:325-366`):
  - the upload is at **`:352`**, not :342;
  - it is built from `workspace.origin` at `:432`.
- The socket factories:
  - `TerminalController.swift:146-149` (`WebSocketTransport(origin:…)`);
  - `TileTerminal.swift:251-253`, whose URL is built at `:54` and whose URLSession is made at `:64`.
- `NativeTile.swift:334` already calls `workspace.transport.send`.
- `WebTileController` (`/home/magik6k/buxon/native/ios/App/Tiles/WebTileController.swift`):
  - allows chrome navigation only to `workspace.origin` (`:323-342`, `:363-373`);
  - reloads only after more than 14 minutes in the background (`:237-240`).

**The places that must keep the public origin** (all read `w.origin` today):
- Handoff (`/home/magik6k/buxon/native/ios/App/Model/Handoff.swift:19`);
- the switcher (`SwitcherOverlay.swift:108`);
- Settings' "Server" row (`InboxAndSettings.swift:90`);
- "Sign in again" (`RootView.swift:293`);
- `AddDevice.outcome` (`AddDeviceView.swift:105`).

So `WorkspaceModel.origin` keeps meaning the public origin, and a new transport origin is added next to it.

**The address split already exists.** `WorkspaceRecord` keeps `server` (where requests go) apart from `deviceOrigin`/`signedOrigin` (what device login signs). `WebTicket.destination` already moves a ticket URL from the signed origin onto the address the app uses (`/home/magik6k/buxon/native/ios/Packages/XbinCore/Sources/XbinCore/Client/WebHandoff.swift:64-104`; device-login.md §6, "the ticket is not bound to a host name"). The loopback origin plugs into this as one more "address the app talks to".

**The e2e suite already proves WebKit works on `http://127.0.0.1`.** The UI tests run the app against `http://127.0.0.1:9871`, including a chrome tile signed in through the web ticket's "Continue as" page (`/home/magik6k/buxon/native/AGENTS.md:911-955`).

**Corrections to the brief:**
- **`native/tools/app-check` runs in no CI job.** Nothing in `.github`, the `Makefile` or `native/ios/scripts` calls it. The tunnel vectors would gate nothing until it is wired in (Phase 7).
- **`website/privacy.html` is guarded by `hack/check-website.mjs`, not `store-check.py`.** That script pins a digest, `PRIVACY_TEXT_SHA256`, regenerated with `--privacy-digest`. `store-check.py` only checks that `ITSAppUsesNonExemptEncryption` is a boolean.
- **The `snapshots` job only draws renderer fixtures.** `XbinSnapshotTests` sources are `Packages/XbinRenderer/Tests/XbinRendererTests`. App screens get screenshots from UI-test galleries (`XbinOnboardingTests.test10Gallery`), so the new screens' "snapshots" are gallery shots.
- **`app-stubcheck/run.sh` only picks up top-level `App/Model/*.swift`.** A `Model/Tunnel/` folder would be silently skipped. Also, `/home/magik6k/buxon/native/tools/term-stubcheck/Stubs/AppStubs.swift` stubs `WorkspaceModel` and `WebSocketTransport(origin:…)` and must be updated too.
- **`AddDevice.link(for:origin:)` rebuilds the link unless it parses as `.enroll`** (`WebHandoff.swift:199-203`). With a new tunnel link case it would silently drop `t/k/p/r`.
- **The Swift API is `EndpointTicket.parse(str:)`**, per n0's Swift docs (JS uses `fromString`). Other Swift names: `Endpoint.bind(options: EndpointOptions(preset:alpns:…))`, `secretKey().toBytes()`, `connect(addr:alpn:)`, `openBi()`, `send().writeAll(buf:)`/`finish()`, `recv().readToEnd(sizeLimit:)`, `close()`. n0's docs also require **`-framework Network`** on iOS, or linking fails on `_nw_interface_get_index`. Confirm every name against the pinned release's generated `IrohLib.swift` in Phase 0.
- **Old apps really do ignore the extra QR parameters.** `DeepLink.init` reads only `u` and `c` (`DeepLink.swift:73-79`).
- **Preconditions on the Mac mini:**
  - AGENTS.md says the `dev` user "does not exist yet". The `mac-remote.sh` dev loop needs the owner to create it.
  - Agents may not sign anything. The real-device half of the spike must be run by the owner, or with a signing setup the owner provides.

---

### 2. The transport decision

#### 2.1 Answers to the questions about (A)

**Can other apps reach the loopback port?**
- Yes. Loopback TCP is not sandboxed on iOS and not covered by Local Network privacy.
- While xbin is in the foreground, apps with background modes can connect. So can Safari next to xbin on an iPad (Split View, Stage Manager).
- The forwarder adds no credential, so the exposure is "anonymous reach to xbind as device D's tunnel".
- That matters for iroh-only workspaces, which may not be reachable at all otherwise. So xbind must restrict the `xbin/tunnel/1` surface. Requests on a device's tunnel are served only when they carry that same device's credential:
  - a bearer token, a device cookie session, or a frame token minted from that device's session;
  - plus the anonymous steps of that device's own sign-in: the challenge and login for its `deviceId`, `GET /login?ticket=`, `POST /login/web-ticket`, and `/vendor/*`.
  - Everything else gets 403.
- This also keeps invariant 2 of plans/native.md §2: the tunnel only ever restricts, it never grants.

**A per-launch secret Host such as `<random>.localhost`?**
- It is defense in depth only. Tile pages need the forwarder's address anyway: it arrives in their `xbin-ws-origin` meta, which xbind fills from `r.Host` (`internal/server/native.go:142-156`). So the secret only keeps out other apps and Safari.
- Evidence so far:
  - macOS 26 resolves `*.localhost` to loopback (WebKit bug 160504, confirmed 2025-09). iOS 26 is unverified.
  - On macOS, WebKit sends `foo.localhost` to a configured system proxy, because the exception list matches exactly. That would break users behind a Wi-Fi or MDM proxy.
- So **the default is `127.0.0.1`**. `*.localhost` stays an optional hardening, adopted only if spike check B7 passes, including with a proxy and a VPN on.
- If adopted, the forwarder checks only the first request's `Host` on each TCP connection, then splices; anything else gets `400` and close.

**Is `http://127.0.0.1` a secure context in WKWebView?**
- WebKit treats 127.0.0.0/8, `localhost` and `*.localhost` as potentially trustworthy, so `isSecureContext` should be true (WebKit bug 281149).
- The spike confirms it (B1). The web shell must not depend on it otherwise.

**`Secure` and `__Host-` cookies:**
- WebKit only merged "Secure cookies for loopback hostnames" in April 2026 (PR 61338). It depends on a CFNetwork change (rdar://168179921), so iOS 26 will most likely drop such cookies. The deployment target is 26.0 (`project.yml`).
- xbind's `SecureRequest` already returns false for Host `127.0.0.1` but true for `*.localhost` (`internal/auth/tilebinding.go:66-76`).
- Rule: **on tunnel connections xbind never sets `Secure` or `__Host-` cookies.**
- That is today's behaviour for 127.0.0.1, and the e2e chrome-tile path already works with it.

**What xbind must change (the iOS side depends on each):**
1. Tunnel connections arrive with Host `127.0.0.1:<port>`.
   - `xbin-ws-origin` echoes it, which is wanted.
   - `webConfirmSameOrigin` compares the Origin host with `r.Host`, so it works unchanged (`webticket.go:304-322`).
   - WebSocket `CheckOrigin` already returns true.
2. `EnrollOrigin` on a tunnel request must be the device's recorded origin or `--external-url`, never `http://127.0.0.1:port`. Today it falls back to `scheme://r.Host` (`devicelogin.go:158-171`). This affects add-device codes minted over the tunnel.
3. Redirects on tunnel requests must be relative or Host-based. The app also rewrites absolute public-origin redirects (Phase 1).
4. Refusals must be **403** with a readable message and a new field `"tunnel": "required"`.
   - Not 401: old apps would loop re-signing.
   - Not 404: old apps read that as "device removed".
   - An old app then shows `SignInError.server` with the message (`Auth.swift:257-262`).
5. `GET /api/xbin/login/methods` gets an additive `"tunnel": {"enabled", "required"}`.
6. `POST /api/xbin/devices/enroll-code` returns the tunnel link when tunnelling is on.
7. A pending-pairings API: list, and confirm with the 6-digit code. Both the web dialog and the app's add-device screen use it.
8. `GET /api/xbin/tunnel` returns the current ticket, relay, relay token and server Noise key. It is how tickets and tokens get refreshed.
9. Recommended: `POST /api/xbin/devices/tunnel`, callable only with a device session. It upgrades an already-enrolled device to the tunnel, so turning tunnels on does not force everyone to re-pair.
10. A dev-only flag serving both ALPNs over plain TCP on loopback, so `app-live` can test Noise and pairing on Linux without iroh.

#### 2.2 Why not (B): `proxyConfigurations` with an in-app CONNECT proxy

- WebKit does TLS inside the CONNECT tunnel. So the sidecar must present a certificate for the real hostname that WebKit trusts.
- WKWebView never asks the navigation delegate about server trust for WebSocket connections, a long-standing WebKit bug. So a self-signed tunnel certificate cannot be pinned for the shell's or the tiles' own WebSockets.
- xbind's main listener is plain HTTP behind someone else's TLS; it only terminates TLS for ingress. The sidecar would need the operator's certificate and key. That is impossible for iroh-only home servers, which are the main use case.
- (B) would also lose xbind's ability to tell tunnel requests from public ones.
- Its one benefit, keeping the real origin, is already covered by the `server`/`deviceOrigin` split.

#### 2.3 Other options considered (C)

- **A custom scheme for chrome pages, with an app-managed cookie jar:** custom schemes cannot carry WebSockets, and the shell and xbind's pages need them.
- **`NEPacketTunnelProvider`:** it would replace the user's VPN (iOS has one VPN slot, and the owner's case is people on VPNs). It also brings the VPN permission prompt, a 50 MB extension memory limit and an App Review "VPN app" position.
- **Native HTTP and WebSocket directly over iroh streams for URLSession, bypassing loopback:** a large rewrite (streaming, SSE, `URLSessionWebSocketTask`) for no gain, and WebKit would still need loopback.

---

### 3. Phase 0: the spike (in parallel with Phase 1; about one week; nothing merged)

**What to build** (branch `spike/iroh-tunnel`; worker A for iOS, the xbind side supplies the server):
1. **Server stub on the Linux box:** a small Rust binary using `iroh` and `snow`.
   - ALPN `xbin/tunnel/1`, Noise IK responder.
   - Splices each stream to the e2e xbind at `127.0.0.1:9871`.
   - Plus `iroh-relay --dev` with an auth token on port 9873.
   - It is a throwaway stand-in for the sidecar.
2. **iOS: a separate app target `XbinTunnelSpike`** in a branch-only `project.yml` change, never merged. It contains:
   - `LoopbackForwarder` (NWListener plus NWConnection splice);
   - a first cut of the Noise code (the Phase 2 code);
   - IrohLib pinned `exactVersion`, with `- sdk: Network.framework`;
   - a test screen: one WKWebView on a persistent `WKWebsiteDataStore(forIdentifier:)`, one ephemeral URLSession, a WebSocket task, and timers/logging to a file pulled with `simctl` or Xcode.
3. **The same forwarder inside the real app** behind a Debug launch argument `-XbinTunnelTicket <ticket>`, once Phase 1 lands (step 0b). It runs the full e2e suite through the tunnel.

**What to measure and how it is judged** (a real iPhone on iOS 26.x, plus iOS 27 if available, and the Mac mini simulator):

| # | Check | Pass |
|---|---|---|
| A1 | NWListener on `127.0.0.1:0`, `requiredInterfaceType = .loopback`: no Local Network prompt | required |
| A2 | Another app, and Safari next to xbin on an iPad, `fetch`es the port while xbin is in front | record it (expected reachable, which justifies xbind's tunnel restriction) |
| A3 | Rebinding the stored port after 20 background/foreground cycles | at least 19 of 20, and the fallback path is exercised |
| B1 | `isSecureContext` and `crypto.subtle` on a page at `http://127.0.0.1:P` | record it |
| B2 | A non-`Secure` HttpOnly SameSite=Lax cookie persists and is sent on navigation, fetch and the WebSocket upgrade | required |
| B3 | `Secure` and `__Host-` cookies over http loopback | record it (expected to fail on 26; this confirms the xbind cookie rule) |
| B4 | A cookie set on port P is sent to port Q; localStorage is per port | record it (decides how much a stable port matters) |
| B5 | A page loaded through the `xbin-ws://` scheme handler opens `ws://127.0.0.1:P`; it survives 30 minutes idle and a background/foreground cycle | required |
| B6 | The real web ticket, "Continue as", `POST /login/web-ticket` against the e2e xbind through the forwarder | required |
| B7 | `abc.localhost:P` resolves to the forwarder in both URLSession and WKWebView: plain, with a manual Wi-Fi proxy, and with Tailscale on | record it (decides the secret-host hardening) |
| C | URLSession through the forwarder: GET/POST, 10 minutes of SSE, `/ws/events`, `/ws/term`, a 64 MiB upload | required |
| D1 | Cold time to first byte (bind, connect, Noise, request), 20 runs on Wi-Fi and cellular, through n0's relay and through the self-hosted relay with a token | p95 under 2.5 s through a relay |
| D2 | Direct path on the same LAN, its RTT, and falling back to the relay when UDP is blocked | required |
| D3 | Tear down on background, recreate on `active` after 30 s and after 10 min (iroh #4289) | first request under 3 s, no crash, no stuck socket |
| D4 | When the Local Network prompt appears, for presetN0 and for Minimal with a custom relay; still works after "Don't Allow" | works over the relay when denied |
| D5 | 50 MB download through the forwarder, Noise and iroh: throughput, CPU, memory (Instruments `footprint`) | at least 5 MB/s on a LAN direct path; at most 25 MB extra resident memory |
| D6 | 64 concurrent long-lived streams (WebKit's 6 per host times pages, plus SSE and WebSockets) | no stalls; record the stream limits on both sides |
| D7 | What goes to n0: capture DNS and HTTP with `rvictl` and Wireshark on the Mac. Is there an iroh-ffi option to resolve without publishing? | record it (feeds the privacy answer) |
| E | Swift Noise interoperates with the stub's `snow`; extra latency per new TCP connection | it interoperates; about +1 RTT |
| F | Release build for `generic/platform=iOS`, `CODE_SIGNING_ALLOWED=NO`, with and without IrohLib: size of `Xbin.app/Xbin` and `zip -9` of the `.app`; `nm -u`; `otool -L` | numbers for the owner's budget |
| G | IrohLib types used from Swift 6 strict-concurrency code (wrapped in an actor) | it compiles |

**Decision rule:**
- (A) with `127.0.0.1` is confirmed if B2, B5, B6, C, D1 and D3 pass on both the device and the simulator.
- `*.localhost` is adopted on top only if B7 passes everywhere.
- If B2 or B6 failed, which is unlikely given the e2e result: inject the session cookie with `WKHTTPCookieStore.setCookie` after the ticket redemption, and re-test.
- If D3 failed: keep the endpoint and recreate only its socket, if iroh-ffi exposes that; otherwise escalate to n0 before building further.
- **Output:** a results table in the PR description, stating where each row was verified.

---

### 4. Implementation phases

#### Phase 1: the per-workspace transport (no iroh; can ship on its own)

**New file** `/home/magik6k/buxon/native/ios/App/Model/WorkspaceTransport.swift`:
- Foundation (plus FoundationNetworking) only, so app-stubcheck checks it.
- It holds the protocol below and `DirectTransport`, which wraps `AppTransport.shared` with a fixed origin.

```swift
/// How one workspace's requests reach its xbind (D204): its server, or its
/// tunnel's loopback forwarder.
protocol WorkspaceTransport: APITransport {
    /// Where requests physically go (synchronous: the tunnel's port is kept,
    /// so it is known before the listener is up). Direct: the record's server.
    var origin: ServerOrigin { get }
    /// Bumped when `origin` changed (a lost port): open pages reload.
    var generation: Int { get }
    /// Starts the tunnel if it is down and waits until requests can flow;
    /// direct: returns at once. Throws TunnelProblem.
    func ready() async throws -> ServerOrigin
    func stream(_ request: URLRequest, allowRedirect: @escaping @Sendable (URL) -> URL?, onResponse: …, onData: …, onDone: …) -> StreamingLoad
    func upload(_ request: URLRequest, from data: Data, delegate: (any URLSessionTaskDelegate)?) async throws -> (Data, URLResponse)
    /// Ephemeral, no cookies, no cache: the configuration every socket uses.
    func socketConfiguration() -> URLSessionConfiguration
}
```

- `send(_:to:)` must refuse any origin other than its own workspace's public origin. That way a bearer can never be sent elsewhere.
- `allowRedirect` now returns a URL: public-origin redirects are rewritten onto `origin`.

**Changes, at the sites checked in §1:**
- `WorkspaceModel.swift`:
  - `transport: any WorkspaceTransport` (`:18`); the init takes a transport, defaulting to one built from the record (`:76-82`);
  - `agents` (`:131`); `makeEvents` (`:296-300`) passes an `endpoint:` closure and the socket configuration;
  - `chromeURL` and `pageDestination` use `try await transport.ready()` as the address for `WebTicket.destination` / `XbindPageTicket.outcome`. Their existing signed-origin move does the rewrite.
  - Add `var transportOrigin: ServerOrigin { transport.origin }` and keep `origin` public.
- `WorkspaceEvents.swift`: add `init(auth:endpoint:makeSocket:…)` with a default `{ await auth.origin }`, so the app-check call sites (`EventsTests.swift:72`, `app-live/main.swift:245`) still compile. `connect` (`:247`) uses the endpoint. `URLSessionEventSocket` takes a configuration.
- `AppTransport.swift`:
  - `StreamingLoad` redirect handling returns the rewritten request;
  - `AgentSessionTransport` holds `any WorkspaceTransport` and uses `ready()` instead of `auth.origin` (`:119-145`);
  - `AppTransport.shared` stays for the kill switch (`RemoteConfig.swift:51`) and the push relay (`PushManager`, `LiveActivities`). These deliberately stay on HTTPS.
- `TileSchemeHandler.swift:76-89`: `ready()`, then `ws.transport.stream`.
- `TileAttach.swift`: `TileUploader(transport:tile:frameTokens:)`, `transport.upload` (`:352`), construction at `:432`.
- `TerminalController.swift:146-149` and `WebSocketTransport.swift:21-46`: origin from `ready()`, configuration from the transport.
- `TileTerminal.swift:41-64`: the same.
- `WebTileController.swift`:
  - policy at `:323-342` and `:363-373` against `transportOrigin`;
  - a chrome navigation to the public origin of a tunnelled workspace is cancelled and loaded on the transport origin (`WebTicket.moved`);
  - `willEnterForeground` (`:237`) also reloads when `transport.generation` changed.
- `Auth.swift`: `Enrollment.firstSignIn` accepts a transport, so pairing can sign in over the tunnel.

**Stubs:**
- `/home/magik6k/buxon/native/tools/app-stubcheck/Stubs/App/AppStubs.swift`: `AgentSessionTransport.transport`, `StreamingLoad`, and later the tunnel types.
- `/home/magik6k/buxon/native/tools/term-stubcheck/Stubs/AppStubs.swift`: `WorkspaceModel.transport`/`transportOrigin`, and the new `WebSocketTransport` init.

**Gate:** no behaviour change.
- `make swift-test`, `make swift-stubcheck`, the app-check tests;
- `mac-remote.sh e2e` (the whole suite) is green;
- ios.yml `app` and `duo` are green.

#### Phase 2: the tunnel protocol in Swift (Linux-tested)

**`/home/magik6k/buxon/native/ios/Packages/XbinCore/Sources/XbinCore/Client/Tunnel.swift`** (new; Foundation only):
- `TunnelLink`: parse and build `xbin://enroll?u&c&t&k&p&r`. If `t`, `k` and `p` are all present the link is a tunnel link. If any one is malformed, the link is refused; it never silently falls back to direct enrollment.
- `TunnelFrame`: a 2-byte big-endian length plus ciphertext, at most 65535 bytes.
- The rekey schedule, as a pure counter per Noise §11.3; the interval is fixed in the spec.
- `PairMessage` JSON codec:
  - the app sends `{op:"enroll", code, name, platform, publicKey, noiseKey, endpointId}`;
  - the server answers `{op:"waiting"}`, then either `{op:"enrolled", deviceId, user, origin, name, tunnel:{required, relay}}` or `{op:"error", status, error}`.
  - xbind must check that `noiseKey` and `endpointId` equal what the handshake and iroh's `remoteId` authenticated.
- `TunnelSAS.digits(_ okm8: Data) -> String`: big-endian u64 mod 10^6, zero-padded, shown as "123 456".
- `TunnelInfo` (the record type, §5).
- `TunnelStatus`: direct, connecting, a direct path with its RTT, a relay path with its host and RTT, or failed with a reason.
- `TunnelReconnectPolicy`: backoff, plus foreground/background state.
- `ByteStream`: a protocol with async `read`, `write` and `finish`.

Plus additive changes:
- `DeepLink.tunnelEnroll(TunnelLink)` and `SignInLink.tunnelEnroll`.
- `LoginMethods.tunnel`; `SignInOptions` shows pairing only when `required`.
- `SignInError.tunnelRequired` (with `needsEnrollment == true`).
- `APIError.tunnel`.
- `AddDevice.link` accepts tunnel links.
- `ConnectProblem` gains `tunnelUnreachable`, `pairingDeclined`, `pairingExpired` and `tunnelRequired`.

**`/home/magik6k/buxon/native/ios/Shared/NoiseTunnel.swift`** (new):
- Uses `#if canImport(CryptoKit) import CryptoKit #else import Crypto`, exactly like `Shared/PushCrypto.swift`.
- `NoiseHandshake` for IK and IKpsk2, initiator and responder. The responder exists for tests and vectors. The ephemeral key can be injected, so vectors reproduce.
- `NoiseCipherState`: ChaChaPoly with nonce = 4 zero bytes ‖ LE64 counter; AD = h; `rekey()` encrypts at the maximum nonce, per §11.3.
- HKDF from Noise is done with `HMAC<SHA256>` (written out, not `HKDF.deriveKey`, so the code reads like the spec).
- DH outputs that are all zero are refused, matching `PushCrypto.derive`. xbind must do the same.
- Prologue: `"xbin-tunnel-v1"` ‖ the server EndpointId (32 bytes) ‖ the device EndpointId (32 bytes).
- `TunnelSAS.derive(h)` = `HKDF-SHA256(ikm: h, salt: empty, info: "xbin-sas-v1", L: 8)`, passed to `digits`.
- `TunnelSplice`: pumps two `ByteStream`s through a Noise session. A TCP end-of-stream calls `finish()`; a stream end-of-stream half-closes TCP.
- `PairingClient`: the IKpsk2 state machine over a `ByteStream`.

**`/home/magik6k/buxon/native/spec/tunnel-vectors.json`:**
- Generated by the xbind side behind `UPDATE_VECTORS=1`, like `internal/push/seal_test.go:109-118`.
- Per vector: every private and public key, ephemerals, psk, both EndpointIds, msg1 and msg2 in hex, h, the SAS, and N transport frames each way.
- A rekey case.
- Invalid cases: wrong psk, wrong prologue, a flipped byte, a low-order point.

**Tests:**
- `/home/magik6k/buxon/native/tools/app-check/Tests/AppCheckTests/TunnelTests.swift`, with `NoiseTunnel.swift` symlinked as `PushCrypto.swift` is: the vectors; initiator against responder in memory; the splice with in-memory streams, including 1 MiB transfers and half-close; `PairingClient` against a fake responder.
- XbinCore tests: `DeepLinkTests` (the tunnel link, and an old-style link still producing `.enroll`), `OnboardingTests`, `WorkspaceRecordTests` (`tunnel` round-trips; an older record decodes with `nil`), `ClientHandoffTests` (`AddDevice.link` keeps `t/k/p/r`).
- Optionally, `app-live --tunnel`, using xbind's dev plain-TCP ALPN listener: pairing, SAS, device login and the web ticket against a real xbind on Linux.

#### Phase 3: the iroh runtime (Apple-only)

**`project.yml`:**
- `packages: IrohLib: url: https://github.com/n0-computer/iroh-ffi, exactVersion: <1.2.x>`, with a comment like SwiftTerm's;
- `Xbin` dependencies: `- package: IrohLib`, `- sdk: Network.framework`, plus `- sdk: SystemConfiguration.framework` if linking needs it;
- not linked into the notification extension or the widgets.

**New files**, top-level in `App/Model` and skipped by `app-stubcheck/run.sh` the way `AppTransport.swift` is:
- `TunnelEndpoint.swift`: an actor around IrohLib, keeping its non-Sendable objects inside.
  - `bind(secretKey:, relay: n0 | custom(url, token))` with no ALPNs (the app only dials);
  - `connect(ticket, alpn)`, checking `remoteId` equals the ticket's id;
  - `openBi() -> ByteStream`;
  - path type and RTT for `TunnelStatus` (the spike finds the FFI call; if there is none, ping over a stream);
  - `close()`.
- `LoopbackForwarder.swift`:
  - `NWListener` on `127.0.0.1:<stored port>`, loopback only; any peer that is not 127.0.0.1 is refused;
  - on each accept: open a stream, run the IK handshake (the server key from the Keychain, the device's static key), then `TunnelSplice`;
  - if nothing has been forwarded when the tunnel fails, it writes a canned `HTTP/1.1 502` with `Connection: close`, so WebKit shows a page;
  - if the port is taken: bind port 0, store the new port, bump `generation`.
- `TunnelTransport.swift`: implements `WorkspaceTransport`. `origin` is `http://127.0.0.1:<port>`, `ready()` starts the session, and it uses `AppTransport.shared.session`, since loopback needs no special session.
- `TunnelManager.swift` (main actor):
  - one session per tunnelled workspace, started lazily on the first `ready()`;
  - `suspendAll()`, called from `AppModel.enteredBackground` (`AppModel.swift:333`) inside `beginBackgroundTask`: cancel the listener and spliced connections, then `conn.close()` and `endpoint.close()`. An endpoint is never reused after a suspend (iroh #4289).
  - Pre-warming for the workspaces on screen in `becameActive` (`:307`), before `refresh()`;
  - reconnection with backoff while active, plus `NWPathMonitor`;
  - after a successful connect, it refreshes the ticket, relay and token from `GET /api/xbin/tunnel`.
- `TunnelKeyStore.swift`: the Keychain item described in §5, plus garbage collection of entries whose workspace record no longer exists, run at launch.

**`WorkspaceModel.forget`** (`:550-573`): stop the tunnel and delete the Keychain item.

#### Phase 4: pairing

**Flow:**
1. `OnboardingFlow.use(link:)` (`Onboarding.swift:109-125`) and `SceneModel.open(link:)` (`SceneModel.swift:84-97`) route a tunnel link to a new `AddRequest.tunnelEnroll` (`AppModel.swift:15-31`).
2. The question at `Onboarding.swift:267-277` gets a tunnel variant, "Pair with \<host\>?". It says the device connects through an encrypted tunnel and that iOS may ask about the local network.
3. A new `OnboardingStep.pairing(TunnelLink)` opens `PairingPage`, in new file `/home/magik6k/buxon/native/ios/App/Shell/Pairing.swift`.
4. `PairingFlow`, in `/home/magik6k/buxon/native/ios/App/Model/TunnelPairing.swift`:
   - creates the enclave key (`EnclaveKeyStore.createKey`), the iroh secret and the Noise static key under the new workspace id, and deletes all three on any failure, as `Enrollment.enroll` does;
   - binds, connects to `xbin/pair/1`, runs IKpsk2, and shows the SAS: "Confirm on your computer: 123 456 — Waiting…" (accessibility id `pairing-sas`);
   - sends `enroll` and waits up to 5 minutes;
   - on `enrolled`: builds the record (`server = u`, `deviceOrigin = origin`, `tunnel = …`), keeps the endpoint, opens `xbin/tunnel/1`, and builds a `WorkspaceModel` with that `TunnelTransport`;
   - runs `auth.signIn()` (device login over the tunnel; one Face ID prompt), then `AppModel.add(_:session:in:replacing:)`.
5. Failures map to `ConnectProblem`: code refused, declined on the computer, expired, unreachable (with relay or direct detail), device limit (409).

**Auto-upgrade, if the owner agrees to xbind route 9:** a device enrolled directly upgrades to the tunnel without re-pairing when `whoami` or login methods report `tunnel.enabled`. This uses the device session, which is backed by Face ID.

#### Phase 5: the rest of the user-facing changes

- **Settings** (`InboxAndSettings.swift:88-107`):
  - a "Connection" row: "Tunnel · direct · 18 ms", "Tunnel · relay (relay.example.com) · 85 ms", "Tunnel · connecting…" or "Direct (https)";
  - it links to new `/home/magik6k/buxon/native/ios/App/Shell/ConnectionView.swift`: the path, the relay, "Encrypted: xbin tunnel (Noise) inside iroh (QUIC)", "Required by the workspace", short ids of this device and the server, and a Reconnect button.
- **Encryption note:** add `WorkspaceRecord.isEncryptedInTransit` (true if `tunnel != nil`, otherwise `server.isEncryptedInTransit`) and use it on the Connection screen. The sign-in page's http warning (`SignInPages.swift:107`) stays as it is; it is about the direct path.
- **Workspaces that refuse non-tunnel connections:**
  - `MethodsPage` shows only: "This workspace only accepts paired devices over its tunnel — scan a pairing code" (settings → add a device on a computer), with no password or SSO button, because SSO cannot use the tunnel.
  - `SignInProblemBar` (`RootView.swift:281-303`) offers "Pair again" for `.tunnelRequired`, opening Log in rather than the address page.
- **Add another device from the app** (`AddDeviceView.swift`):
  - Yes, it needs the larger QR. xbind mints it over the tunnel; the app just renders the link it gets back (after the `AddDevice.link` fix).
  - Use `correctionLevel "L"` or a bigger frame for links over about 300 characters (`:155`).
  - **The app must also be the confirming side:** after showing the QR it polls the pending pairings. When the new device appears it shows "\<name\> is pairing — type the 6 digits it shows", and posts the confirmation.
  - The footer warns that the link (and its relay token) is a credential until it expires.
- **Device limit:** a 409 at pairing or minting shows "This account can have N app(s) connected — remove one under Devices or ask an admin". The Devices section footer shows the limit if xbind adds a `limit` field to `GET /api/xbin/devices`.
- **Copy:** `AboutPage` page 3 (`Onboarding.swift:529-535`) and `NSLocalNetworkUsageDescription` mention tunnels and relays.

#### Phase 6: App Store, privacy and export compliance (detail in §9)

- `PrivacyInfo.xcprivacy`: the required-reason categories that `nm -u` finds in the linked binary. Expect `NSPrivacyAccessedAPICategorySystemBootTime` / `35F9.1` if `_mach_absolute_time` appears, and `DiskSpace` only if `statfs`-type symbols do. Update the comment block about relays.
- `store-check.py`:
  - if `project.yml` links IrohLib, require those categories and `ITSAppUsesNonExemptEncryption == true`;
  - the export rule is enabled only once the owner has answered.
- `website/privacy.html` relay paragraph, plus `--privacy-digest` to update `PRIVACY_TEXT_SHA256`.
- The privacy table and the export paragraph in `native/AGENTS.md`.

#### Phase 7: end-to-end tests and CI

- **`e2e-xbind.sh --tunnel`:** enables the sidecar, runs `iroh-relay --dev` with a token on 9873, and sets the workspace's custom relay through the API.
- **`mac-remote.sh e2e`:** adds `-R 127.0.0.1:9873:127.0.0.1:9873`. The simulator then reaches the relay through ssh, so tests are deterministic over the relay path; direct paths are covered only in the device spike.
- **New `/home/magik6k/buxon/native/ios/UITests/XbinTunnelTests.swift`** (skips without `XBIN_E2E_TUNNEL`):
  1. `launchFresh`;
  2. mint the link through `E2EServer.send("POST", "/api/xbin/devices/enroll-code")`, which returns the tunnel link;
  3. paste it on Log in (the simulator has no camera);
  4. tap Pair; read `pairing-sas`; confirm through the pairings API;
  5. Home loads; `apps/welcome` (scheme handler); the native counter +1; a terminal (WebSocket through the forwarder); chrome tile `tiles/organisations` ("Continue as" on 127.0.0.1);
  6. Settings shows "Tunnel · relay";
  7. press Home, re-activate, the terminal reconnects;
  8. the admin turns on refusal of non-tunnel connections: the tunnelled workspace keeps working, and a direct sign-in shows the pairing-only page;
  9. a `testGallery` takes light and dark shots.
- **Deterministic shots:** a Debug-only `-XbinPairingDemo <state>` renders each pairing state (connecting, sas, declined, unreachable, expired) without a server.
- Run `uitest-stubcheck` after editing the tests.
- **CI:**
  - add a `make app-check` target (`cd native/tools/app-check && swift build --build-tests && ulimit -s 512 && swift test --skip-build`) to ci.yml's `native` job;
  - `native-stubcheck` covers the new Shell files automatically and the tunnel types through stubs;
  - ios.yml `packages` runs the new XbinCore tests;
  - `app` and `duo` link IrohLib. The roughly 40 MB download lands in the SourcePackages cache, which is already keyed on `project.yml`.
  - `snapshots` is unchanged.

#### Phase 8: documentation

- Write `native/spec/tunnel.md` jointly with the xbind side: link format, ALPNs, Noise patterns, prologue, framing, rekey, SAS, pairing messages, refusals.
- `native/spec/device-login.md`: §2 (the tunnel QR) and §6 (the transport address).
- plans/native.md §5 and §24.
- `native/AGENTS.md`: app-check list, stubcheck skip list, `e2e --tunnel`.

---

### 5. Persistence and downgrade

**`WorkspaceRecord`** (`WorkspaceRecord.swift`): add `public var tunnel: TunnelInfo?`, with `tunnel` in the explicit `CodingKeys` (`:168-170`), `decodeIfPresent` and `encodeIfPresent`.

```swift
public struct TunnelInfo: Sendable, Hashable, Codable {   // explicit keys: endpoint, relay, required, port
    public var endpoint: String      // xbind's iroh EndpointId
    public var relay: String?        // custom relay URL; nil = n0's default relays
    public var required: Bool        // the workspace refuses this device off the tunnel
    public var port: Int?            // the forwarder's port, kept across launches (origins of pages)
}
```

**One Keychain item per workspace**, written with `Keychain.write`, service `dev.xbin.tunnel`, account = workspace id, `whenUnlockedThisDevice`, no access group (the extensions do not need it):
- JSON `{v:1, irohSecret, noiseStatic, serverNoiseKey, ticket, relayToken?, endpoint, relay?, required}`;
- the ticket is stored here because it contains the server's IP addresses.
- One item rather than five gives atomic writes and lets a lost record field be rebuilt.

**Downgrade behaviour:**
- An old app decodes the record and ignores `tunnel`. On its next save it drops the field: `WorkspaceList` re-encodes known fields only, and `unreadable` keeps only records that failed to decode.
- The old app then talks to `server` directly:
  - in a workspace that allows direct connections, it works;
  - in a workspace that refuses them, xbind's 403 message tells the person to update.
- When the new app returns, it **restores `record.tunnel` from the Keychain item at load** (the Keychain is the source of truth), so nothing is lost.
- Keep `WorkspaceList.currentVersion` at 1. Raising it would make old apps refuse the whole list (`:228`).
- On iOS, real downgrades only happen to TestFlight users and to reinstalls (Keychain items survive deleting the app). The garbage collection in Phase 3 cleans up after reinstalls.

---

### 6. Work split (two iOS workers)

**Mac mini setup:** each worker uses `XBIN_MAC=dev@mini` with their own `XBIN_MAC_DIR=xbin-remote-<name>`, `XBIN_SIM_ENSURE=xbin-e2e-<name>`, `--port`, `XBIN_MAC_PULL` and `XBIN_E2E_DIR` (AGENTS.md, "Sharing the Mac"). This needs the `dev` user to exist first.

**Worker A — transport and WebKit**
1. The iOS half of Phase 0, including the spike target and step 0b in the real app.
2. Phase 1: the refactor, both stubchecks, a full e2e regression run.
3. Phase 3: forwarder, endpoint, `TunnelTransport`, `TunnelManager`, the lifecycle.
4. WebKit origin rewriting and reload on a port change.
5. The Connection row and screen.
6. Size, `nm -u` and Instruments measurements.

**Worker B — crypto, pairing and model**
1. Phase 2: the XbinCore tunnel types, `Shared/NoiseTunnel.swift`, the app-check tests, the vector consumer, and the `make app-check` CI wiring.
2. Phase 4: pairing (`TunnelPairing.swift`, `Pairing.swift`, `AddRequest`, the onboarding changes).
3. Phase 5: refusals, the pairing-only methods page, add-device as the confirming side, the device-limit messages.
4. `TunnelKeyStore`, the record and its restore logic.
5. Phase 6 file changes, prepared for the owner's answers.
6. Phase 7 tests and gallery.

**Order:**
- Phase 0 and Phase 1 run in parallel with Phase 2.
- Phase 3 needs Phase 1, the spike's go-ahead and the pinned IrohLib.
- Phase 4 needs Phases 2 and 3, and xbind's pairing endpoints.
- Phase 7 needs the e2e xbind with the sidecar.
- Each worker runs `make swift-test`, `make swift-stubcheck` and `ci-local-check.sh` after merging the other's branch (AGENTS.md, "A clean merge proves nothing").

---

### 7. Risks

- **Other apps reaching the forwarder** (Safari next to xbin on an iPad, apps with background modes): mitigated only by xbind's tunnel restriction (§2.1). Must have a Go test; the secret host is optional on top.
- **Cookie behaviour changing between iOS 26 and 27** (the 2026 CFNetwork change): xbind must never send `Secure`/`__Host-` on tunnel connections; test on both versions.
- **A lost port reloads pages and loses chrome localStorage:** the port is stored and rebound; `generation` drives reloads.
- **iroh #4289** (a broken UDP socket after a suspend): never reuse an endpoint across a suspend. **#3474** (the Local Network prompt): explain it before it appears; works over the relay if denied.
- **No `UIBackgroundModes`:** uploads and streams die when the app is backgrounded (the Live Activity and push keep going through the relay). A `beginBackgroundTask` grace period for `TileUploader` is optional.
- **QUIC stream limits** with many long-lived SSE and WebSocket requests: spike check D6; xbind should allow at least 256 concurrent streams.
- **IrohLib packaging:**
  - uniffi classes are not Sendable (wrap them in an actor);
  - `-framework Network` is needed;
  - an Xcode preview link problem with `SwiftUICore` that n0 reports for Xcode 16;
  - API changes between versions (pin `exactVersion`, review every bump).
- **Size:** if it is over budget, fall back to building iroh-ffi ourselves with size optimisation (`opt-level="z"`, LTO, `panic=abort`, stripped) from a pinned commit.
- **Stale tickets, relay token rotation, server Noise key rotation:** refresh after every connect; xbind keeps accepting the old token or key during a rotation window.
- **A device limit of 1 by default** breaks "Sign in again" (`AppModel.add(replacing:)` deletes the old device only after the new enrollment) and adding from one app to another. Needs an enroll `replaces` field or auto-upgrade (owner decision).
- **Several tunnelled workspaces mean several endpoints** (sockets, memory, battery): start lazily and measure.
- **App Review:** not a VPN (no NetworkExtension), so low risk. Delta Chat reportedly ships iroh in its App Store app; worth confirming as precedent.
- **Export documentation could delay the first release** that includes tunnels.
- **e2e flakiness** from running the relay over ssh `-R`.

---

### 8. How to verify

**On Linux:**
- `make swift-test`: XbinCore tunnel and link tests.
- `cd native/tools/app-check && swift test`: Noise vectors, splice, pairing state machine.
- `make swift-stubcheck`.
- `python3 native/tools/store-check.py`.
- `node hack/check-website.mjs`.
- `CI_LOCAL_BASH32=1 XCODEGEN=… native/ios/scripts/ci-local-check.sh`: `project.yml` generates with IrohLib, and the scripts' dry tests pass.

**On the Mac mini:**
- `mac-remote.sh build`, `packages`, `e2e` (the whole suite after Phase 1; `--only XbinUITests/XbinTunnelTests` after Phase 7).
- Look at the pulled gallery PNGs.

**On CI:** ios.yml `packages`, `app`, `duo`, `snapshots`; ci.yml `native` (with `app-check`) and `native-stubcheck`.

**On a device (owner-run):**
- the spike table;
- the final build passes D1, D3, D4, D5;
- 20 background/foreground cycles;
- Local Network denied, still works over the relay;
- the iroh-only refusal on https;
- adding a device from one app to another, with the typed confirmation code;
- Instruments memory;
- `nm -u` and the size report attached to the PR.

**Reporting:** each PR says where every item was verified ("passes swift test on Linux", "compiles on CI", "not run on a device").

---

### 9. Decisions for the owner

1. **Export compliance (the account holder's legal declaration):**
   - The app will ship non-Apple cryptography: rustls/QUIC inside IrohLib, and a Noise protocol of our own for confidentiality. The current justification (`native/AGENTS.md:846-852`) stops being true.
   - Recommendation: `ITSAppUsesNonExemptEncryption = true`.
   - In App Store Connect, answer: "standard encryption algorithms in addition to Apple's", none proprietary.
   - Classify as mass market (EAR 5A992.c/5D992.c, License Exception ENC §740.17(b)(1)), with the yearly self-classification report to BIS and NSA by February 1. Or, if counsel agrees, use the publicly-available-source route, since the app and iroh are open source.
   - Answer the France question to match where the app is sold.
   - Once Apple accepts the documentation, set `ITSEncryptionExportComplianceCode`.
   - Have counsel confirm all of this.
2. **App size budget:** I suggest accepting at most about 15 MB of extra download size, measured in the spike. Above that, build iroh-ffi ourselves with size optimisation.
3. **Shipping before the spike settles WebKit:** I recommend not to. Tunnels stay TestFlight-only until the device half of the spike passes. The spike is short, and Phase 1 ships independently.
4. **n0's public relays as the default:**
   - Accept that n0 sees the device's IP address and the endpoint ids.
   - Choose whether the phone may use n0's DNS (lookups only; it never publishes, if spike check D7 finds the option).
   - App Privacy stays "Data Not Collected" only if n0 keeps nothing beyond relaying in real time. Check n0's policy, or require self-hosted relays for privacy-sensitive workspaces.
5. **What "limit for connected apps" means:** enrolled devices or simultaneous connections? And how re-pairing works when at the limit: an enroll `replaces` field, or auto-upgrade.
6. **Auto-upgrading already-enrolled devices** to the tunnel through their device session (xbind route 9), versus requiring everyone to re-pair.
7. **When a tunnel is not required but unreachable:** I recommend no automatic fallback to direct; optionally a manual "connect directly" switch.
8. **The confirmation code:** the web side asks the person to **type** the 6 digits the phone shows (I recommend this), versus showing both and asking for a click.
9. **Secret-host hardening (`*.localhost`):** adopt it only if spike check B7 passes everywhere.

---

#### Critical files for implementation
- /home/magik6k/buxon/native/ios/App/Model/WorkspaceModel.swift
- /home/magik6k/buxon/native/ios/App/Model/AppTransport.swift
- /home/magik6k/buxon/native/ios/Packages/XbinCore/Sources/XbinCore/WorkspaceRecord.swift
- /home/magik6k/buxon/native/ios/Shared/PushCrypto.swift (the pattern for the new /home/magik6k/buxon/native/ios/Shared/NoiseTunnel.swift)
- /home/magik6k/buxon/native/ios/App/Shell/Onboarding.swift


## Appendix C — iroh research (2026-10-27)

### 1. Iroh's state (October 2026)

- **1.0 shipped on 2026-06-15.** The latest release is **v1.4.1** (crates published 2026-10-21, binaries 2026-10-22). The 1.x line so far: 1.0.1 (06-29), 1.0.2 (07-06, an iroh-relay crash fix), 1.0.3 (07-20), 1.1.0 (08-25), 1.2.0 (09-09), 1.3.0 (09-28) and 1.4.0 (10-20, rustls RUSTSEC-2026-0346 through 0349). Sources: https://iroh.computer/blog/v1, https://github.com/n0-computer/iroh/releases, CHANGELOG.md.
- **Stability.** In n0's words: "Iroh version 1.0 asserts stability for both the wire protocol and language APIs." Any v1 endpoint can talk to any other v1 endpoint, regardless of minor version or language binding. Wire-breaking changes will only come with a major release, and 2.x will interoperate with 1.x.
  - Support windows (https://docs.iroh.computer/about/release-policy.md): a major version gets 1 year of full support and maintenance until year 3; a minor version gets 3 months of full support and maintenance until 1 year.
  - Public-relay end of life: 0.9x and rc versions on 2026-09-30, 0.35 on 2026-12-31.
  - Caveat: 1.1.0 still shipped a "[breaking]" CustomAddr serialization fix. A Reddit thread called the 1.0 release rushed and said regressions from 0.95 remain.
- **Identity.** An `EndpointId` is a 32-byte Ed25519 public key. Its `Display` is 64 lowercase hex characters; parsing accepts hex or base32, and z32 is used for pkarr (source: `iroh-base/src/key.rs`). The `NodeId`/`NodeTicket`/`node…` names are from before 1.0 and were renamed to Endpoint* in the late 0.9x releases. The exact version of the rename is from memory, not verified.
- **Tickets** (`iroh-tickets` 1.0.0, https://github.com/n0-computer/iroh-tickets, `src/endpoint.rs`):
  - String form: `"endpoint"` + lowercase base32 without padding.
  - Payload: postcard encoding of `TicketWireFormat::Variant1{ addr: { id, info: { addrs: BTreeSet<TransportAddr> } } }`, where `TransportAddr` is `Relay(RelayUrl) | Ip(SocketAddr) | Custom`.
  - Tickets contain your current IPs, can be reused and are not secret (https://docs.iroh.computer/concepts/tickets.md).
- **ALPNs** are arbitrary byte strings. An endpoint advertises a list of them, and the Router or your own accept loop dispatches on `conn.alpn()`. Examples: dumbpipe uses `b"DUMBPIPEV0"`; iroh-blobs uses `/iroh-bytes/<ver>`. Since 1.0.3 an empty ALPN is an error.

### 2. Go support

| Option | Status | Works with CGO_ENABLED=0? |
|---|---|---|
| iroh-ffi (n0) | Official bindings for Python, Swift, Kotlin and JS only. The README lists Go only as "community maintained". | — |
| `git.coopcloud.tech/decentral1se/iroh-go` (linked from https://docs.iroh.computer/languages/go.md) | Wraps iroh-ffi through uniffi-bindgen-go and embeds a static `libiroh`. "Highly Experimental", Linux musl x86_64/aarch64 only, no tags, iroh 1.0.3 / iroh-ffi 1.1.0, last commit 2026-10-24. | **No.** It requires `CGO_ENABLED=1`. |
| `iroh-c-ffi` (n0, safer-ffi) | Raw C API. Linux x86_64 glibc, macOS arm64 and Windows only. | No (it is C). |
| **`github.com/tmc/go-iroh`** | Clean-room pure-Go port that aims for wire compatibility with iroh v1. v0.3.0 (2026-10-01), 61 stars, MIT. Almost all commits are by one author (675 of ~693). Forks quic-go v0.59.1 (`internal/qng`) and carries its own RFC 7250 TLS. Includes `relayserver` and `cmd/iroh-relay`. COMPATIBILITY.md records interop passes against Rust 1.2.0 and 1.3.0. The Go API is not stable before v1, and nothing says it has been audited. | **Yes.** CI has a "Build without cgo" step. |
| **dumbpipe** (https://github.com/n0-computer/dumbpipe) | v0.40.1 (2026-10-21), depends on iroh 1.0 with `tls-ring`. Prebuilt binaries for linux, darwin and windows are 2.5–3.4 MB compressed. Commands: `listen`/`connect` (stdio), `listen-tcp --host localhost:8000`, `connect-tcp --addr … <ticket>`, `listen-unix --socket-path …`, `connect-unix`, `generate-ticket`, plus `--custom-alpn`, `--ipv4-addr` and `--ipv6-addr`. The secret key comes from the `IROH_SECRET` env var. ALPN is `DUMBPIPEV0` with a 5-byte `"hello"` handshake. | Yes (separate binary). |
| sendme | File transfer over iroh-blobs. No forwarding. | — |

dumbpipe's limitations, checked in `src/main.rs`:
- It hardcodes `presets::N0`, so there is no custom-relay flag and no relay auth token.
- It does not check `remote_id()` against any allowlist; anyone with the ticket can connect.
- It accepts one bidirectional stream per connection.
- It does not pass the peer's EndpointId on to the local socket.

So it works as a prototype, or as a template to fork, but not as the product sidecar. I found no other maintained iroh-to-TCP forwarder. `pirohxy` (an iroh SOCKS proxy) was last updated in 2025.

### 3. iOS / Swift

- **IrohLib** is distributed through SwiftPM from https://github.com/n0-computer/iroh-ffi (also on CocoaPods as `IrohLib`). The latest release is **v1.2.0 (2026-10-22)** on iroh 1.x, and n0 is actively maintaining it.
  - `Package.swift` sets `.iOS("17.5")` as the minimum.
  - It uses a binaryTarget, `IrohLib.xcframework.zip`, which is **40.6 MB zipped** and covers iOS device, iOS simulator and macOS arm64. I could not establish how much it adds to an App Store download; you would need to measure that.
  - It needs Xcode 16 or later. Docs: https://docs.iroh.computer/languages/swift.md.
- **What the Swift API exposes** (checked in the Rust source): `preset_n0` / `preset_minimal` / `preset_n0_disable_relay`, `EndpointOptions{secretKey, alpns, relayMode, bindAddr, protocols}`, `RelayMap.fromUrls`/`insert(RelayConfig)`, `RelayMode.custom(map)`/`customFromUrls`, and **`RelayConfig.authToken`** (sent as a Bearer header).
  - Also: `connect(addr, alpn)`, `acceptNext`, `Connection.remoteId/openBi/acceptBi/close`, stream read/write/finish, and `EndpointTicket.fromString`.
  - It does **not** expose `export_keying_material`.
- **Apps using iroh on iOS:**
  - Delta Chat uses iroh for webxdc realtime channels (since 1.48, Nov 2024), and Delta Chat is on iOS.
  - Fedimint/Fedi appears in iroh issue #3474.
  - zuko has an iOS beta client.
  - None of these is an App Store app built specifically on IrohLib.
- **iOS caveats** (open issues):
  - **#4289**: on iOS the UDP socket "becomes permanently broken after the process is suspended and then resumed". If the rebind fails, the endpoint can silently die. Plan to tear down and recreate the endpoint on foreground.
  - **#3474**: iroh triggers the Local Network permission prompt even without mDNS, so add `NSLocalNetworkUsageDescription`.
  - **PR #4577** (open): no NAT64 support yet. On IPv6-only carriers, a direct path to an IPv4-only peer stays on the relay.
  - **#4424**: on CGNAT, the direct path drops back to the relay every 50–75 s.
  - iOS suspends background apps, so there are no persistent background tunnels. Reconnect on foreground, or use push notifications to wake the app.
- **Fallback:** build your own thin Rust `staticlib` for `aarch64-apple-ios` and `aarch64-apple-ios-sim` with a cbindgen C API, wrapped in your own XCFramework. iroh-c-ffi does not list iOS.

### 4. Self-hosted relay (`iroh-relay` 1.4.1)

- **Getting it.** Prebuilt binaries are attached to iroh releases (`iroh-relay-v1.4.1-{x86_64,aarch64}-unknown-linux-{gnu,musl}`, about 4 MB), and there is a Docker image `n0computer/iroh-relay`. Run it with `iroh-relay --config-path config.toml`; `--dev` gives plain HTTP on port 3340. Full config reference: `iroh-relay/src/main.rs` (https://github.com/n0-computer/iroh/blob/main/iroh-relay/src/main.rs).
- **Config file:**
  - Top level: `http_bind_addr` (default `[::]:80`), `enable_relay`, `enable_quic_addr_discovery` (default false, needs TLS), `enable_metrics`/`metrics_bind_addr` (:9090 — do not expose this), `key_cache_capacity`, `[limits.client.rx] bytes_per_second / max_burst_bytes`.
  - `[tls]`: `https_bind_addr` (:443), `quic_bind_addr` (default UDP **7842**, from `DEFAULT_RELAY_QUIC_PORT`; the iroh-relay README says 7824, but the source constant is 7842), `hostname` (a string or a list), `cert_mode = "LetsEncrypt" | "Manual" | "Reloading"`, `cert_dir`, `manual_cert_path`/`manual_key_path`, `prod_tls`, `contact`.
  - ACME uses tokio-rustls-acme 0.9 with **TLS-ALPN-01**, so port 443 must be reachable. `IROH_RELAY_ACME_URL` and `IROH_RELAY_ACME_CA` override the ACME server.
  - STUN is gone (stun-rs was removed in 0.94); QUIC address discovery (QAD) replaced it.
- **Access control (`access`):**
  - `"everyone"` (default).
  - `access.allowlist = [<endpoint ids>]`.
  - `access.denylist = [...]`.
  - `access.http = { url = "...", bearer_token = "..." }`: the relay POSTs once per connecting endpoint with an `X-Iroh-Endpoint-Id` header containing the hex id (`X-Iroh-NodeId` was deprecated in 1.4.0). Your service grants access only by returning `200` with body `true`. `IROH_RELAY_HTTP_BEARER_TOKEN` overrides the bearer token.
  - `access.shared_token = ["tok-a", ...]` (added in 1.0.0): the client sends an `Authorization: Bearer` header, or `?token=` as a fallback. `IROH_RELAY_ACCESS_TOKEN` replaces the list with a single token. Revoking a token means editing the config and restarting.
  - The relay also verifies that the client holds the secret key for its id.
  - Per the source comment, access applies only to "endpoints… allowed to relay connections". A phone has to be allowed on the **server's home relay**, because that is the relay it dials through.
- **Client side (Rust):** `RelayMode::Custom(RelayMap::from_iter([RelayConfig::new(url, quic).with_auth_token(tok)]))` or `RelayMode::custom([urls])`. In Swift, use the IrohLib equivalents listed above. `RelayStatus::auth_denied_reason` was added in 1.2.0.
- **n0 public relays** (https://docs.iroh.computer/iroh-services/relays/public.md):
  - Free, but rate-limited with unpublished limits, no SLA, and "suitable for development and hobby use only". n0 can block abusers.
  - Paid options: Iroh Services Pro at $19/mo (shared authenticated relays, 5 MB/s, 100 GB) or Dedicated at $199/region (https://iroh.computer/blog/shared-relays).
  - Mixing: a `RelayMap` is just a list of URLs, so you can technically mix your relay with n0's. The docs neither cover nor endorse this, and each relay needs its own token.
- **Privacy:** the `N0` preset publishes pkarr records containing the endpoint id and **relay URL** to n0's DNS (https://iroh.computer/blog/blinded-address-lookup). With a private relay, use `presets::Minimal` (or self-host `iroh-dns-server`) and put the full address in the QR code instead.
- **Keep the relay patched:** 1.0.2 fixed a single-packet crash, and 1.4.0 fixed the rustls advisories.

### 5. Iroh's own security

- Iroh is QUIC over TLS 1.3 with **RFC 7250 raw public keys** using Ed25519, since 0.34. The old x509 path was removed in 0.90.
- Authentication is mutual. `Connection::remote_id()` "Returns the EndpointId from the peer's TLS certificate".
- Key exchange is ephemeral X25519, which gives **forward secrecy for 1-RTT data**. Hybrid post-quantum `X25519MLKEM768` is opt-in and needs the `tls-aws-lc-rs` feature (https://iroh.computer/blog/iroh-post-quantum-handshakes).
- Caveat: 0-RTT data, if you use it, has the usual TLS 1.3 problems: it is replayable and not forward-secret. In 0-RTT states, `remote_id` returns a `Result`.
- Relays only ever see ciphertext, plus metadata: IPs, timing and volume.
- `export_keying_material` (RFC 5705) exists in Rust but not in the FFI.

### 6. Application-layer secure channel

- **Go:** `github.com/flynn/noise` is at v1.1.0. Its last commit was 2024-02 (psk and export support), so it is dormant, but go-libp2p (active, October 2026) still depends on v1.1.0. It is pure Go on `x/crypto`, so it builds without CGO. Hand-rolling Noise IK on `x/crypto` is also about 300 lines.
- **Swift:** there is no widely used Noise package. Candidates: `swift-libp2p/swift-noise` (4 stars), and bitchat's in-app CryptoKit implementation (`bitchat/Noise/*`, Unlicense, about 37k stars, supports XX/IK/NK; worth reading or copying from). CryptoKit has everything needed for `Noise_IK_25519_ChaChaPoly_SHA256`: `Curve25519.KeyAgreement`, `ChaChaPoly` (you build the 12-byte nonce as 4 zero bytes + a 64-bit little-endian counter), `SHA256`, `HMAC` and `HKDF`. Validate against the cacophony/snow test vectors.
- **Pattern** (Noise rev 34, §7.7):
  - **IK** is right when the device already has the server's static key from the QR code. It takes one round trip.
  - Transport messages get destination property 5 (strong forward secrecy).
  - The first message's payload is only dest 2 (not forward-secret, replayable). Put no application data in message 1.
  - XX adds a round trip and only helps when neither side knows the other's key.
  - For pairing, use **`Noise_IKpsk2`** (as WireGuard does) with psk = a one-time secret from the QR code. This proves the device saw the QR.
  - After pairing, use IK, or KK, which keeps the device's static key off the wire.
  - Put a protocol version and both iroh EndpointIds in the **prologue**, so the inner session is tied to the outer one.
- **SAS / comparison code:** Noise §11.2 says the handshake hash `h` can serve as a channel binding.
  - Derive `HKDF(h, "xbind-sas-v1")` and show 6 digits (as Bluetooth LE numeric comparison does) or Matrix-style 3×4 digits or emoji.
  - This code proves which device actually paired, which protects against a leaked or phished QR code. With IK, a man-in-the-middle cannot impersonate the server anyway.
  - If you ever pair **without** a pre-shared key, a 6-digit code can be brute-forced by grinding ephemeral keys. Then you need a commitment step (Bluetooth SC, ZRTP and Matrix's `commitment` field all have one) or a PAKE (SPAKE2, as Magic Wormhole uses; or CPace).
  - Signal safety numbers are fingerprints of the long-term keys rather than per-session codes. They are useful for a "verify this device later" screen.
  - I found no iroh pairing or SAS example.

### Recommended architecture

1. **`xbind` plus an `xbind-iroh` Rust sidecar**, shipped prebuilt in the bundle for linux-musl x86_64/aarch64 and darwin-arm64.
   - Write it on `iroh = "1.4"` with `tls-ring`, using dumbpipe (MIT/Apache) as the template.
   - xbind supervises the process. It passes the secret key through stdin or an env var (never argv), plus the relay URL and token.
   - Control plane: JSON lines over stdio, carrying status, `EndpointAddr`/ticket, home relay, and allowlist updates.
   - Data plane: the sidecar accepts ALPNs `xbind/tunnel/1` and `xbind/pair/1`. An `EndpointHooks::after_handshake` hook rejects unknown `remote_id` values on the tunnel ALPN.
   - Each bidirectional stream maps to one unix-socket connection to xbind, with a header carrying magic, version, the remote EndpointId and the ALPN, followed by a byte splice. Windows would need a named pipe or loopback TCP instead.
   - Keep the interface narrow so the sidecar can later be swapped for `tmc/go-iroh`, once that project has more maintainers, a v1 API and an audit.
2. **iOS:** use IrohLib 1.2.x through SwiftPM (iOS 17.5 or later).
   - Store the iroh secret key and the Noise static key in the Keychain (`…ThisDeviceOnly`).
   - Build `RelayMode.custom` with `RelayConfig(url, authToken)` when the admin has configured a relay; otherwise use `presetN0()`.
   - Recreate the endpoint on foreground (#4289), declare `NSLocalNetworkUsageDescription`, and do not rely on background connections.
3. **Relay:** run `iroh-relay` 1.4.1 with LetsEncrypt.
   - Open TCP 80/443 and UDP 7842, and set `enable_quic_addr_discovery = true` and `[limits.client.rx]`.
   - For auth, prefer `access.http` → `https://xbind/…/relay-auth` with a bearer token. xbind returns `true` only for its own id and paired device ids, so revoking a device also revokes its relay access.
   - Use `access.shared_token` (token carried in the QR code) when the relay can't reach xbind.
   - With a private relay, use `presets::Minimal` so the relay URL isn't published to n0's DNS.
   - Treat n0's public relays as the zero-config default for hobby-scale use only.
4. **Inner channel:** the QR code carries {server EndpointAddr (id + relay URL), server Noise static key, relay token, one-time pairing psk with a TTL}.
   - Pair with `Noise_IKpsk2_25519_ChaChaPoly_SHA256`; the admin confirms the 6-digit SAS on both screens before xbind stores the device's static key and iroh id.
   - Reconnect with `Noise_IK` (or KK), using a fresh handshake per iroh connection, the prologue binding, and `Rekey` on long sessions.
   - Go side: flynn/noise v1.1.0, or a small audited implementation on x/crypto. Swift side: CryptoKit plus a vendored or derived Noise state machine, checked against test vectors.

**Still uncertain:**
- How much IrohLib actually adds to an App Store download (not measured).
- Whether relay access control also covers the QAD port (not documented).
- The public relays' rate limits (unpublished).
- go-iroh's security posture (no audit found).
- Exactly when the Node→Endpoint rename happened.
- Whether 1.x will stay as stable in practice as n0 promises (based on the Reddit criticism).


## Appendix D — iOS app map (exploration)

### 1. Pairing and sign-in

**QR scanning.** It uses VisionKit, not AVFoundation: `QRScanButton` and `QRScanner` wrap `DataScannerViewController` with `[.barcode(symbologies: [.qr])]` (`App/Shell/ScannerAndSSO.swift:15-77`). The button is disabled on the simulator, which leaves the "Paste the link" field (`App/Shell/SignInPages.swift:23-33`, `LoginLevel`).

**Payload.**
- The format is `xbin://enroll?u=<origin>&c=<26 chars A–Z2–7>` (`/home/magik6k/buxon/native/spec/device-login.md:37-54`).
- `DeepLink.init(string:)` parses it (`Packages/XbinCore/Sources/XbinCore/DeepLink.swift:73-79`). Unknown query items are ignored (`queryDictionary`, :134), so new params could ride on this link. The reserved hosts are `["enroll","sso"]` (:45).
- `SignInLink` (`Packages/XbinCore/Sources/XbinCore/Client/Onboarding.swift:152-196`) decides between an enrollment, an invite and a bare address. It refuses any other `xbin:` link (:168-173), so a new pairing link kind has to be added there.
- The "Add another device" QR is generated by `QRCodeImage` with `CIFilter.qrCodeGenerator()` and `correctionLevel = "M"` (`App/Shell/AddDeviceView.swift:132-155`).

**Flow.**
1. `OnboardingFlow.use(link:)` (`App/Shell/Onboarding.swift:109`) receives the scanned or pasted text.
2. A confirmation dialog asks "Add this workspace?" (`App/Shell/Onboarding.swift:267-277`). This is where a pairing-code confirmation popup would go.
3. `enroll(_:)` (:163) calls `Enrollment.enrollChecked` (Core `Onboarding.swift:265`). That first calls `GET /api/xbin/login/methods` so the code isn't spent on a dead address, then `Enrollment.enroll` (`Client/Auth.swift:371-400`).
4. Enrollment creates a key, sends `POST /api/xbin/devices/enroll {code,name,platform,publicKey}`, and gets back `{deviceId,user,origin,name}`.
5. It then signs in once: `POST /login/device/challenge` → sign → `POST /login/device` (`WorkspaceAuth.deviceSignIn`, `Auth.swift:254-288`).
6. The signed message is `"xbin-device-login-v1\n"+origin+"\n"+deviceId+"\n"+nonce` (`DeviceLogin.swift:30-36`). It signs the enrollment's `origin`, kept as `WorkspaceRecord.deviceOrigin` / `signedOrigin` (`WorkspaceRecord.swift:28,60`), not `u`.

`Enrollment` is built with `AppTransport.shared` in `OnboardingFlow.enrollment` (`App/Shell/Onboarding.swift:76-79`).

**Device key.** There is a real keypair, not just a token.
- `EnclaveKeyStore` (`App/Model/DeviceKeys.swift:13-91`) creates a `SecureEnclave.P256.Signing.PrivateKey` per workspace with `.biometryCurrentSet`, falling back to `.userPresence`.
- The enclave blob is stored in the Keychain (service `dev.xbin.devicekey`, account = workspace id).
- On the simulator it uses a software P-256 key, `SoftwareDeviceKey` (`Shared/SoftwareDeviceKey.swift`).

**Stored credentials.** All are generic-password Keychain items (`Shared/Keychain.swift`, `WhenUnlockedThisDeviceOnly` by default).

| Service | What it holds |
|---|---|
| `dev.xbin.session` | `SessionCredential` JSON: bearer token, kind, user, expiries (`App/Model/Stores.swift:6-21`; type at `Auth.swift:9-53`) |
| `dev.xbin.devicekey` | the enclave key blob |
| `dev.xbin.push` / `keyring` | X25519 push private keys, in the shared access group, after-first-unlock (`Keychain.swift:64-100`) |
| `dev.xbin.pushstate`, `dev.xbin.livestart` | relay handles (`App/Push/PushManager.swift:90-96,239-245`) |

The session is a **bearer token** with no cookies. A 401 re-signs with the enclave key, single flight (`WorkspaceAuth.send`, `Auth.swift:189-207`). Password, invite and SSO sign-ins mint a code and enroll right away (`Enrollment.enrollSignedIn`, :432). SSO uses `ASWebAuthenticationSession` with PKCE (`ScannerAndSSO.swift:82-114`).

### 2. Networking stack

**Central client.** There is no single `XbinClient`. The pieces are:
- **`APITransport`** (`Packages/XbinCore/Sources/XbinCore/Client/API.swift:75-77`): `send(_:to: ServerOrigin)`, with paths relative to the origin. The URL is built by `APIRequest.url(on:)` → `ServerOrigin.url(path:)` (API.swift:32, `ServerOrigin.swift:98`).
- **`AppTransport.shared`** (`App/Model/AppTransport.swift:8-65`) is the app's implementation: one ephemeral `URLSession` with no cookies or cache, which never follows redirects. It adds `stream(_ URLRequest…)` returning `StreamingLoad` (:54) for SSE, NDJSON and scheme pages.
- **`WorkspaceAuth`** (an actor, `Auth.swift:122`) holds a `record` and `any APITransport`. `origin` is `record.server` (:148).
- **`WorkspaceModel`** (`App/Model/WorkspaceModel.swift:13`) owns `auth`, `frameTokens`, and `transport`. `transport` is typed as the concrete `AppTransport` (:18, :76), which would need to become a protocol.

**Base URL.** It is `WorkspaceRecord.server: ServerOrigin`, persisted in `workspaces.json` (`Stores.swift:24-65`). `ServerOrigin` only accepts `http`/`https` (`ServerOrigin.swift:25`). It also provides `webSocketOrigin` (:101) and `isEncryptedInTransit` (:113), which drives the sign-in page's warning about plain http.

**Every path to xbind:**

| Path | Code | How it is built | Through an app-owned transport? |
|---|---|---|---|
| REST: whoami, catalog, prefs, devices, push registration, … | `w.auth.send/json/call` throughout `App/` | `APITransport` | yes |
| Agent NDJSON and tile logs | `AgentSessionTransport` (`AppTransport.swift:119-188`), `TileTools.swift:360` | `AppTransport.urlRequest` + `stream` (URLRequest-based) | yes, but bypasses the protocol |
| Enrollment and discovery | `Enrollment(transport: AppTransport.shared…)` | `APITransport` | yes |
| `/ws/events` | `URLSessionEventSocket` (`App/Model/WorkspaceEvents.swift:338-429`), made by an injected `EventSocketFactory` in `WorkspaceModel.makeEvents` (:296-300); URL from `EventsRoute.url` (Core `Client/Events.swift:225`) | own `URLSessionWebSocketTask`; bearer header; 30 s ping | factory is injectable |
| `/ws/term` | `WebSocketTransport` (`App/Terminal/WebSocketTransport.swift:12-109`), a `TermTransport` (XbinTerm `TermSession.swift:51`), built at `TerminalController.swift:149` | `origin.webSocketOrigin + path` (:22) | protocol is injectable |
| Native tile pty socket | `TileSocketTransport` (`App/Tiles/TileTerminal.swift:22-136`), built at :254 | `TileResource.socketURL(...)?frame=` (`TileResources.swift:80`) | protocol is injectable |
| Tile pages, runtime docs, canvas islands (`xbin-ws://<wsid>/c/...`) | `TileSchemeHandler: WKURLSchemeHandler` (`App/Tiles/TileSchemeHandler.swift:14`) → `ws.origin.url(path:)` (:76) → `AppTransport.shared.stream` (:89) | forwarded with the frame token, Set-Cookie stripped (`TileScheme`, `Client/TileLoading.swift:34-142`) | yes, but hard-wired to `.shared` |
| Native tile images and files | `NativeTile.getFile` → `workspace.transport.send` (`App/Tiles/NativeTile.swift:334`) | `APITransport` | yes |
| Native tile uploads | `TileUploader` → `AppTransport.shared.session.upload(for:from:delegate:)` (`App/Tiles/TileAttach.swift:342-352`) | raw URLSession | needs rework |
| **Tile pages' own WebSockets** | WebKit, direct | xbind injects `<meta name="xbin-ws-origin" content="ws(s)://<r.Host>">` when it sees `X-XBin-Client: app/` (`/home/magik6k/buxon/internal/server/native.go:142-156`; design: `/home/magik6k/buxon/plans/native.md:220-226`, "WebSockets bypass scheme handlers") | **no** |
| **Chrome tiles and xbind pages** (partitions) | `WebTileController.load()` (`App/Tiles/WebTileController.swift:173-193`): top-level `webView.load` on the real origin, URL from `WebTicket.destination` (`Client/WebHandoff.swift:64`); cookie session in `chromeDataStore` (`WorkspaceModel.swift:110`) | WebKit networking | **no** |
| **SSO** | `ASWebAuthenticationSession` on `server.url(path: /login/sso…)` (`ScannerAndSSO.swift:95-100`) | Safari, out of process | **no** |
| Non-workspace traffic | kill switch `https://xbin.dev/app/ios.json` (`App/Model/RemoteConfig.swift:51`); push relay (`PushManager.swift:183-250`, `LiveActivities.swift:589,614`) | `AppTransport.shared`, different origins | must stay on HTTPS |

**WKWebView details:**
- `WebTileController.configuration` (:153-171) gives each workspace its own data store, `WKWebsiteDataStore(forIdentifier: wsUUID)` (`WorkspaceModel.swift:99`). The chrome store uses the same UUID with its last byte flipped (:110-123).
- The scheme handler is registered only for non-chrome views.
- There is no `WKHTTPCookieStore` or `HTTPCookie` usage anywhere. Tiles hold no cookies; chrome views get a cookie by redeeming a one-shot web ticket in the view ("Continue as").
- Other web views are offline only: `loadHTMLString` in `BrandImage.swift:103` and `TileCanvas.swift:105`.
- `TileScheme.allowsRedirect` and the chrome navigation policy compare scheme, host and port against `workspace.origin` (`TileLoading.swift:130`, `WebTileController.swift:327-342`).

**Other places a host appears:** Handoff `webpageURL` (`App/Model/Handoff.swift:19-35`, `HandoffLink`), and deep-link matching by `server.authority` (`WorkspaceRecord.swift:53`).

### 3. Packages and build

**Layout.** The local packages are XbinCore, XbinTerm and XbinAgent (Foundation only, tested on Linux) and XbinRenderer (`Packages/*/Package.swift`). XbinCore has no dependencies; XbinRenderer depends only on XbinCore.

**Rules (`/home/magik6k/buxon/native/AGENTS.md:132-146`).** "Foundation only. No SwiftUI, UIKit, Combine, OSLog — and no `#if canImport(SwiftUI)` escape hatches. Transports are injected." In practice:
- Code that touches CryptoKit goes in `Shared/`, written as `#if canImport(CryptoKit) … #else import Crypto` (see `PushCrypto.swift`).
- It is tested on Linux by `/home/magik6k/buxon/native/tools/app-check`, which symlinks the app's files and uses swift-crypto ≥ 3.9.0 and SwiftTerm.

**Xcode project.** It is XcodeGen (`project.yml`); the `.xcodeproj` is never committed.
- Deployment target iOS **26.0**; packages declare `.iOS(.v26), .macOS(.v26)`. AGENTS.md's `v18` is only an example.
- Swift 6.0.
- Targets: Xbin, XbinNotificationService, XbinWidgets, XbinSnapshotHost, XbinSnapshotTests, XbinUITests.

**Third-party code.** The only third-party package is **SwiftTerm, pinned `exactVersion: 1.20.0`** (project.yml:47-52). The comment there explains why it is exact: a range would let a release pick up an unreviewed version and run its build plugin. There is no written "no deps" rule for the app (only `sdk/` is zero-dependency, per the root AGENTS.md:111). In practice there is one dependency, and `PrivacyInfo.xcprivacy` says "no third-party SDKs".

**Release.** `scripts/release-build.sh` is run by hand as the Mac's `release` user. It runs `xcodebuild archive` with `-allowProvisioningUpdates` and an App Store Connect key, then exports with method `app-store-connect`. Agents never run it (AGENTS.md:1220-1223).

**Entitlements and Info.plist.**
- `Support/Xbin.entitlements` has only `aps-environment` and `keychain-access-groups`. There is **no Network Extension, no multicast, and no `UIBackgroundModes`**.
- `Support/App-Info.plist` has `NSAllowsArbitraryLoads` (alone, on purpose: AGENTS.md:836-844), `NSLocalNetworkUsageDescription`, `NSCameraUsageDescription`, `NSFaceIDUsageDescription`, `ITSAppUsesNonExemptEncryption=false`, and the URL scheme `xbin`.

**App Store rules (AGENTS.md:765-852):**
- **Icon:** `App/Resources/AppIcon.icon`.
- **Privacy:** three places must agree — `App/Resources/PrivacyInfo.xcprivacy`, App Store Connect's App Privacy, and `website/privacy.html`. They currently say the app talks only to the user's workspaces and to `xbin.dev/app/ios.json`. `native/tools/store-check.py` enforces this, including required-reason APIs found via `nm -u`.
- **Export compliance:** `false` is justified because there is "no own cipher code compiled for iOS" (CryptoKit, Secure Enclave, URLSession TLS only). AGENTS.md says to "revisit it if the app ever ships its own cryptography". An iroh library plus an xbin-level authenticated encryption layer triggers that.
- **Invariants (`/home/magik6k/buxon/plans/native.md:52-73`):** the human credential never reaches tile code, and no credential-less loads.

### 4. Settings, multiple workspaces, capability detection

**Per-workspace state:**
- `WorkspaceRecord` (`Packages/XbinCore/Sources/XbinCore/WorkspaceRecord.swift:14-81`) holds id, server, user, deviceId, deviceOrigin, deviceName, branding and dates, stored in `workspaces.json`.
- Its Codable uses explicit keys (:167-197). An older app would decode a record with a new `tunnel` field but drop that field when it saves. Only records that fail to decode are kept verbatim (`unreadable`).
- Secrets are in the Keychain under account = workspace id. App-wide settings are `AppSettings` in UserDefaults (`App/Model/AppInfo.swift:70`). The only per-workspace UserDefaults value is the `forceWeb` "ws|tile" keys.
- Runtime state lives in `WorkspaceModel` and `AppModel.workspaces` (`App/Model/AppModel.swift:44`).

**Settings screen.** `SettingsView` (`App/Shell/InboxAndSettings.swift:68`) has a per-workspace section with "Signed in as", "Server" and "Sign-in" rows (:88-107). A "Connection: iroh tunnel (direct/relay)" status row or toggle fits there. There is also an app-wide "App" section (:157). The switcher is in `App/Shell/SwitcherOverlay.swift`, and `RootView.swift:293` is the "Sign in again" entry.

**Capability detection:**
- `GET /api/xbin/login/methods` → `LoginMethods {api,title,auth,password,sso,invites}` (Core `Onboarding.swift:16-70`). This is the natural place to advertise a tunnel before pairing.
- `GET /api/xbin/whoami` → `Whoami.nativeRuntime`, etc. (`Client/Catalog.swift:11-41`).
- Per-feature `features` arrays, e.g. `/api/xbin/partitions` (`Client/XbindPages.swift:44-75`).
- The app manifest `https://xbin.dev/app/ios.json` only carries `nativeRuntime` kill switches (`Client/KillSwitch.swift:1-40`, `RemoteConfig.swift`). It is fetched without credentials and fails open, so it could hold a tunnel kill switch.

### 5. Existing crypto

- `Shared/PushCrypto.swift`: X25519 (`Curve25519.KeyAgreement`), HKDF-SHA256 (`hkdfDerivedSymmetricKey`, salt `epk‖R`, info `xbin-push-v1`), AES-GCM, and a low-order (all-zero shared secret) check. Vectors are in `/home/magik6k/buxon/native/spec/push-vectors.json`, checked by app-check. This is the closest building block for a Noise-style handshake. There is no ChaChaPoly use yet; CryptoKit has it.
- P-256 ECDSA with the Secure Enclave (`DeviceKeys.swift`) and the software version (`SoftwareDeviceKey.swift`, including `verify`). The enclave key can only **sign**, and each signature prompts Face ID. A per-connection handshake signature would prompt every time, so a separate X25519 static key in the Keychain, like the push keys, is more practical.
- SHA-256 for PKCE (`ScannerAndSSO.swift:94`).
- **No certificate pinning.** There is no `SecTrust` or `URLAuthenticationChallenge` handling anywhere; TLS errors are only classified (`Client/ConnectProblem.swift:134-175`).

### 6. Tests and CI

**Packages.**
- XbinCore tests stub the server with `actor FakeServer: APITransport` (`Packages/XbinCore/Tests/XbinCoreTests/ClientSupport.swift:8`), `FakeKeys: DeviceKeyStore` (:42), and `FailingTransport` / `OriginLog` (`OnboardingTests.swift:9,15`).
- `make swift-test` runs all four packages under `ulimit -s 512`.

**Linux app checks.**
- `/home/magik6k/buxon/native/tools/app-check`: `swift test` covers push vectors, the device-login vector and the events socket. `swift run app-live <host> <user> <password>` runs the workspace client against a live xbind through its own `Transport: APITransport` (`Sources/app-live/main.swift:104`).
- `/home/magik6k/buxon/native/tools/app-stubcheck` stubs `AppTransport`, `AgentSessionTransport`, `EnclaveKeyStore` and `Keychain` with their real signatures (`Stubs/App/AppStubs.swift:17-36`). Any API change to `AppTransport` must be mirrored there.

**UI tests (`UITests/`).** They do **not** stub the server. They run against a real xbind started by `scripts/e2e-xbind.sh` and reverse-tunnelled over ssh to `http://127.0.0.1:9871`.
- They skip without `XBIN_E2E_URL/_USER/_PASSWORD` (`UITests/E2E.swift:13-30`).
- They sign in through Log in → address → password.
- Launch args: `-XbinFreshStart`, `-XbinUITesting`.
- There is no camera on the simulator, so a pairing test would paste the link into "Paste the link".

**CI.**
- `.github/workflows/ios.yml` jobs: `packages`, `app`, `duo`, `snapshots` (+ hosted snapshots inside `snapshots`).
- `ci.yml` Linux jobs: `native` ("native swift-test"), `native-stubcheck`, `native-ci` (`ci-local-check.sh`).

### Where a tunnel transport plugs in

1. **HTTP.** Replace `WorkspaceModel.transport: AppTransport` with a per-workspace protocol that keeps `send`, `stream` and `upload`. Route `TileSchemeHandler` (:89), `AgentSessionTransport.open` and `TileUploader` through it instead of `AppTransport.shared`. `WorkspaceAuth` and `Enrollment` already take `any APITransport`. Leave the relay and kill-switch traffic on `AppTransport.shared`.
2. **Sockets.** Swap the three factories — `makeEvents` (`WorkspaceModel.swift:298`), `TerminalController.swift:149`, `TileTerminal.swift:254` — for tunnel-backed `EventSocket` / `TermTransport` implementations. The protocols already isolate them.
3. **WebKit-direct paths** (tile WebSockets, chrome and xbind pages, SSO). A `WKURLSchemeHandler` can't serve http/https or WebSockets. The options are:
   - an in-app loopback proxy, which also needs xbind to emit a loopback `xbin-ws-origin`, since that meta echoes `r.Host`;
   - per-data-store proxying through `WKWebsiteDataStore.proxyConfigurations` (iOS 17+). This isn't used anywhere in the codebase today; it would keep the real origin, so cookies and ticket checks still work.
   - SSO in `ASWebAuthenticationSession` runs outside the app, so it can only reach a loopback proxy, if anything.
4. **Pairing.** Extend `DeepLink.enroll` / `SignInLink`, show the confirmation in `OnboardingStack`'s dialog (`App/Shell/Onboarding.swift:267`), and keep the ticket and auth secret in new Keychain items keyed by workspace id. Keep `server` as a real `ServerOrigin` so `signedOrigin`, links and Handoff keep working.

### Constraints

- **No background work.** There is no `UIBackgroundModes`. Sockets close on background (`AppModel.enteredBackground` → `updateSockets`, `AppModel.swift:157,332`), and web tiles reload after 14 minutes suspended (`WebTileController.swift:237`). An iroh endpoint gets suspended with the app and must reconnect on `becameActive`. The only keepalive today is the 30 s WebSocket ping on `/ws/events`. There is no Network Extension entitlement. The notification extension decrypts pushes offline and needs no tunnel.
- **Simulator.** There is no Secure Enclave (software key) and no QR scanner.
- **App Store and privacy.**
  - The iroh relays would be a new third party that sees IP addresses, so the privacy manifest, App Privacy and `privacy.html` all need updating together.
  - Export compliance (`ITSAppUsesNonExemptEncryption`) must be revisited, because the app would ship its own crypto layer.
  - A Rust library would be a new, likely binary, dependency. Pin it exactly, as SwiftTerm is, and run `store-check.py` and `nm -u` on it.
  - iroh's mDNS local discovery would need Bonjour keys and possibly the multicast entitlement.
  - The http warning (`isEncryptedInTransit`) should say "encrypted" for tunnelled workspaces.
