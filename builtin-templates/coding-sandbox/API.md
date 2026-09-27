# coding-sandbox — the builtin sandbox manager

A **sandbox manager** (docs/sandbox-manager.md, D115): it serves the
`sandbox-manager` contract, protocol 1, to the tiles bound to it — the agent
(a conversation works in a sandbox), `sandbox-terminal`, any tile — and runs
their sandboxes on a **backend**: xbind's own tile-sandbox runtime by default
(VMs where they run, docs/protocol.md §Tile sandboxes), or whatever a copy
adds (a cloud's API and ssh: AGENTS.md). Each copy of this template is a
manager of its own, with its own images, sizes and quotas (D122).

The contract is the manager's whole face to consumers; this page is what it
adds, the tile's own page, and the operators' API.

## Wiring

```sh
bx template new coding-sandbox as apps/coding-sandbox
# an admin approves cap:sandboxes (the tile's pending grants)
bx bind apps/agent sandboxes+=apps/coding-sandbox     # a consumer
bx bind apps/coding-sandbox internet=internet          # sandboxes may reach the internet
bx bind apps/coding-sandbox open=lan:10.42.0.0/16      # …and, as `open`, a network of yours
```

- **`provides.sandboxes`** — `{kind: http, service: sandbox-manager, role:
  consumer}`. The binding grants the consumer the `consumer` role, which
  reaches `/sbx/*` only.
- **`uses`** — its sqlite `db` and **`cap:sandboxes`** (xbind's tile-sandbox
  runtime; only a workspace admin approves it, docs/auth.md).
- **`interfaces.internet` and `interfaces.open`** — `sandbox-net` classes: the
  networks its *sandboxes* may use, never its backend's own. A consumer is
  offered `internet` while the `internet` class is bound to the public
  internet (reach `internet`), and `open` while the `open` class is bound to
  anything; unbound, both are left out and sandboxes get `none`
  (docs/overview/12-egress.md §Sandbox network classes). A sandbox's `egress`
  is its class's word, or its reach when that is wider — it never claims
  less than it can reach.

## Who is asking

As the contract says: the consumer is `X-XBin-From`; a person is verified
(`X-XBin-User`, a page's call) or asserted (`Sbx-User`, a backend's call).
`/sbx/*` admits the `consumer` role and the tile itself (its own page is a
consumer with a partition of its own). **Operators** — the tile's owner and
the people with write access to it — use `/ops/*`: they see every
sandbox's metadata and run its lifecycle (start, stop, delete, snapshots)
within the quotas they set. They never change who may use a sandbox: its
`visibility`, `members` and `shares` change only through its home consumer
(that consumer's backend, or its verified owner there). No route reads or
writes a sandbox's contents except through a consumer's own partition.

**The tile's own page** is a consumer like any other, with one more rule
(docs/auth.md, D29's rule for mutating endpoints): its calls run at the
tile's own role, so a change needs the verified person's **write** access
to the tile (write or terminal). With read access a person may look: hello,
the list, a sandbox, a running sandbox's files and trees, its execs and
their output, its snapshots. Every change is `403 not-allowed` before it is
routed: create, PATCH, DELETE, start and stop, run, execs and their stdin,
signals and resizes, terminals (both `tty` routes), file writes, moves and
removes, `PUT …/tar`, and taking, restoring or deleting snapshots. A read
never starts a stopped sandbox for them: that is `403 not-allowed` too, and
`409 state` while one starts. Calls from every other consumer are as the
contract says, whatever the person's level on this tile: the contract
trusts its consumers.

## What it adds to the contract

- **Ids.** A sandbox's contract id is `sb-<10 hex>`; the substrate's
  sandbox behind it has a name of its own, never shown to consumers. Errors
  that name it are rewritten to the id, and so are a clone's errors that
  name its source; an image's template sandbox is `image:<id>`. Exec and
  snapshot ids are the substrate's.
- **Its own table** (the `db` resource): per sandbox the id, runtime name,
  name, image, size, egress asked, owner, visibility, members, shares,
  labels, layout, version and an overlay state (`creating`, `deleting`,
  `error`); create and snapshot `clientId`s (per consumer; per consumer and
  sandbox); the built images; the config. Execs and their output are the
  substrate's; exec `clientId`s are deduped in memory, and passed to the
  substrate prefixed with the consumer, so two consumers of a shared
  sandbox never collide.
- **`version`** is the manager's: it grows with every change the manager
  makes — PATCH, start, stop, an auto-start, a restore.
- **Creating.** A plain or cloned sandbox is made before the create answers
  (`running`, or `stopped` with `start: false`). The first sandbox of an image
  with a setup script waits for the image to build: the create answers after
  at most `?wait=` seconds, `creating` until it is done (poll `GET`); a build
  that fails leaves the sandbox in `error`, its `stateDetail` the script's
  last lines. A restarted manager finishes creations and deletions it left.
- **Starting.** A stopped sandbox starts on a command, a file operation or a
  terminal, as the contract says; the manager starts it itself first, so the
  start counts against the quotas (`429 limit` when over). The first start
  makes the workdir and home.
- **Inside.** Every command runs as the layout's user (uid/gid) with `HOME`,
  `USER`, `IN_SANDBOX=1`, `SANDBOX_ID` and `SANDBOX_NAME`; `cmd` runs in the
  layout's shell. On a substrate that runs everything as root (the runtime's
  `users: root`), the user is root at `/root`.
- **`caps`** are the substrate's (`exec`, `files`, `tar`, `tty`,
  `snapshots`, `clone`); `archive` isn't offered yet (its routes answer 501).
- **`hello.limits`** are the substrate's, with `sandboxes` the caller's
  effective count quota, plus (additive) `running`, `memMiB`, `vcpus` and
  `diskGiB` — 0 is no fixed limit. `hello.notes`, when present, says what
  isn't offered and why.

## Images

An image is the substrate's base plus an optional **setup script**:

```jsonc
{"id": "node", "title": "Node 22 + pnpm", "tools": ["git", "node", "pnpm"],
 "setup": "apt-get update && apt-get install -y nodejs npm && npm i -g pnpm",
 "buildEgress": "internet"}
```

The first sandbox of an image builds it: a template sandbox of its own is
made and prepared, the script runs in it **as root** in the workdir (with
`IMAGE_ID`, `SANDBOX_USER`, `SANDBOX_HOME`, `SANDBOX_WORKDIR`), and the
sandbox is stopped and snapshotted. Every later sandbox of the image is a
**clone** of that snapshot. A changed script, a changed mode or an outdated
base rebuilds it at the next use. The old template sandbox goes only once
the new build is ready: **a rebuild that fails keeps the previous good
build**. While that build is still current for the script and the mode (an
outdated base, an operator's rebuild), new sandboxes clone it, and the
failure is the image's `detail`. A changed script whose build fails leaves
the new sandboxes of the image in `error` as a first build does; the old
build waits, and a script changed back clones it at once. The
build's network is `buildEgress` (default: `internet` where that class is
bound, else `none`). A built image keeps its template sandbox, stopped: it
counts against the substrate's sandbox limit for the tile. Building needs
the substrate's `snapshots` and `clone`; where they're missing, images with
a script aren't offered and hello's `notes` say so — plain images (no
`setup`) always are.

## Quotas

Set by operators (`config.quotas`), each field 0 for no limit:

```jsonc
"quotas": {
  "consumer":  {"sandboxes": 20, "running": 8, "memMiB": 16384, "vcpus": 16, "diskGiB": 400},
  "person":    {"sandboxes": 4, "running": 2},
  "consumers": {"apps/agent": {"sandboxes": 50, "running": 12}},   // replaces "consumer" for it
  "people":    {"alice": {"sandboxes": 10, "running": 4}}          // replaces "person" for her
}
```

A consumer's quota counts the sandboxes whose home it is; a person's, those
they own (`owner.user`, verified or asserted) across consumers. `sandboxes`
and `diskGiB` are checked when a sandbox is made (and a size change for
disk); `running`, `memMiB` and `vcpus` when one starts. The substrate's own
limits for the whole tile bind too.

## Config

`GET /ops/state` shows it; `PUT /ops/config` replaces the top-level fields
it carries (each whole) and answers the state:

| field | default | |
|---|---|---|
| `backend` | `"xbin"` | a registered backend; changing it needs no sandboxes or built images left |
| `backendConfig` | `{}` | the backend's own settings |
| `mode` | `auto` | `auto` (or `""`): a VM where the substrate offers VMs now, else a namespace (else another backend's first mode); `vm` or `namespace`: only that — while the substrate lacks it no sandbox is made (`503`, its reason; hello's `notes` say so), never another mode. A sandbox's `isolation` says the mode it got. Another backend may name its own (`container`, `cloud-vm`) |
| `images` | `base` (the substrate's base, no script) | above; one is the default |
| `sizes` | `small` 2 GiB/2/20 GiB, `medium`, `large` | `{id, title, memMiB, vcpus, diskGiB, default}`; sizes over the substrate's per-sandbox caps aren't offered |
| `quotas` | none | above |
| `layout` | `/work`, `/home/dev`, `dev` 1000:1000, `/bin/bash` | `{workdir, home, user, uid, gid, shell}` |
| `autoStopMin` | 0 (the substrate's) | a new sandbox's idle stop (the runtime's `idleStopMin`) |
| `mounts` | none | `[{res, path?, at, ro?}]`: filesystem resources this tile holds (`res:<scope>/<name>`, its own scope's or granted to it; `path` a clean sub-path) mounted at `at` in every new sandbox — never under `/proc`, `/sys`, `/dev`, `/run/xbin`, `/opt/xbin`; a reader grant is read-only whatever `ro` says. Image builds get none |

## The operators' API

Every route needs an operator (the owner, or a person with write access to
the tile): `403 not-allowed` otherwise. Errors are the contract's shape.

| Route | |
|---|---|
| `GET /me` | `{user, level, write, operator, self}` — anyone the tile serves: the person, their level on the tile (`read`, `write`, `terminal`; `""` with no person), whether they may change sandboxes on the page, whether they are an operator |
| `GET /ops/state` | `{self, backend: {name, registered, error?}, runtime?, runtimeError?, listError?, offer: {caps, egress, images, sizes, notes}, config, images: [built image], sandboxes: [sandbox + {consumer, runtime, mode, diskBytes, execsRunning, base}], orphans: [{name, state, labels, created}], usage: {consumers, people}}` |
| `PUT /ops/config` | above → the state |
| `PATCH /ops/sandboxes/{id}` | the contract's PATCH body without who may use it (name, labels, egress, size, autoStopMin, version) → the sandbox. `visibility`, `members` or `shares` are `403 not-allowed`: they change only through the home consumer |
| `POST /ops/sandboxes/{id}/start` · `/stop` | `?wait=` → the sandbox |
| `DELETE /ops/sandboxes/{id}` | 204 |
| `GET /ops/sandboxes/{id}/snapshots` | `{snapshots: [{id, name, created, bytes?}]}` — any consumer's sandbox (`501 unsupported` while the substrate keeps none) |
| `POST /ops/sandboxes/{id}/snapshots` | `{name}` → 201 the snapshot (it may stop the sandbox briefly) |
| `POST /ops/sandboxes/{id}/snapshots/{sid}/restore` | → the sandbox (its execs are killed) |
| `DELETE /ops/sandboxes/{id}/snapshots/{sid}` | 204 |
| `POST /ops/images/{id}/build` | 202 — (re)build an image now |
| `DELETE /ops/orphans/{name}` | 204 — a substrate sandbox the manager doesn't know (a creation cut short before it was written down) |

A built image is `{id, runtime, snapshot, setupHash, mode, state:
building|ready|error, detail, log, started, built, previous?}`. `previous`
is the last good build (`{id, runtime, snapshot, setupHash, mode, state:
ready, started, built}`), kept while this one is building or failed.

## The page

`index.html` draws `model/` with lit (`web.js`, `web-ops.js`,
`web-settings.js`, `web-mine.js`); the app's native view (`native.js`,
`native/`) draws the same model. `model/features.js` lists every feature,
and `web-features.js` / `native-features.js` say where each view implements
it (`hack/coding-sandbox-ui.test.mjs` holds them level, D96).

- **Operators** (write access to the tile) get four tabs:
  - **Sandboxes** — every consumer's sandboxes, most recently active first:
    state (and why), consumer, owner (asserted ones say so), who may use it
    (shown, never changed here), image, size, network, isolation, disk and
    last activity; start, stop, delete; snapshots (take, restore, delete).
    Usage by consumer and person against the
    quota that binds each; the substrate (its errors, modes, capabilities,
    hello's notes — and, while `cap:sandboxes` waits, who approves it);
    orphans.
  - **Images** — each image's build (built, building, failed and why, and
    the previous good build a failed or running rebuild keeps), its script
    and last output; build now; add, edit, remove.
  - **Settings** — the mode (and what new sandboxes get with it now, or why
    none can be made), the `sandbox-net` classes (bound to what, reaching
    what, offered or not, the `bx bind` to bind one), sizes, quotas (the
    defaults and each override), the layout, the idle stop and mounts.
  - **Yours** — below.
- **People with write access** get **their own sandboxes** (the operators'
  Yours tab): the page calls `/sbx/*` as a consumer of its own (its
  partition is this tile's path; the person is verified), within the
  per-person quota. Create
  (name, image, size, network, who may use it), start, stop, delete,
  visibility and shares; a **file browser** (list, view the first 256 KiB of
  a text file, download, upload, a new folder, remove) over the contract's
  `files/*` routes; a **terminal**: `<bx-terminal src="/api/<self>/sbx/sandboxes/{id}/tty?cwd=<workdir>">`,
  dialled with the frame token (docs/elements.md), ended at the manager
  (`DELETE …/execs/{session}`) when it is closed.
- **People with read access** get a read-only view (`/me`'s `write` is
  false) of the sandboxes they may use: the ones the team may use, or those
  they are a member of. They see the list and its facts, and a running
  sandbox's files (browse, view, download). Every change is hidden or
  disabled with the reason: New sandbox, start, stop, delete, sharing, the
  terminal, upload, new folder, remove. A stopped sandbox's files say that
  starting it needs write access.
- **The native view** has the same, one screen at a time. Its terminal is
  the app's `terminal` on the tile's own route: it starts a login shell as a
  `tty` exec (`POST …/execs {argv: [<shell>, -l], tty: true}`) and attaches to
  `sbx/sandboxes/{id}/execs/{eid}/tty`, so a reconnect is the same shell;
  leaving the screen ends it. Downloads go through the app's share sheet.
  Uploads are the one difference: the app uploads only from a composer.
- Tests: `hack/coding-sandbox-ui.test.mjs` (`make js-test`: the model, the
  parity, the native trees against `test/stub.mjs`); `test/web.mjs`
  (`PLAYWRIGHT_DIR=… node test/web.mjs`, `SHOTS=<dir>` for screenshots);
  the UI harness's `codingSandbox` pass (a copy on the fake backend, bound
  to the agent).

## Backends

`_backend/backend.go` is the seam: a **Backend** is a `Fleet` (the
substrate's offer, and sandboxes by name: list, create — clones with
`from` —, get, patch, delete, start, stop) and a `Box` per sandbox (run,
execs and their output by offset, stdin, signals, resizes, the terminal
relay, files, trees, snapshots). The shapes are the Go SDK's
(`sdk/sandbox*.go`). Refusals are `*xbin.SandboxError` with the contract's
refusal enum; any other error answers `503 unavailable`. Adding one — a
cloud's API and ssh, say — is `AGENTS.md`.

**`xbin`** (the default, `_backend/backend_xbin.go`) is xbind's tile-sandbox
runtime (docs/protocol.md §Tile sandboxes): `*xbin.Sandboxes` and
`*xbin.Sandbox` themselves, every method one `/api/xbin/sandboxes` route
(D120; D122). What the manager sends it:

- **create**: `mode` (above — never chosen by the runtime), the size's
  `memMiB`/`vcpus`/`diskGiB`, `net.egress` `none` or `class:internet` /
  `class:open`, `defaults` (the layout: cwd, uid/gid, shell, `HOME`, `USER`,
  `IN_SANDBOX`, `SANDBOX_ID`, `SANDBOX_NAME`), the operators' `mounts`,
  `idleStopMin` (`autoStopMin`), `for`/`forUser` (the consumer and the
  person, as claims), `labels` `{coding-sandbox/id}` (never the consumer's
  own), `clientId` = the runtime name, and `from` for clones and images;
- **PATCH**: `net`, the sizes, `idleStopMin`, and `defaults` on a rename;
- **commands**: `uid`/`gid` the layout's (the first start's prepare runs as
  root), `forUser` the person, exec `clientId`s prefixed per consumer;
- **terminals**: relayed byte for byte (`RelayTTY` / `RelayNewTTY`) with
  `forUser` = the person and the session frame's ids = the contract's; a
  refusal before the upgrade comes back with the runtime's name for the
  sandbox replaced by its id. The consumer's headers never travel;
- **ids**: exec and snapshot ids are the runtime's, as they are. One its
  grammar can't hold (`xbin.IsExecID`, `xbin.IsSnapshotID`) is `not-found`
  here and never reaches the runtime, so a consumer's id can't name
  another route or sandbox;
- **copies**: the runtime copies a snapshot, a clone and a restore off the
  request, and answers one still copying after its `waitMaxSec` as it
  stands — a snapshot `pending` (202), a clone `creating`, a restore with
  the sandbox's `stateDetail` `busy: …`. The manager waits each out
  (`_backend/settle.go`, polling while the caller waits), so the contract
  answers it done: an image's snapshot before its clones are made, a
  consumer's snapshot `201`, a restore the sandbox restored. The runtime
  clones only a stopped sandbox, or a snapshot: a clone of a running one
  without a `snapshot` (the contract's "the sandbox now") is made of a
  snapshot taken for it, which is deleted once the clone is made.

It needs **`cap:sandboxes`**, which only a workspace admin approves: until
then every call is refused and the page says who approves it. On an xbind
without `--isolate` the runtime runs nothing, and says so. Where the runtime
lacks something its answer leaves it out of `caps` (routes still being
built answer `501`): hello offers only what it lists — no images with a
setup script without `snapshots` and `clone`, no terminals without `tty`,
never `archive` — and `notes` say what is missing.

The **`fake`** backend (`_backend/fake_*_test.go`: host directories, host
processes, host pseudo-terminals) exists in the tests alone — never in a
build of the tile; `contract_test.go` runs the contract's conformance suite
(`sdk/sandboxcontract`) against this manager over it
(`hack/tile-check.sh coding-sandbox`). The UI harness copies it into a
throwaway instance (`backendConfig.root` `res:<resource>`), and nothing
else should.

## Testing on xbind

`_backend/backend_xbin_test.go` checks the `xbin` backend against a double of
the runtime's routes, and `_backend/contract_test.go` runs the conformance
suite over the fake backend. The live end to end is
`test/isolated/codingsandbox_test.go` in xbind's repo (D120's WP-21, beside
`examples/sandbox-go`): `TestCodingSandbox[VM]` walks this plan,
`TestCodingSandboxContract[VM]` runs the suite, on an `--isolate` xbind of
the test's own, or on another through `XBIN_E2E_URL`
(plans/tile-sandbox-runtime.md §13 has the commands):

1. An `--isolate` xbind (range-uid; KVM VMs; emulated VMs are a known
   issue: plans/tile-sandbox-runtime.md §14) with owner auth on, so
   people are accounts:
   `bx template new coding-sandbox as apps/cs`, approve `cap:sandboxes`,
   `bx bind apps/cs internet=internet`, and a consumer tile whose
   `sandboxes` slot is bound to it (`bx bind apps/csc sandboxes=apps/cs`).
   Binding another consumer doesn't restart the manager: calls in flight
   (relayed terminals, long polls) carry on.
2. Hello: `caps` are the runtime's (`exec files tar tty snapshots
   clone`), `egress` `none internet`, no `notes` but the missing ones.
3. The conformance suite through xbind's proxy, every section but
   `archive`: each consumer the suite names (`apps/ct-…`) is a tile bound
   to `apps/cs`, calling with its page's frame token (xbind sets
   `X-XBin-From`); a verified person is that token minted by the person's
   session (xbind sets `X-XBin-User`); an asserted one is `Sbx-User`. On a
   `--no-auth` xbind there are no verified people: the checks that act as
   them (`people/visibility`, `people/owners`, `partitions/shares`,
   `tty/refusals`) go in `Target.Skip`, saying so.
4. `mode`: `auto` gives `vm` with KVM (the sandbox's `isolation` `vm`),
   `namespace` without; `vm` on a host without VMs refuses the create with
   the runtime's reason.
5. The first start makes the workdir and home as root (the runtime must let
   a tile sandbox run uid 0); a command runs as 1000:1000 with `HOME`
   `/home/dev`, and a file written through the contract is 1000's.
6. Commands: `run`'s result, an output long poll answering when output
   comes, stdin, a signal to the group, the exec list; files with etags
   (`ifMatch`), move and list; tar both ways.
7. Snapshots, a restore, a clone of a snapshot and of the running sandbox
   (made of a snapshot taken for it and deleted after: the source's
   snapshots are as they were).
8. Images: a setup script builds once, as root, with `IMAGE_ID` and the
   layout in its environment (a template sandbox, snapshotted); the next
   sandbox of it is a clone (`from`), a changed script rebuilds.
9. Terminals through a consumer and through the page (`<bx-terminal
   src>`), with a gorilla client through the proxy as a page's `xbin.ws`:
   the session frame carries the contract's ids, a tty exec outlives its
   client; `forUser` reaches the runtime: a `noTerminal` person is refused
   (D88); people with read access to the tile may look and never change.
10. Quotas, per person (verified or asserted) and per consumer: a create
    or a start over one is `429 limit`; the operators' usage counts them.
11. The operators' views: every consumer's sandboxes with their consumer
    and owner, lifecycle and labels, never who may use a sandbox.
12. Egress: `internet` reaches the internet and not the LAN nor the host's
    own addresses (on a server, its public one); `none` reaches nothing;
    an unbound `open` isn't offered; a PATCH of a running sandbox's egress
    sets `egressNext`.
13. `idleStopMin` stops an idle sandbox; the next command starts it again
    within the quotas.
14. A restart of xbind: execs `lost`, sandboxes `stopped`, state kept (a
    read starts one again); a creation the restart cut (an image build) is
    finished by the manager.
