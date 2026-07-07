# Template components — design

A **template component** is a component blueprint: it isn't a live tile until
you **instantiate** it, at which point its files are copied into a new,
independently-named component you can build on. Templates let xbin ship
opinionated starting points (the first is a builtin **AI agent**) and let
users author their own reusable blueprints.

This generalizes the existing builtin-tile import: same copy-with-rewrite
machinery, but templates are a first-class *kind* (browsable, instantiable
many times, and authorable inside a workspace — not only embedded).

## What makes a component a template

A `xbin.json` `template` block:

```jsonc
{
  "template": {
    "title": "AI Agent",
    "description": "A blank-slate agentic loop you clone and build up.",
    "defaultName": "agent"      // suggested instance basename
  },
  "runtime": "go",              // the *instance's* runtime, deps, uses, etc.
  "uses": [ … ]
}
```

The presence of `template` marks the component. Consequences (a template is
**not plugged in**):

- **No backend runs.** The runner/proxy refuse a template's `/api/…` with a
  clear "this is a template — instantiate it first" message.
- **Not a live tile.** It's excluded from the shell's normal tile listing and
  from `/api/xbin/components` default view; it appears in a **Templates**
  section instead, with an *instantiate* action rather than *open*.
- Its files are still watched/editable (you author a template like any
  component), and it's git-versioned like everything else.

## Sources

- **Builtin** — embedded under `builtin-templates/<name>/` (parallel to
  `builtin-tiles/`), each a normal component tree whose `xbin.json` carries a
  `template` block. Go backends ship as `go.mod.tile` (go:embed skips nested
  modules), restored on instantiate.
- **Workspace** — any component in the workspace with a `template` block. So a
  user builds an app, adds the marker, and it becomes a clonable blueprint;
  `cp -r` + edit is the authoring flow, or `bx new … --template`.

## Instantiate

`POST /api/xbin/templates/new {source, path}` — copy the template's files to
`path` (a new component), then:

- **Strip the `template` block** from the instance's `xbin.json` (the
  instance is a normal, plugged-in component).
- Rewrite the template's own default path → the instance path in text files
  (self-references: view `<script src>`, its scope's `res:` ids). Cross-tile
  references (to *other* components) are left intact.
- Set a Go backend's module path to the instance path (unique; two instances
  coexist in go.work).
- Never overwrite an existing component; reconcile deps + go.work immediately
  so the instance is usable at once (same as tile import).

`source` is a builtin template name or a workspace template's component path.
The copy machinery is the shared `builtins.CopyTree`, already used by tile
import — templates add only the marker-strip and the two source kinds.

## Surfaces

- `GET /api/xbin/templates` → `[{id, source: "builtin"|"workspace", title,
  description, defaultName}]` (builtin catalog ∪ workspace templates).
- `POST /api/xbin/templates/new {source, path}` (gated by `xbin:writer`,
  like `/create` and tile import).
- `bx template ls | new <source> [as <path>]`.
- Tile Manager gains a **New from template** tab (name → instantiate);
  surfaces the pending grants the new instance needs (as tile import does).

## Updating instances (the `template` remote)

Instances diverge freely after the copy, so the builtin-tile updater (3-way
merge) deliberately skips them — a blind merge would clobber the builder's
work. Instead, updates follow the **fork-upstream** model:

- At start, each builtin template is materialized into a git repo under
  `.xbin/template-repos/<name>/` (a snapshot committed per version) and served
  read-only over dumb HTTP at `GET /api/xbin/templates/<name>.git/…` (admin).
- On instantiate, that repo is added to the instance as the `template` remote
  (URL `http://xbin/api/xbin/templates/<name>.git`).
- The terminal's git is configured so `http://xbin/…` transparently rewrites to
  the reachable `XBIN_URL` with the owner bearer token (term.sandboxEnv), so a
  raw `git fetch` reaches the admin-gated endpoint.
- The builder adopts upstream fixes on their terms, from the instance terminal.

**Existing instances** (created before this shipped) have no `template` remote —
add it once:

```sh
git remote add template http://xbin/api/xbin/templates/agent.git
git fetch template
git log --oneline template/main          # what's new upstream
```

The template repo and the instance have **unrelated histories** and differ in a
few instantiation-specific files (`go.mod` vs `go.mod.tile`, the stripped
`template` block in `xbin.json`), so don't blind-`merge`. Pull the changed code
surgically and review:

```sh
git checkout template/main -- _backend index.html agent.js API.md
# then hand-apply any xbin.json additions (e.g. a new interfaces block),
# skipping the template marker; rebuild is automatic.
git commit -am "adopt upstream agent update"
```

(Or `git merge --allow-unrelated-histories template/main` and resolve the few
conflicts, keeping your `go.mod` and marker-free `xbin.json`.)

## Why not just "another builtin tile"?

Builtin *tiles* (llm-gw, chat) are import-once infrastructure — you want one
`apps/llm-gw`. *Templates* are clone-many blueprints — you might spin up
`apps/support-agent`, `apps/triage-agent`, each its own instance with its own
sqlite/state, diverging freely after the copy. The template kind makes that
intent explicit (named instantiation, not "import"), keeps blueprints out of
the live tile surface, and — crucially — lets templates live in the workspace,
so the agent you build can itself become a template for the next one.
