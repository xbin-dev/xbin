# xbin documentation

xbin is a workspace where people and AI agents build the systems a company
runs on, and where those systems run. Each app is a folder: a page, an
optional backend and its own history. Save a change and it is live. Every
app and every agent works in its own sandbox and reaches only what someone
has granted, and the workspace itself (its shell, its admin console) is
built the same way, so it changes like any other app.

These pages are how to build in it, for people and for the coding agents
that work in its terminals. They call the folder you build a *tile* (or
*component*): a directory with an `index.html` and, if it needs one, a
backend that rebuilds when you save (unless the tile paused live reload:
[tile-deployments.md](/docs/tile-deployments.md)). Tiles call each other
through roles they are granted and share storage the workspace provides;
the mental model below has the rest. How a tile should look and read is
[design.md](/docs/design.md).

**New here? Take the guided tour.** [overview/](/docs/overview/00-index.md)
is a top-down walk through the whole system — how the subsystems compose and
why they're shaped the way they are — in 17 short chapters (the model, the
sandbox, identity & authorization, interfaces, egress/ingress, lifecycle,
operations…). The reference docs below stay the field-level truth; the
overview is the map that puts them in context.

**Reading order for builders:**

1. [getting-started.md](/docs/getting-started.md) — install, login, first component
2. [elements.md](/docs/elements.md) — the component contract: directories, manifests, views, runtimes
3. [auth.md](/docs/auth.md) — identities, roles, grants, the vault
4. [resources.md](/docs/resources.md) — kv, blob, bus, cron, sqlite
5. [sdk.md](/docs/sdk.md) — the Go SDK, node/python patterns, and the in-frame `xbin` JS API
6. [design.md](/docs/design.md) — how a tile looks and reads: the workspace's
   design rules (Base Two), both themes, components, words

**Reference:**

- [overview/](/docs/overview/00-index.md) — the top-down system tour (start at `00-index.md`)
- [protocol.md](/docs/protocol.md) — every HTTP/WS endpoint, header, and event
- [isolation.md](/docs/isolation.md) — sandboxes, terminal scoping, the dev layer, egress
- [ingress.md](/docs/ingress.md) — publishing tiles: public HTTP(S) + TCP/UDP endpoints
- [agent-inbox.md](/docs/agent-inbox.md) — the contract between chat adapters
  (Slack, …) and agents built from the agent template
- [sandbox-manager.md](/docs/sandbox-manager.md) — the contract between
  sandbox managers (tiles that run coding sandboxes) and the tiles that use
  them, such as the agent template
- [scm.md](/docs/scm.md) — the contract between scm providers (tiles that
  hold a code host's credentials, such as the `scm-github` template) and
  the tiles that use them: repo credentials, pull requests, issues, CI and
  events
- [bx.md](/docs/bx.md) — the `bx` CLI
- [tile-deployments.md](/docs/tile-deployments.md) — pausing a tile's live
  reload, Reload now, named deployments (`/c/<tile>+<name>/`), deploying and
  promoting, the deploy log, rolling back, protecting the primary, and
  `git fetch xbin-deploy` (tiles that never opt in keep live reload and no
  deploy step)
- [partitions.md](/docs/partitions.md) — partitioned tiles (in
  development): one backend instance per person, the mode a tile records
  and how it switches, shared resources, global and personal binds, and what
  providers key on
- [config.md](/docs/config.md) — every `xbind` flag and `XBIN_*` variable,
  generated from the daemon's configuration
- [changelog.md](/docs/changelog.md) — builder-visible changes per xbind
  upgrade; **BREAKING** entries link migration notes under `/docs/changes/`
- [frontend-kit.md](/docs/frontend-kit.md) — the `/vendor/` modules a tile
  may import (`bx-kit`, `bx-dialog`, `bx-code`, …), the theme's mechanics
  (opting in, tokens, icons), the URL rules, lit pitfalls
- [design.md](/docs/design.md) — the design guide for tiles: calm surfaces,
  one accent, status as glyph + word + colour, square corners, dialogs that
  show the plan before they ask, the workspace's voice, both themes
- [native.md](/docs/native.md) — a tile's native UI in the xbin mobile app:
  `native.js`, the template API, every primitive of the vocabulary, the
  rules, and checking it with `bx lint --native` / `bx preview --native`
- [compat.md](/docs/compat.md) — what a workspace can rely on across xbind
  upgrades (API additive-only, frozen URLs, additive scaffold layouts, CLI
  superset)
- [maintenance.md](/docs/maintenance.md) — for xbin contributors: the guards
  CI runs and the checklists behind them

All of these are served by xbind at `/docs/` in every workspace and live in
the xbin repo under `docs/`. They are plain markdown — readable with `less`
in a terminal just as well as in the browser.

Additionally, every workspace root contains **`AGENTS.md`** (symlinked as
`CLAUDE.md`) — a self-contained builder reference for humans and coding
agents working in workspace terminals: the manifest schema, recipes, SDK
cheat sheets, and the mistakes to avoid, all in one file on disk.

## The 60-second mental model

```
Workspace  = one directory tree, git-versioned
Scope      = a subtree marked by scope.json = "an app"; owns resources
Component  = any directory with index.html and/or xbin.json = "an element"
```

- `<bx-frame src="apps/thing">` renders a component. The 7×7 px button in
  its corner opens a real shell in that component's directory. Save a file →
  the frame reloads; save backend code → it recompiles and swaps live (while
  the tile's live reload isn't paused).
- Components call each other through xbind only, with verified identity:
  callees declare **roles**, callers request them in `uses`, the owner
  approves once. The same grammar covers shared **resources**.
- Terminals are root (the editing plane). Running components are
  least-privileged tenants (the runtime plane).
