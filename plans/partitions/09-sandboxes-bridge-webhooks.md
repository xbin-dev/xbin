# 09 — coding-sandbox, sandbox-terminal, messaging bridge, webhooks, llm-gw

Verdict: **none of these five is partitioned.** Each serves one shared
external thing (a sandbox runtime and its quotas, an SSH port, a bot
connection, public webhook URLs, upstream LLM keys) and holds no per-person
data a partition would protect — or holds it keyed per person already. What
they need is to key correctly on the new caller identity, to be reached by
the right instance of a partitioned agent, and to be recognised as part of a
partitioned consumer's **trust base** (PD-23, S7): each of them sees what the
partitions that call it send.

## 1. coding-sandbox (builtin-templates/coding-sandbox)

### Current behaviour

- Consumer = `X-XBin-From` (`_backend/manager.go:276` `callerOf`); a sandbox
  is visible to its home consumer and consumers it is shared with
  (`visible` `:312`); person checks (`personOK` `:323`) only on verified
  calls; a backend's `Sbx-User` is an unchecked assertion; `canAdmin`
  (`:341`).
- Records `Owner{Via, User}`; the tile runtime is told `For: rec.Owner.Via,
  ForUser: rec.Owner.User` (`create.go:403`); quotas per consumer and per
  person (`quotas.go:121,156`).
- Operators (write level on the manager) see metadata only, never contents
  (`operator.go:1-6`, `:21`).
- The manager holds `cap:sandboxes` (D113) — tilesbx keys by
  `Key{Tile, Deployment}` (`internal/tilesbx/keys.go:19`).

### The change (PD-39)

- **Stays non-partitioned** (a partitioned tile can't hold `cap:sandboxes`,
  PD-28; its quotas and images are workspace-level).
- **Consumer identity = (From, partition id)** (07 §4): `caller` gains
  `partID` (`X-XBin-Partition-Id`) and `part` (`X-XBin-Partition`, display);
  `record.Owner` gains `PartitionID string json:"partitionId,omitempty"`
  and `Partition string json:"partition,omitempty"` (absent for today's
  consumers and for a partitioned consumer's global instance — **`""` ≡
  `global`**, so every existing sandbox of an agent that turns partitions on
  stays with its global instance, S14); `visible`/`personOK`/`canAdmin`/
  `share` compare the pair; `shares` entries accept an optional
  `partitionId`.
- **Person from the partition:** when the call carries a partition id, the
  person is the partition's: an `Sbx-User` that differs → 403 `not-allowed`;
  `personOK` treats the call as verified.
- **Global-home records seen from the same consumer's partitions (C7):**
  visible when `personOK` passes for the partition's person (team, member,
  share) — the agent page's direct terminal dial
  (`builtin-templates/agent/sandboxes.js:29-33`) keeps working for shared
  conversations. Never the converse.
- **Quotas:** per consumer keeps summing by tile (all partitions of
  `apps/agent` share its consumer quota); per person as today.
- **`hello`:** `caps.partitions: 1`.
- **Runtime labels:** `For` stays the tile; a new `ForPartition` label (the
  partition id) is passed to tilesbx for the admin sandbox registry
  (metadata).
- **Operators (S19):** unchanged (metadata only); names of records homed in a
  user partition are shown as `<consumer>/<partition id, 8> #<n>` unless the
  record is shared with the viewer — model-generated names can carry content;
  labels likewise (the agent's `xbin.agent/home`/`conversation` labels are
  ids, not content).
- **Trust base:** the manager's writers (and admins) reach every private
  sandbox; `bx doctor` and the partitioned consumer's trust panel list it
  (06 §4). The "reviewed code only" switch (PD-23, decided) requires its
  primary to be protected when a partitioned consumer binds it.
- **Binding it to a partitioned agent** is a global bind, so it is an
  admin's act (PD-16, 05 §3). A person who owns a sandbox-manager tile may
  personal-bind it into their own partition of the agent. It then holds
  only that person's sandboxes, keyed by the same partition id.
- Contract tests: `userPartitionChecks` (07 §4).

## 2. sandbox-terminal (builtin-tiles/sandbox-terminal)

### Current behaviour

A consumer of sandbox managers (`interfaces.sandboxes` multi), per-person SSH
keys in one kv (`backend/keys.go`), one TCP port 2222 (`exposes.ssh`,
`backend/sshd.go`), access re-checked per person every 30 s
(`backend/access.go:1-15`).

### The change

- **Stays non-partitioned** (one port, one key store keyed by person — a
  partition per person would need N listeners).
- Its consumer identity is unchanged (`apps/sandbox-terminal`, no
  partition). A sandbox created by a person's agent partition reaches it only
  through an explicit share `{consumer: "apps/sandbox-terminal", users:
  ["alice"]}` — the agent's "open in terminal" action already shares per
  person; the partition-aware manager's share path is verified by the
  contract suite.
- Docs: people's keys are per person already; admins with write on
  sandbox-terminal see key metadata (as today); its writers are in the trust
  base of partitions whose sandboxes are shared to it (06 §4).

## 3. Agent messaging bridge (builtin-templates/agent-messaging-bridge)

### Current behaviour

`alwaysOn`, one bot connection, one `agent` slot (service `agent-inbox`),
state `res:…/state`; calls `/adapter/hello|message|files|ack|outbox`
(`_backend/agent.go:66-152`); linking happens from its **frame**, which
calls the agent's `/adapter/link` as the signed-in person.

### The change

- **Stays non-partitioned.** Against a partitioned agent it reaches the
  agent's **global** instance (05 §1); binding it to a partitioned agent
  without `global` is refused at bind time (05 §3).
- **No wire change:** global keeps one outbox stream per adapter; DMs of
  linked people are mailed to their partitions and replies come back through
  global, which picks the destination from its own handoff record (08 §5).
  The frame's `/adapter/link` call reaches global with `X-XBin-User` (the
  link lands in global's `db`, as today).
- A linked person's DM waits (in their partition's inbox) until their
  partition has run once (mail never starts a never-run partition, 04 §3);
  the agent answers such a first DM with a short notice ("open <agent> once
  to receive your messages privately").
- The bridge's console ("try it first") works against global.
- AGENTS.md of the template (the coding agent's guide) gains a paragraph:
  "the agent may be partitioned; you always talk to its global instance".
- Tests: the bridge e2e against a partitioned agent fixture (linked DM
  round trip, group message, link/unlink).

## 4. Webhooks (builtin-tiles/webhooks)

### Current behaviour

Hooks have no owner (writers manage them); `/hook/<id>` is public via
ingress; each delivery POSTs `/adapter/event` to bound agents
(`backend/main.go:176` `forward`), data class `public`.

### The change

- **Stays non-partitioned.** Deliveries reach the agent's global instance,
  which runs team triggers itself and mails private triggers' events to their
  person's partition (08 §5). A private push trigger needs a non-empty,
  non-overlapping `match` (08 §5, S10), so a person can't capture every
  hook's traffic. No code change here; the 404 "no trigger" / 503 retry
  semantics carry through (global answers once the event is stored or mailed,
  not when the partition ran it — documented in API.md).
- The webhooks tile's own `exposes` keep working (it isn't partitioned).
  A partitioned tile's `exposes` would be served by its global instance only.

## 5. llm-gw (builtin-tiles/llm-gw)

### Current behaviour

OpenAI-compatible proxy with per-backend usage counters (`backend/main.go`,
`:617-775`), relaying every prompt body (`:640-700`), no per-user data;
bound as `openai` providers (roles reader/writer).

### The change

- **Stays non-partitioned** (no per-person data; shared upstream keys).
- Every partition's calls land here with `X-XBin-Partition`/`-Id`. The agent's
  tile-wide LLM cap is on by default (08 §8), so upstream concurrency stays at
  today's level.
- **Per-(From, Partition-Id) usage counters on by default** (metadata:
  requests, tokens; display name from `X-XBin-Partition`); a per-(From,
  Partition-Id) fairness limit stays optional, off by default.
- **Trust base (S7):** llm-gw sees every private prompt it relays; its
  writers (and admins) are listed in the trust panel of partitioned consumers
  bound to it, and the "reviewed code only" switch covers it (06 §4).
  Binding it to a partitioned agent is an admin's global bind (05 §3).
  People may personal-bind their own OpenAI-compatible tiles instead.

## 6. Summary

| Tile | Partitioned? | Code change | Contract/doc change |
|---|---|---|---|
| agent template | yes, by default for new instances (`user`,`global`, PD-35); existing instances keep their mode | large (08) | agent-inbox: adapters reach global |
| coding-sandbox | no | consumer = (From, partition id), `""` ≡ global; person from partition; global-home visible by person; operator redaction; `caps.partitions` | sandbox-manager.md |
| sandbox-terminal | no | none (verify shares) | note; trust base |
| messaging bridge | no | none | AGENTS.md note; binding needs global; first-DM notice |
| webhooks | no | none | API.md note on answer semantics and private-trigger rules |
| llm-gw | no | per-partition counters on | note; trust base |
