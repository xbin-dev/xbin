# Partitioned tiles: one instance per person

> **Status: in development.** The model on this page is settled; xbind is
> gaining it in stages. Until this line changes, don't rely on a tile being
> partitioned: an xbind that doesn't run partitioned tiles treats
> `"partition"` like any manifest key it doesn't know and runs the tile as
> one ordinary instance ([§Older xbinds](#older-xbinds)). The builder side is
> in place — the Go SDK's partition functions and `xbin.partition` in the
> in-frame client ([§In your code](#in-your-code)) — and reads nothing on an
> xbind that doesn't partition. Items marked **TODO** aren't built yet.

A **partitioned tile** keeps the people who use it apart. It declares, in
its `xbin.json`:

```jsonc
"partition": ["user"]            // one backend instance per person
"partition": ["user", "global"]  // … plus one "global" instance
```

xbind then runs **one backend instance per person** who uses the tile — that
person's **user partition**, named `user:<id>` — instead of one instance for
everybody. The partitions share the tile's code, its build, its grants and
its global binds; each one has its own:

- data: every resource of the tile's scope, unless the scope declares it
  shared ([§Shared resources](#shared-resources));
- vault;
- cron jobs and bus subscriptions;
- backend log and status;
- terminal layer;
- personal binds ([§Bind types](#bind-types-global-and-personal)).

With `"global"`, the tile also runs its **global instance**: one instance,
like the tile's instance without partitions, serving everything that doesn't
act for a person (below). It keeps its data, vault and registrations at
today's keys — which a switch to partitions empties, so the global instance
starts empty too ([§The mode](#the-mode-set-while-empty-then-switch-or-keep)).
Without `"global"` only people's partitions run: the tile has no public
surface, and only partitioned tiles can call it.

A person's partition starts on their first use and stops when idle, like any
backend. Partitions run only on the tile's primary deployment, and only on
an xbind started with `--isolate` (each partition is its own sandbox). A
non-primary deployment ([tile-deployments.md](tile-deployments.md)) runs one
instance, as today, reachable by the tile's writers; when its code asks for
partitions that instance runs as `global` — whether or not the primary is
partitioned — so every writer who opens the deployment shares it.

## Who reaches which partition

xbind decides the partition a request reaches from its verified credential —
never from the URL, and never from a header the caller controls:

| The caller | Reaches |
|---|---|
| a person: their session, and the tile's frames, terminals, agent sessions and path tickets they open | **their own** partition, if they can read the tile. A workspace admin too: an admin reaches their own partition, never someone else's |
| a user partition's backend calling its own tile | its own partition |
| `user:alice`'s partition of another partitioned tile, holding a grant on this one | `user:alice` of this tile, if alice can read it — and, when the workspace asks people first, has allowed it ([below](#calls-between-partitioned-tiles)) |
| any other tile, the root token, a public request through a published endpoint | the **global** instance; without one, 403 (503 for a public request) |
| xbind's cron and bus deliveries | the partition that registered them |
| an admin viewing the workspace as a person | no partition: view-as never opens a person's partition. It reaches the global instance only by an explicit call to it ([below](#the-global-instance-and-peoples-partitions)) |

A person who can no longer read the tile, is disabled or is deleted stops
reaching their partition, and its cron jobs and deliveries stop with them.

## Declaring it

| `partition` | Meaning |
|---|---|
| absent | not partitioned (every tile today) |
| `["user"]` | user partitions only; callers that don't act for a person are refused |
| `["user", "global"]`, in any order | user partitions plus the global instance |
| anything else: `[]`, `["global"]` alone, duplicates, an unknown word, not an array | **invalid**: no backend runs (409 naming the reason) and nothing is recorded or deleted |

Unknown words fail closed on purpose: a later kind of partition makes an
xbind that doesn't know it run no backend rather than a wrong one.

A `partition` request is also **invalid** (the same 409, nothing recorded)
when:

- the tile uses its scope's resources but doesn't **root its scope**: the
  resources belong to the scope, which another tile roots;
- another tile inside the partitioned scope asks for a **different** list:
  a scope's resources are partitioned one way for all its tiles;
- the tile is a chrome tile or a `vm` tile, or it holds (or asks for)
  `xbin`/`xbin:*`, `cap:sandboxes`, `cap:net-admin` or `cap:containers` —
  approving such a grant for a partitioned tile answers 409 too;
- a resource of the scope is `"shared": "read"` with type `sqlite`
  ([§Shared resources](#shared-resources)).

A template's own `partition` isn't checked: its instances carry it
([§The mode](#the-mode-set-while-empty-then-switch-or-keep)).

Some things apply to a partitioned tile's global instance only, so without
`"global"` they do nothing: `alwaysOn`, `exposes` (ingress), a `net → host`
binding, a net provider or a provider splice. People's partitions get only
relayed egress under the tile's egress policy
([§How people's partitions run](#how-peoples-partitions-run)). A static tile may declare
`partition`, with a warning: it has no backend to partition.

Two optional keys go with it. `"partitionNote": "…"` (at most 280
characters) is the tile's own words, shown as plain text under xbind's on
the page a paused tile's frame shows when a mode switch is requested — say
what its data is, and what a switch would lose. `"partitionMail": "/path"`,
where xbind rings the global ↔ person mail doorbell, is **TODO**,
documented when it is built.

## The mode: set while empty, then switch or keep

The manifest can be changed by anyone who can write the tile's code, by a
rollback to an older checkpoint, or by a typo. So `partition` doesn't decide
the mode on its own. xbind **records** a tile's mode, and routing follows
only the record:

- **While the tile holds no data, the manifest sets the mode at once** —
  a new tile, a fresh instance of a template, or a tile without resources,
  vault or registrations. Nothing is asked.
- **Once the tile holds data, a change is a mode-switch request.** Nothing
  of the tile's primary runs while it is pending: its frames show xbind's
  page ("A partition mode switch is requested for `<tile>`. All data in
  this tile will be deleted for the switch to happen"), its API answers
  409, and the shell shows an alert. A **tile manager** — a workspace admin,
  the tile's owner, or an admin of the organisation that owns it, in their
  own session ([auth.md](auth.md): no tile credential decides, whatever
  grants its tile holds) — then decides:
  - **Keep the current mode.** The decision is recorded, the tile runs in
    its recorded mode again, and nothing is deleted. The code keeps asking,
    and managers keep seeing the request with a Switch action.
  - **Switch and delete all data.** After the manager types the tile's path,
    xbind stops the tile and deletes every data namespace, vault,
    registration and partition of it, and erases them from backups. It
    then records the new mode; people's partitions start empty on first
    use, and everyone whose partition was deleted is told.

  Code that goes back to the recorded mode withdraws the request.
- **Adding or removing `"global"`** on a partitioned tile goes through the
  same request and confirmation but doesn't wipe the tile: adding it starts
  an empty global instance; removing it deletes only the global instance's
  data and the shared resources. People's partitions stay.

**Where it shows, and where managers decide.** While a switch is pending:

- the tile's documents are xbind's page, in every shell and in the app —
  the switch (for example `unpartitioned → user`), what it deletes (all
  data in the tile will be deleted for it to happen; when `"global"` comes
  or goes, nothing or only the global instance's data), the tile's
  `partitionNote`, and who decides; the tile's API answers 409
  ([protocol.md](protocol.md)). The page is what a browser or the app
  loads to show the tile — its frame, a navigation, the primary's
  deployment URL; a script's fetch of the tile's HTML, or a tool reading
  it with a token, still gets the file;
- `GET /api/xbin/alerts` carries a `partition-switch` alert for admins and
  the tile's readers, which the shell shows as its top banner (and a
  `partition-invalid` alert, the same way, for a tile whose `partition`
  can't run);
- the tile's managers get a push notification (kind
  `tile.partition-switch`) when the request opens — at most one per tile
  every 15 minutes, however often the code toggles `partition`.

Managers decide with `bx partition switch <tile>` or `bx partition keep
<tile>` ([bx.md](bx.md)), or `POST /api/xbin/partitions/mode`. A switch
first shows what it deletes — data namespaces (the tile's own and every
deployment's), people's partitions, vault keys, cron jobs, bus
subscriptions, interface instances and ingress hosts, bytes, backup keys —
and what it keeps: the code (the tile directory, checkpoints, deployment
records), grants and bindings, the tile's own terminal layer, people's
homes and their own agent-session history, records a provider keeps (such
as sandboxes at a sandbox manager — clean them up there), and backups made
before backups were sealed, which no key erases. The manager types the
tile's path; xbind stops the tile, deletes, erases the data's backup keys,
records the new mode (with what it deleted, in the tile's mode history)
and writes a line in the tile's deploy log. A switch that fails part-way —
erasing the backup keys included — records nothing and can be retried. A
switch to user partitions needs xbind's `--isolate`. A tile at the path
`workspace` never counts or deletes the workspace-level resources, whose
keys its own would share.

A promote or roll back of a tile's primary to code that asks for another
mode is a request like an edit: its dry run warns that the tile will pause
for a partition-mode decision.

A tile **holds data** when xbind's own stores have anything of it: a data
namespace with content (a file, a kv key, a blob — an empty sqlite file
counts), a vault key, a cron job, bus subscription, interface instance or
ingress host registration, or a partition, its mail or a personal bind.
Volumes that are provisioned but empty don't count.

Templates and updates never switch a mode:

- **A template** asks for the mode of **new** instances in its `template`
  block (`"template": {"partition": […]}`), never at its top level.
  Instantiating writes it as the instance's own top-level `partition`, and
  the fresh instance, which holds no data, starts in that mode at once (the
  record names who created it). You can opt out when you create one: untick
  **Keep each person's data apart** on the Tile Manager's template card,
  run `bx template new <source> --no-partition`, or send `"partition":
  false` to `POST /api/xbin/templates/new`
  ([protocol.md](protocol.md)). Without `xbind --isolate` the default isn't
  written at all (the box is off, "needs --isolate"), since people's
  partitions need isolation. The block is stripped from instances, so `git
  merge template/main` can't add `partition`: an upstream change to the
  default touches lines the instance dropped and shows up as a conflict.
- **`bx builtin update`** (replace, merge or a proposal) keeps each
  `xbin.json`'s **installed** `partition` — present, absent or its list, in
  whatever case you wrote the key — and changes nothing else about it.
  Replace puts your value where your file has it; merge and proposals never
  take upstream's change to the key, so your line (comment included) merges
  as it is. After a merge that left conflict markers, a replace reads your
  side of them; for a manifest that doesn't parse at all it writes the mode
  xbind last read from the tile, or, when that isn't known either, leaves
  the file alone and keeps the update offered. When upstream asks for
  something else, it prints `partition kept as installed (…; upstream asks
  …): edit it deliberately to request a switch`. A proposal whose only
  change would be the partition has nothing to propose: the version is
  recorded instead.

The agent template is to start new instances partitioned, with that
opt-out, once the agent can run partitioned (**TODO**: not yet — today its
instances start unpartitioned); existing instances stay as they are.

## Shared resources

Every resource of a partitioned tile's scope is **per partition** by
default: each partition, and the global instance, gets its own copy at the
same name (`xbin.Resource(name)` and `res:` ids don't change). A scope can
opt a resource out in `scope.json`:

```jsonc
"resources": {
  "docs":   { "type": "sqlite" },                 // one per partition
  "public": { "type": "kv", "shared": "read" },   // global writes, partitions read
  "board":  { "type": "sqlite", "shared": true }  // everyone reads and writes
}
```

- `"shared": true` — every partition and the global instance read and write
  the one copy.
- `"shared": "read"` — the global instance reads and writes; user partitions
  only read (not for `sqlite`: use `kv`, or `true`). A `read` directory is
  mounted read-only in people's partitions, and their kv, blob and bus
  writes answer 403 `res:<scope>/<name> is read-only for people's
  partitions`.

Each person's partition has its own disk ceiling (by default the tile's
per-namespace quota): a full partition answers that person's writes 507,
nobody else's. Where the data sits on disk, and when it is deleted, is in
[resources.md](resources.md#partitioned-tiles-shared). A bus that isn't
shared keeps each partition's events apart: an event published on it — the
global instance's too — reaches only subscribers in the publisher's
partition. A shared bus's events reach every reader, as today.

A `cron` resource can't be shared: `shared` on it is ignored, with a
warning. In a scope no tile partitions, `shared` is ignored silently, so a
template can ship it ready.

**Anything a partition writes to a `true` resource is readable — and
changeable — by every partition's code and by the global instance.** Never
let a row in a `true` resource decide where a person's private data goes:
keep that in a `read` resource the global instance writes, or in the
partition's own data.

## The global instance and people's partitions

The global instance never calls a user partition, reads its data or sees its
bus events. The two sides talk through:

- **shared resources** (above);
- **partition mail** — **TODO**: an xbind-owned inbox per addressee; the
  global instance mails one person's partition, a partition mails only
  global, and xbind stamps the sender. The SDK's `xbin.Mail`, `xbin.Inbox`
  and `xbin.Ack` come with it;
- **a user partition calling its own global instance.** A person's frame,
  terminal or partition backend may call the tile's global instance, and the
  call arrives there **as that person**: `X-XBin-User` is the person,
  `X-XBin-User-Level` their level on the tile, `X-XBin-Role` clamped to it
  (`reader` for read, `writer` for write or terminal — never `admin`), and
  `X-XBin-Partition: user:<id>` with its `X-XBin-Partition-Id` — never the
  tile calling itself, even from the partition's backend. A view-as frame
  gets there read-only: `reader`, whatever the viewed person's level. An
  admin calling the tile directly may use it too, keeping their own role
  (other people come in through the tile's frame). Other tiles, cron and
  bus deliveries and path tickets can't (403), and a tile without a global
  instance answers 404 ([protocol.md](protocol.md) §HTTP routes › Core has
  every refusal). In Go,
  `xbin.Client().Get(xbin.GlobalURL("runs/42"))`; in a frame,
  `` xbin.fetch(`/api/${xbin.self}/runs/42`, {partition: 'global'}) ``. Both
  add `?xbin-partition=global`, and only in a user partition; elsewhere they
  call the tile as today. Both reach **the tile's own API only**:
  `GlobalURL` always builds `/api/<self>/…`, and `xbin.fetch` rejects the
  option (a `TypeError`, in every document) on any other URL — another
  tile's, xbind's own `/api/xbin/…`, another host — and on any value but
  `'global'`. A falsy value (`false`, `''`, `null`) means the viewer's own
  partition.

  **A global instance must never treat a call carrying
  `X-XBin-Partition: user:…` as the tile itself**, even when `X-XBin-From`
  is its own path.

## Bind types: global and personal

A partitioned tile's interface slots are wired by two kinds of bind:

| | **Global bind** | **Personal bind** |
|---|---|---|
| What it is | today's binding, in the workspace `xbin.json` | a person's own wiring, kept by xbind |
| Who creates it | whoever may bind it today — a workspace admin, an org admin within their org, a personal tile's owner to what they own or are allowed (D88); partitioning adds no rule | the owner of a personal (user-owned) tile, into **their own** partition of a partitioned tile they can read, with their own sign-in (never tile code). Admins can list and delete personal binds, never create them |
| Seen by | the global instance and every partition | only that person's partition and frames |
| Lets calls through from | every instance of the tile | only that person's partition, while they still own the provider |

A global bind puts its provider in every person's trust base: its code sees
what each partition sends it. A personal bind is removed when its owner
removes it or gives the provider away, when the person is deleted, and when
the requester switches mode.

**TODO:** personal binds' rows in `XBIN_IFACE_<SLOT>` and `xbin.iface()`
(`personal: true`), the routes and `bx bind --personal`.

## Providers: calls from partitioned tiles

A tile called by a partitioned one learns which partition is calling:

- `X-XBin-Partition` — `user:<id>` or `global`: the partition the caller acts
  in. A display name.
- `X-XBin-Partition-Id` — for user partitions only: an opaque key, stable
  for the person and never reused by a person later created under the same
  id.

A provider that keeps per-caller state (a sandbox manager, a chat bridge)
**keys it on (`X-XBin-From`, `X-XBin-Deployment`, `X-XBin-Partition-Id`)**:
an absent partition and `global` are then one consumer — the tile's own —
and each person's partition is another. Keyed on `X-XBin-From` alone, a
provider merges every person's data into one. Both headers are absent on
calls from tiles that aren't partitioned, and xbind strips any inbound
value, like every `X-XBin-*` header.

A partitioned tile's own backend sees `X-XBin-Partition` too, on the calls
into it that act for a partition: a person reaching their partition
(`user:<id>`), the root token reaching the global instance (`global`), and —
at global — a user partition's call to it (`user:<id>`, with `X-XBin-From`
the tile's own path; see above). A tile that isn't partitioned calling in
sends neither header.

## Calls between partitioned tiles

When a partitioned tile Z holds a grant on another partitioned tile X — a
call grant or binding, or a `uses` of one of X's per-partition resources —
alice's partition of Z reaches **alice's** partition of X, never anyone
else's. It needs three things, checked at every call and data reach:

- the grant, approved as today;
- alice's read access on X: Z can't create data for alice in a tile she
  can't open;
- only when the workspace policy **partitionConsent** is on (the admin
  console's workspace → policies tab, or `bx policies set partition-consent
  on`; off by default): alice's own consent for Z → X.

A resource X declares shared (`"shared": true` or `"read"`) is one copy for
everyone, no person's data: alice's partition of Z reaches it with the
grant and her read access on X, never her consent, whatever the policy
says. The approval warning, the prompts and the ledger below leave shared
resources out too.

**With the policy off** (the default) the grant and read access suffice:
Z's code — and everyone who can change it — reads and writes the X data of
every person who can read X. Approving such a grant says so where it is
approved — the grants panel, the admin console's binding → grants view,
the organisations tile's pending approvals and `bx grants`
(`GET /api/xbin/grants`' pending rows carry the `warning`).

**With the policy on**, a call without alice's consent is refused, `403
alice hasn't let apps/z use their apps/x data`, and alice is asked — a push
and a `partitions` event to her own sockets (never to X's code), at most
once a day per edge, and only for an edge an admin approved, so tile code
can't make people consent ahead of the grant. The push links to the
partitions page, which isn't served yet (below): she allows it with `bx
partition consent apps/z apps/x`, from her own sign-in (never tile code:
`POST /api/xbin/partitions/consents` takes a person's own session, app or
device only), and takes it back the same way (`--revoke`): the next call
and data reach are refused, and Z's backend instance of her stops (it
starts again on its next request, without her consent). A stream that a
page, terminal or agent session of Z opened as her before the revocation
lasts until it closes; so does one opened before an admin turned the
policy on. Consents belong to the person as they are now: someone deleted
and created again under the same id has none. They name tiles by path: a
tile that is deleted or moved, or whose partition mode switches, takes
every consent naming it, so a new tile at its path starts with none.
Turning the policy off keeps every consent, unused; they apply again when
it comes back. Before an admin turns it on, the Policies tab shows each
edge between partitioned tiles and how many people used it in the last 30
days (`GET /api/xbin/partitions/edges`). A consent record this xbind can't
read (another schema, a hand edit) counts as no consent and is kept as it
is: `/alerts` names it to admins, its person can't consent or revoke, and
switching a tile it may name stops, until an admin fixes or removes it.

**The egress ledger**, in both settings: xbind counts, per person's
partition and per day, the calls and data reaches into another partitioned
tile (`edge`) and the calls to tiles that aren't partitioned — or to a
partitioned tile's deployment beyond its primary, named `<tile>+<name>`
(`provider`) — counts only, never what was sent. A person reads their own
(`bx partition ledger`, `GET /api/xbin/partitions/ledger`); a tile's
writers and managers its totals per target, with every personal tile
counted as `(a personal tile)`, never by its path; admins each person's
totals. It keeps 90 days. Switching a tile's partition mode deletes its
people's ledgers and every consent naming it.

## How people's partitions run

Each person's partition is **its own backend process**, in its own sandbox:

- **Started on first use, stopped when idle.** Nothing starts a person's
  partition at boot. It starts on their first request (or a cron or bus
  delivery of theirs), and stops **10 minutes** after its last use. A
  request in progress or a held connection counts as use; an open event
  stream (SSE) doesn't, so a background tab doesn't keep it running —
  frames reconnect their streams while they are visible. The next use
  starts it again. The global instance keeps the usual 30 minutes, and
  `alwaysOn` keeps up the global instance only.
- **What it gets.** `XBIN_PARTITION=user:<id>` (the global instance gets
  `global`; an unpartitioned tile gets nothing new), the same
  `XBIN_COMPONENT`, `XBIN_RES_*` paths and code as every other instance,
  its own socket, instance token, log and cgroup, and its own data behind
  those paths. Its network is a non-primary deployment's: relayed egress
  under the tile's egress policy, and no host network, provider splice,
  net-provider roster, lan-ingress leg, ingress path or stream-slot dial —
  none of which the global instance loses.
- **One build.** A change to the tile's code — a save, a deploy or a
  restart of the primary — builds it once; every running partition
  restarts onto that build (a few at a time), and the others build it on
  their next start, as the tile's own instance always did. A pinned
  primary's checkpoint is shared the same way. A person's start that fails
  for them alone (it didn't come up healthy, say) is tried again on their
  next request; a build error or a crash loop stays until the code changes.
- **Only on the primary, only with `--isolate`.** A person's partition
  runs only on the tile's primary deployment, and only on an xbind started
  with `--isolate`; elsewhere the tile's API answers that a person's
  partition can't run here.

**Limits.** How many people's instances run at once is capped, per tile and
for the whole workspace, from the host's memory M — per tile
clamp(M/4 ÷ E, 4, 32), workspace-wide clamp(M/2 ÷ E, 8, 128), with E about
160 MiB per instance (a 4 GiB machine runs 6 per tile, 12 in all). M is
the machine's MemTotal, or xbind's own memory limit when it runs in a
cgroup (a container) that caps it lower. At the cap, a person's start stops the least recently used partition
that isn't in use (no request in the last 2 minutes and no held
connection); with none to stop, it answers **503** `too many people's
instances of <tile> are running; try again shortly`. A cron, bus or mail
start never stops a partition that is in use or streaming, runs at most 4
at once in the workspace (mail: 6 a minute per tile), and otherwise waits
and retries. An admin sets the caps, and each person's byte ceiling on a
tile, with `POST /api/xbin/partitions/limits` ([protocol.md](protocol.md));
a tile manager may lower their own tile's.

**Admins** see who has an instance running — the tile and the partition
key, in the sandbox list (`/api/xbin/sandboxes` rows gain `partition`) —
never what it holds. A person's partition's crash messages name the
partition, not its log.

## Vault and registrations

A person's partition has its own vault and its own registrations. Code that
runs in every partition keeps calling the same routes; xbind answers each
partition from its own store:

- **Vault.** `xbin.Secret` / `SetSecret` from a user partition's backend,
  and `bx vault` from its person's terminal on the tile, reach that
  partition's vault; its values are readable by that partition's backend
  only (the terminal lists and sets them, as today). Everyone else —
  other tiles, the root token, workspace admins — reaches the global
  instance's vault; admins' vault listing counts how many people keep one,
  never their key names.
- **Cron jobs** are the partition's own: at most 16 (the 17th: 409), none
  more often than once a minute (400). They tick that partition while its person exists, is
  enabled and can read the tile, and resume by themselves when they can
  again. A tick whose background start is deferred (too many people's
  instances starting) is tried again, with jitter, before the next one is
  due; one that stays deferred is missed and counted.
- **Bus subscriptions** are the partition's own too, at most 16 (the 17th:
  409). An event a person's partition publishes on its scope's own bus
  reaches that partition's subscriptions (and may start it); an event of a
  shared bus, of an unpartitioned scope, or one another tile's code
  published for the person, reaches a person's subscription only while their
  partition runs — it never starts it — and a skipped one is counted
  (`dormantDrops`). The global instance's events never reach people's
  partitions, and theirs never reach it. Subscribing to another partitioned
  tile's bus follows the rule of calling it: the person must be able to read
  that tile and, with the workspace's `partitionConsent` policy on, have
  consented — otherwise 403, and a subscription made before gets nothing
  (listed `"dormant": true`) until they can again.
- **Interface instances and ingress hosts** registered from a person's
  partition answer 200 with `"dormant": true`, are kept for that partition,
  and never route: `provider#instance` bindings and the public surface are
  the global instance's.
- **Notifications** (`xbin.NotifyUser`, `POST /api/xbin/notify`) from a
  person's partition reach only that person; the global instance's reach any
  reader.

`?partition=` on the vault, cron or bus-subscription routes of a partitioned
tile is refused (400) for everyone: a partition's own stores are reached only
from inside it. While the tile is paused — its mode pending or invalid, or a
switch deleting its data — a person's partition can't change its vault or
registrations (409). A switch of the tile's mode that deletes its data deletes
all of these (removing or adding `"global"` keeps people's). They are
deleted with the partition 30 days after its tile is removed (unless the
tile comes back) or its person's id is given to someone new.

## In your code

**Go** ([sdk.md](sdk.md)):

```go
xbin.Partition()        // "user:<id>" | "global" | "" (not partitioned) — $XBIN_PARTITION
xbin.PartitionUser()    // the <id> of a user partition, "" otherwise
xbin.RequirePartition() // exit 3 unless run as a partition (§Older xbinds)
xbin.GlobalURL(path)    // this tile's global instance, from a user partition
c := xbin.Caller(r)
c.Partition             // X-XBin-Partition: the partition the call acts in, "" if none
c.PartitionID           // X-XBin-Partition-Id: key per-caller state on it
```

**`global` is one instance for everyone who reaches it**: other tiles, the
root token, public requests, every person's calls to it — and, on a
non-primary deployment whose code asks for partitions, every writer who
opens it. Keep per-person data where `xbin.PartitionUser() != ""`, or judge
`global`'s callers yourself (`c.User`, `c.UserCanWrite()`), as an
unpartitioned tile does today.

**node / python:** `process.env.XBIN_PARTITION` (`os.environ.get`), and the
`x-xbin-partition` and `x-xbin-partition-id` request headers.

**In a frame:** `xbin.partition` is the partition the viewer reaches —
`user:<id>`, or `global` for the root token and `--no-auth` — and is absent
in a tile that isn't partitioned. `xbin.fetch(url, {partition: 'global'})`
calls the global instance, on the tile's own API only (above).

Code that runs in every partition needs no change to keep people apart:
`Resource(name)`, the vault and registrations are the partition's own.
`Status` and `Notify` from a user partition reach only its person. One
partition is one process, so what a backend holds in memory is its person's
too. A user partition's bus subscriptions get events of a shared resource
or of another tile's bus only while the partition runs: such an event never
starts it (the `xbin.Subscribe` exception).

## Older xbinds

An xbind older than partitions ignores `"partition"` and `"shared"` and runs
**one instance** of the tile on the global instance's data and the shared
resources. It never reads people's partition data, so a downgrade exposes
nobody's partition; but whatever that one instance writes goes to the global
instance's data, for everyone.

A tile whose code expects xbind to keep people apart calls
`xbin.RequirePartition()` first thing in `main`: without `XBIN_PARTITION`
it exits (status 3) and names the reason, so that xbind runs no backend at
all. The same holds when a tile's managers keep it unpartitioned while its
code asks for partitions.

`RequirePartition` returns in `global` too, and `global` is one instance for
everyone who reaches it — including every writer of a non-primary deployment
whose code asks for partitions, even when the primary isn't partitioned
([§In your code](#in-your-code)). It proves only that xbind runs this
backend as `user:<id>` or `global`, not that the instance serves one person:
code that must never mix people serves per-person data only where
`xbin.PartitionUser() != ""`.

## What partitions protect, and what they don't

Partitions keep the people who *use* a tile apart, and keep workspace admins
from reading people's partitions through xbind. They don't protect against
whoever can change the tile's code or the code of a provider it uses (write
level on the tile, an ancestor, or the provider — the code runs in every
partition), the host and the vault unseal key, or what a partition itself
writes to a shared resource or mails to the global instance. An admin can
still take over a person's account by resetting its credentials; that is
recorded and the person is told, and a workspace policy can make such resets
wait for the person. Switching a tile's partition mode deletes all its data,
and erases it from backups, after a manager confirms. Backups are encrypted,
and restoring one on another machine needs the exported backup keys.

## Not documented yet (TODO)

- partition mail, `partitionMail`, and the SDK's `Mail`/`Inbox`/`Ack`;
- personal binds in `XBIN_IFACE_<SLOT>` / `xbin.iface()`, their routes and
  `bx bind --personal`;
- the workspace policy for credential resets that wait for the person;
- terminals, agent sessions, logs and status on partitioned tiles;
- the partitions page (`/xbin/partitions`), `bx partition` beyond
  `switch`/`keep`, the admin tile's Partitions section, the shell's marker
  and its Keep/Switch overlay;
- sealed backups, backup keys and disaster recovery;
- the wire reference in [protocol.md](protocol.md) for the partitions API
  (the headers, the `xbin-partition` meta, `XBIN_PARTITION`,
  `?xbin-partition=global`, which partition each credential acts in and the
  API's partition classes are there).
