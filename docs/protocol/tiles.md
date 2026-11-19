# Protocol: tiles — listing, creating, templates, lifecycle

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the component list and tile assets, creating and cloning
tiles, builtins and templates, lifecycle (disable, offload, remove…) and
tile reports.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Components and assets

```
GET    /components                 any. [{path, scope, runtime, hasIndex,
                                   title?, icon? (what the tile calls
                                   itself, D194: the manifest's title —
                                   control and bidi characters out, at most
                                   80 characters — and icon name, absent
                                   when none; show them beside the path,
                                   never instead of it),
                                   state? (lifecycle; absent = enabled),
                                   roles, uses, deps, manifestError,
                                   agent? ("off" | "read": the manifest's
                                   "agent" key, when it narrows what a
                                   person's workspace agent may use of the
                                   tile's API; absent = full),
                                   chrome? (trusted chrome — bx-frame does
                                   not sandbox these: root, shell, shipped
                                   tiles/organisations, admin-approved tiles),
                                   chromeRequested? (the manifest says
                                   chrome: true but no workspace admin
                                   approved it — it runs sandboxed; D118),
                                   sandbox? (extra iframe/CSP sandbox tokens
                                   the tile's grants unlock — cap:open-links
                                   → allow-popups allow-popups-to-escape-
                                   sandbox, ND11; bx-frame appends them),
                                   native? ({entry}: the tile's native app
                                   UI module, tile-relative — its runtime
                                   document is /c/<path>/?native=1; absent
                                   when none, and on chrome),
                                   origin? (--tile-assets=origins only: the
                                   tile's own origin, e.g.
                                   https://t-<id>.tiles.example.com — bx-frame
                                   frames the tile's workspace URL, which the
                                   server sends there, with allow-same-origin
                                   and no credentialless, and talks to it with
                                   that postMessage origin)}]
                                   The entry of a tile with deployments (a
                                   deployment record: live reload paused, or
                                   a deployment beyond main) gains
                                   deployments: {primary, pinned, protected},
                                   the same for every caller who sees it
                                   (pinned: its primary doesn't follow the
                                   work tree; no other deployment's name, no
                                   count); runtime, hasIndex, native, chrome
                                   and template then describe the primary's
                                   code (its checkpoint while pinned), roles,
                                   uses, deps and manifestError the work
                                   tree, and origin is the primary's.
                                   Deployments are never rows.
                                   The entry of a tile whose primary's code
                                   asks for a "partition", or whose
                                   recorded partition mode has one, gains
                                   partition: {state: partitioned |
                                   unpartitioned | pending | invalid, user,
                                   global (the recorded mode), request?:
                                   {user, global, declined} (what the code
                                   asks when it differs: pending, or kept
                                   by a tile manager — declined), note?
                                   (while pending: the code's
                                   partitionNote, trimmed — the tile's own
                                   words, sandbox-writable: show it as
                                   text, attributed to the tile)} and, for
                                   an invalid request, partitionError (also
                                   in manifestError). A tile with a
                                   recorded mode whose code's xbin.json, or
                                   whose mode record, can't be read is
                                   invalid too, with partitionError saying
                                   so (user and global: the recorded mode,
                                   both false when the record can't be
                                   read). Both are absent for every other
                                   tile.
                                   The entry of a tile whose primary
                                   backend is building, starting or failed
                                   gains backend: {state: building |
                                   starting | failed} (D196) — what a
                                   window opened mid-build shows before the
                                   next event; absent while it serves or
                                   sleeps
GET    /components/<path>          any. {component, apiDoc: <API.md text>}
                                   (component as above, native included)
GET    /tile-assets                any (read-filtered); ?component=<p> for one.
                                   The tile asset report:
                                   per sandboxed tile, what strict tile asset
                                   gating refuses — {mode, tiles:[{component,
                                   injectFalse?, files, findings:[{file, line,
                                   col, kind: html-attr|importmap|css-url|
                                   js-import|js-string|inject-false|base-tag|
                                   symlink-escape, ref, target, breaks:
                                   ""|tokens|strict, fix (the relative URL bx
                                   fix assets writes; "" = by hand), note}],
                                   truncated?, breaking:{tokens, origins}}]}.
                                   Only tiles with findings unless
                                   ?component= (404 if unknown/unreadable).
```

## Creating tiles, builtins and templates

```
POST   /create                     owner/admin, a user creating a tile
                                   they will own, or an element granted
                                   target "xbin" at role writer (workspace
                                   management). body {path, runtime?,
                                   title?, expose?, owner?} → {path, files,
                                   owner?}. owner: "org:<id>" creates the
                                   tile OWNED by that org — gated by the
                                   org's Create knob / org admin (elements
                                   still need the xbin:writer capability).
                                   Non-admin humans (directly, or driving
                                   an element) get 403 when the path is
                                   reserved (tiles/…, root, shell, a ':'
                                   in any segment), inside a scope the new
                                   owner doesn't own, or still carries
                                   leftovers of a removed tile (grant
                                   rows, bindings, vault, others' access
                                   entries, a deployment record or
                                   checkpoint store; and, at the path or
                                   under it, a deployment's vault,
                                   registrations or data, a partitioned
                                   tile's partition mode record or its
                                   people's partition data — listed in the
                                   error; the path's owner is exempt);
                                   creation by anyone drops a deployment
                                   record left at the path, so the new
                                   tile starts with plain live reload; D82,
                                   docs/auth.md §Creating tiles. Default:
                                   the human creator becomes
                                   user-owner, admin/automation →
                                   workspace-owned. Under the org-only
                                   tile-creation policy (/defaults, D52) —
                                   or the account's noPersonalTiles (D88) — a
                                   non-admin's "user:" owner is refused and
                                   an empty one resolves to their single
                                   Create org. Same scaffolder as `bx
                                   new`; never overwrites. Clone/imports
                                   take the same owner? and assign the
                                   same default ownership. Every creator
                                   (admins too, every creation route) gets
                                   409 for a path nested with an existing
                                   component, or whose scope data key
                                   ("/" → "~") another scope has — e.g.
                                   apps~x beside apps/x, or workspace
                                   (D118, docs/resources.md). Every
                                   creator, admins included, every
                                   creation route (and `bx new`'s local
                                   write): 403 "can't create <path>: '+'
                                   isn't allowed in tile names (it names
                                   a tile deployment in URLs,
                                   /c/<tile>+<name>/) — pick another
                                   path" for a '+' in any segment (D127j).
                                   A directory already named with '+'
                                   keeps resolving (an exact match wins)
                                   but can't get deployments.
POST   /clone                      same authority as /create (the
                                   ownership path rule; the deputy clamp applies)
                                   + the human must have READ on `from`
                                   (copying is reading). body {from, to,
                                   owner?}
                                   → {path, from, rewritten, pendingGrants}.
                                   Forks a component: copies it (git history
                                   included), rewrites old-path references
                                   across its files, registers it fresh; a
                                   template instance's copy gets its
                                   manifest's merge driver for its own path
                                   (docs/overview/03-components.md §Templates).
                                   Secrets/resource data are NOT copied;
                                   unresolvable uses reject the clone.
GET    /builtins                   any. optional tile catalog
                                   [{name,title,description,defaultPath,installed}]
POST   /builtins/import            same authority as /create, checked on
                                   the resolved target (path? or the tile's
                                   defaultPath). body {name, path?, owner?}
                                   → {path, files, pendingGrants}
                                   — installs an
                                   embedded tile (docs/overview/14-lifecycle.md §Getting code in).
                                   A retired tile (`devbox`) → 410 {error}
                                   saying what replaces it.
GET    /builtins/updates            any. builtins (scaffold + imported tiles) with
                                   a newer embedded version. [{id,installPath,
                                   fromVersion,toVersion,adopted,files:[{path,
                                   status,note?}],clean,conflicts}] (docs/overview/14-lifecycle.md §Keeping code fresh).
                                   A file is read as the file at its path:
                                   a symbolic link (anywhere on the path),
                                   a FIFO, socket or device, a directory, a
                                   file over 32 MiB, or a file inside a
                                   component nested in the unit that the
                                   unit doesn't ship is never followed or
                                   read — it is a "conflict" whose note says
                                   why ("apps/chat/index.html is a symbolic
                                   link: not read, and no update writes it").
POST   /builtins/update             xbin:writer. body {id, mode:
                                   replace|merge|pr|pin|unpin}. replace
                                   overwrites, merge 3-way-merges (git merge-file);
                                   both → {files, notes?} and re-record provenance
                                   eagerly. No mode adds, removes or changes a
                                   tile's mode request, each xbin.json's
                                   top-level "partition" (any key case, as
                                   xbind reads it — docs/partitions.md):
                                   replace writes the INSTALLED value (present,
                                   absent or its list, spliced where the
                                   installed file has it; from the builder's
                                   own side of conflict markers an earlier
                                   merge left; for a file that doesn't parse,
                                   the mode xbind last read from the tile —
                                   and when even that is unknown the manifest
                                   is left as it is and the update stays
                                   offered); merge and "pr" undo upstream's own
                                   change to the key before git sees it, so
                                   the builder's line merges untouched. notes
                                   (absent when empty) name each manifest
                                   where upstream asks otherwise ("…/xbin.json:
                                   partition kept as installed (<ours>; upstream
                                   asks <theirs>): edit it deliberately to
                                   request a switch") — replace whenever it
                                   does, merge and "pr" when upstream changed
                                   the key — and a "pr" proposal's message
                                   says so too. No mode reads or writes
                                   through a link or opens a FIFO: merge
                                   and "pr" leave such a file (and a file
                                   in a nested component) as it is and note
                                   it, and the update stays offered;
                                   replace puts upstream's regular file in
                                   place of a link or a FIFO at a file's
                                   path (never following it) and leaves a
                                   file beneath a link, a directory, or a
                                   nested component's file, noted. Each
                                   file is written beside itself and
                                   renamed in; merge writes a file only
                                   while it is what merge read (a file
                                   changed meanwhile stops the merge there:
                                   400 naming it and the files written
                                   before) and never rewrites a file it
                                   leaves as it is. mode "pr" (D49) writes NOTHING:
                                   the update is filed as a change proposal
                                   against the tile → {pr:{target,number,…}} —
                                   the tile's own plane reviews and `git am`s
                                   it, and provenance refreshes only when the
                                   PR closes merged. When upstream changed
                                   nothing but a partition there is nothing to
                                   propose: "pr" answers {files: [], notes}
                                   instead and records the version as applied
                                   (the tile's files are what a merge would
                                   leave). Idempotent per embed
                                   version; a newer embed auto-withdraws the
                                   stale open proposal. Such PRs carry
                                   kind:"builtin-update" + builtin/toVersion/
                                   toHash in their meta. Never touches
                                   template instances.
GET    /templates                   any. template blueprints (builtin ∪ workspace).
                                   [{id,source,title,description,defaultName,
                                   partition?,partitionSkipped?}] — partition:
                                   the mode new instances start in (the
                                   template block's "partition", e.g.
                                   ["user","global"]); partitionSkipped:
                                   "needs --isolate" when this xbind runs
                                   without isolation and won't write it. Both
                                   absent for a template that names no mode.
POST   /templates/new               same authority as /create on the
                                   resolved target; a workspace-template
                                   source also needs READ. body {source,
                                   path?, owner?, partition?} → {path,
                                   files, pendingGrants, partition?,
                                   partitionSkipped?} — instantiates a template
                                   into a named copy (docs/overview/03-components.md §Templates).
                                   The copy's top-level "partition" is the
                                   template block's (docs/partitions.md) unless
                                   the body says "partition": false or xbind
                                   runs without --isolate; the answer carries
                                   partition (the mode written) or
                                   partitionSkipped ("opted out" | "needs
                                   --isolate"), both absent when the template
                                   names no mode. Without the default the copy
                                   asks for no mode at all. An xbind older
                                   than partitioned tiles refuses the
                                   partition field (400 "need {source, path?,
                                   owner?}"); it never writes a mode, so send
                                   the field only when opting out. A
                                   builtin-template instance gets a read-only
                                   `template` git remote (below), and its repo
                                   is SEEDED from the template's repo (D50):
                                   main = the template snapshot + one commit of
                                   instantiate rewrites — so `git fetch
                                   template && git merge template/main` applies
                                   upstream template fixes cleanly (shared
                                   ancestry; the builder picks what to adopt).
                                   The instance's xbin.json is the template's
                                   JSONC without the block (and the comment
                                   lines above it), its partition the line
                                   after "{" — in a workspace whose template
                                   repo this xbind created, the served one
                                   plus that line. Its repo names the merge
                                   driver for it (merge.xbin-manifest, bx
                                   template merge-manifest: where the line
                                   merge of the template's change conflicts,
                                   xbin.json merges by keys;
                                   docs/overview/03-components.md §Templates).
GET    /templates/updates           authenticated. → {instances:[{path,
                                   template, head, legacy}]} — instances whose
                                   builtin template gained snapshots they
                                   haven't merged (filtered to tiles the
                                   caller can read; all local git plumbing).
                                   legacy = pre-seeding instance (unrelated
                                   history): its FIRST merge needs
                                   --allow-unrelated-histories, then it's
                                   normal.
GET    /templates/{repo}/{rest...}  authenticated. Read-only dumb-HTTP git server for a
                                   builtin template's source repo, e.g.
                                   /templates/agent.git/info/refs. Each instance
                                   has it as its `template` remote, so a builder
                                   pulls upstream fixes: git fetch template &&
                                   git merge template/main. Its xbin.json never
                                   changes the "template" block (instances never
                                   carry it): a repo xbind creates has none (nor
                                   the comment lines above it), one
                                   an older xbind created keeps its own; a
                                   change to the block is a snapshot whose
                                   message says so (an empty commit when nothing
                                   else changed; trailer Xbin-Template-Block).
```

## Lifecycle

```
POST   /lifecycle                  admin, the tile's user-owner, or an
                                   owning-org admin (D24: lifecycle is the
                                   owner's). body {component, state} — component
                                   lifecycle (docs/overview/14-lifecycle.md). state
                                   also takes `hidden` — disabled + kept
                                   out of sidebars/listings until unhidden
                                   (D42; refused while offloaded). state:
                                   enabled | disabled | offloaded | offloaded-full.
                                   A non-enabled backend is not spawned (the proxy
                                   returns 409 + an X-XBin-Lifecycle header);
                                   disabling stops a running backend now. Offload
                                   archives then frees local bytes (data, or +
                                   source/term-env for -full, which ends the
                                   tile's terminal sessions first — 502, nothing
                                   removed, if one won't end); enabling an
                                   offloaded component restores it. Offloading
                                   a partitioned tile is refused (409 "can't
                                   offload …", nothing archived or stopped;
                                   docs/partitions.md). State is in the
                                   overview's component list (state field).
                                   Lifecycle is the tile's: disabling, hiding
                                   or offloading stops every deployment;
                                   enabling starts the primary (an alwaysOn
                                   primary at once), a deployment beyond it
                                   by its own alwaysOn switch or on its next
                                   request. A change emits the bare reload as
                                   before and, for each deployment that isn't
                                   the primary, a deployments event {op:
                                   "reload", deployment}. A qualified name
                                   (<tile>+<name>) has no lifecycle (404).
                                   Offloading archives every deployment's
                                   data before removing anything; beyond main
                                   it frees the kv data, and file resources
                                   stay on disk.
                                   Every state but enabled also stops the tile
                                   sandboxes the tile manages (state kept), and
                                   an offload is refused 409 — nothing archived
                                   — while any of them holds state (an upper, a
                                   disk, a snapshot): offload can't carry it yet.
                                   {ok, state}; enabling an offloaded tile adds
                                   what its restore answers (POST /restore):
                                   deployments, sandboxesSkipped?, and
                                   dataErased? / dataMissing? when a sealed
                                   backup's data couldn't come back.
```

## Tile reports

```
GET    /tile-report                      any signed-in user (read-filtered).
                                         {statuses:{<component>:{level,message,ts}}}
                                         — status tiles reported about themselves,
                                         for the shell's sidebar/tab indicators.
                                         (Distinct from /tile-status = runtime
                                         metrics, protocol/runtime.md.)
POST   /tile-report                      element (self) or owner (?component=).
                                         body {level:ok|info|warn|error, message?,
                                         transient?}. Sets this tile's persistent,
                                         self-clearing status (ok+empty message
                                         clears it); transient=true fires a one-shot
                                         notification instead. Publishes a `status`
                                         event. SDK xbin.Status/Notify; JS
                                         xbin.status/notify. Cleared on backend
                                         restart. Guidelines: workspace AGENTS.md.
                                         From a non-primary deployment's
                                         credentials: the same answer; kept for
                                         that deployment and sent as a
                                         deployments event, op status
                                         {deployment, level, message, ts,
                                         transient}, never `status`; GET
                                         /tile-report never lists it. It clears
                                         when that deployment rebuilds or a
                                         deploy swaps onto it; a reassignment
                                         exchanges it with the primary's.
                                         From a user partition's credentials
                                         (partitioned tiles, protocol.md §Authentication):
                                         kept for that partition, never the
                                         tile's status; its `status` event
                                         carries partition and reaches only
                                         that person (protocol/events.md); GET
                                         /tile-report answers such a caller
                                         its partition's own record for its
                                         tile (none: no entry), and never a
                                         deleted person's to one created
                                         later under the same id. Cleared when
                                         that partition's instance restarts
                                         or the tile's code rebuilds
```
