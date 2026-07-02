# apps/llm-gw API

An OpenAI-compatible LLM gateway. Configure a base URL and upstream API
token on the tile itself; other tiles get a grant to call the proxied
`/v1/*` surface exactly like the real OpenAI API (or any OpenAI-compatible
provider — Ollama, OpenRouter, a local vLLM server, etc).

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
`/v1/embeddings`, …) is proxied byte-for-byte to `<baseURL><path>` with the
configured upstream token attached. Streaming (`"stream":true`, SSE)
responses pass through as they arrive. If the request body is
`application/json` and has a top-level `"model"` field that matches a
configured alias, it's rewritten to the alias's target before forwarding —
otherwise the model id is passed through untouched.

No token configured ⇒ `502` with an error body instead of a broken upstream
call.

## Use it

```jsonc
// caller's buxon.json
{ "uses": [{ "target": "apps/llm-gw", "role": "writer" }] }
```

```js
const r = await buxon.fetch('/api/apps/llm-gw/v1/chat/completions', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ model: 'best-model', messages: [{ role: 'user', content: 'hi' }] }),
});
```

```go
resp, _ := buxon.Client().Post("http://buxon/api/apps/llm-gw/v1/chat/completions",
    "application/json", body)
```

## What's not proxied

`/config` (`GET`/`PUT`) is the tile's own settings endpoint — gated to
`admin`, i.e. only the tile's own frontend (self is always admin of itself)
or the workspace owner. It is not part of the reader/writer surface and
granting `writer` does not expose it.
