# Protocol: coding-agent sign-ins held by xbind

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the sign-ins a person saves for their coding agents (D230) — the list, the
guided sign-in xbind runs, the admins' view and the workspace's switch.
What they are for, the rule for shared tiles and who can read what:
[agent-signins.md](/docs/agent-signins.md).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it. "A person" here is a person's own session,
app or device: never a tile's credentials (a frame, a backend, cron), a
terminal's or an agent session's token, an admin viewing as someone, the
root token, or a person's workspace agent — each is 403.

```
GET    /agent/signins             a person. your saved sign-ins —
                                   metadata only (below); with ?tile=<p>,
                                   whether a session there would get one
POST   /agent/signins             a person. {harness, secret, name?,
                                   default?} → 201 {signin}: a pasted key
                                   or token
PUT    /agent/signins/<id>        a person. {name?, default?: true,
                                   secret?} → {signin}
DELETE /agent/signins/<id>        a person. → {ok, ended}: forgotten, and
                                   the sessions running on it ended
DELETE /agent/signins/notice      a person. the notice was read → {ok}
GET    /agent/signins/uses        a person. {uses: [{at, id, name, harness,
                                   tile, session, origin}]}, newest first
POST   /agent/signins/guided      a person. {harness, name?, default?} →
                                   202 {flow}: xbind runs the coding
                                   agent's own sign-in and answers the page
                                   to open
POST   /agent/signins/guided/<flow>
                                   a person. {code} → 201 {signin} · 409
                                   (the code again) · 502 · 504
DELETE /agent/signins/guided/<flow>
                                   a person. cancel → {ok}
GET    /agent/signins/people      a workspace admin. {available, why?,
                                   policy, vault, people: [{user, total,
                                   signins: [{id, harness, kind, createdAt,
                                   lastUsedAt?, expiresAt?, default}]}]} —
                                   no name, no secret
DELETE /agent/signins/people/<user>/<id>
                                   a workspace admin. revoke one →
                                   {ok, ended}
PUT    /agent/signins/policy      a workspace admin. {enabled?, trust?} →
                                   {policy, ended}; 409 while the host's
                                   file sets the one written
POST   /agent/signins/trust       a person. {tile} → 201 {trust}: "I trust
                                   this tile's source" — your saved sign-in
                                   is used in your sessions there. 409 the
                                   tile is yours alone; 403 trusting is off
DELETE /agent/signins/trust/<id>  a person. → {ok, ended}: no longer
                                   trusted; your sessions there on a saved
                                   sign-in, or with your real home, are
                                   ended
DELETE /agent/signins/homes/<id>  a person. → {ok, tile, ended}: forget
                                   what that tile wrote to your home (its
                                   copy; your real home is not touched)
```

**No answer of any route carries a secret, or a part of one** — not a
prefix, not the last four characters. A secret leaves the store one way:
xbind puts it into the environment of a coding agent it starts for its
person ([agent-signins.md](/docs/agent-signins.md)).

## The list

`GET /agent/signins`:

```
{ "available": true,            false: this xbind keeps none — "why" says
  "unisolated": true,           only on a development workspace run without
                                sandboxes (--dev)
  "vault": "open",              open | sealed | locked | plaintext
  "policy": {"enabled": true, "locked": true, "reason": "set by your host",
             "trust": {"allowed": true}},   people may trust a tile that isn't only theirs (locked?, reason?)
  "trusted": [ {"id": "tr_5d0e…", "tile": "apps/crm", "name": "CRM", "at": 1794846000000, "via": "card",
                "holds": true} ],           the tiles whose source you said you trust; holds false + why: xbin asks again
  "notice": {"at": 1794845000000, "text": "Your saved sign-in Work was removed by root."},
  "admin": true,                the caller may use the admins' routes
  "harnesses": [
    { "id": "claude", "name": "Claude Code",
      "guided": true,           xbind can run its own sign-in for you
      "keys": [ {"env": "CLAUDE_CODE_OAUTH_TOKEN", "label": "Claude subscription token (claude setup-token)", "kind": "setup-token"},
                {"env": "ANTHROPIC_API_KEY", "label": "Anthropic API key", "kind": "api-key"} ],
      "login": "claude auth login",   the line that signs it in on one tile, in a terminal
      "max": 8,
      "signins": [
        { "id": "si_3f0c…", "harness": "claude", "name": "Personal",
          "kind": "setup-token", "env": "CLAUDE_CODE_OAUTH_TOKEN",
          "createdAt": 1794840000000, "lastUsedAt": 1794844000000,
          "expiresAt": 1826376000000, "default": true,
          "refusedAt": 0, "refused": "" } ] } ],
  "flows": [ {"id": "sf_91ab…", "harness": "claude", "url": "https://claude.com/cai/oauth/authorize?…", "paste": true, "expiresAt": 1794840900000} ] }
```

- With `?tile=` (a tile you may open a terminal on, or a directory inside
  one — the rule is judged on the registered tile it lies in) each harness
  carries `here: {inject, why?, trust?, without}`: whether a session of
  yours on that tile would get a saved sign-in of it — the rule's answer,
  whether or not you saved one yet — and why not. `trust` is there when
  the only reason is that the tile's source isn't only yours and you
  haven't said you trust it — xbind's question, for the card:

  ```
  "trust": { "tile": "apps/crm", "name": "CRM",
             "basis": "tb_9c1e…",          a token of everything below: sent back with the answer
             "ownerKind": "person",        you | person | org | workspace
             "owner": "bob",               the owner's ACCOUNT (or org) id — what xbind enforces
             "ownerName": "Bob B.",        its display name, where it has another: beside the id, never instead
             "admin": false,               true: a workspace-owned tile, and you administer the workspace
             "editors": ["carol", "dave"], the account ids of ALL the others who can change it
             "names": {"carol": "Carol C."},  their display names, where they have another
             "allowed": true }             false + forbidden: trusting is switched off
  ```

  A client lists a few editors and must let the person see all of them
  before it offers the answer.

  `without` is what a session there runs on without a saved sign-in:
  `own` (the tile's own login), `connector` (the workspace's model) or
  `none` (it starts signed out).
- `harnesses` are the coding agents that take a credential xbind can keep
  (today: Claude Code, Codex, Gemini CLI, OpenCode). `guided` is true where
  the CLI has a sign-in that prints a long-lived credential — Claude Code's
  `claude setup-token`, a one-year token for model requests only. The
  others take a pasted key: their subscription sign-ins keep a refresh
  token in a file, which xbind never copies.
- A sign-in's `kind` and `env` follow from the value: `keys` are matched by
  prefix, in order.
- Times are Unix milliseconds. `expiresAt` is known for a setup-token
  (created + 365 days) and absent for a key. `refusedAt`: the coding agent
  itself refused the credential when a session last used it; `refused` its
  words, with anything credential-shaped masked.
- Names are one line, up to 40 characters, unique per person and harness
  (case-insensitive). One sign-in per harness is the `default`: the first
  saved, until another is made it; removing the default hands it to the
  newest one left. At most 8 per harness.
- `vault`: `sealed` — nothing can be used or saved until an admin unseals
  the workspace vault (the list still reads, and Remove still works);
  `locked` — no vault is set up, and a sign-in is never kept in the clear;
  `plaintext` — the workspace runs with `--insecure-vault` or `--no-auth`
  and keeps every secret in the clear, these too.
- `notice` is set when someone else removed sign-ins: an admin revoked
  one, or a credential someone else made for the account (a password set by
  an admin, a sign-in link, an SSO email) removed them all.

## Saving one

**Pasted** — `POST /agent/signins {harness, secret, name?, default?}`.
The value is trimmed and must be one line of printable ASCII, at most 4096
bytes, with no spaces, quotes, backslashes, `<`, `>` or `&`. Nothing is
checked against the vendor: xbind calls no provider. A name the harness
already has replaces that sign-in's secret and keeps its id.

| Status | When |
|---|---|
| 201 | `{signin}` |
| 400 | an unknown harness, an unfit secret or name |
| 403 | the workspace's switch is off |
| 409 | the harness takes no saved credential; 8 are kept already; this xbind keeps none |
| 503 | the vault is sealed, or not set up |

**Guided** — three calls:

1. `POST /agent/signins/guided {harness, name?, default?}` → 202 `{flow:
   {id, harness, url, paste, expiresAt}}`, once the CLI printed its page
   (30 s). xbind runs the CLI in a throwaway sandbox: the base image, no
   tile, no home, an empty `HOME`, internet egress only. `url` is https on
   the harness's own hosts with its path pinned; open it in your own
   browser and sign in there.
2. `POST /agent/signins/guided/<flow> {code}` with the code the page
   shows. xbind types it and waits for the CLI's word (60 s):

   | Status | Meaning | The flow |
   |---|---|---|
   | 201 `{signin}` | the credential the CLI printed is kept | over |
   | 409 | the CLI refused the code as malformed and asks again | open |
   | 502 | the CLI refused the sign-in (its reason line) or ended | over |
   | 504 | no word from the CLI | over |

3. `DELETE /agent/signins/guided/<flow>` cancels.

A person has one flow per harness — a second start answers the same one —
and it ends by itself after 15 minutes. A flow answers its starter only:
anyone else's call is 404. The workspace runs at most 8 at once (429).

## Admins

`GET /agent/signins/people` shows **that** people have sign-ins: per
person and sign-in the harness, kind and times — no name, which is the
person's own label, and no secret. `DELETE
/agent/signins/people/<user>/<id>` revokes one: it is removed, that
person's sessions running on it are ended, and their list carries a notice
naming who did it.

## Trusting a tile's source

A saved sign-in is used by default on a tile that is its person's alone.
On a tile whose source isn't only theirs — it is owned by someone else (a
person, an org, the workspace), **or** someone else who isn't a workspace
admin can change its code (write level or more; partitioned tiles too),
**or** someone else could before and can't now (xbind's record of who
could change each tile, kept from this rule on: `trust.past`) —
it is used only once the person says so
([agent-signins.md](/docs/agent-signins.md) → the rule).

`POST /agent/signins/trust {tile, basis}` is that answer: "I trust this
tile's source". `tile` is a tile the caller may open a terminal on, or a
directory in one; **`basis` is the question's own** (`here.trust.basis`,
or a start's `signInTrust.basis`): the person trusts what they were
shown. 201 `{trust: {id, tile, name?, at, via, holds}}`. **409 `{error,
changed: true, trust}`** when the tile is no longer what that basis
stands for — another owner, someone else able to change it, moved, made
again: nothing is recorded, and `trust` is the question as it stands now
(a card draws it again and makes its button wait again). 400 without a
basis; 409 when the tile is the caller's alone (nothing to trust), or
xbind can't tell it from another tile at its path; 403 when the workspace
doesn't let people trust; 404 no such tile.

The question (`here.trust`, a row's `home.trust`, a start's
`signInTrust`) names, beside the owner and `editors`, **`past`** — the
account ids (and `org:<id>`) that could change the tile's code before and
can't now — with `pastUntil` (Unix ms: when the last of them was last
seen able to) and their `names`; and **`means`**, the version of what an
answer stands for (2: the saved sign-in is used there *and* the tile's
code can change the person's home). `means` is part of the basis (now
`tb2_…`): a card drawn by an older xbind, or an approval's stored
preview, gets the 409 above and is drawn again; an answer **kept** from
before (no `means`) is void — `holds: false`, `why` says trusting means
more now — and the question is asked again.

The answer is kept with the person's sign-ins **for the tile as it
stood** — the tile itself (its directory's identity) at its path, its
owner, and who could change it. It is **void from the moment xbind sees
any of that change** — on the event that changes it (an account, a role,
an access entry or pattern, a permission set, an org's members or
shares, an owner transfer; a tile moved, removed or made again on disk),
with or without a session running, and again at every judgement of the
rule as a backstop — and it **stays void** until the person answers
again: someone who gained write and lost it, or an owner changed and
changed back, do not bring it back. `holds` is then false (`why` says
what changed), `here.trust` is back, and the sessions that ran on it are
ended. People who can no longer change the tile are dropped from the
answer's record, so their return is someone new. `via` is `card`,
`approval` (the trust line of a start the person approved) or `plan`.

`DELETE /agent/signins/trust/<id>` takes it back → `{ok, ended}`: the
caller's sessions on that tile that run on a saved sign-in, or that have
their real home (below), are ended.

Only a person's own session, app or device calls either — never a tile's
frame, backend or terminal token, a person's workspace agent, an admin
viewing as them, or the root token (403). The agent's page may open the
sign-in card (`xbin:agent-open-signin`); no message answers it.

## Your home on a tile that isn't only yours

The same answer decides which home a session mounts (D245,
[agent-signins.md](../agent-signins.md#your-home-on-a-tile-that-isnt-only-yours)).
A person has one home for all their tiles. On a tile whose source isn't
only theirs and that they have not trusted, their terminals and agent
sessions read that home and write to a **copy** kept for that tile: what
the tile's code writes under `~` never reaches the real home. Everywhere
else — their own tiles, trusted ones — the home is the real one.

Every session row (`GET /term/sessions`, `POST /term/sessions`, `GET
/term/sessions/<id>`) carries it, fixed at the session's start:

```
home: { mode: "own" | "copy",   // "home: yours" | "home: this tile's copy
                                //  (changes stay on this tile)"
        why?,                   // why it is the copy
        kept?,                  // it restarted or resumed a session that ran
                                //  on the copy, and stays on it
        confined?,              // an admin's (the root token's) session on
                                //  the copy: it has a member's sandbox —
                                //  the restricted tier, its view, no host
                                //  network — until they trust the tile
        unsandboxed?,           // the workspace runs without sandboxes:
                                //  nothing is protected
        trust?,                 // the question (as here.trust) whose answer
                                //  gives NEW sessions there the real home
        docs }
```

`GET /agent/signins?tile=<path>` answers `home` the same way for a session
the caller would open there now, with `copy` — the tile's copy, when there
is one — and `homes`, all the caller's copies:

```
homes: [{ id, tile, name?, created, bytes,   // bytes -1: not measured
          sessions,                           // live sessions on it
          gone?,                              // its tile was deleted (it goes at the next
                                              //  sweep) or made again at its path (kept
                                              //  until forgotten; never mounted again)
          unused }]                           // the tile is trusted now: kept, not used
```

Trusting a tile (`POST /agent/signins/trust`) merges nothing into the real
home, and there is no route that copies a copy over. New sessions on the
tile then mount the real home; the ones already open stay on the copy
(`kept` after a restart). A trust that stops holding ends the sessions
that have the real home on that tile; the next one is on the copy as it
was left. `DELETE /agent/signins/homes/<id>` removes a copy: the caller's
sessions on it are ended first (`ended`), a start under way on it is
waited for (up to 20 s) and ends as it registers; **409** when one is
still starting or ending then — nothing was removed, ask again; 404 when
it isn't theirs. xbind removes a copy by itself only when its tile's
directory is gone or its account is deleted — never on an identity that
changed or could not be read (a restored workspace keeps its people's
copies, listed `gone`).

A session is judged when it is opened and **again when it is listed** if
anything that ends sessions happened in between (a start takes seconds; a
VM's, longer): one that would no longer get its real home, or the saved
sign-in it carries, is ended with the reason as its first frame. A
session opened with a terminal's token (`bx agent` in a shell) has the
real home only when the session that token was minted for has it; the
root token's terminals' tokens are the root token's.

A session that has its person's real home is that person's alone: an
admin's attach (`/ws/term?session=`) and the per-session routes answer 403
for anyone else, as on a partitioned tile; ending it stays allowed. A
session on a copy is confined like a member's whoever opened it
(`home.confined` for an admin and the root token). A
copy that cannot be mounted fails the session's start with the reason —
`400` on `/ws/term`, `POST /term/sessions` — and the real home is not
opened in its place. Without `--isolate` there are no sandboxes and no
copies: `home.mode` is `own` with `unsandboxed: true`.

## The switches

`PUT /agent/signins/policy {enabled?, trust?}` writes the workspace's two
switches. `enabled` is "people may
save coding-agent sign-ins", on by default. Off: nothing is put into a
session and nothing can be saved, replaced or made default; the running
sessions that carry a saved sign-in are ended (`ended`: how many), as when
one is removed; what is saved stays listed and removable, and works again
when the switch returns. A host
sets it for good with `"agentSignIns": true | false` in the deployment file
([llm.md](/docs/llm.md) → "The host's file"): the policy then reads
`{enabled, locked: true, reason: "set by your host"}` and a write is 409.
A host's file that turns them off while sessions run ends those sessions
the next time xbind judges the rule (anyone opens a launcher, starts an
agent or reads `?tile=`), not at the moment the file changes.

`trust` is "people may trust a tile that isn't only theirs", allowed by
default: `policy.trust: {allowed, locked?, reason?}`. Off: nothing can be
trusted, what was trusted isn't used (it is kept, and holds again when the
switch returns), `here.trust.allowed` is false with `forbidden` saying who
can change that, and the sessions that ran on a trust are ended. The
host's file sets and locks it with `"agentSignInTrust": true | false` —
turned off there while sessions run, they are ended the next time xbind
judges any tile's source, as with `agentSignIns`. The admins' word is
kept in `data/signins/policy.trust.json`, a file of its own: an xbind
from before this switch rewrites `policy.json` and would drop a ban kept
there, so a downgrade keeps it.

Every save, removal, revoke, switch and use is in xbind's audit log
(`audit` lines with `event` `signin.save`, `signin.remove`,
`signin.revoke`, `signin.policy`, `signin.use`, `signin.trust`,
`signin.untrust`) — ids, names, the tile and
the session; never a secret.

## What other routes say

- `POST /term/sessions` takes `signin`: one of the caller's sign-ins by
  id, `"none"`, or nothing for their default for the harness. 404 for an
  id that isn't theirs; a person's workspace agent can't send it (403).
- A session's row (`GET /term/sessions`, `GET /term/sessions/<id>`) and
  one of its first `notice` events carry `signin: {source, id?, name?,
  why?, trust?}` once the sandbox settled it:

  | `source` | The session signs in with |
  |---|---|
  | `saved` | the person's saved sign-in `name` |
  | `home` | the coding agent's own login in the session's `$HOME` — the tile's own login |
  | `connector` | nothing: the workspace's LLM connector serves it |
  | `none` | nothing was found: the agent will say it is signed out |

  `why` (with `id` and `name`) says why a saved sign-in the person has is
  not used: the tile's source isn't only theirs and they haven't said
  they trust it (`trust` then carries the question, as `here.trust`), a
  start by their workspace agent without their tick (then with no `id`,
  `name` or `trust`: what the agent can read of a session names nothing
  of the person's — a session it started on a ticked sign-in says
  `source: "saved"` and no more),
  the connector alone may serve there or was chosen, the vault is sealed,
  a switch is off, it expired, or the CLI is signed in on its own in this
  home (Codex).
- `GET /agent/providers[?cwd=]` adds `saved: {id, name, kind, expiresAt?,
  inject, why?, trust?}` to a harness the caller has a default sign-in
  for: `inject` — a session of theirs on that tile would get it — `why`
  not, and `trust` where the tile asks for it first. Read by a person's
  workspace agent it is `saved: {inject: false}` — that one exists, and
  nothing of it. Its `llm.use` reads `own` where a saved sign-in would serve.
- A session that holds a saved sign-in has it masked in everything xbind
  keeps or sends of it — events, history, the session's log,
  `GET /delegate/sessions/<id>/result` — as `[saved sign-in]`: the value
  on the joined text of each stream (a message, thinking, a tool's output
  deltas), with anything between its characters, and its base64, hex and
  `%XX` forms; credential-shaped strings (`sk-…`, `AIza…`, `gsk_…`,
  `xai-…`) read `[redacted]`. A piece of a stream whose end could be the
  start of a match is delivered with the next piece. **This lowers
  accidental exposure and is not a guarantee**: the coding agent, its
  model and what it runs hold the credential
  ([agent-signins.md](/docs/agent-signins.md) → "Who can read it").
- `POST /term/sessions/{id}/restart` keeps the sign-in the session was
  started with and never adds one; `GET /agent/history` entries carry
  `signin: true` for a session that ran on one, and an admin viewing as
  the person reads no such transcript (403).
- `GET /delegate/start-defaults` says per harness what a session would
  run on (`signIn`: `connector` | `own` | `needed` — never a saved
  sign-in) and `signInOffered` where the person has one; a start's
  preview carries `signIn`, `signInId` (the sign-in the sheet OFFERS as
  an unticked line) and `signInTrust`. A session a person's workspace
  agent starts has their sign-in only where they ticked that line on the
  start's approval — never by default, never under "start without
  asking" ([delegation.md](/docs/protocol/delegation.md)).
- An admin's `GET`/`POST` on another person's session that carries a saved
  sign-in is 403, as on a partitioned tile; `DELETE` still ends it. Such a
  session's `session` events reach its person's sockets only.
