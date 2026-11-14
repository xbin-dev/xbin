# S4 — hooking coding harnesses up to the LLM connector (spike)

Spike for `plans/workspace-agent.md` §5.4 and §5.7 (issue #51), 2026-11-14.
Branch `wsagent/s4-connector`. The connector itself is B1's; this says how
harnesses and terminals get pointed at it, and proves it once.

**Result.** OpenCode 1.18.32 in a tile agent session answered a prompt
through an xbind route in front of `hack/fakeopenai`, with no sign-in and
nothing but injected environment, on three backends: the relay netns
(`internet`), host networking, and a Firecracker VM. Codex's pinned adapter
does the same from one variable (shown outside xbind). Claude Code and
Gemini need a wire the connector does not have yet. Sandboxes made by a
sandbox manager cannot reach xbind at all today; that needs one narrow
change (§2.3).

Evidence marks: **[run]** observed on this box with the pinned binaries from
`.rootfs` (opencode 1.18.32, codex-acp 1.13.1, codex 0.156.1,
claude-agent-acp 0.81.1, claude-code 2.1.280, gemini-cli 0.60.0);
**[bin]** the name is present in the pinned binary or bundle, behaviour not
run; **[doc]** upstream docs or source, read at a nearby version, not run.

## 1. Per harness

| Harness | How (injected only) | Wire | No sign-in | Evidence |
|---|---|---|---|---|
| OpenCode | `OPENCODE_CONFIG_CONTENT` (inline JSON config) declaring a provider; key by `{env:XBIN_LLM_KEY}` | Chat Completions (`@ai-sdk/openai-compatible`) or Responses (`@ai-sdk/openai`) | yes | [run], in xbind |
| Codex (`codex-acp`) | `DEFAULT_AUTH_REQUEST` = a `gateway` auth request (base URL + headers) | Responses only | yes | [run], outside xbind |
| Claude Code | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` + model names | Anthropic Messages | yes, once the connector has `/v1/messages` | [bin] + [doc] |
| Gemini CLI | `GOOGLE_GEMINI_BASE_URL` + `GEMINI_API_KEY` | Google `generateContent` only | no OpenAI mode exists | [bin] + [doc] |

### 1.1 OpenCode

- `OPENCODE_CONFIG_CONTENT` exists in 1.18.32 [run]. It is deep-merged over
  the person's global and project config, and wins over both [run: case D
  below; doc: load order remote → global → `OPENCODE_CONFIG` → project →
  `.opencode/` → inline → console org → `/etc/opencode`].
- The provider, as injected by the proof:

  ```json
  {"provider": {"xbin": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "LLM by xbin",
      "options": {"baseURL": "<XBIN_LLM_URL>", "apiKey": "{env:XBIN_LLM_KEY}"},
      "models": {"<id>": {"name": "<label>"}}}},
   "model": "xbin/<default id>"}
  ```

  `{env:VAR}` substitution works [run], so the token sits in one variable
  and the config text is static. Models must be listed: OpenCode does not
  read `/models` for a custom provider. Model entries also take
  `limit.context`/`limit.output`, `tool_call`, `reasoning` [doc]; xbind
  fills them from the connector's model list at spawn.
- Both provider packages are bundled in the binary: nothing was fetched
  from npm for them [run: the npm cache held only `@opencode-ai/plugin`,
  `@opentui/core`, `web-tree-sitter`]. `@ai-sdk/openai-compatible` posted
  to `/chat/completions`, `@ai-sdk/openai` to `/responses` [run].
- Default model in `opencode acp` [run, `session/new` probes, no prompt sent]:

  | Case | Reported model |
  |---|---|
  | A: no config, no sign-in | `opencode/big-pickle` |
  | B: inline provider, no `model` | `opencode/big-pickle` |
  | C: inline provider + `model` | `xbin/fake-chat` |
  | D: person's `opencode.json` names a model, inline has `model` | `xbin/fake-chat` (inline wins) |
  | E: person's `opencode.json` names a model, inline has none | the person's model |
  | F: person has an `auth.json` login, inline has `model` | `xbin/fake-chat`; both providers offered |
  | G: same login, inline has no `model` | `opencode/big-pickle` |

  So: declaring the provider is not enough, the `model` key (or the ACP
  `model` config option) is what makes the connector the default; and the
  inline `model` overrides a model the person configured themselves.
- **Today's default sends prompts to a third party.** With no sign-in,
  OpenCode enables its own "Zen" provider with a public key and picks one
  of its free models (case A). An org that must keep prompts on private
  upstreams needs `"disabled_providers": ["opencode"]` in the inline
  config [doc; not run]. This is for §5.6's org constraints.
- A person's `opencode auth login` (`auth.json`) is untouched and stays
  usable next to the connector (cases F, G). `OPENCODE_AUTH_CONTENT`
  would replace it [doc]: never set it.
- Never put `OPENAI_API_KEY` (or any other provider key variable of
  `Keys`) in an agent session's environment: OpenCode turns each into one
  of its built-in providers, pointed at that vendor's own address.

### 1.2 Codex

- The CLI speaks **Responses only**: `wire_api = "chat"` is a config error
  at 0.156 [doc]. `OPENAI_BASE_URL` is not read by the provider code
  [doc]; `/etc/codex/config.toml` sits below the person's config [doc].
- `codex-acp` 1.13.1 reads `CODEX_CONFIG` (JSON merged into the session
  config), `MODEL_PROVIDER` and `DEFAULT_AUTH_REQUEST` at start [bin:
  `dist/index.js` `startAcpServer`].
- `CODEX_CONFIG` + `MODEL_PROVIDER` naming a custom `model_providers`
  entry is **not enough** signed out: `session/new` still answers
  `-32000 Authentication required` [run]. The adapter asks the app
  server's account state, which does not see the per-session config.
- What works [run]: `DEFAULT_AUTH_REQUEST={"methodId":"gateway","_meta":
  {"gateway":{"baseUrl":"<XBIN_LLM_URL>","headers":{"Authorization":
  "Bearer <key>"},"providerName":"LLM by xbin"}}}`. `session/new` opens
  with no sign-in and the turn posts to `<baseUrl>/responses`. No
  `auth.json` is written.
- Three things the connector must do for it [run]:
  - serve Codex's model names or have xbind set the session's `model`
    option: the adapter sent its own default (`gpt-6-astra`), not a model
    of the upstream;
  - send `usage.total_tokens` in `response.completed`: without it Codex
    drops the stream and retries (`hack/fakeopenai` lacks it, so the fake
    answered six times);
  - expect the key in `$HOME`: Codex wrote its environment, the request
    included, to `~/.codex/shell_snapshots/`. A key given to Codex must be
    narrow and short-lived (§3).
- This is a replacing hook-up: while the gateway request is set, the
  person's own `codex login` is not used.

### 1.3 Claude Code (needs the Anthropic-wire passthrough)

What the connector must offer, and what xbind then injects [doc:
code.claude.com llm-gateway pages; names all present in 2.1.280, bin]:

- Routes: `POST <base>/v1/messages` (called as `/v1/messages?beta=true`,
  streaming), optionally `POST <base>/v1/messages/count_tokens`. Forward
  `anthropic-version` and `anthropic-beta` unchanged. `GET /v1/models`
  only when `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`.
- `ANTHROPIC_BASE_URL=<XBIN_URL>/api/xbin/llm` (Claude appends
  `/v1/messages`, so one root serves both wires if the passthrough is
  mounted at `/api/xbin/llm/v1/messages`).
- `ANTHROPIC_AUTH_TOKEN=<key>` (sent as `Authorization: Bearer`; not
  `ANTHROPIC_API_KEY`, which goes as `x-api-key` and asks for approval).
- Model names the upstream knows: `ANTHROPIC_MODEL`,
  `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`,
  `ANTHROPIC_DEFAULT_HAIKU_MODEL` (haiku also runs background tasks).
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`.
- Precedence inside Claude Code: `ANTHROPIC_AUTH_TOKEN` outranks
  `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` and the `/login`
  subscription. So this is a replacing hook-up, and `ANTHROPIC_BASE_URL`
  must never be set alone: the subscription token would then be sent to
  the connector.
- Trap: an `env` block in the person's `~/.claude/settings.json` can
  override injected variables.
- `claude-agent-acp` 0.81.1 carries a `gateway` auth method and
  `providers/set` [bin]; whether they are a cleaner path than the
  variables is not checked.

### 1.4 Gemini CLI

No OpenAI-compatible mode [doc; bin: no `OPENAI_BASE_URL` in the bundle].
`GOOGLE_GEMINI_BASE_URL` + `GEMINI_API_KEY` point it at a gateway that
speaks Google's wire. Trap: ACP `session/new` takes
`security.auth.selectedType` from the person's settings before the
environment, so a saved Google login wins unless system settings
(`GEMINI_CLI_SYSTEM_SETTINGS_PATH`) say otherwise. Out of scope until the
connector has a Google-wire passthrough; `Connector` stays nil.

### 1.5 Precedence rule (the person's own login keeps working)

Who serves a session's model calls, first match wins:

1. **The session's own choice**: a new field on `POST /term/sessions`
   (`llm: "connector" | "own"`), an account picker in the Agent tab.
2. **The person's saved default for that harness** (a per-user pref).
3. **The workspace default**: `own-first` (default) — the person's own
   sign-in when one exists, else the connector. An admin or the host
   config may set `connector-first` or `connector-only` (org constraints).

"Own sign-in exists" means a sign-in xbind holds for the person (§5.7) or
one of the provider's `LoginFiles` in the session's `$HOME`. xbind never
reads `$HOME` itself (D78): it sends both environments to the agent host
(`bx __agent-host`, inside the sandbox), which checks `LoginFiles` and
applies the connector's only when the rule says so.

Two kinds of harness:

- **Additive** (OpenCode): the connector's provider is always declared, so
  its models sit next to the person's own in the model picker. The rule
  only decides whether the inline config carries `model`.
- **Replacing** (Codex, Claude Code, Gemini): the connector's variables
  are set only when the connector wins, and never together with a key of
  the person's (`Keys`).

## 2. Network path

How `bx` finds xbind in a sandbox: `XBIN_URL` and the session's
`XBIN_TOKEN` in the environment (`cmd/bx/main.go:207-243`,
`internal/term/sandboxenv.go:41-86`). No unix socket.

### 2.1 Tile terminals and tile agent sessions

Both go through the same `sandboxShell` (`internal/term/term.go:566`); an
agent session only swaps the entry for `bx __agent-host`.

| Session | Reaches xbind | How | Evidence |
|---|---|---|---|
| no isolation (host shell) | yes | `XBIN_URL=http://<listen>` on loopback (`term.go:500-527`) | code |
| isolated, `host` net | yes | shares the host netns, loopback | [run] |
| isolated, relay scopes (`internet`, `org`, `personal`, `set:<name>`) | yes | `XBIN_URL=http://10.0.2.2:<port>`; the relay forwards that gateway port to xbind on loopback, exempt from the egress rules (`sandboxenv.go:23-33,52-55`, `term.go:702-719`, `relay.Config.HostFwd`) | [run] for `internet` |
| isolated, `none` | no | netns with no relay; `XBIN_URL` still names loopback, which is not xbind there (`term.go:586,676`, `scopes.go:16`) | [run]: the turn ends in an error |
| VM, Firecracker on KVM | yes | the guest sits behind the same relay and gateway (`internal/vm/manager.go:318-321`) | [run] |
| VM, QEMU emulation | expected yes | same relay path | not run |

The tile's `net` rules never block the connector: the host-forward is
policy-exempt. An `api: false` session has no `XBIN_TOKEN`
(`term.go:380`); it should still get the LLM key (§3).

For B1: `/api/xbin` routes are default-deny for a tile credential bound to
a non-primary deployment or acting in a partition until the pattern is in
`routeClasses` (`internal/server/deployclass.go:441-470`) and
`partitionClasses` (`partitionclass.go:118-150`). The LLM routes need
both entries or sessions on such tiles get 403, plus a row in `openapi.go`
and `docs/protocol.md`. The guards say so on this branch: the spike's two
routes fail `TestRouteInventory`, `TestDeploymentRouteClasses` and
`TestPartitionRouteClasses` (`internal/apicheck`). They are left failing
rather than given rows: a throwaway route does not belong in the builder
docs. The 403 itself was not exercised (the proof ran on a primary,
unpartitioned tile).

### 2.2 Sandboxes made by a sandbox manager

They cannot reach xbind, by design:

- `internal/tilesbx/netcfg.go:49-70`: the relay gets `Deny` with xbind's
  listen addresses and no `HostFwd`; "the gateway a dead end".
- `relay.HostDeny` also denies every address the host delivers locally
  (`internal/sandbox/relay/deny.go:23-30`), so the workspace's public URL
  does not work from the same host either.
- `docs/sandbox-manager.md:650-653`: "No xbin identity, ever: no token,
  no gateway, no route to xbind".

Environment is not the problem: the agent template already builds the
harness's environment (`builtin-templates/agent/_backend/harness_engine.go:558`).

### 2.3 Least invasive route for manager sandboxes

Recommended: **an LLM-only listener, forwarded at the gateway, asked for
per sandbox.**

- xbind serves the connector's `/v1/*` routes, and nothing else, on a
  unix socket of its own. It accepts LLM tokens only (§3).
- A sandbox created with `llm: true` (a new optional create field; the
  manager advertises feature `llm` in its hello) gets one `HostFwd` entry
  in `relayConfig`: gateway port → that socket (`relay.Config.HostDial`
  already allows `unix:` targets). In the sandbox:
  `http://10.0.2.2:<port>/v1`.
- It works with `egress: none` and in VM sandboxes (same relay).
- The contract line becomes: no route to xbind's API or the workspace;
  the LLM connector only, when the consumer asked for it.
- Roughly 40 lines in `internal/tilesbx` plus the listener, and the
  `docs/sandbox-manager.md` text.

Alternatives, not recommended first:

- **Relay through the consumer.** The agent template runs a forwarder in
  the sandbox over the manager's `stdio` exec and answers its HTTP from
  the tile backend's own `llm` binding. No contract change and the key
  never enters the sandbox, but it is a multiplexing protocol to build
  and keep. The right answer for managers that run sandboxes off-host.
- **Public URL + `internet` egress.** Only for off-host managers with a
  published workspace URL.

## 3. Identity

A session carries one credential: its terminal token, an element
principal `{Component: tile, UserID: person, Via: "terminal"}`
(`internal/auth/auth.go:397-430`). The proof used it as the API key and
xbind's ordinary auth resolved it (`via=terminal tile=apps/welcome`).

**Do not ship that.** Recommend a new, narrow token:

- A tool that reads `OPENAI_API_KEY` but not the base URL sends the key to
  the vendor (Codex ignores `OPENAI_BASE_URL`; OpenCode's built-in
  providers use their own address). The terminal token is the tile's API
  credential.
- Harnesses persist their environment (Codex's shell snapshots, §1.2).
- `api: false` sessions have no terminal token and should still get a model.

**LLM token.** `auth.MintLLM(userID, component, session)`, kept in memory
next to the terminal tokens, minted in `term.prepare` and revoked by the
same `revokeTok` (lifetime: the session).

- Scope: the LLM routes only. Resolve it to a principal with
  `Via: "llm"` and refuse that `Via` on every other route in one gate, so
  metering and org filters still see a principal.
- Attribution: the person who owns the session, plus tile, session and
  harness as labels.
- A recognisable prefix (`xbl_`) so redaction and secret scanners match it.
- The connector may also accept a person's cookie or a terminal token
  (`bx llm`, `curl` with `$XBIN_TOKEN`); the injected key is always the
  narrow one.

**The agent template** (harnesses in manager sandboxes), new SDK call:

```go
// LLMKey asks xbind for a connector key acting for user: good for the LLM
// routes only, metered to user and this component, dead at Expires or Revoke.
type LLMKeyOpts struct {
    User  string        // the person the run acts for
    TTL   time.Duration // capped by xbind; the run's lifetime
    Label string        // harness and run id, for the usage table
    Sandbox string      // optional: the sandbox it is for (picks URL, §2.3)
}
type LLMKey struct {
    ID, Token string
    URL       string    // the base URL as that sandbox reaches it
    Expires   time.Time
}
func LLMKeyFor(ctx context.Context, o LLMKeyOpts) (LLMKey, error) // POST /api/xbin/llm/keys
func RevokeLLMKey(ctx context.Context, id string) error           // DELETE /api/xbin/llm/keys/{id}
```

xbind grants it to a component whose `llm` slot is bound to the connector
(the binding is the grant), for a person who may use that tile; quotas are
per person and per component. The worst a leaked key does is spend that
person's quota until it expires. The broader "act as this person"
primitive (§4 of the plan) is not needed for this.

## 4. The provider table

Addition to `sdk/acp/providers.go` (stdlib only; `Provider` only grows):

```go
// Connector is how to point a provider's CLI at an LLM connector — an
// endpoint a host serves in place of the vendor's — from its environment
// alone. nil: the CLI cannot (no wire the connector speaks).
type Connector struct {
    // Wire the CLI speaks to it: "chat" (OpenAI Chat Completions),
    // "responses", "anthropic" (Messages) or "google".
    Wire string
    // Additive: the CLI keeps the person's own providers next to the
    // connector's (opencode). false: the variables replace the person's
    // sign-in for the session; set them only when the connector serves it.
    Additive bool
    // Env builds the variables from the endpoint. With Default false an
    // additive CLI declares the provider and leaves its default model alone.
    Env func(c ConnectorParams) map[string]string
    // ModelOption is the ACP config option a client sets to pick one of
    // the connector's models after session/new ("model"), and ModelPrefix
    // what the CLI puts before a model id there ("xbin/").
    ModelOption, ModelPrefix string
    // Never lists variables that must not be set next to Env's: the keys
    // that would send the connector's key to a vendor, or outrank it.
    Never []string
}

// ConnectorParams is one endpoint for one session.
type ConnectorParams struct {
    Root    string   // …/api/xbin/llm — Anthropic clients append /v1/messages
    URL     string   // Root + "/v1", the OpenAI base URL
    Key     string   // the session's LLM key
    KeyEnv  string   // the variable Key is in: XBIN_LLM_KEY
    Name    string   // "LLM by xbin"
    Models  []ConnectorModel // id, label, context, output; first is the default
    Default bool     // the connector serves this session (§1.5)
}

func (p Provider) ConnectorEnv(c ConnectorParams) map[string]string // nil without a Connector
```

Entries:

| Provider | Wire | Additive | Env | ModelOption | Never |
|---|---|---|---|---|---|
| opencode | chat | yes | `OPENCODE_CONFIG_CONTENT` (§1.1; `model` only with `Default`) | `model`, prefix `xbin/` | `OPENAI_API_KEY`, `OPENCODE_AUTH_CONTENT` |
| codex | responses | no | `DEFAULT_AUTH_REQUEST` (§1.2) | `model` | `CODEX_API_KEY`, `OPENAI_API_KEY`, `MODEL_PROVIDER` |
| claude | anthropic | no | `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | — | `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` |
| gemini | — (nil) | | | | |

A consumer offers a provider's connector only when the connector serves
its `Wire`. `acp.SpawnParams` gains the connector's environment and the
provider's `LoginFiles` so the agent host applies §1.5's rule.

## 5. Plain terminals

Variables:

- `XBIN_LLM_URL` (`<XBIN_URL>/api/xbin/llm/v1`) and `XBIN_LLM_KEY`: always,
  when the switch below allows.
- `OPENCODE_CONFIG_CONTENT`: the same inline provider, so `opencode` typed
  in a terminal works with no setup.
- `OPENAI_BASE_URL` + `OPENAI_API_KEY`: the pair most tools read. Only in
  plain terminals, never in agent sessions (§1.1). A person's own
  `export` in their shell rc runs later and wins.

Is it an escalation? No new authority over the workspace: a terminal is
human-plane and already holds the tile's API token and the person's
`$HOME` logins. It is a new thing to spend: any code run in that terminal
(a build script, a dependency's install hook) can use the person's quota
and reach upstreams marked private, for as long as the session lives. A
tile's backend never sees it (its environment is the runner's; it binds
the `llm` slot and is metered as the component).

Guards:

- the narrow, session-lived key, metered per person with a quota;
- a workspace switch with three settings, lockable from
  `XBIN_LLM_CONFIG`: agent sessions and terminals (default), agent
  sessions only, off;
- the person's `CanTermAPI` grant (D17) as the per-user gate: someone who
  may not hold a tile API token in a terminal gets no LLM key there;
- a separate switch for the `OPENAI_*` pair (default on).

Not checked: what OpenCode does in a terminal that has both
`OPENAI_API_KEY` and `OPENAI_BASE_URL` (whether its built-in `openai`
provider honours the base URL). If it does not, the pair's default should
be off.

## 6. The proof

On the branch, throwaway, on only while `XBIN_S4_LLM_UPSTREAM` is set:

- `internal/server/s4llm.go`: `GET|POST /api/xbin/llm/v1/{rest...}`, a
  streaming reverse proxy to the upstream, the caller's `Authorization`
  stripped.
- `internal/term/s4connector.go` (+ one line in `agent.go`): an agent
  session gets `XBIN_LLM_URL`, `XBIN_LLM_KEY` (the terminal token, see §3)
  and, for OpenCode, `OPENCODE_CONFIG_CONTENT`.

The code is its own commit, apart from this document, and is not for
merge: `go test ./internal/apicheck/` fails on it by design (§2.1).

Run: `hack/fakeopenai` on 127.0.0.1:18881; xbind from this branch,
`--dev --no-auth --isolate --rootfs <.rootfs>` on 127.0.0.1:18880, a fresh
workspace; `bx agent run --tile apps/welcome --provider opencode "hello"`.

| Run | Result |
|---|---|
| default (`internet`, relay) | `[ready] · model xbin/fake-chat`, "Hello from the fake model.", `end_turn` |
| `--net host` | same |
| `--vm` (Firecracker, KVM) | same |
| `--net none` | session opens, the turn ends in `error` (xbind unreachable) |

xbind logged each call as `via=terminal tile=apps/welcome`, user agent
`opencode/1.18.32`; the fake recorded two Chat Completions requests per
turn (title, answer). The home was fresh: no `auth.json`, no config file.

Outside xbind, same binaries, a scratch `$HOME`: OpenCode's Responses
variant, the config-precedence cases of §1.1, and Codex through
`DEFAULT_AUTH_REQUEST` (§1.2).

## 7. Not proven

- Attribution to a person: the run was `--no-auth`, so the principal had a
  tile and no user id.
- A turn with tool calls through the proxy; only a text answer.
- Codex inside an xbind session, and against an upstream that sends
  `usage.total_tokens`; Codex's model naming against a real model list.
- Claude Code and Gemini: nothing run; the connector lacks their wires.
- QEMU-emulated VM sessions; partitioned tiles; non-primary deployments
  (§2.1's route-class note).
- Manager sandboxes: §2.3 is a design, not built.
- OpenCode with no internet (it fetched models.dev's catalog at start;
  `OPENCODE_DISABLE_MODELS_FETCH` exists), and `disabled_providers` for
  Zen.
- The [doc] facts were read at nearby versions (opencode v1.18.0 source,
  codex-acp and claude-agent-acp main); the names this plan relies on
  were confirmed present in the pinned binaries, their behaviour only
  where marked [run].
