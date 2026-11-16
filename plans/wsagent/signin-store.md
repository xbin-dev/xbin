# Coding-agent sign-ins held by xbind

> Status: live

The owner (2026-11-16, on `plans/workspace-agent.md` §5.7): "That's for
agents like Claude Code? Would say do now, very important to UX." The
decision and its reasons are D230 in `plans/DECISIONS.md`; the contract,
once built, is `docs/agent-signins.md` and `docs/protocol/signins.md`. This
file is the spec the implementation follows: every route, field and
setting, the rule for shared tiles, and who could read a credential at each
step. It builds on D178 (the guided sign-in in the Agent tab) and D179 (the
agent template's saved sign-ins), and lifts D179's saved sign-ins into
xbind.

## 1. What a person gets

A person signs a coding agent in **once**. xbind keeps the sign-in — a
long-lived token the CLI's own flow printed, or an API key the person
pasted — sealed under the vault barrier, and puts it into the coding-agent
sessions that person starts, on every tile where the shared-tile rule (§5)
allows it: ordinary tiles, partitioned tiles and VM sandboxes alike. A
login a person made in a terminal (`claude auth login`, `codex login`: it
lives in their `$HOME`) keeps working exactly as before and is used when no
saved sign-in goes in.

Harnesses that accept a custom endpoint keep running on the workspace's
LLM connector with no sign-in (D226); nothing here changes who serves a
session (`own` / `connector` / `own-first`, D229) — a saved sign-in *is*
the person's own sign-in for that question.

| Harness | Guided sign-in (xbind runs it) | Pasted credential | How it reaches a session |
|---|---|---|---|
| Claude Code | `claude setup-token` — a one-year token for model requests only | `sk-ant-oat…` token, or an Anthropic API key | `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` in the adapter's environment |
| Codex | none: `codex login` keeps a refresh token in a file, which is never copied (D179) — the subscription login stays the tile's own | OpenAI API key | the key in the adapter's environment and handed over through ACP `authenticate`; Codex writes its `auth.json` into a private `CODEX_HOME` of that session |
| Gemini CLI | none | Gemini API key | `GEMINI_API_KEY`, and `authenticate` when the adapter refuses its session |
| OpenCode | none | a provider key (Anthropic, OpenRouter, OpenAI, Google, Groq, xAI) | that provider's variable |

The plan asked for "`claude setup-token` and its equivalents". There are no
equivalents today: only Claude Code mints a credential that can be held
(D179 found the same). For the others the guided flow would have to copy
the CLI's credential file, which this design never does.

## 2. The store

`internal/signins`. One file per person, `data/signins/<hex(user id)>.json`
(0600, under `data/`, which no sandbox mounts):

```
{ "v": 1, "user": "alice", "account": <the account's creation stamp>,
  "signins": [ { "id": "si_…", "harness": "claude", "name": "Personal",
                 "kind": "setup-token" | "api-key", "env": "CLAUDE_CODE_OAUTH_TOKEN",
                 "createdAt": ms, "lastUsedAt": ms, "expiresAt": ms,
                 "default": true, "refusedAt": ms } ],
  "enc": 1, "secrets": <sealed: {id: secret}> }
```

- **Sealed like the connector's keys** (`internal/llm/keys.go`): the secrets
  map is sealed whole under a barrier subkey of the person's own
  (`signins/<user id>`). The four vault states behave as the connector's:
  open — read and written; sealed — nothing readable, so nothing is
  injected and nothing can be saved until an admin unseals; locked (no
  barrier set up) — saving is refused with how to set the vault up;
  plaintext (`--insecure-vault`, `--no-auth`) — kept in the clear, 0600,
  like every vault file in that mode, and sealed on the first read after a
  barrier exists.
- **Metadata is in the clear**, so the list, the admin's counts and "the
  vault is sealed" all work while the vault is sealed. Metadata is: id,
  harness, name, kind, the variable it goes to, created, last used,
  expires (a setup-token: created + 365 days; unknown for a key), default,
  refused. `account` (the label a harness reports) is reserved: no
  harness's mint reports one today, so it is never written.
- **A read never returns a secret**, nor any part of one (no "last four").
  There is no route, event, log line or audit row that carries one. The
  only way out is `Store.Use`, called by xbind's own session start.
- **Names**: one line, up to 40 characters, unique per person and harness
  (case-insensitive); none given → the harness's name, then "‹name› 2"….
  **One default per harness**: the first saved; removing the default makes
  the newest remaining one the default. At most 8 per person and harness.
- **The account stamp** binds the file to the account that made it: an
  account deleted and made again under the same id reads none of it.
- **Use log**: `data/signins/<hex>.use.jsonl` (0600), one row per
  injection — time, sign-in id and name, harness, tile, session id, origin
  (`person` | `agent`); never a secret. Kept to the newest 500 rows. The
  workspace's audit log gets the same as an `audit` line
  (`signin.use`).

Availability: the store needs sandboxes (`--isolate`). Without isolation
every terminal runs as xbind's own user and could read any process's
environment, so the routes answer `available: false` with that reason —
except under `--dev` / `--no-auth`, a development workspace, where it
works and says so (`unisolated: true`), the same exception delegation has
(D228).

## 3. Making a sign-in

**Guided** (`sdk/acp/guided.go` is the driver; xbind hosts it): xbind runs
the harness's `Mint` (`claude setup-token`) itself, server-side, in a
**throwaway confined sandbox** (`internal/confine`): the base image, no
tile directory, no home, no layer, an empty throwaway `HOME`, internet
egress only, a terminal 1000 columns wide (Ink needs one; the link and the
token are one line each), an environment of `PATH`, `HOME`, `TERM`, `LANG`
alone. Nobody can enter that sandbox and nothing a tile wrote is in it —
stricter than D179, which had to mint inside the person's own sandbox.
Without isolation (`--dev`) the same command runs as a host process with a
throwaway `HOME` and `env -i`.

1. `POST /agent/signins/guided {harness, name?, default?}` → 202
   `{flow: {id, harness, url, paste, expiresAt}}` once the CLI printed its
   link (30 s bound). `url` passed `Signin.Allowed` (https, the harness's
   exact hosts, the pinned path).
2. The person opens the link in their own browser, signs in there, and
   gets a code.
3. `POST /agent/signins/guided/{flow} {code}` → xbind types the code;
   the CLI's words decide: a token → saved (201 `{signin}`); "Invalid
   code" → 409 with the flow still open; a refusal → 502 with the CLI's
   reason line, token-shaped text masked; no word in 60 s → 504. A mint's
   last line is never shown (a token in a format the scan doesn't know
   would be that line: D179 L7).
4. `DELETE /agent/signins/guided/{flow}` cancels. A flow ends by itself
   after 15 minutes; one per person and harness; a flow belongs to the
   person who started it and no one else can address it. The sandbox is
   killed and its output buffer zeroed at every end.

**Pasted**: `POST /agent/signins {harness, secret, name?, default?}`. The
value is trimmed and must be one line of printable ASCII without quotes,
backslashes, `<`, `>`, `&` or spaces (D179's `cleanSecret`), at most 4096
bytes; `Provider.KeyFor` picks the variable by prefix. Nothing validates it
against the vendor (no real provider is ever called by xbind); a wrong key
shows as "refused" the first time a session uses it.

## 4. Putting it into a session

At an agent session's start (`term.createAgent`), after the gates that
exist today, xbind asks the store for the person's default sign-in for the
session's harness (or the one the start names: `signin: "<id>"`; `signin:
"none"` asks for none). If the rule of §5 allows it:

- the secret travels in the **spawn frame** to the agent host (PID 1 of
  the session's sandbox) and is set in the **adapter's environment only**
  — not in the sandbox spec, not in PID 1's environment, not in a terminal
  the person opens on the tile (the same path the LLM connector's key
  takes, D226);
- a credential variable of the same harness already in the environment is
  dropped first; the saved sign-in outranks a login in `$HOME` (the CLIs'
  own precedence for Claude Code; the owner's ruling in D179);
- **Codex** keeps a key only after ACP `authenticate`, which makes it
  write `auth.json`. The agent host gives that session a private
  `CODEX_HOME` — a directory in the sandbox's own `/tmp` (a tmpfs that
  dies with the sandbox), holding links to everything in the person's
  `~/.codex` except `auth.json` — so the key file is never in the shared
  `$HOME`; the host removes the directory when the adapter exits (without
  isolation the daemon makes and removes it, as it does a session's
  attachments). If the
  person's `~/.codex/auth.json` exists (Codex signed in on its own on this
  home), the saved key is **not** used and the session says so: Codex
  would otherwise start signed in by the file and the chip would name the
  wrong account (D179 M2);
- the session **says which sign-in it runs on**: its directory row and its
  first notice carry `signin: {source, id?, name?, why?}` with `source`
  one of `saved` ("using your saved sign-in ‹name›"), `home` ("using this
  tile's own login"), `connector`, `none`; `why` is the reason a saved
  sign-in was not used;
- with a credential of xbind's in the environment, the adapter's own
  status probe doesn't count as "signed out" (claude-agent-acp reports an
  environment token as `none`, D179): only the adapter's refusal does
  (`acp.AuthState`). A refusal marks the sign-in `refusedAt`, and the
  sign-in card says "your saved sign-in was refused — sign in again";
- what xbind keeps of the session is **redacted**: the session's exact
  secret, wherever it appears in a frame from the agent host, becomes
  `[saved sign-in]` before the event is logged, published, written to
  history or shown to anyone (a tool that printed the environment); the
  stderr log too.

Precedence, said in the docs and drawn in the UI:

1. the session's own choice (`signin: "<id>" | "none"`);
2. the person's saved default for the harness, where §5 allows it;
3. the login in the session's `$HOME` (the tile's own login);
4. the LLM connector, where the credential rule lets it serve (D229).

A rule that gives the connector alone (`connector-only`, an org's model
rule) still wins over all of it: no sign-in is injected into a session
only the connector may serve.

## 5. The shared-tile rule

What is true of a session's sandbox (checked in `internal/term`):

- every session is a sandbox of its own — own PID, mount and network
  namespaces, own `/tmp` — and the adapter's environment is readable only
  from inside it;
- but what *runs* inside comes from three places: the tile's persistent
  environment layer (the binaries — shared by everyone who may open a
  terminal on the tile, one per person on a partitioned tile), the tile's
  directory (the project's own configuration: hooks, MCP servers,
  instructions, scripts the agent is asked to run — written by everyone
  with terminal access), and the person's `$HOME`;
- an admin may drive another person's session on a tile that isn't
  partitioned.

So a co-user of a shared tile cannot *read* another person's session, but
can change what that session runs — a hook in `.claude/settings.json`, a
Makefile target, a script the agent is asked to run — and so reach its
environment. The rule, enforced in `internal/term/signin.go`
(`signinPlanFor` / `signinWhy`; the UI only repeats its answer):

A saved sign-in is injected only when **all** hold:

1. **The session is the person's own**: opened by their own signed-in
   session, app or device. Never a view-as session, never a component's
   credentials (a tile's backend, frame, cron, an agent session's own
   token), never the root token, never another person. A session the
   person's **workspace agent** starts (D228) gets one only when the
   person approved that start and the approval named the sign-in — never
   by default, never on a start their preference lets through unasked
   (below).
2. **Sandboxes are on** (or the workspace is a development one, §2).
3. **Nobody else can change the tile's code**: no other person who is not
   a workspace admin holds **write level or more** on the tile, by any
   source — ownership, an entry of their own, a pattern, an org's share
   (`Broker.SigninOthers`, the one real answer; table-tested). The tile is
   the **registered tile the session's directory lies in** (the
   registry's resolver), never the directory as written: a session in
   `apps/crm/src` is judged on `apps/crm`, and a directory in no tile gets
   nothing. **Partitioned tiles are not exempt**: a partition isolates
   data, and the tile's directory is bound read-write into every
   partition's sessions. Workspace admins are not counted: they can
   already change any tile's code, grants and the workspace itself, and
   are in the trust base (D179 N20) — the table in §9 says what that
   leaves.
4. **The connector is not the only server allowed** (§4).
5. The store can be read (vault open), the workspace policy allows saved
   sign-ins (§7), the tile's orgs allow the harness (D229, already checked
   by the start), the sign-in isn't expired.

Where 3 fails the session starts **without** the saved sign-in and says
why — "Your saved sign-in Personal isn't used here: bob, carol and 1 more
can also change apps/crm and could change what your agent runs there" —
and offers what works: the LLM connector where it can serve, or the
tile's own login. The same reason is in `GET /agent/providers?tile=`
before the session starts and on the brief's card.

**The workspace agent.** It steers a session it started and reads what
the session prints (`tile_agent_result`), so a sign-in in such a session
is, in effect, within its model's reach. A start's preview therefore says
it in xbind's words: `preview.session` / a plan line's `start` carry
`signIn: "saved"` and `signInId`; xbin's chrome (never the agent's page)
draws "Uses your saved Claude Code sign-in ‹label›. The tile's coding
agent will hold that sign-in: it, and anything it runs in ‹tile›, can read
it", the label read from the person's own list; the ticket binds the id
with the harness and the mode (`DelegateStart.Signin`), and the open uses
exactly that sign-in or is refused. The agent's model is told that a
saved sign-in exists (`start-defaults` `signIn: "saved"`, only for a
start that needs approval) — never its label.

And a session that carries a saved sign-in is **its person's alone**:
`MayDrive`, the reattach rule and the event, log and diff reads refuse
everyone else, admins included (they may still end it) — as on a
partitioned tile (PD-09). Without this an admin would prompt the session to
print its environment. The mark is sticky (`sessionSignin.held`): it
outlives the sign-in being taken away mid-run, goes into the session's
history entry (`HistoryMeta.Signin`: view-as reads neither transcript nor
first prompt, 403), and a session that resumes such a transcript is one
too.

The rule is evaluated at the session's start, and a **restart never
widens it**: the session keeps the choice it was opened with (none stays
none; a named sign-in stays that one or becomes none; an agent-started
session keeps exactly what it carried). If someone becomes able to change
the tile while a session with a saved sign-in runs — or saved sign-ins
are switched off — the session is ended with a notice (the broker's
`OnPeopleChanged` and the policy route call `Term.SigninSweep`; a host
file that turns them off is noticed at the next judgement); the next
start is judged afresh.

**What the rule costs** (measured on the UI harness's seed, where most
tiles belong to an org whose members hold terminal level): a saved sign-in
is used on the tiles a person alone can change (others may hold read
level) and in a one-person workspace — and on none of a team's shared tiles, where the
card leads with the tile's own sign-in instead. A per-tile "use it here
anyway" is the obvious next lever; it is the person's informed choice and
the owner's call, and is not built.

## 6. The consumer interface (designed; not built in this change)

The plan: "AgTT's partition asks xbind for that person's sign-in instead
of keeping its own … hands a credential only to a session launch xbind
itself performs or mediates — a component never receives the secret as
data."

Checked against the code, the last clause cannot hold as written. An agent
template instance runs its coding agents in sandboxes of a sandbox
manager, through that manager's exec route; the instance chooses the argv
and can read the sandbox's files and processes. Whatever xbind puts into
such a launch, the instance's code can read back. So a component can be
kept from receiving the secret *in an answer*, but not from reaching it —
the person must have chosen to trust that component with it, as they do
today when they paste a key into the instance (D179).

The interface that follows from that:

- **consent per component**: a person allows one tile to use one harness's
  saved sign-in (`PUT /agent/signins/consumers/{tile} {harnesses}`, a
  person's own act; listed and revocable in the sign-in UI). Nothing is
  granted by a manifest, a binding or an admin;
- **a grant, not a secret**: the person's partition of that tile calls
  `POST /agent/signins/grants {harness}` → `{grant: "xsg_…", env,
  name, kind, expiresAt}`. The grant is opaque, single-use, two minutes,
  bound to person, tile, harness and sign-in;
- **xbind redeems it at a launch it performs**: the tile-sandbox runtime
  (`internal/tilesbx`, where every manager's exec ends) replaces an
  environment value `xbin-signin:<grant>` with the secret when it starts
  the process, for the catalog's own adapter argv only, and records the
  use. The manager and the instance forward the reference unchanged;
- the instance keeps its own store (D179) as the fallback and for the
  sign-ins that already exist; nothing is migrated, an existing instance
  behaves exactly as before, and the D179 gates (a sandbox of the
  person's own that no one else uses, no hosted conversation) stay the
  instance's.

Not built here because it changes three components (the store, the tile
sandbox runtime, the template) and needs its own review; the store's
`Use` already takes the consumer as a parameter.

## 7. Policy and admin

- **Workspace switch** "people may save coding-agent sign-ins" —
  `GET`/`PUT /agent/signins/policy {enabled}`, workspace admins; default
  on; kept in `data/signins/policy.json`. Off: nothing is injected,
  nothing can be saved or made default; what is saved stays listed (and
  removable) and works again when the switch returns.
- **The host's file** (`XBIN_LLM_CONFIG`) gains a sibling of
  `workspaceAgent`: `"agentSignIns": true | false`. Set, it is the host's:
  the policy reads `{enabled, locked: true, reason: "set by your host"}`
  and a write is refused 409.
- **Orgs**: an org's harness list (D229) is checked by the session start
  before any of this; a harness an org doesn't allow gets no session, so
  no sign-in.
- **Admins see that, not what**: `GET /agent/signins/people` →
  per person, per sign-in: id, harness, kind, created, last used,
  expires, default — no name (a person's own label), no secret. `DELETE
  /agent/signins/people/{user}/{id}` revokes one; the person sees
  "removed by ‹admin›" in their list's notice.

## 8. Lifecycle

- **A person deleted**: their file and use log are deleted at once
  (`OnUserSignedOut(deleted)`); an account made later under the id reads
  nothing (the account stamp).
- **Disabled, or signed out everywhere by an admin**: `Use` answers no for
  a disabled account; their running agent sessions that carry a saved
  sign-in are ended.
- **A credential someone else made for the account** (an admin sets their
  password, mints a sign-in link, binds an SSO email): the person's saved
  sign-ins are **removed** and the list says so ("removed when ‹admin›
  reset your sign-in — sign in again"). Otherwise a reset would be a way
  for an admin to run sessions with the person's sign-ins.
- **The vault sealed or lost**: nothing can be read; nothing is injected;
  the list says "the workspace vault is sealed". A vault re-initialised
  with another key can't open the old secrets: those sign-ins read
  "can't be opened with this vault" and can only be removed.
- **A sign-in removed** (by its person or an admin): not used by any later
  start. A session already running keeps the copy in its adapter's
  environment until it ends — xbind **ends** the running sessions that
  carry it, with a notice ("your saved sign-in was removed"); the
  transcript stays and the session resumes on whatever the next start
  finds. The credential itself stays valid at its vendor until it expires
  or the person revokes it there; the UI says so at Remove.
- **Expiry**: a setup-token's `expiresAt` is shown from 30 days before;
  an expired one is not injected.
- **Backups**: the files are under `data/` and travel with a workspace
  backup as every vault file does — sealed; a restore needs the same
  vault. In the plaintext vault mode they are in the clear in the backup,
  like the rest.
- **A downgraded xbind** ignores `data/signins/` (it knows no such
  directory): nothing is injected, logins in `$HOME` work as before, and
  an upgrade finds the files as they were.

## 9. Who could read a credential, step by step

| Step | Other people on a shared tile | The tile's code | A tile agent's model (tool output) | The workspace agent's model | Logs | Backups | A downgraded xbind |
|---|---|---|---|---|---|---|---|
| Guided flow: link shown | no: the flow answers its starter only | no: the throwaway sandbox binds no tile | no | no: `/agent/signins*` is refused to delegates | the URL is not logged | — | — |
| Guided flow: token printed | no: nobody can enter the throwaway sandbox | no | no | no | never logged; the buffer is zeroed | — | — |
| Pasted key in the request | no | no: person-only route, refused to tile credentials | no | no | request bodies are not logged | — | — |
| At rest | no | no: `data/` is mounted in no sandbox | no | no | — | sealed (plaintext mode: in the clear) | not read |
| Read routes | metadata of their own only | refused | — | refused | — | — | — |
| Admin routes | — | refused | — | refused | — | — | — |
| Spawn frame → adapter | no: own sandbox; not injected on a tile anyone else (non-admin) can change | **yes** — residual 1 | **yes**: it can run a command that prints it — residual 2 | **only for a start the person approved with the sign-in named — then yes**, through the session it steers — residual 7 | masked in the session log | history is masked | — |
| Codex's `auth.json` | no: private `CODEX_HOME` in the session's `/tmp` | yes, as above | yes, as above | no | — | not in `$HOME`, so not in a backup of homes | — |
| Session events, history | the person only (admins and view-as barred, live and in history) | — | — | masked on the joined stream (value with separators, base64, hex, %XX; credential shapes) — lowers accidental exposure, **not a guarantee** — residual 7 | masked, the same way | masked, the same way | reads masked history |
| The harness's own transcripts in `$HOME` (`~/.claude/projects`, `~/.codex/sessions`) | no | **a session of the person's on another tile can read them** — residual 9 | yes | through a session it steers | — | **unmasked**, in a backup of homes | — |
| Use log, audit | the person; admins see metadata | no | no | no | ids and names only | no secret | — |

Residual risks, stated:

1. **The tile's code and the person's own session.** Anything that runs in
   the session's sandbox — the agent's tools, the project's hooks and
   scripts — can read the adapter's environment. On a tile only the
   person (and admins) can change, that code is theirs or an
   admin's; a tile imported from elsewhere can carry a hook. This is the
   same exposure the `$HOME` login has always had, with a narrower
   credential (a setup-token makes model requests only).
2. **The harness's own model can read the credential**: it can run a
   command that prints its environment and read the output. It is the
   vendor the credential belongs to, except when a harness is pointed at
   another provider (OpenCode with several keys).
3. **Workspace admins** can change a tile's layer or code before a person
   starts a session there. They cannot read the store, drive the session,
   or keep the sign-ins across a credential reset.
4. **The host's operator** (root on the machine) can read xbind's memory
   and, in the plaintext vault mode, the files.
5. **The `$HOME` login is unchanged** and more exposed than a saved
   sign-in on a shared tile: it is a full login with a refresh token,
   readable by the same code. Narrowing `$HOME` for sessions is a
   follow-up (`plans/workspace-agent.md` §4).
6. **A running session** keeps its copy until xbind ends it; a vendor-side
   revoke is the person's.
7. **Redaction is not a guarantee, and the workspace agent can get the
   credential of a session it was approved to start with one.** xbind
   masks the value on the joined text of each stream — with anything
   between its characters, as base64 (any offset), hex or `%XX` — and
   the harnesses' credential shapes (`signinredact.go`). That catches
   accidents: a tool that printed its environment, `env | base64`. A
   session that is *asked* to hand the credential over can do it in a
   form nothing recognises (reversed, in words, across interleaved
   streams). So the boundary is the approval, not the mask: the sheet
   says the tile's coding agent will hold the sign-in, and nothing
   starts with one unasked.
8. **A team's shared tiles get nothing** from the store (§5), so people
   there keep using the `$HOME` login — the broader credential.
9. **One `$HOME` for all of a person's tiles, and the harnesses keep
   their own transcripts in it**, unmasked — Claude Code's
   `~/.claude/projects/*.jsonl`, Codex's `sessions/` and `log/` (linked
   into the private `CODEX_HOME` on purpose). If a tool printed the
   credential in a session on a tile the person alone can change, a
   session of theirs on a shared tile — which got no sign-in, but runs
   the co-users' hooks — can read it from those files. A per-session
   private state directory would close this and was not built: session
   resume (`session/load`, D75) reads exactly those files, and the
   person's settings, MCP servers and memory live beside them, so every
   sign-in session would start as a first run and never resume. The
   person's mitigation: don't have the agent print its environment; if it
   did, remove the sign-in, revoke it at the vendor and delete that
   transcript. Narrowing `$HOME` per tile (`plans/workspace-agent.md` §4)
   is the real fix.

## 10. Routes, fields, settings

All under `/api/xbin`. "Person" = a person's own session, app or device —
never a tile's credentials, a terminal or agent-session token, view-as,
the root token, or a delegate (`PersonOnly`; delegation class `never`).

| Route | Who | What |
|---|---|---|
| `GET /agent/signins[?tile=]` | person | `{available, why?, unisolated?, vault, policy: {enabled, locked?, reason?}, notice?, admin?, harnesses: [{id, name, guided, keys: [{env, label, kind}], login, max, signins: [meta], here?: {inject, why?}}], flows: [{id, harness, url, paste, expiresAt}]}` — `here`: the rule's answer for that tile, whether or not a sign-in is saved yet |
| `POST /agent/signins` | person | paste: `{harness, secret, name?, default?}` → 201 `{signin}` |
| `PUT /agent/signins/{id}` | person | `{name?, default?: true, secret?}` → `{signin}` |
| `DELETE /agent/signins/{id}` | person | → `{ok, ended}` (sessions ended) |
| `DELETE /agent/signins/notice` | person | the notice was read |
| `POST /agent/signins/guided` | person | `{harness, name?, default?}` → 202 `{flow}` |
| `POST /agent/signins/guided/{flow}` | person | `{code}` → 201 `{signin}` · 409 · 502 · 504 |
| `DELETE /agent/signins/guided/{flow}` | person | cancel |
| `GET /agent/signins/uses` | person | the use log, newest first |
| `PUT /agent/signins/policy` | workspace admin | `{enabled}` → `{policy, ended}` (off ends the sessions running on one); 409 when the host holds it |
| `GET /agent/signins/people` | workspace admin | counts and metadata per person |
| `DELETE /agent/signins/people/{user}/{id}` | workspace admin | revoke one |

Additive fields elsewhere:

- `GET /agent/providers[?cwd=]`: a provider the caller has a default
  sign-in for gains `saved: {id, name, kind, expiresAt?, inject, why?}`
  (`inject`: a session of theirs on that tile would get it; `why`: why
  not), and every provider xbind keeps sign-ins for `savable: true`.
- `POST /term/sessions`: `signin` (`"<id>"` | `"none"`).
- A session's row (`GET /term/sessions`, `term` events) and its first
  notice: `signin: {source, id?, name?, why?}`.
- `GET /delegate/start-defaults`: each harness gains `signIn`
  (`saved` | `own` | `connector` | `needed`) — no name, no id;
  the default harness prefers one that runs as things are, a saved
  sign-in included. A start's preview (`preview.session`, a plan line's
  `start`) carries `signIn` and `signInId`, which the ticket binds.
- The agent's page and the chrome: `xbin:agent-open-signin {harness}` (page
  → chrome), `xbin:agent-signins` (chrome → page).
- No hub event was added: the element reads the list again after its own
  changes, and a session's row changing is the existing `term` event.
- The host's file: `"agentSignIns": bool`.
- `sdk/acp`: `guided.go` (`Guided`, `GuidedExec`, `CleanCode`,
  `MaskCredentials`, `MintCommand`), `Config.Credential` (`Credential`:
  the key an adapter wants through `authenticate`, and where its refusal
  goes), `Provider.KeyHome`, `APIKeyMeta`.
- `internal/confine`: `StartTTY` — Run's sandbox, interactive, on a
  terminal.

## 11. The sign-in UI

One element, `<bx-agent-signin>` (`/vendor/agent-signin.js`, the same URL
as D178's; its old properties keep working), with three faces:

- **the card** (`harness`, `component`) — where a session says it is
  signed out, on a brief or plan card that needs a sign-in, in the
  launcher: "Sign in to Claude Code" → the link (Open sign-in page ↗, Copy
  link), the code field, Finish → "Saved as ‹name› — used for your coding
  agents on every tile where only you can open a terminal". Beside it:
  "Paste a key instead", and "Sign in on this tile only" (D178's terminal
  flow, the `$HOME` login);
- **the list** (`view="manage"`) — per harness: name, kind, created, last
  used, expires, default; Make default, Rename, Remove; Add; where it was
  used; for admins the workspace switch and the per-person table with
  Revoke;
- **the chip** — in the Agent tab's header: what the session runs on, and
  why a saved sign-in wasn't used.

Phone width, light and dark, keyboard-first (the code field takes the
focus when the link is up; Enter finishes; Escape cancels), errors say
what to do. iOS: the Agent screen and the workspace agent's cards read the
same fields and say "needs your sign-in — set it up on the web"; a saved
sign-in is used transparently; a native guided flow is not in scope.

## 12. Order of work and state

1. store + API + tests;
2. the guided driver (`sdk/acp/guided.go`) and xbind's host for it, end to
   end against a scripted `claude`;
3. injection at session start, the shared-tile rule, redaction, the
   session's `signin`;
4. the element in the Agent tab;
5. `/agent/providers`, `/delegate/start-defaults`, the cards;
6. Codex (the pasted key, `authenticate`, the private `CODEX_HOME`),
   Gemini, OpenCode;
7. policy, admin, lifecycle;
8. docs and changelog.

**State (2026-11-16).** Built: 1–5, 7, 8, and of 6 Codex's and Gemini's
key through `authenticate` with Codex's private `CODEX_HOME` and
OpenCode's provider key from the environment — all against the scripted
agent, none against a real CLI. In the workspace agent: the brief's card,
the approval sheet and a plan's start line say what the session signs in
with and offer the chrome's card.

Not built in this change, each said where it would be expected: the
consumer interface (§6) — an agent-template instance keeps its own store
exactly as before; a native guided flow on iOS (the app says "set it up on
the web" and uses a saved sign-in like any client); `bx` commands for the
store; moving the agent template's `harness_guided.go` onto
`sdk/acp/guided.go` (the driver is written so it can); a per-tile opt-in
for shared tiles (§5).

Not verified: a real `claude setup-token` (its success lines are as
captured for D179, where the capture ends at the code prompt), real
Codex, Gemini and OpenCode adapters with a saved key, a VM sandbox with a
saved sign-in (the path is the namespace sandbox's: the spawn frame), the
dock's sign-in overlay and the approval sheet's card in a browser.
