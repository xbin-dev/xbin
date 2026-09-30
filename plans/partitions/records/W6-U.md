# W6-U — agent frontend: coding agents under partitions

W6's frontend pack, [96-agtt-merge.md](../96-agtt-merge.md) §B3 (U-M1 to
U-M5, U-S3 to U-S5). It implements the owner's ruling 90 §I15 in the agent
template's page: coding agents (D147's harnesses) run only in a person's own
conversations. Branch `pt/w6-u` on `pt/w6-base` (3fdb5ca0).

Scope: the agent template's frontend only, meaning `model/`, the web view,
the native view, their tests and the template's API.md.
- No Go changes. `_backend/` is byte-identical to `pt/w6-base`, so the
  legacy golden and the backend tests are untouched.
- No xbind change and no new HTTP/WS surface, so `docs/protocol.md` is
  untouched.
- This pack edits neither `docs/changelog.md` nor `plans/DECISIONS.md`. The
  texts below go there at the fold.

Commits (oldest first):
- the page (`d8f469a2`): the model, both views, the three feature
  registries and API.md;
- the tests (`04d890c4`);
- the composer's words for a sign-in the page can't offer (`4c0d9d63`);
- the dialog's fixed "Who answers" as its own select (`c399cb73`);
- this record.

## What was built

The rules live in one new model file, **`model/harness-homes.js`**. It is
pure: its inputs are `partitionState()`, a run id and a sandbox row. Both
views follow it, and an unpartitioned page gets today's answers from it.

- **U-M1: a coding agent's calls go to the run's home.**
  `model/harness-store.js`'s `call()` now sends
  `x.fetch(url, at(homeOf(runOfPath(path)), opts))`, as `actions.js` does.
  - Every call on a run now goes to that run's home: mode, options, a
    permission's option, a question's answer, authenticate, the log, a
    message (steer), stop, cancel, retry and get.
  - `/harnesses` and `/prefs/harness-mode` name no run, so they stay at the
    person's own partition.
- **U-M2: the native run relay.** `model/terminals.js runTerminalSrc`
  appends `xbin-partition=global` for a global-home id. It combines with
  `login=1` as `?login=1&xbin-partition=global`. An own or unpartitioned id
  keeps the path as before.
- **U-M3: the sandbox a coding agent starts in.** `model/harness.js`
  `sandboxFits` asks `homedWhy(s)` first. In the `user` state, a row that
  isn't homed in this partition doesn't fit. The reason is "‹name› isn't a
  sandbox of your own space (the team's, or shared with you) — a coding
  agent works only in one of your own, where its sign-in stays yours".
  - That covers every harness pick:
    - the composer's ▣ picker (`fitsWhy`);
    - the new-chat dialog's `#n-sandbox` (`sandboxOptions`);
    - `preferredSandbox` and `keepSandbox`, which pass over a remembered
      team sandbox;
    - the setup card (`setupOf`).
  - The setup card offers Create when none fits. Its title and text say "of
    your own". When team or shared rows were passed over, the text also says
    that those are for shared chats. `createPrefill` is unchanged: a sandbox
    made from a person's page is homed in their partition.
  - `agentPicker`'s "signed in on ‹sandbox›" detail is left out for a
    remembered sandbox that isn't the person's own.
  - The seam is in "Seams" below.
- **U-M4: sign-in only where the credentials stay the person's.**
  `signInAway(id)` decides where the sign-in is offered:
  - the global state: never (`SIGNIN_GLOBAL`);
  - the `user` state: only for a run homed in the person's partition. A
    global-home run gets `SIGNIN_SHARED`;
  - legacy: always, as today.

  The rest follows from `signIn()`:
  - `signIn()` carries `away`. `talk` is false while `away` is set, and
    `view` says why.
  - Web: the card is read-only (`signin.js`, and a child card through
    `signInTpl`).
  - Native:
    - the `end` notice says why;
    - the composer has no Sign in button;
    - ⋯ has no **Sign in…** (`native/terminal.js`, now gated on `c.talk`);
    - a child card's notice shows `si.view` (`native/harness-child.js`).
  - The composer's placeholder (`model/harness-ask.js steerWords`) says
    "‹name› is waiting for a sign-in — your message waits with it…" in place
    of "sign in first" (`4c0d9d63`).
- **U-M5: "Who answers".**
  - The new-chat dialog's `#n-agent` (`harness-start.js`) reads
    homes-ui.js's `#n-share` and redraws when it changes. For a share other
    than "Only you" (`sharedNewChat`):
    - `#n-agent` is disabled and set to the built-in agent. The fixed one
      is a one-option select of its own (`c399cb73`), so the person's pick
      shows again when the share goes back;
    - `#n-agent-shared` says why, and the sandbox field goes away;
    - the class and instructions fields come back;
    - the ask carries no `harness`.

    Switching back to "Only you" restores the pick.
  - In the `global` state, `agentPicker` lists no coding agent, so it is not
    shown on the web or in the app. `app.harness.picked()` is null there, so
    `askPart`, `newClassId` and `keepSandbox` treat the built-in agent as
    the one that answers. `newChatPick` ignores a harness id.
  - The catalog is still read at global: managers' ⚙ Coding agents and
    ⚙ Classes need it.
- **U-S3: nothing moves a coding agent's conversation.** `keepsHome(r)` is
  true for a harness run at its root. A child's root is the built-in
  agent's.
  - `rules.js`: `rowMenu` has no **Share a copy…**. `topBar` has
    `publish: false`, and its `shareRun` carries `engine` for such a root.
  - `share.js`: the dialog of a shared harness root leaves out `copyTpl`
    (Copy to my own space).
  - `hosted-ui.js`: `hostTpl(…, {host: false})` leaves out "Use my private
    resources…" (A-M2's UI side). "Add a copy of my files…" stays.
  - `topChip`'s title adds "stays in your own space" for the person's own
    harness conversation. With no share pill, it is the one place that says
    so.
- **U-S4: a relay holds its partition.** API.md's "Terminal relays" gains a
  bullet: the app's relay socket is a held connection
  ([/docs/partitions.md](/docs/partitions.md) §How people's partitions run).
  While the Terminal screen is up, it keeps the person's partition running,
  or the global instance for `?xbin-partition=global`. `native/terminal.js`'s
  header says the same.
- **U-S5: registries and tests.**
  - `state.partition.harness` is in `model/features.js`, `web-features.js`
    and `native-features.js`. The native entry says that a shared new chat
    is the web's (`state.partition.newShared`).
  - The tests are in "Tests" below.
- **API.md**: in §Coding agents, a new paragraph "In a partitioned instance
  (the UI)" sits after "Terminals and sign-in in the UI", and the relay
  bullet is added. The frontend table gains a `harness-homes.js` row.

## Seams

- **Per-row `homed` / `bindWhy` (W6-A, A-S3).** `homedWhy` takes the
  backend's word when a GET /sandboxes row has a boolean `homed`. If `homed`
  is false, its reason is `bindWhy`, or else the default text.
  - Until then the page derives it as `_backend/sandbox_partition.go
    homedHere` checks it: not `shared`, `owner.partitionId` set,
    `owner.partition === xbin.partition` and `owner.via === xbin.self`. (The
    page can't see the partition id, so it compares the key.)
  - The plan says `homed`/`bindWhy` and the task said `homed`/`why`. I read
    `bindWhy`, because a bare `why` on a sandbox row is ambiguous. If W6-A
    names it otherwise, one line changes in `homedWhy`.
- **A-S3's "keyed by home" for `prefs/harness-sandbox`.** Only the page
  writes that pref. With U-M3 a remembered ref that isn't the person's own
  doesn't fit, so `keepSandbox` replaces it with a homed one. The page needs
  no key by home. For the catalog's `sandboxes` map, `signedIn` of a row
  that isn't homed is ignored by the picker (`seenOn`, `sandboxFits`).
- **A-M1 (409 at global on authenticate and `login=1`).** The page no longer
  offers either at global or for a global-home run, so the 409 is defence in
  depth. The run relay without `login=1` (a shell) is still offered, relayed
  at global.
- **A-M3 (409 for a tree with live harness children).** The page hides the
  move actions only for a harness **root**. A built-in conversation with
  live coding agents below it still shows the following, and the backend's
  409 is shown as the dialog's error:
  - Share a copy… (its row carries `kids`, but it may finish);
  - Copy to my own space;
  - the share dialog's un-share, "Only you and the people below". This holds
    for a harness root at global too.
- **The native sandbox relay (`relaySrc`, not the run relay)** in a shared
  conversation dials the person's partition. U-M2 left it as it was. A-S2
  sends that relay through `partitionBoxRefusal`, which would refuse a team
  sandbox's terminal there. But `sandbox_partition.go`'s header says "Its
  terminal can still be opened". The wiring pass should pick one rule; if
  the refusal stays, `relaySrc` needs the list's home like `runTerminalSrc`.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: coding agents only in your own conversations** (the
  template's API.md "Coding agents" → "In a partitioned instance (the UI)").
  In a partitioned agent a coding agent (Claude Code, Codex, Gemini CLI,
  opencode) works only in a person's own conversations: its sign-in lives
  in its sandbox's home, and the shared space holds no one's credentials.
  The page now follows that:
  - The agent's shared instance (the owner token) doesn't offer "Who
    answers".
  - In your own partition a coding agent starts only in a sandbox of your
    own space. The team's sandboxes, and ones shared with you, are shown as
    not fitting, with the reason, and the setup card offers to create one.
  - New chat with options answers a chat shared with others with the
    built-in agent.
  - A sign-in is offered only in your own conversations; elsewhere its card
    says why.
  - A coding agent's conversation has no Share a copy…, Copy to my own
    space or "Use my private resources…".
  - A shared conversation's coding agent is reached at the shared instance,
    so its buttons (approve, answer, mode, stop…) and the app's terminal
    work there.
  - The app's terminal keeps the partition it reaches running while its
    screen is up.

  Unpartitioned instances change nothing. Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

This is the UI part of B4's "coding agents under partitions" decision. It
folds into that entry or stands on its own:

```markdown
- **D<next> — Partitioned tiles, W6-U: coding agents in the agent's page
  under partitions (2026-09-30).** The page's side of the owner's ruling
  plans/partitions/90-decisions.md §I15 (coding agents only in a person's
  own conversations); plan plans/partitions/96-agtt-merge.md §B3; the
  template's API.md "Coding agents" → "In a partitioned instance (the UI)".
  - **Chosen.**
    - **One pure model file** (`model/harness-homes.js`) holds where a
      coding agent starts, which sandbox is the person's own, where a
      sign-in is offered, a shared new chat's "Who answers", and that a
      coding agent's conversation never moves. Both views follow it, and
      it answers today's values unpartitioned.
    - **A sandbox is the person's own** by the backend's per-row word when
      it sends one (`homed`, `bindWhy`), else derived as the backend's
      `homedHere` checks it (not `shared`; the owner's partition and tile
      are this page's). Rows that aren't stay listed, disabled with the
      reason, so a person sees why the team's box isn't offered and is
      offered Create.
    - **Sign-in is read-only away from the person's own partition** (the
      global instance's page, a shared conversation's run). The card still
      says what it waits for; nothing on the page asks the backend for a
      sign-in it refuses.
    - **Calls follow the run's home** (the harness store and the app's run
      relay), as every other call about a conversation does.
    - **"Share a copy", "Copy to my own space" and hosting are hidden on a
      coding agent's root conversation**; copying one's files into a
      shared one stays.
  - **Rejected.**
    - Emptying the catalog at the global instance: its managers set up
      classes and coding agents there (⚙ Coding agents, ⚙ Classes).
    - Hiding the move actions on a built-in conversation with live coding
      agents below it: that state passes, and the backend's 409 says why.
    - Keying `prefs/harness-sandbox` by home: a remembered sandbox that
      isn't the person's own doesn't fit, and is replaced.
```

## Deviations

- **Hosting hidden, copy-in kept.** U-S3 names only Share a copy and Copy
  to my own space. A-M2 (a harness conversation never becomes hosted) has
  the same UI side, so "Use my private resources…" is hidden too. "Add a
  copy of my files…" moves nothing and stays.
- **Extras beyond B3:**
  - the composer's placeholder for a sign-in the page doesn't offer;
  - `topChip`'s "stays in your own space" line;
  - `agentPicker` leaving out "signed in on" for a sandbox that isn't the
    person's own.
- **The seam's field name** is `bindWhy`, not `why` (see "Seams").
- **The read-only sign-in card offers no Retry.** "Elsewhere a read-only
  card says why" is taken literally.

## Bugs found

- **My own, caught before review (`c399cb73`).** The first version of U-M5
  marked the built-in agent `selected` in the same select. Once the person
  had picked a coding agent, that option was dirty. Switching the share
  back to "Only you" then left the select showing the built-in agent while
  the dialog would start the coding agent. `test/homes.mjs`'s round trip
  fails on that version and passes now.
- **The native child card's sign-in notice ignored `si.view`.** A view-only
  reader of a parent conversation was told "Open it (↗) and tap Sign in."
  for a child they may only read. Fixed along with U-M4
  (`native/harness-child.js` now shows `view` first).
- **Seen, not fixed (outside B3):** in a person's partition the built-in
  agent's sandbox picker still offers the team's sandboxes. The backend
  (`partitionBoxRefusal`) refuses to bind them for any conversation there,
  so picking one fails at bind time with the backend's words. The same
  `homedWhy` could grey them out in `model/sandboxes.js sandboxPicker`. That
  area is B2b's and AF's; flagged for the wiring pass or the owner.

## Owner questions

1. Should the built-in agent's sandbox picker in a person's partition also
   show the team's sandboxes disabled, with the reason, as a coding agent's
   pick now does? Today it offers them and the bind is refused.
2. A shared (global-home) coding-agent conversation exists only as data from
   before A-M1 and A-M2. Should its share dialog also hide un-sharing (make
   private), which A-M3 refuses? Today it is offered and the refusal is
   shown.

## Tests

On `pt/w6-u`:

- **`make js-test`**: 633 tests, 632 pass, 1 skipped (as on the base), 0
  fail. The new cases:
  - `hack/agent-template-homes.test.mjs`, four tests:
    - the rules;
    - in the `user` state: a shared run's twelve harness calls at global,
      the relay src, the sandbox pick and setup card, the read-only sign-in
      and its placeholder, the row menu, top bar and chip;
    - the `global` state: no picker, `picked()` null, `newChatPick`
      built-in, sign-in away;
    - unpartitioned as ever.
  - `hack/agent-template-harness-term.test.mjs`, one native test: a shared
    conversation's coding agent in a partition shows a notice saying why,
    with no Sign in in the composer or ⋯, and its Terminal relayed with
    `?xbin-partition=global`. The global instance's page is the same.
- **`make js-check`**, and `go test ./internal/sizebudget
  ./internal/assetscan ./internal/docscheck ./internal/builtins`: ok.
- **The template's browser tests** from a scratch copy: homes, partition,
  share, hosted, native, harness, harness-ask, harness-board, harness-cards,
  harness-child, harness-manage, harness-start and harness-term all passed.
  - `test/homes.mjs` gained the shared harness row:
    - her coding agent's row menu and top bar have no share;
    - the chip's title;
    - a shared coding agent's permission is answered at global;
    - its share dialog has no `#sh-copy` and no `#host-use`, and keeps
      `#copy-mine`;
    - the new-chat dialog: `#n-agent` is enabled for "Only you", then
      disabled with `#n-agent-shared` for the team. Back to "Only you" it
      shows her pick again. The ask is made at global with no `harness`.
  - A mutation run (the `call()` routing, the dialog's `keeps` and the
    `#n-share` rule reverted in the scratch copy) failed those checks.
- **UI harness `agentHarness`**, unisolated (PORT=9111,
  `HARNESS_DIR=…/w6/W6-U/h`, `run.sh --keep agentHarness`, then `--stop`):
  72 PASS, 0 FAIL, no SKIP. It ran on `4c0d9d63`, the code before
  `c399cb73`; the new-chat dialog is unchanged unpartitioned. That is the
  legacy page end to end, web and native. The workspace `ws/` was deleted
  afterwards.
- **Not run:** `hack/tile-check.sh agent`. No Go changed and the script
  copies only `_backend/`. The isolated e2e is B5's.
