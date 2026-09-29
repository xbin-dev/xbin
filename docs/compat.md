# Compatibility: what a workspace can rely on across xbind upgrades

A workspace is a directory seeded once (`xbind init`) and then owned by its
builders. Upgrading xbind swaps the binary, **not the workspace**: the
shell, admin tile and every other scaffolded file on disk stay exactly as
they were, and `bx builtin update` offers new scaffold versions only when a
person asks. Workspaces created before 2026-07-03 are *adopted* (no
recorded base, so `--merge` is refused and `--replace` deletes nothing).
That is why the rules below exist: a workspace whose chrome is a year old
must keep working against today's daemon. **Never break users.**

This page is the contract. The repo's `AGENTS.md` points here, and every
change to shipped files, routes, manifests, the CLI, the SDK or boot-time
migrations is checked against it.

## The rules

1. **Old workspaces boot on new binaries.** Boot-time migrations are
   additive and idempotent: running the same binary twice changes nothing
   the second time. The home-directory migration keeps its hard stop when
   both `home/` and `homes/` hold data (turning that into a warning would
   lose a user's home). Boot never records a user's edited scaffold files as
   pristine — that would freeze their chrome forever.
2. **Old chrome keeps working against new xbind.** Nothing in an existing
   workspace changes at boot except the essential `tiles/organisations`
   backfill, so the HTTP API is **additive only**: no route removed or
   renamed, no response field removed or retyped, new fields only. The
   shipped shell ignores unknown fields; an old one must be able to.
3. **Shipped URLs are frozen.** Every `/vendor/<name>` served today stays
   served (shipped documents link `/vendor/theme.css`; third-party tiles
   import `/vendor/bx-frame.js` and friends by absolute path). The import
   map lives in each workspace's `xbin.json`, which no upgrade rewrites, so
   shipped code never introduces a **bare specifier**: shared modules are
   imported by absolute `/vendor/…` URL. A moved file leaves a re-export
   shim at its old URL.
4. **Scaffold layouts are additive.** `bx builtin update` delivers new files
   inside a scaffold unit, but a renamed file orphans the old one in an
   adopted workspace. Entry files (`shell/bx-shell.js`,
   `tiles/admin/admin.js`, `index.html`) keep their names and import new
   siblings relatively; nothing shipped is renamed or removed.
5. **Theme.** Third-party tile documents that never link `theme.css` are
   styled only by the fallback values in `web/bx-*.js`. Fallbacks are not
   removed; when the palette changes their values are regenerated from
   `theme.css`.
6. **CLI is a superset.** Every `bx` invocation accepted today stays
   accepted: interleaved positionals, repeated accumulating flags, `--flag
   value` and `--flag=value`, `--no-x` pairs. Where behaviour changes (an
   unknown flag that was silently ignored starts to error) the release
   carries a changelog line and, for one release, a warning instead of the
   error.
7. **Manifest schema is additive.** Both `expose` (roles) and `exposes`
   (ports) keep working; new keys only; unknown keys are ignored, never
   rejected. The one removal was a security hole, closed at once:
   `"runtime": "cgi"` (and `bx new --runtime cgi`) — such a tile keeps
   serving its files and reports a manifest error
   ([changes/2026-09-27-cgi-removed.md](/docs/changes/2026-09-27-cgi-removed.md)).
   One key fails closed by design: `"partition"`. A word this xbind
   doesn't know (a later `"org"`), or a value that isn't a list, makes the
   tile's request invalid — its backend doesn't run, the manifest error
   says why — rather than run a mode other than the one asked for. Without
   the key nothing changes, and `partitionMail`, `partitionNote` and
   `scope.json`'s `shared` are ignored while no tile of the scope asks for
   partitions. A tile's partition mode changes by itself only while the
   tile holds no data; on one that does, a change pauses the tile until a
   tile manager keeps the mode or switches, deleting its data — nothing
   an upgrade, a `bx builtin update` or a template merge does starts that.
8. **The SDK stays zero-dependency**, and its semantics change only in the
   permissive direction (a call that succeeds today keeps succeeding), with
   a changelog entry.
9. **Every boot-time migration has a fixture test** — `make integration`
   boots an aged workspace (legacy `home/`, no ledger, no provenance, no
   essential tile) twice: the first boot may change only the listed paths
   and never the root `xbin.json`; the second boot changes nothing; a
   `home/` + `homes/` conflict stops the daemon. A new migration extends
   that test's allowed list with its reason.
10. **The native app contract is additive.** Shipped mobile apps lag
    behind xbind by months, so everything a tile's `native.js` and the app
    rely on only grows: the vocabulary (`/vendor/xb/vocab.js` — primitives,
    props, events, tokens, features), the tree format and bridge messages
    the runtime speaks, the exports of `/vendor/xb-native.js`, and
    `xbin.native`. A new prop bumps its primitive's revision; removing or
    re-meaning anything needs a new major version, served side by side
    (below).
11. **Enforcement.** A builder-visible change needs a `docs/changelog.md`
    entry; one that requires an operator action needs a migration note under
    `docs/changes/`; CI verifies what it can. When a silent path has to
    become an error, it warns for one release first — except a security
    hole, which closes in the release that finds it, with a changelog entry
    and a migration note (e.g. D118: `chrome: true` needs an admin's
    approval) — and a character refused in **new** tile names, which is
    refused at once and never touches an existing tile, also with a
    changelog entry and a migration note (D82's `:`; `+`, *Tile
    deployments* below).

## Tile asset URLs (strict tile asset gating)

Tile frontends' files are moving from a credential-less rule to strict,
per-user gating ([auth.md §Tile asset gating](/docs/auth.md)). The daemon
flag `--tile-assets` selects the mode:

- **this release** ships `legacy` as the default — today's credential-less
  rule and injection, unchanged, **except three security fixes** that apply
  in every mode (a security hole closes now, see *The rules* above): a
  symlink in a tile resolving outside the workspace or into `.xbin/`,
  `data/` or `homes/` answers 404 (a tile that served backend output
  through a `data/` symlink must serve it from its API instead); a tile's
  non-document files carry `Content-Security-Policy: sandbox` (inert as
  subresources; an SVG opened directly or framed no longer runs script
  as the workspace); and a tile document fetched — or opened or framed with
  `xbin.url()` — by *another* tile gets no frame token (navigations within a
  tile's own nested pages still do; docs/changes/2026-09-26-frame-tokens.md).
  Plus the strict `tokens` and `origins` modes, the detection (`bx doctor`,
  `GET /api/xbin/tile-assets`), the codemod (`bx fix assets <tile>`) and
  xbin-client's console diagnostics;
- **the next release enforces**: the credential-less rule is deleted, not
  kept behind a flag.

What a tile can rely on across that change: **relative URLs to its own and
other readable tiles' files keep working in every mode**, as do absolute
module imports of its own files and workspace import-map entries. What stops
working under `tokens`: absolute `/c/` URLs in HTML attributes, CSS and a
document's own import map, and `inject: false` documents. The breaking
change is announced in the changelog with a migration note
([changes/2026-09-26-tile-asset-gating.md](/docs/changes/2026-09-26-tile-asset-gating.md));
run `bx doctor` to find affected tiles now. Switching a workspace to
`origins` signs every browser out once (the session cookie is renamed
`__Host-xbin_session`).

## The native app contract

Rule 10 in detail. A tile's `native.js` ([native.md](/docs/native.md)) runs
against whatever xbind serves, and the app that draws it shipped months
before — or after. Both directions keep working because every surface
between them only grows:

| Surface | What stays |
|---|---|
| the vocabulary (`/vendor/xb/vocab.js`) | primitives, props, events and their payloads, child rules, token sets and values, icon names, feature flags — never removed or re-meant. A new prop raises its primitive's `rev` and carries `since`; a new primitive starts at rev 1; a new value an app must support to draw names a feature flag |
| `/vendor/xb-native.js` | its exports and what they do: `html`, `render`, `repeat`, `nothing`, the template syntax and bindings, how keys derive from template positions, `native`, `createRuntime`, `attach`, `applyOps`, `boot`, `VOCAB` |
| `xbin.native` | `caps`, `supports`, `meta`, `copy`, `share`, `open`, `state`, `saveState` |
| the tree and the bridge | nodes `{k, t, p, e, c}`; the `mount` and `patch` messages and their ops; the `meta`, `call`, `state`, `error` and `diag` messages; the app's calls into the runtime (`event`, `visibility`, `resolve`, `frame`, `remount`); the injected `caps` and `state`. Receivers ignore fields and messages they do not know |
| xbind's side | the runtime document `/c/<tile>/?native=1` (and `&preview=1`), `native: {entry}` in `/api/xbin/components`, `native: {runtime: 1}` in `/api/xbin/whoami`, the manifest's `"native"` key and the `native.js` convention |

- **New xbind, older app.** The runtime checks each render against the
  app's `caps`; a tree that needs a primitive, a prop revision or a feature
  flag the app lacks makes the app show the tile's web page instead ("update
  the app for the native view"). A tile that wants older apps to stay native
  branches on `xbin.native.supports()`.
- **Older xbind, new app.** An xbind without `native` in `/whoami` and
  `/components` has no native tiles: the app opens every tile as its web
  page. A newer app keeps speaking an older runtime's version.
- **Breaking** — removing or re-meaning anything — needs a new major `v` of
  the tree format and vocabulary, served side by side with the old one.
  Nothing ships that makes an installed app misdraw an existing tile.
- **Not covered:** the reference renderer (`/vendor/xb/render.js`,
  `preview-host.js`, `fixture.html`) follows the app's look for previews and
  tests; its internals and its pictures change freely (its URLs stay served,
  rule 3).

## Tile deployments and paused live reload

Pausing a tile's live reload and giving it tile deployments
([tile-deployments.md](/docs/tile-deployments.md)) are opt-in per tile, so
the rules above hold for every tile that never opts in, byte for byte: saves
reload live, there is no deploy step, and no existing route's answer, event,
header, env variable, token, backup archive or file differs.

- **No manifest key and no boot migration.** A tile's deployment state lives
  in new files under `data/` and `.xbin/` that only the new routes write,
  starting at the first opt-in; nothing is written for a tile before that
  (rules 1, 7, 9).
- **Additive wire.** New routes (`/api/xbin/deployments…`,
  `/api/xbin/checkpoints/…`), a new event type (`deployments`), new optional
  query parameters (`?deployment=` on the logs, tile-status, frame-token,
  vault, cron, bus-subscription, sandboxes, terminal and agent-session
  routes) and new fields that are absent for a
  tile without deployments; no existing request body gains a field (rule 2).
  Where an older xbind would silently ignore a new parameter, the answer
  echoes it, and new clients check the echo. Existing event types keep their
  shapes and speak only of the tile's primary, with the bare component: a
  non-primary deployment's builds, status and reloads ride `deployments`
  alone, a deploy of a checkpoint reports on `deployments`, never `build-*`,
  and one `reload` follows a swap that changed the code the primary serves.
  Lists keyed by tile keep one entry per tile.
- **A new URL form; the old ones stay (rule 3).** `/c/<tile>+<name>/` and
  `/api/<tile>+<name>/…` resolve only for a tile with deployments, and only
  when nothing answers the path today: an existing directory whose name holds
  `+` keeps resolving. The form lives in paths and JSON bodies only: a query
  string names the tile and `deployment=` apart (a `+` there reads as a
  space), and the routes that take a deployment answer a `tile+name` query
  parameter with 400.
- **`+` is refused in new tile names — the naming exception to rule 11,
  as D82's `:` was.** Creating a tile whose path holds `+` in any segment is
  refused at once, for admins too, on every creation path (`bx new`, create,
  clone, template instantiate, builtin and git import): `<tile>+<name>` is a
  deployment's URL. Existing tiles are untouched: a directory whose name
  holds `+` keeps resolving as an exact match and serving as before; it
  can't get deployments, and `bx doctor` flags it. The changelog marks it
  **BREAKING**, with
  [changes/2026-09-28-plus-in-tile-names.md](/docs/changes/2026-09-28-plus-in-tile-names.md).
- **The SDK changes only permissively (rule 8).** `xbin.Deployment()`,
  `CallerInfo.Deployment`, `xbin.deployment`, `XBIN_DEPLOYMENT`,
  `X-XBin-Deployment` and `<meta name="xbin-deployment">` are absent for the
  primary, so every call that succeeds for a tile today succeeds for its
  primary. A non-primary deployment is a new context someone opted into, and
  there some calls behave differently by design: writes to other tiles are
  read-clamped, edges that can't be clamped are blocked, a tile whose net
  shares the host's gives it no egress, notifications are held, and
  interface instances and ingress hosts are stored dormant — they still
  answer success, so start-up code keeps working (its cron jobs and bus
  subscriptions fire for it). `xbin.self`, `Self()` and resource ids stay the tile
  path in every deployment.
- **Old clients.** An old shell, admin tile, `bx` or app sees a tile with
  deployments as its primary: one row, one card, one status, and a tile with
  live reload paused as a tile that serves and doesn't reload; nothing new is
  required of it. The web shell's reload targeting and the shipped iOS app's
  are replayed against the new event stream as fixtures
  (`hack/events-socket.test.mjs`, the app's `ClientEventsTests.swift`), so a
  browser tab or an app that predates tile deployments is checked, not
  assumed. New clients detect the feature from `GET /api/xbin/deployments`: a
  plain 404 or 405 means an older xbind, and the new `bx` commands exit 6
  against one.
- **Tokens.** A frame token of a deployment other than `main` has a sixth
  field, which an older xbind refuses; `main`'s tokens, and every token of a
  tile without deployments, keep today's shape. The native app contract
  (rule 10) is unchanged: `/c/<tile>/?native=1` and `native: {entry}` in
  `/api/xbin/components` describe the primary's code.
- **Downgrading** loses no state: an older xbind ignores every deployment
  record, checkpoint store, non-`main` data, vault and registration file —
  none of it ever activates there — and serves every tile's work tree with
  live reload again. Reassign every primary back to `main` and align each
  pinned tile's work tree with what it runs first
  ([tile-deployments.md](/docs/tile-deployments.md), *A tile's life,
  backups and downgrades*).

## What this does *not* promise

- Undocumented internals: `.xbin/` contents, the on-disk shape of
  `data/`, and the names of temporary files may change between releases.
  That includes the tile-deployments state — `data/deployments/` (with
  each deployment's registration files), `data/checkpoints/`, the data,
  vault and prefs of deployments other than `main` (the `.deployments/`
  directories under `data/resources-enc/`, `data/vault/` and `data/prefs/`),
  `.xbin/deploy/` and the per-checkpoint builds under `.xbin/build/` —
  whose layout is not a builder contract; an older xbind started on the
  workspace never reads it.
- Behaviour that `docs/changelog.md` marks **BREAKING** with a linked
  migration note under `/docs/changes/` — that note is the one place a
  workspace has to act after an upgrade.
