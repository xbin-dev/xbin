# 08 — Data: namespaces, seeding, secrets, backup

> Status: live — how every tile store is keyed per deployment, which data a request reaches, and how deployment data is seeded, reset, deleted, vaulted, quota'd and backed up (part of [plans/dev-lifecycle](README.md))

This document is the storage half of [05-model.md](05-model.md) (§3, §9, §11,
§12). It implements:
- **P6**: storage follows the deployment, and `main` owns today's keys forever;
- **P14**: non-primary data and vault start empty, and seeding and vault copy
  are tile-manager acts;
- the storage side of **P13**: dormant registrations and per-deployment status;
- the data side of **P18**: a non-`main` backend reaches its files only through
  sandbox binds.

Terms are [01-glossary.md](01-glossary.md)'s. Unless another file is named,
facts about today come from [research/data-plane.md](research/data-plane.md)
and were re-verified against this worktree. Wire shapes named here are
proposals; `11-contract.md` owns the final ones.

## 0. Decisions at a glance

1. **`main` keeps today's storage.** Its paths, buckets and labels stay
   byte-for-byte, whether or not `main` is primary. Nothing is ever migrated.
2. **Every other deployment gets its own data namespace.** Deployment `d` of a
   tile in scope `S` uses the data namespace `(S, d)`. It is a `.deployments`
   directory level inside today's roots, keyed by an injective encoding of the
   scope path. No existing key can produce it, and no older xbind computes it.
3. **`XBIN_RES_*` is identical in every deployment.** The broker binds the
   deployment's volumes at `main`'s paths (`Src ≠ Dst`). For everything reached
   over HTTP it maps (caller's deployment, canonical id) to a namespace.
4. **Workspace-level resources are never split.** `res:workspace/*` is always
   reached by an assigned grant, so it is an edge, and non-primary deployments
   reach it under the edge policy.
5. **Seeding replaces the target.** A seed is explicit, audited and a
   tile-manager act. It replaces the target namespace with a consistent copy of
   the primary's, re-keyed under the target's labels. It runs online by default,
   and stops the primary when no consistent online copy exists.
6. **The vault splits per deployment.** D30's backend-only read rule holds per
   deployment. Placeholders are computed, never stored.
7. **Backups keep today's archive and always cover the primary.** A component
   backup keeps today's archive and adds the deployment record and the
   checkpoint store. When the primary isn't `main`, the backup also archives
   the primary's data under its own key. Other non-primary data is opt-in.
   Every deployment archive is one that older xbinds refuse.
8. **Offload frees encrypted file-resource bytes.** Today it leaks them. It now
   frees them, but only for resources the archive holds without loss.
9. **Non-primary usage has its own quota.** It is quota'd per namespace, never
   pooled with the primary's, and write-blocked first when the disk runs low.

## 1. Terms this document adds

These refine glossary terms. They are not product vocabulary.

| Term | Meaning |
|---|---|
| **data namespace `(S, d)`** | The storage of scope `S`'s resources for deployment name `d`. `(S, main)` is today's storage. The deployment data of deployment `d` of tile `T` is `(scope(T), d)` plus `T`'s vault for `d`. |
| **canonical resource path** | The host path today's `EnvFor` hands a same-scope backend for a `filesystem` or `sqlite` resource: `main`'s mount `.xbin/resenc/<ScopeKey(S)>/<name>` (plus `/<name>.sqlite`). The phrase "the primary's resource paths" in 05-model §12 means these paths. They don't move when the primary is reassigned. |
| **addressed deployment** | The deployment whose data namespace and vault a data-plane request reaches (§4). |
| **scope primary name `P(S)`** | The primary's name of the tile at `S`'s root. It is `main` when the root is no tile (an S2 scope), for the workspace scope, and in the zero state. This is the same rule as `09-fabric.md`'s NP-09-7, and §6.5 keeps it the name every member serves. "The primary's data" (for seeding and for the read clamp) is `(S, P(S))`. |
| **namespace hold** | The state of a data namespace while it is seeded, reset, restored or deleted. Data-plane requests to it get 503 with `Retry-After`, and backends that address it don't start. |

## 2. Every store

"Today" is `main`'s key, which never changes. `<escS>` is the injective scope
encoding (§3.3). `<d>` is the deployment name. `<CK>` is `util.CompKey(tile)`
(`internal/util/util.go:137-144`).

| Store | Today (`main`, unchanged) | Non-`main` | Computed by | Notes |
|---|---|---|---|---|
| kv buckets | bucket `res:<scope>/<name>` (`workspace` for scope `""`) in the shared `data/kv.db` | the same bucket name in the namespace's own file `data/resources-enc/.deployments/<escS>/<d>/kv.db` | today: `resTarget.String()` (`internal/broker/broker.go:329-335`) via `kvAccess` (`internal/broker/resources.go:173-202`), plus three hand-built strings (`internal/broker/backup.go:146`, `:332`, `:482`); new: `resKeys` (§3.2) | One bbolt file per namespace: disk is freed on reset (bbolt files never shrink), namespace writes never take `kv.db`'s single writer lock, and older binaries never see it. No code enumerates `kv.db`'s top-level buckets; every use is a named lookup (`resources.go:212`, `:228`, `:268`, `:287`; `backup.go:179`, `:330`, `:479`; `internal/broker/resusage.go:151`). |
| kv encryption label | `kv:<bucket>`, i.e. HKDF info `xbin/kv:res:<scope>/<name>` (`internal/broker/resenc_wire.go:202-229`, `internal/vault/barrier.go:277-290`) | `kv:.deployments/<escS>/<d>/<bucket>` | `encodeKV`/`decodeKV` take the label from `resKeys` instead of building it from the bucket | A `main` value copied byte-for-byte into a non-`main` namespace fails to decrypt, so misplaced data fails closed. |
| file-backed ciphertext (`filesystem`, `sqlite`, `blob`) | `data/resources-enc/<ScopeKey(S)>/<name>/` (`internal/resenc/resenc.go:84-86`) | `data/resources-enc/.deployments/<escS>/<d>/fs/<name>/` | `resenc.Manager.CipherDir(dirKey, name)`, unchanged; `dirKey` comes from `resKeys` | `Ensure(resID, scopeKey, name, …)` already takes the label apart from the location (`resenc.go:132`). The manager needs no change. |
| decrypted mount | `.xbin/resenc/<ScopeKey(S)>/<name>/` (`resenc.go:89-91`) | `.xbin/resenc/.deployments/<escS>/<d>/fs/<name>/` | `resenc.Manager.MountDir`, unchanged | Must stay under `.xbin/resenc/**`, the only FUSE mount point D110's AppArmor rule allows (`deploy/install.sh:841-848`, `:909`). |
| fs password label | `fs:<ScopeKey(S)>/<name>` (`resLabel`, `resenc_wire.go:43`; `password`, `resenc.go:108-118`) | `fs:.deployments/<escS>/<d>/fs/<name>` | `resKeys` | Re-keying on seed follows (§8.3). |
| plaintext resource dir | `data/resources/<ScopeKey(S)>`, created only for `cron` resources and chowned under tier 2 (`resources.go:36-58`) | none | — | No plaintext file resource exists anywhere today (`resources.go:46-49`). |
| namespace metadata (new) | none | `data/resources-enc/.deployments/<escS>/<d>/ns.json` | the deployments service | State (`empty`, `seeding`, `seeded`, `partial`, `restored`, `orphaned`), provenance and history (§8). `main` gets a metadata-only `…/<escS>/main/ns.json` if it is ever seeded or reset while not primary; its data stays at today's keys. |
| bus topics | in-memory topic `res:<scope>/<name>/<topic>` (`resources.go:410-437`) | the same topic; the hub event carries `deployment: "<d>"` | `apiBusPublish` from the addressed deployment | Delivery matches by namespace (§4.3). The per-id counter (`resusage.go:182-185`) is keyed `id` for `main` and `id + "\x00" + d` otherwise. |
| vault | `data/vault/<CK>.json`, the whole map sealed with the DEK and no label (`internal/broker/vault.go:45-47`, `:79-111`) | `data/vault/.deployments/<CK>/<d>.json`, the same envelope | `vaultPath(comp, dep)` | A byte copy decrypts as-is. Copying is the vault-copy act (§10). Placeholders are computed (§10). |
| logs | `.xbin/log/<CK>.log` (`internal/runner/runner.go:470-473`; `internal/obs/logs.go:69`), documented in `docs/protocol.md:2400` and `docs/elements.md:529` | `.xbin/log/.deployments/<CK>/<d>.log` | the runner's log open; `obs` `/logs` | `GET /logs` without a deployment returns the primary's log (routing). `bx logs` reading the file directly (`docs/bx.md:368`) keeps reading `main`'s. |
| status | in-memory `statuses[<tile>]`, cleared on that tile's `build-start` (`internal/obs/status.go:129-144`) | keyed by the event component form: the bare `<tile>` is the **primary**, `<tile>+<name>` any other deployment | `obs` status plane | Status follows the role, like events (05-model §8). Reassigning the primary re-keys both entries atomically. Non-primary entries never appear in `GET /tile-report` or `status` events; they ride the `deployments` event (`11-contract.md`). See Divergences. |
| prefs | `data/prefs/<CK(user)>/<CK(comp)>.json` (`internal/obs/prefs.go:33-48`) | `data/prefs/<CK(user)>/.deployments/<CK(comp)>/<d>.json` | `prefsPath(p)`, from the principal's deployment | Prefs don't follow a reassignment of the primary. |
| agent history | `data/agent-history/<user>/<CK(tile)>/<id>.json` (`internal/term/history.go:27`, `:49-55`) | the same path; each entry records its session's target in an additive `deployment` field | `saveHistory` | Stays per tile. See Divergences. |
| cron jobs | `data/cron-jobs.json`, key `component\x00name` (`internal/broker/cron.go:76-78`, `:108`) | `data/deployments/<CK>/<d>/cron.json` | the self-scoped cron API | Never rows in today's file: an older xbind would fire them as `main`'s (05-model §3). Activation is `09-fabric.md`'s. |
| bus push subscriptions | `data/bus-subscriptions.json`, key `component\x00name` (`internal/broker/bussubs.go:158-162`), at most 64 per component (`bussubs.go:50`) | `data/deployments/<CK>/<d>/bus-subscriptions.json` | the self-scoped subscription API | As above. |
| interface instances | root `xbin.json` `ifaceInstances[<tile>]`, replaced wholesale (`internal/registry/registry.go:223`; `internal/broker/netfn.go:1110`) | `data/deployments/<CK>/<d>/iface-instances.json` | `apiIfaceInstancesSet` (`netfn.go:1037`) | Never active for non-primary deployments (05-model §7). |
| ingress hosts | root `xbin.json` `ingressHosts[<tile>]` (`registry.go:228`; `internal/broker/ingressfn.go:573`) | `data/deployments/<CK>/<d>/ingress-hosts.json` | `apiIngressHosts` (`ingressfn.go:491`) | As above. |
| backup schedule | `data/backup-schedule.json`, keyed by component (`internal/broker/backup_cron.go:19-27`, `:57-70`) | `data/deployments/<CK>/<d>/backup-schedule.json` | the backup schedule API | An older xbind would read a row with a `deployment` field as `main`'s schedule and prune `main`'s archive to its retention. |
| tile-managed sandboxes (D113, once built) | definitions in `data/sandboxes.json`; state in `.xbin/sbx/<CK>/<name>/` ([../tile-sandboxes.md](../tile-sandboxes.md) §1) | definitions in `data/deployments/<CK>/<d>/sandboxes.json`; state in `.xbin/sbx/.deployments/<CK>/<d>/<name>/` | D113's API, keyed by the calling backend's deployment | Resource mounts resolve in the deployment's namespace (§5). |
| archive key | `CompKey(tile)` (`backup.go:45`) | `.deployments.<CK>.<d>` | `backupKey(comp, dep)` | §11.3. |
| tier-2 uid | `.xbin/uids.json[scope]` (`internal/broker/uids.go:36-69`), used only on the non-isolated spawn path (`runner.go:453-469`) | none: never consulted | — | §13. |
| disk quota key | `ScopeKey(S)`, measuring `data/resources/<key>` and `data/resources-enc/<key>` (`broker.go:185-198`); kv is not counted | `.deployments/<escS>/<d>`, measuring the namespace root (kv included) | `scopeDiskUsage` and a namespace walker | §12. |

**Runtime artifacts** are owned by `07-runtime.md`: build output, run dir,
cgroup leaf, env layer and D112 registry entries.

**Unchanged and per tile (not per deployment):** the deployment record and
checkpoint store (05-model §3), PRs (`data/prs/<CK>`), the terminal layer
(`.xbin/term/<CK>`), owner and access rows, lifecycle state, grants and
bindings.

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
  vault/.deployments/<CK>/dev.json
  prefs/<CK(user)>/.deployments/<CK(comp)>/dev.json
  deployments/<CK>.json                    the deployment record (05-model §3)
  deployments/<CK>/dev/                    cron, bus-subscriptions, iface-instances,
                                           ingress-hosts, sandboxes, backup-schedule (.json)
.xbin/
  resenc/apps~crm/<name>/                  main mounts (unchanged)
  resenc/.deployments/apps~crm/dev/fs/<name>/   non-main mounts (inside D110's rule)
  log/.deployments/<CK>/dev.log
  sbx/.deployments/<CK>/dev/<sandbox>/     D113, once built
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
- **Resource names are validated for every namespace, `main` included.** A name
  must be non-empty and contain no NUL, no leading `/`, and no `.`, `..` or
  empty segment. Today nothing validates `scope.json` resource names. A name
  like `../../../x` reaches `MountEncrypted` → `Ensure`, whose `filepath.Join`
  resolves it outside `data/resources-enc/`. xbind then creates directories
  there and runs `gocryptfs -init` on a path that tile-writable data chose
  (`resenc_wire.go:139-144`, `resenc.go:156-176`). The key function refuses
  such names. Error text: `resource name "<n>" in <scope>/scope.json is not a
  plain relative path`.
- **The encoded scope has a length limit.** `escS` longer than 200 bytes is an
  error: that tile cannot have non-primary deployments, and the add operation
  says so.

**Every site that builds a physical key goes through the function:**
- the 11 `util.ScopeKey` call sites:
  - `broker.go:188`, `:730`;
  - `resources.go:38`, `:318`;
  - `resenc_wire.go:95`, `:119`, `:140`;
  - `backup.go:48`, `:312`;
  - `internal/broker/diskmon.go:243`;
  - `resusage.go:119`;
- the 3 hand-built bucket strings (`backup.go:146`, `:332`, `:482`);
- the direct `b.kv.db` uses listed in §2, which take the namespace's `*bolt.DB`;
- the re-derived `data/resources-enc` root (`broker.go:190`);
- the labels inside `encodeKV`/`decodeKV` (`resenc_wire.go:204`, `:223`).

A guard test in the style of `TestNoDirectExec` must refuse new
`util.ScopeKey(` or `"res:" +` uses in `internal/broker` outside
`deploydata.go`.

### 3.3 Why no key can collide

- **`util.ScopeKey` is not injective, so non-`main` keys never use it.**
  - It maps `/` to `~` (`util.go:127-132`), and `ComponentPathOK` allows `~`
    (`util.go:97-115`). So scope `apps~cal` and scope `apps/cal` share a key.
  - For `main` this is exploitable today; see NP-08-13.
  - Non-`main` keys use `escS` instead: replace `%` with `%25`, then `~` with
    `%7E`, then `/` with `~`.
  - The encoding is injective. After it, `%` appears only as `%25` or `%7E`, and
    `~` only for `/`.
  - It is readable: `apps/crm` becomes `apps~crm`, the same as `ScopeKey` in the
    common case, and `apps~crm` becomes `apps%7Ecrm`.
  - It is one path segment, never empty, and never starts with `.`.
- **The dot level cannot collide with a scope key.** No scope key starts with
  `.`: the registry skips dot directories (`internal/registry/registry.go:439-441`),
  and `ComponentPathOK` rejects dot segments (`util.go:109`). So `.deployments`
  directly under `data/resources-enc/` or `.xbin/resenc/` cannot be a scope key.
- **The same holds for `CompKey` roots.** No `CompKey` starts with `.`, so the
  `.deployments` level is equally safe under `data/vault/`, `.xbin/log/`,
  `data/prefs/<CK(user)>/` and `.xbin/sbx/`.
- **The dot level is never scanned.** `data/` and `.xbin/` are reserved
  top-level names (`util.go:67-70`) that the registry never scans
  (`registry.go:442-444`). Every walker that skips dot entries also stays out of
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

### 3.4 What an older xbind sees

| State | What an older binary does with it |
|---|---|
| `data/resources-enc/.deployments/…`, `.xbin/resenc/.deployments/…` | Never computes these paths. At boot, `RecoverStale` lazily unmounts any mount left under `.xbin/resenc/` (`resenc.go:270-278`), which is harmless. |
| non-`main` kv | Lives in its own files and never in `data/kv.db`. Old code does only named lookups of `res:` buckets. |
| non-`main` vaults | Live under `data/vault/.deployments/`. `migrateVaults` skips directories (`vault.go:123`), and `vaultPath` never yields them. |
| logs, prefs, D113 state | In dot-level subdirectories that old code never computes. |
| dormant registrations, non-primary backup schedules | In files under `data/deployments/`. They are never rows in `cron-jobs.json`, `bus-subscriptions.json`, the root `xbin.json` or `backup-schedule.json`. |
| `main` archives | Stay schema 1. New manifest fields are additive, and new tar prefixes are skipped by old `restore`, whose switch has no default case (`backup.go:242-280`). |
| deployment archives | Schema 2. Old readers refuse them with "upgrade to restore" (`internal/backup/backup.go:165-167`). |
| quotas | The old disk monitor measures only `ScopeKey` directories, so non-`main` usage goes unaccounted while downgraded. |

`12-compat.md` owns the downgrade proof. This table is the storage input to it.

### 3.5 AppArmor

D110's rule allows FUSE mounts only at `<ws>/.xbin/resenc/**/`
(`deploy/install.sh:841-848`). AppArmor's `**` matches across `/` and doesn't
exclude dot-prefixed components. Stock profiles rely on this when they write
`@{HOME}/**` and then deny `@{HOME}/.ssh/**` explicitly. That is an inference,
so the QA box (Ubuntu with the installer's block) must mount a non-`main` volume
before this ships (§14). Non-`main` mounts must never be placed outside
`.xbin/resenc/`.

### 3.6 Mounting and sealing

- **`main`'s volumes mount as today.** `MountEncrypted` mounts every declared
  `main` volume at provision, unseal and `cap:containers` changes
  (`resources.go:65`; `broker.go:219`, `:227`, `:664`).
- **Non-`main` volumes mount on first use:** a backend start of a deployment
  that addresses the namespace, or a blob request. They stay mounted until
  seal, reset, removal, orphaning or shutdown. Mounting them all eagerly would
  run one gocryptfs process per volume per deployment for nothing.
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

## 4. Which data a request reaches

### 4.1 The addressed deployment

The principal carries a deployment attribute. `07-runtime.md` and
`11-contract.md` define how instance, frame and terminal tokens set it. An
absent value means `main`, as a frame token without the claim does
(05-model §7).

| Caller | Addressed deployment |
|---|---|
| backend of `T+d` (instance token) | `d` |
| frontend document of `T+d` (frame token) | `d` (the claim; absent means `main`) |
| terminal or agent session of `T` | its target deployment |
| admin, or the owner token (`IsAdmin`); on the vault routes also tile managers (`11-contract.md`) | `?deployment=<name>` when given; otherwise the primary of the tile or scope addressed |
| a tile principal passing `?deployment=` naming another deployment | refused with 403. A tile principal can't address another deployment of its own tile (P12). |

Non-admin human sessions can't reach resources directly today: `allowRes`
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

Workspace-level resources are an edge for every tile, including tiles in the
workspace scope:
- **They are always reached by an assigned grant.** `sameScope` needs a
  non-empty scope (`broker.go:454-460`), so a `res:workspace/*` grant is never
  auto-granted.
- **Splitting them would create an unseeded namespace per deployment name.**
  Every `dev` of every workspace-scope tile would share it. No shipped workspace
  declares any workspace-level resource.
- **`EnvFor` treats them specially today.** For a workspace-scope tile it still
  hands a workspace-level `filesystem`/`sqlite` resource a path, because
  `"" == ""` (`resources.go:83-91`). §5 says what non-primary deployments get
  instead.

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
  field tells admin tools which one (`11-contract.md`). Non-admin sessions
  receive no bus events today (`busFilter` needs a component,
  `resources.go:441-444`), and that doesn't change.
- **Push delivery.** A publish in `(R, e)` goes only to active subscriptions
  whose addressed namespace for `R` is `(R, e)`. Which registrations are active
  is `09-fabric.md`'s.
- **In the zero state** only `main`'s namespace exists, so every rule reduces to
  today's.

## 5. Env vars and binds

1. **Values are identical.** `EnvFor` (`resources.go:71-145`) emits `main`'s
   values for every deployment:
   - the canonical resource path for same-scope `filesystem`/`sqlite`;
   - the canonical id for everything else.

   The values never depend on the deployment. Code that stores absolute paths
   therefore keeps working after a seed. The devbox podman store is the case in
   point: `--root` sits on `res:apps/devbox/storage`
   (`builtin-tiles/devbox/xbin.json`).
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
   *is* `main`'s data, which is why P18 refuses pinned and non-primary backends
   without `--isolate`. Static-only tiles need no binds: their data is reached
   through the broker.
6. **Workspace-level `filesystem` and `sqlite` for a non-primary deployment of
   a workspace-scope tile:** their edge is `block` only (`09-fabric.md`'s edge
   table).
   - There is no remap entry and no bind. The env var stays, so env is
     identical, and the path is simply absent in the sandbox.
   - A read-only bind is not a safe read. A WAL database's readers need `-shm`
     write access, and VM guests' caches aren't coherent with a writer
     (`vm.go:124-127`).
   - A later read-only value for `filesystem` alone would reuse the remap's
     read-only flag.
7. **Tile-managed sandboxes** (D113, once built): resource mounts resolve
   through the same remap, by the owning backend's deployment. In-sandbox paths
   are identical across deployments. `{"source":true}` is `07-runtime.md`'s.

## 6. Multi-tile scopes

No shipped scope has more than one member tile (research §1.3). The rules
exist for S2/S3 shapes and must not cost the common case anything.

1. **Keyed by `(scope, deployment name)`.** Siblings with a deployment named `d`
   share `(S, d)`. A sibling without `d` never touches it: its primary uses
   `(S, P(S))`.
   - A same-scope call from `apps/a+dev` to `apps/b` is an edge
     (`09-fabric.md`). Under `read` it reaches `apps/b`'s primary, which serves
     `(S, P(S))`. So `apps/a+dev` can read the scope's primary data through
     `apps/b`'s API, read-clamped.
   - The isolation of non-primary data is for **direct** access (kv, blob, bus,
     binds). The edge's `block` closes the API path, and the edge-policy UI
     must say what `read` exposes (`02-goals.md` Divergence 2).
2. **Joining a non-empty namespace is a manager act.** Adding `d` to a tile
   whose scope already has `(S, d)` in state `seeded` or `restored` gives that
   tile's writers reach into seeded data. It is a tile-manager act on the
   joining tile. Joining an `empty` namespace is ordinary (terminal level). The
   add response says whose data the new deployment joins (from `ns.json`).
3. **Scope-wide acts.** Seeding, resetting and restoring `(S, d)` stop every
   backend of every member tile with `d`. The actor needs the act's level (§8.1,
   §9.1) on **each** of those tiles. The response names any tile that blocks it.
4. **Deletion is refcounted by claimants.** The claimants of `(S, d)` are the
   member tiles of `S` whose deployment record lists `d`. The set is derived,
   never stored, so it can't drift. The namespace is deleted when the last
   claimant removes `d` (§9.2). Items 3 and 4 are `02-goals.md`'s NP-02-10.
5. **Primary reassignment is refused where it would split a scope (NP-08-4).**
   - Reassigning one member's primary would split the scope's primary data:
     `apps/a`'s primary on `(S, dev)`, `apps/b`'s on `(S, main)`. That breaks the
     documented same-scope sharing (`docs/resources.md`).
   - In v1, xbind refuses to reassign a tile's primary unless one of two things
     holds:
     - the tile is in the workspace scope, whose members never share a split
       namespace;
     - the tile roots its scope and is that scope's only member.
   - Creating a member tile inside a scope whose `P(S)` isn't `main` is refused,
     for the same reason.
   - Every member therefore serves `(S, P(S))`, with `P(S)` defined as in
     `09-fabric.md` NP-09-7: the scope-root tile's primary, or `main`.
6. **Scope membership comes from the work tree** (05-model §6). A `scope.json`
   that disappears for a while (an agent checking out an old branch) moves a
   tile's claim to another namespace. The grace period of namespace GC (§9.3)
   makes that harmless.

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
| bus push subscription, cron job on a foreign resource | stored dormant; whether activation re-checks the clamp is `09-fabric.md`'s | refused |
| cross-scope `filesystem`/`sqlite` | no env path, no bind (unchanged: `resources.go:92-94`) | same |
| workspace-level `filesystem` / `sqlite` (workspace-scope tile) | not offered: this edge is `block` only (§5.6) | no bind |

- **The error names the policy.** For example: `apps/email+dev may not write
  res:apps/calendar/bus: non-primary access is "read" (edge policy; a tile
  manager can change it)`. The exact text is `11-contract.md`'s.
- **The clamp is `min(granted role, reader)`.** Bus aliases normalize as in
  `roleSatisfies` (`broker.go:371-380`): `publisher` clamps to `subscriber`.
- **The canonical id is unchanged.** `examples/email+dev` reading
  `res:apps/calendar/bus` gets the calendar's primary bus. It never gets a
  namespace the calendar scope never seeded. `match` would route it to
  `(apps/calendar, dev)` later.

## 8. Seeding

A seed copies `(S, P(S))` into `(S, d)`, where `d ≠ P(S)`, **replacing**
whatever `(S, d)` held. Never seed through the backup tar: it drops symlinks
and races the running backend (`internal/backup/backup.go:127-130`; §11.6).

### 8.1 Who, when, and what the UI must say

- **Authority:** a tile manager (D24/D33) of every claimant tile of `(S, d)`
  (§6.3). It requires a human credential: session, device, app or owner token.
  - Terminal and agent tokens are refused even when their user is a manager,
    because the terminal's agents share those tokens (NP-08-5).
  - View-as sessions (D64) are refused.
- **Confirmation:** the request carries `11-contract.md`'s `confirm:
  "copy-data"` token. `bx deployment seed` asks, or takes `--yes`.
- **Required warning, from facts xbind computes:**
  1. It copies the primary's data **as of now**, which may include personal
     data: N MiB, in these resources.
  2. Everyone with `write` on each claimant tile, and their agents, can open
     `<tile>+<d>` and run any code there. **Protecting the primary doesn't cover
     the copy.**
  3. The copy stays until reset or removal, and opt-in backups of `<d>` send it
     to the archiver.
  4. Online or stopped (§8.4), and for how long the primary will be unavailable
     if stopped.
- **Audit:** a `slog` line `deployment data seeded` with tile, scope, from, to,
  by, bytes, resources and consistency. A history entry in `ns.json`. The
  tile-level `deployments` event (05-model §8) with a data change (NP-08-12).

### 8.2 The procedure

Operations on one namespace serialize (one lock per namespace). A second data
act on a held namespace answers `409 data busy` (`11-contract.md`). A seed:

1. **Authorize and preflight.**
   - The vault must be unsealed; kv and file volumes need keys.
   - gocryptfs must be available if `S` has file-backed resources.
   - The size of `(S, P(S))` must fit the target's quota and the free space
     above the reserve (§12).
   - Decide online or stopped (§8.4). If §8.4 requires a stopped seed and the
     request didn't ask for one (`stop: true`), refuse and say why, so the
     primary's outage is always a stated choice.
2. **Hold `(S, d)`.** Write `ns.json` state `seeding` with by, at and
   `from: P(S)`. Stop every backend, and every D113 sandbox, addressing
   `(S, d)`. Requests to `(S, d)` get 503 with `Retry-After`. With deliveries
   on, cron and bus deliveries fail meanwhile, at most once.
3. **Stopped mode only: hold `(S, P(S))` too.** Stop the primary's backends.
   Data-plane writes into it wait on its write gate (below). The primary's API
   answers 503 until step 7.
4. **Wipe `(S, d)`.** Use the reset steps (§9.1, steps 2–3). The fresh volumes
   `Ensure` creates are initialized under `d`'s labels, which is the re-key.
5. **Copy each resource** (§8.3), recording bytes and anything lost.
6. **Verify** (§8.5).
7. **Commit.** Write `ns.json` state `seeded`, with:
   - `from`;
   - `fromCheckpoint`: the primary's checkpoint, or `worktree` if it follows the
     work tree (helps rehearse migrations, flow F);
   - `at`, `by`, `consistency` (`online` or `stopped`);
   - per-resource bytes.

   Lift the holds.

The **write gate** of a namespace is an `RWMutex`:
- API-mediated writes (`apiKVPut`/`apiKVDelete`, `resources.go:249-298`;
  `apiBlobPut`/`apiBlobDelete`, `:366-406`) take it shared.
- A seed takes it exclusively, only while it reads that namespace.
- A write waiting more than 30 s gets 503 with `Retry-After`.
- Only that namespace's writes wait; every other scope and tile is untouched.

### 8.3 Per kind

- **kv (in-process, no tool).**
  1. Under `(S, P(S))`'s write gate, copy the raw, still-encrypted pairs of
     every kv bucket of `S` from the primary's store into a spool file in
     `(S, d)`'s root. Use bounded read transactions of at most 10,000 keys or
     4 MiB each, so no long read transaction holds up `kv.db`'s remaps.
  2. Lift the gate.
  3. Decode each value with the primary's label, encode it with `d`'s label,
     and write it into `(S, d)`'s store in bounded write transactions of at most
     1,000 keys or 8 MiB. Delete the spool.
  - All buckets of the scope are consistent with one another at one moment.
  - Don't reuse `loadKV`: it writes everything in one `Update` on the shared
    `kv.db` (`backup.go:330`), blocking every tile's kv writes.
  - Don't reuse `dumpKV` as-is: it discards the `View` error (`backup.go:179`),
    so a value that fails to decode at `:187-190` yields a silently truncated
    dump. The seed code must propagate every decode error.
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

### 8.4 Online or stopped

| Condition | Mode |
|---|---|
| default | **online**: kv consistent under the gate; each sqlite database point-in-time; `filesystem` and `blob` copied file by file, not point-in-time (a file written during the copy may be copied mid-write) |
| the manager asks (`stop: true`) | **stopped**: everything point-in-time across resources; the primary is down for the copy |
| `S` has a single-tenant volume | stopped, required |
| a backend addressing `(S, P(S))` is a VM with file-backed resources (the `stopFirst` condition, `vm.go:128-133`) | stopped, required. The host can't see a guest's cached pages or its WAL shared memory (`vm.go:124-127`). |
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

Reset empties a non-primary namespace. It is terminal-level (05-model §10), on
every claimant tile (§6.3), and carries `11-contract.md`'s `confirm:
"erase-data"`. Resetting `main` is a tile manager's act (`11-contract.md`).

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

Reset leaves the vault alone by default. With `vault: true` it also empties the
deployment's vault file, so every key shows as a placeholder again (§10; see
Divergences).

**Resetting `main` while it is not primary** deletes the data `main` served
when it was primary. The model allows it, as a tile manager's act. The UI must
say whose data it deletes.

### 9.2 Removing a deployment

05-model §5's "Remove deployment Y" deletes, in this order:

1. Mark `d` `removing` in the record, so nothing starts it. Stop it
   (`07-runtime.md`).
2. **Per-tile state**, deleted at once:
   - the vault file;
   - the log;
   - every user's prefs file for `(T, d)`;
   - the dormant-registration directory `data/deployments/<CK>/<d>/`;
   - D113 sandbox state;
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
  - the deployment record and checkpoint store (05-model §11);
  - non-`main` vault files;
  - dormant-registration directories;
  - every data namespace whose scope is at or under the path, orphaned or not.

  The path's current owner stays exempt (`policy.go:219-221`).
- **`main`'s own scope data** left by a removed scope-root tile isn't in the
  leftovers list today either (research/identity-resources.md §5). Fixing that
  changes zero-state behaviour, so it is outside this design.

### 9.4 The offload leak, fixed here

**Today:**
- `removeScopeData` drops the kv buckets and the plaintext
  `data/resources/<key>` (`backup.go:473-489`, side finding #3).
- `data/resources-enc/<key>` and the mounts stay. The docs' "archived +
  removed" (`docs/overview/14-lifecycle.md:33-34`) is untrue for `filesystem`,
  `sqlite` and `blob`.
- `MountEncrypted` then keeps the offloaded scope's volumes mounted.

**Why a naive fix loses data.** A restore after offload currently merges into
the surviving volume, so symlinks, empty directories, xattrs and modes survive
by accident. The archive can't hold them:
- `Tree` skips every non-regular file and never writes directories
  (`internal/backup/backup.go:124-129`);
- `writeFileFrom` ignores the recorded mode (`backup.go:356-371`).

Deleting the volume on offload would turn a disk leak into data loss. The
devbox container store is the extreme case.

**The fix:**

1. **The backup walk reports loss instead of skipping silently.** A file-backed
   resource is **lossy** if its walk meets:
   - a symlink, device, FIFO or socket;
   - an empty directory;
   - a file with more than one link, or with xattrs;
   - or if it is a single-tenant volume.

   The manifest lists lossy resources in an additive `lossy` field (§11.2).
2. **Headers carry mode and mtime, and new restores apply them.** `Tree`
   already writes permission bits into each header
   (`internal/backup/backup.go:139`). It also records the mtime, and new
   `restore` code applies both. Old binaries ignore them.
3. **After every archive PUT is confirmed, `removeScopeData` also removes the
   ciphertext of each resource that is not lossy.** Per resource:
   - `Unmount`, and check that it is unmounted;
   - `RemoveAll` the cipher directory and the mount directory.

   Lossy resources keep their ciphertext. The offload result and an admin alert
   name them ("kept 3.1 GiB of `storage`: the archive can't hold symlinks").
4. **`MountEncrypted` skips scopes whose root tile is offloaded.** Otherwise the
   next rescan re-initializes an empty volume for a removed one.
5. **The docs become true.** `/docs/overview/14-lifecycle.md` states the lossy
   exception.

This changes zero-state tiles' offload, which is a deliberate P5 exception (see
Divergences). With the fix, offload frees exactly the bytes the archive can
restore.

A later change can shrink the lossy set. It would record empty directories and
symlink targets in a sidecar tar entry that older restores ignore, and have new
restores recreate them inside the mount without following them. Until then the
rule stays strict.

## 10. The vault

- **One file per deployment.** `data/vault/.deployments/<CK>/<d>.json` uses the
  same envelope as `main`'s (`vault.go:34-37`, `:79-111`). `vaultPath` takes
  the addressed deployment (§4.1).
- **Who reaches which vault:**
  - a backend reaches its own deployment's vault;
  - a terminal reaches its target's;
  - frames still get nothing (`vault.go:167-170`);
  - admins reach the vault named by `?deployment=`, otherwise the tile's
    primary's (the admin console's password-manager function).
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
  into a non-primary deployment.
  - It is a tile-manager act with a human credential (NP-08-5).
  - It reads through `vaultRead` and writes through `vaultWrite`, both atomic
    (`vault.go:55-111`).
  - It overwrites the named keys and never goes toward the primary.
  - The audit line names keys, never values.
  - The warning says the values become readable by `<d>`'s backend, whose code
    any terminal-level user can deploy, including while the primary is
    protected.
- **Reset and removal.** Reset empties the vault only with `vault: true` (§9.1).
  Removal deletes the file.
- **Legacy migration.** `migrateVaults` (`vault.go:115-147`) must also walk
  `.deployments/`, so a non-`main` vault written as plaintext under
  `--insecure-vault` is encrypted when a barrier is first initialized. Older
  binaries skip it, which is harmless.
- **Reassignment preflight.** Before a manager confirms, it lists the target's
  placeholders: "`dev` has no value for 3 keys the current primary uses". Flow F
  otherwise cuts over to a backend without its secrets.
- **Barrier rekey** re-wraps the DEK only (`internal/vault/barrier.go:203`).
  Every deployment's vault is unaffected.

## 11. Backup, offload, restore

### 11.1 What an archive holds

| Content | Main archive (key `CompKey`, today's) | Deployment archive (key `.deployments.<CK>.<d>`) |
|---|---|---|
| work tree (`source/`), terminal layer (`term/`) | always, as today (`backup.go:60-108`) | never |
| `main`'s data (`data/…`), when the tile roots its scope | always, exactly as today | — |
| `main`'s cron jobs and bus subscriptions (manifest) | always, as today (`backup.go:79-86`); never another deployment's rows (`12-compat.md` PO-9) | — |
| deployment record, per-deployment registration files | always, under a new prefix | — |
| checkpoint store | always, under a new prefix | — |
| deployment `d`'s data `(S, d)` | — | always |
| any vault | never (`WithVault` is never set today, `internal/backup/backup.go:56`) | never |

A deployment archive is written in four cases:
- **The primary, when it isn't `main`: on every backup of the tile, manual or
  scheduled.** Otherwise, after flow F, the data actually served would be in no
  archive. The glossary counts "backups of record" among what only the primary
  receives. The main archive lists this archive's version in
  `deployments.archives`, so one restore brings both back.
- an explicit admin backup naming a non-primary deployment (opt-in);
- that deployment's own schedule (§2), which is admin-only, like today's
  (`backup_cron.go:123-158`);
- an offload, which is forced (§11.5).

The data of a tile that doesn't root its scope is in no archive, as today
(`backup.go:61`, `:474`).

### 11.2 Format additions

- **Manifest fields** (`internal/backup/backup.go:45-57`), all additive:
  - `deployment`: whose namespace `data/` holds. It is set only in deployment
    archives; absent means `main`.
  - `deployments`: `{record: bool, checkpoints: bool, archives: {"<d>": "<version>"}}`.
    `archives` lists the deployment archives written by the same backup or
    offload.
  - `lossy`: a list of resource names (§9.4).
- **Schema numbers.** Main archives stay `schema: 1`. Deployment archives are
  `schema: 2`, so older readers refuse them instead of restoring `d`'s data into
  `main`'s keys (`internal/backup/backup.go:165-167`). `Writer.Manifest` stops
  forcing the schema (`internal/backup/backup.go:74-81`); the caller sets it,
  and readers accept up to 2.
- **New tar prefixes**, which old `restore` skips:
  - `deployments/record.json`;
  - `deployments/registrations/<d>/<file>`;
  - `deployments/checkpoints/<path in the bare repository>`.

  Entry types stay regular files, so old binaries never meet a type they would
  write as an empty file.
- **The main archive means what it means today.** Its `data/` holds `main`'s
  namespace, and its cron/bus lists hold `main`'s rows. An older xbind
  restoring it gets `main` back as a zero-state tile, and refuses every
  deployment archive. `12-compat.md` NP-12-10 states the same obligations.

### 11.3 Archive keys and retention

- **Keys:**
  - `main`: `CompKey(tile)`, unchanged.
  - Deployment `d`: `.deployments.<CompKey>.<d>`. It can never equal a
    `CompKey`, because no `CompKey` starts with `.`. It is injective: `<d>`
    contains no `.`, so the last `.` splits it.
  - It uses only characters today's keys already use, in one path segment. The
    builtin s3 archiver lists by `<prefix>/<key>/`
    (`builtin-tiles/s3-archiver/backend/main.go:104-121`), so the keys never
    share a listing.
- **Retention is separate:**
  - `pruneVersions` (`backup_cron.go:97-118`) lists one key, so `main`'s
    retention never deletes another key's version, and the reverse.
  - The tile's schedule prunes the primary's deployment archive (when the
    primary isn't `main`) to the same count, separately.
  - Non-primary schedules carry their own retention. The proposed default keeps
    3 versions.
  - Offload-forced versions are pruned only by their own deployment's schedule.
  - Removing a deployment never deletes its archives, as removing a tile never
    does today.

### 11.4 Restore

- **Where data goes:** into the namespace named by the manifest's `deployment`
  (absent means `main`). If that deployment no longer exists, the data part is
  refused unless the admin names `into: "<name>"`. The response lists the
  choices.
- **Referenced archives.** Restoring a main archive also restores each archive
  its `deployments.archives` lists, by replace, into those of the named
  deployments that exist. The response lists any it skipped.
- **Replace or merge.** Today `restore` merges:
  - `loadKV` puts into existing buckets (`backup.go:330-353`);
  - `writeFileFrom` replaces file by file (`backup.go:356-371`);
  - so stale keys and files survive (side finding #4).

  The docs already say "a restore overwrites wholesale"
  (`docs/overview/14-lifecycle.md:167-168`). **Replace** means: for each
  resource in the archive that isn't lossy, empty it first, then write.
  - kv: delete the bucket or namespace file.
  - files: unmount, `RemoveAll`, then a fresh volume through `restoreFileDest`
    (`backup.go:311-320`).
  - Lossy resources always merge into their kept volume.
  - Restoring into a non-`main` namespace **always** replaces. That is the
    reset-from-archive use.
  - Restoring into `main` keeps today's merge by default, to keep P5, and takes
    an explicit `replace: true`. Switching `main`'s default is NP-08-9.
- **The record.** The archived record is installed only when the tile has none.
  Otherwise the current record wins. The archived checkpoint store's objects are
  merged in without moving any current ref: archived refs are kept under a
  restore-only namespace, so checkpoint GC keeps them. Restore never removes a
  deployment.
  - `07-runtime.md` owns the store's ref layout.
  - Restore runs confined git (D78).
  - It never restores the archived `config` or `hooks/`; xbind writes its own.
- **Registrations.**
  - The main archive's cron/bus lists restore into today's stores, as today
    (`backup.go:283-303`). They only ever hold `main`'s rows.
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
     (`backup.go:451-469`) now covers every namespace.
   - A manager who doesn't want non-primary data archived resets it first.
3. Only after every PUT is confirmed, remove local data of **every** namespace
   of the scope:
   - kv buckets or files;
   - non-lossy file volumes (§9.4).
4. **`offloaded-full`** also removes the terminal layer, the bulk of the work
   tree (`backup.go:493-512`) and the checkpoint store. The record stays, so the
   tile stays listed with its deployments.
5. **Re-enabling** restores the main archive, then each archive it lists.

### 11.6 Walking resource trees safely

`backup.Writer.Tree` checks each entry's type with `WalkDir` and then calls
`os.Open` on the path (`internal/backup/backup.go:109-139`). A running backend
can replace the entry with a symlink in between and make xbind archive any file
it can read (side finding #5).

The contract of `fsutil.OpenBeneath` says that daemon code reading
sandbox-written files "opens them through here, never os.Open"
(`internal/fsutil/beneath.go:31-34`). So every walk this design adds (the blob
seed, backups of non-primary namespaces) must open through `OpenBeneath(mount,
rel)` and report loss (§9.4). The same fix applies to today's backups, because
they share the code.

## 12. Quotas and low-disk blocks

Today `diskMon` works as follows (`diskmon.go:18-37`, `:114-166`):
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
2. **The limit is the existing per-scope quota** (`XBIN_LIMIT_DISK`, default
   50 GiB), applied per namespace. There is no new knob in v1. The per-tile and
   per-workspace deployment caps (`07-runtime.md`) bound the total.
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
   the primary namespace's size exceeds the target's quota, or when free space
   minus 110% of that size would fall below the reserve. New seeds are refused
   while the disk is low.
5. **Reporting is additive.**
   - `TileDiskStatus` (`broker.go:724-731`) reports the addressed deployment's
     namespace.
   - `ResourceUsage` (`resusage.go:62-76`) gains per-deployment rows with an
     additive `deployment` field.
   - Alerts for non-`main` namespaces carry the scope key in `tile`, as today,
     plus an additive `deployment`.

## 13. Tier-2 uids, VM backends and file resources

- **Tier-2 uids never meet deployment data.**
  - Per-scope uids apply only while xbind stays root with `--scope-uids`
    (`internal/boot/boot.go:354`, `:555-556`), and only on the non-isolated
    spawn path (`runner.go:464-468`).
  - Pinned and non-primary backends require isolation (P18), so no non-`main`
    namespace is ever used under a scope uid. `uids.json` is unchanged, and
    `chownScopeData` (`uids.go:73-81`) is never applied to non-`main`
    directories.
  - Under tier 2 the only deployments that can exist are static-only ones, whose
    data the broker reaches.
  - The suspected tier-2 + encryption breakage (side finding #8) is orthogonal.
- **VM backends and file resources:**
  - `stopFirst` (`vm.go:128-133`) stops a VM backend's old generation before
    starting the new one, when it has file-backed resources. It stays **per
    deployment**, since blue/green generations of one deployment share its
    namespace. Different deployments never share a namespace, so no cross-deployment
    stop is needed.
  - Siblings with the same deployment name share `(S, d)`, exactly as `main`
    siblings share `(S, main)` today. That isn't new.
  - An online seed from a VM primary with file-backed resources is impossible
    (§8.4): the host-side copy can't see the guest's page cache or its WAL
    shared memory (`vm.go:124-127`).
  - A read-only bind into a VM guest would see a view that isn't coherent with a
    writer. That is one reason workspace-level `filesystem` and `sqlite` edges
    are `block` only (§5.6).
  - VM reservations of every deployment are charged to the tile
    (`Reserve(owner = tile)`, `internal/vm/policy.go:132`; 05-model §12).
    `07-runtime.md` owns the numbers.

## 14. Code shape and required tests

**New files**, because `broker.go` is at 743/800, `runner.go` at 831/850 and
`auth.go` at 707/732:
- `internal/broker/deploydata.go`: the key function, namespace handles, write
  gates, the resource remap;
- `deployseed.go`: seed, reset, delete, GC;
- `deployvault.go`;
- `backup_deploy.go`.

The broker learns each tile's deployments and primary through an injected hook
(for example `DeploymentsOf(tile) (primary string, names []string)`), like its
other hooks, so no import cycle forms. `13-surfaces.md` owns the final
placement.

**Tests this document requires** (input to `15-test-plan.md`):

| Area | Must hold |
|---|---|
| key function | `main` outputs byte-identical to today's (the §3.2 matrix); non-`main` outputs injective (property test over paths with `~`, `%`, `/` and names with `/`); `..` names refused, `main` included; the ad-hoc-key guard test |
| binds | `XBIN_RES_*` identical across deployments; no non-`main` spec binds from `main`'s volumes (§5.3); a VM export with `Src ≠ Dst` |
| kv seed | consistent under concurrent writes; a byte-copied `main` value fails under the target label; bounded transactions; no write transaction on `kv.db` for a non-`main` target; decode errors propagate |
| sqlite seed | integration, with a namespace-mode primary writing: `integrity_check` passes |
| filesystem seed | symlinks, hard links and xattrs preserved; a symlink planted toward `data/vault/` isn't followed |
| single-tenant seed | refused online; after re-wrap the target opens only under its own label |
| reset | an unmount failure aborts; nothing is removed while mounted |
| multi-tile scopes | refcounted deletion; joining a seeded namespace needs a manager; reassignment refused |
| GC | a `scope.json` removed for a while deletes nothing inside the grace period |
| offload, restore | non-lossy ciphertext removed, lossy kept; nothing removed when any PUT fails; replace or merge as §11.4 says |
| downgrade | a main archive written with deployments restores on the previous release as a zero-state tile; a deployment archive is refused there |
| backup of record | with the primary reassigned to `dev`, a scheduled backup writes `dev`'s archive and lists it in the main archive; restoring the main archive restores both |
| bus | a non-primary publish never reaches a primary frontend (an admin's included) or the primary's push subscriptions |
| vault | a non-primary backend can't read the primary's values; placeholders listed; the vault-copy audit names keys only |
| quota | non-primary usage never blocks the primary; low disk blocks non-primary namespaces first |
| AppArmor | on the QA box (Ubuntu with the installer's block), a non-`main` volume mounts under `.xbin/resenc/.deployments/` |

**Builder docs to update at implementation** (with a `docs/changelog.md`
entry):
- [/docs/resources.md](/docs/resources.md): per-deployment namespaces; ids and
  `XBIN_RES_*` unchanged;
- [/docs/overview/10-resources.md](/docs/overview/10-resources.md) and
  [/docs/overview/02-workspace.md](/docs/overview/02-workspace.md): the storage
  table;
- [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md): backups,
  the offload fix, replace semantics;
- [/docs/auth.md](/docs/auth.md): the vault per deployment;
- [/docs/protocol.md](/docs/protocol.md): new parameters and fields.

## Divergences from the model

1. **The default backup must also cover the primary.**
   - 05-model §11 says a component backup always includes the record, the
     checkpoint store and "`main`'s data (today's backup)", and that
     non-primary data is opt-in. The glossary's **Primary** entry lists
     "backups of record" among what only the primary receives.
   - After flow F the primary is a non-`main` deployment. Its data, the data
     actually served, is then in no default backup. It isn't non-primary either,
     so the opt-in never covers it.
   - **Fix (additive):** keep §11's main archive exactly. When the primary isn't
     `main`, every backup also writes the primary's deployment archive and lists
     it in the main archive (§11.1). This keeps `12-compat.md` PO-9 and NP-12-10
     intact: the main archive holds only `main`'s data and rows.
2. **Agent history stays per tile.**
   - 05-model §9 keys agent history per deployment. But the history is a
     conversation about the work tree, which is one per tile
     (`internal/term/history.go:3-11`).
   - Splitting it would scatter one user's list of 20
     (`history.go:27`) across targets. Removing a deployment would delete
     conversations about code that still exists.
   - **Fix:** keep today's path and add a `deployment` field to `HistoryMeta`
     (`history.go:30-42`), so resume restores the target.
3. **Status follows the role, not the deployment.**
   - 05-model §9 lists status among stores "keyed per deployment", where "`main`
     keeps today's keys". But §8 sends the primary's events under the bare
     tile, and the status map is keyed by the event's component
     (`internal/obs/status.go:132-137`).
   - Keyed by deployment, a non-`main` primary's status would sit under
     `<tile>+<name>` while the shell reads the bare key.
   - **Fix:** bare means primary. Entries are re-keyed atomically on
     reassignment (§2).
4. **Independent primary reassignment splits a multi-tile scope.**
   - 05-model §5 lets any tile reassign its primary. In a scope with several
     member tiles, that leaves members on different namespaces of one scope
     (§6.5).
   - **Fix:** NP-08-4.
5. **Reset leaves the vault by default.**
   - The glossary defines deployment data as resources *and* vault entries, and
     "Reset" as emptying it. Read literally, a terminal-level reset would also
     wipe a deployment's own secrets.
   - Seed already excludes the vault (vault copy is separate), so by symmetry
     reset does too.
   - **Fix:** reset takes `vault: true` to include the vault (§9.1).
6. **Dormant registrations live per deployment.** 05-model §3's example path
   `data/deployments/<CompKey>/cron.json` is per tile. With several non-primary
   deployments the files are per deployment: `data/deployments/<CompKey>/<d>/`.
   This is a refinement, not a change of intent.
7. **Removing a deployment deletes its data namespace only if it was the last
   claimant.** 05-model §5 says removal deletes "its data namespace". In a
   multi-tile scope the namespace is shared, so deletion waits for the last
   claimant (§6.4, §9.2).
8. **The offload fix changes zero-state tiles (a P5 exception).** The brief asks
   for the offload leak to be fixed here. The fix changes what offload frees for
   tiles that never opt in. It stays inside what the docs already promise
   ("archived + removed", `docs/overview/14-lifecycle.md:33-34`), and it frees
   only what the archive holds without loss (§9.4). It needs explicit
   ratification as a P5 exception.

## New proposals

- **NP-08-1: the storage scheme.**
  - Non-`main` data lives under a `.deployments` level inside today's roots,
    keyed by the injective `escS` scope encoding and the deployment name.
  - Each non-`main` namespace has one bbolt file.
  - Per-deployment registration files live under `data/deployments/<CK>/<d>/`.
  - One key function (`resKeys`) computes every physical key, enforced by a
    guard test (§3).
- **NP-08-2: the addressed-deployment rule.**
  - Tile principals always reach their own deployment's data.
  - Admins may pass `?deployment=` (on the vault routes, tile managers too, per
    `11-contract.md`); otherwise they reach the primary's data.
  - A tile principal passing `?deployment=` for another deployment gets 403
    (§4.1).
- **NP-08-3: workspace-level resources are never split.** `res:workspace/*` is
  an edge for every tile, including tiles in the workspace scope.
  - `read` reaches `main`'s workspace data read-only for kv, blob and bus.
  - Workspace-level `filesystem` and `sqlite` are `block` only, as in
    `09-fabric.md`'s edge table (§4.2, §5.6).
- **NP-08-4: multi-tile scope rules.**
  - v1 refuses to reassign a tile's primary unless the tile is in the workspace
    scope, or roots its scope as its only member.
  - Creating a member tile is refused while `P(S) ≠ main`.
  - Joining a non-empty `(S, d)` is a tile-manager act.
  - Seed, reset and restore need the act's level on every claimant tile (§6).
- **NP-08-5: manager data acts need a human credential.** Seed, vault copy, and
  non-primary backup and restore require a session, device, app or owner token.
  Terminal and agent tokens are refused even when their user is a manager,
  because agents share them (§8.1).
- **NP-08-6: seeding modes.**
  - `online` is the default: kv under a per-namespace write gate, sqlite through
    the backup API in confine, `filesystem` via rsync in confine, blob
    in-process.
  - `stopped` is on request, and required for single-tenant volumes and for VM
    primaries or VM sandboxes with file resources.
  - `ns.json` records the mode (§8.3–§8.4).
- **NP-08-7: namespace metadata is authoritative.** The state and provenance of
  `(S, d)` live in its `ns.json`. The record's per-deployment `data` object
  (05-model §4) is derived when the record is read, never stored, because
  siblings share the namespace.
- **NP-08-8: vault placeholders are computed.** They are derived at request time
  from the primary's key names, returned in an additive `placeholders` list, and
  never stored (§10).
- **NP-08-9: archive format.**
  - Main archives keep today's meaning and schema 1. They add the `deployments`
    and `lossy` fields and `deployments/` tar prefixes.
  - Deployment archives are schema 2 under the key `.deployments.<CK>.<d>`,
    with a `deployment` field and separate retention.
  - When the primary isn't `main`, every backup writes the primary's
    deployment archive too.
  - Restores into a non-`main` namespace replace.
  - Switching `main`'s default restore to replace is recommended as a separate
    compat-reviewed change, since the docs already promise it (§11).
- **NP-08-10: the offload fix is gated on fidelity.**
  - The backup walk opens through `fsutil.OpenBeneath` and reports lossy
    resources.
  - Headers carry mode and mtime, and new restores apply them.
  - Offload removes the ciphertext of non-lossy resources after confirmed PUTs.
  - `MountEncrypted` skips offloaded scopes (§9.4, §11.6).
- **NP-08-11: resource names are validated for every namespace.** The key
  function refuses resource names with `.`/`..`/empty segments, a leading `/` or
  NUL. Today a `scope.json` key like `../../../x` steers `MountEncrypted`'s
  `MkdirAll` and `gocryptfs -init` outside `data/resources-enc/`
  (`resenc_wire.go:139-144`, `resenc.go:84-91`, `:156-176`).
- **NP-08-12: data changes are announced.** The tile-level `deployments` event
  also announces data changes (seeded, reset, restored, vault copied), which
  05-model §8's list omits. `11-contract.md` already carries this as op
  `data`.
- **NP-08-13 (urgent, separate from this feature):** close the existing
  `ScopeKey` and `CompKey` collisions for `main`.
  - Today a user who may create a top-level tile can create `apps~x` with a
    `scope.json` declaring the same file resource name as scope `apps/x`. Both
    scopes then map to `.xbin/resenc/apps~x/<name>` under the same `fs:` label.
    The attacker's backend gets the victim's decrypted volume read-write:
    `util.go:127-132`, `resenc_wire.go:43`, `:94-100`, `:139-144`. Creation
    passes D82: `policy.go:156-174` checks reserved names, scope ownership and
    leftovers only.
  - `CompKey`'s 32-bit hash (`util.go:137-144`) can likewise be ground for paths
    longer than 24 characters. A colliding tile created before its victim
    stores a first secret then shares the victim's vault file, log and run dir:
    `pathLeftovers` refuses the path only once the vault file exists
    (`policy.go:250-252`).
  - **Fix:** bind each `ScopeKey` to the first scope path that initializes it,
    and refuse file-backed resources for any other scope mapping to the same
    key, with an admin alert. D82 creation also refuses a path whose `CompKey`
    or scope key equals an existing tile's, scope's or leftover's.
  - Non-`main` keys are collision-proof by construction (§3.3).
- **NP-08-14: namespace GC.** Orphaned non-`main` namespaces are marked, listed
  to admins, and deleted after a 14-day grace period. `main`'s data is never
  deleted automatically. D82's leftovers list gains the record, the checkpoint
  store, non-`main` vaults, registration directories and namespaces at or under
  the path (§9.3).
- **NP-08-15: quotas.** Each non-`main` namespace has its own quota bucket at
  the existing per-scope limit, never pooled with the primary's. Low disk
  write-blocks non-primary namespaces first. Seeds preflight size against quota
  and reserve (§12).
