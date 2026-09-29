# SDKs: Go backends, node/python patterns, and the in-frame JS API

## Go SDK — `github.com/xbin-dev/xbin/sdk`

Zero-dependency. In a xbin workspace the generated `go.work` resolves it
(the container ships the module at `/opt/xbin/sdk`); just require it:

```go
import xbin "github.com/xbin-dev/xbin/sdk"
```

### Serving

```go
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", list)
	mux.Handle("POST /events", xbin.RoleFunc("writer", create))
	xbin.Serve(mux) // listens on XBIN_SOCKET, drains gracefully on SIGTERM
}
```

Your handler sees paths with the `/api/<component>` prefix already stripped.
`xbin.Self()` returns your component path. `xbin.Deployment()` returns your
tile deployment's name when this backend runs a deployment that is not the
tile's primary, and `""` for the primary (and on an xbind without tile
deployments) — `$XBIN_DEPLOYMENT`. `Self()` stays the tile path in every
deployment, so vault URLs and `res:${self}` ids keep working; xbind picks the
deployment's data and vault from your token (§Tile deployments below).

```go
xbin.WriteJSON(w, http.StatusOK, out)         // Content-Type + status + body
xbin.WriteError(w, http.StatusForbidden, "…") // {"error": "…"} — the shape
                                              // xbin's own API uses
```

### Callers and roles

```go
c := xbin.Caller(r)          // CallerInfo{From, Role, Owner, User, UserLevel, ViewedBy, Deployment,
                             //            Partition, PartitionID}
c.UserCanWrite()             // gate mutating endpoints on the DRIVING user's
                             // level (D29) — frame calls from your own UI run
                             // at full role even for read-level viewers
c.ViewedBy                   // an admin viewing as User (D64): hide User's
                             // private data from them
xbin.AccessOf(ctx, user)     // that person's level on this tile NOW — for a
                             // credential kept past the call (below)
xbin.Role("writer", h)       // middleware: 403 below writer
xbin.RoleFunc("writer", hf)  // same, for HandlerFuncs
xbin.RoleSatisfies(have, want) // admin ⊃ writer ⊃ reader; custom = exact;
                               // bus aliases fold in (subscriber = reader,
                               // publisher = writer), as the broker grants them
c.Ingress()                  // anonymous PUBLIC traffic via a published
                             // endpoint (docs/ingress.md) — no role; the
                             // public hostname is in X-XBin-Ingress-Host
c.Deployment                 // the calling tile's deployment when the call
                             // comes from one of its non-primary deployments
                             // (X-XBin-Deployment); "" otherwise. c.From
                             // stays the bare tile path
c.Partition                  // X-XBin-Partition: the partition a partitioned
                             // tile's caller acts in, "user:<id>" | "global";
                             // "" otherwise. A display name
c.PartitionID                // X-XBin-Partition-Id: key per-caller state on
                             // (From, Deployment, PartitionID) — partitions.md
```

Headers are trustworthy: xbind strips inbound `X-XBin-*` and injects
verified values ([auth.md](/docs/auth.md)).

### Calling other elements & xbin APIs

```go
resp, err := xbin.Client().Get("http://xbin/api/apps/calendar/events?day=2026-07-02")
```

`xbin.Client()` routes through the gateway socket with this generation's
instance credential. The host is always the literal `xbin`. 403 means a
missing grant — declare it in `uses`, get it approved.

**Long-running calls stream.** The client has no overall timeout: SSE /
chunked responses from another element run until either side closes (bound
individual calls with a request context). For **WebSocket** to another
element, use `sdk/ws` (below) with `xbin.Client()`, or dial any WS library
through the gateway:

```go
d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
	return xbin.GatewayDial(ctx)
}}
h := http.Header{"Authorization": {"Bearer " + os.Getenv("XBIN_TOKEN")}}
conn, _, err := d.DialContext(ctx, "ws://xbin/api/apps/other/stream", h)
```

Remember the lifecycle: streams to a backend die at its blue/green drain
(30 s after a save there) — reconnect loops are mandatory. Backends serving
active streams are not idle-reaped.

### WebSocket — `github.com/xbin-dev/xbin/sdk/ws`

A WebSocket (RFC 6455) client and server in the SDK, on the standard
library alone. Dial another tile (or xbind) through the gateway — the
handshake goes through `xbin.Client()`, with this instance's credential:

```go
import "github.com/xbin-dev/xbin/sdk/ws"

c, _, err := ws.Dial(ctx, "ws://xbin/api/apps/other/stream", nil, &ws.DialOptions{Client: xbin.Client()})
if err != nil { … }
defer c.Close()
go func() { // keep one goroutine reading: it answers the peer's pings
	for {
		typ, msg, err := c.ReadMessage() // ws.TextMessage or ws.BinaryMessage, whole
		if err != nil {
			return // a *ws.CloseError when the peer closed
		}
		…
	}
}()
err = c.WriteMessage(ws.TextMessage, []byte(`{"op":"hello"}`)) // from any goroutine
```

Serve one from a handler:

```go
mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
	c, err := ws.Upgrade(w, r, nil) // a request that isn't a handshake is answered for you
	if err != nil {
		return
	}
	defer c.Close()
	…
})
```

- `DialOptions{Client, Subprotocols, MaxMessageSize}`: `Client` sends the
  handshake (nil: `http.DefaultClient`, for any `ws://` or `wss://` URL); the
  context bounds the handshake only. A refused handshake is
  `ws.ErrBadHandshake`, with the response (its status and up to 4 KiB of its
  body) returned alongside.
- `UpgradeOptions{Subprotocols, CheckOrigin, MaxMessageSize, Header,
  Error}`: every Origin is accepted unless `CheckOrigin` says otherwise —
  xbind authenticated the call before it reached your backend (a sandboxed
  page's Origin is `null`); a server of its own that trusts cookies checks
  it. `Error` answers a refused upgrade in your API's error shape.
- A received message is at most `MaxMessageSize` (default 32 MiB; over it
  the connection closes with 1009 and the read returns
  `ws.ErrMessageTooBig`); fragmented messages arrive whole. `Ping` sends a
  ping; `SetPongHandler` / `SetPingHandler` observe them. `SetReadDeadline`
  / `SetWriteDeadline` bound reads and writes (past one, the connection is
  spent). `Close` / `CloseWith(code, reason)` do the close handshake,
  waiting up to 2 s for the peer's answer.

### Testing a sandbox manager — `sdk/sandboxcontract`

A tile that runs sandboxes for other tiles ([sandbox-manager.md](sandbox-manager.md))
checks itself with the contract's conformance suite:
`sandboxcontract.Run(t, sandboxcontract.Target{URL: srv.URL})` runs every
section of the contract against it as subtests. How to aim it, and its
knobs: [sandbox-manager.md](sandbox-manager.md) §Building a manager.

### Resources, vault, bus

```go
kv := xbin.KV(xbin.Resource("events"))      // resources.md for the full KV API
path := xbin.Resource("db")                  // sqlite file path (same-scope)
secret, err := xbin.Secret("imap-pass")      // own vault
err = xbin.SetSecret("imap-pass", v)          // write / rotate it (DeleteSecret removes)
err = xbin.Publish(xbin.Resource("bus"), "events/created", ev)
err = xbin.Subscribe("deploys", "res:apps/ci/bus", "deploy/", "/on-deploy") // push, below
```

`xbin.Resource(name)` reads `XBIN_RES_<NAME>`; empty string = not granted.
A tile's frontend can't reach the vault API (D30), so a settings page that
takes a token posts it to the tile's own backend, which stores it with
`SetSecret` — gate that route on `xbin.Caller(r).UserCanWrite()`, since
anyone who can open the page reaches the backend at full role.

A backend reacts to bus traffic with a **push subscription** (D85): xbind
POSTs each matching event to your endpoint as `From: xbin/bus`, starting an
idle backend. Subscribe at start (idempotent by name); the component needs
`reader` on the bus in `uses`:

```go
_ = xbin.Subscribe("deploys", "res:apps/ci/bus", "deploy/", "/on-deploy")
mux.HandleFunc("POST /on-deploy", func(w http.ResponseWriter, r *http.Request) {
	if xbin.Caller(r).From != "xbin/bus" { http.Error(w, "", 403); return }
	var ev xbin.BusEvent // {ID, Subscription, Resource, Topic, Data, TS}
	_ = json.NewDecoder(r.Body).Decode(&ev)
	// at-most-once; ev.ID dedupes a publish that reaches several subscriptions
})
```

`xbin.Unsubscribe(name)` removes it; `GET /api/xbin/bus/subscriptions`
lists yours with delivered/dropped/failed counters.

### Your code in a tile deployment

A tile can run more than one deployment of its code
([tile-deployments.md](/docs/tile-deployments.md)): the primary, which
everyone and everything else reaches, and others (`dev`, …) its developers
open at `/c/<tile>+<name>/`. Your backend runs unchanged in each; the SDK
needs nothing new, and nothing in it changes for a tile that has only its
primary.

- **Same names, own state.** `Self()`, `Resource(name)` and the vault keys are
  the same in every deployment, and xbind resolves each call by your token: a
  non-primary deployment gets its own data, its own vault (where the
  primary's keys start as placeholders with no value — `Secret` answers an
  error until a tile manager copies them over) and its own log.
- **Calls stay in your deployment.** `xbin.Client()` calls to `/api/<self>/…`
  reach your own deployment, never another one of the tile. Calls to other
  tiles reach their primary under the tile's edge policy: by default read
  only, so a write to another tile is answered as a `reader` call (llm-gw's
  completions refuse it), some edges are blocked outright, and a write to
  another scope's resource is refused.
- **Side effects are held.** `NotifyUser` answers success but nothing is sent
  (the developers see "would notify" in the Deployments panel); `Status` and
  `Notify` show only in that panel.
- **Registrations are the deployment's own.** `Subscribe` and a cron
  registration fire for the deployment that made them, never the primary,
  unless a tile manager switched its deliveries off (the answer then says
  `dormant`). An interface instance or ingress host registration succeeds
  (the answer says `dormant`), so start-up code doesn't crash, but nothing
  routes to it until the deployment becomes the primary.
- **Callers can tell.** A call from another tile's non-primary deployment
  carries `Caller(r).Deployment`; a provider that must refuse test traffic
  checks it.

### Partitioned tiles (in development)

A tile that declares `"partition"` runs one backend instance per person who
uses it, plus an optional global instance
([partitions.md](/docs/partitions.md) — in development; on an xbind that
doesn't partition, these read nothing and the tile runs as one instance):

```go
xbin.Partition()        // "user:<id>" | "global" | "" — $XBIN_PARTITION
xbin.PartitionUser()    // the person of a user partition, "" otherwise
xbin.RequirePartition() // first thing in main: exit 3 unless xbind runs this
                        // as a partition — an older xbind, which ignores
                        // "partition", then runs no backend instead of one
                        // shared by everybody
xbin.GlobalURL("runs/42") // http://xbin/api/<self>/runs/42?xbin-partition=global
                        // from a user partition (the call arrives at global
                        // as the partition's person); the plain URL elsewhere
```

Your code needs nothing else: `Resource(name)`, the vault and registrations
are the partition's own. **TODO:** partition mail (`Mail`, `Inbox`, `Ack`).

### Notifying a person on their phone

`xbin.NotifyUser` sends a push notification to a person's xbin app devices —
for the moments that need them while they are away: a question, an approval,
a failed run. It is the platform's `POST /api/xbin/notify`
(docs/protocol.md §Push notifications), so node/python backends can call
that route directly with the same body. Notify from the **backend**: a tile's
frontend (and a shell in the tile) may notify only the person using it.

```go
u := xbin.Caller(r).User // the person who started it
err := xbin.NotifyUser(ctx, u, "Approval needed", "Deploy v2.3 to prod?", "#approvals/17")

// every field: kind (devices can filter on tile.<kind>) and collapseId
// (a later notification with the same id replaces the earlier one)
err = xbin.NotifyUserWith(ctx, xbin.UserNotification{User: u, Title: "3 failed runs",
	Body: "nightly-backup, sync, report", Link: "#runs", Kind: "failure", CollapseID: "failed-runs"})
if errors.Is(err, xbin.ErrNotifyRateLimited) { /* back off */ }
```

- The person must be able to **read this tile** — anyone else (and an
  unknown or disabled account) is refused with 403. The notification names
  your tile; tapping it opens the tile at `Link` (`#fragment`, `?query` or a
  path inside the tile — never another URL; `..` segments are refused, even
  percent-encoded).
- **Best-effort.** A nil error means xbind accepted it. Nothing is sent when
  the workspace has push off, the person registered no device for it, muted
  your tile, or has had too many notifications from tiles this hour (240
  deliveries — one per device — in bursts of 40, across every tile — over it
  they are dropped, not refused, so no tile learns what the others send);
  delivery is asynchronous with retries.
- **Rate-limited**: 120 an hour per tile (bursts of 20); over it,
  `ErrNotifyRateLimited` (429, `Retry-After`). Notify for things a person
  must act on, not for every event — a summary with a `CollapseID` beats a
  stream.
- Plain text only, short: titles over 120 characters and bodies over 1000
  are cut. The content is sealed end to end to the device; the push relay
  never sees it.
- `xbin.Notify(level, message)` is different: a toast in the web shell.

### Is a person still one of this tile's users? — `xbin.AccessOf`

`xbin.Caller(r).UserLevel` says what the person **calling now** may do. A
credential your tile keeps past that call — an SSH key, an API token or a
webhook secret a person registered on your page — outlives it: they may be
removed from the workspace or from the tile later, and the credential would
still name them. Check it at each use:

```go
a, err := xbin.AccessOf(ctx, key.User) // GET /api/xbin/access/<user>
if err != nil || !a.CanRead() {        // fail closed: no answer is no
	// refuse, and mark the credential inactive rather than delete it —
	// access may come back
}
```

- `a.Level` is `none | read | write | terminal` on **this tile** — what
  `UserLevel` would say if they called now; `a.Active` is false for a
  disabled or deleted account (its level is then `none`). `CanRead()` /
  `CanWrite()` fold both.
- Only the backend asks (its instance token), and only about its own tile;
  an unknown id is level `none`, never an error. An error means xbind
  didn't answer — don't let the credential in. Cache answers briefly (the
  sandbox-terminal tile keeps them 30 s) rather than asking on every
  packet.

### Tile sandboxes, for manager tiles — `xbin.SandboxAPI()`

A **manager tile** serves the sandbox-manager contract
([sandbox-manager.md](sandbox-manager.md)) to other tiles and can have
xbind run its sandboxes (D120). Its backend needs **`cap:sandboxes`**, a
grant only a workspace admin approves, and an xbind running with
`--isolate`. The routes are in [protocol.md](protocol.md) §Tile sandboxes;
the SDK has one call per route. The xbin repository's
`examples/sandbox-go` is the smallest manager built on it — each of its
routes a few lines (its `API.md`):

```go
sbx := xbin.SandboxAPI()
rt, err := sbx.Runtime(ctx) // modes, egress classes, limits (rt.Limits.Flows too), and rt.Caps: what this xbind serves
info, err := sbx.Create(ctx, xbin.SandboxSpec{Name: "sb-7f3a", Mode: "vm",
	Net: &xbin.SandboxNet{Egress: "class:internet"}, ClientID: reqID})
info, err = sbx.Start(ctx, "sb-7f3a", 30*time.Second) // Stop, Reset, Rebase alike; List, Get, Patch, Delete, Copy

sb := sbx.Sandbox("sb-7f3a")
res, err := sb.Run(ctx, xbin.RunRequest{Cmd: "go test ./...", Cwd: "/work"})
ex, err := sb.Exec(ctx, xbin.ExecRequest{Cmd: "make", Stdin: true}) // Execs, GetExec, Stdin, Signal, Resize, Kill
for c, err := range sb.Follow(ctx, ex.ID, 0) { // to the exec's end; c.Bytes() are exact
	…
}
st, err := sb.WriteFile(ctx, "/work/a.go", r, xbin.WriteOptions{Mkdirs: true}) // ReadFile, Stat, List, Mkdir, Remove, Move
tr, err := sb.GetTar(ctx, "/work", []string{"node_modules"})               // PutTar; both stream
snaps, err := sb.Snapshots(ctx)                                             // Snapshot, RestoreSnapshot, DeleteSnapshot
```

- **Errors.** Every refusal is a `*xbin.SandboxError`: `Status`, the
  contract's `Refusal`, `Message`, `State`, `ETag`, `RetryAfter`.
  `errors.Is` matches `xbin.ErrSandboxNotFound`, `ErrSandboxLost` and
  `ErrSandboxState`. `xbin.WriteSandboxError(w, err)` answers one to your
  consumer unchanged. A route this xbind doesn't serve yet answers
  `unsupported`. A name that would change the route (empty, `.`, `..`, one
  with a `/`, or a reserved name: `runtime`, `policy`, `copy`) is refused
  (`invalid`) before anything is sent, and so is an exec or snapshot id
  that fails the runtime's grammar: exec ids are `^[0-9a-f]{6}-[0-9]{1,12}$`
  (`ab12cd-7`), snapshot ids `^s-[0-9]{1,12}$` (`s-3`). If your contract
  ids are the runtime's, answer one that fails it `not-found` yourself
  (`xbin.IsExecID(eid)`, `xbin.IsSnapshotID(sid)`): it names nothing, and
  the contract says so.
- **Copies.** A snapshot, a restore and a clone (`SandboxSpec.From`) copy
  the sandbox's state off the request; the runtime waits up to
  `limits.waitMaxSec` for the copy, then answers as it stands: a
  `Snapshot` with `Pending` set, a restore's `SandboxInfo` with
  `StateDetail` `busy: …`, a clone's with `State` `creating`. Poll
  `Snapshots` or `Get` until it is done. Meanwhile the sandbox is busy:
  its calls answer `ErrSandboxState` with a `RetryAfter`.
- **Forwarding.** The runtime's routes mirror the contract's, so most of a
  manager's routes pass its own request through to a typed route:
  `sb.Forward(w, r, xbin.ExecOutput(eid), q)`. The routes are
  `xbin.ExecRoute(eid)` (GET, DELETE), `ExecOutput`, `ExecStdin`,
  `ExecSignal`, `ExecResize`, `ExecTTY`, `FilesRoute(xbin.FilesStat |
  FilesContent | FilesList | FilesMkdir | FilesRemove | FilesMove)` and
  `TarRoute()`; there is no free-form one. Each builder checks its id
  against the grammar and escapes it itself, so a consumer's id (one with a
  `/`, `..`, `%2F`, `?` or `#`) can only fail — Forward answers `400
  invalid` and sends nothing — and never reaches another route or
  sandbox. (A consumer's `%2F` never gets that far through xbind: its
  proxy hands your backend the path decoded, so your router sees the route
  a crafted path names and your own checks apply to that — the builders
  guard an id from a body, a query or a router that keeps encodings.)
  Forward streams both bodies and copies the status and headers
  (but not `Set-Cookie`). It sends only the query `q` you chose, never the
  consumer's raw query. It drops the inbound `Cookie`, `Authorization`,
  `Sbx-User`, `X-XBin-*` and `Sec-WebSocket-Extensions`: the call carries
  your tile's credential. Do your own checks first (the verified person,
  the consumer's sandboxes).
- **Ports** (the contract's `ports`, D135; `SandboxRuntime.Caps` carries
  `"ports"` on an xbind that has them). `xbin.PortRoute(port, path,
  rawQuery)` is the runtime's `ports/{port}/{path}`: any method, an HTTP
  proxy to a server on the sandbox's own loopback, WebSocket upgrades
  tunnelled. `path` is the consumer's path below the port, still escaped,
  and `rawQuery` its query — both go on unchanged (so pass `nil` as
  Forward's `q`; anything else is `400 invalid`), below that one port only:
  a segment that decodes to `.` or `..`, a bad escape or a raw `?` or `#`
  refuses the route. `sb.Forward(w, r, xbin.PortRoute(8000,
  r.PathValue("path"), r.URL.RawQuery), nil)` after your own checks — it
  drops the same credentials as any Forward, and xbind drops them again,
  with the server's `Set-Cookie`. Nothing listening is `502
  not-listening`; a stopped sandbox is `409 state` (the route never starts
  one).
- **Terminals.** `sb.RelayTTY(w, r, eid, xbin.TTYOptions{SessionID,
  SandboxID, ForUser})` relays a consumer's terminal WebSocket to a tty
  exec, and `sb.RelayNewTTY(w, r, xbin.TTYStart{Cwd, Cmd, …})` starts one
  (the login shell unless `Cmd`). The upgrade is tunnelled byte for byte,
  so your backend needs no WebSocket code and the consumer speaks the
  `/ws/term` wire end to end. `SessionID` and `SandboxID` put your own ids
  in the session frame; `ForUser` is the verified person, refused by xbind
  when they have no terminal access. A manager that drives a terminal
  itself (an SSH bridge) dials it with `sb.DialTTY(ctx, eid,
  xbin.TTYOptions{…})`, which returns an `sdk/ws` connection speaking the
  same wire.
- **Compatibility.** Request structs omit empty fields and answers decode
  leniently, so a newer SDK works against an older xbind.
- **Never hand your token to a sandbox.** A sandbox has no xbin identity:
  `XBIN_*` variables are refused in its environment, and it has no route to
  xbind.

## node backend (no SDK needed)

The contract is just "HTTP on a unix socket + a couple of env vars", so a
library is optional. `bx new --runtime node` scaffolds:

```js
const http = require('http');
const srv = http.createServer((req, res) => {
  const caller = req.headers['x-xbin-from'];   // verified
  const role   = req.headers['x-xbin-role'];
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify({ hello: caller }) + '\n');
});
srv.listen(process.env.XBIN_SOCKET);
process.on('SIGTERM', () => srv.close(() => process.exit(0)));
```

`process.env.XBIN_DEPLOYMENT` is the tile deployment's name in a non-primary
deployment (absent for the primary), and the `x-xbin-deployment` header names
a calling tile's non-primary deployment, as in Go. Likewise
`process.env.XBIN_PARTITION` and the `x-xbin-partition` /
`x-xbin-partition-id` headers on partitioned tiles
([partitions.md](/docs/partitions.md)).

Calling out through the gateway:

```js
const { request } = require('http');
const req = request({
  socketPath: process.env.XBIN_GATEWAY,
  path: '/api/apps/calendar/events',
  headers: { authorization: `Bearer ${process.env.XBIN_TOKEN}` },
}, handleResponse);
req.end();
```

## python backend

`bx new --runtime python` scaffolds a `UnixStreamServer` +
`BaseHTTPRequestHandler` skeleton. Gateway calls: any HTTP client that
supports unix sockets (`requests` + `requests-unixsocket`, or raw
`http.client.HTTPConnection` with a connected `socket`), bearer token from
`XBIN_TOKEN`.

A shell-script endpoint (what the removed `cgi` runtime was for) is a few
lines of any of these backends running the script per request — see
[changes/2026-09-27-cgi-removed.md](/docs/changes/2026-09-27-cgi-removed.md).

## In-frame JS API (`window.xbin`)

Injected into every component document via `xbin-client.js` (unless the
manifest sets `inject: false`). No imports needed.

```js
xbin.self                       // "apps/thing" — this component's path
xbin.deployment                 // "dev" — only in a document of a tile deployment
                                // other than the tile's primary
                                // (/c/<tile>+<name>/); absent means the primary.
                                // xbin.self stays the tile path, and
                                // xbin.fetch(`/api/${xbin.self}/…`) reaches this
                                // document's own deployment
xbin.partition                  // "user:alice" | "global" — only in a partitioned
                                // tile's document (partitions.md, in development):
                                // the partition the viewer reaches

// a bound http interface (docs/overview/11-interfaces.md): { url, service } or null. Call a
// typed, swappable dependency instead of hard-coding a path — the owner binds
// which provider satisfies it (bx bind / admin Interfaces tab), and the binding
// is also the call grant.
const llm = xbin.iface('llm');  // { url: '/api/apps/llm-gw', service: 'openai' }
if (llm) await xbin.fetch(`${llm.url}/v1/chat/completions`, { method: 'POST', … });

// fetch with identity attribution. REQUIRED for calling other elements'
// APIs from the browser: it attaches the frame token so the callee sees your
// tile as the caller. A plain fetch to a sibling is unattributed — 403 for a
// non-admin user; an admin's own cookie would call as admin and mask a missing
// grant, so always use xbin.fetch (auth.md). Streaming (SSE) works.
const r = await xbin.fetch(`/api/${xbin.self}/events`);
const r2 = await xbin.fetch('/api/apps/calendar/events'); // needs a grant
// from a user partition's document: the tile's global instance, as the viewer
// (partitions.md). The option is stripped, and ignored in any other document
const r3 = await xbin.fetch(`/api/${xbin.self}/shared/42`, { partition: 'global' });

// attributed WebSocket to an element API (browsers can't set WS headers,
// so the frame token rides a query param xbind consumes — the callee
// never sees it). In the xbin app it connects to the injected
// xbin-ws-origin (docs/elements.md §Views) — same code:
const sock = xbin.ws('/api/apps/other/stream');

// attributed URL string — same query-param trick for TAG-driven requests
// (<a href>, media src) that can't set headers. Build at click time: the
// embedded token is short-lived. The classic use is a backend-streamed
// download (endpoint answers Content-Disposition: attachment):
a.href = xbin.url(`/api/${xbin.self}/export.csv`);

// client-side file download (sandboxed tiles may download — allow-downloads,
// ND10). data: Blob | ArrayBuffer | TypedArray | string. Call from a user
// gesture; browsers throttle unprompted downloads.
xbin.download('report.csv', csvText, 'text/csv');

// links in NEW TABS (<a target="_blank">, window.open) need the tile to
// hold the cap:open-links grant (ND11: uses [{target:"cap:open-links",
// role:"writer"}], admin-approved) — otherwise the sandbox drops them.
// Use rel="noopener"; in a credentialless frame window.open() returns null.

// bus (needs a reader grant on the resource)
const off = xbin.bus.on('res:apps/thing/bus/events/', (topic, data) => {…});
await xbin.bus.publish('res:apps/thing/bus', 'events/created', ev); // writer

// raw event stream: reload / build-start / build-error / build-ok / bus / grants /
// deployments (docs/protocol.md §/ws/events). reload and build-* speak only of a
// tile's primary; a document of another deployment gets its reloads and builds as
// {type: "deployments", data: {op: "reload"|"build", deployment}}
const off2 = xbin.events.on((e) => {…});

// ---- dialogs & pop-out windows (a tile is an iframe → the SHELL spawns these
// over the whole workspace; see docs/elements.md §Dialogs & windows) ----

// A trusted modal rendered by the shell from plain data (no markup — safe).
// Resolves { button, values }: button = the clicked button's value (null if
// dismissed via Esc / backdrop / Cancel), values = the field inputs.
const res = await xbin.dialog({
  title: 'Delete account?',
  message: 'This removes apps/imap#aurora and its stored mail.',
  fields: [{ name: 'confirm', label: 'Type the name to confirm', placeholder: 'aurora' }],
  buttons: [{ label: 'Cancel', value: null }, { label: 'Delete', value: 'del', danger: true }],
});
if (res.button === 'del' && res.values.confirm === 'aurora') { … }
// spec.error (plain text) renders as an alert box — re-open with it set to
// say why the last submit failed: xbin.dialog({ ...spec, error: 'Name taken' })

// A floating window running YOUR OWN UI: it frames a sub-path of this component
// (a normal tile document), so it has its own xbin client and talks to your
// backend with the usual xbin.fetch. Escapes the tile's clipping.
const win = xbin.window({ path: 'compose', title: 'New message', width: 620, height: 440 });
win.closed.then(() => refresh());   // resolves when the window is closed
// win.close() to close it yourself. spec.src frames a full component path
// instead of a sub-path (subject to the same tile-access RBAC as <bx-frame>).
```

`xbin.dialog` falls back to an in-frame modal when the tile isn't embedded in
the shell; `xbin.window` needs the shell (no-op otherwise).

In the xbin app's native runtime document (a tile's `native.js`) `xbin` also
has `native` — `caps`, `supports()`, `meta()`, `copy()`, `share()`, `open()`,
`state`, `saveState()`: see [native.md](/docs/native.md). Web pages don't
get it.

Height reporting to the embedding `<bx-frame>` and frame-token refresh are
automatic.

## Core elements (import in main-document pages like `root/index.html`)

```html
<script type="module">
  import '/vendor/bx-frame.js';     // <bx-frame src="…">
  import '/vendor/bx-terminal.js';  // <bx-terminal src="…"> (a terminal-wire endpoint; bx-frame uses its cwd="…" mode)
  import '/vendor/bx-grants.js';    // <bx-grants> owner approval panel
  import '/vendor/bx-dialog.js';    // <bx-dialog> modal (xbin.dialog fallback)
  import { xbinApi, jbody } from '/vendor/bx-kit.js'; // the helper kit
</script>
```

Every module a tile may import — and the shell-only ones — is listed in
[frontend-kit.md](/docs/frontend-kit.md); always import by absolute
`/vendor/…` URL.

`lit` is importable everywhere via the injected import map
(`import { LitElement, html, css } from 'lit'`) — vendored, no CDN, works
offline.
