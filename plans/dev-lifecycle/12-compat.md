# 12 — Compatibility: zero change, old clients, downgrade

> Status: live — the compatibility proof for tile deployments: the zero-change guarantee and the tests that discharge it, docs/compat.md rule by rule, old and new clients, the downgrade story, fixture expectations and the breaking-change verdict (part of [plans/dev-lifecycle](README.md))

This document owns one claim and its proof: **the dev lifecycle breaks no
one.** Vocabulary is [01-glossary.md](01-glossary.md), used verbatim. The
object model is [05-model.md](05-model.md); P5 (zero state), P6 (storage
follows the deployment), P13 (dormant registrations, namespaced events), P15
(state in `data/`) and P17 (additive markers) carry most of the weight here.

Every "today" fact below was re-verified on the `dev-lifecycle-design`
worktree (master plus the `sandbox-visibility` branch: D112 landed, D113
designed). The evidence base is:
- [research/builder-contract.md](research/builder-contract.md): the compat rules and contract hooks;
- [research/delivery-infra.md](research/delivery-infra.md): the legacy fixture and the guards;
- [research/ui-surfaces.md](research/ui-surfaces.md): old clients;
- [research/terminology-census.md](research/terminology-census.md): vocabulary in existing docs;
- [research/side-findings.md](research/side-findings.md);
- [research/sandbox-visibility.md](research/sandbox-visibility.md): the D112 wire.

**Conclusions.**
- Nothing in the design is BREAKING (§10). No migration note is needed.
- One transition warns before it errors: `+` in new tile names (§7).
- Downgrade is supported in the sense that matters: an older xbind loses
  nothing and activates nothing it shouldn't. It does serve every tile's
  work tree with `main`'s data, so operators get a checklist (§5).
- Holding these claims needs eight tightenings of the model and thirteen new
  proposals, listed at the end.

Each implementation work package's "Compat" section
([research/delivery-infra.md](research/delivery-infra.md) §5) cites the proof
obligations (PO-n) below that it touches, and keeps their tests green.

## 1. The zero-change guarantee

### 1.1 Terms

- **Zero-state tile:** a tile with no deployment record (P5). A tile that
  opted out is in the zero state. Its checkpoint store may remain as inert
  history, but nothing on the request, save or boot path reads it.
- **The previous release:** the last xbind release without this feature.
  "Today" means its behaviour, which is master's as of this writing.

### 1.2 Statement

**Z1: per tile.** For a zero-state tile T, every interaction that exists in
the previous release gives the same observable result on a binary with the
feature:
- requests to `/c/T/…`, `/api/T/…` and `/ws/…` on T's behalf: same
  resolution, status, headers and body;
- T's backend: same spawn inputs (argv, env, binds, sandbox mode, cgroup
  leaf, VM booking);
- T's calls to other tiles and to xbind: same injected headers, same
  authorization answers;
- a save in T's work tree: same events in the same order, same rebuild and
  blue/green swap;
- boot, lifecycle, grant or binding changes, backup, restore and offload:
  same files read and written, with the same bytes;
- tokens minted for T's documents, backend and terminals: same formats and
  verification;
- the D112 registry and the admin surfaces: same entries, fields and values.

**Z2: per workspace.** A workspace where no tile has ever opted in gains
nothing from the feature, at boot or at runtime: no file, directory, process,
cgroup, kv bucket or registry entry.

**Z3: neighbours.** Z1 holds for a zero-state tile even while other tiles
have deployments. The only traffic it can see that the previous release could
not produce is new traffic: calls from another tile's non-primary deployment.
These arrive read-clamped and carry `X-XBin-Deployment` (P3, P17). Nothing it
received before changes.

**Z4: opting out.** Returning a tile to the zero state (P5) restores Z1 for it
at once, with no restart and no lingering behaviour.

### 1.3 What the guarantee deliberately does not cover

| Allowed change | Why it is not a violation |
|---|---|
| New routes (the deployments family under `/api/xbin/`); one new event type (`deployments`), emitted only by opt-in operations; new optional response fields, absent for zero-state tiles | compat rule 2 permits additive API. No existing request or event changes. |
| The terminal window, which is binary-served, shows the opt-in controls (pause live reload, the deployments panel) to users at `terminal` level | It is where one opts in. The controls act only when pressed, and viewers of the tile see nothing new. |
| `/docs/`, `bx` usage text and the changelog grow | Documentation, not behaviour. |

### 1.4 Proof obligations

Tests marked *new* are specified here. The others are already named in
[15-test-plan.md](15-test-plan.md), which schedules all of them. Its §2.7
zero-state goldens are written in M0, against today's code.

| # | Area | Obligation | Discharged by |
|---|---|---|---|
| PO-1 | URLs | Every URL that resolves today resolves identically. The qualifier split (`<tile>+<name>`) is tried only when today's resolution finds no component and no on-disk path at the full candidate (exact match wins), and only for a tile that has a deployment record (NP-12-1). In the zero state `/c/<tile>+main/` behaves exactly as today. A `+` in a query string is never read as the qualifier. | `TestResolveDeploymentQualifier` (`internal/registry/qualifier_test.go`, [15-test-plan.md](15-test-plan.md) §3.1) gains rows: an on-disk `a+b` directory that is not a component; a qualified path on a zero-state tile; `+main` in the zero state; nested `apps/shop/admin+dev`; names like `c++`. `TestZeroStateDocumentUnchanged` (`internal/server/zerostate_test.go`, built like `TestLegacyInjectionUnchanged`, `internal/server/tileassets_test.go:187`) gains a matrix of `/c/` and `/api/` URLs with and without `+`, byte-compared on a zero-state workspace. |
| PO-2 | Storage keys | Every key function returns today's value for `main`. For every other deployment it returns a value no path can produce under today's or any older key function: `util.CompKey`, `util.ScopeKey` (`internal/util/util.go:127-144`) and `res:<scope>/<name>` buckets (`internal/broker/backup.go:332`). A dot-prefixed path level does this, since no component or scope segment may start with `.` (`util.go:108-110`); so does a bucket name outside `res:`. | `TestZeroStateKeys` (15-test-plan §2.7) for `main`. *new* `TestDeploymentKeysDisjoint`, a property test over adversarial paths (`<t>+<n>`, `<t>~<n>`, `<t>.<n>`, `<t>/<n>`, 24-character truncation collisions). `TestResourceBinds` (`internal/runner/resourcebinds_test.go:9`) and the `EnvFor` joined-string tests (`internal/broker/ingressfn_test.go:120`) gain `main` rows equal to today's strings. |
| PO-3 | Env | A zero-state backend's env equals today's set (`internal/runner/runner.go:423-431`, `internal/broker/resources.go:71-145`): no `XBIN_DEPLOYMENT`, and the same `XBIN_RES_*`, `XBIN_IFACE_*` and `XBIN_SOCKET` values. A zero-state terminal's env equals today's (`internal/term/term.go:737-795`): no `XBIN_DEPLOYMENT`, and `GIT_CONFIG_COUNT=2` with today's two keys (NP-12-7). | `TestZeroStateBackendEnv` and `TestSessionEnvZeroState` (`internal/term/deploy_test.go`), both in 15-test-plan §2.7. |
| PO-4 | Headers | For a primary's calls, `identify` (`internal/proxy/proxy.go:275-305`) injects exactly today's headers. `X-XBin-Deployment` appears only on calls from a non-primary deployment. An inbound `X-XBin-Deployment` is stripped like every `X-Xbin-*` header (`proxy.go:276-280`). xbind adds no response header to zero-state responses. | `TestIdentifyZeroState` (`internal/proxy/deploy_test.go`, 15-test-plan §2.7), with added cases for element, human, view-as and ingress principals and a forged inbound header. |
| PO-5 | Events | A zero-state tile's event stream is today's: `reload` per changed tile (`internal/boot/serve.go:175-182`), the runner's `build-*`, `status`, `grants`, `pr` and `term`, with the same types, components, fields and order, and no `deployments` event. On a tile with deployments, events about the primary are exactly today's. Non-primary activity is never emitted as `reload` or `status`, and preferably not as `build-*` (§3.1, C2). | `TestEventBytesZeroState` (`internal/server/deployevents_test.go`, 15-test-plan §2.7). `TestChangedComponentsNativeEntry` (`internal/boot/watch_native_test.go:16`) gains zero-state rows, and rows with live reload paused, reproducing today's `(reload, restart)`. `TestStatusPerDeployment` (on `testPlane`, `internal/obs/status_test.go:89`). *new* `TestNonPrimaryUsesNewEventTypes`: no event that names a non-primary deployment has type `reload` or `status`. |
| PO-6 | Tokens | Frame tokens for `main`'s documents keep today's five-field wire format bit for bit (`internal/auth/frametoken.go:39-43`). The four- and five-field verification paths are unchanged (`frametoken.go:252-294`). A deployment claim adds a sixth field, so older verifiers, which accept only four or five (`frametoken.go:255-266`), reject it. Renewal copies the claim as it copies the generation. Instance and terminal tokens stay in-memory maps (`internal/auth/auth.go:219-220`); their principal gains a deployment defaulting to `main`. | `TestLegacyFrameTokenUpgrade` (`internal/auth/frametoken_test.go:157`) stays unchanged and green. `TestZeroStateTokens` (`internal/auth/deployment_test.go`) and `TestFrameTokenDeploymentClaim`, both in 15-test-plan. |
| PO-7 | Files | A workspace where no tile has opted in never gains `data/deployments/`, `data/checkpoints/`, `.xbin/deploy/`, a dot-level namespace, or a per-deployment log, socket directory or cgroup leaf, under any use of its tiles: save, build, crash, idle reap, lifecycle, grants, backup, restore. | Both legacy fixtures gain the no-deployment-state assertion (§6.1). `TestZeroStateCreatesNoDeploymentFiles` (`internal/boot/deploystate_test.go`) and `TestZeroStateLifecycleFiles` (`test/livereload_test.go`), from 15-test-plan §2.7; the integration test copies its Go tile from `examples/counter-go` and never edits the example in place. |
| PO-8 | Boot | The aged workspace boots with today's allowed-path lists unchanged, and the second boot changes nothing. A workspace with deployment state boots twice idempotently (§6.2). | `TestLegacyWorkspaceBootsTwice` (`test/legacy_workspace_test.go:31`) and `TestLegacyWorkspaceBootsTwiceInProcess` (`internal/boot/boot_test.go:118`), lists untouched. `TestDeploymentStateBootsTwice` (`test/deploystate_boot_test.go`) and `TestDeploymentStateBootsTwiceInProcess` (`internal/boot/deploystate_test.go`), from 15-test-plan §6. |
| PO-9 | Registrations | `data/cron-jobs.json`, `data/bus-subscriptions.json` and the root `xbin.json` maps `ifaceInstances` and `ingressHosts` hold `main`'s rows and nothing else, whatever non-primary deployments register (P13; model §3). Component backups carry only `main`'s rows in `Manifest.CronJobs` and `Manifest.BusSubs` (`internal/broker/backup.go:79-86`). | *new* `TestOlderBinaryIgnoresDeploymentFiles` (the name [06-security.md](06-security.md) T11 uses): on a `main` + `dev` fixture, in a new helper file so that `testWorkspace` stays untouched, `dev` registers a cron job, a bus subscription, interface instances and ingress hosts named like `main`'s; the three stores and a backup manifest must be byte-equal to a run without `dev`. With a real previous-release binary: `TestDowngradeDormantRegistrations` (15-test-plan §5.6). |
| PO-10 | UI | Every tile document is served with today's D4 injection (the one sanctioned HTML transform), byte for byte: no `xbin-deployment` meta on a primary's documents. bx-frame draws no new ambient element for a zero-state tile. Old scaffold surfaces receive nothing they could misrender (§3.2). | `TestZeroStateDocumentUnchanged` (PO-1), plus a record-bearing tile's primary document. The zero-state rows of `hack/deploy-state.test.mjs` and part 0 of the `livereload` harness pass (15-test-plan §2.7): no chip and no badge, through `testApi()`, with tile state restored at the end ([research/ui-surfaces.md](research/ui-surfaces.md) §6.3). `hack/ui-harness/passes/agenttab.js:238`, which expects exactly five `.lyt` buttons, is updated in the commit that adds a sixth. |
| PO-11 | Sandboxes (D112) | A zero-state tile's registry entries keep today's ID `backend:<CompKey>:g<gen>` and fields (`internal/runner/sbx.go:63-74`), including `leaf`, which stays the flat `comp-<CompKey>` ([07-runtime.md](07-runtime.md) §10.3). A `deployment` field appears on every entry of a tile with a record, and on no zero-state entry (07-runtime §10.1). `/runtime` keeps one row per tile and `/backends` one key per tile (`internal/runner/runner.go:784-807`). VM usage stays keyed by tile (`internal/boot/sandboxes.go:75-78`). | `TestZeroStateListings` (`internal/boot/api_zerostate_test.go`), `TestSandboxEntriesPerDeployment` (`internal/runner/sbx_deploy_test.go`) and `TestCgroupLeafPerDeployment`, all in 15-test-plan; `TestSandboxEntries` (`internal/runner/sbx_test.go:20`) stays green. |
| PO-12 | Default path (P8) | A save in a zero-state tile reaches `hub.Publish` and `run.Changed` without touching the checkpoint store, materializing anything, or reading a record beyond an in-memory lookup. | `TestWatchLoopZeroStateNoStoreIO` (15-test-plan §2.7): the pure gate beside `changedComponents` takes a store interface, and its zero-state rows run against a fake store that fails the test on any call. |
| PO-13 | CLI | Every invocation accepted today parses to the same request (compat rule 6). New commands are new names in `moreCmds` (`cmd/bx/native.go:28-32`). `bx status` and `bx logs` keep treating any other argument as the tile (`cmd/bx/main.go:349-358`, `:494-503`). | *new* `TestBxTodayInvocationsUnchanged` (`cmd/bx`): a table of today's documented invocations (docs/bx.md), comparing the requests bx would send. `TestParseDeploymentArgs` (15-test-plan) covers the new commands. |
| PO-14 | API additivity | No route removed or renamed; no field removed or retyped. `/components` never gains rows (`internal/server/api.go:162-175`). Lifecycle `state` never gains values (`internal/broker/lifecycle.go:45-79`). Errors keep the `{"error","docs"}` shape (`internal/server/api.go:48-59`). No existing request body gains a required field, since `DecodeJSON` is strict (`api.go:67-76`). | `TestRouteInventory` (`internal/apicheck/apicheck_test.go:244`). `TestZeroStateComponentsEntry` and `TestZeroStateListings` (15-test-plan §2.7). |
| PO-15 | Opting out (Z4) | Resuming live reload on a tile whose only deployment is `main`, with default settings, deletes the record, and the tile passes every zero-state test above without a restart. | `TestOptOutReturnsToZeroState` (15-test-plan §2.7), at the plane and integration layers. |

## 2. docs/compat.md, rule by rule

[/docs/compat.md](/docs/compat.md) is the contract. Each rule is
paraphrased closely, with its line numbers, followed by how the design
complies and what care it needs.

**Rule 1: old workspaces boot on new binaries** (`docs/compat.md:18-23`).
Boot migrations are additive and idempotent: a second run changes nothing.
The `home/` + `homes/` hard stop stays. Boot never records edited scaffold
files as pristine.
- *Complies.* The feature adds no boot migration. The zero state is the
  absence of state, so an old workspace needs nothing (P5). Boot with
  deployment state present reads that state and never rewrites it (§6.3).
  The home stop and scaffold provenance are untouched.
- *Care.* `.xbin/deploy/` is derived state, like `.xbin/build/`. The legacy
  fixture's file map skips `.xbin/{log,run,term,build,cache,env}` but not
  `.xbin/deploy` (`test/legacy_workspace_test.go:182`,
  `internal/boot/boot_test.go:293`). The aged workspace never has it (§6.1),
  and the deployment-state fixture filters it the same way (§6.2). The
  fixture also allows any path containing `/.git/`
  (`test/legacy_workspace_test.go:82`), which would hide a boot that writes
  tile repositories: boot must not (NP-12-7).

**Rule 2: old chrome keeps working against new xbind** (`docs/compat.md:24-28`).
The HTTP API is additive only: no route removed or renamed, no response field
removed or retyped, new fields only.
- *Complies.* A new endpoint family; new optional fields; lists keyed by tile
  keep one entry per tile (§3.1, C3); `state` values are unchanged.
- *Care.* `DecodeJSON` rejects unknown fields (`internal/server/api.go:67-76`).
  A new optional field on an existing body is therefore a 400 on an older
  server, so new clients send it only after feature detection (§4.1). Event
  types are part of this surface in practice: old types never carry
  non-primary activity (§3.1, C2).

**Rule 3: shipped URLs are frozen** (`docs/compat.md:29-35`). Every
`/vendor/<name>` stays served, imports use absolute `/vendor/` URLs (no bare
specifier), and a moved file leaves a re-export shim.
- *Complies.* New UI lands in new `/vendor/` modules imported by absolute URL.
  bx-frame, events-socket and xbin-client keep their exports, attributes,
  events and methods (they are on docs/frontend-kit.md's importable list).
  Existing directories whose names contain `+` keep resolving, because exact
  match wins (PO-1). Deployment URLs are new URLs.
- *Care.* An older binary doesn't serve the new modules, so scaffold code
  must not import them statically (§5.4, R-9).

**Rule 4: scaffold layouts are additive** (`docs/compat.md:36-40`). Entry
files keep their names and import new siblings relatively; nothing shipped is
renamed or removed.
- *Complies.* Everything needed to use the feature lives in the binary-served
  terminal window ([research/ui-surfaces.md](research/ui-surfaces.md) §0).
  Scaffold additions (a card chip, a sidebar badge, a tile-menu square, a
  tile-admin section) are optional, arrive by `bx builtin update` as new
  sibling files, and tolerate an absent server field.
- *Care.* `hack/menus.test.mjs` expects four squares and indexes the admin
  block by position. The phone sheet's grid is fixed at four columns
  (`web/bx-menu.js:145`), so a fifth square wraps. Deployments never become
  sidebar rows or cards (§3.1, C4).

**Rule 5: theme** (`docs/compat.md:41-44`). Fallbacks in `web/bx-*.js` stay,
regenerated from `theme.css`.
- *Complies.* Every `var(--bx-*, literal)` fallback in the new modules equals
  `theme.css`, and `make theme-check` enforces it.

**Rule 6: the CLI is a superset** (`docs/compat.md:45-50`). Every accepted
invocation stays accepted; a newly erroring path gets a changelog line and a
one-release warning.
- *Complies.* New commands register through `moreCmds`. The names are free:
  none is in main's switch (`cmd/bx/main.go:31-104`), and unknown names end
  in `cmdExtra`'s `usage()` (`cmd/bx/agent.go:30-46`). No existing flag
  changes meaning, and nothing newly errors in bx.
- *Care.* `bx status` and `bx logs` take any other argument as the tile, last
  one wins (`cmd/bx/main.go:352-357`, `:497-503`). A new value flag there
  changes only invocations that contain the flag's name, which no documented
  or plausible command does today. Flags must be consumed before the
  positional loop, and must stay lenient.

**Rule 7: the manifest schema is additive** (`docs/compat.md:51-53`). New keys
only; unknown keys are ignored.
- *Complies.* There is no manifest key (P5). Deployment-level fields (model §6)
  are read from each deployment's code with today's parser, which ignores
  unknown keys (`internal/jsonc/jsonc.go:90-95`, via
  `internal/registry/registry.go:458-467`).
- *Care.* A checkpoint carries the manifest of its day. Rolling back to it must
  never be refused for lacking keys the work tree has since gained. The schema
  only grows, so older manifests stay valid.

**Rule 8: the SDK stays zero-dependency and changes only permissively**
(`docs/compat.md:54-56`). A call that succeeds today keeps succeeding.
- *Complies.* `xbin.Deployment()` and `CallerInfo.Deployment` are additive. An
  absent env var or header means the primary and never errors (§4.4). Every
  call that succeeds for a tile today succeeds for its primary.
- *Care.* In a non-primary deployment some calls behave differently by design:
  writes to other tiles are read-clamped (P3), notifications are held (P13),
  registrations are dormant. Rule 8 compares the same call in the same context
  across upgrades. The non-primary context is new and opt-in, so these are not
  regressions, but docs/sdk.md must say so. Registration calls keep returning
  success, so start-up code doesn't crash (model §7).

**Rule 9: every boot migration has a fixture test** (`docs/compat.md:57-62`).
- *Complies.* The feature has no boot migration and extends no allowed list.
  The fixtures gain assertions (§6).

**Rule 10: the native app contract is additive** (`docs/compat.md:63-70`, and
§The native app contract at `:110-139`).
- *Complies.* Nothing in `/vendor/xb/vocab.js`, the tree format, the bridge
  messages, `/vendor/xb-native.js` or `xbin.native` changes. On xbind's side
  (`docs/compat.md:123`): `native: {runtime: 1}` in `/whoami` is unchanged,
  and `/c/<tile>/?native=1` and `native: {entry}` in `/components` describe
  the primary (NP-12-5). Non-primary activity never reaches the app as
  `reload` or `status` (§3.4).
- *Care.* Both xbind-side rows are derived from the work tree today: the
  registry's scan (`internal/registry/registry.go:471-473`) feeds
  `internal/server/api.go:179` and `internal/server/native.go:54-56`. With a
  pinned primary, the work tree may add or drop `native.js`. The rows must
  follow the primary's checkpoint, or the app opens a runtime document the
  primary cannot serve.

**Rule 11: enforcement** (`docs/compat.md:71-74`). A builder-visible change
needs a changelog entry. One that requires operator action needs a migration
note. A silent path that must become an error warns for one release first.
- *Complies.* One changelog entry, owned by the integrator, including a
  downgrade note (§10.4); no migration note (§10); one warned transition (§7).

**Tile asset gating** (`docs/compat.md:76-108`).
- *Complies.* The three modes apply to deployment URLs unchanged. In `tokens`
  mode, asset tokens are scoped to the deployment URL's path. In `origins`
  mode, each non-primary deployment needs its own origin (NP-12-4). The tile
  cookie is the documented credential for a raw `fetch('/api/<you>/x')`, and
  the origin owns the tile's browser storage (`docs/elements.md:280-283`). A
  shared origin would route such fetches to the primary, and would let
  non-primary code read and rewrite the primary's `localStorage`.
- *Care.* `legacy` mode still loads absolute self-references without a
  credential (`docs/elements.md:297-299`). A request with no credential can
  only mean the primary (C3). So in a non-primary document, an absolute
  `/c/<tile>/…` module import, `<script src>` or CSS `url()` loads the
  primary's file, and that document mixes code from two deployments.
  Relative URLs, already the portable form, resolve under the deployment
  URL, and so does the documented navigation
  `xbin.url('/c/' + xbin.self + '/page2.html')` (`docs/elements.md:272-276`),
  which carries the frame token. The asset detection that
  D95 added to `bx doctor` already finds absolute self-references. The
  deployments panel repeats its finding for tiles with deployments (NP-12-13).

**What compat.md does not promise** (`docs/compat.md:141-147`). The contents of
`.xbin/` and the on-disk shape of `data/` may change.
- The new state lives there, so its layout is not a builder contract.
- It is still an operational contract with older binaries that may be started
  on the same workspace. Every layout choice must pass §5.

## 3. Old clients facing a new server with deployments in use

### 3.1 Four rules that keep old clients correct

**C1: the credential decides.** Every deployment distinction a client could
get wrong is resolved on the server.
- **Tile credentials** (frame token claim, instance token, terminal or agent
  session target) carry their deployment. A URL qualifier, if present, must
  agree with it (P12).
- **Humans** choose by URL. A bare URL is the primary.
- **Hints are not routing inputs.** `XBIN_DEPLOYMENT`, `xbin.deployment` and
  `X-XBin-Deployment` are information for code that wants it.

So a client that knows nothing about deployments still reaches its own
deployment's backend, data, vault and registrations. That covers old bx, old
SDK builds and code written against today's docs.

**C2: old event types carry only the primary.** `reload`, `build-start`,
`build-error`, `build-ok` and `status` are emitted for the primary exactly as
today, with the bare `component`. Non-primary activity is emitted under the
new `deployments` type.
- **`status` and `reload`: must.**
  [11-contract.md](11-contract.md) §3.2 already routes non-primary status
  through `deployments`. `reload` must follow, because of the ancestor case
  below.
- **`build-*`: should.** Shipped consumers only refetch on a qualified
  `build-ok`. But `xbin.events` is a public stream (docs/sdk.md), and
  third-party consumers of it can't be audited.

A qualified `component` string does not protect old clients on its own:
- **Ancestor frames claim it.** bx-frame counts
  `e.component.startsWith(this.src + '/')` as its own
  (`web/bx-frame.js:366-373`). `isReloadTarget` picks the longest covering
  `src` (`web/events-socket.js:40-49`). The shipped app does the same
  (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:162-166`).
  A `reload` for `apps/shop/admin+dev` would therefore reload an open
  `apps/shop` frame, even while `apps/shop/admin`'s own frame is mounted:
  that frame's `src` never covers the qualified string, so the ancestor is
  the most specific covering frame.
- **The old shell's status handler matches nothing.** Any `status` event
  enters its status map. It sets the browser-title mark for every user and,
  when transient, raises a toast
  (`workspace-template/shell/bx-shell.js:1257-1266`, `:1278-1283`).
- **bx-code refreshes on prefix matches** of `reload` and `build-ok`
  (`web/bx-code.js:267-272`).

`grants`, `pr`, `term` and `session` stay tile-scoped. Sessions and proposals
belong to the tile, so they keep the bare component and gain fields only.

`bus` events keep their canonical topic in every data namespace. Delivery is
filtered by namespace ([09-fabric.md](09-fabric.md) §5.10), so a primary's
frames never see a non-primary deployment's publishes. The exception is admin
principals, which receive every namespace (11-contract §3.2,
`internal/server/server.go:606-607`), and an old consumer running as an admin
can't tell the namespaces apart. No shipped consumer is affected:
- the shell ignores `bus` events (`workspace-template/shell/bx-shell.js:194-201`);
- the admin console reads bus counts from `/runtime`
  (`workspace-template/tiles/admin/tabs/runtime.js:79-89`).

A third-party tile with xbin capabilities that subscribes to a topic would see
both namespaces' events; the changelog entry says so.

Non-bus events reach every subscriber except `pr`, `term` and `session`
(`internal/server/server.go:592-610`). No old consumer depends on the new
types, so they can be filtered to the tile's writers from day one
([06-security.md](06-security.md)).

**C3: tile-level reads describe the primary.** An existing read that names a
tile without a deployment selector answers:
- for the primary, when the caller is a human or an admin;
- for the caller's own deployment, when the caller is a tile credential (C1).

This applies to `/c/<tile>/`, `?native=1`, `/components` (including `runtime`
and `native`), `/tile-status`, `/logs`, `/backends`, `/runtime`,
`/tile-report`, `/cron`, and the vault and kv routes. A request that carries
no credential (a legacy-mode subresource, `docs/elements.md:297-299`) gets
the primary.

Lists keyed by tile keep one entry per tile, because old code counts or
matches them by path:
- `/backends` is counted by the old shell's footer
  (`workspace-template/shell/bx-shell.js:1300-1333`).
- `/runtime.backends` is matched by path: first match in the tile popover
  (`workspace-template/shell/bx-tile-admin.js:153-154`), last match in the
  admin runtime tab (`workspace-template/tiles/admin/tabs/runtime.js:338`),
  first match in `/tile-status` itself (`internal/boot/api.go:102-107`).

Non-primary rows go in new fields, never as extra keys or rows.

**C4: deployment URLs never enter state that old clients read as tile paths.**
That state is `/components` rows, screen layouts (the app de-duplicates
`tilePaths` by path,
`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Catalog.swift:185-204`),
`defaultTiles`, notify links and push payloads. Non-primary notifications are
never pushed (P13).

### 3.2 Old workspace-owned shells

Shells, menus, cards and the tile popover are workspace-owned; upgrades never
rewrite them (compat rule 4). Against a tile with deployments:

| Surface | What the old code does | Why it stays correct |
|---|---|---|
| Sidebar, cards, open-tile menu | one row or card per `/components` row | C4: deployments are never rows |
| Status dot, tab tint, title mark, toasts | `status` events and `/tile-report` | C2 and C3: only the primary's status reaches it |
| Frame reloads | `reload` through bx-frame | C2. Every deploy, promote, roll back or reload now onto the primary emits today's `reload` for the bare path, so primary viewers reload exactly when the primary's code changes, and on no save while it is pinned |
| Lifecycle items | `state !== 'enabled'` offers Enable (`workspace-template/shell/menus.js:111-116`) | Paused live reload is not a lifecycle state, and `state` never gains a value (PO-14). ⏸ Disable still stops every deployment (model §11) |
| Footer "N / M running" | counts `/backends` values (`bx-shell.js:1326-1333`) | C3: one key per tile |
| ⇄ proposals | `/code/prs/summary`, per tile | Proposals stay per tile and land in the work tree |
| Tile-menu squares | open bx-frame layouts | The binary-served window carries the deployments UI; the old menu simply lacks a square for it |
| `xbin.window` from a frame | `[d.from, spec.path].join('/')` (`bx-shell.js:677`) | Unchanged for primary frames. From a frame whose `src` is `<tile>+<name>`, it yields `<tile>+<name>/<sub>`, which resolves inside that deployment, because the qualifier belongs to the tile's last segment |

### 3.3 Old bx

An old bx meets a new server in two ways.
1. **The common case: every isolated terminal whose layer predates the
   release.** The rootfs ships its own `/usr/local/bin/bx`
   (`docker/rootfs.Dockerfile:166`), and the rootfs `PATH` wins in sandboxed
   terminals (`internal/term/term.go:745`, `:757-758`). A layer stays pinned
   to the base it was built on until someone takes "⬆ base update"
   (`docs/overview/09-terminals.md:336-360`).
2. **A host or laptop bx from an older release**, talking to the server
   through `XBIN_URL` or the owner token.

| Invocation | What old bx does | Result with deployments in use |
|---|---|---|
| `bx status` in a tile terminal | `GET /tile-status?component=$XBIN_COMPONENT` (`cmd/bx/main.go:349-372`) | The session's target deployment (C1, C3). The answer gains a `deployment` field that old bx doesn't print, so the user sees their target's status without a label |
| `bx status` on the host | `/backends` + `/status` (`main.go:361-369`) | One row per tile: the primary's (C3) |
| `bx logs <tile>` | reads `.xbin/log/<CompKey>.log` directly (`main.go:494-515`) | `main`'s log, since storage follows the deployment (P6). This already fails in isolated terminals, where `.xbin` is masked (`internal/term/binds.go:49-53`). Non-primary logs are reachable only through `GET /logs` |
| `bx logs <tile>+<name>` | the key `CompKey("<tile>+<name>")` | "no logs yet": non-primary logs are never stored under that key (PO-2) |
| `curl $XBIN_URL/api/$XBIN_COMPONENT/…`, `bx api` | the proxy, with the terminal token | The session's target (C1; model flow C) |
| `bx cron …`, `bx vault …` | self-scoped routes, with the terminal token | The target's cron set (dormant when non-primary) and vault (model §7, §9) |
| `bx enable\|disable\|hide\|unhide\|offload\|backup\|restore` | lifecycle and backup routes | Unchanged: these belong to the tile (model §11) |
| `bx code pr …`, `bx builtin …`, `bx template …` | work-tree operations | Unchanged. The result reaches only the live reload target (model §11) |
| Creating a tile whose name contains `+` | creation routes | §7: refused for the narrow collision, warned otherwise. Old bx ignores the `warnings` field; the warning still reaches the server log and the changelog |
| `bx deploy`, `bx promote`, `bx deployment`, `bx live-reload` | unknown command: usage, exit 2 (`cmd/bx/agent.go:44`) | The terminal window and a current bx are the way in |

Old bx ignores `XBIN_DEPLOYMENT`, and nothing depends on bx reading it (C1).

### 3.4 Shipped iOS apps

Shipped apps lag months behind xbind (compat rule 10).
- **Events.** New types parse to `.other` and are ignored
  (`Events.swift:43-44`, `:79-80`). `reload` and `status` only ever describe
  primaries (C2), so a non-primary build can never reload an open view
  (`native/ios/App/Model/WorkspaceEvents.swift:243-245`).
- **Tile list and layouts** come from `/components` rows only (C4); `isListed`
  is unchanged (`Catalog.swift:111-113`).
- **Pages.** `/c/<tile>/` and `/c/<tile>/?native=1` are the primary's (C3,
  NP-12-5). A pinned primary serves its checkpoint; the app can't tell and
  needn't.
- **Frame tokens** are opaque to clients (`docs/protocol.md:34-38`). A
  primary document's token has a sixth field only when the primary is not
  `main`.
- **Deployment URLs** are never sent to the app (C4). If one is pasted,
  `tile(forPath:)` matches on `/` boundaries
  (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/TileLoading.swift:74-79`),
  so `/c/apps/crm+dev/` maps to no tile, or to an ancestor tile if one exists.
  The server's `write` gate still applies.
- **Push notifications and Live Activities** come from primaries only (P13).

### 3.5 Old tiles and old SDK builds

A backend compiled against an older `sdk/`, or a frontend written against
today's docs, sees no change while it runs as a primary (Z1 applies to
everything but the code it runs). Running as a non-primary deployment:

| Pattern | Where it is documented | Why it keeps working |
|---|---|---|
| `XBIN_COMPONENT`, `Self()`, `xbin.self` return the tile path | `sdk/xbin.go:81-82`, `web/xbin-client.js:50` | Unchanged in every deployment. The deployment is not an identity (P2) |
| Vault URLs built from `Self()` | `sdk/xbin.go:264-317` | The server picks the deployment's vault from the instance token (C1) |
| Canonical `res:<scope>/<name>` ids, from env or built by hand | docs/resources.md | The ids stay identical; the broker maps them into the deployment's namespace by credential (model §9) |
| `XBIN_RES_*` paths for fs and sqlite | `internal/broker/resources.go:81-99` | Identical values: the launch spec binds the deployment's namespace at the primary's paths (model §12) |
| `xbin.Subscribe` at every start; `PUT /iface-instances`; `PUT /ingress-hosts` | docs/sdk.md, docs/protocol.md | They succeed and are stored dormant (model §7), with today's response shapes |
| `xbin.Status`, `xbin.Notify`, `NotifyUser` | docs/sdk.md | They succeed. Status is namespaced; notifications are held as "would notify" (P13) |
| `/api/${xbin.self}/…` through `xbin.fetch` or `selfApi` | `web/bx-kit.js:41` | The frame token's claim routes to the document's deployment (P12) |
| A raw `fetch('/api/<you>/x')` in origins mode | `docs/elements.md:280-282` | Only with the deployment's own origin (NP-12-4) |
| Absolute `/c/<self>/…` references in legacy mode | `docs/elements.md:297-299` | Loaded without a credential (module imports, `<script src>`, CSS), they get the primary's files. This is the one pattern that mixes deployments; it is flagged, not fixed (§2, tile asset gating) |
| Providers checking `X-XBin-From` | docs/protocol.md, identity headers | A non-primary deployment acts as its tile's principal: `X-XBin-From` is still the tile path, plus `X-XBin-Role: reader` under `read` |

**Care: providers that never check the role.** The proxy decides only
"allowed" and "which role" (`internal/proxy/proxy.go:165-173`); what a role
may do is the provider's check. A provider that ignores `X-XBin-Role` accepts
writes from a read-clamped caller. [09-fabric.md](09-fabric.md) sets the
defaults for edges that can't be read-clamped. A tile manager's `block` is
the backstop, and a non-primary deployment stays an accident boundary (P20).

### 3.6 Old admin and manager tiles

- **Admin runtime tab and tile popover:** one `/runtime` row per tile, the
  primary's (C3).
- **Admin components tab** (the D112 sandbox column): one entry per
  `/components` row, showing the primary's mode.
- **Admin sandboxes tab** (D112,
  `workspace-template/tiles/admin/tabs/sandboxes.js`).
  - *Correct:* it groups every entry under its tile (`sandboxes.js:199`),
    non-primary generations included. They are the tile's sandboxes and are
    charged to it (model §12).
  - *Mislabelled:* it takes the current generation as the highest `gen` in
    the group (`sandboxes.js:220`) and marks lower ones "draining" (`:239`).
    Generations count per deployment. With `dev` at g7 and `main` at g3, a
    tab that hasn't been updated labels `main`'s serving generation
    "draining". Stats rows of a tile with a record carry
    `scope: "deployment"` ([07-runtime.md](07-runtime.md) §10.1), which the
    old tab shows on every row of that deployment (`:238`), so a blue/green
    pair repeats them.
  - This is cosmetic and admin-only. NP-12-8 fixes it forward.
- **VM budget view:** `usedBy` stays keyed by tile
  (`internal/boot/sandboxes.go:78`). Every deployment's VMs are booked to the
  tile (model §12), so the old view shows the tile's true total.
- **Failure ring:** non-primary sandbox failures appear under the tile. The
  new `deployment` field is ignored, so they carry no deployment name.
- **Backup tab:** the guided disable → back up → offload flow works
  unchanged. The server's archive contents grow (model §11).
- **Manager tile.**
  - Create, clone, new from template, builtin import and git import all
    produce zero-state tiles (model §11). Clone's promise "Secrets and stored
    resource data don't come along"
    (`workspace-template/tiles/manager/index.html:113-118`) stays true.
  - The Updates tab writes the work tree, which now reaches only the live
    reload target. An old manager tile reports "updated" while a pinned
    primary still runs its checkpoint. The terminal window shows how far the
    work tree has moved, and the changelog entry says so.
- **Organisations tile:** unchanged.

## 4. New clients facing an old server

### 4.1 Feature detection and selector hygiene

An older xbind has no deployments routes. Its API mux answers an unknown
`/api/xbin/…` path with net/http's plain-text 404, never the JSON error shape
(`internal/server/server.go:112-118`, `:563-583`). New clients follow four
rules:

- **NC-1: detect by the endpoint** (NP-12-2). A new client calls the deployments
  endpoint and expects its JSON answer. A plain 404 or a 405 means "no tile
  deployments on this xbind". No version string and no `/whoami` field is
  needed.
- **NC-2: confirm every selector sent to an existing endpoint.** Older servers
  ignore unknown query parameters and silently answer for the tile. For
  example, `/tile-status` reads only `component` (`internal/boot/api.go:87-99`).
  A client that sends a deployment selector to an existing endpoint
  (`/tile-status`, `/logs`, `/ws/term`) must find the deployment echoed in
  the answer:
  - a `deployment` field in a JSON answer;
  - the session frame, for `/ws/term`;
  - a response header, for the `/logs` stream.

  An absent echo means the feature is missing. The client must never display
  that answer as the deployment's.
- **NC-3: never send a new field in an existing JSON body** before NC-1 has
  passed. `DecodeJSON` makes it a 400 (`internal/server/api.go:67-76`): safe,
  but unhelpful.
- **NC-4: never send `<tile>+<name>` in a query string.** `+` decodes to a
  space. Use a separate `deployment` parameter (glossary, Spellings).

### 4.2 A current bx against an older xbind

- New commands fail with one message, "this xbind has no tile deployments;
  upgrade xbind to use `bx deploy`", and a distinct non-zero exit code
  (`TestBxOldXbind` in [15-test-plan.md](15-test-plan.md)). There is no
  fallback and no partial action.
- `bx status` and `bx logs` with a deployment flag fail with the same message
  (NC-2).
- Plain invocations behave as today's bx did against that server.
- An older xbind never sets `XBIN_DEPLOYMENT`, so a current bx in its
  terminals addresses the tile as it always has.

### 4.3 The terminal window against an older xbind

The window ships inside the binary, served from `/vendor/` with
`Cache-Control: no-cache` (`internal/server/static.go:585-606`), so it matches
the server it came from. The one exception is a browser tab loaded before the
server was replaced by an older binary: its code is newer than the server
until the page reloads.

- **NC-5.** The window fetches deployment state when it opens, and again after
  every events-socket reconnect: a reconnect may mean a different binary. On
  an NC-1 failure it drops cached state and renders the zero-state bar, with no
  controls and no error banner.
- **NC-6.** The persisted window pref (`term:<tile>`,
  `web/bx-frame.js:305-312`) writes `layout` only with values every older
  frame knows: `term`, `code`, `split`, `logs`, `prs`. The deployments panel
  is recorded in a new field (NP-12-3). An older frame restores `layout`
  verbatim (`bx-frame.js:328`) and, for an unknown value, renders an empty
  window body while sessions exist (`bx-frame.js:671`, `:839-845`).

### 4.4 Newer SDK and client code

- `xbin.Deployment()` reads `XBIN_DEPLOYMENT`, which is set only for
  non-primary deployments. Absent means "the primary"; under an older xbind it
  is always absent, which is correct.
- `CallerInfo.Deployment` comes from `X-XBin-Deployment`. An absent header
  means the caller is not a non-primary deployment.
- `xbin.deployment` in xbin-client comes from a meta that only non-primary
  documents carry, so it is absent under an older server.
- None of these may error when the signal is absent.

## 5. Downgrade: an older xbind on a workspace with deployment state

An older binary knows one runtime per tile. Downgrade is supported in the
sense that it **loses no state and activates nothing the new binary had kept
dormant**. It is not free: pinned primaries become live again.

### 5.1 What an older xbind ignores

| State | Where | Why an older xbind never reads it |
|---|---|---|
| Deployment record | `data/deployments/<CompKey>.json` | No older code path builds `data/deployments` (see the list below). `Rescan` skips `data/` (`internal/registry/registry.go:443-445`, `internal/util/util.go:67-70`), and so does the watcher (`internal/watch/watch.go:61-67`) |
| Per-deployment registration files | `data/deployments/<CompKey>/…` (model §3) | Same |
| Checkpoint store | `data/checkpoints/<CompKey>.git` | Same. It is not a registered component, so no `/git/*` route and no repository housekeeping opens it |
| Non-`main` namespaces | a dot-prefixed level above today's keys ([08-data.md](08-data.md); NP-12-12) | Older key functions never produce a dot-prefixed level (PO-2). kv buckets are opened by exact `res:` name (`internal/broker/backup.go:332`) |
| Non-`main` vaults | under `data/vault/`, below a dot-prefixed directory | `vaultPath` builds only `data/vault/<CompKey>.json` (`internal/broker/vault.go:45-47`). `migrateVaults` skips directories (`vault.go:115-123`) |
| Materialized checkpoints | `.xbin/deploy/<CompKey>/<id>/` | `.xbin` is dot-prefixed, so `Rescan` and the watcher skip it (`registry.go:440-442`, `watch.go:68-70`). The `xbin.json` and `index.html` inside never register as tiles |
| Per-checkpoint artifacts | `.xbin/build/<CompKey>/…`, beside today's `bin` | The older runner uses only `.xbin/build/<CompKey>/bin` (`internal/runner/runner.go:372`) |
| Per-deployment logs, sockets, cgroup leaves | new names | Older code uses `.xbin/log/<CompKey>.log`, `.xbin/run/<CompKey>/g<N>.sock` (`runner.go:414-418`) and `comp-<CompKey>` (`internal/cgroup/cgroup_linux.go:89`) |
| Deployment sections in backups | a new tar prefix (NP-12-10) | The restore switch has no default arm, so unknown entries are skipped (`internal/broker/backup.go:242-280`) |
| Non-`main` archives | their own archive key (model §11; NP-12-10) | Their manifest schema exceeds today's `1`, which older readers refuse (`internal/backup/backup.go:28-29`, `:165-167`) |
| Frame tokens with a deployment claim | a sixth field (PO-6) | Older verifiers accept four or five fields only (`internal/auth/frametoken.go:255-266`) |

**Completeness.** These are all the `data/` paths daemon code builds
(re-verified with `rg '"data"' internal cmd`):

| File:line | Path |
|---|---|
| `internal/broker/cron.go:77` | `data/cron-jobs.json` |
| `internal/broker/bussubs.go:161` | `data/bus-subscriptions.json` |
| `internal/broker/vault.go:46`, `:116` | `data/vault/` |
| `internal/obs/prefs.go:47` | `data/prefs/` |
| `internal/term/history.go:50` | `data/agent-history/` |
| `internal/resenc/resenc.go:85` | `data/resources-enc/` |
| `internal/broker/prs.go:86` | `data/prs/` |
| `internal/broker/screens.go:81` | `data/screens.json` |
| `internal/broker/resusage.go:123`, `internal/broker/resources.go:39`, `internal/broker/backup.go:48` | `data/resources/` |
| `internal/broker/backup_cron.go:26` | `data/backup-schedule.json` |
| `internal/boot/push.go:18` | `data/push/` |
| `internal/boot/boot.go:184`, `:384`, `:673` | users, backfills, branding |

None is `data/deployments` or `data/checkpoints`. Only two directory walks run
under `data/`:
- `migrateVaults` skips subdirectories (`vault.go:121-123`);
- the agent-history listing walks one level of directories and reads `.json`
  files (`internal/term/history.go:150-165`).

Per-deployment files must therefore sit where neither walk lists them
(NP-12-12).

### 5.2 What an older xbind does

- **It serves the work tree for every tile, at the bare URLs.** The static
  plane serves the tile directory (`internal/server/static.go:70-180`). The
  runner builds from it into `.xbin/build/<CompKey>/bin`
  (`internal/runner/runner.go:372`). **Pinned primaries become live again.**
- **Live reload returns for every tile.** Each save reloads frames and
  rebuilds the backend (`internal/boot/serve.go:175-182`), including in tiles
  where live reload was paused or attached to a non-primary deployment.
- **It uses `main`'s data, vault, logs and registrations for every tile**
  (today's keys, P6). That includes tiles whose primary had been reassigned.
- **It fires only `main`'s registrations** (§5.3). Non-`main` registrations
  stop, including those with deliveries on.
- **It resolves deployment URLs by today's longest-prefix walk**
  (`internal/registry/registry.go:517-536`). `/c/<tile>+<name>/` and
  `/api/<tile>+<name>/…` answer 404, or reach an ancestor tile's sub-path if
  an ancestor tile exists.
- **It rejects six-field frame tokens** (§5.1). `main`'s five-field tokens
  keep verifying, since the HMAC key in `.xbin/secret` is shared
  (`docs/protocol.md:2417-2418`), so open primary frames survive the change of binary.
- **It drops a tile whose work tree lacks both `xbin.json` and `index.html`**
  (`registry.go:471-474`), even if its primary was pinned and serving (model §6).
- **It lists one backend per tile** in its D112 registry: what it runs.
- **It offers no checkpoint remote.** `git fetch xbin-deploy` fails. With
  NP-12-7 no remote definition lingers in the tile's repository.
- **It creates tiles with `+` in their names without warning**; it predates
  the reservation.

### 5.3 Proof: no non-`main` registration activates under an older binary

1. **The registration stores an older binary loads are exactly these:**
   - `data/cron-jobs.json` (`internal/broker/cron.go:76-94`);
   - `data/bus-subscriptions.json` (`internal/broker/bussubs.go:104-115`);
   - the root `xbin.json` maps `ifaceInstances` and `ingressHosts`
     (`internal/registry/registry.go:218-228`), read by `Rescan`
     (`registry.go:481-486`);
   - the `CronJobs` and `BusSubs` of any backup it restores
     (`internal/broker/backup.go:282-303`).

   alwaysOn comes from the work tree's manifest. Nothing else activates a
   registration.
2. **Non-`main` registrations never enter those stores** (PO-9), nor a backup
   manifest's `CronJobs` or `BusSubs` (NP-12-10). They live in per-deployment
   files under `data/deployments/<CompKey>/` (model §3), which no older code
   path opens (§5.1).
3. **An older binary rewrites these stores only from its in-memory set**
   (`cron.go:96-106`, `bussubs.go:165-182`, `registry.go:584-595`). It loaded
   only `main`'s rows, so it writes only `main`'s rows, and never touches the
   per-deployment files.
4. **Therefore** the registrations active under an older binary are exactly
   `main`'s. After re-upgrade, every per-deployment file is byte-for-byte what
   the new binary left.

This is checked in two ways. `TestOlderBinaryIgnoresDeploymentFiles` (PO-9)
needs no older binary. `TestDowngradeStatic` and
`TestDowngradeDormantRegistrations` start the real previous release on the
same workspace (`XBIN_DOWNGRADE_BIN`, [15-test-plan.md](15-test-plan.md) §5.6).

**Why not a `deployment` field inside today's files**, as
[research/data-plane.md](research/data-plane.md) considered. An older binary
unmarshals rows into structs without the field (`cron.go:31-38`,
`bussubs.go:55-62`) and keys them by `component\x00name` (`cron.go:108`,
`bussubs.go:158`). A non-`main` row would then:
- (a) fire at the tile's primary;
- (b) collide with `main`'s row of the same name, one of them silently lost;
- (c) lose its `deployment` field for good on the next persist, and be
  indistinguishable from `main`'s after re-upgrade.

The model's per-deployment files rule out all three.

### 5.4 What is at risk

| # | Risk | When | Severity | Mitigation |
|---|---|---|---|---|
| R-1 | Unreviewed work-tree code serves primary traffic against `main`'s data | A pinned primary whose work tree moved past its checkpoint | High: irreversible if that code migrates `main`'s data | Checklist step 2: check out `deploy/<primary>` in the work tree first |
| R-2 | Traffic switches data | A tile whose primary is not `main` (flow F) is served from `main`'s data | High: users see older data, and writes diverge from the primary's namespace, which becomes invisible | Checklist step 1: never downgrade with a reassigned primary |
| R-3 | Non-primary deployments stop: deployment URLs 404, deliveries and alwaysOn stop | Any tile with one | Low: nothing is lost, and all of it resumes on re-upgrade | none needed |
| R-4 | Saves go live again | Tiles with live reload paused or attached to a non-primary deployment | Medium. Sessions end at the restart (`docs/overview/09-terminals.md:113-114`), but a resumed agent transcript (D75) still believes its saves reach a non-primary deployment | Checklist step 3 |
| R-5 | A tile vanishes | A pinned primary whose work tree lacks `xbin.json` and `index.html` | Medium | Checklist step 2 restores the files |
| R-6 | Cross-owner inheritance. The older leftover check (`internal/broker/policy.go:211-258`) knows no deployment state, so another owner can re-create a removed tile's path; after re-upgrade, the new tile would find the old record, checkpoints (code history) and namespaces | Removal and re-creation during the downgrade | High (disclosure), rare | NP-12-9 |
| R-7 | A `+` path created under the older binary shadows a deployment URL after re-upgrade, since exact match wins | `<tile>+<name>` created while `<tile>` has deployment `<name>` | Low | The new binary reports the collision (NP-12-13) and never renames anything |
| R-8 | An older binary restores a backup the new binary made | Restore during the downgrade | Low. Deployment sections are skipped; after re-upgrade the record may describe code newer than the restored work tree | NP-12-10. The terminal window shows the difference. No action at boot |
| R-9 | Updated scaffold code imports a `/vendor/` module the older binary doesn't serve, and a failed static import breaks the whole module graph | A workspace that took `bx builtin update` on the new binary | Medium | Scaffold code loads deployment modules with dynamic `import()` and a fallback |
| R-10 | An older frame restores an unknown window layout | A window left on the deployments panel | Low | NC-6, NP-12-3 |
| R-11 | An older offload or removal leaves non-`main` namespaces and the store behind | Offload or delete during the downgrade | Low: leftovers are never inherited (NP-12-9) | The new binary lists them as leftovers |
| R-12 | Non-`main` encrypted mounts outlive an unclean stop | A crash just before the downgrade | Low: they sit under `.xbin/resenc/`, masked from terminals (`internal/term/binds.go:49-51`) | Stop the new xbind cleanly; its shutdown unmounts non-`main` mounts |

### 5.5 Operator guidance

**Before starting an older xbind:**
1. **List** every tile with deployment state (`bx deployment ls --all`; the
   final spelling is [11-contract.md](11-contract.md)'s). If any tile's
   primary is not `main`, reassign it to `main`, or don't downgrade. Data
   moves in neither direction.
2. **Align each pinned primary's work tree with what the primary runs.** In
   the tile's terminal:
   - `git fetch xbin-deploy`;
   - commit or stash unfinished work on a branch;
   - `git checkout -b pre-downgrade deploy/<primary>`, as in flow H.

   The older xbind then serves the primary's code, not the unfinished work.
   Files the work tree holds but the checkpoint lacks (untracked build
   output, for example) stay where they are; remove them if they matter.
3. **Tell agents** that saves go live on every tile until the upgrade.
4. **Stop the new xbind cleanly.** That ends every session and unmounts
   non-`main` mounts.

**After upgrading again:**
5. Each pinned primary returns to its checkpoint, which may be older than
   what the older binary served. `bx doctor` lists every tile whose work tree
   differs from its primary's checkpoint (NP-12-13).
   - **Step 2 skipped:** the older binary served the work tree, so deploy the
     work tree to the primary at once (`bx deploy <tile> --to main`). Code
     then matches what `main`'s data last met. Roll back deliberately only
     after that.
6. Check out the unfinished branches again, which reverses step 2.

The changelog entry's downgrade note carries steps 1, 2 and 5 (§10.4).

**Considered and rejected:** making an older binary refuse to start, for
example by planting a terminal layer pinned to a missing base so that its
base-image gate stops it (`docs/overview/09-terminals.md:348-351`). That
abuses an unrelated safety gate with a misleading message, and an operator
would "fix" it by resetting a terminal.

### 5.6 After a downgrade: what the new binary finds

| Situation | What the new binary does |
|---|---|
| Nothing unusual | Records, stores, per-deployment files and namespaces are as it left them (§5.3). `main`'s registrations and data carry whatever the older binary did to them |
| A tile removed under the older binary | Its record, store and namespaces are leftovers (model §11). They are never resurrected, and stay inert unless an owner-matching tile reappears (NP-12-9) |
| Ownership transferred under the older binary | The owner check makes the record inert (the safe direction) until an admin adopts it |
| A tile moved or renamed under the older binary | It starts in the zero state at its new path. Its state stays keyed by the old path, as leftovers |
| A `<tile>+<name>` directory created meanwhile | Exact match wins; the collision is reported (§7.3) |

## 6. Legacy-fixture expectations

### 6.1 The aged workspace: no state, no migration

**Unchanged:**
- both allowed-path lists (`test/legacy_workspace_test.go:74-83`,
  `internal/boot/boot_test.go:166-173`);
- the root `xbin.json` byte check (`legacy_workspace_test.go:70-72`,
  `boot_test.go:163-165`);
- the second-boot check (`legacy_workspace_test.go:90-95`, `boot_test.go:180-184`).

**Added to both fixtures:**
- After each boot, none of `data/deployments`, `data/checkpoints`,
  `.xbin/deploy` or a dot-level namespace root exists.
- `data/cron-jobs.json` and `data/bus-subscriptions.json`, when present, are
  byte-equal before and after. `data/` is in the allowed list, so today's
  assertions would let a migration rewrite them silently.
- No tile repository gains a remote, ref or config entry. Otherwise the
  `/.git/` wildcard (`legacy_workspace_test.go:82`) would hide it.

The feature extends no allowed list. Compat rule 9 is satisfied by having no
boot migration.

### 6.2 Booting twice with deployment state present

`TestDeploymentStateBootsTwice` (`test/deploystate_boot_test.go`) and
`TestDeploymentStateBootsTwiceInProcess` (`internal/boot/deploystate_test.go`)
are specified in [15-test-plan.md](15-test-plan.md) §6. Its setup (a paused
static tile, a static `dev` deployment, an edge policy, a dormant job
registered with a `dev` credential) creates the state with the feature itself.
This document requires the following of them.

**Assertions.**
- Boot 1 and boot 2 change nothing under `data/deployments/**` or
  `data/checkpoints/**`, nothing in `data/cron-jobs.json` or
  `data/bus-subscriptions.json`, and nothing in the root `xbin.json`.
- Derived trees (`.xbin/deploy/`, `.xbin/build/`) may be rebuilt. They are
  filtered from the comparison, as the file map already skips `.xbin/build`.
- Every tile repository's refs, `HEAD` and `config` are compared explicitly.
- After boot 1, `main`'s cron job is scheduled and `dev`'s is dormant.
- The integration twin should also cover a Go tile with alwaysOn. Boot then
  builds and materializes a pinned checkpoint, the one boot path that writes
  derived deployment state.

### 6.3 What boot may and may not do with deployment state

| Boot may | Boot must not |
|---|---|
| Read records, validate invariants, and surface problems in the log, `bx doctor` and the deployments panel | Rewrite, "repair", migrate or delete a record, a store or a per-deployment file |
| Materialize a pinned deployment's checkpoint under `.xbin/deploy/` when something needs it (alwaysOn, a request): content-addressed, written once | Write anything under `data/` whose bytes depend on time, randomness or iteration order |
| Rebuild per-checkpoint artifacts under `.xbin/build/` | Run `git gc`, repack, prune or re-initialize the checkpoint store |
| Refresh the checkpoint remote's dumb-HTTP files when the refs changed | Add remotes, refs or config to tile repositories |
| Activate registrations as the record says | Move non-`main` rows into today's stores, or `main`'s rows out of them |
| Treat a record whose tile directory is gone as leftovers | Resurrect it or delete it |

## 7. Warn before error

### 7.1 `+` in new tile names, for non-admin creators

**Today** nothing stops a non-admin from creating `apps/a+b`:
- `util.ComponentPathOK` has no charset rule (`internal/util/util.go:97-114`);
- among characters, D82's path rule refuses only `:`
  (`internal/broker/policy.go:162-166`);
- every API creation path goes through `canCreateAt`
  (`internal/broker/create.go:54`, `clone.go:54`, `templates.go:108`,
  `tiles.go:73`, `gitimport.go:143`).

The reservation has two parts, handled differently (NP-12-11).

**(a) The narrow collision is refused at once.** This covers `<P>+<N>` where
`<P>` is an existing component and `<N>` fits the deployment-name grammar. The
refusal applies to every creator, admins included (§7.2), from the release
that ships deployments.
- *Why no warning:* such a tile would sit at the URL that `<P>`'s writers use
  for `<P>`'s deployment `<N>`, and would take it by exact match. That is a
  spoofing hole the feature itself creates.
- The repo's `AGENTS.md` makes the exception explicit: a silent path that
  must become an error warns for one release first, "except a security hole:
  that closes now, with a changelog entry" (`AGENTS.md:112-113`).
- The deployments API adds the mirror rule: adding deployment `<N>` to `<P>`
  is refused while a path `<P>+<N>` exists.

**(b) Any other `+` in a new name warns in release R and is refused in R+1.**
- In release R, creation succeeds. The response carries an additive
  `warnings` entry (precedent: `internal/broker/admin.go:116`), and xbind
  logs the creator.
- In R+1, non-admins are refused the D82 way, with D82's hint style
  (`policy.go:157`, `:164`): "can't create <path>: '+' is reserved in tile
  names (it selects a tile deployment in URLs) — pick another path, or ask a
  workspace admin".
- Existing directories keep resolving in both releases, because exact match
  wins (PO-1).
- The warning reaches new clients only. Old shells and old bx ignore unknown
  fields, and the changelog carries it for everyone else.

### 7.2 Admin creation of names containing `+`

- Admins bypass D82's path rule (`policy.go:106-108`). They keep bypassing
  the general reservation: they get the warning in every release, never the
  refusal.
- The narrow collision (a) is refused for admins too, through the API. The
  check sits before `canCreateAt`'s admin early return. An admin who needs
  the name removes the deployment first; names are immutable, so it cannot
  be renamed.
- Anyone with a terminal can still `mkdir`. `Registry.Rescan` registers any
  directory holding `xbin.json` or `index.html`
  (`internal/registry/registry.go:456-474`). See §7.3.

### 7.3 Directories created outside the API

- Exact match wins: the directory becomes a tile, and its URL shadows the
  deployment URL.
- The new binary never renames anything and never refuses at boot; there is
  no migration. It reports the collision: `bx doctor` names it, and the
  deployments panel marks the deployment "URL shadowed by tile `<P>+<N>`".
- The shadowed deployment's data, registrations and target sessions are
  untouched. It is unreachable by URL until the tile moves or the deployment
  is removed.

### 7.4 Nothing else becomes an error for zero-state tiles

Every refusal the design introduces requires an opt-in first:
- cgi excluded from pinning (P18);
- isolation required for pinned or non-primary backends (P18);
- no non-primary deployments on chrome and xbin-capable tiles (P19);
- cross-deployment self-calls refused (P12);
- protected-primary refusals (P21);
- per-tile and per-workspace caps.

The only change a user of zero-state tiles can meet is §7.1.

## 8. Vocabulary updates to existing docs

Rules: glossary rules 2 and 4, and the repo `AGENTS.md` instruction to grep
every statement of a contract (`AGENTS.md:132-139`). The save → live contract
is stated in the places that
[research/delivery-infra.md](research/delivery-infra.md) §4.2 lists.

| Where | Today | Change |
|---|---|---|
| `docs/overview/15-operations.md:1`, `:17` | "Deployment & operations", "The reference deployment" (the install) | Keep. Add one sentence to the intro: in this chapter a deployment is the xbin install, and a tile's own deployments are tile deployments |
| `docs/overview/00-index.md:83`, `:91` | "Operating a deployment" | The same qualification, where it sits next to builder links |
| `README.md:139` | "## Deployment on a VM" | Keep (the install). If the README's feature list gains the feature, it says "tile deployments" |
| `cmd/bx/main.go:220`, `workspace-template/tiles/admin/admin.js:94` | code comments using "deployment" for the install | None: comments are not builder-facing |
| `plans/deployment.md`; BU-2's "per-deployment" | historical records | None: historical records keep their words |
| `workspace-template/AGENTS.md:34-36` | "Saving any file live-reloads the frontend and rebuilds/swaps the backend. There is no deploy step" | Keep, and add "unless live reload is paused or attached to another tile deployment; the terminal bar says so". This reaches new workspaces only (§9) |
| `workspace-template/apps/welcome/index.html:55`; `website/index.html:376` | "No deploy step, no build step"; "Live, no deploy step." | Keep: true for every tile that never opts in (glossary rule 4) |
| `docs/index.md:4-7`, `docs/getting-started.md:85`, `docs/elements.md:356`, `docs/overview/03-components.md:208-228`, `docs/overview/04-frontend.md:112`, `README.md:57` | save → live statements | One qualifying clause each. Where a line is touched, "hot-reload" becomes "live reload" (`docs/index.md:6`) |
| `workspace-template/apps/welcome/notes.js:93-103` | a note titled "terminal ≠ deployment" (the backend's run-time sandbox) | Retitle it "terminal ≠ deployed backend" in the next scaffold version. Existing workspaces keep the old title, which stays true |
| `docs/bx.md:138` | "lifecycle: pause/resume a tile" | Keep, and add "(not live reload: see `bx live-reload`)". New bx.md entries never say "pause" unqualified |
| `workspace-template/AGENTS.md:474`, `:485` | "A save = a new process"; "marked failed until you save a change" | Qualify for pinned deployments ("until the next deploy"). New workspaces only; /docs/ carries it for all |
| `docs/protocol.md:2409-2430` (filesystem contract) | lists `data/` and `.xbin/` contents | Add `data/deployments/` and `data/checkpoints/` (still the backup unit), and `.xbin/deploy/` (derived) |
| `docs/elements.md:5`, docs/overview/05-identity.md | "the path is the identity" | Add that a tile deployment shares its tile's identity and principal (P2, P11) |

## 9. Agent guidance in existing workspaces

**The facts:**
- `AGENTS.md` is seeded only when absent (`internal/boot/boot.go:157-161`),
  through `seedTemplateFile`, which never overwrites
  (`internal/boot/workspace.go:67-86`).
- It is not a scaffold unit: units are directories holding `xbin.json` or
  `index.html` (`internal/builtins/updates.go:150-175`).
- So every existing workspace keeps today's text, including "There is no
  deploy step" (`workspace-template/AGENTS.md:35`). That stays true for
  zero-state tiles.
- `/docs/` is re-extracted into `.xbin/docs` at every boot
  (`internal/boot/boot.go:343-347`) and exposed as `XBIN_DOCS` (`:276`).
- Agents are told to read the changelog after upgrades
  (`workspace-template/AGENTS.md:15-18`).

**Therefore:**
- **G1: /docs/ and the changelog carry the explanation.** One builder page,
  named by [10-ux.md](10-ux.md) and [11-contract.md](11-contract.md), is
  linked from docs/index.md and docs/elements.md. The changelog entry stands
  on its own. Together they cover:
  - what paused live reload means for a save;
  - how to tell your target deployment;
  - how to test on a non-primary deployment and promote;
  - that committing deploys nothing.
- **G2: the product explains itself** to an agent that never read the page.
  - When a session's target is not the primary, or live reload is paused, the
    terminal prints a grey notice on attach naming the target and the doc
    page. The precedent is `netNote` (`web/bx-terminal.js:634-636`).
  - A current `bx status` prints the deployment and the live reload state.
  - Refusals, such as a protected primary's, name the policy and the page.
- **G3: zero-state sessions see today's env.** `XBIN_DEPLOYMENT` is set only
  when the target isn't the primary, so an agent in a zero-state tile sees
  exactly today's env vars (PO-3).
- **G4: CM-2 stays safe.** "Commit often and unprompted" remains good advice,
  because committing deploys nothing in v1 (model, flow C). The tracked-branch
  rung must keep that true, or change this guidance and the changelog in the
  same release.
- **G5:** the template's `AGENTS.md` gains a short section, for new workspaces
  only. Nothing relies on it.
- **G6:** the welcome notes are a scaffold unit. A one-line addition in a new
  scaffold version is optional; existing workspaces keep their notes.

## 10. Is anything BREAKING?

### 10.1 Verdict: no

The repo's `AGENTS.md` calls a change BREAKING when "existing tiles/providers
must change code or config, or a wire/persisted format changes incompatibly"
(`AGENTS.md:150-152`). By that definition nothing breaks:
- **No tile or provider changes code or config.** The feature is opt-in per
  tile, with no manifest key (P5).
- **No wire format changes incompatibly.** Routes, fields and event types are
  additive. The frame token stays opaque; `main`'s format is unchanged, and a
  new field appears only in non-`main` documents' tokens (PO-6).
- **No persisted format changes.** Today's stores keep today's rows; new state
  lives in new files (§5.3).
- **The one newly refused path**, §7.1(a), is a security closure that the
  rules allow without a warning release. The general reservation warns first.

No migration note is required, and the changelog entry is not marked
BREAKING.

### 10.2 Tripwires

Any of these would break existing workspaces: as a regression against Z1–Z3,
or as BREAKING in the repo's sense. Each needs a redesign, or failing that a
migration note:

| Tripwire | Why it breaks | Guard |
|---|---|---|
| Emitting `reload` or `status` for non-primary activity | Ancestor frames and the app's views reload; old shells toast and mark the title (§3.1, C2) | PO-5 |
| Adding rows to `/components` or `/runtime.backends`, or keys to `/backends` | Old shells and admin tiles count or match by path (C3) | PO-11, PO-14 |
| Changing `main`'s storage keys, or moving data at opt-in | Older binaries and existing backups would read the wrong place | PO-2 |
| Any manifest key | Compat rule 7; P5 | P5 |
| Changing `XBIN_COMPONENT`, `xbin.self` or `Self()` for any deployment | Vault URLs, `res:` ids and frame-token renewal break ([research/builder-contract.md](research/builder-contract.md) §0) | PO-3 |
| Non-`main` rows in today's registration stores or backup manifests | Older binaries fire them (§5.3) | PO-9 |
| Changing the frame-token format for `main`'s documents | Pages open across the upgrade stop verifying | PO-6 |
| A required field in an existing request body | Old clients get a 400 (`internal/server/api.go:67-76`) | PO-14 |
| Refusing `+` names generally without the warning release | Compat rule 11 | §7.1 |
| A lifecycle `state` value for paused live reload | Old shells offer "Enable" (`workspace-template/shell/menus.js:111-116`), and the proxy answers 409 (`internal/proxy/proxy.go:145-158`) | PO-14 |

### 10.3 If a tripwire must be crossed

Mark the changelog entry **BREAKING** and add
`docs/changes/YYYY-MM-DD-<slug>.md`, linked from the entry. The repo's
`AGENTS.md` requires exactly four sections (`AGENTS.md:150-155`). The title
follows the existing notes:

```markdown
# YYYY-MM-DD — <summary> (D<n>)

## What changed
## Who's affected
## How to migrate
## Why
```

### 10.4 Changelog entry: a draft for the integrator

The D-number is assigned at ratification. The downgrade note follows the
existing precedent (`docs/changelog.md:1667-1668`).

```markdown
- **Tile deployments and pausing live reload** (D<n>, <builder page>). Tiles
  that never opt in change in no way: saves still reload live and there is
  still no deploy step. For a tile you opt in (the terminal window, or
  `bx live-reload` / `bx deployment`): pause live reload and reload now;
  named deployments with their own data and vault at `/c/<tile>+<name>/`;
  deploy, promote and roll back. New for builders: `XBIN_DEPLOYMENT`
  (non-primary backends and sessions only), `X-XBin-Deployment` on calls from
  non-primary deployments, `xbin.Deployment()` and `CallerInfo.Deployment` in
  the SDK, the `deployments` event type and API, and new `bx` commands. Bus
  events from a non-primary deployment's data carry `deployment`; only admin
  principals receive every namespace's.
  **`+` in new tile names is reserved**: a tile named `<tile>+<name>` next to
  an existing `<tile>` is refused; any other `+` warns now, and is refused
  for non-admins from the next release; existing directories keep working.
  **Downgrade note:** an older xbind serves every tile's work tree with
  `main`'s data. Before downgrading, reassign primaries to `main` and check
  out each pinned primary's checkpoint (`git checkout -b pre-downgrade deploy/main`). After
  upgrading again, run `bx doctor`.
```

## Divergences from the model

1. **Model §8 overstates what prefix matching protects.** It says old clients'
   exact-or-prefix matching ignores events with `component: "<tile>+<name>"`.
   That holds for the tile's own frame only.
   - Ancestor frames claim such events (`web/bx-frame.js:366-373`,
     `web/events-socket.js:40-49`, `Events.swift:162-166`).
   - The old shell's `status` handler matches no component at all
     (`workspace-template/shell/bx-shell.js:1257-1266`, `:1278-1283`).

   The glossary's Spellings row ("old clients' prefix match ignores them")
   repeats the claim, and so do the sibling documents that build on it:
   - [07-runtime.md](07-runtime.md) §8.5 and
     [11-contract.md](11-contract.md) §3.2 keep non-primary `reload` and
     `build-*` on the old types, with the qualified component;
   - [09-fabric.md](09-fabric.md) §6 has non-primary `status` events and
     toasts use it. 11-contract §3.2 already overrides that: non-primary
     status rides the `deployments` type.

   A qualified `reload` breaks Z3. It reloads the nearest mounted ancestor
   tile's frame, and the app's open view of it, on every save to a nested
   tile's non-primary deployment, even when that ancestor tile is in the zero
   state.

   *Fix:* rule C2 (§3.1). Emit non-primary `reload` as a `deployments` event
   (e.g. `op: "reload"`), handled by the binary-served frame. Move `build-*`
   the same way unless 11-contract has a reason not to.
2. **"`<tile>+<primary name>` also works" conflicts with P5.** The glossary's
   rule would change what `/c/<tile>+main/` resolves to for every zero-state
   tile. Today it is a 404, or an ancestor tile's sub-path by longest prefix
   (`internal/registry/registry.go:517-536`; the static plane is path-based,
   `internal/server/static.go:127-130`). [09-fabric.md](09-fabric.md) §2.1
   applies the split, and its `no such deployment` 404, to every tile.
   *Fix:* NP-12-1.
3. **P17's wording differs from the glossary's.** P17 says the markers are
   "absent for `main`". The glossary sets `XBIN_DEPLOYMENT`,
   `X-XBin-Deployment` and the qualified event component by role
   (non-primary), and only the frame-token claim by name (non-`main`). The
   two coincide in the zero state but differ after a reassignment. *Fix:*
   reword P17 as "absent for the primary; the frame-token claim is absent
   for `main`".
4. **The model doesn't say what `/components` and `?native=1` describe for a
   pinned primary.** Today both come from the work tree
   (`internal/server/api.go:179`, `internal/server/native.go:54-56`, via
   `internal/registry/registry.go:471-473`), and shipped apps rely on the two
   agreeing (`docs/compat.md:123`). *Fix:* NP-12-5.
5. **Model §12's per-tile cgroup parent needs a zero-state exception.** Under
   cgroup v2, a parent whose children have controllers enabled cannot hold
   processes, so `main`'s processes leave `comp-<CompKey>`
   (`internal/cgroup/cgroup_linux.go:89`). Its `leaf` value on `/sandboxes`
   changes with them (`internal/runner/sbx.go:65-67`). Applied to every
   tile, that contradicts
   [research/sandbox-visibility.md](research/sandbox-visibility.md): "the
   admin tab and `/sandboxes` stay unchanged for zero-state tiles".
   [07-runtime.md](07-runtime.md) §10.3 already supplies the exception.
   *Fix:* NP-12-6 records it in the model.
6. **Model §11 gives non-primary backups "their own archive key" without the
   constraints that stop an older binary restoring them over `main`.** Restore
   placement is decided by the archive's manifest
   (`internal/broker/backup.go:217-226`), and archive keys are
   `CompKey(path)` (`backup.go:45`). A key such as `CompKey("<tile>+<name>")`
   would be restorable into `<tile>` by an older binary, once someone creates
   that path. *Fix:* NP-12-10.
7. **Model §11 relies on D82's leftover refusal to prevent inheritance.** An older
   binary's `pathLeftovers` (`internal/broker/policy.go:211-258`) knows no
   deployment state, so a downgrade window reopens the hole. *Fix:* NP-12-9.
8. **The model is silent on origins mode.** There, the tile cookie is the
   documented credential for a raw `fetch('/api/<you>/x')`, and the origin
   owns browser storage (`docs/elements.md:280-282`). A non-primary document
   on the primary's origin would route such calls to the primary and share
   its storage. *Fix:* NP-12-4.

## New proposals

- **NP-12-1 — The qualifier resolves only when it must.** It applies only to
  tiles with a deployment record, and only after today's resolution (a
  component, or a path on disk) has failed. In the zero state, `+<name>` URLs
  behave exactly as today.
- **NP-12-2 — Feature detection by the endpoint.** Clients detect the feature
  from the deployments endpoint's JSON answer; a plain 404 or 405 means an
  older xbind. `/whoami` needs no new field. Every selector sent to an
  existing endpoint must be echoed back in the answer (NC-2).
- **NP-12-3 — Downgrade-safe window prefs.** The terminal window writes only
  layout values that every older frame knows into `term:<tile>`. The
  deployments panel is recorded in a new field.
- **NP-12-4 — One origin per deployment in origins mode.** In `origins` mode,
  every non-primary deployment gets its own tile origin, keyed by the tile
  path and the deployment name. The tile cookie and browser storage then
  never cross deployments.
- **NP-12-5 — What `/components` describes.** Deployment-level fields
  (`runtime`, `native`) and the runtime document `/c/<tile>/?native=1`
  describe the primary's code: its checkpoint when pinned. `manifestError`
  keeps describing the work tree, which is where tile-level authority comes
  from.
- **NP-12-6 — Name the sandbox surfaces in P5.** P5's list of what stays
  byte-for-byte should add the cgroup leaf and the D112 registry rows:
  - a zero-state tile keeps the flat `comp-<CompKey>`;
  - a tile with a record nests, as [07-runtime.md](07-runtime.md) §10.3
    specifies, moving at a generation boundary so no live process is moved;
  - `Entry.Deployment` stays empty on zero-state rows (07-runtime §10.1).
- **NP-12-7 — The checkpoint remote comes from session env.** It is injected
  per session through additional `GIT_CONFIG_KEY_n` pairs, only for tiles with
  a record. xbind never writes it into a tile's `.git/config`, unlike the
  `template` remote (`internal/broker/templaterepo.go:139-151`). Opt-out and
  downgrade leave nothing behind, and boot never touches tile repositories.
- **NP-12-8 — Fix the D112 sandboxes tab forward.** Before the
  `sandbox-visibility` branch ships in a release, the tab should compute the
  current generation per `(tile, deployment ?? '')`
  (`workspace-template/tiles/admin/tabs/sandboxes.js:220`, `:238-239`), with
  `hack/ui-harness/passes/sandboxes.js:120` kept green. Admin consoles updated
  between the two releases then render non-primary entries correctly.
- **NP-12-9 — Records remember their owner.** A deployment record stores the
  tile's owner ref at creation, and applies only while that matches the
  tile's current owner. Otherwise the record, store and namespaces are inert
  leftovers until an admin adopts or clears them. A transfer (D39) on the new
  binary updates the ref. This complements [06-security.md](06-security.md)'s
  record of the tile path (NP-06-11). The path check catches key collisions;
  the owner check catches a path re-created by someone else while an older
  binary ran.
- **NP-12-10 — Backups that older binaries can't misuse.**
  - Deployment sections of a component backup use a new tar prefix.
  - Non-`main` archives use a key that no `CompKey(path)` can produce, and a
    manifest schema above today's `1`, so older readers refuse them.
  - Non-`main` rows never enter `CronJobs` or `BusSubs`.
- **NP-12-11 — The `+` reservation.**
  - The narrow collision is refused at once, for every creator, as a
    security closure.
  - Any other `+` warns in release R and is refused for non-admins in R+1.
    Admins are only warned.
  - Adding deployment `<N>` to `<P>` is refused while a path `<P>+<N>`
    exists.
- **NP-12-12 — Where non-`main` namespaces may live.** At a dot-prefixed
  level above the scope key. Never nested inside a directory that `main`'s
  resource paths expose: an older binary's blob listing
  (`internal/broker/resources.go:350-360`) would show it to `main`'s code.
  Never where an older binary's directory walks under `data/` list it
  (`internal/broker/vault.go:115-123`, `internal/term/history.go:150-165`).
- **NP-12-13 — `bx doctor` checks.** For tiles with deployment state, `bx
  doctor` (and the deployments panel) reports:
  - a pinned primary whose work tree differs from its checkpoint (the
    aftermath of a downgrade, or just unshipped work);
  - `+` path collisions;
  - records whose owner no longer matches;
  - absolute self-references in legacy mode, which make a non-primary
    frontend load the primary's files.
