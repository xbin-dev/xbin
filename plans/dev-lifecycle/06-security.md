# 06 — Security: threat model and mitigations

> Status: live — the threat model for pause live reload, tile deployments and promotion: assets, actors, trust assumptions, every threat with its normative mitigation, enforcement point and test, the D78 ledger and residual risks (part of [plans/dev-lifecycle](README.md))

This document is normative for the implementation swarm. Vocabulary is
[01-glossary.md](01-glossary.md); the objects, operations and invariants
P1–P29 are [05-model.md](05-model.md). Mitigations say **must**, **should** or
**never**. Threats are T1–T20 (T13 is retired; the ID is kept so later IDs
stay stable); cross-cutting rules are C1–C10 (§3.1). Every threat names its
tests so [15-test-plan.md](15-test-plan.md) can collect them. New code lives
where [13-surfaces.md](13-surfaces.md) puts it: `internal/checkpoint` for the
checkpoint store, `internal/deployments` for the deployments plane.

Every "today" statement carries a `file:line` verified against this worktree.
The baseline is master: D112 is landed, and D113 is designed
([plans/tile-sandboxes.md](../tile-sandboxes.md)). Four defects that exist
today, and that this design must not inherit, were found while writing it
(§5.2).

## 1. Assets and actors

### 1.1 Assets

| # | Asset | Why it matters here |
|---|---|---|
| A1 | The primary's data and traffic: its scope resources, its vault, and every inbound edge (humans' requests carry `X-XBin-User`, `internal/proxy/proxy.go:290-304`) | The accident boundary exists to keep untested code away from it (P20). |
| A2 | What the primary runs: its pinned checkpoint, built artifact, `setup` env layer, and the inbound surface its code declares | Pinning (P9) and protection (P21) are integrity promises about this asset. |
| A3 | Other tiles' data and behaviour, reachable through the tile's outbound edges | Grants are the tile's (P11); a non-primary deployment uses them. |
| A4 | xbind's credentials and every tile's secrets: `.xbin/secret` (frame-token HMAC key), `.xbin/token` (owner token), the vault barrier, `data/users.json` | A host-side read through a planted symlink, or a tool run as xbind, exposes all of them (D78). |
| A5 | Humans' sessions: the session cookie, frame tokens, tile-origin cookies | A frame token is self-admin on its tile (`internal/broker/broker.go:482-484`); an unsandboxed frame acts as the viewer. |
| A6 | Deployment state: the record, the deploy log, the checkpoint store, the view repository | Checkpoints capture uncommitted and gitignored files, secrets included (T20). |
| A7 | Availability: the primary, and the workspace's disk, memory, CPU, build capacity and VM budget | Deployments multiply backends, builds and stored trees. |
| A8 | Accountability: deploy-log attribution, the D112 registry's rows, audit lines | An operation nobody can attribute cannot be reviewed or rolled back with confidence. |

### 1.2 Actors

| Actor | How it authenticates today | What this design gives it |
|---|---|---|
| Tile reader (`read`) | session; opens the tile's frames, whose frame principal is self-admin on the tile (`internal/broker/broker.go:482-484`) | Reads the primary's code through `/c/`, exactly as it reads the work tree today. Sees primary-scoped deployment facts only (C9). |
| Tile writer (`write`, including `noTerminal` accounts, D88) | as above | Opens non-primary deployment URLs, sees their facts, fetches the checkpoint remote. Operates nothing. |
| Terminal-level user (`terminal`) | session; terminal sessions gated by `CanTerminalTile` (`internal/term/term.go:229`) | Operates deployments at terminal level, and deploys to the primary under parity (P4). |
| Coding agent in a tile sandbox | the session's terminal token: the tile's element principal, `Via: "terminal"`, attributed to the human (`internal/auth/auth.go:471-477`, `:498-507`) | Everything its token's terminal level allows, on its session's target (P24), which is never a protected primary. Never a manager act: the manager gate needs `p.Component == ""` (T9), and `humanID` is empty for element principals (`internal/broker/orgsapi.go:73-78`). Follows instructions from untrusted content, so it is the likeliest actor to deploy something nobody reviewed. |
| The tile's runtime code in any deployment (backends, frontends) | instance token (`internal/auth/auth.go:445-449`, resolved at `:589-591`), frame token (`internal/auth/frametoken.go:226-232`) | Non-primary code runs with the tile's grants narrowed by edge policy. It never operates deployments (C4). |
| Other tiles as principals | their own instance, frame or terminal tokens | Reach only the tile's primary (P7). Refused by every deployments operation, whatever they are granted (T9). |
| Anonymous ingress | no credential; `X-XBin-From: ingress` (`internal/proxy/ingress.go:31`) | Nothing new: it never reaches a non-primary deployment. |
| Tile managers (user-owner, owning-org admins, workspace admins; D24/D33) | session; `mayManageTile` (`internal/broker/orgsapi.go:427-443`) | In a human session: seed, vault copy, deliveries, alwaysOn, edge policy, per-deployment limits, reassign the primary, protect it, purge checkpoints. |
| Workspace admins (root token, admin users, tiles granted `xbin` admin) | `IsAdmin` (`internal/broker/broker.go:466-475`) | Admin users and the root token: everything a manager can, on every tile, in a human session; restore (`internal/broker/backup.go:552-555`). Tiles granted `xbin` admin never pass the deployments plane's manager gate (T9). |
| Archiver tiles and their writers | their tile | The bytes a restore reads come from the archiver (`internal/broker/backup.go:424-441`); archives now also carry the record and the store. |

## 2. Trust assumptions

### 2.1 An accident boundary, not a trust boundary (P20)

A non-primary deployment keeps untested code away from the primary's data,
traffic and registrations **when everyone who can change the tile's code is
already trusted with it**. Every terminal-level user and every agent they run
can deploy anything to any non-primary deployment, and under parity to the
primary too, exactly as saving a file changes the primary today. Code written
by people who cannot write the tile (a proposed PR, D48) must never run in a
deployment; that needs separate principals and is out of scope.

### 2.2 The one trust line: the protected primary (P4, P21)

Protection is the only place this design separates people who can write the
tile's code. While it is on, terminal-level users and their agents can build,
test and promote into non-primary deployments, and only tile managers change
what the primary runs. The line must hold against a careless or
prompt-injected agent, so it is enforced server-side on every path in T16, not
by UI.

### 2.3 Boundaries that stay hard

- **D78:** nothing in this design runs a tool as xbind on data a sandbox can
  write, and xbind never follows a symlink out of such data (C5, C6, §4).
- **Authority stays per tile (P11):** a deployment never holds more than its
  tile. Governance capabilities never reach a non-primary principal (T14).
- **The [plans/auth.md](../auth.md) semantics:** inbound `X-XBin-*` headers are stripped
  (`internal/proxy/proxy.go:276-280`), element principals are default-deny,
  the owner is admin.

### 2.4 What this design promises

1. No deployment holds more authority than its tile; non-primary deployments
   hold less (P3, P11).
2. Nothing from outside reaches a non-primary deployment (P7). It never shares
   the host network, so no host port it opens is reachable (T4). A non-primary
   deployment's writes never land in the primary's data, vault, registrations
   or status (P6, P13, P14).
3. A pinned deployment runs exactly its checkpoint through every restart path
   (P9), whatever happens to the work tree.
4. What a pinned primary serves inbound (`template`, `exposes`,
   `expose.roles`, `provides`, `chrome`) follows its code, not the work tree
   (05-model.md §6). Under protection, what the primary runs, and so that
   surface, changes only by a tile manager's act (P21).
5. Every operation is attributed in the deploy log.

### 2.5 What it does not promise

1. That non-primary code cannot read other tiles' primaries: the `read` edge
   policy exists so it can (P3).
2. That non-primary code cannot exfiltrate what it reads: the net edge's
   default `inherit` gives it the tile's relay policy
   ([09-fabric.md](09-fabric.md) §5.8).
3. That seeded data or copied vault values stay away from terminal-level users
   and their agents. They can deploy code that prints them (T5, T8).
4. That D30 separates code writers from vault values. It stops humans reading
   values through the API (`internal/broker/vault.go:183-194`), not a writer
   whose backend prints them.
5. That a rollback restores data. Promotion and rollback move code (P10).
6. Protection against workspace admins, the host, the xbind binary, or an
   archiver tile's integrity (§5.1).

## 3. Threats

### 3.1 Cross-cutting rules

Every mitigation below relies on these. Each is testable on its own.

- **C1 — The acting deployment comes from the credential.** Instance tokens
  carry the deployment the runner started (T3c); frame tokens carry it inside
  their HMAC (T3b); tile origins carry it in their label; terminal and agent
  tokens carry a server-side target (T3d). A tile principal's bare self-call
  (`/api/<self>/…`) routes to the credential's deployment (P12). A URL
  qualifier (`/c/<tile>+<name>/`, `/api/<tile>+<name>/…`) selects a deployment
  only for human principals; from a tile principal it must name the
  credential's own deployment, or the request is refused.
  `XBIN_DEPLOYMENT` is advisory and never authorizes anything.
- **C2 — The role is resolved at decision time.** Instance and frame
  credentials name a deployment, never "primary". A terminal or agent token
  names a deployment or "the primary" (P24). Every check reads the current
  record, so reassigning the primary or turning protection on takes effect at
  once for credentials already issued.
- **C3 — Non-primary principals are default-deny on `/api/xbin/*` (P26).**
  Every core route is classified in one table as deployment-scoped (acts on
  the caller's deployment), primary-only (refused for non-primary principals)
  or neutral. An unclassified route refuses non-primary principals, reads
  included. A guard test over the apicheck route inventory fails on an
  unclassified route. A handler that nobody converted therefore fails closed
  instead of writing the primary's state
  ([research/serving-fabric.md](research/serving-fabric.md) §B.6). PR
  decisions are primary-only for instance principals.
- **C4 — The tile's own runtime principals never operate deployments**
  (05-model.md §10). Instance, frame, cron and bus principals of a tile are
  refused by every deployments operation. Backends cannot change their tile's
  code today (the tile directory is bound read-only,
  `internal/runner/runner.go:641`), and they must not gain that power.
- **C5 — No host-side following reads or walks inside sandbox-writable or
  tool-written trees.** Such trees are the work tree, resource mounts,
  materialized checkpoints and quarantine directories. xbind opens files in
  them only through `fsutil.OpenBeneath`/`OpenIn`
  (`internal/fsutil/beneath.go:19-45`), and never calls `os.Open`,
  `os.ReadFile`, `os.Stat`, `os.Chmod`, `os.Chown`, `filepath.EvalSymlinks` or
  a symlink-following walk on a path inside them. `os.RemoveAll` is allowed;
  it does not follow symlinks, and materialized trees keep owner-writable
  directories so it needs no chmod walk (T2). Bulk copies run in confine. A
  guard test in `TestNoDirectExec`'s style, `TestNoFollowingHostWalks`,
  refuses those calls in `internal/checkpoint`, `internal/deployments` and the
  checkpoint-serving code of `internal/server`, unless the call site says
  `// walk-ok: <why>` on its line or one of the two above (15-test-plan
  §3.13).
- **C6 — Every tool run is confined** (the ledger, §4). Under isolation a
  sandbox that fails to start is an error (`internal/confine/confine.go:114`,
  `:165-168`): the operation fails and the deployment keeps its previous code.
- **C7 — Fail closed, never onto the work tree.** An invalid record, a missing
  checkpoint, lost isolation or a store that fails verification refuses the
  operation or holds the start, with a reason. Nothing ever runs the work tree
  in place of a pinned checkpoint.
- **C8 — Mutations are non-GET with JSON bodies** (05-model.md §10). The
  view-as middleware refuses every non-GET
  (`internal/server/server.go:229-232`, `internal/server/impersonate.go:23-29`),
  so a deployments mutation must never be a GET or a WebSocket upgrade.
- **C9 — Who hears about a non-primary deployment.** Facts about the primary
  keep today's audience, the tile's readers. Anything that names a non-primary
  deployment (its name, builds, compiler output, status, would-notify lines,
  data operations, registry rows) goes only to:
  - admins;
  - humans with at least `write` on the tile, at their current level;
  - that deployment's own principals (frame and instance tokens bound to it),
    and the tile's terminal and agent session tokens while their user holds at
    least `write`.

  The primary's frame token is minted for every reader, so it is **not** an
  own principal for non-primary facts. Other tiles receive none of it. Log
  text keeps today's gate per deployment: terminal level for humans
  (`internal/obs/logs.go:42-47`), and a tile principal reads only its bound
  deployment's log and status (P12).
- **C10 — Every store this design adds names its tile.** The new stores
  (`data/deployments`, `data/checkpoints`, `.xbin/deploy`) are keyed by
  `<TileKey>`, a 128-bit hash of the path, and the record carries and verifies
  the full tile path, the owner ref and a creation stamp (05-model.md §3,
  P29). Existing stores keep their keys, and `util.CompKey` keeps only 32 bits
  of hash (`internal/util/util.go:137-144`), so collisions can be constructed
  for paths longer than 24 characters (§5.2 S3). Every per-deployment file this
  design adds under a `CompKey`-keyed store (a non-`main` vault, log or
  registration file) therefore records the full tile path, and a load whose
  path does not match is refused.

### 3.2 Summary

| ID | Threat | Severity if unmitigated | Key mitigation | Main enforcement point |
|---|---|---|---|---|
| T1 | Code execution as xbind through git on tile data | Critical | Confine only; a private store with pinned config and attributes | `internal/checkpoint` over `confine.GitCmd` |
| T2 | Poisoned checkpoint store or materialization | High | Quarantine, caps, fsck; OpenBeneath serving; cross-tile links re-dispatched, never followed | `internal/checkpoint`; the `/c/` openers |
| T3 | Cross-deployment privilege | High | Deployment-bound credentials; P12; P24 targets | `internal/auth`, `mayMintFrameToken`, `Broker.Policy`, the proxy |
| T4 | Mutation or exfiltration via outbound edges | High | One resolver; read clamp; unclampable edges and host networking blocked (P23) | the broker's edge resolver |
| T5 | Secrets | High | A vault per deployment; vault copy is a manager act | `internal/broker/vault.go` |
| T6 | Side effects from non-primary code | High | Registrations kept per deployment and delivered only to it; its routes dormant; deliveries a manager's off switch | cron, bus subscriptions, push, ingress, iface instances |
| T7 | Disclosure through events and reads | Medium | Non-primary activity only in `deployments`; audience filter; reader view | `handleEventsWS`, the deployments plane |
| T8 | Seeding and PII | High | Manager act; confined, direction-checked seeding | the data plane ([08-data.md](08-data.md)) |
| T9 | Holes in the authority matrix | High | One authorize function; the human-session manager gate; a matrix test | `internal/deployments` |
| T10 | Resource exhaustion | Medium | Caps, rate limits, bounded diffs, primary-first budgets | runner, cgroup, VM books, diskmon |
| T11 | Tampering, downgrade, restore | Medium | xbind-owned record; validation; restore checks | the record loader; restore |
| T12 | Non-isolated mode | Medium | Refuse backend deployments; hold on isolation loss | API and runner |
| T13 | Retired (the ID is kept so later IDs stay stable) | — | — | — |
| T14 | Chrome and xbin-capable tiles | Critical | P19 enforced continuously; principal clamp; chrome from the primary's code | broker, `/c/` plane |
| T15 | Fetch remote, tracked branch, deploy remote | High | A derived view repository, write-gated and allow-listed; confined, bearer-only, quarantined feeds | new endpoints |
| T16 | Protected-primary bypass | High | P21 on every path; `checkpoint`/`expect` compare-and-set | many (T16 table) |
| T17 | `setup` supply chain per deployment | Medium | Checkpoint bind; trust-separated layers | `internal/runner/env.go` |
| T18 | The shared sandbox layer | High | Per-deployment run dirs, binds, registry rows, cgroups; no-symlink mount points | runner, `internal/sbx`, cgroup, sandbox init |
| T19 | Tile-managed sandboxes per deployment (D113) | High | Sets keyed by deployment | the D113 API |
| T20 | Checkpoint content disclosure and retention | Medium | Read gates; purge | `internal/checkpoint` |

### T1 — Code execution as xbind through git on tile data

**Vector.** A tile's `.git` is writable from its terminals and agents (D78).
Its config names commands: `core.fsmonitor`, `core.hooksPath`,
`filter.<d>.clean|smudge|process`, `diff.external`, `core.sshCommand`,
`core.askPass`, `credential.helper`, `include.path`. Its `hooks/`,
`objects/info/alternates` and `.gitmodules` do the same. In-tree
`.gitattributes` bind drivers and rewrite content (`text`, `eol`, `ident`,
`working-tree-encoding`, `export-subst`, `export-ignore`). File names can be
argument-shaped (`--upload-pack=…`, `-c…`). Checkpoint capture, diff, GC, the
view repository's refresh and the later rungs all run git over tile content.

**Impact.** Code runs as xbind: every tile's vault and every user's data
(A4). A content rewrite breaks P9, because the checkpoint is not the work tree.

**Mitigation.**
1. The checkpoint store and the view repository are bare repositories that
   xbind creates with a confined `git init --bare`. xbind writes their
   `config` and `info/attributes` from constants; nothing from the tile is
   ever copied into either.
2. Store operations must run as `--git-dir=<store> --work-tree=<work tree>`
   (the D77 pattern, `internal/term/agentdiff.go:115-124`) through
   `confine.GitCmd` (`internal/confine/git.go:50-68`). They must never use
   the tile's `.git` as the repository, never `-C <tile>`, and never
   `runGitIn`, which binds its directory read-write
   (`internal/broker/code.go:64-80`).
3. Every run passes, beyond `gitFlags` (`internal/confine/git.go:14-23`) and
   `gitEnv` (`:28-34`), these as `-c` flags: `core.attributesFile=/dev/null`,
   `core.excludesFile=/dev/null`, `core.autocrlf=false`,
   `core.symlinks=true`, `core.fileMode=true`, `core.ignoreCase=false`,
   `core.precomposeUnicode=false`, `core.protectNTFS=true`,
   `core.protectHFS=true`, `core.quotePath=false`, `gc.auto=0`,
   `submodule.recurse=false`, `transfer.fsckObjects=true`,
   `protocol.allow=never`. Only runs that fetch override the last one, per
   run and for the `file` transport only: the view repository's refresh (L6),
   restore (L13) and the later rungs of T15.
4. `<store>/info/attributes` is
   `* -text -filter -ident -working-tree-encoding -export-subst -export-ignore !diff !merge`.
   `$GIT_DIR/info/attributes` outranks in-tree `.gitattributes`
   (gitattributes(5)), so capture stores bytes exactly and no driver the tile
   names resolves. Diffs add `--no-ext-diff --no-textconv`.
5. Gitignore rules do not apply to a checkpoint (glossary). Capture uses
   `add -A --force` with the excludes file pinned to `/dev/null`, and
   excludes nested components by pathspec. The same confined run evaluates
   the tile's own ignore rules to compute the checkpoint's git view (T15a);
   ignore patterns are data, and no host or global excludes file applies.
6. Tile-controlled strings (paths, and branch names in T15) never appear on
   git's command line. Pathspecs go through
   `--pathspec-from-file=<f> --pathspec-file-nul`; names that must be argv
   follow `--end-of-options`. Object ids are validated as 40 hex digits
   before use.
7. The store and the view repository never have `objects/info/alternates`
   files of their own. Each confined run binds only this tile's store, view
   repository and work tree (`confine.Cmd.binds`,
   `internal/confine/confine.go:218-224`), so an alternates path anywhere
   else cannot resolve.
8. Without isolation, confine runs git directly
   (`internal/confine/confine.go:140-143`) with the same flags. Capture still
   reads no tile git config, because the repository is the store. D78
   accepts that this mode has no boundary (T12).

**Enforcement.** `internal/checkpoint` (new), wrapping `confine.GitCmd`.
`TestNoDirectExec` (`internal/confine/guard_test.go:22-66`) covers the
package automatically; it is not in the exempt list (`:27`).

**Tests.**
- `TestCaptureIgnoresTileGitConfig` (`internal/checkpoint`,
  `linux && integration`, modelled on `TestHostileRepoStaysInside`,
  `internal/confine/sandbox_linux_test.go:40-81`). The tile's config sets
  fsmonitor, `diff.external`, `filter.x.clean` and hooks; `.gitattributes`
  sets `filter=x`, `text eol=crlf`, `ident` and `working-tree-encoding`.
  Capture, materialize and diff: no marker is written anywhere, and every
  blob is byte-identical to its file.
- `TestCaptureDirectModeHardening` (unit, direct mode, in the style of
  `TestGitDirect`, `internal/confine/confine_test.go:31`): argv carries the
  pinned `-c` set; the store's config and attributes are the constants.
- `TestCapturePathNamesNeverArgv`: files named `--output=x`, `-c` and
  `:(glob)*` are captured correctly, and no tile string reaches argv.
- The new package must be added to the `make integration` package list, or
  its tests never run ([research/delivery-infra.md](research/delivery-infra.md) §1.4).

### T2 — Poisoning the checkpoint store and materialization

**Vector.**
- Trees that escape when checked out: `..`, `.git` in any case, gitlinks,
  symlinked directories followed by writes beneath them.
- Pushed objects (T15) that are malformed or decompression bombs.
- Huge trees: gitignore does not apply, so `node_modules` is captured.
- xbind reading a materialized file on the host (manifest fields,
  `scope.json`, index presence) through a symlink such as
  `xbin.json → ../../.xbin/token`.
- Serving a checkpoint with the legacy opener, which follows symlinks
  anywhere in the workspace (`OpenResolved`, `internal/server/static.go:193-219`).
- Cross-tile links inside a checkpoint (`deps/<name>`), followed on disk.

**Impact.** Files written outside the tree; xbind secrets served, logged or
parsed (A4); disk exhaustion (A7); a pinned primary's imports re-pointed by a
work-tree edit (A2).

**Mitigation.**
1. **Quarantine.** Capture writes objects into a per-run object directory
   (`GIT_OBJECT_DIRECTORY`, with the store as
   `GIT_ALTERNATE_OBJECT_DIRECTORIES`). A checkpoint is admitted, meaning its
   objects move in and its ref is written, only after the caps and `fsck`
   pass. The move is part of the confined admission run (L2), never a host
   walk over the quarantine (C5). A refused capture leaves the store unchanged
   and the deployment on its previous code.
2. **Caps.** Files, total bytes, largest file, path length, depth and time
   ([07-runtime.md](07-runtime.md) sets the numbers). A refusal names the cap
   and the largest paths.
3. **Tree hygiene at admission.** fsck's `hasDotgit`, `badTree`,
   `zeroPaddedFilemode` and friends are errors. Gitlinks are refused: the
   checkpoint excludes nested repositories by definition. Symlinks are kept
   as mode 120000 entries (glossary).
4. **Materialization.** `read-tree` into a private index plus
   `checkout-index -a`, inside confine, with only a fresh temporary directory
   bound read-write and the store read-only. The same run sets file modes to
   `0444`, or `0555` where the exec bit is kept, and directory modes to
   `0755`, so host `os.RemoveAll` (and `rm -rf .xbin`) works with no chmod
   walk. A materialized tree is read-only to every sandbox by bind flags, not
   by host modes. xbind then renames the directory into
   `.xbin/deploy/<TileKey>/<full tree hash>/`. Never `git archive`, which
   honours `export-*` attributes. A materialized tree is never written again;
   if a crash leaves it in doubt, it is deleted and materialized afresh.
5. **Serving.** Every opener that serves a checkpoint must open files with
   `fsutil.OpenBeneath(<materialized root>, rel)`
   (`internal/fsutil/beneath.go:19-34`), in every asset mode: the legacy and
   strict planes, asset tokens, tile origins and the native document. In-tree
   symlinks resolve; FIFOs and devices are refused (`:13-17`). `OpenResolved`
   and the dev overlay never apply to a checkpoint. `SafeJoin`
   (`internal/util/util.go:81-93`) and `pathAllowed`
   (`internal/server/static.go:347-361`) still apply to the URL.
6. **Cross-tile links are never followed on disk** (P16).
   - A request for `deps/<name>/<rest>` inside a checkpoint is re-dispatched
     to the target tile's `/c/` plane, which serves that tile's primary.
   - The target comes from the checkpoint's own `deps/<name>` entry, resolved
     against the tile's canonical path to a registered component. It never
     comes from the work tree's manifest or `deps/` links, so a work-tree edit
     cannot re-point what a pinned primary imports (P9).
   - The re-dispatched request is authorized exactly as a direct request for
     `/c/<target>/<rest>` by the same principal, in every asset mode, so it
     never serves what that request would refuse.
   - Any other symlink that leaves the checkpoint answers 404 (`ErrEscapes`).
   - Confined Go builds get the other tile's pinned primary checkpoint, or its
     work tree while that primary follows it, bound over its `go.work`
     directory (T18).
7. **xbind-side reads.** Deployment-level manifest fields (`runtime`,
   `entry`, `setup`, `alwaysOn`, `vm`, `inject`, `native`), `scope.json`'s
   `resources` (P22) and index presence come from the materialized tree
   through `OpenBeneath`, never from `os.ReadFile` or `os.Stat` (C5). Today
   the registry reads `scope.json` with `os.ReadFile`
   (`internal/registry/registry.go:447`); a checkpoint's never is. A
   checkpoint's resource names are validated against the resource-name
   charset before anything is provisioned. A read or validation that fails
   refuses the deploy (C7), and file contents never appear in errors.
   `/components`' `runtime`, `hasIndex` and `native`
   (`internal/server/api.go:174-179`) describe the primary's code, read the
   same way (05-model.md §6).
8. Backends and builds see a materialized tree only through a read-only bind
   at the canonical path (T18).
9. Checkpoint ids from clients are validated (`c:` plus hex) and resolved
   only in the tile's own store. An ambiguous prefix is refused.

**Enforcement.** `internal/checkpoint`; one resolver from (tile, deployment)
to a root, used by `openLegacy` (`internal/server/static.go:193`),
`openStrict` (`internal/server/tileassets.go:133`), `serveAssetToken`
(`:275`), the tile-origin plane and `serveNativeRoute`.

**Tests.**
- `TestAdmissionRefusesBadTrees` (hand-made trees: `..`, `.GIT`, a gitlink,
  a bad mode).
- `TestMaterializeSymlinkEscape` (`a → /etc`, `b → ../../.xbin/secret`, a
  write under a symlinked directory: nothing is written outside the tree).
- `TestMaterializeAtomicReadOnly` (files `0444`/`0555`, directories `0755`;
  host `os.RemoveAll` removes the tree; a backend cannot write it).
- `TestPinnedServingUsesOpenBeneath` (`internal/server`: an escaping symlink
  answers 404, an in-tree one is served; a `deps/<name>/x.js` request is
  re-dispatched and authorized as `/c/<target>/x.js`; a work-tree `deps` edit
  does not re-point a pinned primary).
- `TestCaptureCapsRefuseHugeTree`.
- `TestCheckpointManifestReadNoFollow` (`xbin.json` or `scope.json` is a
  symlink to `.xbin/token`: the deploy is refused and no content is logged).
- `TestProvisionFollowsCode` (an invalid resource name in a checkpoint's
  `scope.json` is refused before anything is provisioned).

### T3 — Cross-deployment privilege

**T3a — Non-primary code reaching the primary with self-admin.** The self
rule grants admin whenever `p.Component == target.Path`
(`internal/broker/broker.go:482-484`), and an instance principal is only a
path (`internal/auth/auth.go:589-591`). A non-primary backend calling
`/api/<self>/…` would land on the primary as admin.
- `Principal` gains `Deployment` (empty means `main`). The self rule compares
  (component, deployment) with the target deployment. A terminal or agent
  token that follows the primary is bound, per request, to the current
  primary (C2).
- A self-call on the bare URL routes to the caller's deployment and gets
  self-admin there only. A tile principal naming another deployment of its
  own tile with `+name` gets 403 naming P12.
- Cron and bus deliveries carry their target deployment in the request
  context, set by the dispatcher. Today the dispatcher composes
  `/api/<comp><path>` from stored strings (`internal/broker/cron.go:200-208`)
  and `Policy` trusts a cron or bus principal's bound role without checking
  the target (`internal/broker/broker.go:485-487`). The deployment must
  therefore never be encoded in that string, and the proxy must ignore a
  qualifier on synthetic requests.

**T3b — Frame tokens across deployments.** `mayMintFrameToken` accepts any
principal of the same component (`internal/server/static.go:494-503`), and so
does renewal (`internal/server/api.go:244`). Non-primary JavaScript, or a
terminal token that targets a non-primary deployment, could fetch
`/c/<tile>/` with its own credential and scrape the primary's token from the
injected meta (`internal/server/static.go:435-436`). With it, that code is
self-admin on the primary.
- The token carries the deployment inside its HMAC: a sixth field, absent for
  `main`. Today `verifyFrame` accepts 4 or 5 fields
  (`internal/auth/frametoken.go:253-266`); 4- and 5-field tokens keep
  verifying, as `main`.
- For **every** document, bare or qualified, minting is allowed only (a) for
  a human who may open that deployment — `read` for the primary, `write` for
  a non-primary deployment, checked on every mint and renewal — or (b) for a
  frame, terminal or agent principal of the tile whose bound deployment
  equals the document's. Any other principal of the tile gets the document
  with an empty token (`content=""`).
- The same-tree navigation exception (`internal/server/static.go:502`) never
  crosses deployments. Renewal copies the claim.
- Origins mode: every deployment gets its own label, and the tile-origin
  cookie and `TilePrincipal` (`internal/auth/assettoken.go:215`) carry the
  deployment. `allow-same-origin` (`internal/server/tileassets.go:78-84`) is
  added only when the origin's deployment is the document's, so no
  deployment reads or writes another's storage.
- Tokens mode: asset tokens bind the deployment. `serveAssetToken` serves the
  token's deployment, never one chosen by the path.
- Every request that reaches a non-primary deployment on behalf of a human,
  through `/api/<tile>+<name>/` or a frame principal's self-call, re-checks
  that human's `write` level.

**T3c — Instance tokens.** The token map is token → path
(`internal/auth/auth.go:219`, `:445-449`).
`RegisterInstance(token, comp, dep)` takes the deployment from runner state,
never from env or headers. A token dies at process exit
(`internal/runner/runner.go:619`), so the 30 s blue/green overlap stays inside
one deployment.

**T3d — Terminal and agent targets (P24).** A terminal token is (component,
user) (`internal/auth/auth.go:242-245`), and the shell sees it as
`XBIN_TOKEN` (`internal/term/term.go:775-777`).
- The session's target is bound into the token server-side when the session
  opens: a named deployment, or "the primary", resolved at decision time
  (C2). Setting `XBIN_DEPLOYMENT` by hand changes nothing (C1).
- The target is chosen in the terminal window's existing API dropdown
  (`web/frame-titlebar.js:162-167`), which gains one entry per deployment the
  user may reach. There is no separate target picker. The default is the
  primary. A protected primary is never offered, and the default then falls to
  the live reload target. When neither exists, the dropdown falls to "API
  off", in which no terminal token is minted at all (`api=0`,
  `internal/term/term.go:188-190`).
- The target is fixed for the session's life. Changing it restarts the
  session with a new token, as the network and API pickers already do
  (`web/frame-titlebar.js:154`). No API re-binds a live token.
- Opening a session whose requested target is a protected primary is refused
  with a 403 that names the protection.
- Protecting the primary restarts every session that targets it onto P24's
  default, or ends it (05-model.md §5). Until the restart lands, such a
  token's self-calls, self-scoped API calls, frame-token mints and qualified
  URLs on the protected primary answer 403 naming the protection, because
  protection is read at decision time (C2).
- A terminal or agent token may mint a frame token only for its target
  (T3b); `internal/server/static.go:498` accepts any principal of the tile
  today. Otherwise an agent reaches the primary's self-admin through
  `/c/<tile>/`.

**T3e — URL shadowing.** `Resolve` picks the longest registered prefix
(`internal/registry/registry.go:519`), and `+` is legal in tile paths
(`internal/util/util.go:97-114`). Today non-admins are refused only `:`
(`internal/broker/policy.go:162-166`). A tile at `apps/crm+dev` would shadow,
or be shadowed by, deployment `dev` of `apps/crm`.
- A qualified URL resolves only for a tile that has a deployment record, and
  only after today's resolution fails. Every existing path resolves as today,
  including a directory whose name contains `+` and `<tile>+main` on a
  zero-state tile.
- Two narrow refusals apply to every creator (create, clone, import,
  template-new; admins and `xbin:writer` elements included, before any admin
  early return):
  - creating a tile at `<P>+<N>` while `P` has deployment `N`;
  - adding deployment `N` to `P` while a component exists at `<P>+<N>`.
- Other new tile names containing `+` get a one-release warning, the D82 way
  ([/docs/compat.md](/docs/compat.md) rule 11), never a refusal.

Whichever of the two paths exists first blocks the other, so neither can
shadow the other.

**Other tiles and ingress.** A deployment URL refuses every principal except
humans with ≥ `write`, the tile's own terminal and agent tokens that target
it, and that deployment's own principals. `<tile>+<primary name>` works for
humans and the tile's own principals; other tiles use the bare URL.
`ForwardIngress` always resolves the primary and never parses a qualifier.

**Tests.**
- `TestSelfCallStaysInDeployment` (a non-primary instance principal's
  `/api/<tile>/` reaches its own deployment as admin; `/api/<tile>+main/`
  gets 403).
- `TestFrameTokenBoundToDeployment` (`internal/auth`: the claim is inside the
  HMAC; a forged claim fails; a 5-field token verifies as `main`).
- `TestMintRefusesCrossDeployment` (`internal/server`, both directions: a
  `dev` frame or `dev`-targeted terminal token fetching `/c/<tile>/` gets
  `content=""`, and so does a primary frame fetching `/c/<tile>+dev/`;
  renewal re-checks `write`).
- `TestSessionTarget` (the default is the primary; with the primary
  protected, the live reload target; with neither, API off and no token).
- `TestTargetProtectedPrimary` (a session requesting a protected primary is
  refused; protecting restarts or ends the sessions that targeted the
  primary, and their old tokens get 403 at once).
- `TestTerminalTargetBinding` (`XBIN_DEPLOYMENT` edits change nothing).
- `TestTerminalTokenCannotMintProtectedPrimaryToken`.
- `TestDeploymentURLRefusesOtherTiles`.
- `TestIngressNeverReachesNonPrimary`.
- `TestPlusReservedInNewTilePaths` (the two refusals for every creator,
  admins included; a warning, never a refusal, for any other `+`).
- `TestOriginLabelPerDeployment` (origins mode).

### T4 — Data mutation or exfiltration via outbound edges

**Vector.** Grants are keyed by component
(`internal/broker/broker.go:418-452`), and a binding is its own grant
(`httpBindingRole`, `internal/broker/netfn.go:286`). Streams dial the
provider's current backend (`DialInto`, `internal/runner/ingress.go:32`).
Provider splices re-register per client and close the previous link fd
(`internal/runner/netmux.go:22-32`). Bus publishes fan out by resource alone
(`internal/broker/resources.go:410-437`), and the WebSocket filter checks only
the component's grant (`:441-458`). A net slot that shares the host network
(`Broker.NetHostShare`, `internal/broker/netfn.go:196-209`: the `host`
builtin, or an org, personal or named set whose rules say host) would put a
non-primary backend in the host network namespace.

**Impact.** Non-primary code writes other tiles' primary data. Its events
reach consumers of the primary. It hijacks or kills the primary's provider
links. On a host-sharing tile it listens on host ports that outside traffic
reaches, grabs the primary's fixed port during a restart, or dials host-bound
listeners of primaries directly (A1, A3).

**Mitigation.**
1. **One resolver.** Every access decision for a non-primary principal goes
   through `resolveTarget(caller, callerDeployment, target, edgePolicy)`
   (05-model.md §7): proxy HTTP calls, `allowRes` for resources, bus publish
   and subscribe, stream forwards, and the net plane's roster, target and
   links hooks. No path computes access outside it.
2. **`read`.** The role is clamped to `reader` only when the granted role
   implies `reader` in the built-in ordering
   (`internal/broker/broker.go:385-388`), or through the provider's own
   `implies` ([09-fabric.md](09-fabric.md) §5.5). A custom role with no path to
   `reader` is an edge that cannot be read-clamped (item 3). Two such edges to
   state plainly:
   - the agent inbox's custom `channel` role;
   - the agent tile reaches its `sandbox-manager` providers through the custom
     role `consumer` (the sandbox-managers programme, on another branch), so a
     non-primary deployment of the agent tile is blocked from its sandbox
     managers. Agent sandbox work is tested from the primary.

   llm-gw guards completions with `writer`: under the clamp, an LLM-using
   tile's non-primary deployments can list models but can't run a turn.
3. **Edges that cannot be read-clamped are blocked for non-primary
   deployments in v1, with no override (P23):** custom roles with no path to
   `reader`, stream interfaces, lan-ingress links and net-provider splices.
   [09-fabric.md](09-fabric.md) §5 lists them. No edge-policy value un-blocks
   them in v1. A non-primary deployment never joins a provider roster: its
   registration would close the primary's link fd
   (`internal/runner/netmux.go:28-30`).
4. **The net edge and capability grants.** `inherit` (the default for the net
   slot) gives non-primary deployments the tile's relay policy in their own
   network namespace, never host networking or a provider splice.
   - A tile whose `net` resolves to host sharing (`Broker.NetHostShare`) gives
     its non-primary deployments no egress (`block`, P23), and the panel shows
     why.
   - A net slot bound to a provider tile is a splice, so non-primary
     deployments get no egress through it (P23).
   - `gpu:*` defaults to `block`; other capability grants default to
     `inherit`.
5. **`block`** fails closed with an error naming the edge and the policy.
   Refusals and clamps are visible: the deployments panel counts them per
   edge, and a clamped call's response names the clamp.
6. **Who enforces the clamp.** For resources and cron, which live under
   `/api/xbin/*`, xbind enforces it (`allowRes` with `writer`,
   `internal/broker/broker.go:493-509`). For HTTP edges the provider's code
   does, by reading `X-XBin-Role`. Providers must treat `reader` as
   read-only, and [/docs/auth.md](/docs/auth.md) must say so at implementation time.
   `X-XBin-Deployment` is trustworthy because `identify` strips inbound
   `X-Xbin-*` headers (`internal/proxy/proxy.go:276-280`).
7. **Bus.** A non-primary publish lands in the deployment's namespace. The hub
   event carries the namespace, and `busFilter` and the push fan-out deliver
   it only to that namespace's subscribers. Subscribers of the primary never
   receive a non-primary event.
8. Frame principals of non-primary documents, and terminal and agent tokens
   that target a non-primary deployment, are non-primary principals for every
   edge.
9. Setting an edge policy is a manager act (05-model.md §10). An edge
   approved later starts at its kind's default (`read`, `inherit`, or `block`
   for `gpu:*` and for edges that cannot be read-clamped), and the approval UI
   must show what non-primary deployments will get ([10-ux.md](10-ux.md)). An
   unknown or invalid value reads as `block`, and any `block` among several
   authorizing edges refuses the call (P27).

**Enforcement.** A new resolver file in `internal/broker`; the proxy's
`Policy` call (`internal/proxy/proxy.go:160-171`); `allowRes`, `busFilter`
and `apiBusPublish` (`internal/broker/resources.go`); `bussubs.go`;
`hostDial`/`DialInto` (`internal/runner/ingress.go`); the `netfn.go` hooks;
the backend launch spec's network mode.

**Tests.**
- `TestReadClampOnEveryEdge`, a table: an http binding `writer` becomes
  `reader`; a grant `admin` becomes `reader`; a resource `writer` becomes
  `reader`; a cross-scope publish is 403.
- `TestUnclampableEdgeDefaults`: a custom role (`channel`; `consumer` on a
  `sandbox-manager` provider), a stream and a splice are blocked, and no
  edge-policy value un-blocks them.
- `TestNetEdgeDefault` and `TestNonPrimaryNeverSharesHostNetwork` (a `dev`
  deployment of a host-sharing tile gets no egress and cannot bind a host port
  or reach the primary's host-bound listeners).
- `TestNonPrimaryBusPublishIsolated`.
- `TestNonPrimaryNeverJoinsProviderRoster`.
- `TestEdgeBlockFailsClosed`.
- `TestDeploymentHeaderOnlyFromXbind`.

### T5 — Secrets: vault separation, vault copy, alwaysOn

**Vector.** The vault is keyed by the tile path
(`internal/broker/vault.go:45-47`) and gated by `p.Component == comp`
(`:171-174`). Values are readable by the instance principal
(`:188-194`). A non-primary backend would read the primary's secrets. A
bridge that holds the platform token, running alwaysOn in two deployments,
opens two connections ([research/inbound-edges.md](research/inbound-edges.md) §3.21).

**Impact.** The primary's external accounts driven by untested code; duplicate
or conflicting connections that the platform may revoke (A1, A4, A7).

**Mitigation.**
1. **One vault per deployment (P14).** `main` keeps
   `data/vault/<CompKey>.json`; every other deployment has its own file in a
   namespace no existing key can produce ([08-data.md](08-data.md)), which
   records and verifies the tile path (C10). The principal's bound deployment
   selects the file, and a non-primary principal never resolves the
   primary's.
2. A new deployment starts with the primary's key names only.
3. **Vault copy** is a manager act in a human session, key by key. The
   confirmation says: "these values become readable by any code a
   terminal-level user deploys to `<name>`". Per-key provenance is kept
   ([08-data.md](08-data.md) §10).
4. **Under protection, no session token reaches the primary's vault.** Today
   the vault API admits the tile's instance and terminal tokens and
   admin-capable principals, and refuses frame tokens
   (`internal/broker/vault.go:166-169`). A tile principal addresses only its
   bound deployment's vault (C1), and no session targets a protected primary
   (P24). A protected primary's vault is therefore written only by its own
   backend, which runs reviewed code, and by admins. A session path for tile
   managers who are not admins is open (NP-06-9).
5. **alwaysOn** is off for non-primary deployments and set by managers.
   Turning it on for a deployment whose vault holds copied values warns about
   duplicate connections and lists those keys.
6. D30 applies per deployment: terminals and admins list and set; only that
   deployment's backend reads values.

**Tests.** `TestVaultPerDeployment` (neither side reads the other's
values), `TestVaultCopyManagerOnly`,
`TestProtectedPrimaryVaultWritesManagerOnly`,
`TestAlwaysOnNonPrimaryManagerOnly`.

### T6 — Side effects: cron, bus pushes, notify, ingress hosts, interface instances

**Vector.** Today every one of these self-scoped stores is keyed by the
caller's path, so a second deployment overwrites or deletes the primary's
rows, and deliveries still go to the primary:
- cron jobs, keyed (component, name) (`internal/broker/cron.go:108`), with PUT
  and DELETE forced to the caller (`:227-270`);
- bus subscriptions, keyed the same way (`internal/broker/bussubs.go:158`,
  `:391-449`);
- interface instances: a PUT replaces the whole map in the root `xbin.json`
  and restarts every bound consumer (`internal/broker/netfn.go:1102-1129`);
- ingress hosts: a PUT replaces the tile's hosts
  (`internal/broker/ingressfn.go:564-574`);
- notify: charged to the tile's budget, with a link to the primary
  (`internal/push/api.go:287-300`, `:356-368`);
- status: cleared by any `build-start` for the path
  (`internal/obs/status.go:129-143`).

**Impact.** The primary's schedules and subscriptions rewritten; consumers
rewired; public hostnames removed; real pushes sent to users; the primary's
status cleared or painted by a non-primary build; a non-primary deployment of
a terminator tile (traefik) requesting certificates for the primary's hosts
(A1, A7).

**Mitigation.**
1. Non-primary registrations are stored in per-deployment files beside the
   record (05-model.md §3), never as rows in today's stores or in the root
   `xbin.json`. `main`'s rows stay byte-for-byte where they are (P13).
2. A non-primary deployment's cron jobs and bus subscriptions fire only for
   it: the dispatcher passes the deployment in the request context (T3a), so
   a tick or delivery reaches its backend and its data, never the primary.
   A subscription on another scope's bus reads under the edge policy, re-
   checked at every delivery (T4). What its jobs send is real (it has the
   tile's network, read-clamped edges): the accident boundary (P20) keeps it
   off the primary's data, and a manager silences a noisy deployment with
   its deliveries switch (off; on by default since P13's revision of
   2026-09-28).
3. Interface instances and ingress hosts registered by a non-primary
   deployment are dormant: never active in routing, deliveries on or off. A
   non-primary terminator reading `/ingress-routes` gets an empty list, so it
   never requests certificates for the primary's hosts.
4. Notify from a non-primary deployment is never pushed and never charges the
   tile's budget. It rides the `deployments` event to C9's audience as
   "would notify".
5. A non-primary deployment's status, notices and build activity ride only
   the `deployments` event, never `status`, `build-*` or `reload`
   (05-model.md §8). A non-primary build therefore emits no `build-start`, and
   cannot clear or paint the primary's status.
6. `forget(path)` (`internal/broker/cron.go:148`, `bussubs.go:220`) and
   deployment removal also delete the per-deployment files.
7. **Run now** on a non-primary deployment's job is a terminal-level act
   (05-model.md §10) and delivers once, whatever its deliveries switch says.

**Tests.** `TestNonPrimaryCronDormant`, `TestNonPrimaryBusSubDormant`,
`TestDeliveriesSwitchManagerOnly`, `TestNonPrimaryIfaceInstancesNeverRouted`,
`TestNonPrimaryIngressHostsNeverRouted`, `TestNonPrimaryNotifyNeverPushed`,
`TestNonPrimaryStatusNamespaced`, `TestReassignMovesActiveRegistrations`.

### T7 — Information disclosure through events and reads

**Vector.** `handleEventsWS` sends every non-bus event to every subscriber
(`internal/server/server.go:603-605`). That includes tiles and users who
cannot read the tile; only `pr`, `term` and `session` are filtered
(`:597-602`). `build-error` carries compiler output in `Text`
(`internal/events/events.go:14`). A qualified `component` on an old event
type would reach old clients that don't know it: ancestor frames and the
shipped app claim `<ancestor>/…` components, the old shell stores and toasts
any `status`, and several consumers refetch on any `reload`. The new
`deployments` event, and the deployments read API, would announce names,
checkpoints, deployers and edge policy. The primary's frame token is minted
for every reader and is extractable from the page, so a rule that trusts "the
tile's own principals" hands readers every non-primary fact. This is
[research/side-findings.md](research/side-findings.md) #2, multiplied by
deployments.

**Impact.** Tile names, deployment names, fragments of tile code and
operators' names disclosed to people who can only read the tile, or cannot
read it at all (A6, A8). Old clients reloading or toasting on non-primary
activity.

**Mitigation.**
1. **Non-primary activity never rides today's event types** (05-model.md §8,
   P13). `reload`, `build-*`, `status` and notify carry only the primary, with
   the bare `component`. No qualified `component` string appears on an old
   event type, ever: not for deploys, not for reassignment, not for
   lifecycle. Non-primary activity, and every deploy's phases and failures,
   ride only the new `deployments` type, which names the deployment. A
   successful swap on the primary emits one `reload` for the bare component,
   only when the primary's code changed.
2. **Audience filter.** `handleEventsWS` filters every `deployments` event,
   like `pr` (`internal/server/server.go:597-599`), with C9's audience: an
   event about the primary alone goes to the tile's readers; an event that
   names a non-primary deployment goes only to C9's narrower set. Other
   tiles' principals never receive either. Old clients are unaffected,
   because the type is new.
3. **What readers see.** For a reader, including the primary's frame token,
   `GET /api/xbin/deployments` returns only primary-scoped facts: live reload
   state as it concerns the primary, the primary's checkpoint and deploy
   state, and protection. It names no non-primary deployment and carries no
   count that reveals one. `/components` adds only the primary summary.
   [11-contract.md](11-contract.md) owns the exact shape. Readers get this
   filtered view, not a 403.
4. Rows for non-primary deployments in `/tile-status`, `/backends` and
   `/runtime`, the deploy log (which is how checkpoints are listed) and diffs
   follow C9. Log
   text follows C9's log rule. The D112 `/sandboxes` view stays admin-only
   (`internal/boot/sandboxes.go:34-39`).
5. The existing broadcast of the primary's `build-*`, `reload` and `status`
   should be narrowed to readers of the tile in a separate change. This
   design must not widen it.

**Tests.** `TestNonPrimaryUsesNewEventTypes`, `TestDeploymentEventsFiltered`,
`TestPrimaryFrameTokenGetsNoNonPrimaryFacts`,
`TestReaderSeesPrimaryOnly`, `TestNonPrimaryRowsNeedWrite`,
`TestLogsPerDeployment` (a non-primary instance token cannot read the
primary's log, nor the primary's frame token `dev`'s).

### T8 — Seeding and PII

**Vector.**
- A seed puts the primary's data where every terminal-level user and agent
  can deploy code, and where every `write` user can open the UI.
- Host-side copying through a resource mount, which the primary's backend
  writes, follows symlinks it planted. Backups already have that race: a type
  check, then `os.Open` (`internal/backup/backup.go:127-130`).
- The backup tar path is lossy ([research/data-plane.md](research/data-plane.md) §4).
- In a shared scope, the namespace (scope, name) holds data written by
  sibling tiles.

**Impact.** PII disclosure. xbind secrets pulled into a namespace that
non-primary code reads (A4). Seeding in the wrong direction corrupts the
primary.

**Mitigation.**
1. Seed is a manager act in a human session, on one deployment. Seeding is
   optional; the default is empty data. The warning names who can reach the
   data afterwards: writers through the UI; terminal-level users and their
   agents through code.
2. Seeding reads from the primary's namespace, mounted read-only, and writes
   to the target's. It refuses a target whose physical key equals the
   primary's: it compares keys, not names.
3. The copying runs in confine (the primary's namespace read-only, the
   target's read-write, no network), or duplicates xbind-only ciphertext
   under the resenc precedent
   (`internal/resenc/resenc.go:283`). xbind never reads plaintext mounts on
   the host, and never seeds through the backup tar.
4. kv is re-encoded in process. No plaintext touches disk.
5. **Shared namespaces (P28).** In a shared scope, seed, reset, restore and
   remove need the act's level on every tile of the scope that has that
   deployment name, and stop all of them; a refusal names the blocking tile.
   Joining a namespace whose state is seeded or restored is a manager act on
   the joining tile, and the answer says whose data it joins
   ([08-data.md](08-data.md) §6).
6. The namespace's own metadata keeps the data's provenance
   ([08-data.md](08-data.md)), and the UI shows its age. Reset and remove
   unmount and `RemoveAll` the namespace, which does not follow symlinks.
7. Non-primary data is in backups only when opted in (05-model.md §11).
   Per-deployment backup, restore and seed use new endpoints under
   `/api/xbin/deployments/…` ([11-contract.md](11-contract.md)); `POST /restore`
   is unchanged. Each is a manager act in a human session.

**Tests.** `TestSeedManagerOnly`, `TestSeedCopyConfinedNoFollow` (a symlink to
`.xbin/token` planted in the primary's filesystem resource is copied as a
symlink and never dereferenced), `TestSeedNeverWritesPrimary`,
`TestSharedScopeSeedNeedsEveryTile`, `TestJoinSeededNamespaceManagerOnly`.

### T9 — Enforcement of the authority matrix

**Vector.**
- `CanTerminalTile` refuses every element principal
  (`internal/auth/auth.go:104-115`), so building the model's §10 table from
  today's gates alone would refuse agents.
- A naive `p.Component == tile` check would admit the tile's frame and
  instance principals, and non-primary code could promote itself.
- `mayManageTile` passes elements holding `xbin:users`, or `xbin` admin,
  through `canManageUsers` (`internal/broker/orgsapi.go:428-430`,
  `internal/broker/usersapi.go:38-50`), exactly as it does for lifecycle
  today. Reused bare, it would let a governance tile's code, or anything that
  reaches its principal, act as a manager of every tile's deployments.
- Checks done only at request time race protection being turned on.

**Impact.** Any operation reachable by the wrong principal.

**Mitigation.** One `authorize(p, tile, op, target)` in `internal/deployments`,
called by every operation handler (05-model.md §10):

| Operation | Allowed | Refused |
|---|---|---|
| Read the record, deploy log and diffs | humans ≥ `write`; admins; the tile's terminal and agent tokens (user ≥ `write`); a non-primary deployment's own principals, for facts about that deployment | readers and the primary's frame tokens get the primary-scoped view (T7.3); other tiles get the primary-scoped view at most |
| Open or call a non-primary deployment | humans ≥ `write` (checked on every request); terminal and agent tokens targeting it; its own principals | other tiles, ingress, readers, other deployments' principals |
| Pause live reload, resume live reload, reload now; attach live reload to a non-primary deployment | humans with terminal level; the tile's terminal and agent tokens whose user holds terminal level | the tile's instance, frame, cron and bus principals; other tiles' principals; view-as; `noTerminal` accounts; resume onto a protected primary; reload now onto a protected primary (next rows) |
| Add, remove, deploy to, promote to, roll back a non-primary deployment; reset its data; open a session targeting it; run now | as above; joining a seeded or restored namespace, and acts on a shared namespace, as in T8.5 | as above |
| Deploy, promote, roll back, reload now onto the primary | unprotected: as above. Protected: tile managers in a human session, naming the reviewed checkpoint (`checkpoint` on deploy and roll back, `expect` on promote and reload now), else 400 | protected: every terminal and agent token, a manager's own included; every element principal |
| Seed, vault copy, deliveries, alwaysOn, edge policy, per-deployment limits, reassign (M2, with `expect`), protect, unprotect, purge, non-primary backup and restore, reset `main`'s data while it isn't primary | a human session: `p.Component == ""` and (`IsAdmin` or `mayManageTile`) (`internal/auth/auth.go:92`, `internal/broker/orgsapi.go:427`) | terminal and agent tokens (a manager's own included); every element principal, whatever `xbin` or `xbin:users` grants its tile holds |

1. View-as sessions are refused twice: by the middleware
   (`internal/server/server.go:229-232`) and by `authorize`.
2. Terminal and agent tokens pass only when the driving user holds terminal
   level. Their `Access` is rebuilt on every request
   (`internal/auth/auth.go:498-507`), so a downgrade or D88's `noTerminal`
   applies at once.
3. Principals of other tiles are refused every deployments operation.
   `CanTerminalTile` already refuses element principals, and the manager gate
   requires `p.Component == ""`. Admins act through their own session.
4. Each operation carries the record `seq` it was authorized against. The
   commit is a compare-and-set, and authorization is re-checked if the record
   changed, for example when protection was turned on mid-deploy.
5. Every operation writes a deploy-log or record entry naming the principal:
   user, and via terminal token or session.
6. Every refusal is a 403 whose message names who can act; `bx` reports it as
   exit 3. A loud act that asks for confirmation, run by `bx` with no terminal
   and no `--yes`, exits 4 and changes nothing (11-contract.md §9.1).
   Confirmation guards against mistakes; it is not an authority check.

**Tests.** `TestDeployAuthzMatrix` (a table over principal class ×
operation × protection, on `testBroker` with a users store, in the style of
`orgFixture`/`principalFor`; rows include an `xbin`-admin tile and an
`xbin:users` tile, refused as managers), `TestViewAsRefusedEveryOp`,
`TestNoTerminalCannotOperate`, `TestOwnRuntimePrincipalsCannotOperate`,
`TestAuthorizationRecheckedAtCommit`.

### T10 — Resource exhaustion

**Vector.**
- Many deployments per tile, each with backends, builds, artifacts, trees and
  data.
- Reload now in a loop, or diffs of the work tree in a loop, each a capture.
- No global build limiter exists ([research/side-findings.md](research/side-findings.md) #12).
- `diskmon` counts only `data/resources` and `data/resources-enc` per scope
  (`internal/broker/broker.go:185-198`).
- `Reserve` checks only the global VM count and budget, not a per-tile cap
  (`internal/vm/policy.go:132-168`).
- The VM leaf is sized for two guests (`internal/runner/vm.go:103-111`).
- Cgroups are best-effort and silently absent without delegation
  (`internal/cgroup/cgroup_linux.go:3-7`).
- Non-primary code declares its own resources (P22), and each file-backed one
  is a gocryptfs mount (`internal/resenc/resenc.go:283`).
- D113's per-tile caps are shared across deployments.

**Impact.** The primary starved, OOM-killed or refused a VM by its own
non-primary deployments; the workspace's disk filled; every tile's promote
confirmations stalled behind one tile's diffs (A7).

**Mitigation.**
1. Per-tile and per-workspace caps on non-primary deployments, enforced by the
   API and independent of cgroups.
2. The checkpoint caps of T2. A rate limit on checkpoint creation per tile.
   The deploy queue is a FIFO of at most 8 per deployment, and a request
   whose checkpoint equals the tail is merged into it (07-runtime.md §8.3).
3. **Diffs and dry runs.** A diff whose `to` side is the work tree captures,
   so it needs terminal level; every other diff is a ≥ `write` read (C9).
   Frame and instance principals are refused ([11-contract.md](11-contract.md)
   §1.11). At most one diff runs and one
   waits per tile, and two run across xbind; a further request answers 429 at
   once (NP-06-20). Dry runs and diffs never create a checkpoint store on a
   zero-state tile (05-model.md §5): dry runs of pause live reload and add
   deployment compute the impact without capturing, and a work-tree diff on a
   tile without a record answers 409 and captures nothing (11-contract.md
   §1.11). The store exists only after a committed opt-in.
4. One build limiter shared by all tiles, in which a primary's build goes
   before non-primary builds.
5. **Quotas.** Each non-`main` data namespace is its own quota bucket at the
   existing per-scope limit, and is write-blocked first on low disk
   ([08-data.md](08-data.md) §12, P25). Pooling it with the primary's would
   let a non-primary seed or bug push the primary over quota. Per-deployment
   limits default to the tile's, are set by tile managers, and never exceed
   the tile's ceilings (P22). Checkpoint stores, view repositories, artifacts
   and materialized trees count against a per-tile quota
   ([07-runtime.md](07-runtime.md) sets the numbers). Both raise diskmon
   alerts.
6. **Declared resources.** The number of resources a non-`main` namespace's
   code may declare is capped (proposed: 64, NP-06-21). A deploy whose code
   declares more is refused before anything is provisioned.
7. VM books are charged to the tile (05-model.md §12) under a per-tile cap. A
   non-primary VM reservation leaves the primary's guest size free and never
   preempts a primary start; a start that would take that headroom is refused
   with `sbx.Refuse` (P25).
8. A non-primary runaway must hit its own limits first. Each deployment has
   its own cgroup (05-model.md §12): its backend leaf carries its own memory
   and pids caps; no memory cap is shared by the primary and a non-primary
   deployment above the leaves; the primary's CPU weight is above non-primary
   weights (P25). The layout of [07-runtime.md](07-runtime.md) §10.3 meets
   this. `memory.low` on the primary is not in v1 (R10).
9. GC never removes a tree or artifact that a running generation binds. It
   asks the runner which roots and artifacts its running generations use; it
   does not rely on a registry field, because `main`'s rows stay
   byte-identical (T18).

**Tests.** `TestDeploymentCountCaps`, `TestCheckpointRateLimit`,
`TestDeployQueueCoalesces`, `TestDiffCaptureNeedsTerminalLevel`,
`TestDiffQueueBounded`, `TestDryRunsCreateNoStore`,
`TestDeclaredResourceCap`, `TestNonPrimaryVMCannotStarvePrimary`,
`TestNonPrimaryCgroupSubtree`, `TestDiskQuotaCountsDeploymentData`,
`TestGCKeepsTreesOfRunningGenerations`.

### T11 — Tampering with or downgrading deployment state

**Vector.**
- Hand edits to the record.
- Older binaries that ignore `data/deployments/`.
- A restore that trusts the archive. Placement comes from the manifest's
  component, not the requested tile (`internal/broker/backup.go:219-226`).
  Cron jobs are restored without checking their component
  (`:283-290`), while bus subscriptions are checked (`:296`).
- CompKey collisions (`internal/util/util.go:137-144`).
- A tile re-created at a path that had a record.

**Impact.** Protection or pinning silently lost; another tile's code history
or switches applied; archived switches such as deliveries or `read` edges
re-enabled (A2, A6).

**Mitigation.**
1. The record and the store are xbind-owned under `data/`. Terminals mask
   them (`internal/term/binds.go:51`), and so do confined builds
   (`internal/runner/build.go:55`). They are never kept in the root
   `xbin.json` or the work tree (P15).
2. Writes are atomic and durable through `fsutil.WriteFileAtomic`
   (`internal/fsutil/fsutil.go:19`). The previous valid record is kept.
3. **Load-time validation:** schema, the invariants of 05-model.md §4, the
   name grammar, the full tile path, owner ref and creation stamp (C10, P29),
   and that every named checkpoint exists. An invalid record holds the tile's
   backends with an admin alert (C7). A record whose owner ref no longer
   matches is inert until an admin adopts or clears it. Every creation path
   resets the path's deployment state first. Deleting the record is a
   deliberate return to the zero state.
4. New stores use `<TileKey>` and record the path they belong to (05-model.md
   §3, P29).
5. **Restore:**
   - `POST /restore` keeps its strictly-decoded body. Per-deployment backup
     and restore use new endpoints under `/api/xbin/deployments/…`
     ([11-contract.md](11-contract.md));
   - refuses an archive whose manifest names another tile before it writes
     anything;
   - rebuilds the store from its objects in confine, and never restores its
     `config`, `hooks/`, `info/` or `objects/info/alternates`; the view
     repository is derived and rebuilt, never restored;
   - validates the record like a load;
   - re-validates every registration's component and deployment.
6. **Downgrade:** an older binary runs the primary from the work tree, so
   pinning and protection lapse for as long as it runs.
   [12-compat.md](12-compat.md) must document this, and the upgrade notes must
   say it (§5.1 R6). Non-primary registrations and data stay untouched,
   because they live in files older binaries never read. `bx` against an xbind
   without the deployments API exits 6 and never falls back to a route that
   would act on the primary (11-contract.md §9.1).

**Tests.** `TestRecordValidationFailsClosed`,
`TestRecordTilePathMismatchRefused`, `TestRestoreRefusesForeignManifest`,
`TestRestoreRebuildsStoreConfig`, `TestRestoreRevalidatesRegistrations`,
`TestOlderBinaryIgnoresDeploymentFiles` (the 12-compat.md fixture).

### T12 — Non-isolated mode

**Vector.** Without `--isolate`:
- confine runs tools directly (`internal/confine/confine.go:140-143`);
- backends run as the xbind user (`internal/runner/runner.go:453-469`), so a
  non-primary backend could read the primary's process env (`XBIN_TOKEN`)
  and its data files;
- nothing can show a checkpoint at the canonical path
  ([research/serving-fabric.md](research/serving-fabric.md) §A.9).

`--no-auth` turns every credential-less caller into the owner
(`internal/auth/auth.go:566-582`).

**Mitigation.**
1. Non-isolated mode is unsupported for pinned or non-primary backends
   (P18). Operations that would pin a backend or add a non-primary backend are
   refused without isolation, at the API and again at every start in the
   runner.
2. Static-only tiles may pause live reload and have deployments: the static
   plane serves the checkpoint.
3. A workspace restarted without isolation holds its existing pinned or
   non-primary backends with a reason (C7). It never runs the work tree in
   their place. Their frontends keep serving checkpoints.
4. Under `--no-auth`, protection and the authority matrix enforce nothing. The
   API and the UI must say "not enforced: authentication is off".

**Tests.** `TestNonIsolatedRefusesBackendDeployments`,
`TestIsolationLossHoldsPinnedBackends`,
`TestStaticTilePauseLiveReloadWithoutIsolation`,
`TestNoAuthProtectionMarkedUnenforced`.

### T13 — Retired

cgi no longer exists (removed from xbin by its own change). The ID is kept
so later IDs stay stable.

### T14 — Chrome and xbin-capable tiles

**Vector.**
- A non-primary deployment of a governance tile would act on real users and
  grants: `IsAdmin` passes elements granted `xbin` admin
  (`internal/broker/broker.go:466-475`), and `canManageUsers` passes
  `xbin:users` (`internal/broker/usersapi.go:38-50`).
- Grants change after a deployment exists.
- The `chrome` flag is read from the work tree. The code comment
  (`internal/registry/registry.go:59-65`) and [/docs/auth.md](/docs/auth.md)
  call it host-set, yet every terminal-level user can edit the tile's
  `xbin.json` (`internal/term/binds.go:57-58`, `:136-137`). The flag turns
  off the frame sandbox (`internal/server/static.go:57-63`, `:317-325`) and
  reaches bx-frame through `/components` (`internal/server/api.go:178`).

**Impact.** Governance acts from untested code. Session riding as anyone who
opens the tile, admins included (A5): this is a defect today (§5.2 S1), and a
bypass of P19 and P21.

**Mitigation.**
1. Adding a non-primary deployment is refused while the tile is chrome or
   holds any `xbin` or `xbin:*` grant (P19). Pausing live reload stays
   allowed.
2. Approving an `xbin` or `xbin:*` grant for a tile that has non-primary
   deployments is refused with a reason (P19).
3. Principal clamp: a non-primary principal never satisfies `IsAdmin`,
   `canManageUsers`, the `xbin:writer` create gate or any other `xbin*` role,
   whatever the grant table says.
4. `chrome` follows the inbound surface, so a pinned primary's comes from its
   checkpoint, and a non-primary document is never chrome, whatever any
   manifest says (05-model.md §6). A save while live reload is paused cannot
   make the primary chrome.
5. For a primary that follows the work tree, the defect of S1 remains:
   `chrome` must become an xbind-owned attribute that admins set outside the
   work tree (NP-06-5). Until then the P19 check treats the tile as chrome if
   the work tree or any of its deployments' code says so.

**Tests.** `TestP19RefusesChromeAndXbinTiles`,
`TestXbinGrantRefusedWithNonPrimary`, `TestNonPrimaryPrincipalNeverAdmin`,
`TestNonPrimaryDocumentAlwaysSandboxed`, `TestManifestFieldSplit` (a pinned
primary's `chrome` comes from its checkpoint; a non-primary document is never
chrome).

### T15 — The fetch remote, tracked branches and the deploy remote

**T15a — The read-only checkpoint fetch remote** (v1, flow H).

**Vector.** The `template` remote's server uses `http.ServeFile`
(`internal/broker/templaterepo.go:214-227`), which follows symlinks and serves
every file under the git directory. Served from the store, dumb HTTP would
also advertise every ref that `update-server-info` lists
(`refs/xbin/checkpoints/*`, and the deploy logs under `refs/xbin/log/*` with
their deployer and error trailers), and hand out a pack holding every
deployment's objects, gitignored files included.

**Mitigation.**
1. **A separate view repository.** The remote is served from
   `data/checkpoints/<TileKey>.view.git`, which xbind derives from the store
   (05-model.md §3). It holds only `refs/heads/deploy/<name>` for each pinned
   deployment, plus `HEAD` naming the primary's ref (dangling while the
   primary follows the work tree), and only the objects those refs reach. It
   is never the store: no `refs/xbin/*` ref is advertised, and no deploy-log
   commit, full checkpoint tree or other store object is fetchable.
2. **Git views only.** Each ref points at the checkpoint's git view: the
   checkpoint minus the paths the tile's own ignore rules exclude, evaluated
   in the confined capture run (L1) and kept in the store beside its
   checkpoint, never advertised. A branch made from it never commits
   `node_modules`, `.env` or build output into the tile's history.
3. **Refresh** is one confined run (L6): fetch the view commits of the pinned
   deployments from the store into the view repository, prune everything
   else, and run `git update-server-info`. It follows every change to a pinned
   deployment's checkpoint, to the set of pinned deployments, or to the
   primary. The view repository is removed with the record and rebuilt from
   the store when missing.
4. **Allow-list.** Only `HEAD`, `info/refs`, `objects/info/packs`, loose
   objects matching `objects/[0-9a-f]{2}/[0-9a-f]{38}`, and
   `objects/pack/pack-<hex>.{pack,idx}`. Never `config`, `hooks/`, other
   `info/` files, `objects/info/alternates` or `http-alternates`. Files are
   opened with `OpenBeneath` beneath the view repository, and any symlink is
   refused: none is legitimate in a git directory. The handler is GET-only;
   [11-contract.md](11-contract.md) §1.12 owns the route.
5. **Gate.** The tile's own terminal and agent session tokens, while their
   user holds at least `write`, and humans with at least `write` on the tile
   (their current level, checked per request; admins hold it everywhere).
   Readers, the primary's frame tokens, instance tokens and other tiles'
   `code:` grants are refused: dumb HTTP cannot filter objects per caller, and
   the view repository carries non-primary deployments' code (C9).
6. **Injection.** Sessions opened while the tile has a record get the
   `xbin-deploy` remote as extra `GIT_CONFIG_*` env pairs next to today's
   rewrite, which attaches the session's bearer to `XBIN_URL` only
   (`internal/term/term.go:787-793`). It is never written into the tile's
   `.git/config`: clone copies `.git`, and opting out or downgrading must leave
   nothing behind. Zero-state sessions keep today's env byte-for-byte.
7. Nothing is ever pushed through it; pushing is T15c.

**T15b — Tracked branches** (later rung).
1. The tile's `.git` is read only inside confine, bound read-only, with
   nothing else bound but the quarantine. xbind never reads a ref file on the
   host: a ref that is a
   symlink to `.xbin/token` would carry the token into an error message.
2. The workspace watcher ignores dot directories, so polling happens in
   confine. The commit is fetched into a quarantine as in T2. An alternates
   path in the tile's `.git` cannot resolve beyond the binds.
3. Branch names map to deployments through explicit configuration, and reach
   git only after `--end-of-options`.
4. A protected primary never follows a tracked branch (P21). Every
   terminal-level user can move a branch of the tile's repository.

**T15c — The deploy remote** (later rung).
1. Smart-HTTP `receive-pack --stateless-rpc` runs in a streaming confined run
   (05-model.md §12). Its only read-write bind is a per-push quarantine
   repository; the store is bound read-only as an alternate for thin packs.
   There is no network, and there are caps on input bytes, objects and time.
2. `receive.fsckObjects=true`; `receive.maxInputSize` is set. No hook runs
   (`core.hooksPath=/dev/null`). xbind decides the ref update after
   `receive-pack` returns.
3. Only the ref of an existing deployment, in the `deploy/<name>` namespace
   ([11-contract.md](11-contract.md) owns the spelling). No deletes, no tags,
   one ref per push, and a push rate limit per tile.
4. After admission (T2), one push is one deploy, attributed to the token's
   user.
5. Authentication is by bearer terminal token only. Cookies are refused, so a
   browser form cannot forge a push. The token's tile must be the store's
   tile, its user must hold terminal level, and a push to a protected
   primary's ref is refused (P21).

**Tests.** `TestViewRepoHoldsOnlyPinnedViews` (no `refs/xbin/*` in
`info/refs`; an object only the store holds is 404; a deployment that follows
the work tree has no ref), `TestGitViewExcludesIgnored`,
`TestFetchRemoteServesOnlyGitFiles`, `TestFetchRemoteRefusesSymlinks`,
`TestFetchRemoteReadGate` (a reader, the primary's frame token, an instance
token and a `code:`-granted tile get 403; a writer and the tile's terminal
token pass), `TestFetchRemoteInjection` (present only with a record;
`.git/config` untouched), `TestTrackedBranchReadsGitOnlyInConfine`,
`TestTrackedBranchRefusedOnProtectedPrimary`, `TestDeployRemoteHostilePack`,
`TestDeployRemoteRefNamespace`, `TestDeployRemoteBearerOnly`.

### T16 — Protected-primary bypass attempts

Each row is a path by which a non-manager, or an agent, could change what a
protected primary runs or how it behaves. Every row must be closed.

| Attempt | Rule | Enforcement |
|---|---|---|
| Deploy, promote, roll back or reload now onto the primary through a terminal or agent token, or `bx` | Refused with a 403 naming the protection; `bx` exits 3 (P21) | `authorize` (T9) |
| Resume, or attach live reload, onto the primary | Refused; protecting detaches live reload (05-model.md §5) | `authorize` |
| Self-admin calls on the primary through a terminal or agent token, or through a frame token it mints | Impossible: no session targets a protected primary (P24). It is never offered, a request naming it is refused, protecting restarts or ends the sessions that targeted it, and a token still bound to it is refused at decision time (C2). A session token mints frame tokens only for its target (T3b, T3d) | the session opener, `Broker.Policy`, `mayMintFrameToken` |
| A manager reviews one state and ships another: a promote from a deployment that follows the work tree, or a reload now, captures when it executes | Every operation onto a protected primary names what the manager reviewed: `checkpoint` on deploy and roll back; `expect` on promote, reload now and reassignment. A request without it is refused with 400. The commit is a compare-and-set on the record's `seq` with authority re-checked; a record or work tree that moved answers 409. The server never captures on its own for such an operation (05-model.md §5) | `internal/deployments` |
| Code PRs (D48) and builtin updates in `pr` mode (D49) | They land in the work tree, which reaches only the live reload target, never a protected primary (05-model.md §11) | none needed |
| Builtin updates in `replace` or `merge` mode (`internal/builtins/updates.go:504`, `:538`) | Write the work tree only; same as above | none needed |
| Editing the inbound surface in the work tree: `chrome`, `template` (the proxy 404s a template, `internal/proxy/proxy.go:135-139`), `exposes` (ingress reconciles on every watcher batch, `internal/boot/serve.go:168`), `expose.roles`, `provides` | The inbound surface follows the primary's code, its checkpoint while pinned, for every pinned primary, protected or not (05-model.md §6). `uses`, `interfaces` and `deps` stay the work tree's: they are authority requests, and new `uses` and `interfaces` entries file pending grants and bindings that need approval. A checkpoint's `deps/` links resolve from the checkpoint (T2.6) | registry and broker readers of those fields: `chrome` (`internal/server/static.go:57-63`, `internal/server/api.go:178`), `template` (proxy), `exposes` (ingress reconcile), `provides` and `expose.roles` (netfn, broker) |
| Editing deployment-level fields in the work tree (`runtime`, `entry`, `setup`, `scope.json`'s `resources`, …) | They come from each deployment's own code; a pinned primary's from its checkpoint (05-model.md §6, P22). The proxy and the runner take `runtime` from the target deployment's code, never from the work tree's registry entry | proxy, runner, the provisioner |
| Setting the primary's vault values | No session token reaches a protected primary's vault (T5.4) | `vaultAccess` |
| Poisoning a build product the primary will use: an env layer keyed by `setup` alone (`internal/runner/env.go:39-46`), a Go cache shared per tile (`internal/runner/build.go:43-45`), an artifact built by a non-manager's deploy | Build products for a protected primary come only from manager-initiated operations, in a separate namespace; a promote onto it rebuilds (05-model.md §9, P21; T17) | runner |
| Following a tracked branch, or pushing to the deploy remote | Refused (P21, T15) | feeds |
| Nested components inside the tile (their code is the parent's writers' to change, `internal/server/static.go:505-511`) | Protecting a primary lists the nested components whose code the parent's writers write and whose pages the parent serves, and warns unless each is protected too. Protecting them in the same act requires managing each child. There is no automatic cross-tile protection (NP-06-8) | `internal/deployments`, the protect dialog |
| Reassigning the primary to a deployment that non-managers filled | Manager act (M2) with a loud confirmation that shows that deployment's checkpoint and deploy log. The request carries `expect`, and the reassignment is a compare-and-set that pins exactly the reviewed checkpoint (05-model.md §5) | `internal/deployments` |
| Downgrading xbind | Out of reach: an admin act; documented (T11, R6) | 12-compat.md |

**Tests.** `TestProtectedPrimaryRefusesTerminalTokens`,
`TestProtectedPromoteNeedsReviewedCheckpoint` (400 without `checkpoint` or
`expect`; 409 when the record or work tree moved),
`TestManifestFieldSplit` (the inbound surface follows the primary's code),
`TestProxyRuntimeFromDeployment`
(a work-tree `runtime` edit leaves a pinned primary's runtime unchanged),
`TestProtectedBuildProductsSeparated`, `TestProtectionCoversNestedComponents`,
`TestReassignPinsReviewedCheckpoint`.

### T17 — The `setup` supply chain, per deployment

**Vector.**
- The `setup` script runs with internet egress (`internal/runner/env.go:74-86`)
  and sees the tile directory read-only (`:78`), but the layer is keyed only
  by a hash of the script and the rootfs (`:39-46`). The first deployment to
  build a hash decides the layer's content for every deployment of the tile.
  A script that reads a tile file, or fetches from the network, yields
  different layers from the same key.
- The layer is a lower that can replace any binary of the backend's rootfs.
- `gcEnvLayers` deletes every other hash of the tile (`:133-145`).
- Today the setup run binds the work tree, even for a deployment pinned to a
  checkpoint.

**Impact.** Code from a non-primary deployment ends up inside the primary's
runtime, bypassing P21. Layers deleted under running deployments (A2, A7).

**Mitigation.**
1. The setup run of a pinned deployment binds that deployment's checkpoint at
   the canonical path, never the work tree (05-model.md §12).
2. Layers used by a protected primary are built only during manager-initiated
   operations onto it (a promote onto it rebuilds), in a separate namespace,
   and are never shared with non-primary deployments (05-model.md §9, P21).
   Unprotected tiles keep sharing by hash: under parity their writers can
   deploy anything anyway.
3. GC keeps every layer that any deployment references (05-model.md §9).
4. The deploy log records the layer hash and build time. A reviewed checkpoint
   does not pin what `setup` downloads, and the confirmation says so.
5. VM backends still refuse `setup` (`internal/runner/vm.go:70-72`).

**Tests.** `TestSetupLayerSeesCheckpointNotWorkTree`,
`TestProtectedPrimaryLayerNotShared`, `TestEnvLayerGCKeepsReferenced`.

### T18 — The shared sandbox layer: binds, run dirs, registry rows, cgroup leaves

**Vector.**
- **Run dir.** The backend sandbox binds its run directory read-write
  (`internal/runner/runner.go:642`), and the socket lives there
  (`:414-419`). If deployments shared `RunDir/<CompKey>/`, non-primary code
  could replace the primary's `g<N>.sock` with its own listener. Then every
  inbound edge of the primary — humans, consumers, ingress, with their
  `X-XBin-*` headers — would be served by non-primary code.
- **Resource binds** are computed with `Src == Dst` from env paths
  (`internal/runner/binds.go:51`). Under the model the env paths stay the
  primary's (05-model.md §12), so a bug here binds the primary's data into a
  non-primary sandbox.
- **Binds inside the checkpoint.** Checkpoint content can put a symlink at a
  path where a later bind lands (a nested component's mount point, taken
  before that component existed and brought back by roll back, add from a
  checkpoint or restore). A mount that follows it places code elsewhere in the
  sandbox's view.
- **Registry IDs** are `backend:<CompKey>:g<gen>`
  (`internal/runner/sbx.go:63`), and `Add` overwrites on the same ID
  (`internal/sbx/sbx.go:131-140`), so a colliding ID hides a running
  generation from admins.
- **The leaf** is one per tile, and every exit removes it
  (`internal/runner/runner.go:613-615`).

**Impact.** Traffic hijack of the primary; the primary's data exposed to
non-primary code; loss of attribution (A1, A5, A8).

**Mitigation.**
1. Every deployment has its own run directory, within the 108-byte socket
   limit. A sandbox binds only its own. The proxy's transport and the relay's
   host forwards resolve sockets per (tile, deployment).
2. Resource binds take `Src` from the deployment's namespace and `Dst` from
   the env path. The launch spec asserts that no non-primary bind has a `Src`
   inside the primary's namespace, and the start fails if one does (C7). VM
   exports follow the bound view (`internal/vm/manager.go:339-371`), so the
   same assertion covers VM backends.
3. The checkpoint is bound read-only at the canonical path. confine's new
   bind destination (`DirFrom` in [13-surfaces.md](13-surfaces.md)) serves
   builds the same way, and direct mode refuses it (T12). Binds inside it
   follow two rules:
   - mount points under the canonical path are created without traversing
     symlinks: the sandbox init opens each with `openat2` and
     `RESOLVE_NO_SYMLINKS | RESOLVE_BENEATH`, and a destination that is a
     symlink fails the start (C7);
   - a nested component's own code is bound after the checkpoint bind, at its
     own path, from its own primary's code (05-model.md §6).
4. **Registry rows (D112).** `main`'s rows are byte-identical to today's
   (`backend:<CompKey>:g<gen>`, no `Deployment` field), even when the tile has
   a record. A non-`main` deployment's rows use
   `backend+<name>:<CompKey>:g<gen>` and set `Entry.Deployment`, so no two
   deployments' generations share an ID (05-model.md §12).
5. **Separate cgroups per deployment.** Zero-state and main-only tiles keep
   today's flat `comp-<CompKey>` leaf. A tile that runs a non-`main`
   deployment gets `tile-<CompKey>/d-<name>/{backend, sbx-*}`, and `main`
   moves at a generation boundary (05-model.md §12). A deployment's exit
   removes only its own leaf.

**Tests.** `TestRunDirPerDeployment` (a non-primary sandbox cannot see or
bind the primary's socket), `TestNonPrimaryResourceBindsNeverPrimary` (in
`internal/runner`, next to `resourcebinds_test.go`),
`TestVMBackendNamespaceBinds`, `TestNestedComponentBoundAfterCheckpoint`,
`TestRegistryRowsPerDeployment`, `TestEntryJSONZeroState`,
`TestLeafPerDeployment`.

### T19 — Tile-managed sandboxes per deployment (D113, once built)

**Vector.** D113 lets every instance token of the tile address one shared set
([../tile-sandboxes.md](../tile-sandboxes.md) §1). Those sandboxes mount the
tile's `filesystem` resources read-write, and human-attach tickets bind
(tile, sandbox, exec, user). A non-primary backend would exec into the
primary's sandboxes, and so reach the primary's data.

**Mitigation** (05-model.md §12):
1. Definitions and state are keyed by (tile, deployment), in per-deployment
   files beside the record, never as rows in `main`'s `data/sandboxes.json`.
2. A principal addresses only its deployment's set (C1).
3. Resource mounts resolve in the deployment's namespace.
   `{"source":true}` mounts the deployment's code.
4. Attach tickets bind the deployment. Egress `inherit` follows the
   deployment's effective net edge (T4.4).
5. `cap:sandboxes` stays the tile's. Per-tile caps keep headroom for the
   primary (T10, P25). Removing a deployment deletes its sandboxes.

**Tests.** `TestTileSandboxesPerDeployment`, `TestAttachTicketBindsDeployment`,
`TestTileSandboxMountsDeploymentNamespace`.

### T20 — Checkpoint content: disclosure and retention

**Vector.** A checkpoint captures everything under the tile, including
uncommitted and gitignored files such as `.env` (glossary). Checkpoints
persist until GC and are always in component backups (05-model.md §11). The
primary's checkpoint is served to readers through `/c/`; every pinned
deployment's git view is fetchable by writers (T15a).

**Impact.** A secret deleted from the work tree lives on in checkpoints and
archives (A6).

**Mitigation.**
1. Readers see no more than they see today: the work tree is already readable
   through `/c/` and terminal mounts, and readers get only the primary's code.
   The git view drops ignored paths, so `.env` never reaches the fetch remote.
2. A manager-only **purge** removes a checkpoint that no deployment runs from
   every deploy log, prunes its objects at once in confine, rebuilds the view
   repository, and records who purged what (NP-06-16). Archives made earlier
   are out of reach, and the UI says so.
3. The builder docs say plainly that checkpoints capture gitignored files, and
   that secrets belong in the vault.

**Tests.** `TestPurgeRemovesObjects`, `TestPurgeRefusesDeployedCheckpoint`,
`TestPurgeManagerOnly`.

## 4. The D78 compliance ledger

Every new tool run on tile data, on the checkpoint store or on the view
repository, and how it is confined. Each run goes through `confine.Run` or
`confine.GitCmd` unless it says otherwise. `Dir` is bound read-write unless
marked read-only; nothing else is bound. Row numbers are stable; later rows
were added without renumbering.

| # | Run | Trigger | Read-write | Read-only | Net | Limits | Test |
|---|---|---|---|---|---|---|---|
| L1 | capture: `add -A --force`, `write-tree`, the git view's tree (the tile's ignore rules applied), `ls-tree` for admission | pause live reload, reload now, deploy or promote from the work tree, add deployment | the store directory (its private index); new objects go only to the run's quarantine | work tree | none | time; T2 caps before admission | `TestCaptureIgnoresTileGitConfig` |
| L2 | admit: fsck, move the quarantined objects into the store, write the retention ref | after L1 passes | store | quarantine | none | time | `TestAdmissionRefusesBadTrees` |
| L3 | materialize: `read-tree` + `checkout-index -a` (private index); files `0444`/`0555`, directories `0755` | first use of a checkpoint | fresh temporary directory | store | none | time; caps | `TestMaterializeSymlinkEscape` |
| L4 | diff for review: `diff-tree -p --no-ext-diff --no-textconv` | promote or deploy confirmation, `bx` | — | store | none | 30 s, 16 MiB output (the D77 precedent); T10.3's queue | `TestCheckpointDiffNoExternalTools` |
| L5 | not a tool run: deployment-level manifest fields and `scope.json`'s `resources` are read through `OpenBeneath` on the materialized tree (T2.7) | every deploy and start | — | materialized tree | — | small read cap | `TestCheckpointManifestReadNoFollow` (both files) |
| L6 | view repository refresh: fetch the pinned deployments' view commits from the store, prune the rest, `update-server-info` | every change to a pinned deployment's checkpoint, to the pinned set or to the primary | view repository | store | none | time | `TestViewRepoHoldsOnlyPinnedViews` |
| L7 | GC and purge: `reflog expire`, `repack -ad`, `prune --expire=now`, holding the store lock that admissions take | GC, purge, deployment removal | store | — | none | time | `TestPurgeRemovesObjects` |
| L8 | Go build of a checkpoint: `buildConfined` with `DirFrom` | a deploy with no artifact for that checkpoint | that checkpoint's artifact dir; the cache namespace (protected or shared, T17) | checkpoint at the canonical path; workspace with `.xbin`, `data`, `homes` masked (`internal/runner/build.go:51-60`) | internet, as today | 20 min, 1 MiB (`:88`) | `TestConfinedBuildOfCheckpointAtCanonicalPath` |
| L9 | `setup` layer of a pinned deployment: a sandbox launch, not confine (`internal/runner/env.go:92`) | a deploy needing a layer | layer upper and work (per trust namespace) | checkpoint at the canonical path | internet relay, as today | as today | `TestSetupLayerSeesCheckpointNotWorkTree` |
| L10 | seed, file resources: `cp -a` or `rsync -aHAX` | manager seed | the target's mount | the primary's mount | none | time | `TestSeedCopyConfinedNoFollow` |
| L11 | seed, sqlite online backup (python3's sqlite3), if [08-data.md](08-data.md) chooses it | manager seed | the target's mount | the primary's mount | none | time | `TestSeedSqliteConfined` |
| L12 | gocryptfs init, mount, unmount of namespaces: host exec under `exec-ok` (`internal/resenc/resenc.go:283`, `:360`) | provision, seed, reset, remove | — (xbind-only cipher directories) | — | — | — | existing resenc tests |
| L13 | restore: `init --bare`, fetch objects from the unpacked archive, fsck | restore | new store | unpacked archive | none | time | `TestRestoreRebuildsStoreConfig` |
| L14 | tracked branch: `rev-parse`, `fetch <tile .git> <commit>` into a quarantine (later) | ref poll | quarantine | tile `.git` only | none | time | `TestTrackedBranchReadsGitOnlyInConfine` |
| L15 | deploy remote: streaming `receive-pack --stateless-rpc`, then L2 (later) | push | per-push quarantine repository | store (alternate) | none | input bytes, objects, time | `TestDeployRemoteHostilePack` |
| L16 | drift count: L1's step against a scratch copy of the index and a scratch quarantine, then `diff-index --cached --name-status <pinned tree>`; both scratch copies discarded | a watcher batch on a tile whose live reload is paused; a client asks (debounced per tile) | scratch index and quarantine | work tree; store objects | none | time | `TestDriftCountChangesNothingDurable` |
| L17 | deploy log: write an entry (`commit-tree`, `update-ref refs/xbin/log/<name>`); read entries (`git log` printing trailers) | after each finished attempt; deployments reads | store (write); — (read) | — (write); store (read) | none | time; output cap on reads | `TestDeployLogConfined` |
| L18 | background `repack -d -q` after a capture admits many loose objects | admission | store | — | none | time | `TestNoDirectExec` (a confined run) |
| L19 | embedded repositories: L1 again with `rm --cached` for each gitlink and a `confine.Mask` over each `<dir>/.git`; without isolation the checkpoint fails instead | L1 printed a gitlink | as L1 | work tree, with the masks | none | time | `TestCaptureIgnoresTileGitConfig` |
| L20 | none: a work-tree diff on a tile without a record runs no tool and answers 409 ([11-contract.md](11-contract.md) §1.11) | — | — | — | — | — | `TestDryRunsCreateNoStore` |

Host-side operations with no tool, and their rules:
- `rename` of a materialized tree into place;
- `RemoveAll` of trees, quarantines and namespaces;
- in-process kv re-encoding;
- record writes (temporary file plus rename);
- cgroupfs writes;
- `OpenBeneath` reads of a materialized tree (L5) and of the tile's
  `.git/HEAD` for the best-effort work-tree head trailer.

They follow C5. `TestNoDirectExec` (`internal/confine/guard_test.go:22-66`)
enforces the exec side, and `TestNoFollowingHostWalks` (C5) enforces the
no-follow side.

## 5. Residual risks

### 5.1 Accepted, with the reason

- **R1 — Non-primary code reads other tiles' primaries through `read` edges
  and can send what it reads out through the tile's relay network.** Accepted
  by P3 and the accident boundary. Managers set `block` per edge.
  [16-open-questions.md](16-open-questions.md) should decide whether
  protecting the primary offers "block every edge for non-primary
  deployments" in one step.
- **R2 — The read clamp on HTTP edges is only as strong as the provider's
  handling of `X-XBin-Role: reader`.** A provider that mutates state for
  readers is not protected. It is documented, and `X-XBin-Deployment` lets
  providers refuse such calls.
- **R3 — Seeded data and copied vault values are readable by every
  terminal-level user and their agents.** Accepted: both are explicit manager
  acts with warnings (T5, T8).
- **R4 — Under parity, an agent whose target is the primary acts on the
  primary's data.** The primary is P24's default target, so this is today's
  behaviour by default. Protection is the remedy, and `XBIN_DEPLOYMENT` plus
  the terminal bar show the target ([10-ux.md](10-ux.md)).
- **R5 — SHA-1 object ids.** A store in SHA-256 cannot serve flow H's fetch
  into SHA-1 tile repositories. Git's collision detection is relied on, and it
  matters only for pushed objects (T15c), whose author is already a
  terminal-level user.
- **R6 — A downgrade lapses pinning and protection** for as long as the older
  binary runs (T11). Accepted as an admin act. It is documented in
  [12-compat.md](12-compat.md) and in the upgrade notes.
- **R7 — Archive integrity.** Archives carry no MAC. An archiver tile's
  writers control what a restore reads. The restore checks in T11 limit what
  a crafted archive can do, but cannot prove an archive authentic.
- **R8 — What `setup` downloads is time-varying.** A reviewed checkpoint does
  not pin it (T17). The layer hash and build time are logged.
- **R9 — Go cache poisoning inside one tile.** It needs code execution in a
  build sandbox (VCS or toolchain paths). Closed only for protected primaries
  (T17).
- **R10 — Cgroups are best-effort** (`internal/cgroup/cgroup_linux.go:3-7`).
  Without delegation, memory protection of the primary is absent, and v1 sets
  no `memory.low` for it even with delegation. The count caps and the VM books
  still apply.
- **R11 — The legacy asset mode's credential-less subresources.** In the
  legacy mode, non-HTML subresources pass without a credential from an address
  that recently authenticated (`tileSubresourceAuthed`,
  `internal/server/static.go:286-311`), and a deployment URL's do too, as the
  primary's do today. Anyone who guesses a deployment name can fetch that
  deployment's scripts and styles, and so learn that the name exists, which
  T7.3 otherwise hides from readers. Documents, `/api/`, events, reads of
  deployment state and the fetch remote stay write-gated. The strict asset
  modes have no credential-less path and close it.

### 5.2 Defects found today that this design must not inherit

All four were verified by reading the code, not by exploit. Each needs its
own fix outside this design.

- **S1 — Any terminal-level user can make a tile chrome.** `chrome: true` is
  read from the tile's own `xbin.json`
  (`internal/registry/registry.go:59-65`). The tile directory is read-write in
  every terminal on it (`internal/term/binds.go:57-58`, `:136-137`), and the
  flag drops the CSP sandbox (`internal/server/static.go:57-63`). The tile's
  frames then run unsandboxed with the session of whoever opens them, admins
  included. [/docs/auth.md](/docs/auth.md) calls the flag a host-level decision. Fix
  direction: NP-06-5. This design keeps a pinned primary's `chrome` in its
  checkpoint and never makes a non-primary document chrome (T14).
- **S2 — Restore trusts the archive's manifest.** Tile files, scope data and
  the terminal layer land at `m.Component`/`m.Scope` from the archive
  (`internal/broker/backup.go:219-280`), without comparing them to the
  requested tile (`:424-441`). Cron jobs of any component, carrying any role,
  are re-added (`:283-290`). A crafted archive from an archiver tile can
  write another tile, or plant a cron job that calls any tile with an
  arbitrary `X-XBin-Role` (`internal/broker/broker.go:485-487`). Fix
  direction: apply T11.5's checks (refuse a foreign manifest before writing;
  re-validate every registration's component) to every restore, in their own
  change.
- **S3 — CompKey collisions.** `CompKey` is the first 24 characters of the
  path plus 32 bits of SHA-256 (`internal/util/util.go:137-144`). For a path
  longer than 24 characters, a second path with the same key takes about
  2^32 hashes. Colliding tiles share the vault file
  (`internal/broker/vault.go:45-47`), the build output, the log and the
  socket directory. `pathLeftovers` refuses such a creation only when the
  victim already has a vault file (`internal/broker/policy.go:250-252`). Fix
  direction: this design's new stores use `<TileKey>` (P29) and its files
  under existing stores verify the tile path (C10); existing stores need the
  same in a separate change.
- **S4 — Known issues this design depends on:** non-bus events are broadcast
  ([research/side-findings.md](research/side-findings.md) #2); backups race
  symlinks (#5, `internal/backup/backup.go:127-130`). T7 and T8 keep the
  design clear of each.

## Divergences from the model

All seven divergences this document raised are now resolved in the spine:

- `chrome` and the other inbound-surface fields read from the work tree:
  resolved by ruling 17 (05-model.md §6, the inbound surface follows the
  primary's code).
- The promote row's fresh checkpoint at execution time: resolved by ruling 14
  (05-model.md §5, reviewed operations name `checkpoint` or `expect`).
- The `setup` layer shared by hash across trust levels: resolved by
  05-model.md §9 and P21 (a protected primary's build products come only
  from manager operations).
- The fetch remote working "like the `template` remote": resolved by ruling 6
  (05-model.md §3, the view repository).
- Events namespaced but broadcast: resolved by rulings 1 and 2 (05-model.md
  §8, P13).
- Stores keyed by the collidable `CompKey`: resolved by ruling 21 and P29
  (05-model.md §3, `<TileKey>`).
- Backups carrying the record and store to a restore that trusts the archive:
  resolved by 05-model.md §11 and ruling 11.

No open divergence remains.

## New proposals

IDs are stable; resolved entries keep their number with a one-line pointer.

- **NP-06-1** — resolved by P26 (ruling 13).
- **NP-06-2** — resolved by 05-model.md §10 (ruling 13).
- **NP-06-3** — resolved by ruling 14 (05-model.md §5).
- **NP-06-4** — resolved by ruling 17 (05-model.md §6).
- **NP-06-5 — Chrome is xbind-owned** (open; an owner call). `chrome` becomes
  an admin-set attribute outside the work tree, fixed as its own urgent change
  before M1 with its own decision and changelog entry. This design already
  keeps a pinned primary's `chrome` in its checkpoint and never makes a
  non-primary document chrome. It is the fix for §5.2 S1.
- **NP-06-6** — resolved by 05-model.md §9 and P21.
- **NP-06-7** — resolved by P21.
- **NP-06-8 — Protection and nested components** (adopted in this document,
  T16). Protecting a primary lists the nested components whose code the
  parent's writers write and whose pages the parent serves, and warns unless
  each is protected too. Protecting them in the same act requires the actor to
  manage each child. There is no automatic cross-tile protection.
- **NP-06-9 — Vault writes under protection** (open; an owner call, narrowed).
  P24 already keeps every session token off a protected primary's vault
  (T5.4), so its vault is written only by its own backend and by admins. Open:
  whether tile managers who are not admins get a session path to set a
  protected primary's vault values. The default for v1 is no.
- **NP-06-10** — resolved by rulings 1 and 2 (05-model.md §8, P13).
- **NP-06-11** — resolved by P29 and ruling 21.
- **NP-06-12 — Restore validation** (adopted in this document, T11.5; the
  wire is ruling 11). A foreign manifest is refused before anything is
  written, the store is rebuilt from its objects only, the view repository is
  rebuilt, the record is validated like a load, and every registration's
  component and deployment is re-validated.
- **NP-06-13** — resolved by P19.
- **NP-06-14** — resolved by P25 (`memory.low` stays out of v1).
- **NP-06-15** — resolved by P28.
- **NP-06-16 — Checkpoint purge** (adopted in this document, M2). A manager
  act in a human session: remove a checkpoint that nothing runs from every
  deploy log, prune its objects at once, rebuild the view repository, and
  record the purge.
- **NP-06-17** — resolved by ruling 4 (P17, glossary).
- **NP-06-18** — resolved by ruling 6 (05-model.md §3).
- **NP-06-19** — resolved by 05-model.md §10.
- **NP-06-20 — Bounded, level-gated diffs** (open). A diff that captures the
  work tree needs terminal level; other diffs are ≥ `write` reads, and frame
  and instance principals diff only existing checkpoints of their own
  deployment and the primary. At most one diff runs and one waits per tile,
  and two run across xbind; anything more answers 429 at once. It stops
  readers and non-primary code from looping captures that hold the store
  mutex, spend the checkpoint rate limit and block every tile's promote
  confirmations (T10.3).
- **NP-06-21 — A cap on declared resources** (open). Under P22 a non-primary
  deployment's code declares its own resources, and each file-backed one is a
  gocryptfs mount. A non-`main` namespace may declare at most 64 resources; a
  deploy whose code declares more is refused before provisioning (T10.6).
