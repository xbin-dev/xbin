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
PUT    /agent/signins/policy      a workspace admin. {enabled} → {policy};
                                   409 while the host's file sets it
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
                                sandboxes (--dev, --no-auth)
  "vault": "open",              open | sealed | locked | plaintext
  "policy": {"enabled": true, "locked": true, "reason": "set by your host"},
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

- With `?tile=` (a tile you may open a terminal on) each harness carries
  `here: {inject, why?}`: whether a session of yours on that tile would
  get a saved sign-in of it — the shared-tile rule's answer, whether or not
  you saved one yet — and why not.
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

`PUT /agent/signins/policy {enabled}` is the workspace's switch "people may
save coding-agent sign-ins", on by default. Off: nothing is put into a
session and nothing can be saved, replaced or made default; what is saved
stays listed and removable, and works again when the switch returns. A host
sets it for good with `"agentSignIns": true | false` in the deployment file
([llm.md](/docs/llm.md) → "The host's file"): the policy then reads
`{enabled, locked: true, reason: "set by your host"}` and a write is 409.

Every save, removal, revoke, switch and use is in xbind's audit log
(`audit` lines with `event` `signin.save`, `signin.remove`,
`signin.revoke`, `signin.policy`, `signin.use`) — ids, names, the tile and
the session; never a secret.

## What other routes say

- `POST /term/sessions` takes `signin`: one of the caller's sign-ins by
  id, `"none"`, or nothing for their default for the harness. 404 for an
  id that isn't theirs; a person's workspace agent can't send it (403).
- A session's row (`GET /term/sessions`, `GET /term/sessions/<id>`) and
  one of its first `notice` events carry `signin: {source, id?, name?,
  why?}` once the sandbox settled it:

  | `source` | The session signs in with |
  |---|---|
  | `saved` | the person's saved sign-in `name` |
  | `home` | the coding agent's own login in the session's `$HOME` — the tile's own login |
  | `connector` | nothing: the workspace's LLM connector serves it |
  | `none` | nothing was found: the agent will say it is signed out |

  `why` (with `id` and `name`) says why a saved sign-in the person has is
  not used: other people can open a terminal on the tile, the connector
  alone may serve there or was chosen, the vault is sealed, the switch is
  off, it expired, or the CLI is signed in on its own in this home (Codex).
- `GET /agent/providers[?cwd=]` adds `saved: {id, name, kind, expiresAt?,
  inject, why?}` to a harness the caller has a default sign-in for:
  `inject` — a session of theirs on that tile would get it — and `why`
  not. Its `llm.use` reads `own` where a saved sign-in would serve.
- `GET /delegate/start-defaults` adds `signIn` and `savedSignIn` per
  harness ([delegation.md](/docs/protocol/delegation.md)).
- An admin's `GET`/`POST` on another person's session that carries a saved
  sign-in is 403, as on a partitioned tile; `DELETE` still ends it. Such a
  session's `session` events reach its person's sockets only.
