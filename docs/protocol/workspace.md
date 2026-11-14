# Protocol: workspace settings

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
per-person preferences, screens and sidebar folders,
branding, the native runtime switch, workspace settings, chrome approval
and the defaults for new users.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Preferences

```
GET    /prefs                     the caller's per-(user×tile) prefs object
GET    /prefs/<key>               one pref value (arbitrary JSON) | 404
PUT    /prefs/<key>               set it (body = JSON value)
DELETE /prefs/<key>               remove it
                                  (each principal reads/writes only its own
                                   bucket; the shell stores layout here; a
                                   tile credential acting in a deployment
                                   beyond main keeps a bucket per user and
                                   deployment, which doesn't follow a
                                   reassignment of the primary. Writes to
                                   one bucket are serialised — concurrent
                                   writes of different keys all land. A
                                   successful PUT/DELETE publishes a
                                   `prefs` event to the bucket owner's own
                                   clients; an optional request header
                                   `X-Prefs-Writer: <id>` (≤64 chars) is
                                   echoed in it as data.writer, so a client
                                   can skip its own writes. Two keys of the
                                   shell's own bucket are the person's
                                   appearance (D184), which xbind reads into
                                   every document it serves them:
                                   `theme` "light" | "dark" (absent or
                                   "system": follow the system) and
                                   `density` "comfortable" (absent:
                                   compact); any other value reads as the
                                   default. Another is the shell's
                                   `dismissed` (D188): the grant requests
                                   and interfaces to bind the person
                                   dismissed, {grants:{"from|target|role":
                                   at}, bindings:{"component|slot|kind":
                                   at}} (docs/elements.md §bx-grants).
                                   Only chrome writes that bucket —
                                   a tile's PUT lands in its own)
```

## Screens

```
GET    /screens                   signed-in. {default: {tiles,blocks?,
                                   mode?,arrange?}|null,
                                   org: [{id,org,name,edit,mode?,tiles,
                                   blocks?,arrange?,rev,updatedBy,
                                   updatedAt,canEdit}],
                                   folders: {"ws":
                                   {folders,rev,updatedBy,updatedAt,canEdit},
                                   "org:<id>": {…}}} — the ws default
                                   screen, your orgs' screens, and the
                                   shared sidebar folder sets you may see
                                   (ws always; each org you belong to;
                                   ws-admins: every org) (D37/D55)
PUT    /screens/default           ws-admin. {tiles, blocks?, mode?,
                                   arrange?} — the seed screen new users
                                   start from (opaque; its mode and
                                   arrange seed theirs, D193)
PUT    /screens/org               {id?,org,name?,edit?,mode?,tiles?,
                                   blocks?,arrange?,rev?,force?}
                                   — create (no id; org admin) → {ok,id,
                                   rev:1,…}; a tiles update follows the
                                   screen's edit knob (admins|write|
                                   members) and carries `rev`, the
                                   revision it was based on: stale → 409
                                   {error, rev, screen} unless force:true;
                                   no rev = legacy overwrite. No tiles =
                                   meta-only (name/edit/mode; org admin),
                                   which never bumps the revision (D55).
                                   mode: "canvas"|"fixed"|"doc"|"tiled",
                                   how the shell arranges the screen
                                   (D187, D193; anything else → 400);
                                   absent in GET = canvas, and a PUT
                                   without it keeps the stored one.
                                   blocks: the screen's headings and text
                                   (D192) — a JSON array, the shell's
                                   shape [{id,kind,text,level?,x,y,w,h,
                                   doc?}], opaque here (not an array →
                                   400). Content like tiles: the same
                                   edit knob, `rev` and 409; a save of
                                   tiles and/or blocks bumps the
                                   revision; a PUT without blocks keeps
                                   the stored ones (so a shell from
                                   before D192 never wipes them);
                                   blocks:[] clears.
                                   arrange: the screen-level layout
                                   (D193) — a JSON object, the shell's
                                   shape {docWidth?, tiling?}, opaque
                                   here (not an object → 400). Content
                                   like blocks: the edit knob, `rev` and
                                   409, bumps the revision, kept by a
                                   PUT without it; arrange:{} clears
DELETE /screens/org               org admin. {id, org}
PUT    /screens/folders           {scope:"ws"|"org:<id>", folders:[…], rev,
                                   force?} — replace one owner section's
                                   curated sidebar tree (ws: ws-admin;
                                   org: its admins). rev 0 for a scope's
                                   first save; stale → 409 {error, rev,
                                   folders}; folders:[] clears (D55)
```

## Branding, native runtime, workspace settings, chrome, defaults

```
GET    /branding                  authenticated. {title, icon, hasIcon} — the
                                   workspace's branding (D76): title replaces
                                   "workspace" in the shell header and the
                                   tab title ("<title> · xbin"); icon is a
                                   data: URI that replaces xbin's mark as the
                                   favicon and logo on the workspace page and
                                   the sign-in / invite pages. Empty = xbin's
                                   own. The shell reads it at boot and on the
                                   `branding` event
PUT    /branding                  admin. {title?, icon?}: each present key
                                   is a whole-value replace ("" clears), an
                                   absent one is left alone. title ≤ 64 chars;
                                   icon a base64 data: URI of image/svg+xml |
                                   png | jpeg | webp | x-icon whose bytes
                                   match, ≤ 256 KiB (body ≤ 512 KiB) → the
                                   full view; publishes `branding`; audited.
                                   Kept in data/branding.json — readable while
                                   the vault is sealed, so the sign-in page
                                   shows it
GET    /native-runtime            authenticated. {enabled, runtime,
                                   version} — the workspace's native-runtime
                                   switch: runtime is whoami's native.runtime
                                   (version while enabled, 0 while off)
PUT    /native-runtime            admin. {enabled: bool} → the same view.
                                   Off: whoami says native {runtime: 0,
                                   disabled: true} (the app opens tiles as
                                   web pages) and /c/<tile>/?native=1
                                   answers 410 with the reason (&preview=1
                                   still served). Kept in users.json;
                                   publishes `native`; audited
GET    /workspace-policies        an alias (v0.3.66's route, D180): the
                                   partitioned tiles' two switches of GET
                                   /workspace-settings, from the same store.
                                   A person (session, device, or a terminal
                                   or agent session they drive, any
                                   deployment) or admin; other tile
                                   principals (frames, instances, cron, bus)
                                   403. {schema: 1, partitionConsent,
                                   credentialResetConfirm}; while one can't
                                   be read, 500 (admins get the reason)
PUT    /workspace-policies        an alias (D180): admin.
                                   {partitionConsent?,
                                   credentialResetConfirm?} — PUT
                                   /workspace-settings with those keys only
                                   (at least one; any other key, base
                                   auto-update's included, is 400) → the
                                   alias's view; publishes
                                   `workspace-settings` and `policies`;
                                   audited with each setting's old→new

GET    /workspace-settings        authenticated; the partitioned tiles'
                                   keys only for a person (session, device,
                                   or a terminal or agent session they
                                   drive) or admin — other tile principals
                                   get the rest. {baseAutoUpdate,
                                   partitionConsent?, credentialResetConfirm?,
                                   errors?, error?} — the workspace settings
                                   (D175, D180), by topic.
                                   Terminals: baseAutoUpdate (default true):
                                   a tile's terminal layer built on an older
                                   base image moves to the current base at
                                   its next session start (everything
                                   outside the workspace files and $HOME is
                                   reset: installed packages, /etc, /var,
                                   /opt…, a VM terminal's disk; a running
                                   session keeps its base until it ends);
                                   false: it stays, and the terminal window
                                   offers the base update.
                                   Partitioned tiles (PD-55), both default
                                   false: partitionConsent — a partitioned
                                   tile uses another partitioned tile's
                                   data of a person only with that person's
                                   consent; credentialResetConfirm — an
                                   admin-set sign-in link, password or SSO
                                   email for someone holding partitions
                                   waits for them to confirm, or 24 h after
                                   they are notified.
                                   errors: {key: why} for each setting xbind
                                   can't read (the file not a JSON object, a
                                   value not true or false, a partitioned
                                   tiles' key spelled in another case) —
                                   people get a generic reason, admins the
                                   file's; such a setting takes its
                                   fail-safe value: base auto-update off, a
                                   partitioned tiles' switch the last value
                                   xbind read, or on. error: base
                                   auto-update's, as D175 gave it. Admins
                                   see a `workspace-settings` alert until
                                   the file is fixed by hand
PUT    /workspace-settings        admin. {baseAutoUpdate?, partitionConsent?,
                                   credentialResetConfirm?: bool}: each
                                   present key replaces its setting, an
                                   absent one is left alone; an unknown key
                                   or no key is 400 → the full view; 500
                                   without writing while a setting can't be
                                   read. Publishes `workspace-settings`
                                   {baseAutoUpdate, changed: [keys]} (open
                                   terminal windows re-read GET /ws/term/env
                                   on it; a console re-reads GET) and, when
                                   a partitioned tiles' switch is set,
                                   `policies` (what clients before D180
                                   follow); audited with each setting's
                                   old→new.
                                   Kept in data/workspace-settings.json,
                                   each setting by its exact key, whose
                                   other keys (a newer xbind's) are kept;
                                   an older xbind ignores the keys it
                                   doesn't know. The partitioned tiles'
                                   switches were v0.3.66's
                                   data/workspace-policies.json: imported on
                                   the first read after the upgrade, and
                                   written there too at each change, for a
                                   downgrade (one that file gets from an
                                   older xbind is imported again)
GET    /chrome                    admin. {tiles: [{path, requested,
                                   approved, shipped?, chrome, missing?}]} —
                                   every component whose xbin.json says
                                   chrome: true and every approved path
                                   (D118). chrome = effective (runs
                                   unsandboxed); missing = approved, no
                                   component there now. root/shell (implicit
                                   chrome) are not listed
PUT    /chrome                    admin. {path, approved: bool} → that row.
                                   Approving needs a component at the path
                                   and applies while its xbin.json says
                                   chrome: true: its documents then run with
                                   the session cookie, acting as whoever
                                   opens it — approve only a tile whose every
                                   writer you trust like the shell. 400 for
                                   root/shell; 404 approving a missing path.
                                   Kept in users.json (a leftover a new tile
                                   at the path would inherit); publishes
                                   `grants` for the tile; audited
GET    /defaults                  admin/xbin:users. {defaultTiles:
                                   {pattern: level}, newUsers: {tiles,
                                   termApi, termNet, orgs: [{org, level,
                                   create}], noPersonalTiles, noTerminal,
                                   sets, netSets, canCreate (deprecated,
                                   ignored — D82)}, tileCreation:
                                   any|org-only, personalDefaults: {sets,
                                   netSets}}. personalDefaults = the live
                                   personal plane every non-admin gets on
                                   top of their own sets, for the tiles
                                   they own (D88; a network change
                                   restarts every personal net tile). defaultTiles = the live
                                   visibility baseline every user gets
                                   (D27). newUsers = what every NEW account
                                   starts with, copied onto the row at
                                   creation — admin-added, invited, or SSO
                                   auto-provisioned — as a UNION with the
                                   request (never admin; D52). tileCreation
                                   = whether non-admins may own tiles
                                   personally — the knob that restricts
                                   personal creation (org-only: they may only
                                   create org-owned tiles — an unspecified
                                   owner resolves to their single Create
                                   org, several → they must name one, none
                                   → refused; admins unaffected)
PUT    /defaults                  admin/xbin:users. each present key
                                   replaces that setting wholesale (absent
                                   = untouched); default orgs must exist.
                                   → the resulting defaults
```
