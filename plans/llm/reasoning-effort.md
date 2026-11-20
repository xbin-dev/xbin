# Reasoning effort and reasoning replay in xbin

> Status: live — stages 0 (replay) and 1 (levels) built, D249 (2026-11-20).
> Later stages (ceilings, harness configs, request patches, numeric values)
> are planned below, not built. The research behind it (the product and
> harness surveys) was trimmed from this copy; the provider table and the
> sources the vendored table (`internal/llm/effort_table.json`) was written
> from are kept. After review (D249's last bullet): the vendored table
> applies only on each family's maker's own API host — elsewhere it is a
> suggestion an admin adopts; remembered refusals are per conversation (the
> agent) and per upstream + model with a Reset (the connector); a replay
> names its model (`X-XBin-LLM-Expect-Model`).

## What was decided

1. **One portable intent**: a level of one ordered ladder — `none · minimal
   · low · medium · high · xhigh · max`, OpenAI's enum, which every caller
   of the connector already speaks — or "default" (absent: the model's own;
   nothing is sent). No product-wide default: a tile's `reasoningEffort`,
   a class's `effort` or a conversation's pick is the only level ever sent.
2. **The connector owns the mapping** (`internal/llm/effort.go`), per
   upstream and model, from a **capability record**: an admin's or the
   host's entry (`upstreams.<name>.reasoning`, keyed like `prices`; whole
   truth for its models) over the upstream's own `/models` (DeepSeek's
   `effort`, OpenRouter's `reasoning`; a router's `supported_features`
   naming `reasoning` says only "reasons") over a vendored table. A model
   nothing describes is relayed byte for byte as before.
3. **Rules**: exact → the provider's alias → the highest level at or below
   → the lowest; `none` is off only where the model can be turned off, else
   its lowest; a non-ladder word passes untouched; a model whose record says
   it has no reasoning gets none (dropped, named). The answer says what was
   sent: `X-XBin-LLM-Effort`.
4. **The control** is a chip beside the model picker: a notched track over
   the model's own levels (Off first where it can be off; Off | On for a
   toggle-only model), Default named after the model's own; none for an
   undescribed model or one without reasoning; an Effort picker in the app.
   A person's last choice is not remembered for new chats.
5. **Thinking replay is kept by default** — the owner, 2026-11-20: "at this
   stage it's industry standard to keep thinking blocks in context pretty
   much always". A model's reasoning goes back to it on every later turn,
   for every model that returned any, with no per-model opt-in — only ever
   to the same upstream (provider), model and wire that produced it, in the
   field it came in. A record may say `"replay": "never"` (the escape hatch).
   An upstream that 400s on the field gets the call once more without it:
   the agent remembers that for the process (only when the retry worked),
   the connector's translation never remembers (a caller can provoke a
   refusal).

## 1. Facts re-checked while building (2026-11-19/20)

- Provider pages re-read for the vendored table: xAI (grok-4.7, grok-4.6:
  low · medium · high · xhigh, default high; grok-4.5: low · medium · high,
  xhigh treated as high; "Reasoning cannot be disabled"); Mistral
  (`mistral-small-2603`, `mistral-medium-3-5`: high | none — none thinks
  minimally; `mistral-large-4-0`/`mistral-large-4`: low · medium · high,
  default high, no none); MiniMax (`MiniMax-M3.1-Flash`: low … max, default
  max, always thinks, none → 400); Gemini's OpenAI-compatible page
  (`gemini-4-neon`, `gemini-4-hydrogen`: minimal→low, low, medium, high,
  xhigh; reasoning cannot be turned off for Gemini 3/4; 2.5 is a budget and
  3's ids aren't named — left out); Kimi (`kimi-k3`, `kimi-k3.1-pro`: low ·
  high · max, default max, no off documented); Z.ai (`GLM-5.3`,
  `GLM-5.3-FLASH`: low · high · max, default max, disabling is an error;
  `GLM-5.2`: minimal … max, default max, none/minimal stop thinking); Model
  Studio (`qwen4-max`: low · high · xhigh with minimal/low→low,
  medium/high→high, xhigh/max→xhigh, default high, none disables;
  `qwen3.8-max`, `qwen3.8-max-0902`, `qwen3.8-flash`: low · medium · xhigh,
  default xhigh, high/max→xhigh). OpenAI's reasoning guide gives defaults
  and which models reject `none`, not each model's levels: **OpenAI is not
  in the table**.
- A real OpenAI-style router (the owner's gateway, probed by the
  coordinator 2026-11-20): its `/models` is OpenRouter-shaped with no
  `effort` and no `reasoning` object; `supported_features` names
  `reasoning`; `supported_sampling_parameters` leaves `reasoning_effort` out
  of a model that takes it — so a list's silence says nothing. It serves
  `glm-5.2` with none · medium · high · xhigh (refusing minimal, low, max)
  and `kimi-k3` with all seven (none turns it off): capability is a
  property of upstream + model, which is why an admin's entry beats the
  table, and why gateway behaviour never goes into the table. It accepts
  `reasoning_content` (and `reasoning`) on assistant messages, with tools
  and without; for kimi-k3 it is counted as prompt tokens.

## 2. Providers' native APIs (fetched 2026-11-19/20) — what the vendored table was written from

| Provider · wire | Parameter | Values | Default | Off? | Unsupported value | Reasoning tokens | Reasoning output | Replay rule |
|---|---|---|---|---|---|---|---|---|
| **DeepSeek** · chat | `reasoning_effort`; `thinking: {type: enabled\|disabled}` (via `extra_body` in the OpenAI SDK); `thinking.type` wins when both are sent | `none` (off), `low`, `high`, `max`; aliases `minimal`→low, `medium`→high, `xhigh`→high, `ultra`→max | thinking on, `high` | yes | only the aliases are documented; integers/other strings **unverified** | `completion_tokens_details.reasoning_tokens` | `reasoning_content` | **with `tools`: MUST pass back every turn's `reasoning_content`, else 400**; without tools: ignored |
| DeepSeek · Responses (stateless) | `reasoning.effort` | same | same | `none` | "Unsupported parameters are silently ignored" | `output_tokens_details.reasoning_tokens` | `reasoning` item with `reasoning_text` | items merged into the assistant message; no `encrypted_content`/`summary` |
| DeepSeek · Anthropic-compatible | `thinking.type` + `output_config.effort` | `budget_tokens` ignored | `high` | yes | — | `output_tokens_details.thinking_tokens` | `thinking` + unvalidated `signature` | with tools, pass the content back |
| DeepSeek · `GET /models` | `effort: {supported_levels, default_level}`, also `context_window`, `max_output_tokens`, `input_modalities` | `["low","high","max"]`, `high` | | | | | | |
| **OpenAI** · Responses | `reasoning: {effort, summary, context, mode}` | effort `none\|minimal\|low\|medium\|high\|xhigh\|max`, model-dependent; summary `auto\|concise\|detailed` | per model (GPT-5.5/5.6/6.x `medium`; GPT-5.1/5.2 `none`) | per model | **400** (e.g. `none` on GPT-6 Astra / 6.2) | `output_tokens_details.reasoning_tokens` | `reasoning` item: `summary[]`, `encrypted_content` (default in stateless mode) | MUST pass items back with function calls; dropped across model families |
| OpenAI · chat | `reasoning_effort` | same enum | per model | per model | 400 | `completion_tokens_details.reasoning_tokens` | none | — |
| **Anthropic** · Messages | `thinking: {type: adaptive\|enabled\|disabled\|between_tools, display, budget_tokens}` + `output_config.effort` | effort `low\|medium\|high\|xhigh\|max` per model; legacy budget ≥1024, < `max_tokens` | `high` (`medium` on Opus/Haiku 5.5) | per model; `enabled` → 400 on 4.7+ | 400 | `output_tokens_details.thinking_tokens` | `thinking` (summary or empty) + `signature`; `redacted_thinking` | MUST within a tool turn, unmodified |
| Anthropic · OpenAI-compat layer | `thinking: {type: enabled, budget_tokens}` in the body | `reasoning_effort` **"Ignored"** | | | `adaptive` → 400 | "Always empty" | not returned | — |
| **Gemini** · native | `thinkingConfig: {thinkingLevel \| thinkingBudget, includeThoughts}` | levels `minimal…xhigh` per model; budget `-1` dynamic, `0` off, ranges per model (2.5 Pro 128–32768) | per model | 2.5 Flash/Lite only | level+budget together → 400 | `thoughtsTokenCount` | `thought: true` parts | `thoughtSignature` MUST be returned with function calls (Gemini 3/4) |
| Gemini · OpenAI-compat | `reasoning_effort`; `extra_body.google.thinking_config` | minimal/low→low…; 2.5: 1024/1024/8192/24576 tokens | | `none` on 2.5 only | **unverified** | **unverified** | **unverified** | `extra_content.google.thought_signature` (third-party sources) |
| **xAI** | `reasoning.effort` / `reasoning_effort` | `low\|medium\|high\|xhigh` | `high` | "cannot be disabled" | `xhigh` on 4.5 "treated as high"; 400 on non-reasoning models (third-party) | `reasoning_tokens` | encrypted + summaries (Responses) | send `encrypted_content` back |
| **Mistral** | `reasoning_effort` | Small 4/Medium 3.5 `high\|none`; Large 4 `low\|medium\|high` | Large 4 `high` | not on Large 4 | "does not accept" (code **unverified**) | **unverified** | `thinking` chunks in `content` | replay the full message |
| **Qwen** (Model Studio) | `enable_thinking`, `thinking_budget`, `reasoning_effort` (`extra_body`), `preserve_thinking` | qwen4-max: minimal/low→low, medium/high→high, xhigh/max→xhigh; budget bands | `high` | hybrid models | "will cause an error" | **unverified** | `reasoning_content` | `preserve_thinking` (billed as input) |
| **Kimi** | K3 `reasoning_effort`; K2.x `thinking: {type, keep}` | K3 `low\|high\|max` | `max` | K3 no | error | **unverified** | `reasoning_content` | K3 MUST pass the full message back; "Switching levels invalidates prefix-cache hits" |
| **GLM** (Z.ai) | `thinking: {type, clear_thinking}` + `reasoning_effort` | 5.3 `low\|high\|max` | `max` | not on 5.3 | "Any other input will result in an error" | **unverified** | `reasoning_content` | `clear_thinking: false` keeps history, unmodified |
| **MiniMax** | `thinking: {type: adaptive\|disabled}`, `reasoning_effort`, `reasoning_split` | `low…max` (M3.1-Flash) | `max` | M3.1-Flash → 400 | `none` → 400 | **unverified** | `reasoning_content` or `<think>` | MUST append the full message |

What the table means for xbin: (a) the level vocabulary is shared, the **subset** and the **default** are per model; (b) "unsupported" ranges from silent remapping (DeepSeek) to silent ignoring (DeepSeek Responses, OpenRouter's providers) to 400 (OpenAI, Anthropic, GLM, MiniMax); (c) at least five providers now *require* reasoning to be sent back when tools are in play, in three different fields; (d) DeepSeek is the first to describe effort in its own `/models`.

Interactions that matter for the design:
- **Output limits.** Reasoning counts inside `max_output_tokens`/`max_completion_tokens` (OpenAI, Gemini, DeepSeek Responses, Kimi, MiniMax, Qwen from qwen4-max); OpenAI can end `incomplete` "before any visible output tokens". DeepSeek's default `max_tokens` is 64K in thinking mode, **128K at `max`**. Anthropic's legacy budget must be < `max_tokens`.
- **Sampling.** Thinking modes ignore or reject `temperature`/`top_p` (DeepSeek ignores; Anthropic 4.7+/5 400; OpenAI GPT-5.x only at `none`; Kimi/MiniMax fixed at 1). AgTT sends no sampling params — fine.
- **Tool choice.** `required`/named tool choice is refused in thinking mode by DeepSeek (400), Anthropic (manual thinking: `auto`/`none` only; some Claude 5 always 400), Kimi K2.x.
- **Prompt cache.** Anthropic: "Treat any thinking or top-level effort change as invalidating the whole cache." Kimi: "Switching levels invalidates prefix-cache hits." OpenAI offers a `configuration_update` input item to change effort mid-conversation that "preserves the original prompt prefix for prompt caching". DeepSeek's encoding renders the effort line at message index 0 (inference: a change misses the whole prefix cache; DeepSeek's caching page doesn't say — **unverified**).


## 3. The design (as built for stages 0–1; later stages marked)

### A. The canonical setting

**Representation.** `effort: <level>` where level ∈ the ladder `none < minimal < low < medium < high < xhigh < max`, or absent (= default). Optionally `effortValue: {model: "<upstream>/<model>", value: <number>}` — a native number for the one model it was set on (a 1–100 scale position or a token budget), honoured only while the conversation is on that model.

Why the ladder: it is OpenAI's enum, and every layer read for this research accepts it as input (OpenRouter `reasoning.effort`, LiteLLM, AI SDK 7's top-level `reasoning`, vLLM, SGLang, Ollama, llama.cpp, DeepSeek with aliases, Qwen with mapping, Copilot's levels); the stored value means the same thing to a person on every model ("cheaper/faster ↔ slower/more thorough"), and every caller of the connector already sends it.

How it survives each kind (which kind a model is follows from its record's fields — levels, `can_disable`, `scale`, `budget`; §B):

| Kind | Example | What a stored level becomes | What the control shows |
|---|---|---|---|
| (i) on/off | DeepSeek chat app, Continue, Kimi K2.x, GLM ≤5.2 | `none` → off; any other → on | two notches: Off · On |
| (ii) 2–6 levels | DeepSeek API `low/high/max`, Kimi K3, GLM-5.3, xAI, OpenAI, Gemini 3/4 | exact; else the provider's documented alias (DeepSeek: medium→high); else **the highest supported level at or below** (Claude Code's rule); never rounded to `none`; none below → the lowest | the model's levels as notches (+ Off if it can be disabled) |
| (iii) token budget | Gemini 2.5, Anthropic legacy, Qwen `thinking_budget` | the record's per-level budgets (per-model data, e.g. Gemini's 1024/8192/24576), else OpenRouter's ratios of the call's `max_tokens`; clamped to [min, max] and below `max_tokens` | level notches; free positions show tokens (only if the record allows) |
| (iv) continuous scale | DeepSeek-V4.1 self-hosted 1–100 | the record's anchors (DeepSeek: low 50, high 75, max 100); levels without an anchor round as in (ii) | notches at the anchors; free positions show the number |
| (v) dynamic/auto | Gemini `-1`, DeepSeek default, LibreChat "Auto" | "default" sends nothing → the model's own dynamic behaviour | the default notch is labelled "Default (High)" / "Auto" |
| (vi) no reasoning | most chat models | nothing sent; a caller's own effort is **dropped and named** (only when the record positively says "none"; unknown → pass) | no control |
| (vii) cannot be turned off | Gemini 3/4, xAI, Kimi K3, GLM-5.3, MiniMax M3.1-Flash | `none` → the lowest level, said | no Off notch; "always thinks" under the track |
| (viii) unforeseen | — | the record can carry, per level, a JSON merge patch for each wire (admin-written `send`); an xbind that meets a record field it doesn't know ignores it and uses the levels and patches, or treats the model as unknown | the levels the patches name |

**Model switch mid-conversation.** The level is kept (it is the intent); the new model's record maps it; `effortValue` is dropped unless the model is the same; the transcript gets one line — `model → gpt-6.1-sol · effort Max → High (its highest)` — and the chip shows the new value. A default stays default. (AgTT already drops reasoning replay on a model change, `llm_chat.go:54`.)

**"Default".** The model's own default: nothing is sent. The UI names it when the record knows it ("Default · High"). A product-wide default would change every existing conversation's cost on upgrade and contradict the providers' own tuning (decision 5). Admins can still set a *preset* for a tile (AgTT config), a class, or the workspace agent — that is "a default the admin chose", stored as a level.

### B. The capability record

What the mapping needs, per upstream + model (one JSON object; the same shape in the vendored table, the admin config and the model list):

```jsonc
"effort": {                                  // the member name and its first two keys are DeepSeek's own (GET /models)
  "supported_levels": ["low", "high", "max"],// ladder words this model takes when reasoning is on, in order; "none" excluded
  "default_level": "high",                   // what it does when nothing is sent ("" = unknown)
  "can_disable": true,                       // "none" turns reasoning off (OpenRouter's mandatory = !can_disable)
  "thinks_by_default": true,                 // reasons when nothing is sent (OpenRouter default_enabled)
  "aliases": {"minimal": "low", "medium": "high", "xhigh": "high", "ultra": "max"},  // the provider's own documented mapping
  "scale":  {"min": 1, "max": 100, "at": {"low": 50, "high": 75, "max": 100}},       // optional: a real numeric knob
  "budget": {"min": 1024, "max": 24576, "at": {"low": 1024, "medium": 8192, "high": 24576}}, // optional: tokens
  "replay": {"field": "reasoning_content", "when": "tools"},  // what a conversation must send back
  "source": "upstream"                       // upstream | xbin | admin | host — shown, never sent upstream
}
// server-side only (config, never in the listing): how a level is written per wire, when not OpenAI's
"send": {
  "chat":      {"reasoning_effort": "{level}"},                 // default for both wires: OpenAI's shapes
  "responses": {"reasoning": {"effort": "{level}"}},
  "none":      {"chat": {"thinking": {"type": "disabled"}}},     // e.g. a toggle-only model
  "value":     {"chat": {"chat_template_kwargs": {"thinking": true, "reasoning_effort": "{value}"}}}  // a self-hosted DeepSeek
}
```

The shape is deliberately an *import* target: DeepSeek's `effort`, OpenRouter's `reasoning` (`supported_efforts`, `default_effort`, `default_enabled`, `mandatory`), models.dev's `reasoning_options`/`interleaved` and LiteLLM's `reasoning_effort_levels` each convert into it field for field.

**Where it comes from, highest trust first:**

1. **The host's file**, then **the admin's entry**: `upstreams.<name>.reasoning` — a table keyed exactly like `prices` (exact id → id after the last `/` → longest `prefix*`), so `"deepseek*": {...}` covers a family. Edited on `/xbin/llm` ("Describe…") and `bx llm`. The only place a `send` patch can come from.
2. **The upstream's own listing** (`GET /models`, already fetched and cached a minute, `models.go:262-280`): DeepSeek's `effort`, OpenRouter's `reasoning`/`supported_parameters`, and any listing that already uses this shape (another xbind). Untrusted data: only ladder words, ≤ 16 known keys, numbers in range are accepted; never a `send` patch.
3. **A vendored table** embedded in xbind (`internal/llm/effort_table.json`, a few dozen pattern entries), written from the providers' docs and cross-checked at release time against models.dev (MIT) and LiteLLM's map (MIT) — it is the floor for air-gapped installs. A newer xbind refreshes it; nothing is fetched at run time. **As built:** each family names its maker's API hosts and applies only to an upstream at one of them (exact host); on any other upstream a match is a suggestion (`xbin_effort_suggestion`) an admin may adopt — the model stays undescribed.
4. **Unknown**: no record → no control, nothing added, callers' own fields untouched.

Why admin over upstream over table: the same model id behaves differently per serving stack (§3) and the admin is the only one who knows which stack an upstream is; the upstream's own words are the next most specific; a shared table is a guess about someone else's deployment.

**No probing.** A probe costs a model call per model and cannot detect the commonest failure (silent ignoring); D244's rule — learn only from evidence a caller can't forge — rules out learning from a caller-provoked 400.

**Consuming models.dev/LiteLLM/OpenRouter at run time** (rejected for now): both files are MIT and offline-capable, but they are 5–7 MB, describe *providers'* endpoints not the admin's gateway, disagree with each other on the owner's own model, and a run-time fetch breaks air-gapped installs. An optional `reasoningCatalog: {file}` (an admin-supplied models.dev snapshot, imported into tier 3) is a cheap later addition if the vendored table proves too slow to follow.

### C. The mapping

**Where:** a new `internal/llm/effort.go`, called in `apiCall` after `resolve` and before `req.Body` (`serve.go:86-111`), and in `responsesToChat` in place of the verbatim copy (`translate_request.go:161-176`). Every caller benefits: AgTT, the workspace agent, Codex (Responses, often translated), OpenCode (chat), any tile.

**Wire contract for callers (additive; nothing existing changes):**
- In: chat `reasoning_effort` and Responses `reasoning.effort` — OpenAI's strings, as today. Later (stage 5): `xbin_effort_value` (a number, the model-local native value; stripped before the relay, honoured only when the record has `scale`/`budget`).
- Out: the answer's header **`X-XBin-LLM-Effort: high`** — and `high; asked=medium` when it differed, `none; can't` / `low; ceiling` when clamped. The model list carries `effort` per model (the upstream's own member merged, tier 1–3 filled in).
- A model **without a record**: exactly today's behaviour (pass-through). A model whose record says **no reasoning**: the caller's effort is dropped and named in `X-XBin-LLM-Dropped` (strict providers 400 on it — xAI, per third-party tests).

**Rules, in order:**
1. Read the intent (chat `reasoning_effort`; Responses `reasoning.effort`; on the chat wire also OpenRouter's `reasoning.effort` when the upstream is not OpenRouter). A string outside the ladder (`ultra`, a future word) passes **untouched** — the future is never blocked.
2. Map to the model: `supported_levels` exact → `aliases` → highest supported at or below → lowest. `none` → off if `can_disable`, else the lowest level.
3. Clamp to the ceiling in force (host > admin, per upstream; by ladder index).
4. Write it: the record's `send` patch for the wire (default: OpenAI's shape, i.e. leave `reasoning_effort` as the mapped word); remove the caller's member when the native shape differs (e.g. `thinking`). Patches may not touch `model`, `messages`, `input`, `tools`, `stream`, `stream_options` (refused at config time).
5. Budget kinds: `budget = clamp(at[level] or ratio × max_tokens, min, min(max, max_tokens − 1))`; if `max_tokens` can't hold `min` + 1, send reasoning off (or the lowest level) and say so. Ratios are OpenRouter's (minimal .1, low .2, medium .5, high .8, xhigh/max .95) — data in the table, not code.
6. Say it: `X-XBin-LLM-Effort`, and the usage row keeps `reasoningTokens` (already metered).

**Rounding, argued.** Down ("highest at or below") is Claude Code's and the AI SDK's rule and is cost-safe; the provider's own aliases take precedence because that is what the provider would do with the raw string anyway (DeepSeek turns `medium` into `high`). LiteLLM rounds up and OpenRouter to the nearest — the header makes our choice visible either way, and the UI never offers a level the model lacks, so rounding only happens on a model switch or for API callers.

**The D244 translation, extended:**
- `reasoning.effort` goes through rules 1–6 for the chat upstream (not verbatim).
- `reasoning.summary` stays dropped and named (chat has no such thing).
- A `reasoning` input item **the translation itself minted** (id `rs_…`, `summary_text` only, no `encrypted_content` — `translate.go:280-285, 489-494`) becomes `reasoning_content` on the next assistant message when the record's `replay` says so (DeepSeek: when the request has tools). Any other reasoning item stays dropped — and is now **named** (`input.reasoning`), which D244's own rule ("nothing vanishes silently") asks for.

**Mapping in the connector vs. in the agent.** The agent keeps one job: send the person's level (a string) and replay what its model produced. Everything model-specific — subsets, aliases, budgets, patches, ceilings — is the connector's, so a new model is a table or config change in one place.

### D. UX

Principles: one small chip next to the model, never a panel; show only what the model has; say what it became; feedback in the transcript (time + reasoning tokens), not in settings.

**(1) AgTT — composer and per-conversation.** A chip beside the model picker (`#msel`), hidden when the model has no record or no reasoning:

```
┌────────────────────────────────────────────────────────────────┐
│ Ask anything…                                                  │
├────────────────────────────────────────────────────────────────┤
│ [ deepseek-v4.1-flash ▾ ]  [ ◕ High ▾ ]                ↵ Send   │
└────────────────────────────────────────────────────────────────┘

 click the chip → popover (levels model: DeepSeek API)
 ┌ Reasoning · deepseek-v4.1-flash ─────────────────────────┐
 │   Off       Low          High          Max               │
 │   ○─────────○────────────●─────────────○                 │
 │   faster, cheaper            slower, more thorough       │
 │   High is this model's default                [Default]  │
 │   Last answer: thought 21 s · 4 310 reasoning tokens     │
 └──────────────────────────────────────────────────────────┘

 continuous model (an admin described a self-hosted 1–100 scale)
 │   1 ·············· Low ········ High ········ Max        │
 │   ├────────────────┼──────●─────┼─────────────┤          │
 │                    50     62    75            100  = 62  │

 toggle-only model            │  Thinking   [ Off | On ]   │
 can't be turned off          │  Low ── Medium ── ●High ── Max   (always thinks) │
```

- Keyboard: the chip is a button; ←/→ move notch to notch, Home/End = lowest/highest, `0` = Default; Shift+←/→ moves by 1 on a continuous scale. Enter/Esc close.
- Stored like the model pick: `runs.config.effort` via the same `PATCH /runs/{id}` (`{effort}`), read every turn, applied from the next one; subagents inherit (childConfig already copies the config) — the same choice Claude Code made and was criticised for when a session sat at `xhigh` (claude-code#92660), so the workflow tree should show each run's reasoning tokens, and a class or a spawn may set its own level.
- Defaults: the tile's ⚙ Config gets one "Reasoning effort" select (reusing `reasoningEffort`, "" = model default); an agent class (D116) may carry `effort` beside its `model`. A person's last choice is **not** remembered for new chats (a forgotten `max` is the cost surprise the complaints describe); revisit if people ask.
- On a model switch: the transcript line from §A; the chip shows the new value; a level the new model lacks shows as "High (Max isn't offered)" for one turn.
- Feedback: the thinking fold already shows the time (`fold.js:214`); add the reasoning tokens from `meta.usage` ("thought 21 s · 4.3k tokens").
- The popover carries one honest line when the conversation is long: "Changing effort can make the next call re-read the whole conversation (cache)."

**(2) Workspace agent.** Admin: one row on the agent's card on `/xbin/system`, under Context budget — `Reasoning effort [ Model default ▾ ]` — and the host file `workspaceAgent.effort` as a preset and a ceiling (the `contextBudget` pattern). Person: the same chip in the session's scope panel, beside the model (`ws/scope.js`), stored per session; changing it is not a taint question (no new data reaches a new model).

**(3) Agent tab (ACP harnesses).** The harness owns the knob; xbin does not add a second one. Keep rendering every config option the agent advertises; draw an option of category `thought_level` with the same notched control (ACP v2 asks agents to list its values lowest → highest; a value that isn't a ladder word is just a labelled notch). Through the connector, give each harness what it needs to know the connector's models (stage 3), so its own option appears: OpenCode's inline config gets `reasoning: true` and explicit `variants` per model from the record (`{"low": {"reasoningEffort": "low"}, "high": …, "max": …}`) — its ACP `effort` option is built from variant names; Codex gets a model catalog (`model_catalog_json`, a file path read at startup — present in the shipped 0.156.1) with `supported_reasoning_levels` and `default_reasoning_level` per connector model. Whatever the harness then sends is mapped by the connector like any caller's. The person's choice is already saved per harness (`harnesses.<id>.options`).

**(4) Admin (`/xbin/llm`).** Per upstream model row, read-only unless edited:

```
 house   https://ai.example/v1   chat only · translating
   deepseek/deepseek-v4.1-flash   Reasoning: Low · High · Max  (default High) · from the upstream   [Edit]
   qwen-local                     Reasoning: not described — no control is offered               [Describe…]
   Highest effort for this upstream   [ High ▾ ]          Set by your host: at most Max
```
"Describe…" offers presets (OpenAI-style levels; on/off; DeepSeek-style low/high/max; Gemini 2.5 budget; self-hosted DeepSeek 1–100) and an advanced JSON field for `send` patches. No per-person effort ceilings: quotas are the per-person spend lever.

**(5) iOS.** The native vocabulary has no slider, so: an "Effort" `picker` (menu style) next to "Model" in the conversation's ⋯ menu — "Default (High) · Low · High · Max"; a continuous value set on the web shows as "Custom (62)" read-only. No vocabulary change, no app release.

**Where it must not appear:** models without reasoning; unknown models; compaction/title/judge calls (they keep the model default — unchanged behaviour, and Zed's #60411 shows what goes wrong when side calls carry the conversation's effort); a harness that advertises no such option.

**The owner's slider, evaluated.** Right where the model is really continuous (a self-hosted DeepSeek-V4.1: notches at 50/75/100 are exactly "where low/high/max sit"), and the industry has converged on drawing *levels* as a stepped slider (ChatGPT, Claude Code, LibreChat, Coder). Wrong as a universal 0–100: it promises steps most models don't have, the region below DeepSeek's `low` (1–49) is undocumented (DeepSeek's benchmarks are all at 100), and budget sliders were the most confusing control in the products surveyed (Cline removed its own; Cline #6572). So: the slider look, snapping to the model's levels, free only when the record says so.

### E. Cost and safety

- **Spend.** Reasoning tokens are output tokens (`price.go:7`), metered per row (`meter.go:38`) and counted by token quotas (input + output). Quotas are checked before a call (`quota.go:31-34`), so one call can overshoot by up to its `max_tokens` — 128K by default at DeepSeek `max` — bounded by the per-caller concurrency cap. The **ceiling** (decision 5) is the pre-call lever; it clamps, says so, and never refuses.
- **Context budget.** Replayed reasoning is input (DeepSeek concatenates it; Qwen bills `preserve_thinking` as input), so it counts toward AgTT's compaction trigger and the workspace agent's context budget — both already use the provider's prompt-token count.
- **Prompt cache.** Treat an effort change as a likely full cache miss (Anthropic says so; Kimi says so; DeepSeek's encoding puts effort at index 0). Hence per-conversation, applied from the next turn, with the one-line warning. OpenAI's `configuration_update` item avoids the miss — a later refinement for the Responses wire to OpenAI itself only.
- **Replay — what must be sent back, and what must be dropped:**

| | Same model, effort changed | Model switched |
|---|---|---|
| OpenAI encrypted reasoning items | keep | drop (the API omits them anyway) |
| Anthropic thinking + signature | keep within a tool turn | Anthropic says don't strip; xbin can't relay that wire yet |
| `reasoning_content` (DeepSeek, Kimi K3, GLM preserved, MiniMax, Qwen preserve) | keep — **required with tools** | drop |
| OpenRouter `reasoning_details` | keep | drop |
| Gemini thought signatures | keep (required with function calls) | drop |

  AgTT's existing rule — replay only to the same wire and model — is right; it only lacks `reasoning_content`.
- **Trust.** The upstream's listing is untrusted input: only ladder words and bounded numbers are read from it, and only admins write `send` patches (security review). Reasoning text is replayed only to the upstream + model that produced it.

### F. Future-proofing

| What changes | xbind release | admin edit | nothing |
|---|---|---|---|
| A provider adds a level to a model | — | if the upstream doesn't list it: "Describe…" | if the upstream lists it (DeepSeek does); callers sending the word pass through |
| A provider renames a level | vendored alias | alias in the entry | stored intents are ladder words — unaffected |
| A new word above `max` becomes common | to place it on the ladder | until then: map `max` → the word in `send` | callers sending it pass through |
| A new kind of knob (e.g. seconds of thought) | to draw a new control kind | per-level `send` patches work at once | — |
| A new model of a known family | — | — | pattern entries (`deepseek*`) cover it |
| A new family | vendored entry | entry | if its upstream lists it |
| A harness adds its own effort option (ACP `thought_level`) | — | — | the Agent tab renders advertised options |
| A harness changes its config format (OpenCode variants) | `sdk/acp/connector.go` | — | — |
| ACP standardises effort beyond `thought_level` | to style it | — | the generic select works meanwhile |


### G. Stages

| Stage | What | State |
|---|---|---|
| **0 · Replay** | the agent template keeps a chat answer's plain reasoning (`reasoning_content`, `reasoning`, a leading `<think>`) and sends it back in that field to the same provider, model and wire; the translation turns its own reasoning items back into `reasoning_content` and names every other one; both recover once from a 400 on the field | **built (D249)** |
| **1 · Levels** | the record, the vendored table, the upstream's listing (untrusted), the admin's entries (presets, or ticked levels) and `bx llm --reasoning`, `effort` on `GET /v1/models`, the mapping on both wires and in the translation, `X-XBin-LLM-Effort`; the agent template's `runs.config.effort`, class `effort`, Config-tab default, the composer chip, the app's picker, the fold's tokens | **built (D249)** |
| 2 · Ceilings + feedback | per-upstream highest effort (host preset + ceiling), usage's reasoning column, the workspace agent's default and chip | planned |
| 3 · Harness configs | OpenCode `reasoning` + `variants`, Codex `model_catalog_json`, `thought_level` options drawn as the track | planned |
| 4 · `send` patches, budget/toggle kinds | request patches per level, token budgets, `max_tokens` constraints | planned (records may carry `scale`, `budget`, `send` now; a record that needs them is treated as unknown) |
| 5 · Fine values | `xbin_effort_value`, free positions on a continuous scale (a self-hosted DeepSeek-V4.1's 1–100 via `chat_template_kwargs`, which passes untouched today) | planned |

## Appendix — sources

**DeepSeek**
- ✓ https://api-docs.deepseek.com/guides/thinking_mode — no date shown; levels, aliases (minimal→low, medium→high, xhigh→high, ultra→max), "If your code does not correctly pass back `reasoning_content`, the API will return a 400 error."
- ✓ https://api-docs.deepseek.com/api/list-models (and `.md`) — `effort.supported_levels`, `effort.default_level`, `context_window`, `max_output_tokens`; example `deepseek-flash` (DeepSeek-V4.1-Flash) and `deepseek-v4-pro`
- https://api-docs.deepseek.com/api/create-chat-completion — `reasoning_effort` values, `thinking` precedence, `max_tokens` defaults (64K thinking, 128K at `max`), replay "even for turns where the model did not perform a tool call"
- https://api-docs.deepseek.com/api/create-response, https://api-docs.deepseek.com/guides/responses_api — "Unsupported parameters are silently ignored"
- https://api-docs.deepseek.com/guides/anthropic_api, https://api-docs.deepseek.com/api/create-message
- https://api-docs.deepseek.com/news/news250821 (2025-08-21) — "DeepThink" toggle; the 2026-09-10 release note on v4-pro routing
- ✓ https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash — "continuously controllable reasoning effort from 1 to 100"; "All instruct results below use the maximum effort setting (`reasoning_effort=100`)"; no Jinja chat template
- ✓ https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash/blob/main/encoding/README.md — commit `314113f`; "Reasoning effort is a numeric budget (1–100)"; low→50, high→75, max→100; default high (75); rendered only in thinking mode at index 0. (An earlier commit `df42c10`, Sep 10, had low 25 / high 50 / xhigh 75 / max 100 — research pass, not re-read.)
- vLLM DeepSeek-V4.1 recipe and vLLM issue #62220 (`chat_template_kwargs.reasoning_effort` integer; `minimal`/`medium` → 400) — research pass

**OpenAI**
- ✓ https://developers.openai.com/api/docs/guides/reasoning — value set "can include `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`"; GPT-6 Astra `none` → 400; defaults; `encrypted_content` by default in stateless mode; `configuration_update` "preserves the original prompt prefix for prompt caching"; reasoning omitted across model families
- https://developers.openai.com/api/reference/resources/responses/methods/create; https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create; https://developers.openai.com/api/docs/guides/latest-model/gpt-5.2

**Anthropic**
- https://platform.claude.com/docs/en/build-with-claude/thinking, …/effort, …/extended-thinking, …/adaptive-thinking — per-model `thinking` types, effort levels, budget rules, replay ("Required: within a tool-use turn…"), cache ("Treat any thinking or top-level effort change as invalidating the whole cache"), per-message effort beta `mid-conversation-output-config-2026-07-01`
- https://platform.claude.com/docs/en/api/openai-sdk — `reasoning_effort` "Ignored"; `completion_tokens_details` "Always empty"

**Google**
- https://aistudio.google.com/docs/thinking?generate-content=true; https://aistudio.google.com/docs/thought-signatures?generate-content=true; https://ai.google.dev/gemini-api/docs/thinking; https://ai.google.dev/gemini-api/docs/openai (effort→level/budget table)

**Others**
- xAI: https://docs.x.ai/docs/guides/reasoning; https://docs.x.ai/developers/model-capabilities/text/best-practices; third-party live tests at git.spboucher.ai (2026-09-19) for the non-reasoning-model 400
- Mistral: https://docs.mistral.ai/inference/conversations/reasoning
- Qwen: https://www.alibabacloud.com/help/en/model-studio/qwen-api-via-openai-chat-completions; https://www.alibabacloud.com/help/en/model-studio/deep-thinking
- Kimi: https://platform.kimi.ai/docs/guide/use-thinking-models; https://platform.kimi.ai/docs/api/models-overview
- Z.ai GLM: https://docs.z.ai/api-reference/llm/chat-completion; https://docs.z.ai/guides/capabilities/thinking
- MiniMax: https://platform.minimax.io/docs/api-reference/text-openai-api; https://platform.minimax.io/docs/guides/text-generation


- Re-read 2026-11-19 while building: https://developers.openai.com/api/docs/guides/reasoning, https://docs.x.ai/docs/guides/reasoning, https://docs.mistral.ai/inference/conversations/reasoning, https://platform.minimax.io/docs/api-reference/text-openai-api, https://aistudio.google.com/docs/openai, https://platform.kimi.ai/docs/guide/use-thinking-models, https://docs.z.ai/guides/capabilities/thinking, https://www.alibabacloud.com/help/en/model-studio/qwen-api-via-openai-chat-completions
