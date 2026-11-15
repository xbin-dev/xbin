# Sealed components (system components)

A **sealed component** is a part of xbin that runs like a tile and is not
one of yours: its code is always the code of the xbin release that is
running. It has a page and a backend, runs in the tile sandbox under the
same limits, and keeps its data in the workspace. It is never a directory
you can open, edit, clone, deploy or delete.

People see them called **system components** ("System component · managed
by xbin"). "Sealed" is the word in code, in these docs and in the API
(`/api/xbin/sealed`, `sealed: true`).

> **Not the vault's "sealed".** The vault is *sealed* when its encryption
> key isn't loaded, and *unsealed* when it is ([auth.md](/docs/auth.md)).
> That is a state of the workspace's secrets. A sealed component is a
> component whose code is fixed by the release. The two have nothing to do
> with each other, and a sealed component needs the vault unsealed for its
> encrypted data like any tile.

xbin ships **one** sealed component: the workspace agent, `xbin/wsagent`
([workspace-agent.md](/docs/workspace-agent.md)) — one backend per person,
its model slot on the workspace's own LLM connector and nothing else, and
the right to act for the person whose partition it is, under xbind's
delegation. It is inert until an admin sets it up, and nobody has it until
it is given to them; a workspace that does nothing sees nothing new.

## What makes one

A table compiled into xbind lists each sealed component. A row says:

| | |
|---|---|
| its name | its path is `xbin/<name>` |
| its source | a tree embedded in the xbind binary |
| its partition mode | one instance, or one per person ([partitions.md](/docs/partitions.md)) |
| its interface slots | the ones an admin may bind |
| its grants and capabilities | what the release lets it reach |
| its data format | a number the release raises when its stored data changes shape |

Nothing in a workspace declares one. No `xbin.json`, flag or setting adds,
changes or removes a row, and `xbin` is a reserved top-level name: a
directory at `<workspace>/xbin/…` is never scanned, served or run, and no
tile can be created, cloned or imported there.

## How it differs from a tile

| | A tile | A sealed component |
|---|---|---|
| Code | a directory of the workspace; yours to edit | the running release's; no directory in the workspace |
| Updates | you merge them | with xbin: a new release is the new code |
| Terminal, coding agents, source view, change proposals | yes | none |
| Deployments, checkpoints, pausing live reload | yes | none: it always runs the release's code |
| Owner | a person or an organisation | none; workspace admins manage it |
| Access levels | read, write, terminal | read, or none |
| Grants | an approver decides | fixed by the release |
| Callable by other tiles | when granted | never |
| Partition mode | the manifest asks, a manager switches | fixed by the release |
| Interface slots | whatever its manifest declares | the ones the release declares |
| Backup | source and data | data only |
| Sandbox, limits, logs, cron, bus, notifications, vault | yes | the same |

**It is never a target.** A sealed component exposes no roles, provides no
interface and publishes no endpoint. No grant names it or its resources as
a target and no slot binds to it, so another tile's code can't call into
it or reach a person's partition of it. People use it from its own page.
Its resources, secrets and registrations are its own: an admin can't read
or write them through the API.

**Its page is a viewer, not the component.** A tile's page may use the
tile's own storage and grants directly, because a tile's page and backend
are one author's code. A sealed component's page runs in the browser of
whoever may read it, so the credential that page holds reaches the
component's own backend and nothing else: not xbind's API, not the
component's storage or bus, not another tile. The backend sees who is
asking and decides. Only the backend itself acts as the component.

## What an admin decides

Four things, with the tools used for tiles (the admin console, `bx`, the
API):

- **Who has access.** Read, or none: `bx access` / the Access panel /
  `PUT /api/xbin/access`. A person with read access may use it. Levels
  above read don't exist on it. An entry that names it at `write` or
  `terminal` is refused with the reason wherever entries are written (one
  entry, a new or edited user's whole list, the workspace defaults, an
  access request). **Nobody has it until it is given to them**: an entry
  reaches a system component only by naming it — in a person's access, an
  org's shares or the workspace's default access. A pattern such as `*` or
  `xbin/*` was written about tiles: it is kept and gives nothing here.
  Admins read every system component, as they manage it.
- **Whether it is on.** Enabled, disabled or hidden (`POST
  /api/xbin/lifecycle`). It can't be offloaded: there is no source to
  archive.
- **Its limits.** Memory, disk and the rest, as for a tile
  ([isolation.md](/docs/isolation.md) → Resource limits; `/xbin/limits`).
- **What its slots are bound to.** Only the slots the release declares,
  and only to the providers the release names for each
  (`bx bind xbin/<name> <slot>=<provider>`, the Interfaces tab,
  `POST /api/xbin/bindings`). Anything else is refused with the reason —
  for an admin's binding and for a person's own bind in their partition
  alike.
  The release decides this because a slot's provider sees what the
  component sends it. **The provider of a model slot reads the
  conversations people have with the component, and its answers steer what
  the component then does with their access.** The workspace agent's model
  slot can be bound only to the workspace's own LLM connector
  (`xbin:llm`), never to a tile: so it is the workspace's LLM provider —
  the one its admin or host configured for the connector
  ([llm.md](/docs/llm.md)) — that sees agent conversations, and nobody who
  merely writes a tile's code. The release may give a slot a **default**:
  a built-in provider it is bound to while you haven't bound it — for
  example its `llm` slot to the workspace's LLM connector (`xbin:llm`),
  once the connector is configured. Your choice always wins and no upgrade
  or restart undoes it: bind the slot to something else, or unbind it and
  it stays unbound. To go back to the default, bind the default provider
  yourself. `/xbin/system` and `GET /api/xbin/sealed` say which binding is
  the default's (`byDefault`). A default binding is not written to
  `xbin.json`.

Everything else is fixed by the release.

### What an admin can and cannot see

A workspace admin runs a sealed component; they don't get to read what
people keep in it.

**An admin can see** each component's state, version and hold reason; who
has access; its limits and usage; the log of its shared (global) instance;
which people have a partition of it and whether it runs; the list of its
backups.

**An admin can do**, for a partitioned sealed component, what they can do
for any partitioned tile ([partitions.md](/docs/partitions.md)): stop a
person's instance, reset or purge a person's partition, back partitions up
and restore one — into the same person's partition only, and the person is
told.

**An admin cannot**:

- read or write its resources, its secrets or its registrations through
  the API (`/kv`, `/blob`, `/vault`, the bus, cron): those answer only the
  component itself;
- open another person's partition: an admin's own requests reach the
  admin's own partition, as anyone's do. Restoring a partition backup into
  a different person's partition, or into the shared instance, is refused;
- read a person's partition log, unless that person shares it
  (`bx partition share-log`), for at most 14 days;
- read a backup through xbind: no route answers an archive's contents, and
  a single file can't be read out of a sealed component's backup
  (`POST /api/xbin/restore` with `file` is refused for one). A backup is
  restored whole, in place. Each person's partition has archives of its
  own, sealed under that partition's key
  ([partitions.md](/docs/partitions.md) → Backups);
- write its data through a restore: only a backup xbind encrypted itself
  is restored into it (see Upgrades, downgrades and data → Backups);
- open a terminal, a coding agent or a view-as session inside it: there is
  no terminal, and viewing the workspace as someone opens nothing of a
  system component — not its page, not its backend (the shared instance
  included), none of that person's partitions;
- read what it publishes on its bus: an admin's event stream carries none
  of it.

What this does not change, for a sealed component as for every
partitioned tile ([partitions.md](/docs/partitions.md)): a workspace admin
controls accounts and the workspace's backup keys. Someone who can reset a
person's credentials can sign in as that person (it is recorded and the
person is told), and someone who exports the backup keys and holds the
archives can decrypt them outside xbind. A plaintext-vault workspace
(`--insecure-vault`) archives no partition at all.

### Where to look

- **`/xbin/system`** — a page served by xbind itself, so it is there on
  every workspace whatever its shell's age: each system component, its
  version, whether it runs and why not, its logs, who has access, its
  limits and its connections.
- **`bx system`** — the same list in a terminal; `bx system <name>` for
  one, `bx logs xbin/<name>` for its log.
- **`GET /api/xbin/sealed`** — the data behind both
  ([protocol/sealed.md](/docs/protocol/sealed.md)).

The tile listings (`GET /api/xbin/components` and the admin console's
tables) leave sealed components out unless asked with `?sealed=1`, because
a shell or console written before them would offer a terminal and a source
view that don't exist ([compat.md](/docs/compat.md)).

## When it can't run

A sealed component that can't run is **held**: it starts nothing, its API
answers with the reason, and `/xbin/system` shows it.

| Hold | What happened | What to do |
|---|---|---|
| `no-build` | This install has no build of its backend for this release: the file is missing, isn't listed in the install's `bin/sealed/sealed.json`, or is no longer the file listed there (its SHA-256 differs). The message says which. | Reinstall the release bundle. xbind never compiles a sealed component on the host, and never runs a file the release didn't ship. |
| `foreign-data` | Data sits at its data key that it didn't write: a tile at a path such as `xbin~<name>` used the same key before. | Move that data away (the message names the directory), then restart xbind. It is never read, moved or deleted for you. |
| `newer-data` | Its data was written by a newer release, in a format this one can't read (after a downgrade). | Install the release named in the message, or a later one. Nothing was changed. |
| `stamp` | Its data stamp can't be read or written. | Fix `data/sealed/<name>.json` (permissions, disk), then restart. |
| `needs-sandboxes` | It keeps each person's data apart, and xbind runs without `--isolate`. | Run xbind with `--isolate`. |

### Partitions need sandboxes

A sealed component whose row says "one instance per person" runs people's
partitions exactly as a partitioned tile does: each person's backend in its
own sandbox, with their own encrypted data and vault
([partitions.md](/docs/partitions.md)). That needs `--isolate`.

- **Without `--isolate`, it is unavailable** (held, `needs-sandboxes`).
- **Except where there are no people.** Under `--no-auth` (every request
  is the owner's), with no sandboxes, it runs as **one global instance**
  so the component can be worked on. Under `--dev` with real users and no
  `--isolate` it is unavailable like anywhere else: one instance would hold
  everyone's data together. Moving a `--no-auth` workspace to `--isolate`
  later is a partition mode switch, which deletes that data.

## Upgrades, downgrades and data

**An upgrade** replaces the code. When xbind starts on a new release it
puts that release's code in place of the old, the component keeps its
path, access, bindings, limits and data, and its backend migrates its data
forward the next time it starts.

**What runs is checked every time it starts**, not only when xbind boots.
The copy of its files under `.xbin/sealed/<name>/` — the directory its
backend runs in — is compared with the release's, byte for byte, and
written again if it differs. Its backend binary must be the file the
install's `bin/sealed/sealed.json` lists, with that hash; if it isn't, the
component is held (`no-build`). The pages people load are never read from
that copy: xbind serves them from the release's own bytes in memory.
`.xbin` is xbind's own directory; under `--isolate` no tile's sandbox,
terminal or agent session has it mounted. Without `--isolate` nothing
separates a tile's backend from xbind's files, as
[isolation.md](/docs/isolation.md) says.

**A page left open across the upgrade** is the old release's. It does not
go on talking to the new backend: the page's first call to the component
after the restart is answered "updated with xbin" and the page reloads onto
the new release's frontend — in the workspace, in a window of its own and
in the app. Nothing a person typed into the old page is sent to the new
backend by it; what the old page had not saved is lost with it, as with any
reload.

**The data stamp.** `data/sealed/<name>.json` records the data format its
data is in and the release that wrote it. It is written the first time a
release with the component starts on the workspace, and raised when a
newer release first starts the component.

**A downgrade** to a release that reads only an older format holds the
component (`newer-data`). The data is left as it is; going back to the
newer release runs it again. A downgrade to a release from before a
component existed leaves its data on disk, unused.

**Backups** hold its data and nothing else: the code is the release's.
They go to the workspace's archiver (the `*` `@archive` binding) and each
records the data format. A restore into a release that reads only an older
format is refused; an older backup is restored and migrated forward at the
next start. A person's partition backup follows the same rule.

A sealed component's backup is **always encrypted, and only an encrypted
one is restored**:

- xbind encrypts ("seals") each archive under one of the workspace's
  backup keys and authenticates it end to end. The keys are derived under
  the vault's key and kept by the vault (`bx backup keys`). The archiver
  only stores the result.
- A restore takes an archive only if it authenticates under a backup key
  **this workspace created itself, for that very archive**, and names this
  component. A plain archive is refused: the archiver is a tile, and a
  plain archive is only its word about what the data was. An archive
  sealed under a key that came in with an imported key bundle is refused
  too: anyone who may import a bundle can make one, with any key in it.
  The same holds for a person's partition of a sealed component.
- **Moving to another host.** Because imported keys don't count, a sealed
  component's data is not recovered on a new machine from the archiver and
  a key bundle (that path brings back tiles). Restore the workspace's own
  directory instead — its `data/` holds the vault, the backup keys and the
  component's data — from a backup of the host or its volume; the
  archiver's copies then restore as before, their keys being the
  workspace's own again.
- A workspace without a vault barrier (`--insecure-vault`, `--no-auth`)
  archives nothing of a sealed component, and says so.
- Nothing is restored while the component is held for its data
  (`foreign-data`, `newer-data`, `stamp`).

## For builders

- You can't make one in a workspace. A tile that should update with a
  release is a [builtin tile or template](/docs/elements.md), which you
  own and merge.
- Other tiles can't call a sealed component or be bound to one. Where a
  release wants tiles to use something it runs, it says so in its own
  docs.
- **Writing one** (in the xbind source, [maintenance.md](/docs/maintenance.md)
  → Sealed components): its frontend talks to its own backend and to
  nothing else. `xbin.fetch('/api/xbin/<name>/…')`, `xbin.ws` and
  `xbin.url` on its own API work as in a tile; `xbin.bus`, `xbin.status`
  and direct calls to `/api/xbin/kv`, `/blob`, `/bus` or another tile are
  refused for its page, so live updates come from its backend (a stream or
  a socket under its own API). The page is reloaded when the release
  changes ([protocol/sealed.md](/docs/protocol/sealed.md) → One release on
  both sides).
- `GET /api/xbin/components` doesn't list them. A client that can show
  one asks with `?sealed=1` and must not offer a terminal, source,
  deployments, ownership or grants on a row with `sealed: true`
  ([protocol/sealed.md](/docs/protocol/sealed.md) has every route's
  answer).
- **Links and the app.** A sealed component's link is a tile link:
  `/c/xbin/<name>/…` on the web, `xbin://<workspace>/c/xbin/<name>/…` for
  the app, `c/xbin/<name>/…` in a notification. `xbin/<page>` stays the
  form of xbind's own pages. In the app (and `bx preview --native`) a
  component's native UI may name only its own files and API; a sealed
  component's (`/c/xbin/<name>/…`, `/api/xbin/<name>/…`) resolve once xbind
  listed it to that client (`?sealed=1`), and nothing else under `xbin/`
  ever does. An app or shell that doesn't ask keeps its behaviour.
- A top-level directory or tile whose name is `xbin~<something>` shares a
  data key with the sealed component `xbin/<something>`, should a release
  add one. The sealed component holds the key; such a tile keeps running
  and its resources are refused, with the reason on its row. Rename it.
