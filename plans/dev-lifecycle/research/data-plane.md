# Scopes and the data plane: forking tile data per deployment

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `data`.

## Report

## Scope semantics and the data plane: can a tile's state be forked per deployment?

### Summary
- **Where resource data is keyed.** All resource data is keyed by `(scope, name)` and reaches disk through a few chokepoints:
  - `util.ScopeKey` (internal/util/util.go:127)
  - `resTarget.String()` (internal/broker/broker.go:329)
  - `resLabel` (internal/broker/resenc_wire.go:43)
  - `resenc.Manager.CipherDir`/`MountDir` (internal/resenc/resenc.go:84-91)
- **kv, sqlite, filesystem and blob can be forked.** The fork adds a deployment dimension to the physical key and keeps the canonical id `res:<scope>/<name>`, which tile code hard-codes.
- **The broker can't tell which deployment is calling.** An instance token maps to a component path and nothing else (internal/auth/auth.go:445-462, 571-572, 589-590). Every data-plane handler needs a deployment attribute on `auth.Principal`, with `""` meaning primary.
- **Some stores need namespacing, not copying.** These are keyed by component path alone, so a second deployment would overwrite or clear the primary's entries:
  - registration stores: cron (cron.go:108), bus subscriptions (bussubs.go:158), iface-instances and ingress-hosts (in the root xbin.json);
  - runtime state: status (obs/status.go:126-143), logs, env layer, build output, run dir, cgroup leaf.
- **Encryption binds data to its name.** kv values use a key derived from `kv:<bucket>`, and gocryptfs passwords one derived from `fs:<scopeKey>/<name>`. A byte copy into a renamed namespace won't decrypt unless kv is re-encoded, or the gocryptfs volume is re-wrapped or keeps its label.
- **No shipped tile is in a multi-component scope.** Outside devws there are 22 shipped components: 9 are single-component scope roots and 13 sit in the workspace scope. So for everything shipped, `(scope, deployment)` is the same as `(tile, deployment)`.
- **The primary can keep today's keys forever, on two conditions:**
  - storage identity follows the deployment, with the implicit default deployment owning the legacy keys, rather than following the "primary" role;
  - non-primary keys live in namespaces that can't collide, such as a dot-prefixed directory level.
- **No existing code deletes encrypted resource data.** Offload leaves ciphertext behind (backup.go:471-489).
- **The backup path is a poor base for seeding.** It is lossy for filesystem resources and not consistent for live tiles.

### 1. Scopes

#### 1.1 scope.json and resolution
- **What scope.json holds.**
  - `ScopeManifest{resources, importMap}` (registry.go:187-191); `Resource.type` is filesystem, sqlite, kv, blob, bus or cron (registry.go:182-185).
  - A `scope.json` that fails to parse still makes a scope, with no resources (registry.go:447-454).
  - The scan skips dot-prefixed dirs, `IgnoredDirs` and `ReservedTop` (registry.go:439-445).
- **Scope membership.**
  - `nearestScope` (registry.go:499-508) walks up from the component's own path. A component whose dir holds scope.json is its own scope root; `""` is the workspace scope. The result is stored in `Component.Scope` at rescan (registry.go:488-491).
  - A second walker, `scopeAt` (policy.go:177-188), is used for tile creation.
- **Resource targets.** `parseRes` (broker.go:337-366) resolves them:
  - `res:workspace/<name>` goes to the root xbin.json `resources` (registry.go:211-212).
  - Otherwise the longest declared scope prefix wins; the remainder is the name, and may contain `/`.
- **Other scope.json effects.** The scope `importMap` is merged over the workspace map (registry.go:565-582).
- **Where manifests are read from.** xbin.json and scope.json always come from the workspace tree and are never overlaid (docs/maintenance.md:155-158; server/static.go:208; server/tileassets.go:149).
- **Who creates scopes.** Scaffolding never writes scope.json (no matches in internal/scaffold). Scopes come from hand edits, imports, templates and builtins.

#### 1.2 ND5, scope-level resources, D82
- **Same-scope auto-grants (ND5).** ND5 is at DECISIONS.md:286-288. In code, `grantedRole` (broker.go:412-452) checks in this order:
  1. the policy ceiling (419-421);
  2. explicit grant rows;
  3. the http-binding role (434-437);
  4. a `uses` entry, which is its own grant when `sameScope` holds (438-450).
- **Workspace-scope tiles never auto-grant.** `sameScope` needs a non-empty scope (broker.go:454-460). Same-scope targets are exempt from `mayCall` ceilings (policy.go:49-61).
- **How scope resources reach a backend (`EnvFor`, resources.go:68-98).**
  - filesystem/sqlite: a path, only when `rt.Scope == c.Scope`. This also matches `"" == ""` for an explicitly granted workspace-level resource.
  - Cross-scope filesystem/sqlite: nothing.
  - kv, blob, bus and cron: the canonical id.
- **Workspace-level resources.** No shipped workspace declares any.
- **The D82 trust boundary.** D82 is at DECISIONS.md:2134-2178.
  - A non-admin may create inside a scope only if the new owner owns the scope (`newTilePathOK` → `scopeAt` + `scopeOwnedBy`, policy.go:141-213). Ownership means the scope root's owner entry, or else every tile already in it. The reason: a new member inherits auto-grants.
  - Tiles never nest: `guardNewComponentTree` (policy.go:289-306) is applied by create (create.go:39-43) and clone (clone.go:71-80).
  - State left behind under a path blocks re-creation (`pathLeftovers`, policy.go:214-257).

#### 1.3 Shipped shapes and counts
| tree | components | single-component scope roots | multi-component | workspace scope |
|---|---|---|---|---|
| workspace-template | 7 | 0 | 0 | 7 |
| builtin-tiles | 8 | 6: devbox (fs), egress-approver (kv), llm-gw (kv), s3-archiver (kv), traefik (fs), webhooks (kv) | 0 | 2 (chat, prometheus-viewer) |
| builtin-templates | 3 | 2: agent (sqlite, cron, bus, blob), agent-messaging-bridge (kv) | 0 | 1 (starter) |
| examples | 4 | 1: calendar (kv, bus, cron) | 0 | 3 |
| devws | 7 | 1: apps/bx-term-tile (sqlite) | 0 | 6 |

- **devws.1–devws.30** hold 17 scanned scope.json files, and each scope contains exactly one component.
- **The only shipped cross-scope resource use** is examples/email reading `res:apps/calendar/bus` (examples/email/xbin.json:6-7).
- **Multi-component shapes exist only in tests and docs.**
  - S2, a plain-directory root: scope.json only, with component subdirectories. It is tested (policy_test.go:458-466) and create/clone can build it for the scope's owner.
  - S3, nested components: documented (`apps/calendar/widgets/month-view`, docs/overview/01-model.md; docs/elements.md "Children are just subdirectories"), but the APIs refuse nesting, so it can only be made by hand.
- **Where scope.json lives.** Every component dir is its own git repo (`gitInitComponent`, code.go:96-108). So in every shipped scope, scope.json sits inside the tile's repo and a branch can change the resource set. In S2 it belongs to no tile repo.
- **Backup gap.** S2 data is in no component backup and is never offloaded: both require `c.Path` to be a scope root (backup.go:61, 474).

### 2. Resource provisioning

#### 2.1 Where keys are computed
- **`util.ScopeKey`** (util.go:125-132) maps `""` to `workspace` and `/` to `~`. It is not injective:
  - scope `apps~cal` collides with `apps/cal`;
  - a scope at path `workspace` collides with workspace-level resources.
  `ComponentPathOK` allows `~` and `@` (util.go:95-114). New-tile rules add only `:` and reserved segments (policy.go:156-167).
- **`util.CompKey`** (util.go:134-144) is the first 24 chars of the path with `~` for `/`, then `-` and 8 hex of sha256. It keeps unix socket paths under 108 bytes.
- **Computed in one place:**
  - `nearestScope`, `parseRes`, `resTarget.String`, `resLabel`
  - `CipherDir`/`MountDir`/`mkey` (resenc.go:81-91), `fsResPath` (resenc_wire.go:94-100), `EnvFor`
  - `vaultPath` (vault.go:45-47), `uidFor` (uids.go:54-69), `prefsPath` (obs/prefs.go:45-48)
  - `jobKey` (cron.go:108), `subKey` (bussubs.go:158)
  - `backupKey`/`resourcesRoot`/`termDir` (backup.go:45-53), `envLayerDir` (env.go:30-35)
- **Computed in many places:**
  - `util.ScopeKey` has 11 call sites: resources.go:38, 318; resenc_wire.go:95, 119, 140; backup.go:48, 312; broker.go:188, 730; diskmon.go:243; resusage.go:119.
  - kv bucket names come from `rt.String()` (resources.go:192; resusage.go:66-67) and also from three hand-built `"res:"+scope+"/"+name` strings (backup.go:146, 332, 482).
  - `b.kv.db` is used directly in resources.go, backup.go and resusage.go.
  - The `data/resources-enc` root is re-derived in broker.go:190.
  - `util.CompKey` builds runtime paths at about 25 sites: runner.go:344/366/408/465/481/483/603, env.go:34/89/135, build.go:43, stats.go, inspect.go, obs/logs.go:69, cmd/bx/main.go:511, obs/prefs.go:47, prs.go:87, term/history.go:54, vault.go:46, ingress/forwards.go:39, boot.go:431.

#### 2.2 kv
- **Storage.** One bbolt file, `data/kv.db` (resources.go:157-169), opened at `broker.New` (broker.go:157).
- **Buckets.**
  - Each resource gets a bucket named by its canonical id (`kvAccess`, resources.go:171-202), created on the first PUT (268-274).
  - kv.db holds nothing but these; bbolt buckets are only created at resources.go:269 and backup.go:333. Every bucket name starts with `res:`.
- **Values.**
  - Stored as a 1-byte tag plus AES-256-GCM under HKDF(DEK, `xbin/kv:<bucket>`) (resenc_wire.go:189-229; barrier.go:270-311).
  - Key names are plaintext; values are limited to 1 MiB (resources.go:258).

#### 2.3 sqlite, filesystem, blob
- **All three are per-resource gocryptfs volumes.** Provision makes no plaintext dir for them (resources.go:46-49). The only dir it creates, `data/resources/<scopeKey>`, is for `cron` and stays empty (42-45).
- **sqlite.** A filesystem volume whose `XBIN_RES` points at `<mount>/<name>.sqlite` (resenc_wire.go:94-100).
- **blob.** Never bound into a sandbox. xbind reads and writes it itself in `blobAccess` (resources.go:300-338) and refuses until the volume is mounted (315-321).
- **How filesystem/sqlite reach the backend.** `resourceBinds` (runner/binds.go:17-54) derives read-write binds from the absolute `XBIN_RES` values, always with `Src == Dst` (binds.go:51).

#### 2.4 Encrypted mounts (resenc, D43–D46)
- **Layout.** Keyed by `(scopeKey, name)`:
  - ciphertext at `data/resources-enc/<scopeKey>/<name>` (resenc.go:83-86);
  - decrypted mount at `.xbin/resenc/<scopeKey>/<name>` (88-91).
- **Keys.**
  - Each resource's password is base64(HKDF(DEK, `xbin/fs:<scopeKey>/<name>`)) (resenc_wire.go:42-43; resenc.go:106-118; barrier.go:277-289).
  - Each volume runs `-init` on first use (resenc.go:172-176).
  - `Ensure(resID, scopeKey, name, …)` already takes the key label separately from the location (resenc.go:132).
- **Single-tenant mode.** Used for filesystem resources of scopes with a cap:containers tile (resenc_wire.go:45-65; D43). It stores virtual ownership in encrypted xattrs, and switching mode remounts (resenc.go:142-155; broker.go:660-665). Writeback is opt-in (D45/D46).
- **Lifecycle.**
  - `RecoverStale` runs at `New` (resenc_wire.go:22-29).
  - `MountEncrypted` runs at every Provision and unseal (resources.go:65; broker.go:214-230; resenc_wire.go:131-145).
  - `EncryptionHold` gates spawning (resenc_wire.go:102-129; boot.go:506-508).
  - Sealing calls `SealResources`, which stops users and then unmounts everything (resenc_wire.go:147-162).
  - `Manager.Unmount` (resenc.go:242-253) has no caller.
- **Placement constraint.** The installer's AppArmor rule allows FUSE mounts only under `<ws>/.xbin/resenc/**/` (deploy/install.sh:812-813, 909; resenc.go:199-226).

#### 2.5 Per-scope uids (tier 2)
- **When it applies.** `--scope-uids` (boot/config.go:41), and only while xbind stays root (boot.go:164-170, 544-546). It is used only on the non-isolated spawn path (runner.go:446-461).
- **The map.** `.xbin/uids.json` maps scope path to uid, starting at 20000; `""` is stored as `"\x00workspace"` (uids.go:15-69).
- **Ownership.** `chownScopeData` chowns only `data/resources/<scopeKey>` (uids.go:71-81).
- **Probably broken with encryption (inference, not verified).** Normal-mode mounts have no allow_other (resenc.go:177-181), so a backend running under a scope uid likely can't open them.

#### 2.6 XBIN_RES_* at spawn
- **Where the env comes from.** `runner.start` builds it from `backendEnv` plus `XBIN_SOCKET`/`COMPONENT`/`GATEWAY`/`TOKEN` plus `EnvForComponent(c)` (runner.go:415-425), which is `Broker.EnvFor` (boot.go:494).
- **What EnvFor does.** It walks the working-tree `Manifest.Uses`, calls `parseRes`, checks `grantedRole`, and emits `XBIN_RES_<NAME>` (resources.go:71-98, 146-153). The binds follow from those env values (runner.go:634-636).
- **Grant changes restart the backend** (broker.go:643-666).
- **No deployment parameter anywhere.** Every runner hook takes only `*registry.Component` (runner.go:89-151; boot.go:494, 604-610).

#### 2.7 Quota, usage, low-disk
- **Accounting.** `diskMon` works per scope key: 50 GiB by default, 10% reserve, a scan every 45 s (diskmon.go:18-37, 114-166).
- **What counts.** Usage is `data/resources/<key>` plus `data/resources-enc/<key>` (broker.go:177-198). kv.db is not counted.
- **Write blocks.** `quotaOK` returns 507, but only for kv and blob writes (diskmon.go:237-248; resources.go:189, 312). filesystem/sqlite writes can't be blocked (diskmon.go:29-31).
- **Admin view.** `ResourceUsage` is keyed by resource id with a 30 s cache (resusage.go:29-131).

#### 2.8 The bus
- **Publishing.** In memory. `apiBusPublish` emits `{type:"bus", topic:"res:<scope>/<name>/<topic>"}`, fans out to push subscriptions, and bumps a per-id counter (resources.go:408-437; resusage.go:180-192).
- **Delivery.** Over WebSocket, admins get every bus event; everyone else must pass `busFilter`, a reader-grant check by component (server.go:592-611; resources.go:439-458).
- **No deployment tag.** `events.Event` has no deployment field (events.go:11-17).

### 3. Other per-tile state
| state | storage and key | deployment note |
|---|---|---|
| Vault (D30) | `data/vault/<CompKey>.json`, the whole map sealed with the DEK directly (`barrier.Encrypt`, no label) as `{"enc":1,"data"}` (vault.go:22-111; barrier.go:250-268). Barrier file: `data/vault/.barrier.json` (barrier.go:66-102). Values readable only when `p.Component==comp && Via=="instance"` (vault.go:188); frames refused (167-170) | a file copy decrypts as-is; sharing is a policy call |
| Logs | `.xbin/log/<CompKey>.log`: every generation plus env setup (runner.go:464-471; env.go:88-90); served by obs/logs.go:69 and `bx logs` (cmd/bx/main.go:511) | single file per tile |
| Status reports | in-memory map by component, cleared on that component's `build-start` (obs/status.go:26-143) | a dev build would clear the primary's status |
| Prefs | `data/prefs/<CompKey(user)>/<CompKey(comp)>.json`, keyed by principal (obs/prefs.go:15-48) | shared unless split |
| Agent history | `data/agent-history/<user>/<CompKey(cwd)>/` (term/history.go:3-11, 49-55) | owner plane; stays per tile |
| PRs | `data/prs/<CompKey(target)>` (prs.go:86-87) | code plane; stays per tile |
| Cron | `data/cron-jobs.json`, key `component\x00name`; ticks go through the proxy to `/api/<comp><path>`; elements register only themselves (cron.go:24-255) | agent template registers at start (agent/_backend/schedule.go:184, owner.go:196) |
| Bus subscriptions | `data/bus-subscriptions.json`, key `component\x00name`, at most 64 per component, grant rechecked on every delivery (bussubs.go:26-362) | agent template subscribes at start (triggers.go:456) |
| iface-instances | root xbin.json `ifaceInstances[provider]`, replaced wholesale; re-registering restarts bound requesters (registry.go:218-223; netfn.go:1037-1125) | per tile |
| ingress-hosts | root xbin.json `ingressHosts[comp]`, replaced wholesale, zone-bounded (registry.go:224-228; ingressfn.go:491-583) | per tile |
| Terminal layer | `.xbin/term/<CompKey>`, plus `vm/disk.img` for VM terminals (term/term.go:481-487; vm/disk.go:9-37) | stays per tile |

### 4. Backups, offload, restore (LC-1..5)
- **What a backup includes (`writeBackup`, backup.go:57-108):**
  - `backup.json`;
  - `source/`, keeping `.git` and skipping `node_modules` and `.xbin` (110-119);
  - `data/`, only when the component roots its scope (61-75): sqlite, filesystem and blob as whole decrypted trees, and kv decoded to base64 JSON (121-196);
  - `term/`, without `vm/` (102-106);
  - cron jobs and bus subscriptions, listed in the manifest (79-86).
- **What it leaves out.** The env layer, logs, workspace-level resources and the data of non-root components. The vault too: `WithVault` is never set (plans/lifecycle.md:133-139).
- **Fidelity.** `Tree` skips non-regular files (backup/backup.go:127-129), so symlinks, special files and xattrs are lost. Copies are live: scheduled backups don't stop the backend (backup_cron.go:83-94), and sqlite is copied as files (docs/overview/14-lifecycle.md).
- **Restore (backup.go:215-305) is placed by the manifest alone.**
  - Source goes to `m.Component`; the terminal layer to `.xbin/term/<CompKey>`.
  - Data goes to `m.Scope` through `restoreFileDest` (Ensure + SafeJoin, 307-320) and `loadKV`, which re-encrypts (322-354).
  - Cron jobs are re-added; bus subscriptions only for `m.Component`.
  - It merges rather than replaces: nothing is wiped first.
- **Offload (449-489).** Stops the tile, backs up, then `removeScopeData` removes kv buckets and `data/resources/<key>`. The ciphertext and mounts stay. A full offload also removes the terminal layer and the source, but keeps xbin.json and scope.json.
- **Archive interface (LC-3).** The archiver tile is resolved from `bindings[comp]["@archive"]`, falling back to `bindings["*"]` (backup.go:31-41). xbind calls it as the owner through the proxy (375-388):
  - `PUT /archive/<CompKey>`
  - `GET /archive/<key>/versions`
  - `GET /archive/<key>/versions/<v>` (add `/file?path=` for one file)
  - `DELETE /archive/<key>/versions/<v>`

  Retention pruning lists one key's versions (backup_cron.go:96-121).
- **Format compatibility.** `Schema` is 1, and readers reject only newer schemas (backup/backup.go:28-29, 165-167), so new manifest fields are additive.

### 5. Per-backend sandbox state
- **Env layer.** Lives at `.xbin/env/<CompKey>/<hash16>/{upper,work,.ok}`, where the hash is sha256 of the setup script and the rootfs id (env.go:28-46). It is built in a sandbox with egress (52-131). `gcEnvLayers` keeps only the newest hash per component (133-145). VM backends refuse `setup` (runner/vm.go:67-69).
- **Ephemeral upper.** Backends pass no `Upper`, so the sandbox init uses its private tmpfs (sandbox.go:137-142; init_linux.go:342-358). Nothing persists outside resources.
- **Network namespaces and relays.**
  - A fresh netns per generation: each `runner.start` calls `sandbox.Launch`, which clones `CLONE_NEWNET` (launch_linux.go:126-129; runner.go:431-445).
  - A userspace egress relay per instance (runner.go:497-544, closed at 591-595); the egress policy is per tile.
  - ISO-4's per-scope reuse (DECISIONS.md:86-87; plans/runtime.md:105-109) is not implemented.
  - Net-provider links are keyed by (provider, client path); each client's /30 comes from its index in the sorted client list (netmux.go:11-51; netfn.go:180-253).
- **Other runtime paths, all per component:** build output `.xbin/build/<CompKey>/bin` (runner.go:366); build caches `.xbin/cache/tile/<CompKey>` (build.go:43-50); run dir `<tmpfs>/<CompKey>/g<N>.sock` (runner.go:408); cgroup leaf named CompKey (480-484, 603).
- **VM backends (D89/D90).**
  - No persistent disk: `EnsureDisk` is used only by terminals (term/vm.go:92, 109), and a backend's upper is tmpfs (vm/manager.go:125).
  - Memory is reserved per generation; the tile's cgroup leaf is capped at two guests (runner/vm.go:54-107).
  - A tile with file resources stops its old generation before starting the new one (120-129).

### 6. Assessment

#### (a) A parallel namespace per deployment
**Why the broker must translate.** Canonical ids can't change:
- They are literal in manifests (agent `res:apps/agent/*`, calendar).
- They are literal in code: the SDK builds `/api/xbin/kv/<res>/…` (sdk/kv.go:29-31), and frontends subscribe by topic prefix (web/xbin-client.js:162-169).
- Clone rewrites them textually (clone.go:207-287).

So the broker has to map (canonical id, caller deployment) to a physical key. That needs two things: a deployment on the principal, and one key function.

**Proposed layout** (primary is byte-identical to today; `k` is a sanitized, stable deployment id):
| state | primary (`""`) | deployment `k` |
|---|---|---|
| ciphertext | `data/resources-enc/<scopeKey>/<name>` | `data/resources-enc/.deploy/<k>/<scopeKey>/<name>` |
| mount | `.xbin/resenc/<scopeKey>/<name>` | `.xbin/resenc/.deploy/<k>/<scopeKey>/<name>` (inside the AppArmor glob; inference: `**` matches dot segments) |
| fs label | `fs:<scopeKey>/<name>` | `fs:.deploy/<k>/…`, or the primary's label if clones are ciphertext copies |
| kv | bucket `res:<scope>/<name>` | nested under top-level bucket `deploy:<k>`, or a per-deployment db file; the label includes `k` |
| vault (if split) | `data/vault/<CompKey>.json` | `data/vault/.deploy/<k>/<CompKey>.json` |
| cron / bus subscriptions | entries as today | same files, entries carry `"deployment"` |
| logs / status | `.xbin/log/<CompKey>.log`, `statuses[comp]` | `.xbin/log/.deploy/<k>/…`, key `(comp, k)` |
| env layer | `.xbin/env/<CompKey>/<hash>` | shared by hash; GC keeps the union of hashes live deployments use |
| build / run / cgroup | per CompKey | per (CompKey, k); keep socket paths under 108 B |
| tier-2 uid | `uids.json[scope]` | the same uid |

**Why a dot-prefixed level and not a suffix.**
- `ScopeKey` already collides, and `@` and `~` are legal in paths.
- No scope key can start with `.`: the registry skips dot dirs (registry.go:440), and `ComponentPathOK` rejects dot segments (util.go:109).
- Resource names can start with `.`, so the dot level has to sit above the scope key, not below it.
- Every existing kv bucket name starts with `res:`, so any other prefix is collision-free.

**`k` must be derived.** Branch names contain `/`, so `k` should be a slug plus a hash, the way `CompKey` is built.

**Stable resource paths in every deployment.** `XBIN_RES_*` for filesystem and sqlite can stay the primary's path in all deployments. Bind the deployment's mount at the primary's in-sandbox path: `Bind` supports `Src≠Dst` (sandbox.go:109-114; init_linux.go:390-416), and VMs export by destination path (vm/manager.go:268-289). `resourceBinds` currently always uses `Src==Dst`. This isn't possible without isolation.

**Code shape.**
- Route the 11 `ScopeKey` sites and the 3 hand-built bucket strings (§2.1) through one function.
- Give the runner hooks a deployment, either as an in-memory `Deployment` field on a per-spawn `registry.Component` copy or through new hook signatures.

#### (b) Seeding a non-primary deployment and resetting it
- **kv.** Done inside xbind, with no tool:
  1. read the primary bucket in one read transaction;
  2. `decodeKV` with the primary label;
  3. `encodeKV` with the dev label;
  4. write in batches.

  This reuses `dumpKV`/`loadKV` (backup.go:174-196, 322-354) once they take a key function. A bbolt read transaction is a consistent snapshot. Avoid one big `Update` on the shared kv.db.
- **blob.** Only xbind ever writes it, through the API, and only regular files (resources.go:366-390). An in-process copy or a ciphertext copy both work.
- **filesystem and sqlite (plaintext the sandbox can write).** Three options:
  1. **Ciphertext clone.**
     - Permitted: the cipher tree is xbind-only data, which is the same rationale as the `exec-ok` note at resenc.go:283.
     - What to copy: gocryptfs.conf, diriv and longname files, and xattrs (single-tenant ownership lives in xattrs, D43). Reflinks where available.
     - Mounting: either with the source's label (Ensure already separates label from location), or after re-wrapping with `gocryptfs -passwd`.
     - Result: lossless (symlinks, whiteouts, virtual owners), and the clone shares the source's master key.
     - Cost: the source must be quiesced (stopped, or at least synced with writeback off, D45/D46).
  2. **Plaintext copy inside `confine` (D78).** Bind the source mount read-only and the destination read-write, then run `cp -a` or `rsync -aHAX` (rsync ships in the rootfs, docker/rootfs.Dockerfile:15-23). The copy gets independent keys; it is slower and still needs a quiesced source to be consistent.
  3. **sqlite online copy (VACUUM INTO or the backup API).**
     - It too must run inside confine: xbind links no sqlite driver (no match in go.mod), and the rootfs has python3's sqlite3 module but no sqlite3 CLI.
     - Only `<name>.sqlite` of a sqlite-type resource can be identified.
     - Cross-process locking over FUSE (D45/D46) and VM primaries (runner/vm.go:120-129) make it unverified.

  Don't seed through the backup tar: it is lossy and exposed to a symlink race (backup.go:127-130).
- **Vault (if split).** A byte copy works, since the file is sealed with the DEK and no label (vault.go:96-101).
- **cron, bus subscriptions, iface-instances, ingress-hosts.** Don't copy them. The dev backend registers its own at start, the same rule D85 applies to new tiles (create.go:184-190).
- **Reset.**
  1. Stop every backend of every tile in the scope that has deployment `k`.
  2. `Unmount` the volumes.
  3. `RemoveAll` the cipher and mount dirs.
  4. Drop the kv buckets.
  5. Reseed, or let `Ensure` initialize empty volumes.

  Don't reset with `restore()`: it merges.

#### (c) Deleting a deployment's data
New code is needed: no current path removes ciphertext, and `Unmount` is unused. In order:
1. Stop the deployment's backends.
2. `resenc.Unmount` each volume.
3. `os.RemoveAll` the cipher dir and the mountpoint. This is not a tool run, and RemoveAll doesn't follow symlinks.
4. Drop the kv namespace: one `DeleteBucket` on `deploy:<k>`, or remove the per-deployment db file.
5. Drop cron and bus entries tagged `k`, in the style of `forget()` (cron.go:146-164; bussubs.go:218-235).
6. Remove the vault file.
7. Remove logs, the run dir, build output, the cgroup leaf, and env hashes nothing else uses.
8. Clear in-memory caches: `resUsageC`, `busEv`, statuses.

In a multi-tile scope, delete `S@k` data only once no tile in S still has `k`. Also teach `pathLeftovers` (policy.go:218-257), or tile removal, about `.deploy` files, or they will outlive the tile.

#### (d) The scope boundary when only one tile is forked
- **Shipped tiles.** Every shipped scope has one component, so `(scope, k)` equals `(tile, k)` and the question doesn't arise.
- **S2/S3 with only tile A forked.** A@k runs against `S@k` while sibling B keeps `S@main`, so direct data isolation holds. But A's same-scope calls to B are auto-granted (broker.go:438-450) and reach B's primary. That is production data, unless a parallel fabric routes A@k to B@k or refuses the call.
- **Does (scope, k) make siblings share?** Yes. Keying by `(scope, k)` gives same-named sibling deployments a shared `S@k`, which keeps the same-scope contract (a shared sqlite or filesystem, docs/resources.md:262-264) inside each environment. Keying by `(tile, k)` would break it: A@dev and B@dev would each get a private copy of what is one db in prod.
- **Costs of (scope, k).**
  - Seeding, reset and deletion of `S@k` become scope-level: every tile with `k` must be stopped, and deletion is refcounted.
  - Sibling repos share data only when their deployment names match.
- **Cross-scope access.** Keying by the resource's scope and the caller's `k` extends naturally (email@dev reads the calendar scope's dev namespace). But it can create namespaces the owning scope never seeded, so it needs a rule. Workspace-level resources would become one namespace per `k` across the whole workspace.
- **Declarations.** Shipped scope.json files change per branch; an S2 scope has one declaration for every deployment; and the registry and `parseRes` read only the working tree.
- **What doesn't change.** D82 is unaffected, since deployments add no owners. The tier-2 uid can stay per scope. ND5 auto-grants depend on whichever manifest a deployment uses.

#### (e) Can the primary keep today's keys forever?
Yes, under three conditions:
1. **Storage follows the deployment, not the primary role.** The implicit default deployment (key `""`, shown as "main") keeps the legacy keys even when another deployment is primary. Otherwise reassigning primary means a data swap that can't be done atomically: re-encrypting kv, re-wrapping or copying volumes, and renaming dirs.
2. **Non-primary keys stay in the disjoint namespaces** from (a).
3. **Nothing is written before someone opts in.** No writes to the root xbin.json or `data/` until a user enables deployments. The legacy fixture test requires a byte-identical root xbin.json and only allowed paths on first boot (docs/maintenance.md:161-172; compat.md rules 1 and 9).

With those, no migration is needed, and tiles that don't opt in see zero change. compat.md doesn't promise the `data/` layout (compat.md:141-144), but "never break users" still applies. Tiles that persist absolute resource paths are one example (a podman store keeps its graph root).

### 7. Implementation constraints
- **Size ratchet** (hack/size-budget.txt:1-16): runner.go is at 833 of 850 lines, auth.go at 707 of 732, and unlisted Go files are capped at 800 (broker.go is at 743). New logic has to go in new files.
- **D78.** `TestNoDirectExec` means gocryptfs, cp or rsync runs must go through `confine` or carry an `// exec-ok:` comment.
- **AppArmor.** Mounts must stay under `.xbin/resenc/`.

## Surfaces this feature touches

### Caller deployment identity

- **Paths:** `internal/auth/auth.go:54-84`, `internal/auth/auth.go:445-462`, `internal/auth/auth.go:569-591`, `internal/auth/frametoken.go:209-275`, `internal/runner/runner.go:415-425`, `internal/runner/runner.go:479`
- **Today:** Instance token maps to a component path only; frame tokens encode (component, user, exp, gen); terminal tokens resolve to the tile's element principal.
- **Change needed:** Add a deployment key to auth.Principal ("" = primary), set by RegisterInstance(token, comp, dep) and by frame/terminal minting for non-primary frontends and terminals; optionally export XBIN_DEPLOYMENT.
- **Compat:** "" default keeps every existing principal and handler identical; token formats are daemon-internal and short-lived.

### Resource identity and scope resolution

- **Paths:** `internal/registry/registry.go:182-191`, `internal/registry/registry.go:423-508`, `internal/registry/registry.go:565-582`, `internal/broker/broker.go:323-366`, `internal/broker/broker.go:412-460`, `internal/broker/policy.go:177-213`
- **Today:** Scope = nearest scope.json in the working tree; res:<scope>/<name> via parseRes; same-scope auto-grants from the working-tree `uses`.
- **Change needed:** Keep canonical ids identical in every deployment; decide whose scope.json/xbin.json (working tree vs a per-deployment snapshot) defines a deployment's resources, XBIN_RES_* and ND5 auto-grants.
- **Compat:** Ids unchanged, so hard-coded ids in manifests, SDK (sdk/kv.go:29-31) and frontends (web/xbin-client.js:162-169) keep working.

### Physical key function for resource data

- **Paths:** `internal/util/util.go:125-144`, `internal/broker/resenc_wire.go:42-43`, `internal/broker/resenc_wire.go:94-100`, `internal/resenc/resenc.go:81-118`, `internal/broker/backup.go:146`, `internal/broker/backup.go:332`, `internal/broker/backup.go:482`, `internal/broker/resources.go:38`, `internal/broker/broker.go:188`, `internal/broker/diskmon.go:243`, `internal/broker/resusage.go:119`
- **Today:** (scopeKey, name) gives cipher/mount dirs and the fs label; "res:<scope>/<name>" gives the kv bucket and kv label; computed at ~15 sites.
- **Change needed:** One resKey(resTarget, dep) returning {dirKey, fsLabel, bucket, kvLabel}; dep "" returns today's values, others a dot-prefixed .deploy/<k> namespace; route all sites through it.
- **Compat:** Primary outputs byte-identical, so no data migration and the legacy fixture is unaffected.

### kv plane

- **Paths:** `internal/broker/resources.go:155-298`, `internal/broker/resenc_wire.go:189-229`, `internal/broker/resusage.go:147-163`, `internal/broker/backup.go:174-196`, `internal/broker/backup.go:322-354`
- **Today:** One data/kv.db; bucket = canonical id; AES-GCM label kv:<bucket>.
- **Change needed:** Non-primary data under a non-"res:" top-level bucket (deploy:<k>, nested per resource) or a per-deployment db file; handlers choose by principal deployment; admin reads default to primary with an optional ?deployment=.
- **Compat:** Primary buckets, labels and URLs unchanged.

### File resources and encrypted mounts

- **Paths:** `internal/broker/resources.go:36-98`, `internal/broker/resources.go:300-338`, `internal/broker/resenc_wire.go:102-187`, `internal/resenc/resenc.go:120-278`, `internal/runner/binds.go:17-54`
- **Today:** One gocryptfs volume per (scopeKey, name), mounted for every declared resource at provision/unseal; spawn held until mounted; Src==Dst binds.
- **Change needed:** Mount and hold per (scope, dep) for existing deployments; EnvFor hands the primary's in-sandbox path and binds the deployment's mount there (Src!=Dst); add delete (Unmount + RemoveAll) and clone (ciphertext copy with label reuse or passwd rewrap).
- **Compat:** Primary mounts and XBIN_RES_* paths unchanged; non-primary mounts must stay under .xbin/resenc/ for the AppArmor rule.

### Bus

- **Paths:** `internal/broker/resources.go:408-458`, `internal/events/events.go:11-17`, `internal/server/server.go:592-611`, `internal/broker/bussubs.go:271-362`, `internal/broker/resusage.go:180-192`
- **Today:** Topic namespace = canonical id; delivery by component grant; admins see every bus event.
- **Change needed:** Tag events with the publisher's deployment; deliver only to same-deployment WS and push subscribers; counters per (id, dep).
- **Compat:** Additive Event field; untagged = primary, so primary delivery is unchanged.

### Cron and bus-subscription stores

- **Paths:** `internal/broker/cron.go:31-270`, `internal/broker/bussubs.go:55-449`, `internal/broker/backup.go:79-86`, `internal/broker/backup.go:282-303`
- **Today:** Keyed component\x00name; dispatch through the proxy to /api/<comp> (the primary).
- **Change needed:** Optional `deployment` field; key (component, dep, name); dispatch to that deployment; per-deployment forget/delete; backups keep primary entries.
- **Compat:** An absent field means primary; persisted JSON stays readable by old and new daemons.

### iface-instances and ingress-hosts

- **Paths:** `internal/registry/registry.go:218-228`, `internal/broker/netfn.go:1037-1125`, `internal/broker/ingressfn.go:491-583`
- **Today:** Per component in the root xbin.json, replaced wholesale per call; re-registration restarts bound requesters.
- **Change needed:** Refuse or separately store registrations from non-primary deployments.
- **Compat:** Root xbin.json untouched unless a user opts in (legacy fixture requires it byte-identical on first boot).

### Vault

- **Paths:** `internal/broker/vault.go:45-176`, `internal/broker/admin.go:48-62`, `internal/broker/admin.go:125`, `internal/broker/policy.go:250`, `sdk/xbin.go`
- **Today:** data/vault/<CompKey>.json sealed with the DEK; values readable only by the tile's instance principal (D30).
- **Change needed:** Decide shared vs per-deployment; if split, data/vault/.deploy/<k>/<CompKey>.json chosen by principal deployment, seeded by file copy.
- **Compat:** Primary file unchanged; migrateVaults reads only top-level files (vault.go:115-147).

### Quota and usage accounting

- **Paths:** `internal/broker/broker.go:177-198`, `internal/broker/broker.go:723-731`, `internal/broker/diskmon.go:114-248`, `internal/broker/resusage.go:60-131`
- **Today:** Per scope key, file trees only (kv not counted); blocks kv/blob writes with 507.
- **Change needed:** Measure the .deploy namespaces; choose pooled or per-deployment quota; per-deployment rows in ResourceUsage.
- **Compat:** Primary figures unchanged if dev is measured separately; additive JSON fields.

### Backup, restore, offload

- **Paths:** `internal/broker/backup.go:57-489`, `internal/broker/backup_cron.go:83-121`, `internal/backup/backup.go:43-57`
- **Today:** One archive key per component (CompKey); data only for scope roots; restore merges by manifest; offload removes kv + data/resources/<key> only.
- **Change needed:** Parameterize dump/load by key function (reuse for kv seeding); primary-only backups by default; a distinct archive key if dev is ever backed up; make offload remove ciphertext; wipe before a restore used as reset.
- **Compat:** Additive optional manifest field; Schema stays 1; primary backups unchanged.

### Observability: status, logs, prefs

- **Paths:** `internal/obs/status.go:26-143`, `internal/obs/logs.go:42-100`, `internal/obs/prefs.go:33-48`, `cmd/bx/main.go:511`
- **Today:** Status and logs keyed by component; any build-start clears status; prefs per user x component.
- **Change needed:** Status and logs per deployment (events carry deployment; clear only that deployment's status); decide whether prefs are shared.
- **Compat:** Primary log path and status key unchanged; `bx logs` gains an optional flag (CLI superset).

### Env layer and per-component runtime paths

- **Paths:** `internal/runner/env.go:28-145`, `internal/runner/runner.go:366`, `internal/runner/runner.go:408`, `internal/runner/runner.go:464-484`, `internal/runner/runner.go:603`, `internal/runner/build.go:43-50`
- **Today:** One env-layer hash kept per component; single build output, run dir, log and cgroup leaf per component.
- **Change needed:** GC keeps the union of hashes used by live deployments (content-addressed sharing is safe); per-deployment build output, run dir (under the 108-byte socket limit), log and cgroup leaf.
- **Compat:** Primary paths unchanged.

### Network namespaces, relays, provider links, VMs

- **Paths:** `internal/sandbox/launch_linux.go:47-129`, `internal/runner/runner.go:431-611`, `internal/runner/netmux.go:11-51`, `internal/broker/netfn.go:180-253`, `internal/runner/vm.go:54-129`
- **Today:** Fresh netns and relay per generation, policy per tile; provider links keyed (provider, client path) with index-derived /30s; VM cgroup cap covers two guests per tile.
- **Change needed:** Nothing for netns/relay; provider links need (client, dep) entries or non-primary deployments get relay-only egress; VM leaf per deployment; if ISO-4 is ever built, key it per (scope, dep).
- **Compat:** Primary unchanged as long as deployments never join provider rosters implicitly.

### Tier-2 per-scope uids

- **Paths:** `internal/broker/uids.go:15-90`, `internal/runner/runner.go:446-461`
- **Today:** Scope path maps to a uid in .xbin/uids.json; non-isolated spawns only; chown covers data/resources/<scopeKey> only.
- **Change needed:** Share the scope's uid across deployments; chown deployment dirs.
- **Compat:** uids.json unchanged for primary.

### Deletion and reset (new)

- **Paths:** `internal/resenc/resenc.go:242-253`, `internal/broker/backup.go:471-489`, `internal/broker/cron.go:146-164`, `internal/broker/bussubs.go:218-235`, `internal/broker/policy.go:218-257`
- **Today:** No path deletes encrypted data; forget() drops cron/bus entries by path at tile creation; leftovers keyed by path.
- **Change needed:** New delete/reset: stop, unmount, RemoveAll cipher and mount dirs, drop kv namespace, cron/bus entries, vault file, logs, run/build, caches; refcount (scope, dep) across sibling tiles; teach leftovers about .deploy files.
- **Compat:** Touches only .deploy namespaces.

### Builder docs

- **Paths:** `docs/resources.md`, `docs/elements.md`, `docs/overview/10-resources.md`, `docs/overview/14-lifecycle.md`, `docs/compat.md`
- **Today:** Documents data/resources-enc/<scope>/, res:<scope>/<name>, same-scope sharing, and backups including scope data.
- **Change needed:** Document per-deployment namespaces, what stays shared (grants, bindings, possibly vault) and that ids and XBIN_RES_* stay stable; changelog entry.
- **Compat:** Additive documentation.

## Hazards

- A non-primary deployment that registers cron jobs or bus subscriptions under the same names overwrites the primary's entries. The agent template does both at start (builtin-templates/agent/_backend/schedule.go:184, triggers.go:456), the stores key on component+name (cron.go:108, bussubs.go:158), and ticks dispatch to /api/<comp> (cron.go:200-208).
- iface-instances and ingress-hosts are replaced wholesale per component in the root xbin.json (netfn.go:1102-1110, ingressfn.go:564-574), so a dev backend's startup registration would replace the primary's and restart its bound requesters (netfn.go:1115-1125).
- Bus events carry no deployment (events.go:11-17) and delivery checks only the component's grant (resources.go:439-458, bussubs.go:337-362), so events a dev deployment publishes would reach production frontends and push subscribers.
- Encrypted data is bound to its name: kv to the kv:<bucket> label and gocryptfs volumes to fs:<scopeKey>/<name> (resenc_wire.go:43, 202-229; resenc.go:106-118). A byte copy into a renamed namespace can't be decrypted unless it is re-encoded or re-wrapped.
- Offload never deletes encrypted file-resource data: removeScopeData drops kv buckets and data/resources/<scopeKey> only (backup.go:471-489) and leaves data/resources-enc/<scopeKey> and the live mount. The docs' 'archived + removed' is untrue for fs/sqlite/blob, and there is no deletion path to reuse.
- restore() merges instead of replacing (loadKV Puts into existing buckets, backup.go:330-353; writeFileFrom replaces file by file, backup.go:356-371), so resetting a namespace by restoring into it leaves stale keys and files.
- backup.Writer.Tree drops symlinks, special files and xattrs (internal/backup/backup.go:127-129), and it opens paths only after a WalkDir type check (backup.go:130), so a running backend can race a symlink in. Seeding through the backup tar is lossy and race-exposed, and scheduled backups don't stop the backend (backup_cron.go:83-94).
- gcEnvLayers deletes every env-layer hash of a component except the newest (env.go:133-145), so two deployments with different setup scripts would delete each other's live overlay lowerdirs and rebuild each other's layers in a loop.
- Build output, run dir, log file, cgroup leaf and VM memory cap exist once per component (runner.go:366, 408, 465, 481-483, 603; runner/vm.go:99-107). Concurrent deployments of one tile collide: an exiting dev backend removes the shared cgroup leaf, and two deployments' VMs exceed the two-guest cap.
- netMux.register closes an existing link fd for the same (provider, client path) (netmux.go:22-32), and link addresses come from each client's index in a sorted list (netfn.go:180-190, 212-253). Adding a deployment as a provider client would kill the primary's link or shift every client's address.
- The obs status map is keyed by component and cleared on any build-start for it (obs/status.go:126-143), so a dev rebuild would erase the production tile's status.
- util.ScopeKey is already not injective (util.go:127-132), and ComponentPathOK permits ~ and @ (util.go:95-114), so a suffix scheme like ScopeKey+'@dev' can collide with a real scope. A dot-prefixed level is safe because the registry never scans dot dirs (registry.go:440).
- The installer's AppArmor rule allows FUSE mounts only under <ws>/.xbin/resenc/**/ (deploy/install.sh:909; resenc.go:199-226), so non-primary mounts placed elsewhere fail on Ubuntu installs and their tiles stay held.
- Seeding a large bucket in one bbolt Update on the shared data/kv.db, as loadKV does (backup.go:330), blocks every tile's kv writes for the duration.
- An online copy of a live primary's sqlite over gocryptfs FUSE relies on cross-process lock and mmap behaviour (D45/D46), and it is incoherent when the primary runs in a VM whose guest caches file pages (runner/vm.go:120-129; D89).
- Some tiles store absolute resource paths inside their data, such as the podman store in devbox storage (builtin-tiles/devbox/xbin.json). A seeded copy exposed at a different path breaks them. Only Src!=Dst binds in isolated mode (init_linux.go:390-416) can keep XBIN_RES_* identical; the no-isolation spawn path (runner.go:446-461) can't.
- Grants are per tile and traffic reaches primaries, so a non-primary deployment running unmerged code calls other tiles' production APIs with production grants (broker.go:412-452). (scope, deployment) data keys don't bound access that goes through APIs.
- With a shared vault, dev code reads production secrets, because the D30 value gate is p.Component==comp && Via=="instance" (vault.go:188).
- Data of plain-directory (S2) scopes is in no component backup and is never offloaded (backup.go:61, 474). Any per-deployment backup or seed built on writeBackup inherits this gap.
- Inference, not verified: tier-2 scope-uid backends probably can't open normal-mode gocryptfs mounts owned by root xbind, because those mounts have no allow_other (resenc.go:177-181; runner.go:457-461).
- The size ratchet leaves little room: runner.go is at 833 of 850 lines, auth.go at 707 of 732, and broker.go at 743 of the 800-line unlisted cap (hack/size-budget.txt:1-16). Deployment logic has to go in new files.

## Open questions

- Does storage identity follow the deployment, or the primary role? Following the deployment means the default deployment keeps the legacy keys even when it isn't primary. Following the role means reassigning primary requires a data swap.
- When a deployment runs a revision whose scope.json or xbin.json differs, which manifest defines its resources, XBIN_RES_* and same-scope auto-grants? For S2 scopes, scope.json belongs to no tile repo.
- When a non-primary deployment reads another scope's resource (e.g. examples/email@dev reading res:apps/calendar/bus), what should it get? Options: the owning scope's namespace for the caller's deployment (which may create namespaces nobody seeded), the primary's data, or a refusal.
- Vault per deployment: shared, copied at creation, or empty?
- Per-user prefs per deployment: shared per user x tile, or split?
- Quota: count non-primary usage against the scope's quota, or give each deployment namespace its own?
- Seeding: may the primary be stopped briefly, allowing a consistent and lossless ciphertext clone? Or must seeding be online (sqlite backup API in confine for known dbs, bbolt snapshot for kv, crash-consistent copy for other files)?
- Is it acceptable for a seeded volume to share the gocryptfs master key with its source (ciphertext copy with a reused label)? Or must clones be plaintext copies with independent keys?
- Workspace-level resources (res:workspace/*): one workspace-wide namespace per deployment name, or not forked at all?
- In multi-tile scopes, who may seed, reset or delete S@k? Does deleting one tile's deployment k wait until no sibling tile still has k?
- Are non-primary deployments ever backed up? If so, under which archive key, so that their versions don't count against the primary's retention pruning (backup_cron.go:96-121)?
- What deployment names are allowed (branch names contain '/')? Does each deployment get a stable internal storage id, separate from its display name and branch?
- Do non-primary deployments require isolation? Without a sandbox a copy can't be shown at the primary's paths, and confine runs tools directly.
