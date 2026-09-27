# 02 — Goals, scenarios, non-goals, success criteria

> Status: live — the problem with evidence, the people involved, the scenarios each milestone delivers, the non-goals, the named success criteria and the zero-change contract (part of [plans/dev-lifecycle](README.md))

This document says what the dev lifecycle is for and how its implementation
will be judged. It does not restate the mechanism. Objects, operations,
routing, authority, invariants P1–P29 and flows A–H are in
[05-model.md](05-model.md), and every term is used exactly as
[01-glossary.md](01-glossary.md) defines it. Facts about today carry
`file:line` references, re-verified against this worktree, and name the
research note they came from. The baseline is master: D112 is landed, and
D113 is designed ([plans/tile-sandboxes.md](../tile-sandboxes.md)). cgi no
longer exists (removed from xbin by its own change), so nothing here covers
it.

**Labels.**
- Goals `G1…` (§1.7).
- Scenarios `S-A…S-H` (the model's flows) and `S-1…S-11` (§3).
- Non-goals `NG-1…` (§4).
- Success criteria `SC-…` (§5). [15-test-plan.md](15-test-plan.md) cites
  them by name.
- Clauses of the zero-change contract `Z1…` (§6).
- New proposals `NP-02-n` (last section).

**Milestones.** Every scenario names the milestone that delivers it.
[14-implementation.md](14-implementation.md) §1 builds this split and owns
the work packages (NP-02-1).

| Milestone | Delivers | Shared sandbox mechanics it changes |
|---|---|---|
| **M1 — pause live reload** | The checkpoint store and deploy log for `main`. Pause live reload, reload now, resume. `main` pinned on the static plane and in isolated backends, VM backends included. A pinned primary serves its inbound surface and provisions its resources from its checkpoint (P9, P22). Roll back of `main` (`bx rollback`). The read-only fetch remote `xbin-deploy`, served from the tile's view repository. The `deployments` event, the terminal window control, `bx live-reload`. | The backend launch-spec bind of a checkpoint at the tile's canonical path. A confine bind destination, so a confined build sees a checkpoint at the canonical path. |
| **M2 — tile deployments** | Non-primary deployments and deployment URLs. The target deployment of sessions, picked in the terminal's API dropdown (P24). Deployment data (empty by default), seed, reset, vault copy; resources declared and limits set per deployment (P22). Dormant registrations and deliveries. Edge policy: `read` / `block`, and `inherit` / `block` for the net slot and capability grants. Promote, reassign the primary, protected primary. Non-primary status, logs and activity in the `deployments` event only. | The D112 registry's deployment dimension, on non-`main` entries only. VM books charged to the tile for every deployment. A separate cgroup per deployment, under a per-tile parent from the tile's first non-`main` deployment. Data-namespace binds at the primary's resource paths. |
| **M3 — later rungs** | Feeds: tracked branch and deploy remote. Edge policy `match`. Per-deployment ingress hostnames. | A streaming confined run for `receive-pack`, independent of D113's exec protocol, which may reuse it. |

Tile-managed sandboxes that belong to their deployment
([05-model.md](05-model.md) §12) land with whichever of M2 and D113
([../tile-sandboxes.md](../tile-sandboxes.md)) is built second.

## 1. The problem today

### 1.1 Every save goes to every user

The product promise is live reload with no deploy step: "Saving any file
live-reloads the frontend and rebuilds/swaps the backend. There is no deploy
step" (`workspace-template/AGENTS.md:34-35`; `docs/getting-started.md:85`).
The mechanism has no place for code that isn't meant for users yet
([research/runner-hot-reload.md](research/runner-hot-reload.md)):

- **One watcher, no gate.** It coalesces saves for 300 ms
  (`internal/boot/boot.go:41`). `watchLoop` publishes `reload` for every
  changed tile and calls `run.Changed`, which rebuilds a running backend at
  once (`internal/boot/serve.go:161-184`;
  `docs/overview/03-components.md:208-228`).
- **Frontends are the files on disk.** They are read from the work tree on
  every request with `Cache-Control: no-store` — "this is a live system"
  (`internal/server/static.go:69-70`, `:153`).
- **Backends run the work tree too.** node and python execute the entry file
  in the work tree (`internal/runner/runner.go:394-407`). Isolated backends
  bind the work tree read-only at its own path (`runner.go:641`);
  non-isolated ones use it as their working directory (`runner.go:462`).
- **One Go artifact.** Go keeps one output file per tile, overwritten by
  every build (`runner.go:372`).
- **Every restart rebuilds from disk:** idle reap, crash restart, grant and
  binding changes, provider nudges, alwaysOn backoff, re-enable, xbind
  restart (`runner.go:190`, `:265`;
  [research/inbound-edges.md](research/inbound-edges.md) §0.5).

No state exists in which a tile keeps serving known code while its files
change. The work tree *is* what users run.

### 1.2 A coding agent's edit breaks the tile for every user

- **Agents edit what users run.** Agent sessions (D74) are built like
  terminals (`internal/term/agent.go:248`). The tile directory is mounted
  read-write (`internal/term/binds.go:57-58`), and the session's token acts
  as the tile's element principal (`docs/overview/07-users-orgs.md:83`).
- **Their guidance assumes it.** Agents commit often and unprompted (CM-2,
  `workspace-template/AGENTS.md:160-166`) and test by curling the tile
  (`workspace-template/AGENTS.md:153`) — the tile everyone uses. Testing
  guidance stays at "build/test what you can"
  (`workspace-template/AGENTS.md:556`), because nothing else exists to test
  against ([research/builder-contract.md](research/builder-contract.md) §8).
- **A multi-file change arrives in pieces.** Each 300 ms batch serves
  whatever is on disk. Frontends, node and python have no build that could
  refuse a half-written state.
- **Every viewer sees the churn.**
  - `reload` re-navigates every mounted frame of the tile
    (`web/bx-frame.js:371-372`) and reloads it in the native app
    (`native/ios/App/Model/WorkspaceEvents.swift:243-245`).
  - A failed build paints compiler output over every viewer's frame
    (`web/bx-frame.js:374-376`).
  - Every `build-start` wipes the tile's reported status
    (`internal/obs/status.go:129-145`).
- **Three quick crashes take the tile down** for everyone until the next
  save: "fix the code and save to retry" (`internal/runner/runner.go:350`;
  `workspace-template/AGENTS.md:485`).
- **The one safeguard is blue/green.** A failed build or health check leaves
  the previous generation serving (`runner.go:287-292`). For Go that is a
  complete safeguard; node and python keep reading the work tree lazily. The
  overlay still reaches every viewer.

### 1.3 xbind's own writers ship immediately

- **Builtin updates.** In `replace` and `merge` mode they write into the tile
  and reprovision "so the change is live at once"
  (`internal/broker/tiles.go:196-203`). `replace` discards local edits
  (`internal/builtins/updates.go:500-504`). `merge` writes conflict markers
  into the tile's files, which users are served at once
  (`updates.go:536-538`). BU-5's safety
  argument is "everything is in git" (`plans/DECISIONS.md:133-134`).
- **PRs.** Update PRs (D49) and code PRs (D48) are reviewed first, but land
  the same way: the tile's own terminal runs `git am --3way` in the work
  tree, then builds and tests a tile that is already serving users
  (`workspace-template/AGENTS.md:574-581`).
- **Templates.** Template merges (D50) are the builder's own `git merge` in
  the work tree.

### 1.4 No rollback except git by hand

- **git is the only history.** xbind makes one initial commit per tile and
  never another (`internal/broker/code.go:96-106`; D2). CM-2 calls git "the
  reversibility net" (`plans/DECISIONS.md:110-114`).
- **Nothing records what a generation ran.** Go builds use `-buildvcs=false`
  (`internal/runner/build.go:86`), the one artifact is overwritten, and
  everything else runs the work tree itself. There is no previous build to
  return to ([research/runner-hot-reload.md](research/runner-hot-reload.md)
  §5).
- **Undo is manual and public.** Open a terminal, find a good commit (if one
  was made), check it out, and let the save rebuild — through every
  intermediate state, to every user.
- **The one non-git undo rolls back data too.** Restoring a backup is
  admin-only, needs a bound archiver, and replaces the tile's data along with
  its code (`docs/protocol.md:1771-1777`).

### 1.5 No separate data for trying things

- **Everything is shared.** Resources belong to scopes and are shared by
  every tile in the scope (`docs/resources.md:3-6`). The vault is one file
  per tile path (`internal/broker/vault.go:46`). Cron jobs and bus
  subscriptions are keyed by (tile, name) and re-registered at every start
  ([research/inbound-edges.md](research/inbound-edges.md) §0.3).
- **So code under test shares** the data, secrets, schedules and
  subscriptions every user relies on, and its notifications reach real
  people.
- **The only separate runtime is clone:** a new tile at a new path, with
  empty data and no vault by design (`internal/broker/clone.go:18-27`). Its
  cross-scope `uses` go back through approval, and it has no consumers,
  bindings or ingress. It tests a different tile, not the next state of this
  one.

### 1.6 The only "stop" takes the tile down

- `bx disable` is documented as "pause/resume a tile" (`docs/bx.md:138`). It
  stops the backend, and the proxy then answers 409 with `X-XBin-Lifecycle`
  (`internal/proxy/proxy.go:149-156`). The page keeps loading from the work
  tree meanwhile ([research/side-findings.md](research/side-findings.md)
  #13).
- There is no restart, stop or build command, and no way to hold a backend on
  known code ([research/runner-hot-reload.md](research/runner-hot-reload.md)
  §4).

### 1.7 Goals

| # | Goal | Answers | Milestone |
|---|---|---|---|
| G1 | Hold a tile on known code while its work tree moves, and ship the work tree deliberately, as one built and health-checked checkpoint. | 1.1, 1.2, 1.6 | M1 |
| G2 | Undo a bad deploy in seconds, without git, without a build and without touching data. | 1.4 | M1 (`main`), M2 |
| G3 | Try changes on a runtime that no user, consumer, schedule or webhook reaches, with data that starts empty, is usually filled by the deployment's own code, and is seeded from the primary only on purpose. | 1.1, 1.2, 1.5 | M2 |
| G4 | Change what a deployment runs only by explicit, logged acts: deploy, promote, roll back, reload now, resume, and the pins that pausing live reload, attaching it or protecting the primary take. | 1.3, 1.4 | M1, M2 |
| G5 | Let a team decide who may change the primary, down to "no agent ever can". | 1.2 | M2 |
| G6 | Make every step drivable by a coding agent from a tile terminal with `bx`. | 1.2 | M1, M2 |
| G7 | Change nothing for tiles that never opt in (§6). | — | always |
| G8 | Build on the shared sandbox mechanics one layer above tile-managed sandboxes, and leave those mechanics unchanged for tiles in the zero state. | — | M1, M2 |

| Problem | What M1 fixes | What M2 adds | What M3 adds |
|---|---|---|---|
| 1.1 every save goes to every user | While live reload is paused, saves reach nobody. Reload now ships a whole, built, health-checked checkpoint. | A non-primary deployment is the place to try things; the primary changes only by deploy or promote. | git-driven feeds |
| 1.2 agents break the tile | Agents pause live reload and reload now; a failed build or health check keeps the previous code serving, and no viewer sees it. | Agents target `dev` from the terminal's API dropdown; a protected primary stops unattended promotion. | — |
| 1.3 xbind's writers ship at once | Pause live reload, apply, reload now once it builds. | Updates and PRs land in the work tree and reach `dev`; promote when they work. | — |
| 1.4 no rollback | Roll back `main` from its deploy log (for tiles that opted in). | A deploy log and roll back per deployment. | — |
| 1.5 no data separation | — | Per-deployment data, empty by default, filled by the deployment's own code or fixtures, seeded on request. | name matching (`match`): the parallel fabric across tiles |

## 2. Who is involved

Access levels are `read < write < terminal` (`docs/auth.md:584-590`).
[05-model.md](05-model.md) §10 sets the authority for each act. This section
says what each party needs, what it gets, and what must never happen to it.

**Tile builder.** A human with `terminal` on the tile (a creator gets it:
D16, `docs/overview/07-users-orgs.md:85`), working in the terminal window.
- *Needs:* to stop reloads while working, try changes against realistic data,
  ship on purpose, and undo fast.
- *Gets:* pause live reload, reload now and resume. Adding, removing,
  resetting and deploying to non-primary deployments, and filling them with
  synthetic data and test secrets from a session that targets them (S-11).
  Deploying, promoting and rolling back onto the primary at parity with
  saving (P4), unless the primary is protected.
- *Never:* loses work — no deployment operation writes the work tree
  (SC-WORKTREE) — and never ships by accident.

**Coding agents in tile terminals.** Terminal and agent sessions (D74), whose
token acts as the tile's element principal with the driving human attributed
(D29).
- *Needs:* a runtime to test on, a stable target, and state and refusals it
  can read from `bx`.
- *Gets:* the builder's operations, addressed by default to the session's
  **target deployment**. The target is picked in the terminal window's API
  dropdown when the session starts (P24): the primary by default; the live
  reload target when the primary is protected; "API off" when neither
  exists. It is fixed for the session's life, and exposed as
  `XBIN_DEPLOYMENT` when it isn't the primary.
- *Structurally never a tile manager.* Manager acts need a human session:
  `p.Component == ""` and (`IsAdmin` or `mayManageTile`;
  `internal/auth/auth.go:92`, `internal/broker/orgsapi.go:427`). Terminal
  and agent tokens carry the tile as their component, so they fail the gate
  even when the driving human is a manager, and no element principal passes
  it, whatever `xbin` or `xbin:users` grants its tile holds
  ([05-model.md](05-model.md) §10). Ownership rights already never ride an
  element principal (`internal/broker/orgsapi.go:71-78`). So an agent can
  never seed, copy vault values, turn on deliveries or alwaysOn, set edge
  policies or resource limits, or reassign or protect the primary. With
  protection on it never changes the primary's code, and the primary is
  never its target (P21, P24).
- *Never:* reaches another deployment of its own tile through self-calls
  (P12). On a non-primary target it is a non-primary principal, so xbind's
  API is default-deny for it: every `/api/xbin/*` route not classified
  deployment-scoped or neutral refuses it (P26).

**Tile managers.** The tile's user-owner, the owning org's admins, and
workspace admins (D24; `mayManageTile`,
`internal/broker/orgsapi.go:425-442`).
- *Needs:* control over the primary and over where its data goes.
- *Gets:* seed, vault copy, deliveries and alwaysOn for non-primary
  deployments; edge policies; per-deployment resource limits, which default
  to the tile's and never exceed its ceilings (P22); reassigning and
  protecting the primary; resetting `main`'s data while it isn't primary;
  purging checkpoints. Each of these needs the manager's human session.
- *Never:* finds the primary's data copied without a manager having done it
  (P14).

**Workspace admins.** Everything a manager can do, on every tile, plus the
host.
- *Needs:* to run deployments within the host's means.
- *Gets:*
  - `--isolate`, which pinned and non-primary backends require: non-isolated
    mode is unsupported for them (P18);
  - the VM policy and budget;
  - every deployment's sandboxes in the D112 registry
    (`GET /api/xbin/sandboxes`): non-`main` backends as
    `backend+<name>:<CompKey>:g<gen>` with the deployment named, `main`'s
    rows exactly as today.
- *Sees:* the admission caps, fixed in v1 at 3 non-primary deployments per
  tile, 24 per workspace and 12 running
  ([07-runtime.md](07-runtime.md) §10.3). Whether they become configuration
  is [13-surfaces.md](13-surfaces.md)'s open question.
- *Never:* sees a non-primary deployment starve a primary of memory or VM
  budget (SC-PRIMARY-FIRST).

**Tile users: readers and writers.**
- Readers (`read`) only ever see the primary. The deployment state they can
  read is primary-scoped: live reload state as it concerns the primary, the
  primary's checkpoint and deploy state, and protection. It holds no
  non-primary deployment names and no counts that reveal them, and
  `/components` adds only the primary summary ([05-model.md](05-model.md)
  §8).
- Writers (`write`) may also open a non-primary deployment's URL and use it,
  with that deployment's data, and follow its status and activity, but
  cannot operate it. Their current level is checked on every request.
- `noTerminal` accounts are capped at `write` (D88; `docs/auth.md:667`).
  View-as sessions are read-only (D64).
- *Never:* a reload, overlay, status change or notification caused by a
  non-primary deployment (P13; SC-EVENTS).

**Consumer tiles and other providers.**
- *Consumers* are tiles bound to this tile, or granted calls to it. They
  reach only the primary: inbound edges never move to a non-primary
  deployment (P7). When the primary is reassigned, they follow it.
- *Providers* are tiles this tile calls. They receive a non-primary
  deployment's calls on their primary, marked with `X-XBin-Deployment`,
  under the calling tile's edge policy (P3): `read`, the default, clamps the
  role to `reader`, and `block` refuses.
  - Edges that can't be read-clamped are blocked in v1, with no override
    (P23): custom roles with no path to `reader`, stream interfaces,
    lan-ingress links and net-provider splices.
  - A provider whose write role means spend refuses clamped calls. llm-gw
    serves `/v1/*` only at `writer`
    (`builtin-tiles/llm-gw/backend/main.go:274`), so non-primary
    deployments can list models but can't run a completion.
  - The agent tile reaches its sandbox managers through the custom role
    `consumer` (the sandbox-managers work, on another branch), so its
    non-primary deployments are blocked from them (NG-16).
- *Never:* a consumer's traffic landing on untested code, or a provider's
  primary written by a non-primary deployment (SC-CLAMP).

**xbind's own writers.** Builtin updates, restore, and PR application by the
tile's plane write the work tree only. They reach the live reload target and
nothing else ([05-model.md](05-model.md) §11).

## 3. Scenarios

The model's flows A–H are normative ([05-model.md](05-model.md) §14). The
narratives below restate them only to attach a milestone, the invariants they
exercise and the criteria that verify them. The table at the end of the
section summarises the mapping.

The cast:
- **Ana** builds `apps/crm` and is one of its managers.
- **Ben** has `terminal` on it but isn't a manager.
- **Carla** has `write` only.
- **Lena** writes a static tile.
- **"The agent"** is an agent session.

### S-A — Pause live reload, fix, reload now, resume (flow A; M1)

`apps/crm` is in use all day. Ana pauses live reload in the terminal window.
`main` is pinned to `c:3f2a1c9` — the code it was already running, now
served from the checkpoint instead of the work tree. Nobody notices.

She edits eight files over half an hour. No frame reloads; the bar counts the
files changed since `c:3f2a1c9`. She builds in her terminal until it is
clean, then presses **Reload now**. The work tree becomes `c:8b04e12`, which
is built, started and health-checked, and every open frame reloads once. Had
the build failed, `main` would have kept serving `c:3f2a1c9`, and the error
would have gone to Ana, not to her users ([05-model.md](05-model.md) §8). She
resumes live reload, and the tile returns to the zero state.

**What M1 does not give her:** while live reload is paused, the work tree is
served nowhere, so she can't see her change running before her users do.
That needs a non-primary deployment (S-B).

### S-B — A `dev` deployment for a busy tile (flow B; M2)

Ana adds `dev`, with code from the work tree and empty data, and attaches
live reload to it. `main` is pinned to a fresh checkpoint; nobody notices.
Her saves now reach `/c/apps/crm+dev/`.

Her change adds an audit trail: `dev`'s code declares a new kv resource in
`scope.json`. It is provisioned in `dev`'s namespace only; `main` gains it
when that code is promoted (P22). `dev` runs on data its own code and
fixtures create (S-11). For one migration she needs realistic data, so she
seeds `dev` from `main`: optional, a manager act, with a PII warning. Ben
could not have. Carla opens the deployment URL Ana sends her and tries the
new flow without a terminal.

Ana presses **Promote dev → main**, reviews the diff between `main`'s
checkpoint and a fresh checkpoint of the work tree, and confirms. The promote
deploys exactly the checkpoint the diff showed (`expect`), or answers 409 if
the work tree moved since. `main` runs the new code on its own data and
provisions the new kv in its own namespace; `dev` keeps following the work
tree.

### S-C — An agent iterating on `dev` (flow C; M2)

Ben opens an agent session and picks `dev` in the terminal window's API
dropdown, so the session's target is `dev` (`XBIN_DEPLOYMENT=dev`) for its
life. The agent:
- edits, and reads `bx status` and `bx logs` for `dev`;
- curls `$XBIN_URL/api/$XBIN_COMPONENT/…`, which reaches `dev`;
- commits often — committing deploys nothing;
- finally runs `bx promote apps/crm dev main --yes`, which succeeds at parity
  (P4). It runs `bx` without a terminal, so it must pass `--yes`; without it
  `bx` exits 4 (not confirmed).

On a tile whose primary is protected, the same command is refused: `bx`
exits 3 (refused) with a message naming the protection and the tile
managers, and the agent reports that to Ben.

### S-D — Roll back (flow D; M1 for `main`, M2 after a promotion)

Ana's reload now shipped a regression. From `main`'s deploy log she rolls
`main` back to `c:3f2a1c9`. That checkpoint's artifact is still built
([07-runtime.md](07-runtime.md) §2.8 keeps the current and last three per
deployment), so the rollback needs no build and no network and completes in
seconds. `main`'s data stays as it is; the UI says a rollback moves code,
not state.

Live reload stays paused, because the next save would otherwise redeploy the
regression ([research/prior-art.md](research/prior-art.md) §2, rule b). The
work tree still holds the bad change, so resuming would redeploy it, and the
terminal window says so.

Had Ana resumed earlier, the checkpoint store would have survived the resume
([05-model.md](05-model.md) §2). A break introduced by a later save can still
be rolled back to a checkpoint from before; the rollback pauses live reload.

**M2 variant:** after **Promote dev → main**, the same roll back of `main`,
with `dev` untouched.

### S-E — A builtin update tried on `dev` (flow E; M2, with an M1 variant)

The Updates tab files an update PR (D49) for `apps/crm`. Ben's agent applies
it with `git am --3way` in the work tree. Live reload is on `dev`, so `dev` runs
it and `main` doesn't. The agent tries it on `dev`, and Ana promotes. A code
PR (D48) from another tile goes the same way. Nothing in the PR flow changes
([05-model.md](05-model.md) §11).

`replace` and `merge` updates still write the work tree directly
(`internal/broker/tiles.go:196-203`). With live reload on `dev`, or paused,
their conflict markers no longer reach the primary.

**M1 variant:** with only `main`, Ben pauses live reload, applies the update,
builds it in the terminal, and ships it with reload now. The update reaches
users as one built unit, or not at all.

### S-F — Cut over with data (flow F; M2)

A schema migration was rehearsed on a seeded `dev`, and `dev` has served
Carla's testing for a day. `apps/crm` roots its scope alone, as v1 requires
of a reassignment (P28). Ana reassigns the primary to `dev`. The
confirmation says three things:
- inbound traffic will read and write `dev`'s data;
- `main`'s data does not follow;
- when `dev` was seeded. Writes to `main`'s data since then stay there
  (NG-4).

`dev` starts as the primary first; then `main` restarts. Every inbound edge
— consumers, ingress, cron ticks, bus deliveries — now reaches `dev`. `main`
remains, pinned, with its registrations dormant, and it can become the
primary again. Had the primary been protected, her request would also have
named the checkpoint of `dev` she reviewed (`expect`), and a `dev` that had
moved since would have answered 409.

### S-G — A multi-tile scope: routing (flow G; M2, `match` in M3)

`apps/shop` and `apps/shop-admin` share a scope, and both have `dev`.
`apps/shop-admin+dev` calls `apps/shop` through its grant. The call reaches
`apps/shop`'s **primary**, read-clamped. The caller's deployment name doesn't
change where a call goes. With `match` (M3) it would reach `apps/shop+dev`
instead. S-4 covers the data side of the same setup.

### S-H — A hotfix while `dev` holds unfinished work (flow H; M2)

`main` is pinned; live reload is on `dev`, whose work tree holds unpromoted
work. Ana:
1. runs `git fetch xbin-deploy`. That read-only remote is injected into the
   tile's sessions while the tile has a deployment record. It is served
   from the tile's view repository, which holds only `deploy/<name>` for
   each pinned deployment: the checkpoint's git view, without the files the
   tile's ignore rules exclude (`node_modules`, `.env`);
2. puts the unfinished work on a branch;
3. runs `git checkout -b hotfix deploy/main`, fixes and commits. `dev`
   follows the work tree, so the fix runs there first;
4. uses **Deploy to main**;
5. checks the unfinished branch out again and moves it onto the fix with the
   `git rebase --onto` command `bx` prints. The checkpoint commit's message
   names the work-tree head it was captured from.

xbind merged nothing and wrote nothing into the work tree. git did the
combining; deployments only moved checkpoints.

### S-1 — An agent iterating overnight on `dev` (M2)

At 19:00 Ben starts an agent session on `apps/crm` with target `dev` and a
task list, and goes home. The primary is protected, as in S-2.

**Through the night:**
- The agent edits and saves; `dev` rebuilds on each save, and `main` keeps
  serving on its pinned checkpoint.
- It tests against the data its own fixtures loaded into `dev` (S-11),
  through `$XBIN_URL/api/$XBIN_COMPONENT/…`, which its session routes to
  `dev` (P12).
- The tile's nightly report is a cron job registered by `dev`'s backend at
  start. It is dormant and fires nowhere. The agent triggers it once with
  **run now** and reads the output.
- `dev`'s notifications appear in the deployments panel as "would notify …"
  instead of paging anyone.

**At 02:10** a change makes `dev` crash-loop. The sticky error is `dev`'s
alone: it rides only the `deployments` event, to writers and the tile's own
sessions. No primary viewer sees an overlay, and `main`'s status is
untouched. The agent fixes it and carries on.

**Resources:** the agent's backend runs in `dev`'s own cgroup, with limits
defaulting to the tile's (P22). If it is a VM, the guest is charged to the
tile's VM books and leaves the primary's guest size free in the budget
(P25). If the tile drives tile-managed sandboxes (S-10), `dev`'s set is
separate from `main`'s.

**One hazard `bx` must defuse** (SC-AGENT-BX): deploying onto its own live
reload target pauses live reload, and silently stops the agent's saves from
reaching `dev`. The command's output must say so.

**At 08:00** Ben reads the deploy log and the diff, and asks Ana to promote.
The agent could not have: the primary is protected. Ana promotes from her own
browser session, naming the checkpoint she reviewed (`expect`).

### S-2 — Protected primary: separation of duties (M2)

A team runs `apps/billing`. The policy: developers and their agents never
change what customers run; two managers do.

Ana turns protection on. Live reload is detached from `main`, which stays
pinned in place, and live reload is now paused. Sessions that targeted
`main` restart onto P24's default; with nothing attached, their API is off.
Ben resumes live reload onto `dev`, and new sessions default to `dev`. From
then on:
- **Ben and every agent session** may deploy to, promote to, roll back and
  reset `dev`, and add or remove other non-primary deployments. `main` is
  never offered in the terminal's API dropdown (P24). Any attempt to deploy,
  promote, roll back, reload now or resume onto it is refused, naming the
  protection and the managers.
- **Carla** tries changes at `/c/apps/billing+dev/`.
- **Ana** reviews and promotes `dev → main`, or rolls `main` back, from her
  browser session, naming the checkpoint she reviewed (`expect` on promote,
  `checkpoint` on roll back). Every act lands in `main`'s deploy log under
  her name.

The separation is structural, not a convention. Manager acts need a human
session (`p.Component == ""`), which no token minted for a terminal or agent
session is, and no element principal passes the manager gate whatever grants
its tile holds (§2). While protected, `main`'s inbound surface (`template`,
`exposes`, `provides`, `chrome`) comes from its checkpoint, so its framing
and routes change only by Ana's deploy. Unprotecting is itself a logged
manager act.

### S-3 — A static-only tile held during a content edit (M1; drafts in M2)

`apps/handbook` is plain HTML and CSS, read by the whole org. Lena is
restructuring five pages over an afternoon.

She pauses live reload. The static plane serves `main`'s checkpoint, so
readers keep the old handbook, whole, and no frame reloads under them. She
edits; the pages change together. When she's done she presses **Reload
now**: the new pages go out at once, and every open frame reloads once.

Static tiles need no isolation (P18), so this works on every host, including
those without `--isolate`.

**M1 limit:** she can't see her pages rendered while live reload is paused.
In M2 she adds `draft` with live reload attached and reads
`/c/apps/handbook+draft/` as she types. Carla proofreads there, and Lena
promotes `draft → main`.

### S-4 — A scope shared by two tiles: data (M2)

`apps/shop` (storefront) and `apps/shop-admin` (back office) share
`apps/shop/scope.json`, which declares `res:apps/shop/orders` (sqlite) and
`res:apps/shop/events` (bus). Both `main`s share the scope's data today.

- **One `dev` namespace for the scope.** Ana adds `dev` to both tiles. Both
  see the scope's `dev` namespace: an order created in `apps/shop-admin+dev`
  shows up in `apps/shop+dev`, and neither `main` sees it (P28).
- **Calls stay edges.** `apps/shop-admin+dev`'s calls to `apps/shop` reach
  `apps/shop`'s primary, read-clamped (S-G). They can read the scope's
  primary data through that API, but never write it; `block` on that edge
  stops even the reads ([05-model.md](05-model.md) §9).
- **Namespace operations reach both tiles** (P28).
  - Seeding or resetting either `dev` fills or empties the shared namespace
    for both, stops both tiles' `dev` for the operation, and needs the
    actor's authority on both tiles.
  - Removing `apps/shop+dev` deletes the namespace only if
    `apps/shop-admin+dev` is gone too: a namespace is deleted with its last
    claimant.
- **A tile without `dev`** in the same scope keeps using the primary
  namespace.
- **No split primaries.** Neither tile can reassign its primary in v1,
  because reassigning one member would split the scope's primary data (P28).

No shipped tile sits in a multi-tile scope
([research/data-plane.md](research/data-plane.md), summary). This is a case
builders make themselves, and it still has to be right.

### S-5 — An agent's multi-file refactor with live reload paused (M1)

Without deployments, Ben's agent is asked to rename a data model across 14
files. Its guidance, from `/docs/` and `bx` usage, says to pause live reload
first:

```sh
bx live-reload pause apps/crm --yes   # main pinned to the code it runs
# edit 14 files, build and test in the terminal
bx live-reload now apps/crm --yes     # exit 1: compiler output; main keeps serving
# fix the error
bx live-reload now apps/crm --yes     # built, health-checked, frames reload once
bx status apps/crm && bx live-reload resume apps/crm --yes
```

The failed reload now leaves `main` serving its previous code, and no viewer
sees an overlay ([05-model.md](05-model.md) §8).

**M1 limit:** the agent still ships to every user when it reloads now — but
as one built, health-checked checkpoint instead of 14 intermediate states.

### S-6 — A chrome tile held during a shell edit (M1)

A workspace admin reworks `shell/`, the layout every user sees. She pauses
live reload on `shell`: users keep the current shell until she reloads now.
`shell` cannot have non-primary deployments (P19): a non-primary deployment
of chrome would act on real users and grants.

### S-7 — A provider changes its API (M2; `match` in M3)

`apps/calendar` provides its API to `apps/crm` and three other consumers. Its
builder adds `dev` and changes an endpoint incompatibly.

The consumers keep calling `apps/calendar`'s primary: their bindings are
inbound edges, and inbound edges never move. The builder tries `dev` through
its own calls and the deployment URL. Promoting to `main` gives every
consumer the new API, so a breaking change still needs the usual
coordination: consumers only ever see the primary's API, old or new.

With `match` (M3), `apps/crm+dev` could reach `apps/calendar+dev`, and both
could be exercised together before either is promoted.

### S-8 — Deploying from git (M3)

A git-minded builder sets `dev` to follow the tracked branch `next`. Each
commit on `next`, read inside confine, becomes a checkpoint and is deployed
to `dev`. She deploys to `main` by pushing to the deploy remote, at parity.
With the primary protected, that push is refused: a protected primary takes
no deploy-remote push (P21), and a push from a terminal carries the
session's tile-scoped token (`internal/term/term.go:791`), which never
passes the manager gate. A manager deploys to it from their own human
session instead.

Commits on other branches deploy nothing. CM-2's "commit often" never turns
into "deploy unprompted", because only the configured branch or the deploy
remote feeds a deployment.

### S-9 — Refusals where isolation is off (M1)

The host runs without `--isolate` — the integration suite's daemon does
(`test/integration_test.go:85`). Pausing live reload on a Go, node or python
tile is refused with a reason naming isolation; nothing changes, and live
reload stays attached. Adding a deployment to such a tile is refused the
same way (M2). Non-isolated mode is unsupported for pinned or non-primary
backends, and there is no fallback to the work tree (P18). Pausing live
reload on a static tile works normally.

### S-10 — A tile that drives tile-managed sandboxes (M2 with D113)

`apps/grader` runs submitted code in tile-managed sandboxes (D113). Its `dev`
backend:
- creates and drives its own sandbox set, keyed per deployment like dormant
  registrations;
- gets `dev`'s code when it mounts `{"source":true}`;
- has filesystem resource mounts resolved in `dev`'s data namespace.

`cap:sandboxes` and the per-tile caps stay the tile's, shared by both
deployments. The D112 registry lists `dev`'s sandboxes under `apps/grader`,
with the deployment named. `main`'s sandboxes never change because of `dev`.

### S-11 — Synthetic data and test secrets on `dev` (M2)

Ben's agent, in a session targeting `dev`, needs customers, orders and a
payment key to exercise a new checkout flow. No manager is involved, and
nothing comes from `main`:
- **Fixtures.** It loads them through `dev`'s own backend API, or writes kv
  and blob values directly through `/api/xbin/kv/…` and `/api/xbin/blob/…`
  (`internal/broker/broker.go:277-282`), which resolve in `dev`'s namespace
  for a session targeting `dev` ([08-data.md](08-data.md) §4.1). sqlite and
  filesystem resources are reached through `dev`'s backend only: `data/` is
  masked in terminals (`internal/term/binds.go:51`).
- **Test secrets.** It sets a test API key with
  `bx vault set apps/crm STRIPE_KEY` in that session. Tile terminals may set
  their tile's secrets today (`internal/broker/vault.go:159-166`), and a
  session's vault writes reach its target's vault (08-data §10). The key had
  been a placeholder: a name from the primary's vault, with no value.
- **Starting over.** It iterates, and uses **Reset** (terminal level) and a
  fixture re-run to start clean.

`main`'s data and vault are untouched throughout (SC-DATA). Copying real
values from `main` (seed, vault copy) stays a manager's act for the rare case
synthetic data can't cover, and it carries the PII warning (NP-02-13).

### Scenario summary

| Scenario | Milestone | Model invariants | Verified by |
|---|---|---|---|
| S-A pause live reload, reload now, resume | M1 | P5, P8, P9, P16, P18 | SC-LIVE-RELOAD-PAUSE, SC-LATENCY-OPS, SC-SAFE-DEPLOY, SC-OPT-OUT |
| S-B a `dev` deployment | M2 | P6, P7, P10, P14, P17, P22 | SC-INBOUND, SC-DATA, SC-LATENCY-OPS, SC-AUDIT |
| S-C agent on `dev` | M2 | P4, P12, P21, P24 | SC-AGENT-BX, SC-PROTECT |
| S-D roll back | M1 (`main`), M2 | P9, P10 | SC-ROLLBACK, SC-WORKTREE, SC-AUDIT |
| S-E update tried on `dev` | M2 (M1 variant) | P8, P9 | SC-PINNED, SC-SAFE-DEPLOY |
| S-F cut over with data | M2 | P6, P7, P13, P28 | SC-INBOUND, SC-DORMANT, SC-LATENCY-OPS |
| S-G multi-tile scope, routing | M2 (`match` M3) | P3, P11 | SC-CLAMP |
| S-H hotfix | M2 | P1, P16 | SC-WORKTREE, SC-AUDIT |
| S-1 overnight agent | M2 | P12, P13, P20, P22, P25 | SC-DORMANT, SC-EVENTS, SC-PRIMARY-FIRST, SC-AGENT-BX |
| S-2 separation of duties | M2 | P4, P21, P24 | SC-PROTECT, SC-PINNED, SC-AUDIT |
| S-3 static content edit | M1 (drafts M2) | P18 | SC-LIVE-RELOAD-PAUSE, SC-LATENCY-OPS, SC-FAIL-CLOSED |
| S-4 shared scope, data | M2 | P6, P14, P28 | SC-DATA, SC-CLAMP |
| S-5 agent refactor, live reload paused | M1 | P4, P9 | SC-AGENT-BX, SC-SAFE-DEPLOY, SC-LIVE-RELOAD-PAUSE |
| S-6 chrome tile | M1 | P19 | SC-LIVE-RELOAD-PAUSE, SC-FAIL-CLOSED |
| S-7 provider API change | M2 (`match` M3) | P3, P7 | SC-INBOUND |
| S-8 deploying from git | M3 | P1, P16, P21 | defined with M3 |
| S-9 isolation off | M1 | P18 | SC-FAIL-CLOSED |
| S-10 tile-managed sandboxes | M2 with D113 | P11, P14 | SC-DATA, SC-PRIMARY-FIRST |
| S-11 synthetic data and test secrets | M2 | P12, P14 | SC-DATA, SC-AGENT-BX |

## 4. Non-goals

| # | Not a goal | Why |
|---|---|---|
| NG-1 | Traffic splits and gradual rollout | A tile has exactly one primary (P7). A split would be a later routing feature on the same inbound resolver ([05-model.md](05-model.md) §7), and nothing here may preclude it. |
| NG-2 | A deployment per pull request, or any trust boundary | A non-primary deployment shares the tile's principal and authority (P11, P20). Running code written by people who can't write the tile needs separate principals. |
| NG-3 | Cross-tile groups of same-named deployments before `match` | Non-primary edges go to providers' primaries, read-clamped (P3). Grouping same-named deployments across tiles needs `match`, which is M3. |
| NG-4 | Data flowing back to the primary | Promotion moves code only (P10). The primary's data changes through the primary's own code and migrations, never by merging a namespace back. |
| NG-5 | Deployments for chrome and governance tiles | Workspace governance has no deployment dimension; a non-primary deployment of the admin tile would act on real users (P19). These tiles may still pause live reload. |
| NG-6 | Per-user work trees | A tile has one work tree, so live reload attaches to at most one deployment (P8). Per-person isolation is a git branch, or clone (a new tile). |
| NG-7 | A manifest key, or any author-side opt-in | Deployment state is operator state in `data/` (P5, P15). A key would travel with clone and import, and toggling it would restart the backend ([research/builder-contract.md](research/builder-contract.md) §1). |
| NG-8 | Per-deployment authority | Grants, bindings and capabilities stay the tile's (P11). Edge policy only narrows them. |
| NG-9 | Merging inside xbind | Promotion is not a merge ([05-model.md](05-model.md) §5). git combines work in the work tree (S-H). |
| NG-10 | Rolling back data | A rollback moves code (flow D). Recovering data stays with backups and the tile's own migrations. |
| NG-11 | History for tiles that never opted in | The zero state has no checkpoint store (P5, Z1). git stays the undo for those tiles, exactly as today (CM-2). |
| NG-12 | Per-deployment ingress hostnames in M1/M2 | Ingress belongs to the primary. A manager-routed test hostname is a later rung ([05-model.md](05-model.md) §15). |
| NG-13 | New health checks or promotion gates | Deploys use D8's blue/green with today's socket-dial health check (`internal/runner/health.go:12-15`). Tests before promoting are the builder's or the agent's job. |
| NG-14 | Pinned or non-primary backends without isolation | Non-isolated mode is unsupported for them: without `--isolate`, xbind refuses rather than falling back to the work tree (P18, D78). |
| NG-15 | Building deployments on tile-managed sandboxes | The owner's direction: a deployment's backend is a runner backend with a serving lifecycle ([05-model.md](05-model.md) §12). |
| NG-16 | Non-primary deployments that need an unclampable or spend edge | Edges that can't be read-clamped are blocked in v1 with no override (P23), and a provider whose write role means spend refuses the clamped role. So a non-primary deployment of the agent tile reaches none of its sandbox managers (the custom role `consumer`, from the sandbox-managers work on another branch) and runs no LLM completion through llm-gw (`writer`). Agent sandbox work and LLM turns are tested on the primary in v1. Loosening later is easy; tightening after the fact is not. |

## 5. Success criteria

Each criterion has a name that [15-test-plan.md](15-test-plan.md) cites, a
statement, how it is measured, when it passes, and the milestone whose exit
it gates.

**Conventions.**
- "p95" is over at least 30 runs, on one host, with the baseline and the
  candidate measured back to back. [15-test-plan.md](15-test-plan.md) §8
  runs a short tier on every `make integration` and the full p95 at
  milestone exits.
- Backends run in namespace sandboxes unless stated otherwise. VM backends
  add today's VM start window (60 s, tripled when emulated:
  `internal/runner/vm.go:26`, `:47-55`).

**Reference tiles.**
- **R-static:** a static tile the size of the largest shipped tile,
  `builtin-templates/agent` (≈155 files, ≈1.8 MB).
- **R-go:** `examples/counter-go` with a warm build cache.
- **R-node:** a node backend of R-static's size.
- **R-large:** 20,000 files, 300 MB (a node tile with `node_modules`). Used
  for stress only.

### SC-ZERO — a zero-state tile is byte-identical

- **Criterion.** For a tile that never opted in, every clause of the
  zero-change contract holds (§6, Z1–Z11). Tiles that opted out are covered
  by SC-OPT-OUT.
- **Measured by.** Golden tests comparing the new xbind with the baseline on
  the same workspace. They cover:
  - served documents (`/c/<tile>/`, `?native=1`);
  - the headers a backend receives, and the backend and session env;
  - the event sequence of a save, and the storage paths touched;
  - API responses: `/components`, `/tile-status`, `/backends`, `/runtime`,
    `/whoami`, `/cron/jobs`, `/bus/subscriptions`, `/api/xbin/sandboxes`
    rows;
  - the backend launch spec, every `confine.Cmd` bind list, and backup
    members;
  - a dry run of pause live reload and of add deployment, and a work-tree
    diff, after which no checkpoint store exists (Z1).

  Templates to follow:
  - `TestLegacyInjectionUnchanged`
    (`internal/server/tileassets_test.go:187`);
  - the joined-string `b.EnvFor` assertions
    (`internal/broker/ingressfn_test.go:120`);
  - `TestResourceBinds` (`internal/runner/resourcebinds_test.go:9`);
  - hub-subscription event assertions
    (`internal/server/termsessions_test.go:91`).

  The legacy fixture also runs unchanged (`test/legacy_workspace_test.go:31`
  and its in-process twin, `internal/boot/boot_test.go:118`).
- **Passes when:** there are zero differences, except values that already
  differ between two runs of today's xbind (per-generation tokens and socket
  numbers, PIDs, timestamps, random names).
- **Milestone:** M1, and a regression gate for every later milestone.

### SC-OPT-OUT — opting out restores the zero state

- **Criterion.** Resuming live reload on a tile whose only deployment is
  `main`, with default settings, removes the deployment record and the view
  repository. The observable clauses Z2–Z10 then hold again, at once: no
  fetch remote is injected or served, and no event names the tile's
  deployments.
- **Measured by:** the SC-ZERO suite, run after pause live reload → reload
  now → resume live reload.
- **Passes when:** there are zero differences. The only permitted leftover
  is the checkpoint store with its deploy log ([05-model.md](05-model.md)
  §2), which nothing reads until the tile opts in again.
- **Milestone:** M1.

### SC-LATENCY-DEFAULT — the default path keeps its budgets

- **Criterion.** The budgets in [../dev-flow.md](../dev-flow.md)
  (`plans/dev-flow.md:102-105`) hold:
  - static save → frame reload < 500 ms;
  - Go save → new backend serving < 2 s with a warm cache;
  - keystroke echo < 30 ms on a LAN.

  They hold for a zero-state tile and, in M2, for a tile whose live reload
  target is a non-primary deployment (P8).
- **Measured by:** an integration benchmark of at least 30 saves. These
  budgets are asserted nowhere today
  ([research/delivery-infra.md](research/delivery-infra.md) §1.2), so the
  benchmark and its baseline land before any watcher or runner change
  (NP-02-9).
- **Passes when:**
  - the absolute budgets hold at p95;
  - p95 regresses by no more than the larger of 10 % and 50 ms;
  - instrumentation counts zero extra confined runs, filesystem walks or disk
    reads per save batch for a zero-state tile.
- **Milestone:** M1 (the baseline comes first), M2.

### SC-LIVE-RELOAD-PAUSE — pausing live reload stops saves reaching anyone

- **Criterion.** After the response to **pause live reload**, no save changes
  what any deployment serves until an explicit reload now, deploy, promote,
  roll back or resume. The checkpoint taken by pausing live reload contains
  every save that completed before the request arrived.
- **Measured by:** a race test that saves continuously across the call that
  pauses live reload. It then compares served frontend bytes, backend
  answers, and behaviour after a reap, a crash, a grant change and an xbind
  restart against the checkpoint.
- **Passes when:** there are zero leaks in at least 100 runs for each of
  static, go, node and python. If the deploy that pausing live reload
  performs fails, the API and the terminal window say so; the deployment is
  pinned to the attempted checkpoint in state `failed`, and every restart
  runs that checkpoint, never the work tree ([05-model.md](05-model.md) §5).
  The race test covers that case too.
- **Milestone:** M1.

### SC-LATENCY-OPS — targets for pause live reload, reload now, deploy, promote

The checkpoint is the only work these operations add to what a save already
does. The design's budget for it is **1 s**, capture plus materialization.

| Operation | Tile | p95 target | Hard bound | Rationale |
|---|---|---|---|---|
| Checkpoint (capture + materialize) | R-static, R-go, R-node | 0.5 s | 5 s, else the operation fails and nothing changes | 1–3 confined runs at 17–45 ms each (D78, `plans/DECISIONS.md:1995-1996`) plus hashing ≈2 MB. D77 already captures a tile's tree (`git add -A` + `write-tree`, `internal/term/agentdiff.go:270-284`) on every agent tool call, with an 8 s cut-off (`:41`). |
| Checkpoint, incremental (≤ 100 changed files) | R-large | 5 s | 60 s | Stress case. The first checkpoint of R-large is reported, not gated; [07-runtime.md](07-runtime.md) §2.10 estimates 2–4 s for the capture and 3–6 s for a full materialization. |
| Pause live reload | R-static | 1 s | 5 s | A checkpoint, then a pointer change on the static plane. |
| Pause live reload | R-node | 1.5 s | 10 s | Checkpoint, start and socket health (`internal/runner/health.go:12-15`), no build: the code-identical case in [05-model.md](05-model.md) §5. |
| Pause live reload | R-go | 3.5 s | the confined build timeout | Always one warm-cache build of the checkpoint: the watcher ignores directories a checkpoint includes, so the work tree's `bin` is never reused ([07-runtime.md](07-runtime.md) §8.6). |
| Reload now, resume | R-static | 1.5 s | 5 s | Today's 500 ms save budget plus the 1 s checkpoint budget. |
| Reload now, resume | R-go | 3 s | — | Today's 2 s warm budget plus 1 s. |
| Reload now, resume | R-node | 2 s | 10 s | Start and health check; no build. |
| Deploy, promote or roll back to an existing checkpoint whose artifact is kept | all | static 1 s, backends 2 s | 10 s | No checkpoint and no build. The bound is the 5 s health timeout (`internal/runner/runner.go:44`) plus start slack. |
| Deploy that needs a build | R-go | build + 1.5 s | the confined build timeout | ([research/runner-hot-reload.md](research/runner-hot-reload.md) §2b) |
| Reassign the primary (the new primary is healthy, its artifact kept) | all | static 1 s, backends 2 s | 10 s | Routing, plus the new primary's restart as primary first (start and health check, no build); the old primary restarts after ([05-model.md](05-model.md) §5). |

- **Measured by:** integration timings from the request to the first request
  served by the new code, with every open frame of the deployment reloaded.
- **Passes when:** every p95 target and hard bound holds on the reference
  tiles.
- **Milestone:** M1 (checkpoint, pause live reload, reload now, resume, roll
  back), M2 (deploy, promote, reassign).

### SC-ROLLBACK — a rollback completes within a bound

- **Criterion.** Rolling a deployment back to any of its previous three
  deploy-log entries:
  - serves new requests with the rolled-back code within 2 s p95 and 10 s
    worst case for backends, and 1 s p95 for static tiles;
  - needs no build, no network, and no read of the work tree;
  - leaves the deployment's data byte-identical;
  - pauses live reload if the deployment was its target.
- **Measured by:** integration runs on R-go and R-static, with networking
  disabled for the build sandbox and the Go toolchain absent, and a hash of
  the deployment's data before and after.
- **Passes when:** every bound holds, the data is unchanged, and the live
  reload state is as stated. This depends on
  [07-runtime.md](07-runtime.md) §2.8's artifact retention (the current and
  last three per deployment) and on NP-02-3's env-layer retention.
- **Milestone:** M1 (`main`), M2 (every deployment).

### SC-PINNED — pinned means pinned

- **Criterion.** A pinned deployment runs its checkpoint through every
  restart path (P9):
  - idle reap, crash restart, crash-loop recovery;
  - a grant or binding change, a provider nudge, alwaysOn backoff;
  - lifecycle re-enable, vault unseal, xbind restart;
  - loss of `.xbin/`.

  What a pinned primary serves and provisions follows its checkpoint, not
  the work tree ([05-model.md](05-model.md) §6):
  - its inbound surface: `template`, `exposes`, `expose.roles`, `provides`,
    `chrome`;
  - the resources its checkpoint's `scope.json` declares (P22), read beneath
    the checkpoint and validated before anything is provisioned;
  - `/components`' `runtime`, `hasIndex` and `native`, while
    `manifestError`, `roles`, `uses` and `deps` keep describing the work
    tree.
- **Measured by:** one test per restart path, each run after changing the
  work tree, asserting that the served code equals the checkpoint; and a
  work-tree edit to each inbound-surface field and to `scope.json`'s
  resources, asserting what the primary exposes, frames, provisions and
  reports.
- **Passes when:** no path runs work-tree code, and no work-tree edit changes
  what the pinned primary exposes, frames or provisions.
- **Milestone:** M1.

### SC-SAFE-DEPLOY — a failed deploy is invisible to users

- **Criterion.** A deploy, promote, roll back or reload now whose build,
  start or health check fails:
  - leaves the deployment serving its previous code;
  - emits no `build-start`, `build-error`, `reload` or `status` for any
    component of the tile: its phases and its failure ride only the
    `deployments` event;
  - leaves the tile's reported status as it was;
  - returns the failure to the actor (the API answer, `bx` exit 1 and its
    output, the terminal window), with the compiler output in the deploy
    log's entry.

  A successful swap emits one `reload` for the primary's bare component,
  only when the primary's code changed, and clears the primary's status at
  the swap. A non-primary swap is announced only in `deployments`. Saves and
  resume onto a primary that follows the work tree keep today's events
  ([05-model.md](05-model.md) §8).
- **Measured by:** fault injection per runtime (a broken build, a crash at
  start, a health timeout). An open primary frame and a client using the
  shipped iOS event parser are subscribed throughout.
- **Passes when:** the previous code serves every request during and after
  the failure, no old-type event reaches a viewer, the status is unchanged,
  and the error reaches the actor.
- **Milestone:** M1.

### SC-INBOUND — no inbound edge reaches a non-primary deployment

- **Criterion.** With a non-primary deployment present (live reload attached
  to it, deliveries off), every inbound edge of the tile reaches the primary
  ([research/inbound-edges.md](research/inbound-edges.md) E1–E16):
  - the bare `/c/` and `/api/` URLs and the native document;
  - consumers' interface bindings and grant calls;
  - ingress HTTP and L4 streams, stream interfaces, lan-ingress and
    net-provider splices;
  - cron ticks, bus push deliveries, event triggers, alwaysOn;
  - archiver calls, notify links, and backups of record.

  A non-primary deployment is reached only through:
  - its deployment URL, by humans with at least `write`;
  - the tile's own sessions, through their target;
  - its own self-calls;
  - its own registrations, once a manager turns deliveries on;
  - run now on one of its dormant cron jobs (terminal level).
- **Measured by:**
  - a matrix test with a backend that reports which deployment answered;
  - a static inventory guard, in the pattern of `TestNoDirectExec`
    (`internal/confine/guard_test.go:22`). `Runner.Ensure`
    (`internal/runner/runner.go:190`) keeps meaning the primary, and
    deployment-aware names sit beside it. The guard lists every call site of
    both. Today's `Ensure` call sites are `internal/proxy/proxy.go:182`,
    `internal/proxy/ingress.go:58`, `internal/runner/ingress.go:37`,
    `internal/runner/netmux.go:62`, `internal/runner/alwayson.go:74` and
    `internal/runner/runner.go:281`. A call site of a deployment-aware name
    must carry an annotation saying why it may reach a non-primary
    deployment.
- **Passes when:** no matrix cell reaches a non-primary deployment and the
  guard finds no unannotated call site. After a reassignment (S-F), the same
  matrix reaches the new primary.
- **Milestone:** M2.

### SC-DATA — no direct access to the primary's data

- **Criterion.** A non-primary deployment never reads or writes its tile's
  primary deployment data or vault through the data plane, except through a
  seed or a vault copy. Both are explicit, logged manager acts. The data
  plane here means:
  - resource APIs and bound paths;
  - `XBIN_RES_*` values and hand-built `res:<scope>/…` ids;
  - bus publishes and subscriptions in its own scope;
  - vault reads built from `Self()`.

  Reading another tile's API under the `read` edge policy is an edge, not
  data-plane access (SC-CLAMP; [05-model.md](05-model.md) §9).
- **Measured by:**
  - a backend on `dev` that exercises every resource kind (kv, sqlite, blob,
    filesystem, bus, cron) through every addressing path and reads every
    vault key;
  - a session targeting `dev` that writes kv and blob values through the
    resource APIs and sets a vault key (S-11);
  - with the primary's data and vault hashed before and after.
- **Passes when:**
  - before a seed, `dev` sees an empty namespace and placeholder vault keys;
  - the primary's data and vault are byte-identical after `dev`'s and the
    session's writes, which land only in `dev`'s namespace and vault;
  - after a seed, `dev` sees the copy, and the primary is still unchanged by
    `dev`'s writes.
- **Milestone:** M2.

### SC-CLAMP — no writes through an edge

- **Criterion.** No call from a non-primary deployment, over any edge, gets
  more than `reader` on any provider's primary.
  - Edges that can't be read-clamped are blocked for non-primary deployments
    in v1, with no override (P23): custom roles with no path to `reader`
    (among them the agent tile's `consumer` role on its sandbox managers),
    stream interfaces, lan-ingress links and net-provider splices.
    [09-fabric.md](09-fabric.md) §5.1 lists them.
  - The net slot and capability grants take `inherit` (the default) or
    `block`. `inherit` never reaches host networking or a provider splice: a
    tile whose `net` resolves to host sharing gives its non-primary
    deployments no egress, with the reason shown. `gpu:*` defaults to
    `block`.
  - A `block` edge fails closed, with an error that names the policy. An
    unknown or invalid policy value reads as `block` (P27).
  - Callees see `X-XBin-Deployment` on non-primary calls.
- **Measured by:** a write attempt over each edge kind: interface binding,
  grant call, a custom-role binding, cross-scope `res:`, bus subscription,
  stream, lan-ingress, the net slot of a relay tile and of a host-sharing
  tile, a `gpu:*` grant, and llm-gw's `/v1/*` at `writer`.
- **Passes when:** no write succeeds, every unclampable edge refuses, the
  host-sharing tile's non-primary deployment has no egress, and the header
  is present on every non-primary call and absent on every primary call.
- **Milestone:** M2.

### SC-DORMANT — non-primary background work stays dormant

- **Criterion.** Cron jobs, bus push subscriptions, interface instances and
  ingress hosts registered by a non-primary deployment (P13):
  - never fire or route while deliveries are off;
  - never overwrite or delete the primary's registrations;
  - are never loaded as `main`'s by an older xbind.

  Its notifications are never pushed.
- **Measured by:**
  - a backend that registers at start, the SDK's documented pattern, running
    on both `main` and `dev`, with deliveries counted;
  - a downgrade run with the previous release's binary.
- **Passes when:**
  - `dev` gets no deliveries while its deliveries are off;
  - `main`'s registration stores are byte-identical after `dev` starts;
  - no notification is pushed;
  - the downgrade fires none of `dev`'s registrations.
- **Milestone:** M2.

### SC-EVENTS — primary viewers never see a non-primary deployment

- **Criterion** ([05-model.md](05-model.md) §8):
  - Non-primary activity never rides `reload`, `build-*`, `status` or
    notify, and no old event type ever carries a qualified `component`. It
    rides only the `deployments` event, which names the deployment.
  - Facts naming a non-primary deployment (its name, builds, compiler
    output, status, would-notify lines, data operations) reach only admins,
    humans with at least `write` on the tile at their current level, that
    deployment's own principals, and the tile's terminal and agent sessions.
    The primary's frame token, minted for readers, is not one of them. Other
    tiles receive none of it.
  - A reader's `GET /api/xbin/deployments` holds only primary-scoped facts:
    no non-primary deployment names, and no counts that reveal them.
    `/components` adds only the primary summary.
- **Measured by:** subscriptions as a reader, through the primary's frame
  token, as a writer, as another tile's principal, and through the shipped
  iOS event parser, while `dev` builds, crashes, reports status, notifies
  and is seeded; the reader's and the writer's `GET /api/xbin/deployments`
  and `/components`; a writer lowered to `read` mid-stream.
- **Passes when:**
  - no old-type event names `dev` or carries a qualified component;
  - no primary frame reloads or shows an overlay, and the primary's status
    never changes;
  - the reader, the primary's frame token and the other tile receive nothing
    that names `dev`, and the lowered writer stops receiving from the next
    event;
  - the reader's state answers 200 with the filtered view, never a 403.
- **Milestone:** M2.

### SC-PROTECT — a protected primary changes only by a manager's hand

- **Criterion.** While the primary is protected:
  - no change to its code succeeds unless a tile manager makes it in a human
    session, naming the reviewed checkpoint: `checkpoint` on deploy and roll
    back, `expect` on promote, reload now and reassignment. A request
    without it answers 400. The check is a compare-and-set on the record's
    `seq`, so a record or work tree that moved since the review answers 409;
  - live reload can't attach or resume onto it, and the terminal's API
    dropdown never offers it (P24);
  - every refusal names the protection and who can act.
- **Measured by:** every code-changing operation, tried by every kind of
  principal: a terminal token and an agent token (a manager's own
  included), a `terminal` human, a `write` human, a view-as session, an
  element principal of a tile holding `xbin:admin` or `xbin:users`, and a
  manager in a human session, with and without the reviewed checkpoint.
- **Passes when:** only the manager's human session naming a current
  reviewed checkpoint succeeds. Refusals are deterministic and
  machine-readable: the protected-primary 403 of
  [11-contract.md](11-contract.md) §1.14, and `bx` exit 3 (refused).
- **Milestone:** M2.

### SC-AGENT-BX — an agent completes flow C with `bx`

- **Criterion.** An agent session targeting `dev` can do all of the
  following with only `bx` and the terminal's `curl` and `git`, and no
  browser. [/docs/bx.md](/docs/bx.md) already names curl as the way to call
  a tile (`docs/bx.md:3-8`).
  1. Read the tile's deployments, the primary, pinned checkpoints, the live
     reload target and its own target.
  2. See `dev`'s build result and logs, per deployment and through
     `GET /logs`. `bx logs` reads `.xbin/log` directly today
     (`cmd/bx/main.go:511`), and `.xbin` is masked in tile terminals
     (`internal/term/binds.go:50`).
  3. Call `dev`'s API as `$XBIN_URL/api/$XBIN_COMPONENT/…`.
  4. Trigger one of `dev`'s dormant cron jobs with run now (terminal level).
  5. See what a promotion would change: `bx deployment diff`, or
     `git fetch xbin-deploy` and a git diff against `deploy/main`.
  6. Promote `dev → main` at parity, and roll `main` back.
  7. On a protected primary, get a refusal it can report, with nothing
     changed.

  Every `bx` command that changes where saves go — deploying onto the live
  reload target, pausing live reload, attaching it — says so in its output.
  The exit codes are [11-contract.md](11-contract.md) §9.1's: without a
  terminal a changing command needs `--yes`, else it exits 4 (not
  confirmed); a refusal exits 3; a deploy still running when `bx` stops
  waiting exits 5; an xbind without tile deployments exits 6.
- **Measured by:** an integration test that runs the steps with an isolated
  session's token and no terminal. The M1 variant is pause live reload,
  reload now, `bx status`, `bx logs`, roll back, resume.
- **Passes when:** every step succeeds, or exits 3 in step 7, with no UI and
  no host token.
- **Milestone:** M1 (the variant), M2.

### SC-WORKTREE — deployment operations never write the work tree

- **Criterion.** No deployment operation creates, changes or deletes any file
  under the tile directory, `.git` included. That covers pause live reload,
  resume, reload now, deploy, promote, roll back, add, remove, seed, reset,
  reassign, protect, dry runs and diffs. The fetch remote reaches sessions
  only as `GIT_CONFIG_*` env entries, never in `.git/config`
  ([05-model.md](05-model.md) §3).
- **Measured by:** a hash of the tile directory, `.git` included, before and
  after each operation.
- **Passes when:** the hashes are identical.
- **Milestone:** M1, M2.

### SC-AUDIT — every code change is in the deploy log

- **Criterion.** Every change to what a deployment runs, by any operation and
  any principal, has a deploy-log entry. The entry records who (the human,
  and the session kind when a terminal or agent token acted), when, the
  feed, the checkpoint, and how.
- **Measured by:** an operation × principal matrix, counting entries.
- **Passes when:** every change has its entry, and entries survive an xbind
  restart and are included in component backups.
- **Milestone:** M1.

### SC-FAIL-CLOSED — refusals instead of fallbacks

- **Criterion.** These are refused with a reason, and none of them ever falls
  back to serving the work tree or to wider authority (P18, P19, P26, D78):
  - without `--isolate`, pausing live reload, pinning, and non-primary
    deployments for go, node and python tiles: non-isolated mode is
    unsupported for them;
  - non-primary deployments for chrome and `xbin`-capable tiles, and
    approving an `xbin`/`xbin:*` grant while a tile has non-primary
    deployments;
  - a non-primary principal on any `/api/xbin/*` route not classified
    deployment-scoped or neutral, reads included. A guard test keeps the
    classification complete.
- **Measured by:** the operations on a non-isolated daemon (the integration
  suite's setup, `test/integration_test.go:85`), on each excluded tile
  class, and a sweep of every `/api/xbin/*` route with a non-primary
  instance token.
- **Passes when:** every refusal happens before any state changes, and
  pausing live reload on a static tile works normally.
- **Milestone:** M1 (pausing live reload), M2 (deployments, route
  classification).

### SC-PRIMARY-FIRST — a non-primary deployment never starves its primary

- **Criterion.** A non-primary deployment never takes what its primary needs
  (P25):
  - its memory, pids and CPU use never lowers what the primary can use below
    today's per-component caps (`internal/boot/boot.go:565-572`): each
    deployment's backend runs in its own cgroup with its own caps, and the
    primary has the higher CPU weight inside the tile;
  - its VM reservation leaves the primary's guest size free in the budget,
    and never preempts a primary start.
- **Measured by:**
  - a `dev` backend that exhausts its own memory;
  - the VM budget filled with the tile's non-primary guests up to the
    headroom, followed by a primary crash, then one more non-primary start.
- **Passes when:** the primary keeps its caps and restarts. The extra
  non-primary start is the one refused, and it is recorded in the D112
  failure ring as `refused` (`internal/sbx/sbx.go:90`).
- **Milestone:** M2.

## 6. The zero-change guarantee

A tile is in the **zero state** when it has no deployment record
(`data/deployments/<TileKey>.json`, [05-model.md](05-model.md) §3). A tile
that never opted in is in the zero state, and opting out returns a tile to it
(P5).

**The contract.** Every clause is normative. A violation blocks the release,
and is reverted, never waived.

- **Z1 — Nothing new exists.** Unless someone asks for a dry run or a diff
  (below), xbind creates for a tile that never opted in:
  - no deployment record, checkpoint store, view repository or materialized
    checkpoint;
  - no per-checkpoint artifact and no per-deployment file;
  - no process, no timer, and no confined run.

  A save costs no confined run, filesystem walk or disk read beyond today's.
  The only added work is an in-memory check that the record is absent.

  A dry run of pause live reload or add deployment computes the impact
  without capturing. A work-tree diff answers 409 and captures nothing
  ([05-model.md](05-model.md) §5; [11-contract.md](11-contract.md) §1.11).
  The checkpoint store exists only after a committed opt-in.
- **Z2 — The same URLs.** These resolve and respond exactly as today —
  status, headers and bodies, including the head injection
  (`internal/server/static.go:431-466`):
  - `/c/<tile>/…` and `/c/<tile>/?native=1`;
  - `/api/<tile>/…`;
  - the tile-origin and asset-token URLs (D95).

  A qualified URL resolves only for tiles with a deployment record, and only
  after today's resolution fails. An existing component whose own path
  contains `+`, and `<tile>+main` on a zero-state tile, therefore resolve
  exactly as today ([01-glossary.md](01-glossary.md)).
- **Z3 — The same events.** Every event about the tile keeps today's `type`,
  bare `component` and fields. None carries `deployment`, and no
  `deployments` event names the tile. A save publishes today's sequence:
  `reload`, `build-start`, then `build-ok` or `build-error`
  (`internal/boot/serve.go:178`; `internal/runner/runner.go:288-358`).
- **Z4 — The same env.** The backend env has today's names and values
  (`internal/runner/runner.go:423-431`; `internal/broker/resources.go:71`),
  with no `XBIN_DEPLOYMENT`. Terminal and agent sessions get today's env
  (`internal/term/term.go:740-793`), with no new `GIT_CONFIG_*` entries: the
  fetch remote is injected only while a tile has a record.
- **Z5 — The same headers and tokens.**
  - Requests to its backend carry today's `X-XBin-*` set, never
    `X-XBin-Deployment`.
  - Frame tokens keep today's format, with no deployment claim
    (`internal/auth/frametoken.go:255-262`).
  - Instance, terminal and agent tokens, and `/whoami` answers, are
    unchanged.
- **Z6 — The same keys and paths.** Every storage key the tile uses stays
  today's:
  - resource namespaces and their encryption labels;
  - `data/vault/<CompKey>.json`;
  - `data/cron-jobs.json` and `data/bus-subscriptions.json`;
  - interface instances and ingress hosts in the root `xbin.json`;
  - `.xbin/log/<CompKey>.log`, which is documented
    (`docs/protocol.md:2401`);
  - `.xbin/build/<CompKey>/bin`, the run dir and the env layer;
  - the backup archive key (`internal/broker/backup.go:45`);
  - prefs and agent history.
- **Z7 — The same API responses.** Every existing endpoint returns today's
  field set for the tile: new fields are `omitempty` and absent.
  - The D112 registry row keeps its ID, `backend:<CompKey>:g<gen>`
    (`internal/runner/sbx.go:63`), and gains no field. `main`'s rows stay
    byte-identical even after the tile gets a record; only non-`main`
    entries carry a deployment.
  - `/backends` gains no key: the old scaffold shell counts its keys
    ([research/builder-contract.md](research/builder-contract.md), hazards).
  - `GET /api/xbin/deployments` answers `record:false` and writes nothing.
- **Z8 — The same sandboxes.** The backend's launch spec equals today's:
  - binds: the work tree read-only at its own path
    (`internal/runner/runner.go:641`), the run dir, the gateway socket, and
    `resourceBinds` (`:647`);
  - argv, env and egress;
  - the VM reservation, owned by the tile path (`internal/runner/vm.go:80`);
  - the effective cgroup limits (`internal/boot/boot.go:565-572`).

  Every existing `confine.Cmd` caller keeps today's binds: the new bind
  destination defaults to `Dir` (`internal/confine/confine.go:75-76`). The
  shared-layer changes of M1/M2 default to today's values for `main` of a
  zero-state tile. Its backend stays in today's flat `comp-<CompKey>` leaf
  (`internal/cgroup/cgroup_linux.go:89`), reported unchanged as `leaf`
  (`internal/runner/sbx.go:66`). The per-tile parent exists only from a
  tile's first non-`main` deployment, so main-only tiles with a record keep
  the flat leaf too ([07-runtime.md](07-runtime.md) §10.3).
- **Z9 — The same behaviour.**
  - Saves live-reload within today's budgets (SC-LATENCY-DEFAULT).
  - Every restart path rebuilds from the work tree, as today.
  - Lifecycle, transfer, clone, templates, import, builtin updates, code
    PRs, backup and restore behave as today.
  - A component backup contains today's members.
- **Z10 — The same UI behaviour.** The tile's frames, overlays and reload
  targeting, the scaffold shell and the native app behave as today. The
  terminal window's API dropdown keeps today's two entries, tile API and no
  API (`web/frame-titlebar.js:162-167`), because the primary is the tile's
  only deployment. The one visible difference is in the terminal window:
  - it offers the opt-in controls ([10-ux.md](10-ux.md)) to users with
    `terminal` on the tile;
  - they show disabled, with the server's reason, where the tile can't opt
    in (S-9);
  - reading the tile's empty deployment state is a GET that changes nothing.
- **Z11 — No silent opt-in.**
  - No boot, migration, update, template, clone, import or restore moves a
    tile out of the zero state. Only an explicit operation by someone who
    holds its authority does ([05-model.md](05-model.md) §10).
  - No manifest key exists that could do it (P5).
  - The legacy fixture's allowed paths and its byte-identical root
    `xbin.json` stay as they are (`test/legacy_workspace_test.go:69-87`).
- **Z12 — Exit.** Opting out (SC-OPT-OUT) re-establishes Z2–Z10 at once. The
  permitted leftover is the checkpoint store with its deploy log; the view
  repository is removed with the record ([05-model.md](05-model.md) §2).

**Permitted workspace-level differences.** These are not per tile:
- New endpoints, a new event type, new `bx` commands, new docs and a
  changelog entry, all additive (rules 2, 6 and 11 of
  [/docs/compat.md](/docs/compat.md)).
- Two narrow refusals, for every creator: no tile at `<P>+<N>` while `P` has
  deployment `N`, and no deployment `N` on `P` while a component exists at
  `<P>+<N>`. They arise only once a tile has deployments. Other new tile
  names containing `+` get a one-release warning, never a refusal, the D82
  way (the precedent for `:` is `internal/broker/policy.go:162-165`), with a
  changelog line.
- A per-tile cgroup parent exists for tiles that run a non-`main` deployment
  (Z8).

**Outside the contract.**
- Tiles that opted in: [05-model.md](05-model.md) defines their behaviour.
- Downgrading a workspace that has opted-in tiles:
  [12-compat.md](12-compat.md) owns it. At goal level, an older xbind must
  never fire a non-primary deployment's registrations or serve its data at a
  bare URL; the per-deployment files of [05-model.md](05-model.md) §3 exist
  for that. An older xbind runs `main` from the work tree, so unpromoted work
  in the work tree reaches users after a downgrade. 12-compat.md must say
  so.

**Enforcement.** SC-ZERO and SC-OPT-OUT prove the contract. They run in
`make test` (in-process) and `make integration`.

## Open questions

Every question this document raised is settled; the numbers stay stable.

1. Cgroup leaf naming for zero-state tiles — resolved by
   [05-model.md](05-model.md) §12 and [07-runtime.md](07-runtime.md) §10.3
   (flat leaf until the first non-`main` deployment; Z8).
2. The `+` reservation — resolved by the narrow-refusal rule
   ([05-model.md](05-model.md) §7; §6 above).
3. What "cannot target" a protected primary covers — resolved by P24.
4. Dependency trees in checkpoints — answered by
   [07-runtime.md](07-runtime.md) §2.2 and §2.10 (incremental capture,
   NP-07-8's differential materialization); R-large's first checkpoint is
   reported, not gated.
5. Reassigning one tile's primary in a multi-tile scope — resolved by P28
   (refused in v1).
6. Who may run now — resolved by [05-model.md](05-model.md) §5 (terminal
   level).
7. Timing criteria in CI — resolved by [15-test-plan.md](15-test-plan.md) §8
   (NP-15-11: a short tier on every run, the full p95 at milestone exits).

## Divergences from the model

None remain.

1. Shared (scope, name) namespaces vs per-deployment remove, seed and reset —
   resolved by P28.
2. §9's "never a shortcut into the primary's data" holds for the data plane
   only — resolved by [05-model.md](05-model.md) §9 and flow G, which state
   the read-clamp exposure and the `block` answer.
3. Who receives non-primary events — resolved by the event-audience ruling
   ([05-model.md](05-model.md) §8).
4. "Published exactly as today" for a pinned primary — resolved by rule C2
   ([05-model.md](05-model.md) §8).
5. The record after pausing live reload fails — resolved by P9
   ([05-model.md](05-model.md) §4, §5).
6. The zero state and the checkpoint store — resolved by P5
   ([05-model.md](05-model.md) §2).

## New proposals

- **NP-02-1 — Milestone split.** Use the split in the milestone table at the
  top of this document; [14-implementation.md](14-implementation.md) §1
  builds it.
  - Roll back of `main` and the fetch remote are in M1. They need only the
    store and log that pausing live reload creates; roll back answers §1.4
    early, and the fetch remote lets a builder branch from what a pinned
    `main` runs.
  - Protected primary is in M2. Without a non-primary deployment it would
    leave developers nowhere to deploy. Reassignment is in M2 (owner,
    2026-09-27).
- **NP-02-2** — resolved by P5 ([05-model.md](05-model.md) §2).
- **NP-02-3 — Env-layer retention for roll back.** The env layers (`setup`)
  of a deployment's current checkpoint and of its previous three deploy-log
  entries count as references for env-layer GC, so rolling back to them
  never rebuilds a layer and never needs the network.
  - [07-runtime.md](07-runtime.md) §2.8 already keeps the artifacts of the
    current and last three checkpoints per deployment; its §3.3 keep set
    covers running generations, pinned manifests and the live reload
    target's manifest, not roll-back targets.
  - Today only the current env layer is kept
    (`internal/runner/env.go:133-145`).
  - SC-ROLLBACK's bound can't hold if a rollback may rebuild a layer.
- **NP-02-4** — resolved by rule C2 ([05-model.md](05-model.md) §8).
- **NP-02-5** — resolved by the event-audience ruling
  ([05-model.md](05-model.md) §8).
- **NP-02-6** — resolved by the narrow `+` refusals
  ([05-model.md](05-model.md) §7).
- **NP-02-7** — resolved by the fetch-remote ruling
  ([05-model.md](05-model.md) §3).
- **NP-02-8** — see NP-15-1, the survivor for the isolated CI rootfs
  ([14-implementation.md](14-implementation.md) R-7).
- **NP-02-9 — Latency baseline first.** The plans/dev-flow.md budgets
  (`plans/dev-flow.md:102-105`) become an integration benchmark, with a
  recorded baseline, before any watcher or runner change lands.
- **NP-02-10** — resolved by P28.
- **NP-02-11** — resolved by P9 ([05-model.md](05-model.md) §4, §5).
- **NP-02-12** — resolved by P25.
- **NP-02-13 — Synthetic data is the documented default.** S-11's pattern
  is taught wherever builders and agents learn deployments.
  - The builder docs and the AGENTS.md text ([10-ux.md](10-ux.md) §10.2)
    say: fill a non-primary deployment from its own code or fixtures, set
    test secrets with `bx vault set` from a session that targets it, and
    reset and re-run to start clean; seeding and vault copy are the
    exception.
  - The deployments panel offers "Set a test value…" at terminal level
    next to "Copy vault values…", never onto a protected primary.
  - [15-test-plan.md](15-test-plan.md) adds
    `TestTargetedSessionWritesOnlyItsNamespace` (SC-DATA).
