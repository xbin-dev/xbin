# 16 — Open questions and the decision register

> Status: live — the decision register (P1–P29 with their status and the documents that build them), the integrator's rulings, the review's adopted and rejected proposals, and the calls still open, each with the default the implementation builds until it is answered (part of [plans/dev-lifecycle](README.md))

This file is the set's ledger. Precedence, highest first:
1. the owner's answers (§1.2);
2. the integrator's rulings of 2026-09-27 (§2);
3. the spine: [01-glossary.md](01-glossary.md) and [05-model.md](05-model.md);
4. every other document of the set.

A work package that meets an open question here builds the question's
**Built meanwhile** default and cites the question's id (O1–O6, Q1–Q23,
L1–L3). It waits only when the question's **Blocks** line names it.

Shorthand:
- `05 §8` means [05-model.md](05-model.md) §8, and likewise for every
  document of the set by its number;
- `NP-07-3` is proposal 3 of 07's "New proposals" list, and `NN Div n` is
  divergence n of document NN's "Divergences from the model";
- `WP-n`, `PRE-n`, `R-n` and `K1`–`K18` are
  [14-implementation.md](14-implementation.md)'s work packages, gates,
  rulings and risks; `T1`–`T20` are [06-security.md](06-security.md)'s
  threats;
- "the review" is the cross-document review whose triage merged every
  document's proposals and open questions into the lists below.

## 1. The decision register

### 1.1 P1–P29

The invariants are [05-model.md](05-model.md) §13's; the statements below
are one-line summaries, and 05 §13 wins over them. **D at** names the
milestone exit whose records commit turns the P-number into a D-number (§7):
the first milestone that builds the invariant in full. That commit writes
the assigned D-number into the row's **D at** cell.

| # | Invariant | Status | Built by | D at |
|---|---|---|---|---|
| P1 | Deployments are pointers over an xbind-owned checkpoint store. The work tree is the default feed; a tracked branch and the deploy remote are later feeds on the same core. | ratified 2026-09-27 | 07 §2; 11 §10.3–§10.4; 06 T1–T2 | M1 |
| P2 | The term is *tile deployment*; "identity" stays reserved for principals. | ratified 2026-09-27 | 01; 10 §12; 12 §8; 15 (`TestDeploymentWording`) | M1 |
| P3 | A non-primary deployment's outbound calls go to the provider's primary, read-clamped, with a per-edge `block`; `match` later, on the same per-edge record. | ratified 2026-09-27 | 09 §5; 06 T4; 11 §4 | M2 |
| P4 | Parity: terminal-level users and their agents deploy to the primary as saving does today. Tile managers can protect the primary. | ratified 2026-09-27 | 10 §4; 11 §1.4–§1.6; 06 §2.2 | M2 |
| P5 | The zero state is the absence of a deployment record, byte-for-byte today (the flat cgroup leaf and today's registry rows included). Opting out removes the record; the store may remain, inert. No manifest key. | proposed | 12 §1; 07 §1.4; 11 §0.1; 15 §2.7 | M1 |
| P6 | Storage follows the deployment; `main` owns today's keys forever. | proposed | 08 §2–§3; 11 §10 | M2 |
| P7 | The primary is a role. Every inbound edge resolves through one resolver, which returns the primary for every v1 edge-policy value. Reassignment moves routing, not data. | owner-confirmed 2026-09-27 | 09 §1, §8; 07 §1.3, §8.8 | M2 |
| P8 | Live reload attaches to at most one deployment, which runs the work tree directly; the default path adds no per-save cost. | proposed | 07 §6, §13.1; 15 §8 | M1 |
| P9 | Pinned means pinned: every restart path runs the record's checkpoint (the attempted one after a failed move off the work tree); artifacts are kept per checkpoint; the inbound surface follows the primary's code. | proposed | 07 §5, §7, §8.2, §8.4; 15 | M1 |
| P10 | Promotion moves code only; data, vault, config and routing are late-bound to the target. | proposed | 07 §5.2, §8; 11 §1.6 | M2 |
| P11 | Authority stays per tile. A deployment is not a principal; non-primary deployments are narrowed by edge policy. | proposed | 09 §3; 06 T3 | M2 |
| P12 | Self-calls stay inside the caller's deployment; tile principals can't call another deployment of their own tile. | proposed | 09 §2.4, §4; 11 §7 | M2 |
| P13 | Non-primary registrations are dormant, notifications aren't pushed, and non-primary status and build activity travel only in the `deployments` event, delivered by access. | proposed | 09 §6–§7; 11 §3; 07 §8.5; 12 §3.1 | M2 |
| P14 | Non-primary data and vault start empty. Seeding is optional; seeding and vault copy are tile-manager acts. | proposed (carries the owner's seeding answer) | 08 §8, §10 | M2 |
| P15 | Deployment state is xbind-owned (`data/`), never in the root `xbin.json` or the work tree. | proposed | 11 §10; 12 §5 | M1 |
| P16 | Every git or tool run touching tile code happens in confine; the store is confine-only; materialized trees are served with containment and never followed out through symlinks. | proposed | 07 §2; 06 T1–T2, §4; 11 §10.3 | M1 |
| P17 | The `+` qualifier resolves only for tiles with a record and only after today's resolution fails; the bare URL is the primary. Signals are absent for the primary, credentials and stored state absent for `main`. | proposed | 11 §0.2, §2; 09 §3.3; 12 §7.1 | M2 |
| P18 | Pinned or non-primary backends need isolation; static tiles pause live reload everywhere; cgi no longer exists. | owner-confirmed 2026-09-27 | 07 §12; 06 T12 | M1 |
| P19 | Chrome and xbin-capable tiles may pause live reload but can't have non-primary deployments; approving an `xbin`/`xbin:*` grant is refused while non-primary deployments exist; a non-primary principal never satisfies a governance check. | owner-confirmed 2026-09-27 | 06 T14; 10 §3.13 | M2 |
| P20 | A non-primary deployment is an accident boundary, not a trust boundary. | proposed | 06 §2.1 | M2 |
| P21 | A protected primary is never the live reload target or a session's target. Every change to its code is a tile-manager act in a human session naming the reviewed checkpoint; its build products come only from manager operations; it follows no tracked branch and takes no deploy-remote push. | proposed | 06 §2.2, T16; 07 §3.4; 10 §3.9; 11 §1.7 | M2 |
| P22 | Resource declarations are deployment-level: each deployment provisions what its own code declares, in its own namespace, with limits defaulting to the tile's, set by tile managers, never above the tile's ceilings. | owner-confirmed 2026-09-27 | 08 §6.7; 07 §5.4; 11 §1.7 | M2 |
| P23 | Edges that cannot be read-clamped are blocked for non-primary deployments in v1, with no override. | owner-confirmed 2026-09-27 | 09 §5.6–§5.8, §5.12–§5.13 | M2 |
| P24 | The terminal's API dropdown selects the session's target, defaulting to the primary; a protected primary is not offered and the default falls to the live reload target, then to "API off"; the target is fixed for the session's life. | owner-confirmed 2026-09-27 | 10 §2.3; 09 §2.3; 11 §7.4 | M2 |
| P25 | Primary first: a non-primary deployment never takes the VM budget, CPU weight, disk quota or per-tile caps the primary needs. | proposed | 07 §10.2–§10.3; 08 §12 | M2 |
| P26 | xbind's API is default-deny for non-primary principals: every `/api/xbin/*` route is classified, unclassified routes refuse them, and a guard test keeps the list complete. | proposed | 09 §6; 08 §4.4; 13 §7 (`TestDeploymentRouteClasses`) | M2 |
| P27 | Edge-policy values fail closed: an unknown or invalid value reads as `block`, and any `block` among several authorizing edges refuses the call. | proposed | 09 §5.3–§5.4 | M2 |
| P28 | A (scope, name) namespace is shared by the scope's same-named deployments; seeding, resetting or restoring it needs authority on every claimant and stops all of them; it is deleted with its last claimant; no v1 reassignment splits a scope's primary data. | proposed | 08 §6 | M2 |
| P29 | Deployment state belongs to the tile it was created for: collision-free path keys, and a record that carries and verifies path, owner ref and creation stamp. A tile re-created at the path never inherits it. | proposed (Q1 refines the stamp check) | 11 §10.1; 06 T11; 12 §5.6; 08 §9.3 | M1 |

[13-surfaces.md](13-surfaces.md) §7 names the code that enforces each
invariant, and [15-test-plan.md](15-test-plan.md) §11.1 the tests that pin
it.

### 1.2 The owner's answers of 2026-09-27

Never contradicted by any document, work package or later ruling short of the
owner's own.

| Answer | Where it lives |
|---|---|
| P1–P4 are ratified. | 05 §13 |
| P7, P18, P19 and P22–P24 are confirmed. | 05 §13 |
| P22: resource declarations are per deployment, from each deployment's own code. | 05 §6; 07 §5.4; 08 §6.7 |
| P23: edges that can't be read-clamped are blocked in v1, with no override. | 05 §7; 09 §5 |
| P24: the target is chosen in the terminal's existing tile-API select, one entry per reachable deployment; default the primary; a protected primary never offered, the default then the live reload target, else "API off"; fixed for the session's life; no separate target picker. | 01 (Target deployment); 05 §7; 10 §2.3; 11 §7.4 |
| The net edge defaults to `inherit`. | 01 (Edge policy); 05 §7; 09 §5.8 |
| Humans with `write` or more may view non-primary deployments. | 05 §7, §10; 09 §2.2 |
| Primary reassignment is in M2: tile managers only, routing only, a loud confirmation. | 05 §5; 14 §1 (M2) |
| Separate cgroups per deployment. | 05 §12; 07 §10.3 |
| Seeding is optional; empty is the default. | 05 §5, §9; 08 §8 |
| Non-isolated mode is unsupported for pinned or non-primary backends. | P18; 05 §12; 06 T12 |
| cgi is removed from xbin by its own change; the set says only "cgi no longer exists". PRE-5 (that change lands first) stays. | 05 §12; 14 §0.1 |
| Deployments are not built on D113's tile-managed sandboxes; the shared sandbox mechanics change one layer up. | 05 §12; 07 §10; 14 §2.5 |

## 2. Rulings

The integrator's rulings settle every cross-document conflict the review
found. Each is settled design; a passage that disagrees is wrong and is
fixed to match.

| # | Settled design | Lives in |
|---|---|---|
| 1 | **Events, rule C2.** Non-primary activity never rides `reload`, `build-*`, `status` or notify, only the new `deployments` type, which carries `deployment`. No qualified `component` string on an old type, ever: not for deploys, reassignment or lifecycle. A successful swap emits one `reload` for the primary's bare component, only when the primary's code changed. A deploy's phases and failures ride `deployments`. | 05 §8; 11 §3.2–§3.3; 07 §8.5; 12 §3.1 |
| 2 | **Event audience.** Facts about the primary go to the tile's readers, as today. Anything naming a non-primary deployment (its name, builds, compiler output, status, would-notify lines, data ops) goes only to admins, humans with ≥ `write` (their current level), and that deployment's own principals plus the tile's terminal and agent sessions. The primary's frame token is not an own principal for these facts. Other tiles get none of it. | 05 §8; 11 §3.4; 09 §6.1 |
| 3 | **What readers see.** `GET /api/xbin/deployments` gives a reader primary-scoped facts only: live reload state, the primary's checkpoint and deploy state, protection; no non-primary name and no count that reveals one. `/components` adds only the primary summary. 11-contract owns the shape; the harness asserts the filtered view, not a 403. | 05 §8; 11 §1.3; 10 §4.1, §14 |
| 4 | **The `+` qualifier.** Two narrow refusals for every creator: no tile at `<P>+<N>` while `P` has deployment `N`; no deployment `N` on `P` while a component exists at `<P>+<N>`. Other new tile names containing `+` get a one-release warning, never a refusal. A qualified URL resolves only for tiles with a record, and only after today's resolution fails. | 01 (Deployment URL); 05 §7; 11 §2.1; 12 §7.1 |
| 5 | **D112 registry rows.** Non-main entry ID `backend+<name>:<CompKey>:g<gen>`; `Entry.Deployment` set only on non-main entries; `main`'s rows byte-identical to today's, even when the tile has a record. | 05 §12; 07 §10.1; 11 §8; 12 PO-11 |
| 6 | **The read-only checkpoint fetch remote** is served from a separate xbind-owned view repository per tile, holding only `refs/heads/deploy/<name>` for pinned deployments plus `HEAD` naming the primary's ref, refreshed by a confined `update-server-info`. Each ref is the checkpoint's git view (minus the tile's ignored paths). Never the store; no `refs/xbin/*` advertised. Gate: the tile's own terminal and agent sessions, and humans with ≥ `write` (current level). An allow-list of dumb-HTTP files opened beneath the view repository, refusing symlinks. Injected per session through `GIT_CONFIG_*` only while the tile has a record; never written into `.git/config`. | 05 §3; 07 §2.7; 11 §1.12, §10.3; 06 T15 |
| 7 | **Net edge `inherit`** inherits the tile's relay policy, never host networking or a provider splice. A tile whose `net` resolves to host sharing gives its non-primary deployments no egress (`block`, P23), with the reason shown. `gpu:*` defaults to `block`; other capability grants to `inherit`. | 05 §7; 09 §5.8–§5.9; 01 (Edge policy) |
| 8 | **Checkpoint fidelity and host access.** Materialized trees: directories 0755, files 0444/0555 (exec bit kept), so `rm -rf .xbin` keeps working; read-only to sandboxes by bind flags, not host modes. Rule C5 (no host-side following opens or walks inside work trees, materialized trees or quarantines) has a guard test, `TestNoFollowingHostWalks`. | 07 §2.6; 12 §2; 06 §3.1, §4; 15; WP-04 |
| 9 | **Nested components inside a checkpoint bind.** Mountpoints under the canonical path are created without traversing symlinks (openat2 with `RESOLVE_NO_SYMLINKS \| RESOLVE_BENEATH` in the sandbox init); a nested component's own code is bound after the checkpoint bind. | 07 §10.4; 06 T18; 13 §4.4 |
| 10 | **`deps/` and cross-tile symlinks in a checkpoint** are never followed on disk. The static plane re-dispatches `deps/<name>/…` to the target tile's `/c/` plane (its primary); any other escaping symlink answers 404 (`ErrEscapes`). Go builds get the other tile's pinned primary checkpoint (or work tree) bound over its `go.work` directory. | 05 §6; 07 §3.1, §4.1 |
| 11 | **Restore and backup wire.** No new field on any existing strictly decoded body; `POST /restore` unchanged. Per-deployment backup, restore and seed use new endpoints under `/api/xbin/deployments/…`, owned by 11-contract. | 05 §11; 08 §11; 11 §1.8 |
| 12 | **The `bx` contract.** 11-contract is authoritative for commands, flags, output and exit codes: 3 refused, 4 not confirmed (`--yes` needed without a TTY), 5 still running, 6 no tile deployments. 10-ux adopts them verbatim. | 11 §9; 10 §8.4 |
| 13 | **Non-primary principals and xbind's API: P26, default-deny.** Every `/api/xbin/*` route is deployment-scoped, primary-only or neutral; unclassified routes refuse non-primary principals. Manager acts need a human session: `p.Component == ""` and (`IsAdmin` or `mayManageTile`). No element principal passes the manager gate in the deployments plane, whatever `xbin` or `xbin:users` grants its tile holds. | 05 §10; 09 §6; 11 §0.5; 07 §8.3 |
| 14 | **Protected-primary operations** name the reviewed checkpoint: `checkpoint` on deploy and roll back; `expect` on promote, reload now and reassignment; otherwise 400. The check is a compare-and-set on the record's `seq`. | 05 §5; 07 §8.3; 11 §1.2, §1.6 |
| 15 | **Dry runs and diffs never create a store on a zero-state tile.** Dry runs of pause live reload and add compute the impact without capturing; a work-tree diff on a tile without a record answers 409 `record:false` (11-contract's pick of the two the spine allows). The store exists only after a committed opt-in. | 05 §5; 07 §2.11; 11 §1.11 |
| 16 | **`/components` deployment-level fields** (`runtime`, `hasIndex`, `native`) describe the primary's code (its checkpoint when pinned); `manifestError`, `roles`, `uses` and `deps` stay the work tree's. | 05 §6; 11 §2.7, §8 |
| 17 | **The inbound surface follows the primary's code:** `template`, `exposes`, `expose.roles`, `provides`, `chrome` (the spine's three-column table). Non-primary documents are never chrome. | 05 §6; 07 §5.1; 11 §2.8 |
| 18 | **P22 resources.** A pinned primary provisions from its checkpoint's `scope.json`; every deployment from its own code. A checkpoint's `scope.json` is read with `OpenBeneath` and validated (resource-name charset). Limits default to the tile's, are set by managers, never above the tile's ceilings. | 05 §6; 07 §5.4; 08 §6.7; 11 §1.7 |
| 19 | **The sandbox-managers interplay** (another branch, described in words). The agent tile reaches `sandbox-manager` providers through the custom role `consumer`, so under P3/P23 its non-primary deployments are blocked from them; likewise llm-gw's `writer` completions under the read clamp. | 09 §5.12–§5.13; 02 §4 (NG-16); 10 §3.10; 14 K14; 15 (`TestSandboxManagerEdgeBlocked`) |
| 20 | **Baseline wording:** the baseline is master; D112 is landed, and D113 is designed ([plans/tile-sandboxes.md](../tile-sandboxes.md)). | [README.md](README.md); 05 §12; 14 PRE-1 |
| 21 | **Names settled by 14 §0.3** bind every document: `internal/deployments`; bx files `livereload.go`, `deploy.go`, `deployment.go`, `deployclient.go`; non-main log `.xbin/deploy/<TileKey>/d/<name>/backend.log`; fixtures `apps/reloady`, `apps/deployy` and passes `livereload`, `deployments`; `web/frame-testapi.js` and `web/bx-deploy.js` defining `<bx-deployments>`; `Ensure` kept with new names beside it; the SDK in WP-59. The new stores (`data/deployments`, `data/checkpoints`, `.xbin/deploy`) are keyed by `<TileKey>`; existing stores keep their keys. | 14 §0.3; 13 §3–§4; 05 §3 |
| 22 | **Decision IDs.** Only D1–D114 are cited; the sandbox-managers work and cgi's removal are described in words. | [README.md](README.md); §7 below |

## 3. Proposals adopted into documents

The review deduplicated every document's "New proposals" into 65 rows: 20
adopted into the model, 37 adopted in a document, 5 owner questions (§4) and
3 rejections (§6). A document's own "New proposals" list still carries each
NP id, marked resolved or adopted.

### 3.1 Into the model (the spine)

| NP ids | Adopted as | Held by |
|---|---|---|
| NP-04-1, NP-02-4, NP-02-5, NP-06-10, NP-10-5, NP-11-8, NP-11-9, NP-11-21, NP-13-5, NP-13-6, NP-08-12 | Rule C2 and the event audience (rulings 1–2); data ops announced in `deployments` | P13; 05 §8; 11 §3 |
| NP-04-8, NP-06-1, NP-09-4, NP-13-11, NP-09-17 | Default-deny route classes, reads included, with a guard test; PR decisions primary-only for instance principals | P26; 05 §10; 09 §6; 13 §7 |
| NP-02-12, NP-06-14, NP-07-6, NP-08-15, NP-09-16 | Primary first: VM headroom, CPU weights, per-namespace quota buckets, per-(tile, deployment) caps such as 64 bus push subscriptions; `memory.low` out of v1 | P25; 05 §12; 07 §10.2–§10.3; 08 §12; 09 §6 |
| NP-09-1, NP-09-5, NP-09-3, NP-04-2, NP-04-3, NP-09-2 | Edge values per kind; unknown values read as `block`; `block` wins | P23, P27; 05 §5, §7; 09 §5.3–§5.9 |
| NP-02-11, NP-07-2 | A failed move off the work tree pins to the attempted checkpoint, state `failed` | P9; 05 §4–§5; 07 §8.2, §8.4 |
| NP-06-3, NP-11-4 | Reviewed-checkpoint compare-and-set (ruling 14) | P21; 05 §5; 11 §1.6 |
| NP-06-4, NP-06-6, NP-06-7 | Inbound surface from the primary's code (every pinned primary); a protected primary's build products only from manager operations; no feeds onto it | P9, P21; 05 §6, §9; 07 §3.4, §5.1 |
| NP-07-3 | A pinned deployment's `XBIN_RES_*` from the union of the work tree's and its code's `uses`; tile-level fields fall back to the primary's checkpoint manifest | 05 §6; 07 §5.2–§5.3 |
| NP-11-1 | Two naming rules: signals absent for the primary, credentials and stored state absent for `main`; reassignment restarts both primaries | P17; 11 §0.2 |
| NP-09-8, NP-09-13 | Reassignment mechanics: new primary first, streams cut, bare `reload`, root `xbin.json` never written, dormant ingress hosts re-validated | 05 §5; 07 §8.8; 09 §8 |
| NP-02-10, NP-06-15, NP-08-4 | Shared (scope, name) namespaces: authority on every claimant, refcounted deletion, no scope splits; creating a member while the scope root's primary isn't `main` is refused, and joining a namespace that holds data is a manager act | P28; 05 §5, §9; 08 §6.2, §6.5 |
| NP-06-11, NP-12-9 | Collision-free `<TileKey>`; the record verifies path, owner ref and creation stamp; every creation path resets the path's state | P29; 05 §3, §11; 11 §10.1 |
| NP-12-1, NP-12-11, NP-02-6, NP-04-9, NP-06-17, NP-13-7, NP-11-14 | The `+` qualifier in its narrow form (ruling 4); `<tile>+<primary>` only for humans and the tile's own principals. The broad refusals are not built (§6) | P17; 01; 05 §7; 11 §2.1 |
| NP-02-2, NP-11-16 | Opting out keeps the store under GC; the zero state is "no record" | P5; 05 §2 |
| NP-11-2, NP-14-13 | The session target fixed for the session's life, "API off" as the last fallback, protecting restarts sessions that target the primary | P24; 05 §5, §7; 11 §7.4 |
| NP-06-2, NP-08-5, NP-09-11, NP-06-19 | Operating checked per request; manager acts need a human session; mutations are non-GET JSON | 05 §10; 11 §0.5 |
| NP-06-13 | P19 enforced in both directions | P19; 05 §10; 06 T14 |
| NP-09-9, NP-11-10 | Run now is terminal level, one run in flight per job | 05 §5, §10; 09 §7; 11 §1.9 |
| NP-11-11 | Resetting `main`'s data while `main` isn't primary is a manager act | 05 §5, §10 |
| NP-07-5, NP-13-2, NP-12-6 | Cgroups flat until the first non-main deployment, then tile → deployment → {`backend`, `sbx-*`}; built once in the shared layer, D113 reuses it | P5; 05 §12; 07 §10.3 |

### 3.2 In a document

| NP ids | Adopted as | Held by |
|---|---|---|
| NP-02-7, NP-12-7, NP-13-8, NP-06-18, NP-11-15 | The fetch remote: per-session injection, hardened server, git view, `rebase --onto` in flow H; its gate is ruling 6's, not the `/c/` read gate | 05 §3; 07 §2.7; 11 §1.12; 06 T15 |
| NP-06-12, NP-12-10, NP-08-9 | Backups: `main`'s archive unchanged, the primary always archived, downgrade-safe deployment archives, validated restore | 05 §11; 08 §11; 06 T11 |
| NP-07-7, NP-02-3 | Caps and retention as v1 constants owned by 07, rollback targets that never build | 07 §2.5, §2.8, §10.3 |
| NP-07-1, NP-07-9, NP-07-8 | Capture in two confined runs with an admission check, one sandbox per capture, over-size refused naming the largest directories, differential materialization gated on a benchmark (NP-04-4's exclude list is rejected, §6) | 07 §2.2, §2.6, §13.3 |
| NP-07-10, NP-14-6 | confine `DirFrom`; shared-layer ownership with D113 | 05 §12; 07 §10.5, §10.7; 14 §2.5 |
| NP-07-4 | `go.work` modules build against the other tile's pinned primary | 05 §6; 07 §3.1 |
| NP-08-1, NP-12-12 | An injective `.deployments` level, one bbolt file per namespace (AppArmor check: Q10) | 08 §3 |
| NP-08-2 | Tile principals reach their own deployment's data; admins and managers pass `?deployment=` | 08 §4.1; 11 §8 |
| NP-08-3 | `res:workspace/*` is never split | 05 §9; 08 §4.2 |
| NP-08-6, NP-08-7, NP-08-8 | Seeding online by default, stopped when required; namespace metadata authoritative; vault placeholders computed (online path: Q9) | 08 §8.3–§8.4, §10; 05 §4 |
| NP-08-14 | Orphaned namespaces marked, listed, deleted after 14 days; `main`'s never | 08 §9.3 |
| NP-09-6, NP-09-12, NP-09-18 | `block` fails at call or dial time naming the edge; non-primary terminators get no forward door and no roster; foreign subscriptions re-checked per delivery | 09 §5.6–§5.10 |
| NP-09-7, NP-09-10 | A clamped call's response names the clamp; refusals counted per (deployment, edge) and shown in the panel | 05 §7; 09 §5.5; 10 §3.10 |
| NP-04-7, NP-09-15 | `match`'s fallback deferred to M3 (L3); v1 fixes only P7's single resolver, P27, and "never a full-role fallback to the primary" | 04 (NP-04-7); 09 §9 |
| NP-10-1, NP-10-2, NP-12-3, NP-10-3, NP-10-8, NP-10-9, NP-10-4 | Terminal notices written by the browser; the `'deployments'` layout never persisted; one pure strings module; logs follow the tab's target; the state names the managers; data labels | 10 §2–§3, §9, §12, §14.5 |
| NP-11-7, NP-11-17 | bx spellings and exit codes (ruling 12) | 11 §9; 10 §8 |
| NP-11-5, NP-10-6, NP-11-20 | `confirm` tokens on data-bearing routes; `--yes` without a terminal; `dryRun: true` answering an `Impact` | 11 §1.2, §9.1; 10 §5, §8.2 |
| NP-11-3 | Record fields `schema`, `seq`, `lastLiveReload`, `liveReloadSince` | 05 §4; 11 §10.1 |
| NP-11-6 | The deploy log as a git-native chain of every finished attempt | 05 §3; 07 §2.3; 11 §10.3 |
| NP-11-13, NP-12-2, NP-14-4, NP-11-22, NP-11-12, NP-04-5 | `features` in `GET /deployments`; every `deployment` parameter echoed; `deployment` as its own field; `+` only in paths, bx arguments and display text | 11 §1.3, §8; 12 §4.1; 14 §4.6 |
| NP-11-18, NP-12-4 | An origin label per deployment in origins mode; a 404 with the reason until WP-38 lands | 05 §7; 11 §7.5; WP-38 |
| NP-11-19 | Readers see the primary-scoped view (ruling 3) | 05 §8; 11 §1.3 |
| NP-12-5 | `/components` and `?native=1` describe the primary's code (ruling 16) | 05 §6; 11 §2.7 |
| NP-12-8 | The admin sandboxes tab keys generations per (tile, deployment); fixed on master in M0a, and installed admin tiles are fixed forward by the admin tile's update | 12 §3.6; 14 (M0a) |
| NP-12-13 | `bx doctor` and panel diagnostics (plus 04's open question 4: `read` edges to providers declaring no `expose.roles`) | 12 §5.5 |
| NP-13-1, NP-14-3, NP-13-3, NP-14-8 | Package layout, generic op dispatch, the per-spawn deployment view, new runner names beside the old | 13 §3–§4; 14 §0.3; 05 §7 |
| NP-13-4 | `/backends` and `/runtime` keep one row per tile, non-primary nested | 11 §8 |
| NP-13-9 | The M3 streaming run is a confine change | 05 §12; 07 §10.6; WP-S6 |
| NP-13-10, NP-02-8, NP-15-1, NP-15-2, NP-15-3, NP-15-4, NP-15-6 | One CI rootfs, `startIsolatedDaemon`, `HARNESS_ISOLATE=1`, downgrade tests against the previous release, `TestIntegrationPackagesListed` (NP-02-8's Go toolchain is not built, §6) | 15 §10; 14 R-7 |
| NP-13-12 | The drift count is a debounced confined diff, never a count of watcher batches | 07 §2.9; 10 §2.1 |
| NP-13-13, NP-14-1, NP-13-14, NP-14-2, NP-14-11, NP-14-7, NP-14-9, NP-14-12, NP-15-8, NP-15-12 | M0 preparation and swarm hygiene: M0a on master, contracts WPs first, the port grid, legacy assertions in new files, gated WPs never hold an exit, hand-kept goldens, label-based asserts | 14 §1–§5; 13 §2; 15 §1 |
| NP-02-9, NP-15-11 | Latency baseline first; two test tiers | 15 §8; WP-04 |
| NP-15-9 | `TestDeploymentWording` | 15; 14 K15 |
| NP-14-10 | One builder page, `docs/tile-deployments.md`, written at implementation time with a changelog entry | WP-29, WP-65 |
| NP-02-1 | The milestone split (reassignment in M2; feeds and `match` in M3) | 14 §1 |
| NP-06-8 | Protecting a primary lists nested components and warns unless they are protected too | 06 T16 (10 still lacks the warning: A16) |
| NP-06-16 | Manager-only checkpoint purge, M2 (settles R-4 as yes; the route: Q11) | 05 §10; 06 T20; WP-66 |

### 3.3 Written after the review

These appeared in the documents after the review's triage. None changes the
spine; each is built as its holder states unless an open question below
names it.

| NP ids | Proposal | Held by | State |
|---|---|---|---|
| NP-02-13 | Synthetic data and test secrets are the taught default; "Set a test value…" at terminal level | 02 §3 (S-11) | build as written; 10 and 15 still lack it (A14) |
| NP-06-20, NP-11-31 | Bounded, level-gated diffs | 11 §1.11; 06 T10 | build 11's text (A9) |
| NP-06-21, NP-08-16 | At most 64 declared resources per non-main namespace; declared sets per namespace | 08 §6.7 | build as written |
| NP-07-11, NP-14-14 | The registry's component is the primary's, through one `PinnedPrimary` hook | 07 §5.1 | build as written (A10) |
| NP-07-12 | A tile-rooted scope's `importMap` follows the deployment's code | 07 §5.1 | open: Q4 |
| NP-07-13 | The deploy journal, `pending.json`, reconciled at boot | 07 §8.4 | open: Q3 |
| NP-07-14 | A protected primary's lost build products hold its start until a manager redeploys | 07 §3.4 | build as written (Q2 uses it) |
| NP-07-15 | No checkpoint exclude list in v1 | 07 §2.5 | adopted; rejects NP-04-4's list (§6) |
| NP-09-19 | Providers key caller state on (`X-XBin-From`, `X-XBin-Deployment`) before any later value unblocks `consumer` | 09 §5.13 | a precondition for O3 and L3; nothing in v1 |
| NP-10-10 | The `Impact`, `Deployment` and `Edge` facts the confirmations render | 10 §5 | open: Q5 |
| NP-10-13, NP-10-14 | Flow E says where an applied change lands; a session states its own target | 10 §3.14, §9 | build as written |
| NP-11-23 | `restart: true` and restart of a failed deployment's current checkpoint | 11 §1.6 | open on a protected primary: Q2 |
| NP-11-24 | `bx deployment fetch` | 11 §1.12 | build as written (Q6 limits it) |
| NP-11-25 | A tile without a record accepts only the three opt-ins; a diff answers 409 | 11 §1.2, §1.11 | build as written |
| NP-11-26, NP-11-27, NP-11-28, NP-11-29, NP-11-30, NP-11-32 | `--yes` scoped to guarded commands; the limits wire; per-deployment backup routes; the `<TileKey>` spelling; deployment-bound frame-token minting; edge reporting | 11 §9.1, §1.7, §1.8, §10, §7.2, §1.1 | build as written |

## 4. Owner questions, still open

Each has a default the implementation builds meanwhile. None blocks a
milestone exit.

### O1 — The offload fix for `main`

**Question.** May a separate change make offload delete the encrypted
file-resource volumes it leaves behind today, for every tile, zero-state
tiles included?
- Today `removeScopeData` drops kv buckets and the plaintext
  `data/resources/<key>` (`internal/broker/backup.go:472-488`), but
  `data/resources-enc/<key>` and its mounts stay, although
  [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md) says
  "archived + removed" (`:33-34`).
- A naive deletion loses what the archive can't hold: symlinks, empty
  directories, xattrs and modes (08 §9.4). Deleting bytes users keep today
  falls under the never-break-users rule.

**Built meanwhile.** `main`'s offload stays today's, leak included. The docs
WPs state that offload keeps `main`'s encrypted file resources on disk.
Non-main namespaces get 08 §9.4's fix inside this design: lossy resources
detected and kept, mode and mtime headers, ciphertext removed only after
every archive PUT is confirmed. Their archives are new, so that part needs no
P5 exception (08 Div 8).

**Recommended answer.** Yes, as its own change outside this programme, with
its own decision, changelog entry and downgrade note. It frees only what a
verified archive holds without loss, and counts a non-default file mode as
lossy while an older restore is possible (NP-08-10).

**Blocks.** No milestone exit (WP-45 is gated, and gated WPs never hold an
exit: NP-14-12). Only a change to `main`'s offload waits for the answer;
R-3 and WP-45's card are read that way (A15).

### O2 — Pre-existing security defects

**Question.** Are the defects the research found in shipped behaviour fixed
as separate urgent changes before M1? A separate hardening branch is in
progress for the first three:
- **`chrome` from `xbin.json`.** It is read from the tile's own manifest
  (`internal/registry/registry.go:59-65`), which every terminal on the tile
  can write, and it drops the CSP sandbox
  (`internal/server/static.go:57-63`). The tile's frames then run with the
  session of whoever opens them, admins included (06 §5.2 S1; NP-06-5: make
  it an admin-set attribute outside the work tree).
- **`ScopeKey` and `CompKey` collisions.** `ScopeKey` is not injective
  (`internal/util/util.go:127-133`): `apps~x` and `apps/x` share a key, and
  with it a file resource's encrypted volume. `CompKey` keeps 32 bits
  (`:137-145`) and can be ground, sharing a vault file, log and run dir
  (NP-08-13; 06 S3).
- **Resource-name validation.** `scope.json` resource names are not
  validated: a name like `../../../x` steers `MkdirAll` and
  `gocryptfs -init` outside `data/resources-enc/`
  (`internal/broker/resenc_wire.go:139-144`;
  `internal/resenc/resenc.go:84-91`, `:156-176`; NP-08-11). 03 Div 11 asks
  for the check on every `scope.json` read, the work tree's included.
- Also found: restore trusts the archive's manifest and cron rows (06 S2);
  frame-token self gates admit any reader to backend logs, status and PR
  decisions; `/ws/events` hands every non-bus event to every subscriber
  (`internal/server/server.go:592-612`); an archiver tile's archives are
  unsigned.

**Built meanwhile.** This programme doesn't wait, and builds its own
surfaces correctly regardless (14 §0.4):
- a non-primary document is never chrome; a pinned primary's `chrome` comes
  from its checkpoint; the P19 check treats a tile as chrome if the work tree
  or any deployment's code says so (06 T14 step 5);
- the new stores use `<TileKey>`, non-main keys are injective, and 08's key
  function refuses `..` and NUL segments in every namespace (WP-39);
- a checkpoint's `scope.json` is read beneath and validated (ruling 18);
- every new event type and field is filtered (ruling 2), and the new archive
  sections are validated (WP-23, WP-44a).

**Recommended answer.** Yes: each on the hardening branch with its own
decision and changelog entry, the `ScopeKey` collision and the resource-name
traversal first; the remaining items as follow-ups; archive signing a later
proposal.

**Blocks.** Nothing in M0–M2. If the hardening branch lands first, WP-39
(every `ScopeKey` site) and WP-18 (the registry's `chrome`) rebase onto it,
and its D-numbers come before this programme's (§7).

### O3 — A `full` edge for roles that mean spend

**Question.** Under P3's read clamp, a non-primary deployment of any
LLM-using tile can list models but can't run a turn: llm-gw guards `/v1/*`
with `writer` (`builtin-tiles/llm-gw/backend/main.go:274`). Should a later
manager-set edge value `full` lift the clamp on one edge (NP-09-14), or should
providers opt in through a request header carrying the pre-clamp role? Either
relaxes P3's "a non-primary deployment never writes into another tile's
primary" and needs the owner. P23 doesn't decide it, because `writer` can be
read-clamped.

**Built meanwhile.** Not built (R-5). The values stay `read`, `block` and
`inherit`. 02 §4 (NG-16), 10 §3.10, the builder docs and the edge panel say
that non-primary deployments lose completions; the clamp header and the
refusal counters make it diagnosable (09 §5.12). The agent tile's sandbox
managers are a separate matter, settled by P23 with no override (ruling 19).

**Blocks.** Nothing in M0–M2. It limits M2's use for developing LLM tiles,
the agent template among them, on a non-primary deployment. Adding `full`
later is additive: P27 makes an older binary read the unknown value as
`block`. NP-09-19's partition keying must land before it.

### O4 — The ship-dark switch's default

**Question.** In the first release that carries M1, does the ship-dark switch
(NP-14-5; 14 §6.3) default to on (opting in open) or off? And does M1 ship to
users on its own, with M2's routes reserved and `features` listing only
`live-reload/1`?

**Built meanwhile.** The switch is one `boot.Config` field (WP-06), enforced
in the plane's single authorize function (WP-14a). It defaults to on (R-9)
and is turned off only if the QA box soak finds trouble. M1 may ship alone
(14 §4.6). The field regenerates [/docs/config.md](/docs/config.md).

**Recommended answer.** As built.

**Blocks.** The first release carrying M1, not implementation.

### O5 — Does protection cover the vault?

**Question.** Protection covers the primary's code and the inbound surface
that follows it (P21). Should it also gate the protected primary's vault and
config? P24 already keeps every session token off a protected primary, so its
vault is written only by its own backend and by admins (06 T5). The open part
is whether tile managers who aren't admins get a manager-only session path to
set a protected primary's vault values (NP-06-9, narrowed).

**Built meanwhile.** Code only. Vault writes stay as today. Per-key
vault-copy provenance and the alwaysOn duplicate-connection warning stay 08
details (08 §10).

**Blocks.** Nothing. WP-53a builds protection code-only; a vault gate would
add one manager route later.

### O6 — Coordination before M0

**Question.** Who is the integrator (PRE-4)? When does cgi's removal land
(PRE-5: one commit, 3cc44d7, not yet an ancestor of master)? Does D113's
owner accept 14 §2.5's split of the shared sandbox files (K6)?

**Built meanwhile.** The orchestrating session acts as integrator: it owns
the changelog, the decision log and D-number assignment on the integration
branch (14 §4.2). Wave 0.1 waits for cgi's removal; M0a work outside its
files proceeds. The D113 split is taken as accepted, since D113 is designed
but not built (the first lander builds WP-S5). The integrator keeps one name
per test pair of 15 §9 as WPs merge.

**Blocks.** PRE-4 and PRE-5, so the start of M0 wave 0.1. This file's
existence unblocks PRE-2.

## 5. Other open questions

### 5.1 Calls the integrator makes

Each lists the recommended default, which the WPs build until the integrator
rules, and what it blocks.

**Integrator ruling (2026-09-27):** every default in §4 and §5.1 is adopted as
written and is what M0–M2 build. The owner handed the build to the
orchestrating session ("I'll let you drive until implemented"). The owner
may overrule any of them at a milestone exit.

**Q1 — The record's creation stamp has nothing to compare with** (11 DIV-8).
P29 and 05 §3 ignore a record unless path, owner ref and creation stamp match
the tile, but a tile carries no creation stamp today.
- *Default:* verify `tile` and `owner` on every read; keep `created` for the
  admin's adopt-or-clear act and for display; rely on every creation path
  removing a leftover record first (05 §11), which covers re-creation by the
  same owner through xbind. P29's wording follows ("verifies path and owner;
  the creation stamp orders adopt-or-clear").
- *Alternative:* a stamp of the tile's own, written at creation (the
  directory's birth time where the filesystem reports one), which would also
  catch a same-owner re-creation made outside xbind or under an older binary.
- *Blocks:* WP-13's validation rule (M1); WP-32 if frame-token claims carry
  the stamp (M2).

**Q2 — Restarting a protected primary.** 11 §1.6 makes `restart: true`
terminal level on every deployment ("it moves no code"); 07 §8.7 makes it a
reviewed operation on a protected primary that may rebuild lost build
products.
- *Default:* a restart from kept artifacts is terminal level everywhere; it
  is not among ruling 14's reviewed operations, and it does what a crash
  restart already does (P9). On a protected primary a restart never builds:
  when an artifact or env layer is missing it answers 409 naming the
  manager's reviewed redeploy (NP-07-14; P21). 07 §8.7 follows.
- *Blocks:* WP-53a (M2). M1 builds 11's restart; there is no protection in
  M1.

**Q3 — In-flight deploys across an xbind restart.** 07 §8.4 (NP-07-13)
journals every accepted request in `data/deployments/<TileKey>/pending.json`
and reconciles it at boot as `cancelled`, `ok` or `interrupted`; 11 §1.10
says a queued request is lost with an xbind restart.
- *Default:* adopt the journal (SC-AUDIT wants every attempt logged). 11
  §1.10 and §10 add the file's format; the answer to an accepted request
  keeps its shape.
- *Blocks:* WP-14b and WP-16b (M1).

**Q4 — Where a scope's `importMap` comes from** (NP-07-12). 05 §6's table
classifies `scope.json`'s `resources` as deployment-level and says nothing of
its `importMap`, which shapes every document the tile serves.
- *Default:* deployment-level, beside `resources`: a document is served with
  the import map of its deployment's code; the registry's component, which
  describes the primary, takes the primary's. Whether a directory roots a
  scope stays the work tree's. 05 §6 gains the field.
- *Blocks:* WP-18 and WP-19 (M1).

**Q5 — The facts confirmations render** (NP-10-10). 10 §5 renders
`claimants`, `sessions` (`restart`, `retarget`, `loseApi`), `missingSecrets`,
`inbound`, `edges` and per-deployment `limits`, which 11 §1.1's `Impact`,
`Deployment` and `Edge` types don't carry.
- *Default:* 11-contract adds them, additively, through a contract amendment
  before WP-30 declares the M2 types (14 §4.4). Until then the panel omits a
  missing fact and never guesses it.
- *Blocks:* WP-30, WP-54 and WP-55 (M2).

**Q6 — The fetch remote in "API off" sessions** (03 Div 12). The injected
git settings ride the session's bearer, and an "API off" session has none.
With a protected primary and live reload paused, P24 falls to "API off", so
no session can run flow H's `git fetch xbin-deploy`.
- *Default for v1:* document it in 06 T15, 11 §1.12 and 10 §10.2: when the
  primary is protected and live reload is paused, flow H starts by attaching
  live reload to a non-primary deployment (adding one if needed; both are
  terminal-level acts), whose sessions then target it and can fetch. A
  credential scoped to the view repository alone is a later, additive
  change.
- *Blocks:* nothing in M1 (no protection); WP-64's docs in M2.

**Q7 — `<TileKey>` for non-main files inside existing store directories**
(08 Div 9). Ruling 21 keys the three new stores by `<TileKey>` and keeps the
existing stores' keys. 08 also keys the non-main files it adds inside
existing directories (vaults under `data/vault/.deployments/`, prefs, archive
keys) by `<TileKey>`.
- *Default:* confirm 08's reading. The files are new, `main`'s keys don't
  change, and a ground `CompKey` must not make two tiles share a secret or an
  archive, which is ruling 21's reason.
- *Blocks:* WP-39 and WP-43 (M2).

**Q8 — Bus messages from a non-primary namespace** (08 Div 10). Rule C2 names
`reload`, `build-*`, `status` and notify. A bus message is data; moving it to
the `deployments` type would break a non-primary deployment's own `bus.on`
handlers.
- *Default:* confirm that C2 governs lifecycle, status, build and notify
  events only. Bus messages keep type `bus`, stay in their namespace, and
  reach 05 §8's audience by namespace matching (08 §4.3).
- *Blocks:* WP-40 and WP-48 (M2).

**Q9 — Online seeding is unverified**: python3's sqlite3 backup over a
gocryptfs mount with a WAL primary; a non-interactive `gocryptfs -passwd`
re-wrap with the bundled binary; 06's read-only mount against a WAL reader's
`-shm`.
- *Default:* build the stopped seed first. The online sqlite seed ships only
  after its integration test passes; 06 follows 08 (a read-write bind and a
  `mode=ro` connection).
- *Blocks:* WP-42's online path only.

**Q10 — Does AppArmor's `**` in D110's rule match the dot-prefixed
`.deployments` segment?**
- *Default:* verify by mounting a non-main volume on the Ubuntu QA box before
  WP-39 merges (M2 exit criterion 4 repeats it). If it doesn't match, use a
  non-dot level name that no existing key function can produce (08 §3.5).
- *Blocks:* WP-39.

**Q11 — The checkpoint purge route.** The review settled R-4 as yes, in M2
(NP-06-16), but 11-contract has no route for it.
- *Default:* the integrator adds the route through a contract amendment
  (14 §4.4): a manager act in a human session naming the checkpoint, refused
  while any deployment runs it.
- *Blocks:* WP-66 only (gated; never holds M2).

**Q12 — One edge policy per multi slot.** The agent's `mcp` slot binds
several providers (`builtin-templates/agent/xbin.json:49`).
- *Default:* one policy per slot in v1, evaluated per provider; a provider
  whose role can't be clamped is blocked (P23), and any `block` refuses
  (P27). A per-provider key (`slot:<name>#<provider>`) is additive later.
- *Blocks:* nothing.

**Q13 — "Block every edge" in one step** (06 §5.1 R1 asks this file).
- *Default:* yes, as a tile-manager panel action next to Protect, not tied
  to it. It sets each of the tile's edges to `block` through the existing
  edge route; no new wire.
- *Blocks:* nothing (WP-55).

**Q14 — `.xbin/` is documented as "derived state — safe to delete"**
(`docs/protocol.md:2418`), but it also holds terminal layers, the tier-2 uid
map and builtin provenance (03 §10).
- *Default:* WP-29 tightens the wording, with a changelog line, naming what
  is derived. P9's "loss of `.xbin/`" guarantee relies only on `.xbin/deploy`
  and `.xbin/build` being rebuildable, and materialized trees keep
  `rm -rf .xbin` working (ruling 8).
- *Blocks:* nothing; WP-28's restart-path tests assume only those two.

**Q15 — Temp-dir exhaustion.** Every sandbox launch writes a spec file to
the host temp dir, which ran out of inodes on the design box.
- *Default:* a designed failure mode. Spec files are removed on every exit
  path; a deploy that hits `ENOSPC` fails and says so, and the deployment
  keeps serving its previous generation. 15 adds a test.
- *Blocks:* nothing.

**Q16 — A warning when a primary isn't `main`.** An older binary serves such
a tile from `main`'s data (12 §5.4, R-2).
- *Default:* `bx doctor` and the admin console list every tile whose primary
  isn't `main`, beside 12 §5.5's operator checklist. No startup refusal.
- *Blocks:* nothing (WP-63).

**Q17 — Small UX calls** (10's open questions 1, 3 and 4).
- A `+dev` corner label drawn by the injected client in non-primary
  documents. *Default:* no; the frame titlebar and the shell show the
  deployment, and it would add tile-visible UI to documents xbind doesn't
  own.
- An agent-tab transcript line when live reload moves away from its target.
  *Default:* no in v1; `bx` output and terminal notices carry it.
- A shell prompt showing `+dev` when `XBIN_DEPLOYMENT` is set.
  *Default:* no injected prompt hook in v1; the terminal bar and NP-10-14's
  session note state the target.
- *Blocks:* nothing.

**Q18 — An admin "reap now" action** (15's open question).
- *Default:* no product feature; idle reap is tested through the seam's fake
  clock.
- *Blocks:* nothing.

**Q19 — `main`'s restore: merge or replace.** The docs promise a wholesale
overwrite; the code merges (08 §11.4).
- *Default:* keep today's merge for `main` (zero change) and fix the wording
  separately; non-main restores replace. Switching `main` is a separate
  compat-reviewed change, the owner's call if ever pursued.
- *Blocks:* nothing.

**Q20 — Per-deployment tile-managed sandboxes.** D113 keeps definitions in
"the tile's `data/sandboxes.json`" without saying whether the file is
workspace-wide or per tile.
- *Default:* follow D113 as built. Per-deployment keying mirrors dormant
  registrations: a per-deployment file beside the record, or a deployment
  field on workspace-wide rows; `main`'s rows unchanged (WP-S5).
- *Blocks:* nothing until D113 is built.

**Q21 — A readiness probe for protected primaries.** Deploys use today's
5 s socket connect as the health check.
- *Default:* no in v1. A builder-declared health path is a later manifest
  contract.
- *Blocks:* nothing.

**Q22 — Which binds make a direct confined run refuse** (WP-03's report,
wave 0.2). 07 §10.5 and WP-S1's card refused a direct run (no `--isolate`)
with "any non-mask bind with `Src ≠ Dst`". Today's git import already binds
the daemon's `~/.ssh` at `/root/.ssh` (`internal/broker/gitimport.go:63-67`),
and a direct run ignores binds, so an ssh import works without isolation;
under that rule it would fail for every user who runs without `--isolate`.
`TestConfineBindDestinationDefault` (WP-03) pins that this shape still runs
directly.
- *Default:* a direct run returns `ErrNeedsIsolation` only when `DirFrom`
  differs from `Dir` or when a bind made by `confine.At` is present. A bind
  a caller builds by hand keeps today's direct-run behaviour, whatever its
  `Src` and `Dst`: the direct run already sees the host's own files where
  the sandbox would show them. How `At`'s binds are told apart (a field of
  their own on `Cmd`, for example) is WP-S1's choice; the golden stays
  unchanged.
- *Blocks:* nothing; WP-S1 (wave 1.1) builds the default. 07 §10.5 and the
  card follow.

**Q23 — `bx logs` without a deployment** (WP-02's report, wave 0.2). 11
§9.3, the wire's authority, says it "behaves as today"; 13 §4.14's
`cmd/bx/status.go` row, 08 §2's backend-log row and WP-26's card move it to
`GET /logs`, to fix side finding #22 (`.xbin` is masked in isolated
terminals). `TestBxTodayInvocationsUnchanged` (WP-02) pins today's reading:
the file, no request, and exit 1 without a request for a tile with no log.
- *Default:* 11 §9.3 wins. Every invocation that works today keeps reading
  `.xbin/log/<CompKey>.log` without a request. `GET /logs` is used only where
  that read cannot answer: a deployment named by `--deployment` or
  `$XBIN_DEPLOYMENT`, and a workspace whose `.xbin/log` bx cannot see (an
  isolated terminal: side finding #22). A missing file under a visible
  `.xbin/log` stays today's "no logs yet", exit 1. Changing the golden
  instead is a compat change (14 §2.1 clause 8).
- *Blocks:* nothing; WP-26 (wave 1.2) builds the default. 13 §4.14, 08 §2
  and the card follow.

### 5.2 Divergences still open in the documents

Wording and alignment only; the winning text is named, and no work package
builds the losing one.

| # | Where | Fix |
|---|---|---|
| A1 | 05 §8 says "Today every non-bus event reaches every subscriber" (03 Div 9) | `reload`, `build-*`, `status` and `grants` reach everyone; `bus`, `pr`, `term` and `session` are filtered (`internal/server/server.go:592-612`); 11 adds `deployments` to the per-type switch |
| A2 | P17 lists "event component" among the role-rule signals (13 Div 7) | **done:** dropped from 05 §13's P17; under C2 no event carries a qualified `component` |
| A3 | 05 §12 says `cap:sandboxes` stays the tile's, as if any tile may hold it (09 Div 6) | the rule applies to a sandbox-manager tile's own deployments; other tiles' non-primary deployments have no sandboxes in v1 (ruling 19) |
| A4 | 01's Deploy remote cites "the `template` remote precedent, via `GIT_CONFIG_COUNT`" (03 Div 14) | the `template` remote is written into `.git/config` (`internal/broker/templaterepo.go:139-151`); the deploy remote, like `xbin-deploy`, is injected the way the git rewrite is |
| A5 | 01 and 11 §2.1 warn about `+` names "the D82 way" (03 Div 10) | D82 has no warning; say "a `warnings` entry on the create answer, for one release" |
| A6 | `/components`' `roles` is `expose.roles` from the work tree (03 Div 13) | 11 §2.8 states that `roles` may lead the primary's code; 10 says so where roles are offered |
| A7 | 04 marks NP-04-4 "adopted into 07" | rejected (07 NP-07-15; §6) |
| A8 | 06 T10 says the deploy queue keeps only the newest pending deploy | **done:** 06 T10 now says 07 §8.3's FIFO, at most 8 deep per deployment, a request equal to the tail merged into it |
| A9 | 06 T10 lets frame and instance principals diff their own deployment and the primary | **done:** 06 T10 follows 11 §1.11: frame and instance principals are refused; the bounds are shared |
| A10 | 13 §4.1–§4.2 and §5 name a registry `Keep` hook | **done:** 13 and 14 WP-05 name 07 §5.1's `PinnedPrimary` (NP-07-11, NP-14-14) |
| A11 | 05 §5 has no operation row for limits (10 Div 7) | add "Set limits for Y" (tile managers, human session, never above the tile's ceilings); 11 §1.7 has `POST /deployments/limits` |
| A12 | 08 lists NP-08-4 and NP-08-15 as still open | adopted under P28 and P25 (§3.1) |
| A13 | 12 NP-12-13's `bx doctor` list | add `read` edges to providers that declare no `expose.roles` |
| A14 | NP-02-13 lives only in 02 | 10 §10.2 teaches synthetic data and test secrets; the panel offers "Set a test value…"; 15 adds `TestTargetedSessionWritesOnlyItsNamespace` |
| A15 | 14 R-3 and WP-45 call the offload fix a P5 exception | per 08 Div 8, the non-main fix is design; only a `main` change waits for O1 |
| A16 | 10 §3.9 never mentions nested components | the protect confirmation lists the nested components whose code the parent's writers write, and warns unless each is protected too (06 T16, `TestProtectionCoversNestedComponents`) |

### 5.3 Later rungs (M3)

R-8 gates M3; each call gets a full WP card once ruled.
- **L1 — A tracked branch deploys which heads?** *Default:* any new head,
  force pushes included; each is just a new checkpoint.
- **L2 — May a push to `deploy/<name>` create deployment `<name>`?**
  *Default:* no (auto-creation on push is a documented pitfall elsewhere).
- **L3 — `match`'s fallback when the provider has no same-named
  deployment.** NP-04-7 refuses, naming the missing deployment; NP-09-15
  falls back to the provider's primary, read-clamped, with the provider's
  managers opting deployments in. *Default:* decided in M3's design; never a
  full-role fallback to the primary (both agree), and NP-09-19 first.
- *Blocks:* M3 only.

### 5.4 Settled since the review

Questions the review raised that the rulings, the spine or an owning
document now answer. They are not open.

| Question | Answer | Where |
|---|---|---|
| Non-primary deployments and the agent tile's sandbox managers | blocked, no override | P23; ruling 19 |
| `+` in other new tile names ever refused | never; a one-release warning | ruling 4 |
| The git view's commit parent | parentless; the work-tree head named in the message; `rebase --onto` | 05 §3, §14 (flow H) |
| Origins mode | an origin label per deployment; 404 with the reason until WP-38 | 05 §7; R-6 |
| Who sets P22 limits, and how | tile managers; `POST /deployments/limits` | ruling 18; 11 §1.7 |
| Human access to non-primary kv and blob data | `?deployment=` for admins and managers | 08 §4.1 |
| `/c/<parent>+<name>/<child>/` | 404 naming the child's own deployment URLs | 11 §2.4; 04 (open question 1) |
| The write gate in the legacy asset mode | subresources by path; documents and `/api/` write-gated | 05 §7 |
| `/sandboxes` stats with a per-tile parent | `main`'s rows keep `scope: "tile"`; non-main rows `scope: "deployment"` | 07 §10.1; 11 §8; 12 NP-12-8 |
| Terminal-window controls on every tile | the zero-state panel on every tile, disabled with the reason where impossible; the API dropdown keeps today's two entries without a record | 10 §3.13; 14 §0.3 |
| Offload of non-empty non-primary namespaces | archived before anything is removed | 05 §11; 08 §9.4 |
| Caps: constants or config | v1 constants owned by 07 | 07 §2.5 |
| Prefs after a reassignment | stay with the deployment (the name rule) | 05 §9 |
| Protection under `--no-auth` | allowed, and marked "not enforced: authentication is off" | 06 T12; WP-53a |
| Differing registry IDs, package, bx files, exit codes, fixtures, log path | 14 §0.3's names | rulings 5, 12, 21 |
| Harness isolation | `HARNESS_ISOLATE=1` | 15 (NP-15-3) |
| Reassigning one member of a multi-tile scope | refused in v1 | P28; 05 §5 |
| Who may run now | terminal level | 05 §5, §10 |
| Timing criteria in CI | two tiers | 15 §8 |

## 6. Rejected proposals

| Proposal | Reason |
|---|---|
| NP-10-7 — exit code 6 for authority or policy refusals | Collides with 11's exit 6 ("no tile deployments"); 11's exit 3 already signals "refused by authority or policy" (ruling 12). |
| NP-15-7 — a guard test for a removed runtime | cgi no longer exists. |
| NP-04-6 — build a candidate on each save while live reload is paused | Contradicts the glossary's "paused: saves change the work tree and nothing else", costs a build per save on busy tiles, and adds activation-by-hash state. Revisit only if SC-LATENCY-OPS fails with warm caches and differential materialization. |
| NP-04-4 — a per-tile checkpoint exclude list | Excluded paths would silently vanish from pinned code, and the glossary defines a checkpoint as every file but `.git` and nested components. Over-size captures are refused, naming the largest directories, and the cap error points large files to a resource (07 NP-07-15). Revisit with the differential-materialization benchmark. |
| NP-02-8's Go toolchain in the CI rootfs | Confined builds bind the host GOROOT; NP-15-1's rootfs carries git, ca-certificates, nodejs and python3 only. |
| Refusing `+` in new tile names beyond the two narrow collisions (02, 06, 11, 13, 15; NP-12-11's refusal for non-admins in the next release) | Would turn successful requests into errors for no collision; ruling 4 keeps a one-release warning only. |
| The losing variants in 14 §0.3's table (a separate target picker, a session defaulting to the live reload target, a store-served or `/c/`-gated fetch remote, CompKey-named new stores, events with qualified components, new fields on `POST /restore`, …) | Settled by the owner and rulings 1–21; that table is the full list. |

NP-15-5, NP-15-10 and NP-15-13 were dropped by their author as duplicates of
sibling proposals, and NP-10-11 and NP-10-12 were never used; none is a
rejection.

## 7. From P-numbers to D-numbers

P-numbers are local to this set, and `make test`'s docscheck refuses a
D-number that no `plans/*.md` defines. D-numbers are therefore assigned only
in a milestone exit's records commit ([14-implementation.md](14-implementation.md)
§4.5), never earlier.

**Which P at which exit.** A P-number becomes a D-number at the exit of the
first milestone that builds it in full (§1.1's **D at** column):
- **M1:** P1, P2, P5, P8, P9, P15, P16, P18, P29.
- **M2:** P3, P4, P6, P7, P10–P14, P17, P19–P28.
- **M3:** no P-number is left. M3's own calls (L1–L3) become new entries
  when its design rules on them.

Until its exit a P-number stays in Go comments as `(Pn)` and in the plans as
it is.

**The records commit, per milestone.** The integrator:
1. Collects the **Decision:** hand-offs and gets the owner's ratification of
   the milestone's P-numbers and of the NP ids adopted under them (§3).
   Ratified and owner-confirmed P-numbers need their entry, not a new ruling.
2. Picks the numbers: the next free ones after the highest D-number in use at
   that moment on master **and** on every open branch, never a number another
   branch holds. Today master defines up to D114; the next two numbers are
   held by work on other branches, and cgi's removal takes one more when it
   lands. The floor is therefore the fourth number after D114, recomputed at
   the commit. Changes from O2's hardening branch number in their own
   commits, before this programme if they land first.
3. Writes one entry per P-number, in P order, as a bold-ID bullet in
   [../DECISIONS.md](../DECISIONS.md) with **Why**, **Chosen** and **Not
   chosen**, drawn from 05's text, 04's axis for it, §2's rulings and §6's
   rejections. An adopted NP that refines the P-number is a lettered sub-point
   of its entry (docscheck resolves a lettered cite to its base entry). An
   owner question answered with its default is recorded as "default
   accepted" in the entry it touches; one answered otherwise gets its own
   entry when it is built.
4. In the same commit: rewrites every `(Pn)` in Go comments to its
   D-number; writes it into §1.1's **D at** cell; adds the citations to the
   builder docs and the changelog entry (12 §10.4's draft); updates the status
   lines of [README.md](README.md), 05 §13 and the implemented documents. No
   D-number is cited before the commit that defines it.
5. Runs `make check` (docscheck included), `make integration` with the
   rootfs, the fresh harness and the milestone's manual checklist.
