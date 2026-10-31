# The app tunnel (iroh)

Decision: D204. The xbin app can reach your workspace through an **iroh
tunnel** instead of over the workspace's web address: the phone and xbind
find each other through a relay (n0's public relays, or one you run) and
talk directly when the network allows. A workspace that sits behind NAT, on
a home connection or a VPN-only network needs no public address for its
apps, and a workspace that does have one can refuse the app anywhere else
("iroh-only"), so an app session can't leak off a phone that left the VPN.

The UI calls it **Iroh tunneling** (admin console → workspace → app).

## What protects the traffic

Two layers, end to end between the phone and xbind:

- **iroh** (QUIC + TLS 1.3, keyed by each side's Ed25519 identity — its
  *EndpointId*). The relay forwards encrypted packets; it never sees inside.
- **xbin's own layer: Noise**, inside iroh. xbind holds a Noise static key;
  the pairing QR code carries its public half, so a phone only ever talks to
  *this* xbind. Every connection runs a fresh handshake
  (`Noise_IK_25519_ChaChaPoly_SHA256`: forward secret, mutually
  authenticated), bound to both iroh identities, and rekeys as it goes. The
  iroh endpoint runs in a separate, sandboxed process (`xbin-iroh`); the
  Noise keys and handshakes stay in xbind, so even that process sees only
  ciphertext.

Inside, it is plain HTTP — the same API, the same sign-in: the app still
signs in with its device key (Face ID), and its sessions work only through
the tunnel they were made on.

## Turning it on

1. Admin console → **workspace → app** → **Iroh tunneling** → on (or
   `bx app tunnel on`; `PUT /api/xbin/app-tunnel {"enabled":true}`).
2. Pick the relay: **n0's public relays** (the default — no setup; n0 runs
   them for hobby-scale use, and xbind publishes its endpoint and addresses
   to n0's discovery service) or **your own** (below).
3. **Test** checks that a throwaway endpoint reaches this one through the
   relay (`bx app tunnel test`).

Add a device as before (settings → **add a device**): with the tunnel on,
the QR code is larger — it also carries how to reach the tunnel. Scan it
with the app, then **compare the codes**: the phone shows six digits, and
your computer shows the same six and the phone's name. Press **Codes match**
only if they do — **They don't** cancels, and nothing is paired. The page
`/xbin/devices` does the same from any browser (an older shell can't show
the code step; use it there).

Phones paired before the tunnel was on keep working over the web address;
an app that supports it moves itself onto the tunnel (the device registers
its tunnel keys at its next sign-in). An older app scanning a tunnel QR code
is told the code is invalid: while iroh-only is off, the dialog offers a
classic code for it.

The app keeps the tunnel only while it is on screen: in the background it
closes it, and coming back it opens a fresh one and checks it with a quick
request before anything uses it; the open screen carries on where it was.
To everything above it the tunnel is just the way to the workspace: while
it is down, requests fail exactly as they do when the phone's network drops
(a tile page sees a lost connection, never a page about the tunnel), and
when it is back, pages that failed load again and the others get the
browser's `online` event — the same as for a workspace the app reaches
directly once its connection returns.
**Settings → Connection** on the phone shows the tunnel's path, relay and
round trip, a **Reconnect** button, and **Diagnostics** — what the tunnel
did lately (connects, drops, retries, the app going away and back). **Copy
diagnostics** copies that with the app's version, for a bug report: it holds
no keys, tickets or tokens, and ids are cut short.

## Refusing the app outside the tunnel (iroh-only)

**Refuse app connections outside the iroh tunnel** (needs the tunnel on;
turning the tunnel off turns it off). Then:

- a device's sessions, the browser sessions a device opened, and the app's
  password, invite and SSO-ticket sign-ins are refused off the tunnel with
  `403 {"tunnel":"required"}` (an old app shows the message; it never loops
  re-signing in);
- adding a device works only through the tunnel's QR code (an older shell's
  dialog is refused and names `/xbin/devices`);
- browsers are not affected, and the app's SSO browser step still runs at
  the web address (the device then signs in over the tunnel).

The switch says how many devices aren't paired with the tunnel yet — they
are locked out until they pair again (or update and move themselves over
first). If the tunnel itself is down while iroh-only is on, every app is
locked out: the admin console raises a critical alert.

## How many devices

Each person may have **one** app device paired unless you say otherwise:
the workspace default (`bx app limit <n>`, 1–32) and per-person overrides
(`bx user set <id> --max-devices n|default`, 0–32). At the limit, pairing a
new phone over the tunnel offers to **replace** one of the person's devices
in the same step; a classic code is refused until one is removed. Lowering a
limit removes nobody's device.

## Running your own relay

A self-hosted `iroh-relay` keeps the workspace's endpoint off n0's
discovery service and lets you control who uses the relay. It needs a
public host name, TCP 80 and 443 and UDP 7842. Give the host name both an A
and an AAAA record and listen on both: a phone on an IPv6-only network
(several mobile carriers; Apple's App Review tests on one) reaches the
workspace only through the relay, because iroh can't yet cross NAT64 to an
IPv4-only server directly. Get the binary from iroh's GitHub releases
(`iroh-relay-v<version>-x86_64-unknown-linux-musl`, matching the iroh
version this xbind's `xbin-iroh version` prints) or the `n0computer/iroh-relay`
image.

```toml
# iroh-relay.toml — top-level keys before any [table]
enable_quic_addr_discovery = true
metrics_bind_addr = "127.0.0.1:9090"     # metrics are public by default: keep them local
access.shared_token = ["<token>"]        # a list: add the next token before rotating

[tls]
cert_mode = "LetsEncrypt"
hostname = "relay.example.org"
contact = "admin@example.org"            # required for LetsEncrypt
cert_dir = "/var/lib/iroh-relay/certs"

[limits.client.rx]
bytes_per_second = 2097152
max_burst_bytes = 4194304
```

Run it under systemd (`DynamicUser=yes`, `AmbientCapabilities=CAP_NET_BIND_SERVICE`)
or in a container, and keep it patched. In the admin console pick **custom
relay**, enter `https://relay.example.org`, and **generate** a token: it is
shown once — put it in `access.shared_token` and restart the relay, then
**promote** it. The token travels in pairing QR codes (anyone holding one can
use the relay's bandwidth), so rotate it from time to time:

1. **generate next** (or set one), add it to the relay's `access.shared_token`
   list, restart the relay;
2. wait until the console shows every device **synced** (devices fetch the
   staged token whenever they connect);
3. **promote** — xbind switches to it;
4. drop the old token from the relay's list.

`XBIN_IROH_RELAY` overrides the console's relay for one xbind (`n0`, a URL,
or `disabled` — direct addresses only, for tests); see
[config.md](/docs/config.md).

## Threat model, briefly

- **The relay operator (n0's included)** sees endpoint ids and timing, never
  content. n0's preset also publishes this xbind's endpoint and addresses to
  n0's discovery; a custom relay doesn't.
- **A leaked QR code** lets someone try to pair — only with you confirming
  matching codes on your own screen, within minutes; its code never works
  over the web address. It also leaks the relay token until you rotate it.
- **Another app on the phone** can reach the app's local tunnel port; the
  tunnel serves only its own device's signed-in requests and sign-in steps.
- **A stolen phone** can open the tunnel but still needs Face ID to sign
  in; removing the device closes its tunnel and ends its sessions.
- **A leaked app session** is useless off the tunnel it was made on.
- **The sidecar process** holds the iroh key and runs on the host network
  (sandboxed, no capabilities): it could impersonate the endpoint or drop
  traffic, but not read or forge what travels inside Noise.
- **The server's keys** (`data/app-tunnel/keys.json`, outside the vault so a
  sealed workspace's apps still sign in) let whoever steals them impersonate
  this xbind to its phones; recovery is new keys and pairing every device
  again.

## API

`GET|PUT /api/xbin/app-tunnel`, `POST /api/xbin/app-tunnel/relay-tokens`,
`POST /api/xbin/app-tunnel/test`, `GET /api/xbin/app-tunnel/client`,
`POST /api/xbin/devices/enroll-code {"tunnel":true}`,
`GET /api/xbin/devices/pairing/<id>`, `POST /api/xbin/devices/pairing/<id>/confirm`,
`POST /api/xbin/devices/<id>/tunnel`, `GET|PUT /api/xbin/devices/limits`, the
page `/xbin/devices` ([protocol.md](/docs/protocol.md)); the wire contract
the app implements is `native/spec/tunnel.md` in the source tree.
