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
2. your saved default for the coding agent, where the rule allows it;
3. the coding agent's own login in the session's `$HOME` — the tile's own
   login;
4. the workspace's LLM connector, where it can serve.

Where a rule gives the connector alone (the workspace's `connector-only`,
an org's model rule), or you chose the connector, no sign-in goes in. The
session says which it is — "Using your saved sign-in Personal", or why
not — in its first lines and in the Agent tab's header.

### The rule for shared tiles

Every session is a sandbox of its own, so nobody else can read what your
coding agent holds. But what *runs* in that sandbox comes from places
other people may write: the tile's directory — its code, and the project
settings a coding agent loads (hooks, MCP servers, a Makefile or script
it is asked to run) — and the tile's environment layer, which holds the
programs. Someone who can change the tile could change what your agent
runs, and so reach a credential in your session.

So xbind puts a saved sign-in into a session only when **all** of these
hold, and decides it itself at every start:

1. **It is your own session**: opened by your signed-in browser, app or
   device. Never by an admin viewing as you, a tile's own credentials (a
   frame, a backend, a terminal's token), or the root token. A session
   **your workspace agent** starts gets one only when you approved that
   start and the approval named the sign-in (below).
2. **Sandboxes are on.**
3. **Nobody else can change the tile's code**: no other person who isn't
   a workspace admin holds write level or more on the tile — by owning
   it, an entry of their own, a pattern, or an org it is shared with.
   The tile is the registered tile the session's directory lies in: a
   session opened in `apps/crm/src` is judged on `apps/crm`. **A
   partitioned tile is no exception**: a partition keeps people's data
   apart, and the tile's directory is the same code in every partition.
4. The connector isn't the only one allowed to serve, and you didn't
   choose it.

Otherwise the session starts without it and says why — "Your saved
sign-in Personal isn't used here: bob can also change apps/crm and could
change what your agent runs there" — and runs on the tile's own login or
the connector.

**Your workspace agent.** It steers a session it starts and reads what
the session prints, so it never gets a saved sign-in by default, and
never on a start your "start without asking" preference lets through. The
approval of a start says it in xbin's own words — "Uses your saved Claude
Code sign-in Personal. The tile's coding agent will hold that sign-in: it,
and anything it runs in ‹tile›, can read it" — and approving is what puts
that sign-in, and no other, into that session. Your agent is told only
that a saved sign-in exists, not its name.

Workspace admins are not counted as "other people" in 3: an admin can
change any tile, its grants and the workspace itself. What an admin
cannot do is listed under "Who can read it" below.

A session that carries a saved sign-in is **yours alone**: no one else —
admins included — can prompt it, answer its questions or read its
transcript, as on a partitioned tile; nor its transcript in your history
once it ended (an admin viewing as you sees the entry, not what was said:
403). An admin can still end it.

A restart keeps what the session was started with and never adds to it:
one started without a saved sign-in stays without, one your agent started
keeps exactly what your approval put in.

### When it stops being used

| What happens | Then |
|---|---|
| You remove the sign-in, or replace its secret | later starts don't have it; the sessions running on it are **ended** (they hold a copy), with a line saying why. The credential stays valid at its vendor until it expires or you revoke it there. |
| An admin revokes it | the same, and your list says who |
| Someone else becomes able to change the tile (write level or more) | the session running there on your sign-in is ended; the next start is judged again |
| An admin switches saved sign-ins off (or the host does) | the sessions running on one are ended |
| An admin sets your password, mints you a sign-in link, or binds an SSO email to you | **all your saved sign-ins are removed**: otherwise resetting your account would be a way to run sessions on them. Your list says so; sign in again. |
| Your account is disabled, or signed out everywhere | no sign-in is used; sessions running on one are ended |
| Your account is deleted | your sign-ins and their use log are deleted at once; an account made later under the same id reads none of it |
| The vault is sealed | none can be used; sessions start without and say so |
| It expires (a setup-token: a year) | it is not used, and the list says so |

### Who can read it

| | |
|---|---|
| Other people on a shared tile | no: it is not put into a session there |
| A tile's own code (backend, frame) | no: `data/` is in no sandbox, the routes refuse tile credentials |
| **Code that runs in your own session** | **yes**: the coding agent, its tools, the project's hooks and scripts share its environment. On a tile only you (and admins) can change, that code is yours — but a tile you imported can carry a hook. The same was always true of a login in `$HOME`, which is the broader credential. |
| **The coding agent's model** | **yes**: it can run a command that prints the credential and read the answer. It is the credential's own vendor unless you pointed the agent elsewhere. |
| **Your workspace agent's model** | only for a session whose start you approved with the sign-in named — and then **it can**: it prompts that session and reads its results. Masking (next row) lowers the chance; it does not prevent a session that is asked to from handing the credential over in a form xbind doesn't recognise. |
| What xbind keeps of a session (events to pages, history, your agent's `tile_agent_result`, logs, backups of them) | masked, **not guaranteed**: xbind replaces the credential with `[saved sign-in]` on the joined text of each stream — as it is, with anything between its characters (spaces, newlines), as base64, hex or `%XX` — and masks anything shaped like a harness credential (`sk-…`, `AIza…`). That catches a tool that printed its environment. It cannot catch every encoding, and it is not a boundary: treat a session that holds your sign-in as able to disclose it. |
| **The coding agent's own files in your home** | **not masked**: Claude Code and Codex keep their own transcripts (`~/.claude/projects/`, `~/.codex/sessions/`) — xbind needs them there to resume a conversation — and your home is the same on every tile. If a tool printed the credential, it is in those files, where a session of yours on *another* tile (one other people can change, which got no sign-in) could read it. If that happened: remove the sign-in here, revoke it at its vendor, and delete that transcript. |
| A workspace admin | not through xbind: no route reads it, they can't drive your session, and a reset of your account removes the sign-ins. An admin can change a tile's programs or code before you start a session there. |
| Whoever runs the machine | yes (xbind's memory; the files, in the plaintext vault mode) |

Every use is logged for you (`GET /agent/signins/uses`: when, which
sign-in, which tile and session, whether you or your agent started it) and
in xbind's audit log — never the secret.

## Where you meet it

One element, `<bx-agent-signin>`, in xbin's own chrome:

- **The Agent tab.** A coding agent that says it is signed out shows the
  card: **Sign in** runs the guided sign-in (a link, a code, Finish — the
  code field has the focus, Enter finishes, Escape cancels), **Paste a key
  instead**, and **Sign in on this tile only** — the CLI's own sign-in,
  whose login stays in your home on this workspace. After a sign-in is
  saved the agent restarts on it and its conversation resumes where the
  coding agent can reopen it. On a tile where a saved sign-in would not be
  used the card says so and signs in on the tile first. The tab's status
  line says what a session signs in with ("your saved sign-in: Personal",
  "this tile's own login", "saved sign-in not used" with the reason), and
  **Sign-ins…** opens the list.
- **The list**: per coding agent, each sign-in's name, kind, when it was
  saved and last used, and when it expires; Make default, Rename, Remove
  (which says what it does first), Add; where they were used; for
  workspace admins the switch and who has sign-ins.
- **Your workspace agent.** A brief or a plan line whose coding agent
  would start signed out says "Needs your Claude Code sign-in" with a
  button. The agent's page only names the coding agent
  (`xbin:agent-open-signin`): the card is drawn by xbin's chrome, so no
  link, code or key passes through the agent or its page. The approval
  sheet and a plan's start line say what the session will sign in with.
- **The app (iOS)** uses a saved sign-in like every other client — it is
  xbind that puts it into the session — and shows what a session signs in
  with; saving one is done on the web.

## Admins and hosts

- **The workspace's switch** — "people may save coding-agent sign-ins", on
  by default (`PUT /agent/signins/policy`). Off: none is used or saved,
  and the running sessions that carry one are ended; what is saved stays
  and works again when the switch returns.
- **The host** sets and locks it with `"agentSignIns": true | false` in
  the deployment file ([llm.md](/docs/llm.md) → "The host's file").
- **Admins see that, not what**: who has sign-ins, for which coding agent,
  of which kind, made and last used when — not their names, never a
  secret — and can revoke one. The person is told who did.
- **Backups** carry `data/signins/` as they carry every vault file: sealed,
  and restorable only with the same vault.
- An xbind older than this feature ignores the directory.
