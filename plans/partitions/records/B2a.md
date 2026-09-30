# B2a — records for the integrator

Work pack B2a (agent: modes, split, engine, default — 08 §1-§2, §6-§9, §11
states) of the partitioned-tiles plan, branch `pt/b2a` on `partitions` at
a5e26298. Implements **PD-30**, **PD-35** (decided: new agent instances are
partitioned by default), the agent's side of **PD-34** (decided: existing
instances keep their mode — no migration), **PD-36** and the agent's half
of **PD-39** (the old-manager degrade, C12). This pack edits neither
docs/changelog.md nor plans/DECISIONS.md; the texts below go there at merge.

Commits: `0e38c7af` (the backend: three modes, the split, the engine),
`75451a5f` (the default, the frontend's three layouts, docs), `f1bd5949`
(the e2e and the real-template merge guard), `da9cbd53` (slots fail open),
`98b57d60` (the harness pass), then this record.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: new instances keep each person's conversations
  apart** (the template's API.md "Partitioned instances",
  [partitions.md](/docs/partitions.md) §The mode,
  [sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned consumers). The
  builtin agent template now asks for `"template": {"partition": ["user",
  "global"]}`: a new instance (Tile Manager → New from template, `bx
  template new agent`) runs one backend per person who uses it — their own
  conversations, memory, skills, schedules and sandboxes, which nobody
  else's frame, terminal or tile reaches, workspace admins included — and a
  global instance for the tile-wide settings, chat channels, event triggers
  and other tiles' calls. Untick **Keep each person's data apart** (or
  `--no-partition`) for an unpartitioned instance; without `xbind
  --isolate` it is unpartitioned anyway. **Existing instances keep their
  mode** and run exactly as before; switching one deletes its
  conversations, memory and schedules (its `partitionNote` says so). In a
  partitioned instance: a person's conversation ids start at 2^40 (the
  global instance's, like an unpartitioned instance's, at 1); the config,
  classes, halt switch and shared skills are the global instance's,
  mirrored into a new read-only-for-people `conf` resource, and a manager's
  change from their own partition is forwarded there; a conversation in a
  person's partition can't be shared, and channels and triggers are set up
  at the global instance (409 in a partition); model calls have a tile-wide
  cap (`maxActiveRuns`, default 4) across every partition, as lock files
  beside the new shared `team` resource, and each partition's own gate
  allows 2; a partition that stops with work asks to be started again only
  for work that moves without its person; a sandbox manager whose
  `hello.caps` lack `partitions` isn't used in people's partitions (refusal
  `partitions`, 409, and one banner on the page naming it and the update —
  update the sandbox managers before creating or switching an agent);
  every sandbox the agent makes is labelled `xbin.agent/home`; a person's
  own providers (personal binds) are offered in their own conversations
  only; the page's live streams close while it is hidden. New in every
  instance: `GET /health`, `POST /mailbox` (the partition-mail doorbell,
  `partitionMail`; an unpartitioned instance answers 404), `partition` in
  `GET /me` on a partitioned instance, and the `conf` and `team` resources
  in scope.json (never opened unpartitioned). Existing instances take the
  new files with a template merge (`git merge template/main`); its
  `xbin.json` conflicts in the `template` block, which the instance
  dropped — keep your side. Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, B2a: the agent template partitioned by
  default — three modes in one binary (2026-09-30).** Implements PD-30,
  PD-35, the agent's side of PD-34 and PD-36, and C12 (PD-39) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §1-§2, §6-§9, §11. The template's
  API.md "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **One binary, three modes from `xbin.Partition()`** (mode.go): ""
      legacy (every existing instance, the opt-out, an older xbind — today's
      code path, every hook nil), `global`, `user:<id>`; an unknown kind of
      partition exits (status 3) rather than serve everyone. The legacy
      golden is the whole existing template suite, unchanged, plus
      TestLegacyModeUnchanged.
    - **The split (PD-30).** `db`, `files`, `events` stay per partition at
      their names (global's at today's keys, by F4); no person's partition
      mounts global's db (checked from /proc in the e2e). Each mode
      migrates only its own db — no code change: the partition's db is its
      own volume at the same path. A person's partition raises runs'
      AUTOINCREMENT to 2^40 − 1 at every open (idempotent, never lowers),
      so its ids start at 2^40.
    - **`conf`** (kv, `"shared": "read"`): global mirrors `config`, `halt`,
      `classes` (one `settings` key, with a revision of the shared skills)
      and each shared skill (`skill:<name>`) after every change, in the
      writer's goroutine after the commit, retrying every 30 s on failure,
      and at start. A partition reads it through `getSetting`'s hook
      (cached 3 s; invalidated after a forwarded write), refuses writing a
      mirrored key (`errSharedSetting`), lists shared skills beside its own
      and reloads its classes when conf's change. An empty conf (global
      never ran) reads as defaults and wakes global (at most every 5 min).
    - **Managers edit through global.** In a partition `PUT /config`,
      `/classes`, `/halt`, and `PUT`/`DELETE` of a skill that isn't the
      partition's own are relayed by the partition's backend to
      `xbin.GlobalURL(path)` (F5/F9: attributed to its person); a
      manager's request for work lifts the halt there. The partition's
      guard checks first, global again (its level). The page is unchanged.
    - **Global takes a person's attributed calls** (agentRole): the route
      gate stays `RoleFunc("admin")` except, in global mode, a call with
      From = the tile, X-XBin-Partition `user:…`, a person and the role
      xbind clamps to (reader/writer) — read as `whoUser` with their level
      (W2's "F9's attribution reads as whoUser"). Such a call without its
      person is nobody (`whoNone`), never the tile itself.
    - **`team`** (sqlite, `"shared": true`): migrated only by global under a
      `<team>.migrate` flock (schema 1: its version row); a partition opens
      it without migrating (`openShared`), wakes global (`GET /health`) and
      waits ≤30 s when behind; `teamUnavailable` answers 503 "the shared
      space is being upgraded" for B2d's hosted features.
    - **LLM concurrency (PD-36):** `maxActiveRuns` lock files
      `llm.slot.<i>` in team's directory, try-locked (LOCK_NB, random
      start, 20→400 ms jittered backoff) after the process gate; released
      with the call; a dead process's slot frees with its fds. A directory
      that can't hold them costs only the cap. A partition's gate: 2.
    - **Resume (C4):** a partition leaves `resume` (@every 1m) only for
      running/queued runs, undelivered inbox rows or settled-but-undelivered
      subagent links; else `wake` (`CRON_TZ=UTC m h d M *`, the earliest
      sleeping run's wake rounded up to its minute; within a minute counts
      as runnable); else nothing. Takeover deletes `wake` too. Global and
      legacy: today's rule.
    - **Sandboxes (C12):** a partition refuses a manager's hello without
      `partitions` (refusal `partitions` → 409) — catalog, tools, dialogs;
      global keeps using it. A partition never binds a sandbox the manager
      marks `shared` (the team's) into a conversation (403); its terminal
      still opens. `xbin.agent/home` = the partition id (learned from
      X-XBin-Partition-Id on calls into it, kept in settings; the partition
      key until known) or `global`; none unpartitioned.
    - **Personal providers:** `personal: true` rows of XBIN_IFACE_LLM/MCP
      are offered only when a context says the conversation is the
      partition's person's (a turn, a compaction and a title set it from the
      root's owner; the model picker from the caller, not view-as). The
      default model's lookup uses shared providers only.
    - **Mail skeleton:** `POST /mailbox` (mounted apart: xbind rings it as
      xbin/mail with the writer role) and every start pull a `mailSource`;
      a topic's handler runs with the item's id recorded in `mail_seen` in
      one transaction, then the item is acked; a redelivery is only acked;
      an unknown topic stays unacked. No handler, and the source holds
      nothing, until F6 is wired.
    - **The page's three layouts** (model/partition.js): unset —
      unchanged, not one element or call added; `user:` — no sharing (row
      menu, top bar, the Shared view; web and native), a banner for old
      sandbox managers (read at start when the slot is bound); `global` —
      a note to sign in as a person. Partitioned pages close their live
      stream while hidden and resume from the cursor.
  - **Not chosen:** the frame calling global for settings (the forward
    keeps the page unchanged and one place checks); a sqlite table or kv
    counter for the LLM cap (a write per call, and no release on death);
    one conf key holding the skills too (kv values are cut at 1 MiB);
    migrating team from any partition; dropping unknown mail topics (a
    newer global mid-deploy may be the sender); pausing unpartitioned
    pages' streams (xbind counts them as activity there today); filtering
    personal sandbox managers (hosted conversations may use the host's).
```

## Seams for the integrator

| Seam | Where | Filled by |
|---|---|---|
| `partitionMail mailSource` (default `noMail{}`: holds nothing) | `_backend/mailbox.go` | **F6**: an adapter over the SDK's `xbin.Inbox(after, limit)` / `xbin.Ack(ids...)` mapping `xbin.MailItem{ID, From, Topic, Data}` to `mailItem` (ctx unused by the SDK), set in `startMode` for partitioned modes. The doorbell `POST /mailbox` is already mounted (any role, `From` must be `xbin/mail` or the tile/owner) and xbin.json declares `"partitionMail": "/mailbox"`. F6 documents `partitionMail` in docs/partitions.md (its TODO line stays) |
| `mailHandlers map[topic]mailHandler` (empty) | `_backend/mailbox.go` | **B2c**: `handoff/dm`, `handoff/event`, `outbox/add` (global side), handoff ids idempotent through `mail_seen` + their own records |
| `userNoChannels` routes (409 in a partition: `POST /channels/{id}/claim`, `POST /triggers`) | `_backend/partition_routes.go` `userRoutes` | **B2c**: private triggers registered at global through F5 (the registry stays global's) |
| `userNoShare` routes (409: members, links, join) and `sharing()` (false in `user:`) | `partition_routes.go`, `model/partition.js` | **B2b**: the Shared list from global (`{partition: 'global'}`), publish-a-copy; the router by id (≥ 2^40: the partition; below: global — `partitionIDBase`) |
| global mode's F5 persons (`agentRole`, `personFromPartition`) | `partition_routes.go` | **B2b**: shared conversations created and listed there; today a person's `POST /ask` at global makes a private conversation in global's db — B2b decides whether global refuses that |
| `teamStore`, `teamUnavailable(w)`, `teamMigrations` (schema 1) | `_backend/team.go` | **B2d**: the hosted table, host scope lock `<team>.engine.<pkey>`, schema 2+ appended to `teamMigrations` |
| `ownConversation(root)` / `personalCtx` | `_backend/iface_personal.go` | **B2d**: a hosted conversation's runs are not the host's own (false today: they aren't in the partition's runs) |
| `callGlobal`, `gwDo`, `gatewayKV` | `_backend/gwcall.go` | B2b–d reuse them for F5 calls and kv |
| managers' `partitions` cap at instantiation / switch / `bx doctor` | xbind | **F13a/F11/F7b** (08 §6: the checks come first; the agent degrades on its own meanwhile) |
| the agent's `xbin.agent/home` in the admin's sandbox registry | xbind | B1's follow-up (`forPartition` on `SandboxSpec`) |

## Deviations

- **Settings writes are relayed by the partition's backend**, not sent by
  the page to global: the page and the native view need no second base for
  settings, and the partition checks the manager before global does.
- **In a person's partition, sharing and channel/trigger setup answer 409**
  and the page hides sharing and the Shared view, until B2b/B2c build their
  global halves. Schedules work in the partition (its own cron store).
- **The halt in a partition**: a halt set at global stops a partition's
  runs at their next step (each reads conf); the manager's own partition
  cancels its live runs at once.
- **The partition gate is 2 at start** (min with `maxActiveRuns`); a later
  config change reaches the tile-wide cap at once, not the partition's gate.
- **`xbin.agent/home`** is the partition key (`user:<id>`) until the
  partition learns its id from a call — normally before any sandbox.
- **`GET /health`** is a new route in every mode (the wake-up and a probe).
- **The personal filter covers `llm` and `mcp`**, not `sandboxes` (08 §4
  lets a hosted conversation use the host's sandboxes).
- **Stream pausing is partitioned-only** (08 §11 lists it for
  `model/stream.js` generally): unpartitioned pages keep today's stream,
  which xbind counts as activity for today's idle reap.
- **test/isolated/consumers_test.go opts out** (`"partition": false`): its
  cases share conversations and team sandboxes between people in one
  instance. On a remote QA xbind older than F13b, the field is refused
  (400) — run it against a release that has F13b.
- **The native view** gets the no-sharing state and the notices on its home
  screen; it reads `xbin.partition` as the web does (absent: today's).
- **hack/ui-harness/passes/agenttemplate.js**: the fan-out check expects 1
  subagent holding the model in a person's partition (its gate is 2), 3
  elsewhere, as before.
- **The LLM slots fail open**: a team directory that can't hold the lock
  files (missing, read-only, no flock) lets calls through without the
  tile-wide cap rather than hold them (logged nowhere: the gate still caps
  each process).

## Merge risks

- **Existing instances merging template/main** get a conflict in
  `xbin.json` (the `template` block they dropped changed): keep ours. That
  is F13b's design (a block change always conflicts), now live for every
  agent instance; the changelog says so. `TestAgentTemplateDefaultKeepsExistingMode`
  (internal/broker) pins that neither side of that conflict carries a
  top-level `partition`.
- **F6 in parallel** touches docs/partitions.md (the `partitionMail`
  paragraph and the TODO list) — this pack changed only §The mode's last
  paragraph.
- `builtin-templates/agent/agent.js` is at 991 of its 1016-line budget.

## Tests

- **The legacy golden:** `hack/tile-check.sh agent` with
  `TILE_TEST_FLAGS="-race -count=1"`: every existing template test passes
  unchanged (XBIN_PARTITION unset) — PASS (208 s, on the final tree).
- New Go (template `_backend`): `TestModeOf`, `TestDetectMode`,
  `TestLegacyModeUnchanged`, `TestNotePartitionID`,
  `TestPartitionIDsStartAt2to40`, `TestConfMirror` (mirror, read, refusal,
  skills merge, a deleted shared skill, classes and halt from conf),
  `TestConfReaderCaches`, `TestTeamOpenShared` (no migration from a
  partition, wake, 503, upgrade seen), `TestLLMSlotsCap` (the cap across
  two semaphores; a slot held by a killed helper process frees),
  `TestAcquireLLMWithSlots`, `TestUserModeWake`, `TestLeaveWakeUpByMode`
  (legacy resume, a partition's wake job, nothing for a waiting person, the
  takeover deletes), `TestUserModeRoutes` (forwarding, the reader refused
  locally, own vs shared skills, the 409s, `/me`), `TestUserModeHalt`,
  `TestGlobalTakesPartitionCalls`, `TestOldManagerDegrade`,
  `TestPartitionSandboxes` (labels by mode, the shared-sandbox refusal),
  `TestMailboxSkeleton`, `TestPersonalProviders`.
- internal/broker: `TestAgentTemplateDefaultKeepsExistingMode` (the real
  template: new instance partitioned, opt-out not, an instance from before
  the default never gains one through a template merge).
- JS: `hack/agent-template-partition.test.mjs` (layouts, rules, notices,
  the stream's pause/resume, unpartitioned unchanged) in `make js-test`;
  the template browser test `test/partition.mjs` (the three layouts
  against the real page; the stream closes hidden and resumes from its
  cursor); the features registry's `state.partition.*` keys in both views.
  Template browser tests from a scratch copy: all pass except
  `terminal.mjs`, which fails identically on the base commit a5e26298
  (`#sbxterm-pane bx-terminal` never shows in this environment) — not this
  pack.
- **e2e** `TestPartitionsAgent` (test/isolated/partitions_agent_test.go,
  real isolated xbind): modes (new instance partitioned and recorded
  `auto`, opt-out unpartitioned), private chats (alice, bob, carol's own
  partition, the owner token at global: each sees only their own; ids ≥
  2^40 vs 1; files stay; a reader can't change settings; sharing 409),
  global's db not mounted in alice's partition (/proc mount table: her db
  volume and the shared team only), sandboxes (old manager refused with
  `partitions` naming it, alice's sandbox labelled with her partition id,
  unseen by bob's partition and global; global keeps both managers),
  the existing (unpartitioned) instance unchanged — PASS (50 s).
- S1 smoke `-run 'TestPartitionsSmoke(W2)?$'`: PASS (35 s + 71 s).
  `TestCodingSandboxConsumers` (its agent now opted out): PASS (77 s).
- Repo guards: `go test . ./internal/assetscan ./internal/sizebudget
  ./internal/docscheck ./internal/builtins ./internal/apicheck`, `go test
  ./internal/broker`, `make fmt-check vet js-check js-test`: PASS.
- UI harness (PORT 8991, HARNESS_DIR …/scratchpad/h3-B2a), unisolated (the
  seeded agent is unpartitioned there: the legacy page): `agentTemplate` 28,
  `agentConvs` 19, `agentTask` 13, `agentSandbox` 76 checks, all PASS.
  With `HARNESS_ISOLATE=1` the seeded `apps/agent` is a partitioned instance
  (recorded `partitioned`, user + global) and `agentTemplate` runs in the
  admin's own partition: 28/28 PASS after one check was taught the
  partition's gate — its fan-out check wanted 3 subagents holding the model
  at once, and a person's partition allows 2 model calls, so 1 (the new
  chat still answered in ~140 ms). The other agent passes (sharing
  conversations with dev1, etc.) are written for the unisolated harness and
  weren't run isolated.
