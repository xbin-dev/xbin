# coding-sandbox — the builtin sandbox manager

A **sandbox manager** (docs/sandbox-manager.md, D115): it serves the
`sandbox-manager` contract, protocol 1, to the tiles bound to it — the agent
(a conversation works in a sandbox), `sandbox-terminal`, any tile — and runs
their sandboxes on a **backend**: xbind's own tile-sandbox runtime by default
(VMs where they run, docs/protocol.md §Tile sandboxes), or whatever a copy
adds (a cloud's API and ssh: AGENTS.md). Each copy of this template is a
manager of its own, with its own images, sizes and quotas (D122).

The contract is the manager's whole face to consumers; this page is what it
adds, and the operators' own API.

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
the people with write access to it — use `/ops/*`; they see every
sandbox's metadata and change who may use it, but no route reads or writes
a sandbox's contents except through a consumer's own partition.

## What it adds to the contract

- **Ids.** A sandbox's contract id is `sb-<10 hex>`; the substrate's
  sandbox behind it has a name of its own (never shown to consumers; errors
  that name it are rewritten to the id). Exec and snapshot ids are the
  substrate's.
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
base rebuilds it at the next use, and the old template sandbox goes. The
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
| `mode` | `""` | `vm`, `namespace`, or `""`: a VM where the substrate offers one, else a namespace |
| `images` | `base` (the substrate's base, no script) | above; one is the default |
| `sizes` | `small` 2 GiB/2/20 GiB, `medium`, `large` | `{id, title, memMiB, vcpus, diskGiB, default}`; sizes over the substrate's per-sandbox caps aren't offered |
| `quotas` | none | above |
| `layout` | `/work`, `/home/dev`, `dev` 1000:1000, `/bin/bash` | `{workdir, home, user, uid, gid, shell}` |
| `autoStopMin` | 0 (the substrate's) | a new sandbox's idle stop |

## The operators' API

Every route needs an operator (the owner, or a person with write access to
the tile): `403 not-allowed` otherwise. Errors are the contract's shape.

| Route | |
|---|---|
| `GET /me` | `{user, operator, self}` — anyone the tile serves |
| `GET /ops/state` | `{backend: {name, registered, error?}, runtime?, runtimeError?, offer: {caps, egress, images, sizes, notes}, config, images: [built image], sandboxes: [sandbox + {consumer, runtime, mode, diskBytes, execsRunning, base}], orphans: [{name, state, labels, created}], usage: {consumers, people}}` |
| `PUT /ops/config` | above → the state |
| `PATCH /ops/sandboxes/{id}` | the contract's PATCH body (name, visibility, members, shares, labels, egress, size, autoStopMin, version) → the sandbox |
| `POST /ops/sandboxes/{id}/start` · `/stop` | `?wait=` → the sandbox |
| `DELETE /ops/sandboxes/{id}` | 204 |
| `POST /ops/images/{id}/build` | 202 — (re)build an image now |
| `DELETE /ops/orphans/{name}` | 204 — a substrate sandbox the manager doesn't know (a creation cut short before it was written down) |

A built image is `{id, runtime, snapshot, setupHash, mode, state:
building|ready|error, detail, log, started, built}`.

## Backends

`_backend/backend.go` is the seam: a **Backend** is a `Fleet` (the
substrate's offer, and sandboxes by name: list, create — clones with
`from` —, get, patch, delete, start, stop) and a `Box` per sandbox (run,
execs and their output by offset, stdin, signals, resizes, the terminal
relay, files, trees, snapshots). The shapes are the Go SDK's
(`sdk/sandbox*.go`): the `xbin` backend is `*xbin.Sandboxes` and
`*xbin.Sandbox` themselves. Refusals are `*xbin.SandboxError` with the
contract's refusal enum; any other error answers `503 unavailable`.

The `fake` backend (`_backend/fake_*_test.go`: host directories, host
processes, host pseudo-terminals) exists in the tests alone — never in a
build of the tile; `contract_test.go` runs the contract's conformance suite
(`sdk/sandboxcontract`) against this manager over it
(`hack/tile-check.sh coding-sandbox`).
