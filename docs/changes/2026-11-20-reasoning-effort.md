# Reasoning effort is mapped onto each model's levels, and reasoning goes back to its model

**BREAKING for a caller that relied on its effort word reaching a model
the connector describes verbatim** — and for an upstream that refuses a
field it was never sent before. Most callers notice nothing, or a call that
used to fail now working.

## What changed

**The LLM connector** (`/api/xbin/llm/v1/…`) now knows, for many models,
which reasoning levels they take — from an admin's or the host's
description (`upstreams.<name>.reasoning`), the upstream's own model list
(DeepSeek's `effort`, OpenRouter's `reasoning`), or a table xbind ships
(DeepSeek's API, xAI Grok 4.5–4.7, Gemini 4's thinking models, Kimi K3,
GLM-5.2 and 5.3, MiniMax M3.1-Flash, Qwen4-Max and Qwen3.8, Mistral Small
4, Medium 3.5 and Large 4). For such a model:

- chat's `reasoning_effort` (and an OpenRouter-style `reasoning.effort`
  beside it) and Responses' `reasoning.effort` are rewritten to a level the
  model takes: the same word if it has it; else the provider's own alias
  (DeepSeek: `medium` → `high`); else the highest level the model has at or
  below the one asked; else its lowest. `none` stays `none` only where the
  model can be turned off; elsewhere it is the model's lowest level;
- for a model whose description says it has **no reasoning**, the effort is
  removed, and named in `X-XBin-LLM-Dropped`;
- a word that is not one of `none minimal low medium high xhigh max` is
  sent as it came;
- the answer carries `X-XBin-LLM-Effort` saying what was sent.

A model nothing describes is relayed exactly as before, byte for byte.
`GET /v1/models` carries `effort` on described models (replacing an
upstream's own member of that name, keeping its values).

**The Responses → chat translation** sends a model's own reasoning back:
the reasoning item it made of an answer (ids are now `rs_<tag>_…`) becomes
`reasoning_content` on the next assistant message of a later request, for
the same upstream and model only. Other reasoning items are still not sent,
and are now named (`input.reasoning` in `X-XBin-LLM-Dropped`). An upstream
that answers `400` to a request carrying `reasoning_content` gets the
request once more without it.

**The agent template** sends a model's plain reasoning back on later turns
(`reasoning_content`, `reasoning`, or a leading `<think>…</think>` in the
content, as the answer had it), only to the provider and model that
produced it; a provider that 400s on it gets the call once more without it,
and that agent stops sending it there until it restarts (the conversation
says so). Its default `reasoningEffort` goes only to a model that reasons by
its description; a model described as having none gets no
`reasoning_effort`, and no `reasoning` block on the Responses wire.

## Who's affected

- A tile, harness or script that sends `reasoning_effort` through the
  connector to a described model and depends on the exact word arriving —
  for example `medium` to `kimi-k3` now arrives as `low` (Kimi K3's own API
  takes low, high and max). Behind a router that serves a model differently
  from its maker's API (one that takes all seven words for `kimi-k3`, or
  refuses `low` for `glm-5.2`), xbind's table is the maker's description,
  not the router's.
- An upstream that refuses an unknown assistant-message member
  (`reasoning_content`, `reasoning`) — the agent template and the
  translation now send it to models that produced reasoning. Both recover
  by themselves (one more call without it); the log and the conversation say so.
- Code that parsed the translation's reasoning item ids (`rs_<resp>_<n>`):
  they are opaque, and now `rs_<tag>_<resp>_<n>`.

## How to migrate

Nothing, for most workspaces. Where a model's levels on your upstream
differ from what `bx llm models` says ("Reasoning: … · from xbin"),
describe it: on `/xbin/llm`, the provider's *reasoning…* → the model's
*describe…*: tick the levels it takes, whether it can be turned off, its
default — or `bx llm upstream set <name> --reasoning
'<model>=none,medium,high,xhigh/high'` (presets: `openai`, `toggle`,
`deepseek`, `none`). Your description is the whole truth for that model.
For an upstream that refuses reasoning sent back, a host's description
with `"replay": "never"` stops it for good (the agent and the translation
already recover call by call).

## Why

Effort words went through verbatim, so a model given a word it lacks
either remapped it silently (DeepSeek) or refused the call (Kimi, GLM,
MiniMax, and a non-reasoning model on a strict provider); nothing told a
person's UI which levels a model has. And DeepSeek's API requires a tool
call's follow-up to carry each earlier answer's `reasoning_content`
(otherwise `400`) — which neither the agent template nor the translation
sent. See [llm.md §Reasoning effort](/docs/llm.md#reasoning-effort) and the
agent template's API (§Reasoning effort).
