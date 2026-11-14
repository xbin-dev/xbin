# Protocol: terminal and agent sessions

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: the session directory (`/term/sessions`: terminals and coding
agent sessions), agent providers and their sign-in, prompts, queues,
permissions, diffs, agent history, and the agent session event log.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Sessions, agent providers and history


```
GET    /term/sessions             authenticated. the caller's live terminal
                                   sessions — the session directory (D73):
                                   [{id,cwd,net,label,scopes,gpu,api,name,
                                   created,lastActive,clients,envHeld,vm}], oldest
                                   first (agent rows add kind:"agent",
                                   provider, mode, model?, status, pending
                                   — unanswered permission requests — and
                                   questions — unanswered elicitations);
                                   ?cwd=<p> one tile only. [] without
                                   terminal rights; a tile the caller may no
                                   longer open a terminal on is omitted.
                                   ?user=<id> admin: another user's. Every
                                   browser the user signs into sees the same
                                   tabs (the shell's <bx-frame> lists them here,
                                   not in the browser). A row whose session
                                   targets a named deployment carries
                                   deployment. A row of a session on a
                                   partitioned tile carries partition
                                   ("user:<id>" | "global": what it acts
                                   in); in an admin's ?user= listing, and
                                   in an admin's view as its person, a
                                   person's session there has an empty
                                   name. A row with purpose:"signin" (set
                                   by xbind only, on a shell opened with
                                   /ws/term?purpose=signin; absent on
                                   every other row) is the Agent tab's
                                   guided sign-in running a CLI's login
                                   (D178): it is no tab — a client that
                                   makes tabs of this list skips a shell
                                   row with that purpose (never an agent
                                   row, and never by its name). Such a
                                   session is named xbin:sign-in and ends
                                   15 minutes after it opened
PATCH  /term/sessions/<id>        creator or admin (on a partitioned tile
                                   the creator only: 403 for an admin on
                                   another person's session). {name}: name
                                   the tab (empty clears; lives on the
                                   session → follows the user) → ok. 400
                                   the name xbin:sign-in (any case:
                                   xbind's, for a guided sign-in); 409 a
                                   session with purpose signin
GET    /agent/providers           authenticated. the coding agents this daemon
                                   runs: [{id,name,modes:[{id,name,explicit?}],
                                   defaultMode,login,signin?}] (D74; explicit
                                   modes are never defaults; login is the
                                   shell command that signs the CLI in — the
                                   agent uses the session's $HOME, no vault
                                   key). signin (D178; claude only, today) is
                                   a sign-in a client drives for a person
                                   without showing a terminal: {command,
                                   argv, env?, tty, fallback?, url, hosts,
                                   code, invalid?, done, fail} — run command
                                   in a terminal session on the tile (or
                                   argv+env over pipes when tty is false),
                                   offer the first https URL with no user
                                   part on one of hosts exactly (no
                                   subdomain) matching the regular
                                   expression url whole (anchored at its
                                   host and path: claude's is
                                   https://(claude.com/cai|claude.ai)/oauth/
                                   authorize?…) — an OSC 8 link's target
                                   first, else the text, rejoined where the
                                   CLI broke it over full-width lines —
                                   write the person's code and Enter once a
                                   line holds code, and read invalid (code
                                   refused as malformed, asked again), done
                                   (signed in), fail (gave up; that line
                                   says why). A run that ends without a URL
                                   is a CLI too old for command: fallback is
                                   the interactive sign-in to run in a
                                   terminal instead. Nothing is saved but
                                   what the CLI keeps in $HOME
POST   /term/sessions             terminal-level on the tile (a shell's own
                                   terminal token counts; an agent
                                   sandbox's never — 403). {cwd, kind:"agent",
                                   provider, mode?, model?, options?, net?,
                                   api?, gpu?, name?, resume?} → SessionInfo
                                   (kind agent, status starting): an AGENT
                                   SESSION — the tile's sandbox runs the
                                   provider's ACP adapter instead of a shell.
                                   resume: a past session id (GET
                                   /agent/history) to reopen — provider, mode
                                   and name carry over, the agent replays the
                                   earlier turns (session/load); 409 when it
                                   cannot (start a new one). 400 unknown
                                   provider/mode, 403, 409 per-user limit,
                                   503 vault sealed / no bx. net/api/gpu:
                                   the sandbox pickers a shell's socket
                                   takes (api false = code-only, no
                                   terminal token). Shells still open on
                                   /ws/term.
                                   ?deployment=<name>: the session's target,
                                   fixed for its life (the primary's name
                                   follows the primary; a protected primary
                                   403, an unknown one 404, not a name 400).
                                   SessionInfo echoes deployment; no echo
                                   means this xbind can't target deployments.
                                   Without it the session follows the
                                   primary — or, while the primary is
                                   protected, targets the live reload target,
                                   and has no tile API (api false) when
                                   there is none (§/ws/term)
GET    /term/sessions/<id>        creator or admin → {session, permissions:
                                   [{pid,toolCall,options}], elicitations:
                                   [{eid,toolCallId?,message,schema}]}
                                   (either kind; the unanswered permission
                                   requests and questions, oldest first —
                                   the elicitation.request payloads)
DELETE /term/sessions/<id>        creator or admin → 204 (either kind; the
                                   API twin of DELETE /ws/term?session=)
POST   /term/sessions/<id>/prompt creator or admin. {text, attachments?:
                                   [{name, mime?, data}]} → {ok, turn};
                                   409 while a turn runs or another prompt
                                   is being taken (before this body is
                                   read), and when a cancel lands while its
                                   files are handed over (no turn starts).
                                   attachments (optional; text may then be
                                   empty): files, data standard base64 — at
                                   most 10, each ≤ 10 MiB, 20 MiB together
                                   (413 past a limit; the body is capped at
                                   32 MiB). Each is written inside the
                                   agent's sandbox (its private /tmp; with
                                   isolation off a directory xbind makes
                                   and removes with the session — the
                                   newest 64 MiB kept) and handed to the
                                   agent by that path. A png/jpeg/gif/webp
                                   (the bytes decide, not mime) also goes
                                   inline as an image the model sees —
                                   400 when the agent does not take images
                                   (it did not advertise
                                   promptCapabilities.image) — when it is
                                   ≤ 3.75 MiB (5 MiB as base64, the
                                   strictest model API's limit) and the
                                   prompt's inline images stay ≤ 4 MiB
                                   together; past either it is only a file
                                   the agent opens with its tools. The
                                   agent keeps inline images in its
                                   conversation and resends them every
                                   turn, so downscale photos (≤ 2576 px
                                   on the long edge is all a model reads)
                                   and convert HEIC to JPEG (other image
                                   types are files). A text file ≤ 128 KiB
                                   also goes inline as an embedded
                                   resource when the agent takes embedded
                                   context. The log records the names,
                                   types, sizes and whether each went
                                   inline, never the bytes. (To send
                                   while a turn runs, use …/queue.)
POST   /term/sessions/<id>/queue  creator or admin (not an agent sandbox's
                                   token). A message whatever the agent is
                                   doing (D197): {text, attachments?,
                                   interrupt?} → {outcome, turn?, id?}.
                                   Idle: it starts a turn, as /prompt does
                                   (outcome "prompted", turn). While a turn
                                   runs it goes to that turn where the
                                   agent takes messages mid-turn (the
                                   adapters' _session/steering — claude,
                                   codex — at the agent's next step;
                                   outcome "steered"; the log's user
                                   message.delta carries steered:true),
                                   else it is held and sent as the next
                                   prompt when the turn ends (outcome
                                   "queued", id). Held messages go in
                                   together, in order, as one prompt (text
                                   one paragraph each, as many as one
                                   prompt's attachment limits allow), and
                                   once one is held later ones queue behind
                                   it. interrupt: hold it and cancel the
                                   running turn — it goes in once that turn
                                   has ended. The held messages are the
                                   session's (every client sees them: a
                                   `queue` event after each change, the tail
                                   page's state.queue now); a cancel keeps
                                   them, the session's end drops them (a
                                   notice says so). 409 while another
                                   message for the session is being taken
                                   (before this body is read) or 16 are
                                   held; 413 past one prompt's attachment
                                   limits, or past 40 MiB of held files.
                                   Attachments as for /prompt
DELETE /term/sessions/<id>/queue  creator or admin (not an agent sandbox's
                                   token). Take every held message back →
                                   {items:[{id, text, files?, at}]}, whole
                                   (their files are not returned): a client
                                   puts the text back where it was typed
DELETE /term/sessions/<id>/queue/<qid>
                                   the same for the one held message <qid>;
                                   404 when it is no longer held (sent, or
                                   taken back elsewhere)
POST   /term/sessions/<id>/restart
                                   the creator. {net?, api?, gpu?} →
                                   {session, resumed}: the sandbox pickers
                                   are fixed at start, so the session ends
                                   and a new one opens — same tile,
                                   provider, mode, settings, name —
                                   resuming the conversation where the
                                   agent can (session/load; resumed true,
                                   its history entry superseded), else
                                   fresh (the transcript stays in
                                   /agent/history).
                                   ?deployment=<name>: restart onto that
                                   target (the primary's name follows the
                                   primary; 403 a protected primary, 404
                                   unknown, 400 not a name — a refused
                                   target leaves the session running); the
                                   new SessionInfo echoes deployment
POST   /term/sessions/<id>/cancel creator or admin → ok (the turn ends
                                   cancelled; pending permissions cancelled;
                                   a prompt still handing its files over
                                   gets 409 and never starts)
POST   /term/sessions/<id>/permissions/<pid>
                                   creator or admin. {optionId} | {decision:
                                   allow_once|allow_always|reject_once|
                                   reject_always} → ok; the first answer
                                   wins (404 after); allow_always records a
                                   session rule (nothing in xbin.json) —
                                   never for kind switch_mode (a plan
                                   approval: its allow_always options are
                                   modes), which is always asked
POST   /term/sessions/<id>/elicitations/<eid>
                                   creator or admin. {action: accept|
                                   decline|cancel, content?} → ok: answer
                                   a question the agent asked
                                   (elicitation.request); content = the
                                   form's values (an object keyed by the
                                   schema's properties) with accept; the
                                   first answer wins (404 after)
GET    /term/sessions/<id>/events creator or admin. ?since=<seq> → {events,
                                   next, truncated}; ?follow=1 streams NDJSON
                                   from the cursor until the client or the
                                   session goes (§Agent session events); its
                                   head is sent at once, before any event.
                                   ?before=<seq>&limit=<n> (either one) → a
                                   page instead: {events, hasOlder,
                                   nextBefore, truncated, next, last, state}
                                   — the tail (no before) or the events
                                   before a seq, cut where a fold may start
                                   (§Agent session events → Pages)
POST   /term/sessions/<id>/options
                                   creator or admin. {id, value}: change a
                                   setting the agent advertised (model,
                                   effort, mode, … — the `options` list on
                                   the session's idle status event); applied
                                   to the next turn, the refreshed list rides
                                   the next status event → ok
GET    /term/sessions/<id>/log    creator or admin. text/plain: the adapter's
                                   stderr + driver notes (debugging)
GET    /term/sessions/<id>/diff   creator or admin. ?toolCallId=<id> |
                                   ?turn=<n>, ?path=<file>? → text/x-diff:
                                   the complete git patch behind a
                                   files.changed event (whose own patch is
                                   capped), or one file of it — up to 16
                                   MiB (X-Truncated: true when cut). Edit
                                   calls (which report their own diff) have
                                   one too. 404 when the session keeps no
                                   snapshot for it (nothing changed, not a
                                   git tile, or the session ended). One
                                   diff runs at a time per session and two
                                   across xbind: a request past that waits
                                  (prompt, permissions, elicitations,
                                   options, restart and diff answer 403 to
                                   any agent session's own terminal token —
                                   an agent sandbox's XBIN_TOKEN, on every
                                   session, its own or another's: an agent
                                   never answers its own requests or drives
                                   itself, not through a sibling either; a
                                   shell's token on the tile still does,
                                   as `bx agent` there)
                                  (on a partitioned tile — docs/partitions.md
                                   §Terminals and agent sessions — "or
                                   admin" holds for the admin's own
                                   sessions only: on another person's
                                   session every route above but DELETE
                                   answers 403 `session belongs to another
                                   user: <tile> keeps each person's data
                                   apart, so an admin can't open other
                                   people's sessions there (ending one is
                                   allowed)`, and an admin viewing as a
                                   person gets 403 `viewing as someone
                                   opens no session on <tile>: it keeps
                                   each person's data apart` on every GET
                                   above, the person's own sessions
                                   included. A new session there acts in
                                   its opener's partition — SessionInfo
                                   carries partition — or, without a
                                   person, in global (api false when the
                                   tile has none); 409 while the tile's
                                   partition mode switches or the
                                   opener's partition of it is being
                                   reset or removed. resume answers 409
                                   for a past session of the other store:
                                   a partition's entry continues only in
                                   its person's session on the
                                   partitioned tile, a person's own entry
                                   only outside one. A person's terminal
                                   on the tile may open and drive its own
                                   agent sessions)
GET    /agent/history             terminal-level. Your past agent sessions,
                                   newest first: [{id, cwd, provider, mode,
                                   name, created, ended, turns, preview,
                                   loadable}] — the transcripts kept when a
                                   session ended or the daemon stopped (per
                                   user × tile, the newest 20 per tile;
                                   never-prompted sessions are not kept).
                                   ?cwd= narrows to a tile. loadable: the
                                   agent can reopen it (resume). An entry of
                                   a session that had a named target carries
                                   deployment. A session on a partitioned
                                   tile's entry is kept with its person's
                                   partition, and listed, read and deleted
                                   here the same way — never by a person
                                   recreated under the same id, nor by an
                                   admin viewing as the person (404)
GET    /agent/history/<id>/events terminal-level (own). {meta, events} — the
                                   persisted transcript in the live /events
                                   shape; 404 when not yours or gone.
                                   ?before=&limit= → {meta, …a page} as on a
                                   live session
DELETE /agent/history/<id>        terminal-level (own) → 204 (forget it)
```

## Agent session events


An agent session's log (`GET /term/sessions/<id>/events`) and its live
`session` events carry the same entries: `{seq, ts, type, data}` — `seq`
from 1 per session, `ts` unix milliseconds. A client renders from the
replay and applies live events by `seq`; on a skipped `seq`, a socket
reconnect, or a tab becoming visible it re-fetches `?since=<last>` (the
hub drops a slow subscriber rather than queue for it).

A resumed session's replay (the agent's `session/load` streams the earlier
turns back while the session is `starting`) is logged like any events but
not published one by one: when it is over, the hub carries one `session`
event `{seq:0, type:"replayed", data:{first, last}}` (the replayed seqs),
then the status that ended it. Re-read the tail (a page, or `?since=`) on
it. A client that ignores it still catches up: the status after it skips
`seq`s (xbind before 2026-09-28 published each replayed entry).

**Pages** (D130). A client need not replay the whole log (up to 5000
events): `?limit=<n>` (default 200, at most 5000) is the tail page,
`?before=<seq>&limit=<n>` the page before a seq — pass the previous page's
`nextBefore`. Either parameter selects a page; without both the replay
above is unchanged. A page is cut where a fold may start: never inside a
message or thought run, a tool call and its updates (and a subagent's
calls and text under it), a request and its answer, a turn's plan, or a
turn's end and its `files.changed` — nor after anything that may still
change (the running turn's unfinished calls and unanswered requests, the
text being written, a snapshot not yet reported). So folding the pages
one by one gives the blocks the whole log gives; a card longer than
`limit` makes its page longer. `{events, hasOlder, nextBefore, truncated,
next, last, state}`: `hasOlder` the ring holds events before the page,
`nextBefore` its first seq (0 when none), `truncated` the oldest page of a
ring that dropped earlier events, `next` its newest seq (the tail page's
is the follow cursor), `last` the log's newest. `state` is what a fold
holds before the page: `status` (a status event's data — the last status
before the page, with `status`, `detail`, `currentMode`, `options`,
`modes` and `commands` each from the latest status that carried it: fold
it first, as a status event), `usage` and `turn` of the last `turn.end`
before it, and on the tail page of a live session the `permissions` and
`elicitations` waiting now (they are in its events too) and `queue`, the
messages held for the turn's end now (a `queue` event's `items`; take it
before the page's events, whose `queue` events are newer). An older xbind
ignores both parameters and answers the whole replay — a client tells
them apart by `hasOlder`.

| type | data |
|---|---|
| `message.delta` | `{role:"user"\|"agent", text, messageId?, parent?, attachments?, steered?}` — a prompt is logged as one `user` delta, so every client sees it (`steered: true` on a message the running turn took mid-turn — POST …/queue, D197), with `attachments:[{name, mime, size, inline?}]` when it carried files (their bytes are not logged; `inline` true when the model got it with the prompt — an image block, an embedded text — else it is a file the agent was pointed at); agent text arrives in runs (a burst of tokens is coalesced into a few events); `parent` is the subagent tool call (`tool.call` with `subagent`) the text came from |
| `thought.delta` | `{text, parent?}` — the agent's reasoning, when it shares it |
| `plan` | `{entries:[{content, priority, status}]}` — the whole list, replacing the last |
| `tool.call` | `{id, title, kind, status, content?, locations?, rawInput?, rawOutput?, name?, label?, parent?, subagent?, planReview?, output?, outputDelta?, exitCode?}` — kind: read \| edit \| delete \| move \| search \| execute \| think \| fetch \| switch_mode \| other; content items are `{type:"content", content:{type:"text", text}}` (often markdown — the adapters fence command output), `{type:"diff", path, oldText, newText}` or `{type:"terminal", terminalId}`. The rest is lifted from the adapter's `_meta` so a client needs no per-agent code: `name` the tool's own name (`Bash`, `ExitPlanMode`, …); `label` a human headline for the call when the harness wrote one (Claude's description of a shell command — the `title` is the command); `parent` the subagent call this one runs under; `subagent` true on a subagent (Task/Agent) call itself; `planReview` true on Codex's plan approval; `outputDelta` a chunk of a shell command's output (append), `output` its whole output (replace), `exitCode` its exit status |
| `tool.update` | `{id, …}` — a partial update of that call (`status`: pending \| in_progress \| completed \| failed \| cancelled); `content`/`locations` replace, `outputDelta` appends to the output, `output` replaces it; a command's output chunks arrive in runs (`{id, outputDelta, parent?}` updates of one call are coalesced like the agent's text) |
| `permission.request` | `{pid, toolCall:{id, title, kind, rawInput?, content?}, options:[{optionId, name, kind}], rule:{kind, title, scoped}, meta?}` — kind: allow_once \| allow_always \| reject_once \| reject_always; answer on `POST …/permissions/<pid>`. `rule` is what "allow for the session" would remember (`scoped:false` = no session rule is possible and the clients hide that choice: the call has neither kind nor title, or it is a `switch_mode` — a plan approval, whose allow_always options are modes, never remembered); `meta` is the adapter's presentation hint when it sends one (`{title, description, defaultToNo}`) |
| `files.changed` | `{toolCallId?, turn?, changes:[{path, oldPath?, status, add, del, binary?}], patch:{format:"git_patch", text, truncated}}` — what a finished tool call (`toolCallId`) or a whole turn (`turn`, after its `turn.end`) changed in the tile, from snapshots of the work tree (tiles that are git repos; the tile's own repo, index and HEAD are never touched). status: added \| modified \| deleted \| renamed \| typechange; `patch` is a git patch capped at 64 KiB per call, 192 KiB per turn (`truncated`) — `GET …/diff?toolCallId=|turn=` serves the whole of it (and one file of it) while the session lives. Not sent for edit/delete/move/read/search calls (an edit reports its own diff) nor when nothing changed |
| `elicitation.request` | `{eid, toolCallId?, message, schema}` — the agent asks the user a question (ACP `elicitation/create`, form mode — Claude's AskUserQuestion, an MCP server's form); the session is `waiting_permission` until one client answers on `POST …/elicitations/<eid>`. `schema` is a flat JSON Schema object: a `oneOf`/`enum` string is a single choice (options `{const, title, description?}`), an array of `anyOf`/`enum` items is a multi-choice, plus plain string/number/integer/boolean fields; a string field whose `_meta._askUserQuestionCustomAnswer.questionId` names another field is that question's free-text "Other" answer. Cancelling the turn answers `cancel` |
| `elicitation.resolved` | `{eid, action, by, content?}` — action: accept \| decline \| cancel (by `agent`: withdrawn); `content` the submitted values (with accept) |
| `permission.resolved` | `{pid, optionId, by}` — by: `user:<id>`, `owner`, `auto` (a session rule), `cancel` |
| `turn.end` | `{turn, stopReason, usage?:{used, size, cost?}, error?}` — stopReason: end_turn \| max_tokens \| max_turn_requests \| refusal \| cancelled \| error |
| `status` | `{status, detail?, modes?, currentMode?, options?, commands?, agent?, login?, usage?, title?}` — status: starting \| idle \| running \| waiting_permission \| cancelling \| error \| exited; `title` is the agent's own name for the session (ACP `session_info_update` — most adapters generate one after the first turn); it names a session that has no name yet (SessionInfo `name`, announced by a `term` `rename` event) — a name the user gave is kept; `modes` (the agent's available modes), `options` (its settings: `[{id, name, category, type, currentValue, options:[{value, name}]}]` — model, effort, …, in the agent's priority order) and `agent` (`{name, version}`) ride every `idle`; `options` also rides a status whenever a setting changes; `commands` (the agent's slash commands, `[{name, description?, hint?}]` — `hint` says what to type after the name; a command is sent as ordinary prompt text, `/name args`) rides a status when the agent advertises them and every `idle` after; `login` (`{needed:true, provider, command}`) rides every status while the agent reports it is signed out (an `_auth/status_update{kind:none}`) or a turn hit auth-required — the frontend shows a sign-in: the provider's guided one when `GET /agent/providers` gives it a `signin` (D178: a link to open and a code to paste, the CLI running in a terminal session no tab shows), else — and on request — `command` run in a shell terminal sharing the agent's home; an `error` names what to do (no login → the command to sign the CLI in from a terminal) |
| `gap` | `{before}` — only on a `?follow=1` stream: the cursor predated the log's ring; earlier events were dropped |
| `queue` | `{items:[{id, text, files?, at}], sent?, dropped?, reason?}` — xbin's, not the agent's (D197): the messages held for the running turn's end after a change (`POST`/`DELETE …/queue`) — the whole list, replacing the last; `text` is a preview (200 characters), `files` the attachments' names. `sent` lists the ids that just went in as a prompt (its user `message.delta` follows); `dropped` counts held messages that went without being sent, with `reason` (the session ended; a prompt refused for good — a `notice` says so too). A client that doesn't know the type skips it |
| `notice` | `{text}` — a line xbin itself says in the session, not the agent; a client shows it muted, as its own row (the Agent tab does; a client that doesn't know the type skips it). Today: the session's start moved the tile's terminal layer to a new base image, resetting everything outside the workspace files and `$HOME` (base auto-update, D175; the event is the session's first); held messages that were not sent (D197) |

The live log is in memory; an `exited` or `error` status is final and the
session leaves the directory (`term` event `close`). Its transcript does not
die with it: when a session ends — or the daemon stops — the log and a little
metadata are written to `data/agent-history/` (per user × tile, the newest 20
per tile — on a partitioned tile per person's partition × tile, deleted by a
partition mode switch; a session that never took a prompt is not kept). `GET
/agent/history` lists them and `GET /agent/history/<id>/events` serves one in
this same shape, read-only; where the agent advertised `loadSession`, `POST
/term/sessions {resume:<id>}` reopens it (the agent replays the earlier turns
as events, then continues) and the continuation supersedes the entry.
