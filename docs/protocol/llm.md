# Protocol: LLM connector

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the routes of xbind's built-in LLM connector (D226) — its
OpenAI-compatible endpoint, configuration, usage and quotas. What the
connector is, how an admin or a host configures it and how a tile binds to
it: [llm.md](/docs/llm.md).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it. Two families:

- **the OpenAI-compatible routes** (`/llm/v1/…`): OpenAI's wire, errors in
  OpenAI's error shape, so an OpenAI SDK or a coding CLI pointed at
  `<workspace>/api/xbin/llm/v1` works and shows the connector's messages;
- **management** (everything else under `/llm`): xbind's own JSON and its
  own error shape (`{"error": …, "docs": …}`).

## Who is calling

The identity always comes from the credential xbind verified, never from a
header or the body ([auth.md](/docs/auth.md) §The LLM connector).

| Caller | Credential | Admitted when | Metered to |
|---|---|---|---|
| a person | their session cookie, an app or device bearer, the owner token | the workspace's people switch admits them (`admins` — the default — `everyone`, `nobody`) | that person (`owner` for the owner token) |
| a component | its instance, frame, terminal or agent-session credential | one of the tile's `openai` slots is bound to `xbin:llm`, asked on every call | the tile, and the person acting: a partition's person, the person driving the frame or session; nobody for a backend that isn't a person's partition |
| an LLM key | `Authorization: Bearer xbl_…` | the key is live | the person and tile the key was minted for |

An LLM key is accepted on `/llm/v1/…` only; presented anywhere else the
request is `401`, whatever else it carries. It stands while its person
exists, is enabled and can open the tile it was minted for (and, minted
under a binding, while the tile is bound).

A component's credential bound to a non-primary deployment of its tile uses
the binding by the tile's edge policy: `read` (the default) may `GET` the
routes below and is refused the two `POST`s, `block` is refused all of
them — `403 xbin_llm_edge`.

One caller — a person, or a component for calls nobody acts in — has a
bounded number of calls in flight (`concurrency`, 16 unless set): the next
is `429 xbin_llm_busy` with `Retry-After: 1`. Who is calling, that bound
and the quotas are checked before the body is read.

## The OpenAI-compatible routes

```
POST   /llm/v1/chat/completions    a caller (above). OpenAI's Chat Completions
                                   request, relayed to the upstream its model
                                   names. Every member sent reaches the
                                   upstream (written anew, each once: one
                                   sent twice goes with its last value),
                                   except: model → the upstream's own name;
                                   on a stream, stream_options.include_usage
                                   → true (the last chunk has empty choices
                                   and the usage; other stream options
                                   stay). model: a string of at most 200
                                   characters without spaces; stream: true
                                   or false (400 otherwise). The answer is
                                   the upstream's, streamed as it arrives
                                   (text/event-stream) or whole. Body ≤ 32
                                   MiB (413). A client that goes away (or
                                   stops reading for 2 minutes) ends the
                                   upstream call after a grace of 5 s, in
                                   which the answer is still read for its
                                   usage; without it the call is metered as
                                   an estimate. Answer
                                   headers: the upstream's Content-Type,
                                   Retry-After, request id and x-ratelimit-*
                                   only; X-XBin-LLM-Quota: warn; quota=<name>;
                                   resets=<time> when a warn quota is reached
POST   /llm/v1/responses           a caller. OpenAI's Responses request,
                                   relayed the same way; only model is
                                   rewritten and the stream's events pass
                                   untouched (usage is read from
                                   response.completed). background: true is
                                   400 xbin_llm_unsupported: an answer that
                                   doesn't pass through can't be metered
GET    /llm/v1/models              a caller. {object: "list", data: [{id,
                                   object, owned_by, alias_of?, …}],
                                   upstream_errors?}: each upstream's models
                                   as <upstream>/<model> — its configured
                                   list, or its own /models answer (cached a
                                   minute) with the upstream's members kept
                                   (context sizes pass through) — then the
                                   aliases (owned_by "alias") and
                                   xbin/default, xbin/<role> with alias_of.
                                   upstream_errors: {<upstream>: why} for a
                                   list that couldn't be read (detail for
                                   admins only)
```

**The model** of a request, first match: absent, `xbin/default` or
`xbin/<role>` (the workspace's default models); `<upstream>/<model>`; a
bare name one upstream lists; an alias; any name when the workspace has a
single upstream that passes names through. A name that resolves to nothing
goes to the alias `*` when an admin set one, and is otherwise refused —
never mapped to the default silently. An alias never takes over a name the
configuration has (a name under an upstream, a listed model).

**Errors** are `{"error": {"message", "type", "param", "code", …}}`:

| Status | `code` | When |
|---|---|---|
| 400 | `xbin_llm_body` | the body is not a JSON object, or `stream` is not a boolean (415: not JSON at all; 413: over 32 MiB) |
| 400 | `xbin_llm_model` | `model` is not a string, has spaces or is over 200 characters |
| 400 | `xbin_llm_unsupported` | a Responses request with `background: true` |
| 400 | `xbin_llm_no_default`, `xbin_llm_ambiguous_model`, `xbin_llm_alias_loop` | no model named and no default; a bare name two upstreams list; aliases in a circle |
| 401 | `invalid_api_key` | no valid credential; an LLM key that expired or was revoked |
| 403 | `xbin_llm_not_bound` | a component whose slot isn't bound to `xbin:llm` |
| 403 | `xbin_llm_people_off` | a person the people switch doesn't admit |
| 403 | `xbin_llm_policy` | the call's upstream or model is not allowed for this caller |
| 403 | `xbin_llm_edge` | a non-primary deployment under its tile's edge policy: a call under `read`, anything under `block` |
| 429 | `xbin_llm_busy` | the caller already has `concurrency` calls in flight; `Retry-After: 1` |
| 404 | `model_not_found` | the model resolves to no upstream, or isn't on its upstream's list |
| 429 | `xbin_llm_quota_exceeded` | a stopping quota is reached. `type: "insufficient_quota"`, plus `quota` (`user.tokensPerDay`, `org:<id>.costPerMonth`, `default.…`, `workspace.…`), `resets` (RFC 3339, UTC), `limit`, `used`; `Retry-After` in seconds |
| 502 | `xbin_llm_upstream_auth` | the upstream refused the workspace's key (its 401 or 403 — never passed on as the caller's) |
| 502 | `xbin_llm_upstream_unreachable`, `xbin_llm_upstream` | the upstream can't be reached; it answered a redirect (never followed) |
| 503 | `xbin_llm_not_configured`, `xbin_llm_disabled` | no upstream exists; the connector is switched off |
| 503 | `xbin_vault_sealed`, `xbin_llm_upstream_key` | the upstream's key is in the vault and the vault is sealed or not set up; the key is missing |
| 504 | `xbin_llm_upstream_timeout` | the upstream sent no answer for 10 minutes |

Any other upstream error passes with the upstream's status and body (the
key cut out of it, should the upstream echo it); one that isn't
OpenAI-shaped is answered as `xbin_llm_upstream` with the same status. The
connector does not retry.

## Status and defaults

```
GET    /llm                        any authenticated caller. The connector as
                                   the caller meets it, always 200:
                                   {configured (an upstream exists), enabled,
                                   callable, reason?, code? (why not, as the
                                   call's error would say), url:
                                   "/api/xbin/llm/v1", ref: "xbin:llm",
                                   people, host (a deployment file is in
                                   force), defaults: {model?, roles}, user?,
                                   component?, admin?, quota?: {user, orgs?,
                                   limits: [Limit], workspace: [Limit],
                                   state: ok|warn|stop}}. Limit: {name:
                                   tokensPerDay | tokensPerMonth | costPerDay
                                   | costPerMonth, value, source: user |
                                   org:<id> | default | workspace, action:
                                   stop|warn, used, resets, state}
GET    /llm/preferred?use=<role>   a caller. use is optional. The workspace's default models in
                                   the shape the llm-gw tile answers. use:
                                   {use, model} — that role's model as
                                   <upstream>/<model>, the default model when
                                   the role has none, "" when neither. No
                                   use: {preferred: {default?, <role>…},
                                   useTypes: [roles]}. Errors as the
                                   OpenAI-compatible routes'
```

## Configuration (admins)

"admin" is a workspace admin: an admin's own sign-in, the owner token, or
the admin console (a tile holding `xbin:admin`). A view-as session changes
nothing. Writes are audited.

```
GET    /llm/config                 admin. The configuration in force, the
                                   admin's settings and the host's file
                                   (XBIN_LLM_CONFIG) merged: {configured,
                                   enabled, people, upstreams: [{name, kind,
                                   baseURL, key: {set, source?: vault|host,
                                   hint?}, models, private, prices?, source:
                                   admin|host, locked, reason?}], defaults:
                                   {model?, roles}, aliases, quotas, locks,
                                   concurrency, host: {active, lockAll?,
                                   error?}, vault: open | sealed | locked |
                                   plaintext, problems, storeError?,
                                   notices?, set?, by?}. locks
                                   maps each setting the host's file sets —
                                   enabled, people, upstreams.<name>,
                                   defaults.model, defaults.roles.<role>,
                                   aliases.<alias>, concurrency,
                                   quotas.workspace | default | users |
                                   orgs, and "*" when it locks all — to
                                   {locked: true, reason: "set by your
                                   host"}. concurrency: calls one caller may
                                   have in flight. notices: on the answer to
                                   a write, what it did beyond what was
                                   asked (a key removed). No key is ever answered:
                                   hint is an admin-set key's last four
                                   characters, while the vault is open.
                                   problems: defaults and aliases that
                                   resolve to nothing
PUT    /llm/config                 admin. {enabled?, people?: admins |
                                   everyone | nobody, defaults?: {model?,
                                   roles?: {<role>: <model>}}, aliases?:
                                   {<alias>: <model>}, concurrency?: 1–1024
                                   (0: the default, 16)} — only what is
                                   named changes; "" removes a role, an
                                   alias or the default; the alias "*"
                                   catches every unknown model name. An
                                   alias named <upstream>/… for an upstream
                                   in force, or like a model an upstream
                                   lists, is 400. → the view. 400 invalid; 409 {error, locked: [<path>…],
                                   reason} when a named setting is the
                                   host's (a locked setting sent back
                                   unchanged passes)
PUT    /llm/upstreams/<name>       admin. Add or change an upstream: {baseURL?,
                                   key?, models?: [<model>], private?,
                                   prices?: {<model>: {in, out, cachedIn?}},
                                   kind?: "openai"} — a new one needs
                                   baseURL (http(s), no credentials or query
                                   in it); what isn't named stays. name:
                                   lowercase letters, digits, . _ - (not
                                   xbin). key goes to xbind's vault ("" removes
                                   it); a change of baseURL's scheme, host
                                   or port without key removes the stored
                                   key (notices says so): a key is only sent
                                   to the address it was set for. models
                                   empty: the upstream's own
                                   list and any model name pass through.
                                   prices: USD per million tokens. → the
                                   view. 400 invalid; 409 {locked} for a
                                   host's upstream, or any new one when the
                                   host locks all; 409 when a key is given
                                   and the vault is sealed or not set up
DELETE /llm/upstreams/<name>       admin. The upstream and its key go; metered
                                   usage under its name stays. → the view.
                                   404 unknown; 409 {locked} for a host's
POST   /llm/upstreams/<name>/test  admin. {model?} → {ok, model?, models?,
                                   status?, ms, tokens?, error?}: the
                                   smallest chat completion (one word, one
                                   token out) with model (default: the
                                   upstream's first listed model); without a
                                   model on an upstream that lists none, its
                                   /models list (models: how many). A call
                                   like any other: metered to the admin who
                                   ran it, 429 at their quota, the
                                   workspace's or their calls in flight, 409
                                   while the connector is off. 200 whatever
                                   the upstream answered; 400 a model off
                                   the upstream's list; 404 unknown upstream
```

## Usage and quotas

```
GET    /llm/usage?period=day|month&from=&to=&user=&component=&model=
                                   every query member optional. A person's own
                                   sign-in: their own rows; admin: every row. {period, from, to,
                                   scope: own|all, rows: [{day | month, user,
                                   component, model, requests, failed?,
                                   inputTokens, outputTokens, cachedTokens?,
                                   reasoningTokens?, cost, unpriced?,
                                   estimated?}], totals}. estimated: the
                                   requests whose answer ended without the
                                   provider's usage and are charged an
                                   estimate (4 bytes of request a token in;
                                   a token per stream event relayed, or per
                                   4 bytes of a plain answer, out). Days and months are UTC; from
                                   and to are YYYY-MM-DD (default: this
                                   month so far per day, the last 12 months
                                   per month). user "owner": the owner
                                   token; "": a component acting for nobody;
                                   component "": a direct call; model:
                                   <upstream>/<model>, or <upstream>/(unknown)
                                   for every name neither the configuration
                                   nor the upstream's own list has. cost (USD) covers the
                                   requests whose model has a price,
                                   unpriced counts the others. 403 for a
                                   tile's credentials (the admin console
                                   aside) and for another person's rows
GET    /llm/quotas?user=<id>       admin. user is optional. {quotas: {workspace?, default?,
                                   users?: {<id>: Quota}, orgs?: {<id>:
                                   Quota}}, locks, effective?}. Quota:
                                   {tokensPerDay?, tokensPerMonth?,
                                   costPerDay?, costPerMonth?, action?:
                                   stop|warn} — a limit of 0 is not set, -1
                                   is unlimited. user=: also effective, that
                                   person's limits as they stand (as GET
                                   /llm's quota)
PUT    /llm/quotas                 admin. {workspace?, default?, users?:
                                   {<id>: Quota | null}, orgs?: {<id>: Quota
                                   | null}} — only what is named changes;
                                   null removes. → as GET. 400 invalid; 409
                                   {locked} for a part the host's file sets
```

A person's quota is their own override, else the most generous of their
orgs' overrides per limit, else the default — each limit on its own
([llm.md](/docs/llm.md) §Quotas). `workspace` holds everyone together. A
default the host's file sets is a ceiling: a `users` or `orgs` entry
written through this route narrows it, and one above it (or unlimited)
gives nothing — the person's limits then show the host's value with
`source: "default"`.
