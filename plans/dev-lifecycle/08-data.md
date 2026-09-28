# 08 — Data: namespaces, seeding, secrets, backup

> Status: implemented (D119, D127; M3 designed, not built) — how every tile store is keyed per deployment, which data a request reaches, which resources each deployment's namespace has, and how deployment data is seeded, reset, deleted, vaulted, quota'd and backed up (part of [plans/dev-lifecycle](README.md))

This document is the storage half of [05-model.md](05-model.md) (§3, §6, §9,
§11, §12). It implements:
- **P6**: storage follows the deployment, and `main` owns today's keys forever;
- **P14**: non-primary data and vault start empty. Seeding is optional and
  empty is the default; seeding and vault copy are tile-manager acts;
- **P22**: each deployment provisions what its own code declares, in its own
  namespace, with per-deployment limits that default to the tile's;
- **P28**: a (scope, name) namespace is shared by the scope's same-named
  deployments;
- the storage side of **P13** (per-deployment registrations; non-primary status only
  in the `deployments` event), of **P18** (a pinned or non-primary backend
  reaches its files only through sandbox binds) and of **P26** (the classes of
  the data routes, §4.4).

Terms are [01-glossary.md](01-glossary.md)'s. Unless another file is named,
facts about today come from [research/data-plane.md](research/data-plane.md)
and were re-verified against this worktree. Wire shapes named here are
proposals; `11-contract.md` owns the final ones.

## 0. Decisions at a glance

1. **`main` keeps today's storage.** Its paths, buckets and labels stay
   byte-for-byte, whether or not `main` is primary. Nothing is ever migrated.
   A tile without a deployment record backs up, restores and offloads exactly
   as today. The only zero-state changes are two security closures (§14.3).
2. **Every other deployment gets its own data namespace.** Deployment `d` of a
   tile in scope `S` uses the data namespace `(S, d)`. It is a `.deployments`
   directory level inside today's roots, keyed by an injective encoding of the
   scope path. No existing key can produce it, and no older xbind computes it.
3. **Resource declarations are per deployment (P22).** `(S, d)` has the
   resources that `d`'s own code declares in `S`'s `scope.json`. The primary
   gains a resource only when code declaring it is deployed to it (§6.7).
4. **`XBIN_RES_*` is identical in every deployment** for every resource both
   declare. The broker binds the deployment's volumes at `main`'s paths
   (`Src ≠ Dst`). For everything reached over HTTP it maps (caller's
   deployment, canonical id) to a namespace.
5. **Workspace-level resources are never split.** `res:workspace/*` is always
   reached by an assigned grant, so it is an edge, and non-primary deployments
   reach it under the edge policy.
6. **Seeding replaces the target.** A seed is optional, explicit, audited and
   a tile-manager act in a human session. It replaces the target namespace
   with a consistent copy of the primary's, re-keyed under the target's
   labels. It runs online by default, and stops the primary when no
   consistent online copy exists.
7. **The vault splits per deployment.** D30's backend-only read rule holds per
   deployment. Placeholders are computed, never stored.
8. **Backups keep today's archive and always cover the primary.** For a tile
   with a record, a component backup keeps today's archive and adds the
   deployment record and the checkpoint store. When the primary isn't `main`,
   the backup also archives the primary's data under its own key. Other
   non-primary data is opt-in. Every deployment archive is one that older
   xbinds refuse. Per-deployment backup and restore use new endpoints;
   `POST /restore` is unchanged.
9. **Offload frees non-`main` namespaces.** Their encrypted file-resource bytes
   are removed after a confirmed archive, but only for resources the archive
   holds without loss. `main`'s offload stays today's; fixing its leak is a
   separate change (§9.4).
10. **Non-primary usage has its own quota.** It is quota'd per namespace,
    never pooled with the primary's, limited per deployment (P22), and
    write-blocked first when the disk runs low.

## 1. Terms this document adds

These refine glossary terms. They are not product vocabulary.

| Term | Meaning |
|---|---|
| **data namespace `(S, d)`** | The storage of scope `S`'s resources for deployment name `d`. `(S, main)` is today's storage. The deployment data of deployment `d` of tile `T` is `(scope(T), d)` plus `T`'s vault for `d`. |
| **declared set `D(S, d)`** | The resources (name → type) that `(S, d)` has: what the code that owns `S`'s declarations for `d` says (§6.7). |
| **canonical resource path** | The host path today's `EnvFor` hands a same-scope backend for a `filesystem` or `sqlite` resource: `main`'s mount `.xbin/resenc/<ScopeKey(S)>/<name>` (plus `/<name>.sqlite`). The phrase "the primary's resource paths" in 05-model §12 means these paths. They don't move when the primary is reassigned. |
| **addressed deployment** | The deployment whose data namespace and vault a data-plane request reaches (§4). |
| **scope root tile** | The tile whose own directory holds `S`'s `scope.json`. A scope whose `scope.json` lies in no tile's own directory is a **plain-directory scope**. |
| **scope primary name `P(S)`** | The name of the scope root tile's primary. It is `main` for a plain-directory scope, for the workspace scope, and while the root tile has no deployment record. §6.5 keeps every member of `S` serving `(S, P(S))`. "The primary's data" (for seeding, backups of record and the read clamp) is `(S, P(S))`. |
| **namespace hold** | The state of a data namespace while it is seeded, reset, restored or deleted. Data-plane requests to it get 503 with `Retry-After`, and backends that address it don't start. |
| **`<TK>`** | 05-model §3's `<TileKey>`, a 128-bit hash of the tile path. Every new tile-keyed file in this document uses it. This document relies only on its spelling containing no `.` or `/`, which hex satisfies; `11-contract.md` §10 fixes the spelling. |

## 2. Every store

"Today" is `main`'s key, which never changes. `<escS>` is the injective scope
encoding (§3.3). `<d>` is the deployment name. `<CK>` is `util.CompKey(tile)`
(`internal/util/util.go:137-144`); `<TK>` is defined in §1.

| Store | Today (`main`, unchanged) | Non-`main` | Computed by | Notes |
|---|---|---|---|---|
| kv buckets | bucket `res:<scope>/<name>` (`workspace` for scope `""`) in the shared `data/kv.db` | the same bucket name in the namespace's own file `data/resources-enc/.deployments/<escS>/<d>/kv.db` | today: `resTarget.String()` (`internal/broker/broker.go:329-335`) via `kvAccess` (`internal/broker/resources.go:173-202`), plus three hand-built strings (`internal/broker/backup.go:146`, `:332`, `:482`); new: `resKeys` (§3.2) | One bbolt file per namespace: disk is freed on reset (bbolt files never shrink), namespace writes never take `kv.db`'s single writer lock, and older binaries never see it. No code enumerates `kv.db`'s top-level buckets; every use is a named lookup (`resources.go:212`, `:228`, `:268`, `:287`; `internal/broker/backup.go:179`, `:330`, `:479`; `internal/broker/resusage.go:151`). |
| kv encryption label | `kv:<bucket>`, i.e. HKDF info `xbin/kv:res:<scope>/<name>` (`internal/broker/resenc_wire.go:202-229`, `internal/vault/barrier.go:277-290`) | `kv:.deployments/<escS>/<d>/<bucket>` | `encodeKV`/`decodeKV` take the label from `resKeys` instead of building it from the bucket | A `main` value copied byte-for-byte into a non-`main` namespace fails to decrypt, so misplaced data fails closed. |
| file-backed ciphertext (`filesystem`, `sqlite`, `blob`) | `data/resources-enc/<ScopeKey(S)>/<name>/` (`internal/resenc/resenc.go:84-86`) | `data/resources-enc/.deployments/<escS>/<d>/fs/<name>/` | `resenc.Manager.CipherDir(dirKey, name)`, unchanged; `dirKey` comes from `resKeys` | `Ensure(resID, scopeKey, name, …)` already takes the label apart from the location (`resenc.go:132`). The manager needs no change. |
| decrypted mount | `.xbin/resenc/<ScopeKey(S)>/<name>/` (`resenc.go:89-91`) | `.xbin/resenc/.deployments/<escS>/<d>/fs/<name>/` | `resenc.Manager.MountDir`, unchanged | Must stay under `.xbin/resenc/**`, the only FUSE mount point D110's AppArmor rule allows (`deploy/install.sh:841-848`, `:909`). |
| fs password label | `fs:<ScopeKey(S)>/<name>` (`resLabel`, `resenc_wire.go:43`; `password`, `resenc.go:108-118`) | `fs:.deployments/<escS>/<d>/fs/<name>` | `resKeys` | Re-keying on seed follows (§8.3). |
| plaintext resource dir | `data/resources/<ScopeKey(S)>`, created only for `cron` resources and chowned under tier 2 (`resources.go:36-58`) | none | — | No plaintext file resource exists anywhere today (`resources.go:46-49`). |
| namespace metadata (new) | none | `data/resources-enc/.deployments/<escS>/<d>/ns.json` | the deployments service | State (`empty`, `seeding`, `seeded`, `partial`, `restored`, `orphaned`), provenance, skipped resources and history (§8). `11-contract.md` §1.1 reports a summary of it as `Deployment.data`. `main` gets a metadata-only `…/<escS>/main/ns.json` if it is ever seeded or reset while not primary; its data stays at today's keys. |
| bus topics | in-memory topic `res:<scope>/<name>/<topic>` (`resources.go:410-437`) | the same topic; the hub `bus` event carries an additive `deployment: "<d>"` | `apiBusPublish` from the addressed deployment | Delivery matches by namespace (§4.3). The per-id counter (`resusage.go:182-185`) is keyed `id` for `main` and `id + "\x00" + d` otherwise. |
| vault | `data/vault/<CK>.json`, the whole map sealed with the DEK and no label (`internal/broker/vault.go:45-47`, `:79-111`) | `data/vault/.deployments/<TK>/<d>.json`, the same envelope | `vaultPath(comp, dep)` | A byte copy decrypts as-is. Copying is the vault-copy act (§10). Placeholders are computed (§10). |
| backend log | `.xbin/log/<CK>.log` (`internal/runner/runner.go:470-473`; `internal/obs/logs.go:69`), documented in `docs/protocol.md:2401` and `docs/elements.md:529` | `.xbin/deploy/<TK>/d/<d>/backend.log` (`11-contract.md` §10.4, `07-runtime.md` §9) | the runner's log open; `obs` `/logs` | `GET /logs` without `deployment` returns the primary's log (`11-contract.md` §0.4, §8). A non-primary log has a narrower audience (§4.4). `bx logs` without a deployment keeps reading `main`'s file, as today, and goes through `GET /logs` only where that file can't answer (`16-open-questions.md` Q23). |
| status | in-memory `statuses[<tile>]`, cleared on that tile's `build-start` (`internal/obs/status.go:129-144`) | the bare `<tile>` entry holds the **primary**'s status, whichever deployment it is; a non-primary status is held by the deployments plane per (tile, name), never in `obs`'s `statuses` map | `obs` status plane (the primary); the deployments plane (the others) | Status follows the role (05-model §8, §9; 12-compat rule C2). A non-primary status never appears in `GET /tile-report`, a `status` event or an old shell's toast. It rides only the `deployments` event (op `status`, `11-contract.md` §3.3), to 05-model §8's audience. Reassignment exchanges the two entries atomically. |
| prefs | `data/prefs/<CK(user)>/<CK(comp)>.json` (`internal/obs/prefs.go:33-48`) | `data/prefs/<CK(user)>/.deployments/<TK>/<d>.json` | `prefsPath(p)`, from the principal's deployment | Prefs don't follow a reassignment of the primary. |
| agent history | `data/agent-history/<user>/<CK(tile)>/<id>.json` (`internal/term/history.go:27`, `:49-55`) | the same path; each entry records its session's target in an additive `deployment` field | `saveHistory` | Stays per tile: it is a conversation about the one work tree (05-model §9). Resume restores the target. |
| cron jobs | `data/cron-jobs.json`, key `component\x00name` (`internal/broker/cron.go:76-78`, `:108`) | `data/deployments/<TK>/<d>/cron.json` (`11-contract.md` §10.2) | the self-scoped cron API | Never rows in today's file: an older xbind would fire them as `main`'s (05-model §3). Activation is `09-fabric.md`'s. |
| bus push subscriptions | `data/bus-subscriptions.json`, key `component\x00name` (`internal/broker/bussubs.go:158-162`), at most 64 per component (`bussubs.go:50`) | `data/deployments/<TK>/<d>/bus-subscriptions.json` | the self-scoped subscription API | As above. |
| interface instances | root `xbin.json` `ifaceInstances[<tile>]`, replaced wholesale (`internal/registry/registry.go:223`; `internal/broker/netfn.go:1110`) | `data/deployments/<TK>/<d>/iface-instances.json` | `apiIfaceInstancesSet` (`netfn.go:1037`) | Never active for non-primary deployments (05-model §7). |
| ingress hosts | root `xbin.json` `ingressHosts[<tile>]` (`registry.go:228`; `internal/broker/ingressfn.go:573`) | `data/deployments/<TK>/<d>/ingress-hosts.json` | `apiIngressHosts` (`ingressfn.go:491`) | As above. |
| backup schedule | `data/backup-schedule.json`, keyed by component (`internal/broker/backup_cron.go:19-27`, `:57-70`) | `data/deployments/<TK>/<d>/backup-schedule.json` | the per-deployment schedule endpoint (§11.1) | An older xbind would read a row with a `deployment` field as `main`'s schedule and prune `main`'s archive to its retention. |
| tile-managed sandboxes (D113, once built) | definitions in `data/sandboxes.json`; state in `.xbin/sbx/<CK>/<name>/` ([../tile-sandboxes.md](../tile-sandboxes.md) §1) | definitions in `data/deployments/<TK>/<d>/sandboxes.json`; state in `.xbin/deploy/<TK>/d/<d>/sbx/<name>/` (`11-contract.md` §10.4) | D113's API, keyed by the calling backend's deployment | Resource mounts resolve in the deployment's namespace (§5). |
| archive key | `CompKey(tile)` (`internal/broker/backup.go:45`) | `.deployments.<TK>.<d>` | `backupKey(comp, dep)` | §11.3. |
| tier-2 uid | `.xbin/uids.json[scope]` (`internal/broker/uids.go:36-69`), used only on the non-isolated spawn path (`runner.go:453-469`) | none: never consulted | — | §13. |
| disk quota key | `ScopeKey(S)`, measuring `data/resources/<key>` and `data/resources-enc/<key>` (`broker.go:185-198`); kv is not counted | `.deployments/<escS>/<d>`, measuring the namespace root (kv included) | `scopeDiskUsage` and a namespace walker | §12. |

**Runtime artifacts** are owned by `07-runtime.md`: build output, run dir,
cgroup leaf, env layer and D112 registry entries.

**Unchanged and per tile (not per deployment):** the deployment record, the
checkpoint store and its view repository (05-model §3), PRs
(`data/prs/<CK>`), the terminal layer (`.xbin/term/<CK>`), owner and access
rows, lifecycle state, grants and bindings.

## 3. The namespace scheme

### 3.1 Layout

```
data/
  kv.db                                    main: every scope's kv buckets (unchanged)
  resources-enc/
    apps~crm/<name>/                       main: ciphertext (unchanged)
    .deployments/                          never produced by ScopeKey
      apps~crm/                            escS("apps/crm")
        dev/
          ns.json                          namespace metadata
          kv.db                            this namespace's kv buckets
          fs/<name>/                       one gocryptfs volume per file-backed resource
        main/ns.json                       metadata only, if main was seeded/reset while not primary
  vault/<CK>.json                          main (unchanged)
  vault/.deployments/<TK>/dev.json
  prefs/<CK(user)>/.deployments/<TK>/dev.json
  deployments/<TK>.json                    the deployment record (05-model §3)
  deployments/<TK>/dev/                    cron, bus-subscriptions, iface-instances,
                                           ingress-hosts, sandboxes, backup-schedule (.json)
.xbin/
  resenc/apps~crm/<name>/                  main mounts (unchanged)
  resenc/.deployments/apps~crm/dev/fs/<name>/   non-main mounts (inside D110's rule)
  deploy/<TK>/d/dev/backend.log            non-main backend log (11-contract §10.4)
  deploy/<TK>/d/dev/sbx/<sandbox>/         D113 state, once built
```

The kv file, `ns.json` and the resource volumes live in separate places under
the namespace root, so no resource name can collide with them. That matters
because resource names are arbitrary `scope.json` keys and may start with `.`.

### 3.2 The key function

One function computes every physical key. It goes in a new file, because
`broker.go` is at 743 of the 800-line cap for unlisted files
(`hack/size-budget.txt`).

```go
// internal/broker/deploydata.go
type resKeys struct {
	NS      string // "" for main | ".deployments/<escS>/<d>" (also the disk-quota key)
	DirKey  string // resenc's scopeKey argument: ScopeKey(S) | NS+"/fs"
	Name    string // the resource name, validated
	FSLabel string // resenc resID: resLabel(ScopeKey(S), name) | NS+"/fs/"+name
	KVFile  string // "" = data/kv.db | "data/resources-enc/"+NS+"/kv.db"
	Bucket  string // rt.String() in both cases
	KVLabel string // "kv:"+Bucket | "kv:"+NS+"/"+Bucket
}

func (b *Broker) resKeys(rt resTarget, dep string) (resKeys, error)
```

Rules:
- **`main` is unchanged.** `dep` `""` or `"main"` must return today's values
  byte-for-byte. A table test pins them for the workspace scope, for `apps/cal`
  and `apps~cal`, and for names containing `/`.
- **The workspace scope isn't split.** `rt.Scope == ""` with a non-`main` `dep`
  is an error. The data plane never asks, because workspace-level resources are
  an edge (§4.2).
- **The deployment name is re-checked.** `dep` must match the deployment-name
  grammar (glossary). This is defense in depth.
- **Resource names are validated in every namespace, strictly outside
  `main`.** Today nothing validates `scope.json` resource names. A name like
  `../../../x` reaches `MountEncrypted` → `Ensure`, whose `filepath.Join`
  resolves it outside `data/resources-enc/`. xbind then creates directories
  there and runs `gocryptfs -init` on a path that tile-writable data chose
  (`resenc_wire.go:139-144`, `resenc.go:156-176`).
  - For `(S, main)` the function refuses only a name with a `..` segment or a
    NUL. That closes the traversal; every other name that works today keeps
    working (a zero-state security closure, §14.3).
  - For every other namespace a name must also be non-empty, with no leading
    `/` and no `.` or empty segment. Such names alias today (`./x` and `x` join
    to one directory), and a new namespace needn't inherit that.
  - A checkpoint's `scope.json` is held to the same rules (§6.7).
  - A refused name is not provisioned, and its error surfaces on the tile:
    `resource name "<n>" in <scope>/scope.json is not a plain relative path`.
- **The encoded scope has a length limit.** `escS` longer than 200 bytes is an
  error: that tile cannot have non-primary deployments, and the add operation
  says so.

**Every site that builds a physical key goes through the function:**
- the 11 `util.ScopeKey` call sites:
  - `broker.go:188`, `:730`;
  - `resources.go:38`, `:318`;
  - `resenc_wire.go:95`, `:119`, `:140`;
  - `internal/broker/backup.go:48`, `:312`;
  - `internal/broker/diskmon.go:243`;
  - `resusage.go:119`;
- the 3 hand-built bucket strings (`internal/broker/backup.go:146`, `:332`,
  `:482`);
- the direct `b.kv.db` uses listed in §2, which take the namespace's `*bolt.DB`;
- the re-derived `data/resources-enc` root (`broker.go:190`);
- the labels inside `encodeKV`/`decodeKV` (`resenc_wire.go:204`, `:223`).

A guard test in the style of `TestNoDirectExec` must refuse new
`util.ScopeKey(` or `"res:" +` uses in `internal/broker` outside
`deploydata.go`.

### 3.3 Why no key can collide

- **`util.ScopeKey` is not injective, so non-`main` keys never use it.**
  - It maps `/` to `~` (`internal/util/util.go:127-132`), and `ComponentPathOK`
    allows `~` (`util.go:97-114`). So scope `apps~cal` and scope `apps/cal`
    share a key.
  - For `main` this is exploitable today; see NP-08-13.
  - Non-`main` keys use `escS` instead: replace `%` with `%25`, then `~` with
    `%7E`, then `/` with `~`.
  - The encoding is injective. After it, `%` appears only as `%25` or `%7E`, and
    `~` only for `/`.
  - It is readable: `apps/crm` becomes `apps~crm`, the same as `ScopeKey` in the
    common case, and `apps~crm` becomes `apps%7Ecrm`.
  - It is one path segment, never empty, and never starts with `.`.
- **Tile-keyed files use `<TK>`, not `CompKey`.** 05-model §3 keys the new
  stores by `<TileKey>` because `CompKey` keeps only 32 bits of hash and can be
  ground (`util.go:137-144`). The same reasoning covers every new tile-keyed
  file here (non-`main` vaults, prefs, registrations, logs, D113 state,
  archive keys), so a ground `CompKey` collision (NP-08-13) can never make two
  tiles share one of them.
- **The dot level cannot collide with a scope key.** No scope key starts with
  `.`: the registry skips dot directories
  (`internal/registry/registry.go:439-441`), and `ComponentPathOK` rejects dot
  segments (`util.go:109`). So `.deployments` directly under
  `data/resources-enc/` or `.xbin/resenc/` cannot be a scope key.
- **The same holds beside `CompKey`-named files.** No `CompKey` starts with
  `.`, so the `.deployments` level is equally safe under `data/vault/` and
  `data/prefs/<CK(user)>/`, where `main`'s files sit.
- **The dot level is never scanned.** `data/` and `.xbin/` are reserved
  top-level names (`util.go:67-70`) that the registry never scans
  (`registry.go:443-445`). Every walker that skips dot entries also stays out of
  `.deployments`.
- **Labels cannot collide.**
  - `main`'s fs labels are `<ScopeKey>/<name>`, which never starts with `.`.
  - `main`'s kv labels start with `kv:res:`.
  - Non-`main` labels start with `.deployments/` or `kv:.deployments/`.
  - Among non-`main` keys, `<escS>` and `<d>` contain no `/`, so the triple
    (`d`, `S`, name) is recoverable from any key.
- **Deployment names need no hash.** They are immutable, contain no `/` or `.`,
  and have at most 24 characters, so the name itself is the key. (The research
  proposed a slug plus hash only because branch names contain `/`.)
- **A stale file never applies.** A per-deployment file is honoured only for a
  deployment that the tile's verified record lists (P29). Adding deployment
  `d` first deletes any per-deployment files for `d` left from an earlier
  deployment of that name: its vault file, every user's prefs file for
  `(T, d)`, its registration directory and its log directory. A new
  deployment's vault therefore starts with placeholders only (P14). The shared
  `(S, d)` namespace is not deleted by an add; joining one that holds data is
  §6.2's manager act.

### 3.4 What an older xbind sees

| State | What an older binary does with it |
|---|---|
| `data/resources-enc/.deployments/…`, `.xbin/resenc/.deployments/…` | Never computes these paths. At boot, `RecoverStale` lazily unmounts any mount left under `.xbin/resenc/` (`resenc.go:270-278`), which is harmless. |
| non-`main` kv | Lives in its own files and never in `data/kv.db`. Old code does only named lookups of `res:` buckets. |
| non-`main` vaults | Live under `data/vault/.deployments/`. `migrateVaults` skips directories (`vault.go:122`), and `vaultPath` never yields them. |
| prefs | In a dot-level subdirectory that old code never computes. |
| logs, D113 state | Under `.xbin/deploy/`, which old code never computes. |
| non-primary registrations, non-primary backup schedules | In files under `data/deployments/`. They are never rows in `cron-jobs.json`, `bus-subscriptions.json`, the root `xbin.json` or `backup-schedule.json`. |
| main archives of a tile with a record | Stay schema 1. The new manifest field is additive (old readers decode the manifest without refusing unknown fields, `internal/backup/backup.go:161-164`), and new tar prefixes are skipped by old `restore`, whose switch has no default case (`internal/broker/backup.go:242-280`). A zero-state tile's archive is today's, byte for byte. |
| deployment archives | Schema 2. Old readers refuse them with "upgrade to restore" (`internal/backup/backup.go:165-167`). |
| quotas | The old disk monitor measures only `ScopeKey` directories, so non-`main` usage goes unaccounted while downgraded. |

`12-compat.md` owns the downgrade proof. This table is the storage input to it.

### 3.5 AppArmor

D110's rule allows FUSE mounts only at `<ws>/.xbin/resenc/**/`
(`deploy/install.sh:841-848`). AppArmor's `**` matches across `/` and doesn't
exclude dot-prefixed components. Stock profiles rely on this when they write
`@{HOME}/**` and then deny `@{HOME}/.ssh/**` explicitly. That is an inference,
so the QA box (Ubuntu with the installer's block) must mount a non-`main` volume
before this ships (§14.2). Non-`main` mounts must never be placed outside
`.xbin/resenc/`.

### 3.6 Mounting and sealing

- **`main`'s volumes mount as today.** `MountEncrypted` mounts every
  file-backed resource of `D(S, main)` at provision, unseal and
  `cap:containers` changes (`resources.go:65`; `broker.go:219`, `:227`,
  `:664`). For a scope root tile without a record, and for a plain-directory
  scope, `D(S, main)` is today's work-tree declaration, so nothing changes.
- **Non-`main` volumes mount on first use,** and only for resources of
  `D(S, d)`: a backend start of a deployment that addresses the namespace, or
  a blob request. They stay mounted until seal, reset, removal, orphaning or
  shutdown. Mounting them all eagerly would run one gocryptfs process per
  volume per deployment for nothing.
- **The spawn hold is per deployment.** `EncryptionHold`
  (`resenc_wire.go:106-129`) checks the addressed namespace's volumes, and
  mounts non-`main` ones on demand. `ShouldRun` (`internal/boot/boot.go:517-518`)
  composes it per deployment (`07-runtime.md`).
- **A `cap:containers` change reaches mounted non-`main` volumes too.** They are
  re-`Ensure`d, so the single-tenant mode follows the grant
  (`resenc_wire.go:55-65`, `resenc.go:142-155`).
- **Sealing stops every deployment that addresses a file-backed volume** and
  unmounts all volumes. `SealResources` (`resenc_wire.go:150-162`) stops by tile
  path today, so its stop must reach every deployment. `UnmountAll`
  (`resenc.go:256-266`) already covers every volume the manager mounted.

### 3.7 When a namespace comes into existence

- **A zero-state workspace gains nothing:** no `.deployments` level, `ns.json`,
  namespace kv file or volume (12-compat PO-7).
- **`(S, d)` is created by the first committed act that needs it:** a data
  write or a volume mount by deployment `d` (the kv file on its first write, a
  volume on its first use), a committed seed, or a restore into it. A read of
  a namespace that doesn't exist yet answers as an empty one (a missing key, an
  empty list) and creates nothing.
- **Dry runs create nothing** (05-model §5). A dry run (`dryRun: true`,
  `11-contract.md` §1.2) of add, seed, reset, restore or remove computes its
  impact from `diskMon`'s figures, `ns.json` and the declared sets: bytes,
  resources, the deployments that would stop, the claimants, and whose data a
  join would expose. It creates no namespace directory, `ns.json`, mount, kv
  file or checkpoint store.

## 4. Which data a request reaches

### 4.1 The addressed deployment

The principal carries a deployment attribute. `07-runtime.md` and
`11-contract.md` (§0.4, §7) define how instance, frame and terminal tokens set
it. An absent value means `main`, as a frame token without the claim does
(05-model §7).

| Caller | Addressed deployment |
|---|---|
| backend of `T+d` (instance token) | `d` |
| frontend document of `T+d` (frame token) | `d` (the claim; absent means `main`) |
| terminal or agent session of `T` | its target deployment (P24): a named deployment, or "the primary", which means whichever deployment is primary at each request (`11-contract.md` §7.1). A session whose API dropdown is "API off" holds no tile token and reaches no tile data. |
| admin (`Broker.IsAdmin`, as today); on the vault routes also tile managers in a human session (`11-contract.md` §8) | `?deployment=<name>` when given; otherwise the primary of the tile or scope addressed |
| a tile principal passing `?deployment=` naming another deployment | refused with 403. A tile principal can't address another deployment of its own tile (P12). |

- **A protected primary is never a session's target** (P24), so no terminal or
  agent token addresses its data. Its kv, blob, bus and vault are reached only
  by its own backend and frontend, by admins, and on the vault routes by tile
  managers in a human session.
- **Non-admin human sessions can't reach resources directly** today: `allowRes`
  refuses a principal without a component (`broker.go:493-509`). That doesn't
  change.

### 4.2 Same scope, cross scope, workspace level

For a caller of tile `T` in scope `S` addressing deployment `d`, and a resource
in scope `R`:

| Case | Data namespace | Role |
|---|---|---|
| `R == S`, and `S` is not the workspace scope | `(S, d)`: the deployment's own data | as today (same-scope auto-grant, ND5) |
| otherwise (cross-scope, or any `res:workspace/*`), `d` is `T`'s primary | `(R, P(R))`; for workspace-level resources, `main`'s workspace data | as today: the primary uses edges exactly as today |
| otherwise, `d` is not `T`'s primary, edge policy `read` | `(R, P(R))`, as above | clamped to `reader` (§7) |
| otherwise, `d` is not `T`'s primary, edge policy `block` | none | refused, naming the policy |
| admin with `?deployment=e` | `(R, e)` | admin |

In every row the name must be in the namespace's declared set (§6.7). One that
isn't is refused as an undeclared resource is refused today.

Workspace-level resources are an edge for every tile, including tiles in the
workspace scope (05-model §9):
- **They are always reached by an assigned grant.** `sameScope` needs a
  non-empty scope (`broker.go:454-460`), so a `res:workspace/*` grant is never
  auto-granted.
- **Splitting them would create an unseeded namespace per deployment name.**
  Every `dev` of every workspace-scope tile would share it. No shipped workspace
  declares any workspace-level resource.
- **`EnvFor` treats them specially today.** For a workspace-scope tile it still
  hands a workspace-level `filesystem`/`sqlite` resource a path, because
  `"" == ""` (`resources.go:83-91`). §5 item 6 says what non-primary
  deployments get instead.

Hand-built ids resolve like env-provided ones. The broker maps
(addressed deployment, canonical id), so SDK code that builds
`res:${self}/…` reaches the caller's own namespace. The agent template builds
`"res:" + xbin.Self() + "/beat"` (`builtin-templates/agent/_backend/schedule.go:176`,
`owner.go:225`), and the SDK builds `/api/xbin/kv/<res>/…`
(`sdk/kv.go:29-31`). Canonical ids never change, so manifests, SDK calls and
frontends' topic filters keep working.

### 4.3 The bus

Bus topics are the canonical id, so separation is server-side. Two things
reach consumers today:
- hub events filtered by `busFilter` (`resources.go:441-458`), which admins
  bypass entirely (`internal/server/server.go:606-608`);
- push subscriptions matched by resource and prefix (`bussubs.go:271-299`).

The rules:
- **Publish.** A publish lands in the addressed namespace. The hub event gets an
  additive `deployment: "<d>"` field when that namespace isn't `main`'s, and no
  field for `main`'s (`internal/events/events.go:11-17` has no such field today).
  Same-scope publishes from non-primary deployments stay in their namespace;
  cross-scope publishes need `writer`, which the read clamp removes.
- **WebSocket delivery to tile principals.** An event from namespace `(R, e)`
  reaches a frame, terminal or instance subscriber only if two things hold:
  - the subscriber passes today's reader check;
  - its addressed namespace for `R`, computed by §4.2, is `(R, e)`.

  Tile frontends always take this path, even in an admin's browser. A frame
  principal is never an admin: `framePrincipal` sets no `User`
  (`internal/auth/frametoken.go:304-320`), and `IsAdmin` needs `User` or
  `Owner` (`internal/auth/auth.go:92-94`). So a non-primary publish can't fire a
  primary frontend's `bus.on(prefix)` handlers, which filter only by topic
  prefix (`web/xbin-client.js:162-164`).
- **WebSocket delivery to admins.** Admin and owner sessions keep today's
  bypass (`server.go:606-608`) and receive every namespace. The `deployment`
  field tells admin tools which one (`11-contract.md` §3.4). Non-admin human
  sessions receive no bus events today (`busFilter` needs a component,
  `resources.go:441-444`), and that doesn't change.
- **Push delivery.** A publish in `(R, e)` goes only to active subscriptions
  whose addressed namespace for `R` is `(R, e)`. Which registrations are active
  is `09-fabric.md`'s.
- **Why `bus` and not `deployments`.** Rule C2 (12-compat §3.1) keeps non-primary activity off
  `reload`, `build-*`, `status` and notify (05-model §8). A bus message is
  data, not activity. It keeps type `bus`, so a non-primary deployment's own
  frontend receives its own messages with unchanged code. Namespace matching
  keeps it from every subscriber that addresses another namespace: the tile's
  readers and every other tile's principals. Only the claimants of `(R, e)`
  (the scope's deployments named `e`, which share it by P28), sessions that
  target them, and admins receive it. It carries no `component`, as today's bus
  events don't (`resources.go:429-433`).
- **In the zero state** only `main`'s namespace exists, so every rule reduces to
  today's.

### 4.4 The data routes under P26

xbind's API is default-deny for non-primary principals: every `/api/xbin/*`
route is classified deployment-scoped, primary-only or neutral, and an
unclassified route refuses them (05-model §10). `09-fabric.md` §6 and
`11-contract.md` own the complete table and its guard test. The routes this
document changes:

| Route | Class | For a non-primary principal |
|---|---|---|
| `/kv/{rest…}`, `/blob/{rest…}`, `POST /bus/publish` | deployment-scoped | its own namespace (§4.2), or the edge policy for other scopes (§7) |
| `/vault/{rest…}` | deployment-scoped | its own deployment's vault (§10); `?deployment=` naming another is 403 |
| `/prefs`, `/prefs/{key}` | deployment-scoped | its own prefs file |
| `GET /logs`, `GET /tile-status` | deployment-scoped | its own log, backend and namespace |
| `/cron/jobs`, `/bus/subscriptions`, `PUT /iface-instances`, `PUT /ingress-hosts` | deployment-scoped | its own registration files: cron and bus fire for it unless its deliveries are off, interface instances and ingress hosts dormant (`09-fabric.md` §6) |
| `POST /backup`, `GET /backups`, `POST /restore`, `/backup-schedule`, `GET /vaults`, `GET /resources`, `POST /vault-seal`, `POST /vault-unseal`, `POST /vault-rekey` | primary-only (admin gates, `internal/broker/admin.go:25-31`) | refused: a non-primary principal never satisfies `IsAdmin` (P19) |
| the data acts under `/api/xbin/deployments/…` (seed, reset, vault copy, per-deployment backup, restore and schedule) | the deployments plane | refused for instance, frame, cron and bus principals (05-model §10); reset alone admits the tile's terminal and agent tokens (§9.1) |

**Who reads non-primary facts through these routes.** `GET /logs` and
`GET /tile-status` with a non-primary `deployment` answer admins, humans at
`terminal` level on the tile (today's human gate for logs,
`internal/obs/logs.go:42-47`), that deployment's own principals, and the
tile's terminal and agent sessions. They never answer the primary's frame
principal, which is minted for the tile's readers and so is not an own
principal for non-primary facts (05-model §8), nor another deployment's frame
or instance principal. Alerts that name a non-primary namespace reach admins
only (§12 item 5).

## 5. Env vars and binds

1. **Values are identical for every resource two deployments both declare.**
   `EnvFor` (`resources.go:71-145`) emits `main`'s values for every deployment:
   - the canonical resource path for same-scope `filesystem`/`sqlite`;
   - the canonical id for everything else.

   The values never depend on the deployment. Code that stores absolute paths
   therefore keeps working after a seed. The devbox podman store is the case in
   point: `--root` sits on `res:apps/devbox/storage`
   (`builtin-tiles/devbox/xbin.json`). A resource that only some deployments
   declare yields its variable only in those, because `EnvFor` resolves each
   `uses` entry against the deployment's declared set (§6.7) and skips an
   undeclared one, as it does today (`resources.go:74-77`).
2. **The broker supplies a remap.** `EnvFor(c, dep)` returns the env plus a
   remap from each canonical resource path to the host directory that backs it
   for `dep`. `07-runtime.md` §10.4 owns the signature.
   - For `main` the remap maps each path to itself, so the binds are today's
     (`Src == Dst`).
   - For `d`, the canonical path maps to
     `.xbin/resenc/.deployments/<escS>/<d>/fs/<name>` (`Src ≠ Dst`).
   - An entry can carry a read-only flag. v1 never sets it (item 6), but the
     flag keeps the shape ready for a read-only edge value.
   - The sandbox init already supports this: `mountBind` binds `Src` at
     `<root>/Dst` (`internal/sandbox/init_linux.go:391-422`; `Bind`,
     `internal/sandbox/sandbox.go:109-114`).
3. **An unmapped path never binds at itself for a non-`main` deployment.**
   - Today `resourceBinds` (`internal/runner/binds.go:28-54`) binds every
     absolute env path under the workspace at itself (`binds.go:51`). That path
     is `main`'s mount, so without the remap a non-`main` backend would get
     `main`'s data read-write.
   - `resourceBinds(env, root, remap)` fails the start when a non-`main`
     deployment has a path-valued resource with no remap entry
     (`07-runtime.md`).
   - A test must assert that no non-`main` spec contains a bind whose `Src` lies
     under `main`'s `.xbin/resenc/<ScopeKey>/` or `data/resources-enc/<ScopeKey>/`.
   - `stopFirst` (`internal/runner/vm.go:128-133`) uses the same per-deployment
     binds.
4. **VM guests see the deployment's data.** `exports` hands binds to the guest
   by destination (`internal/vm/manager.go:338-370`), and the shim's view already
   holds the deployment's volume at the canonical path. No VM change.
5. **Binds need isolation.** The non-isolated spawn path runs the backend on the
   host with `cmd.Dir = c.Dir` (`runner.go:453-469`). There the canonical path
   *is* `main`'s data, which is why non-isolated mode is unsupported for pinned
   and non-primary backends (P18). Static-only tiles need no binds: their data
   is reached through the broker.
6. **Workspace-level `filesystem` and `sqlite` for a non-primary deployment of
   a workspace-scope tile** are `block` only (P23; `09-fabric.md`'s edge
   table), because a read-only bind is not a safe read:
   - a WAL database's readers need `-shm` write access, and VM guests' caches
     aren't coherent with a writer (`internal/runner/vm.go:124-127`);
   - so there is no remap entry and no bind. The env var stays, so env is
     identical, and the path is simply absent in the sandbox;
   - a later read-only value for `filesystem` alone would reuse the remap's
     read-only flag.
7. **Tile-managed sandboxes** (D113, once built): resource mounts resolve
   through the same remap, by the owning backend's deployment. In-sandbox paths
   are identical across deployments. `{"source":true}` is `07-runtime.md`'s.

## 6. Scopes: sharing and declarations

No shipped scope has more than one member tile (research §1.3). The sharing
rules (§6.1–§6.6) exist for multi-tile scopes and must not cost the common
case anything. §6.7 applies to every scope.

### 6.1 Keyed by `(scope, deployment name)`

Siblings with a deployment named `d` share `(S, d)` (P28). A sibling without
`d` never touches it: its primary uses `(S, P(S))`.
- A same-scope call from `apps/a+dev` to `apps/b` is an edge
  (`09-fabric.md`). Under `read` it reaches `apps/b`'s primary, which serves
  `(S, P(S))`. So `apps/a+dev` can read the scope's primary data through
  `apps/b`'s API, read-clamped.
- The isolation of non-primary data is for **direct** access (kv, blob, bus,
  binds). The edge's `block` closes the API path, and the edge-policy UI must
  say what `read` exposes (`02-goals.md` Divergence 2).

### 6.2 Joining a namespace that holds data is a manager act

Adding `d` to a tile whose scope already has `(S, d)` in state `seeded`,
`restored` or `partial` gives that tile's writers reach into that data. It is a
tile manager's act on the joining tile, in a human session. Joining an `empty`
namespace is ordinary (terminal level). The add's answer, and its dry run,
say whose data the new deployment joins (from `ns.json`).

### 6.3 Scope-wide acts

Seeding, resetting and restoring `(S, d)` stop every backend of every member
tile with `d`. The actor needs the act's level (§8.1, §9.1, §11.4) on **each**
of those tiles (P28). A 403 names the tile that blocks it.

### 6.4 Deletion is refcounted by claimants

The claimants of `(S, d)` are the member tiles of `S` whose deployment record
lists `d`. The set is derived, never stored, so it can't drift. The namespace
is deleted when the last claimant removes `d` (§9.2, P28).

### 6.5 Reassignment and new members never split a scope

- Reassigning one member's primary would split the scope's primary data:
  `apps/a`'s primary on `(S, dev)`, `apps/b`'s on `(S, main)`. That breaks the
  documented same-scope sharing (`docs/resources.md`). 05-model §5 therefore
  allows reassignment in v1 only for a tile in the workspace scope, whose
  members never share a split namespace, or a tile that roots its scope as its
  only member.
- The same split would follow from a new member. While `P(S) ≠ main`, creating
  a tile inside `S` through xbind (create, clone, template instantiation,
  import) is refused for every creator, with a reason that names the
  reassignment (NP-08-4).
- A member that appears in the work tree anyway (a terminal creates the
  directory) serves `(S, main)`, as P6 says. The scope root tile's deployments
  panel names the split.
- Every member created through xbind therefore serves `(S, P(S))`.

### 6.6 Scope membership comes from the work tree

Membership is tile-level (05-model §6). A `scope.json` that disappears for a
while (an agent checking out an old branch) moves a tile's claim to another
namespace, for every deployment. The grace period of namespace GC (§9.3) makes
that harmless. A checkpoint's `scope.json` never creates or removes a scope; it
only declares resources for a scope the work tree has (§6.7).

### 6.7 Which resources a namespace has (P22)

Resource declarations are deployment-level (05-model §6). `D(S, d)` is read
from the code that owns `S`'s `scope.json` for `d`:

| `S` is | `D(S, d)` comes from |
|---|---|
| the workspace scope | the root `xbin.json`'s `resources`, for every deployment: workspace-level resources are never split (§4.2) |
| rooted by tile `R` without a record | the work tree, as today |
| rooted by tile `R`, and `R` has deployment `d` | `d`'s code: the work tree while `d` is `R`'s live reload target, otherwise `d`'s checkpoint's `scope.json` |
| rooted by tile `R`, and `R` has no deployment `d` (a sibling has one) | `R`'s primary's code, the same set as `D(S, P(S))` |
| a plain-directory scope | the work tree's `scope.json`, for every deployment (it belongs to no tile, so it has no checkpoint) |

**Reading a checkpoint's `scope.json`.**
- It is opened with `fsutil.OpenBeneath(<materialized tree>, "scope.json")`
  (`internal/fsutil/beneath.go:34`), never with `os.ReadFile`, which follows
  symlinks; that is how the registry reads the work tree's today
  (`internal/registry/registry.go:447`).
- It is parsed as JSONC, as `Rescan` parses the work tree's, and handled the
  same way: a file that doesn't parse declares nothing, and its error surfaces
  on the deployment (`registry.go:449-453`).
- Names are validated by §3.2's rules for the target namespace; a refused name
  is skipped with its error.
- A non-`main` namespace takes at most 64 declared resources, in name order.
  The rest are skipped with an error, because each file-backed one can mean a
  gocryptfs process. `main` keeps no cap (P5).
- Checkpoints are immutable, so the parsed set is cached by (tile, tree).

**When provisioning runs.**
- **A save** provisions, from the work tree, only the namespace whose
  declarations the work tree owns: `(S, L)`, where `L` is the scope root
  tile's live reload target. While that tile's live reload is paused, a save
  provisions nothing for `S`. For a root tile without a record `L` is `main`,
  and this is today's `Provision()` (`resources.go:36-66`), called from the
  watcher (`07-runtime.md` §6). In the workspace scope and a plain-directory
  scope the work tree declares for every deployment, so a save updates every
  namespace's declared set; only `main`'s volumes mount eagerly (§3.6).
- **A deploy or start** of deployment `d` settles `D(S, d)` for the namespaces
  `d` addresses before its backend starts, on the spawn path, under the spawn
  hold (§3.6). A pinned primary provisions `(S, P(S))` from its checkpoint (M1,
  14-implementation WP-18). Every deployment provisions from its own code (M2,
  WP-40).
- **`(S, P(S))` is never provisioned from non-primary code.** The primary gains
  a resource only when code declaring it is deployed to it (05-model §6).
- **Provisioning never deletes.** A resource the code no longer declares keeps
  its data, as today, and is reachable again when later code declares it.
  Reset and namespace GC (§9) are the only deleters.

**What the data plane resolves against.** `parseRes`
(`internal/broker/broker.go:340-365`) finds the scope from the registry, which
is tile-level (the work tree), and then looks the name up in the addressed
namespace's declared set. So an id declared only in `dev`'s code resolves for
`dev`, and is unknown to the primary and to cross-scope readers, who reach
`(S, P(S))` (§4.2). `EnvFor(c, dep)` resolves types against `D(S, dep)`.

**Limits.** A deployment's limits (its quota share, §12; its cgroup memory and
CPU, `07-runtime.md` §10.3) default to the tile's. Tile managers set them, in a
human session, never above the tile's ceilings (P22).

**Seeding across divergent declarations.** A seed copies the resources that
`D(S, P(S))` and `D(S, d)` both declare with the same type. Resources only `d`
declares start empty. Resources only the primary declares, or that the two
declare with different types, are skipped, and both the answer and `ns.json`
list them (§8.3).

## 7. Cross-scope reads under the read clamp

Under `read`, a non-primary deployment reaches the provider's primary data
(`(R, P(R))`) read-only. Per operation:

| Operation | Under `read` | Under `block` |
|---|---|---|
| kv `GET` key / list (`resources.go:204-247`) | allowed, from `(R, P(R))` | 403 |
| kv `PUT` / `DELETE` | 403, naming the policy | 403 |
| blob `GET` | allowed | 403 |
| blob `PUT` / `DELETE` | 403 | 403 |
| bus WebSocket subscription (`reader`, `subscriber`) | allowed; events from `(R, P(R))` only (§4.3) | none delivered |
| bus publish (`writer`, `publisher`) | 403 | 403 |
| bus push subscription, cron job on a foreign resource | stored; a subscription receives from `(R, P(R))`, its edge re-checked at every delivery (`09-fabric.md` §7) | refused |
| cross-scope `filesystem`/`sqlite` | no env path, no bind (unchanged: `resources.go:92-94`) | same |
| workspace-level `filesystem` / `sqlite` (workspace-scope tile) | not offered: this edge is `block` only (§5 item 6, P23) | no bind |

- **The error names the policy and offers no way around it.** A resource grant
  takes only `read` or `block` for non-primary deployments (P3), so nothing
  lets one write another scope's data. For example: `apps/email+dev may not
  write res:apps/calendar/bus: non-primary deployments reach other scopes
  read-only (edge policy "read")`. The exact text is `11-contract.md`'s.
- **The clamp is `min(granted role, reader)`.** Bus aliases normalize as in
  `roleSatisfies` (`broker.go:371-380`): `publisher` clamps to `subscriber`.
- **The canonical id is unchanged.** `examples/email+dev` reading
  `res:apps/calendar/bus` gets the calendar's primary bus. It never gets a
  namespace the calendar scope never seeded. `match` would route it to
  `(apps/calendar, dev)` later.
- **Tile-to-tile HTTP is `09-fabric.md`'s.** Two of its consequences bear on
  data (05-model §7): the agent tile reaches its sandbox managers through a
  custom role, which P23 blocks for non-primary deployments, and llm-gw guards
  completions with `writer`, which the read clamp removes.

## 8. Seeding

Seeding is optional, and empty is the default (P14; the owner, 2026-09-27).
Most non-primary deployments run on empty or synthetic data that their own
code creates. A seed copies `(S, P(S))` into `(S, d)`, where `d ≠ P(S)`,
**replacing** whatever `(S, d)` held. Never seed through the backup tar: it
drops symlinks and races the running backend
(`internal/backup/backup.go:127-130`; §11.6).

### 8.1 Who, when, and what the UI must say

- **Authority:** a tile manager (D24/D33) of every claimant tile of `(S, d)`
  (§6.3), in a human session: `p.Component == ""` and (`IsAdmin` or
  `mayManageTile`) (`internal/auth/auth.go:92`,
  `internal/broker/orgsapi.go:427`; 05-model §10).
  - Terminal and agent tokens are refused even when their user is a manager,
    because the terminal's agents share those tokens.
  - No element principal passes, whatever `xbin` or `xbin:users` grants its
    tile holds.
  - View-as sessions (D64) are refused.
- **Confirmation:** the request carries `11-contract.md`'s `confirm:
  "copy-data"` token. `bx deployment seed` asks on a TTY; without one it needs
  `--yes`, and otherwise exits 4 (`11-contract.md` §9.1). A dry run answers
  the facts below and creates nothing (§3.7).
- **Required warning, from facts xbind computes:**
  1. It copies the primary's data **as of now**, which may include personal
     data: N MiB, in these resources.
  2. Everyone with `write` on each claimant tile can open `<tile>+<d>` and see
     this data; `terminal`-level users and their agents can deploy any code
     there that reads it. **Protecting the primary doesn't cover the copy.**
  3. The copy stays until reset or removal, and opt-in backups of `<d>` send it
     to the archiver.
  4. Online or stopped (§8.4), and for how long the primary will be unavailable
     if stopped.
  5. The resources the two sides declare differently (§6.7): which are
     skipped, and which start empty.
- **Audit:**
  - a `slog` line `deployment data seeded` with tile, scope, from, to, by,
    bytes, resources, skipped resources and consistency;
  - a history entry in `ns.json`;
  - the tile-level `deployments` event, op `data` (05-model §8;
    `11-contract.md` §3.3), for each claimant tile. It names a non-primary
    deployment, so it goes only to admins, humans with at least `write` on that
    tile (their current level), and deployment `d`'s principals plus the
    tile's terminal and agent sessions: never to the tile's readers or to other
    tiles.

### 8.2 The procedure

Operations on one namespace serialize (one lock per namespace). A second data
act on a held namespace answers `409 data busy` (`11-contract.md` §1.14). A
seed:

1. **Authorize and preflight.**
   - The vault must be unsealed; kv and file volumes need keys.
   - gocryptfs must be available if `S` has file-backed resources.
   - The size of `(S, P(S))` must fit the target's limit and the free space
     above the reserve (§12).
   - Compute the resources to copy, start empty and skip from the declared
     sets (§6.7).
   - Decide online or stopped (§8.4). If §8.4 requires a stopped seed and the
     request didn't ask for one (`stop: true`), refuse and say why, so the
     primary's outage is always a stated choice.
2. **Hold `(S, d)`.** Write `ns.json` state `seeding` with by, at and
   `from: P(S)`. Stop every backend, and every D113 sandbox, addressing
   `(S, d)`. Requests to `(S, d)` get 503 with `Retry-After`. Unless its
   deliveries are off, cron and bus deliveries fail meanwhile, at most once.
3. **Stopped mode only: hold `(S, P(S))` too.** Stop the primary's backends.
   Data-plane writes into it wait on its write gate (below). The primary's API
   answers 503 until step 7.
4. **Wipe `(S, d)`.** Use the reset steps (§9.1, steps 2–3). The fresh volumes
   `Ensure` creates are initialized under `d`'s labels, which is the re-key.
5. **Copy each resource both sides declare** (§8.3), recording bytes and
   anything lost.
6. **Verify** (§8.5).
7. **Commit.** Write `ns.json` state `seeded`, with:
   - `from`;
   - `fromCheckpoint`: the primary's checkpoint, or `worktree` if it follows the
     work tree (helps rehearse migrations, flow F);
   - `at`, `by`, `consistency` (`online` or `stopped`);
   - per-resource bytes, and the skipped resources.

   Lift the holds.

The **write gate** of a namespace is an `RWMutex`:
- API-mediated writes (`apiKVPut`/`apiKVDelete`, `resources.go:249-298`;
  `apiBlobPut`/`apiBlobDelete`, `:366-406`) take it shared.
- A seed takes it exclusively, only while it reads that namespace.
- A write waiting more than 30 s gets 503 with `Retry-After`.
- Only that namespace's writes wait; every other scope and tile is untouched.

`11-contract.md` §1.8's seed body must carry `stop?: bool`. It is a new
route's body, so adding the field costs nothing.

### 8.3 Per kind

- **kv (in-process, no tool).**
  1. Under `(S, P(S))`'s write gate, copy the raw, still-encrypted pairs of
     every kv bucket to copy from the primary's store into a spool file in
     `(S, d)`'s root. Use bounded read transactions of at most 10,000 keys or
     4 MiB each, so no long read transaction holds up `kv.db`'s remaps.
  2. Lift the gate.
  3. Decode each value with the primary's label, encode it with `d`'s label,
     and write it into `(S, d)`'s store in bounded write transactions of at most
     1,000 keys or 8 MiB. Delete the spool.
  - All buckets of the scope are consistent with one another at one moment.
  - Don't reuse `loadKV`: it writes everything in one `Update` on the shared
    `kv.db` (`internal/broker/backup.go:330`), blocking every tile's kv writes.
  - Don't reuse `dumpKV` as-is: it discards the `View` error
    (`internal/broker/backup.go:179`), so a value that fails to decode at
    `:187-190` yields a silently truncated dump. The seed code must propagate
    every decode error.
  - When the target is `main`'s store (`main` not primary), the write
    transactions go to `data/kv.db` with the same bounds.
- **sqlite (`<name>.sqlite` of a `sqlite` resource): confined.**
  - Run `python3 -` in confine with an embedded script on stdin. xbind links no
    sqlite driver, and the rootfs ships `python3` (`docker/rootfs.Dockerfile:15-23`).
  - Binds: the primary's mount at `/from`, read-write because a WAL reader
    writes the `-shm`; the connection opens `file:/from/<name>.sqlite?mode=ro`.
    The fresh target mount goes at `/to`.
  - `06-security.md`'s confined-run table (row L11) lists the primary's mount
    read-only. With a read-only bind, SQLite reads a WAL database only in its
    read-only-shm fallback, which retries or fails under concurrent
    checkpoints. This document therefore binds it read-write and never writes
    the database; L11 should say so.
  - `Connection.backup(target, pages=-1)` copies in one step: a consistent
    point-in-time read while the primary keeps writing.
  - Then run `PRAGMA integrity_check` on `/to`.
  - Every other file in the resource directory is copied as for `filesystem`,
    excluding `<name>.sqlite` and its `-wal`, `-shm` and `-journal` sidecars.
  - confine's direct mode (`internal/confine/confine.go:140-143`) runs the tool
    on the host when isolation is off. Only static tiles can have non-primary
    deployments there (P18), and they have no file-resource backends.
- **filesystem (normal mode): confined.**
  - Run `rsync -aHX --numeric-ids /from/ /to/` (rsync ships in the rootfs,
    `docker/rootfs.Dockerfile:22`). The primary's mount is bound read-only at
    `/from`, and the target mount at `/to`. There is no `-A`: xbind mounts
    gocryptfs without `-acl` (`resenc.go:177-181`), so there are no POSIX ACLs
    to copy, and rsync would report a partial transfer.
  - Files starting with the 16-byte SQLite header should be copied with the
    backup-API step above instead, so databases a tile keeps inside a
    `filesystem` resource are consistent even online.
  - rsync exit 24 (files vanished during the copy) is a warning, not a failure.
  - The confine sandbox sees only the two mounts. A symlink the primary planted
    to reach `data/vault/` resolves inside the sandbox and finds nothing.
- **filesystem in single-tenant mode** (scopes with a `cap:containers` tile,
  `resenc_wire.go:55-65`; D43): **stopped only.**
  - Copy the primary's cipher directory into the target's, with `cp -a
    --reflink=auto` in confine. Cipher directories are xbind-only data
    (`resenc.go:283`).
  - Then re-wrap the target's `gocryptfs.conf` under `d`'s label
    (`gocryptfs -passwd`, fed both passwords on stdin like `resenc.Manager.run`,
    `resenc.go:282-290`).
  - This is lossless: virtual owners, modes, whiteouts and devices live in the
    ciphertext and its encrypted xattrs.
  - The copy **shares the primary volume's master key**. That is documented,
    and accepted only for this mode, which cannot be copied through the mount.
- **blob: in-process by xbind.**
  - Blob volumes are written only through the API, as regular files and
    directories (`resources.go:366-406`), and are never bound into a sandbox
    (they're absent from `EnvFor`'s path cases and from `resourceBinds`).
  - Walk with `fsutil.OpenBeneath` (`internal/fsutil/beneath.go:34`) and copy
    regular files and directories only. This needs no host tool when isolation
    is off.
- **bus:** nothing. It is in-memory and at-most-once.
- **cron resources, registrations:** nothing. A deployment registers its own at
  start, as a new tile does (D85; `internal/broker/create.go:188-192`).
- **vault:** never part of a seed (vault copy, §10).
- **A resource only one side declares, or declared with different types:**
  nothing is copied (§6.7).

### 8.4 Online or stopped

| Condition | Mode |
|---|---|
| default | **online**: kv consistent under the gate; each sqlite database point-in-time; `filesystem` and `blob` copied file by file, not point-in-time (a file written during the copy may be copied mid-write) |
| the manager asks (`stop: true`) | **stopped**: everything point-in-time across resources; the primary is down for the copy |
| `S` has a single-tenant volume | stopped, required |
| a backend addressing `(S, P(S))` is a VM with file-backed resources (the `stopFirst` condition, `internal/runner/vm.go:128-133`) | stopped, required. The host can't see a guest's cached pages or its WAL shared memory (`vm.go:124-127`). |
| a D113 VM sandbox mounts a resource of `(S, P(S))` | stopped, required, for the same reason |

`ns.json` records the mode, and the deployments panel shows it
(`10-ux.md`).

### 8.5 Failure and recovery

- **Verification:**
  - kv: key counts match, and every value round-trips.
  - sqlite: `integrity_check` returns `ok`.
  - filesystem: rsync exits 0 or 24.
  - blob: file counts and bytes match.
- **Any failure** leaves `ns.json` in state `partial`, with the failing step and
  error. `(S, d)` stays held. Deployments addressing it refuse to start with
  `seed of <S> for <d> failed at <step>: reset or seed again`.
- **A crash mid-seed** (xbind dies) leaves state `seeding`. Boot turns it into
  `partial`.
- **The primary's namespace is only read.** Every copy binds it read-only,
  except the sqlite `mode=ro` connection, which writes only the WAL's
  shared-memory read marks.

## 9. Reset, remove, GC

### 9.1 Reset

Reset empties a non-primary namespace. It is `terminal`-level (05-model §10),
and the tile's terminal and agent tokens may do it, as they may other
non-primary acts. It needs that level on every claimant tile (§6.3) and
carries `11-contract.md`'s `confirm: "erase-data"`; `bx deployment reset`
without a TTY needs `--yes`, and otherwise exits 4. Resetting `main` while it
is not primary is a tile manager's act in a human session. A dry run creates
nothing (§3.7).

1. Hold `(S, d)` (§8.2 step 2).
2. For each file-backed resource:
   - `resenc.Manager.Unmount` (`resenc.go:243-253`, which has no caller today);
   - check that the mount is gone. If it is still mounted (for example
     `EBUSY`), **abort** and report. Never `RemoveAll` a mounted directory:
     that deletes through the FUSE mount, partially;
   - `os.RemoveAll` the cipher directory and the mount directory. This is not a
     tool run on sandbox data: cipher directories are xbind-only, and
     `RemoveAll` doesn't follow symlinks.
3. **kv:** close and delete the namespace's `kv.db`. If the target is `main`'s
   (reset while `main` isn't primary), `DeleteBucket` each of `S`'s kv buckets
   in `data/kv.db` instead.
4. Write `ns.json` state `empty`, keeping the history, and lift the hold. The
   next `Ensure` initializes fresh volumes with fresh master keys.

Reset leaves the vault alone by default (the glossary's **Reset**; 05-model
§5). With `vault: true` it also empties the deployment's vault file, so every
key shows as a placeholder again (§10). `11-contract.md` §1.8's reset body
must carry `vault?: bool`.

**Resetting `main` while it is not primary** deletes the data `main` served
when it was primary. The UI must say whose data it deletes.

### 9.2 Removing a deployment

05-model §5's "Remove deployment Y" deletes, in this order:

1. Mark `d` `removing` in the record, so nothing starts it. Stop it
   (`07-runtime.md`).
2. **Per-tile state**, deleted at once:
   - the vault file;
   - every user's prefs file for `(T, d)`;
   - the dormant-registration directory `data/deployments/<TK>/<d>/`;
   - the derived-state directory `.xbin/deploy/<TK>/d/<d>/` (the log, and D113
     sandbox state);
   - runtime artifacts (`07-runtime.md`);
   - in-memory entries: status, bus counters, `resUsageC`.
3. **The data namespace `(S, d)`:** deleted with the reset steps plus
   `RemoveAll` of the namespace root, **only if `T` was its last claimant**.
   Otherwise it stays for the siblings.
4. Remove `d` from the record.

If xbind dies midway, the GC sweep (§9.3) finishes the job.

### 9.3 Namespace GC and leftovers

Checkpoint and store GC is `07-runtime.md` §2.8's. This section covers data
namespaces only.

- **Sweep:** at boot, after the registry scan, and after every structure change.
  It lists `data/resources-enc/.deployments/*/*` and compares them with the
  claimant set. This is cheap: directory names and in-memory records.
- **Orphans:**
  - A non-`main` namespace with no claimant is unmounted and marked `orphaned`
    (with the time) in `ns.json`. It is listed in the admin console and
    `bx doctor`.
  - It is deleted after **14 days**, unless a claimant reappears. The grace
    period covers `scope.json` vanishing for a while (§6.6).
  - Admins can delete an orphan at once.
  - `main`'s data is never deleted by GC, as today.
- **D82 leftovers:** `pathLeftovers` (`internal/broker/policy.go:218-258`)
  checks only `vaultPath(path)` among data (`:250-252`). It must also report,
  for the path and anything under it:
  - the deployment record and the checkpoint store (05-model §11; the view
    repository is derived and goes with the record);
  - non-`main` vault files;
  - dormant-registration directories;
  - every data namespace whose scope is at or under the path, orphaned or not.

  The path's current owner stays exempt (`policy.go:219-221`).
- **`main`'s own scope data** left by a removed scope-root tile isn't in the
  leftovers list today either (research/identity-resources.md §5). Fixing that
  changes zero-state behaviour, so it is outside this design.

### 9.4 Offload of non-`main` namespaces, and the leak `main` keeps

**Today:**
- `removeScopeData` drops the kv buckets and the plaintext
  `data/resources/<key>` (`internal/broker/backup.go:473-489`, side finding #3).
- `data/resources-enc/<key>` and the mounts stay. The docs' "archived +
  removed" (`docs/overview/14-lifecycle.md:33-34`) is untrue for `filesystem`,
  `sqlite` and `blob`.
- `MountEncrypted` then keeps the offloaded scope's volumes mounted.

**Why a naive fix loses data.** A restore after offload merges into the
surviving volume, so symlinks, empty directories, xattrs and modes survive by
accident. The archive can't hold them:
- `Tree` skips every non-regular file and never writes directories
  (`internal/backup/backup.go:124-129`);
- `writeFileFrom` ignores the recorded mode
  (`internal/broker/backup.go:356-371`).

Deleting the volume on offload would turn a disk leak into data loss. The
devbox container store is the extreme case.

**The fix in this design covers non-`main` namespaces only,** whose archives
(schema 2) are new:

1. **The deployment-archive walk reports loss instead of skipping silently.** A
   file-backed resource is **lossy** if its walk meets:
   - a symlink, device, FIFO or socket;
   - an empty directory;
   - a file with more than one link, or with xattrs;
   - or if it is a single-tenant volume.

   The deployment archive's manifest lists lossy resources in `lossy` (§11.2).
2. **Deployment-archive headers carry mode and mtime, and new restores apply
   them.**
3. **After every archive PUT is confirmed, offload removes the ciphertext of
   each non-`main` resource that is not lossy.** Per resource:
   - `Unmount`, and check that it is unmounted;
   - `RemoveAll` the cipher directory and the mount directory.

   Lossy resources keep their ciphertext. The offload result and an admin alert
   name them ("kept 3.1 GiB of `storage`: the archive can't hold symlinks").
4. **Non-`main` namespaces of an offloaded scope root are never mounted.**
   First-use mounting skips them, so a request can't re-initialize an empty
   volume for a removed one.

**`main`'s offload stays today's, leak included, for every tile.** Removing
`main`'s ciphertext, adding mode and mtime headers to main archives, or a
`lossy` field on them would change zero-state tiles (12-compat Z1). It would
also open a downgrade gap: an older restore rebuilds a deleted volume without
file modes, because `writeFileFrom` ignores them. That fix is a separate change
with its own decision, changelog entry and downgrade note, which must count a
non-default file mode as lossy while an older restore is possible (NP-08-10).
Until then, [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md)
states that offload keeps `main`'s encrypted file resources on disk.

A later change can shrink the lossy set. It would record empty directories and
symlink targets in a sidecar tar entry that older restores ignore, and have new
restores recreate them inside the mount without following them. Until then the
rule stays strict.

## 10. The vault

- **One file per deployment.** `data/vault/.deployments/<TK>/<d>.json` uses the
  same envelope as `main`'s (`internal/broker/vault.go:35-38`, `:79-111`).
  `vaultPath` takes the addressed deployment (§4.1).
- **Who reaches which vault:**
  - a backend reaches its own deployment's vault;
  - a terminal or agent session reaches its target's (P24), never a protected
    primary's;
  - frames still get nothing (`vault.go:167-170`);
  - admins reach the vault named by `?deployment=`, otherwise the tile's
    primary's (the admin console's password-manager function); tile managers
    may do the same in a human session (`11-contract.md` §8).
- **A protected primary's vault** is written only by its own backend, by
  admins, and by tile managers in a human session (§4.1).
- **D30 per deployment.** A value read requires three things:
  - `p.Component == T` and `p.Via == "instance"` (`vault.go:188`);
  - **and** the instance token's deployment equals the addressed deployment.

  The last clause holds automatically, because tile principals can't address
  another deployment. It is still checked explicitly, as defense in depth. A
  non-primary backend never reads the primary's values unless a manager copied
  them.
- **Placeholders are computed, never stored.**
  - Listing a non-`main` deployment's vault returns `keys`: the keys with a
    value in this deployment's file.
  - It also returns an additive `placeholders` field: the primary's key names
    absent here, computed at request time from the primary's file.
  - A key the primary gains later shows up at once; a key it drops disappears.
  - Reading a placeholder's value answers 404: `vault key "<k>" has no value in
    deployment <d>; a tile manager can copy it from the primary`.
  - Key names aren't secret: every terminal-level user can list them today.
- **Vault copy.** A request copies `keys: […]` or `all: true` from the primary
  into a non-primary deployment. `11-contract.md` §1.8's body must carry
  `all?: bool`.
  - It is a tile manager's act in a human session (05-model §10), with the
    same gate as a seed (§8.1).
  - It reads through `vaultRead` and writes through `vaultWrite`, both atomic
    (`vault.go:55-111`).
  - It overwrites the named keys and never goes toward the primary.
  - The audit line names keys, never values.
  - The warning says the values become readable by `<d>`'s backend, whose code
    any terminal-level user can deploy, including while the primary is
    protected.
- **Add, reset and removal.** An add deletes a stale vault file for the name
  (§3.3). Reset empties the vault only with `vault: true` (§9.1). Removal
  deletes the file.
- **Legacy migration.** `migrateVaults` (`vault.go:115-147`) must also walk
  `.deployments/`, so a non-`main` vault written as plaintext under
  `--insecure-vault` is encrypted when a barrier is first initialized. Older
  binaries skip it, which is harmless.
- **Reassignment preflight.** The reassignment's dry run lists the target's
  placeholders: "`dev` has no value for 3 keys the current primary uses". Flow
  F otherwise cuts over to a backend without its secrets. While the primary is
  protected, the confirmed reassignment carries `expect` (05-model §5).
- **Barrier rekey** re-wraps the DEK only (`internal/vault/barrier.go:203`).
  Every deployment's vault is unaffected.

## 11. Backup, offload, restore

### 11.1 What an archive holds

| Content | Main archive (key `CompKey`, today's) | Deployment archive (key `.deployments.<TK>.<d>`) |
|---|---|---|
| work tree (`source/`), terminal layer (`term/`) | always, as today (`internal/broker/backup.go:60-108`) | never |
| `main`'s data (`data/…`), when the tile roots its scope | always, exactly as today | — |
| `main`'s cron jobs and bus subscriptions (manifest) | always, as today (`internal/broker/backup.go:79-86`); never another deployment's rows (`12-compat.md` PO-9) | — |
| deployment record, per-deployment registration files | only for a tile with a record, under a new prefix | — |
| checkpoint store | only for a tile with a record, under a new prefix. The view repository never: it is derived and rebuilt after a restore (05-model §3) | — |
| deployment `d`'s data `(S, d)` | — | always |
| any vault | never (`WithVault` is never set today, `internal/backup/backup.go:56`) | never |

**A tile without a record gets today's archive, byte for byte**, including one
that opted out and kept its checkpoint store: no `deployments` field, no new
prefix, and not the leftover store (P5).

A deployment archive is written in four cases:
- **The primary, when it isn't `main`: on every backup of the tile, manual or
  scheduled** (05-model §11). Otherwise, after flow F, the data actually served
  would be in no archive. The main archive lists this archive's version in
  `deployments.archives`, so one restore brings both back.
- **An explicit backup naming a non-primary deployment** (opt-in), through a
  new per-deployment backup endpoint under `/api/xbin/deployments/…`
  (`11-contract.md` owns it). It is a tile manager's act in a human session, or
  an admin's (05-model §10).
- **That deployment's own schedule** (§2), set through the same family by the
  same people. Today's schedule routes stay admin-only and `main`'s
  (`internal/broker/backup_cron.go:123-158`).
- **An offload**, which is forced (§11.5).

The data of a tile that doesn't root its scope is in no archive, as today
(`internal/broker/backup.go:61`, `:474`).

### 11.2 Format additions

- **Manifest fields** (`internal/backup/backup.go:45-57`), all additive:
  - `deployment`: whose namespace `data/` holds. Deployment archives only;
    absent means `main`.
  - `deployments`: `{record: bool, checkpoints: bool, archives: {"<d>": "<version>"}}`.
    Main archives of a tile with a record only. `archives` lists the
    deployment archives written by the same backup or offload.
  - `lossy`: a list of resource names (§9.4). Deployment archives only.
- **Schema numbers.** Main archives stay `schema: 1`. Deployment archives are
  `schema: 2`, so older readers refuse them instead of restoring `d`'s data into
  `main`'s keys (`internal/backup/backup.go:165-167`). `Writer.Manifest` stops
  forcing the schema (`internal/backup/backup.go:74-81`); the caller sets it,
  and readers accept up to 2.
- **New tar prefixes**, in main archives of a tile with a record only, which
  old `restore` skips:
  - `deployments/record.json`;
  - `deployments/registrations/<d>/<file>`;
  - `deployments/checkpoints/<path in the bare repository>`.

  Entry types stay regular files, so old binaries never meet a type they would
  write as an empty file.
- **Deployment-archive entries** carry mode and mtime in their headers (§9.4).
  Main-archive headers stay today's.
- **The main archive means what it means today.** Its `data/` holds `main`'s
  namespace, and its cron/bus lists hold `main`'s rows. An older xbind
  restoring it gets `main` back as a zero-state tile, and refuses every
  deployment archive. `12-compat.md` NP-12-10 states the same obligations.

### 11.3 Archive keys and retention

- **Keys:**
  - `main`: `CompKey(tile)`, unchanged.
  - Deployment `d`: `.deployments.<TK>.<d>`. It can never equal a `CompKey`,
    because no `CompKey` starts with `.`. It is injective: neither `<TK>` nor
    `<d>` contains `.`, so the dots split it.
  - It uses only characters today's keys already use, in one path segment. The
    builtin s3 archiver lists by `<prefix>/<key>/`
    (`builtin-tiles/s3-archiver/backend/main.go:104-121`), so the keys never
    share a listing.
- **Retention is separate:**
  - `pruneVersions` (`internal/broker/backup_cron.go:97-118`) lists one key, so
    `main`'s retention never deletes another key's version, and the reverse.
  - The tile's schedule prunes the primary's deployment archive (when the
    primary isn't `main`) to the same count, separately.
  - Non-primary schedules carry their own retention. The proposed default keeps
    3 versions.
  - Offload-forced versions are pruned only by their own deployment's schedule.
  - Removing a deployment never deletes its archives, as removing a tile never
    does today.

### 11.4 Restore

- **Two paths, and no new field on an existing body.** `POST /restore` keeps
  its strict body `{component, version, file}`
  (`internal/broker/backup.go:556-557`) and its meaning: it restores a main
  archive. A deployment archive is restored through a new endpoint under
  `/api/xbin/deployments/…` that `11-contract.md` defines, which names the
  deployment to restore into. That is a tile manager's act in a human session
  on every claimant of the namespace (§6.3), or an admin's.
- **Where data goes.** A deployment archive's data goes into the namespace of
  the deployment the request names (by default the manifest's `deployment`).
  If that deployment doesn't exist, the request is refused, and the answer
  lists the tile's deployments.
- **Validation before anything is written** (05-model §11). A deployment
  archive is refused if its manifest names another tile, carries a schema this
  binary doesn't know, or has a `deployment` that breaks the name grammar. A
  main archive whose `deployments` section belongs to another tile is refused
  the same way. A main archive without that section is placed as today.
- **Referenced archives.** Restoring a main archive of a tile with a record
  also restores each archive its `deployments.archives` lists, by replace, into
  those of the named deployments that exist. The answer names any it skipped.
- **Replace or merge.** Today `restore` merges:
  - `loadKV` puts into existing buckets (`internal/broker/backup.go:330-353`);
  - `writeFileFrom` replaces file by file (`internal/broker/backup.go:356-371`);
  - so stale keys and files survive (side finding #4).

  The docs already say "a restore overwrites wholesale"
  (`docs/overview/14-lifecycle.md:167-168`).
  - Restoring into a non-`main` namespace **always** replaces, which is the
    reset-from-archive use. For each resource in the archive that isn't lossy,
    it empties the resource first, then writes: kv deletes the namespace file;
    files unmount, `RemoveAll`, then take a fresh volume through
    `restoreFileDest` (`internal/broker/backup.go:311-320`). Lossy resources
    merge into their kept volume.
  - Restoring `main` through `POST /restore` keeps today's merge. Switching it
    to replace, as the docs promise, is a separate compat-reviewed change
    (NP-08-9).
- **The record** is installed only when the tile has none and it validates:
  its schema, a tile path equal to the restored tile, valid names, and 05-model
  §4's invariants. An owner ref that doesn't match the tile's current owner
  leaves it inert until an admin adopts or clears it (05-model §11). Otherwise
  the current record wins. Restore never removes a deployment.
- **The checkpoint store is rebuilt from the archive's objects only** (05-model
  §11).
  - Confined git (D78) imports the objects into the store and fscks them.
  - The archived refs are read as data and validated: each must name a commit
    among the imported objects. They are recreated under a restore-only ref
    namespace, so checkpoint GC keeps them, and no current ref moves.
  - The archived `config`, `hooks/`, `info/` and alternates are never
    restored; xbind writes its own.
  - `07-runtime.md` owns the store's ref layout.
  - The view repository is then refreshed by a confined `update-server-info`.
    It never advertises the restored refs (05-model §3).
- **Registrations.**
  - The main archive's cron/bus lists restore into today's stores, as today
    (`internal/broker/backup.go:283-303`). They only ever hold `main`'s rows.
  - `deployments/registrations/*` restores into the per-deployment files, only
    for deployments that exist.
- **Holds.** Restore takes the namespace hold, and refuses while a seed or reset
  holds it.

### 11.5 Offload

1. Stop every deployment of the tile.
2. Archive each non-empty namespace of the scope the tile roots other than
   `main`'s: the primary's, when it isn't `main`, and every non-primary one.
   - Write these archives first.
   - Then write the main archive, which lists them in `deployments.archives`.
   - The invariant "nothing removed before the archive PUT is confirmed"
     (`internal/broker/backup.go:451-469`) now covers every namespace.
   - A manager who doesn't want non-primary data archived resets it first.
3. Only after every PUT is confirmed, remove local data:
   - `main`'s exactly as today (its kv buckets and plaintext directory; §9.4);
   - every non-`main` namespace's kv file and non-lossy file volumes (§9.4).
4. **`offloaded-full`** also removes the terminal layer and the bulk of the
   work tree (`internal/broker/backup.go:493-512`), as today, and, for a tile
   with a record, the checkpoint store and view repository. The record stays,
   so the tile stays listed with its deployments.
5. **Re-enabling** restores the main archive, then each archive it lists.

A tile without a record offloads exactly as today. A leftover checkpoint store
is neither archived nor removed.

### 11.6 Walking resource trees safely

`backup.Writer.Tree` checks each entry's type with `WalkDir` and then calls
`os.Open` on the path (`internal/backup/backup.go:124-139`). A running backend
can replace the entry with a symlink in between and make xbind archive any file
it can read (side finding #5).

The contract of `fsutil.OpenBeneath` says that daemon code reading
sandbox-written files "opens them through here, never os.Open"
(`internal/fsutil/beneath.go:31-34`).
- **Every walk this design adds** (deployment archives, the blob seed) opens
  through `OpenBeneath(mount, rel)`, records mode and mtime, and reports loss
  (§9.4).
- **Today's main-archive walk** would change no archive byte by switching to
  `OpenBeneath`, except when an entry is replaced by a symlink mid-walk: that
  entry is then skipped, as `Tree` skips symlinks. This document therefore asks
  for the switch as a zero-state security closure (§14.3, NP-08-10). If it is
  not ratified, only the new walks use `OpenBeneath` (14-implementation §0.4).

## 12. Quotas and low-disk blocks

Today `diskMon` works as follows (`internal/broker/diskmon.go:18-37`,
`:114-166`):
- it measures each scope key every 45 s;
- it blocks kv and blob writes with 507 when the scope is over 50 GiB, or when
  it is a top user while less than 10% of the disk is free;
- `filesystem` and `sqlite` writes can't be blocked;
- kv isn't counted.

1. **Each non-`main` namespace is its own quota bucket,** keyed
   `.deployments/<escS>/<d>`. It is measured as the size of its root, so its kv
   file counts.
   - Pooling it with `(S, main)` would let a non-primary seed or bug push the
     primary over quota and block its writes, the opposite of an accident
     boundary.
   - `main`'s figures and blocks stay exactly as today.
2. **The limit is per deployment and defaults to the tile's (P22).**
   - The per-scope quota (`XBIN_LIMIT_DISK`, default 50 GiB,
     `diskmon.go:34`, `:65-70`) is the tile's limit and its ceiling.
   - A tile manager, in a human session, may set a lower disk limit for a
     deployment (05-model §4's `limits`; `11-contract.md` names the field).
   - A shared namespace `(S, d)` takes the lowest limit among its claimants'
     deployments named `d`.
   - No limit exceeds the per-scope quota. The per-tile and per-workspace
     deployment caps (`07-runtime.md`) bound the total.
3. **Low disk blocks non-primary namespaces first.** When free space is below
   the reserve:
   - every non-primary namespace with usage above 0 is write-blocked (kv and
     blob API writes, 507);
   - primary namespaces keep today's fair-share rule;
   - "non-primary" follows the role: `(S, main)` counts as non-primary when
     `P(S) ≠ main`.
   - Stopping non-primary backends that fill directly mounted volumes stays the
     admin's call, as today, but the alert names them first.
4. **Seed preflight refuses a seed that would break the limits.** It refuses when
   the primary namespace's size exceeds the target's limit, or when free space
   minus 110% of that size would fall below the reserve. New seeds are refused
   while the disk is low.
5. **Reporting is additive and keeps non-primary facts from readers.**
   - `TileDiskStatus` (`broker.go:724-731`) reports the addressed deployment's
     namespace, for `GET /tile-status`, whose audience §4.4 sets.
   - `ResourceUsage` (`resusage.go:62-76`) gains per-deployment rows with an
     additive `deployment` field. It is served only on admin surfaces
     (`GET /runtime`, `internal/boot/api.go:30-34`, `:63`).
   - Alerts for non-`main` namespaces carry the scope key in `tile`, as today,
     plus an additive `deployment`. They are never `System`, and reach admins
     only.
   - The workspace "write-blocked" count, a `System` alert that every tile's
     `/tile-status` shows (`broker.go:735-743`, `diskmon.go:154-157`), counts
     primary namespaces only, so no tile-visible figure reveals a non-primary
     deployment.

## 13. Tier-2 uids, VM backends and file resources

- **Tier-2 uids never meet deployment data.**
  - Per-scope uids apply only while xbind stays root with `--scope-uids`
    (`internal/boot/boot.go:354`, `:555-556`), and only on the non-isolated
    spawn path (`runner.go:464-468`).
  - Non-isolated mode is unsupported for pinned and non-primary backends
    (P18), so no non-`main` namespace is ever used under a scope uid.
    `uids.json` is unchanged, and `chownScopeData` (`uids.go:73-81`) is never
    applied to non-`main` directories.
  - Under tier 2 the only deployments that can exist are static-only ones, whose
    data the broker reaches.
  - The suspected tier-2 + encryption breakage (side finding #8) is orthogonal.
- **VM backends and file resources:**
  - `stopFirst` (`internal/runner/vm.go:128-133`) stops a VM backend's old
    generation before starting the new one, when it has file-backed resources.
    It stays **per deployment**, since blue/green generations of one deployment
    share its namespace. Two deployments of one tile never share a namespace,
    so no cross-deployment stop is needed.
  - Siblings with the same deployment name share `(S, d)`, exactly as `main`
    siblings share `(S, main)` today. That isn't new.
  - An online seed from a VM primary with file-backed resources is impossible
    (§8.4): the host-side copy can't see the guest's page cache or its WAL
    shared memory (`vm.go:124-127`).
  - A read-only bind into a VM guest would see a view that isn't coherent with a
    writer. That is one reason workspace-level `filesystem` and `sqlite` edges
    are `block` only (§5 item 6).
  - VM reservations of every deployment are charged to the tile
    (`Reserve(owner = tile)`, `internal/vm/policy.go:132`; 05-model §12).
    `07-runtime.md` owns the numbers.

## 14. Code shape, tests and docs

### 14.1 New files

New files, because `broker.go` is at 743/800, `runner.go` at 831/850 and
`auth.go` at 707/732 (`hack/size-budget.txt`):
- `internal/broker/deploydata.go`: the key function, the declared sets, the
  namespace handles, the resource remap;
- `internal/broker/deployns.go`: `ns.json`, holds and write gates, reset,
  delete, GC (14-implementation WP-41);
- `internal/broker/deployseed.go`: seed;
- `internal/broker/deployvault.go`;
- `internal/broker/backup_deploy.go`.

The broker learns each tile's deployments, primary, live reload target and
each deployment's code through a hook that the deployments plane
(`internal/deployments`) installs (for example
`DeploymentsOf(tile) (primary string, names []string)`), like its other hooks,
so no import cycle forms. `13-surfaces.md` owns the final placement.

### 14.2 Required tests

Input to `15-test-plan.md`:

| Area | Must hold |
|---|---|
| key function | `main` outputs byte-identical to today's (the §3.2 matrix); non-`main` outputs injective (property test over paths with `~`, `%`, `/` and names with `/`); `..` and NUL names refused in `main`, the full rule outside it; the ad-hoc-key guard test |
| tile keys | non-`main` vault, prefs, registration, log and archive keys use `<TK>`; two paths with a ground `CompKey` collision share none of them; `TestAddDeploymentDropsStaleFiles` |
| declarations (P22) | `TestProvisionFollowsCode`; `TestLiveTargetSaveProvisionsOnlyItsNamespace`; `TestMultiTileScopeDeclarationSource` (§6.7's table, the plain-directory row included); `TestCheckpointManifestReadNoFollow` (a symlinked `scope.json` in a checkpoint is refused, as a symlinked `xbin.json` is); the 64-resource cap for non-`main` |
| binds | `XBIN_RES_*` identical across deployments for resources both declare; no non-`main` spec binds from `main`'s volumes (§5 item 3); a VM export with `Src ≠ Dst` |
| dry runs | `TestDataDryRunCreatesNothing`: dry runs of add, seed, reset, restore and remove on a zero-state tile leave no namespace, `ns.json`, mount, kv file or checkpoint store |
| kv seed | consistent under concurrent writes; a byte-copied `main` value fails under the target label; bounded transactions; no write transaction on `kv.db` for a non-`main` target; decode errors propagate |
| sqlite seed | integration, with a namespace-mode primary writing: `integrity_check` passes |
| filesystem seed | symlinks, hard links and xattrs preserved; a symlink planted toward `data/vault/` isn't followed |
| single-tenant seed | refused online; after re-wrap the target opens only under its own label |
| divergent declarations | `TestSeedWithDivergentDeclarations`: shared resources copied, target-only empty, primary-only and type-mismatched skipped and listed |
| seed authority | `TestSeedManagerOnly` (terminal and agent tokens refused for a manager; an element holding `xbin:users` refused); `TestSharedScopeSeedNeedsEveryTile` |
| reset | an unmount failure aborts; nothing is removed while mounted; `TestSharedScopeResetNeedsEveryTile` |
| multi-tile scopes | refcounted deletion; `TestJoinSeededNamespaceManagerOnly` (`seeded`, `restored` and `partial`); reassignment refused; member creation refused while `P(S) ≠ main` |
| GC | a `scope.json` removed for a while deletes nothing inside the grace period |
| offload, restore | non-`main` non-lossy ciphertext removed, lossy kept; `main`'s offload byte-identical to today's; nothing removed when any PUT fails; replace for non-`main`, today's merge for `main` |
| zero state | `TestZeroStateBackupBytes`: a zero-state tile's main archive, with and without a leftover checkpoint store, is byte-identical to today's (modulo the manifest's timestamp) |
| downgrade | a main archive written with deployments restores on the previous release as a zero-state tile; a deployment archive is refused there |
| backup of record | with the primary reassigned to `dev`, a scheduled backup writes `dev`'s archive and lists it in the main archive; restoring the main archive restores both |
| restore validation | a deployment archive naming another tile is refused before any write; archived store config and hooks are never installed |
| bus | a non-primary publish never reaches a primary frontend (an admin's included) or the primary's push subscriptions |
| audience | `TestNonPrimaryLogsAudience`: a non-primary log and `tile-status` refuse the primary's frame principal; non-`main` disk alerts never reach `/tile-status` |
| vault | a non-primary backend can't read the primary's values; placeholders listed; the vault-copy audit names keys only; `TestProtectedPrimaryVaultWritesManagerOnly` |
| quota | non-primary usage never blocks the primary; low disk blocks non-primary namespaces first; `TestNamespaceLimitIsClaimantsMinimum` |
| AppArmor | on the QA box (Ubuntu with the installer's block), a non-`main` volume mounts under `.xbin/resenc/.deployments/` |

### 14.3 Zero-state security closures

Nothing in this document changes a tile without a record, except three
security closures. `12-compat.md` must list each in §1.3 and §10.1, and each
gets a `docs/changelog.md` line:
1. A `scope.json` resource name with a `..` segment or a NUL is refused in
   `(S, main)` (§3.2). It escapes the resource roots today.
2. Today's backup walk opens through `OpenBeneath` (§11.6). Archive bytes change
   only when an entry is replaced by a symlink mid-walk.
3. A restore refuses resource data an archive files under the workspace scope
   (manifest scope `""`; WP-39, added at its merge). Routed through the key
   function it would write the workspace-level buckets and volumes. No
   archive xbind writes holds such data.

### 14.4 Builder docs to update at implementation

Each with a `docs/changelog.md` entry:
- [/docs/resources.md](/docs/resources.md): per-deployment namespaces and
  declarations; ids and `XBIN_RES_*` unchanged;
- [/docs/overview/10-resources.md](/docs/overview/10-resources.md) and
  [/docs/overview/02-workspace.md](/docs/overview/02-workspace.md): the storage
  table;
- [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md): backups,
  per-deployment archives and restores, and the truth about offload (§9.4);
- [/docs/auth.md](/docs/auth.md): the vault per deployment;
- [/docs/protocol.md](/docs/protocol.md): new parameters, fields and
  endpoints.

## Divergences from the model

1. Resolved by 05-model §11: every backup archives the primary's data when the
   primary isn't `main`.
2. Resolved by 05-model §9: agent history stays per tile and records each
   session's target.
3. Resolved by 05-model §8–§9 and 12-compat rule C2: status is emitted by role; the bare
   entry is the primary's.
4. Resolved by 05-model §5 and P28: reassignment in v1 requires the workspace
   scope or a scope the tile roots alone.
5. Resolved by the glossary's **Reset** and 05-model §5: reset keeps the vault
   unless `vault: true`.
6. Resolved by 05-model §3: registrations live in
   `data/deployments/<TileKey>/<name>/`.
7. Resolved by 05-model §5 and P28: a removal deletes the namespace only with
   its last claimant.
8. Withdrawn: §9.4 now leaves `main`'s offload as today, so it needs no P5
   exception; the `main` fix is a separate change (NP-08-10).
9. **`<TileKey>` for non-`main` files inside existing store directories.**
   05-model §3 names `data/deployments`, `data/checkpoints` and
   `.xbin/deploy` as the new stores keyed by `<TileKey>`, and existing stores
   keep their keys. This document also keys the non-`main` files it adds inside
   existing directories by `<TileKey>`: vaults (`data/vault/.deployments/`),
   prefs (`data/prefs/<CK(user)>/.deployments/`) and archive keys. The reason
   is the same: a ground `CompKey` must not make two tiles share a secret or an
   archive. `main`'s keys don't change. The integrator should confirm this
   reading.
10. **Bus messages from a non-primary namespace keep type `bus`** (§4.3). Rule
    C2 lists `reload`, `build-*`, `status` and notify. A bus message is data:
    moving it to the `deployments` type would break a non-primary deployment's
    own `bus.on` handlers. Namespace matching gives it the audience 05-model
    §8 requires. The integrator should confirm that C2 doesn't reach data-plane
    events.

## New proposals

- **NP-08-1: the storage scheme.**
  - Non-`main` data lives under a `.deployments` level inside today's roots,
    keyed by the injective `escS` scope encoding and the deployment name.
  - Each non-`main` namespace has one bbolt file.
  - New tile-keyed files use `<TileKey>`: registration files under
    `data/deployments/<TK>/<d>/`, vaults, prefs, the log directory under
    `.xbin/deploy/<TK>/d/<d>/`, and archive keys.
  - Adding a deployment deletes stale per-deployment files for its name.
  - One key function (`resKeys`) computes every physical key, enforced by a
    guard test (§3).
- **NP-08-2: the addressed-deployment rule.**
  - Tile principals always reach their own deployment's data. A session
    reaches its P24 target's; an "API off" session reaches none.
  - Admins may pass `?deployment=` (on the vault routes, tile managers in a
    human session too, per `11-contract.md`); otherwise they reach the
    primary's data.
  - A tile principal passing `?deployment=` for another deployment gets 403
    (§4.1).
- **NP-08-3** — resolved by 05-model §9 (`res:workspace/*` is never split) and
  P23 (workspace-level `filesystem`/`sqlite` are `block` only).
- **NP-08-4: multi-tile scope rules.** Reassignment's precondition is resolved
  by 05-model §5, and acts on every claimant by P28. Still open:
  - creating a member tile through xbind is refused while `P(S) ≠ main`
    (§6.5);
  - joining a namespace in state `seeded`, `restored` or `partial` is a tile
    manager's act (§6.2).
- **NP-08-5** — resolved by 05-model §10 (the human-session manager gate).
- **NP-08-6: seeding modes.**
  - `online` is the default: kv under a per-namespace write gate, sqlite through
    the backup API in confine, `filesystem` via rsync in confine, blob
    in-process.
  - `stopped` is on request, and required for single-tenant volumes and for VM
    primaries or VM sandboxes with file resources.
  - `ns.json` records the mode (§8.3–§8.4).
- **NP-08-7** — resolved by 05-model §4 (data state derived from the
  namespace's own metadata).
- **NP-08-8: vault placeholders are computed.** They are derived at request time
  from the primary's key names, returned in an additive `placeholders` list, and
  never stored (§10).
- **NP-08-9: archive format.**
  - Main archives keep today's meaning and schema 1. For a tile with a record
    only, they add the `deployments` field and `deployments/` tar prefixes; a
    zero-state tile's archive is byte-identical.
  - Deployment archives are schema 2 under the key `.deployments.<TK>.<d>`,
    with `deployment` and `lossy` fields, mode and mtime headers, and separate
    retention.
  - When the primary isn't `main`, every backup writes the primary's
    deployment archive too.
  - Per-deployment backup, restore and schedule use new endpoints under
    `/api/xbin/deployments/…`; `POST /restore` gains no field.
  - Restores into a non-`main` namespace replace.
  - Switching `main`'s default restore to replace is recommended as a separate
    compat-reviewed change, since the docs already promise it (§11.4).
- **NP-08-10: offload and walk fixes gated on fidelity.**
  - In this design: deployment-archive walks open through
    `fsutil.OpenBeneath`, record mode and mtime, and report lossy resources;
    offload removes non-`main` non-lossy ciphertext after confirmed PUTs
    (§9.4).
  - As a zero-state security closure: today's backup walk opens through
    `OpenBeneath` (§11.6, §14.3).
  - As a separate change with its own decision, changelog entry and downgrade
    note: `main`'s offload fix, main-archive mode and mtime headers, and a
    `main` `lossy` field.
- **NP-08-11: resource names are validated.** In `(S, main)` the key function
  refuses a `..` segment or a NUL (a zero-state security closure). In every
  other namespace it also refuses empty names, a leading `/`, and `.` or empty
  segments. A checkpoint's `scope.json` is held to the same rules. Today a
  `scope.json` key like `../../../x` steers `MountEncrypted`'s `MkdirAll` and
  `gocryptfs -init` outside `data/resources-enc/` (`resenc_wire.go:139-144`,
  `resenc.go:84-91`, `:156-176`).
- **NP-08-12** — resolved by 05-model §8 and 12-compat rule C2 (the
  `deployments` event announces data changes, op `data`).
- **NP-08-13 (urgent, separate from this feature):** close the existing
  `ScopeKey` and `CompKey` collisions for `main`.
  - Today a user who may create a top-level tile can create `apps~x` with a
    `scope.json` declaring the same file resource name as scope `apps/x`. Both
    scopes then map to `.xbin/resenc/apps~x/<name>` under the same `fs:` label.
    The attacker's backend gets the victim's decrypted volume read-write:
    `util.go:127-132`, `resenc_wire.go:43`, `:94-100`, `:139-144`. Creation
    passes D82: `internal/broker/policy.go:156-174` checks reserved names,
    scope ownership and leftovers only.
  - `CompKey`'s 32-bit hash (`util.go:137-144`) can likewise be ground for paths
    longer than 24 characters. A colliding tile created before its victim
    stores a first secret then shares the victim's vault file, log and run dir:
    `pathLeftovers` refuses the path only once the vault file exists
    (`internal/broker/policy.go:250-252`).
  - **Fix:** bind each `ScopeKey` to the first scope path that initializes it,
    and refuse file-backed resources for any other scope mapping to the same
    key, with an admin alert. D82 creation also refuses a path whose `CompKey`
    or scope key equals an existing tile's, scope's or leftover's.
  - Non-`main` keys are collision-proof by construction: `escS` is injective
    and tile-keyed files use `<TileKey>` (§3.3).
- **NP-08-14: namespace GC.** Orphaned non-`main` namespaces are marked, listed
  to admins, and deleted after a 14-day grace period. `main`'s data is never
  deleted automatically. D82's leftovers list gains the record, the checkpoint
  store, non-`main` vaults, registration directories and namespaces at or under
  the path (§9.3).
- **NP-08-15: quotas.** P22 settles that limits are per deployment, default to
  the tile's and never exceed its ceilings. Still open: each non-`main`
  namespace is its own quota bucket, never pooled with the primary's; a shared
  namespace takes its claimants' lowest limit; low disk write-blocks
  non-primary namespaces first; seeds preflight size against the limit and the
  reserve (§12).
- **NP-08-16: declared sets per namespace (§6.7).** `D(S, d)` comes from the
  scope root tile's deployment `d` (its checkpoint, or the work tree while it
  is the live reload target), from the root's primary when the root has no `d`,
  and from the work tree for a plain-directory scope. A save provisions only
  the live reload target's namespace. A checkpoint's `scope.json` is read
  through `OpenBeneath`, and a non-`main` namespace takes at most 64 declared
  resources. A seed copies only what both sides declare.
