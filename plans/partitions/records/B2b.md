# B2b — records for the integrator

Work pack B2b (agent: shared conversations, two homes — 08 §3, PD-31; the
owner's 90 §I4) of the partitioned-tiles plan, branch `pt/b2b` on
`partitions` at d6499604. It builds on B2a (records/B2a.md, the seams
`userNoShare`, `sharesInPartition`, `sharing()`, global mode's F5 persons)
and F9 (own-global addressing: `xbin.fetch(…, {partition: 'global'})`,
`?xbin-partition=global`, `GlobalURL`). No xbind change: no new xbind
HTTP/WS surface (docs/protocol.md untouched), no route-class rows. This
pack edits neither docs/changelog.md nor plans/DECISIONS.md; the texts
below go there at merge.

Commits: the backend (`e2454350`), the page's two homes (`5ec856ec`), a
copy reading whole bundles + the app's uploads (`0eb38947`), the e2e and
fakeopenai's prefix (`c40d5184`), docs (`7497871f`), the harness pass
(`0a676be5`), and this record. The review's fixes are on `pt/b2b-fix`
(see "Review fixes" below): the backend (`dfef7ff4`), the page
(`86991036`), API.md (`be7e0e21`), the e2e and harness pass, and this
record's update.

## Bugs found

- **hack/fakeopenai**: `paras N` / `long N` ("starts the message") never
  matched in a shared conversation, where the agent prefixes a person's
  message with `[<user id>] ` — fixed (test infrastructure).
- **Found and fixed before commit**: `POST /copy` read global's export
  through `callGlobal`, whose answer `gwDo` cuts at 4 MiB — a copy with
  files would have been truncated; it reads through its own call now.
- **Found by the review, fixed on `pt/b2b-fix`** (all B2b's own code; the
  table under "Review fixes"): a person could import a bundle naming
  someone else as a message's writer; the merged list could list a
  conversation twice; a person's draft upload at global left a private
  run in the shared space; a copy lost its task ledger and stubs; the
  native view's images of a shared conversation 404'd; an oversize copy
  failed as a truncated body's 400.
- **Found while fixing, fixed (tests only)**: B2b's own homes tests
  switched the mode and `confIn` while their runs' passes and titles still
  read them — a data race under `-race -count=6` (`homeAgent` waited for
  titles only, and a title could start after the wait); `quiet()` now waits
  for no pass, then no title, before a mode change and at cleanup.
- **Pre-existing, fixed (test only)**: B2a's `TestBrakeRequests` swapped
  `confIn` while the run its first ask queued was still in a pass reading
  it — a data race under `-race` 4 runs in 5 on this box, on the
  partitions base (d6499604) alike, which failed `hack/tile-check.sh
  agent`. It now waits for that pass to end first; nothing it asserts
  changed (`b8dffee2`).
- **Observed, not changed** (legacy look): the existing share dialog's
  visibility radios sit inside `.field`, so `.field input { width: 100% }`
  stretches them and `.field label`'s uppercase applies to their labels
  (read from index.html's CSS; the same markup rendered that way in "Share
  a copy" until `#pubdlg` got its own two rules). A follow-up could add the
  same rules for `#sharedlg`; it changes an unpartitioned page's look, so
  this pack left it.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: shared conversations in a partitioned agent** (the
  template's API.md "Partitioned instances", [partitions.md](/docs/partitions.md)
  §The mode). In a partitioned agent a shared conversation lives at the
  agent's global instance — in its database, run by it, so it reaches no
  one's own partition — and people's pages reach it through their own
  global instance (`?xbin-partition=global`, attributed to them), where the
  sharing rules apply to them exactly as in an unpartitioned agent:
  sharing with the team or people, join links, each person's pins and read
  state. A conversation's id says its home (a person's own from 2^40, the
  shared space's below), so `#c=<id>` addresses, push links and the iOS
  app's saved places keep working. Everyone in a shared conversation
  follows its stream at the global instance, so all see a run as it
  streams. The page's **Mine** lists your own conversations and the shared
  ones you take part in, **Shared** the shared space's; the web's Share on
  one of your own is **Share a copy…** (a copy goes to the shared space —
  who can see it, its session files or not, the original kept or deleted),
  a shared one's share dialog offers **Copy to my own space**, and **New
  chat with options** asks who can see the new chat (only you, the team,
  or people you name). A copy is the conversation the model reads — every
  message and tool result, its task ledger — and names a message's writer
  only when that is who made the copy: anyone else's message comes as
  "copied · <id>", never as theirs. New routes, in a partitioned instance
  only: `POST /runs/{id}/publish`, `POST /copy`, `GET /runs/{id}/export`,
  `POST /import` (413 over 48 MiB); `POST /ask` takes `share` (in every
  instance — unpartitioned it shares the new conversation at once); at the
  global instance a person's `POST /ask` must carry it (409 otherwise) and
  their `POST /runs` and `PUT /ask/upload` are refused; `POST /join` in a
  person's partition is redeemed at the global instance. An unpartitioned
  instance's page, and the global instance's own (the owner token), are
  unchanged. Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, B2b: the agent's shared conversations at
  the global instance, two homes on the page (2026-09-30).** Implements
  PD-31 and the agent's side of 90 §I4 (global is the realtime hub) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §3. The template's API.md
  "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **Shared conversations are the global instance's**: made there with
      `POST /ask {…, share}` (the team to read or write, and/or people),
      reached by people's pages with F9's `{partition: 'global'}`; global
      reads such a call as the person (B2a's `principal`/`agentRole`), so
      D83 applies unchanged — no new ACL code.
    - **The shared space keeps shared conversations**: a person's attributed
      `POST /ask` at global without `share` (or with a draft) is 409/400,
      and their `POST /runs` and `PUT /ask/upload` (a new chat's draft) are
      409 — a buggy or old page can't start a private chat of a person's in
      global's db (B2a's open question). A person may still un-share one
      there later (an owner question). The owner token (the global-viewer
      state) asks as ever.
    - **Two homes by id** (`model/homes.js`): below 2^40 the global
      instance's, from 2^40 the person's partition; every call about a
      conversation — path `/runs/<id>…` — and the stream following it go to
      its home (`model/home-api.js` wraps the kit's `selfApi`/`xbin.fetch`);
      only a person's partition has two homes, so unpartitioned and
      global-instance pages send exactly what they did.
    - **Streams**: the page's own stream always; the global instance's
      follows a shared conversation while it is open, and the run list only
      while the list shows shared rows (or the Shared view) — else it is
      closed, so a page with nothing shared never keeps the global instance
      busy; such a page reads the shared space's first page again when it
      shows or gains focus (at most every 15 s), and lists what was shared
      with the person since. Both pause while hidden (B2a). Live for every
      member (90 §I4) holds by construction: each member's page follows
      global's stream.
    - **The merged list**: Mine reads both homes and cuts at a horizon (the
      newest of the last rows, as read, of homes that have more pages),
      holding rows below it until the next page, so paging never shows a
      row above one a later page could still bring; live events reach held
      rows too, and each conversation is listed once; Shared is global's;
      search and "needs you" read both.
    - **Copies between homes**: `POST /runs/{id}/publish {share, files?,
      keep?}` in a person's partition exports the conversation (the root's
      transcript as the model reads it — tool calls and results, folded and
      masked ones marked, the summary, the task ledger on the messages it
      pins; session files only with `files`, ≤ 16 MiB together) and sends
      it to global's `POST /import` through `callGlobal` (attributed), then
      deletes the original unless `keep`; `POST /copy {from}` fetches
      global's `GET /runs/{id}/export` the same way and imports it
      privately. A bundle over 48 MiB is 413. Subagents' transcripts,
      memory, grants and sandboxes stay behind; the copy gets a note naming
      where it came from. The four routes exist only in a partitioned
      instance.
    - **A bundle is its caller's word**: at `POST /import` a message keeps
      its writer only when that is the caller; anyone else's comes as a
      copy (`origin: "copy"`, `label` the id it named — the page shows
      "copied · <id>", the model reads no `[id]` for it) and a ledger row
      that isn't the caller's own request becomes a `copy` one. `POST
      /copy` imports the global instance's own export, whose writers stand.
    - **Sharing in a partition**: `POST /runs/{id}/members`, `/links`,
      `PATCH` visibility and team schedules keep B2a's 409 (a conversation
      there is its person's alone); `POST /join` is relayed to global (join
      links are global's); `sharing()` is true in every layout — the web's
      Share on one's own conversation opens "Share a copy", on a shared one
      the share dialog at global.
  - **Not chosen:** the frame carrying the transcript to global (08 §3's
    wording) — the partition's backend makes the same attributed F5 call,
    so the transcript never passes through the browser and the original is
    deleted only after global took the copy; a second list API merging
    homes server-side (the partition can't read global's ACL'd list as the
    person without a round trip per page); an always-open global stream
    (keeps global awake for pages with nothing shared); a membership
    doorbell for newly shared conversations (a mail or ping per share — the
    focus/visibility re-read covers the page a person is looking at);
    trusting a bundle's writers at import (any person could put words in
    another's name); refusing `share` on an unpartitioned `POST /ask` (it
    is what sharing right after does).
```

## Review fixes (pt/b2b-fix)

Every finding of the B2b review, and what became of it (tests: the
template `_backend`'s unless said otherwise):

| # | Finding | Resolution |
|---|---|---|
| 1 (medium) | `POST /import` stored each bundle message's `sender`: a person could import a hand-written bundle in which bob wrote something — carol's page showed it as bob's, and the global agent read it as `[bob] …` | **Fixed** (`vouchedBy`, `_backend/homes_bundle.go`): at `POST /import` only the caller is taken at their word. Anyone else's message (or one naming nobody) is stored as a copy — `origin: "copy"`, `label` the id it named (a valid user id, else none), no sender — so `senderOf` never returns it, the model gets no `[id]` for it and `model/fold.js` shows "copied · bob"; automation prompts keep their origin; a ledger row that isn't the caller's own request becomes `copy`. The owner token vouches for no one. `POST /copy` (the global instance's own export, read by the partition's backend) keeps its senders. `TestImportTakesOnlyTheCallersWord` (carol's view, the ledger, the model's wire messages after carol talks: `[alice]`, `[carol]`, no `[bob]`; the owner token's import; the private copy keeps bob), the e2e (alice's hand-made bundle, read by dave), `hack/agent-template-chat.test.mjs` (fold). |
| 2 (medium) | the merged Mine listed a conversation twice when a live event reached a row held below the horizon; `ustate` for held rows was dropped | **Fixed** (`model/conv-list.js`): `find()` looks in `held` too; an event on a held row re-places it (it lists once it rises above the horizon); `place()` keeps each id once (the newest row); the horizon is the row as read (`{id, activityMs}`), not the object a later event moves; an unpinned row is re-placed with two homes. `hack/agent-template-homes.test.mjs`: a held row's `ustate`, an event that leaves it held, one that lifts it, then `more()` — each id once. |
| 3 (medium) | `PUT /ask/upload` at global from a person made a held private run with its file in the shared space, which no shared ask could send | **Fixed:** `globalRoute` refuses it (409: drafts are made in your own space; a shared chat is created held and uploaded into). `TestSharedAskAtGlobal` (409, no held run left; the owner token as ever) and the e2e. The un-share paths are folded into the owner question. |
| 4 (medium) | a copy wasn't faithful once compacted: a compacted request lost its pin, the agent's own user messages became `human` asks, masked tool results came back full-size | **Fixed:** the bundle carries each message's ledger row (`ask: {source, who}`), `masked`, and a user message's `origin`/`label`; import records exactly those rows (compacted ones included — `readAsks` works out Live) and restores the stubs. `TestCopyKeepsTheLedgerAndMasks` (a compacted first request stays `# Your task`, a `[subagent results]` message is no request, the masked result stays masked). |
| 5 (low) | the native view's thumbnails, previews and exports of a shared conversation asked the person's own partition (404) | **Fixed:** `native/ui.js` `thumb`/`raw` add `&xbin-partition=global` for a global id, as `uploadTarget` does. `hack/agent-template-homes.test.mjs` (global, own, unpartitioned). |
| 6 (low) | New chat offered only you / the team — 08 §3's "with people…" missing, unrecorded | **Fixed:** "Only the people you name (shared space)", and people beside the team; none named keeps the dialog open and says so (`agent.js`: one line). `test/homes.mjs`. The composer's plain Send stays a chat of your own — recorded under Deviations. |
| 7 (low) | the publish notice said "your private files and sandbox stay yours unless you add them" though tool results can quote them | **Fixed:** "A copy of its whole transcript goes … your messages, the agent's answers and everything its tools returned, which can quote your private files, memory or sandbox … Its session files go too only if you add them." `test/homes.mjs`. |
| 8 (low) | text files and messages were uncounted; an oversize import was cut by `LimitReader` into a `400 unexpected EOF`, `POST /copy` a 502 | **Fixed:** one 16 MiB cap for session files, text and binary (over it: `left`); the bundle is measured as JSON — export and publish answer 413 "too large to copy: N MiB … leave its session files out"; import reads through `http.MaxBytesReader` (413); `POST /copy` passes the export's 413 on. `TestCopyTooLarge`. |
| 9 (low) | a person with nothing shared listed never learnt live that a conversation was shared with them | **Fixed (the page they look at):** `ConvList.catchUp` — while Mine lists nothing shared (global's list stream closed), showing the page or focusing it re-reads the shared space's first page (at most every 15 s); anything there reloads the list and opens the stream. "Needs you" at global follows with the stream's first run events (the app reloads needs on them) or going home. A doorbell for a background page is not built (Decision "Not chosen"). `hack/agent-template-homes.test.mjs`. |
| 10 (low) | two merge conflicts with parallel packs not called out | **Recorded** under Merge risks, with the resolution. |
| 11 (low) | the harness could pass without checking the global-viewer state; SKIP by default | **Fixed:** with `HARNESS_AGENT_PARTITION` set, a page that isn't a person's partition, a missing owner token and an owner-token page not opening as global are failed checks. The pass stays opt-in (`HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`, run.sh's documented recipe, which already names it for agentTemplate); the default harness seeds the agent unpartitioned for every other pass (B2a review #19). |

## Seams for the integrator

| Seam | Where | Filled by |
|---|---|---|
| `globalRoute(pattern, h)` — global mode's per-route wrapper (today: `POST /runs` and `PUT /ask/upload` from a person → 409) | `_backend/homes.go`, called first in `partitionRoute` (partition_routes.go) | **B2c** may add its global-side refusals there (e.g. a private trigger's registration rules) |
| `shareSpec` / `askShareOK` / `shareNew` (POST /ask's `share`) | `_backend/homes.go`, three lines in `ask.go` `handleAsk` | **B2d**: a hosted (non-secure) conversation's members snapshot can reuse `shareSpec.check` |
| `exportConv` / `importConv` (`convBundle` v1), `vouchedBy`, `copyOrigin` | `_backend/homes_bundle.go` | **B2d**'s "share a copy of my …" posts items into an existing shared conversation — a different route, but the bundle's file part (`bundleFile`, `acceptUploadSrc` with source `copy`) is reusable, and anything a person posts there on someone else's behalf should go through `vouchedBy` (only the caller is a sender) |
| `ConvList.catchUp` (a person's page re-reads the shared space's first page when shown/focused while nothing shared is listed) | `model/conv-list.js` | a follow-up could add a membership doorbell (a mail when someone shares a conversation with you) for a page in the background; the page needs nothing more for it than calling `catchUp` |
| `twoHomes`, `homeOf`, `publishes`, `listHomes`, `splitRows` (pure) and `runApi`/`homeApi`/`homeFetch` | `model/homes.js`, `model/home-api.js` | **B2c/B2d**'s frontend calls: anything about a shared conversation goes through `runApi` (by path) or `homeApi('global', …)` |
| `Session.liveG`, `Session.homes()`, `wantGlobalList` | `model/session.js` | **B2d**: a hosted conversation's live deltas come through global's stream already (08 §4) — nothing to add on the page |
| sandboxes in a shared conversation | `model/sandbox-store.js` (unchanged) | follow-up (B2d or later): the picker lists the person's partition's sandboxes; binding one to a shared conversation is refused by global (not its sandbox). The page should read `/sandboxes` at the conversation's home |
| native publish / copy / shared new chat | `native/` | follow-up: listed as DIFFERENCES (features.js) — the native view lists and shares shared conversations, publishing is the web's for now |

## Deviations

- **Publishing is the partition backend's F5 call**, not the frame's (08 §3
  says "the person's frame performs [it] with an F5 request carrying the
  transcript"): same attribution (xbind attributes a user partition's
  backend's own-global call to its person), and the original is deleted
  only after global answered with the copy.
- **Sharing a person's own conversation stays 409** at the sharing routes;
  the page's Share on it is "Share a copy…". 08 §3's "sharing a private
  conversation later is a copy" is exactly that.
- **`POST /ask`'s `share` is accepted unpartitioned** (applied as sharing
  right after): additive; unpartitioned pages never send it.
- **The global instance's list stream runs only while shared rows are
  listed** (08 §11: "the shared stream only while a shared conversation or
  the Shared list is open") — widened to "or Mine shows a shared row", so
  a merged list stays live.
- **Session files are opt-in on a copy** ("your private files … stay yours
  unless you add them"): a checkbox; files over 16 MiB together (text and
  binary) are left and named (`left`). The transcript itself always goes
  whole, tool results included — the dialog says they can quote private
  files, memory or the sandbox.
- **A new shared chat is made from New chat with options** (08 §3: new
  chats "Shared with the team" / "with people…"): only you, the team (to
  read or write) or people you name, with the team or alone. The
  composer's plain Send stays a chat of your own — the composer has no
  share choice (a person's everyday chat stays private; sharing it later is
  a copy).
- **A newly shared conversation reaches a page with nothing shared listed
  when the page shows or gains focus** (a re-read of the shared space's
  first page), not live while it sits in the background: global's list
  stream stays closed for such a page (08 §11). A doorbell is not built.
- **A copy names a message's writer only when that is who made it** (a
  bundle at `POST /import` is the caller's word): anyone else's message in
  a published copy reads "copied · <id>" and is no one's to the model;
  08 §3 doesn't say — this keeps one person from putting words in
  another's name.
- **The native view** gets the merged list, the Shared view and a shared
  conversation's share sheet; publish, copy-to-mine and a shared new chat
  are the web's (feature DIFFERENCES with reasons).
- **`hack/ui-harness/shots.js`**: to stay at its 877-line budget with the
  new `PASSES.agentHomes` line, one blank line inside `reloadFocus` went.
- **`hack/fakeopenai`**: its "starts the message" scripts (`paras N`, `long
  N`) now allow a shared conversation's `[<user id>] ` prefix (the agent
  prefixes each person's message once a conversation is shared, API.md
  "Sharing") — the e2e and the harness drive `paras N` in a shared chat.
- **The native app's uploads** into an open shared conversation
  (`app.uploadTarget()`, the path the app PUTs to itself) carry
  `&xbin-partition=global` — F9's parameter, consumed by xbind for the
  tile's own frames.

## Tests

**The review fixes (`pt/b2b-fix`), on its final tree** (`.dev.mk`'s env
exported for the integration runs, the Bash sandbox off for isolated ones;
no SKIP):

| Run | Result |
|---|---|
| **the legacy golden**: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing test (B2a's `TestBrakeRequests` with its race fixed, nothing it asserts changed) plus the new ones | PASS (223 s) |
| template `_backend`, new: `TestImportTakesOnlyTheCallersWord`, `TestCopyKeepsTheLedgerAndMasks`, `TestCopyTooLarge` (homes_copy_test.go); extended: `TestSharedAskAtGlobal` (a person's `PUT /ask/upload` at global 409, no held run left, the owner token's 200) — with every homes test and `TestBrakeRequests`, `-race -count=10`; each new check seen failing with its fix taken out (the vouching, the stubs) | PASS |
| **e2e** `go test -tags=integration -run '^TestPartitionsAgent$' ./test/isolated/` on a real isolated xbind — all seven cases; `shared-chats` now also imports a bundle alice made naming bob as a writer (dave reads a copy labelled bob, no sender) and has her draft upload at global refused (409) | PASS (67 s) |
| JS: `hack/agent-template-homes.test.mjs` (+ a held row's `ustate` and run events, then `more()` — each id once; `catchUp` — nothing shared, the 15 s floor, a newly shared row listed and the stream wanted; the native view's `thumb`/`raw` at global, at home, unpartitioned), `hack/agent-template-chat.test.mjs` (+ a copied message is "copied · bob" / "copied"); `make fmt-check vet js-check js-test` (461 pass, 1 skipped as before) | PASS |
| template browser tests from a scratch copy: `test/homes.mjs` (+ the publish notice's words; New chat with people: none named keeps the dialog open with its error, then a chat shared with carol and dave made at global), and `partition share sidebar chat home attach automations settings native sandbox layout triggers channels grant long live-policy frame-policy` | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| UI harness `agentHomes` (PORT 8966, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`, …/scratchpad/h4-B2b): 20 checks, none skipped — the 16 before, the partition and owner-token checks now failures when unmet, and **a copy dev1 shares back shows the admin's message as "copied · admin" and dev1's as dev1's** (the real page). Screenshots looked at: `agent-homes-publish` (the new notice), `-live-dev1`, `-copied` | PASS |

**B2b as first built (`pt/b2b`):**

| Run | Result |
|---|---|
| **the legacy golden**: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing template test unchanged, plus the new ones | PASS (223 s, final tree) |
| e2e on the final tree: `go test -tags=integration -run '^(TestPartitionsAgent\|TestCodingSandboxConsumers)$' ./test/isolated/` — all seven `TestPartitionsAgent` cases (modes, private-chats, shared-chats, global-db-not-mounted, mailbox, sandboxes, existing-instance) and the unpartitioned agent's `TestCodingSandboxConsumers` | PASS (60 s, 79 s) |
| template `_backend` (new, `homes_test.go`): `TestSharedAskAtGlobal` (a person's unshared ask at global 409, bad shares 400, `POST /runs` 409, the owner token asks as ever; team/people ACLs; bob's Shared and own lists; participant vs viewer; a stranger 404; sharing on at global; in a partition a shared ask 409; unpartitioned `share` applied), `TestSharedRunStreamsToEveryMember` (90 §I4: alice's and bob's streams of one shared run, through the route table as attributed F5 calls, both carry the draft while the model call is still blocked and both get the next piece as a `text.delta`, then the answer; a stranger's stream 404), `TestPublishAndCopy` (publish kept/moved, files only when asked, no system prompt travels; global's import makes alice's shared copy with the transcript and a note, bob reads it, it goes on; binary + text files and `message_files`; `POST /copy` from global's export into a private copy ≥ 2^40; the refusals), `TestJoinAtTheSharedSpace`, `TestNoHomeRoutesUnpartitioned` — `-race -count=6` | PASS |
| JS: `hack/agent-template-homes.test.mjs` (new: ids/paths/views → homes, the horizon merge; a person's page against two scripted homes — Mine from both, paging only the home with more, `#c=5` read and followed at global with the own stream on the list, events from global applied, send at global, the native upload path, Shared from global, search/needs from both, a shared ask at global, publish/copy at home, the global stream closing when nothing shared is listed; unpartitioned and global-instance pages never ask for a partition); `hack/agent-template-partition.test.mjs` (B2a's sharing expectations updated to B2b's); `make fmt-check vet js-check js-test` (457 pass, 1 skipped as before) | PASS |
| template browser tests from a scratch copy: new `test/homes.mjs` (the two homes against the stub: lists by home, the router, streams, Share → share dialog at global + Copy to my own space, Share a copy published from home, New chat with options → a shared chat at global); `test/partition.mjs` (the user layout's expectations updated: the Shared view, "Share a copy…", the pill); and `share sidebar chat home attach automations settings native sandbox layout triggers channels grant long live-policy frame-policy` | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| **e2e** `TestPartitionsAgent/shared-chats` (new, real isolated xbind): alice's unshared ask at global 409, a shared ask in her partition 409; a chat shared with bob made at global (id < 2^40), in both Shared lists, not in bob's own, dave 404; **alice's and bob's streams of it (`?xbin-partition=global`, their frame tokens) both carry ≥ 2 `text.delta`s before either has the answer, then both the answer**; publish kept (bob reads the copy, can't write in it — team viewer — nor read the original) and moved (the original 404s); bob's private copy (≥ 2^40) has the transcript, dave can't copy what he can't read; a join link made at global redeemed from dave's partition, and dave reads the chat | PASS (6 s) |
| UI harness `agentHomes` (PORT 8961, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`, …/scratchpad/h4-B2b): 16 checks — ids by home, the unshared ask at global refused, the admin's Mine merges both homes, dev1's never the admin's, dev1's Shared, `#c=` of the shared id in both pages, **both pages show the answer streaming at once**, then both finished, Share a copy (opens at global, original kept, dev1 lists it), Copy to my own space (≥ 2^40, the transcript), **the global-viewer state** (the owner token's page: the note, global's list only, no call asks for a partition), no page errors. Screenshots looked at: `agent-homes-mine`, `-shared-view`, `-live-admin`, `-live-dev1`, `-publish` (after the #pubdlg styling fix), `-global-viewer` | PASS |

## Merge risks

- **Shared agent files, line-local edits** (B2c touches the same files):
  `_backend/ask.go` (the `Share` field and three calls in `handleAsk`),
  `routes.go` (one line at the top of `routes()`), `partition_routes.go`
  (`partitionRoute`'s global branch; the `POST /join` row → `userGlobal`),
  `model/actions.js` (imports, `refusing`, `ask`, `needs`, `publish`,
  `copyToMine`, `rawFile`, `Attachments.upload`), `model/app.js` (the
  ConvList's change hook, `wantGlobalList`, `uploadTarget`),
  `model/session.js`, `model/conv-list.js`, `model/stream.js` (`home`),
  `model/rules.js`, `model/partition.js` (`sharing`), the three feature
  registries, `share.js`, `sidebar.js`, `native/convs.js` (a comment),
  `agent.js` (3 lines; now 994 of 1016), `index.html` (3 CSS lines after
  `.share .linkout`), `API.md` (the "Your conversations are yours" bullet
  and a new "Shared conversations" bullet after it; the `POST /ask` row),
  `docs/partitions.md` (one paragraph at the end of the agent paragraph in
  §The mode).
- **`test/isolated/partitions_agent_test.go`**: one `t.Run("shared-chats",
  …)` line before `global-db-not-mounted` and one header bullet; the case
  itself is in `partitions_agent_shared_test.go`.
- **`hack/ui-harness/shots.js`**: `PASSES.agentHomes` on its own line after
  `PASSES.partitionMark`, and one blank line removed inside `reloadFocus`
  (the file's 877-line budget) — another pack cutting the same blank line
  merges cleanly; one cutting a different line doesn't conflict. **B2c
  (`PASSES.channelsPartitioned`), F11 (`PASSES.personPage`), F12
  (`PASSES.adminPartitions`, `PASSES.partitionLogs`) and F14b insert their
  lines at the same anchor, so git conflicts on each pair despite the
  comment**: resolve by keeping every `PASSES.x = …` line (order doesn't
  matter). The sum stays under the budget, since F12 moves adminMap out.
- **`_backend/partition_routes.go` `userRoutes`**: B2b changes the `"POST
  /join"` row (→ `userGlobal`) and B2c the two rows right below it
  (`"POST /channels/{id}/claim": userGlobal`, `"POST /triggers":
  userLocal`) — adjacent lines, a textual conflict. Resolve by keeping all
  three: `"POST /join": userGlobal` (B2b's, with its comment) next to B2c's
  rows as B2c has them.
- **`_backend/homes.go` split** (review fix): the bundle code moved to
  `homes_bundle.go` (homes.go would have passed the 800-line cap); B2b's
  own files, nothing else moved. `model/fold.js` gains one line (a copied
  message's "copied · <id>"); `agent.js` one line (New chat's people,
  now 995 of 1016); `hack/ui-harness/run.sh` one comment line (the
  partitioned recipe names agentHomes).
- **B2a's JS expectations changed on purpose**: `sharing('user')` is true;
  `rowMenu`/`topBar` of a person's own conversation offer publish, not
  Share (`hack/agent-template-partition.test.mjs`, `test/partition.mjs`).
- **The partitions branch's master gate** (W3b, 90 §I "built as
  recommended": held until B2b and B2c land) — B2b's half is here.

## Owner questions

- **A person un-sharing a shared conversation** at the global instance —
  `PATCH /runs/{id}` to `visibility: private` with no members left, or
  removing the last member (`DELETE /runs/{id}/members/{user}`) — leaves a
  private conversation of theirs in the shared space (readable by the
  agent's managers, as unpartitioned). Starting one there is refused now
  (`POST /ask` without `share`, `POST /runs`, `PUT /ask/upload`); should
  these two paths be refused too (409: "Copy to my own space", then delete
  the shared one)? This pack allows them (D83's rule unchanged).
