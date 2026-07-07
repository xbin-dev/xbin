# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-07-07

- auth: the `code` capability now also has a **blanket form** — `uses {target:
  "code", role:"reader"}` grants read-only source access to **every** component
  (for scanners/linters/stats), alongside `code:<component>` for one. Honored
  on `/api/xbin/code/*` and `/git/{log,diff}`.
- docs: corrected the workspace `AGENTS.md` §Sandbox — `deps/` symlinks are
  editing-plane only; their targets are NOT mounted in the backend sandbox
  (matches docs/isolation.md). Read a sibling's source via a `code:` grant, or
  call it over HTTP.

## 2026-07-06

- frontend: tiles can spawn workspace-level UI via the shell — `xbin.dialog(spec)`
  (a data-driven modal that escapes the iframe; resolves `{button, values}`) and
  `xbin.window(spec)` (a floating window framing one of the tile's own sub-paths,
  a real tile frame that uses `xbin.fetch` normally). New core element
  `<bx-dialog>`. Docs: elements.md §Dialogs & windows, sdk.md, protocol.md.

- interfaces/auth: new `code:<component>` capability — grant a component
  read-only access to *another* component's source (files + git log/diff via
  `/api/xbin/code/*`, `/git/{log,diff}`), owner-approved like any cross-scope
  grant. A component always reads its own source; admin reads any.

- **BREAKING** — interfaces: instance path prefixes registered via
  `PUT /api/xbin/iface-instances` are **provider-relative** and
  workspace-absolute (`/api/<self>/…`) registrations are now rejected with
  a 400. Providers that registered absolute paths must re-register.
  → [migration note](/docs/changes/2026-07-06-instance-paths.md)
- interfaces: multi-input slots (`{kind:"http", multi:true}` — a slot binds a
  SET of providers) and provider instances (`provides {instances:true}` +
  runtime registration, bound as `provider#id`). Injection shapes documented
  in [elements.md](/docs/elements.md); multiselect binding UI everywhere.
- interfaces (admin UI): http bind options are filtered by the slot's
  `service` contract (an `s3basic` slot no longer offers an `openai`
  provider).
- chat tile: binds multiple `llm` providers (model picker aggregates all) and
  any number of `mcp` servers — Model Context Protocol tools are offered to
  the model and executed in an agent loop. MCP providers serve
  Streamable-HTTP JSON-RPC at `/mcp` under themselves (`service: "mcp"`).
- llm-gw tile: multiplexes multiple named upstream backends; model ids become
  `<backend>/<model>` once more than one is configured; per-backend usage
  (reqs, tokens in/out, in-flight) shown on the tile.
- shell: per-tile ⚙ mini-admin on card title bars (admins), sidebar folders /
  collapse / resize, and a system-status footer (cpu, mem, disk, services,
  vault state, req/s). `GET /api/xbin/status` grew `host` and `traffic`
  gauges.
- workspace: `POST /api/xbin/clone {from,to}` forks a component (git history
  kept, old-path references rewritten); Tile Manager grew a clone tab.
  Prefer it over `cp -r`.
- terminals: `IN_SANDBOX=1` / `IS_SANDBOX=1` set in sandboxed terminals;
  terminal panes use a distinct `--bx-term-bg` theme token.
- vault: admin-tile barrier UI (seal state, first-time passphrase, unseal,
  rekey), `POST /api/xbin/vault-rekey`, `bx vault rekey`, and a red
  vault-sealed banner on the admin overview.
- auth: `PATCH /api/xbin/auth-settings` can disable owner-token *browser*
  login once an admin user exists (`bx` Bearer token unaffected).
- terminals: Ctrl+W now reaches the shell as word-erase instead of closing the
  browser tab (best-effort — some browsers reserve it); overlapping floating
  windows get a higher-contrast edge.
- terminals: exiting the shell now closes that terminal (its tab, or the
  window if it was the last one) instead of leaving a dead "[session ended]"
  pane; terminal tabs can be renamed (double-click).

## 2026-07-05

- **BREAKING** — the project renamed **buxon → xbin**: module
  `github.com/magik6k/xbin`, daemon `xbind`, env `XBIN_*`, API `/api/xbin/*`,
  headers `X-XBin-*`, manifest `xbin.json`, runtime dir `.xbin/`, JS global
  `xbin`. Workspaces are migrated on upgrade; external tiles need the same
  rename (see the bx-term-tile migration for the pattern).
- resources: encryption at rest is **always on** — filesystem/sqlite/blob are
  per-resource gocryptfs mounts, kv is envelope-encrypted; sealed vault ⇒
  resource unavailable + component held, never silent plaintext
  ([resources.md](/docs/resources.md)).
- terminals: sessions are persistent server-side (reattach with exact
  scrollback); component terminals are scoped read-only outside the
  component's own dir + `$HOME`.
- shell: tiles can be unpinned into floating windows (persisted per user).
- imports: a component whose `uses` reference nonexistent resources is
  rejected at import time instead of silently landing broken.
