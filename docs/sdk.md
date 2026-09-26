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
`xbin.Self()` returns your component path.

```go
xbin.WriteJSON(w, http.StatusOK, out)         // Content-Type + status + body
xbin.WriteError(w, http.StatusForbidden, "…") // {"error": "…"} — the shape
                                              // xbin's own API uses
```

### Callers and roles

```go
c := xbin.Caller(r)          // CallerInfo{From, Role, Owner, User, UserLevel, ViewedBy}
c.UserCanWrite()             // gate mutating endpoints on the DRIVING user's
                             // level (D29) — frame calls from your own UI run
                             // at full role even for read-level viewers
c.ViewedBy                   // an admin viewing as User (D64): hide User's
                             // private data from them
xbin.Role("writer", h)       // middleware: 403 below writer
xbin.RoleFunc("writer", hf)  // same, for HandlerFuncs
xbin.RoleSatisfies(have, want) // admin ⊃ writer ⊃ reader; custom = exact;
                               // bus aliases fold in (subscriber = reader,
                               // publisher = writer), as the broker grants them
c.Ingress()                  // anonymous PUBLIC traffic via a published
                             // endpoint (docs/ingress.md) — no role; the
                             // public hostname is in X-XBin-Ingress-Host
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
element, dial any WS library through the gateway:

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
  your tile, or has had too many notifications from tiles this hour (240,
  bursts of 40, across every tile — over it they are dropped, not refused, so
  no tile learns what the others send); delivery is asynchronous with
  retries.
- **Rate-limited**: 120 an hour per tile (bursts of 20); over it,
  `ErrNotifyRateLimited` (429, `Retry-After`). Notify for things a person
  must act on, not for every event — a summary with a `CollapseID` beats a
  stream.
- Plain text only, short: titles over 120 characters and bodies over 1000
  are cut. The content is sealed end to end to the device; the push relay
  never sees it.
- `xbin.Notify(level, message)` is different: a toast in the web shell.

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
`XBIN_TOKEN`. For quick scripts consider `runtime: cgi` instead — env in,
stdout out, nothing to keep alive.

## cgi backend

Any executable. CGI/1.1 env (`PATH_INFO`, `QUERY_STRING`, `REQUEST_METHOD`,
body on stdin) plus `XBIN_COMPONENT`, `XBIN_FROM`, `XBIN_ROLE`. Response:
headers, blank line, body on stdout. Perfect for shell-script endpoints;
one exec per request (no persistent process, so no idle reaping or drain).

## In-frame JS API (`window.xbin`)

Injected into every component document via `xbin-client.js` (unless the
manifest sets `inject: false`). No imports needed.

```js
xbin.self                       // "apps/thing" — this component's path

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

// raw event stream: reload / build-start / build-error / build-ok / bus / grants
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

Height reporting to the embedding `<bx-frame>` and frame-token refresh are
automatic.

## Core elements (import in main-document pages like `root/index.html`)

```html
<script type="module">
  import '/vendor/bx-frame.js';     // <bx-frame src="…">
  import '/vendor/bx-terminal.js';  // <bx-terminal cwd="…"> (bx-frame uses it)
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
