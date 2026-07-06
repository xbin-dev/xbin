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

## Roles

| Role   | Grants |
|--------|--------|
| reader | `GET /v1/models` — list available models (real + aliases) |
| writer | Everything under `/v1/` — chat/completions, completions, embeddings, etc. (implies reader) |

## Endpoints

### GET /v1/models — role: reader

```json
{"object":"list","data":[
  {"id":"best-model","object":"model","owned_by":"alias","alias_of":"lab/strong-model-4.8"},
  {"id":"lab/strong-model-4.8","object":"model","owned_by":"lab"}
]}
```

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

`/config` (`GET`/`PUT`, plus `/config/backend` add/remove) and `/stats`
(per-backend usage counters) are the tile's own settings endpoints — gated
to `admin`, i.e. only the tile's own frontend (self is always admin of
itself) or the workspace owner. They are not part of the reader/writer
surface and granting `writer` does not expose them.
