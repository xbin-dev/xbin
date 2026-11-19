# Workspace agent — seeing updates, a builtin's simple update, and a tile agent for the rest

> Status: live — the owner's decision of 2026-11-19 on "does the workspace
> agent have enough tools to manage template tile / builtin tile
> upgrades?" (it had none: both update lists were refused to it as "not
> delegated", `POST /builtins/update` is in the never class "an agent
> doesn't write code", and no change kind covered updates). Decision D248.
> Branch `wsagent/updates`.

The owner's words: "give it tools to see updates and apply simple builtin
updates (incl the PR flow when there are conflicts), any application
should just be done by having the workspace agent prompt a tile agent for
it - let's just make sure it's clear to workspace agents".

Three things, and nothing else:

1. **Seeing** — one read, `GET /delegate/updates`, and one tool, `updates`.
2. **A simple builtin update** — one change kind, `builtin.update {id}`,
   applied through the by-hand route; on a conflict it files the update as
   a proposal (the `--pr` flow) instead of writing anything.
3. **Everything else is a tile agent's job** — a template instance's
   merge, a builtin update that became a proposal: the workspace agent
   starts that tile's coding agent with a brief xbind writes, and its
   person reviews the tile agent's changes. The tool's description and
   every row of its result say so in plain words.

## How updates work today (what this builds on)

- **Builtins** (the scaffold — `shell`, `root`, `apps/welcome`, the
  consoles `tiles/{admin,manager,organisations}`, `tiles/apidocs` — and
  imported builtin tiles) carry provenance: `.xbin/builtins.json` and a
  base snapshot per unit. `GET /builtins/updates` (any authenticated
  principal) lists every unit with a newer embedded version: per file
  `clean` (upstream changed, local not), `new`, `removed`, `user` (local
  only), `conflict` (both changed — or, for an *adopted* unit with no
  recorded base, any difference); pinned units are left out; an
  essential scaffold unit this workspace predates is `missing`.
  `POST /builtins/update {id, mode}` (`xbin:writer`: a workspace admin, or
  a component granted it — the Tile Manager) applies one:
  - `replace` writes upstream's files over the installed copy — **local
    edits are lost** (but a manifest's `partition`);
  - `merge` fast-forwards unmodified files and runs `git merge-file` on
    files both sides changed — **a conflict leaves markers in the live
    files**; not for adopted units;
  - `pr` writes nothing: the update is filed as a change proposal on the
    tile (D49), which its terminal or tile agent applies with
    `git am --3way`; closing it merged records the new version;
  - `pin` / `unpin`.
- **Template instances** are forks: never tracked by that updater. A
  builtin template is served as a git repo (`GET /templates/<name>.git/…`,
  dumb HTTP, any authenticated principal), every instance has it as its
  `template` remote (`http://xbin/api/xbin/templates/<name>.git`), and
  `GET /templates/updates` lists the instances the caller can read whose
  template has a snapshot they haven't merged. The builder merges in the
  instance's terminal; the manifest merges by keys (the
  `bx template merge-manifest` driver xbind names in the instance's repo).
- **Sealed components** (`xbin/…`, the workspace agent) are neither: their
  code is the running release's, `POST /builtins/update` refuses them ("it
  is updated with xbin itself"), and they are in no list. Out of scope —
  said, not changed.

## 1. Seeing: `GET /delegate/updates` and the `updates` tool

```
GET /delegate/updates[?tile=<path>]   the person's agent, under its session.
  → {builtins: [{id, kind: "tile" | "scaffold", tile, title?, from, to,
                 missing?, adopted?, clean, conflicts, edited, level,
                 agent, proposal?: n, next, why?, brief?}],
     instances: [{tile, title?, template, behind, local, legacy?, level,
                  agent, next, why?, brief?}],
     leftOut: {hiddenByYou, blocked}}
  next: "propose" | "tile-agent" | "by-hand"
  brief: {goal, doneWhen, leaveAlone, checkBy, known, commands: [...]}
```

**Class:** `read`, `tile:byTileQuery` (with `?tile=`, the gate holds that
tile as any read naming one; without, the rows are held one by one), not
audited (a listing — it carries no code, log or diff). Partition
`neutral`, deployments `primary only`, a sealed component's own
credential `refused` (it is a person's agent's read).

**⊆ the person's by-hand listing, per row, under the agent's gates.**
Every row is a row the person's own `GET /builtins/updates` or
`GET /templates/updates` gives (same function, called with the gates
below; never more rows):

| Part | The by-hand read | The agent's gate on top |
|---|---|---|
| a builtin row: id, kind, from, to, missing, adopted, clean, conflicts | `GET /builtins/updates`' row (same `Updater.Updates()`) | its tile: the person can read it, inside the conversation's scope, its orgs' rules let the agent in (`delegateHides` — and the row taints the conversation with the tile's orgs, as a listing row does); a row whose tile fails a gate is left out (counted in `leftOut` when the person can read it) |
| `edited` (files changed here only, kept by any update) | the same row's `files` with status `user`, counted | as above |
| `proposal` (an open builtin-update proposal on the tile) | the person's `GET /code/prs?target=<tile>` (read on the tile) | as above |
| an instance row: tile, template, legacy | `GET /templates/updates`' row (same `TemplateInstanceUpdates`) | as above |
| `behind`, `local` | the instance's and the template's git history (`git rev-list`; confined, D78) — the by-hand row's own question (is the template's head in the instance's history?) counted: commits of the template's `main` the instance lacks; non-merge commits of the instance's that aren't the template's and aren't xbind's instantiate commit | as above |
| `title` | the map's row (`GET /delegate/map`) | as above |
| `level`, `agent` | the person's level (`/whoami`), the tile's agent level (the map) | as above |

Cheap and exact, nothing more: the read runs no merge. Whether an update
merges without a conflict is said by the line (§2), which runs the merge
dry when it is proposed.

**`next`, xbind's, per row:**

- a builtin with no open proposal → `propose` (a `builtin.update` line);
- a builtin with an open builtin-update proposal → `tile-agent`, with
  xbind's brief for merging that proposal;
- an instance → `tile-agent`, with xbind's brief for the merge;
- `by-hand`, with `why`, where the agent can't take it further: a tile
  whose agent level is `off` (the consoles always are: a line about them is
  refused — D238's rule, kept; an admin updates them in the Tile Manager's
  updates tab or with `bx builtin update <id> --safe`); a `tile-agent` row
  on a tile where the person has no terminal level (a tile agent opens as
  them: whoever has terminal there merges it); the shell and `root`, which
  no terminal opens on, when their update became a proposal.

**The brief is xbind's text**, built only from xbind's own names — the
tile's path, the template's catalog name, the builtin's id, counts, a
proposal's number — never a title, a commit message, a manifest's words or
anything else a tile wrote (`TestUpdateBriefHoldsNothingTileAuthored`).
`commands` are the exact commands it names, in order, for the tests that
prove them (§3).

**The tool** `updates` (no arguments; `tile` to narrow to one) answers
compact rows, each ending with its next step in plain words; the titles,
the only tile-authored strings in it, come inside one `wsData` frame,
each through `wsPlain`. Its description and its result's last line say
the rule: a builtin's clean update is proposed as `builtin.update`; every
other application — a template's merge, a builtin update that became a
proposal, anything needing a code decision — is the tile's own agent's,
started with xbind's brief (`tile_agent_brief {tile, template: "update"}`,
then `tile_agent_start`), and the workspace agent never merges or edits
code itself. The prompt stays as it is (at its 16 KiB cap): the guidance
is in the descriptions and results.

## 2. A simple builtin update: `builtin.update {id}`

```
builtin.update {id}      POST /builtins/update {id, mode: "safe"}   (an admin's line, always)
```

**What "simple" means — mode `safe`, new on the by-hand route.** Read
from what each mode does to a locally edited builtin: `replace` loses
local edits and `merge` writes conflict markers into live files, so
neither is the line's. `safe`:

- **a tracked unit** (a recorded base): the `merge` plan, computed first
  and written only when every file of it is clean — a file only upstream
  changed is fast-forwarded, a file only this workspace changed is kept as
  it is, a file both changed is merged by `git merge-file` and taken only
  when the merge has no conflict, a file upstream removed is removed only
  when unedited here. **Any** conflict — a merge with markers, or a file
  upstream removed that was edited here — writes nothing, and the update
  is filed as a proposal instead, exactly as `pr` does
  (`ProposeBuiltinPR`), which the tile's agent merges;
- **an adopted unit** (no recorded base): applied only where every file it
  has is upstream's byte for byte (it gains the files it lacks — `replace`
  then overwrites nothing); any difference → the proposal;
- **a missing essential scaffold unit**: installed (`replace`) when
  nothing is at its path; refused when something is;
- **a pinned unit**: refused ("pinned: an admin chose to skip its
  updates — unpin it first"), as is a unit with no update, an unknown id
  and a sealed path (the by-hand route's own refusals).

→ `{applied: "updated" | "installed" | "proposed", files, notes?, pr?, conflicts?}`.
`merge` and `replace` are unchanged; `safe` is added (additive, D248):
`bx builtin update <id> --safe` is the same call by hand. **Local edits are
never overwritten by `safe`** (`TestSafeUpdateNeverOverwritesLocalEdits`).

**Applied through the by-hand route, never a parallel one.** The line's
call is `POST /builtins/update {id, mode: "safe"}`, run through
`handleAPI` as the approving person with their account read again
(`currentPerson`) — the handler's own `requireWriter` decides, and it
refuses anyone who is no workspace admin at that moment. The merge is the
`merge` mode's own code (`applyMerge` split into planning and writing;
`safe` writes the plan only when it is clean), the proposal the `pr`
mode's.

**Why an agent may choose this one code change** ("an agent doesn't write
code" stays the rule, and `POST /builtins/update` stays in the never class
for the agent's own credential): what the agent chooses is only *which*
builtin — the id. The content is the binary's embedded copy; there is no
argument that carries a file, a path, a mode or a version. A
prompt-injected "update it with this content" has nothing to aim: the
kind takes `{id}` and refuses any other key (`TestBuiltinLineTakesOnlyAnID`).
A tile's code is changed by xbind's release as an admin approved it, or by
a tile agent its person watches — never by the workspace agent's text.

**Always an admin's line.** `xbin:writer` is an admin's; the line is never
personal. For a workspace admin it starts unticked (an admin's line of
their own plan) and applies on their tick and a recent sign-in. For anyone
else — an org admin too — it is `needsAdmin`: "needs an admin" and **Ask
an admin**, which sends it to the workspace's admins as any such line. It
never waits, is never deferred (a tile a plan still has to make has no
builtin to update), and no auto-approval path reaches it: it is no request
(so no standing approval or ticket), a change set is never approved from a
notification, and the agent's own call to the route is `never`.
`knows: ["access.set", "builtin.update"]`: a client that doesn't name it
never applies it (409), so an older app — which ticks any unblocked line
of a non-plan set — can't.

**The line is xbind's**, from the unit's id, its path, the versions, the
counts and the dry run:

- `Update the builtin llm-gw (apps/llm-gw) from v3 to v4: 2 files change —
  your local changes are kept` (nothing edited here: `— nothing here was
  edited`);
- `Update the builtin llm-gw (apps/llm-gw) from v3 to v4: 2 files change —
  conflicts with your changes: filed as a proposal for review instead`;
- `Install the builtin page tiles/organisations, which this workspace
  predates (v1, 3 files)`.

Its effects name the files that change and what follows (the backend
rebuilds; or: nothing in the tile changes now, its tile agent merges the
proposal, closing it merged completes the update). **Warned** — so
unticked by default on every review — when it is a scaffold unit:
`SCAFFOLD: <path> is part of this workspace's own chrome — the shell, a
console or a page everyone has: updating it changes what everyone here
sees`. A pinned unit, one with no update, an unknown id and a sealed path
are refused (400 at the proposal for an id that is no builtin; a blocked
line otherwise). The line is pinned (`pin`) to the state it was described
from — the installed files' hashes, the embedded copy's, the decision
(merge, install or proposal) — so a file edited between the proposal and
Apply makes it `stale`; and `safe` decides again when it runs, so a
change in that last moment becomes a proposal, never a conflict marker.

**Held to the conversation** like every plan line (`lineAgentRefusal`:
the tile readable, in scope, not `off`, its orgs letting the agent in) —
in `admin_propose` sets too. The consoles are `off`: refused.

**What the agent reads** of such a line, whoever the person is: `n`,
`kind`, `args`, `why`, the flags, `needs`, `result`, `out` — `{update:
"updated" | "installed" | "proposed", files, tile, proposal?}` — a line
written from its arguments ("Update the builtin tile:llm-gw"), and, when
it is blocked or failed, xbind's reason where it is one of the builtin's
own (pinned, no update, not installed), else the fixed sentence. Never
xbind's line (it holds the dry run's result before the person decided),
the effects (file names), the warnings, `basis`, `pin` or `before`/
`after`. After `merged` or `installed` the result tells it to check the
tile with `tile_status`; after `proposed`, that the update waits in the
tile as proposal #n and that its next step is the tile's agent with
xbind's brief.

## 3. Everything else: a tile agent, with xbind's brief

**How a tile session merges a template's update — as built and proven**
(`TestTemplateUpdateInATileSession`, an isolated xbind, the commands run
in a terminal session on the instance: the same sandbox, environment and
token a tile agent's session has — `term.sessionEnv` is one function for
both):

- the instance's repository has the `template` remote
  `http://xbin/api/xbin/templates/<name>.git` (written when it was made);
- the session's environment rewrites `http://xbin/` to its `XBIN_URL` and
  adds `Authorization: Bearer $XBIN_TOKEN` for that URL alone
  (`GIT_CONFIG_*`, D119g) — the session's tile-scoped terminal token;
- `GET /templates/<name>.git/…` answers any authenticated principal: the
  terminal token included (the test fetches with it);
- `bx` is on the session's `PATH`, so git runs the manifest's merge
  driver.

The commands, in the tile's directory: `git status --short` (stop and
commit first if anything is uncommitted), `git fetch template`,
`git log --oneline HEAD..template/main`, `git merge --no-edit
template/main` (`--allow-unrelated-histories` for a legacy instance); then
`bx status`, a call to the tile's own API, `bx logs $XBIN_COMPONENT` —
xbind rebuilds the backend after the merge by itself.

**A builtin update's proposal, resolved from inside the tile**
(`TestBuiltinProposalInATileSession`): `bx code pr fetch <n> <tile>` (the
tile's own terminal token reads its proposals), `git am --3way` on a
clone, resolve, `git am --continue`, pull the result into the tile,
`bx code pr close <n> <tile> --merged` — the tile's own principal may
close it merged (`canDecidePR`), and closing it merged records the new
version (`RecordApplied`): the update is no longer listed.

**What was missing for a tile session, and is added** — found by those
two tests, each the smallest thing under the existing rules:

- **A git identity.** In the sandbox git can't make up an address (the
  host name has no domain: "unable to auto-detect email address"), so every
  commit — the template's merge commit, `git am`, `git am --continue` —
  failed in a session whose person set none. A sandboxed session's
  environment now carries `EMAIL=<person>@xbin.invalid`: git's last
  resort, below `user.email` in any config file and `GIT_*_EMAIL`, so an
  identity the person set wins (`TestGitIdentityIsAFallback`); the name
  stays git's own (the sandbox user's, `root`) unless they set one.
  `.invalid` never delivers. No tool runs; nothing is written.
- **The proposal's copy lives in the tile's own `.git`**, not `/tmp`: the
  terminal read guard (Landlock) grants the sandbox's `/tmp` only as it is
  when the session starts where the workspace itself lives under `/tmp`
  (a test's, a demo's), so a clone there failed ("could not lock config
  file … Permission denied"); the tile's `.git` is readable and writable
  in every session on the tile, holds no live file and nothing git tracks.
- **The read says when a tile agent of the person's can't reach xbin.**
  The brief fetches through xbind — the template remote, `bx code pr` —
  and a non-admin's sessions get a tile-API token only with the terminal
  API grant and a network only with the terminal network grant or the
  tile's org's network (D17). Where they lack either, the row's next step
  is `by-hand`, saying which (`SessionReachesXbind`); no grant is widened.
  An admin's sessions always reach it — and `builtin.update` is an
  admin's line, so a builtin's proposal is merged by an admin's tile agent.

**Where the agent reads it:** the `updates` result's row for the tile
carries the brief; `tile_agent_brief {tile, template: "update"}` writes
it into a brief the person sees and may edit — the backend asks xbind for
that tile's row (`?tile=`) and takes its brief as xbind wrote it, so the
model never writes the steps — and `tile_agent_start` starts it, an
approved start as today. A plan's `agent.start` takes the same template.

## Every new route, field, kind and tool

| What | Class | The rule it keeps |
|---|---|---|
| `GET /delegate/updates[?tile=]` | delegate `read`, `tile:byTileQuery`, not audited; partition neutral; deployments primary only; sealed refused | ⊆ the person's `GET /builtins/updates` / `GET /templates/updates` rows, each under the agent's gates (readable, scope, org rules, taint); the brief names nothing tile-authored |
| kind `builtin.update {id}` | — (a change-set kind, in `admin_propose` sets and plans) | the by-hand route at Apply with the approver's current rights; always an admin's line; never personal, never waits, never deferred; scaffold warned; pinned / no update / sealed refused; held to the conversation; agent reads args, flags, result, out |
| mode `safe` on `POST /builtins/update` | the route's (`xbin:writer`); delegate `never` for the agent's own credential (unchanged) | never overwrites a local edit; a conflict becomes a proposal, nothing written; additive |
| `bx builtin update <id> --safe` | — | the CLI a superset |
| `knows: "builtin.update"` | the person's own approve | an older client never applies the kind |
| `out` of a `builtin.update` line `{update, files, tile, proposal?}` | the person's set; the agent reads it | the outcome, nothing of the files |
| tool `updates`; `builtin.update` in `admin_propose` / `plan_propose`; brief template `update` (`tile_agent_brief`, a plan's `agent.start`) | — | prompt unchanged (at its cap): descriptions and results |

## Tests and the rule each holds (red without it)

Filled in as built — see "Red runs" below.

## Not done

- Updating sealed components: they update with xbind itself.
- The consoles through the agent: their agent level is `off` (D238); an
  admin updates them by hand. The owner may overrule.
- Listing pinned builtins: the by-hand listing leaves them out, and so
  does this one (⊆); a pinned id is refused with its reason.
- A template instance's merge by xbind: it is the tile agent's, by design.
