# The LLM connector

xbind has an LLM connector built in (D226): one OpenAI-compatible endpoint
in front of the model providers a workspace uses, so its tiles, agents and
coding tools share one LLM setup — the keys in one place, every call metered
to the person it was made for, quotas where an admin or a host wants them.

It is off until someone configures it. A workspace that never adds an
upstream sees nothing new: the routes answer "not configured", no picker
offers the connector, and no session's environment changes.

- endpoint: `/api/xbin/llm/v1` — `chat/completions`, `responses`, `models`
  ([protocol/llm.md](/docs/protocol/llm.md))
- coding agents and terminals are pointed at it by default (§Coding agents
  and terminals)
- page: `/xbin/llm` — an admin configures it there; everyone else reads
  their own usage and quota
- admin console: workspace → LLM (the same page, inside the console)
- binding ref: `xbin:llm` (the provider of the `openai` service)
- CLI: `bx llm` ([bx.md](/docs/bx.md))
- host configuration: `XBIN_LLM_CONFIG` ([config.md](/docs/config.md))

The optional `llm-gw` tile is unchanged and still works. A tile binds its
`llm` slot to either, or (a multi slot) to both.

## Setting it up (admins)

Open **`/xbin/llm`** — or the admin console's **workspace → LLM**, which is
the same page — or use `bx llm`. Until a provider exists, the admin console
shows one card, *Set up an LLM for this workspace*, that leads there, and
an admin's shell a banner saying the same (admins only; **dismiss** — in
either, or *Stop reminding* on the page — ends it for the workspace). The page, top to
bottom:

1. **Status** — not set up, on, or off; the endpoint; a note when the
   vault is sealed or a default names a model that doesn't exist. *Connector
   on* refuses every call when unticked.
2. **Providers** — *Add a provider*: a name, the base URL, the API key (a
   masked field; once saved the key is never shown again — the row reads
   its last four characters), the models, the `private` mark and prices.
   Changing a provider's scheme, host or port drops its stored key — a key
   is only ever sent to the address it was set for — so the form asks for
   the key again (or for *remove the key*). **Test** makes the smallest
   call through the provider and answers in the row; it is a call like any
   other (refused while the connector is off or at your quota).
3. **Models** — the default model, the model of each role (`xbin/agent`,
   `xbin/harness`, or a role you add) and aliases; one *Save models*.
4. **Who may call directly** — admins (the default), everyone, nobody;
   and *Calls at once*, how many calls one caller may have in flight.
5. **Quotas** — the workspace total, the default for each person, and
   overrides per person and per organisation; each limit *stops calls* or
   *warns only*. A person's quota is also in the console's users → *LLM
   quota…*, an org's on its page. Where your host sets the default, it is
   a ceiling: the per-person and per-org forms say so, show it in each
   field and refuse a higher limit, which would give nothing.
6. **Usage** — this month per person, per component and per model, today
   and the month so far, tokens and cost; a click on a column header
   sorts. `~` and *estimated* mark requests whose answer ended without the
   provider's own count; *unknown models* is every name neither a
   provider's list nor the configuration has; *unpriced* requests count
   for no cost limit.

What a write did beyond what was asked — a key removed with its address —
is shown at the top of the page until the next write.

A setting your host's deployment file holds (§The host's file, below) is
read-only, marked **Set by your host**; a connector the host locks entirely says so at the top of the
page.

The same from a terminal:

```sh
bx llm upstream add openai https://api.openai.com/v1     # asks for the key (never on the command line)
bx llm upstream test openai gpt-5                        # a one-line call through it
bx llm default set openai/gpt-5                          # the model of a call that names none
bx llm default set openai/gpt-5-mini --role harness      # xbin/harness
bx bind apps/agent llm=xbin:llm                          # let a tile use it
bx llm                                                   # what is in force
```

An **upstream** is one provider: a name (`openai`, `local`, …), the base URL
of its OpenAI-compatible API (with or without `/v1`), its key, and
optionally:

- `models` — the models offered through it. Empty (the default) passes the
  provider's own `/models` list and any model name through.
- `private` — marks an upstream that keeps prompts inside your
  organisation. Nothing filters on it yet; org constraints will.
- `prices` — `{<model>: {in, out, cachedIn?}}` in USD per million tokens,
  for the cost counters and cost quotas. `"gpt-5*"` prices a family. A
  provider that reports the cost of a call itself (OpenRouter) is believed
  when no price is set.

Upstream URLs come only from this configuration, never from a request, and
a provider's redirect is never followed. A key is only ever sent to the
address it was set for: change an upstream's scheme, host or port without
giving its key again and the stored key is removed (the answer's `notices`
say so).

`bx llm upstream test` makes the smallest call there is (one word in, one
token out). It is a call like any other: metered to you, held by your
quota and the workspace's, and refused while the connector is off.

### Model names

A model is named `<upstream>/<model>`. What the `model` of a request
resolves to, first match wins:

1. no model, or `xbin/default` → the default model; `xbin/<role>` → that
   role's model (`agent`: the workspace agent, `harness`: coding harnesses,
   or any role you add), else the default;
2. `<upstream>/<model>` → that upstream. With a `models` list the model
   must be on it; without one the provider judges the name;
3. a bare name on exactly one upstream's `models` list → that upstream;
4. an alias → its target;
5. any other name, when the workspace has a single upstream that passes
   names through → that upstream;
6. anything else → the alias `*`, when you set one;
7. otherwise the call is refused with `404 model_not_found`.

An unknown name is never silently mapped to the default: a client that asked
for one model isn't billed for another unless an admin said so. Coding CLIs
send their own model names; map them with aliases
(`bx llm alias set gpt-6-astra openai/gpt-5`), or catch every unknown name
with `bx llm alias set '*' openai/gpt-5`.

An alias adds a name; it never takes one over. A name under an upstream
(`openai/…`) or on an upstream's `models` list always means that model, so
an alias can't be given such a name (400), and one that ends up shadowed —
an upstream added later, by you or by your host — is simply never used
(the configuration's `problems` say so).

A model name is at most 200 characters, without spaces. On an upstream that
passes names through, a call is metered and logged under its model's name
only when the upstream's own `/models` list has it; every other name —
whether the provider answered it or refused it — is counted together as
`<upstream>/(unknown)`. A caller chooses what it asks for, never how many
rows your usage has.

`GET /api/xbin/llm/v1/models` lists every upstream's models, the aliases
and `xbin/default`, `xbin/<role>`.

### Who may call

- **A tile**, when one of its `openai` slots is bound to `xbin:llm`. The
  binding is the authorization, as for any provider; nothing else lets tile
  code call the connector. Binding it is a workspace admin's act (its calls
  spend the workspace's quota), and pickers offer it once an upstream
  exists.
- **A person directly** (a browser session, the app, `bx` or `curl` with
  their own sign-in), by the **people switch**: `admins` (the default),
  `everyone` or `nobody` — `bx llm people everyone`.
- **An LLM key** — a narrow credential good for these routes only
  ([auth.md](/docs/auth.md) §LLM keys). xbind gives one to each agent
  session and terminal it points at the connector, and a bound tile asks
  for one for a coding tool it runs in a sandbox (§Coding agents and
  terminals). A key stops when its person is disabled or deleted, loses
  the tile it was made for, or — a key a tile asked for under its binding
  — the tile is unbound.

`bx llm off` stops every call; `bx llm on` resumes.

**One caller, so many calls at once.** A person (or, for calls nobody acts
in, a tile) may have 16 calls in flight; the next one is answered `429
xbin_llm_busy` with `Retry-After: 1`. `bx llm concurrency <n>` changes it.

**A tile's non-primary deployments** ([tile-deployments.md](/docs/tile-deployments.md))
use the binding by the tile's edge policy, like a slot bound to a provider
tile ([auth.md](/docs/auth.md), the edge policy): `read` by default — they
can list models and read the connector's status, and their calls are
refused (`403 xbin_llm_edge`) — or `block`, which a tile manager sets in
the Deployments panel. Run model calls from the primary.

**Adding an upstream is an admin's power over the host's network.** xbind
itself calls the address you give — a provider on the internet, a model
server on the LAN, something on the host's loopback — and relays what it
answers to whoever may call the connector. Private and loopback addresses
are allowed on purpose (local model servers are a main use), so treat "may
add upstreams" as "may make xbind reach that address". A host that doesn't
want its workspace admins to have it sets `"lockAll": true` in its file
(below): then nobody adds or changes an upstream through the API.

## Using it from a tile

Declare a slot for the `openai` service and let an admin bind it — the same
manifest a tile writes for `llm-gw`:

```jsonc
"interfaces": { "llm": { "kind": "http", "service": "openai" } }
```

Bound to `xbin:llm`, the tile gets the connector's address the way it gets
any provider's ([overview/11-interfaces.md](/docs/overview/11-interfaces.md)):
`XBIN_IFACE_LLM_URL=http://xbin/api/xbin/llm` in its backend (a multi slot:
a `{provider: "xbin:llm", url, service}` row in `XBIN_IFACE_LLM`),
`xbin.iface('llm').url` = `/api/xbin/llm` in its frontend. Append
`/v1/chat/completions`, `/v1/responses` or `/v1/models`, and call it with
the SDK's client (`xbin.Client()`) or `xbin.fetch` — the tile's own
credential is the key; send no other.

```go
req, _ := http.NewRequestWithContext(ctx, "POST", os.Getenv("XBIN_IFACE_LLM_URL")+"/v1/chat/completions", body)
req.Header.Set("Content-Type", "application/json")
resp, err := xbin.Client().Do(req) // streams as the model answers
```

- The body is OpenAI's: every member you send reaches the provider (written
  anew, each once — a member sent twice goes out with its last value). Two
  are rewritten: `model` (to the provider's own name) and, on a streamed
  chat completion, `stream_options.include_usage` (always true — the stream
  ends with a usage chunk whose `choices` is empty). `stream` must be
  `true` or `false`; a Responses call with `background: true` is refused,
  since its answer — and its usage — would never pass through.
- The answer streams as it arrives. Closing the request ends the provider's
  call a few seconds later (see Usage: a call you abandon is still
  metered).
- Errors are OpenAI-shaped (`{"error": {"message", "type", "code"}}`), so an
  OpenAI SDK pointed at the URL shows them.
- `GET <url>/preferred?use=<role>` answers `{use, model}` — the convention
  `llm-gw` has, so a tile that asks its provider for the default model works
  bound to either.
- The Go types for both wires, usage and prices are in the SDK:
  `github.com/xbin-dev/xbin/sdk/llm`.

Each call is metered to **the person acting** and to **the tile**: the
person of a partition's backend ([partitions.md](/docs/partitions.md)), the
person driving a frame, a terminal or an agent session. A backend that
serves everyone (not a person's partition) acts for nobody: its calls are
metered to the tile and held by the workspace quota only.

## Coding agents and terminals

Once the connector serves calls, the coding tools people run on tiles are
pointed at it — no sign-in, no setup — unless the person has a sign-in of
their own, which keeps working.

```sh
bx llm default set openai/gpt-5-mini --role harness   # the model harnesses start on
bx llm sessions                                       # what is in force
bx org agents --credentials connector                 # the connector, whatever people are signed in to
bx llm sessions agents=off                            # agent sessions get nothing of it
bx llm sessions terminals=off openai-env=on           # the terminal switches
```

### Agent sessions

An agent session (the Agent tab, `bx agent run`) starts its CLI with the
connector in its environment. What each harness gets:

| Harness | Through the connector | What xbind sets |
|---|---|---|
| OpenCode | yes — beside the person's own providers | an inline config (`OPENCODE_CONFIG_CONTENT`) declaring the provider **LLM by xbin** with the connector's models, so they can always be picked; its `model` — what makes it the session's default — only when the connector serves the session |
| Codex | yes — in place of a sign-in | codex-acp's gateway sign-in (`DEFAULT_AUTH_REQUEST`) and the model (`CODEX_CONFIG`), only when the connector serves the session |
| Claude Code | not yet | nothing: it speaks Anthropic's Messages wire, which the connector doesn't relay yet. It runs on the person's own sign-in |
| Gemini CLI | no | nothing: it has no OpenAI-compatible mode. Own sign-in only |

**Who serves a session**, first that says so:

1. the session's own choice — `llm: "connector" | "own"` on `POST
   /term/sessions`, `bx agent run --llm`;
2. the person's saved choice for that harness (`credentials` in their
   agent preferences, [protocol/agents.md](/docs/protocol/agents.md)
   §Agent preferences);
3. the workspace's credential mode (the agents policy, `bx org agents
   --credentials`; [auth.md](/docs/auth.md) §Agent constraints):
   `own-first` (the default) — the person's own sign-in when the session
   finds one in their `$HOME` (`opencode auth login`, `codex login`; a
   provider key in the environment counts), else the connector;
   `connector-first` — the connector, unless a person or a session says
   their own.

`connector-only` is not a default but a rule: the connector serves,
whatever a person is signed in to or a session asks for, and a harness the
connector can't serve doesn't start (403, with the reason). The workspace
can set it, and **an org's model rule sets it on the org's tiles**: where
only private models (or a list of them) may see a tile's code, a coding
agent on its own sign-in would send that code to a model xbind can't see.
The connector then holds the rule on every call: a session's key carries
its tile, and a call for a model the tile's orgs don't allow is refused
(`403 xbin_llm_policy`), whatever the CLI asks for; the models a session is
offered, and the one it starts on, are the allowed ones.

The `agents` switch (`bx llm sessions agents=off`) takes the connector away
from agent sessions altogether.

So with `own-first` a person who signed OpenCode in keeps their models as
the default and gains the connector's in the picker, and a person with no
sign-in gets the connector's model — never, silently, OpenCode's own free
third-party models. Asking for the connector where it can't serve (Claude
Code, no default model, a session with no network or one that targets a
tile deployment other than the primary) is refused with the reason; it is
never dropped quietly.

The session says what it runs on: a first line in the transcript when the
connector serves it, `llm: "connector" | "own"` in its row, and `GET
/agent/providers` tells a launcher beforehand, per harness
([protocol/agents.md](/docs/protocol/agents.md)).

The default model is `xbin/harness` — the `harness` role's model, else the
default model, else the first model an upstream lists — resolved when the
session starts. Codex has no metadata for a model it doesn't know and says
so in its answers ("Model metadata … not found"); its own model names stay
in its picker and work when an upstream serves them or an alias maps them
(§Model names).

### Terminals

A tile terminal gets, while the `terminals` switch is on (the default):

- `XBIN_LLM_URL` — the connector's OpenAI base URL as the terminal reaches
  it (`$XBIN_URL/api/xbin/llm/v1`);
- `XBIN_LLM_KEY` — the terminal's own LLM key.

```sh
curl -s "$XBIN_LLM_URL/chat/completions" -H "Authorization: Bearer $XBIN_LLM_KEY" \
  -H 'Content-Type: application/json' -d '{"messages":[{"role":"user","content":"hello"}]}'
OPENAI_BASE_URL=$XBIN_LLM_URL OPENAI_API_KEY=$XBIN_LLM_KEY some-tool   # one tool, on purpose
```

`OPENAI_BASE_URL` and `OPENAI_API_KEY` — the pair most tools read — are set
only with the `openaiEnv` switch (`bx llm sessions openai-env=on`), off by
default: everything started in the terminal inherits its environment, and a
tool that reads the key but not the base URL sends the key to OpenAI. A
person whose terminals hold no tile API token (no `termApi` grant) gets no
LLM key in one either; a guided sign-in's shell gets none.

### The key

Every session has an **LLM key** of its own (`xbl_…`,
[auth.md](/docs/auth.md) §LLM keys) — never its terminal token, which is
the tile's API credential:

- good for `/api/xbin/llm/v1/…` only; anywhere else it is a `401`;
- metered to the person who opened the session and to the tile, and held
  by that person's quota;
- minted when the session opens, revoked when it ends — closed, crashed,
  restarted (the new session has a new key), xbind stopped. A resumed
  session gets a fresh one;
- an agent session's is in the agent's own environment only — not in the
  sandbox's, and not in the terminals the agent opens through xbind.
  Where the person's own sign-in serves a harness that takes the connector
  *in place of* a sign-in (Codex), the key is revoked at once.

Coding tools keep things: **Codex writes its environment, the key in it, to
`~/.codex/shell_snapshots/`**, and xbind does not clean a person's home.
What stays there is a key that stopped working when its session ended; for
as long as the session lives, whatever else reads that home (the person's
other terminals) could spend their quota with it — which a terminal of
theirs can already do with its own key.

What xbind itself keeps of a session's output masks LLM keys by their
shape (`xbl_[redacted]`): the session's log (`GET
/api/xbin/term/sessions/<id>/log` — the agent's stderr, where a hook may
print the environment) and the last lines xbind logs of a session that
died at start. A conversation's transcript is the person's own and is not
rewritten.

### The network path

The connector is reached at xbind's own address (`XBIN_URL`), which a
session's sandbox reaches whatever network scope it has — the tile's `net`
rules never block it — except one: a session with **no network** (`net:
none`) has no route to xbind at all. It gets no key; an agent session says
so in its first line and runs on the person's own sign-in.

A session that targets a **tile deployment other than the primary**
([tile-deployments.md](/docs/tile-deployments.md)) gets no key either, and says so
the same way: a key carries no deployment, so the tile's edge policy —
which decides what a non-primary deployment may use — could not be held to
its calls.

When the connector refuses an agent session's call — a quota reached
(`xbin_llm_quota_exceeded`), too many calls at once (`xbin_llm_busy`), a
model nothing serves (`model_not_found`), a provider's own error — xbind
says so in the session, as a line of its own with the code and the
message: a coding CLI often keeps such an answer to itself (OpenCode waits
out a `429`'s `Retry-After` in silence, until midnight UTC for a daily
quota). The line is said once per kind while the CLI keeps retrying; the
turn is the person's to cancel. A terminal's tools show the answer
themselves.

### Sandboxes made by a sandbox manager

A coding agent the agent template runs lives in a sandbox a manager tile
made ([sandbox-manager.md](/docs/sandbox-manager.md)), which by design
reaches nothing of xbind. Two things connect it:

- **the sandbox** is created with the connector asked for (`llm: true` in
  the manager's create request; on xbind's own runtime `net.llm`). xbind
  then forwards one port of the sandbox's gateway —
  `http://10.0.2.2:4180/v1` — to its **LLM-only listener**: the three
  model routes for an LLM key, nothing else of xbind, with any egress
  class, `none` included. The sandbox's row names the address (`llm.url`);
  a manager or an xbind that doesn't know the field leaves it out, and the
  consumer runs the agent on its own sign-in;
- **the key** is the consumer's to ask for: a tile whose `llm` slot is
  bound to `xbin:llm` calls `xbin.LLMKeyFor` ([sdk.md](/docs/sdk.md); `POST
  /api/xbin/llm/keys`) for the person it acts for, hands the key to the
  agent as an exec's environment, and revokes it when the run ends.

The agent template does both: bind its `llm` slot to `xbin:llm` and a
coding agent with no sign-in of its own — none saved by the person, none in
the sandbox — starts on the connector.

## Usage

```sh
bx llm usage                       # this month so far, per day
bx llm usage --month --user bob    # per month, one person (admins)
```

Counters per UTC day, person, tile and model: requests, input and output
tokens, cached and reasoning tokens, and cost where the model has a price.
Only counts are kept — never a prompt, an answer or a key. A person reads
their own rows (`GET /api/xbin/llm/usage`), a workspace admin everyone's.
They live in `data/llm/usage/<YYYY-MM>.json`, 24 months of them.

**A call you abandon is not free.** Providers report a call's usage at the
end of its answer and bill what they generated. So when a client goes away
(or stops reading for two minutes), the connector keeps reading the
provider's answer for a grace of 5 seconds (`XBIN_LLM_CANCEL_GRACE`) to
catch that usage, then ends the provider's call. If the usage came, the
call is metered exactly. If it didn't, the call is charged an **estimate**
— one input token per four bytes of the request, one output token per
stream event relayed (per four bytes of a plain answer) — and the row
counts it under `estimated`. The same estimate covers a provider that
reports no usage at all. Quotas see estimates like any other use.

**Backups.** `data/llm/` — the settings, the sealed keys and the usage
files — is the daemon's own state, like the rest of `data/` outside tiles'
resources: no tile backup contains it, and restoring a tile changes none of
it. Whoever backs up the workspace directory backs it up. After restoring
that directory, settings and usage are as they were; `keys.json` opens only
with the vault barrier it was sealed under (the same `data/vault/`
restored, unsealed with its passphrase) — otherwise upstreams read "its key
is missing" and the keys are set again.

## Quotas

```sh
bx llm quota set default tokens/day=2M cost/month=50     # each person
bx llm quota set user:bob tokens/day=unlimited           # one person
bx llm quota set org:interns tokens/day=200k action=warn # an org's members
bx llm quota set workspace cost/month=500                # everyone together
bx llm quota show bob                                    # bob's limits as they stand
```

A quota limits tokens (input + output) and cost (USD) per UTC day and per
UTC month. Reaching a limit either **stops** the person's calls (the
default) — `429` in OpenAI's error shape, naming the quota and when it
resets — or **warns**: the call goes through with an `X-XBin-LLM-Quota`
header, and the person's status (`bx llm`, `GET /api/xbin/llm`) shows it.

Whose quota applies to a person, each limit on its own:

1. their own override (`user:<id>`), whichever way it points;
2. else their orgs' overrides: the **most generous** any of their orgs
   sets for that limit (`unlimited` beats every number);
3. else the default.

An org override replaces the default for its members; a person in several
orgs gets the best of them, as with access. The `workspace` quota is checked
for every call as well, and is the only one for calls nobody acts in.

A quota is checked before a call starts, against what is already metered:
the calls in flight when a limit is reached finish. A limit can therefore
be passed by at most what one caller's calls in flight use — 16 calls by
default (`bx llm concurrency`).

**Cost quotas and models without a price.** A cost limit counts the calls
whose model has a price: the upstream's `prices` (an entry `"*"` prices
every model the table doesn't name: `bx llm upstream set openai --price
'*=5:15'`), else what the provider itself reported. A call on a model with
no price at all counts for nothing toward a cost limit; it is marked
`unpriced` in usage, token limits still hold it, and while cost limits are
in force and such calls exist this month, workspace admins see one alert
naming the models.

### When a limit is reached

- **The person** reads it on `/xbin/llm` — their own page shows each limit
  as a bar and says in a sentence what ran out and when it is back — and
  in the refusal of their call (`429`, a stopping limit) or its
  `X-XBin-LLM-Quota` header (a warning one). `GET /api/xbin/llm` answers a
  person's own `quota` whether or not the people switch admits their
  direct calls: the calls tiles make for them count against the same
  limits.
- **The workspace's admins** get an alert (`GET /api/xbin/alerts`, kind
  `llm-quota`; a banner in an admin's shell, the admin console's alert bar)
  naming
  the person, the limit, where it comes from — their own quota, an org's,
  the default — whether their calls are refused or only warned about, when
  it resets, and `/xbin/llm`. A workspace quota that refuses every call is
  critical. More than five people at a limit are one alert. It goes when
  the window resets or the quota is raised.

## Keys and the vault

A key an admin sets is stored in xbind's vault — sealed in
`data/llm/keys.json` under the vault barrier ([auth.md](/docs/auth.md)
§Encryption at rest) — and never answered by any API: reads show whether a
key is set and, while the vault is open, its last four characters.

| Vault | What the connector does |
|---|---|
| open | keys are read and stored |
| sealed | calls to an upstream whose key is in the vault answer `503 xbin_vault_sealed` until an admin unseals; setting a key is refused; nothing decrypted is kept across a seal. Upstreams of the host's file keep working (their keys are files) |
| not set up (production before `bx vault unseal`) | as sealed; a key is never written in the clear where a vault is meant to exist |
| plaintext (`--insecure-vault`, `--no-auth`) | keys are kept in the clear, mode 0600, like every vault file in that mode, and sealed by the first read after a vault is set up |

No request or response body is ever logged, and no key. A provider's
refusal of the workspace's key reaches the caller as a `502` of the
connector's own, never as the caller's `401`.

## The host's file (`XBIN_LLM_CONFIG`)

Whoever runs xbind can configure the connector from a file, and what the
file sets is **locked**: the page and the admin console show it read-only
with **Set by your host** and its controls disabled, and the admin API
refuses to change it. A hosted instance uses it to
bring its own models and hold quotas the workspace's admins can't lift.

```jsonc
// XBIN_LLM_CONFIG=/etc/xbin/llm.jsonc
{
  "people": "everyone",
  "upstreams": {
    "house": {
      "baseURL": "https://llm.internal.example/v1",
      "keyFile": "llm.key",              // relative to this file; one line
      "models": ["gpt-5", "gpt-5-mini"],
      "private": true,
      "prices": { "gpt-5": { "in": 1.25, "out": 10, "cachedIn": 0.125 } }
    }
  },
  "defaults": { "model": "house/gpt-5", "roles": { "harness": "house/gpt-5-mini" } },
  "aliases": { "*": "house/gpt-5" },
  "sessions": { "agents": true, "openaiEnv": false },
  "quotas": {
    "workspace": { "costPerMonth": 500 },
    "default": { "tokensPerDay": 2000000 }   // a ceiling: admins may narrow it per user or org
  }
  // "lockAll": true                      // every setting is the host's
}
```

- The settings are the admin API's (`enabled`, `people`, `upstreams`,
  `defaults`, `aliases`, `sessions`, `quotas`), with a key named by file
  (`keyFile`).
- **What it sets is locked, piece by piece**: `enabled`, `people`, each
  upstream it defines, the default model, each role and alias it names,
  each `sessions` switch it sets (`agents`, `terminals`, `openaiEnv`),
  and each part of `quotas` it mentions (`workspace`, `default`, `users`,
  `orgs` — an empty `{}` locks that part with nothing in it). What it
  leaves out stays the admin's: their own upstreams, other aliases, the
  quota parts it didn't mention.
- `"lockAll": true` makes every setting the host's: the admin's own
  settings and upstreams are not in force, and what the file leaves out
  keeps its default.
- **The host's quotas are a ceiling.** `quotas.workspace` holds everyone
  and nothing lifts it. `quotas.default` holds each person, and the
  workspace's admins can only narrow it: a per-user or per-org entry of
  theirs that is lower applies (it may warn at its own value; the host's
  limit still stops), one that is higher or `unlimited` gives nothing. The
  host's own `users` and `orgs` entries are the host's to give and may
  exceed its default.
- `concurrency` (calls in flight per caller) is lockable like the rest.
- The file is read at boot — xbind does not start with a file it can't
  read, so a host never runs without the limits it wrote — and again when
  it or a key file changes (looked at every couple of seconds while the
  connector is used). A change that is invalid is refused: the last good
  file stays in force, xbind's log says why, and the admin's view says the
  host's configuration could not be read.
- Reads mark each locked setting: `locks: {"defaults.model": {"locked":
  true, "reason": "set by your host"}}`; a write to one answers `409` with
  the settings it named.

## Limits today

- One upstream kind: OpenAI-compatible APIs. Anthropic's Messages wire is
  not relayed yet; Claude models are reachable through a provider that
  serves them on the OpenAI wire.
- The `llm-gw` tile can't be an upstream yet (the connector has no identity
  to call a tile with); bind tiles to one or the other.
- The connector doesn't retry: a provider's `429` or `5xx` passes with its
  `Retry-After`, and the client's own retry applies.
- Responses calls run in the background (`background: true`) are refused.
- Claude Code and Gemini CLI can't run on the connector yet (their wires
  aren't relayed), and a session with no network can't reach it.
- A person is told about their quota on `/xbin/llm` and in their calls'
  answers; their shell has no banner for it (the alert is the admins').
- Calls a tile makes for nobody in particular are held by the workspace
  total only; usage lists them as their own row, *no person*.
