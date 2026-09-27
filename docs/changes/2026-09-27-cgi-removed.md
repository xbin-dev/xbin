# 2026-09-27 — runtime "cgi" is removed (D117)

## What changed

`"runtime": "cgi"` no longer runs anything. xbind used to execute a cgi
tile's `backend/handler` once per request, itself, on the host — as the
daemon's user, in the tile's directory, with isolation or without. That
path is gone:

- A tile whose `xbin.json` says `"runtime": "cgi"` still loads and **keeps
  serving its files** (`/c/<tile>/…`, frames, the sidebar), but it has no
  backend. Its manifest error says so — `bx ls` flags it `MANIFEST-ERROR`,
  `bx doctor` prints it, `GET /api/xbin/components` carries it as
  `manifestError`, the sidebar and the admin console show ⚠:

  ```
  runtime "cgi" was removed (unsafe: it ran tile code outside the sandbox) —
  port the handler to a go/node/python backend (/docs/changes/2026-09-27-cgi-removed.md)
  ```

- `/api/<tile>/…` answers **410 Gone** with that message (as
  `{"error": "<tile>: …"}`); the handler is never executed.
- `bx new --runtime cgi` and `POST /api/xbin/create {"runtime": "cgi"}` are
  refused with the same message.

Nothing else moved: go, node and python backends, static tiles and the
rest of the manifest behave as before.

## Who's affected

Tiles with `"runtime": "cgi"` in their `xbin.json` — typically a shell
script scaffolded with `bx new --runtime cgi`. Find them with `bx doctor`,
or `bx ls | grep MANIFEST-ERROR`, or from a terminal at the workspace
root: `grep -rl --include=xbin.json '"cgi"' .`. None of the builtin tiles,
templates or examples used it.

## How to migrate

Turn the tile into a small long-running backend. Its code then runs in the
tile's sandbox like every other backend: on an isolated workspace that is
the base rootfs's tools (plus the manifest's `setup`), the tile directory
**read-only**, no network unless the owner binds a `net` interface, and
brokered resources for state. A handler that wrote files into its own
directory must write to a resource instead (`filesystem`, `kv`, `sqlite`;
[resources.md](/docs/resources.md)).

**Keep the script — a Go wrapper (smallest change).** The tile's own backend
may run CGI: inside the sandbox that is safe; what was removed is xbind
running it. In the tile directory:

1. In `xbin.json`, set `"runtime": "go"` (`"entry"` can go, unless you had
   changed it; the default Go entry is the `./backend` package).
2. Add `go.mod` (the module name is yours — the tile's basename is the
   convention; the workspace `go.work` resolves the SDK):

   ```
   module old-tile

   go 1.24

   require github.com/xbin-dev/xbin/sdk v0.0.0
   ```

3. Add `backend/main.go` next to your `backend/handler`:

   ```go
   // Runs the old CGI script per request — inside this tile's sandbox.
   package main

   import (
   	"net/http"
   	"net/http/cgi"
   	"path/filepath"

   	xbin "github.com/xbin-dev/xbin/sdk"
   )

   func main() {
   	script, _ := filepath.Abs("backend/handler") // a backend starts in its tile directory
   	xbin.Serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
   		c := xbin.Caller(r)
   		h := &cgi.Handler{
   			Path: script,
   			Dir:  ".", // the tile directory, as before (read-only now)
   			Env: []string{"XBIN_COMPONENT=" + xbin.Self(),
   				"XBIN_FROM=" + c.From, "XBIN_ROLE=" + c.Role},
   			InheritEnv: []string{"PATH", "HOME"},
   		}
   		h.ServeHTTP(w, r)
   	}))
   }
   ```

   The script keeps seeing what it saw: CGI/1.1 env (`PATH_INFO` relative
   to the tile's `/api/<tile>`, `QUERY_STRING`, `REQUEST_METHOD`, body on
   stdin), `XBIN_COMPONENT`/`XBIN_FROM`/`XBIN_ROLE`, headers + blank line +
   body on stdout. The first request after the save builds and starts the
   backend; `bx logs -f <tile>` shows build errors.

**Or rewrite the handler as the backend.** Usually a few lines —
`xbin.Serve(mux)` with `xbin.Caller(r)` for the identity the env vars
carried ([sdk.md](/docs/sdk.md)), or a node/python server reading the
`X-XBin-From` / `X-XBin-Role` headers: `bx new <scratch-path> --runtime
node|python` writes a working skeleton to copy into the tile
(`backend/server.js` / `backend/server.py`; then set the runtime and delete
the scratch tile). A backend can still shell out per request
(`child_process.execFile`, `subprocess.run`) if part of the work is best
left as a script.

## Why

A tile directory is writable from inside sandboxes: its terminals, and the
coding agents working in it. The cgi runtime ran whatever `backend/handler`
held through Go's `net/http/cgi` directly on the host, as xbind — even
under `--isolate`, since only go/node/python backends were sandboxed. So
anyone who could write a tile could run code with the daemon's privileges:
every tile's vault, every user's data, the host. That is exactly what D78
rules out (nothing runs as xbind on data a sandbox can write). Sandboxing a
per-request exec was not worth building for a runtime nothing shipped used;
the long-running runtimes already give a script the same reach, inside the
sandbox. A guard test now fails the build if daemon code imports
`net/http/cgi` again.
