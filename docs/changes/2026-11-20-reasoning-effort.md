# Reasoning effort is mapped onto each model's levels, and reasoning goes back to its model

**BREAKING for a caller that relied on its effort word reaching a model
the connector describes verbatim** — and for an upstream that refuses a
field it was never sent before. Most callers notice nothing, or a call that
used to fail now working. **On an upstream that is not a model maker's own
API — a router, a local server, a company gateway — nothing an upgrade
changes rewrites an effort** until an admin describes its models (or the
upstream's own list gives their levels).

## What changed

**The LLM connector** (`/api/xbin/llm/v1/…`) now knows, for many models,
which reasoning levels they take — from an admin's or the host's
description (`upstreams.<name>.reasoning`), the levels the upstream's own
model list gives (DeepSeek's `effort`, OpenRouter's `reasoning`), or a
table xbind ships — which applies **only when the upstream is the model's
maker's own API** (its base-URL host exactly one of the maker's:
`api.deepseek.com`, `api.x.ai`, `generativelanguage.googleapis.com`,
`api.moonshot.ai`, `api.z.ai`, `api.minimax.io`, `api.mistral.ai`) —
DeepSeek's API, xAI Grok 4.5–4.7, Gemini 4's thinking models, Kimi K3,
GLM-5.2 and 5.3, MiniMax M3.1-Flash, Mistral Small 4, Medium 3.5 and
Large 4. For such a model:

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

A model nothing describes is relayed exactly as before, byte for byte. On
another upstream, a model the table knows stays undescribed: `GET
/v1/models` gives it `xbin_effort_suggestion` (the maker's levels), and
`/xbin/llm` offers *Use these* to make them your description. `GET
/v1/models` carries `effort` on described models (replacing an upstream's
own member of that name, keeping its valid values; one that is no record
is removed). Every answer names the model called (`X-XBin-LLM-Model`).

**The Responses → chat translation** sends a model's own reasoning back:
the reasoning item it made of an answer (ids are now `rs_<tag>_…`) becomes
`reasoning_content` on the next assistant message of a later request, for
the same upstream and model only. Other reasoning items are still not sent,
and are now named (`input.reasoning` in `X-XBin-LLM-Dropped`). This is so
for every model, described or not. An upstream that answers `400` to a
request carrying `reasoning_content` gets the request once more without
it; when its error named the field and the request without it worked,
the connector remembers it for that caller, upstream and model (never for
another caller) until an admin's Reset, a change of the upstream, or a
restart.

**The agent template** sends a model's plain reasoning back on later turns
(`reasoning_content`, `reasoning`, or a leading `<think>…</think>` in the
content, as the answer had it), only to the provider and model that
produced it — the one the connector says answered, and a request that
carries it names that model, so an alias pointed elsewhere refuses it
(`409 xbin_llm_model_moved`) and the call goes once more without; a
provider that 400s on it gets the call once more without it, and when its
refusal named the field that conversation stops sending it to that model
(the conversation says so). Its default `reasoningEffort` goes only to a model that reasons by
its description; a model described as having none gets no
`reasoning_effort`, and no `reasoning` block on the Responses wire.

## Who's affected

- A tile, harness or script that sends `reasoning_effort` through the
  connector to a described model and depends on the exact word arriving —
  for example `medium` to `kimi-k3` on Moonshot's own API now arrives as
  `low` (it takes low, high and max). Behind a router nothing is rewritten
  unless the router's own list gives the model's levels or an admin
  describes it.
- An upstream that refuses an unknown assistant-message member
  (`reasoning_content`, `reasoning`) — the agent template and the
  translation now send it to models that produced reasoning. Both recover
  by themselves (one more call without it); the log and the conversation say so.
- Code that parsed the translation's reasoning item ids (`rs_<resp>_<n>`):
  they are opaque, and now `rs_<tag>_<resp>_<n>`.

## How to migrate

Nothing, for most workspaces. Behind a router or a gateway, `bx llm models`
says "not described — xbin knows the maker's levels: …" where xbind's table
knows a model: if your upstream takes the maker's levels, adopt them (*Use
these* on `/xbin/llm`, or the `--reasoning` it prints); if not, describe
the model. Where a model's levels on your upstream differ from what `bx
llm models` says, describe it: on `/xbin/llm`, the provider's *reasoning…* → the model's
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
