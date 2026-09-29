# Partitioned tiles — implementation plan: overview

**Status:** plan, **revision 3** (2026-09-29). Nothing here is built.
- The owner has ruled on all ten owner calls, and every ruling is folded
  in; [90-decisions.md](90-decisions.md) marks each one **DECIDED**.
- Every finding of the two adversarial reviews (isolation-security S1–S20,
  compat-complexity C1–C23) stays mapped in 90 §G.
- Every `file:line` was checked by hand at master `45c7c637` (which
  includes the 0897bb0b sidebar change).
- Decision ids `PD-nn` live in 90, and work packs in
  [95-work-packs.md](95-work-packs.md).

## Revision 3: owner rulings

**The mode (PD-44).** The manifest's `partition` sets the mode **only while
the tile holds no data**. On a tile with data, a change is a **mode-switch
request**:
- the tile greys out with "all data in this tile will be deleted for the
  switch to happen";
- a tile manager either **switches**, which deletes every namespace, vault
  and registration and erases them from backups, or **keeps the current
  mode**, which is recorded, runs as before and deletes nothing;
- nothing runs while the request is pending;
- builtin updates and template merges never add or remove `partition`.

A small half-split teal marker (proposed) shows partitioned tiles on the
window head and sidebar row.

**User → global (PD-16)** is modelled as **bind types**:
- **Global binds** of a partitioned tile are admin-only, since admins
  could change the shared tile's code anyway.
- **Personal binds** are new. A person wires their own user-owned tile into
  their own partition only. Other people's partitions and global never see
  it, and xbind lets it through only when the caller's partition is the
  tile's owner.
- F5 (the same tile's global instance, attributed to the person) stays.

**The other rulings:**
- **Cross-tile partition edges** need the grant and the person's read
  access. **Per-person consent** is a workspace policy on a new admin
  **Policies** tab, off by default (PD-13).
- **Admins:** hygiene now, plus an off-by-default "credential resets wait
  for the person" policy. Sealing is future work (PD-07).
- **The agent template** is **partitioned by default** for new instances,
  with an opt-out at instantiation (PD-35).
- **Existing agent instances** have **no migration**: they stay
  unpartitioned, or a switch deletes their conversations, memory and
  schedules after an explicit confirmation (PD-34).
- **Non-secure chats** are hosted by the person's partition, plus
  **"share a copy"** (PD-32).
- **Backups:** **all backups are encrypted** under random subkeys wrapped
  by the vault. Deleting a subkey crypto-erases that data in every backup
  (a deleted user or partition, a switched tile); old plaintext archives
  still restore (PD-25, new file [11](11-backup-encryption.md)).
- **Code trust:** warn, plus the "reviewed code only" switch (PD-23).
- **Downgrade:** accept, plus the SDK's `xbin.RequirePartition()` (PD-06).

The rulings raised three questions, in 90 §H.

## 1. Goal

A tile may declare in its `xbin.json`:

```jsonc
"partition": ["user"]            // one backend per person, no global instance
"partition": ["user", "global"]  // … plus one background "global" instance (PD-02)
"partitionMail": "/mailbox"      // optional: where xbind rings the mail doorbell (04 §3)
"partitionNote": "…"             // optional: shown when a mode switch is requested (01 §2.4)
```

On a tile that holds no data, the declaration takes effect at once. On one
that holds data, it waits for a tile manager's decision (01 §2).

Once partitioned, xbind runs **one backend instance per partition** with:
- **shared:** one code tree, one build, one set of grants and global
  binds;
- **per partition:** data (resources), vault, cron/bus registrations,
  logs, run dir, terminal layer and personal binds.

xbind decides the partition a request reaches from the verified
credential, never from the URL or a header the caller controls:

- a person's frames, terminals, agent sessions and path tickets on the tile
  reach **that person's** partition;
- a partition's backend calling a partitioned provider reaches **the same
  person's** partition of the provider, if the person can read the provider
  (and, when the workspace policy says so, consented, PD-13);
- a partition's backend calling a non-partitioned provider, through a global
  bind or grant (admin-made, PD-16) or through **its person's own personal
  bind**, reaches that provider, which learns the caller's partition
  (`X-XBin-Partition`, `X-XBin-Partition-Id`);
- non-partitioned callers (other tiles, ingress, webhooks, the root token)
  reach only the **global** instance, and only if the tile declares one;
- the global instance and the user partitions talk only through **shared
  resources** and **partition mail**, plus the person-attributed F5 from a
  partition to its own global instance.

## 2. Terms

| Term | Meaning |
|---|---|
| **recorded mode (R)** | xbind state in `data/partitions/<TileKey>/mode.json`; routing obeys only this (01 §2.1) |
| **requested mode (Q)** | the `partition` of the primary's running code (a pinned primary's checkpoint) |
| **holds data** | any namespace content, vault key, registration, partition record, mail or personal bind of the tile, judged from xbind's own stores (01 §2.2) |
| **pending / declined** | Q ≠ R on a tile that holds data: **pending** until a tile manager decides (nothing runs); **declined** after "Keep the current mode" (R runs, nothing deleted) |
| **switch** | a tile manager's confirmation of a pending request. It deletes all the tile's data and erases its backups by key, then sets R := Q (01 §2.5) |
| **tile manager** | `mayManageTile` (`internal/broker/orgsapi.go:433-450`): a workspace admin, the tile's user owner, or an admin of its owning org. Distinct from **writers** (write level) |
| **partitioned tile** | a tile whose recorded mode has user partitions and that isn't pending or invalid |
| **partition key** (wire) | `user:<id>` or `global`. Carried in `X-XBin-Partition`, `XBIN_PARTITION`, `xbin.partition`, mail items (`from`), API JSON |
| **uid** | an immutable random id in the person's users-store record (`users.User.UID`); deleting the user deletes it (PD-43) |
| **pkey** (storage; also the wire **partition id**) | `u-<32 hex>` = SHA-256(`xbin-partition-v1` ‖ 0 ‖ user id ‖ 0 ‖ uid)[:16 bytes]; sent as `X-XBin-Partition-Id` |
| **global partition** | the optional extra instance, at today's keys (PD-04) |
| **user partition** | one per person who uses the tile; primary deployment only in v1 (PD-17) |
| **partitioned / shared resource** | per-partition namespaces by default; `"shared": true` or `"read"` in `scope.json` for one namespace at today's keys |
| **partition mail** | xbind-owned, durable, per-addressee inbox; global → one person, or a person's partition → global (04 §3) |
| **global bind** | today's binding record in the workspace `xbin.json`, seen by every instance; on a partitioned requester, admin-only (05 §3) |
| **personal bind** | xbind state per person (`data/partitions/binds/<uid>.json`): the owner of a user-owned tile wires it into their own partition of a partitioned tile; only that partition sees and reaches it (05 §3) |
| **workspace policies** | `data/workspace-policies.json`: `partitionConsent`, `credentialResetConfirm`, both off by default (PD-55) |
| **edge consent** | a person's recorded "tile Z may use my data in tile X"; required only with `partitionConsent` on |
| **backup subkey** | a random key per subject (`tile:`, `ns:`, `part:`), wrapped by the vault in `data/vault/.backup-keys/`. It seals that subject's archives, and deleting it crypto-erases them (11) |
| **interactive / background start** | a partition start caused by the person's own request, vs. one caused by cron, a bus delivery or mail |

## 3. Guarantees and non-goals

**G1 — Data separation between people using the tile (hygiene, enforced by
xbind).** No xbind route, event, env, mount, network path, registration or
backup hands a user partition's data to any principal other than that
partition's own. That data is:
- resources and vault values;
- the backend log and tile report;
- mail and bus traffic;
- registration payloads and personal binds;
- agent-session transcripts and the terminal layer.

The partition's own principals are:
- its backend generations;
- its person's frames, terminals, agent sessions and path tickets;
- its cron and bus deliveries, and mail doorbells addressed to it;
- calls from the same person's partitions of other partitioned tiles
  holding the grant, when the person can read the tile (and, if the policy
  requires it, consented).

A mode switch deletes the tile's data, so no data from before a switch
survives into the new mode (PD-44, PD-34).

**G2 — Admins have no easy read path (hygiene, PD-07 decided).** "Owner is
admin" (plans/auth.md §3) keeps every governance power: grants, global
binds, lifecycle, **mode switches** (as tile managers), stopping, resetting
and purging partitions, deleting users and killing sessions.

On a partitioned tile, admins lose these read paths:
- reading a user partition through the API (an admin reaches only their
  own partition);
- view-as into it;
- reattaching to, driving or reading another person's terminal or agent
  session;
- its bus and session events, and its logs (unless the person shares
  them);
- its vault values and key names, and its mail;
- any backup they can decrypt (archives are sealed; 11);
- addressing mail to a person;
- creating personal binds for others.

**Remaining paths**, each named in docs/partitions.md, each leaving a trace:
1. Changing the tile's code, or the code of a provider bound to it (PD-23);
   this shows in the deploy log and the trust panel.
2. Taking over the person's account (`internal/broker/usersapi.go:313-345`,
   `:216-224`). It is audited and the person is notified; with
   `credentialResetConfirm` on it also waits for the person or 24 h.
3. Approving a partitioned tile Z's grant on partitioned X. With the
   consent policy off (the default) this opens every reader's X data to Z's
   code (AR-19); with it on, each person's consent is needed too.
4. Host root and the vault passphrase with the keystore (non-goal).

**G3 — Fail closed.**
- A principal whose partition can't be determined reaches no user
  partition.
- An invalid `partition`, or a switch request on a tile that holds data,
  runs **no backend** (409), and nothing is deleted without a tile
  manager's typed confirmation (PD-44).
- A user partition never starts without `--isolate` (PD-19).
- A partition's deliveries stop while its person is deleted, disabled or
  lost read (PD-20).
- A user-partition instance token stops authenticating the moment its
  partition is no longer covered (02 §2).
- A personal bind grants only its owner's live partition.

**Non-goals (v1).**
- Cryptographic isolation per person ("sealed partitions", PD-07 future).
- Protection from people who can change the tile's code or a bound
  provider's code.
- Protection of anything a partition writes to a shared resource or mails
  to global.
- Partition kinds beyond `user` (orgs/groups): the array and the
  personal-bind record leave room.
- User partitions in non-primary deployments, partitioned `vm` tiles, and
  offloading a partitioned tile.
- An older xbind honouring the flag (PD-06: accepted; `RequirePartition()`
  fails closed).

## 4. End-to-end flows

### 4.0 A new tile, or a new agent instance

A template whose `template` block asks for partitions (the agent,
PD-35) is instantiated:
- the instance's manifest gets `"partition": ["user","global"]` unless the
  "Keep each person's data apart" box was unchecked or xbind lacks
  `--isolate` (01 §2.7);
- the instance holds no data, so the first rescan records the mode
  (`auto`), with no confirmation;
- people's partitions start on first use.

A hand-written tile that adds `partition` before it holds data is the same.

### 4.1 The mode switch (a tile that holds data)

1. A writer adds or changes `partition` in `apps/docs/xbin.json`, or a
   rollback lands on code that asks differently. The rescan sees Q ≠ R on a
   tile that holds data, so the request is **pending**. Every instance of
   the primary stops.
2. Everyone who opens `apps/docs` sees xbind's greyed page in the frame
   (01 §2.4), and the shell shows the `/alerts` banner. The page says: "A
   partition mode switch is requested for apps/docs (unpartitioned → user +
   global). All data in this tile will be deleted for the switch to
   happen", followed by the tile's `partitionNote` and who can decide. The
   API answers 409.
3. Tile managers get a push. On `/xbin/partitions` (or the new shell's card
   overlay, the admin tile, or `bx`) they pick one:
   - **Keep the current mode:** `declined` is recorded, the tile runs
     unpartitioned again, and nothing is deleted.
   - **Switch and delete all data…:** they type the tile path. xbind stops
     and revokes, wipes every namespace, vault file, registration and
     partition store (01 §2.6), erases the tile's `ns:` and `part:` backup
     subkeys (11 §3), records R := Q with a history entry, a deploy-log line
     and slog audit, and notifies everyone whose partition was deleted.
     People's partitions then start empty on first use.

### 4.2 Frontend (a person's frame)

1. Alice opens `/c/apps/agent/`. The D4 injection
   (`internal/server/static.go:526`) mints her frame token as today and adds
   `<meta name="xbin-partition" content="user:alice">`; the
   `xbin-interfaces` meta includes her personal binds.
2. `xbin.fetch('runs')` goes to `/api/apps/agent/runs`, then
   `proxy.ServeHTTP` (`internal/proxy/proxy.go:170`), then `px.decide`,
   then broker `Route` (`internal/broker/deploydata.go:227`), rule 2:
   partition `user:alice`.
3. `identify` (`proxy.go:416`) strips inbound `X-XBin-*` and sets From,
   User, `X-XBin-Partition` and `-Id`.
4. `Runner.EnsurePartition` spawns (or reuses) alice's instance with
   `XBIN_PARTITION=user:alice`, the non-primary network wiring, and her
   volumes at the canonical `XBIN_RES_*` paths.

### 4.3 Terminal and agent sessions

Alice's terminal on `apps/agent` (a terminal token,
`internal/auth/auth.go:263`) reaches `user:alice` for `bx`, `curl
$XBIN_URL/api/…`, `bx vault` and `bx logs`. The session starts in her
`$HOME`, and its layer is per (tile, person). Admins can list and kill it,
never reattach, drive or read it (PD-09). A root terminal reaches global or
nothing.

### 4.4 Backend → backend and bind types

Alice's agent partition calls:
- **`apps/docs`** (partitioned) → `apps/docs`'s `user:alice`. It needs the
  grant and her read on `apps/docs`, plus her consent when
  `partitionConsent` is on (05 §2). The call is counted in her ledger.
- **`apps/llm-gw`** (shared) → through an admin-made **global bind** on the
  agent's `llm` slot; llm-gw sees `X-XBin-Partition: user:alice`.
- **`users/alice/mcp`**, alice's own tile → through her **personal bind**
  (§4.8); only her partition lists and reaches it.
- **its own tile** (`/engine/hold`) → stays in `user:alice`.
- **its own global instance** (F5, `?xbin-partition=global`) → attributed
  to alice.

A non-partitioned tile calling `apps/docs` reaches its global instance, or
403 without one (PD-12).

### 4.5 Cron and bus

Alice's partition registers cron jobs in
`data/partitions/<TileKey>/<dep>/<pkey>/cron.json`. They fire as her
partition while she is live (PD-20), as background starts with retry.
Events on her partitioned bus reach only her partition's subscriptions and
her frames.

### 4.6 Ingress (public) and webhooks

`ForwardIngress` (`internal/proxy/ingress.go:39`) serves a partitioned tile
only from global (503 without one). The webhooks tile and the messaging
bridge reach the agent's global instance, which mails the person's
partition (09).

### 4.7 Global ↔ user partitions

- Shared resources: `"shared": true` or `"read"`.
- Mail: from the tile's global instance only, to `user:<id>`, which is
  stored, sealed and doorbelled. A partition mails only `global`, and
  xbind stamps `from`.
- F5: a partition's own principals reach its global instance, always
  attributed to their person.

### 4.8 A personal bind

1. Alice owns `users/alice/mcp` (a D88 personal tile that provides service
   `mcp`). In `/xbin/partitions` (or `bx bind --personal apps/agent
   mcp=users/alice/mcp`) she binds it into **her** partition of the
   partitioned `apps/agent`, with her own session (PersonOnly).
2. xbind validates the request:
   - alice owns the provider;
   - she can read `apps/agent`;
   - `mcp` is a multi http slot;
   - the provider provides the service;
   - the ceiling allows it.

   It then stores `data/partitions/binds/<alice's uid>.json` and restarts
   **only alice's** partition instance.
3. Alice's partition's `XBIN_IFACE_MCP` now also lists `{provider:
   "users/alice/mcp", url, service: "mcp", personal: true}`. Bob's
   partition and the global instance see no such row.
4. On every call, `grantedRoleIn` accepts the personal bind only when the
   caller acts in `user:alice` with her live uid and `users/alice/mcp` is
   still owned by `user:alice`. Anyone else gets 403 (05 §3).
5. The bind disappears if alice transfers the tile or is deleted, if the
   agent switches mode, or when she or an admin deletes it.

## 5. Scope of this plan

- **Fabric:**
  - manifest, registry and mode (01);
  - identity and routing (02);
  - runner, data, vault and registrations (03);
  - shared resources and mail (04);
  - edges, consent policy, bind types and global (05);
  - terminals, ops, surfaces and the marker (06);
  - SDK and contracts (07);
  - compat and security (10);
  - backup encryption (11).
- **Builtin tiles:** the agent template (08); coding-sandbox,
  sandbox-terminal, messaging bridge, webhooks and llm-gw (09).

New HTTP surface is listed per file under "Wire".

## 6. Shape of the change in one table

| Area | Today's key | Added |
|---|---|---|
| mode | — | `data/partitions/<TileKey>/mode.json`: recorded mode, request, declined, history (PD-44) |
| person key | user id | `users.User.UID` → pkey (PD-43) |
| runner state | `stateKey(tile, dep)` `internal/runner/deployments.go:38` | `(tile, dep, pkey)`, dep = the primary; global = today's |
| run dir | `sockDir(tile, dep)` `internal/runner/deploy.go:202` | `p-<16 hex SHA-256(tile‖0‖dep‖0‖pkey)>` |
| instance token | `instanceID{component, deployment}` `internal/auth/deployment.go:52` | `+ partition` (part of the token's identity) |
| principal | `auth.Principal.Deployment` `internal/auth/auth.go:54` | `+ Partition` |
| routing | `Route` `internal/broker/deploydata.go:227` | Decision `+ Partition`, `CallerPartition`; `grantedRoleIn` for personal binds |
| data namespace | `scopeKeys(scope, dep)` `deploydata.go:380` | `.partitions/<escS>/<dep>/<pkey>` for partitioned resources |
| vault | `vaultPathIn(comp, dep)` `internal/broker/deployvault.go:64` | `data/vault/.partitions/<TileKey>/<dep>/<pkey>.json` |
| cron/bus rows, identity record | `data/cron-jobs.json`, `dormant.go` dep files | `data/partitions/<TileKey>/<dep>/<pkey>/{partition,cron,bus-subscriptions,…}.json` |
| mail | — | `data/partitions/<TileKey>/<dep>/mail.db` |
| bindings | workspace `xbin.json` `bindings` (`internal/registry/registry.go:300`) | + personal binds `data/partitions/binds/<uid>.json` |
| policies | — | `data/workspace-policies.json` |
| consents, ledger | — | `data/partitions/consents/<uid>.json` (policy on), `…/<dep>/<pkey>/ledger.json` |
| backend log | `.xbin/log/<CompKey>.log` | `.xbin/partition/<TileKey>/<dep>/<pkey>/backend.log` |
| terminal layer | `termKey(rel)` `internal/term/term.go:493` | `.xbin/term-part/<TileKey>/<pkey>` |
| agent-session history | `data/agent-history/<user>/<tile>/` | `data/agent-history/.partitions/<pkey>/<TileKey>/` |
| backups | plaintext tar per tile + deployment (`internal/backup/backup.go:7-27`) | sealed archives; `.data.<TileKey>`, `.partitions.<TileKey>.<dep>.<pkey>`; subkeys `data/vault/.backup-keys/` (11) |
| API route classes | `routeClasses` `internal/server/deployclass.go:84` | second table `partitionClasses` + guard test |

## 7. Files of this plan

| File | Contents |
|---|---|
| [01-manifest-registry.md](01-manifest-registry.md) | keys (`partition`, `partitionMail`, `partitionNote`, `template.partition`); validation and structural rules; **the mode**: states, "holds data", pending, the grey-out, deciding, the wipe list, templates and updates; `shared` |
| [02-identity-routing.md](02-identity-routing.md) | `Principal.Partition`, `addressedPartition`, Route rules, F5 attribution, headers, the partition route-class table, events |
| [03-runner-data-vault.md](03-runner-data-vault.md) | runner, admission (incl. coverage for the agent default), data namespaces, volumes, vault, registrations, identity records, wipe hooks |
| [04-shared-resources.md](04-shared-resources.md) | shared resources (`true`/`"read"`), partition mail, patterns |
| [05-fabric-edges-global.md](05-fabric-edges-global.md) | the edge matrix, the consent policy, **bind types** (global/personal), providers, the global instance, F5, deployments × partitions |
| [06-terminals-ops.md](06-terminals-ops.md) | terminals and sessions, logs, the partitions API, `bx`, people's lifecycle and credential resets, the person page, **the marker designs**, the admin Policies tab |
| [07-sdk-contracts.md](07-sdk-contracts.md) | Go SDK, JS client, protocol rows, sandbox-manager and agent-inbox contracts |
| [08-agent-template.md](08-agent-template.md) | the agent: default partitioned, modes, split, shared and non-secure conversations (+ share a copy), channels/triggers via mail, interfaces, existing instances (no migration) |
| [09-sandboxes-bridge-webhooks.md](09-sandboxes-bridge-webhooks.md) | coding-sandbox, sandbox-terminal, messaging bridge, webhooks, llm-gw |
| [10-compat-security.md](10-compat-security.md) | compatibility (zero state, mode changes, downgrade, clients, wire), security invariants and the threat model |
| [11-backup-encryption.md](11-backup-encryption.md) | **new**: sealed archives for all backups, the key hierarchy, crypto-erase, restore rules, DR, the archiver contract |
| [90-decisions.md](90-decisions.md) | PD-nn (DECIDED/DEFAULT), accepted risks, contradictions, the review map, **open questions** (§H) |
| [95-work-packs.md](95-work-packs.md) | F1–F17b, B1–B3, I1–I2: files, dependencies, parallel waves, acceptance, sizes |
