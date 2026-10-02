# llmreplay — record a take's model calls once, replay them for every retake

A promo shot of xbin is a real agent doing real work, and a real model never
answers the same way twice. `llmreplay` sits between the agents and the model
APIs: one take runs against the real models with real keys and is
**recorded**; every retake is **replayed** from that recording — the same
answers, streamed event by event on the recorded clock (or faster, or at
once). Footage becomes reproducible and a retake costs nothing.

It is an HTTP proxy for the wires xbin's agents use:

- the **OpenAI-compatible API** — `chat/completions` and `responses`, streamed
  (SSE) or not, plus `GET /v1/models`, embeddings and the rest (what llm-gw
  sends its backends, what Codex sends `OPENAI_BASE_URL`);
- the **Anthropic Messages API** — `POST /v1/messages`, streamed, plus
  `count_tokens` and the rest (what Claude Code sends `ANTHROPIC_BASE_URL`).

Standard library only, one static binary, in the xbin module.

## Build

```sh
CGO_ENABLED=0 go build -o bin/llmreplay ./hack/demo/llmreplay   # static: runs inside a coding sandbox as is
# (GOARCH=arm64 for an arm64 host or VM)
```

## The loop

```sh
mkdir -p -m 700 /home/magik6k/buxon/.film-media/replay
C=/home/magik6k/buxon/.film-media/replay/launch-day.jsonl

# 1. the real take — keys from the environment, never from flags
OPENAI_API_KEY=… bin/llmreplay record -cassette $C        # Ctrl-C when the take is done
bin/llmreplay inspect -v $C                               # what got recorded, lane by lane

# 2. every retake
bin/llmreplay replay -cassette $C                         # recorded timing
bin/llmreplay replay -cassette $C -timing 0.5             # every pause halved
bin/llmreplay replay -cassette $C -timing instant         # no pauses at all
bin/llmreplay replay -cassette $C -max-wait 3s            # recorded timing, no pause over 3 s

# between takes: start over without a restart
curl -X POST http://127.0.0.1:9398/_llmreplay/reset       # (or kill -HUP <pid>; ?lane=claude for one lane)
curl http://127.0.0.1:9398/_llmreplay/status              # served / left / drift per lane
```

It listens on `127.0.0.1:9398` (`-listen`). `record`, `replay` and
`passthrough` take `-detach [-log FILE]`: start in the background, return
once it listens — or at once when an llmreplay already answers there —
saying nothing on stdout (see the Claude Code hook below). `record` refuses
to write over an existing cassette (`-append` adds a session to it,
`-overwrite` replaces it) and refuses a path git would track (§Security).
`passthrough` relays without recording — for checking the wiring.

Upstreams for `record` and `passthrough`:

| | base URL | key |
|---|---|---|
| OpenAI-compatible | `LLMREPLAY_OPENAI_BASE_URL` (default `https://api.openai.com/v1`; OpenRouter: `https://openrouter.ai/api/v1`) | `LLMREPLAY_OPENAI_API_KEY`, else `OPENAI_API_KEY` (Bearer) |
| Anthropic | `LLMREPLAY_ANTHROPIC_BASE_URL` (default `https://api.anthropic.com`) | `LLMREPLAY_ANTHROPIC_API_KEY`, else `ANTHROPIC_API_KEY` (`x-api-key`), else `ANTHROPIC_AUTH_TOKEN` (Bearer) |

A call goes to the Anthropic upstream when it carries `anthropic-version` or
`x-api-key` or its path is `/v1/messages…` or `/api/…`; to the OpenAI one
otherwise. The base URLs have names of their own so a shell that points
Claude Code at the proxy never points the proxy at itself (it refuses to
start if it would). With a key configured the proxy replaces the caller's
credentials with it — llm-gw and Claude Code then hold placeholders. With
no key (and no `LLMREPLAY_TOKEN`), the caller's own credentials go through
unchanged: a signed-in Claude Code records without the proxy ever holding a
key. Replay needs no key at all.

## Lanes

Each client gets a **lane**, matched on its own, so llm-gw's calls and a
coding agent's never interleave. A lane is the path prefix
`/lane/<name>/…` — the proxy strips it — or, without one, the
`X-Replay-Lane` header; else `default`. Use the prefix: it rides in any base
URL. By convention: `agent` for llm-gw (the agent template's models),
`claude` for Claude Code, `codex` for Codex.

## llm-gw (the agent template's models)

llm-gw sends `<backend base URL>/v1/…` with its backend's token and no
other caller headers, so its lane goes in the base URL. It must reach the
proxy on the host's loopback:

```sh
bx bind apps/llm-gw net=host        # rebinding restarts llm-gw
```

Then point a backend at the proxy — on the llm-gw page (Backends → add), or:

```sh
curl -X PUT -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' \
  "$XBIN_URL/api/apps/llm-gw/config/backend" \
  -d '{"name":"openai","baseURL":"http://127.0.0.1:9398/lane/agent/v1"}'
curl -X PUT -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' \
  "$XBIN_URL/api/xbin/vault/apps/llm-gw/api-token-openai" -d '{"value":"replay"}'
```

- The token is a placeholder: the proxy puts the real key on the way out
  (record) or needs none (replay). Without a key in the proxy's environment,
  set the real token here instead — it is passed through, never stored.
- **Name the backend like the real provider.** Its name is what model ids
  show once llm-gw has two backends (`openai/gpt-5.1`), and the agent's
  model picker, llm-gw's usage table and its preferred models read like a
  real setup. With one backend ids stay bare. Aliases and preferred models
  don't change.
- llm-gw counts usage from the replayed streams, so its page shows the
  recorded tokens (and cost, where prices are set) on every take.
- Back to production: set the backend's base URL to the real API
  (`https://api.openai.com/v1`) with the real token, and bind
  `net=internet` again.

## Claude Code in a coding sandbox

Claude Code takes its API from `ANTHROPIC_BASE_URL` (and extra headers from
`ANTHROPIC_CUSTOM_HEADERS`), in its environment or in `env` of
`~/.claude/settings.json`. The ACP adapter the agent template drives
(`claude-agent-acp`) loads user settings too (`settingSources: user,
project, local`), so the same file serves an Agent-tab coding agent and a
person's `claude` at a terminal — `~/.claude/settings.json` in the sandbox:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:9398/lane/claude",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```

(The second keeps telemetry and update checks out of a take.) Use the path lane: Claude Code's connectivity probe (`HEAD /api/hello`)
goes out without `ANTHROPIC_CUSTOM_HEADERS`. During a replay Claude Code
only has to believe it is signed in — its own sign-in, a saved one, or a
placeholder `ANTHROPIC_API_KEY`; the proxy neither checks, forwards nor
stores it.

**A coding sandbox never reaches the host.** Sandbox networks get no host
networking, and every address the host delivers locally is refused
whatever the class (docs/isolation.md §Networks for a manager's
sandboxes), so the proxy llm-gw uses on the host's loopback is out of a
sandbox's reach. Run the Claude lane where the sandbox can reach it:

### A. Inside the sandbox (recommended)

The proxy is one static binary. It and the Claude lane's cassette live in
the sandbox user's home (out of the workdir: never in the agent's file
views or `git status`), and a `SessionStart` hook starts it before Claude
Code's first call. `-detach` blocks until it listens, does nothing when one
already runs (every session start, resume and compaction fires the hook)
and prints nothing on stdout — a SessionStart hook's stdout lands in the
model's context, and the recorded prompt.

1. **Prepare the sandbox** at the start state of the shot — on the
   coding-sandbox page, Yours → New sandbox (network `internet` for the
   recording take). Files → Upload `bin/llmreplay`, then in its Terminal:

   ```sh
   mkdir -p ~/.local/share/llmreplay ~/.claude
   mv /work/llmreplay ~/.local/share/llmreplay/ && chmod 755 ~/.local/share/llmreplay/llmreplay
   cat > ~/.claude/settings.json <<'EOF'
   {
     "env": {
       "ANTHROPIC_BASE_URL": "http://127.0.0.1:9398/lane/claude",
       "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
     },
     "hooks": {
       "SessionStart": [{ "hooks": [{ "type": "command",
         "command": "$HOME/.local/share/llmreplay/llmreplay record -detach -append -log $HOME/.local/share/llmreplay/llmreplay.log -cassette $HOME/.local/share/llmreplay/claude.jsonl" }] }]
     }
   }
   EOF
   ```

   Share it with the agent (Sharing → `apps/agent`) so a conversation can
   work in it, and take a snapshot, `before-take` (the page's snapshots,
   or `POST /ops/sandboxes/{id}/snapshots`).
2. **Record**: the real take. The recorder holds no key: Claude Code's own
   sign-in (the agent's saved sign-in, or `claude auth login` in the
   sandbox) goes through to `api.anthropic.com` untouched and is never
   written. Don't put a key in `settings.json`: the snapshots would keep it.
3. **Keep the cassette**: Files → path `/home/dev/.local/share/llmreplay`
   (the layout's home) → download `claude.jsonl` into
   `/home/magik6k/buxon/.film-media/replay/`.
4. **Arm the replay**: restore `before-take`, upload the cassette (into
   `/work`, then `mv /work/claude.jsonl ~/.local/share/llmreplay/` in the
   Terminal), change `record -detach -append` to `replay -detach` in the
   hook, and snapshot `replay-ready`.
5. **Every retake**: restore `replay-ready`, then run the shot. Taking or
   restoring a snapshot stops everything in the sandbox, so each take
   starts a fresh replayer at its first Claude Code session.

### B. A proxy the sandbox can route to

Run `llmreplay` where the host routes but doesn't deliver locally — another
machine on the LAN, a VM — bind the manager's `open` class there and give
the sandbox egress `open`:

```sh
bx bind apps/coding-sandbox open=lan:10.20.0.15/32
LLMREPLAY_TOKEN=… bin/llmreplay replay -listen 10.20.0.15:9398 -cassette $C   # on that machine
```

Claude Code then gets `ANTHROPIC_BASE_URL=http://10.20.0.15:9398/lane/claude`
and `ANTHROPIC_AUTH_TOKEN` = the same token. On a network, always set
`LLMREPLAY_TOKEN` (§Security); recording then needs the proxy's own key,
as callers' credentials are not passed on under a token.

### Codex and other OpenAI-wire agents

`OPENAI_BASE_URL=http://127.0.0.1:9398/lane/codex/v1` (an API-key sign-in;
a ChatGPT sign-in talks to another backend). The same in-sandbox layout
applies.

## How a retake finds its answers

A take is matched lane by lane. A **model call** (`chat/completions`,
`responses`, `messages`) takes an unused recorded exchange of the same kind
— wire, model, stream flag:

1. **exact** — the earliest whose body hashes the same, ignoring the fields
   that differ every run (`metadata` with Claude Code's session id, `user`,
   `prompt_cache_key`, `safety_identifier`) and key order;
2. **thread** — else the next one of its conversation. Requests that share
   a thread key — the system prompt's start and the first user message —
   are one conversation, and the *N*th request of a live conversation gets
   its recorded conversation's *N*th answer. With one conversation that is
   simply "the *N*th request of the lane gets the *N*th answer"; with an
   agent's side calls (titles, summaries) and parallel subagents (each
   has its task as its first message) every conversation stays in step
   however their requests interleave;
3. **nearest** — else the most similar unused one, up to `-window` (8) past
   the furthest exchange the take reached; the live conversation then
   follows the recorded one it matched (a retyped first prompt keeps its
   thread).

The loose comparisons (thread keys, similarity) see normalized text: UUIDs,
dates and times, durations, numbers of five digits or more, hex ids and the
providers' call ids masked, and Claude Code's per-request billing line
dropped. So a new day, a new session, new tool-call ids or a test that ran
in 0.38 s instead of 0.41 s change nothing.

Every match short of exact is compared with the live request. Below
`-drift` (0.8) similarity — of the whole request, or of what is new in it
since the model last spoke (the newest prompt or tool result) — or with a
different number of messages, it is logged as **DRIFT** with where the two
part:

```
DRIFT agent request 2 ← seq 1 (thread 1 step 2, thread): similarity 0.21 (new input 0.21, whole 0.93):
  message 3 (tool) differs: recorded "ok northwind/billing <dur> …", live "--- FAIL: TestRoundTotals …"
```

The recorded answer is served anyway — the take carries on, and the log
(and `/_llmreplay/status`) say which shot no longer matches its recording.
A request nothing fits gets a **MISS**: a 400 in its API's error shape, or,
with `-on-miss passthrough`, the real upstream (unrecorded).

Everything else — `GET /v1/models`, embeddings, `count_tokens`, Claude
Code's probes — is a **lookup**: answered by the same request recorded
(else the most similar on the same path, this lane first), any number of
times, without moving the sequence. A model list nobody recorded is made
up from the models the cassette's calls used.

Also:

- **Transient failures are left out**: a recorded 429, 5xx or unreachable
  upstream was the provider's hiccup, and the client's retry got the real
  answer; the replay serves that answer to the first try. `-keep-errors`
  replays them too.
- **A caller that hangs up mid-replay** (a timeout) gets the same exchange
  on its retry. A call whose caller hung up in the recording (an interrupt)
  plays its recorded events and is then held open, as the model was still
  going then.
- **Timing**: the headers come at the recorded latency counted from the
  request's arrival (matching time included), then each event after its
  recorded pause, flushed one by one, then the recorded tail before the
  stream ends. `-timing` scales every pause; `-max-wait` caps each one.

## Cassettes

A cassette is JSON Lines: a header, then one exchange per line, appended
and synced as each exchange ends (a recording cut short keeps every
exchange that finished):

```jsonc
{"llmreplay":1,"created":"2026-10-02T21:36:34Z"}
{"lane":"agent","seq":0,"started":"…","method":"POST","path":"/v1/chat/completions",
 "reqHeaders":{…allowlisted…},"reqBody":{"model":"gpt-5.1","stream":true,"messages":[…]},
 "status":200,"headers":{"Content-Type":["text/event-stream"]},"headMs":412.5,
 "chunks":[{"ms":0.1,"data":"data: {…}\n\n"},{"ms":38.2,"data":"data: {…}\n\n"},…],"bodyMs":0.4}
```

`seq` is the arrival order within the lane; `chunks` are the events
verbatim, each with the pause before it; a plain response is `body`
verbatim with `bodyMs`. `inspect` lists a cassette numbered as replay
numbers it (`t2·3`: thread 2, its third call); `-v` adds each call's newest
input. Matching data (hashes, thread keys) is derived when a cassette
loads, never stored — a better matcher needs no re-recording.

## Security

- **A cassette is sensitive.** It holds every prompt and answer verbatim,
  and with them whatever the agents read and ran: file contents, command
  output, anything secret that showed up there. Keep cassettes under
  `/home/magik6k/buxon/.film-media/replay/` — excluded from git by the main
  checkout's `.git/info/exclude` — never in the repository, a tile, a
  shared drive or a chat. `record` refuses a path git would track
  (`git check-ignore`; `-allow-tracked` overrides) and writes cassettes
  `0600` in `0700` directories. A cassette copied into a sandbox for
  replay is readable by whoever can reach that sandbox: delete the sandbox
  (and its snapshots) when the shoot is done.
- **No credential is ever written.** Request headers are kept from an
  allowlist (`Content-Type`, `Accept`, `Anthropic-Version`,
  `Anthropic-Beta`, `OpenAI-Beta`, `User-Agent`, `X-App`): never
  `Authorization`, `x-api-key` or cookies. Response headers likewise
  (content type, rate-limit and request-id headers; never `Set-Cookie` or
  organization ids). The tests check it.
- **Keys come from the environment only** — flags would show in `ps` — and
  the startup log names the variable, never the value. A replay holds no
  key at all.
- **Whoever reaches a recording proxy spends its keys.** It listens on
  `127.0.0.1` by default. Anywhere else, set `LLMREPLAY_TOKEN`: callers
  must then send it as their API key (Bearer or `x-api-key`; `401`
  otherwise, the control routes included) and it never goes upstream.
- **Logs carry excerpts**: a DRIFT line quotes ~70 characters of each side,
  `inspect -v` the newest inputs. Keep them out of shared channels and out
  of frame.
- A replayed answer was a real model's answer once: review the recording
  like any real output before it goes into a published video.

## Limits

- Only the models are replayed. Tools, sandboxes and files run for real on
  every take: a take that diverges gets the recorded answers anyway, and a
  DRIFT line says where.
- WebSocket transports (Realtime, Responses over WebSocket) are not
  proxied; bodies over 64 MiB and responses over 256 MiB are refused.
- Interrupting a take where the recording didn't (or not interrupting
  where it did) drifts like any other divergence.

## Tests

`go test ./hack/demo/llmreplay/` (`-race` clean) — httptest fake upstreams
for the three wires: Chat Completions, Responses and Messages recorded and
replayed byte for byte, streamed and not; the upstream gets the proxy's key
and never the caller's, the lane or cookies, and no credential reaches a
cassette; sequence matching across threads, parallel subagents and two
lanes replayed in another order with every volatile detail changed; drift
on a different tool result, a retyped prompt finding its thread; misses in
each API's error shape; replay timing (original, scaled, capped, instant —
and on the real clock, event by event); lookups and the made-up model list;
transient errors left out or kept; a hung-up replay put back for its retry;
`LLMREPLAY_TOKEN`; caller credentials passed through; passthrough;
concurrent conversations; the cassette file (refusal, append, torn last
line, `0600`); `inspect`; the git guard; `-detach` (silent on stdout, a
second start leaves the first alone); normalization, thread keys and the
event splitter. `BenchmarkLongSession`: a 150-call, 25 MB coding session
loads and a whole retake of it matches in about 1.2 s (1.5 s on 2 CPUs).

Also checked end to end on the dev box (fakes, no real keys):

- xbind → llm-gw (`net=host`, backend base URL `…/lane/agent/v1`) →
  llmreplay → `hack/fakeopenai`: the model list, a streamed chat call, a
  title call and a streamed Responses call recorded, then replayed with
  fakeopenai gone and llm-gw untouched — identical bytes through llm-gw, on
  the recorded clock, and llm-gw's usage counters moving as if real. (The
  agent template itself didn't run here: its encrypted resources need
  FUSE, which this dev session can't mount.)
- Claude Code 2.1.280 (the rootfs's version) against a fake Messages API:
  `claude -p` recorded and replayed with identical output, by the path lane
  and by `ANTHROPIC_CUSTOM_HEADERS`; a tool loop (`tool_use` → Bash `ls -la`
  → `tool_result` → answer) replayed after the listing's times changed,
  matched by thread with no drift, at `-timing 0.5`; and the in-sandbox
  recipe on the host — `ANTHROPIC_BASE_URL` from `~/.claude/settings.json`
  alone, the replayer started by the `SessionStart` hook with `-detach`,
  two takes with a reset between them (a day after the recording: dates
  masked, no drift), both identical to the recording.

Not exercised here: a real coding sandbox (this dev session can't run
isolation either), so steps A.1–A.5 rest on the coding-sandbox page and the
sandbox-manager contract as documented; the first shoot should check
`/_llmreplay/status` from the sandbox's Terminal after a take.
