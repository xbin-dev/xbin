# B2d — records for the integrator

Work pack B2d (agent: non-secure (hosted) conversations and "share a copy"
— 08 §4 as amended by the owner, 90 §I4; PD-32, PD-33) of the
partitioned-tiles plan, branch `pt/b2d` on `partitions` at 3092f0f2. It
builds on B2b (shared conversations at global, two homes, the bundle —
records/B2b.md) and B2c (partition mail's handlers — records/B2c.md), and
W3b's `team` (`teamStore`, `teamUnavailable`, `openShared`). No xbind
change: no new xbind HTTP/WS surface (docs/protocol.md untouched), no
route-class rows; every new route is the agent template's own (API.md). This
pack edits neither docs/changelog.md nor plans/DECISIONS.md; the texts below
go there at merge.

Commits (oldest first): the backend (`1b7fbfaf`), the audience ring for an
idle conversation found by the e2e and the forwarder's lock (`05fa5c70`),
the web page (`25917c7a`), the isolated e2e (`901f71d6`), docs (`189f434f`),
the harness pass (`42af204a`), who-is-new / approvals / the fan-out's event
allowlist (`748a10a8`), the native view and the features registry
(`adc63cfb`), the host's push and API.md's native sentence (`de865d26`),
the composer strip's CSS and a pause's words (`28ec365f`), and this record.

**Review fixes, branch `pt/b2d-fix`** (on `pt/b2d`; the review's 14
findings, each fixed or answered in "Review fixes" below): a hosted run's
missing tools (`70b1b2d2`), the backend's failure paths (`2877e0f7`), their
tests (`2c55afbb`), the page and the native modal (`d3eac8ee`), API.md
(`b5c9cc57`), a file split (`91ea3d45`), and this record's update.

## What was built

- **`team` holds the agent's run schema** (`team_runs.go`): the global
  instance re-applies `migrate()` to team at every start under team's migrate
  flock (team's own schema 1→2 adds `team_hosts`); runs there are numbered
  from **2^39** — below 2^40, so a person's page reaches a hosted
  conversation at the global instance with B2b's `homes.js` unchanged, and
  far above anything global's own db numbers, so global tells a hosted id by
  the number. A person's partition checks team column by column against an
  in-memory reference of this code's schema (`teamCovers`) and waits for
  global like for team's own schema (W3b's `openShared`/`teamUnavailable`).
- **Hosting** (`hosted.go`, a person's partition): `POST /hosting
  {conversation, seen}` → the global instance moves the conversation into
  team (`POST /hosted`: B2b's `exportConv` from global's db, `importConv`
  into team — text session files, transcript, task ledger, audience,
  members' pins; the run's config sanitized with `confConfig` so no MCP
  header reaches team; join links deleted; the original deleted) and the
  partition records it in its own **`hosted` table** — the authority — with
  the audience snapshot the person was shown (a wider one at move time is
  recorded paused at once). `GET /hosting`, `POST /hosting/{id}/confirm
  {seen}`, `/decline`, `DELETE /hosting/{id}`.
- **The host's engine** (`hosted_engine.go`): a second `Engine` in the
  person's partition over team — lock `<team>.engine.<partition id>`, epoch
  `engine_epoch.<partition id>` in team's settings (fencing per host), its
  own wake-up at exit (the partition's `resume` only for active hosted
  work), the partition's model gate, tools, blob store and hold. Its scope
  (`hostDrives`, at every pass and before marking a run driven) is: the
  conversation is **active in the partition's own `hosted` table** and its
  team ACL is within the snapshot (`audience.beyond`). A team row naming the
  person as host, an inbox row, a status — none of it makes the engine take
  anything else up (`TestHostEngineDrivesOnlyItsOwn`).
- **Live for every member (90 §I4)**: the host engine's hub has a tap
  (`eventHub.tap`, nil elsewhere); a forwarder batches its events (20 ms,
  drafts coalesced — a commit resets coalescing so a draft never jumps ahead
  of one) and posts them to its own global instance (`POST /hosted/events`,
  F5, attributed). Global publishes them on **its own hub** for the
  conversation's subscribers (subscribed under team's ACL), re-reads a run's
  summary from team instead of trusting the post, keeps the draft in flight
  for a member who connects mid-answer, and accepts only a run's own event
  types, only for conversations team says the caller hosts. A batch global
  doesn't take after four tries becomes partition mail `hosted/changed`
  (durable; global re-reads the conversation).
- **The members at global** (`hosted_serve.go`, `hostedRoute` — one wrapper
  line in `routes()`): a route on a hosted id is served from team with the
  agent's own code (a never-started "team view" engine sharing global's hub;
  `viewWith`/`streamWith` — handleView/handleStream take an agent now):
  view (with `hosted`, also on run summaries via `Engine.decorate`), stream,
  `GET /runs/{id}`, message/answer (into team's inbox + a ring:
  `hosted/input` mail to `user:<host>`, coalesced per host, a final refusal
  marks hosting `gone`), interrupt/cancel (a signal ring), queued-message
  removal, approve (a grant is refused), asks, tree, members (add/remove —
  a ring with the `audience` signal), PATCH (title, pins, visibility, model;
  class/sandbox 409), read, files (text), delete; join links **409**;
  anything else 409. `GET /conversations` at global adds the caller's hosted
  conversations to its first page (with `hosted`). `POST /hosted/{id}/continue`
  (a participant, once hosting ended — declined, taken back, 7 days paused,
  host gone): a plain shared conversation at global again, the team copy
  deleted.
- **Widening → pause** (`pauseHosting`): a wider audience than confirmed
  pauses the conversation in the host's table and in team (`team_hosts`:
  `paused`, reason `confirm`, pending = the audience and who is new), the
  step in flight is signalled `errHandoff` (a model call writes nothing and
  is made again after a confirmation; a tool call in flight ends as after a
  restart), members' messages answer 409; the ring's
  `audience` signal makes the host look at once even when nothing runs.
  The host is asked on the page (above the composer; in the native view's
  composer) and by a push to their xbin app (`NotifyUserWith`, `#c=<id>`).
  Confirm takes exactly what was shown (`seen` = pendingKey, 409 when it
  changed again). 7 days without an answer drops hosting (a timer while the
  partition runs, a sweep at the host engine's start; global shows it
  dropped after 7 days whatever the partition says).
- **"Add a copy of my …" (PD-32)** (`copyin.go`): `POST /copyin
  {conversation, files: [{run, path}]}` in a person's partition reads their
  own session files and posts them (F5) to `POST /runs/{id}/copyin` at
  global (a participant of a shared, non-hosted conversation): they land
  under `from-<person>/` (source `copy`), with a note step; the originals
  stay where they are.
- **The web page** (`hosted-ui.js`, pure `model/hosted.js`): the ⚠ **not
  private** chip (header, list row); the warning — whose private resources,
  who can read it (members, the agent's managers, workspace admins, anyone
  who can change the code) — opens by itself each time a hosted
  conversation is opened into a page session, **Start anyway** / **Open
  without sending**, no "don't show again"; the composer (and Send) locked
  until started, while paused (the host asked above the composer: Confirm /
  Decline) and once ended (Continue without <host>'s private resources).
  The share dialog of a shared conversation: **Use my private resources…**
  (the warning in its hosting form, then `POST /hosting` with the audience
  shown) and **Add a copy of my files…** (pick one of your conversations,
  its files; who can read the copies; originals stay private); a host's
  **Take them back**. Hooks: agent.js 3 lines (998/1016), share.js 2,
  sidebar.js 2.
- **The native view** (`native/hosted.js`): the warning heads the
  transcript, the composer is locked the same way with its buttons (Start
  anyway, the host's Confirm/Decline, Continue without …); the row says ⚠
  not private. Hosting and adding a copy are DIFFERENCES (the web's for now).

## Review fixes (pt/b2d-fix)

Every high and medium finding is fixed; the lows too. By finding:

1. **A hosted run's `schedule` wrote the host's cron** (high). Team
   numbers schedules from 1 like the partition, so `sched-<id>` was the
   host's private job; `unschedule #N` deleted it. A hosted run now has
   **no schedule, automation-thread or skill tools** — absent from its
   tool list, refused if called (`hosted_tools.go`; one line each in
   `runToolSpecs` and `runTool`). The thread tools went too (they read the
   owner's other threads in team: other hosts' conversations), and the
   skill tools (team's `skills` table is every hosted conversation's).
   Audited the rest of the engine's tool paths for the global `agent`
   (partition state) instead of the run's `ag`: none.
2. **A paused conversation's audience changing again** (high): `hostDrives`
   now looks at a paused one too — a further change rewrites `hosted.pending`
   and `team_hosts.pending` (`repauseHosting`, `setTeamHostPending`: the
   7 days keep running from the first pause) and republishes, so the
   prompt's key follows; an audience back within the snapshot makes it
   active again (`resumeHosting`).
3. **The host removed** (medium): `hostDrives` requires the host to be a
   participant (owner, member participant, or the team as participant);
   otherwise hosting drops (`left`), and confirm refuses.
4. **Mid-turn widening without a ring** (medium): the turn loop checks
   `e.scope` next to `brakeInTurn` and again before `execTools` (two
   line-local lines in `actor.go`; nil on every other engine).
5. **Forged live events** (medium): an event must name a run whose root is
   the conversation the caller hosts; messages, steps, the queue and links
   are re-read from team by id (only drafts are taken as posted, with keys
   made at global); a draft never touches another conversation's.
6. **The move** (medium) is two-phase (`hosted_move.go`): the partition's
   intent row (`hosting_moves`) and a detached context; team's copy is
   `pending` until the partition acks (conditional on the host and on it not
   being continued); `POST /hosted` idempotent by `(moved_from, host)`,
   finishing the delete a stop left, with `lookup: true` for the
   partition's start (`reconcileHostingMoves` — looks, never moves); a
   pending copy unacked for 10 minutes shows `dropped`/`unclaimed`
   (continuable), and one whose original is still at global is deleted
   (`settleHostedMoves`, at global's start and on every move/continue). The
   re-adopt of a dropped one now makes it `pending`, not active.
7. **The host engine's wake-up** (medium): `hostedWake` is `userWake`
   over the active hosted trees (settled links, sleeps on jobs, wake times);
   it leaves `resume`, or `wake` at the earlier of its own and the main
   engine's time (the jobs share the name).
8. **AF's un-share move** (medium): un-sharing a hosted conversation (only
   its owner left: the last member removed or gone, made private with nobody
   in it) ends hosting and brings it back to global as a plain conversation
   (`unshareHosted` — the continue path; the answer carries `movedTo`, the
   deleted `run` event too). AF's `moveIfUnshared` then applies to it: one
   line at merge (below). `POST /hosted` answers 409 while AF's `conv_moves`
   row says `asked` (`convMovingHome` reads AF's table; no table, no row).
9. **Phantom drafts** (low): drafts let go after 60 s silent (with a
   `draft.end`); `hosted/changed` drops the conversation's drafts and sends
   its streams `reset`; the forwarder flushes (one try, then mail) at stop.
10. **Consent failing open** (low): `seen` is required by `POST /hosting`
    and confirm (400); the web's Confirm is disabled and the native one
    absent without a `pendingKey`.
11. **The 7-day timer** (low): every pause arms its own; a confirmation
    after the 7 days drops it (409).
12. **Delete and continue** (low): deleting rings the host (the ring is
    sent with the host read before the delete); continue claims the row
    (`continuing`, a compare-and-set on what it read; a claim older than two
    minutes is a stopped process's and is taken again), imports, leaves a
    tombstone (`continued`, `continued_to`, kept 30 days) that answers a
    retry — or another participant — with the same id.
13. **Spec gaps** (low): PD-33's modal — the native view now opens the
    warning as a **sheet** the first time a hosted conversation is opened in
    an app session (the composer's "Read the warning…" reopens it; the
    notice still heads the transcript). Approvals — **only the host approves
    a parked call** in a hosted conversation (it runs with their resources);
    any participant may deny. `POST /runs/{id}/resume` is served for hosted
    runs (a wake into team + a ring). PD-32's other sources stay a
    deviation and an owner question (below).
14. **Tests** (low): `hosted_fix_test.go` covers each of the above; the
    fencing test now runs real per-host lock files (below).

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: non-secure (hosted) conversations and "Add a copy of
  my …"** (the template's API.md "Partitioned instances" → "Non-secure
  conversations", [partitions.md](/docs/partitions.md) §The mode). In a
  partitioned agent a shared conversation runs at its global instance,
  which reaches no one's own partition. A participant may now let it use
  **their** private resources — their sandboxes, their data in other
  partitioned tiles, their vault: everything their partition reaches —
  which makes it **non-secure**: it moves into the agent's shared `team`
  database (a new id, still below 2^40) and their own partition runs it,
  driving only what its own table lists. The members keep using it at the
  global instance, which rings the host's partition when they write and
  streams the host's run to everyone at once. The page warns everyone who
  opens it — every time, with who can read it and whose resources it uses —
  and keeps the composer locked until they start it; a ⚠ **not private**
  chip marks it. Adding people pauses it until the host confirms them (on
  the page, or from a push); the host's partition stops at its next step.
  Declining, taking the resources back, 7 days without an answer or the
  host leaving it end hosting, and any member may then continue it without
  them; un-sharing it (only its owner left) brings it back to the shared
  instance. Only the host approves a parked tool call in it, and it has no
  schedule, automation-thread or skill tools. It has no join links. The non-hosting alternative: **Add a copy of my files…** puts
  copies of your own session files into a shared conversation; the
  originals stay private. New routes, in a partitioned instance only: `GET`
  / `POST /hosting`, `POST /hosting/{id}/confirm|decline`, `DELETE
  /hosting/{id}`, `POST /copyin` (a person's partition); `POST /hosted`,
  `POST /hosted/events`, `POST /hosted/{id}/continue`, `POST
  /runs/{id}/copyin` (the global instance); partition-mail topics
  `hosted/input` and `hosted/changed`. The `team` resource's schema moves to
  2 and carries the agent's run schema (the global instance migrates it; a
  person's partition waits for it). Unpartitioned instances change nothing
  (the routes answer 404). Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, B2d: the agent's non-secure (hosted)
  conversations and "Add a copy of my …" (2026-09-30).** Implements PD-32,
  PD-33 and the agent's side of 90 §I4 (global is the realtime hub) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §4, §9. The template's API.md
  "Non-secure conversations"; docs/partitions.md §The mode.
  - **Chosen.**
    - **Transcript in `team`, with the agent's own run schema**, numbered
      from 2^39: the host drives it with the unchanged engine, global reads
      it with the unchanged view/stream code, and a hosted id is below 2^40
      (pages reach it at global, `homes.js` unchanged) and above global's
      own ids (global tells it by the number). Global re-applies
      `migrate()` to team at every start; a partition compares team's
      columns with its own code's schema and waits for global (W3b's rule).
    - **The host's own `hosted` table is the authority**; `team_hosts`
      (host, state, pending) is a display hint. The host engine is a second
      `Engine` over team with its own lock and epoch key per partition id,
      a scope checked at every pass (and before a run is marked driven):
      active in the table, audience within the confirmed snapshot. Global's
      engine never drives team (its team view is never started).
    - **Members at global use the routes of any shared conversation**:
      `hostedRoute` serves a route on a hosted id from team (a curated set;
      the rest 409), so the page needs no new calls; inputs go into team's
      inbox and ring the host by partition mail (`hosted/input`, coalesced
      per host; signals for interrupt/cancel/audience), which wakes a
      stopped partition.
    - **Live (90 §I4)**: the host engine's hub taps into a batched F5 post
      (`POST /hosted/events`, 20 ms, drafts coalesced), which global
      publishes on its own hub for the members' streams under team's ACL —
      run summaries re-read from team, only a run's own event types, only
      for conversations team says the caller hosts. Mail (`hosted/changed`)
      is the durable fallback when the post fails.
    - **Widening pauses** (a member added, the team let in, a viewer made a
      participant, another owner): the host engine stops at its next look
      — or at once on the audience ring — abandoning the step in flight (a
      model call writes nothing and is made again after the confirmation;
      a tool call ends as after a restart); the host is asked on the page
      and by a push; members' inputs 409; the host confirms exactly what
      they were shown (`seen`) or
      declines. Declining, taking the resources back, 7 days unanswered or
      the host's partition refusing mail for good end hosting; any
      participant then continues it at global without them (a copy back).
    - **The warning (PD-33)**: a modal each time a hosted conversation is
      opened into a page session (and before hosting), listing its members,
      the agent's managers, workspace admins and anyone who can change its
      code, and whose resources it uses; Start anyway / Open without
      sending; no "don't show again"; the composer locked until started; a
      ⚠ not private chip on the header and rows; the native view the same
      in its vocabulary.
    - **"Add a copy of my …"** copies a person's own session files into a
      shared conversation at global (`from-<person>/`, a note): no host, no
      pause, no chip; the originals stay private.
    - **Failure paths**: the move is two-phase (the partition's intent, a
      `pending` copy the partition acks; idempotent by the original's id; a
      copy never taken up is continuable after 10 minutes, one whose original
      stayed is deleted); continue claims the conversation and leaves a
      tombstone; the host engine looks at the audience before every step
      (paused again on a further change, resumed when it narrows back,
      dropped when the host is no longer in it); global re-reads every
      durable live event from team; a hosted run lacks the tools that keep
      state outside it (schedules, automation threads, skills); approvals
      are the host's; un-sharing ends hosting (90 §I10).
  - **Not chosen:** a mirror of the transcript in global's db (two copies
    and id clashes; team is the ruling's home); making every handler take
    its agent from the request (≈250 call sites in shared files); a
    per-resource choice (the engine runs as the host's partition, which
    xbind attributes as a whole — a partial one would be advisory); mailing
    `hosted/changed` after every commit (the live post carries it; mail is
    the fallback); join links on hosted conversations (they'd admit people
    the host never saw); giving team's schedules their own id range and a
    fire route the host serves from team (a hosted schedule would fire with
    the host's resources long after the members stopped looking — the
    members can schedule after continuing it without the host); refusing
    to un-share a hosted conversation (a member couldn't leave).
```

## Seams for the integrator

| Seam | Where | State / who |
|---|---|---|
| `Engine.epochKey`, `scope`, `wake`, `decorate`; `eventHub.tap` | `engine.go`, `actor.go` (`pass`), `events.go` (`publish`, `publishRun`) | nil/"" everywhere but the host engine and global's team view — every other engine behaves as before |
| `viewWith(w, r, ag, more)`, `streamWith(w, r, ag)` | `stream.go` (handleView/handleStream are one-line wrappers) | reusable for any other store an agent serves |
| `hostedRoute(pattern, need, next)` | `routes.go` (one wrapper in `routes()`), `hosted_serve.go` | a new `/runs/{id}…` route answers 409 on a hosted id until it is added to `hostedHandlers` — **AF / anyone adding a run route**: decide whether hosted conversations get it |
| `teamView()`, `hostedInfo`, `publishHosted` | `hosted_global.go` | global's read of team; anything showing a hosted conversation's state should use `hostedInfo` |
| `teamRuns()`, `teamIDBase`, `hostedID` | `team_runs.go` | the run store in team; ids [2^39, 2^40) |
| mail topics `hosted/input` (global → host), `hosted/changed` (host → global) | `hosted.go` `init` | no files ride them (no `prepareMail` case needed) |
| `hostedRoutes(mux)` | `hosted.go`, one line in `routes()` | the hosting, events, continue, copy-in routes |
| B2b's `exportConv`/`importConv` | used by the move in and out (`moveIntoTeam`, `handleHostedContinue`) with the team view as the agent | `vouchedBy` isn't involved: global's own export vouches for its senders; copy-in carries files, not messages |
| `hosted-ui.js` `hostedPaint(v, app)`, `hostedChipTpl`, `hostedRowChip`, `hostTpl` | agent.js, sidebar.js, share.js | one call each |
| `native/hosted.js` | native/chat.js (notice, composer state, buttons), native/convs.js (row) | — |
| a shared conversation's sandbox picker | not changed | still B2b's open item; a hosted conversation's `PATCH {sandbox}` answers 409 (the agent in it can still use the host's sandboxes through its tools) |
| `hostedToolRefused`, `hostedToolSpecs` | `hosted_tools.go`; one line each in `tools.go` (`runToolSpecs`, `runTool`) | a new toolset that keeps state outside a conversation: add it to `hostedToolsets` |
| `hosting_moves`, `reconcileHostingMoves`, `adoptHosted`, `ackTeamHost` | `hosted_move.go` (a person's partition) | the partition's intent; `startHosting` runs the reconcile |
| `team_hosts` states `pending`, `continuing`, `continued`; column `continued_to` | `hosted_move.go`, `team_runs.go` (schema 2 as shipped here: nothing released) | `setTeamHostState` never overwrites `continuing`/`continued` |
| `unshareHosted` — **AF at merge** | `hosted_move.go` | after the import, add `_ = agent.db.Tx(func(t *DB) error { return t.moveIfUnshared(back.ID) })` (the comment marks the place) |
| `convMovingHome` | `hosted_move.go` | reads AF's `conv_moves` by SQL (false without the table); at merge it may call AF's `moving()` instead |
| `dropConversationTo(ag, root, why, to)` | `hosted_move.go` | the deleted `run` event carries `movedTo` (AF's page follows it) |
| `ringing` (a WaitGroup) | `hosted_global.go` | tests wait for rings in flight (`quickWakes` does) |
| `hostedWake` | `hosted_engine.go` | `userWake`'s rule over given trees; keep it in step with `resume_mode.go` |

## Deviations

- **The resources are all-or-nothing.** 08 §4 has "conversation settings →
  'Use my private …'" with a `resources` list: the host engine runs in the
  host's partition, whose outbound calls xbind attributes to it as a whole
  (sandboxes, other tiles' partitions, the vault), so v1 offers one choice
  and records `["sandboxes","tiles","vault"]`; the warning names exactly
  that. The host's private **skills and memory** and other conversations'
  session files are **not** wired into a hosted run (they live in the host's
  own db); "Add a copy of my files…" is the way to bring files in.
- **Binary session files don't move**: text ones travel with the
  conversation into team (and back on continue); binary ones stay behind,
  named in the move's note (their bytes are in a blob store only one
  instance reads). A binary file the host's run makes in a hosted
  conversation is in the host's blob store: members see it listed, its
  bytes answer 404 at global.
- **`hosted/changed` is the fallback, not every commit**: the live post
  carries each change to global at once; mail goes when a batch doesn't get
  through after four tries.
- **Paused conversations refuse members' inputs** (409) rather than queue
  them for later — matching the locked composer.
- **Managers' count**: the warning says "the agent's managers (everyone
  with write access to it)" — tile code can't count them.
- **Moving changes the id** (global's `#c=<old>` 404s afterwards; the page
  opens the new one). Keeping the id would clash with global's ids in team.
- **At global, the hosted routes are a curated set**; compact, learn,
  memory writes, uploads, sandbox binds, grants, schedules and automations
  on a hosted conversation answer 409 (the top bar still shows some of
  those buttons).
- **agent.js** grew 3 lines (998 of 1016); **hack/ui-harness/shots.js** one
  line (848 of 877).
- **"Add a copy of my …" copies session files only.** PD-32 (decided) also
  lists a memory item, a skill, a document from another partitioned tile
  and sandbox files; v1 builds session files. Owner question below.
- **A hosted run has no schedule, automation-thread or skill tools** (review
  finding 1): the members schedule after continuing it without the host.
- **A move's copy is `pending` until the host's partition takes it up**;
  members' writes answer 409 meanwhile (normally well under a second); one
  no partition takes up within 10 minutes is continuable.
- **xbind doesn't tell a person's frame from their partition's backend**
  (`personFromPartition` admits both): the frame can post `/hosted/events`
  and `POST /hosted` too. Global now trusts nothing durable from the post;
  a frame's direct `POST /hosted` leaves a `pending` copy no partition
  acks — continuable after 10 minutes.

## Bugs found

- **Found by the isolated e2e, fixed** (`05fa5c70`): the audience ring only
  poked runs with work, so an **idle** hosted conversation whose audience
  widened wasn't paused until someone wrote; the ring now checks it at once
  (`TestHostedAudienceMail`).
- **Found by `-race`, fixed**: the forwarder read its post hook outside its
  lock.
- **Found in the harness screenshots, fixed** (`748a10a8`): the paused
  prompt named every member as new (the whole audience) — `team_hosts.pending`
  now carries who is new.
- **Pre-existing, not changed**: the share pill's "shared with N people"
  count isn't live (list rows carry `members`, run summaries don't) — seen
  on a hosted conversation after adding someone.

- **Found by the review, fixed on `pt/b2d-fix`**: the fourteen findings
  in "Review fixes" above — among them a hosted run's `schedule` replacing
  the host's private cron job, a paused conversation stuck on 409, the host
  removed yet still driving it, a frame's forged live events, and a move
  stranded by a closed page.

## Merge risks

- **Shared agent files, line-local edits** (AF also edits the template):
  `_backend/routes.go` (the `hostedRoutes(mux)` line and `hostedRoute(…)`
  around `guard` in `routes()`), `stream.go` (`handleView`/`handleStream`
  became one-line wrappers of `viewWith`/`streamWith`: their bodies use `ag`
  for `agent`), `engine.go` (four fields, `epochName`, the two epoch
  queries, `runActor`'s `markDriving`, `Shutdown`'s wake), `actor.go` (three
  lines at `pass`'s top), `events.go` (`tap`: a field and three lines in
  `publish`; `decorate`: three lines in `publishRun`), `main.go` (three
  lines), `partition_start.go` (three lines in the user branch), `team.go`
  (schema 2, the `runs` field, `check`'s `current`). A new `/runs/{id}…`
  route anyone adds answers 409 on a hosted id until it is added to
  `hostedHandlers` (safe by default).
- **Page**: `agent.js` (an import and two lines: 998 of 1016), `share.js`
  (an import and one line after `copyTpl`), `sidebar.js` (an import and one
  line in `rowTpl`), `native/chat.js` (an import and three lines),
  `native/convs.js` (the row's subtitle), `model/features.js`,
  `web-features.js`, `native-features.js` (appended keys).
- `hack/ui-harness/shots.js`: `PASSES.agentHosted` on its own line after
  `PASSES.channelsPartitioned` — a union with other wave-5 packs' lines;
  `run.sh`: one comment line.
- `test/isolated/partitions_agent_test.go`: one `t.Run("hosted-chats", …)`
  line after `shared-chats` and a header bullet; the case is in
  `partitions_agent_hosted_test.go`.
- `API.md`: a new bullet after "Shared conversations"; the mail topics
  sentence; the `team` bullet. `docs/partitions.md`: a paragraph after the
  shared-conversations sentences of §The mode.

- **AF (pt/af-fix) — review finding 8**: `git merge-tree` conflicts in
  `_backend/routes.go`, `API.md`, `docs/partitions.md`,
  `hack/ui-harness/shots.js` and `test/isolated/partitions_agent_test.go`
  (union both sides' lines). Then the one line in `unshareHosted` (seams),
  and AF's new `/runs/{id}…` routes answer 409 on a hosted id until added to
  `hostedHandlers` (for AF's `/moves` routes nothing is needed: they aren't
  `{id}` run routes). Function names checked against AF: its `movesWait`,
  `moving`, `moveOf` are distinct from this pack's `hostingMovesWait`,
  `convMovingHome`. SH and I1 also add lines after `PASSES` in shots.js.

## Owner questions

- **Per-resource hosting.** 08 §4 lets a person enable "their private …"
  resources one by one; v1 hosts with everything the person's partition
  reaches (sandboxes, their partitions of other tiles, their vault), and the
  warning says so. A per-resource choice would be a code-level restriction
  inside the host's partition (xbind attributes the whole partition), i.e.
  advisory. *Recommendation:* keep all-or-nothing, named exactly in the
  warning; revisit if xbind ever narrows a partition's identity per call.
- **The host's private skills and memory in a hosted run.** Not wired: a
  hosted run reads skills from `team` (none), so the host's private skills
  and other conversations' files don't reach it. *Recommendation:* keep them
  out — anything a hosted run reads lands in a transcript every member reads
  — and extend "Add a copy of my …" to a skill or a memory item (explicit,
  per item), as 08 §4 lists.
- **Binary files in hosted conversations.** They stay in one instance's
  blob store (the move leaves global's behind; the host's are the host's).
  *Recommendation:* a shared blob resource beside `team` in the template
  (`"shared": true`) in a follow-up; T1's merge by keys brings the `uses`
  line to existing instances.

- **"Add a copy of my …" beyond session files.** PD-32 lists a memory
  item, a skill, a document from another partitioned tile and sandbox files.
  *Recommendation:* next, a skill and a memory item (both are the person's
  own rows, one copy each, the same `from-<person>/` note); a document from
  another tile and sandbox files later, through that tile's or sandbox's
  own export — the copy-in route takes files, so only the picker grows.
- **Approvals in a hosted conversation are its host's** (built: only the
  host approves a parked call — it runs with their resources; anyone may
  deny). *Recommendation:* keep; if members need to approve, it is a
  per-conversation delegation the host grants explicitly.
- **xbind can't tell a person's frame from their partition's backend.**
  The agent now trusts nothing durable from `/hosted/events`, but a frame
  can still ask global to move a conversation of theirs directly (it stays
  `pending`, unhosted). *Recommendation:* an xbind header marking calls
  from a partition's backend (instance) vs. its frames, so tile code can
  require the backend for backend-only routes.

## Tests

(final tree; `.dev.mk`'s env exported for the integration runs, the Bash
sandbox off for isolated runs and the harness; no SKIP)

| Run | Result |
|---|---|
| template `_backend`, new (`hosted_test.go`): `TestAudienceBeyond`, `TestHostedAtGlobal` (move, idempotent/one host, links deleted and refused, bob's view, dave 404, a member's message into team + the ring + nothing driven at global, lists, bob's stream gets the host's posted draft as a delta, a non-host's post and a stream's own words refused, mid-answer drafts in `/view`, the audience ring, approve 400, continue 409 → 403 viewer → 200), `TestHostEngineDrivesOnlyItsOwn` (**a forged team row never driven nor marked**, the per-host epoch, the live post, widening → paused with who is new, the host's push, confirm only with what was shown, a view-as call 403, the drop), `TestHostedAudienceMail`, **`TestHostEngineFencing`** (a successor fences its predecessor; another host's takeover fences neither), `TestCopyIn` (**a copied file lands in the shared conversation, the original unchanged**) — `-race -count=5` | PASS |
| **the legacy golden**: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing template test unchanged, plus the new ones | PASS (244 s; 243 s again after the last backend change) |
| **e2e** `go test -tags=integration -run '^TestPartitionsAgent$' ./test/isolated/` on a real `xbind --isolate` — all nine cases; new `hosted-chats`: alice hosts from her partition (id in [2^39, 2^40)), bob reads it at global, dave 404, links 409; **bob writes `paras 12` at global and his stream there carries ≥ 2 `text.delta`s of alice's partition's run before the answer**, then both streams the answer; dave added → paused (bob 409, bob can't confirm from his partition), alice confirms, it answers again; alice's file copied into another shared conversation, read there by bob, her original hers alone | PASS (66 s; hosted-chats 3.2 s) |
| e2e `TestPartitionsAgentChannels` (B2c's mailbox beside the new topics) | PASS (40 s) |
| JS: new `hack/agent-template-hosted.test.mjs`, `hack/agent-template-native-hosted.test.mjs` (the native warning, lock, Start anyway, the host's Confirm); `make fmt-check vet js-check js-test` | PASS (510 tests, 509 pass, 1 skipped as before) |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| template browser tests from a scratch copy: new `test/hosted.mjs` (chips, the warning by itself, Open without sending → locked, the strip drawn only while locked, Start anyway, same page session no warning, the paused host's Confirm with what she saw, Use my private resources → `POST /hosting` with the audience, Add a copy → `POST /copyin`), and `homes partition share sidebar chat home attach native` | PASS |
| UI harness `agentHosted` (PORT 8961, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`, …/scratchpad/h5-B2d, fresh workspace each run): 18 checks — the warning before hosting (who can read it), the move (#1 → #549755813888), the ⚠ chip, the host writes at once; **dev1: the warning by itself, Open without sending → locked, a reload → the warning again, Start anyway → unlocked**, the row's chip; both pages watch the admin's partition's run stream; **sales1 added → dev1 locked "waiting for admin … (new: sales1)"**, the admin asked above the composer, confirms → dev1 unlocked; no page errors. Screenshots looked at: `agent-hosted-host-warning`, `-start-warning`, `-locked`, `-live-dev1`, `-paused-dev1`, `-paused-admin` (the first run's showed the prompt naming everyone as new and an empty strip under an unlocked composer — both fixed, the final run's are clean) | PASS |

| UI harness `agentHomes` beside it (the same page's share dialog, lists and streams; B2b's pass) on the final tree | 20 PASS |
| e2e `TestPartitionsAgent` again on the final tree | PASS (67.5 s) |

Not run: the other harness passes (unchanged surfaces), the partitions
smokes `TestPartitionsSmoke*` (no xbind change). An e2e run in the middle
failed on the box's full `/tmp` (tmpfs out of inodes — builds of parallel
packs), not on the code; re-run alone, it passed.

### On pt/b2d-fix (the review fixes; final tree)

| Run | Result |
|---|---|
| template `_backend`, new `hosted_fix_test.go`: `TestHostedRunLacksPartitionTools` (a control run has `schedule`; a hosted run lacks all nine tools and refuses them; **the probe: with alice's schedule #1 in her partition, a hosted run's `schedule` call is refused, team holds no schedule, #1 unchanged, the model was never offered it**), `TestHostedPausedAudienceChanges` (**carol then dave while paused: asked about both, the first key 409s, the new one confirms; erin added then removed: active again, it answers**), `TestHostRemovedDropsHosting` (**alice removed: dropped `left`, never answers**), `TestHostedWidenMidTurn` (**mallory added during step 1, no ring: paused, the tool never ran, one model step**), `TestHostedEventsChecked` (**alice's draft naming bob's run refused, carol's view keeps bob's draft; a message team doesn't hold refused; a real one published as team has it — no `FORGED` reaches bob**), `TestHostedDraftsLetGo` (a silent draft goes; `hosted/changed` sends `reset` and drops drafts), `TestHostingMoveTwoPhase` (`seen` required; a 503 keeps the intent; the start's lookup takes it up and acks; a never-made move is forgotten; **a copy continued meanwhile isn't taken back**), `TestHostedMoveIdempotent` (**the lookup finishes deleting an original a stop left**; another person 409; a stale copy beside its original deleted; an unclaimed copy continued, **a second continue answers the same id**, one copy at global), `TestHostedContinueClaim`, `TestHostedUnshareAndDelete` (the last member leaving: back at global, a tombstone, the host rung; **deleting rings the host**; AF's `asked` move → 409), `TestHostedApprovalsAndResume` (a participant's approve 403, the host's 200, a deny 200; resume queues a wake), `TestHostingConsentChecks` (`{}` and `""` 400; after 7 days 409 and dropped; a pause's own timer drops it), `TestHostedWakeUp` (a hosted run sleeping 5 min → `wake` at its time; the partition's earlier wake wins; a settled subagent → `resume`; paused → nothing), `TestHostEngineLocks` (real lock files: alice's successor waits on the lock, bob's engine takes its own at once, the successor fences the predecessor after it lets go); the existing hosted tests (TestHostedAtGlobal now acks the pending copy) — `-race -count=3` | PASS |
| **the legacy golden**: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` | PASS (261 s) |
| **e2e** `TestPartitionsAgent` on a real `xbind --isolate` — all nine cases, `hosted-chats` through the two-phase move | PASS (67.8 s; hosted-chats 3.3 s) |
| JS: `hack/agent-template-hosted.test.mjs` (+ the `moving` lock), `hack/agent-template-native-hosted.test.mjs` (+ **the warning sheet opens by itself, Open without sending closes it and leaves the composer locked, Read the warning… reopens it, Start anyway unlocks**; no Confirm without a `pendingKey`); `make fmt-check vet js-check js-test` | PASS (513 tests, 512 pass, 1 skipped as before) |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| template browser tests from a scratch copy: `hosted homes share native chat` | PASS |
| UI harness `agentHosted` | **not re-run** — see below |

The harness pass wasn't re-run on this branch: the box's `/tmp` (tmpfs,
shared by the parallel packs) ran out of inodes while its seed was
building (the tool shell itself failed with ENOSPC), so the run was
stopped and its workspace deleted to give the other packs room. The page
changes it would see are small — Confirm needs a `pendingKey` (the pass's
host always has one), a `moving` lock for the fraction of a second a move
is pending — and the template's browser test `test/hosted.mjs` (the same
flows against the stub backend) passes. Re-run it when `/tmp` has room:
`HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1 hack/ui-harness/run.sh --keep
agentHosted agentHomes`.
