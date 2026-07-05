# buxon documentation

buxon is a self-modifying workspace: every piece of UI is backed by a
directory you can open a shell into and edit live — including this
workspace's own root page. Components have real backends (Go, node, python,
shell) that hot-reload on save, declare roles other components can be
granted, and share brokered resources (kv, blobs, bus, cron, sqlite).

**Reading order for builders:**

1. [getting-started.md](/docs/getting-started.md) — install, login, first component
2. [elements.md](/docs/elements.md) — the component contract: directories, manifests, views, runtimes
3. [auth.md](/docs/auth.md) — identities, roles, grants, the vault
4. [resources.md](/docs/resources.md) — kv, blob, bus, cron, sqlite
5. [sdk.md](/docs/sdk.md) — the Go SDK, node/python patterns, and the in-frame `buxon` JS API

**Reference:**

- [protocol.md](/docs/protocol.md) — every HTTP/WS endpoint, header, and event
- [isolation.md](/docs/isolation.md) — sandboxes, terminal scoping, the dev layer, egress
- [bx.md](/docs/bx.md) — the `bx` CLI

All of these are served by buxond at `/docs/` in every workspace and live in
the buxon repo under `docs/`. They are plain markdown — readable with `less`
in a terminal just as well as in the browser.

Additionally, every workspace root contains **`AGENTS.md`** (symlinked as
`CLAUDE.md`) — a self-contained builder reference for humans and coding
agents working in workspace terminals: the manifest schema, recipes, SDK
cheat sheets, and the mistakes to avoid, all in one file on disk.

## The 60-second mental model

```
Workspace  = one directory tree (one host — typically a VM), git-versioned
Scope      = a subtree marked by scope.json = "an app"; owns resources
Component  = any directory with index.html and/or buxon.json = "an element"
```

- `<bx-frame src="apps/thing">` renders a component. The 7×7 px button in
  its corner opens a real shell in that component's directory. Save a file →
  the frame reloads; save backend code → it recompiles and swaps live.
- Components call each other through buxond only, with verified identity:
  callees declare **roles**, callers request them in `uses`, the owner
  approves once. The same grammar covers shared **resources**.
- Terminals are root (the editing plane). Running components are
  least-privileged tenants (the runtime plane).
