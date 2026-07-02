# Elements (components)

A **component** is a directory. It becomes visible to buxon when it contains
an `index.html` (a view), a `buxon.json` (a manifest), or both. Its
workspace-relative path *is* its identity — `mv` renames it, `cp -r` forks
it, `rm -r` deletes it. There is no registry beyond the filesystem.

```
apps/thing/
  buxon.json      # manifest (optional for pure-static components)
  index.html      # view, rendered in <bx-frame>
  backend/        # backend entry (runtime-specific, see below)
  API.md          # required when exposing roles (see §API contract)
  deps/           # managed symlinks to declared dependencies (don't edit)
  ...anything     # it's just a directory
```

Reserved names you cannot use: component id `buxon`; top-level dirs
`vendor`, `data`, `home`, `.buxon`. Dirs named `deps`, `node_modules`,
`.git`, or starting with `.` are never scanned or watched.

## Manifest — `buxon.json`

JSONC (comments and trailing commas allowed). Everything is optional.

```jsonc
{
  // Backend runtime: "static" (default, no backend), go, node, python, cgi.
  "runtime": "go",

  // Backend entry. Defaults: go "./backend" (a package), node
  // "backend/server.js", python "backend/server.py", cgi "backend/handler".
  "entry": "./backend",

  // Source-level dependencies: materialized as deps/<basename> symlinks so
  // a shell here can read/edit them. Editing-plane only — this grants no
  // runtime call rights (that's "uses").
  "deps": ["lib/ui-kit"],

  // Runtime call rights this component wants (docs/auth.md). Targets are
  // component paths or resources ("res:<scope>/<name>").
  "uses": [
    { "target": "apps/calendar",         "role": "reader" },
    { "target": "res:apps/thing/db",     "role": "writer" }
  ],

  // The callable surface this component offers to others.
  "expose": {
    "roles": {
      // name → human description. Descriptions are REQUIRED — they render
      // in the grant-approval UI and `bx api`.
      "reader": "Read thing data",
      "writer": "Modify thing data"
    },
    // Optional, for custom role names: which roles a role includes.
    // reader/writer/admin ordering is built in (admin ⊃ writer ⊃ reader).
    "implies": { "auditor": ["reader"] }
  },

  // Set false to serve this component's HTML byte-exact, skipping the
  // standard <head> injection. You lose the import map, buxon-client.js,
  // and frame-token attribution (your frontend can then only be called as
  // anonymous). Escape hatch; leave it alone normally.
  "inject": true,

  // Marks this component a TEMPLATE — a blueprint, not a live tile. It runs
  // no backend and isn't openable; you instantiate it into an independent
  // named copy (Tile Manager → "New from template", or `bx template new`),
  // which copies the files and strips this block. See plans/templates.md.
  "template": {
    "title": "AI Agent",
    "description": "A blank-slate agentic loop you clone and build up.",
    "defaultName": "agent"   // suggested instance basename (under apps/)
  }
}
```

Manifest errors don't take the workspace down: the component keeps serving
statically, the parse error shows in `bx ls` / `bx doctor` /
`/api/buxon/components`.

## Views

`GET /c/<component>/` serves the component directory (`index.html` for the
dir itself, correct MIME for everything, `Cache-Control: no-store` — it's a
live system). HTML gets exactly one transform on the way out: buxond injects
into `<head>`:

- the merged **import map** (workspace `buxon.json` `importMap` + scope
  overrides) — so `import { LitElement } from 'lit'` works with no build step
- `<meta name="buxon-component">` and a short-lived frame token
- `<script type="module" src="/vendor/buxon-client.js">` — the in-frame API
  (`buxon.self`, `buxon.fetch`, `buxon.bus`; see [sdk.md](/docs/sdk.md))

Write your view as a plain HTML document. Relative URLs work (you're a real
document in an iframe). Vendored libraries: `lit` via the import map,
anything else you drop into your own component dir.

### `<bx-frame>`

```html
<bx-frame src="apps/thing"></bx-frame>            <!-- auto-height -->
<bx-frame src="apps/thing" height="420px"></bx-frame>
<bx-frame src="apps/thing" no-edit></bx-frame>
```

- **Auto-height**: the framed document reports its size via buxon-client
  (with hysteresis, so no resize loops). Set `height` for a fixed frame.
- **Edit button**: 7×7 px, top-right, 35 % opacity until hover. Opens a
  **floating terminal window** with a shell cwd'd to `src`: it appears
  anchored at the frame's corner, drags by its title bar, resizes by the
  bottom-right handle, and stacks above everything (click brings to front).
  Ctrl+scroll inside adjusts the font size (remembered across terminals).
  Multiple terminal tabs per window; sessions persist server-side when you
  close it — reopening reattaches with scrollback.
- **Live reload**: the most specific mounted frame for a changed path
  reloads — editing `apps/cal/widgets/month` reloads that frame, not the
  whole `apps/cal` frame, when both are mounted.
- **Build errors** render as an overlay with compiler output; cleared by the
  next successful build.

Frames nest. The root page is itself a component full of frames; you can
frame the root inside the root if you enjoy that sort of thing.

## Runtimes & backend lifecycle

Backends serve plain HTTP on a unix socket buxond hands them
(`BUXON_SOCKET`). buxond routes `ANY /api/<component>/<path>` to them,
stripping the prefix (your handler sees `/<path>`).

| runtime | entry default | change behavior |
|---------|---------------|-----------------|
| `go` | `./backend` package | `go build` (workspace go.work, shared cache) → new process → health check → atomic swap → old gets SIGTERM, 30 s drain |
| `node` | `backend/server.js` | restart-on-change (same swap dance, no compile) |
| `python` | `backend/server.py` | restart-on-change |
| `cgi` | `backend/handler` (executable) | executed per request, CGI/1.1; nothing to restart |

Lifecycle facts that matter when writing backends:

- **Lazy start**: nothing runs until the first request (or first save).
- **Blue/green**: in-flight requests finish on the old generation. Requests
  during a rebuild wait for the new one (never connection-refused). A failed
  build keeps the old generation serving.
- **Statelessness pays**: a swap is a new process — keep state in resources
  (kv, sqlite), not memory. Long-lived connections (WS/SSE) to an old
  generation are killed at the 30 s drain deadline; reconnect.
- **Idle reaping**: ~30 min without requests → the process is stopped; the
  next request restarts it (~100–300 ms). Need periodic work? Use a `cron`
  resource, not a sleeping goroutine.
- **Crash loops**: 3 quick exits → marked failed (overlay + `bx status`)
  until you save a change. Logs: `bx logs -f <component>`,
  or `tail -f $BUXON_WORKSPACE/.buxon/log/<key>.log`.
- **Graceful stop**: handle SIGTERM ([sdk.md](/docs/sdk.md) `buxon.Serve`
  does).

Env every backend instance gets:

| Env | Meaning |
|-----|---------|
| `BUXON_SOCKET` | unix socket to listen on |
| `BUXON_COMPONENT` | own path (identity) |
| `BUXON_GATEWAY`, `BUXON_TOKEN` | how to call other elements / buxon APIs (this generation's credential — dies at swap) |
| `BUXON_RES_<NAME>` | each granted resource ([resources.md](/docs/resources.md)) |

## Scopes

A `scope.json` marks a directory as a **scope** — an app boundary:

```jsonc
// apps/thing/scope.json
{
  "resources": { "db": { "type": "sqlite" }, "bus": { "type": "bus" } },
  "importMap": { "thing-ui": "/c/apps/thing/ui/lib.js" }   // merged over workspace map
}
```

Components inside a scope get **auto-approved** grants to that scope's
resources and to each other (declaring in `uses` is still required — the
manifest documents the graph). Everything cross-scope needs one-time owner
approval. Rule of thumb: one scope = one app = one trust unit.

## API contract (the docs standard)

If your component sets `expose`, ship an **`API.md`** in its root. This is a
convention with teeth: `bx doctor` warns when it's missing, `bx api <path>`
and the grants UI render it, and it's how the next builder (human or agent)
integrates with you in minutes. Standard shape (scaffolded by
`bx new --expose`):

1. One-paragraph overview.
2. **Roles** table mirroring the manifest descriptions.
3. **Endpoints**: method, path, minimum role, request/response example.
4. **Bus topics** you publish, if any.
5. **Use it**: a copy-paste `uses` snippet.

Keep it truthful over pretty — it's a contract, not marketing.

## Cross-component code access (editing plane)

- Children are just subdirectories; a shell in the parent sees them.
- `deps` in the manifest materializes `deps/<name>` symlinks (relative, so
  the workspace stays relocatable). Follow them, edit through them, build
  against them.
- Go: buxond maintains a generated `go.work` at the workspace root listing
  every Go component and the buxon SDK — any shell can `go build`/`gopls`
  across the whole workspace. If you hand-edit `go.work`, remove the
  generated-marker line and buxond will leave it alone.
