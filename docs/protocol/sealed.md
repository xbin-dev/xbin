# Protocol: sealed components

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the routes of sealed components — what people see called **system
components** — and what every other route answers when it is asked about
one. What they are and how an admin manages them:
[sealed.md](/docs/sealed.md).

"Sealed" here means the component's code is fixed by the xbin release. It
is unrelated to the vault's sealed state ([auth.md](/docs/auth.md)).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Addresses

A sealed component lives at the path `xbin/<name>`. Nothing in a workspace
can hold or create a component there: `xbin` is a reserved top-level name.

```
GET    /c/xbin/<name>/[file]       its frontend, from the release's own files
                                   (the document injection of every tile page
                                   applies). Read access on xbin/<name>, as
                                   for a tile; a workspace directory at
                                   <workspace>/xbin/… is never served.
ANY    /api/xbin/<name>/<p>        its backend, as /api/<tile>/<p> is a
                                   tile's. No route of xbind's own begins
                                   with a sealed component's name: xbind
                                   refuses to start otherwise.
```

People reach its backend the way they reach a tile's: from its own page,
with the frame token a reader of it gets. **Nothing else calls into it.** It
exposes no roles, provides no interface and publishes no endpoint, so no
grant, binding or ingress route names it as a target, and a tile's backend,
frame or terminal calling `/api/xbin/<name>/…` is answered 403.

## The admins' read

```
GET    /sealed                     admin. {release, components: [Row]} — the
                                   sealed components of this xbind. Row:
                                   {path, name, title, runtime, digest,
                                   build?, state, hold?, backend?, partition,
                                   partitioned, dataFormat, stamp?, slots,
                                   grants, delegate?}
                                   digest: the short digest of the code this
                                   release runs for it; it changes exactly
                                   when an upgrade changes the code.
                                   build: release (the install's prebuilt
                                   backend) | development (a development
                                   xbind compiles it) | none (held).
                                   state: enabled | disabled | hidden.
                                   hold: {code, reason} while it can't run —
                                     no-build         the install has no
                                                      build of its backend
                                     foreign-data     data it didn't write
                                                      sits at its data key
                                     newer-data       its data is in a
                                                      format newer than this
                                                      release reads
                                     stamp            its data stamp can't
                                                      be read or written
                                     needs-sandboxes  it keeps people's
                                                      data apart and xbind
                                                      runs without --isolate
                                   A held component's API answers 502 with
                                   the reason; nothing of it starts.
                                   backend: idle | building | starting |
                                   healthy | failed.
                                   partition: its partition mode ([] = one
                                   instance); partitioned: whether people's
                                   partitions run in this xbind.
                                   dataFormat: the data format this release
                                   reads and writes; stamp {data, release,
                                   digest, at}: what its data was last
                                   written as, and by which release.
                                   slots: [{slot, kind, service?, multi?,
                                   bound: [ref]}] — the interface slots an
                                   admin may bind. grants: [{target, role}]
                                   — what the release grants it. delegate:
                                   it may act for the person whose partition
                                   it is.
```

`/xbin/system` ([core.md](/docs/protocol/core.md)) is the page that shows
this, and `bx system` the command.

## Listings: `?sealed=1`

A shell, an admin console or an app written before sealed components would
offer a terminal, the source and deployments on such a row. So the listings
below leave sealed components out unless the caller asks with `?sealed=1`,
and mark a row `sealed: true` where the row has the field:

```
GET    /components?sealed=1        + rows {path: "xbin/<name>", sealed: true,
                                   …} the caller may read
GET    /auth-overview?sealed=1     + their rows, and the release's grants to
                                   them (approvedBy "xbin")
GET    /grants?sealed=1            + the release's grants to them
GET    /backends?sealed=1          + their backends
GET    /runtime?sealed=1           + their backends, stats and resources
GET    /access-matrix?sealed=1     + their column
GET    /resources?sealed=1         + their resources
GET    /cron/jobs?sealed=1         + their jobs (a component always lists
GET    /bus/subscriptions?sealed=1   its own)
```

`GET /components/xbin/<name>` answers one row without the switch. `GET
/vaults` never lists a sealed component's secrets, and `GET /tile-assets`
reports one only when it is named.

## What an admin may change

Four things, each with the route a tile has:

```
PUT    /access                     {tile: "xbin/<name>", kind, id, level} —
                                   level read, none or "" (remove). write and
                                   terminal don't exist on a sealed
                                   component: 400. A pattern or a permission
                                   set that gives more reads as read.
POST   /access-requests            a person asks for read (400 for more);
POST   /access-requests/approve    an admin approves at read.
POST   /lifecycle                  {component, state} — enabled, disabled or
                                   hidden. offloaded, offloaded-full: 400.
POST   /limits                     as for a tile (docs/isolation.md).
DELETE /limits
POST   /partitions/limits
POST   /bindings                   {component: "xbin/<name>", slot, provider}
DELETE /bindings                   — a slot the release declares for it
                                   (GET /sealed slots). Any other slot,
                                   pseudo-slots (@archive) included: 400.
```

Backups are data only (the code is the release's):

```
POST   /backup                     {component: "xbin/<name>"} — its scope's
                                   data, through the workspace's archiver
                                   (the "*" @archive binding). The manifest
                                   has no source and no terminal layer, and
                                   carries sealedData: the data format the
                                   data was in.
POST   /restore                    its data back. 502 when the archive's
                                   sealedData is above what this release
                                   reads: install the release that wrote it,
                                   or a later one. An older format is
                                   restored; the backend migrates it forward
                                   at its next start.
POST   /partitions/restore         the same check, for a person's partition.
POST   /backup-schedule            as for a tile.
```

People's partitions of a partitioned sealed component are listed, stopped,
reset, purged, backed up and restored as a partitioned tile's
([partitions.md](/docs/protocol/partitions.md)).

## What is refused

Every route that names a tile gets one of these answers for a path under
`xbin/` (and for `res:xbin/…`), whoever asks — admins and the root token
included. The body is `{"error": "<path> is a system component, managed by
xbin: <why>", "docs": "/docs/sealed.md"}`.

**400 — the operation doesn't exist for a sealed component:**

| Routes | Why |
|---|---|
| `GET /code/tree`, `/code/file`, `/git/log`, `/git/diff`, `/git/activity` | it has no source to read: its code is the xbin release's |
| `GET|POST /code/prs`, `GET /code/pr`, `/code/pr/series`, `POST /code/pr/comment`, `/code/pr/state` | it takes no change proposals |
| every `/deployments…` route (`tile`), `GET /checkpoints/…` | it has no deployments and no checkpoints: it always runs the release's code |
| `POST /clone` (`from`, `to`), `/create`, `/builtins/import`, `/git/import`, `/templates/new` (`path`, `source`) | it can't be cloned, and nothing is created under `xbin/` |
| `POST /builtins/update` | it is updated with xbin itself |
| `PUT /chrome` | what it is trusted with is fixed by the release |
| `POST|DELETE /grants` (`from` or `target`, its resources included) | its grants are fixed by the release, and nothing is granted a call into it or its resources |
| `POST|DELETE /bindings` (`provider`, `providers`) | it is never the provider of a binding |
| `POST /owner`, `GET /owner/preview` | it has no owner: workspace admins manage it |
| `PUT /iface-instances`, `/ingress-hosts` | it provides no interface and publishes no endpoint |
| `POST /partitions/mode`, `/partitions/reviewed` | its partition mode and its code are the release's |
| `POST|DELETE /partitions/consents` (`to`) | no tile reaches people's partitions of it |
| `POST /term/sessions` (`cwd`), `GET /term-net` | it has no terminal and no agent sessions |
| `POST /sandboxes`, `DELETE /sandboxes/{name}`, `POST /sandboxes/{name}/stop` (`tile`) | it runs no tile sandboxes |
| `POST /go-build-versions/dismiss` | it is built by the release |

`/ws/term?cwd=xbin/<name>` answers 403 with the same text.

**403 — only the component's own credentials pass:**

| Routes | Why |
|---|---|
| `GET|PUT|DELETE /kv/res:xbin/…`, `/blob/res:xbin/…` | only it reads and writes its own resources |
| `GET|PUT|DELETE /vault/xbin/…` | only it reads and writes its own secrets |
| `POST /bus/publish` (`resource`) | only it publishes on its own bus |
| `PUT /bus/subscriptions`, `/cron/jobs`, `DELETE /bus/subscriptions/{name}`, `/cron/jobs/{name}` | only it registers its own deliveries |

`GET /logs?component=xbin/<name>` is the component's own and the admins'
(403 for anyone else, readers included).

A workspace `xbin.json` that names a sealed component in a grant (as
`from` or `target`), binds a slot the release doesn't declare for it, or
names it as a provider, is read as if those entries weren't there. The
file is never rewritten for it.
