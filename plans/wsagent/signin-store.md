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

## 5. The rule: used by default, trust asked where the source isn't only the person's

> Changed 2026-11-17. The first rule here ("a saved sign-in goes in only
> where nobody else who isn't an admin can change the tile's code",
> reviewed and released in v0.3.80) was replaced on the owner's word:
> "maybe let's default sign-in, AND on tiles which were created by
> different users have a 'I trust this tile source' with a small risks
> explanation." The owner **accepted the risk model** of this section and
> §9 on 2026-11-17. An independent review of the same day changed four
> things, all below: a trust is VOID when its basis is seen to change and
> stays void; an answer is bound to the basis it was shown; a session the
> workspace agent starts has a sign-in only by the person's tick on that
> start (never by default — the earlier "start without asking carries the
> default" is reverted); a workspace-owned tile needs no trust for a
> workspace admin.

What is true of a session's sandbox (checked in `internal/term`):

- every session is a sandbox of its own — own PID, mount and network
  namespaces, own `/tmp` — and the adapter's environment is readable only
  from inside it;
- but what *runs* inside comes from the tile's directory (the project's
  own configuration: hooks, MCP servers, instructions, scripts the agent
  is asked to run), the tile's persistent environment layer and the
  person's `$HOME`: **code in the tile runs with the sign-in**;
- an admin may drive another person's session on a tile that isn't
  partitioned.

The rule, enforced in `internal/term/signin.go` (`signinPlanFor` /
`signinJudge`; the UI only repeats its answer). A saved sign-in is
injected when **all** hold:

1. **The session is the person's own**: opened by their own signed-in
   session, app or device, or by their workspace agent for them (below).
   Never a view-as session, never a component's credentials (a tile's
   backend, frame, cron, an agent session's own token), never the root
   token, never another person.
2. **Sandboxes are on** (or the workspace is a development one, §2); the
   workspace policy allows saved sign-ins (§7); **the connector is not
   the only server allowed** and wasn't chosen (§4); the store can be
   read; the tile's orgs allow the harness (D229); the sign-in isn't
   expired.
3. **The tile's source is only the person's own — the default is on —
   or the person said they trust it.** A source is not only theirs under
   either of two conditions, kept apart in the code so one can go without
   the other:

   | | Condition | Where |
   |---|---|---|
   | 1 | **the tile is owned by someone else** — another person, an org, or the workspace (the owner's "created by different users": a tile belongs to whoever created it until it is handed on). **Not** a workspace-owned tile for a workspace admin — by their STORED role (`Source.Admin`), never the request's principal: admins can change every tile, theirs included; a demotion flips the answer and the sweep ends the session. Org tiles always ask (an org's other admins can give themselves write at any time); the "sole org admin" case was left asking | `broker.signinNotOwn`, `signins.Source.NotOwn` |
   | 2 | **someone else who is not a workspace admin can change its code** — write level or more, by any source (an entry, a pattern, an org's share); partitioned tiles included, whose directory is shared code | `broker.signinEditors`, `signins.Source.Editors`; **off with one line: `signins.CoEditorsNeedTrust = false`** (then `NeedsTrust` and a trust's basis ignore editors) |

   Condition 2 is the coordinator's addition to the owner's sentence: the
   risk is identical (a file a co-editor plants runs with the credential).
   The tile is the **registered tile the session's directory lies in**
   (`term.Manager.TileOf`), never the directory as written; a directory in
   no tile gets nothing. Workspace admins are not counted as editors
   (they are in the trust base, D179 N20); an admin who *owns* the tile is
   its owner like anyone (condition 1).

**Trust** (`internal/signins/trust.go`) is the person's own answer, "I
trust this tile's source":

- **per (person, tile identity)** — the directory's own identity
  (`broker.TileIdentity`: inode, birth time, generation — what a waiting
  plan line is pinned to) **at its path**. Kept in the person's sign-in
  file (`trust: [{id, tile, name, ident, owner, notOwn, editors, at, via,
  voidAt?, voidWhy?}]`), metadata, readable while the vault is sealed;
  removed with the account and by an admin-made credential (`Clear`).
- **for the tile as it stood, and VOID once that is seen to differ**
  (`Trust.against`, `Store.judge`): another identity at the path
  (re-made), another path (moved — a move is a change), another owner,
  the tile becoming someone else's, or an editor who was not one when it
  was given. Void is recorded (`voidAt`, `voidWhy`) and **permanent**:
  the tile back as it was — mallory gained write and lost it, the owner
  A→B→A — does not revive it; only a new answer does. An editor who
  *left* is dropped from the record when that is seen, so their return
  is someone new. Seen where: (a) **at the event** — `Store.Revalidate`
  from the broker's `OnPeopleChanged` (every users-plane mutation:
  accounts, roles, access entries and patterns, permission sets, orgs,
  their members and shares, owner transfers) and from the watcher's
  rescan when the set of (tile path, identity) changes (moved, removed,
  re-made) — with or without a session running; (b) **at every
  judgement** of the rule (`Store.Trusts`), the backstop for a change no
  event announced; (c) at the person's list (`Recheck`). Then
  `SigninSweep` ends the sessions that ran on it.
  `TestSigninTrustIsVoidedByEveryChangeOfItsBasis` (internal/boot)
  enumerates the ways through the routes an admin uses.
- **bound to what the person was shown** — `Source.Basis()`: a token over
  identity, path, owner, whose-it-is and the full editor list. The
  question carries it (`here.trust.basis`, `signInTrust.basis`); the
  answer returns it; another basis than the tile's now is 409 — nothing
  recorded, nothing approved or applied — and the card redraws the
  question and **re-arms** (`agent-armed.js`: a button is armed for its
  `data-armed` key; lit reuses the element, so a changed key waits
  again).
- **given only by the person**, in xbin's own chrome: `POST
  /agent/signins/trust {tile}` (person-only: never a tile's credentials,
  a delegate, view-as, the root token), the tick of an approval's trust
  line, the tick of a plan's start line. Never implied by an admin, a
  tile's owner, the workspace agent, or a page message —
  `xbin:agent-open-signin` opens the card, nothing answers it.
- **listed and revocable**: `GET /agent/signins` → `trusted` (with
  `holds` / `why`), `DELETE /agent/signins/trust/{id}` → the person's
  sessions there on a saved sign-in are ended.
- **forbiddable**: a workspace switch beside the sign-ins switch (§7),
  default allowed, host-lockable the same way. Off, nothing is trusted
  (kept answers aren't used) and the card says who can change that. An
  org-level switch was not built.

Where trust is needed and absent the session starts **without** the
sign-in and says why — "Your saved sign-in Personal isn't used here:
apps/crm isn't only yours — it belongs to bob, and carol can change it —
and you haven't said you trust its source" — and its row carries the
question (`signin.trust`), as do `GET /agent/providers` (`saved.trust`)
and `GET /agent/signins?tile=` (`here.trust`, with `without`: what a
session runs on instead).

**The card** (`<bx-agent-signin view="trust">`, the tile window's Agent
tab, before a first start and whenever a session ran without): whose tile
it is and who can change it (xbind's names, at most three and "N more");
"Trusting means your ‹harness› sign-in is used here and this tile's code
can change your home. People who can change this tile could read it and
use your ‹harness› account." (two things since D245: the sign-in, and the
home — `home-on-untrusted-tiles.md`); two buttons of the same size and look — "Trust this tile — use
my sign-in" (armed: 600 ms after it shows, the person's own click, nothing
over it, never Enter) and "Start without my sign-in" — and what the agent
runs on without. Neither is focused or pre-selected. Declining is
remembered per tile in the browser until the question changes.

**The workspace agent** (decision A of the review, 2026-11-17). It
directs a session it started — nothing refuses its prompts to a session
that carries a sign-in — and reads what the session prints
(`tile_agent_result`); redaction is best-effort and a file written in the
tile skips it. So **a session it starts never carries the person's
sign-in unless the person ticked it for that start, each time**:

- A start under **"start without asking"** has none, on any tile; no
  sub-preference changes that. `start-defaults` says what the session
  runs on (`connector` | `own` | `needed`), never `saved`, and prefers a
  harness the workspace's model serves — the owner's original decision
  for harness auth: connector by default, sign-ins held by xbind.
- A start **the person approves**: `preview.session` / a plan line's
  `start` carry `signIn` (what it runs on as it stands) and `signInId` —
  the sign-in the sheet OFFERS, a line of its own, **unticked**, warned,
  in xbind's words: "Use my ‹harness› sign-in ‹label› for this session —
  Your workspace agent directs this session: what it tells the coding
  agent could expose your sign-in." The tick rides the approval
  (`approve {signin: true}` → `Request.UseSignin`; a plan's apply
  `{signin: [n]}` → `ChangeItem.SigninUse`), the ticket then carries the
  id (`DelegateStart.Signin`) and the open uses exactly that sign-in or
  is refused.
- Where the tile needs trust the preview also carries **`signInTrust`**
  and the sheet the trust line under the first — unticked, tickable only
  with it and only once every editor has been shown. `{signin, trust}`
  records the person's trust (`via` approval/plan) before the open, for
  the basis the sheet showed (409 otherwise: nothing decided). The
  sign-in ticked without it: the session starts without
  (`DelegateStart.TrustAsked`); a sheet that carried no line, on a tile
  that asks by the open, is refused.
- A start on a tile **the same plan creates** as the person's own offers
  their default (`plannedSignIn`, `broker.SigninSourcePlanned`).

The agent learns that a saved sign-in exists (`start-defaults`
`signInOffered`, `GET /agent/providers` `saved: {inject: false}`) —
never its label, kind or id outside its own requests; the row and events
of a session it started name no sign-in and nobody's access.

**Remaining exposure of a ticked start**, stated: the agent can steer
that session — tell the coding agent to print its environment, or to
write the sign-in into a file in the tile, which no mask sees. The tick
is the person's informed consent to that for one session.

And a session that carries a saved sign-in is **its person's alone**:
`MayDrive`, the reattach rule and the event, log and diff reads refuse
everyone else, admins included (they may still end it) — as on a
partitioned tile (PD-09). The mark is sticky (`sessionSignin.held`): it
outlives the sign-in being taken away mid-run, goes into the session's
history entry (`HistoryMeta.Signin`: view-as reads neither transcript nor
first prompt, 403), and a session that resumes such a transcript is one
too.

A **restart never widens**: the session keeps the choice it was opened
with (none stays none; a named sign-in stays that one or becomes none; an
agent-started session keeps exactly what its tick put in). A person's own
session opened on their default asks for the default again — which is how
the Agent tab's restart after "Trust this tile" picks the sign-in up.

**Migration from the first rule.** Nothing is stored that needs
converting. A session that was refused under the old rule ("bob can also
change apps/crm") now carries the question; the card asks the next time
the person opens the tab. Tiles owned by an org, the workspace or another
person that nobody else could change used the sign-in silently under the
old rule and now ask once. Nothing is trusted on anyone's behalf.

**What the rule costs.** A non-admin on a workspace-owned tile, and
anyone on an org's tile, is asked once per tile — and again after any
change of its owner or editors, also one that was undone. A workspace
admin on the scaffold's workspace-owned tiles is not asked (decision B).
A session the workspace agent starts runs on the workspace's model unless
the person ticks their sign-in each time.

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
- **The switch on trusting** "people may trust a tile that isn't only
  theirs" — `PUT /agent/signins/policy {trust}`, default allowed, in the
  same `policy.json`; `policy.trust: {allowed, locked?, reason?}`. Off:
  kept answers aren't used, none can be given, the sweep ends the
  sessions that ran on one. The host's file: `"agentSignInTrust"`.
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

> Revised 2026-11-17 for the rule of §5, whose risk model the owner
> accepted that day: what the default exposes, and what trusting does.

| Step | Other people on a shared tile | The tile's code | A tile agent's model (tool output) | The workspace agent's model | Logs | Backups | A downgraded xbind |
|---|---|---|---|---|---|---|---|
| Guided flow: link shown | no: the flow answers its starter only | no: the throwaway sandbox binds no tile | no | no: `/agent/signins*` is refused to delegates | the URL is not logged | — | — |
| Guided flow: token printed | no: nobody can enter the throwaway sandbox | no | no | no | never logged; the buffer is zeroed | — | — |
| Pasted key in the request | no | no: person-only route, refused to tile credentials | no | no | request bodies are not logged | — | — |
| At rest | no | no: `data/` is mounted in no sandbox | no | no | — | sealed (plaintext mode: in the clear) | not read |
| Read routes | metadata of their own only | refused | — | refused | — | — | — |
| Admin routes | — | refused | — | refused | — | — | — |
| Spawn frame → adapter | **by default: nobody** — it goes in unasked only on a tile the person owns (or, for a workspace admin, a workspace-owned one) that no non-admin else can change. **After "I trust this tile's source": the people the card listed, by account id** — the tile's owner and every editor at that moment: a file any of them leaves in the tile runs in the session — residual 8. Someone new, another owner, a move: the trust is void for good, the session is ended, trust is asked again | **yes** — residual 1: on the person's own tile only code they (or an admin) put there; on a trusted tile the listed people's too | **yes**: it can run a command that prints it — residual 2 | **never by default; yes for a session whose start the person approved with "Use my sign-in for this session" ticked** — it directs that session — residual 7 | masked in the session log | history is masked | — |
| The trust answer | given by the person only (person-only route; a ticked line of their own approval), for the basis they were shown (409 on another); never an admin, an owner, the agent, a page message | refused: tile credentials can't call the route | — | refused (`dNever`); a tick can't be sent by the agent or from a notification | audited (`signin.trust`, `signin.untrust`, `signin.trust.void`): tile, owner, counts | metadata in the person's file | an older xbind ignores it (it uses the stricter rule of v0.3.80). The admins' ban on trusting is in `policy.trust.json`, which an older xbind never rewrites: a downgrade and return keeps it |
| The person's `$HOME`, one for all their tiles — **closed for writes, open for reads** (D245, `home-on-untrusted-tiles.md`) | **writes: no** — on a tile the person has NOT trusted their sessions mount a copy-on-write view of the home: what is written there stays in a layer per (person, tile identity) under `data/home-copies/`, never the real home; a copy session is confined like a member's whoever opened it (an admin's sandbox could mount its way around the copy); an admin has no pass into a session that has someone's real home; a tile someone else could change BEFORE keeps asking (xbind's record of past editors, from D245 on — earlier history is unknown). **reads: yes**, as always — residual 10. **An admin's REAL-home session on a tile runs that tile's code with the admin's sandbox powers** (CAP_SYS_ADMIN in its user namespace: it can remount the workspace) — pre-existing, by design, now gated by the trust answer on tiles that aren't the admin's own | **writes: no; reads: yes**: code in that tile runs in a session that carries no sign-in and reads the person's home (a login kept there, an SSH key, the transcripts of the row below) | — | — | — | the layers are in a backup of `data/` (they hold what untrusted code wrote; nothing reads them but their overlay) | an older xbind ignores the layers and mounts the real home read-write again |
| Codex's `auth.json` | no: private `CODEX_HOME` in the session's `/tmp` | yes, as above | yes, as above | no | — | not in `$HOME`, so not in a backup of homes | — |
| Session events, history | the person only (admins and view-as barred, live and in history) | — | — | masked on the joined stream (value with separators, base64, hex, %XX; credential shapes) — lowers accidental exposure, **not a guarantee** — residual 7 | masked, the same way | masked, the same way | reads masked history |
| The harness's own transcripts in `$HOME` (`~/.claude/projects`, `~/.codex/sessions`) | no | **a session of the person's on another tile — one they haven't trusted — can read them** — residual 9 | yes | through a session it steers | — | **unmasked**, in a backup of homes | — |
| Use log, audit | the person; admins see metadata | no | no | no | ids and names only | no secret | — |

Residual risks, stated:

1. **The tile's code and the person's own session.** Anything that runs
   in the session's sandbox — the agent's tools, the project's hooks and
   scripts — can read the adapter's environment. **By default** (a tile
   the person owns, which nobody else who isn't an admin can change) that
   code is theirs or an admin's; a tile they made from a template or
   imported can carry a hook. This is the same exposure the `$HOME` login
   has always had, with a narrower credential (a setup-token makes model
   requests only).
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
7. **Redaction is not a guarantee; a session the workspace agent starts
   with a ticked sign-in is within the agent's reach.** xbind masks the
   value on the joined text of each stream — with anything between its
   characters, as base64 (any offset), hex or `%XX` — and the harnesses'
   credential shapes (`signinredact.go`). That catches accidents: a tool
   that printed its environment, `env | base64`. It does not see a file
   written in the tile, and a session that is *asked* to hand the
   credential over can do it in a form nothing recognises. The workspace
   agent drives the sessions it opened and nothing refuses its prompts
   to one that carries a sign-in. So the boundary is the person's tick,
   not the mask and not the approval alone: a session the agent starts
   has NO saved sign-in by default — not under "start without asking",
   not by approving the start — and has one only where the person ticked
   "Use my ‹harness› sign-in for this session" on that start's sheet,
   whose one sentence says the agent directs the session. What a ticked
   start exposes is exactly this residual.
8. **A trusted tile exposes the sign-in to the people the card listed**
   (accepted by the owner, 2026-11-17). Trust is the person's informed
   choice, once per tile: its owner and everyone who could change it then
   can plant a file that runs in the person's session, read the sign-in
   and use the person's account with the harness's vendor until the
   person revokes it there. What bounds it: the card names them, by
   account id, all of them; a change of the basis — someone new, another
   owner, a move, a re-made tile — voids the trust when it is seen, for
   good (the session ends, trust is asked again, and undoing the change
   revives nothing); the person can stop trusting, and
   an admin or the host can forbid trusting. What doesn't: a person who
   trusted a tile and one of the listed people turning hostile later; an
   org owner whose membership grows *read*-level people is unaffected,
   but one who gains write is "someone new" only if they weren't an
   editor when trust was given. Workspace admins are never listed as
   editors and are always able to change a tile (residual 3).
9. **One `$HOME` for all of a person's tiles, and the harnesses keep
   their own transcripts in it**, unmasked — Claude Code's
   `~/.claude/projects/*.jsonl`, Codex's `sessions/` and `log/` (linked
   into the private `CODEX_HOME` on purpose). If a tool printed the
   credential in a session on a tile the person alone can change, a
   session of theirs on a tile they have NOT trusted — which got no
   sign-in, but runs other people's hooks — can read it from those files. A per-session
   private state directory would close this and was not built: session
   resume (`session/load`, D75) reads exactly those files, and the
   person's settings, MCP servers and memory live beside them, so every
   sign-in session would start as a first run and never resume. The
   person's mitigation: don't have the agent print its environment; if it
   did, remove the sign-in, revoke it at the vendor and delete that
   transcript. Narrowing `$HOME` per tile (`plans/workspace-agent.md` §4)
   is the real fix.
10. **The person's one `$HOME`: closed for writes on untrusted tiles
    (D245), open for reads.** Until D245 the home was mounted read-write
    into every session, so code in a tile the person had not trusted — a
    hook a co-editor left — could plant `~/.claude/settings.json` hooks,
    `~/.gitconfig` (an alias, `core.hooksPath`, a credential helper) or
    `~/.bashrc` that ran LATER, in a session of the person's on their own
    or a trusted tile, one that does carry the sign-in: the default and
    the trust prompt bounded code that runs in the session they are
    decided for, not code that waits in the home. Now a session on a tile
    whose source needs the person's trust and has not got it mounts a
    copy-on-write view of the home (`home-on-untrusted-tiles.md`): reads
    come from the real home, writes go to a layer per (person, tile
    identity) that never reaches it; trusting merges nothing; a trust
    that stops holding ends the sessions that had the real home there; a
    copy that cannot be mounted fails the session. A copy session is
    confined like a member's also for an admin and the root token (their
    sandbox could mount around the copy), the condition is sticky (who
    could change a tile before is remembered from D245 on; earlier
    history is unknown), and an answer given before D245 is void — it
    was given for the sign-in only. An admin's REAL-home session on a
    tile still runs that tile's code with the admin's sandbox powers: by
    design, and now behind the trust answer. **What remains: such
    code still READS the home** — a `claude auth login` kept there, SSH
    keys, `~/.netrc`, the harness transcripts of residual 9 — as it
    always could. The saved sign-in is the answer for the coding agents'
    credentials (it is never in the home, and never in a session on an
    untrusted tile); hiding credential directories from reads on
    untrusted tiles is designed, not built (the plan's §9). Without
    sandboxes (`--isolate` off) nothing of this applies: sessions are
    processes of xbind's own user. The builder docs say it under "Your
    home on a tile that isn't only yours".

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
| `PUT /agent/signins/policy` | workspace admin | `{enabled?, trust?}` → `{policy, ended}` (off ends the sessions running on one; trust off, those on tiles that needed it); 409 when the host holds the one written |
| `POST /agent/signins/trust` | person | `{tile, basis}` → 201 `{trust}`: "I trust this tile's source", for the question as shown · 409 `{changed, trust}` the tile is no longer that · 409 the tile is theirs alone · 403 trusting is off |
| `DELETE /agent/signins/trust/{id}` | person | → `{ok, ended}` (their sessions there on a saved sign-in are ended) |
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
  `start`) carries `signIn` (what it runs on), `signInId` (the sign-in
  the sheet offers, unticked) and `signInTrust` where the tile's source
  needs the person's trust (`approve {signin: true, trust: true}`, a
  plan's apply `{signin: [n], trust: [n]}`).
- `GET /agent/signins`: `trusted`, `policy.trust`, `here.trust`,
  `here.without`; a session's `signin.trust`; a providers row's
  `saved.trust`. The host's file: `agentSignInTrust`.
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
