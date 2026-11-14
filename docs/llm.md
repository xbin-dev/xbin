# The LLM connector

xbind has an LLM connector built in (D226): one OpenAI-compatible endpoint
in front of the model providers a workspace uses, so its tiles, agents and
coding tools share one LLM setup — the keys in one place, every call metered
to the person it was made for, quotas where an admin or a host wants them.

It is off until someone configures it. A workspace that never adds an
upstream sees nothing new: the routes answer "not configured" and no picker
offers the connector.

- endpoint: `/api/xbin/llm/v1` — `chat/completions`, `responses`, `models`
  ([protocol/llm.md](/docs/protocol/llm.md))
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
   **Test** makes a one-line call through the provider and answers in the
   row.
3. **Models** — the default model, the model of each role (`xbin/agent`,
   `xbin/harness`, or a role you add) and aliases; one *Save models*.
4. **Who may call directly** — admins (the default), everyone, nobody.
5. **Quotas** — the workspace total, the default for each person, and
   overrides per person and per organisation; each limit *stops calls* or
   *warns only*. A person's quota is also in the console's users → *LLM
   quota…*, an org's on its page.
6. **Usage** — this month per person and per component, today and the
   month so far, tokens and cost; a click on a column header sorts.

A setting your host's deployment file holds (§The host's file, below) is
read-only, marked **Set by your host**; a connector the host locks entirely says so at the top of the
page.

The same from a terminal:

```sh
bx llm upstream add openai https://api.openai.com/v1     # asks for the key
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
a provider's redirect is never followed.

### Model names

A model is named `<upstream>/<model>`. What the `model` of a request
resolves to, first match wins:

1. no model, or `xbin/default` → the default model; `xbin/<role>` → that
   role's model (`agent`: the workspace agent, `harness`: coding harnesses,
   or any role you add), else the default;
2. an alias → its target;
3. `<upstream>/<model>` → that upstream. With a `models` list the model
   must be on it; without one the provider judges the name;
4. a bare name → the one upstream that lists it; or, when the workspace has
   a single upstream that passes names through, that upstream;
5. anything else → the alias `*`, when you set one;
6. otherwise the call is refused with `404 model_not_found`.

An unknown name is never silently mapped to the default: a client that asked
for one model isn't billed for another unless an admin said so. Coding CLIs
send their own model names; map them with aliases
(`bx llm alias set gpt-6-astra openai/gpt-5`), or catch every unknown name
with `bx llm alias set '*' openai/gpt-5`.

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
  ([auth.md](/docs/auth.md) §LLM keys), for coding tools in terminals and
  agent sessions. Nothing hands them out yet.

`bx llm off` stops every call; `bx llm on` resumes.

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

- The body is OpenAI's, passed through as sent. Two members are rewritten:
  `model` (to the provider's own name) and, on a streamed chat completion,
  `stream_options.include_usage` (always true — the stream ends with a
  usage chunk whose `choices` is empty).
- The answer streams as it arrives; closing the request ends the provider's
  call.
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

A call a client abandons mid-answer is counted as a request without tokens:
the provider reports usage at the end of an answer.

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
the calls in flight when a limit is reached finish.

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
  "quotas": {
    "workspace": { "costPerMonth": 500 },
    "default": { "tokensPerDay": 2000000 },
    "users": {}                           // present, so locked: no per-user overrides
  }
  // "lockAll": true                      // every setting is the host's
}
```

- The settings are the admin API's (`enabled`, `people`, `upstreams`,
  `defaults`, `aliases`, `quotas`), with a key named by file (`keyFile`).
- **What it sets is locked, piece by piece**: `enabled`, `people`, each
  upstream it defines, the default model, each role and alias it names, and
  each part of `quotas` it mentions (`workspace`, `default`, `users`,
  `orgs` — an empty `{}` locks that part with nothing in it). What it
  leaves out stays the admin's: their own upstreams, other aliases, the
  quota parts it didn't mention.
- `"lockAll": true` makes every setting the host's: the admin's own
  settings and upstreams are not in force, and what the file leaves out
  keeps its default.
- A quota a host must be able to hold against the workspace's admins is
  `quotas.workspace` (nothing lifts it), or `default` together with
  `"users": {}` and `"orgs": {}`.
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
- Nothing injects the connector into terminals or agent sessions yet.
- A person is told about their quota on `/xbin/llm` and in their calls'
  answers; their shell has no banner for it (the alert is the admins').
- Calls a tile makes for nobody in particular are held by the workspace
  total only; usage lists them as their own row, *no person*.
