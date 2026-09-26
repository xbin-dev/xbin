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
   rejected.
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
    become an error, it warns for one release first.

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

## What this does *not* promise

- Undocumented internals: `.xbin/` contents, the on-disk shape of
  `data/`, and the names of temporary files may change between releases.
- Behaviour that `docs/changelog.md` marks **BREAKING** with a linked
  migration note under `/docs/changes/` — that note is the one place a
  workspace has to act after an upgrade.
