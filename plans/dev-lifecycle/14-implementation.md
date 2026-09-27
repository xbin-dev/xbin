# 14 — Implementation plan: milestones and work packages for the swarm

> Status: live — the milestones, work packages, waves, file ownership, integration model and risk plan the implementation swarm builds tile deployments from (part of [plans/dev-lifecycle](README.md))

This document turns the design set into work that a swarm of coding agents
can run in parallel without colliding. It owns:
- the milestones and their exit criteria (§1);
- the work packages (WPs), each at most about two days of focused agent work
  (§2);
- the waves, the dependency graph and the file-ownership matrix (§3);
- how the integrator merges, numbers decisions and ships (§4), and how the
  agents work (§5);
- risks, rollback and shipping dark (§6);
- the proof that every surface of [13-surfaces.md](13-surfaces.md) and every
  test of [15-test-plan.md](15-test-plan.md) has an owner (§7).

It adds no mechanism. What a WP builds is specified by
[05-model.md](05-model.md) and the documents that elaborate it:
[06-security.md](06-security.md), [07-runtime.md](07-runtime.md),
[08-data.md](08-data.md), [09-fabric.md](09-fabric.md), [10-ux.md](10-ux.md),
[11-contract.md](11-contract.md) and [12-compat.md](12-compat.md). The spine
([05-model.md](05-model.md), [01-glossary.md](01-glossary.md)) wins over any
passage that disagrees with it; §0.3 lists the settled points and the losing
variants a WP must not build. Terms are [01-glossary.md](01-glossary.md)'s.
Delivery facts (guards, budgets, the harness, the native-swarm precedent) come
from [research/delivery-infra.md](research/delivery-infra.md); the shared
sandbox layer from [research/sandbox-visibility.md](research/sandbox-visibility.md).
Every `file:line` below was re-checked in this worktree.

**Reading a card.** The heading gives the WP's id, slug, size, wave (§3.1;
"1.2" is M1's second wave) and merge-order dependencies ("after"). A WP may
start earlier against declared interfaces, but merges after them. A letter
suffix (WP-14a, WP-14b) marks a card split for size; the halves own disjoint
files. Then:
- **Scope**, with the sections that specify it;
- **Owns**: the files it may create or edit, exclusively within its wave
  (§3.3);
- **Tests**: named as in [15-test-plan.md](15-test-plan.md); `=` joins an
  [06-security.md](06-security.md) alias to its name; "(new)" marks a name
  15-test-plan adds (§7.2);
- **Links**: the interfaces it provides or consumes (→ the WPs that use
  them), the docs it hands over, and the [12-compat.md](12-compat.md) proof
  obligations (PO-n) it keeps green.

Sizes: **S** is about half a day, **M** one day, **L** two days of focused
agent work. Every card also carries the standard clauses of §2.1.

## 0. Before the first work package

### 0.1 Gates

| Gate | Holds when | State on 2026-09-27 |
|---|---|---|
| PRE-1 | The baseline is master: D112 is landed, and D113 is designed ([plans/tile-sandboxes.md](../tile-sandboxes.md)) | **Holds.** The design set is written against current master. The integration branch is cut from master as it stands when wave 0.1 starts, and the integrator re-measures line ranges and budgets then (PRE-5). |
| PRE-2 | Phase 0: the design set is committed as plans only, with `make check` green | Open. [README.md](README.md), [04-options.md](04-options.md) and [06-security.md](06-security.md) link `16-open-questions.md`, which must exist first (`TestRelativeLinksResolve`, `internal/docscheck/docscheck_test.go:114`) |
| PRE-3 | The owner has ruled on each open item of §0.2 before the wave it blocks, or accepted the default this plan builds | Open |
| PRE-4 | An integrator is named (§4.2) | Open |
| PRE-5 | The branch that removes cgi from xbin (`remove-cgi`) is in master before wave 0.1. This set assumes cgi no longer exists. The branch edits files M0 splits or owns: `internal/runner/{runner,alwayson}.go`, `cmd/bx/main.go`, `internal/proxy/proxy.go`, `internal/registry/registry.go`, `internal/confine/guard_test.go`, `internal/server/openapi.go`, `workspace-template/tiles/admin/tabs/runtime.js`, `workspace-template/shell/shell-kit.js`, `workspace-template/AGENTS.md`, `docs/{protocol,bx,compat,elements,maintenance,sdk}.md` and `docs/overview/{03-components,16-extending}.md` | Open: one commit (3cc44d7), not yet an ancestor of master. After it lands, the integrator re-measures WP-00's line ranges and ratchets, and refreshes the docs WPs' line references. |

### 0.2 Rulings that block a wave

| # | Ruling | Blocks | Built until ruled |
|---|---|---|---|
| R-1 | **Ruled** by rule C2 ([05-model.md](05-model.md) §8): non-primary activity travels only in the `deployments` type | nothing | C2 (§0.3) |
| R-2 | **Ruled** by P24 (owner, 2026-09-27): the terminal's API dropdown picks the target, defaulting to the primary, never offering a protected primary, then falling to the live reload target, then to "API off" | nothing | P24 (§0.3) |
| R-3 | The offload fix as a P5 exception ([08-data.md](08-data.md) §9.4, its Divergence 8) | WP-45 | not built |
| R-4 | The checkpoint purge route. The spine makes purging a tile manager's act ([05-model.md](05-model.md) §2, §10), but [11-contract.md](11-contract.md) defines no route for it | WP-66 | not built; T20's purge tests wait |
| R-5 | **Ruled for v1** by P3 and P23: no edge-policy value widens a non-primary deployment past the read clamp, so a `full` value for roles that mean spend ([09-fabric.md](09-fabric.md) NP-09-14) is a later rung | nothing in v1 | not built; the docs say non-primary deployments lose LLM completions |
| R-6 | **Ruled** by [05-model.md](05-model.md) §7: in origins mode (D95) each deployment has its own origin label, keyed by name; `main` keeps today's | nothing | WP-38 builds it in M2 |
| R-7 | A git-bearing rootfs in CI (NP-15-1), and the previous release's binary for downgrade tests (NP-15-4) | confined tests in CI from M1; the M2 exit | the integrator adds both to `ci.yml`, in M0 and in M2 |
| R-8 | M3's calls: `match`'s fallback (NP-09-15 or NP-04-7), force pushes on a tracked branch, creating a deployment by push ([04-options.md](04-options.md) open questions 2–3) | M3 | M3 doesn't start |
| R-9 | The ship-dark switch's default in the first release that carries M1 (NP-14-5) | that release | on |

Each milestone's records commit (§4.5) also needs the owner's ratification of
the P-numbers it implements. WPs build the proposals as written.

### 0.3 Settled points: what gets built

The design documents disagreed on these points. Each is settled by the owner
or by the spine. The middle column names the variant that some passages of
the other documents may still carry: no WP builds it, and a WP that meets it
follows the right-hand column.

| Topic | Not built (the losing variant) | Built |
|---|---|---|
| Plane package | `internal/deploy` (15-test-plan's paths); 13-surfaces forbids the name (the installer's `deploy/`) | `internal/deployments`; test names unchanged |
| `bx` files | one `cmd/bx/deploy.go` (13-surfaces) | `livereload.go`, `deploy.go`, `deployment.go`, plus `deployclient.go` for shared plumbing |
| Harness fixtures and passes | `apps/deploy-demo`, passes `tileDeployments` and `tileDeploymentRefusals` ([10-ux.md](10-ux.md) §14) | `apps/reloady`, `apps/deployy`, passes `livereload` and `deployments`; 10-ux's refusal assertions join `livereload` step 5 and `deployments` step 7; 10-ux's `testApi` member names are kept |
| `testApi` and the panel | `testApi` in `web/bx-frame.js`; the panel in `web/bx-deployments.js` | `web/frame-testapi.js`; `web/bx-deploy.js` defining `<bx-deployments>` |
| Runner funnel | `Ensure(ctx, c)` renamed to `Ensure(ctx, c, dep)` (07-runtime §1.2), which edits every caller at once | `Ensure` kept as the primary's funnel; `EnsureDeployment`, `TrackDeployment` and `ChangedDeployment` beside it (05-model §7); callers move with the WPs that own them, and `TestEnsureCallSitesPassPrimary` pins the rest |
| SDK | declared by the contracts role (13-surfaces §5) | WP-59, in M2, when xbind first sets what the SDK reads |
| Store keys | `<CompKey>` names for the new stores (11-contract §10) | `<TileKey>`, a 128-bit hash of the tile path (05-model §3), for `data/deployments`, `data/checkpoints` and `.xbin/deploy` only; existing stores keep their keys |
| Non-main backend log | `.xbin/log/.deployments/<CK>/<d>.log` (08-data §2) | `.xbin/deploy/<TileKey>/d/<name>/backend.log`; D113 state follows it (`…/d/<name>/sbx/`) |
| D112 registry rows | `backend:<CompKey>:<name>:g<gen>` (13-surfaces §4.4), `backend:<CompKey>+<name>:g<gen>` (10-ux §7); `Deployment` on every entry of a tile with a record; an `Entry.Checkpoint` field | non-main entries: ID `backend+<name>:<CompKey>:g<gen>`, `Entry.Deployment` set; `main`'s rows byte-identical to today's (`backend:<CompKey>:g<gen>`, `internal/runner/sbx.go:63`, no `Deployment`) even when the tile has a record; no `Checkpoint` field (05-model §12) |
| Events | non-primary activity on `reload` and `build-*` with a qualified `component` (11-contract §3.2) | rule C2 (05-model §8): non-primary activity never rides `reload`, `build-*`, `status` or notify, only the `deployments` type, which carries `deployment`. No qualified `component` on an old type, ever: not for deploys, reassignment or lifecycle. A successful swap on the primary emits one `reload` for the bare component, only when the primary's code changed. A deploy's phases and failures ride `deployments` |
| Event audience | ops `record` and `deploy` to readers (11-contract §3.4); every new event to readers (13-surfaces) | 05-model §8: facts about the primary go to the tile's readers, as today. Anything naming a non-primary deployment (its name, builds, compiler output, status, would-notify lines, data ops) goes only to admins, humans with ≥ `write` on the tile (their current level), and that deployment's own principals plus the tile's terminal and agent sessions. The primary's frame token is not an own principal for these facts. Other tiles get none of it |
| What readers see | the full state minus writers-only fields (11-contract §1.3); a 403 (10-ux §4.1) | 05-model §8: `GET /api/xbin/deployments` gives a reader primary-scoped facts only (live reload state as it concerns the primary, the primary's checkpoint and deploy state, protection), with no non-primary name and no count that reveals one. `/components` adds only the primary summary. 11-contract owns the shape; the harness asserts the filtered view, not a 403 |
| Session target | the live reload target by default, and a protected primary targetable (11-contract §7.4 and DIV-6, 09-fabric §2.3); a separate target picker (10-ux §2.3) | P24: the terminal's existing tile-API select, with one entry per deployment the user may reach. The default is the primary. A protected primary is never offered, and a request naming it is refused; the default then falls to the live reload target, and when neither exists, to "API off". A session's target is fixed for its life. A tile with no record, or only an unprotected `main`, keeps today's two options |
| `+` in tile names | refused now for non-admins or for every creator (02, 06, 11, 13, 15); refused for non-admins in the next release (12-compat NP-12-11) | two narrow refusals, for every creator (05-model §7): no tile at `<P>+<N>` while `P` has deployment `N`, and no deployment `N` on `P` while a component exists at `<P>+<N>`. Any other new tile name containing `+` gets a warning for one release and is never refused. A qualified URL resolves only for a tile with a record, and only after today's resolution fails |
| `bx` exit codes | 6 for refusals (10-ux NP-10-7) | 11-contract §9.1: 3 refused, 4 not confirmed (`--yes` needed without a TTY), 5 still running, 6 no tile deployments |
| The checkpoint fetch remote | M2 (02-goals' milestone table); gated like `/c/` reads or by `requireCodeRead` (06, 13, 15); served from the store | M1: the downgrade checklist (12-compat §5.5 step 2) needs it once a primary can be pinned. Served from a separate, xbind-owned view repository per tile (05-model §3) that holds only `refs/heads/deploy/<name>` for pinned deployments, plus `HEAD` naming the primary's ref; each ref is the checkpoint's git view (minus the tile's ignored paths); no `refs/xbin/*` is ever advertised. Gate: the tile's own terminal and agent sessions, and humans with ≥ `write` (current level) |
| Checkpoint `xbin.json` and `scope.json` reads | a confined `git cat-file` (06-security ledger L5) | `fsutil.OpenBeneath` (`internal/fsutil/beneath.go:34`) on the materialized tree, for both files; `scope.json` validated (the resource-name charset) before anything is provisioned; pinned by `TestCheckpointManifestReadNoFollow` |
| Resource declarations | provisioned tile-wide from the work tree (07-runtime §6, 08-data) | P22: every deployment provisions what its own code declares, in its own namespace; a pinned primary provisions from its checkpoint's `scope.json`. Per-deployment limits default to the tile's, are set by tile managers, and never exceed the tile's ceilings |
| Inbound surface and `/components` | tile-level fields from the work tree, with a protected primary as the only exception (07-runtime §5.1, 06-security NP-06-4) | 05-model §6: `template`, `exposes`, `expose.roles`, `provides` and `chrome` follow the primary's code: the work tree while the primary is the live reload target, its checkpoint while pinned. A non-primary document is never chrome. `/components`' `runtime`, `hasIndex` and `native` describe the primary's code; `manifestError`, `roles`, `uses` and `deps` stay the work tree's |
| `deps/` and escaping symlinks in a checkpoint | a 404 for `deps/` too (07-runtime §4.1); following the host symlink (today's legacy plane, `internal/server/static.go:182-219`) | never followed on disk. The static plane resolves `deps/<name>/…` by re-dispatching the path to the target tile's `/c/` plane (its primary); any other symlink that leaves the checkpoint answers 404 (`ErrEscapes`). Go builds get the other tile's pinned primary checkpoint (or its work tree) bound over its `go.work` directory |
| Materialized trees | write bits dropped from directories too | directories 0755, files 0444 or 0555 (the exec bit kept), so `rm -rf .xbin` keeps working; read-only to sandboxes by bind flags, not host modes |
| Nested components in a checkpoint bind | no bind inside a checkpoint bind (06-security T18.3) | mountpoints under the canonical path are created without traversing symlinks (openat2 with `RESOLVE_NO_SYMLINKS \| RESOLVE_BENEATH` in the sandbox init); a nested component's own code is bound after the checkpoint bind |
| The net edge's `inherit` | host networking or a provider splice inherited with the tile's authority | the tile's relay policy only. A tile whose `net` resolves to host sharing (the `host` builtin, or a set whose rules say host: `Broker.NetHostShare`, `internal/broker/netfn.go:196`), or a net slot bound to a provider, gives its non-primary deployments no egress (`block`, P23), with the reason shown. `gpu:*` defaults to `block`; other capability grants to `inherit` |
| Backup and restore wire | new fields on `POST /restore` and other strictly decoded bodies | no field added to any existing body; `POST /restore` unchanged. Per-deployment backup, restore and seed use new endpoints under `/api/xbin/deployments/…`, owned by 11-contract |
| Manager acts | terminal and agent tokens of managers; element principals whose tile holds `xbin` or `xbin:users` | a human session: `p.Component == ""` and (`IsAdmin` or `mayManageTile`; `internal/auth/auth.go:92`, `internal/broker/orgsapi.go:427`). No element principal passes the manager gate in the deployments plane, whatever grants its tile holds |
| Operations onto a protected primary | a request that names no reviewed checkpoint | `checkpoint` on deploy and roll back; `expect` on promote, reload now and reassignment; otherwise 400. The check is a compare-and-set on the record's `seq`, with authority re-checked at commit |
| Dry runs and diffs | a capture into a store created for the purpose | never a checkpoint store on a zero-state tile: dry runs of pause live reload and add deployment compute the impact without capturing; a work-tree diff on a tile without a record answers 409 and captures nothing (11-contract §1.11's pick of the two allowed answers). The store exists only after a committed opt-in |
| xbind's API for non-primary principals | the self rule alone | P26: every `/api/xbin/*` route is classified deployment-scoped, primary-only or neutral; unclassified routes refuse non-primary principals, reads included |
| `TestOlderBinaryIgnoresDeploymentFiles` | one name for two tests (15-test-plan §9) | 12-compat PO-9's unit test keeps the name (WP-49); the downgrade tests keep theirs (WP-63) |
| Legacy fixture | new assertions inside both fixtures (12-compat §6.1) | new test functions in new files reuse the fixtures' helpers (NP-14-9) |

### 0.4 Defects the plan stays clear of, without fixing them

| Defect | Evidence | How the plan stays clear |
|---|---|---|
| `ScopeKey` and `CompKey` collide | side finding #7; 08-data NP-08-13; 06-security S3 | non-main keys are injective (WP-39); the new stores use `<TileKey>`, and the record carries and verifies path, owner ref and creation stamp (WP-13) |
| `chrome` is tile-editable | 06-security S1 | the inbound surface, `chrome` included, follows the primary's code, so a pinned primary's comes from its checkpoint (WP-18); a zero-state tile keeps today's behaviour |
| Restore trusts the archive | 06-security S2 | the new archive sections are validated (WP-23, WP-44a) |
| Backups race symlinks | side finding #5 | every new walk opens through `OpenBeneath` (WP-42, WP-44a); `TestNoFollowingHostWalks` guards it (WP-04) |
| Non-bus events reach everyone | side finding #2 | every new event type and field is filtered from day one (WP-21, WP-48) |

Side finding #1 is closed by PRE-5's branch. Two more are fixed in scope:
`bx logs` reading `.xbin/log` (#22, WP-26), and `internal/sandbox`'s
integration tests never running (#24, WP-04).

## 1. Milestones

### M0 — prep

**Goal.** Before any feature code: split the files at their budgets, give the
runner and the watcher a test seam, pin today's behaviour in golden tests,
measure the latency budgets, and declare every shared interface, route and
harness pass once.

**Work packages.** M0a, on master: WP-00 to WP-04 (WP-01 as WP-01a and
WP-01b), WP-S0. M0b, on the integration branch: WP-05 to WP-08.

**Exit criteria.**
1. `make check` green on master after M0a, and on the integration branch
   after M0b; `make integration` green with `XBIN_TEST_ROOTFS`.
2. The zero-state goldens of [15-test-plan.md](15-test-plan.md) §2.7 and seam
   rows 1–15 green against today's code; `go test -race ./internal/runner/`
   clean.
3. The latency baseline in WP-04's commit body. A budget that master already
   misses is reported to the owner, never loosened.
4. Every route of [11-contract.md](11-contract.md) §1.13, and every §8
   addition to an existing route, reserved: new routes mounted as 501 stubs,
   all of them with their openapi and protocol rows marked reserved; both
   planned passes registered, printing SKIP.
5. The ratchets lowered as [13-surfaces.md](13-surfaces.md) §2 lists, no
   budget raised, and a fresh-seed harness run with no new FAIL against
   master (A/B).

**Afterwards.** Nothing changes for any user or tile. The reserved routes
exist only on the integration branch.

### M1 — pause live reload

**Goal.** Flow A, rolling back `main`, and flow H's fetch
([05-model.md](05-model.md) §14), on the checkpoint core, while `main` is
every tile's only deployment.

**Work packages.** WP-10 to WP-29 (with WP-14a/b, WP-16a/b and WP-23b),
WP-S1.

**Exit criteria.**
1. These criteria of [02-goals.md](02-goals.md) §5 hold: SC-ZERO (the
   regression gate from here on), SC-OPT-OUT, SC-LATENCY-DEFAULT (M1 half),
   SC-LIVE-RELOAD-PAUSE (100 runs per runtime, `XBIN_TEST_FULL=1`),
   SC-LATENCY-OPS (M1 rows), SC-ROLLBACK (`main`), SC-PINNED,
   SC-SAFE-DEPLOY, SC-AGENT-BX (its M1 variant), SC-WORKTREE and SC-AUDIT
   (M1 operations), SC-FAIL-CLOSED (pausing live reload).
2. Every M1 row of [15-test-plan.md](15-test-plan.md) green under `make
   check` and `make integration` (the isolated daemon, a git-bearing rootfs).
3. The harness gate on a fresh seed, `run.sh --keep livereload viewAs agentTab
   sandboxes reloadFocus`, then a full run with no new FAIL against M0.
4. Items 1 (flows A, D and H), 2, 4, 5 and 6 of the manual checklist
   (15-test-plan §10.3), on an isolated xbind with auth on.
5. WP-29's docs merged, and the records commit (§4.5).

**Afterwards.** On any static tile, and on backend tiles of an xbind started
with `--isolate`, a person or an agent can:
- pause live reload from the terminal window or with `bx live-reload pause`,
  and keep editing while every viewer keeps the pinned checkpoint, its
  inbound surface included;
- see how many files moved since, reload now, and resume live reload, which
  returns the tile to the zero state;
- roll `main` back to any checkpoint in its deploy log (`bx rollback`);
- run `git fetch xbin-deploy` in the tile's terminal, and branch from the git
  view of exactly what `main` runs.

A transfer keeps a pinned primary pinned, and a tile re-created at a removed
tile's path starts in the zero state.

### M2 — tile deployments

**Goal.** Flows B–H and scenarios S-1 to S-10 except S-8
([02-goals.md](02-goals.md) §3): non-primary deployments with their own URL,
data, vault and dormant registrations; the edge policy; promotion,
reassignment and protection.

**Work packages.** WP-30 to WP-66 (with WP-44a/b, WP-53a/b and WP-56a/b);
WP-S2 to WP-S4; WP-S5 if D113 is built first.

**Exit criteria.**
1. SC-INBOUND, SC-DATA, SC-CLAMP, SC-DORMANT, SC-EVENTS, SC-PROTECT,
   SC-PRIMARY-FIRST, SC-AGENT-BX (all seven steps), SC-ROLLBACK (every
   deployment), SC-LATENCY-OPS (M2 rows), SC-LATENCY-DEFAULT (live reload
   attached to `dev`), SC-WORKTREE and SC-FAIL-CLOSED (M2 operations) hold;
   SC-ZERO and SC-OPT-OUT stay green.
2. Every M2 row of 15-test-plan green, the downgrade tests against the
   previous release included.
3. The harness gate of 15-test-plan §7.1 (`run.sh --keep livereload
   deployments agentTab sandboxes viewAs`), then a full run.
4. The whole manual checklist, and a non-main volume mounted on the QA box
   under the installer's AppArmor rule ([08-data.md](08-data.md) §3.5).
5. WP-64 and WP-65 merged, and the records commit with 12-compat §10.4's
   changelog entry. No migration note unless a 12-compat §10.2 tripwire was
   crossed.

Gated WPs (WP-45, WP-66) never hold the exit: without them M2 ships without
the offload fix, and without purging (NP-14-12).

**Afterwards.** A person or an agent can:
- add `dev`, with empty data and a vault holding key names only, attach live
  reload to it, and open `/c/<tile>+dev/`;
- point a terminal or agent session at `dev` from the terminal's API
  dropdown, so its calls and `bx` commands reach `dev`;
- promote `dev → main` after reading the diff, and roll back from the deploy
  log;
- run a dormant cron job now, at terminal level;
- follow `dev`'s status, logs and "would notify" list in the Deployments
  panel, or with `bx deployment`.

A tile manager, in a human session, can also seed `dev`, copy vault values,
reassign the primary (flow F), protect it, set each edge's policy (`read`,
`inherit` or `block`, as the edge's kind allows), set per-deployment limits,
and turn deliveries on.

### M3 — later rungs

**Goal.** The tracked-branch and deploy-remote feeds on the same core, and
edge policy `match` (P1, P3). **Work packages:** WP-70 to WP-75 and WP-S6,
outlined in §2.6; each gets a full card once R-8 is ruled. **Exit:** the M3
rows of 15-test-plan, scenario S-8, WP-75's docs, the records commit.
**Afterwards:** a deployment can follow a branch of the tile's repository, a
push to the deploy remote deploys, and an edge can reach the provider's
same-named deployment.

## 2. Work packages

### 2.1 Standard clauses

Every card includes these; a card states only what differs.

1. **Place.** A branch `dl/<slug>` from the integration branch's head, in its
   own worktree (§4.1); M0a WPs branch from master. Never in
   `/home/magik6k/buxon`.
2. **Owned files only.** A seam needed elsewhere is a contract amendment
   (§4.4): stop and ask the integrator.
3. **Never edited by a WP:** `docs/changelog.md`, `docs/changes/*`,
   `plans/DECISIONS.md`, plan status lines, `Makefile`,
   `.github/workflows/ci.yml`; `hack/size-budget.txt` after WP-00; the
   `shots.js` requires and `PASSES` entries, and `seed.sh`, after WP-08;
   `internal/boot/boot.go` outside WP-06, WP-07 and WP-30; openapi rows and
   protocol.md API-fence rows outside WP-07, except that the docs WPs
   (WP-29, WP-64) edit protocol.md's prose and drop its reserved markers;
   `examples/`, `test/integration_test.go`, both legacy allowed lists,
   `workspace-template/shell/bx-shell.js`,
   `workspace-template/tiles/admin/admin.js`, `internal/builtins/updates.go`,
   `registry.WorkspaceManifest`, `builtin-templates/agent/`.
4. **Budgets.** New logic goes in new files; a budgeted or near-cap file takes
   seams only (a call, a field, a `case`). No budget is raised: a file that
   won't fit gets a verbatim split from the integrator.
5. **Citations.** No new D-number anywhere; P-numbers only in Go comments, as
   `(P9)`; no `plans/` path in `docs/`, `web/` or `workspace-template/`
   (`TestEmbeddedAssets`).
6. **Security.** D78: every tool run on tile data or the checkpoint store goes
   through `internal/confine`, no `exec.Command` without `// exec-ok:`, and
   under isolation a sandbox failure is an error. `plans/auth.md` holds:
   inbound `X-XBin-*` stripped, element principals default-deny, the owner is
   admin. 06-security C5: no `os.Open`, `os.Stat`, `os.Chmod`, `os.Chown` or
   symlink-following walk inside a work tree, resource mount, materialized
   tree or quarantine (`TestNoFollowingHostWalks` guards it). Manager acts
   pass only the human-session gate of §0.3.
7. **Wire.** Handlers use `server.WriteError`, `WriteOK` and `DecodeJSON`,
   with [11-contract.md](11-contract.md) §1.14's texts; no existing request
   body gains a field (11-contract §0.6).
8. **Zero state.** The goldens of WP-01a to WP-03 stay green, unchanged.
   Changing one is a compat change: the integrator's sign-off and a
   [12-compat.md](12-compat.md) entry.
9. **Tests.** New files, one owner each; the doc comment starts with the IDs
   covered (`// covers P9 T11 — …`, 15-test-plan §1).
10. **Done when** `make check` is green; `make integration` is green when
    runner, sandbox, cgroup, VM, broker, confine, proxy or term code changed;
    the card's harness passes are green on a fresh seed, every SKIP
    explained; commits are area-prefixed and the last body carries
    **Tests:** (run, and not run with the reason), **Docs:** (text for the
    docs WPs), **Changelog:** and **Decision:** (for the integrator).

### 2.2 M0 — prep

#### WP-00 prep-split · S · wave 0.1 · on master
- **Scope.** 13-surfaces §2's verbatim moves, a commit each, ratchets lowered:
  `build` (`internal/runner/runner.go:363-411`) → `build.go`; `sandboxable`,
  `sandboxCmd` (`:625-726`) → `sandboxcmd.go`; `stop` … `reaper`
  (`:728-831`) → `states.go`; `hostForward`, `sandboxEnv`
  (`internal/term/term.go:715-795`) → `sandboxenv.go`; `cmdStatus` …
  `cmdLogs` (`cmd/bx/main.go:346-530`) → `status.go`; the `testApi()` body
  (`web/bx-frame.js:560-601`) → `web/frame-testapi.js`, with a delegate.
  The ranges are measured before PRE-5's branch; re-measure after it lands.
- **Owns.** `internal/runner/{runner,build,sandboxcmd,states}.go`,
  `internal/term/{term,sandboxenv}.go`, `cmd/bx/{main,status}.go`,
  `web/{bx-frame,frame-testapi}.js`, `hack/size-budget.txt`.
- **Tests.** `make check`; `git diff --color-moved` shows moved blocks only;
  harness `windows agentTab reloadFocus`. No behaviour changes.

#### WP-01a prep-seam · L · wave 0.2 · after WP-00 · on master
- **Scope** (15-test-plan §2.2–§2.4). The engine seam, `reapOnce()`, the fake
  engine and rows 1–15; the runner goldens. `idleReap` stays 30 minutes
  (`internal/runner/runner.go:46`).
- **Owns.** `internal/runner/{runner,engine,states}.go`;
  `internal/runner/{fake_engine,statemachine,zerostate}_test.go`.
- **Tests.** Rows 1–15, `TestZeroStateKeys` (runner half), `go test -race
  ./internal/runner/`.
- **Links.** The engine → WP-16a, WP-16b, WP-33, WP-34. PO-2, PO-12.

#### WP-01b prep-launchspec · M · wave 0.2 · after WP-00 · on master
- **Scope** (15-test-plan §2.6). `sandboxCmd` split into a pure `launchSpec`
  and `sandbox.Launch`, keeping `sandboxCmd`'s signature so `runner.go` is
  untouched; the watcher tables.
- **Owns.** `internal/runner/sandboxcmd.go`,
  `internal/runner/launchspec_test.go`, `internal/watch/watch_test.go`,
  `internal/boot/watch_nesting_test.go`.
- **Tests.** `TestZeroStateLaunchSpec`, `TestIgnoreRules`,
  `TestChangedComponentsNesting`.
- **Links.** `launchSpec` → WP-17, WP-35. PO-11, PO-12.

#### WP-02 goldens-wire · L · wave 0.2 · after WP-00 · on master
- **Scope** (15-test-plan §2.7; NP-15-8). Hand-maintained goldens for URLs,
  listings, events, tokens, headers, session env and `bx`, run-to-run values
  masked.
- **Owns (new).** `internal/server/{zerostate,deployevents}_test.go`,
  `internal/boot/{api_zerostate,deploystate}_test.go`,
  `internal/auth/deployment_test.go`, `internal/proxy/deploy_test.go`,
  `internal/term/deploy_test.go`, `cmd/bx/deploy_test.go`.
- **Tests.** `TestZeroStateDocumentUnchanged` (three asset modes, PO-1's `+`
  matrix), `TestZeroStateComponentsEntry`, `TestZeroStateListings`,
  `TestEventBytesZeroState`, `TestZeroStateTokens`, `TestIdentifyZeroState`,
  `TestSessionEnvZeroState`, `TestBxTodayInvocationsUnchanged`,
  `TestZeroStateCreatesNoDeploymentFiles`.
- **Links.** PO-1, PO-3 to PO-7, PO-13, PO-14.

#### WP-03 goldens-data · M · wave 0.2 · after WP-00 · on master
- **Scope.** Goldens for backend env, storage keys, backups, confine binds
  and the sandbox registry; the zero-state lifecycle test; 12-compat §6.1's
  "no deployment state" checks on the aged workspace, as new functions that
  reuse the fixtures' helpers (NP-14-9).
- **Owns (new).** `internal/broker/{deploy_env,deploy_backup}_test.go`,
  `internal/confine/bind_test.go`, `internal/sbx/deploy_test.go`,
  `test/{livereload,legacy_nodeploy}_test.go`,
  `internal/boot/legacy_nodeploy_test.go`.
- **Tests.** `TestZeroStateKeys` (broker half), `TestZeroStateBackendEnv`,
  `TestZeroStateBackupMembers`, `TestConfineBindDestinationDefault` (every
  `confine.Cmd` user's bind list), `TestEntryJSONZeroState`,
  `TestZeroStateLifecycleFiles`.
- **Links.** PO-2, PO-3, PO-7 to PO-9, PO-11.

#### WP-04 latency-guards · M · wave 0.2 · on master
- **Scope.** The budgets of `plans/dev-flow.md:102-105` as tests, in
  15-test-plan §8's two tiers (NP-15-11); `TestIntegrationPackagesListed`
  beside `TestNoDirectExec` (NP-15-6); rule C5's Go-code guard beside them,
  armed before any feature code (its patterns and scope are 15-test-plan's;
  a package that doesn't exist yet is skipped).
- **Owns.** `test/latency_test.go`,
  `internal/confine/{integration_list,nofollow}_test.go`.
- **Tests.** `TestLatencyStaticSaveToReload`, `TestLatencyGoSaveToServing`,
  `TestIntegrationPackagesListed`, `TestNoFollowingHostWalks`.
- **Links.** The integrator adds `./internal/sandbox/` to `make integration`
  (`Makefile:110-117`) in this merge. The measured numbers go in the commit
  body.

#### WP-05 contracts-types · L · wave 0.3 · after M0a
- **Scope** (13-surfaces §5 "Contracts"; 07-runtime §1.2). Each shared
  declaration once, with today's behaviour behind it:
  - `util.DeploymentNameOK` and `util.TileKey` (05-model §3);
  - `events.Event.Deployment`, `auth.Principal.Deployment`;
  - the registry's deployment fields (`WorkTree`, `Kept`), its
    `PinnedPrimary` hook (07-runtime §5.1; nil = today's scan) and its
    `ScopeResources` hook (nil = today's `scope.json` map; wave 1.1 deleted
    it: WP-18 carries the checkpoint's `scope.json` in `PinnedCode.Scope`);
  - the terminal manager's has-record hook;
  - 07-runtime §1.2's runner hooks and helpers in
    `internal/runner/deploy.go` (`CodeFor`, `EnvFor` with its `ResBind`
    remap, the rest), with a stub `Deploy`, the additive names of §0.3's
    runner-funnel row, and a `Runner.RootsInUse()` stub answering nil;
  - `server.Policy`'s `CodeRoot`, `HasDeployment` and `Addressable`
    (`internal/server/policy.go:18-43`);
  - in `internal/broker/deployhooks.go`, embedded in `Broker` by one line in
    `broker.go`: the tile-life hooks (rewrite a record's owner ref on
    transfer, reset a path's deployment state on creation, list a path's
    deployment leftovers), each nil = today, and
    `func (b *Broker) MayManageDeployments(p auth.Principal, tile string) bool`,
    exactly §0.3's manager gate: `p.Component == "" && (b.IsAdmin(p) ||
    b.mayManageTile(p, tile))` (`internal/broker/broker.go:466`,
    `internal/broker/orgsapi.go:427`).

  No field or signature that `boot.go` uses changes.
- **Owns.** `internal/util/util.go`, `internal/events/events.go`,
  `internal/auth/auth.go`, `internal/registry/registry.go`,
  `internal/term/term.go` (one field),
  `internal/runner/{runner,deploy,build,sandboxcmd,env,states}.go`,
  `internal/server/policy.go`, `internal/broker/{broker,deployhooks}.go`,
  `internal/server/policy_zerostate_test.go`.
- **Tests.** `TestNoopPolicyZeroState`; every golden unchanged.
- **Links.** Every declaration M1 consumes; the manager gate → WP-14a,
  WP-42, WP-43, WP-47, WP-49, WP-53a. PO-1 to PO-15 hold: nothing reads the
  new fields yet.

#### WP-06 contracts-wiring · M · wave 0.4 · after WP-05, WP-07
- **Scope.** `internal/deployments/plane.go`: the plane in the D63 style,
  with every method boot, the runner, the broker, the server and the terminal
  manager call, answering the zero state. `boot.go` installs the hooks
  (the broker's tile-life hooks and the registry's `ScopeResources`
  included; wave 1.1 deleted that hook with WP-18), points `OnGrantChange` at `run.ChangedTile`
  (`internal/boot/boot.go:508-512`), and hands the plane to `watchLoop` and
  `registerDeploymentsAPI`. The ship-dark switch (NP-14-5), a tagged `Config`
  field on the `--tile-assets` precedent (`internal/boot/config.go:50`), with
  `docs/config.md` regenerated. Then `boot.go` closes to feature WPs
  (NP-14-2).
- **Owns.** `internal/deployments/plane.go`,
  `internal/boot/{boot,serve,config,deployments}.go`, `docs/config.md`.
- **Tests.** `TestConfigDoc` (`internal/boot/config_doc_test.go:17`);
  `TestStepsOrder` if a boot step is added (none is planned).
- **Links.** PO-12: the zero-state path never reads the switch.

#### WP-07 contracts-routes · M · wave 0.3 · after M0a
- **Scope.** Every route of 11-contract §1.13, the per-deployment backup,
  restore and seed endpoints included, as a `RegisterAPI` literal in
  `internal/boot/deployments.go`, answering 501 through `server.WriteError`,
  with its `internal/server/openapi.go` row (§1.15's capability phrases,
  added to `apiInfo`) and its column-0 protocol.md row. Also 11-contract §8's
  additions to existing routes: each openapi row gains its
  `queryParam("deployment", …)` or field note, and each protocol.md fence row
  its `&deployment=<name>` or field note, for `/tile-status`, `/logs`,
  `/frame-token`, `/term/sessions`, `/term/sessions/{id}/restart`,
  `/ws/term`, `/sandboxes`, `/cron/jobs`, `/bus/subscriptions` and
  `/vault/<component>` (queries), and `/backends`, `/runtime`, `/components`
  and `/whoami` (fields). Everything is marked reserved until its owning WP
  lands (NP-14-4); one line in `stepServer` registers the new routes.
  Today's rows: `docs/protocol.md:500`, `:528`, `:550`, `:601`, `:694`;
  `internal/server/openapi.go:103-104`, `:158-160`, `:168-173`.
- **Owns.** `internal/boot/deployments.go`, `internal/server/openapi.go`,
  `docs/protocol.md` (API-fence rows), `internal/boot/boot.go` (one line).
- **Tests.** `TestRouteInventory` (`internal/apicheck/apicheck_test.go:244`),
  `TestOpenAPISpec` (`internal/server/openapi_test.go:9`), with rows for the
  new parameters.
- **Links.** PO-14. No existing request body gains a field (§0.3).

#### WP-08 harness-infra · M · wave 0.3 · after M0a
- **Scope** (15-test-plan §7.1). The `livereload` and `deployments` passes
  registered (a `require` on an existing line, names in `PASSES` at
  `hack/ui-harness/shots.js:845-849`) as stubs printing `SKIP not
  implemented yet`; the fixtures `apps/reloady` and `apps/deployy` and their
  user entries in `seed.sh`; `HARNESS_ISOLATE=1` in `run.sh` (NP-15-3);
  `testApi().layouts` for `agentTab`, and label-based `hack/menus.test.mjs`
  (NP-15-12).
- **Owns.** `hack/ui-harness/shots.js`, `hack/ui-harness/{seed,run}.sh`,
  `hack/ui-harness/passes/{livereload,deployments,agenttab}.js`,
  `web/frame-testapi.js`, `hack/menus.test.mjs`.
- **Tests.** A fresh-seed full run with no new FAIL (A/B); `make js-test`;
  `make shellcheck`.

### 2.3 M1 — pause live reload

#### WP-10 checkpoint-capture · L · wave 1.1 · after WP-05
- **Scope** (07-runtime §2.1–§2.5; 06-security T1, T2, ledger L1–L2). The
  `Store` skeleton with every method WP-11 and WP-12 fill; the lazy,
  confined store at `data/checkpoints/<TileKey>.git`, created only by a
  committed opt-in, never by a dry run or a diff; capture into a quarantine,
  the pure admission check and the retention commit (07-runtime §2.2); the
  checkpoint's git view, the tile's ignore rules evaluated in confine at
  capture and kept in the store with the checkpoint, never advertised, with
  the work-tree HEAD named in its commit message (05-model §3); ids; the
  per-tile mutex and rate limit.
- **Owns.** `internal/checkpoint/{store,capture,admit}.go`,
  `hostile_linux_test.go`, unit tests.
- **Tests.** `TestCheckpointContent`, `TestCheckpointIDs`,
  `TestCheckpointIncremental`, `TestCaptureCapsRefuseHugeTree`,
  `TestStoreUsesConfineOnly`, `TestCaptureDirectModeHardening`,
  `TestCapturePathNamesNeverArgv`, `TestAdmissionRefusesBadTrees`,
  `TestCheckpointRateLimit`, `TestGitViewExcludesIgnored`; confined
  `TestCaptureIgnoresTileGitConfig`, `TestCheckpointStorePrivate`.
- **Links.** `Store` → WP-11, WP-12, WP-14b, WP-23. PO-7 (the store is lazy).
  The integrator adds `./internal/checkpoint/` to `make integration`.

#### WP-11 checkpoint-materialize · M · wave 1.2 · after WP-10
- **Scope** (07-runtime §2.6, §2.8; 06-security T2, L3, L7). Confined
  `read-tree` and `checkout-index` into `.xbin/deploy/<TileKey>/.tmp-*`, then
  directories set to 0755 and files to 0444 or 0555 (the exec bit kept), so
  `rm -rf .xbin` keeps working, and an atomic rename to the full tree hash;
  sandboxes see the tree read-only through bind flags, not host modes;
  single-flight; stale `.tmp-*` swept; retention by 07-runtime §2.8
  (artifacts are WP-16a's); GC, in one confined run, takes a `keep func()
  []string` and never evicts a tree that a running generation binds.
- **Owns.** `internal/checkpoint/{materialize,gc}.go`,
  `materialize_linux_test.go`, unit tests.
- **Tests.** `TestMaterializeAtomicReadOnly`, `TestCheckpointGC` (with
  `TestGCKeepsTreesOfRunningGenerations`); confined
  `TestMaterializeSymlinkEscape`.
- **Links.** `Store.Materialize`, `Store.GC` → WP-14b, WP-16a; `keep` is the
  `Runner.RootsInUse` WP-05 declared and WP-16a fills.

#### WP-12 checkpoint-log-remote · L · wave 1.2 · after WP-10
- **Scope** (07-runtime §2.3, §2.7, §2.9; 11-contract §1.10–§1.12, §10.3;
  06-security T15, L4, L6). The deploy log's commit chain
  (`refs/xbin/log/<name>`). The view repository
  `data/checkpoints/<TileKey>.view.git` (05-model §3): only
  `refs/heads/deploy/<name>` for each pinned deployment, pointing at the
  checkpoint's git view, plus `HEAD` naming the primary's ref (dangling while
  the primary follows the work tree); no other objects, never a `refs/xbin/*`
  ref; a confined `update-server-info` after each change to its refs;
  rebuilt from the store's git views; removed with the record. `ServeFetch`:
  an allow-list of dumb-HTTP files (`HEAD`, `info/refs`,
  `objects/info/packs`, loose objects, packs) opened beneath the view
  repository, every symlink refused (NP-06-18), never through `http.ServeFile`
  (`internal/broker/templaterepo.go:226`). The diff (`--no-ext-diff
  --no-textconv`, its caps, one running and one waiting per tile); a
  work-tree diff on a tile without a record answers 409 and captures nothing
  (11-contract §1.11). The drift count (NP-13-12).
- **Owns.** `internal/checkpoint/{log,remote,diff,drift}.go`,
  `fetch_linux_test.go`, unit tests.
- **Tests.** `TestDeployLog`, `TestViewRepoHoldsOnlyPinnedViews` (=
  `TestFetchRemoteNeverServesDeployLog`), `TestFetchRemoteServesOnlyGitFiles`,
  `TestFetchRemoteRefusesSymlinks`,
  `TestCheckpointDiffNoExternalTools`, `TestDiffQueueBounded`,
  `TestDeployLogConfined`, `TestDriftCountChangesNothingDurable`; confined
  `TestCheckpointFetchRemoteFlowH` (the branch carries no ignored file).
- **Links.** `Log`, `AppendLog`, `Diff`, `Drift`, `ServeFetch` → WP-14b,
  WP-15, WP-20. Nothing is written to a tile's repository (NP-12-7). WP-10's
  store keeps no `refs/heads/deploy/*` (07-runtime §2.3): those live only in
  the view repository. `Store` carries the shared pieces (`acquire`,
  `script`, `forget`, `ViewDir`, `TreesDir`, `Caps`); the package doc lists
  the methods WP-11's and WP-12's own files add. `capture.go` is 665 of 800:
  add files, don't grow it.

#### WP-13 plane-record · M · wave 1.1 · after WP-06
- **Scope** (11-contract §10.1; 05-model §3, §4; 06-security T11, C10;
  P29). The record at `data/deployments/<TileKey>.json`, carrying the full
  tile path, the owner ref and a creation stamp, and ignored unless all three
  match the tile it is read for; `seq`; a load that validates and fails
  closed; atomic writes that keep unknown fields; an in-memory index for O(1)
  lookups; the zero state synthesized, writing nothing.
- **Owns.** `internal/deployments/{record,index,plane}.go`, unit tests.
- **Tests.** `TestRecordLocation`, `TestRecordValidationFailsClosed`,
  `TestRecordTilePathMismatchRefused`, `TestRecordInvariantsRandomized`
  (record half).
- **Links.** The record and the index → WP-14a, WP-14b, WP-18, WP-20, WP-22,
  WP-23b. PO-7, PO-8 (boot reads records, never rewrites them).
  `Plane.Boot` runs inside boot's `stepRegistry`, after the registry hooks
  and before `broker.New`'s first `Provision`, and an error from it stops
  xbind (WP-06). Following 07-runtime §5.1, a tile whose record or
  preparation fails, fails closed on its own: no inbound surface, no
  backend, and a status that names the failure. So Boot returns an error
  only for what no tile can survive, and one bad record never keeps xbind
  down.

#### WP-14a plane-authz · M · wave 1.1, merged after WP-13 · after WP-13
- **Scope** (05-model §10; 11-contract §1.2; 06-security T9, C4; NP-14-3).
  One authorize function over the operation rows (`CanTerminalTileVia`,
  `internal/auth/sessiongate.go:7`; manager acts through
  `Broker.MayManageDeployments`, never an element principal); view-as
  refusals; a `Recheck` the ops call at commit; the ship-dark switch
  (NP-14-5), read as `Plane.OptInClosed` (§6.3); the op registry.
- **Owns.** `internal/deployments/{authz,dispatch}.go`, unit tests.
- **Tests.** `TestDeployAuthzMatrix` (M1 rows, and the manager column),
  `TestViewAsRefusedEveryOp` (M1 ops), `TestNoTerminalCannotOperate`,
  `TestOwnRuntimePrincipalsCannotOperate` (including a tile that holds `xbin`
  and `xbin:users` grants).
- **Links.** The op registry and `Recheck` → WP-14b, WP-15, WP-52, WP-53a.

#### WP-14b plane-ops-m1 · L · wave 1.2, merged after WP-11, WP-12, WP-16b · after WP-14a
- **Scope** (05-model §5; 07-runtime §8.2–§8.7; 11-contract §1.4, §1.6). The
  M1 operations onto `main`, with 07-runtime §8.2's pointer rules; the queue
  and `seq`; the `expect` and `checkpoint` fields; `confirm`; `dryRun`, which
  computes the impact without capturing; P18's refusals before any change; a
  failed attempt to pause live reload (NP-02-11); opt-out, which removes the
  record and the view repository and keeps the store; `deployments` events
  under C2; deploy-log writes.
- **Owns.** `internal/deployments/{ops,queue,events,plane}.go`,
  `internal/deployments/dryrun_test.go`, unit tests.
- **Tests.** For the M1 ops: `TestDeployPlaneOperations`,
  `TestOperationsNeverWriteWorkTree`, `TestRecordInvariantsRandomized`, the
  plane half of `TestNonIsolatedRefusesBackendDeployments`,
  `TestAuthorizationRecheckedAtCommit`, `TestDeployLogAudit`,
  `TestOptOutReturnsToZeroState` (plane), `TestDryRunsCreateNoStore`.
- **Links.** Ops → WP-15. PO-7, PO-15. The plane has no store field and
  boot builds no store: WP-14b builds WP-10's lazy `Store` inside the plane
  from `Plane.Root`, since `boot.go` is closed (NP-14-2).
- **From wave 1.1.**
  - WP-13 took `TestRecordInvariantsRandomized` for the record half: the
    plane half is a subtest of it or a function of another name.
  - The record: `p.idx.commit(tile, expect, change)` and
    `p.idx.remove(tile, expect)`, `expect < 0` meaning no compare-and-set
    (the zero state has seq 0); `ErrRecordHeld`, `ErrRecordInert`,
    `ErrStaleSeq` (with §1.14's stale-seq text). WP-13 built the plane side
    of the three tile-life hooks.
  - `PinnedPrimary` answers "not prepared" for every pinned primary until
    WP-14b prepares checkpoint code in `Boot` with
    `registry.ReadCheckpoint(materializedRoot)`; a read error fails the tile
    closed (a zero `&registry.PinnedCode{}`, the error in its status), and a
    `ManifestErr` means the checkpoint can't start, so a deploy of it is
    refused before commit. The `View` hook calls
    `p.Reg.View(c, registry.ViewCode{Deployment, Tree, Root})`, the
    deployment named explicitly (WP-18).
  - The store: `checkpoint.New(p.Root)`, lazily; `Source{Tile, WorkTree,
    Nested}`; `Capture` with `Create` only on a committed opt-in; dry runs
    use `Estimate` and `Estimate.Check(tile, store.Caps)`; errors
    `ErrRefused` (`*Refusal`), `ErrRateLimited` (429,
    `*RateLimited.RetryAfter`), `ErrNoStore`, `ErrBadID`,
    `ErrUnknownCheckpoint`, `ErrAmbiguousID` (WP-10).
  - Ops register from `init` with `register(op, Handler[R]{Subject, Run})`;
    `Run` calls `p.Recheck(g, subjectNow)` where it commits and returns
    `*Error` for refusals (WP-14a).
  - `deployments` events: a reader form is exactly record `{op, what}` or
    deploy `{op, deployment, checkpoint, result, phase, by}`; any other key
    makes it a full form, which only the write audience receives (WP-21).

#### WP-15 plane-api · M · wave 1.2, merged after WP-14b · after WP-07
- **Scope** (11-contract §1). Generic handlers: decode a route's body into its
  op's request type, call the registry, answer `State`, `DeployEntry` or
  `Impact`; an unregistered op answers 501. `GET /deployments` with its
  reader view (§0.3: primary-scoped facts only) and `features` (NP-14-4); the
  log (with `wait`); the diff (text/x-diff or `stat=1`,
  `X-XBin-Checkpoint-*`); `GET /checkpoints/{rest...}` → `ServeFetch`, open
  to the tile's own terminal and agent sessions and to humans with ≥ `write`
  at their current level, checked per request.
- **Owns.** `internal/boot/deployments.go`, `internal/boot/deployreads.go`
  (the log, diff and remote handlers: the card's scope doesn't fit one file
  under the 800-line cap), `internal/boot/deployments_test.go`.
- **Tests.** `TestDeploymentStateReadIsPure` (with the reader rows),
  `TestFetchRemoteReadGate`, `TestDiffCaptureNeedsTerminalLevel`, a handler
  test per route.
- **Links.** Protocol prose for WP-29. PO-7, PO-14. From wave 1.1: an op
  that isn't `deployments.Registered(op)` answers `reservedRoute`; otherwise
  `NewRequest(op)`, `server.DecodeJSON`, `dp.Do(ctx, auth.PrincipalOf(r),
  op, req)`, and a `*deployments.Error` answers `server.WriteError(w,
  e.Status, e.Msg, e.Docs())`. Reads use `dp.Authorize` with `OpState`,
  `OpLog`, `OpDiff`, `OpDiffWorkTree` or `OpFetch`, and `dp.Audience` picks
  the full, deployment or reader view; `caller.can`, `allowed` and
  `caller.manager` come from `dp.Can`, `dp.Allowed`, `dp.Manager`
  (WP-14a); `Plane.Lookup` gives `Found{State, Record, Err}` (WP-13).
  `TestOperationsNameTheRoutes` compares `deployments.go`'s POST literals
  with WP-14a's acts table. The terminal's `xbin-deploy` remote escapes each
  tile-path segment with `url.PathEscape` (WP-22), so the `{rest...}`
  handler splits the decoded `PathValue`, never `RawPath`.

#### WP-16a runtime-codefor · L · wave 1.2 · after WP-01a, WP-05
- **Scope** (07-runtime §1, §7, §9). Every restart path builds from
  `CodeFor` (P9); per-checkpoint artifacts, current plus three kept
  (NP-02-3); `RootsInUse` answers the trees running generations bind.
- **Owns.** `internal/runner/{runner,inspect}.go`, `pinned_test.go`.
- **Tests.** Seam rows 16–22 and 30, `TestArtifactPerCheckpoint`.
- **Links.** `Artifact`, `RootsInUse` → WP-11, WP-16b. PO-3, PO-11, PO-12;
  rows 1–15 unchanged. `Registry.View` takes the deployment explicitly,
  `registry.ViewCode{Deployment, Tree, Root}`, since `runner.Code` names
  none; views and cached checkpoints are shared pointers, never mutated
  (WP-18).

#### WP-16b runtime-deploy · L · wave 1.2 · after WP-01a, WP-05
- **Scope** (07-runtime §8.1–§8.7, §11, §12). `Runner.Deploy` on the build
  turn, reporting phases and failures through `commit` and `progress` into
  `deployments`, never `build-*` (NP-11-21); a successful swap on the
  primary emits one `reload` for the bare component, only when the primary's
  code changed; restart as a deploy of the same checkpoint; alwaysOn, VM and
  entry checks read the view; no checkpoint starts without isolation, and
  lost isolation holds pinned backends (06-security C7); a failed attempt to
  pause live reload (NP-02-11).
- **Owns.** `internal/runner/{deploy,states,alwayson,vm}.go`,
  `deployop_test.go`.
- **Tests.** Seam rows 23–29 and 38, `TestNonIsolatedRefusesBackendDeployments`
  (runner half), `TestIsolationLossHoldsPinnedBackends`,
  `TestDeployQueueCoalesces` (= row 28).
- **Links.** `Deploy` → WP-14b. PO-5, PO-11.

#### WP-17 runtime-checkpoint-build · L · wave 1.2 · after WP-01b, WP-05, WP-S1
- **Scope** (07-runtime §3, §10.4's code bind; 06-security L8, L9, T17).
  Checkpoint builds through `DirFrom`, with 07-runtime §3.1's binds
  (NP-07-4) and `build.json`; `go.work` modules of other tiles resolved by
  binding that tile's pinned primary checkpoint (or its work tree, while that
  primary follows it) over their directory; the launch spec binds `{Src:
  <code root>, Dst: c.Dir, RO: true}` for pinned code, and a nested
  component's own code after it; the env layer hashes the view's `setup` and
  binds the code root; `gcEnvLayers` keeps every referenced hash.
- **Owns.** `internal/runner/{build,sandboxcmd,env}.go`,
  `build_checkpoint_linux_test.go`, `launch_checkpoint_linux_test.go`.
- **Tests.** `TestLaunchSpecBinds`, `TestEnvLayerGCKeepsReferenced`,
  `TestSetupLayerSeesCheckpointNotWorkTree`, `TestConfinedGoBuild` (its
  checkpoint case); confined `TestConfinedBuildOfCheckpointAtCanonicalPath`,
  `TestPinnedBackendSeesCheckpoint` (code half, with a VM variant that skips
  without KVM), `TestNestedComponentBoundAfterCheckpoint`.
- **Links.** PO-11: the live reload target's spec equals the golden. From
  WP-S1: `At` binds go in `Cmd.Binds`; a mount point missing under a
  read-only checkpoint is made inside the materialized tree (07 §10.4's
  footprint); a symlink or file on the path fails the start and names it.
  `internal/sandbox/init_linux.go` is 752 of 800 after WP-S1: a verbatim
  split of `mountBinds` into `nested_linux.go` is the integrator's if room
  is needed.

#### WP-18 registry-view · L · wave 1.1 · after WP-05
- **Scope** (07-runtime §5; 05-model §6). `View` parses the checkpoint's
  `xbin.json` through `OpenBeneath`, takes deployment-level fields from the
  deployment's own code and tile-level fields from the work tree, and caches
  by (path, tree, manifest hash); a pinned view's `uses` is the union of
  both, for env (NP-07-3); `resolveNative` checks the code root. For a tile
  with a record whose primary is pinned, `Rescan` gives the registry entry
  the primary checkpoint's inbound surface (`template`, `exposes`,
  `expose.roles`, `provides`, `chrome`), so every existing reader (NP-14-14)
  follows the primary's code without an edit of its own; the work tree's
  values still feed the manifest error. `Rescan` keeps a record-bearing tile
  whose work-tree manifest is missing or broken, with the primary
  checkpoint's tile-level fields and the error surfaced. P22 in M1:
  `PinnedCode.Scope` (07-runtime §5.1) carries a pinned scope root's
  declarations (resources and importMap) from its checkpoint's
  `scope.json`, read through `OpenBeneath` and validated (the
  resource-name charset), where the registry reads the work tree's today
  (`internal/registry/registry.go:447`); `Provision`
  (`internal/broker/resources.go:36`, called at `internal/boot/serve.go:172`)
  provisions from the registry's scopes and keeps resources no longer
  declared. WP-05 declared the checkpoint's `scope.json` in two places, as
  the design gave it: `PinnedCode.Scope` and a `ScopeResources` hook. WP-18
  wired `PinnedCode.Scope`, since only it carries the importMap (Q4); the
  integrator deleted the hook (wave 1.1 amendment).
- **Owns.** `internal/registry/{deployview,native,registry}.go`,
  `internal/broker/resources.go` (`Provision`), unit tests.
- **Tests.** `TestManifestFieldSplit` (the three field kinds, the inbound
  surface of every pinned primary included),
  `TestPinnedPrimarySurvivesMissingManifest`,
  `TestCheckpointManifestReadNoFollow` (with `scope.json`),
  `TestProvisionFollowsCode` (the pinned primary's rows; P22).
- **Links.** The view → WP-16a, WP-16b, WP-19, WP-40. The zero-state view is
  the registry's own pointer (NP-13-3). PO-12.

#### WP-19 serving-pinned · M · wave 1.2 · after WP-18
- **Scope** (07-runtime §4.1, §4.2, §4.5; 06-security T2).
  `internal/server/deployserve.go` and its call sites in the static, strict
  and native openers, with `inject`, `native` and `HasIndex` from the view;
  `deps/<name>/…` inside a checkpoint re-dispatched by path to the target
  tile's `/c/` plane (its primary), any other escaping symlink a 404
  (`ErrEscapes`), no host symlink ever followed; the proxy's template and
  no-backend gates read the primary's view, since `runtime` is
  deployment-level; `/components` (`componentInfo`,
  `internal/server/api.go:130-160`, filled at `:162`) takes
  `runtime`, `hasIndex` and `native` from the primary's code and adds only
  the primary summary.
- **Owns.** `internal/server/{deployserve,static,tileassets,native,api}.go`,
  `internal/proxy/proxy.go`, tests.
- **Tests.** `TestPinnedPrimaryServesCheckpoint`,
  `TestPinnedServingUsesOpenBeneath` (the `deps/` and escaping-symlink rows),
  `TestNativeDocumentFollowsPrimary`, `TestComponentsDeploymentFields`,
  `TestProxyRuntimeFromDeployment` (a work-tree `runtime` edit never changes
  what pinned code runs as);
  `TestLegacyInjectionUnchanged` unchanged.
- **Links.** PO-1, PO-10, PO-14. `/components` reads roles from
  `c.WorkTreeManifest().Expose` (WP-18); every other reader follows the
  primary's code with no edit (NP-14-14).

#### WP-20 watch-gate · M · wave 1.2 · after WP-06, WP-12, WP-13
- **Scope** (07-runtime §6; NP-13-12). The pure `liveTargets` in
  `internal/boot/liveroute.go`; the six-line change in `watchLoop`
  (`internal/boot/serve.go:167`, its loop at `:183-187`); while live reload
  is paused, a drift count debounced to one per tile every 2 s, and the
  `deployments` op `work-tree`. `watchLoop` already receives the plane as
  `dp` and doesn't read it (WP-06). `dp.LiveReload(tile) (dep, attached)` and
  `dp.Primary` are `liveTargets`' `lr` and `primary`. `dp.WorkTreeMoved(tile)`
  is the loop's notice for a paused tile that a batch touched, and the drift
  count, its debounce and the op go behind it.
- **Owns.** `internal/boot/{serve,liveroute}.go`, `liveroute_test.go`,
  `internal/deployments/worktree.go`. WP-06 left a no-op `WorkTreeMoved` in
  `plane.go`, which is WP-14b's file in this wave. The integrator moved
  that method verbatim into `worktree.go` at the wave's start, and WP-20
  fills it there. A held tile answers `LiveReload` ("", false), so it never
  reaches `WorkTreeMoved`; `p.Lookup(tile).State == RecordHeld` tells it
  apart (WP-13).
- **Tests.** `TestReloadPlan`, `TestWatchLoopZeroStateNoStoreIO`,
  `TestPendingCountAfterDroppedBatch`, a 1 000-path batch benchmark within 5 %
  of today (07-runtime §13.1).
- **Links.** PO-5, PO-12.

#### WP-21 events-status · S · wave 1.1 · after WP-05
- **Scope** (11-contract §3.3–§3.4; 05-model §8; 06-security T7).
  `handleEventsWS` (`internal/server/server.go:592-612`) filters by §0.3's
  audience rule, keyed on the event's `deployment`: an event about the
  primary goes to the tile's readers; one naming a non-primary deployment
  only to admins, humans with ≥ `write` (current level), and principals
  bound to that deployment plus the tile's terminal and agent sessions; the
  primary's frame token never counts as bound to it; never other tiles'
  principals. The status plane clears the primary's status when a
  `deployments` op `deploy` swaps, not at build start, since checkpoint
  deploys emit no `build-start` (`internal/obs/status.go:129-143`).
  `/ws/term/env` only gains fields.
- **Owns.** `internal/server/server.go`, `internal/obs/status.go`, tests.
- **Tests.** `TestDeploymentEventsFiltered` (primary rows),
  `TestTermEnvAdditive`, a status test on `testPlane`.
- **Links.** The filter → WP-48 (the non-primary rows, once claims exist).
  PO-5.

#### WP-22 term-remote · S · wave 1.1 · after WP-05
- **Scope** (11-contract §1.12; 05-model §3; NP-12-7, NP-13-8). A session
  opened while its tile has a record gets `GIT_CONFIG_COUNT=4`: today's two
  pairs (`internal/term/term.go:787-792`, now in `sandboxenv.go`) plus
  `remote.xbin-deploy.url` and `.fetch` (`+refs/heads/deploy/*:refs/deploy/*`),
  never written into the tile's `.git/config`. Zero-state sessions, and those
  of a tile whose record is gone while its store remains, keep two.
- **Owns.** `internal/term/sandboxenv.go`, `internal/term/remote_test.go`.
- **Tests.** `TestFetchRemoteInjection`, `TestTerminalMasksDeploymentState`;
  `TestSessionEnvZeroState` unchanged. `internal/term`'s tests build `./cmd/bx`
  (`internal/term/agent_test.go:30-49`).
- **Links.** PO-3, PO-15.

#### WP-23 backup-record · M · wave 1.2 · after WP-10, WP-13
- **Scope** (08-data §11.1, §11.2, §11.4; 06-security T11, L13; NP-12-10).
  Tar prefixes `deployments/record.json` and `deployments/checkpoints/…`;
  restore refuses an archive naming another tile before writing anything,
  installs the record only when none exists, rebuilds the store from its
  objects in confine (never `config`, `hooks/`, `info/` or alternates), and
  keeps archived refs in a restore-only namespace; `Schema` stays 1; `POST
  /restore`'s body is unchanged.
- **Owns.** `internal/broker/backup_deploy.go`, `internal/broker/backup.go`
  (seams), `internal/backup/backup.go`, tests.
- **Tests.** `TestBackupCoversDeployments` (record, store),
  `TestRestoreRefusesForeignManifest`; confined
  `TestRestoreRebuildsStoreConfig`.
- **Links.** PO-9; older xbinds skip the new prefixes (12-compat §5.1). The
  integrator adds `./internal/broker/` to `make integration` in this merge.

#### WP-23b tile-life-m1 · S · wave 1.2 · after WP-13
- **Scope** (05-model §11; P29; NP-12-9). The M1 half of the tile's life,
  through the tile-life hooks WP-05 declared and WP-06 installed:
  - `SetOwner` rewrites the record's owner ref in the same step, before the
    unconditional restart at `internal/broker/transfer.go:274-277`, so a
    pinned primary stays pinned across a transfer (D39);
  - `assignOwner` (`internal/broker/create.go:188`), which every creation
    path calls (`create.go:64`, `clone.go:151`, `gitimport.go:224`,
    `tiles.go:110`, `templates.go:171`), resets the path's record and view
    repository before it assigns the owner, so a tile re-created at the path
    starts in the zero state;
  - `pathLeftovers` (`internal/broker/policy.go:218`) lists the record and
    the store, the path's owner exempt, as today.
- **Owns.** `internal/broker/{transfer,create,policy}.go` (seams),
  `internal/broker/orgsapi.go` (one seam before `SetOwner`),
  `internal/broker/deploy_life_m1_test.go`.
- **From wave 1.1.** WP-13's index compares the record's owner ref with the
  owner store on every lookup (Q1's default), so the rewrite must run
  before `st.SetOwner` (`internal/broker/orgsapi.go:492`):
  `executeTransferEffects` runs after it, and a rewrite there finds the
  record inert and the pinned primary loses its pin. WP-23b either calls
  `b.rewriteDeploymentOwner(tile, to)` just before `SetOwner` in
  `orgsapi.go`, or moves the `SetOwner` call into `transfer.go`. WP-13
  built the plane side: `RewriteDeploymentOwner`, `ResetDeploymentState`
  (removes the record and the view repository, keeps the store),
  `DeploymentLeftovers` (the record, and the store by `Lstat`).
- **Tests.** `TestDeploymentsAcrossTileLife` (the M1 half of its transfer
  row: a pinned primary stays pinned), `TestPathLeftoversIncludeDeploymentState`
  (the M1 half: the record and the store, and a tile re-created at the path
  starts in the zero state).
- **Links.** The hooks' M2 halves → WP-46, WP-53b. PO-7, PO-15.

#### WP-24 deploy-state · M · wave 1.1
- **Scope** (10-ux §2, §4, §12, §14.5; NP-10-3). `web/deploy-state.js`, which
  imports nothing: (state, permission flags) → view model (chip, offer,
  pending count, disabled, protected and view-as reasons), and every §12
  string M1 needs. Its exports are fixed in the first commit, for WP-25.
- **Owns.** `web/deploy-state.js`, `hack/deploy-state.test.mjs`.
- **Tests.** `hack/deploy-state.test.mjs` (M1 rows, the zero-state row first).
- **Links.** A new `/vendor/` module (compat rules 3, 4).

#### WP-25 terminal-window · L · wave 1.2, merged after WP-15 · after WP-24
- **Scope** (10-ux §2, §5, §9, §14; 13-surfaces §4.16). `frame-deploy.js`
  (state reloaded after every events reconnect, 12-compat NC-5); bx-frame's
  one-line hooks; the chip and the Reload now offer in the bar and the tools
  row; the launcher banner; bx-terminal's `note(text)` (NP-10-1); the test
  names; the `livereload` pass; `viewAs`'s attempts to pause live reload
  and to deploy. The API dropdown is untouched in M1.
- **Owns.** `web/{frame-deploy,bx-frame,frame-titlebar,frame-launcher,bx-terminal,frame-testapi}.js`,
  `hack/ui-harness/passes/{livereload,viewas}.js`.
- **Tests.** Harness `livereload` (parts 0–6), `viewAs`; `agentTab`,
  `reloadFocus`, `windows` unchanged; `js-check`, `theme-check`.
- **Links.** PO-10; the window pref never persists an unknown layout
  (NP-12-3). From WP-24: 10-ux's zero-state entry (the ⇈ layout button and
  the panel) is M2's, so `entry()` answers the ⇈ control on every tile of
  an xbind with the feature and `chipItems()` answers the zero state (Pause
  live reload); where it sits in the bar is WP-25's call.

#### WP-26 bx-live-reload · L · wave 1.2, merged after WP-15 · after WP-07
- **Scope** (11-contract §9; 10-ux §8; 12-compat §4.2). `bx live-reload`;
  deploy and roll back onto `main`; `deployclient.go` (feature detection, the
  `Can` pre-check, `--dry-run`, `--yes`, `--json`, waiting, and the exit
  codes 3 refused, 4 not confirmed (`--yes` needed without a TTY), 5 still
  running, 6 no tile deployments); registration through `moreCmds`
  (`cmd/bx/native.go:28-32`), usage on the `+nativeUsage` line
  (`cmd/bx/main.go:174`); `bx logs` through `GET /logs` where the log file
  can't answer (16-open-questions Q23).
- **Owns.** `cmd/bx/{livereload,deploy,deployclient,status,main}.go`,
  `cmd/bx/deploy_test.go`.
- **Tests.** `TestParseDeploymentArgs`, `TestBxSaysWhereSavesGo` (M1),
  `TestBxExitCodes`, `TestBxOldXbind`, `TestBxJSON`; `TestBxTodayInvocationsUnchanged`
  unchanged.
- **Links.** /docs/bx.md text for WP-29. PO-13.

#### WP-27 itest-infra · M · wave 1.1
- **Scope** (15-test-plan §5.1–§5.2; NP-15-2). `startIsolatedDaemon(t, opts)`
  (`--isolate --rootfs $XBIN_TEST_ROOTFS`, optional auth and ingress,
  `d.restart(t)`, a process-group kill, an honest skip), and `writeProbe`.
- **Owns.** `test/isolated_test.go`, `test/probe_test.go`.
- **Tests.** A smoke test of a probe on the isolated daemon.

#### WP-28 itest-m1 · L · wave 1.3 · after every M1 feature WP
- **Owns.** `test/{livereload,pinned,deployments,flowc,deploystate_boot,latency}_test.go`,
  `internal/boot/deploystate_test.go`.
- **Tests.** `TestLiveReloadPauseStatic` (=
  `TestStaticTilePauseLiveReloadWithoutIsolation`),
  `TestLiveReloadPauseRefusedWithoutIsolation`, `TestLiveReloadPauseRace`,
  `TestLiveReloadPauseGo`, `TestPinnedThroughRestartPaths`,
  `TestRollBackBound` (`main`), `TestFailedDeployInvisible`,
  `TestAgentBxLiveReload`, `TestDeploymentStateBootsTwice`,
  `TestDeploymentStateBootsTwiceInProcess`, `TestOptOutReturnsToZeroState`
  (integration), `TestLatencyLifecycleOps` (M1 rows).
- **Links.** `TestFailedDeployInvisible`'s event tape → WP-60. WP-27's
  helpers: `startIsolatedDaemon(t, isoOpts{Auth, Ingress, Args, Env})`,
  `d.do`, `d.stop`, `d.start`, `d.restart`, `d.Bin`; `writeProbe`,
  `waitProbe`, `writeIfChanged`, `probeFile`. `TestAgentBxLiveReload`
  builds `bx` itself and passes `isoOpts{Env: []string{"XBIN_BIN=<dir>"}}`.
- **From wave 1.2.**
  - Every M1 route answers in the daemon: the state, pause, resume, reload
    now, deploy (restart:true), rollback, the log (`id`, `wait`), the diff
    and the checkpoint remote; `TestDeploymentReadsLive`
    (`internal/boot/deployreads_live_test.go`) drives them once through a
    real daemon, and the harness `livereload` pass under `HARNESS_ISOLATE`
    runs pause, Reload now and resume on a node backend. `bx` has run only
    against unit fakes and 501s: `TestAgentBxLiveReload` is its first real
    run.
  - Not wired yet, so not testable: checkpoint GC (`Store.GC` after a
    deploy, keep = the record's pointers + `Runner.RootsInUse`), the boot
    `.tmp-*` sweep (`Store.Sweep`), artifact pruning
    (`Runner.PruneArtifacts`), and a restore's put-back of deployment state
    (WP-23's A1–A4: a restore checks the archive and leaves the record and
    store out, with a warning). Retention and restore rows wait for them.
  - A dry run's `impact.code` carries `files`, `added`, `removed` from a
    stat diff through the store.

#### WP-29 docs-m1 · L · wave 1.3, drafting from 1.2 · after every M1 feature WP
- **Scope** (13-surfaces §4.18's M1 rows; 12-compat §8, §9; 10-ux §10.2).
  [/docs/protocol.md](/docs/protocol.md)'s M1 prose, routes, event and paths,
  and its reserved markers for what M1 built;
  [/docs/elements.md](/docs/elements.md), [/docs/isolation.md](/docs/isolation.md),
  [/docs/bx.md](/docs/bx.md), [/docs/frontend-kit.md](/docs/frontend-kit.md),
  [/docs/compat.md](/docs/compat.md), [/docs/maintenance.md](/docs/maintenance.md);
  the save-to-reload statements in `docs/overview/{01,03,04,09,14,15}-*.md`,
  `docs/index.md` and `docs/getting-started.md`; `workspace-template/AGENTS.md`;
  the builder page `docs/tile-deployments.md` (NP-14-10);
  `TestDeploymentWording` (NP-15-9). `--tile-deployments=off` goes in
  `docs/tile-deployments.md`, `docs/maintenance.md` and
  `docs/overview/15-operations.md`, as §6.3 describes it (WP-06's hand-off;
  `docs/config.md` already carries the generated row).
- **Owns.** Those files, `internal/docscheck/wording_test.go`.
- **Tests.** `TestDeploymentWording`, `TestRelativeLinksResolve`,
  `TestDecisionIDsResolve`, `TestEmbeddedAssets`.
- **Links.** "No deploy step" stays true for tiles that never opt in.
- **From wave 1.2.** The docs hand-offs are in the WP commits' bodies
  (`git log dev-lifecycle --grep='^Docs:'`) and the merge commits of
  wave 1.2. Three texts refuse a pinned backend without isolation: the
  deployments API's 409 and `runner.ErrNeedsIsolation` use 11-contract
  §1.14's, while a restart through `Ensure` fails its build with
  `resolveGen`'s longer text (both name `--isolate`). The diff's `stat=1`
  answer also carries the `X-XBin-Checkpoint-*` headers. `/runtime` rows
  gain `checkpoint`. protocol.md keeps its "(reserved)" markers on every
  route built in 1.2 until this WP drops them; openapi.go dropped them at
  the merges.

### 2.4 M2 — tile deployments

#### WP-30 m2-interfaces · M · wave 2.0 · after the M1 exit
- **Scope** (NP-14-11; the D109 lesson). Every cross-WP M2 interface, once,
  answering today's: the broker hooks the plane installs (a tile's
  deployments and primary, a principal's addressed deployment, the active
  registrations, the edge verdict); `resKeys`'s signature; the obs plane's
  deployment input; the terminal-target hook; the M2 ops' request types; the
  shared broker fixture of 15-test-plan §3.7, with a users store;
  `Runner.AtLimitTile(path) []LimitHit` in a new file, answering today's
  single-leaf `AtLimit(CompKey)`, with `stepLimitAlerts`
  (`internal/boot/boot.go:457-484`, today `AtLimit(key)` at `:466`) switched
  to it so at-limit alerts survive the nested leaves; a `boot.go` amendment
  installing the hooks, the runner's `LimitsFor` among them (P22). WP-06 left
  `LimitsFor` nil, which keeps the installed caps. After WP-06, `boot.go` is
  754 lines of its 800, so WP-30 has 46.
- **Owns.** `internal/broker/{broker,deploydata}.go` (declarations),
  `internal/broker/deploy_fixture_test.go`, `internal/obs/obs.go`,
  `internal/term/target.go`, `internal/deployments/{plane,m2types}.go`,
  `internal/runner/limits.go`, `internal/boot/boot.go`.
- **Tests.** 09-fabric §10's `TestZeroStateRoute`; every golden unchanged.
- **From wave 1.2.** `plane.go` is 726 of 800. The plane grew three files
  outside any card: `storeadapter.go` (the checkpoints interface's adapter
  over `*checkpoint.Store`, moved out of `plane.go`), `reads.go`
  (`Plane.Diff`, `Plane.ServeFetch`) and `summary.go`
  (`Plane.PrimarySummary`); the interface gained `RemoveView`, `Drift`,
  `Diff` and `ServeFetch`. `ops.go` is 768 of 800.

#### WP-31 qualifier · S · wave 2.1 · after WP-30
- **Scope** (11-contract §2.1, §2.2, §2.4; NP-12-1). `ResolveRef`: a `+`-free
  fast path; a split only for a tile with a record, and only after today's
  resolution fails; a component or on-disk path at the full candidate wins;
  unknown names 404; nested components never entered.
- **Owns.** `internal/registry/qualifier.go`, `qualifier_test.go`.
- **Tests.** `TestResolveDeploymentQualifier`, with PO-1's rows.
- **Links.** `ResolveRef` → WP-36, WP-37, WP-38. PO-1.

#### WP-32 auth-claims · M · wave 2.1 · after WP-30
- **Scope** (11-contract §7.1–§7.4; 09-fabric §3). `RegisterInstance(token,
  path, dep)` and the instance map by (tile, deployment); `MintTerminal`
  with a target; the sixth frame-token field under the name rule (never for
  `main`; 4- and 5-field forms mean `main`); renewal copies the claim; old
  signatures stay as `main` wrappers.
- **Owns.** `internal/auth/{deployment,auth,frametoken}.go`,
  `deployment_test.go`.
- **Tests.** `TestPrincipalDeploymentZeroValue`,
  `TestInstanceTokenCarriesDeployment`, `TestFrameTokenDeploymentClaim` (=
  `TestFrameTokenBoundToDeployment`), `TestTerminalTokenTarget` (=
  `TestTerminalTargetBinding`, `TestTerminalTokenCannotMintProtectedPrimaryToken`);
  `TestLegacyFrameTokenUpgrade` unchanged.
- **Links.** Claims → WP-33, WP-36, WP-37, WP-48, WP-51. PO-6.

#### WP-33 runner-deployments · L · wave 2.1, merged after WP-32 · after WP-30
- **Scope** (07-runtime §1.2–§1.3, §8.5, §8.8, §9; NP-07-7). The runner API
  of 07-runtime §1.2 keyed by (tile, deployment), under §0.3's additive
  names; per-deployment sockets, logs (a non-main deployment's at
  `.xbin/deploy/<TileKey>/d/<name>/backend.log`) and `XBIN_DEPLOYMENT` (the
  role rule); `RegisterInstance` with the deployment; one `eventComp` switch:
  the primary's activity on today's types with the bare component, any other
  deployment's only as `deployments` ops (C2); reassignment restarts, new
  primary first; the admission caps (3, 24, 12 running).
- **Owns.** `internal/runner/{runner,deploy,states,inspect,rundir}.go`,
  `deployments_test.go`.
- **Tests.** Seam rows 31–34, 37; `TestBackendEnvPerDeployment`,
  `TestInstanceTokenRegisteredWithDeployment`, `TestLogPathsPerDeployment`,
  `TestRunDirPerDeployment`, `TestEnsureCallSitesPassPrimary`, 09-fabric §10's
  `TestDrainAuthority`.
- **Links.** Per-deployment state → WP-34, WP-37, WP-52, WP-53b, WP-54,
  WP-S4. PO-2, PO-3, PO-5, PO-11.
- **From wave 1.2.** `deploy.go` is 739 of 800 and `runner.go` 615 of 639:
  split the deploy worker verbatim before growing it. A deploy resolves its
  generation through `resolveGen` (inspect.go), as `Ensure`'s `runCurrent`
  does; `inst.artifact` is the artifact's directory; `watchGen` takes the
  generation's release. `isTreeID` (runner), `fullTree` (build.go) and the
  plane's `fullTreeID` are one predicate three times, and the isolation
  refusal has three texts (WP-29's note).

#### WP-34 runner-edges · L · wave 2.2 · after WP-33, WP-S2, WP-S3
- **Scope** (07-runtime §10.2, §10.3, §11, §12; 09-fabric §5.7–§5.8;
  NP-02-12). Effective alwaysOn and backoff per deployment; VM reservations
  for the tile with headroom, `stopFirst` per deployment; `DialInto` and
  `ensureProvider` resolve the primary; non-primary generations never join a
  roster or splice; the net verdict applied at spawn (no egress for a
  non-primary deployment of a host-sharing tile or through a provider
  splice); the leaf chooser (flat while `main` is alone), each deployment's
  caps a per-deployment limit defaulting to the tile's and never above its
  ceilings (P22); stats from `ProcsTree`; `AtLimitTile` filled: it walks the
  tile's leaves (the flat leaf for zero-state and main-only tiles) and names
  the deployment in each hit.
- **Owns.** `internal/runner/{runner,alwayson,vm,ingress,netmux,stats,limits}.go`,
  tests.
- **Tests.** Seam rows 35–36; `TestCgroupLeafPerDeployment` (=
  `TestLeafPerDeployment`), `TestVMReserveOwnerIsTile`,
  `TestNonPrimaryNeverJoinsProviderRoster`, `TestLimitAlertsNestedLeaves`
  (new); 09-fabric §10's `TestDialIntoPrimary`,
  `TestTerminatorDoorPrimaryOnly`, `TestNonPrimaryAlwaysOn`,
  `TestEdgeStreamDial`.
- **Links.** PO-11: zero-state and main-only tiles keep the flat leaf.

#### WP-35 launch-spec · M · wave 2.1 · after WP-30
- **Scope** (07-runtime §10.4; 08-data §5; 06-security T17, T18, NP-06-6,
  T10 item 3). `resourceBinds(env, root, remap)`, consuming the `ResBind`
  remap WP-05 declared with `EnvFor`, and failing a start on an unmapped
  path; per-deployment env-layer logs and GC keep set; a protected primary's
  build products kept apart; one build limiter, primary first.
- **Owns.** `internal/runner/{binds,sandboxcmd,env,build}.go`,
  `resourcebinds_deploy_test.go`.
- **Tests.** `TestNonPrimaryResourceBindsNeverPrimary`, `TestResourceBinds`
  (remap rows), `TestProtectedBuildProductsSeparated`,
  `TestProtectedPrimaryLayerNotShared`, `TestVMBackendNamespaceBinds`;
  confined `TestPinnedBackendSeesCheckpoint` (data half).
- **Links.** PO-11: `main`'s binds keep `Src == Dst`.
- **From wave 1.2.** `build.go` is 753 of 800 (WP-17): its checkpoint half
  moves verbatim into a file of its own before the protected namespace
  lands.

#### WP-36 deployment-urls · L · wave 2.2 · after WP-31, WP-32
- **Scope** (11-contract §2.3, §2.5–§2.7, §7.2; 07-runtime §4.3–§4.6;
  NP-09-11). Deployment URLs on the static and tokens planes, with the write
  gate at the user's current level on every request, the D4 additions and
  the claim; `mayMintFrameToken` never crosses deployments, for every
  document, bare or qualified: a tile principal mints only for its bound
  deployment, and a human only for a deployment they may open; `?native=1`
  per deployment; origins mode answers 404 with the reason until WP-38
  merges.
- **Owns.** `internal/server/{static,tileassets,deployserve,native}.go`,
  `deployurl_test.go`.
- **Tests.** `TestQualifiedURLRouting`, `TestDeploymentURLGate` (with
  `TestDeploymentURLRefusesOtherTiles`), `TestFrameTokenClaimInjected`,
  `TestMayMintFrameTokenNeverCrossesDeployments` (=
  `TestMintRefusesCrossDeployment`, both directions),
  `TestNonPrimaryDocumentAlwaysSandboxed`.
- **Links.** PO-1, PO-10; `TestLegacyInjectionUnchanged` unchanged.

#### WP-37 proxy-routing · M · wave 2.2 · after WP-31, WP-32, WP-33
- **Scope** (11-contract §2.3, §4; 09-fabric §4; P12). `ResolveRef` in the
  proxy; the deployment choice and the cross-deployment refusal of 09-fabric
  §4; `EnsureDeployment` at `internal/proxy/proxy.go:182`;
  `X-XBin-Deployment` on non-primary calls and responses (NP-11-12); the
  edge verdict for non-primary callers; ingress to the primary.
- **Owns.** `internal/proxy/{proxy,ingress}.go`, `deploy_test.go`.
- **Tests.** `TestProxyQualifiedTarget`, `TestIdentifyDeploymentHeader` (=
  `TestDeploymentHeaderOnlyFromXbind`), `TestIngressForwardPrimaryOnly` (=
  `TestIngressNeverReachesNonPrimary`), 09-fabric §10's
  `TestCrossDeploymentRefused`.
- **Links.** PO-4; `TestIdentifyZeroState` unchanged.

#### WP-38 origins-mode · L · wave 2.3 · after WP-36
- **Scope** (05-model §7; 11-contract §2.6, §7.5; NP-12-4, NP-11-18).
  Name-based origin labels for non-main deployments; `x2`/`c2` tickets and
  cookies; a label → (tile, deployment) lookup at every `TileHostID`
  comparison; navigation to the right origin, the bare URL to the primary's;
  `TilePrincipal` bound to the deployment.
- **Owns.** `internal/server/{tileorigin,tilenav}.go`,
  `internal/auth/{assettoken,tilebinding}.go`, tests.
- **Tests.** `TestOriginLabelPerDeployment`.
- **Links.** `main`'s `x1` and `c1` stay byte-identical (PO-6).

#### WP-39 data-keys · L · wave 2.1 · after WP-30
- **Scope** (08-data §2, §3; NP-08-1, NP-08-11). `resKeys` with the
  injective `escS`, the `.deployments` level, a bbolt file per namespace;
  every `util.ScopeKey` site and hand-built bucket of 08-data §3.2 routed
  through it; resource names validated everywhere; a guard against new
  `util.ScopeKey(` or `"res:" +` in `internal/broker` outside
  `deploydata.go`.
- **Owns.** `internal/broker/{deploydata,broker,resources,resenc_wire,backup,diskmon,resusage}.go`
  (key sites), tests.
- **Tests.** `TestDeploymentKeysDisjoint`, the guard (proposed name
  `TestNoAdHocResourceKeys`), the resource-name test; 08-data §14's
  `TestAddDeploymentDropsStaleFiles`.
- **Links.** `resKeys` → WP-40 to WP-44b. PO-2; `TestZeroStateKeys` unchanged.

#### WP-40 data-access · L · wave 2.2 · after WP-39
- **Scope** (08-data §3.6, §4–§6; 09-fabric §5.10; P22). `EnvFor(view,
  dep)`: identical values plus the remap; `kvAccess`, `blobAccess` and bus
  publish by the principal's deployment (08-data §4.2), the workspace scope
  never split; `busFilter` by namespace; non-main volumes mounted on first
  use; `EncryptionHold` per namespace; `SealResources` stops every
  deployment; `cap:containers` changes re-`Ensure` mounted non-main volumes;
  every deployment provisions what its own code declares, in its own
  namespace, through the view WP-18 built.
- **Owns.** `internal/broker/{resources,resenc_wire}.go`,
  `internal/resenc/resenc.go`, `internal/broker/deploy_env_test.go`.
- **Tests.** `TestEnvForPerDeployment`, `TestKVNamespacePerDeployment`,
  `TestMultiTileScopeNamespaces`, `TestBusPublishStaysInNamespace` (=
  `TestNonPrimaryBusPublishIsolated`), `TestProvisionFollowsCode` (the
  non-primary rows), `TestDeclaredResourceCap`, 09-fabric §10's
  `TestBusNamespaceMatch`; 08-data §14's
  `TestLiveTargetSaveProvisionsOnlyItsNamespace` and
  `TestMultiTileScopeDeclarationSource`.
- **Links.** Fills the `EnvFor` remap WP-05 declared, which WP-35's binds
  consume. PO-2, PO-3.

#### WP-41 data-namespaces · M · wave 2.2 · after WP-39
- **Scope** (08-data §6.3–§6.4, §8.2's holds, §9; NP-08-7, NP-08-14,
  NP-02-10). `ns.json` states and history; the namespace hold and write gate;
  reset (unmount, verify, remove, abort when busy, `vault: true`); deletion by
  the last claimant only; the sweep, orphans, the 14-day grace.
- **Owns.** `internal/broker/deployns.go`, tests.
- **Tests.** `TestResetAndRemoveScope`; 08-data §14's reset, multi-tile, GC
  and dry-run rows (`TestSharedScopeResetNeedsEveryTile`,
  `TestJoinSeededNamespaceManagerOnly`, `TestDataDryRunCreatesNothing`).
- **Links.** PO-2.

#### WP-42 data-seed · L · wave 2.3 · after WP-40, WP-41
- **Scope** (08-data §8; 06-security T8, L10, L11; NP-08-5, NP-08-6). An
  optional, confirmed act of a tile manager of every claimant, in a human
  session (`MayManageDeployments`), through its own endpoint under
  `/api/xbin/deployments/…`; preflight (vault, gocryptfs, quota, reserve,
  online or stopped); kv spooled and re-encoded in bounded transactions;
  sqlite through the backup API and `filesystem` through rsync, both
  confined; single-tenant copy and re-wrap; blob through `OpenBeneath`;
  verification, `partial` on failure, provenance.
- **Owns.** `internal/broker/deployseed.go`, `deploy_seed_test.go`,
  `seed_linux_test.go`.
- **Tests.** `TestSeedManagerOnly`, `TestSeedNeverWritesPrimary`,
  `TestSeedCopyConfinedNoFollow`, `TestSharedScopeSeedNeedsEveryTile`;
  confined `TestSeedSqliteConfined`; 08-data §14's seed rows
  (`TestSeedWithDivergentDeclarations`).
- **Links.** PO-2: the primary's keys are only read.

#### WP-43 data-vault · M · wave 2.2 · after WP-39
- **Scope** (08-data §10; 06-security T5, NP-06-9). `vaultPath(comp, dep)`;
  access by the addressed deployment, D30 re-checked; computed placeholders;
  vault copy (tile manager, human session, keys-only audit);
  `migrateVaults` walks `.deployments/`; the reassignment preflight; a
  protected primary's vault writes are tile managers' acts.
- **Owns.** `internal/broker/{deployvault,vault}.go`, tests.
- **Tests.** `TestVaultPerDeployment`, `TestVaultCopyManagerOnly`,
  `TestProtectedPrimaryVaultWritesManagerOnly`,
  `TestTargetedSessionWritesOnlyItsNamespace`.
- **Links.** PO-2, PO-3: `main`'s vault path is today's.

#### WP-44a data-backup · L · wave 2.3 · after WP-39, WP-41
- **Scope** (08-data §11; NP-12-10; 11-contract's per-deployment backup and
  restore endpoints). Deployment archives (schema 2, key
  `.deployments.<TileKey>.<d>`); the primary archived on every backup when it
  isn't `main`; opt-in non-primary archives with their own retention;
  replace-restores into namespaces through the new endpoints, `POST
  /restore` unchanged; registrations restored for existing deployments;
  offload of every namespace.
- **Owns.** `internal/broker/{backup_deploy,backup,backup_cron}.go`,
  `internal/backup/backup.go`, tests.
- **Tests.** `TestBackupCoversDeployments` (M2),
  `TestRestoreRevalidatesRegistrations`; 08-data §14's archive rows
  (`TestZeroStateBackupBytes`).
- **Links.** PO-9: the main archive means what it means today.

#### WP-44b data-quota · M · wave 2.3 · after WP-39, WP-41
- **Scope** (08-data §12; 06-security T10 item 4; P22). A quota bucket per
  non-main namespace, with a per-deployment quota share that defaults to the
  tile's, is set by tile managers and never exceeds the tile's ceiling;
  non-primary namespaces write-blocked first on low disk; a per-tile quota
  for stores, artifacts and trees.
- **Owns.** `internal/broker/{diskmon,resusage}.go`, tests.
- **Tests.** `TestDiskQuotaCountsDeploymentData`; 08-data §14's quota rows
  (`TestNamespaceLimitIsClaimantsMinimum`).
- **Links.** The limit setter → WP-53a.

#### WP-45 offload-fix · M · wave 2.4 · after WP-44a · gated by R-3
- **Scope** (08-data §9.4, §11.6; NP-08-10; side findings #3, #5). The backup
  walk opens through `OpenBeneath` and reports lossy resources; headers carry
  mode and mtime; ciphertext removed after confirmed PUTs; `MountEncrypted`
  skips offloaded scopes.
- **Owns.** `internal/broker/{backup,resenc_wire}.go`,
  `internal/backup/backup.go`, tests.
- **Tests.** 08-data §14's offload and restore rows.
- **Links.** A deliberate P5 exception: ships only if ratified.

#### WP-46 leftovers-plus · M · wave 2.3 · after WP-39, WP-49, WP-23b
- **Scope** (05-model §7, §11; 08-data §9.3; 12-compat §7). `pathLeftovers`
  also lists non-main vaults, registration directories and namespaces at or
  under a path, the owner exempt; `newTilePathOK`
  (`internal/broker/policy.go:156`) refuses a tile at `<P>+<N>` while `P` has
  deployment `N`, for every creator, before the admin early return; any
  other new tile name containing `+` is created as today, with a
  one-release warning, and is never refused.
- **Owns.** `internal/broker/{policy,create}.go`, `deploy_life_test.go`.
- **Tests.** `TestPathLeftoversIncludeDeploymentState` (M2 half),
  `TestPlusReservedInNewTilePaths` (the narrow refusal, and a warning with no
  refusal for any other `+` name); `TestNewTilePathRule` unchanged.
- **Links.** The warning's text to the changelog hand-off. PO-1.
- **From wave 1.2.** WP-23b's `internal/broker/deploy_life_m1_test.go`
  holds `TestDeploymentsAcrossTileLife` and
  `TestPathLeftoversIncludeDeploymentState`: extend them there, or name the
  M2 halves otherwise. WP-23b's open finding: clone, git import, builtin
  install and template instantiation call `assignOwner` after their first
  `Rescan` (clone.go, gitimport.go, tiles.go, templates.go), so an owner
  re-creating their own removed, paused tile by one of those paths runs its
  leftover pinned record until the next rescan; `resetDeploymentState`
  belongs before those rescans.

#### WP-47 edge-policy · L · wave 2.2 · after WP-30, WP-39
- **Scope** (09-fabric §5, NP-09-1 to NP-09-6, NP-09-10; 08-data §7; P3,
  P11, P12, P23, P27). `internal/broker/edgepolicy.go`, as 09-fabric §5
  specifies:
  - every edge that can't be read-clamped (custom roles with no path to
    `reader`, stream interfaces, lan-ingress links, net-provider splices) is
    blocked, and a request setting `read` or `inherit` on one answers 400;
  - the net edge's `inherit` gives the tile's relay policy only, and resolves
    to `block` with the reason when the tile's `net` resolves to host sharing
    (`Broker.NetHostShare`, `internal/broker/netfn.go:196`) or the slot is a
    provider splice;
  - `gpu:*` defaults to `block`, other capability grants to `inherit`;
  - an unknown or invalid value reads as `block`, and any `block` among
    several authorizing edges refuses the call;
  - setting a policy is a manager act (`MayManageDeployments`).

  `broker.Policy`'s self rule compares deployments and non-primary results
  pass the verdict; `allowRes` clamps `res:`; `httpBindingRole` feeds the
  clamp; grant, binding and interface-instance restarts fan out to every
  deployment.
- **Owns.** `internal/broker/{edgepolicy,broker,netfn}.go`,
  `deploy_edges_test.go`.
- **Tests.** `TestGrantedRoleReadClamp` (= `TestReadClampOnEveryEdge`),
  `TestEdgePolicyBlock` (= `TestEdgeBlockFailsClosed`),
  `TestUnclampableEdgeDefaults`, `TestUnclampableEdgesRefuseOverride`,
  `TestSandboxManagerEdgeBlocked` (the multi `sandbox-manager` slot at the
  custom role `consumer`, and llm-gw's `writer` completions clamped away),
  `TestNetEdgeDefault` (with the host-sharing and splice rows), `TestEdgePolicyNeverTouchesPrimary`,
  `TestSelfCallRouting` (= `TestSelfCallStaysInDeployment`),
  `TestXbinGrantRefusedWithNonPrimary`, `TestNonPrimaryPrincipalNeverAdmin`;
  09-fabric §10's `TestEdgeBlockWins`, `TestEdgeUnknownValueBlocks`,
  `TestEdgeCapabilities`, `TestEdgePolicyCallers`,
  `TestNonPrimaryNeverSharesHostNetwork` (isolated).
- **Links.** The verdict → WP-34, WP-37, WP-50. The primary equals the
  goldens (12-compat Z3). `netfn.go` (1131 of 1170) takes at most 15 lines
  across WP-47 and WP-50.

#### WP-48 route-classes-events · M · wave 2.3 · after WP-32, WP-33
- **Scope** (P26; 09-fabric §6's enforcement; NP-09-4, NP-06-1, NP-04-8,
  NP-13-11; 11-contract §3.2–§3.4). One table classifies every `/api/xbin/*`
  `RegisterAPI` pattern as deployment-scoped, primary-only or neutral;
  `handleAPI` refuses non-primary principals on any unclassified route,
  reads included (403), and PR decisions are primary-only for instance
  principals; a guard over the route inventory keeps the table complete. The
  event filter's non-primary rows, now that principals carry their bound
  deployment; non-main bus events reach admins and principals of that
  namespace only.
- **Owns.** `internal/server/{server,deployclass,deployaudience}.go`,
  `internal/apicheck/deployclass_test.go`, `internal/server/deployevents_test.go`.
  WP-21 put the event filter in `deployaudience.go` (`server.go` is 697 of
  800); its non-primary rows key on `Principal.Deployment`.
- **Tests.** 09-fabric §10's `TestDeploymentRouteClasses`,
  `TestNonPrimaryUsesNewEventTypes`, `TestDeploymentEventsFiltered`
  (non-primary rows, the primary's frame token among the refused),
  `TestNonPrimaryBuildErrorNotBroadcast`, 09-fabric §10's
  `TestPrimaryFrameTokenGetsNoNonPrimaryFacts`.
- **Links.** PO-5; zero-state tiles never meet the class check.

#### WP-49 dormant-cron-bus · L · wave 2.1 · after WP-30
- **Scope** (09-fabric §6, §7; 11-contract §10.2; P13; NP-09-9, NP-09-16,
  NP-09-18). `internal/broker/dormant.go`: the per-deployment files under
  `data/deployments/<TileKey>/<name>/` (05-model §3), the active set, the
  panel's list; dormant cron jobs and bus subscriptions, gated at fire and
  delivery; the deliveries switch, a manager act (`MayManageDeployments`);
  run now, at terminal level; `assignOwner` drops dormant stores.
- **Owns.** `internal/broker/{dormant,cron,bussubs,create}.go`,
  `deploy_dormant_test.go`.
- **Tests.** `TestDormantRegistrations` (cron and bus subtests =
  `TestNonPrimaryCronDormant`, `TestNonPrimaryBusSubDormant`),
  `TestDeliveriesSwitch` (= `TestDeliveriesSwitchManagerOnly`), `TestRunNow`,
  `TestOlderBinaryIgnoresDeploymentFiles` (12-compat PO-9's unit test).
- **Links.** The active set → WP-50, WP-53a. PO-9. `brokerPolicy`
  implements `server.PrimaryPolicy` (`Primary(tile) string`, the plane's
  `Primary`): until then WP-21's event filter treats `main` as the primary.

#### WP-50 dormant-rest-obs · L · wave 2.3 · after WP-47, WP-49
- **Scope** (09-fabric §6's rows, §4.2, NP-09-12, NP-09-13; 11-contract §3.3,
  §8; NP-13-5). Dormant interface instances and ingress hosts; held
  notifications ("would notify"); per-deployment status on `deployments` op
  `status`, never a `status` event; `/logs?deployment=` with its echo, where
  a tile principal reads only its bound deployment's logs
  (`internal/obs/logs.go:42-46` admits `p.Component == comp` today);
  per-deployment prefs.
- **Owns.** `internal/broker/{netfn,ingressfn}.go`, `internal/push/api.go`,
  `internal/obs/{status,logs,prefs}.go`, tests.
- **Tests.** `TestDormantRegistrations` (its interface-instance and
  ingress-host subtests = `TestNonPrimaryIfaceInstancesNeverRouted`,
  `TestNonPrimaryIngressHostsNeverRouted`), `TestNotifyNotPushed` (=
  `TestNonPrimaryNotifyNeverPushed`), `TestStatusPerDeployment` (=
  `TestNonPrimaryStatusNamespaced`), `TestLogsPerDeployment`, 09-fabric §10's
  `TestIfaceInstanceFollowsPrimary`, 08-data §14's
  `TestNonPrimaryLogsAudience`.
- **Links.** PO-5, PO-9; old shells never see non-primary status. WP-21's
  `primarySwap` in `status.go` assumes `main` is the primary until the obs
  plane has WP-30's deployment input.

#### WP-51 term-target · L · wave 2.2 · after WP-32
- **Scope** (P24; 05-model §7; 11-contract §7.4's wire). A session's target,
  fixed for its life and echoed: the primary by default; a protected primary
  never, and a request naming it is refused; the default then falls to the
  live reload target, and when neither exists, to "API off";
  `XBIN_DEPLOYMENT` in both session env paths, set only when the target
  isn't the primary at session start; agent history per tile with a
  `deployment` field (08-data Divergence 2). Clients that send no target
  (the shipped app, an old `bx agent run`, a stale tab) get this default.
- **Owns.** `internal/term/{term,target,sandboxenv,attach,sessions,agent,history}.go`,
  `internal/server/{termsessions,agentapi}.go`,
  `internal/term/{gates,deploy}_test.go`.
- **Tests.** `TestSessionTarget`, `TestTargetGates` (extends
  `TestServeWSGates`), `TestTargetProtectedPrimary` (=
  `TestProtectedPrimaryRefusesTerminalTokens`).
- **Links.** The target → WP-53a, WP-56a, WP-58. PO-3;
  `TestSessionEnvZeroState` unchanged; `term.go` keeps its lowered budget.

#### WP-52 plane-code-ops · L · wave 2.2 · after WP-30, WP-33
- **Scope** (05-model §5, §10; 11-contract §1.4–§1.6; 07-runtime §8.6;
  NP-11-4, NP-11-5). Add (no deployment `N` while a component exists at
  `<tile>+<N>`; its dry run computes the impact without capturing), remove
  (its data through WP-41), promote (the diff and `expect`) and attach, as
  those sections specify, with their authority rows; joining a seeded
  namespace is a manager's act (08-data §6.2).
- **Owns.** `internal/deployments/{ops_code,authz}.go`, tests.
- **Tests.** `TestDeployPlaneOperations`, `TestDeployAuthzMatrix`,
  `TestOperationsNeverWriteWorkTree`, `TestViewAsRefusedEveryOp` (these ops);
  `TestPromoteMovesCodeOnly`, `TestAddDeploymentNameCollision`,
  `TestDeploymentCountCaps`, `TestP19RefusesChromeAndXbinTiles`,
  `TestWorkTreeWritersReachOnlyLiveTarget`.
- **Links.** `deployments/1` joins `features` when these register.

#### WP-53a plane-governance · L · wave 2.3 · after WP-34, WP-44b, WP-47, WP-49, WP-52
- **Scope** (05-model §5, §10; 11-contract §1.7–§1.9; 09-fabric §8;
  06-security T16, NP-06-3, NP-06-8; NP-08-4). Each a manager act in a human
  session (`MayManageDeployments`):
  - reassign the primary (manager-only, routing only, a loud confirm; in v1
    only for a tile in the workspace scope or the sole member of the scope it
    roots): the new primary starts first, then the old one restarts or
    stops; `reload` only for the bare component (C2);
  - protect and unprotect: protecting pins the primary in place, detaches
    live reload from it, and restarts the sessions that target it onto P24's
    default, or ends them;
  - onto a protected primary, reviewed operations name their checkpoint
    (`checkpoint` on deploy and roll back; `expect` on promote, reload now
    and reassignment), otherwise 400, with a compare-and-set on `seq`;
  - the data, delivery, alwaysOn, edge, limit and run-now ops, delegating to
    their planes, run now at terminal level;
  - "not enforced" under `--no-auth`.
- **Owns.** `internal/deployments/{ops_gov,authz}.go`, tests.
- **Tests.** `TestReassignPrimaryAtomic` (=
  `TestReassignMovesActiveRegistrations`), `TestProtectedPrimary`,
  `TestProtectedPromoteNeedsReviewedCheckpoint`,
  `TestReassignPinsReviewedCheckpoint`, `TestProtectionCoversNestedComponents`,
  `TestAlwaysOnNonPrimaryManagerOnly`, `TestNoAuthProtectionMarkedUnenforced`,
  `TestDeploymentLimitsDefaultToTile`; `TestDeployAuthzMatrix` and
  `TestDeployPlaneOperations` (these ops).
- **Links.** PO-15: unprotecting stays allowed while the ship-dark switch is
  off.

#### WP-53b tile-life · M · wave 2.3 · after WP-23b, WP-33
- **Scope** (05-model §11). Lifecycle reaching every deployment (disabling,
  hiding or offloading stops each one; enabling starts the primary, and
  non-primary deployments start on demand); transfer restarting every
  running deployment after WP-23b's owner-ref rewrite.
- **Owns.** `internal/broker/{lifecycle,transfer}.go`, tests.
- **Tests.** `TestDeploymentsAcrossTileLife`.
- **Links.** D39. PO-15.
- **From wave 1.2.** WP-23b's `moveOwner` (transfer.go) rewrites the
  record's owner ref before `SetOwner`, under one mutex. Owner changes
  outside a transfer (deleting a user, which makes their tiles
  workspace-owned) rewrite no record, so those records go inert and paused
  primaries run their work trees again: 05-model §11 names only the
  transfer (a design gap for the owner).

#### WP-54 api-shapes · M · wave 2.3 · after WP-33, WP-52
- **Scope** (11-contract §8; NP-13-4; 06-security T7 item 2). `/backends`
  keeps one key per tile and nests `deployments`; `/runtime` adds
  `deploymentBackends[]`; `/tile-status?deployment=` and
  `/frame-token?deployment=` echo; the `/components` summary stays
  primary-only (it may name the primary, never another deployment, and holds
  no count); `/whoami` reports `deployment`, absent for the primary;
  non-primary rows reach only §0.3's non-primary audience.
- **Owns.** `internal/boot/api.go`, `internal/server/api.go`,
  `internal/broker/whoami.go`, tests.
- **Tests.** `TestNonPrimaryRowsNeedWrite`, `TestReaderSeesPrimaryOnly`;
  `TestZeroStateListings`,
  `TestZeroStateComponentsEntry` unchanged.
- **Links.** PO-11, PO-14; 12-compat C3.

#### WP-55 panel · L · wave 2.1 · after WP-24
- **Scope** (10-ux §3–§6.1, §12, §14.1; NP-10-4). `web/bx-deploy.js` defines
  `<bx-deployments>` and its `testApi()`: rows, overview, actions, the
  promotion diff, the deploy log, add and remove, data, vault, deliveries,
  alwaysOn, primary and protection, the edges table with its per-edge
  refusal and clamp counts, dormant registrations with run now, "would
  notify", the view tab; the M2 strings join `deploy-state.js`.
- **Owns.** `web/{bx-deploy,deploy-state}.js`, `hack/deploy-state.test.mjs`.
- **Tests.** `hack/deploy-state.test.mjs` (M2 rows); `js-check`,
  `theme-check`.

#### WP-56a frame-deployments · L · wave 2.2 · after WP-55, WP-51
- **Scope** (10-ux §2.3, §3.1, §6, §9; 11-contract §2.5; NP-10-2, NP-10-8,
  NP-12-3; P24). The sixth `.lyt` layout, never persisted, and the lazy
  panel. The API dropdown (`web/frame-titlebar.js:162-167`) gains one entry
  per deployment the viewer may reach, never a protected primary; a tile
  with no record, or only an unprotected `main`, renders exactly today's
  `🔌 tile API` and `⛔ no API`; changing the entry restarts the session like
  today's `_setApi`. "target: <name>" in the bar; frames served at a
  deployment URL reload on their deployment's `deployments` ops; target
  notices.
- **Owns.** `web/{bx-frame,frame-titlebar,frame-deploy,frame-testapi,bx-terminal}.js`.
- **Tests.** Harness `agentTab` (six buttons); `livereload` unchanged.
- **Links.** PO-10; the session's echoed target is what the bar shows.

#### WP-56b deployment-aware-components · M · wave 2.2, merged after WP-56a · after WP-55
- **Scope** (10-ux §6, §14; 11-contract §2.5). bx-logs' `deployment`;
  frame-info's qualified `src`; term-sessions' pref; the optional
  `xbin.deployment` and `bx-code` line; the `deployments` pass, which
  asserts a reader's filtered view (no non-primary name), not a 403.
- **Owns.** `web/{bx-logs,frame-info,term-sessions,xbin-client,bx-code}.js`,
  `hack/ui-harness/passes/deployments.js`.
- **Tests.** Harness `deployments` (steps 1–8).
- **Links.** PO-10; the echo rule (a component shows the deployment the
  server echoed, never the one it asked for).

#### WP-57 scaffold · M · wave 2.3 · after WP-54
- **Scope** (13-surfaces §4.17). The admin runtime tab nests non-primary rows
  from `deploymentBackends[]`. Optional entries that tolerate an absent field
  and load new modules by dynamic `import()` with a fallback (12-compat R-9):
  the tile menu's `⇈ Deployments…`, the tile-admin section, the clone text,
  the chips.
- **Owns.** `workspace-template/tiles/admin/tabs/runtime.js`,
  `workspace-template/shell/{menus,bx-tile-admin,bx-canvas,bx-side,shell-kit}.js`,
  `workspace-template/tiles/manager/index.html`, `hack/menus.test.mjs`.
- **Tests.** `hack/menus.test.mjs`; harness `adminTabs admin menus`.
- **Links.** Compat rule 4.

#### WP-58 bx-deployments · L · wave 2.1 · after WP-26
- **Scope** (11-contract §9.2–§9.3; 10-ux §8). `cmd/bx/deployment.go` (`ls`,
  `add`, `rm`, `primary`, `protect`, `seed`, `reset`, `vault-copy`, `set`,
  `edge`, `run-now`, `log`, `diff`); `bx promote`; `--deployment` on `status`
  and `logs`, echo checked; `bx agent run --deployment` through a new file; a
  one-line protected-primary hint in `apiJSON`; `$XBIN_DEPLOYMENT` defaults
  read commands only; the exit codes of WP-26.
- **Owns.** `cmd/bx/{deployment,deploy,status,agentdeploy,main,deployclient}.go`,
  `cmd/bx/deploy_test.go`.
- **Tests.** `TestParseDeploymentArgs`, `TestBxSaysWhereSavesGo` (M2),
  `TestBxHonoursXBINDeployment`, `TestBxRefusalMessages`.
- **Links.** PO-13: `TestBxTodayInvocationsUnchanged` unchanged.
- **From wave 1.2.** `cmd/bx/main.go` is 961 lines, the 90% floor of its
  1068 budget: an edit that removes a line trips the ratchet. A dry run of a
  deploy or rollback onto a protected primary must name its checkpoint, but
  bx learns the capture to name from that dry run (WP-26's finding):
  resolve it before WP-53a, e.g. through a diff's `X-XBin-Checkpoint-To`.

#### WP-59 sdk · S · wave 2.1 · after WP-30
- **Scope** (11-contract §6). `xbin.Deployment()`, `CallerInfo.Deployment`.
- **Owns.** `sdk/xbin.go`, `sdk/deployment_test.go`.
- **Tests.** `TestDeploymentFromEnv`, `TestCallerDeployment`; no new
  `require` in `sdk/go.mod`.
- **Links.** Compat rule 8; /docs/sdk.md text for WP-64.

#### WP-60 old-client-fixtures · S · wave 2.3 · after WP-28, WP-48
- **Scope** (15-test-plan §1 principle 4, §3.12; C2). `isReloadTarget`
  (`web/events-socket.js:40-49`) under node, ancestors included; rows for the
  shipped app's `ReloadTargets`
  (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:160-166`),
  replaying WP-28's tape. Both assert that no `reload`, `build-*` or `status`
  event names a non-primary deployment, so an ancestor's prefix match never
  fires on one.
- **Owns.** `hack/events-socket.test.mjs`,
  `native/ios/Packages/XbinCore/Tests/XbinCoreTests/ClientEventsTests.swift`
  (new rows).
- **Tests.** Both; `make swift-test` where Swift exists, else CI's `native`
  job, as the commit body says.
- **Links.** PO-5.

#### WP-61 itest-data · L · wave 2.4 · after every M2 feature WP
- **Owns.** `test/{deploy_static,deployments}_test.go`.
- **Tests.** `TestStaticDeploymentURLAndData`, `TestDataSeparation`,
  `TestPromoteAndRollBack`, `TestRollBackBound` (every deployment),
  `TestRegistrationsAtStartStayDormant`, `TestSelfCallsStayInDeployment`,
  `TestMultiTileScope`.
- **Links.** WP-27's probe registers no cron job or bus subscription at
  start: `TestRegistrationsAtStartStayDormant` brings its own start-time
  registration.

#### WP-62 itest-fabric · L · wave 2.4 · after every M2 feature WP
- **Owns.** `test/{edges,flowc}_test.go`.
- **Tests.** `TestInboundEdgesReachOnlyPrimary`, `TestOutboundEdgePolicy`,
  `TestReassignPrimaryFlowF`, `TestAgentFlowCWithBxOnly`,
  `TestPrimaryFirstUnderPressure`.

#### WP-63 itest-downgrade-latency · M · wave 2.4 · after every M2 feature WP
- **Owns.** `test/{downgrade,latency}_test.go`.
- **Tests.** `TestDowngradeStatic`, `TestDowngradeDormantRegistrations`
  (`XBIN_DOWNGRADE_BIN`), `TestLatencyLifecycleOps` (M2 rows), the save
  budgets with live reload on `dev`.
- **Links.** The integrator adds CI's download of the previous release.

#### WP-64 docs-m2-wire · L · wave 2.4 · after every M2 feature WP
- **Scope.** [/docs/protocol.md](/docs/protocol.md) (every M2 route and the
  reserved markers M2 fills, qualified URLs and the `+` rule,
  `X-XBin-Deployment`, `XBIN_DEPLOYMENT`, the 6-field token, `?deployment=`
  and echoes, dormant semantics, the `deployments` event and who receives
  it, per-deployment files), [/docs/sdk.md](/docs/sdk.md),
  [/docs/resources.md](/docs/resources.md) (per-deployment declarations and
  limits), [/docs/auth.md](/docs/auth.md) (principals' deployments, the edge
  policy and what the read clamp costs: llm-gw's completions and the agent
  tile's sandbox managers are unavailable to non-primary deployments, the
  vault, tile managers' acts in a human session, leftovers, origin labels,
  the accident boundary of P20).
- **Owns.** Those four files. **Tests.** The docscheck guards,
  `TestDeploymentWording`.

#### WP-65 docs-m2-guides · L · wave 2.4 · after every M2 feature WP
- **Scope.** [/docs/elements.md](/docs/elements.md),
  [/docs/isolation.md](/docs/isolation.md), [/docs/bx.md](/docs/bx.md),
  `docs/overview/{05,06,08,09,10,13,14,15,16}-*.md`,
  [/docs/frontend-kit.md](/docs/frontend-kit.md), [/docs/compat.md](/docs/compat.md),
  [/docs/maintenance.md](/docs/maintenance.md) (the new guards and pass),
  `workspace-template/AGENTS.md`, the builder page (its limits section names
  the blocked edges).
- **Owns.** Those files. **Tests.** The docscheck guards,
  `TestDeploymentWording`.

#### WP-66 checkpoint-purge · M · wave 2.3 · after WP-11, WP-12 and a route amendment · gated by R-4
- **Scope** (05-model §2, §10; 06-security T20, NP-06-16). A tile manager,
  in a human session, removes an unused checkpoint from every deploy log,
  its objects are pruned at once, and the purge is logged.
- **Owns.** `internal/checkpoint/purge.go`, `internal/deployments/ops_purge.go`,
  tests.
- **Tests.** `TestPurgeManagerOnly`, `TestPurgeRefusesDeployedCheckpoint`,
  `TestPurgeRemovesObjects`.

### 2.5 Shared sandbox mechanics (WP-S), with D113

Tile deployments are not built on D113's tile-managed sandboxes (the owner's
direction, [05-model.md](05-model.md) §12). They change the layer one level
up, which backends, terminals and D113's sandboxes share: the registry, the
VM books, the cgroup tree, the launch spec, confine and the sandbox init.
WP-S is that work, one owner role in seven packages across the milestones.
The D113 project ([../tile-sandboxes.md](../tile-sandboxes.md)) may run at
the same time and needs the same cgroup parent and registry fields, so four
rules hold:

1. **One declaration in one place** (D109). Each shared field, type or
   function is declared once to [07-runtime.md](07-runtime.md) §10's
   specification; the other project's owner reviews that commit before it
   merges.
2. **Split files.** The projects edit different files (below). Where they
   meet, one declares and the other adds a field to it, never a second
   signature.
3. **Either order.** Each rebases on whatever landed first, and both
   projects' suites run after the merge: a clean textual merge proves nothing
   (`native/AGENTS.md:375-383`).
4. **No single backend.** D113 must not assume one backend per tile: an
   exec's cursor is valid only in the deployment that created it (07-runtime
   §10.7).

| Piece | Files | This feature builds | D113 builds |
|---|---|---|---|
| Registry fields | `internal/sbx/sbx.go` (`Entry` `:50-74`, `Filter` `:77-80`, `Failure` `:97`) | `Deployment` on non-main entries (no `Checkpoint` field), `Filter.Deployment`, `Failure.Deployment` in the coalescing key (WP-S0) | its `tile` entries' fields; `Deployment` on them once WP-S5 applies |
| Backend rows | `internal/runner/sbx.go`, `internal/boot/sandboxes.go`, the admin tab and its pass | non-main IDs, leaves and rows; `main`'s rows unchanged (WP-S0, WP-S4) | its routes in their own file, not `sandboxes.go` (requested) |
| VM books | `internal/vm/reserve.go` (options); `internal/vm/policy.go` (policy fields; `Reserve` `:132`, `UsedBy` `:171`) | `Reserve(owner, memMiB, opts...)`, headroom and primary-first options (WP-S0, WP-S3) | `tiles` and `tilesBudgetMiB`, and its sub-budget as another option |
| cgroup tree | `internal/cgroup/*` (today's leaf, `cgroup_linux.go:89`) | the nested layout, parents, `ProcsTree`, the sweep (WP-S2) | its `sbx-<name>` leaves under `tile-<CompKey>/d-<dep>/` |
| confine | `internal/confine/confine.go` | `DirFrom`, `At` (WP-S1); streaming writers (WP-S6) | nothing |
| Sandbox init | `internal/sandbox/*` | symlink-free mountpoints under a bind destination, a `Src ≠ Dst` test (WP-S1) | `Spec.AgentFD` |
| Tile sandboxes per deployment | D113's manager | the specification (WP-S5) | the code, if D113 lands after M2 |

The cgroup parent is the piece both want soonest. This programme builds it
(WP-S2), and D113 reuses it (05-model §12). If D113's nested-cgroup step is
scheduled before wave 2.1, the integrator pulls WP-S2 forward unchanged,
rather than letting D113 build a second layout.

#### WP-S0 sbx-decl · S · wave 0.2 · after WP-00 · on master
- **Scope.** The registry fields above; `Reserve` gains variadic options in
  `internal/vm/reserve.go`, none honoured yet; the D112 sandboxes tab keys
  the current generation by `(tile, deployment ?? '')` (NP-12-8), and its
  pass asserts `main`'s rows carry no deployment field.
- **Owns.** `internal/sbx/sbx.go`, `internal/sbx/filter_deploy_test.go`,
  `internal/vm/{policy,reserve}.go`,
  `workspace-template/tiles/admin/tabs/sandboxes.js`,
  `hack/ui-harness/passes/sandboxes.js`.
- **Tests.** `TestFilterDeployment`; harness `sandboxes`;
  `TestEntryJSONZeroState`, `TestReserveAttribution` unchanged.
- **Links.** PO-11. D113's owner reviews.

#### WP-S1 confine-dirfrom · M · wave 1.1 · after WP-03
- **Scope** (07-runtime §10.5; NP-07-10). `confine.Cmd.DirFrom`,
  `confine.At(src, dst, ro)`, `ErrNeedsIsolation`; `binds()`
  (`internal/confine/confine.go:218-224`) binds `DirFrom` at `Dir`; a direct
  run (`:140-143`) with `DirFrom ≠ Dir`, or with a bind made by `At`, returns
  `ErrNeedsIsolation` and never runs; binds callers build by hand keep
  today's direct-run behaviour (16-open-questions Q22). In the sandbox init,
  `mountBind` (`internal/sandbox/init_linux.go:391`, today `os.MkdirAll` at
  `:401` and `:405`) creates a mountpoint that lies under another bind's
  destination with openat2 and `RESOLVE_NO_SYMLINKS | RESOLVE_BENEATH`, so a
  nested component bound inside a checkpoint never follows a symlink; binds
  outside another bind keep today's path.
- **Owns.** `internal/confine/{confine.go,bind_test.go,rebind_linux_test.go}`,
  `internal/sandbox/init_linux.go` (`mountBind`),
  `internal/sandbox/bind_srcdst_linux_test.go`.
- **Tests.** `TestDirectModeRefusesRebind`, `TestConfineBindDestinationDefault`
  (defaults to `Dir`); confined `TestRebindDirInSandbox`, `TestBindSrcNotDst`.
- **Links.** `DirFrom` and the nested-mount rule → WP-17, whose confined
  `TestNestedComponentBoundAfterCheckpoint` exercises both. PO-11.

#### WP-S2 cgroup-parent · M · wave 2.1 · after WP-30
- **Scope** (07-runtime §10.3; NP-07-5, NP-13-2, NP-06-14). A name holding
  `/` is a path under the base; parents made with `subtree_control` before
  their leaf, removed bottom-up once empty; `ProcsTree`; hierarchical usage
  and limit readers; a boot sweep of empty `tile-*` subtrees; weights (tile
  100; primary 100, non-primary 50); `memory.low` on the primary's node;
  stubs in `cgroup_other.go`.
- **Owns.** `internal/cgroup/{cgroup_linux,cgroup_other}.go`,
  `parent_linux_test.go`.
- **Tests.** `TestNonPrimaryCgroupSubtree`, `TestPrimaryLeafKeepsCaps`;
  `TestWriteLimits` unchanged.
- **Links.** → WP-34, WP-S4, D113. PO-11: the flat leaf stays for zero-state
  and main-only tiles.

#### WP-S3 vm-books · M · wave 2.1 · after WP-S0
- **Scope** (07-runtime §10.2; NP-07-6, NP-02-12). Headroom: a non-primary
  reservation leaves the primary's guest size free. Primary first: when the
  budget can't admit a primary start, a runner-supplied callback stops the
  tile's non-primary guests; a non-primary start is refused with
  `sbx.Refuse`, never preempting. `UsedBy` stays per tile.
- **Owns.** `internal/vm/{policy,reserve}.go`, `reserve_deploy_test.go`.
- **Tests.** `TestReserveChargesTileForEveryDeployment`,
  `TestNonPrimaryVMCannotStarvePrimary`; `TestReserveAttribution` unchanged.
- **Links.** → WP-34.

#### WP-S4 registry-rows · M · wave 2.2 · after WP-33, WP-S2
- **Scope** (05-model §12; 07-runtime §10.1; 11-contract §8's `/sandboxes`
  row). `main` keeps `backend:<CompKey>:g<gen>` (`internal/runner/sbx.go:63`)
  with no `Deployment` field, even when the tile has a record; other
  deployments get `backend+<name>:<CompKey>:g<gen>` with `Entry.Deployment`
  set; `Leaf` from the generation; failures carry the deployment;
  `/sandboxes?deployment=`, `stats.scope: "deployment"`; the admin tab groups
  by (tile, deployment); the pass gains a non-primary generation.
- **Owns.** `internal/runner/sbx.go`, `internal/boot/sandboxes.go`, the admin
  `tabs/sandboxes.js`, `passes/sandboxes.js`,
  `internal/runner/sbx_deploy_test.go`, `internal/boot/sandboxes_test.go`.
- **Tests.** `TestSandboxEntriesPerDeployment` (=
  `TestRegistryRowsPerDeployment`), `TestSandboxesDeploymentRows`; harness
  `sandboxes`.
- **Links.** PO-11: `main`'s rows stay byte-identical.

#### WP-S5 tile-sandboxes-per-deployment · M · wave 2.4, or in D113's project

> **Handed over (2026-09-27).** The coding-sandboxes programme (branch `sandbox-managers`) builds this rule in its phase 2, where the tile-sandbox runtime itself is built. It has recorded the rule in `plans/sandbox-managers.md` (554c1c9): per-deployment sandbox sets; `{source:true}` and data mounts from the deployment; caps, policy, quotas and VM books stay the tile's; registry rows carry the deployment. This programme only documents the rule, and WP-S5 is not scheduled in wave 2.4. Whoever lands first on `internal/sbx`, `internal/cgroup` or `internal/vm` rebases the other.
- **When.** With whichever of M2 and D113 lands second, as
  [02-goals.md](02-goals.md)'s milestone table says.
- **Scope** (05-model §12; 07-runtime §10.7; 08-data §2). D113's definitions
  and state per deployment (`main` keeps `data/sandboxes.json` and
  `.xbin/sbx/<CompKey>/`; others beside their record and under
  `.xbin/deploy/<TileKey>/d/<name>/sbx/`); `{"source":true}` mounts
  `CodeRoot(tile, dep)`; resource mounts through the remap; a non-primary
  backend drives only its own set; `cap:sandboxes` and the caps stay the
  tile's.
- **Owns** (provisional, re-cut when D113's implementation names its files).
  D113's manager package, its API file, and the per-deployment state paths
  above.
- **Tests.** `TestTileSandboxesPerDeployment`,
  `TestTileSandboxMountsDeploymentNamespace`, `TestAttachTicketBindsDeployment`.

#### WP-S6 confine-streaming · M · wave 3.1 · after the M2 exit
- **Scope** (07-runtime §10.6; NP-13-9). `Cmd.Stdout` and `Stderr` writers,
  used instead of the buffered `Result` (`internal/confine/confine.go:87-89`)
  and the capped writer (`:251`); an output cap that ends the run with a loss
  marker; TERM then KILL on timeout; D78's guarantees unchanged; no D113 code.
- **Owns.** `internal/confine/confine.go`, tests. **Tests.** A loss-marker
  test and a timeout test, named in the M3 card.

### 2.6 M3 — later rungs (outlines)

- **WP-70 tracked-branch (L).** The tracked-branch config in the record; a
  ref poll, since the watcher ignores `.git` (`internal/watch/watch.go:61-74`);
  a confined fetch into a quarantine, `fsck --strict`, admission; refused
  onto a protected primary (NP-06-7); "committing deploys nothing" kept true,
  or CM-2's guidance changed in the same release (12-compat G4). Tests:
  `TestTrackedBranchReadsGitOnlyInConfine`,
  `TestTrackedBranchRefusedOnProtectedPrimary`, a trigger test.
- **WP-71 deploy-remote (L), after WP-S6.** `receive-pack --stateless-rpc` in
  the streaming confine; bearer-only; refs limited to `deploy/<name>` of
  existing deployments; a per-push quarantine; size caps. Tests:
  `TestDeployRemoteBearerOnly`, `TestDeployRemoteHostilePack`,
  `TestDeployRemoteRefNamespace`.
- **WP-72 edge-match (L).** `match` on the existing edge record, behind a
  `features` string; provider consent; per-deployment rosters; the P7
  exception ([09-fabric.md](09-fabric.md) §9).
- **WP-73 materialize-diff (M).** Differential materialization (NP-07-8),
  gated on its benchmark; pulled into M2 if SC-LATENCY-OPS's large-tree case
  fails there.
- **WP-74 test-hostnames (M, optional).** A manager-routed test hostname for
  a named deployment ([05-model.md](05-model.md) §15).
- **WP-75 docs-m3 (M).** The feeds and `match` in the builder docs.

## 3. Waves, dependencies and file ownership

### 3.1 Waves

A wave's WPs run in parallel. The integrator merges them one at a time in
the order listed, then runs the gate.

| Wave | Base | WPs, in merge order | Gate |
|---|---|---|---|
| 0.1 | master | WP-00 | `make check` |
| 0.2 | master | WP-S0, WP-04, WP-03, WP-02, WP-01a, WP-01b | `make check`, `make integration`, a fresh-seed harness run (A/B) |
| — | | the integrator cuts `dev-lifecycle` and commits Phase 0 (PRE-2) | `make check` |
| 0.3 | `dev-lifecycle` | WP-05, WP-07, WP-08 | `make check` |
| 0.4 | | WP-06 | the M0 exit |
| 1.1 | | WP-24, WP-27, WP-S1, WP-10, WP-13, WP-14a, WP-18, WP-21, WP-22 | `make check`, `make integration` |
| 1.2 | | WP-11, WP-12, WP-17, WP-16a, WP-16b, WP-19, WP-20, WP-23, WP-23b, WP-14b, WP-15, WP-25, WP-26 | the same, and harness `livereload` |
| 1.3 | | WP-28, WP-29 | the M1 exit |
| 2.0 | | WP-30 | `make check` |
| 2.1 | | WP-31, WP-32, WP-35, WP-39, WP-59, WP-S2, WP-S3, WP-33, WP-49, WP-55, WP-58 | `make check`, `make integration` |
| 2.2 | | WP-34, WP-40, WP-41, WP-43, WP-47, WP-36, WP-37, WP-51, WP-S4, WP-52, WP-56a, WP-56b | the same, and harness `deployments` |
| 2.3 | | WP-38, WP-42, WP-44a, WP-44b, WP-46, WP-48, WP-50, WP-53a, WP-53b, WP-54, WP-57, WP-60, WP-66 | the same |
| 2.4 | | WP-61, WP-62, WP-63, WP-45, WP-S5, WP-64, WP-65 | the M2 exit |
| 3.1 | | WP-S6, WP-70, WP-72, WP-73, WP-74 | `make check`, `make integration` |
| 3.2 | | WP-71, WP-75 | the M3 exit |

### 3.2 Dependency graph

The cards' "after" lists are authoritative; this is the backbone.

```
M0a  WP-00 ─┬─ WP-01a, WP-01b, WP-02, WP-03, WP-04, WP-S0 ─┐
            │                                               ├─ cut dev-lifecycle, Phase 0
M0b         └───────────────────────────────────────────────┘   WP-05, WP-07, WP-08 ── WP-06 ── M0 exit

M1   WP-13 ── WP-14a ─────┬─ WP-14b ── WP-15 ─┬─ WP-25 ─┐
     WP-10 ─┬─ WP-11 ─────┤                   └─ WP-26 ─┼─ WP-28, WP-29 ── M1 exit
            ├─ WP-12 ─────┼─ WP-20                      │
            └─ WP-23      │                             │
     WP-01a ─ WP-16a, 16b ┘   WP-01b, WP-S1 ── WP-17    │
     WP-13 ── WP-23b   WP-18 ── WP-19   WP-24 ── WP-25  │
     WP-21, WP-22, WP-27 ───────────────────────────────┘

M2   WP-30 ─┬─ WP-31, WP-32 ─┬─ WP-36 ── WP-38
            │                ├─ WP-37 (also after WP-33)
            │                └─ WP-51 ── WP-56a (also after WP-55) ── WP-56b
            ├─ WP-33 ─┬─ WP-34 (also after WP-S2, WP-S3)
            │         ├─ WP-S4 (also after WP-S2)
            │         ├─ WP-52 ─┬─ WP-53a (also after WP-34, WP-44b, WP-47, WP-49)
            │         │         └─ WP-54 ── WP-57
            │         ├─ WP-53b (also after WP-23b)
            │         └─ WP-48 (also after WP-32) ── WP-60
            ├─ WP-39 ─┬─ WP-40 ─┬─ WP-42
            │         ├─ WP-41 ─┴─ WP-44a ── WP-45;  WP-41 ── WP-44b
            │         ├─ WP-43
            │         └─ WP-47 ── WP-50 (also after WP-49)
            ├─ WP-49 ── WP-46 (also after WP-39, WP-23b)
            └─ WP-35, WP-59, WP-S2, WP-S3
     WP-24 ── WP-55 ── WP-56a      WP-26 ── WP-58
     every M2 feature WP ── WP-61, WP-62, WP-63, WP-64, WP-65 ── M2 exit
```

**Critical path** in agent-days, not counting the integrator's gates:
- M0: WP-00 (S) → WP-01a (L) → WP-05 (L) → WP-06 (M), about 5.5.
- M1: WP-13 (M) → WP-14a (M) → WP-14b (L) → WP-15 (M) → WP-28 (L), about 7;
  WP-10 (L) → WP-11 (M), and WP-16a and WP-16b (L each, in parallel), run
  beside it and merge before WP-14b.
- M2: WP-30 (M) → WP-33 (L) → WP-52 (L) → WP-53a (L) → WP-61 (L), about 9.

Waves 1.2 and 2.1 to 2.3 hold eleven to thirteen WPs each, so keeping to the
path takes that many agents.

### 3.3 File-ownership matrix

Files that one WP owns for the whole plan appear only on its card. These are
the files with several owners. Each row shows at most one owner per wave,
which proves that no two WPs in one wave own the same file.

| File | Owners, by wave |
|---|---|
| `internal/runner/runner.go` | 0.1 WP-00 · 0.2 WP-01a · 0.3 WP-05 · 1.2 WP-16a · 2.1 WP-33 · 2.2 WP-34 |
| `internal/runner/states.go` | 0.1 WP-00 · 0.2 WP-01a · 0.3 WP-05 · 1.2 WP-16b · 2.1 WP-33 |
| `internal/runner/sandboxcmd.go` | 0.1 WP-00 · 0.2 WP-01b · 0.3 WP-05 · 1.2 WP-17 · 2.1 WP-35 |
| `internal/runner/build.go` | 0.1 WP-00 · 0.3 WP-05 · 1.2 WP-17 · 2.1 WP-35 |
| `internal/runner/env.go` | 0.3 WP-05 · 1.2 WP-17 · 2.1 WP-35 |
| `internal/runner/deploy.go` | 0.3 WP-05 · 1.2 WP-16b · 2.1 WP-33 |
| `internal/runner/{alwayson,vm}.go` | 1.2 WP-16b · 2.2 WP-34 |
| `internal/runner/inspect.go` | 1.2 WP-16a · 2.1 WP-33 |
| `internal/runner/limits.go` | 2.0 WP-30 · 2.2 WP-34 |
| `internal/term/term.go` | 0.1 WP-00 · 0.3 WP-05 · 2.2 WP-51 |
| `internal/term/sandboxenv.go` | 0.1 WP-00 · 1.1 WP-22 · 2.2 WP-51 |
| `internal/term/deploy_test.go` | 0.2 WP-02 · 2.2 WP-51 |
| `internal/term/target.go` | 2.0 WP-30 · 2.2 WP-51 |
| `cmd/bx/{main,status}.go` | 0.1 WP-00 · 1.2 WP-26 · 2.1 WP-58 |
| `cmd/bx/{deploy,deployclient}.go` | 1.2 WP-26 · 2.1 WP-58 |
| `cmd/bx/deploy_test.go` | 0.2 WP-02 · 1.2 WP-26 · 2.1 WP-58 |
| `web/bx-frame.js` | 0.1 WP-00 · 1.2 WP-25 · 2.2 WP-56a |
| `web/frame-testapi.js` | 0.1 WP-00 · 0.3 WP-08 · 1.2 WP-25 · 2.2 WP-56a |
| `web/{frame-titlebar,frame-deploy,bx-terminal}.js` | 1.2 WP-25 · 2.2 WP-56a |
| `web/deploy-state.js`, `hack/deploy-state.test.mjs` | 1.1 WP-24 · 2.1 WP-55 |
| `hack/ui-harness/passes/livereload.js` | 0.3 WP-08 · 1.2 WP-25 |
| `hack/ui-harness/passes/deployments.js` | 0.3 WP-08 · 2.2 WP-56b |
| `hack/ui-harness/passes/sandboxes.js`, admin `tabs/sandboxes.js` | 0.2 WP-S0 · 2.2 WP-S4 |
| `hack/menus.test.mjs` | 0.3 WP-08 · 2.3 WP-57 |
| `internal/boot/boot.go` | 0.3 WP-07 (one line) · 0.4 WP-06 · 2.0 WP-30 |
| `internal/boot/serve.go` | 0.4 WP-06 · 1.2 WP-20 |
| `internal/boot/deployments.go` | 0.3 WP-07 · 0.4 WP-06 · 1.2 WP-15 |
| `internal/boot/deployreads.go` | 1.2 WP-15 |
| `internal/boot/deploystate_test.go` | 0.2 WP-02 · 1.3 WP-28 |
| `internal/server/{static,tileassets,native,deployserve}.go` | 1.2 WP-19 · 2.2 WP-36 |
| `internal/server/api.go` | 1.2 WP-19 · 2.3 WP-54 |
| `internal/server/server.go` | 1.1 WP-21 · 2.3 WP-48 |
| `internal/server/deployaudience.go` | 1.1 WP-21 · 2.3 WP-48 |
| `internal/server/deployevents_test.go` | 0.2 WP-02 · 2.3 WP-48 |
| `internal/proxy/proxy.go` | 1.2 WP-19 · 2.2 WP-37 |
| `internal/proxy/deploy_test.go` | 0.2 WP-02 · 2.2 WP-37 |
| `internal/auth/auth.go` | 0.3 WP-05 · 2.1 WP-32 |
| `internal/auth/deployment_test.go` | 0.2 WP-02 · 2.1 WP-32 |
| `internal/registry/registry.go` | 0.3 WP-05 · 1.1 WP-18 |
| `internal/deployments/plane.go` | 0.4 WP-06 · 1.1 WP-13 · 1.2 WP-14b · 2.0 WP-30 |
| `internal/deployments/worktree.go` | 1.2 WP-20 |
| `internal/deployments/authz.go` | 1.1 WP-14a · 2.2 WP-52 · 2.3 WP-53a |
| `internal/broker/broker.go` | 0.3 WP-05 · 2.0 WP-30 · 2.1 WP-39 · 2.2 WP-47 |
| `internal/broker/deploydata.go` | 2.0 WP-30 · 2.1 WP-39 |
| `internal/broker/resources.go` | 1.1 WP-18 (`Provision`) · 2.1 WP-39 · 2.2 WP-40 |
| `internal/broker/resenc_wire.go` | 2.1 WP-39 · 2.2 WP-40 · 2.4 WP-45 |
| `internal/broker/backup.go` | 1.2 WP-23 · 2.1 WP-39 · 2.3 WP-44a · 2.4 WP-45 |
| `internal/backup/backup.go` | 1.2 WP-23 · 2.3 WP-44a · 2.4 WP-45 |
| `internal/broker/backup_deploy.go` | 1.2 WP-23 · 2.3 WP-44a |
| `internal/broker/{diskmon,resusage}.go` | 2.1 WP-39 · 2.3 WP-44b |
| `internal/broker/deploy_env_test.go` | 0.2 WP-03 · 2.2 WP-40 |
| `internal/broker/netfn.go` | 2.2 WP-47 · 2.3 WP-50 |
| `internal/broker/transfer.go` | 1.2 WP-23b · 2.3 WP-53b |
| `internal/broker/create.go` | 1.2 WP-23b · 2.1 WP-49 · 2.3 WP-46 |
| `internal/broker/policy.go` | 1.2 WP-23b · 2.3 WP-46 |
| `internal/obs/status.go` | 1.1 WP-21 · 2.3 WP-50 |
| `internal/vm/{policy,reserve}.go` | 0.2 WP-S0 · 2.1 WP-S3 |
| `internal/confine/confine.go` | 1.1 WP-S1 · 3.1 WP-S6 |
| `internal/confine/bind_test.go` | 0.2 WP-03 · 1.1 WP-S1 |
| `test/livereload_test.go` | 0.2 WP-03 · 1.3 WP-28 |
| `test/latency_test.go` | 0.2 WP-04 · 1.3 WP-28 · 2.4 WP-63 |
| `test/deployments_test.go` | 1.3 WP-28 · 2.4 WP-61 |
| `test/flowc_test.go` | 1.3 WP-28 · 2.4 WP-62 |
| `docs/protocol.md` | 0.3 WP-07 (API-fence rows) · 1.3 WP-29 · 2.4 WP-64 |
| `docs/{elements,isolation,bx,frontend-kit,compat,maintenance}.md`, `docs/overview/{09-terminals,14-lifecycle,15-operations}.md`, `workspace-template/AGENTS.md`, `docs/tile-deployments.md` | 1.3 WP-29 · 2.4 WP-65 |

**The C5 guard after wave 0.2.** `internal/confine/nofollow_test.go`
(WP-04) has one owner per wave like any file here, but its behavioural half
must drive operations that land in several WPs of one wave: capture (WP-10),
materialize and GC (WP-11), diff and drift (WP-12), restore (WP-23,
WP-44a) and purge (WP-66). After wave 0.2 the file is the integrator's. Each
of those WPs writes its hostile-tree case in a test file of its own package
and names it in its report's **Tests:**; the integrator wires it into
`TestNoFollowingHostWalks` at the merge (an exported hook in a
`confine_test` file, since package `confine` cannot import the packages that
use it). The static half needs no edit: its scope already lists every
package, and an entry that doesn't exist yet is skipped.

**Seams where a clean merge proves nothing.** After merging the second WP of
each pair, run both WPs' suites: WP-16a and WP-16b (`CodeFor` through the
deploy worker), WP-16a and WP-17 (`Code` through `build`), WP-14a and WP-14b
(`Recheck` at commit), WP-33 and WP-34 (the `start` network block), WP-39 and
WP-40 (the key function's callers), WP-47 and WP-50 (`netfn.go`), WP-52 and
WP-53a (`authz.go`), WP-23b and WP-46 (`create.go`, `policy.go`), WP-23b and
WP-53b (`transfer.go`), WP-S2 and WP-34 (leaf names), WP-S3 and WP-34 (the
preempt callback).

## 4. Integration model

### 4.1 Branches and worktrees

- **M0a** WPs branch from master and merge back into master (NP-14-1): they
  change no behaviour, and landing the budget splits early keeps concurrent
  work on master from colliding with them later.
- **The integration branch** `dev-lifecycle` is cut from master after wave
  0.2. Its first commit is Phase 0, the design set as plans only (the b0787cd
  precedent).
- **Every other WP** branches `dl/<slug>` from the head the integrator names,
  in its own worktree: `git worktree add /home/magik6k/buxon-wt/<slug> -b
  dl/<slug> dev-lifecycle`.
- Nothing happens in `/home/magik6k/buxon`, where another agent works.

### 4.2 The integrator

One agent, or the owner. Only the integrator merges WPs; syncs master into
the integration branch at every wave boundary (the 72e2ef6 precedent; master
already moved, PRE-1); edits §2.1 clause 3's files and makes contract
amendments (§4.4); runs the gates (§3.1); writes the records (§4.5); and
moves master (§4.6).

### 4.3 Merging a WP

1. Merge `dl/<slug>` with `--no-ff`, in the wave's order; list conflicts
   under "Conflicts:" in the merge body, as the native swarm did (6b7f652,
   02d2bab).
2. Run `make check`; `make integration` when runner, sandbox, cgroup, VM,
   broker, confine, proxy or term code changed, with `XBIN_TEST_ROOTFS` and
   the agent's tool sandbox off; the WP's harness passes on a fresh seed.
3. After the second WP of a seam pair (§3.3), run both WPs' suites.
4. A red gate is fixed on the WP's branch and merged again; the integrator
   never fixes a WP's code inside a merge commit.
5. A WP that builds a route or parameter WP-07 reserved: in the same merge the
   integrator drops that row's reserved marks in `openapi.go` (the
   **Reserved** prefix, `x-xbin-reserved` and the 501 response), since
   `TestDeploymentRoutesReserved` (`internal/boot/deployments_reserved_test.go`)
   drives every row still marked and wants 501. protocol.md's "(reserved)"
   markers stay for the docs WPs (WP-29, WP-64). That test file goes with
   `internal/boot/deployments.go`: WP-06 in wave 0.4 (its
   `registerDeploymentsAPI(srv)` call changes when the plane is passed in),
   WP-15 in wave 1.2 (WP-07's report).

### 4.4 Contract amendments

A WP that needs a declaration, a route, an openapi or protocol row, a
`boot.go` line, or an edit outside its Owns list stops and asks. The
integrator makes the amendment in its own commit, or hands it to the next
contracts WP, and every open WP rebases. Feature WPs never add routes or
parameters: `TestRouteInventory` would force them into protocol.md and
`openapi.go`, the native swarm's worst conflict files
([research/delivery-infra.md](research/delivery-infra.md) §0).

### 4.5 Records at a milestone exit

After e1100f6, b81f93c and e32df4f, the integrator:
1. collects the **Decision:** hand-offs and gets the owner's ratification of
   the P-numbers and NP-numbers the milestone implemented;
2. assigns D-numbers: the next free ones after the highest on master at that
   moment, never a number another branch holds. Each becomes a bold-ID bullet
   in [../DECISIONS.md](../DECISIONS.md);
3. rewrites every `(Pn)` in Go comments to its D-number, and adds the
   citations to the docs;
4. writes the changelog entry from the **Changelog:** hand-offs, starting
   from [12-compat.md](12-compat.md) §10.4's draft and its downgrade note;
5. checks 12-compat §10.2's tripwires: none crossed means no migration note
   and no BREAKING mark; a crossed one gets `docs/changes/YYYY-MM-DD-<slug>.md`
   with What changed, Who's affected, How to migrate and Why, linked, and the
   entry marked BREAKING;
6. updates the status lines of [README.md](README.md) and the implemented
   documents;
7. runs `make check`, `make integration` with the rootfs, the full fresh
   harness, and the milestone's manual checklist items.

### 4.6 Master and releases

The integration branch reaches master only at a milestone exit, after its
records commit. Pushing and releasing stay the owner's; a release is cut from
a clean worktree with `make release`. M1 may ship alone: its M2 routes and
parameters then answer 501 or stay unread, documented as reserved, and
`features` lists only `live-reload/1` (NP-14-4).

## 5. Swarm operating notes

**Variables for a WP's worktree.** A worktree has no `.rootfs`,
`bin/gocryptfs` or Playwright (`hack/ui-harness/run.sh:80-91`), so they come
read-only from the main checkout
([research/delivery-infra.md](research/delivery-infra.md) §1.5):

```sh
export PORT=$((8700 + 20 * N))    # WP-N; suffix b adds 10; WP-S<k> uses 7700 + 20*k (NP-14-7)
export HARNESS_DIR=<the agent's scratchpad>/harness-<slug>
export PLAYWRIGHT_DIR=/home/magik6k/lcad-wasm
export XBIN_TEST_ROOTFS=/home/magik6k/buxon/.rootfs
export XBIN_GOCRYPTFS=/home/magik6k/buxon/bin/gocryptfs
export XBIN_FUSE_OVERLAYFS=/home/magik6k/buxon/bin/fuse-overlayfs
```

- **Ports.** `run.sh` derives the ingress port (`PORT+1`) and fakeopenai's
  (`PORT+10280`), and `stop()` kills by port and workspace path
  (`hack/ui-harness/run.sh:22`, `:25`, `:53-66`): a shared port kills another
  agent's harness. The grid's step of 20, with 10 for a `b` half, keeps the
  derived ports apart; the WP-S grid (7700–7820, fakeopenai 17980–18100)
  sits below the WP grid (8700–10210, fakeopenai 18980–20490) and clear of
  `run.sh`'s default 8697.
- **Done is `make check`** (`Makefile:136`; [/docs/maintenance.md](/docs/maintenance.md)
  "Definition of done"), which CI runs before `make integration`
  (`.github/workflows/ci.yml:41-43`). gofmt must match go.mod's minor, as the
  pins check enforces.
- **Confined and isolated tests** need user namespaces and a git-bearing
  rootfs, and skip with the reason otherwise
  (`internal/confine/sandbox_linux_test.go:26-35`). They need the agent's
  tool sandbox off, since it refuses the `/proc/self/exe` re-exec
  (15-test-plan §10.2). CI skips them until R-7 lands.
- **The harness isn't in CI, so WPs report it honestly:** every PASS, FAIL
  and SKIP line from `$HARNESS_DIR/out/<pass>.txt`; each SKIP's reason; for a
  FAIL that looks pre-existing, an A/B run against the integration branch's
  head, named as such (the D88 precedent). A failure that moves to a
  different pass on each run comes from the box, not the code. The gate is a
  fresh-seed run, never `--shots` on a reused workspace.
- **Harness habits.** Never pipe `run.sh --keep` or `--restart`: xbind holds
  the pipe open; read the out files. Stop a hand-started xbind by PID, since a
  `pkill -f` pattern can match the calling shell. A read-only Go module cache
  needs `GOMODCACHE` pointed at a writable directory.
- **Copies of an example in the shared daemon's workspace.** An integration
  test that copies a Go example into `test/`'s shared workspace gives the copy
  a module path of its own: `go.work` refuses two `module counter` lines, and
  every Go build in that workspace then fails (seen in WP-03 and WP-04;
  `TestGoBackendLifecycle`'s copy keeps `counter`). WP-28, WP-61 and WP-62
  follow this.
- **Harness scripts under a running harness.** Never edit `run.sh` or
  `seed.sh` in a worktree while a harness run there is executing them: bash
  reads a script as it runs it. `HARNESS_ISOLATE=1` is for the passes that ask
  for it (`livereload`, `deployments`): the agent passes' scripted fake agent
  is a host path the tile sandbox can't see (WP-08's report).
- **A WP that finds the design wrong stops and reports.** It doesn't
  improvise another design, raise a budget, or edit outside its Owns list.

## 6. Risks, rollback and shipping dark

### 6.1 Risk register

| # | Risk | Mitigation | Watched by |
|---|---|---|---|
| K1 | Two WPs collide on a hot or budgeted file | M0's splits; §3.3's matrix; amendments only through the integrator | integrator |
| K2 | A zero-state tile changes (P5) | goldens first, on master, never regenerated; SC-ZERO gates every milestone | WP-01a to WP-03, every gate |
| K3 | The default save path slows down | M0's baseline; per-save counters (seam row 30, `TestWatchLoopZeroStateNoStoreIO`); SC-LATENCY-DEFAULT at each exit | WP-04, WP-20, WP-63 |
| K4 | An old client (an ancestor frame, the shipped app, an old shell) reacts to non-primary activity | C2: no old event type ever names a non-primary deployment; `eventComp` is the one switch; the old-client fixtures replay a real tape | WP-16b, WP-33, WP-60 |
| K5 | Confined and isolated tests pass only on a developer's box | R-7's CI rootfs in M0; meanwhile, local runs reported in commit bodies | integrator |
| K6 | D113 collides with the shared layer | §2.5's split files, cross-review; the cgroup parent built once, by WP-S2 | WP-S owner, D113's owner |
| K7 | Parallel WPs grow duplicate stubs (the D109 failure) | WP-05 to WP-07 and WP-30 declare every cross-WP interface once | integrator |
| K8 | git runs unconfined on tile data, or a symlink is followed | `TestNoDirectExec`, `TestNoFollowingHostWalks`, the hostile-repo tests, 06-security's ledger as the checkpoint WPs' review list | WP-04, WP-10 to WP-12 |
| K9 | A seed, reset or removal loses data | holds; unmount verified before removal; last-claimant deletion; manager-only acts | WP-41, WP-42 |
| K10 | A flaky harness hides a regression or fakes one | fresh-seed gates, A/B against the branch head, numbers never loosened | every UI WP |
| K11 | The integration branch drifts from master | M0a on master; master merged at every wave boundary | integrator |
| K12 | Sibling proposals creep into the exits | gated WPs never hold an exit; R-5 not built | integrator |
| K13 | Large trees make pinned changes slow | 07-runtime §2.5's caps; progress events; WP-73 pulled forward if needed | WP-11, WP-63 |
| K14 | The read clamp and P23 take capabilities from non-primary deployments that users expect: an LLM-using tile's non-primary deployments can list models but can't run a turn (llm-gw guards completions with `writer`), and a non-primary deployment of the agent tile is blocked from its sandbox managers, which it reaches through the custom role `consumer` (the sandbox-managers programme, on another branch) | stated as limitations in 09-fabric's edge policy and 02-goals' non-goals; the panel counts refusals per edge (10-ux); `TestSandboxManagerEdgeBlocked` pins both; R-5 not built | WP-47, WP-55, WP-64, WP-65 |
| K15 | Docs drift from the code | docs WPs are exit criteria; reserved routes and parameters marked; `TestDeploymentWording` | WP-29, WP-64, WP-65 |
| K16 | A downgrade surprises an operator: pinned primaries go live | 12-compat §5.5's checklist in the changelog entry; the downgrade tests | WP-63, integrator |
| K17 | PRE-5's branch lands after M0 has split the files it edits | PRE-5 before wave 0.1; otherwise the integrator rebases it onto the split files | integrator |
| K18 | The sandbox-managers branches land in either order relative to this plan, and edit files the docs WPs own (`docs/protocol.md`, `docs/index.md`, `docs/frontend-kit.md`, `docs/maintenance.md`, `docs/overview/{11-interfaces,14-lifecycle,16-extending}.md`, `workspace-template/AGENTS.md`) | the docs WPs rebase on whatever landed; D-numbers are assigned only at a records commit, never one another branch holds (§4.5) | integrator, WP-29, WP-64, WP-65 |

### 6.2 Rollback per milestone

| Milestone | Before it reaches master | After a release |
|---|---|---|
| M0a | revert the WP's commits; moves and ratchets revert with them | the same; nothing behaves differently |
| M0b | drop it from the integration branch | not released on its own |
| M1 | revert the WP on the integration branch; the reserved routes keep answering 501 | turn the ship-dark switch off (§6.3): no new opt-ins, and users resume live reload to return tiles to the zero state. A defect in pinned serving or restarts is fixed forward, or the operator downgrades by 12-compat §5.5's checklist; an older xbind ignores every record and store. Records and stores are never deleted by hand. |
| M2 | the same | as M1; primaries are reassigned to `main` before any downgrade (12-compat §5.5 step 1); an older xbind stops non-primary deployments and loses nothing (12-compat §5.4, R-3) |
| M3 | the same | a feed is per-deployment configuration: turn it off; its checkpoints stay ordinary checkpoints |

### 6.3 Shipping dark

The feature is opt-in per tile, so a tile that nobody opts in is untouched
whatever ships. To ship the code with opting in closed, WP-06 adds one switch
and WP-14a enforces it in the plane's single authorize function (NP-14-5).
The switch is `Config.TileDeploys`: `--tile-deployments` or
`XBIN_TILE_DEPLOYMENTS`, set to `on` or `off`, with empty meaning `on`
(R-9). The plane reads it as `Plane.OptInClosed`, so a `Plane{}` literal in
a test is open.
- **Off:** `GET /deployments` lists no `features` and reports every
  `allowed` entry refused with kind `policy` and the reason; POSTs that would
  create or extend deployment state answer 409 with that reason; the terminal
  window renders the zero-state bar, since its controls come from `allowed`;
  `bx` exits 3 (refused) with the reason.
- **Still allowed while off:** resuming live reload onto `main`, removing a
  deployment, resetting its data, and unprotecting, so that every tile can
  return to the zero state without a downgrade.
- **Never changed by the switch:** existing records keep governing serving
  and restarts (P9), so turning it off never unpins anything; the zero-state
  path doesn't read it.
- **Use:** a release that must ship before a milestone's exit criteria are
  all met carries the switch off by default (R-9); the next release turns it
  on, and the changelog says so.

## 7. Coverage check

### 7.1 Every surface has an owner

By the sections of [13-surfaces.md](13-surfaces.md). A "no change" row names
the test that verifies it.

| 13-surfaces | Files → WPs |
|---|---|
| §3 new files | `internal/deployments/` WP-06, 13, 14a, 14b, 30, 52, 53a, 66 · `internal/checkpoint/` WP-10, 11, 12, 66 · `boot/deployments.go` WP-07, 06, 15 · `registry/deployview.go` WP-18 · `registry/qualifier.go` WP-31 · `runner/deploy.go` WP-05, 16b, 33 · `runner/limits.go` WP-30, 34 · `server/deployserve.go` WP-19, 36 · `auth/deployment.go` WP-32 · `broker/deployhooks.go` WP-05 · `broker/deploydata.go` WP-30, 39 · `broker/edgepolicy.go` WP-47 · `broker/dormant.go` WP-49 · `term/target.go` WP-30, 51 · `cmd/bx/deploy.go` WP-26, 58 · `web/frame-deploy.js` WP-25, 56a · `web/deploy-state.js` WP-24, 55 · `web/bx-deploy.js` WP-55 · the moved files and tests: their §4 rows |
| §4.1 registry | `registry.go` WP-05, 18 · `native.go` WP-18 · `util.go` WP-05 (`TileKey`; the per-deployment socket key helper is `sockDir`, 07-runtime §1.2) |
| §4.2 watch, boot | `serve.go` WP-06, 20 · `boot.go` WP-07, 06, 30 (`stepLimitAlerts`) · `api.go` WP-54 · `sandboxes.go` WP-S4 · `config.go` WP-06 (caps stay constants, NP-07-7) · `vm.go`, `push.go`, `workspace.go`, `vault.go`, `watch/watch.go`: no change (`TestIgnoreRules`) |
| §4.3 runner | §3.3's rows · `binds.go` WP-35 · `rundir.go` WP-33 · `ingress.go`, `netmux.go`, `stats.go` WP-34 · `engine.go` WP-01a · `hostenv.go`, `health.go`: no change (`TestZeroStateBackendEnv`) |
| §4.4 shared sandbox layer | `sbx/sbx.go` WP-S0 · `runner/sbx.go`, `boot/sandboxes.go` WP-S4 · `cgroup/*` WP-S2 · `vm/policy.go` WP-S0, S3 · admin `tabs/sandboxes.js`, `passes/sandboxes.js` WP-S0, S4 · `sandbox/init_linux.go` WP-S1 · `vm/manager.go`, other `sandbox/*`, `term/sbx.go`: no change (`TestPinnedBackendSeesCheckpoint`'s VM variant, `TestBindSrcNotDst`) |
| §4.5 confine | `confine.go` WP-S1, S6 · its tests WP-03, S1 · the C5 guard WP-04 · `guard_test.go`: changed by PRE-5's branch, then no change · `git.go`: no change (`TestStoreUsesConfineOnly`) |
| §4.6 server | `static.go`, `tileassets.go`, `native.go` WP-19, 36 · `tileorigin.go`, `tilenav.go` WP-38 · `api.go` WP-19, 54 · `policy.go` WP-05 · `server.go` WP-21, 48 · `openapi.go` WP-07 · `termsessions.go`, `agentapi.go` WP-51 |
| §4.7 proxy | `proxy.go` WP-19, 37 · `ingress.go` WP-37 |
| §4.8 auth | `auth.go` WP-05, 32 · `frametoken.go` WP-32 · `assettoken.go`, `tilebinding.go` WP-38 |
| §4.9(a) broker | `resources.go` WP-18, 39, 40 · `resenc_wire.go`, `resenc/resenc.go` WP-39, 40, 45 · `vault.go` WP-43 · `cron.go`, `bussubs.go` WP-49 · `netfn.go` WP-47, 50 · `ingressfn.go` WP-50 · `exposefn.go`, `delegated.go`, `admin.go`, `templates.go`: no change, since the registry entry carries the primary's inbound surface (`TestManifestFieldSplit`) · 08-data §14's `deployns.go` WP-41, `deployseed.go` WP-42, `deployvault.go` WP-43, `backup_deploy.go` WP-23, 44a |
| §4.9(b) broker | `broker.go` WP-05, 30, 39, 47 · `policy.go` WP-23b, 46 · `lifecycle.go` WP-53b · `transfer.go` WP-23b, 53b · `backup.go` WP-23, 39, 44a, 45 · `backup_cron.go` WP-44a · `backup/backup.go` WP-23, 44a, 45 · `whoami.go` WP-54 · `diskmon.go`, `resusage.go` WP-39, 44b · `orgsapi.go` WP-23b (the transfer's seam before `SetOwner`); the manager gate wraps it from `deployhooks.go` (`TestDeployAuthzMatrix`) |
| §4.9(c) broker | `create.go` WP-23b, 49, 46 · `prs.go`, `tiles.go`, `updates.go`, `templates.go`, `templaterepo.go`, `clone.go`, `gitimport.go`, `code.go`: no change (`TestWorkTreeWritersReachOnlyLiveTarget`, `TestDeploymentsAcrossTileLife`, `TestPathLeftoversIncludeDeploymentState`) |
| §4.10 obs | `status.go` WP-21, 50 · `logs.go`, `prefs.go` WP-50 · `obs.go` WP-30 |
| §4.11 term | `term.go` WP-00, 05, 51 · `sandboxenv.go` WP-00, 22, 51 · `target.go` WP-30, 51 · `attach.go`, `sessions.go`, `agent.go`, `history.go` WP-51 · `binds.go`, `vm.go`: no change (`TestTerminalMasksDeploymentState`) |
| §4.12 push, ingress, VM | `push/api.go` WP-50 · `boot/push.go`, `internal/ingress/*`: no change (`TestInboundEdgesReachOnlyPrimary`) |
| §4.13 users | no change (`TestDeployAuthzMatrix`'s `noTerminal` column) |
| §4.14 cmd/bx | `main.go`, `status.go` WP-00, 26, 58 · `deploy.go`, `deployclient.go` WP-26, 58 · `livereload.go` WP-26 · `deployment.go`, `agentdeploy.go` WP-58 · `native.go`, `agent.go`, `args.go`, `cmd/xbind/main.go`: no change (`TestBxTodayInvocationsUnchanged`) |
| §4.15 SDK | `sdk/xbin.go` WP-59 · `kv.go`, `notify.go`, `http.go`: no change (`TestDataSeparation`) |
| §4.16 web | `bx-frame.js` WP-00, 25, 56a · `frame-testapi.js` WP-00, 08, 25, 56a · `frame-titlebar.js`, `frame-deploy.js`, `bx-terminal.js` WP-25, 56a · `frame-launcher.js` WP-25 · `bx-logs.js`, `frame-info.js`, `term-sessions.js`, `xbin-client.js`, `bx-code.js` WP-56b · `events-socket.js`, `native/ios/…`: no change (WP-60's tests) |
| §4.17 workspace template | `tabs/runtime.js`, `shell/menus.js`, `shell/bx-tile-admin.js`, `manager/index.html`, the optional chips in `shell/{bx-canvas,bx-side,shell-kit}.js` WP-57 · `tabs/sandboxes.js` WP-S0, S4 · `AGENTS.md` WP-29, 65 · `bx-shell.js`, `admin.js`, `welcome/notes.js`: not edited |
| §4.18 docs | `protocol.md` WP-07, 29, 64 · `sdk.md`, `resources.md`, `auth.md` WP-64 · `elements.md`, `isolation.md`, `bx.md`, `frontend-kit.md`, `compat.md`, `maintenance.md`, `tile-deployments.md` WP-29, 65 · `overview/01`, `03`, `04`, `index.md`, `getting-started.md` WP-29 · `overview/05`, `06`, `08`, `10`, `13`, `16` WP-65 · `overview/09`, `14`, `15` WP-29, 65 · `config.md` WP-06 · `changelog.md`, `changes/`: the integrator |
| §4.19 tests, harness, CI | goldens WP-01a, 01b, 02, 03, 05 · seam tests WP-01a · the declarations' zero-state tests (`util/deployname_test.go`, `events/event_deployment_test.go`, `registry/deployzero_test.go`, `runner/deployhooks_test.go`, `broker/deployhooks_test.go`, `server/policy_zerostate_test.go`) WP-05 · `boot/deployments_reserved_test.go` WP-07, 06, 15 · `server/openapi_deploy_test.go` WP-07 · `liveroute_test.go` WP-20 · checkpoint tests WP-10, 11, 12 · `resourcebinds_test.go` WP-35 · `runner/sbx_test.go` WP-S4 · `sbx/filter_deploy_test.go` WP-S0 · `sbx/deploy_test.go` WP-03 · broker fixture WP-30 · `gates_test.go` WP-51 · `test/deployments_test.go` WP-28, 61 · legacy fixtures: untouched (WP-03 adds files) · `shots.js`, `seed.sh`, `run.sh`, `passes/agenttab.js` WP-08 · `passes/livereload.js` WP-08, 25 · `passes/deployments.js` WP-08, 56b · `hack/deploy-state.test.mjs` WP-24, 55 · `Makefile`, `ci.yml`: the integrator |

### 7.2 Every test has an owner

- **By name.** Every test [15-test-plan.md](15-test-plan.md) names is on the
  Tests line of the card that writes or extends it, with each
  [06-security.md](06-security.md) alias joined by `=` to its name. The
  tests that [09-fabric.md](09-fabric.md) §10 and [08-data.md](08-data.md)
  §14 add are on WP-30, 33, 34, 37, 40, 41, 42, 44a, 44b, 45, 47, 48 and 50.
- **Names this plan adds.** `TestLimitAlertsNestedLeaves` (WP-34, marked
  "(new)"), which 15-test-plan adds to its rows. WP-23b writes the M1
  halves of `TestDeploymentsAcrossTileLife` (transfer) and
  `TestPathLeftoversIncludeDeploymentState` (record and store), which
  15-test-plan lists under M2 only.
- **Seam rows.** 1–15 WP-01a; 16–22 and 30 WP-16a; 23–29 and 38 WP-16b;
  31–34 and 37 WP-33; 35–36 WP-34.
- **Harness passes and node tests.** `livereload` WP-08 then WP-25;
  `deployments` WP-08 then WP-56b; `agentTab` WP-08, WP-56a; `sandboxes`
  WP-S0, WP-S4; `viewAs` WP-25; `hack/deploy-state.test.mjs` WP-24, WP-55;
  `hack/events-socket.test.mjs` and the Swift rows WP-60;
  `hack/menus.test.mjs` WP-08, WP-57.
- **Existing tests every WP keeps green**, assigned to no one:
  `TestAlwaysOnBackoff`, `TestChangedComponentsNativeEntry`,
  `TestGitDirect`, `TestGoBackendLifecycle`, `TestHostileRepoStaysInside`,
  `TestIdentifyHeaders`, `TestLegacyWorkspaceBootsTwice`,
  `TestLegacyWorkspaceBootsTwiceInProcess`, `TestMultiUser`,
  `TestRootfsBin`.
- **The mechanical check.** Run in this directory, it prints every name
  15-test-plan.md uses that no card in §2 mentions:

  ```sh
  comm -23 <(grep -oE 'Test[A-Z][A-Za-z0-9]+' 15-test-plan.md | sort -u) \
    <(sed -n '/^## 2\. /,/^## 3\. /p' 14-implementation.md | grep -oE 'Test[A-Z][A-Za-z0-9]+' | sort -u)
  ```

  Its output must be exactly the keep-green list above, plus two matches that
  aren't tests: `TestMain` and `TestFlight` (from "TestFlight build"). Any
  other name is a test without an owner.

## Divergences from the model

No divergence from the model remains open. The six this plan recorded are
settled in the spine:

1. §8's claim that old clients ignore qualified components: resolved by C2
   (05-model §8).
2. P24's missing last fallback: resolved by P24 ("API off" when neither the
   primary nor a live reload target can be offered).
3. §7's one-step funnel change: resolved by 05-model §7 (new names beside
   `Runner.Ensure`).
4. Who builds §12's shared pieces: resolved by 05-model §12 (this programme
   builds the cgroup parent; D113 reuses it).
5. §12's streaming run read as a dependency on D113: resolved by 05-model
   §12 (a confine-level run, independent of D113's exec protocol).
6. §2's zero state "with no checkpoint store" while opting out keeps it:
   resolved by P5 (the zero state is the absence of a record).

## New proposals

- **NP-14-1 — M0a lands on master first.** The splits, the seams, the
  goldens, the latency and guard tests and WP-S0 change no behaviour, so they
  merge into master before the integration branch is cut, and concurrent
  work meets the split files at once instead of at a late merge.
- **NP-14-2 — `boot.go` is edited by contracts WPs only.** After WP-06, only
  WP-30 and the integrator's amendments edit `internal/boot/boot.go`; every
  hook a feature needs is installed there by a contracts WP, with a body that
  gives today's behaviour.
- **NP-14-3 — Generic operation dispatch.** WP-15's handlers decode a route's
  body into the request type its op registers and call the plane's op
  registry; an unregistered op answers 501. M2's operations register in
  `internal/deployments` without touching the handler file.
- **NP-14-4 — Reserved routes and parameters, and `features` per
  milestone.** Routes whose operations aren't built answer 501, and new
  parameters and fields on existing routes are declared but unread; both are
  documented as reserved. `features` lists `live-reload/1` from M1 and
  `deployments/1` from M2 (extending NP-11-13), so M1 can ship alone and
  clients can tell what works.
- **NP-14-5 — A ship-dark switch.** One `boot.Config` field, enforced in the
  plane's authorize function (§6.3): while off, operations that create or
  extend deployment state are refused with kind `policy`, and those that
  return a tile to the zero state stay allowed. The zero-state path never
  reads it.
- **NP-14-6 — Shared-layer ownership with D113.** This feature's WP-S series
  owns `internal/cgroup` (the cgroup parent, which D113 reuses: 05-model
  §12), the deployment fields of `internal/sbx/sbx.go`,
  `internal/vm/reserve.go`, `internal/confine`, the nested-mount rule in the
  sandbox init and the rows of `internal/boot/sandboxes.go`; D113 owns
  `Spec.AgentFD`, its policy fields, its API file and its manager.
  `Reserve(owner, memMiB, opts...)` is declared once and both projects add
  options to one type. Every shared piece is built to 07-runtime §10, and the
  other project's owner reviews it.
- **NP-14-7 — A port grid for the harness.** `PORT = 8700 + 20 × N` for WP-N,
  plus 10 for a `b` half, and `7700 + 20 × k` for WP-S<k>, with
  `HARNESS_DIR` in the agent's scratchpad, so no two agents' derived ports
  (`PORT+1`, `PORT+10280`) collide.
- **NP-14-8** — resolved by 05-model §7 (`Ensure` kept, new names beside it).
- **NP-14-9 — Legacy-fixture assertions in new files.** 12-compat §6.1's "no
  deployment state" checks run as new test functions reusing the fixtures'
  helpers, so neither allowed list nor fixture file changes.
- **NP-14-10 — One builder page.** `docs/tile-deployments.md`, written by
  WP-29 for pausing live reload and extended by WP-65, linked from
  `docs/index.md` and `docs/elements.md` (the page 12-compat G1 asks for).
- **NP-14-11 — An M2 interfaces WP.** WP-30 opens M2 by declaring every
  cross-WP broker, obs, term and runner interface, the M2 request types and
  the shared broker fixture, before the parallel waves start.
- **NP-14-12 — Gated WPs never hold a milestone.** WP-45 and WP-66 land
  complete or not at all; a milestone ships without them.
- **NP-14-13** — resolved by P24 (the "API off" fallback) and 05-model §5
  (protecting the primary restarts the sessions that target it).
- **NP-14-14 — The registry entry carries the primary's inbound surface.**
  For a tile with a record whose primary is pinned, `Rescan` fills the
  component entry's `template`, `exposes`, `expose.roles`, `provides` and
  `chrome` from the primary's checkpoint manifest. Every reader of those
  fields today (`internal/server/{static,api}.go`,
  `internal/broker/{ingressfn,netfn,exposefn,delegated,transfer,admin,templates}.go`,
  `internal/registry/registry.go`'s `IsTemplate`) then follows the primary's
  code (05-model §6) with no edit of its own, and a tile without a record
  keeps today's entry.
