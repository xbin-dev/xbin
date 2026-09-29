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
| `user:alice`'s partition of another partitioned tile, holding a grant on this one | `user:alice` of this tile, if alice can read it |
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
relayed egress under the tile's egress policy. A static tile may declare
`partition`, with a warning: it has no backend to partition.

Two optional keys go with it — **TODO**, documented when they are built:
`"partitionMail": "/path"`, where xbind rings the global ↔ person mail
doorbell, and `"partitionNote": "…"`, the tile's own words shown when a mode
switch is requested (for example, what its data is).

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
  `xbin.json`'s **installed** `partition` — present, absent or its list —
  and changes nothing else about it. When upstream asks for something
  else, it prints `partition kept as installed (…; upstream asks …): edit it
  deliberately to request a switch`.

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
  only read (not for `sqlite`: use `kv`, or `true`).

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
  `X-XBin-User-Level` their level on the tile, `X-XBin-Role` clamped to it,
  and `X-XBin-Partition: user:<id>` — never the tile calling itself. In Go,
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
- the workspace policies (per-person consent for cross-tile partition calls;
  credential resets that wait for the person) and the admin Policies tab;
- terminals, agent sessions, logs and status on partitioned tiles;
- the partitions page (`/xbin/partitions`), `bx partition …`, the admin
  tile's Partitions section and the shell's marker;
- sealed backups, backup keys and disaster recovery;
- the wire reference in [protocol.md](protocol.md): the headers, the
  `xbin-partition` meta, `XBIN_PARTITION`, `?xbin-partition=global` and the
  partitions API.
