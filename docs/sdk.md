# SDKs: Go backends, node/python patterns, and the in-frame JS API

## Go SDK — `github.com/magik6k/xbin/sdk`

Zero-dependency. In a xbin workspace the generated `go.work` resolves it
(the container ships the module at `/opt/xbin/sdk`); just require it:

```go
import xbin "github.com/magik6k/xbin/sdk"
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

### Callers and roles

```go
c := xbin.Caller(r)          // CallerInfo{From, Role, Owner}
xbin.Role("writer", h)       // middleware: 403 below writer
xbin.RoleFunc("writer", hf)  // same, for HandlerFuncs
xbin.RoleSatisfies(have, want) // admin ⊃ writer ⊃ reader; custom = exact
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
err = xbin.Publish(xbin.Resource("bus"), "events/created", ev)
```

`xbin.Resource(name)` reads `XBIN_RES_<NAME>`; empty string = not granted.

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
per-request exec, 30 s timeout.

## In-frame JS API (`window.xbin`)

Injected into every component document via `xbin-client.js` (unless the
manifest sets `inject: false`). No imports needed.

```js
xbin.self                       // "apps/thing" — this component's path

// a bound http interface (plans/interfaces.md): { url, service } or null. Call a
// typed, swappable dependency instead of hard-coding a path — the owner binds
// which provider satisfies it (bx bind / admin Interfaces tab), and the binding
// is also the call grant.
const llm = xbin.iface('llm');  // { url: '/api/apps/llm-gw', service: 'openai' }
if (llm) await xbin.fetch(`${llm.url}/v1/chat/completions`, { method: 'POST', … });

// fetch with identity attribution. REQUIRED for calling other elements'
// APIs from the browser; plain fetch to a sibling 403s (auth.md).
// Streaming responses (SSE via ReadableStream) work.
const r = await xbin.fetch(`/api/${xbin.self}/events`);
const r2 = await xbin.fetch('/api/apps/calendar/events'); // needs a grant

// attributed WebSocket to an element API (browsers can't set WS headers,
// so the frame token rides a query param xbind consumes — the callee
// never sees it):
const sock = xbin.ws('/api/apps/other/stream');

// bus (needs a reader grant on the resource)
const off = xbin.bus.on('res:apps/thing/bus/events/', (topic, data) => {…});
await xbin.bus.publish('res:apps/thing/bus', 'events/created', ev); // writer

// raw event stream: reload / build-start / build-error / build-ok / bus / grants
const off2 = xbin.events.on((e) => {…});
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
