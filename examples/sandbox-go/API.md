# examples/sandbox-go API

The smallest **sandbox manager** on xbind's own runtime (D120): its backend
defines sandboxes xbind runs for it and drives them with
`xbin.SandboxAPI()` ([docs/sdk.md](../../docs/sdk.md) §Tile sandboxes). It
is the runtime's end-to-end fixture (`test/isolated/`) and a starting point:
every route below is a few lines of `backend/main.go`. The builtin
`coding-sandbox` template is the full manager — the sandbox-manager
contract ([docs/sandbox-manager.md](../../docs/sandbox-manager.md)), sharing,
people, images, quotas.

## Set it up

Needs `xbind --isolate`. Import it, then, as a workspace admin:

```sh
bx grant <tile> cap:sandboxes writer    # the runtime: an admin-only grant
bx bind <tile> internet=internet        # optional: its sandboxes' network
```

`internet` is a `sandbox-net` slot: the network this tile's *sandboxes* may
use, never its backend's. Unbound, a sandbox created with
`"egress": "internet"` reaches nothing (`net.reach` is `none`); a binding
made later reaches a running sandbox at its next start.

## Roles

| Role   | Grants |
|--------|--------|
| reader | See your sandboxes, their commands' output, files and snapshots |
| writer | Create and use your sandboxes: commands, terminals, files, snapshots |

**Whose sandbox.** Each sandbox belongs to the caller that created it, as
xbind names it in `X-XBin-From` — the calling tile (this tile's own page
included), or `owner` / `user:<id>` for a call made without a tile's
credential — kept in its `sandbox-go.home` label. A sandbox of another
caller answers `404 not-found` on every route, as if it didn't exist; the
list shows only yours. The caller is a tile, not a person: everyone
driving one tile's page shares that tile's sandboxes (the
`coding-sandbox` template partitions by person too). Names are one
namespace across callers: a create of a name another caller holds answers
`409 exists`.

## Endpoints

Errors are the contract's shape, passed on from the runtime unchanged:
`{"error", "refusal", "state"?, "etag"?, "retryAfterMs"?}` (docs/protocol.md
§Tile sandboxes has the statuses). `{id}` is the sandbox's name
(`[a-z0-9][a-z0-9-]{0,31}`); `{eid}` an exec id (`ab12cd-7`), `{sid}` a
snapshot id (`s-3`) — one that fits no id is `404`.

| Route | Role | What it does |
|---|---|---|
| `GET /runtime` | reader | the runtime's answer: modes, egress classes, caps, limits (`Runtime`) |
| `GET /sandboxes` | reader | `{sandboxes: [SandboxInfo]}`, yours (`List`) |
| `POST /sandboxes` | writer | `{name, mode?, egress?, memMiB?, start?}` → 201 `SandboxInfo`: `mode` `namespace` (the default) or `vm`, `egress` `none` (the default) or `internet` (`Create`) |
| `GET /sandboxes/{id}` | reader | `SandboxInfo` (`Get`) |
| `DELETE /sandboxes/{id}` | writer | stop it and remove its state → 204 (`Delete`) |
| `POST /sandboxes/{id}/start` · `stop` · `reset` · `rebase` `?wait=<sec>` | writer | the lifecycle → `SandboxInfo` (`Start`, …) |
| `POST /sandboxes/{id}/run` | writer | the contract's run body → its result (`Run`); `forUser` is the verified person |
| `POST /sandboxes/{id}/execs` | writer | a background exec → 201 `Exec` (`Exec`) |
| `GET /sandboxes/{id}/execs` · `/execs/{eid}` | reader | `{execs}` · `Exec` (`Execs`, `GetExec`) |
| `DELETE /sandboxes/{id}/execs/{eid}` | writer | kill it and forget it → 204 (`Forward` to `ExecRoute`) |
| `GET /sandboxes/{id}/execs/{eid}/output?since=&max=&waitMs=&encoding=` | reader | a chunk of its output (`Forward` to `ExecOutput`) |
| `POST /sandboxes/{id}/execs/{eid}/stdin?eof=1` · `signal` · `resize` | writer | raw bytes · `{signal, group?}` · `{rows, cols}` → 204 (`Forward`) |
| `GET /sandboxes/{id}/tty?cmd=&cwd=&rows=&cols=` | writer | WebSocket: a new terminal — the login shell unless `cmd` (`RelayNewTTY`) |
| `GET /sandboxes/{id}/execs/{eid}/tty` | writer | WebSocket: attach to a tty exec (`RelayTTY`) |
| `GET /sandboxes/{id}/files/stat` · `content` · `list` `?path=…` | reader | a file's stat, its bytes (+ `ETag`), a directory (`Forward` to `FilesRoute`) |
| `PUT /sandboxes/{id}/files/content?path=&mkdirs=1&mode=&ifMatch=&ifNoneMatch=*` | writer | write a file from the raw body → its stat |
| `POST /sandboxes/{id}/files/mkdir` · `remove` · `move` | writer | `{path, parents}` · `{path, recursive}` · `{from, to, overwrite}` → 204 |
| `GET` · `PUT /sandboxes/{id}/tar?path=&exclude=&mkdirs=1` | reader · writer | a tree as a tar stream, or one extracted (`Forward` to `TarRoute`) |
| `GET` · `POST /sandboxes/{id}/snapshots` | reader · writer | `{snapshots}` · `{name, clientId}` → 201 the snapshot |
| `POST /sandboxes/{id}/snapshots/{sid}/restore` · `DELETE …/{sid}` | writer | restore it → `SandboxInfo` · delete it → 204 |

The terminals speak `/ws/term`'s wire end to end (docs/protocol.md): binary
frames of terminal bytes, `{"op":"session","id","sandbox":"<id>",…}` first,
`{"op":"exit","code"}` at the end. A page opens one with
`<bx-terminal src="/api/<tile>/sandboxes/<id>/tty">` (docs/elements.md); a
dropped socket then dials a new shell — reattaching to the same one is for
the contract's `/sbx/…` routes. Leaving a terminal leaves its command
running: `DELETE …/execs/{eid}` ends it.

## Use it

```jsonc
// caller's xbin.json
{ "uses": [{ "target": "apps/sandbox-go", "role": "writer" }] }
```

```go
c := xbin.Client()
resp, _ := c.Post("http://xbin/api/apps/sandbox-go/sandboxes", "application/json",
	strings.NewReader(`{"name":"build-1","egress":"internet","start":true}`))
resp, _ = c.Post("http://xbin/api/apps/sandbox-go/sandboxes/build-1/run", "application/json",
	strings.NewReader(`{"cmd":"go version"}`))
```
