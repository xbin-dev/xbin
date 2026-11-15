# Chat kit — the block model, the tool record, the fixtures (F0)

> Status: live — specified and pinned by tests (F0, 2026-11-14, branch
> `wsagent/a0-spec`); the models of F2 are built on it (same day, branch
> `wsagent/a2-models`: §10), and the views of F3, with the Agent tab on
> them (2026-11-15: §11; §12 is what F4 has to do). Step F0 of `plans/agent-dedupe.md` §3.
> Everything later in the dedupe — the kit's models (F2) and views (F3),
> AgTT onto the kit (F4), `sdk/acp`'s tool record (B1) — is tested against
> what this step fixes.

What exists after F0 (and what F2 changed in it: §10):

| File | What |
|---|---|
| `web/chat/blocks.js` (`/vendor/chat/blocks.js`) | the block model: typedefs of every kind, the tool record, `ChatCtx`; helpers `block`, `up`, `bump`, `adopt`, `nest`, `inner`, `walk`, `find`, `Versions`, `plain`, `newToolRecord`, `toolDone`, `toolState` |
| `web/chat/rev.js` (`/vendor/chat/rev.js`) | `KIT = 1`, the revision a tile probes |
| `sdk/acp/testdata/transcripts/<name>.json` | 8 event logs in xbind's served shape |
| `…/<name>.records.json`, `…/<name>.blocks.json` | each log's expected tool records and expected blocks |
| `…/<name>.agtt.json` | the run view AgTT's backend makes of the log, per checkpoint |
| `sdk/acp/acptest/transcripts_test.go` | generates the logs from real machinery and fails when they drift |
| `builtin-templates/agent/_backend/transcripts_test.go` | feeds the logs through `harness_map.go`, writes/compares the views |
| `hack/chat-fixtures.test.mjs` | today's `web/agent-fold.js` against the expectations |
| `hack/chat-fixtures-agtt.test.mjs` | today's `model/fold.js` over the views against the same expectations |
| `hack/chat-fixtures/{web,agtt,expect}.mjs` | the two adapters, the expectations' builder and the gap list (F0 also had `reference.mjs`, the record's rules: they are `web/chat/tools.js` since F2) |
| `hack/chat-fixtures/rows-bench.cjs` | the templates-vs-elements measurement of §7 |

## 1. The block model

A transcript is an ordered list of **blocks**. Two reducers produce them
(`web/agent-fold.js` from ACP events; AgTT's `model/fold.js` from a run
view); the kit's views read nothing else. The typedefs in
`web/chat/blocks.js` are normative; this section is their summary and the
reasons.

Every block: `k` (key), `v` (version), `kind`, and optionally `parent` (the
id of the subagent call it ran under) and `at` (ms since the epoch).

- **`k` is a string**, unique among all blocks of a transcript (nested ones
  too) and the same whenever the block is folded again. Each reducer keeps
  its own id space: the Agent tab's is the seq of the event that opened the
  block (D130), AgTT's is its row ids (`m12`, `c<call id>`, `draft-text`).
  Keys are never compared across reducers.
- **`v` is a number** that changes whenever anything a view of the block
  reads changed — a nested block's change included. Same `(k, v)`: same
  picture (it is what `guard` and memos key on). The Agent tab bumps in
  place (`bump`, up the `$up` chain); a reducer that rebuilds objects (AgTT:
  identity means unchanged) takes versions from `Versions.of(k, token)`.
- **`$`-prefixed fields are runtime-only** (`$up`, `$memo`): not part of the
  contract, never serialised (`plain`). Today's `up`, `ui` and `$memo` on
  the Agent tab's blocks become `$up`, the host's open-state (`ChatCtx`),
  and `$memo`.
- **An absent optional field is its zero value** (`''`, `0`, `false`,
  `null`, `[]`). In fixture JSON zero values are left out, except a block's
  `k`/`kind`, a record's `id`/`kind`/`title`/`status`, and an `exitCode` of
  0 (its zero value is `null`).
- A reducer hands its host a **Transcript**: `{blocks, tail?, plan?,
  usage?}`. `tail` is the blocks that wait on a person and are not in
  `blocks`; `plan` the newest plan; `usage` `{used, size, cost?}`.

### Kinds

| Kind | Fields (beyond k, v, kind) | Agent tab today | AgTT today |
|---|---|---|---|
| `user` | `text`, `files[]` (`name`, `path`, `mime`, `size`, `sizeText`), `sender`, `steered`, `ref` | `msg` role user | `user` |
| `assistant` | `text`, `mid`, `model`, `usage` | `msg` any other role (grows while it streams) | `assistant` (a stored row) |
| `draft` | `text` | never | `draft` (`draft-text`) |
| `thought` | `text`, `live`, `ms` | `thought` | `think` (a row's reasoning, or `draft-think`) |
| `tool` | `rec` (ToolRecord), `state`, `children`, `result` | `tool` (the record itself) | `tool` (`acp` + args + result) |
| `agent` | `title`, `ref`, `state`, `phase`, `steps`, `task`, `answer`, `blocks`, `tail`, `provider` | never | `agent` (a spawned child run) |
| `changes` | `turn`, `set` (ChangeSet) | `changes` (`files.changed` of a turn) | never |
| `plan` | `entries[]` (`content`, `status`, `priority`) | `plan` (a row, one per turn) | the summary's `harness.plan` (a pin) |
| `perm` | `pid`, `callId`, `tool`, `options[]`, `rule`, `title`, `description`, `defaultToNo`, `plan`, `calls`, `by`, `optionId`, `park` | `perm` (kept, with its answer) | the park (`pendingState` kind approval) |
| `ask` | `eid`, `callId`, `message`, `mode`, `schema`, `url`, `action`, `by`, `content`, `park` | `ask` (kept, with its answer) | the park (kind question), or the built-in "The agent is asking" card |
| `signin` | `provider`, `command`, `methods[]`, `device`, `park` | the banner under the transcript (`status.login`) | the park (kind login) |
| `notice` | `text` (first line = heading) | `notice` (xbind's line) | `notice` (engine-written user rows, automation prompts) |
| `turn` | `turn`, `stopReason`, `usage`, `error` | `turn` | never (run status; `error`/`cancel` steps) |
| `gap` | — | `gap` | never ("… earlier messages" is host chrome) |

A reducer may emit kinds of its own (AgTT's `step`: journal lines with
preview and live-page buttons); the kit asks the host to draw those
(`ChatCtx.other`).

### Decisions where the two sides differ

| # | Question | Decision | Why |
|---|---|---|---|
| K1 | Tool block: flat record (web) or presentational fields (AgTT's `headline`, `fam`, `sub`, `outcome`)? | `rec` holds the record; `state` and `children` beside it; headline, icon, outcome and chips are derived by `chat/tools.js` (F2) | one record shape for Go, JS and Swift; AgTT's precomputed words become derivations |
| K2 | Where do requests sit? | a `perm`/`ask`/`signin` block may be in `blocks` (the Agent tab: where it was asked, kept with its answer) or in `tail` (AgTT: under the transcript while it waits, gone afterwards) | the plan's "each host keeps its own placement" |
| K3 | The plan: a row or a pin? | a `plan` block either way; a host that pins it reads `Transcript.plan` | same |
| K4 | Streaming text | the Agent tab emits a growing `assistant` (its key is known from the first chunk); AgTT emits `draft`, replaced (another key) by the row's `assistant` | AgTT has no row id until the flush; forcing one kind would change a key mid-stream on one side |
| K5 | A call's state | `TOOL_STATES`: ACP's status as `pending · running · done · error · stopped`, plus `approval` (an unanswered request names the call), `writing` and `waiting` (AgTT's own engine) — `toolState(rec, hint)` | AgTT shows "needs approval" on the card; the Agent tab gets it too (gap W2) |
| K6 | An id used again | a `tool.call` for an id whose record is over, **or** was made in an earlier turn, starts a new record | union of AgTT's rule (ended → new, `h1:id#2`) and the web's (per turn); each has one half today |
| K7 | A later `content` list without diffs / text | keeps the diffs / text there were | AgTT's `mergeTool`; the web replaces the list whole and loses an edit's diff when its completion carries only a message |
| K8 | Text content | `rec.text`: the text items joined by a blank line | AgTT's; the web renders items one by one — item boundaries are lost, the markdown sniff runs on the joined text |
| K9 | Diffs | `rec.diffs[]` always has `path`, `status`, `add`, `del`; plus `patch` (AgTT's backend) **or** `oldText`/`newText` (the web) | the plan's "a patch or an old/new pair"; counts by LCS on both sides (they agree on every fixture) |
| K10 | `files` | `rec.files`: paths of locations then diffs, each once (AgTT's). The web's `t.files` (a snapshot diff) is `rec.changed` | two different things had one name |
| K11 | Output | `rec.output` raw (ANSI kept) + `rec.outputTruncated` (bytes dropped before it); capping is the reducer's choice (AgTT's backend keeps 64 KiB; the web keeps all) | views strip and tail; the fact that something was dropped must survive |
| K12 | `rawOutput` | not in the record | nobody draws it |
| K13 | Permission option names | ACP's (`optionId`, `name`, `kind`) + `explicit` | both sides already hold these; `explicit` comes from the shared Go rule (B1 `escalation.go`) |
| K14 | `perm.rule` | `{kind, title}` or `null` when "always" cannot be scoped (the web's `scoped:false`, AgTT's absent rule) | one way to say it |
| K15 | Plan approval | `perm.plan = {text, path}` (not a flag); `tool.content` still carries the list when the host has it | the card needs the text; AgTT says it once |
| K16 | Questions | `mode`: `form` (schema), `url` (a page), `text` (AgTT's built-in "answer below") | the web ignores url today (gap W4); `text` is drawn today by AgTT |
| K17 | Sign-in | a block (`signin`), not host chrome | one card (F5); each host places it (`tail`) |
| K18 | A message's files | `size` in bytes or `null`, `sizeText` when only words are known | the web has bytes, AgTT a parsed "2 KB" |
| K19 | AgTT's built-in approval of several calls | a `perm` with `calls[]` and the host's own options | so the kit can draw it without a second card |
| K20 | Unknown kinds | drawn by the host (`ChatCtx.other`, asked first for every block) | AgTT's `ui.ext.block` seam, kept |

## 2. The tool record

`ToolRecord` (`web/chat/blocks.js`): `id`, `kind`, `title`, `label`, `name`,
`status`, `parent`, `subagent`, `planReview`, `rawInput`, `locations[]`,
`files[]`, `text`, `terminal`, `output`, `outputTruncated`, `exitCode`,
`diffs[]`, `changed`.

Merge rules (executable as `/vendor/chat/tools.js` — `mergeTool`,
`startsNewCall`, `ToolRecords` — since F2; in F0 they were
`hack/chat-fixtures/reference.mjs`. `sdk/acp/toolrec.go` must reproduce
them; the fixtures' `<name>.records.json` are their output):

1. A field an event carries replaces the record's. `outputDelta` appends
   to `output`; `output` replaces what was gathered (an event with both:
   the whole output, then the delta — the template backend's order; the
   Agent tab's old record takes the whole output alone, see §10). `parent`
   is the first one said. An unknown `kind` reads as `other`.
2. `content` is read into `text`, `diffs` and `terminal`; a list with no
   text keeps the text, one with no diffs keeps the diffs (K7).
3. `files` = locations' paths, then diffs' paths, each once (K10).
4. A diff item is `added` when it has no old text, `deleted` when the call
   is a delete, else `modified`; `add`/`del` are the lines the texts do not
   share (common head and tail apart, LCS between; a final newline ends the
   last line).
5. Id reuse: K6. A `tool.update` never starts a record.
6. `files.changed` naming a call sets the newest record of that id's
   `changed` (the turn may be over).
7. A capping reducer keeps the newest 64 KiB of output, cut between
   characters, and counts the dropped bytes in `outputTruncated`.

Go proposal for `sdk/acp/toolrec.go` (B1; **not added here** — landed as
D225 with these types, plus `ToolEntry` (`Seq`, `Turn`, `N`), `ToolPolicy`
in place of `capOutput`, `Observe`/`EndTurn`/`Newest`/`Entries`,
`StartsToolRecord` and `OutputChunk`; `sdk/acp`'s
`TestToolRecordsMatchTheSharedFixtures` holds it to the records). JSON is the
fixtures' form: zero values omitted, `id`/`kind`/`title`/`status` always.

```go
// ToolRecord is one tool call: its tool.call and tool.update events merged.
type ToolRecord struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"` // an ACP ToolKind; "other" when unknown
	Title           string          `json:"title"`
	Label           string          `json:"label,omitempty"`
	Name            string          `json:"name,omitempty"`
	Status          string          `json:"status"` // pending | in_progress | completed | failed | cancelled
	Parent          string          `json:"parent,omitempty"`
	Subagent        bool            `json:"subagent,omitempty"`
	PlanReview      bool            `json:"planReview,omitempty"`
	RawInput        json.RawMessage `json:"rawInput,omitempty"`
	Locations       []ToolLocation  `json:"locations,omitempty"`
	Files           []string        `json:"files,omitempty"`
	Text            string          `json:"text,omitempty"`
	Terminal        bool            `json:"terminal,omitempty"`
	Output          string          `json:"output,omitempty"`
	OutputTruncated int64           `json:"outputTruncated,omitempty"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	Diffs           []ToolDiff      `json:"diffs,omitempty"`
	Changed         *ChangeSet      `json:"changed,omitempty"` // set by the embedder (xbind's snapshots)
}

type ToolLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// ToolDiff: Patch (a unified diff, Truncated when cut at a hunk), or the two texts (OldText nil: a new file).
type ToolDiff struct {
	Path      string  `json:"path"`
	Status    string  `json:"status"` // added | modified | deleted
	Add       int     `json:"add,omitempty"`
	Del       int     `json:"del,omitempty"`
	Patch     string  `json:"patch,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	OldText   *string `json:"oldText,omitempty"`
	NewText   string  `json:"newText,omitempty"`
}

type ChangeSet struct {
	Files []FileChange `json:"files"`
	Patch *ChangePatch `json:"patch,omitempty"`
}

// ToolRecords folds a session's tool events (the id-reuse rule included).
type ToolRecords struct{ /* held: id → newest record; turn */ }

func (r *ToolRecords) Apply(ev Event) (rec *ToolRecord, isNew bool) // tool.call, tool.update; turn.end moves the turn
func (rec *ToolRecord) Merge(d ToolEvent, capOutput bool)
```

`harnessMeta` (AgTT) maps onto it field for field (`Tool` → `Name`;
`Text`, `RawInput` stop being engine-private); `internal/agent/page.go`
uses `ToolRecords` for its cuts.

## 3. Mapping: the Agent tab (`web/agent-fold.js` `blockOf`)

(Since F3 this mapping is shipped code — `blockOf(b)` / `transcriptOf(fold)`,
what `<bx-agent>` draws from; `hack/chat-fixtures/web.mjs` only names it.)

| Today (`agent-fold.js`, `agent-tools.js`) | Block field |
|---|---|
| `b.key` (seq, or −n) | `k` (as a string) |
| `b.v`; `b.up`, `b.ui`, `b.$memo` | `v`; `$up`, host open-state, `$memo` |
| `msg` `role==='user'`: `text`, `files[{name,mime,size}]`, `steered` | `user`: `text`, `files`, `steered` |
| `msg` other roles: `text`, `mid` | `assistant`: `text`, `mid` |
| `thought`: `text`, `t0`, `t1`, `done` | `thought`: `text`, `at=t0`, `ms=t1−t0`, `live=!done` |
| `tool`: `id`, `tk`, `title`, `label`, `name`, `status`, `parent`, `subagent`, `planReview`, `rawInput`, `exitCode`, `output` | `rec.id`, `rec.kind`, … same names |
| `tool.content[]` (the last list): text items / diff items / terminal item | `rec.text` (joined) / `rec.diffs` (`oldText`, `newText`, counts) / `rec.terminal` |
| `tool.files` `{changes, patch}` | `rec.changed` `{files, patch}` |
| `tool.rawOutput` | dropped (K12) |
| `tool.children`, `tool.t0` | `children`, `at` |
| — (not kept in the old record: `locations`) | `rec.locations`, `rec.files` — since F2 the fold's tool block carries the kit's record itself (`tool.rec`, kept by `mergeTool`), which the adapter hands on whole |
| `changes`: `turn`, `changes`, `patch` | `changes`: `turn`, `set.files`, `set.patch` |
| `plan.entries` | `plan.entries` |
| `perm`: `pid`, `tool{id,name,title,kind,rawInput,content}`, `options`, `rule{kind,title,scoped}`, `meta{title,description,defaultToNo}`, `by`, `optionId` | `pid`, `callId=tool.id`, `tool{title,kind,name,rawInput,content,command}`, `options`, `rule` (null unless scoped), `title`, `description`, `defaultToNo`, `plan` (from `isPlanApproval`/`planText`/`rawInput.planFilePath`), `by`, `optionId` |
| `ask`: `eid`, `toolCallId`, `message`, `schema`, `action`, `by`, `content`; since F2 `mode`, `url` | same; `callId` |
| `turn`: `turn`, `stopReason`, `usage`, `error` | same |
| `notice.text`, `gap` | same |
| `fold.st.last.login` `{needed, provider, command}` | `tail: [signin]` |
| `fold.st.usage` | `Transcript.usage` |

## 4. Mapping: AgTT (`hack/chat-fixtures/agtt.mjs`)

| Today (`model/fold.js` block, run view) | Block field |
|---|---|
| `b.id` | `k` |
| identity of the fold's block (FoldCache) | `v` via `Versions.of(k, block)` |
| `user`: `text`, `files[{path,mime,size}]`, `msgId`, `sender` | `user`: `text`, `files[{name, path, mime, size:null, sizeText}]`, `ref`, `sender` |
| `notice.text` | `notice.text` |
| `think`: `text`, `ms`, `live`, `started`, `parent` | `thought`: `text`, `ms`, `live`, `at`, `parent` |
| `assistant`: `text`, `model`, `usage`, `parent` | same |
| `draft.text` | `draft.text` |
| `tool` with `acp`: `callId` (`h<gen>:<id>#n`), `acp.kind/title/label/tool/status/parent/subagent/planReview/locations/files/exitCode/output/outputTruncated/diffs` | `rec.id` (the agent's own id), `rec.kind/title/label/name/status/parent/…/diffs` (patch form) |
| `tool.args` (rawInput's fields + `summary`) | `rec.rawInput` (`summary` removed, `rawSummary` → `summary`) |
| `tool.result` (the row's content) | `rec.text` when the row holds the call's text (not a placeholder, an "edited …" line or the output's tail); else derived by the view |
| `tool.state` (`acpState` + placeholder) | `state` (`running` → `pending`/`running` by `rec.status`) |
| `tool.kids` | `children` |
| `tool.headline`, `fam`, `sub`, `outcome` | derived from `rec` (K1) |
| `tool` without `acp` (AgTT's own tools) | `rec` made of `name`, the family's kind, `headline` as `title`, `args`, the result as `text`; `result` |
| `agent`: `link.label`/`headline`, `childId`, `state`, `link.phase`/`child.status`, `child.llmCalls`, `task`, `result`, `blocks`, `pendingApproval`, `harness` | `agent`: `title`, `ref`, `state`, `phase`, `steps`, `task`, `answer`, `blocks`, `tail`, `provider` |
| `step` (journal line) | host-private kind `step` (`ChatCtx.other`) |
| `run.pendingState` kind approval + `harness{pid, callId, options[+explicit], tool{title,kind,name,label,command,rawInput,content}, rule, defaultToNo, description, planApproval, plan}`, `park` | `tail: [perm]` — `pid`, `callId`, `options`, `tool`, `rule`, `defaultToNo`, `description`, `plan{text, path}`, `park` |
| kind question + `harness{eid, callId, message, schema, url}` | `tail: [ask]` |
| kind login + `harness.login{command, methods, device}` | `tail: [signin]` (`provider` = the summary's name) |
| kind approval without `harness` (`toolCalls`) | `tail: [perm]` with `calls[]`, options Approve / Deny |
| waiting on a question without `harness` (`run.result`) | `tail: [ask]` `mode:'text'` |
| `run.harness.plan.entries`, `run.harness.usage` | `Transcript.plan`, `Transcript.usage` |

## 5. Fields that cannot match today

The tests compare each reducer's blocks with the expectation **through
that reducer's gaps** (`GAPS` in `hack/chat-fixtures/expect.mjs`); a gap the
fixtures stop showing fails the test, so this list is exact. Closing one is
deleting its line.

Agent tab (to close in F2/F3):

| Gap | Field | Why |
|---|---|---|
| ~~W1~~ | `rec.locations`, `rec.files` | closed in F2: the fold keeps the kit's record (`mergeTool`) on its tool block |
| ~~W2~~ | `tool.state` `approval` → `pending` | closed in F3: the fold ties a request to its call (`perm` on the tool block; the call's label on the request) |
| ~~W3~~ | `perm.options[].explicit` | closed by B1 (D225): `permission.request` options carry `explicit` (`sdk/acp` `Provider.OptionRaises`); the gap line is gone from `expect.mjs` |
| ~~W4~~ | `ask.mode`, `ask.url` | closed in F2: the fold keeps both |

Not reached by a fixture, pinned by inline cases in
`hack/chat-fixtures.test.mjs`: K7 (since F2 the Agent tab's *record* keeps
the diff; its card, which still draws the old record's last content list,
loses it until F3) and K6's first half (an id reused within a turn merges
into one card). Not exercised at all: `perm.tool.label` (no fixture asks
about a labelled call).

AgTT (to close in F4, or permanent by design — marked P):

| Gap | Field | Why |
|---|---|---|
| A1 (P) | `k`, `user.ref` | the template's own row ids |
| A2 (P) | `turn`, `changes` blocks | never rows: the run's status and the summary's counts say them |
| A3 (P) | `plan` placement | the summary's plan, pinned: compared as `Transcript.plan` |
| A4 (P) | `perm`/`ask` placement and history | the park: in `tail` while it waits, not drawn once answered |
| A5 | a url `ask` | the backend answers it itself (declined, or shown as the sign-in's device code) |
| A6 (P) | `step` blocks | the template's own kind (the test pins which appear: `error` + `note` in `signin`) |
| A7 | `draft` for the streaming answer; `assistant.mid` | no row yet (K4); rows carry no message id |
| A8 | `user.sender` | a row says who sent it; the log does not |
| A9 | `thought.ms` | a coding agent's thinking is not timed |
| A10 | `rec.terminal`; `rec.rawInput` `{}` vs none; `rec.text` while the call runs | the lift (`acp`) has no terminal mark and no text; arguments are rawInput + summary |
| A11 | `diffs[].patch`/`truncated` vs `oldText`/`newText` | K9: each side has one form |
| A12 | `rec.changed` | xbind's snapshots |
| A13 | `perm.title` (the request's `meta.title`), `perm.park`, a plan's `tool.content` | the park keeps neither the heading nor (for a plan) the content list |
| A14 | `signin.park` | the sign-in is a park |

Also true but not shown by the fixtures (the direct feed has no
`acp.Client`): a real sign-in park lists `methods` and may carry a
`device`; a turn refused signed out parks on the sign-in at once, where the
feed writes an `error` step first (noted in the backend test's header).

## 6. `ChatCtx` — what a view may ask of its host

A view is `(block, ctx) => template`. `ctx` replaces today's reach into
the host. The typedef is in `web/chat/blocks.js`.

| `ChatCtx` | Agent tab today (`agent-cards.js`, `bx-agent.js`) | AgTT today (`ui`, `ctx.app`) |
|---|---|---|
| `running`, `ended` | `this._status()==='running'`, `this.ended` | `busy(run.status)`; — |
| `agentName` | `_provName()` | `who.name` (`nameOf(run.harness)`) |
| `me` | — | `ui.me()` |
| `can.answer`, `can.raise` | always / — | `who.talk` (`access(v).talk`), `who.owner` (`ownerOf`) |
| `isOpen(block, slot, dflt)` | `a._isOpen(owner, slot)` (default applied by `?open`) | `ui.isOpen(id[:slot], dflt)` |
| `setOpen(block, slot, open, dflt)` | `a._toggled(e, owner, slot, dflt)`, `a._openSlot(owner, slot)` (+ keep the view, `_touch`) | `ui.toggle(id[:slot], dflt)` |
| `permit(block, {optionId, decision, feedback})` | `a._permit(pid, decision, optionId)`; `a._rejectPlan` + `a._planFeedback[pid]` (feedback sent as the next prompt) | `ctx.app.harness.permit(runId, {option, park, feedback})`; `ui.act.approve(runId, yes, how, park)` |
| `answer(block, {action, content})` | `a._answer(eid, action, content, fields)` | `ctx.app.harness.answer(runId, action, content, park)` |
| `confirm(text)` | — | `confirm(optionConfirm(name, o))` |
| `note(block)` | `this._error` (the status line) | `ui.approveNote(runId)` |
| `fileDiff(block, set, file)` | `a._fileDiff(owner, f, c, sel)` (`FileDiffs`: inline part, else the `/diff` route) | — (patches are on the row) |
| `fileState(block, file)`, `openFile(block, file)` | — | `ui.file(msgId, f)`, `ui.act.openFile(path)` |
| `openAgent`, `loadAgent`, `agentError` | — | `ui.act.select(childId)`, `ui.act.loadChild` / `retryRead`, `ui.readError(childId)` |
| `other(block, depth)` | — | `ui.ext.block(b, ui, depth)`; `stepTpl` (`act.openPreview`, `act.openLive`) |
| `repaint()` | `a.requestUpdate()` | `ctx.paint()` |
| (recursion into `children`) | `a._block(c)` | `blocksTpl(kids, ui, depth+1)` — the kit's own, no host call |

Form values (`a._askVals`, the template's `forms` map) and plan feedback
are the widgets' own state: those widgets are elements (§7). Slots in use
today: `body`, `files`, `f:<path>`, `<slot>:all`, `diff<i>`, `task`, `raw`,
`full`, `d:<path>`.

## 7. Performance baseline, and templates vs elements

**Baseline** — `agentLongPerf`, `AGENT_PERF=950` (≈2000 blocks), CPU 4×,
master 83c03745 + this branch (no shipped module changed), three runs
(`PORT=18810 … ./hack/ui-harness/run.sh --keep agentLongPerf`, then
`--shots` twice):

| Measure | run 1 | run 2 | run 3 |
|---|---|---|---|
| 950 units streamed, unthrottled | 616 ms | 550 ms | 550 ms |
| reopen: window open → transcript at the bottom | 34 ms | 35 ms | 27 ms |
| reopen: reload → transcript at the bottom | 1648 ms | 1462 ms | 1432 ms |
| held after reopen | 134 blocks, 30 rendered, 200 events | same | same |
| JS heap after reopen | 11.2 MiB | 11.3 MiB | 11.1 MiB |
| a fresh tab: mounted → at the bottom | 76 ms (1 long task, 50 ms) | 65 ms (0) | 72 ms (0) |
| 30 more units at the bottom | 167 ms; 2 long tasks, max 83, total 166 ms | 128 ms; 2, max 76, total 152 | 144 ms; 2, max 71, total 142 |

The bar for F3 and F4 is the worst run of each row (run it three times:
single runs vary by ~15 %).

**Templates or elements for rows?** Measured with
`hack/chat-fixtures/rows-bench.cjs` (the same rows — a markdown message, an
edit call's card, a thought every tenth — drawn three ways in one
LitElement's scroller; CPU 4×; median of 5):

| Rows | Way | first render | 200 updates of the last row | window step (+30 top, −30 bottom) | heap |
|---|---|---|---|---|---|
| 30 | template fn + `guard([v])` | 20.9 ms | 141.5 ms | 30.4 ms | 0.9 MiB |
| 30 | element, shadow root | 24.0 | 140.5 | 30.1 | 1.0 |
| 30 | element, light DOM | 23.0 | 141.1 | 28.6 | 1.0 |
| 120 | template fn | 50.2 | 163.1 | 20.3 | 1.1 |
| 120 | element, shadow root | 53.4 | 169.4 | 20.3 | 1.2 |
| 120 | element, light DOM | 52.8 | 175.7 | 32.5 | 1.2 |
| 2000 | template fn | 659.5 | 467.5 | 166.8 | 3.3 |
| 2000 | element, shadow root | 769.6 | 669.4 | 179.9 | 4.0 |
| 2000 | element, light DOM | 767.2 | 725.6 | 221.9 | 3.9 |

At the sizes the transcript renders (30 rows first, at most ~120 in the
window) elements are 3 ms slower on first render and within 8 % on streamed
updates — noise at five runs — and the light-DOM variant's window step at
120 rows was 12 ms slower. At "load all" (2000 rows) elements cost 17 % more
on first render, 43–55 % more per streamed update and 21 % more heap.

**Recommendation: rows are lit template functions of `(block, ctx)`.**
Elements buy no speed, and they cost what the two hosts rely on:

- the transcript's selection and rich copy (D199) and "a selection survives
  the stream" need rows in one tree — a selection does not span shadow
  roots;
- AgTT's rows are light DOM under its page stylesheet, measured as direct
  children by `chat-window.js`, and its tests and the harness select
  `.tcard[data-k]`; the Agent tab's are in its shadow root. One CSS text
  and template functions serve both; a row element would have to be light
  DOM (no encapsulation gained) and adds a global `customElements` name
  that an old template instance and a new xbind would both define;
- it is what both sides are today (`toolCard(a, t)`, `toolTpl(b, ui)`), so
  F3 and F4 move code instead of rewriting it.

Elements stay for widgets with state of their own: the question form, the
plan-feedback box, the sign-in card, the composer.

## 8. The fixtures

`sdk/acp/testdata/transcripts/` — plain JSON, no JS-only values.

| Log | Covers | Checkpoints (seq) |
|---|---|---|
| `text` | thoughts then text; text streamed under one message id; usage on each turn's end | 8 (mid-thought), 20 (mid-answer), 24 |
| `tools` | a call of every ACP kind (locations, a diff, terminal output + exit 0); output deltas; a nested subagent | 33 (output streaming), 45 (inside the subagent), 52 |
| `plan` | a plan's three states | 7, 13 |
| `permission` | rejected; allow-always (a rule); answered by the rule; a plan approval answered with a mode-raising option (`explicit`) — id `t1` in three turns | 8, 19, 43 (each waiting), 50 |
| `questions` | a form elicitation answered, then declined — id `ask1` reused | 8, 21 (waiting), 28 |
| `signin` | auth-needed status and a failed turn; a url elicitation (device code) accepted and completed; the turn again | 8 (signed out), 9 (the url question), 19 |
| `id-reuse` | one id, two turns, output that must not accumulate | 25 |
| `xbind-shell` | a real xbind capture: ANSI output, exit 3, `files.changed` per call and per turn, id `run1` reused | 22 |

- `<name>.json`: `{name, about, source, flags, steps, checkpoints, events:
  [{seq, ts, type, data}]}`. Seven are played by
  `sdk/acp/acptest/transcripts_test.go` (acptest scripts through
  `acp.Client` over a pipe; `ts` synthetic: 2026-01-01T00:00:00Z + 100 ms
  per event); the test compares on every run and rewrites with
  `ACP_TRANSCRIPTS_WRITE=1`. `xbind-shell.json` holds the events of the
  native fixture `shell.json` (`internal/term` `TestAgentCaptureFixtures`)
  unchanged — the node test fails when the two differ.
- `<name>.records.json`: `{records: [{seq, turn, rec}]}` after the last
  event.
- `<name>.blocks.json`: `{checkpoints: [{seq, blocks, tail?, plan?,
  usage?}]}` — `k` is the Agent tab's key; `v` and `at` are left out.
- `<name>.agtt.json`: `{views: [{seq, view}]}` — `view` is `GET
  /runs/{id}/view` cut to `run` (status, pendingState, harness summary),
  `messages`, `steps`, `links`, `drafts`; row `created` is the seq of the
  event that wrote it; park ids, clocks and offsets are zeroed.

The expectation is the model's reading of a log: the Agent tab's blocks
with what that fold drops put back from the log (`expect.mjs` header).
`explicit` is in the log since B1 (D225) — it was taken from AgTT's
backend before; the two agree on every fixture.

Regenerating: the three commands in `hack/chat-fixtures.test.mjs`'s header,
in order. Swift: `native/tools/agent-parity.mjs` can read `<name>.json`
as it reads its own fixtures (`events` has the same shape); the parity
fixture was not touched.

What the AgTT backend test stands in for, by hand (its header): the user
row and turn start (the pass's), filing a permission in `acp.Permissions`
(the client's), skipping url questions (answered through the client).

## 9. Open questions

1. **D-number**: K1–K20 are recorded here only. `plans/DECISIONS.md` needs
   an entry when Track A lands; none was allocated (parallel agents collide
   on numbers).
2. ~~**`explicit` for the Agent tab**~~ — done in B1 (D225): the client
   marks the options (`Provider.OptionRaises`, start = `Config.Mode`), the
   expectation reads them from the log.
3. **K8 (joined text)** loses item boundaries the Agent tab renders today
   (each text item sniffed for markdown on its own). If F3 finds a harness
   that relies on it, `rec.text` becomes `texts: string[]` — an additive
   change while nothing reads `text` yet.
4. **`notice` and `gap`** have no shared fixture (xbind writes `notice` on a
   layer move, D175; the tab synthesises `gap`); `hack/agent-fold.test.mjs`
   covers them, and since F2 an inline case of `hack/chat-fixtures.test.mjs`
   holds them as blocks of the model. F2 added no log for them, nor for
   `agent` or `perm.tool.label`: a shared log is made by real machinery —
   `sdk/acp/acptest`'s scripts through `acp.Client`, then the template
   backend's views — and neither `notice` nor `gap` is an agent's event,
   `agent` is the template's own child run (no ACP log), and a labelled
   permission request needs a new acptest script and a regenerated
   `.agtt.json` (Go, B1's package).
5. **The Swift port** keeps its own fold and fixtures; moving
   `ParityTests` onto `<name>.blocks.json` is F2's regeneration step.
6. **docs**: `/vendor/chat/*` is listed in `docs/frontend-kit.md` as the
   kit's first modules with the shapes "only grow" promise; the plan's
   "needs xbind ≥" compat statement is F4's.

## 10. F2 — the models (2026-11-14)

`/vendor/chat/tools.js` and `/vendor/chat/ask.js` (`KIT` 2). What this
step settled where §1–§2 were silent (rationale: D224, "F2"):

| # | Question | Decision |
|---|---|---|
| M1 | Headline | label → a `description` argument → a shell command read by `describeCommand` (while the call runs, a short one-line text content first) → the title's first line → a reading of the arguments by kind → name → id. `/vendor/agent-tools.js` `headline` keeps its old chain (no reading by kind) for the Swift port |
| M2 | A row's label (AgTT) | the lift's `label`; else a `summary` that is neither the lift's title nor the kind |
| M3 | The command among the arguments | `command` (string or argv), else `cmd`, else `script`; a string command's `args` appended (`inputCommand`) |
| M4 | Subline | an execute's `$ command`, clipped to 90, unless what the headline says is the command |
| M5 | Outcome | of a completed call only: `exit N` (ok / bad), `+a −d` (`N files` past one), `−d` for a delete, `N lines` of a read's text, `N matches` (a search's locations) |
| M6 | `stripAnsi` | CSI with any parameter byte (0x30–0x3F) and the 8-bit CSI, an OSC up to its own BEL or ST, character-set designations, ESC @ … ESC _; nothing else (line endings and control characters are a view's) |
| M7 | Output tail | the last 20 000 characters after stripping (`OUTPUT_TAIL`), with `cut` (left out here) and `dropped` (`outputTruncated`) |
| M8 | Diffs | `diffPatch(d)`: the patch, else `unifiedDiff` of the two texts; `joinPatches` whole files under a cap; `patchLines` for drawing one plain (kept: `bx-code.js` `diffHTML` needs lit and highlight.js) |
| M9 | A permission card's options | the agent's order; rejections first when `defaultToNo`; `explicit` ones only with `can.raise`, counted in `hidden` otherwise; a host whose "always" is its own rule (`scopedOnly`) leaves `allow_always` out when `rule` is null — never on a plan |
| M10 | Answering | `permAnswer(option, feedback)` → `{optionId \| decision, verdict, feedback}`; feedback only with a rejection; the host picks the transport (`ChatCtx.permit`) |
| M11 | Forms | the template's superset: `oneOf` / `anyOf` / `enum` (+ `enumNames`) single choice, array multiple choice, `default` as `dflt` (an untouched field answers it), "Other" riding on its question and answering a required one |
| M12 | URL-mode questions | `question(ask)` → `{mode: 'url', url, text}`; `url` only when http(s) |
| M13 | Slash menu | prefix matches first, then names or descriptions containing the query; at most 8; a draft is a command while it is `/[\w:.-]*` |
| M14 | Composer words | `composerWords({name, busy, steering, waits, barred, native})`; `steerTracker` notices a steered message |
| M15 | An event with a whole output and a delta | the record: the whole output, then the delta (as the template's backend). The Agent tab's old record: the whole output alone. One string is held while the two agree. Open: which is right (no fixture has one) |

Gaps closed: W1, W4. `hack/chat-fixtures-agtt.test.mjs` additionally holds
that the kit's headline, subline, state chip and outcome of every call of
the shared transcripts are the same from either reducer's record.

## 11. F3 — the views, and the Agent tab on them (2026-11-15)

`KIT` 3. Rationale and the numbers: D224, "F3".

| Module (`/vendor/chat/…`) | What |
|---|---|
| `views.js` | `rowsView`, `rowView`, `blockView` (dispatch; `ctx.other` first), `userView`, `assistantView`, `draftView`, `thoughtView`, `noticeView`, `turnView`, `gapView`, `hostView`, `mdLive`, `activityView`, `usageView` |
| `cards.js` | `toolView` (+ the subagent's), `agentView`, `changesView`, `filesView`, `planView`, `commandView`, `contentView`, `textView`, `diffView`, `useDiffHighlighter`, `diffEpoch`, `KIND_ICON` |
| `asks.js` | `permView` (+ the plan's approval), `askView` (form / url / text), `signinView`, `canOf`, `bagOf`, `<bx-chat-form>` |
| `composer.js` | `<bx-chat-composer>` |
| `window.js` | `ChatWindow` |
| `css.js` | `chatCssText` |

What F3 settled:

| # | Question | Decision |
|---|---|---|
| V1 | The kit's look | the Agent tab's, class names apart (`bxc-`). Where the two chats' cards differ the kit has the tab's until F4 decides (§12) |
| V2 | A row | `<div class="bxc-row" data-k data-kind>`; its view `guard`ed by `[v, ctx.rev(b), ended, readonly, running (thought, agent, subagent call), mdEpoch, diffEpoch]`; a request that waits is drawn every time (live form state) |
| V3 | Open/closed | `ctx.isOpen(b, slot, dflt)` / `ctx.setOpen(b, slot, open, dflt)`; a `<details>` is `?open=${dflt}` and its body drawn when `dflt` or open; the host hears a move off the default only |
| V4 | A widget's state | `ctx.state(b)`: a bag by block key (`vals`, `feedback`, `busy`); the form is an element so typing draws it alone |
| V5 | Who may answer | `canOf(ctx)`: `ctx.can`, else from `ctx.role` (viewer: nothing; owner: raise); `readonly` / `ended`: nothing |
| V6 | An `explicit` option | drawn `bxc-explicit` (caution, never the primary button), `await ctx.confirm(optionConfirm(…))` before `ctx.permit`; hidden from who may not raise, with a line saying how many |
| V7 | Plan feedback | a textarea in the card (the bag); a reject button's `permAnswer(o, feedback)` → `{optionId, verdict: 'reject', feedback}`; the Agent tab sends it as the next message, the template as a field |
| V8 | The status badge of a call | `exit N` once a command exited; `needs approval` while a request names it; else its status word (`pending`, `in progress`, `completed`, `failed`, `cancelled`) with ` +a/-d` (the tab's) |
| V9 | Sign-in | `signinView`: the kit's card and words, `ctx.signin(block)` its inside. The tab keeps `<bx-agent-signin>` on its own when the guided flow runs |
| V10 | Diff highlighter | optional (`useDiffHighlighter`, lazy `bx-code.js`), plain `patchLines` until then |
| V11 | The composer's files | staged as chips, limits said, read as base64 on Send, kept until the host calls `sent()` |
| V12 | Pickers | plain data (`[{id, label, title, value, options}]`), a `<select>` each; what cannot be offered is `unavailable: [{name, reason}]` |
| V13 | The `host` kind | §12 |

Gaps closed: W2 (none left for the Agent tab). New transcript: `output` (a
whole output and a delta in one event — rule 1 of §2 pinned for Go, the
template's backend and both reducers).

## 12. What F4 has to do (the agent template onto the kit), and the workspace agent

The template renders in a **document** (no shadow root): it adds
`<style>${chatCssText}</style>` once (the kit styles only `bxc-` classes, so
its own sheet is untouched), keeps its timeline as the scroller, and hands
the views a `ChatCtx` built from `ui` and `ctx.app`:

| `ChatCtx` | Template |
|---|---|
| `running`, `ended` | `busy(run.status)`, never |
| `readonly`, `role`, `can` | `access(v)`: viewer → `role: 'viewer'`; `talk` → participant; `ownerOf(v, me)` → owner (`can.raise`) |
| `me`, `agentName`, `agentLabel` | `ui.me()`, `nameOf(run.harness)` or the class's name |
| `isOpen`/`setOpen`/`rev` | `ui.isOpen(id[:slot], dflt)` / `ui.toggle`; `rev` from the same store |
| `state` | its `forms` map (per park) |
| `permit`, `answer` | `app.harness.permit(runId, {option, park, feedback})`, `app.harness.answer(runId, action, content, park)`; its own approval: `ui.act.approve` |
| `confirm`, `note` | `confirm(...)`, `ui.approveNote(runId)` |
| `fileState`, `openFile` | `ui.file(msgId, f)`, `ui.act.openFile(path)` |
| `openAgent`, `loadAgent`, `agentError` | `ui.act.select`, `ui.act.loadChild` / `retryRead`, `ui.readError` |
| `sessionRules` | false: "always" is the agent's own |
| `other` | `ui.ext.block(b, ui, depth)` first, then its `step` kind |
| `signin` | `signin.js`'s card body |

Per view of the template:

| Template view | F4 |
|---|---|
| `model/fold.js` | gains the adapter to blocks (the fixtures' `hack/chat-fixtures/agtt.mjs` becomes its code, as `web.mjs` became `blockOf`): `Versions.of(k, foldBlock)` for `v`, `nest()` for a harness subagent's kids; a park goes to `Transcript.tail` |
| `chat-cards.js` `blocksTpl` (user, notice, think, assistant, draft) | `rowsView` / `userView` … ; its `Copy` on an answer and the model/usage line under it are the template's: `ctx.other` wraps `assistantView`, or the kit grows an optional footer slot (decide there) |
| `chat-cards.js` `toolTpl` (its own tools) | `toolView` over `builtinRecord` (the record made of name, family, args, result: `result` on the block). To decide: the template's **outcome chips** (`exit 1 · 14s`, `3 matches`) and **subline** vs the tab's status badge (V8) — either the kit's card takes `ctx.chips: 'outcome'`, or the tab adopts outcomes; measure which reads better, change both |
| `chat-cards.js` agent card, `harness-child.js` | `agentView` (drafted in F3 from the model, not yet driven by a host): the child's header line (monogram, where it works, counters) is the template's — through `ctx.other` or a `host` block beside it |
| `chat-cards.js` `approvalTpl`, `askTpl` | `permView` with `calls[]` (K19), `askView` mode `text` |
| `harness-cards.js` | `toolView`; its top bar (usage badge → `usageView`, counts, the plan pin → `planView` in its own fold) stays the template's chrome |
| `harness-ask.js` | `permView`, `askView`; `end` seam places them (`Transcript.tail`); its diff preview (`contentPreview` lines) vs the kit's capped full diff — the kit's |
| `harness-controls.js`, the composer in `agent.js`, `page-composer.js` | `<bx-chat-composer>`: `#hctl`'s popover becomes `pickers` (mode with `explicit` marked and confirmed, options) — the setting (Auto / Always approve) and the account stay the template's beside it; `composerWords` for the placeholder; `steerTracker`'s notes; its attachments already upload elsewhere (`/files`) — the composer hands `File`s, the template uploads |
| `chat-window.js` | `ChatWindow` with `{page: 40, trim: 160, minMargin: 80, growFirst: true, holdWhileBusy: true, retrim: 'any'}`; its no-ScrollWindow fallback goes (the page needs the kit's xbind anyway) |
| `chat-copy.js` | unchanged; its `clean()` learns the `bxc-` chrome classes |
| `signin.js` | `signinView` + `ctx.signin` (F5 makes it one card) |
| `native/*` | unchanged (models only) |
| `boot.js` | `NEEDS.kit` 3 |
| tests | `hack/chat-fixtures-agtt.test.mjs` on the real adapter; the template's browser scripts select `.bxc-…` |

**The workspace agent** is the template's code in a workspace mode, so it
gets all of the above, plus two things the kit leaves a slot for instead of
knowing:

- **A tile agent session it started** — a `host` block `{type: 'session',
  data: {tile, session, provider, status, …}}`. The kit gives it a row and a
  frame; the host draws the card (name, state, "open") with `ctx.host`.
- **An approval that trusted chrome must grant** (a change set to apply, a
  delegation) — a `host` block `{type: 'grant', data}`. The transcript (a
  sealed component's page) draws only that something waits; the button
  lives in xbind's own chrome, which the page cannot script. The kit draws
  no answer for a `host` block, so this holds by construction; a `perm`
  block is never used for it.

