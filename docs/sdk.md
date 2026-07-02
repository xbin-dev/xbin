# SDKs: Go backends, node/python patterns, and the in-frame JS API

## Go SDK — `github.com/magik6k/buxon/sdk`

Zero-dependency. In a buxon workspace the generated `go.work` resolves it
(the container ships the module at `/opt/buxon/sdk`); just require it:

```go
import buxon "github.com/magik6k/buxon/sdk"
```

### Serving

```go
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", list)
	mux.Handle("POST /events", buxon.RoleFunc("writer", create))
	buxon.Serve(mux) // listens on BUXON_SOCKET, drains gracefully on SIGTERM
}
```

Your handler sees paths with the `/api/<component>` prefix already stripped.
`buxon.Self()` returns your component path.

### Callers and roles

```go
c := buxon.Caller(r)          // CallerInfo{From, Role, Owner}
buxon.Role("writer", h)       // middleware: 403 below writer
buxon.RoleFunc("writer", hf)  // same, for HandlerFuncs
buxon.RoleSatisfies(have, want) // admin ⊃ writer ⊃ reader; custom = exact
```

Headers are trustworthy: buxond strips inbound `X-Buxon-*` and injects
verified values ([auth.md](/docs/auth.md)).

### Calling other elements & buxon APIs

```go
resp, err := buxon.Client().Get("http://buxon/api/apps/calendar/events?day=2026-07-02")
```

`buxon.Client()` routes through the gateway socket with this generation's
instance credential. The host is always the literal `buxon`. 403 means a
missing grant — declare it in `uses`, get it approved.

**Long-running calls stream.** The client has no overall timeout: SSE /
chunked responses from another element run until either side closes (bound
individual calls with a request context). For **WebSocket** to another
element, dial any WS library through the gateway:

```go
d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
	return buxon.GatewayDial(ctx)
}}
h := http.Header{"Authorization": {"Bearer " + os.Getenv("BUXON_TOKEN")}}
conn, _, err := d.DialContext(ctx, "ws://buxon/api/apps/other/stream", h)
```

Remember the lifecycle: streams to a backend die at its blue/green drain
(30 s after a save there) — reconnect loops are mandatory. Backends serving
active streams are not idle-reaped.

### Resources, vault, bus

```go
kv := buxon.KV(buxon.Resource("events"))      // resources.md for the full KV API
path := buxon.Resource("db")                  // sqlite file path (same-scope)
secret, err := buxon.Secret("imap-pass")      // own vault
err = buxon.Publish(buxon.Resource("bus"), "events/created", ev)
```

`buxon.Resource(name)` reads `BUXON_RES_<NAME>`; empty string = not granted.

## node backend (no SDK needed)

The contract is just "HTTP on a unix socket + a couple of env vars", so a
library is optional. `bx new --runtime node` scaffolds:

```js
const http = require('http');
const srv = http.createServer((req, res) => {
  const caller = req.headers['x-buxon-from'];   // verified
  const role   = req.headers['x-buxon-role'];
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify({ hello: caller }) + '\n');
});
srv.listen(process.env.BUXON_SOCKET);
process.on('SIGTERM', () => srv.close(() => process.exit(0)));
```

Calling out through the gateway:

```js
const { request } = require('http');
const req = request({
  socketPath: process.env.BUXON_GATEWAY,
  path: '/api/apps/calendar/events',
  headers: { authorization: `Bearer ${process.env.BUXON_TOKEN}` },
}, handleResponse);
req.end();
```

## python backend

`bx new --runtime python` scaffolds a `UnixStreamServer` +
`BaseHTTPRequestHandler` skeleton. Gateway calls: any HTTP client that
supports unix sockets (`requests` + `requests-unixsocket`, or raw
`http.client.HTTPConnection` with a connected `socket`), bearer token from
`BUXON_TOKEN`. For quick scripts consider `runtime: cgi` instead — env in,
stdout out, nothing to keep alive.

## cgi backend

Any executable. CGI/1.1 env (`PATH_INFO`, `QUERY_STRING`, `REQUEST_METHOD`,
body on stdin) plus `BUXON_COMPONENT`, `BUXON_FROM`, `BUXON_ROLE`. Response:
headers, blank line, body on stdout. Perfect for shell-script endpoints;
per-request exec, 30 s timeout.

## In-frame JS API (`window.buxon`)

Injected into every component document via `buxon-client.js` (unless the
manifest sets `inject: false`). No imports needed.

```js
buxon.self                       // "apps/thing" — this component's path

// fetch with identity attribution. REQUIRED for calling other elements'
// APIs from the browser; plain fetch to a sibling 403s (auth.md).
// Streaming responses (SSE via ReadableStream) work.
const r = await buxon.fetch(`/api/${buxon.self}/events`);
const r2 = await buxon.fetch('/api/apps/calendar/events'); // needs a grant

// attributed WebSocket to an element API (browsers can't set WS headers,
// so the frame token rides a query param buxond consumes — the callee
// never sees it):
const sock = buxon.ws('/api/apps/other/stream');

// bus (needs a reader grant on the resource)
const off = buxon.bus.on('res:apps/thing/bus/events/', (topic, data) => {…});
await buxon.bus.publish('res:apps/thing/bus', 'events/created', ev); // writer

// raw event stream: reload / build-start / build-error / build-ok / bus / grants
const off2 = buxon.events.on((e) => {…});
```

Height reporting to the embedding `<bx-frame>` and frame-token refresh are
automatic.

## Core elements (import in main-document pages like `root/index.html`)

```html
<script type="module">
  import '/vendor/bx-frame.js';     // <bx-frame src="…">
  import '/vendor/bx-terminal.js';  // <bx-terminal cwd="…"> (bx-frame uses it)
  import '/vendor/bx-grants.js';    // <bx-grants> owner approval panel
</script>
```

`lit` is importable everywhere via the injected import map
(`import { LitElement, html, css } from 'lit'`) — vendored, no CDN, works
offline.
