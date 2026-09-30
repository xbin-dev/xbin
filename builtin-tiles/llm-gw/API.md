# apps/llm-gw API

An OpenAI-compatible LLM gateway multiplexing **multiple named backends**
(OpenAI, OpenRouter, a LAN Ollama/vLLM, …). Add backends — a name, base URL
and API token each — on the tile itself; other tiles get a grant to call the
proxied `/v1/*` surface exactly like the real OpenAI API. With more than one
backend, model ids are namespaced `<backend>/<model>` and requests route by
that prefix (a single backend keeps bare ids, so nothing changes until you
add a second). The tile shows per-backend usage: requests, tokens in/out,
and in-flight calls.

Model **aliases** let you rename upstream models for callers: set
`best-model -> lab/strong-model-4.8` on the tile, and any caller that sends
`{"model":"best-model", ...}` gets transparently routed to
`lab/strong-model-4.8`. Aliases also show up in `GET /v1/models` so callers
can discover them.

**Preferred models.** The tile has one dropdown per **use-type** — `agent`,
`chat`, `pipeline`, `vlm`, `coding`, `summarizing` — naming the workspace's
default model for that job. Callers resolve their default from `GET /preferred`
so the choice lives in one place and swaps everywhere at once (the agent, for
instance, fills any empty model tier from here).

**Cost & metrics.** Set per-model prices (`$/1M` tokens, input/output) and the
tile tracks `$` per backend alongside token counts. `GET /metrics` renders the
same counters in Prometheus text format — bind it into a prometheus-viewer tile
via the `metrics` interface (service `prometheus`).

**Reliability.** Transient upstream failures (`429`, `5xx`) are retried with
jittered backoff (honoring `Retry-After`) while nothing has been sent to the
caller yet, so a rate-limit blip doesn't surface as an error. Streaming is
unaffected (retries happen before the first byte).

## Partitioned callers

llm-gw isn't partitioned: one instance, one set of upstream keys, serving
every caller. A **partitioned** tile ([/docs/partitions.md](/docs/partitions.md))
— the agent template's instances are, by default — calls it from each
person's partition and from its global instance, and xbind says which on
every call (`X-XBin-Partition`, `X-XBin-Partition-Id`).

**Usage by partition.** Such calls are counted per caller — the calling
tile, its deployment and the person's partition (its `X-XBin-Partition-Id`;
the global instance is its own row) — beside the per-backend counters:
requests, tokens in/out and cost. Metadata only: no prompt, answer or
model is kept. The page shows them under **usage by partition**, to each
viewer what is theirs to see:

- a person who opens the page: their own partitions' rows (with the last
  use and the calls in flight);
- a manager of this tile (write or terminal access): each calling tile's
  people's partitions **together** — how many, requests, tokens, cost — and
  its global instance's row, besides their own; never another person's row
  or partition id. A workspace admin signed in as themselves sees this
  view too (a tile learns a person's level on it, not whether they are an
  admin); per person and per day, admins read each person's calls to this
  tile in xbind's egress ledger (`GET /api/xbin/partitions/ledger`,
  [/docs/partitions.md](/docs/partitions.md));
- the owner token (or the page opened with it): every row, a person's
  with its counters only — not when it was last used nor its calls in
  flight, so polling the page never draws anyone's activity over time;
- viewing the workspace as someone: none.

`GET /metrics` stays per backend: a metrics reader never sees who used
what. A call from a tile that isn't partitioned is counted per backend
only, as before, and until a partitioned tile calls, nothing on the page or
in `GET /stats` changes. A person deleted and recreated under the same id
(or whose partition was reset) calls with a new partition id: their row
starts afresh and the old one is dropped then. At most 1000 rows are kept,
the least recently used going first.

**Fairness limit** (off by default). `partitionLimit` caps how many calls one
person's partition of a calling tile may have in flight at once; more wait
their turn, in order, for **at most 20 s** without an answer, and are then
answered `429` with `Retry-After: 2` — well before a caller's own watchdog
(the agent's gives up on a stream with no bytes for 90 s, headers
included) would take a held call for a stalled one. The agent retries a
`429` (three times, backing off), so a call held past that fails its turn
naming the limit. Other people's partitions, the global instance and tiles
that aren't partitioned are never held by it. The agent already caps its
model calls tile-wide (its API.md) and each person's partition to two at
once, so upstream concurrency doesn't grow with the number of people, and
one person can't take more than half of its slots: for the agent the limit
matters only below 2, and a call it holds keeps one of the agent's
tile-wide slots idle while it waits. It is for partitioned callers without
such caps of their own.

**Who is in whose trust base.** Every prompt a person's partition sends
passes through here, so whoever can change this tile's code — its writers
and every admin — can read what the partitions bound to it send. The trust
panel of each partitioned tile bound to it names this tile and its writers,
a writer of this tile who isn't an admin shows there as a trust warning
while this tile (llm-gw) runs its saves live, and **reviewed code only**
on such a tile needs this tile's primary protected too
([/docs/partitions.md](/docs/partitions.md) §Operating people's
partitions). Binding llm-gw into a partitioned tile is
a **global bind**, made by whoever may bind it today — an admin, an org
admin within their org, a personal tile's owner to what they own (D33,
D88) — and every person's partition then uses it. A person who'd rather
their prompts went elsewhere can **personal-bind** their own
OpenAI-compatible tile into their own partition (`bx bind --personal
apps/agent llm=users/<them>/<gateway>`,
[/docs/partitions.md](/docs/partitions.md) §Bind types); it serves only
them.

## Roles

| Role   | Grants |
|--------|--------|
| reader | `GET /v1/models` (list models), `GET /preferred` (workspace default models), `GET /metrics` (Prometheus) |
| writer | Everything under `/v1/` — chat/completions, completions, embeddings, etc. (implies reader) |

## Endpoints

### GET /v1/models — role: reader

```json
{"object":"list","data":[
  {"id":"best-model","object":"model","owned_by":"alias","alias_of":"lab/strong-model-4.8"},
  {"id":"lab/strong-model-4.8","object":"model","owned_by":"lab"}
]}
```

### GET /preferred[?use=&lt;type&gt;] — role: reader

The workspace's preferred model per use-type. Without `?use=`, returns the whole
map; with it, just that one — a caller does `GET /preferred?use=agent` to learn
which model to default to.

```json
{ "preferred": { "agent": "lab/strong-model-4.8", "summarizing": "gpt-4o-mini" },
  "useTypes": ["agent","chat","pipeline","vlm","coding","summarizing"] }
```

### GET /metrics — role: reader

Per-backend counters in Prometheus text format: `llmgw_requests_total`,
`llmgw_tokens_in_total`, `llmgw_tokens_out_total`, `llmgw_cost_usd_total`
(all `{backend="…"}`-labelled counters) and `llmgw_active_requests` (gauge).

### /v1/* — role: writer

Anything else under `/v1/` (`/v1/chat/completions`, `/v1/completions`,
`/v1/embeddings`, …) is proxied byte-for-byte to the routed backend's
`<baseURL><path>` with that backend's token attached. Routing: the JSON
`"model"` field is resolved alias → `<backend>/<model>` prefix → the
default backend, and rewritten to the upstream's bare id on the way out.
Streaming (`"stream":true`, SSE) responses pass through as they arrive.

No token configured for the routed backend ⇒ `502` with an error body
instead of a broken upstream call.

## Use it

```jsonc
// caller's xbin.json
{ "uses": [{ "target": "apps/llm-gw", "role": "writer" }] }
```

```js
const r = await xbin.fetch('/api/apps/llm-gw/v1/chat/completions', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ model: 'best-model', messages: [{ role: 'user', content: 'hi' }] }),
});
```

```go
resp, _ := xbin.Client().Post("http://xbin/api/apps/llm-gw/v1/chat/completions",
    "application/json", body)
```

## What's not proxied

`/config` (`GET`/`PUT`, plus `/config/backend` add/remove, its
`/config/backend/{name}/token` and `/config/preferred`) and `/stats`
(per-backend usage + cost) are the tile's own settings endpoints — gated to
`admin`, i.e. only the tile's own frontend (self is always admin of itself) or
the workspace owner.

- `GET /stats` is `{backends: {<name>: {reqs, tokIn, tokOut, active, cost}}}`,
  plus, once a partitioned tile has called, what the viewer may see of it
  (above): `callers` `[{from, deployment?, partition, partitionId?, reqs,
  tokIn, tokOut, cost, last?, active?, waiting?}]` (`partition` is
  `user:<id>` or `global`, `last` unix ms; `last`, `active` and `waiting`
  only on the viewer's own rows and the global instances'), a manager's
  `callerTotals` `[{from, deployment?, partitions, reqs, tokIn, tokOut,
  cost}]` (each calling tile's people's partitions together), and
  `canManage: true` when the viewer may set the fairness limit (also when
  one is set and no partitioned tile called yet).
- `PUT /config {aliases?, partitionLimit?}`: `partitionLimit` is 0 (off) to
  64 and needs write access to the tile (403 otherwise); `GET /config` names
  it only while it is set. Tokens (`PUT /config/backend {name, baseURL, token?}`,
`PUT /config/backend/{name}/token {token}` — empty deletes) are written by the
backend into its own vault, and only by a user with write access to the tile:
a tile's frontend can't reach the vault API itself (D30). They are not part of the
reader/writer surface and granting `writer` does not expose them. (`GET
/preferred` and `GET /metrics` above are the reader-visible slices.)
