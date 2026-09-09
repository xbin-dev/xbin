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
10. **Enforcement.** A builder-visible change needs a `docs/changelog.md`
    entry; one that requires an operator action needs a migration note under
    `docs/changes/`; CI verifies what it can. When a silent path has to
    become an error, it warns for one release first.

## What this does *not* promise

- Undocumented internals: `.xbin/` contents, the on-disk shape of
  `data/`, and the names of temporary files may change between releases.
- Behaviour that `docs/changelog.md` marks **BREAKING** with a linked
  migration note under `/docs/changes/` — that note is the one place a
  workspace has to act after an upgrade.
