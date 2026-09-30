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

In the workspace shell a partitioned tile is still one tile, with one
window: opening it opens your own partition. Its sidebar row (before ⋯) and
its window head (in place of the runtime dot) carry a small teal disc, half
filled, whose tooltip reads "Partitioned: each person here has their own
data" — and names the global instance when the tile has one. It marks a
state; it isn't a button. It follows the recorded mode, so a pending switch
doesn't change it ([§The mode](#the-mode-set-while-empty-then-switch-or-keep)).
A window showing one of the tile's other deployments has no marker: its
head says `shared` beside the deployment's name, since that deployment's one
instance is shared by the tile's writers (above), not your partition. A
theme may set the marker's colour with the `--bx-part` token. The shell is
workspace scaffold, so a workspace gets the marker with `bx builtin
update`.

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
what its data is, and what a switch would lose. `"partitionMail": "/path"`
(an absolute path without a query, read only beside `"global"`) is where
xbind rings the partition mail doorbell
([§Partition mail](#partition-mail)).

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
  every 15 minutes, however often the code toggles `partition`;
- in the workspace shell, the tile's window greys out under the same words
  and the tile's `partitionNote` (the `/components` row carries it while
  the switch is pending), names who decides — and, for a tile manager,
  offers **Keep the current mode** and
  **Switch and delete all data…** (just **Switch…** when only `"global"`
  comes or goes), which shows what the switch deletes and keeps and asks
  for the tile's path before it switches. A window showing another
  deployment of the tile isn't paused and isn't greyed out. The shell is
  workspace scaffold: a workspace gets this with `bx builtin update`, and
  an older shell shows the banner and the page only.

Managers decide in the shell, with `bx partition switch <tile>` or `bx
partition keep <tile>` ([bx.md](bx.md)), or with `POST
/api/xbin/partitions/mode`. A switch
first shows what it deletes — data namespaces (the tile's own and every
deployment's), people's partitions, vault keys, cron jobs, bus
subscriptions, interface instances and ingress hosts, bytes (partition
mail's among them: it has no count of its own), backup keys —
and what it keeps: the code (the tile directory, checkpoints, deployment
records), grants and bindings, the tile's own terminal layer, people's
homes and their own agent-session history, records a provider keeps (such
as sandboxes at a sandbox manager — clean them up there), and backups made
before backups were sealed, which no key erases. Between user partitions
and unpartitioned it also ends the tile's terminal and agent sessions and
deletes people's terminal layers and partition agent history of the tile
([§Terminals and agent sessions](#terminals-and-agent-sessions)). The manager types the
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
ingress host registration, or a person's partition (its backend started, or
a terminal or agent session of theirs opened on the tile), its mail or a
personal bind.
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
  partitions need isolation. The block is stripped from instances (the
  instance's own `partition` is the line after the opening brace), and the
  merge driver xbind names in each instance's repository never takes a
  `partition` change from upstream
  ([03-components](overview/03-components.md#templates-blueprint-components)),
  so `git merge template/main` can't add or change it — and a builtin template's
  served repository (the instances' `template` remote) never changes the
  block either: one xbind creates carries none, one an older xbind created
  keeps the block it has. An upstream change to the default is therefore
  no conflict: the merge leaves your `xbin.json` and its mode as they are,
  and the snapshot's commit message says, for your information, that the
  block changed and what new instances now start with.
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

The builtin agent template asks for `["user", "global"]`: a new agent
instance keeps each person's conversations in their own partition, unless
you opt out as above (or xbind lacks `--isolate`). Existing agent instances
keep their mode — they run unpartitioned, as before, until a manager
switches them, which deletes their conversations, memory and schedules. The
template's API.md ("Partitioned instances") says what runs where; update
the sandbox managers an agent uses before you create or switch one (a
manager whose `hello.caps` lack `partitions` isn't used in people's
partitions, [sandbox-manager.md](sandbox-manager.md#partitioned-consumers)).

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
- **partition mail** ([below](#partition-mail)): an xbind-owned inbox per
  addressee; the global instance mails one person's partition, a partition
  mails only global, and xbind stamps the sender;
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
  bus deliveries and path tickets can't (403; a cron job or bus
  subscription whose path carries the parameter is refused, 400, when it
  is registered), and a tile without a global instance answers 404
  ([protocol.md](protocol.md) §HTTP routes › Core has every refusal). In Go,
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

## Partition mail

A tile that declares `"global"` gets one inbox per addressee — its global
instance, and each person's partition — that xbind keeps. Through it the
global instance hands something to **one** person (a direct message, a
webhook payload meant for them) without another partition, an admin or its
own storage keeping a copy, and a person's partition hands something back.

| Sender | May mail |
|---|---|
| the tile's global instance (its backend) | `user:<id>` — a person who can read the tile; anyone else answers 404 `no such person here`, which says nothing more — or `global` |
| a person's partition (its backend, that person's frames, terminals and agent sessions) | `global` only: there is no person-to-person channel |
| the tile's frames, terminals and agent sessions acting as global (the owner token's frames, root terminals) | nothing (403) — they read and acknowledge the global instance's inbox, below |
| everyone else — people outside the tile's own credentials (admins included), the root token, view-as, other tiles | nothing (403) |

- **`from` is xbind's.** Every item carries `from`, `global` or `user:<id>`,
  stamped from the sender's credential and never read from the request.
  Trust it; never trust a person named inside `data`.
- **Only the addressee reads.** A partition reads and acknowledges its own
  inbox — its backend and its person's frames, terminals and agent
  sessions — and the global instance its own: its backend, and the tile's
  frames, terminals and agent sessions acting as global (the owner token's
  frames, root terminals), on the primary deployment. No route reads
  another's — view-as, another tile, a person's partition and a deployment
  beyond the primary never reach the global instance's — and admins see
  counts only. Items are sealed with the vault, kept in xbind's own
  data (never in a sandbox) and not backed up.
- **Limits.** An item is at most 1 MiB (topic and data); an inbox holds at
  most 1000 items and 64 MiB — the sender gets 507 until the addressee
  acknowledges some. The global instance's inbox is every person's to
  mail, so each sender has a share of it: at most 100 of their items and
  8 MiB waiting there (507 to that sender alone — one person can't fill it
  for everyone). An item expires after its `ttl` (7 days by default, at
  most 30), and is then dropped and counted; one that can't be opened
  (sealed under another vault key, or damaged) is dropped at the next read
  and counted as undeliverable, and never holds up the items behind it.
- **At least once.** An item stays until it is acknowledged or expires, so a
  handler may see it again: dedupe by `id`.
- **The doorbell.** With `"partitionMail": "/mailbox"`, xbind POSTs
  `{"partition": "<addressee>", "pending": <n>}` to that path on the
  addressee's instance, as `xbin/mail` (role `writer`), while its inbox
  holds items: at once for a new item, again when the addressee starts
  (unless a ring reached that start — often the doorbell's own ring
  started it), and after 1 min, 5 min, 30 min, 2 h, then every 6 h while
  items remain; a start never shortens those steps, so an item a handler
  leaves unacknowledged wakes a stopped partition less and less often.
  It starts a person's stopped partition only if that partition has run
  before and its person can still read the tile — mail never starts a
  person's first instance — and within the background start limits (6 mail
  starts a minute per tile, [§How people's partitions run](#how-peoples-partitions-run));
  otherwise the items wait for the partition's next start. Without
  `partitionMail`, poll the inbox (at start and on a timer).
- **What deletes it.** A reset of a person's partition, or the person's
  deletion, deletes their inbox; a switch between user partitions and
  unpartitioned deletes the tile's mail; removing `"global"` deletes the
  global instance's inbox only. While the tile is paused every mail call
  answers 409; while the vault is sealed a send and a read that returns
  items answer 503, and acknowledging still works.

```go
// the global instance hands alice a message
id, err := xbin.Mail("user:alice", "handoff/dm", dm)

// the tile's partitionMail handler, the same code in every instance
mux.HandleFunc("POST /mailbox", func(w http.ResponseWriter, r *http.Request) {
	after := ""
	for {
		pg, err := xbin.InboxPage(after, 100)
		if err != nil { // the doorbell rings again later
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		var done []string
		for _, it := range pg.Items {
			handle(it) // dedupe by it.ID; it.From is "global" or "user:<id>"
			done = append(done, it.ID)
			after = it.ID
		}
		if err := xbin.Ack(done...); err != nil { // one call a page; acknowledged items are gone
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if !pg.More { // a short page isn't the end; More is
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
})
```

Acknowledge what you won't handle too (a topic this version doesn't know):
an item left unacknowledged is rung for again — less and less often, and it
starts a stopped partition each time — until it expires.

`xbin.MailWith(to, topic, data, xbin.MailOptions{TTL: …, Source: …})` sets
the expiry; `Source` names where a private trigger's event came from when
the global instance hands one to a person, and is counted, never the
content, in that person's egress ledger. The routes are `POST` and `GET
/api/xbin/partitions/mail` and `POST /api/xbin/partitions/mail/ack`
([protocol.md](protocol.md)); frames call them with `xbin.fetch`, though
normally their backend does.

Two patterns keep a person in charge of what leaves their partition:

- **Global keeps routing, never content.** Handing alice a message, global
  records only where it came from (id → destination); alice's partition
  replies by mailing global `{inReplyTo: <id>, text}`, and global takes the
  destination from **its own** record and checks that `from` is the person
  it handed the message to. A partition can't pick an arbitrary
  destination.
- **Publishing on purpose.** A person's partition makes an item visible to
  everyone by mailing global (`publish {docId, …}`); global, the only
  writer of a `"shared": "read"` resource, stores it keyed by `from` and
  serves it from there. The partition's private data never leaves it.

Anything a partition mails to global is readable by the global instance —
its code, and the tile's frames, terminals and agent sessions acting as
global (the owner token's); nothing mailed reaches another person's frame
or partition directly.

## Bind types: global and personal

A partitioned tile's interface slots are wired by two kinds of bind:

| | **Global bind** | **Personal bind** |
|---|---|---|
| What it is | today's binding, in the workspace `xbin.json` | a person's own wiring, kept by xbind |
| Who creates it | whoever may bind it today — a workspace admin, an org admin within their org, a personal tile's owner to what they own or are allowed (D88); partitioning adds no rule | the owner of a personal (user-owned) tile, into **their own** partition of a partitioned tile they can read, with their own sign-in (never tile code). Admins can list and delete personal binds, never create them — an admin's bind is always global, even of a tile they own |
| Seen by | the global instance and every partition | only that person's partition and frames |
| Lets calls through from | every instance of the tile | only that person's partition, while they still own the provider |

A global bind puts its provider in every person's trust base: its code sees
what each partition sends it. A personal bind is removed when its owner
removes it or gives the provider away, when the person is deleted, when
the requester switches mode, and when a tile is created at the requester's
or the provider's path (a removed tile's binds never reach the new one).

**Global binds** are made as today (`POST /api/xbin/bindings`, `bx bind`,
the admin console's wiring view, which labels a partitioned tile's bindings
*global*). One refusal is new: an unpartitioned tile's http slot can't be
bound to a partitioned tile that has no global instance (409) — no call of
it would reach that tile; bind pickers show such a provider greyed out.

**Personal binds.** Alice owns `users/alice/mcp`, which provides the `mcp`
service; `apps/agent` is partitioned, she can read it, and it has a
`"multi": true` http slot `mcp`. With her own sign-in (not from a tile's
terminal) she runs

```
bx bind --personal apps/agent mcp=users/alice/mcp
```

(`POST /api/xbin/partitions/binds {requester, slot, provider}`). Then:

- only her partition of `apps/agent` is restarted, and its
  `XBIN_IFACE_MCP` lists the provider after the global rows:
  `{"provider": "users/alice/mcp", "url": "http://xbin/api/users/alice/mcp",
  "service": "mcp", "personal": true}`; her frames' `xbin.iface('mcp')`
  endpoints list it with `personal: true`. Bob's partition and the global
  instance don't;
- a call to `users/alice/mcp` passes only from `apps/agent` acting in her
  partition, while she still owns the tile. Every other caller — bob's
  partition, the global instance — gets the same 403 as if nothing were
  bound. The provider sees `X-XBin-From: apps/agent`,
  `X-XBin-Partition: user:alice` and `X-XBin-Partition-Id`.

The slot must be a `multi: true` http slot, the provider a personal tile
that isn't partitioned and provides the slot's service as a plain provider
(not instances), and the policy ceiling must allow the edge; a provider
already bound on the slot for everyone can't be bound again. `bx bind
--personal` lists your personal binds (`GET`; admins see everyone's, each
with `live` and, when it no longer holds, `why`), and `bx bind --personal
--unset apps/agent mcp=users/alice/mcp` removes one (`DELETE`; admins may
remove anyone's). An admin can't make one: `bx bind --personal` as an
admin answers 403 — bind the tile for everyone instead.

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
(`GET /api/xbin/grants`' pending rows carry the `warning`), and `bx grant`
prints it once approved (`POST /api/xbin/grants` answers it) — as `bx bind`
does for a global bind of Z's http slot to X (`POST /api/xbin/bindings`).

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
tile (`edge`; a bus subscription there counts once when made and once per
event delivered) and the calls to tiles that aren't partitioned — or to a
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
  (listed `"dormant": true`) until they can again. A bus that tile declares
  shared is no person's data: it needs the person's read access, never
  their consent.
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
deleted with the partition — and erased from its backups
([§Backups](#backups)) — 30 days after its tile is removed (unless the
tile comes back) or its person's id is given to someone new.

## Terminals and agent sessions

A terminal or agent session on a partitioned tile belongs to the person who
opened it, and reaches only that person's partition:

- **Its API is the person's partition.** The session's `XBIN_TOKEN` is
  today's terminal token; xbind puts it in the opener's own partition, so
  `bx`, `curl http://xbin/…` and every tool in the session reach
  `user:<id>`'s instance and data — an admin's session too, only ever the
  admin's own. The session gets `XBIN_PARTITION=user:<id>` (`global` for a
  session targeting a non-primary deployment). A session with no person
  (the owner token) reaches the global instance, or, on a tile without
  one, opens with the tile API off.
- **It starts in `$HOME`.** The tile directory is the tile's code, shared
  by every partition: a file left there is readable by every person's
  terminal and by the tile's code in every partition. Keep your own files
  in `$HOME`; the terminal prints a grey line saying so. (Without
  `--isolate` a terminal is a host shell that can read every partition's
  files, and its grey line says that instead.)
- **Its dev layer is the person's own.** System changes (`apt install`,
  `/etc`) go to that person's layer of the tile, never the tile's shared
  one, so one person's terminal can't plant anything in another's; a VM
  terminal's disk is per person too. Reset resets your own layer.
- **Its agent history is the partition's.** A finished agent session's
  transcript is kept with the person's partition, listed with their other
  past sessions, and never read by a person recreated under the same id. A
  past session continues only where it ran: one of the partition's in the
  partition, one from before the tile was partitioned outside it (a resume
  across answers 409, and the Agent tab offers a fresh start).
- **Admins can end other people's sessions there, and nothing else.** On a
  partitioned tile an admin can't reattach to another person's terminal,
  drive their agent session (read its events, log or diffs, prompt it,
  answer its permissions or questions, change its settings, restart or
  rename it), receives none of its `term` or `session` events, and sees
  another person's sessions listed without their names (`GET
  /api/xbin/term/sessions?user=`, `GET /api/xbin/status`, the sandbox
  list). Ending one (`DELETE /api/xbin/term/sessions/<id>`, `DELETE
  /ws/term?session=`) stays allowed. Viewing as the person opens none of
  it either: no session of the tile is readable that way, its names are
  left out, and the person's partition agent history isn't listed.
- **A mode switch** that deletes the tile's data ends the tile's sessions
  (those opened on a sub-path of the tile too) first, then deletes every
  person's layer and partition agent history of
  it; the tile's own layer and people's own history stay. None of it is in
  backups. While a switch runs, a new session on the tile answers 409.

## Backups

In a workspace with a vault barrier every archive is sealed under a backup
key ([lifecycle](overview/14-lifecycle.md) §Sealed archives). On a
partitioned tile, **each person's partition gets archives of its own**:

- **What goes where.** A backup of the tile (`bx backup`, a schedule)
  writes its main and data archives as for any tile — the global instance's
  data at today's keys, its shared resources, its cron jobs and bus
  subscriptions — and then one archive per person's partition,
  `.partitions.<tile-key>.<deployment>.<partition id>`, sealed under that
  partition's own key: its data (the resources not declared `shared`), its
  vault (the values still sealed by the vault), its registrations and its
  record. **The tile's own archives hold nothing of anyone's partition**,
  and people's terminal layers, partition agent history and mail are in no
  archive. A partition that can't be archived doesn't fail the backup; the
  answer lists it (`partitions: {archived, failed}`), and `bx backup`
  then exits 1. A plaintext-vault workspace (`--insecure-vault`) archives
  no partition — a person's data never leaves in the clear — and the
  answer (and `bx backup`, as a warning) says so. A schedule's retention
  keeps each partition's newest versions, and deletes every version of a
  partition that is gone once its key is erased.
- **Restoring a partition.** `bx restore <tile> --partition` (`POST
  /api/xbin/partitions/restore`) replaces your partition — its data, vault
  and registrations — with a backup of it, after you type `<tile>
  user:<you>`; `bx backups <tile> --partition` lists the versions. You do it
  from your own session, for your own partition only (naming any other
  partition id is refused, before anything is fetched); an admin may for
  anyone (`--user`), and the person is told. Only the archive of **the same tile and the same person**
  restores, and only while the tile is partitioned: a partition archive is
  never restored as a tile, into global, or into anyone else's partition.
  If the person's id was deleted and given out again since the backup, it
  was an earlier holder's: only an admin restores it, naming the id again
  (`--partition-id <the old one> --to <id>`), and the person is told. Main
  archives never restore into a partition. A restore judges all of this
  again once it holds the tile's backups: a switch, reset or erase that ran
  while it waited wins, and nothing is restored.
- **Erasing.** Deleting a partition's backup key crypto-erases it in every
  archive, and the archiver is asked to delete those versions. That
  happens when the partition's data is deleted: a mode switch between user
  partitions and unpartitioned (the tile's `ns:` and `part:` keys; its
  source backups survive), a person's partition swept 30 days after their
  deletion or the tile's removal, and `bx backup erase <tile> --data`. A
  restore of an erased backup says when and why: `this backup's data was
  erased on <date> (<reason>)`. Each erase, and each restore of a
  partition, is recorded in the tile's mode history (`backup-erase`,
  `partition-restore`, with the partition id).
- **Backups from before a switch.** A backup of the tile made before its
  last partition mode switch that deleted data restores only with `bx
  restore <tile> --confirm <the switch's date>` (`POST /api/xbin/restore`
  with `confirm`; otherwise 409 naming the switch): it brings back what the
  switch deleted — a plaintext archive's data, the registrations any
  archive lists — into the global instance's namespace, never a person's
  partition. A sealed backup's data was erased with its key; its source
  restores. A deployment's data restore (`POST /api/xbin/deployments/restore`)
  refuses such a backup: only the whole tile's restore takes the
  confirmation. The switch is remembered for good, however long the tile's
  history grows; while the tile's mode record can't be read, every restore
  of it asks, with the backup's own date.

## In your code

**Go** ([sdk.md](sdk.md)):

```go
xbin.Partition()        // "user:<id>" | "global" | "" (not partitioned) — $XBIN_PARTITION
xbin.PartitionUser()    // the <id> of a user partition, "" otherwise
xbin.RequirePartition() // exit 3 unless run as a partition (§Older xbinds)
xbin.GlobalURL(path)    // this tile's global instance, from a user partition
xbin.Mail(to, topic, data)  // partition mail (§Partition mail); InboxPage, Ack
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

## Operating people's partitions

xbind tells people and operators about partitions without handing anyone a
person's data. `GET /api/xbin/partitions` ([protocol.md](protocol.md)) is
the one read; its `features` list says what this xbind serves (a 404 means
an xbind without partitions). What it answers depends on who asks:

- **you**, for a tile you can read: its state, and your own partition's
  row — whether it is active or dormant (and why), running or not, when it
  last started, how many bytes it holds, how many cron jobs and bus
  subscriptions it keeps (with missed ticks and dropped deliveries), your
  egress ledger's totals, your log share, how many items wait in your
  [inbox](#partition-mail) — plus the tile's **trust panel**:
  who can change the code that runs on your data (the people who write it,
  every admin, the providers bound to it and their writers), whether saves
  reach it live, whether its primary is protected, its last code change
  (for a tile with deployments) and whether it runs **reviewed code only**;
- **the tile's writers and managers**: totals only — people, running
  instances, bytes, cron jobs, bus subscriptions;
- **admins**: every person's metadata row (with their inbox's counts), the
  personal binds on the tile and its orphaned partitions — never what a
  partition holds, its vault key names, its log lines or its mail;
- **the tile's own code** (its frames, backend, terminals): the tile's state
  and the features, nothing about people (not even the workspace policies).

Only the rows the caller sees are built, and a partition's bytes are
measured at most once a minute. `bx partition ls` prints it
([bx.md](bx.md)).

**Logs and status.** Each person's instance logs to its own file. `GET
/api/xbin/logs?component=<tile>` (and `bx logs` in a partition's terminal)
answers **your own partition's log**, at any access level — it is your
data. Nobody else reads it: not the tile's writers, not its managers, not
admins — unless you **share it** (`bx partition share-log <tile> --days
n`, at most 14 days; `--stop` ends it). While shared, the tile's managers
and admins read it with `&user=<your id>` (`bx logs <tile> --user <id>`); a
follow (`-f`) of it ends by itself once the share ends or you stop it. The
global instance's log stays where it was: admins, people with terminal
level and the tile's own credentials read it with `&xbin-partition=global`
(`bx logs <tile> --global`; a person's partition never does), and a
credential that acts in no person's partition (the root token, another
tile) reads it as before. `?partition=` is refused (400):
the credential decides. `GET /api/xbin/tile-status` and `bx status` in a
partition's terminal answer that partition's instance and disk and say
`partition: user:<id>`; admins' `GET /api/xbin/backends` lists people's
running instances inside their tile's row, metadata only (state, uptime,
memory, restarts, crash loop, last exit, an error's class — never its
text).

**Stopping, resetting, purging.** `bx partition stop <tile>` stops your
partition's instance (a tile manager or an admin may stop anyone's); its
data stays and the next request starts it. `bx partition reset <tile>`
deletes your partition's data on the tile — its data, vault,
registrations, ledger, log, mail, terminal layers and agent-session
history, in every deployment it has any — after you type
`<tile> user:<you>`, and erases the tile's backup keys of it, so its
archives can't be read any more; an admin may reset anyone's, and that
person is told. A partitioned
tile inside another tile's scope uses none of the scope's resources, so
its reset never touches the scope root's data or keys. A tile you can't
read answers as a missing one, and naming someone else's partition is
refused before anything of it is said. A partition whose person was
deleted, or whose tile was removed, is **orphaned**: it is deleted 30 days
later, or at once by an admin's `bx partition purge` — which lists what it
would delete and deletes it only with `--yes`. A live person's partition
is never purged.

**People's lifecycle.** Deleting a person stops their instances, revokes
their tokens and orphans their partitions; their personal binds, consents
and notices go with them, and — if they held partitions — their home and
agent-session history move to `data/orphans/<id>-<uid8>/`, so someone
created later under the same id starts with neither. Disabling a person,
or taking away their read access on a tile, stops their instances there at
once: their partition is **dormant** (nothing starts it, no cron job fires,
no delivery arrives) and resumes when they have access again.

**Credential resets.** An admin can still take over someone's account by
giving it a new credential. For a person who holds partitions, a sign-in
link minted for them (by an admin, or an org admin's reset by link), a
password set on them or a single sign-on email bound to them by someone
else is recorded in the audit log, and they are told — a push ("a sign-in
link for your account was created by `<admin>` at `<time>`") and a notice
in `GET /api/xbin/partitions`. With the workspace policy
**credentialResetConfirm** on (`bx policies set credential-reset-confirm
on`, admins) such a credential is **held**: the link answers "waiting for
`<person>` to confirm" when redeemed, and a new password or email is kept
aside while the old one keeps working. The person allows or refuses it from
any of their signed-in sessions, apps or devices (`bx partition credential
<id> allow|refuse`, `POST /api/xbin/partitions/credential-confirm`);
refusing revokes it. Unanswered, it takes effect **24 hours after they were
told**, which covers someone who lost every device; a refusal still
revokes it while it is unused. A link that was already used (or replaced,
or expired) answers "already effective": change your password and sign
out everywhere. A held link is stored so that no xbind without this check
— an older one after a downgrade — redeems it, and a link whose hold
can't be recorded is revoked at once. Repointing the workspace's **single
sign-on provider** (a new kind, issuer or client id) is the same for every
partition holder with a bound email: audited naming them, and each is
told; with the policy on, their sign-ins through the new provider wait for
their answer (or the 24 hours), and refusing unbinds their email. A person
without partitions, and anyone changing their own credentials, is
unaffected.

**Trust warnings.** While a partitioned tile runs its work tree live and
people who aren't admins can change its code — or the code of a provider
bound to it — `/alerts` (kind `partition-trust`), the trust panel and `bx
doctor` say so: their saves run on every person's data there. Once saves
no longer reach the primary — live reload paused or aimed at another
deployment, or the primary pinned to a checkpoint — the warning ends. `bx
doctor` also lists files the tile's own repository doesn't track (a file a
person leaves in the shared directory is everyone's) and the caps its
people's partitions met in the last day.

**Reviewed code only.** An admin can set a partitioned tile to run
reviewed code only (`bx partition reviewed <tile> on`, `POST
/api/xbin/partitions/reviewed`). It needs the tile's primary protected
([tile-deployments.md](tile-deployments.md): its code then moves only by a
tile manager naming the reviewed checkpoint) and so every provider bound to
it that isn't partitioned. While it is on, unprotecting any of them is
refused, and so is binding into the tile a provider whose primary isn't
protected; a gap that opens some other way shows as a trust warning. `off`
lifts it.

**Offload.** A partitioned tile can't be offloaded yet (409); nothing is
archived or stopped.

**When a tile is removed**, its people's partitions are orphaned and
deleted 30 days later with their backup keys; once nothing of the tile is
left, its partition mode record goes too, so a new tile at the path starts
from its own code's request.

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
each person's partition under a key of its own, and restoring one on
another machine needs the exported backup keys ([§Backups](#backups)).

## Not documented yet (TODO)

- the partitions page (`/xbin/partitions`), the admin tile's Partitions
  section, and the shell's consent prompts.
