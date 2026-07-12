# AGENTS.md — building in this xbin workspace

You are inside a **xbin workspace**: a self-modifying browser workspace
where every piece of UI is a directory ("component"/"element"), every
directory can have a live backend, and everything you see — including the
root page — is editable from shells like the one you're probably in.
This file is the complete builder reference. The long-form docs are served
by the daemon; fetch any of them with:

```sh
curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/docs/index.md?raw=1"
# also: getting-started.md elements.md auth.md resources.md sdk.md protocol.md bx.md changelog.md
```

**After an xbind upgrade** (or when a previously-working API starts failing),
read [`/docs/changelog.md`](/docs/changelog.md): builder-visible changes land
there, and **BREAKING** entries link a migration note under `/docs/changes/`
that says exactly what to change in your tile.

## The model in five lines

```
Workspace = this directory tree, served by xbind, git-versioned
Scope     = subtree with scope.json = an app; owns resources; a trust unit
Component = dir with index.html and/or xbin.json; its PATH is its identity
Frontend  = /c/<path>/ (iframe'd by <bx-frame>)   Backend = /api/<path>/…
Terminals (you) are root; running components are least-privileged tenants
```

`mv` renames a component and `rm -r` deletes it. To fork one, prefer
`POST /api/xbin/clone {"from","to"}` (or the Tile Manager's clone tab) over
bare `cp -r`: clone also rewrites old-path references (manifest `res:` uses +
hardcoded strings in code) and registers the copy as its own component.
Saving any file live-reloads the frontend and rebuilds/swaps the backend.
There is no deploy step and **no JS build step — ever** (plain ES modules +
import maps).

## Terminal environment

| Env | Meaning |
|-----|---------|
| `XBIN_WORKSPACE` | workspace root (this file lives there) |
| `XBIN_COMPONENT` | component this terminal was opened on |
| `XBIN_URL` | xbind, e.g. `http://127.0.0.1:8642` |
| `XBIN_TOKEN` | this terminal's **tile-scoped** token — acts as THIS component, never the owner; `bx` and curl use it |
| `HOME` | `<workspace>/homes/<user>` — per-user, contained, persistent; seeded `.zshrc`/`.bashrc` (not the host home) |

Your API identity is **this component** (plans/terminal-tokens.md):
`XBIN_TOKEN` is a per-session token resolving to this tile's element
principal — admin of *this* component (its API, vault, resources) plus
whatever `uses`/bindings the owner approved for it. It is **not** the owner:
admin endpoints, other tiles' admin surfaces, and grant approval all 403.
Approving grants is a human step in the browser. Same rules as the running
component — see §Auth.

**Terminal scope:** a terminal opened on a component can write **only that
component's own directory and `$HOME`** — the **entire rest of the workspace is
read-only** (other components' source, workspace files like `xbin.json`/
`AGENTS.md`/`go.work`). So you can read siblings for deps/patterns and API
integration, but can't touch anything outside your component; a rogue agent
can't break the environment. The platform's secrets are **not even readable**:
`.xbin/` (tokens), `data/` (vault, resource state), and other users' `homes/`
are masked out — use the resource/vault APIs, not the raw files.
**Each component is its own git repo**, so `cd` into it and `git commit` works
even though the root is read-only. To edit a *different* component, open a
terminal on it. There is **no root terminal** (disabled): workspace-wide work —
creating components, workspace `xbin.json`, the workspace-root repo — happens
in the browser (shell right-click → *Create a new tile*, Tile Manager, admin
tile) or from the host shell. Writing outside your component fails with
`Read-only file system`.

**Multi-user:** xbin can have human users with per-tile permissions (admins:
all tiles + terminals + user mgmt; regular users: only allow-listed tiles, no
terminal). Manage them with `bx user ls|add|set|rm` or the admin console's
Users tab. A terminal can only be opened on a tile the user may use, and its
API token is scoped to that tile. Full model: docs/auth.md §multi-user.

## Recipes

(Workspace-wide recipes — creating components, installing tiles, admin `bx` —
need the **browser UI** (shell right-click → *Create a new tile*, Tile
Manager, admin tile) or a **host shell**: a tile terminal's filesystem and
token are scoped to its own component.)

**Create a static component and show it:**

```sh
mkdir -p apps/thing && $EDITOR apps/thing/index.html
```

It appears in the shell sidebar immediately (click to open as a card). To
**pin** it, add `<bx-frame src="apps/thing"></bx-frame>` inside the
`<bx-shell>` in `root/index.html` — or frame it from any other component.

**Create a Go backend component:** `bx new apps/thing --runtime go --expose`
(scaffolds manifest, view, `backend/main.go`, `go.mod`, `API.md`). Other
runtimes: `node`, `python`, `cgi`. Never overwrites existing files.

**Install a bundled optional tile:** `bx tile ls` lists builtin tiles
(e.g. `llm-gw` an OpenAI-compatible gateway, `chat` a streaming chat UI);
`bx tile import <name> [as <path>]` copies one in (or use the Tile Manager's
Import tab). Imported tiles bring their own `uses` — cross-scope grants land
pending for the owner. Sharing model + roadmap: `plans/tile-sharing.md`.

**Update copied builtins:** the scaffold (shell, manager/admin tiles) and
imported tiles are copies you own; a newer xbind can carry newer versions.
`bx builtin updates` lists what changed; `bx builtin update <id> [--replace|
--merge]` applies it (or the Tile Manager's Updates tab). Merge is a 3-way
`git merge-file`, so your customizations survive; everything's in git either
way. Template instances are forks and aren't tracked. Design:
`plans/builtin-updates.md`.

The same scaffolder is exposed as `POST /api/xbin/create`
(`{path, runtime?, title?, expose?}`) — that's what the **Tile Manager**
tile (`tiles/manager`, pinned on the root page) uses. Programmatic creation
from an element requires the workspace-management capability: a grant on the
reserved target `xbin` at role `writer` (the template pre-approves it for
tiles/manager; check `bx grants`).

**Workspace-management capabilities** ride the reserved `xbin` target:
`xbin:writer` = create components; `xbin:admin` = full administration
(read/write any vault, manage any grant/cron, read system state) — held by
the **Admin console** tile (`tiles/admin`). `xbin:admin` is the heaviest
grant in the system; grant it only to tiles you trust as yourself, and
revoke to disarm. Admin-capable endpoints: `/auth-overview`, `/vaults`,
`/resources`, `/status`, `/backends`, and the grants/cron/any-vault
operations (docs/protocol.md, docs/auth.md).

**See it work / debug it:**

```sh
curl -s -H "Authorization: Bearer $XBIN_TOKEN" $XBIN_URL/api/apps/thing/hello
bx status                 # backend states: building | healthy | failed (+error)
bx logs -f apps/thing     # backend stdout/stderr, per generation
bx doctor                 # manifest errors, missing API.md, dangling deps, …
bx ls                     # all components
```

**Commit policy (agents: follow this):** **each component is its own git repo**,
so commit **inside your component** — `cd` into it and `git commit`. Commit
**often and on your own initiative** — after any meaningful change (a working
component, a fix, a refactor that builds), not in big batches. **Never ask the
user whether to commit**; committing is your job, and git is the safety net that
makes every edit reversible. Keep commits small with a short imperative message
(`fix overflow`).

```sh
cd $XBIN_WORKSPACE/apps/thing && git add -A && git commit -m "…"
```

This per-component repo is what makes components **installable/shareable** (push
to GitHub, `git pull` to update — the Admin console shows a component's remote,
the Tile Manager shows update tags). xbind creates the repo when a component is
scaffolded/imported/instantiated; you just commit into it. Its `.xbin/`, `data/`
(and `node_modules/`) are runtime, not source — leave those to xbind and the
backup system. History/diffs are in the Admin tile's component **code & history**
drill-in (click a component in the overview).

## Component anatomy & manifest

```
apps/thing/
  xbin.json     # manifest (JSONC — comments allowed)
  index.html     # view (optional)
  backend/       # backend entry (optional)
  API.md         # REQUIRED if you set "expose" (bx doctor enforces)
  deps/          # machine-managed symlinks — never create/edit by hand
```

Full manifest reference (all fields optional):

```jsonc
{
  "runtime": "go",              // static(default) | go | node | python | cgi
  "entry": "./backend",         // defaults: go ./backend, node backend/server.js,
                                //   python backend/server.py, cgi backend/handler
  "setup": "apt-get update && apt-get install -y ruby",  // extra backend deps →
                                //   cached env layer, built once (see §Extra deps)
  "deps": ["lib/ui-kit"],       // SOURCE visibility: deps/ui-kit symlink appears.
                                //   Editing-plane only; grants no call rights.
  "uses": [                     // RUNTIME call rights you want (see §Auth):
    { "target": "apps/calendar",          "role": "reader" },   // another element's API
    { "target": "res:apps/thing/db",      "role": "writer" }    // a resource
  ],
  "interfaces": {               // typed deps the OWNER binds (see §Interfaces):
    "net": { "kind": "net" },                       // outbound network (see §Sandbox)
    "llm": { "kind": "http", "service": "openai" }  // a service endpoint
  },
  "expose": {                   // what others may be granted on YOU
    "roles": {                  // name → description (description REQUIRED)
      "reader": "Read thing data",
      "writer": "Modify thing data"
    },
    "implies": { "auditor": ["reader"] }  // only for custom role names
  },
  "inject": true                // false = serve HTML byte-exact (rarely wanted)
}
```

Scopes: put a `scope.json` at an app's root dir to declare resources and
import-map overrides:

```jsonc
// apps/thing/scope.json
{ "resources": { "store": {"type":"filesystem"}, "bus": {"type":"bus"},
                 "kvx": {"type":"kv"}, "files": {"type":"blob"},
                 "cron": {"type":"cron"} } }
```

Reserved names — do not create components named/under: `xbin`, `vendor`,
`data`, `home`, `homes`, `.xbin`. Dirs named `deps`, `node_modules`, `.git`, `.*`
are never scanned.

## Frontends

Your HTML is served at `/c/<path>/` inside an iframe. xbind injects into
`<head>`: the import map (`import {LitElement, html, css} from 'lit'` just
works, vendored/offline), identity metas, and `xbin-client.js`, which gives
every component document:

```js
xbin.self                                  // "apps/thing"
await xbin.fetch(`/api/${xbin.self}/x`)   // ALWAYS use xbin.fetch for /api/ —
                                            // raw fetch to other elements 403s
xbin.bus.on(`res:${xbin.self}/bus/`, (topic, data) => {…})   // live events
await xbin.bus.publish(`res:${xbin.self}/bus`, 'changed', {…})
xbin.events.on(e => {…})                   // reload/build/bus/grants stream

// per-user UI state (server-side, follows the user across devices); each tile
// gets its own bucket, isolated per user:
await xbin.fetch(`/api/xbin/prefs/mykey`, {method:'PUT', body: JSON.stringify(v)})
const v = await (await xbin.fetch('/api/xbin/prefs/mykey')).json()

// dialogs & pop-out windows — your tile is an IFRAME, so a modal or window you
// render yourself is clipped to your card. For UI that must float over the
// whole workspace, ask the shell to spawn it:
const res = await xbin.dialog({            // trusted modal from plain DATA
  title:'Rename', message:'New name?',     //   (no HTML — a tile can't inject
  fields:[{name:'v', value:cur}],          //    markup into chrome), un-clipped
  buttons:[{label:'Cancel',value:null},{label:'Save',value:'ok',primary:true}]})
if (res.button === 'ok') save(res.values.v)  // {button, values}; button null = dismissed

const win = xbin.window({ path:'compose', title:'Compose', width:620, height:440 })
win.closed.then(refresh)   // win.close() to close; a floating window whose body
                           // is /c/<self>/compose/ — YOUR OWN sub-page, a real
                           // tile frame that talks to your backend with the
                           // usual xbin.fetch. (spec.src frames another
                           // component instead, same RBAC as a <bx-frame>.)
```

`xbin.dialog` is for confirm/prompt/small forms (falls back to an in-frame
modal when there's no shell); `xbin.window` is for rich pop-out UI. The window
and the card are separate documents (no shared JS memory) — coordinate through
your backend / bus / kv, which they share. Never try to render your own HTML
in the shell: dialogs are data-only, windows are sandboxed sub-frames. No grant
is needed to spawn either, but every dialog is **labelled with your component**
(you can't impersonate system chrome), and you're capped to one dialog + a few
windows at a time — don't rely on stacking modals.

**Never hardcode your own install path in code.** Use `xbin.self` (and
`` `res:${xbin.self}/…` `` for your own resources/bus topics) in the frontend,
`XBIN_COMPONENT` / `xbin.Self()` / `XBIN_RES_*` in the backend. Your literal
path belongs in exactly one place — `xbin.json` `uses` (plus genuine
references to *other* tiles) — which clone/import rewrite. A tile that
hardcodes itself elsewhere breaks the moment it's forked or installed under
a different name.

Embedding other components:

```html
<bx-frame src="apps/other"></bx-frame>             <!-- auto-height -->
<bx-frame src="apps/other" height="400px"></bx-frame>
```

(`bx-frame`/`bx-grants` are importable via `import '/vendor/bx-frame.js'`
etc.) Frames live-reload on save; backend build errors overlay the frame
with compiler output until the next good save.

**Theme & shell.** The workspace look is a light, dense theme defined by
CSS tokens in `/vendor/theme.css` — link it and use `--bx-bg/-panel/
-panel-2/-border/-text/-muted/-accent/-green/-amber/-red/-radius/-shadow/
-font/-mono` (plus `body.bx` base and the `.bx-label` small-caps class).
Match it in your components; override tokens per document to retheme. The
entire workspace layout (top bar, sidebar, card canvas) is the **`shell/`
component in this workspace** — `<bx-shell>` in `shell/bx-shell.js`,
composed by `root/index.html`. Edit it like any component; shells nest
(`shell/index.html` is a working nested preview). Sidebar dots encode
runtime: gray static, blue go, green node, amber python, red cgi. Canvas
cards drag by their title bar into a column layout (drop to reorder within a
column or move between columns); column count follows the canvas width.

**Tile sizing (design constraint).** A card column is **≥ 700px wide** (the
shell fits `floor(canvas / 700)` columns), and a tile can also be opened full
page. Design every tile to be fully usable at **700px wide** and to **reflow,
never scroll horizontally** — use relative units, flexbox/grid, `max-width:
100%` on media, and wrap any inherently wide content (tables, code, diagrams)
in its own `overflow-x:auto` box so the tile body itself never overflows
sideways. Horizontal scroll on a tile is a bug; avoid it at all cost.

## Backends

Contract: plain HTTP server on the unix socket `$XBIN_SOCKET`. xbind
routes `ANY /api/<your-path>/<p>` to you with the prefix stripped. Backend
process env: `XBIN_SOCKET`, `XBIN_COMPONENT`, `XBIN_GATEWAY` +
`XBIN_TOKEN` (this generation's credential for outbound calls),
`XBIN_RES_<NAME>` per granted resource.

Go (SDK `github.com/magik6k/xbin/sdk`, resolved by the generated
`go.work` — just `require` it, no replace needed):

```go
import xbin "github.com/magik6k/xbin/sdk"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items", list)                       // any granted role
	mux.Handle("POST /items", xbin.RoleFunc("writer", add)) // role-guarded
	xbin.Serve(mux)                                         // socket + SIGTERM drain
}

c := xbin.Caller(r)              // verified {From, Role, Owner} — trustworthy
resp, _ := xbin.Client().Get("http://xbin/api/apps/calendar/events") // outbound
kv := xbin.KV(xbin.Resource("kvx"))   // Get/GetJSON/Put/PutJSON/Delete/List
db, _ := sql.Open("sqlite", xbin.Resource("db")+"?_journal_mode=WAL") // sqlite: a
    // FILE PATH from XBIN_RES_DB. Just open it — xbind binds the resource dir
    // rw, so a fresh db (and its -wal/-shm) persists. Never invent a path.
secret, _ := xbin.Secret("api-key")    // own vault only
_ = xbin.Publish(xbin.Resource("bus"), "changed", payload)
```

node/python: no SDK needed — listen on `process.env.XBIN_SOCKET` /
`os.environ["XBIN_SOCKET"]`, read `X-XBin-From`/`X-XBin-Role` headers,
call outbound via the `XBIN_GATEWAY` unix socket with
`Authorization: Bearer $XBIN_TOKEN`. `bx new` scaffolds working skeletons.
cgi: any executable; CGI/1.1 env + `XBIN_FROM`/`XBIN_ROLE`; response on
stdout.

Lifecycle facts you must design around:

- **A save = a new process.** Keep state in resources (kv/sqlite), not RAM.
- Lazy start; idle-reaped after ~30 min (next request revives, ~200 ms).
  Periodic work ⇒ `cron` resource, never a sleeping loop.
- Blue/green swap: in-flight requests finish; long-lived WS/SSE die at the
  30 s drain — clients must reconnect.
- 3 fast crashes ⇒ marked failed until you save a change. `bx logs` first.
- Handle SIGTERM (the SDKs/skeletons do).

## Sandbox — what your backend can reach (isolation is on by default)

Each backend runs in its own sandbox (namespaces + an overlay rootfs; `make dev`
and production isolate). **Design for this — it's default-deny:**

- **Filesystem:** you see the base rootfs (toolchains), your own component dir
  (read-only at runtime — editing is the terminal's job), and your granted
  resource files (rw). **Not** other components' source, other vaults,
  `homes/`, or the host — they aren't mounted. Persist state in resources, not
  scattered files.
  - `deps/` is **editing-plane only**: the symlinks let shells/gopls see your
    dependency components, but their *targets are NOT mounted in the backend*.
    At runtime, call another component over HTTP (§Auth), or read its source
    with a `code:` grant (below) — don't expect `deps/x` to resolve.
- **Network egress is an owner-bound interface (§Interfaces).** With no egress
  bound your backend has **zero IP egress** — outbound calls fail (`dial tcp:
  lookup … connection refused`). Reaching **xbind and other components** through
  the SDK/gateway always works (that's not IP egress). To reach the outside,
  declare a `net` interface and let the owner pick what provides it:
  `"interfaces": { "net": { "kind": "net" } }` → owner binds it to `internet`
  (public only, never LAN/RFC1918), `host`, `lan:<cidr>`, or a **provider tile**
  (a VPN/firewall/router your traffic routes through). `bx bind <you> net=internet`
  or the admin Interfaces tab; the binding is owner-authorized (don't self-bind)
  and **restarts your backend**. (There is no `net:*`-in-`uses` egress grant —
  egress is *only* this interface, so the owner can always reroute you without a
  code change. `bx` and the SDK reach xbind over the gateway with or without it.)
- **GPUs are a grant too.** Request `gpu:all`, `gpu:<index>`, or `gpu:<uuid>` in
  `uses` (role `egress`); the granted GPU's device nodes + NVIDIA driver appear
  in your sandbox with `CUDA_VISIBLE_DEVICES` set. Pair with `setup` for the CUDA
  userland (`"setup":"pip install torch"`). Owner-approved; ungranted = no GPU.
  A terminal can pick a GPU per session from its window menu (owner plane).
- Same-scope resources (`res:<your-scope>/…`) are auto-granted; declare them in
  `scope.json` and request in `uses`.
- See **§Interfaces** below for the richer wiring model (`net` providers, `http`
  service dependencies).

## Extra deps: `setup` + the terminal dev sandbox

The base rootfs has go/node/python + common tools. For **anything else your
backend needs** (a Ruby runtime, imagemagick, a gem/pip package…), add a
**`setup`** script to `xbin.json` — a freeform shell script run once at build
time into a cached environment layer the backend then gets read-only:

```jsonc
"setup": "apt-get update && apt-get install -y --no-install-recommends ruby && gem install --no-document sinatra"
```

Rebuilt only when `setup` changes; runs with internet access in a sandbox. Make
it a **safe, reproducible supply chain:**

- **Update first** — start with `apt-get update` (consider `apt-get upgrade`) so
  installs resolve and pick up security fixes.
- **Pin and verify** — pin versions (`ruby=1:3.2*`, `gem install foo -v X`),
  prefer integrity-checked installs (`npm ci`, `pip install --require-hashes`,
  `bundle install` with `Gemfile.lock`, `cargo --locked`), and verify
  checksums/signatures for anything you download. **Avoid unpinned `curl … | sh`.**
- Keep it minimal and deterministic (prefer distro/registry packages) so the
  cached layer is stable and auditable.

Your **terminal** is a separate **persistent, resettable dev sandbox per
component** (`.xbin/term/<component>/`): `apt install`, dotfiles, and toolchains
you set up in it survive across terminal sessions (your workspace files and
`$HOME` persist independently). It does **not** auto-inherit a component's
`setup` layer — install what you need for interactive work in the terminal
itself. "Reset sandbox" (⟲ in the terminal window) wipes it back to clean.

## Interfaces — typed, swappable dependencies (plans/interfaces.md)

An **interface** is a typed capability slot: a component **requests** slots
(`interfaces`), builtins or tiles **provide** them (`provides`), and the **owner
binds** each request to a provider. The binding is the authorization (owner-only;
you can't self-bind, same rule as grants) — unbound means no capability.

- **`http` — a service dependency you call.** When your component needs a service
  that has a *standard shape* (an LLM, object storage, email, …), **request an
  interface instead of hard-coding a provider**:
  `"interfaces": { "llm": { "kind": "http", "service": "openai" } }`. The owner
  binds it to whatever tile provides that service; you discover the endpoint at
  runtime — `xbin.iface('llm').url` in a frontend, `$XBIN_IFACE_LLM_URL` in a
  backend — and the binding is also your call grant. This makes providers
  **swappable** (Ollama ↔ a cloud proxy) with no code change.
- **Providing a standard API?** Declare it so others can bind you:
  `"provides": { "openai": { "kind": "http", "service": "openai", "role": "writer" } }`
  (`role` = the exposed role a binding grants callers).
- **Many inputs on one slot** (http only): add `"multi": true` to a request
  slot when you genuinely want a *set* of providers (a comm agent binding
  Slack + N email accounts on one `channels` slot). You must opt in — you
  then get `$XBIN_IFACE_<slot>` as a JSON array
  `[{provider, instance?, url, service}]` (backend) / `xbin.iface(slot)` as
  `{service, multi, endpoints}` (frontend), and `provider#instance` is the
  stable per-channel key. Without `multi` a slot binds exactly one provider.
- **Many outputs (instances)**: a provider serving several accounts/profiles
  of one contract declares `"instances": true` on the provide and registers
  them at runtime — `PUT /api/xbin/iface-instances {"instances": {"<id>":
  "/m/1"}}` (replaces the map; re-register when config changes). Paths are
  **provider-relative**: xbind injects `/api/<you><path>` into consumers, so
  register `"/m/1"`, **never** `"/api/<self>/m/1"` (rejected 400 — it would
  double the prefix, and persisted install paths go stale on rename/clone).
  Each instance binds as `provider#id` and presents itself like any provider
  — instance-unaware requesters connect to one unchanged (the injected URL
  routes into your API at that sub-path; serve e.g. `GET /m/{acct}/…`).
- **`net` — L3 egress through a provider tile.** `"interfaces": { "net": { "kind":
  "net" } }`, bound to a builtin (`internet`/`host`) or a provider tile
  (`"provides": { "egress": { "kind": "net" } }`) — a firewall/VPN/router your
  traffic routes through. A provider is a real Linux router in its sandbox
  (`ip_forward` + `nftables`/`wg`); it's also a client of its own egress.
  **If you BUILD a net provider** (you `provide` a `net`/kind:net interface),
  declare `"uses": [{ "target": "cap:net-admin", "role": "writer" }]` — the
  sandbox drops network-admin capabilities from every backend by default, so
  without this grant your dataplane setup fails with *"operation not
  permitted"* (`ip_forward`, `ip route`, `AF_PACKET`). It's **admin-only** to
  approve and lands pending on import; it keeps CAP_NET_ADMIN/NET_RAW/
  NET_BIND_SERVICE inside your own netns only (docs/auth.md, plans/interfaces.md).

**Before you build:** look for an existing interface/service contract to reuse
(check `bx iface` / the admin Interfaces tab); reuse a standard `service` name
(`openai`, …) so your component is interchangeable; define a *new* service
contract only for a genuinely new standard API — and if you expose one, `provide`
it. Declare `interfaces`/`provides`; leave **binding to the owner** (`bx bind
<comp> <slot>=<provider>` or the admin Interfaces tab).

## Auth (read docs/auth.md before building multi-app systems)

- **Callee declares roles** (`expose.roles`, with descriptions), and ships
  an **`API.md`** documenting endpoints per role + a copy-paste `uses`
  snippet. `bx api apps/other` shows you how to integrate with anything.
- **Caller requests** in `uses`. Same scope → auto-granted. Cross-scope →
  pending until the owner approves: `bx grant apps/me apps/other:reader`
  (role goes after the LAST colon; also the panel on the root page).
- **Read another component's source**: request `uses {target:"code:apps/x",
  role:"reader"}` (one component) or `{target:"code", role:"reader"}` (ALL
  components — for scanners/linters/stats). Once approved,
  `GET /api/xbin/code/tree` / `/code/file` / `/git/log` / `/git/diff`
  `?component=<path>` return files + history (read-only; the same view a
  terminal has). You always read your own source; a sibling's needs the grant.
  There is no filesystem mount for this — use the API.
- **Agents cannot approve cross-scope grants — by construction.** Declaring
  `uses` is your job; *approving* a cross-scope (or `xbin:*`) grant is the
  owner's call — the human-in-the-loop the whole permission model exists
  for. Your terminal token is scoped to this tile, so `bx grant` (and every
  other admin endpoint) 403s here. Add the `uses` entry, then tell the user
  it's pending and let them approve in the grants panel. Same-scope grants
  need no approval — nothing to do.
- xbind verifies identity on every call and injects `X-XBin-From` /
  `X-XBin-Role` — never verify auth yourself; never trust a role you
  didn't receive in those headers.
- Role convention: `reader` / `writer` / `admin`, implication downward.
  On bus resources, `subscriber`/`publisher` alias reader/writer.
- **Vault** = per-element private secrets: `bx vault set apps/thing key`
  (value via stdin keeps it out of history); code reads its own via
  `xbin.Secret`. No cross-element vault access exists — wrap shared
  secrets behind a role-guarded API instead. Never put secrets in source,
  manifests, or env files. Encryption at rest is via a seal/unseal barrier
  (`bx vault status|unseal|seal`); when sealed, secret reads/writes 503
  until an admin unseals. See docs/auth.md §vault.
- Prefer **service APIs over shared state** across scopes: "email reads
  calendar" is a reader grant on calendar's API, not a shared db file.

## Resources (docs/resources.md)

Declare in `scope.json` (or workspace `xbin.json` `resources` for
`res:workspace/<name>`), request in `uses`, address as
`res:<scope-path>/<name>`. Granted ⇒ env `XBIN_RES_<NAME>`.

| type | what | access |
|------|------|--------|
| `filesystem` | a **rw directory**, same-scope only | `XBIN_RES_<N>` (= `xbin.Resource("<name>")`) is a **DIRECTORY** path — put a db, files, a cache, anything, and it persists + is backed up. Don't write anywhere else — outside it is a throwaway overlay (lost on restart, not backed up). cross-scope: expose an API instead |
| `sqlite` | a `filesystem` resource pre-pointed at a `.sqlite` **file** | `XBIN_RES_<N>` is the file path (open with WAL). Same rw-dir mechanism as `filesystem` — use `filesystem` if you need a general directory rather than one db |
| `kv` | namespaced kv (≤1 MiB values) | SDK `xbin.KV` or `/api/xbin/kv/res:…/<key>` |
| `blob` | file store (≤256 MiB/write) | `/api/xbin/blob/res:…/<path>` |
| `bus` | at-most-once pub/sub | publish: SDK/HTTP; subscribe: frontend `xbin.bus.on` (backends: use cron to sweep, not subscriptions) |
| `cron` | scheduled POSTs to your own endpoints | `PUT /api/xbin/cron/jobs {"name","resource","schedule","path","role"}`; wakes idle backends; make handlers idempotent |

Bus is a change-notification, not a queue: truth lives in kv/sqlite,
offline subscribers miss messages.

## bx cheat sheet

```
bx ls | status | doctor
bx new <path> [--runtime go|node|python|cgi] [--expose]
bx logs [-f] <component>
bx api <component>                      # roles + API.md of anything
bx grants
bx grant [--revoke] <caller> <target>:<role>
bx vault ls|get|set|rm <component> [key] [value]
bx cron ls
```

Everything bx does is plain HTTP (`/api/xbin/…`, docs/protocol.md) — curl
with `Authorization: Bearer $XBIN_TOKEN` works for all of it, at this
terminal's scope (this component + its grants). Admin/cross-tile operations
need an admin in the browser, or bx on the host.

## Conventions & common mistakes

- **Ship `API.md` when you expose roles.** It's the integration contract;
  `bx doctor` flags its absence. Keep it truthful.
- **Use `xbin.fetch` in frontends** for any `/api/` call. Raw `fetch` to
  another element = 403 by design (identity attribution).
- **Don't hand-edit**: `deps/` (symlinks are reconciled from the manifest),
  `go.work` (generated — has a marker line; removing the marker takes
  ownership), the `grants` array in workspace `xbin.json` (use `bx grant`;
  the file is machine-rewritten and comments there don't survive).
- **Don't touch `data/` paths directly** except a sqlite path you were
  handed via env. Broker state layout is not API.
- **Don't store state in process memory** across requests you care about —
  swaps and reaps will eat it.
- Keep components self-contained: relative asset URLs inside your dir,
  shared code via `deps` + (for Go) workspace go.work packages.
- Editor droppings (`*.swp`, `*~`) and `node_modules` are ignored by the
  watcher; a save is visible within ~300–500 ms, Go swaps in ~1 s.
- After structural changes (new scope.json, renamed components), give the
  scanner a beat, then `bx doctor` to confirm the workspace is coherent.
