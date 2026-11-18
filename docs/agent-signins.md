# Coding-agent sign-ins

A coding agent (Claude Code, Codex, Gemini CLI, OpenCode) needs someone's
account to call its model. There are three ways a session gets one:

1. **the workspace's LLM connector** ([llm.md](/docs/llm.md)) — no
   sign-in at all, for the coding agents that can be pointed at it;
2. **a sign-in you saved with xbind** (D230, this page) — made once, kept
   sealed, used for your sessions on every tile where that is safe;
3. **the tile's own login** — the CLI's own sign-in in a terminal (`claude
   auth login`, `codex login`), which lives in your `$HOME`
   ([protocol/agents.md](/docs/protocol/agents.md)).

The wire is [protocol/signins.md](/docs/protocol/signins.md).

## What a saved sign-in is

A credential the coding agent takes from its environment:

| Coding agent | Guided sign-in | Or paste |
|---|---|---|
| Claude Code | `claude setup-token`: a token good for a year, for model requests only | an `sk-ant-oat…` token, or an Anthropic API key |
| Codex | — | an OpenAI API key |
| Gemini CLI | — | a Gemini API key |
| OpenCode | — | a provider's key (Anthropic, OpenRouter, OpenAI, Google, Groq, xAI) |

Only Claude Code has a sign-in that prints a credential which can be
kept. The other CLIs' subscription sign-ins keep a refresh token in a
file; a copy of such a file signs the original out the first time either
refreshes, so xbind never copies one. Their subscription login stays the
tile's own.

Each person keeps their own: named, up to 8 per coding agent, one of them
the default.

## Making one

**The guided sign-in.** xbind runs the CLI's own sign-in for you — not in
your tile, but in a throwaway sandbox of its own: the base image, no tile
directory, no home, an empty `HOME`, internet egress only. You get a link;
you sign in on the vendor's page in your own browser and bring back the
code it shows; xbind types it into the CLI and keeps the token the CLI
prints. The token goes from xbind's reader into the store. It is never
sent to your browser, never logged, and nobody can enter the sandbox it
was printed in.

**Pasting.** A key or token you already have, pasted once. xbind checks
its shape only (one line, no spaces or quotes) — it calls no vendor — so a
wrong key shows the first time a session uses it.

## Where it is kept

`data/signins/` under the workspace, one file per person, outside every
sandbox's view. The metadata (name, coding agent, kind, the times) is
readable while the vault is sealed; the secrets are sealed under the
workspace vault, each person's under a key of their own.

| The vault is | Then |
|---|---|
| open | sign-ins are used and saved |
| sealed | none can be used or saved until an admin unseals; the list still reads and Remove still works |
| not set up | saving is refused: a sign-in is never kept in the clear |
| plaintext (`--insecure-vault`, `--no-auth`) | kept in the clear like every secret of such a workspace |

**Nothing reads a secret back.** No route, event or log line carries one
or a part of one — there is no "show" and no "last four". You can rename
a sign-in, make it the default, replace it or remove it.

The store needs sandboxes (`xbind --isolate`): without them every terminal
runs as xbind's own user and could read a coding agent's environment. A
development workspace (`--dev`) is excepted, and the list says so. Saved
sign-ins are a signed-in person's: a workspace run with `--no-auth` has no
people, and so none.

## Which sessions use it

When you start a coding agent on a tile — in the Agent tab, or through
your workspace agent — xbind looks up your default sign-in for that coding
agent (or the one the start names) and, where the rule below allows, puts
it into the session:

- into the **coding agent's process alone**. The credential rides the one
  frame that starts the agent, to the agent host inside the session's
  sandbox, which sets it on the agent it spawns. The sandbox's own
  environment, the agent host and a terminal you open on the tile never
  hold it.
- **Codex** takes a key only through its own sign-in call and then writes
  it to `auth.json`. The agent host gives that session a `CODEX_HOME` of
  its own — a directory in the sandbox's `/tmp`, which no other session
  mounts and which dies with the sandbox, holding links to everything in
  your `~/.codex` but `auth.json` — so the key file is never in your shared
  home. If Codex is already signed in on its own in that home, its login
  stays in force and the saved key is left out.

**What a session runs on, in order**:

1. the start's own choice (`signin`: one of yours, or `"none"`);
2. your saved default for the coding agent — by default, and on a tile
   that isn't only yours once you trust it (the rule below);
3. the coding agent's own login in the session's `$HOME` — the tile's own
   login;
4. the workspace's LLM connector, where it can serve.

Where a rule gives the connector alone (the workspace's `connector-only`,
an org's model rule), or you chose the connector, no sign-in goes in. The
session says which it is — "Using your saved sign-in Personal", or why
not — in its first lines and in the Agent tab's header.

### The rule: used by default, and "I trust this tile's source"

*(This rule replaced a stricter one on 2026-11-17 — "only where nobody
else can change the tile's code". The workspace's owner accepted the risk
model below; what it exposes is listed under "Who can read it".)*

Every session is a sandbox of its own, so nobody else can read what your
coding agent holds. But **code in a tile runs with your sign-in while
your coding agent works there**: the project settings a coding agent
loads (hooks, MCP servers), a Makefile or a script it is asked to run.
Whoever put that code there could read the sign-in and use your account
with the coding agent's vendor.

So xbind decides, itself, at every start:

1. **It is your own session**: opened by your signed-in browser, app or
   device. Never by an admin viewing as you, a tile's own credentials (a
   frame, a backend, a terminal's token), or the root token. A session
   **your workspace agent** starts has a sign-in only where you ticked it
   for that start (below).
2. **Sandboxes are on**, the workspace's switch is on, the connector
   isn't the only one allowed to serve, and you didn't choose it.
3. **The default is on.** On a tile whose source is only your own — you
   own it, and nobody else who isn't a workspace admin can change it —
   your default sign-in for the coding agent is used. Nothing is asked.
4. **Anywhere else xbin asks first, once per tile: "I trust this tile's
   source".** A tile's source is not only your own when either holds —
   they are two separate conditions:
   - **it is owned by someone else**: another person, an org, or the
     workspace (a tile belongs to whoever created it until it is handed
     on). A workspace-owned tile is not someone else's **for a workspace
     admin**: every admin can change every tile anyway — theirs too. (An
     org's tile always asks, whatever you are in the org.)
   - **someone else who isn't a workspace admin can change its code**:
     write level or more, by an entry of their own, a pattern, or an org
     the tile is shared with — on a partitioned tile too (a partition
     keeps people's data apart; the tile's directory is the same code in
     every partition).

   - **someone else could change its code before, and can't now** — a
     past owner (a person, an org), or a person who had write level and
     lost it. What they left in the tile is still there. xbind keeps, per
     tile, who it has seen able to change the code; such a tile asks each
     person once, and the card names them ("bob could change this tile's
     code until 2026-11-18; trusting it means you trust what is in it").
     A tile you made and never shared has no such record and asks nothing.
     For a workspace admin a workspace-owned tile stays "theirs" only
     while no non-admin has been able to change it since the record
     began. **What xbind can't know:** who could change a tile before it
     kept this record (it starts with the xbind that has this rule), and
     changes made to the workspace's files while xbind was not running.

   The tile is the registered tile the session's directory lies in: a
   session opened in `apps/crm/src` is judged on `apps/crm`. Until you
   answer, the session starts **without** your sign-in and says why —
   "Your saved sign-in Personal isn't used here: apps/crm isn't only
   yours — it belongs to bob, and carol can change it — and you haven't
   said you trust its source" — and runs on the tile's own login or the
   connector.

**The card.** In the tile's Agent tab: whose tile it is and who can
change it — by **account id**, the display name beside it (a display name
is its bearer's to set; the id is what xbind checks) — two sentences —
"Trusting means your Claude Code sign-in is used here and this tile's code
can change your home. People who can change this tile could read it and
use your Claude Code account." — trusting is those two things, and
nothing a tile wrote to its copy of your home is ever copied into your
home ([below](#your-home-on-a-tile-that-isnt-only-yours)) — and two
choices of equal weight: **Trust this tile — use my sign-in**, or **Start
without my sign-in** (it says what the agent then runs on). Neither is pre-selected; "Trust this tile" answers only your own
click, a moment after it appears, never Enter. With more than three people
able to change the tile the card lists three and offers **Show all**: you
see every one of them before the button can be used. A page can open the
card (`xbin:agent-open-signin`); nothing but you answers it — not an
admin, the tile's owner, your workspace agent, or a page's message.

**You answer what you were shown.** The card's question carries a token
of what it shows — the tile, its owner, everyone who can change it. If
any of that changed while the card was open, your click is refused: the
card draws the question as it stands, says so, and its button waits
again.

**What your trust covers — and when it ends.** That tile — the tile
itself, at that path — as it stood: its owner, and the people who could
change it. It is **void from the moment any of that changes**: someone
new can change the tile, it has another owner, it moved, or a tile was
made again at its path. xbind sees the change when it happens — with no
session of yours running — ends the session that ran on your sign-in,
and asks again the next time. A void answer **stays void**: if someone
gained write and lost it again, or the owner changed and changed back,
you are still asked — they may have left something behind. (Someone who
can *no longer* change the tile is simply dropped from what you trusted;
their coming back is someone new.) An answer is also for
**what trusting meant when you gave it**: one given before trusting a tile also
decided your home there (below) is void, and xbin asks again with the
words as they are now. Your trusted tiles are listed with
your sign-ins ("Tiles you trust"), and **Stop trusting** ends your
sessions there that run on a saved sign-in.

**Your workspace agent never gets your sign-in by itself.** It directs a
session it starts — it writes the session's prompts and reads what the
session prints — so what it tells the coding agent could expose a sign-in
that session holds.

- A start your **"start without asking"** preference lets through
  **never has your sign-in**, on any tile — your own included. There is
  no setting that changes this.
- A start **you approve** shows, on xbin's own sheet, what the session
  runs on as it stands ("Runs on the workspace's model.") and — when you
  have a sign-in saved — a line of its own, **unticked**, in the warned
  style: "Use my Claude Code sign-in ‹label› for this session — Your
  workspace agent directs this session: what it tells the coding agent
  could expose your sign-in." Tick it and that sign-in, and no other,
  goes into that one session. The next start asks again.
- On a tile that isn't only yours the **trust line** sits under it —
  "I trust this tile's source", unticked, tickable only together with
  the first, and only once everyone who can change the tile has been
  shown. Tick both, or the session starts without your sign-in:
  approving a start never implies using your sign-in, and never implies
  trust.
- Left unticked, the session runs on the workspace's model with a coding
  agent it serves — the default for a start your agent proposes. Where
  the chosen coding agent can't run that way, the sheet says so before
  Start ("Starts signed out: …").
- A start on a tile the same plan creates as your own offers the line
  like any other.

Your agent is told only that a saved sign-in exists — never its name, in
`start-defaults`, in the providers list or in what it reads of a session
it started. **What remains when you do tick it**: your agent can steer
that session — ask it to print its environment, to write the sign-in to
a file in the tile — and masking what the session prints is best-effort
(a file written in the tile is not masked at all). Tick it for a start
whose brief you have read.

Workspace admins are not counted among the people who "can change" a
tile: an admin can change any tile, its grants and the workspace itself.
A tile an admin *owns* is another person's tile like any other. What an
admin cannot do is listed under "Who can read it" below.

A session that carries a saved sign-in is **yours alone**: no one else —
admins included — can prompt it, answer its questions or read its
transcript, as on a partitioned tile; nor its transcript in your history
once it ended (an admin viewing as you sees the entry, not what was said:
403). An admin can still end it.

A restart keeps what the session was started with and never adds to it:
one started without a saved sign-in stays without, one your agent started
keeps exactly what your tick put in — or nothing. (After you trust a tile, the Agent tab
restarts your own session there — one you opened on your default — and
that restart uses it.)

**Coming from the stricter rule.** Sessions that were refused a sign-in
because someone else could change the tile get the question instead: the
next time you open the Agent tab there, the card asks. Nothing is trusted
for you. A session your workspace agent starts, which used your sign-in
when you approved the start, now uses it only when you tick the line. Your own tiles that nobody else can change keep working as they
did; tiles owned by an org, the workspace or another person — where the
old rule used your sign-in if nobody else could change them — now ask
once.

### When it stops being used

| What happens | Then |
|---|---|
| You remove the sign-in, or replace its secret | later starts don't have it; the sessions running on it are **ended** (they hold a copy), with a line saying why. The credential stays valid at its vendor until it expires or you revoke it there. |
| An admin revokes it | the same, and your list says who |
| Someone **new** becomes able to change the tile, the tile gets another owner, moves, or is made again at its path | your trust is void from that moment — for good, also if it is undone: the session running there on your sign-in is ended, and xbin asks again |
| You are a workspace admin working on a workspace-owned tile, and stop being an admin | the tile is no longer yours to change as an admin: the session on your sign-in is ended, and xbin asks |
| You stop trusting a tile | your sessions there on a saved sign-in are ended |
| An admin switches saved sign-ins off, or switches trusting off (or the host does) | the sessions running on one are ended — with trusting off, the ones on tiles that aren't only yours |
| An admin sets your password, mints you a sign-in link, or binds an SSO email to you | **all your saved sign-ins and your trusted tiles are removed**: otherwise resetting your account would be a way to run sessions on them. Your list says so; sign in again. |
| Your account is disabled, or signed out everywhere | no sign-in is used; sessions running on one are ended |
| Your account is deleted | your sign-ins and their use log are deleted at once; an account made later under the same id reads none of it |
| The vault is sealed | none can be used; sessions start without and say so |
| It expires (a setup-token: a year) | it is not used, and the list says so |

### Who can read it

| | |
|---|---|
| **On your own tile that nobody else can change** (the default) | **only code you put there** — and code an admin put there. A tile you made from a template or imported can carry a hook: that code is yours to have read. |
| **On a workspace-owned tile, as a workspace admin** (no trust asked while no non-admin can change it) | code any workspace admin put there: admins can change every tile, yours included. |
| **On a tile you trusted** | **the people the card listed**: its owner (a person, the members of the org who can change it, the workspace's admins) and everyone else who could change it then. Any of them can leave a file in the tile that runs in your session, reads your sign-in and uses your account with the coding agent's vendor. Someone who gains that ability later is not covered: your trust is void from then on — also if they lose it again — and xbin ends the session and asks again. |
| On a tile that isn't only yours and that you haven't trusted | no: it is not put into a session there |
| A tile's own code (backend, frame) | no: `data/` is in no sandbox, the routes refuse tile credentials — it reaches a sign-in only by running inside your coding-agent session, as above |
| **Code that runs in your own session** | **yes**: the coding agent, its tools, the project's hooks and scripts share its environment. The same was always true of a login in `$HOME`, which is the broader credential. |
| **The coding agent's model** | **yes**: it can run a command that prints the credential and read the answer. It is the credential's own vendor unless you pointed the agent elsewhere. |
| **Your workspace agent's model** | never by default: a session it starts has your sign-in only where you ticked "Use my … sign-in for this session" on that start's approval — and then **it can**: it prompts that session and reads its results. Masking (next row) lowers the chance; it does not prevent a session that is asked to from handing the credential over in a form xbind doesn't recognise. |
| What xbind keeps of a session (events to pages, history, your agent's `tile_agent_result`, logs, backups of them) | masked, **not guaranteed**: xbind replaces the credential with `[saved sign-in]` on the joined text of each stream — as it is, with anything between its characters (spaces, newlines), as base64, hex or `%XX` — and masks anything shaped like a harness credential (`sk-…`, `AIza…`). That catches a tool that printed its environment. It cannot catch every encoding, and it is not a boundary: treat a session that holds your sign-in as able to disclose it. |
| **The coding agent's own files in your home** | **not masked**: Claude Code and Codex keep their own transcripts (`~/.claude/projects/`, `~/.codex/sessions/`) — xbind needs them there to resume a conversation — and your home is the same on every tile. If a tool printed the credential, it is in those files, where a session of yours on *another* tile (one you haven't trusted, which got no sign-in) could read it. If that happened: remove the sign-in here, revoke it at its vendor, and delete that transcript. |
| **Your home, shared by all your tiles** | **closed for writes, open for reads.** On a tile that isn't only yours and that you haven't trusted, your sessions get a *copy* of your home: they read everything in it, and what they write under `~` stays on that tile ([your home on a tile that isn't only yours](#your-home-on-a-tile-that-isnt-only-yours)). So code there can no longer leave a `~/.claude/settings.json` hook, a `~/.gitconfig` setting or a `~/.bashrc` line that runs later on your own tiles, beside your sign-in. It can still **read** your home: a `claude auth login` kept in `$HOME`, an SSH key, a `~/.netrc`, the transcripts of the row above are readable there as they always were — which is why a saved sign-in (never in your home, never in a session on an untrusted tile) is the safer way to sign a coding agent in. |
| A workspace admin | not through xbind: no route reads it, they can't drive your session, and a reset of your account removes the sign-ins. An admin can change a tile's programs or code before you start a session there. |
| Whoever runs the machine | yes (xbind's memory; the files, in the plaintext vault mode) |

Every use is logged for you (`GET /agent/signins/uses`: when, which
sign-in, which tile and session, whether you or your agent started it) and
in xbind's audit log — never the secret.

## Your home on a tile that isn't only yours

You have one home directory for all your tiles (`~`: your git identity,
SSH keys, dotfiles, your coding agents' settings and logins). Every
terminal and coding-agent session you run mounts it.

**On a tile that isn't only yours — and that you haven't trusted — your
sessions get this tile's copy of your home.** The rule is the trust rule
above, the same answer: the tile is owned by someone else (another person,
an org, the workspace unless you administer it), or someone else who isn't
an admin can change its code — or could before — and you have not said "I
trust this tile's source". Where an admin or the host has switched
trusting off, an answer you gave is not usable: the copy.

- **Reads are unchanged.** The session sees everything in your real home,
  as it is now: `git` knows who you are, your SSH keys work, your tools
  find their settings.
- **Writes stay on this tile.** Whatever the session writes under `~` — a
  dotfile, a tool's cache, a shell history, a `claude auth login` — goes
  to a copy kept for you on that tile, and is there again the next time
  you work on that tile. It never reaches your real home, your other
  tiles, another person, or the tile's own backend.
- The session says which it has: its window's status bar and its row show
  **home: yours** or **home: this tile's copy (changes stay on this
  tile)**, and the first session on a tile's copy says so in its first
  lines.

- **If you are a workspace admin** (or work with the root token): a
  session on a tile's copy of your home is
  **confined like a member's until you trust that tile** — it sees the workspace read-only through
  the tiles you may read, can't mount or open new namespaces, has a
  member's resource limits, and is not offered the host's network. An
  admin's sandbox can mount: with it, the tile's code could mount its way
  around the copy to your real home, or to your own tiles' code. The
  session's first lines and the home dialog say so. Trusting the tile
  gives new sessions there the admin's sandbox, and your real home.

Why: code in a tile runs in your sessions there — a project hook, a build
script, a Makefile. With your real home writable it could leave a file
that runs *later*, on your own tile, where your saved sign-in is:
`~/.claude/settings.json` hooks, `~/.gitconfig` (`core.fsmonitor`,
`core.hooksPath`, an alias), `~/.bashrc`, `~/.npmrc`, `~/.local/bin`,
`~/.ssh/config`.

**Trusting the tile** (the home line in the status bar asks; the sign-in
card does too) means two things: your saved sign-in is used there, and
that tile's code can change your home. New sessions on the tile then have
your real home. Nothing the tile wrote to its copy is merged into your
home — there is no "copy it over", because that would be exactly the
attack — and the copy is kept, unused, until you forget it. Sessions
already open stay on the copy (a restart keeps it: your coding agent's
own session files are in it); start a new one for your real home. If your
trust stops holding — you take it back, someone new can change the tile,
it gets another owner, an admin switches trusting off — the sessions that
had your real home there are ended, and the next one is on the copy as you
left it.

The button that trusts is the same deliberate click as the sign-in
card's: it answers a moment after the dialog appears, to your own click —
not Enter, not a script.

**Forget what this tile wrote to my home** (the same dialog, and the list
under *Coding-agent sign-ins*, which shows each copy and its size) ends
your sessions on a copy and then deletes it; while one of them is still
starting, forget waits for it, and if it doesn't come up in time nothing
is removed and you are told to try again. A copy also goes when its tile
is **deleted** (its directory is not there any more), or your account
is. xbind never deletes a copy on a doubt: when a tile was made again at
its path — which is also what a workspace **restored from a backup**
looks like: every tile gets another identity — the old copy is never
mounted again, and stays in the list as "its tile was deleted or made
again — this copy is not used any more" until you forget it. Your
sessions on the restored tile start a new copy, and your trust answers
are asked again. Your real home is never touched by any of this.

What to know:

- **It is about writes.** Code on an untrusted tile can still *read* your
  home. Don't keep a secret there that such code must not see; a saved
  sign-in is never in your home.
- A file your session changes is copied to the tile's copy first, whole
  (a large database in `~`, once per tile). A program that had it open
  before the change keeps reading the old one.
- File locks and sockets in `~` are shared by your sessions on the same
  tile, not with your sessions elsewhere.
- A copy is mounted with the kernel's overlay filesystem, so every user
  in the session — a non-root one too (`su`, a service's own user) — uses
  its home as on the real one. Its one limit: a directory that exists in
  your real home can't be renamed in place on a copy (`mv` copies it
  instead; a tool that renames it itself fails).
- On a kernel that can't mount an overlay in a user namespace (before
  5.11) the copy is mounted with fuse-overlayfs instead, and stays with
  it. There directories rename, but
  **only root in the session can use the home**: a FUSE mount held outside the session's user namespace
  serves one user, and a non-root process gets "permission denied" on
  `~`.
- A session someone else could type into never has your real home: an
  admin can't attach to, or drive, your session when it has your own home
  (they can end it). A session opened by a terminal's own token (`bx
  agent` in a terminal) has your real home
  **only when that terminal has it** — not because another session of yours on the tile does.
- The root token has its own home (`homes/owner`) and keeps no trust
  answers: on a tile owned by a person or an org it always works on a
  copy. `bx agent` in the root token's terminal is the root token's too.
- If the copy can't be mounted (neither overlay works; a copy
  fuse-overlayfs wrote and no fuse-overlayfs any more), the session
  **fails** and says why. Your real home is never opened in its place.
- A session that was starting while your trust stopped holding — or while
  you forgot its copy — is judged again the moment it is listed, and
  ended with the reason.
- **Without sandboxes** (`xbind` without `--isolate`, the development
  mode) there is nothing to mount: every session is a process of xbind's
  own user with your real home, and nothing here protects it. The row
  says `home: yours` with a note that the workspace has no sandboxes.

Admins: copies live in `data/home-copies/<person>/<tile>/` (in a backup of
`data/`), are mounted in no sandbox but through their overlay, and are
removed by xbind when a tile's directory or an account is deleted —
never because a tile's identity changed or could not be read. Homes have
no quota in xbind; the list shows each copy's size. The record of who
could change each tile's code is `data/signins/tiles.json` (written the
first time a tile has, or had, a second party; a one-person workspace
has none). The process that holds a copy's mount (and, on the
fuse-overlayfs fallback, its fuse-overlayfs) runs outside the tile's
resource limits: it does only what the sessions' file operations ask of
it, and ends with the last session on the copy.

## Where you meet it

One element, `<bx-agent-signin>`, in xbin's own chrome:

- **The Agent tab.** A coding agent that says it is signed out shows the
  card: **Sign in** runs the guided sign-in (a link, a code, Finish — the
  code field has the focus, Enter finishes, Escape cancels), **Paste a key
  instead**, and **Sign in on this tile only** — the CLI's own sign-in,
  whose login stays in your home on this workspace. After a sign-in is
  saved the agent restarts on it and its conversation resumes where the
  coding agent can reopen it. On a tile that isn't only yours the **trust
  card** asks before a saved sign-in is used ("Use your Claude Code
  sign-in on this tile?"); answered "Start without my sign-in", the header
  keeps "Use my sign-in here…". The tab's status line says what a session
  signs in with ("your saved sign-in: Personal", "this tile's own login",
  "saved sign-in waits for your trust", "saved sign-in not used" with the
  reason), and **Sign-ins…** opens the list.
- **The list**: per coding agent, each sign-in's name, kind, when it was
  saved and last used, and when it expires; Make default, Rename, Remove
  (which says what it does first), Add; **Tiles you trust** (Stop
  trusting; one whose owner or editors changed says xbin will ask again);
  where they were used; for workspace admins the switches and who has
  sign-ins.
- **Your workspace agent.** A brief or a plan line whose coding agent
  would start signed out says "Needs your Claude Code sign-in" with a
  button. The agent's page only names the coding agent
  (`xbin:agent-open-signin`): the card is drawn by xbin's chrome, so no
  link, code or key passes through the agent or its page. The approval
  sheet and a plan's start line say what the session runs on, and offer
  your saved sign-in as an unticked line — with the trust line under it
  where the tile isn't only yours.
- **The app (iOS)** uses a saved sign-in like every other client — it is
  xbind that puts it into the session — and shows what a session signs in
  with; saving one, trusting a tile, and ticking your sign-in for a
  session your workspace agent starts are done on the web. A start
  allowed from the app has **no** saved sign-in, and the sheet says what
  it runs on and where to go for the tick.

## Admins and hosts

- **The workspace's switch** — "people may save coding-agent sign-ins", on
  by default (`PUT /agent/signins/policy`). Off: none is used or saved,
  and the running sessions that carry one are ended; what is saved stays
  and works again when the switch returns.
- **The switch on trusting** — "people may trust a tile that isn't only
  theirs", allowed by default (`PUT /agent/signins/policy {trust}`). Off:
  a saved sign-in is used only on tiles that are their person's alone;
  the card says a workspace admin can change that, and sessions that ran
  on a trust are ended.
- **The host** sets and locks either with `"agentSignIns": true | false`
  and `"agentSignInTrust": true | false` in the deployment file
  ([llm.md](/docs/llm.md) → "The host's file").
- **Admins see that, not what**: who has sign-ins, for which coding agent,
  of which kind, made and last used when — not their names, never a
  secret — and can revoke one. The person is told who did.
- **Backups** carry `data/signins/` as they carry every vault file: sealed,
  and restorable only with the same vault.
- An xbind older than this feature ignores the directory.
