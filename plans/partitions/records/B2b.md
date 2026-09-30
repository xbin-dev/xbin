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
(`0a676be5`), and this record.

## Bugs found

- **hack/fakeopenai**: `paras N` / `long N` ("starts the message") never
  matched in a shared conversation, where the agent prefixes a person's
  message with `[<user id>] ` — fixed (test infrastructure).
- **Found and fixed before commit**: `POST /copy` read global's export
  through `callGlobal`, whose answer `gwDo` cuts at 4 MiB — a copy with
  files would have been truncated; it reads through its own call now.
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
  chat with options** asks who can see the new chat. New routes, in a
  partitioned instance only: `POST /runs/{id}/publish`, `POST /copy`, `GET
  /runs/{id}/export`, `POST /import`; `POST /ask` takes `share` (in every
  instance — unpartitioned it shares the new conversation at once); at the
  global instance a person's `POST /ask` must carry it (409 otherwise) and
  their `POST /runs` is refused; `POST /join` in a person's partition is
  redeemed at the global instance. An unpartitioned instance's page, and
  the global instance's own (the owner token), are unchanged. Nothing to
  change.
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
      `POST /ask` at global without `share` (or with a draft) is 409/400, and
      their `POST /runs` is 409 — a buggy or old page can't put a person's
      private chat into global's db (B2a's open question). A person may
      still un-share one there later (it stays in the shared space, readable
      by its managers). The owner token (the global-viewer state) asks as
      ever.
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
      busy. Both pause while hidden (B2a). Live for every member (90 §I4)
      holds by construction: each member's page follows global's stream.
    - **The merged list**: Mine reads both homes and cuts at a horizon (the
      newest of the last rows of homes that have more pages), holding rows
      below it until the next page, so paging never shows a row above one a
      later page could still bring; Shared is global's; search and "needs
      you" read both.
    - **Copies between homes**: `POST /runs/{id}/publish {share, files?,
      keep?}` in a person's partition exports the conversation (the root's
      transcript as the model reads it — tool calls and results, folded
      ones marked, the summary; session files only with `files`, binary
      ones ≤ 16 MiB together) and sends it to global's `POST /import`
      through `callGlobal` (attributed), then deletes the original unless
      `keep`; `POST /copy {from}` fetches global's `GET
      /runs/{id}/export` the same way and imports it privately. Subagents'
      transcripts, memory, grants, sandboxes and the task ledger stay
      behind; the copy gets a note naming where it came from. The four
      routes exist only in a partitioned instance.
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
    (keeps global awake for pages with nothing shared); refusing `share` on
    an unpartitioned `POST /ask` (it is what sharing right after does).
```

## Seams for the integrator

| Seam | Where | Filled by |
|---|---|---|
| `globalRoute(pattern, h)` — global mode's per-route wrapper (today: `POST /runs` from a person → 409) | `_backend/homes.go`, called first in `partitionRoute` (partition_routes.go) | **B2c** may add its global-side refusals there (e.g. a private trigger's registration rules) |
| `shareSpec` / `askShareOK` / `shareNew` (POST /ask's `share`) | `_backend/homes.go`, three lines in `ask.go` `handleAsk` | **B2d**: a hosted (non-secure) conversation's members snapshot can reuse `shareSpec.check` |
| `exportConv` / `importConv` (`convBundle` v1) | `_backend/homes.go` | **B2d**'s "share a copy of my …" posts items into an existing shared conversation — a different route, but the bundle's file part (`bundleFile`, `acceptUploadSrc` with source `copy`) is reusable |
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
  unless you add them"): a checkbox; binary files over 16 MiB together are
  left and named (`left`).
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

On the final tree (`.dev.mk`'s env exported for the integration runs, the
Bash sandbox off for isolated ones; no SKIP):

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
  merges cleanly; one cutting a different line doesn't conflict.
- **B2a's JS expectations changed on purpose**: `sharing('user')` is true;
  `rowMenu`/`topBar` of a person's own conversation offer publish, not
  Share (`hack/agent-template-partition.test.mjs`, `test/partition.mjs`).
- **The partitions branch's master gate** (W3b, 90 §I "built as
  recommended": held until B2b and B2c land) — B2b's half is here.

## Owner questions

- **A person un-sharing a shared conversation** (PATCH visibility private,
  no members) leaves a private conversation in the shared space (readable
  by the agent's managers, as unpartitioned). Refuse it there and offer
  "Copy to my own space" instead? This pack allows it (D83's rule
  unchanged).
