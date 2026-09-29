# 07 — SDK, client and service contracts

Hard rules: the Go `sdk/` module stays zero-dependency; SDK semantics change
only permissively (docs/compat.md rule 8); no JS build step, no TS in `web/`.

## Current behaviour

- Go SDK: `Self()`, `Deployment()` (`sdk/xbin.go:89`, from
  `XBIN_DEPLOYMENT`), `CallerInfo{From, Role, Owner, User, UserLevel,
  ViewedBy, Deployment}` (`:92`) read by `Caller(r)` (`:118`); `Resource`
  (`:264`), `Secret` (`:275`), `Publish` (`:332`; non-200 is an error,
  `:342`), `BusEvent` (`:352`), `Subscribe` (`:366`; builders are told to
  subscribe at every start, `:362-365`), `Client()` (`:216`), the status
  call (`:419`).
- JS client (`web/xbin-client.js`): `xbin.deployment` from
  `<meta name="xbin-deployment">` (`:59`), `bfetch` (`:104`) attaches the
  frame token, `bus` (`:169`), `status/clearStatus/notify` throw on non-2xx
  (`:196-210`).
- Contracts: docs/sandbox-manager.md (consumer = `X-XBin-From`, `:47`;
  asserted person `Sbx-User`, `:52`; shares `[{consumer, users}]`, `:64`);
  docs/agent-inbox.md (adapters call `/adapter/*` with role `channel`).

## The change

### 1. Go SDK (`sdk/xbin.go`, zero-dependency)

```go
// Partition returns this backend's partition of a partitioned tile:
// "user:<id>" or "global"; "" when the tile isn't partitioned — or when an
// older xbind runs it, which doesn't know partitions (see RequirePartition).
func Partition() string { return os.Getenv("XBIN_PARTITION") }

// PartitionUser is the person of a user partition ("" otherwise).
func PartitionUser() string

// RequirePartition exits the process (status 3, a line on stderr) unless
// xbind runs it as a partition. A tile whose code must never serve several
// people from one instance calls it first thing in main: an older xbind,
// which ignores "partition" in xbin.json, then runs no backend at all instead
// of one shared one (PD-06, decided). Also useful against a declined switch
// that keeps the tile unpartitioned (01 §2.1).
func RequirePartition()

// CallerInfo.Partition is X-XBin-Partition: the partition the caller acts in
// ("" when none) — a display name. CallerInfo.PartitionID is
// X-XBin-Partition-Id: the stable key of a caller's user partition. Providers
// that keep per-caller state key it on (From, Deployment, PartitionID) and
// treat "" and "global" alike (docs/partitions.md §Providers).
Partition, PartitionID string

// Mail sends partition mail (docs/partitions.md §Mail): from the global
// instance to "user:<id>", from a user partition to "global".
func Mail(to, topic string, data any) (id string, err error)

// MailItem is one inbox item; From is stamped by xbind ("global" or
// "user:<id>") and can be trusted.
type MailItem struct { ID, From, Topic string; Data json.RawMessage; At, Expires time.Time }

// Inbox lists this partition's unacked mail; Ack removes items.
func Inbox(after string, limit int) ([]MailItem, error)
func Ack(ids ...string) error

// GlobalURL is the URL of path on this tile's global instance, for a user
// partition's own calls (F5; attributed to the partition's person at global).
func GlobalURL(path string) string // "http://xbin/api/<self>/<path>?xbin-partition=global"
```

All additions: an older xbind leaves them empty/unused (`Mail`/`Inbox`
return an error naming the missing feature), and nothing that succeeds today
fails (rule 8). `Resource(name)` keeps returning the same canonical path/id in
every partition — the partition is in the bind, not the string. `Publish` is
unchanged (the bus gained no field; still 200).

Node/Python snippets in docs/sdk.md: `process.env.XBIN_PARTITION`,
`req.headers['x-xbin-partition']`, `req.headers['x-xbin-partition-id']`, the
mail routes.

### 2. JS client (`web/xbin-client.js`)

- `xbin.partition` — from `<meta name="xbin-partition">`, present only in a
  partitioned tile's document (like `deployment`, spread conditionally into
  the frozen `window.xbin`, `:429`); `"global"` for viewers who resolve to
  the global instance (root token, `--no-auth`).
- `xbin.fetch(url, {partition: 'global'})` appends `xbin-partition=global`
  (F5, PD-16 decided). The option is stripped before `fetch` and ignored
  unless `xbin.partition` is a `user:` key, so it never reaches the network
  otherwise.
- `xbin.interfaces` (from the `xbin-interfaces` meta) lists the viewer's
  personal binds with `personal: true` (05 §3), an additive field.
- `xbin.status()`/`xbin.notify()` keep working on partitioned tiles
  (`/tile-report` and `/notify` are classed, 02 §8) — covered by the class
  guard's "every route the client calls" rule.
- `/vendor/xbin-client.js` stays at its URL (rule 3); additions are
  additive exports. No mail helper in v1 (a frame uses `xbin.fetch` on the
  mail routes; normally its backend does).

### 3. docs/protocol.md rows (collected)

- Identity headers `X-XBin-Partition`, `X-XBin-Partition-Id`; F5 attribution
  (02 §6, 05 §6).
- Backend contract: `XBIN_PARTITION` env.
- `<meta name="xbin-partition">`.
- `GET/POST /api/xbin/partitions…`:
  - `mode` (keep/switch), `limits`, `consents`, `binds`, `share-log`,
    `credential-confirm` and `mail` (04 §3, 06 §6);
  - the `partitions` event and the `/xbin/partitions` page;
  - principal `xbin/mail` and the doorbell body.
- `GET/PUT /api/xbin/workspace-policies` and the `policies` event (05 §2).
- The switch page's 409 on tile documents, the pending API body, and the
  `/alerts` kind `partition-switch` (01 §2).
- `scope.json` `shared: true | "read"`; `xbin.json` `partition`,
  `partitionMail`, `partitionNote`, `template.partition`; `POST
  /templates/new` `partition: false` (01, 04).
- `XBIN_IFACE_<SLOT>` multi entries and `xbin-interfaces` rows gain
  `personal: true` (05 §3).
- `?xbin-partition=global` (F5).
- Backups: the sealed archive format, `schema: 3`, the archiver's
  `X-XBin-Backup-Subkey` and `POST /archive/erase`, and the
  `/backup-keys/*` routes (11).
- The partition route classes (02 §8) beside the deployment ones.
- Error texts (01, 02, 05, 06).

### 4. Sandbox-manager contract (docs/sandbox-manager.md), protocol 1 → additive

- **Consumer identity:** (`X-XBin-From`, partition id) — the partition part
  is `X-XBin-Partition-Id` for user partitions and **empty for the consumer's
  non-personal identity**, which covers both an unpartitioned consumer and a
  partitioned consumer's **global** instance (`X-XBin-Partition: global`):
  `""` ≡ `global` by definition, so every existing sandbox of an agent that
  turns partitions on stays its global instance's (S14). Managers record it
  as `owner: {via, partitionId?, partition?, user}` (`partition` for
  display); `visible`/`personOK`/`canAdmin` compare the pair.
- **Person:** on a call from a user partition, the person is the
  partition's — a manager treats `Sbx-User` as verified when it equals the
  partition's person and refuses (403 `not-allowed`) a different one
  (today's "asserted" stays for non-partitioned consumers).
- **Global-home records seen from user partitions (C7):** a record homed at
  (X, "") is visible to a call from (X, user partition P) **when `personOK`
  passes for P's (verified) person** — the record is team-visible or the
  person is a member/share of it. So the agent page — which dials managers
  directly with its frame token (`builtin-templates/agent/sandboxes.js:29-33`)
  — keeps opening shared conversations' sandbox terminals, and a partition
  sees exactly what its person could see at global. The converse never
  holds: records homed at (X, P) are invisible to (X, "") and to other
  partitions.
- **Shares:** `shares: [{consumer, partitionId?, users}]`; a share naming
  `consumer` without `partitionId` means that consumer's non-personal
  identity (today's meaning).
- **Negotiation:** `hello` answers `caps.partitions: 1`. A consumer calling
  from a user partition doesn't use a manager without it (it degrades, 05 §4,
  08 §6): otherwise every person's sandboxes would merge under one consumer.
- **Quotas:** per consumer = per tile (all partitions summed); per person as
  today (now trustworthy for partition calls).
- **Operators (S19):** operators see metadata only, as today; for records
  homed in a user partition the name shown is `<consumer>/<partition id,
  first 8> #<n>` unless the record is shared with them — model-generated
  names can carry content.

### 5. Agent-inbox contract (docs/agent-inbox.md)

- Adapters (bridges, webhooks) are non-partitioned tiles: against a
  partitioned agent they reach its **global** instance; binding an adapter
  to a partitioned agent without `global` is refused at bind time (05 §3).
- Nothing in the wire changes for adapters: `/adapter/hello|message|files|
  outbox|ack|event|link|links` keep their shapes; the agent's global
  instance routes per person internally (08 §5).
- Semantics notes: a linked person's DM is answered from their partition (the
  reply still arrives on the adapter's one outbox stream); `/adapter/event`
  answers once the event is stored or handed off, not when a person's
  partition has run it; a DM for a person whose partition has never run waits
  until they open the agent (the agent may answer the chat with a notice).
- `hello` reply gains `partitioned: true` (informational, additive).

## Tests

- sdk: `TestPartitionFromEnv`, `TestCallerPartition` (headers → fields;
  absent → ""), `TestRequirePartitionExits` (subprocess), `Mail`/`Inbox`/
  `Ack` request shapes and the older-xbind error, `GlobalURL`.
- web: `hack/ui-harness` pass asserting `xbin.partition` in a partitioned
  fixture (user and global viewers) and absent elsewhere; `bfetch` strips the
  `partition` option; `xbin.status()` works in a partition.
- contract suite (`sdk/sandboxcontract`): new `userPartitionChecks` (beside
  today's consumer `partitionChecks`) — two partitions of one consumer don't
  see each other's sandboxes; `""` and `global` are one consumer; a partition
  sees a global-home team record and a member record, not a private one of
  another person; a share to (consumer, partitionId) works; a mismatched
  `Sbx-User` from a partition is refused; a recreated user (new partition id)
  sees none of the old records; operator names redacted;
  `hello.caps.partitions`.
