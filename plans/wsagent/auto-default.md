# Workspace agent — tile agents act without asking, continuing is smooth (D251)

> Status: live

The owner, on two questions about D250 (2026-11-21), verbatim:

- "should xbind pin OpenCode's permissions to ask so it gets a real asking
  mode?" — "no, keep all agents generally in auto/always-accept";
- "continuing a session that carries your sign-in needs the warned tick
  each time — keep or relax?" — "No, continuing sessions should be
  smoother UX".

**The reading this builds.** The tile's sandbox is what contains a coding
agent. What contains the workspace agent is that its person approves
every TEXT it sends a coding agent: the start's brief, each follow-up,
each message. Per-edit asking is not the norm, and xbin stops treating
"acts without asking" as a danger: it is a fact the sheet states.

Supersedes, in D250 (`plans/DECISIONS.md`) and its spec
(`plans/wsagent/approvals-ux.md`): the default mode is the asking one;
a suggested mode that doesn't ask is never preselected (`modeNotTaken`)
and a suggested coding agent with none that asks isn't either
(`harnessNotTaken`); Remember never keeps a mode that doesn't ask (and a
read ignores one); a non-asking mode is a warning — red banner,
danger-coloured Allow, armed delay, a plan line warned and unticked; a
follow-up into a session that carries the person's sign-in needs a tick
each time (L1); a session the person approved in a non-asking mode takes
the agent's messages unapproved. Each is restated below as the rule that
replaces it; `approvals-ux.md` says which of its rules this changes.

## The modes, per coding agent

What each harness's modes are (`sdk/acp/providers.go`; Claude Code's
from its adapter's own table, claude-agent-acp 0.81.1
`dist/session-mode.js`), what a session starts in today, and D251's
default for a session the workspace agent starts:

| Coding agent | Modes (asks / doesn't / explicit) | By hand, Agent tab | Agent template (D147), "auto" | D251 default |
|---|---|---|---|---|
| Claude Code | `default` asks; `plan` plans, edits nothing; `acceptEdits` edits unasked, asks for commands; `auto` "Claude handles permission decisions" (its own reviewer, it works through); `bypassPermissions` explicit, everything | `default`, or the last mode the person picked | `auto` | `auto` |
| Codex | `read-only` asks; `agent` ("Approve for me": edits and runs commands in the workspace unasked, asks to leave it); `agent-full-access` explicit | `read-only`, or the last picked | `agent` | `agent` |
| Gemini CLI | `default` asks; `plan`; `autoEdit` edits unasked, asks for shell commands; `yolo` explicit, everything | its own `default`, or the last picked | `autoEdit` | `autoEdit` |
| OpenCode | its `build` (its own default) and `plan` agents, by its configuration: neither asks (D250 review M2) | its own (`build`) | its own | its own (`build`) |

**The default is the harness's ordinary always-accept mode** — the one
the agent template already starts a person's coding agent in when they
leave it on "auto": the mode `acp.Provider.WorkMode` names (its own
non-explicit `auto`, else its `AutoMode`, else its own default). Where a
harness has both an accept-edits mode and a bypass-everything one, the
default is never the bypass: those are the catalog's Explicit modes —
never a default anywhere, only picked by name (`agent-full-access`,
`yolo`, `bypassPermissions`). For Claude Code that costs nothing — its
`auto` works through without stopping; for Gemini CLI `autoEdit` may
stop to ask before a shell command, and its person can pick `yolo` on
the sheet (remembered from then on). A person can always pick an asking
mode.

## A. Mode is a normal choice

1. The default mode of a start the workspace agent asks for is the
   coding agent's work mode (above). The agent's suggestion of a mode,
   or of a coding agent, is a preselection the person sees — never an
   Explicit mode.
2. A mode, asking or not, is remembered like the coding agent and the
   model — "Remember for this tile", the person's preference
   (`agentSessions.modes`) — still only what the person changed. The
   store keeps any of a harness's modes but an Explicit (bypass) one; a
   v0.3.83 file (OpenCode's `plan` in `modes`) and a v0.3.84 one load,
   and never block a write.
3. The sheet states it: beside the mode, a chip — "acts without asking"
   or "asks before acting" — and one plain line: "It edits files and
   runs commands in this tile's sandbox without asking you. You approve
   what your agent tells it." No red banner, no danger colour, no armed
   delay for the mode; the button is the ordinary primary "Start". The
   same on the brief's card, the batch row, the plan review (a start
   line is an ordinary line, ticked like the others), the continue sheet
   and the app's sheet.
4. What keeps its warning and its explicit, unticked tick, unchanged: a
   saved sign-in riding a NEW session (D230); the trust line and the
   home rules (D245); the wider standing grant ("every write to the
   tile's API"); reading the web (`readWeb`); text the sheet hides; a
   plan line's own warnings (it spends, it is public, an admin's).
5. What must not loosen:
   - a path with no sheet — "start without asking" (the preference),
     an unasked start, a standing approval, a notification's Allow
     once — never starts a session in a mode that doesn't ask, and
     never sends text to one. With the default now such a mode, an
     unasked start goes to the sheet: one approval, and the agent is
     told so in plain words. It starts unasked only where the person's
     preference names a mode that asks;
   - every message the workspace agent sends a session that acts
     without asking is an approval of that exact text, once (a
     `message` request, or a continue) — whatever mode the session was
     approved in (D250 let a session approved in its mode take messages
     unapproved: gone);
   - the ticket binds tile, brief and kind; `seen` is required with any
     pick or continue.
6. The workspace agent is told: sessions normally act without asking; a
   follow-up to one is shown to its person and goes when they allow it —
   file it and carry on; don't ask them in chat first, and don't open a
   new session to avoid it (`tile_agent_start`, `tile_agent_message`,
   and their results: the start's dry run, the "started" update, the
   message's pending answer, the mode refusal's words). The prompt is at
   its cap: it grows by nothing — two of its lines said the D250 rule
   ("messaging needs no approval of its own"; "one you start runs in an
   ask or plan mode") and were replaced by shorter true ones. A message
   held behind a session's question that would meet the mode rule when it
   goes in is filed for its person's approval then, never dropped.

## B. Continuing is smooth

1. Continuing a session the workspace agent started is one plain
   approval: the sheet shows the session — its coding agent, model, mode
   and, when it carries one, "uses your Claude Code sign-in" — as
   elements, the whole text, the primary "Send" and the quiet "Start a
   new session instead". No tick, no warning, no armed delay for a
   carried sign-in or a mode: the person ticked that sign-in when the
   session started; a follow-up is the same session doing more of what
   they approved. What stays: the session must still be the one shown —
   the same session, owner and tile, its coding agent, mode and sign-in
   as the sheet's `seen` — or the approval is 409 and the sheet looks
   again; at the redeem the same, refused, nothing spent.
2. The batch row allows a continue or a message in place: the session's
   chips, the first lines of the text and, when there is more, "Show
   all" (one click, the whole text, before Send), and "Send" and "Deny"
   on the row. Review… opens the full sheet; Review all walks every
   item. In the conversation the marker opens xbin's sheet — the agent's
   page can't decide anything for its person (`POST …/approve` is 403
   from it, D250), so allowing "in place" there is the sheet over the
   page: one click to open it, one to Send.
3. Not in this batch — a per-session standing grant ("don't ask again
   for this session"). It would let the workspace agent's texts go to
   one session unapproved until it ends or the person revokes it; it
   would give up the one thing that contains the workspace agent — the
   person seeing each text before a coding agent that acts without
   asking runs it — for that session, against a prompt injection that
   rides one of its reads. The owner decides it separately.

## C. The `agentTab` pass under `HARNESS_ISOLATE=1`

It isn't written for sandboxes: the scripted agent is a host path
(`XBIN_AGENT_FAKE=$REPO/bin/fakeacp`), which a tile's sandbox can't run —
`hack/ui-harness/run.sh` says so, and `agentChrome` already skips the
same step under isolation. The pass now SKIPs there with that reason.
(The isolated tests run the scripted agent inside the tile instead,
`test/isolated` `startWsAgentAt`.)

## Wireframes

The start sheet — desktop (OpenCode remembered for the tile):

```
+------------------------------------------------------------------+
| [shield] XBIN  APPROVAL                          asking Ada Admin |
| Start a tile agent in Eui?                                       |
|  Coding agent  [OpenCode              v]                         |
|                remembered for this tile                          |
|  Model         [its default           v]                         |
|  Mode          [its own default       v] ( acts without asking ) |
|                It edits files and runs commands in this tile's   |
|                sandbox without asking you. You approve what your |
|                agent tells it.                                   |
|  [ ] Remember for this tile                                      |
|  Tile          Eui   apps/eui                                    |
|  What it will be told   Written by your agent                    |
|  | This is a piece of work in this tile ...                  |   |
|  > Details                                                       |
|  [Deny]                                              [Start]     |
+------------------------------------------------------------------+
```

The start sheet — phone (430): the same rows stacked, the chip under
the mode's select, Deny and Start in the foot, Start on the right.

The continue row — the batch, desktop:

```
| [shield] Continue the tile agent in Eui                          |
|   ( Claude Code ) ( model opus ) ( acts without asking )         |
|   ( uses your Claude Code sign-in )                              |
|   Add a computer player to the tic tac toe game. The computer    |
|   plays O; it waits half a second before its move ...            |
|   [Show all]                        [Review…] [Deny] [Send]      |
```

The continue row — phone: the chips wrap, the text keeps three lines,
"Show all" under it, then Review…, Deny and Send on one line.

The continue sheet — desktop:

```
+------------------------------------------------------------------+
| [shield] XBIN  APPROVAL                          asking Ada Admin |
| Continue the session in Eui?                                     |
|  (o) Continue the Claude Code session from 10:52                 |
|      ( Claude Code ) ( model opus ) ( acts without asking )      |
|      ( uses your Claude Code sign-in )                           |
|  ( ) Start a new session instead                                 |
|  What it will be told   Written by your agent                    |
|  | Add a computer player to the tic tac toe game ...         |   |
|  [Deny]                                               [Send]     |
+------------------------------------------------------------------+
```

Phone: the same, one column; the radio labels wrap.

## Found while building it

What the work-mode default changed elsewhere, fixed in this batch:

- A plan's binding that waits for a tile its own tile agent builds went
  `stale` when the agent — acting without asking now — wrote the
  manifest before Apply reached the line. It is judged as the waiting
  line it was approved as, at once: bound if the tile provides exactly
  what was approved, never stale for being early.
- The workspace agent knew a session asks only when its mode was
  start-defaults' — the work mode now, which doesn't. xbind's compact
  session read says `asks` per session, and the agent goes by it.
- Claude Code's adapter has no `auto` for a model that doesn't support
  it: it takes `acceptEdits` instead (claude-agent-acp 0.81.1
  `AUTO_MODE_FALLBACK`) and says so as the session's mode; by its code
  (not run live here) set_mode succeeds, so the start isn't refused, and
  the session reads as acting without asking either way.

## Nothing breaks for an existing workspace

A person who had picked a mode — asking or not — keeps it: it is their
choice and it is remembered as before. A person who had none: the
sessions their agent starts now act without asking, and the sheet says
so on every start (the chip and the line); their "start without asking"
preference starts a session unasked only in a mode that asks they name —
otherwise the start goes to the sheet, once. No migration note.
