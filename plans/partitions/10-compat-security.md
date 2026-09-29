# 10 — Compatibility and security

## A. Compatibility (docs/compat.md; "never break users")

### A.1 Workspaces with no partitioned tile — byte-identical

For a tile that never asks for `partition`, none of these change: no
manifest key, no mode record, no boot migration, no new file written, and
no env, header, token, event or route answer differs. Every new code path starts with `PartitionState()` =
`Unpartitioned` → today's function. Specifically: no uid is minted for anyone
until a partition is first used (the users store is unchanged); `?partition=`
keeps being ignored on unpartitioned tiles (C22); admins' reattach/drive/
event passes are unchanged on unpartitioned tiles; a user delete keeps
today's behaviour for people who never held a partition; `POST /bus/publish`
is untouched. Guards:
- existing: `TestZeroStateRoute`, the runner env goldens
  (`internal/term/deploy_test.go`, `internal/runner/*deploy*_test.go`),
  `TestDeploymentRouteClasses`, `hack/events-socket.test.mjs`;
- new: `TestNoPartitionGolden` — `/api/xbin/components`, `whoami`, a backend's
  env, `XBIN_IFACE_*`, `X-XBin-*` headers at the callee, bus event JSON,
  cron/bus stores, `?partition=` on vault/cron/logs, the users store after a
  delete/recreate, admin session routes, binding and grant approval by org
  admins and personal owners — for a fixture workspace without partitions,
  compared to master's.

**The one change every workspace sees is sealed backups** (PD-25, 11 §7).
It is independent of partitions and ships with its own migration note.

### A.2 Changing an existing tile's mode (PD-44, decided)

- **A tile that holds no data:** the manifest sets the mode directly
  (`auto`). There is nothing to lose.
- **A tile that holds data:** a change of `partition` is a **switch
  request**, never a silent change. The tile pauses (409, grey-out, banner)
  until a tile manager does one of two things:
  - **keeps the current mode** (nothing changes, nothing is deleted);
  - **switches**, typed and confirmed, which deletes all the tile's data
    and erases its backups by key.

  Promote and rollback to code that asks differently land in the same
  request.
- **Nothing a person didn't confirm ever deletes data.** Builtin updates and
  template merges never add or remove `partition` (PD-52), so no update
  triggers a prompt. Existing agent instances stay unpartitioned unless
  their managers choose otherwise (PD-34).
- This is a builder-visible behaviour with a loud confirmation, not a
  breaking change: no existing workspace's tile changes mode by itself.

### A.3 Downgrade (an older xbind on a workspace with partitioned tiles)

- The older binary ignores `partition`/`partitionMail` (rule 7), `shared` in
  `scope.json`, the mode record, the mail store, consents, ledgers and the
  users store's `uid`: it runs **one** instance per tile at today's keys —
  global's data plus the shared resources. People's partition data
  (`.partitions/…` under `data/resources-enc`, `.xbin/resenc`, `data/vault`),
  registration files, mail (`data/partitions/…`), logs, person layers
  (`.xbin/term-part/…`) and partition history are never read by it: **no
  person's partition data is exposed by a downgrade**, and no partition's
  cron job fires as the tile's (the PO-9 argument of tile deployments).
- If the older binary rewrites the users store it drops `uid`; on re-upgrade
  xbind re-adopts the uid from the partitions' records (03 §E), so nobody
  loses their partitions. If it deletes and recreates a user id, the new
  record is newer than the partitions' records, so the new person gets fresh
  partitions and the old ones are orphaned (C2, S17).
- The older binary also ignores `data/workspace-policies.json` and personal
  binds. Personal binds then grant nothing (fail closed): an older xbind has
  no partitions to reach them from.
- It can't read sealed archives (11 §7); plaintext ones restore as ever.
- What does fail open: **new writes** made while downgraded all go to the one
  instance (global's namespace). Levers (PD-06, decided: accept):
  1. tile code calls `xbin.RequirePartition()` → exits on an older xbind
     (no backend at all: fail closed for tiles that must never be shared);
  2. the agent template's legacy mode = today's behaviour (acceptable: it is
     what every agent does today);
  3. rejected: renaming the runtime (e.g. `"runtime": "go+partition"`) so
     older `HasBackend` (`internal/registry/registry.go:524`) sees no backend —
     it would change the manifest contract the owner specified and break
     tools that read `runtime`;
  4. deferred (C21): a `requires` manifest key — useless for binaries that
     already shipped.
- Docs (docs/partitions.md §Downgrades): "below vX a partitioned tile runs as
  one instance on global's data; tiles that must never do so call
  `xbin.RequirePartition()`".

### A.4 Older clients and scaffold (C23)

- Shell, admin tile, iOS app, `bx`: a partitioned tile is one tile (one
  card, one status). Opening it opens the viewer's partition; frame token
  format unchanged; `native.js` runs against the viewer's partition. `GET
  /api/xbin/partitions` answers 404 on older xbinds; new `bx partition`
  commands exit 6 there.
- The shell and admin tile are scaffold that an xbind upgrade doesn't update
  (docs/compat.md intro, rule 4), so nothing the plan relies on for safety
  lives only there. These are the guaranteed surfaces:
  - `bx` and `bx doctor`;
  - the `/alerts` banner that old shells already render
    (`workspace-template/shell/bx-shell.js:1735-1738`);
  - push notifications;
  - the in-frame switch page (01 §2.4);
  - the xbind-served `/xbin/partitions` page (06 §12).

  The admin Partitions section, the Policies tab, the shell marker, the card
  overlay and consent prompts arrive with `bx builtin update`. A ui-harness
  pass runs an old admin scaffold and shell against a partitioned tile and a
  pending one, and asserts that they render, ignore the new fields, and show
  the in-frame page and the banner.
- New JS options (`xbin.fetch(…, {partition})`) are stripped by the client
  itself when the document isn't a user partition's, so a tile using them on
  an older xbind sends nothing new.

### A.5 Older tile code and providers

- Old SDKs: `CallerInfo` lacks `Partition`/`PartitionID`; the headers are
  ignored; nothing fails (rule 8).
- **Providers keyed on `X-XBin-From` alone merge partitions** (NP-09-19).
  The shipped sandbox-manager contract gains `caps.partitions`; the agent's
  user partitions don't use an old manager (they degrade, 08 §6); third-party
  providers are documented (docs/partitions.md §Providers: key on the
  partition id, `""` ≡ `global`) and flagged by `bx doctor` when bound to a
  partitioned consumer (PD-14; a `provides.<slot>.partitions: "aware"` hint
  stays a follow-up).
- The SDK's `Subscribe` doc (`sdk/xbin.go:360-365`: "an idle backend is
  started for a delivery") gains the partition exception: deliveries from a
  shared or foreign bus reach a user partition only while it runs (03 §D).

### A.6 Wire additivity checklist (rule 2)

| Surface | Change | Additive? |
|---|---|---|
| `/api/xbin/components` | `partition {state,…}`, `partitionError` on requesting/holding rows | yes (absent otherwise) |
| identity headers | `X-XBin-Partition`, `X-XBin-Partition-Id`; F5 attribution | yes (absent for unpartitioned callers) |
| `POST /bus/publish` | unchanged | — |
| bus delivery body, `/ws/events` bus events | `partition` on partitioned namespaces | yes |
| `/api/xbin/partitions*` (incl. `mode` keep/switch, `limits`, `consents`, `binds`, `share-log`, `credential-confirm`, `mail`), `partitions` event, `/xbin/partitions` page, principal `xbin/mail` | new | yes |
| `/api/xbin/workspace-policies`, `policies` event | new; defaults keep today's behaviour | yes |
| a pending tile's documents and API | switch page / 409 body; `/alerts` kind `partition-switch` | new tiles' state only |
| `XBIN_IFACE_<SLOT>` multi entries, `xbin-interfaces` | `personal: true` rows for the person's own partition | yes (partitioned tiles only) |
| binding/grant approval on a partitioned requester | global binds admin-only (PD-54) | new tiles only |
| `POST /templates/new` | `partition: false`; `template.partition` default | yes |
| `bx builtin update` | keeps the installed `partition` value | yes (never changes a mode) |
| held credentials (`credentialResetConfirm`) | off by default | yes |
| archives | sealed format, `schema: 3`, archiver `X-XBin-Backup-Subkey` / `POST /archive/erase` | **migration note** (11 §7); old archives restore unchanged |
| `?partition=` on vault/cron/bus/logs | 400 **on partitioned tiles only**; ignored elsewhere as today (C22) | yes |
| `?xbin-partition=global` | consumed for partitioned targets only | yes |
| terminal env `XBIN_PARTITION`, backend env | new var for partitioned tiles | yes |
| `xbin.json` `partition`, `partitionMail`; `scope.json` `shared` | new keys; unknown `partition` words fail closed by design (compat.md rule 7 note) | yes |
| admin reattach / drive / session events / `?user=` names | narrowed on partitioned tiles only | new tiles only |
| logs, vault, backends rows for admins on partitioned tiles | narrowed / metadata | new tiles only |
| `POST /deployments/primary`, offload | 409 on partitioned tiles only | new tiles only |
| user delete | homes/history moved to the orphan area for people who held partitions only | new state only |

Partitions themselves need no **BREAKING** changelog entry. Sealed backups
get a migration note (`docs/changes/<date>-sealed-backups.md`): disaster
recovery onto a new machine now needs the exported key bundle, and older
xbinds can't restore new archives. The changelog gets one entry
per shipped pack (docs/changelog.md), docs/partitions.md is new, and
docs/protocol.md, resources.md, auth.md, elements.md, sdk.md, bx.md,
sandbox-manager.md, agent-inbox.md, compat.md (a "Partitioned tiles" section
like "Tile deployments", and the rule-7 note) are updated in the same
changes.

### A.7 Boot migrations (rule 9)

None. New state is created lazily (first mode act, first partition start,
first registration, first mail, first uid). `partition.json` rebuild from
`ns.json` at boot only runs when a partition namespace exists without its
record (new state only). The aged-workspace fixture test's allowed list is
unchanged.

## B. Security

### B.1 Invariants kept (plans/auth.md)

- **xbind strips inbound `X-XBin-*`** everywhere it does today
  (`internal/proxy/proxy.go:416`, `internal/proxy/ingress.go:53-62`); the new
  headers are covered by the same prefix strip; the partition is never read
  from a header, query (except F5's *target* request, which widens nothing
  and is attributed to the person), env or manifest — only from xbind state
  and HMAC'd credentials.
- **Default-deny for element principals:** a partition is not a principal
  and gets no authority of its own (grants stay per tile); `/api/xbin/*` is
  default-deny for user-partition credentials (the class table, 02 §8);
  cross-tile partition edges additionally need the person's consent (PD-13).
- **Owner is admin:** admins keep every governance act (grants, bindings,
  lifecycle, users, partition mode, stop/reset/purge, killing sessions); they
  lose read paths into people's partitions (PD-07). The root token reaches
  only `global`. Admins' direct resource API (`allowRes` admin pass,
  `internal/broker/broker.go:533-535`) reaches global's namespace and shared
  resources only (a person's calls resolve to `scopePrimary`,
  `resources.go:648-656`).
- **D78:** nothing new executes as xbind on sandbox-writable data: partition
  namespaces reuse the deployments' mount/wipe/backup code paths (nofollow
  walkers, confined tools); person layers, moved homes and history are removed
  with the confined remover; no new `exec.Command` (`TestNoDirectExec`
  unchanged; resenc's `-scryptn` is an argument of the existing gocryptfs run).
- **D118:** the mode, uids, consents, personal binds, policies, ledgers,
  mail and backup subkeys live in xbind-owned files under `data/`. The
  `partition` flag in `xbin.json`, which the tile's terminals can write,
  sets the mode **only on a tile that holds no data**, where there is
  nothing to expose or lose. On any other tile it is only a request: an
  edit can make the tile `pending` or `invalid` (409, nothing served,
  nothing deleted). It never changes who reaches whose data, since pkeys
  come from the credential's person and the uid. Only a tile manager's
  typed confirmation deletes data. Shared-resource flags in `scope.json`
  can make global's copy shared, never a person's.

### B.2 Threat model

| Actor | Path tried | Outcome |
|---|---|---|
| bob (reader) → alice's data | `/api/<tile>/…` | routed to bob's partition (credential-derived) |
| bob | forge `X-XBin-Partition`/`-Id`/`X-XBin-User` | stripped |
| bob | alice's frame token / path ticket | HMAC-bound to alice's session generation; not bob's to hold |
| bob | `/ws/events` bus/session/status | partitioned events filtered by partition; admins lose the blanket pass |
| bob (terminal level) | `/tmp` leftovers of alice's shell | per-person layer (PD-22); sessions start in `$HOME`; the tile dir is documented shared code |
| bob (write level on the tile, an ancestor, or a bound provider) | change code to exfiltrate | **residual** (PD-23): trust panel, warnings, "reviewed code only" |
| admin | API, view-as, reattach, **agent-session drive/events**, logs, vault (values and names), bus, mail, backups | own partition / refused / refused / refused (kill allowed) / global only unless shared / global only / filtered / refused / ciphertext |
| admin | **mint a reset link or rebind SSO for alice** | **residual** (PD-07, decided): audited, alice notified (push, notice, ledger); held until confirmed or 24 h when the policy is on |
| admin or root token | forge mail to alice's partition | 403: only the tile's global instance may mail a person; `from` stamped by xbind |
| admin | approve Z `uses` X (both partitioned), mail everyone to wake Z | warning at approval; mail never starts a never-run partition; with the consent policy on, **no data flows without each person's consent** |
| admin | restore alice's partition archive into bob | refused (same user id only; `--to` only for a recreated same id, audited, notified); sealed under alice's partition subkey |
| another tile (non-partitioned) | call the partitioned tile | global only |
| bob's partition of another tile Z | call the partitioned tile X | bob's partition of X, if bob can read X (and consented, when the policy is on); counted in bob's ledger |
| a user partition's code (incl. prompt injection) | F5 to global as "the tile itself" | attributed to its person: person's level, clamped role |
| a user partition's code | send the bot's replies anywhere | global resolves destinations from its own handoff record and checks `from` |
| a user partition's code | rewrite shared routing rows (links, triggers, hosts) | routing tables in global's `db` or `"read"` resources; the hosted engine trusts only its own `hosted` table |
| a user partition's code | a private trigger with an empty match | refused (non-empty, non-overlapping); registry visible to managers |
| members of a hosted conversation | widen its audience after the host agreed | paused until the host re-confirms; share links refused |
| the global instance's code | reach a person's partition | no route; mail only; never reads an inbox |
| alice's compromised partition backend | other partitions' mounts, loopback, abstract sockets | not bound; never host network or splice (non-primary network rule); isolate required |
| alice's partition | spam other people | notify clamped to alice; mail only to global |
| a code writer | flip `partition` in `xbin.json` / roll back to code that asks differently | `pending` (409, grey-out); nothing served or deleted until a tile manager keeps or switches |
| a code writer | switch the mode to wipe people's data | can't: deciding needs `mayManageTile` with a person credential; the tile's own principals are refused |
| a tile manager | switch mode to wipe everyone's data | governance (like offload/removal): typed confirmation, history, deploy log, audit, every affected person notified |
| a builtin update or template merge | add/remove `partition` and trigger a wipe prompt | the installed value is kept; the default lives in the stripped template block |
| an org admin or personal owner | bind a shared provider into a partitioned tile (widening everyone's trust base) | 403: global binds on partitioned requesters are admin-only; the same for `uses` approvals |
| bob's partition (or global) of tile Z | call alice's personal tile bound personally into her partition of Z | 403: a personal bind grants only alice's partition (owner and uid checked per call); bob's env never lists it |
| an admin | approve Z `uses` X (both partitioned) with the consent policy off | **data flows** for people who can read X (AR-19); approval warning, ledger, the policy switch |
| an archiver or its operator | read backups | ciphertext; subkey ids and sizes only |
| anyone with an old backup | read a deleted person's or a wiped tile's data | subkey erased → unreadable (pre-sealing plaintext archives excepted, AR-20) |
| an admin | mint a reset link with `credentialResetConfirm` on | held until the person allows it or 24 h after the notice |
| a live user-partition token after the mode changes | read main's data as partition "" | 401: the registered partition is part of the token; revoked before the change is published |
| a lost/corrupted index or users file | everyone gets fresh partitions, old ones swept | uid re-adopted from records; orphaned only on recorded deletes |
| deleted `alice`, recreated as a new person `alice` | old data, sandboxes, consents, homes, history | new uid → new pkeys and partition ids; providers key on the id; consents keyed by uid; homes/history moved to the orphan area |
| a person who lost read | cron/bus/mail keeps running as them | dormant (liveness gate) |
| a provider keyed on From | partitions merge there | consumer-side degrade for sandbox managers; partition id header; docs and doctor for others |
| a crash in alice's partition | log path and headline broadcast | partition-scoped events; `crashLoopError` names the partition |

### B.3 Honest statement for docs/partitions.md and docs/auth.md

"Partitions keep the people who *use* a tile apart, and keep workspace
admins from reading people's partitions through xbind. They don't protect
against whoever can change the tile's code or the code of a provider it uses
(write level on the tile, an ancestor, or the provider — the code runs in
every partition), the host and the vault unseal key, or what a partition
itself writes to a shared resource or mails to the global instance. An admin
can still take over a person's account by resetting its credentials; that is
recorded and the person is told, and a workspace policy can make such resets
wait for the person. Switching a tile's partition mode deletes all its data,
and erases it from backups, after a manager confirms. Backups are encrypted,
and restoring one on another machine needs the exported backup keys."

## Tests

- `TestNoPartitionGolden` (A.1); the aged-workspace boot test unchanged.
- `test/downgrade_test.go` extension: a workspace with a partitioned fixture
  tile, used by two people, then started by the previous release's xbind
  (`.dev.mk` provides it): partition data absent from the one instance's
  view, no partition cron fires, `RequirePartition` fixture exits; back on
  the new xbind, after the old one rewrote the users store, everyone's
  partitions are still theirs (uid re-adopted).
- Mode rule (`test/partitions_test.go`):
  - an unpartitioned fixture with data plus an added `partition` → pending,
    with the old shell's banner and the in-frame page;
  - keep → runs as before, and a byte-compare of its data is unchanged;
  - switch → every store empty, subkeys erased, people notified;
  - the same with a rollback to pre-partition code;
  - a `bx builtin update` and a template merge that add `partition`
    upstream leave the instance unchanged.
- Security regression suite (`test/partitions_security_test.go`): every
  row of B.2 that has an xbind mitigation.
