# Terminology census: candidate words and their existing meanings

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `terms`.

## Summary

Scope: a read-only census over docs/, plans/DECISIONS.md, web/, internal/, cmd/bx, sdk/, workspace-template/, builtin-tiles/, builtin-templates/, website/ and README.md. The hit counts are ripgrep word matches in the builder docs (docs/ without changelog.md, plus workspace-template/AGENTS.md). Each term entry has its meanings with refs, its prominence and a verdict. Inferences are marked.

HEADLINE COLLISIONS
1. 'identity' is the owner's word for deployments, and it is the most loaded term in xbin.
   - 'The path is the identity' is a founding rule (docs/overview/03-components.md:35, docs/elements.md:5, docs/getting-started.md:202).
   - A whole chapter covers the 'identity spine' (docs/overview/05-identity.md). SDK Self() and XBIN_COMPONENT are documented as the identity.
   - D82 reserves ':' as the identity separator (user:bob).
   - The README and website pitch 'each app … with its own identity'.
   - The feature keeps grants, bindings and caps per tile, so a deployment is by design NOT an identity. The word would state the opposite of the contract.
2. 'instance': D32 and docs/auth.md:1109-1111 already document `iface:api@apps/warehouse#dev` as 'the dev instance of the api'. Template instances (D50) and per-generation instance tokens compound it.
3. 'deployment/deploy':
   - In the operator docs and plans it means the xbin install (docs/overview/15-operations.md:1,17; BU-2 'per-deployment').
   - The product promises there is no deploy step: workspace-template/AGENTS.md:35, the welcome tile, and website/index.html:376 'Live, no deploy step.'
   - Only the 'deployed backend' usage (docs/getting-started.md:166) is compatible.
4. 'dev' carries six meanings: dev layer, --dev/make dev, vault dev key, devbox, dev box, and the D32 dev instance. It works as data (a deployment's name) but should not be product vocabulary.
5. Also TAKEN:
   - 'live': live reload, live backend, live tile API, Live Activities, and the pitch.
   - 'pause': `bx disable` is documented as 'pause/resume a tile'.
   - 'source': the ingress source.
   - slot, layer, env, generation, version (backup versions behind `--version`), release (compat's time unit), revision (D55, native revs), draft (D55), snapshot, channel (D86), lane (agent lanes), preview (`bx preview`, `preview=1` in compat), publish, pin, fork, copy, swap (blue/green), production (xbind tier 3), freeze (frozen URLs), lock.

FREE OR SAFE
- FREE: variant, stage/staging (only D40's staged views, boot stages and traefik's ACME staging toggle), ring, rollout, rollback, twin, primary, fabric.
- SAFE-EXTEND:
  - branch, main, head and work tree, all in git's sense only. Tile repos are created with `git init -b main` (internal/broker/code.go:100).
  - remote: the xbind-served `template` remote at http://xbin/api/xbin/templates/<name>.git (templaterepo.go:145-150) is the precedent for a blessed, injected remote.
  - promote (used only for admin promotion), alias (bus role aliases), track (fits git upstream; bare 'tracked' means builtin-update provenance), build (existing sense only).
  - live reload: 'live reload' is the documented term, 'hot reload' appears only 3 times, and the `reload` event is wire contract.

PRODUCT VOICE (D67; website/README.md:31-36)
- Website: the visible copy says app/apps 22 times and room 5 times, and never says tile, component, slot or harness.
  - It uses IT words verbatim: identity, roles, grants.
  - It sells 'live' and 'no deploy step', and markets roles as 'Viewer, developer, admin.' (website/index.html:410). The docs call the levels read/write/terminal (docs/auth.md:588-590).
- README: the pitch uses app/room (README.md:3-17,36-41). It then switches to 'Workspace → Scope (an app) → Component (an element)' (README.md:44-48) and to docs vocabulary: live reload, per-generation instance credentials, per-tile dev layer (README.md:53-99).
- Docs: tile appears 1641 times, component 477 and element 167, as defined in docs/overview/03-components.md:48-58. In the docs, 'the app' mostly means the xbin mobile app, and a scope is 'an app boundary' (docs/elements.md:550).
- Inference: a user-facing name has to sit naturally next to 'app' on the site and 'tile' in the docs. It must also leave the default 'no deploy step, live on save' claim true.

QUALIFIER SYNTAX (details in qualifier_syntax)
- There is no charset validator. ComponentPathOK and Rescan accept any directory name, and only non-admin creation refuses ':' (D82).
- Taken: '#' (instance/slot), '@' (allowance role cap and provider), ':' and '::' (the class separator everywhere; bx grant's last-colon role split), and '~' (/c/~ tokens plane; the unhashed ScopeKey '/'→'~').
- Free: '+' and '!', with practical caveats: '+' decodes to a space in unencoded query strings, and '!' triggers shell history expansion.
- Inference: carry the deployment as a separate field or flag, and keep `component` as the plain path (compat rule 2). In URLs, use an in-segment suffix on the last segment or a new top-level route, never a separate /c/ or /api/ segment (longest-prefix resolution and prefix-based frame reloads).

SIDE FINDINGS (outside this brief, but they touch the path and grant grammar)
1. The o/u positional path reservations that D24 dropped are still described in docs/overview/02-workspace.md:55, docs/overview/03-components.md:45-46, docs/overview/07-users-orgs.md:116 and README.md:66. The reserved-top table omits 'owner', which util.go:69 does reserve.
2. website/index.html:340 shows `bx grant apps/dashboard res:apps/db reader` with three positionals. docs/bx.md:103 specifies `<caller> <target>:<role>`, and cmd/bx/main.go:980-987 rejects three arguments with a usage error.
3. util.ScopeKey already maps the scope paths 'apps~x' and 'apps/x' to the same data key.

## Qualifier syntax

VALIDATORS (facts)
1. util.ComponentPathOK (internal/util/util.go:97-113) is the only syntactic path check. It requires a non-empty, relative path with no '\', stable under path.Clean. The first segment may not be in ReservedTop (util.go:67-70: .xbin vendor data home homes xbin ingress runtime owner). No segment may be '.' or '..', start with a dot, or be one of IgnoredDirs (util.go:73-76: .git .xbin node_modules deps __pycache__). There is NO charset rule. Callers: scaffold.go:34, broker/clone.go:67, gitimport.go:147, prs.go:157, code.go:47, builtins.go:285, deps.go:39.
2. Broker.newTilePathOK (internal/broker/policy.go:156-176, reached through canCreateAt at :105-129) applies to non-admin humans only, whether they act directly or are attributed on an element call. It refuses:
   - a first segment of tiles, root or shell (reservedCreateTop, :136-140);
   - any ':' in any segment ("it separates grant targets and identities", :161-165);
   - a path in someone else's scope;
   - a path with leftovers from a removed tile.
   This is D82 (DECISIONS.md:2134-2176; docs/auth.md:609-613; docs/protocol.md:1302-1306).
3. Registry.Rescan (internal/registry/registry.go:423-497) registers ANY directory that holds xbin.json or index.html. It skips only IgnoredDirs, dot-prefixed names and ReservedTop. So admins, terminals (mkdir, cp -r), clone and import can already create tiles whose names contain @ # ~ + ! and even ':'.
4. Stale docs: docs/overview/02-workspace.md:54-55, docs/overview/03-components.md:45-46, docs/overview/07-users-orgs.md:116 and README.md:66 still describe the o/u positional segments that D24 dropped (DECISIONS.md:682-683). No code enforces them. The 02-workspace table also omits 'owner'. Its wording, 'enforced for new tiles; existing dirs keep working', is the precedent for reserving new syntax compatibly.
5. Neighbouring ID validators:
   - user IDs and net-set names: ^[a-z0-9][a-z0-9._-]{0,31}$ (users.go:733, term/scopes.go:29);
   - iface instance IDs: no '#', '/' or whitespace (broker/netfn.go:1081);
   - bus subscription names: ^[A-Za-z0-9._-]{1,64}$ (bussubs.go:53);
   - push tile kinds: ^[a-z0-9][a-z0-9-]{0,31}$ (push/api.go:29);
   - native entry segments: ^[A-Za-z0-9_~+@-][A-Za-z0-9._~+@-]*$ (registry/native.go:48).

WHAT EACH CHARACTER MEANS TODAY (facts)
- ':' is the class/namespace separator.
  - Grant targets: res:, cap:, code:, gpu:, net:, xbin: (broker/policy.go:62-80).
  - Allowance classes (users/orgs.go:584-660). parseAllowEntry cuts the class at the FIRST ':' (:592).
  - Owner refs user:<id> and org:<id> (D24).
  - Net binding refs lan:, internet:<host>[:port], set:<name>, provider: (docs/bx.md:177-185; sandbox/policy.go:40-70), plus :<port> suffixes and bracketed IPv6.
  - Screens scope org:<id>. Message types such as xbin:resize (docs/protocol.md:2282). Agent keys chan:<C>:dm:<u> and trig:<id>.
  - bx grant cuts the role at the LAST ':' (cmd/bx/main.go:980-987; docs/bx.md:174).
  - Any ':' in a target is read as 'capability class, not a tile' at redact.go:34, broker/delegated.go:46,75, broker/personal.go:47 and users/orgs.go:712.
- '@':
  - Allowance role cap: res:<glob>@<role> and tile:<pat>@<role>, cut at the FIRST '@'. A custom role is accepted if it contains none of ':/@#*' (orgs.go:597-612).
  - Provider marker in iface:<svc>@<provider>[#<inst>], both in allowances and in normalized binding targets (orgs.go:629-640; broker/delegated.go:205-217).
  - A leading '@' marks a pseudo-slot such as @archive (broker/backup.go:31; netfn.go:865).
  - Native event attributes such as @tap. scp-style user@host in git import (gitimport.go:38).
- '#':
  - Binding refs <provider>#<instance> (registry.go:215-223,243; netfn.go:313-320; docs/bx.md:107,191).
  - Stream refs provider#expose-slot (docs/elements.md:88; docs/overview/11-interfaces.md:196).
  - The allowance suffix #<instance-glob> (D32).
  - lan-ingress roster keys <client>#<slot> (runner/runner.go:566-581).
  - In URLs, the fragment (tile-relative links in /notify: docs/protocol.md:1782).
  - `apps/x#dev` already means 'the dev instance of apps/x' (docs/auth.md:1109-1111).
- '~':
  - The /c/~<asset-token>/… plane in tokens mode (server/tileassets.go:253-265; docs/protocol.md:18,296). Tile origins answer it with 404 (tileorigin.go:160).
  - On-disk keys map '/' to '~'. util.ScopeKey has no hash (data/resources, resenc mounts). util.CompKey adds a hash (.xbin/build, .xbin/env, .xbin/log, vault, cgroups) (util.go:126-143).
  - Builtin-update IDs map ':' and '/' to '~' (builtins/updates.go:213). The watcher ignores files ending in '~' (watch/watch.go:50).
- '+': only the bx bind operators <slot>+=<ref> and <slot>-=<ref> (cmd/bx/main.go:883-892; docs/overview/11-interfaces.md:177-180). It means nothing in grants, allowances, refs, events or routes.
- '!': no meaning in any grammar, event, URL or CLI.
- '::': only inside bracketed IPv6 net targets (sandbox/policy.go:28, e.g. net:[2001:db8::]/32) and git's rejected ext:: transport (gitimport.go:25).
- Also: '*' is the glob in tile patterns (users.go:141-153; orgs.go:864-871) and the key for the bindings["*"] workspace default. Dot-prefixed segments are hidden or refused.

EVENT TARGETS (facts)
- /ws/events frames are {type, component, text, topic, data} (internal/events/events.go:11-17; docs/protocol.md:2192-2206). `component` is the plain tile path, the bus topic is res:<scope>/<name>/<topic>, and the session topic is session.<id>.
- Frames reload when `component` equals their src or starts with src+'/' (web/events-socket.js:40-49).
- X-XBin-From is owner | a tile path | xbin/cron | xbin/bus | ingress (sdk/xbin.go:86; docs/overview/05-identity.md:213-217). XBIN_COMPONENT, xbin.self and Self() are the plain path (docs/elements.md:538; web/xbin-client.js:50; sdk/xbin.go:81-82).
- Cron jobs and bus subscriptions always target the registering tile; admins may pass ?component= (docs/resources.md:176-180; D85).
- Push links are c/<tile>/… or agent/<session>, with kinds tile.<kind> and agent.* (docs/protocol.md:1972-1978).

URLS (facts)
- /c/<path>/ and /api/<path>/ resolve by the longest registered component prefix; the remainder is the file or backend path (registry.go:519-536; proxy/proxy.go:130; docs/protocol.md:410). For an unresolved path, the RBAC owner falls back to the first segment (server/static.go:330-338).
- /api/xbin/ is xbind's own API, which works because 'xbin' is a reserved top-level name (server.go:562-583).
- Query keys already taken: frame, native, preview, xbin_begin, xbin_ticket, xbin_state, xbin_retry. Tiles also keep their own ?query and #fragment.
- Top-level mux patterns: /healthz, /login…, /logout, /, /c/, /vendor/, /docs/, /api/, /ws/term, /ws/term/env, /ws/events (server.go:139-170).
- Tile origins are t-<HMAC(path)> (auth/assettoken.go:182-188) and frame tokens base64url-encode the path (auth/frametoken.go:38). So no qualifier character reaches DNS or breaks a token.

ASSESSMENT (inference)
- TAKEN in grammar positions:
  - '#': interface instances and expose slots, literally 'the dev instance'.
  - '@': `tile:apps/x@dev` is silently accepted today as a role cap named dev.
  - ':' and '::': D82 reserved ':' for identity syntax, but every classifier reads ':' as a capability class, net refs use it, and bx grant takes the last ':' as the role.
  - '~': the /c/~ plane, and ScopeKey gives apps/x~dev and apps/x/dev the same data key.
- FREE: '+' and '!'.
  - '+' is literal in URL paths but decodes to a space in query strings that are not encoded.
  - '!' survives encodeURIComponent but triggers history expansion in interactive bash and zsh, the shells tile terminals ship.
- Whatever character is chosen is already legal in tile names. It must be reserved for NEW paths the D82 way, with an exact match on an existing component winning, so old directories keep resolving (compat rules 2 and 7).
- API/grammar positions: compat rule 2 (docs/compat.md:24-28) and the consumers that match paths exactly (events-socket.js:42, SDK From checks) argue for a separate field or flag, e.g. {component, deployment} or bx --deployment, with `component` staying the bare tile path. A text form such as 'apps/x+dev' can serve where a single string is unavoidable (bx args, binding refs).
- URLs: never put the qualifier in its own segment under /c/ or /api/. It would read as a tile sub-path, a nested tile or an xbin.window sub-path, and it would trigger the prefix-based frame reloads. Two forms work:
  - an in-segment suffix on the LAST segment (/c/apps/x+dev/, /api/apps/x+dev/…), with Resolve splitting it off before the prefix walk;
  - a new top-level route outside the current mux patterns.
- Avoid '#' (never sent to the server), '?' (taken keys, and queries are forwarded to tiles) and the /c/~ prefix.

## Census

### identity / identities — TAKEN

- A tile's path is its only ID ('the path is the identity'): docs/overview/03-components.md:35, docs/elements.md:5, docs/overview/01-model.md:35 ('Directory = component = identity'), docs/getting-started.md:202 ('Paths are identities'), docs/overview/05-identity.md:114
- The verified caller identity and the 'identity spine': X-XBin-From/Role are stripped on the way in and injected by xbind (docs/protocol.md:62, docs/overview/05-identity.md:200-219). CallerInfo is 'the verified identity' (sdk/xbin.go:84), XBIN_COMPONENT is 'own path (identity)' (docs/elements.md:538), and Self() returns 'its identity' (sdk/xbin.go:81)
- Principal and owner reference syntax: D82 reserves ':' in tile names as the 'grant-target/identity' separator (user:bob, code, cap:x): DECISIONS.md:2146, docs/auth.md:611-613, internal/broker/policy.go:146,163
- Human identity: the identity store data/users.json (docs/auth.md:1037), SSO identities bound to a user row (docs/auth.md:722, D51 DECISIONS.md:1100), 'Identity and roles' (website/index.html:408)
- Product pitch: 'each app … with its own identity, grants and network policy' (README.md:4, website/index.html:9,16,290, D67 DECISIONS.md:1654)
- Minor: the broker's 'identity plane' (D63 DECISIONS.md:1519) and the gocryptfs 'identity xattr' (D44 DECISIONS.md:920-939)

**Builder-facing:** Very high. 138 hits in 32 builder-doc files. It names a whole overview chapter (docs/overview/05-identity.md, 'Identity: principals & tokens') and the docs/auth.md title. The README and website hero use it.

**Note:** This is the owner's name for deployments, and it is the worst available choice. The feature keeps grants, bindings and caps per tile, so a dev deployment is explicitly NOT a new identity in xbin's sense: it has the same From and the same principal. Calling it an 'identity' would assert the opposite of the contract. On the website 'identity' is IT vocabulary for a principal (D67).

### principal — TAKEN

- auth.Principal struct, with Via = cookie|bearer|instance|frame|cron|terminal|session|device|app and Gen (internal/auth/auth.go:54-84)
- docs/auth.md:14 'Who is calling? (principals)'. Element vs human principals: docs/overview/03-components.md:51-53, docs/overview/05-identity.md:1
- The structural 'ingress' principal (docs/overview/13-ingress.md:174-190). 'Instance principals' (DECISIONS.md:2784)

**Builder-facing:** High. 99 hits in 23 files. Core auth vocabulary.

**Note:** Tiles are principals, and deployments share the tile's principal. Never call a deployment a principal.

### instance — TAKEN

- Template instance: a named copy stamped from a template. See D50 (DECISIONS.md:1075-1097), BU-4 (DECISIONS.md:130), D72 (the agent template instance, DECISIONS.md:1776-1791), /templates/new and /templates/updates {instances:[{path,template,head,legacy}]} (docs/protocol.md:1361-1389), TemplateMeta.DefaultName 'suggested instance basename' (internal/registry/registry.go:41-48) and workspace-template/AGENTS.md:125-131
- Interface instance: provides {instances:true}, registered at runtime with PUT /api/xbin/iface-instances and addressed as <provider>#<instance> (internal/registry/registry.go:112-116,218-223; docs/overview/11-interfaces.md:122-136 IFACE-7; docs/elements.md:96-108). The allowance form iface:<svc>@<tile>#<inst> is documented as 'the dev instance of the api, only' (D32 DECISIONS.md:754-761; docs/auth.md:1109-1111)
- Backend instance token: a per-generation credential (docs/auth.md:20; docs/overview/05-identity.md:104-115; Via "instance"; Auth.RegisterInstance at internal/auth/auth.go:445; D8 DECISIONS.md:306)
- The runner's `instance` struct is one generation's process (internal/runner/runner.go:55-67; state.cur at :73)
- An xbind install: 'env-less production instance' (docs/bx.md:228), 'never expose such an instance' (docs/auth.md:1369), 'the running instance' (docs/maintenance.md:435)
- Loosely: 'Env every backend instance gets' (docs/elements.md:533)

**Builder-facing:** High. 141 hits in 32 files. IFACE-7, templates and the auth chapters all use it.

**Note:** This collides directly with a 'dev' deployment: `apps/x#dev` and 'the dev instance' already mean an interface instance named dev.

### deployment / deploy — TAKEN

- The xbin install or host: 'Deployment & operations' (docs/overview/15-operations.md:1), 'The reference deployment' (docs/overview/15-operations.md:17; cmd/bx/main.go:220), 'Operating a deployment' (docs/overview/00-index.md:91), README.md:139 'Deployment on a VM', 'per-deployment' state (BU-2 DECISIONS.md:124; plans/builtin-updates.md:50), plans/runtime.md:1, RT-1 (DECISIONS.md:52-71), plans/deployment.md (historical), 'deployments run to thousands of tiles' (workspace-template/tiles/admin/admin.js:93), 'a hand-rolled deployment' (D110 DECISIONS.md:3320)
- 'Deployed backend': the backend's run-time sandbox, as opposed to the terminal dev layer (docs/getting-started.md:166; docs/overview/16-extending.md:158-159). The welcome note is titled 'terminal ≠ deployment' (workspace-template/apps/welcome/notes.js:93-103)
- Negated in product copy: 'There is no deploy step' (workspace-template/AGENTS.md:35), 'No deploy step, no build step' (workspace-template/apps/welcome/index.html:55), 'Live, no deploy step.' (website/index.html:376)
- The repo's deploy/ directory (install.sh, publish-release.sh): README.md:154-271, docs/maintenance.md:107
- Example content about users' own CI deploys: bus topic 'deploy/' (docs/resources.md:150; docs/sdk.md:89-105), 'Deploy v2.3 to prod?' (docs/sdk.md:127; sdk/notify.go:47), the webhooks 'deploy' placeholder (builtin-tiles/webhooks/index.html:104), agent trigger placeholders (builtin-templates/agent/auto-triggers.js:104-114), 'main was deployed' (docs/agent-inbox.md:234)

**Builder-facing:** Low in the tile docs: 'deployment(s)' has 10 hits in 6 files, and 'deploy*' has 37 hits in 15 files, mostly deploy/ paths and examples. Prominent in operator docs, and in the pitch's 'no deploy step'.

**Note:** Bare 'deployment' means the xbin install. If the feature keeps the word, qualify it everywhere ('tile deployment'), which extends the compatible 'deployed backend' sense. The default must keep 'no deploy step' true, and the operator chapters would need disambiguating (inference).

### environment / env — TAKEN

- Component env layer, built from the manifest `setup` (plans/component-env.md; registry.go:66-70; docs/overview/03-components.md:246 'Environment layers (`setup`)'; docs/isolation.md:21,264; .xbin/env/<comp~key>/<hash>/ at docs/overview/02-workspace.md:137)
- Terminal env: the /ws/term/env reset and status API for the dev layer (docs/protocol.md:2061-2063,2164-2171), the LC-2 'terminal-env layer' (DECISIONS.md:655), offloaded-full's 'source/term-env' (docs/protocol.md:1681), and 'Terminal environment' (workspace-template/AGENTS.md:38)
- Env vars: backend XBIN_* (docs/elements.md:533-546; docs/protocol.md:2318-2326), XBIN_RES_*, XBIN_IFACE_*. xbind config env (docs/config.md:7-21; D62 DECISIONS.md:1490-1493) and /etc/xbin/xbin.env

**Builder-facing:** High. 'env' has 103 hits in 23 files; 'environment' has 24 in 13.

**Note:** 'dev/prod environments' would read as env layers or env vars.

### version — TAKEN

- Backup archive versions: bx backups, bx restore --version, /backups, /restore and /archive/<key>/versions/<v> (docs/bx.md:142-144; docs/protocol.md:1688-1704; docs/overview/14-lifecycle.md:123-127,159-196)
- Builtin tile versions: tile.json `version`, and fromVersion/toVersion in /builtins/updates (docs/maintenance.md:266-270; docs/protocol.md:1340-1343; BU-1 DECISIONS.md:120-122)
- The xbind version (docs/config.md:18), the app version in X-XBin-Client app/<version> (docs/protocol.md:240), the native-runtime version (docs/protocol.md:1253-1255) and the native major `v` (docs/compat.md:133)
- The manifest `schema` (docs/overview/02-workspace.md:96) and base rootfs <rootfs>-<version> (docs/overview/09-terminals.md:345-353)
- Git tags as 'versions' before install (docs/protocol.md:1411), and the template's 'one-commit-per-version' history (D50 DECISIONS.md:1084-1085)

**Builder-facing:** Medium. 77 hits in 17 files.

**Note:** `--version` already selects a backup archive version.

### revision / rev — TAKEN

- D55 screen and folder revisions: a `rev` compare-and-set, with 409 when stale (DECISIONS.md:1252-1268; docs/protocol.md:937-952; workspace-template/shell/rev-draft.js; docs/maintenance.md:368-372)
- Native primitive revisions: `rev`/`since` and xbin.native.supports(name, rev) (docs/native.md:276-296,676-679; docs/compat.md:68,119; web/xb/vocab.js:6-23)
- Git revs: /git/diff ?rev=<hash> (docs/protocol.md:1401-1402), a code PR's base or target rev (docs/protocol.md:1434; workspace-template/AGENTS.md:576), and revRE (internal/broker/code.go:30)

**Builder-facing:** Medium. 43 hits in 8 files.

### release — TAKEN

- xbin's own releases: make release, tags, bundles (docs/maintenance.md:99-112; D47 DECISIONS.md:999-1024; README.md:113,153)
- The compat contract's unit of deprecation: 'for one release, a warning' and 'legacy the default this release' (docs/compat.md:48-50,74,82-96; docs/protocol.md:259; cmd/bx/args.go:12-18)
- Native app releases (docs/native.md:672)
- In code: release() functions for reservations and connections (internal/runner/runner.go:240; internal/vm/policy.go:121), and the ACP method terminal/release (internal/agent/acp/types.go:31)

**Builder-facing:** Medium. 57 hits in 15 files.

**Note:** 'Release' is xbin's own cadence and the compat contract's unit of time.

### generation — TAKEN

- Backend generation: each blue/green (re)start. Shows as `gen` in /backends and `--- gen N start` in logs (docs/overview/03-components.md:144-164; docs/elements.md:516-521,539; docs/bx.md:12,369; docs/protocol.md:435,454,489,515; internal/runner/runner.go:55-80; D8 DECISIONS.md:306)
- Credential generation: frame and asset tokens bound to the login that minted them (s.<handle>, u.<epoch>.<n>, o.<hash>). See D93 (DECISIONS.md:2732-2746), internal/auth/frametoken.go:21-39, Principal.Gen (internal/auth/auth.go:71-75), docs/auth.md:61,135,145 and docs/protocol.md:25,35
- The agent's `engine:<gen>` lease mark (D81 DECISIONS.md:2131). Filesystem generation numbers (D44 DECISIONS.md:944)

**Builder-facing:** Medium. 42 hits in 14 files, plus README.md:63.

### snapshot — TAKEN

- Backup snapshot: 'bx backup … snapshot to the bound @archive provider' (docs/bx.md:141; docs/overview/14-lifecycle.md:70; README.md:282)
- Builtin-update base snapshots (BU-2 DECISIONS.md:124; docs/overview/02-workspace.md:141; docs/overview/14-lifecycle.md:237)
- Template snapshot, which serves as the merge base (D50 DECISIONS.md:1081-1085; docs/protocol.md:1370,1377)
- Agent work-tree snapshots that feed files.changed (D77 DECISIONS.md:1955-1964; docs/overview/09-terminals.md:503-510; docs/protocol.md:2252)
- The per-request user snapshot (docs/overview/05-identity.md:100)

**Builder-facing:** Medium-low. 19 hits in 12 files.

### layer — TAKEN

- Terminal dev layer: .xbin/term/<key>/, persistent per tile, reset with ⟲, pinned to a base image, one holder at a time (docs/isolation.md:1,250-275; docs/overview/09-terminals.md:260-356; docs/getting-started.md:158-168; README.md:90)
- Component env layer, built from `setup` (docs/isolation.md:21,264; docs/overview/03-components.md:246-256)
- The personal defaults 'live layer' (docs/auth.md:1281), the container layer store (docs/resources.md:221-234), and the VM terminal layer (docs/protocol.md:2135-2136; D89)

**Builder-facing:** High. 118 hits in 25 files. It is in the title of docs/isolation.md.

### build — SAFE-EXTEND

- The backend build per change: go build, sticky build errors, the build overlay (docs/overview/03-components.md:128-171; docs/elements.md:359-360,508,518)
- Events build-start, build-error and build-ok, each carrying only `component` (docs/protocol.md:2193-2195; internal/events/events.go:12)
- Build output at .xbin/build/<comp~key>/bin (docs/overview/02-workspace.md:135; internal/runner/runner.go:366). The env-layer build (docs/elements.md:41-42)
- 'No JS build step — ever' (workspace-template/AGENTS.md:35; CLAUDE.md), and the xbind build commit (docs/protocol.md:429)

**Builder-facing:** High. 160 hits in 27 files.

**Note:** Use it only in the existing sense (each deployment gets built). Do not call a deployment or a promotable artifact 'a build'.

### branch — SAFE-EXTEND

- Git only: the `ref` (a tag or branch) of /git/import (docs/protocol.md:1412-1419; internal/broker/gitimport.go:21-178), /git/remote-info defaultBranch (docs/protocol.md:1409-1411; workspace-template/tiles/manager/index.html:343), the remote-tracking branch in /git/activity (docs/protocol.md:1403-1408; internal/broker/code.go:306,365), and the template repo's `main` (docs/protocol.md:1370-1389)
- A native icon named `branch` (docs/native.md:576; web/xb/vocab.js:67)

**Builder-facing:** Low. 13 hits in 6 files, all in git's sense.

**Note:** It has no xbin-specific meaning, so the branch-backed variant can use it exactly as git does.

### channel — TAKEN

- D86 chat channels: adapter tiles, the custom role `channel` that reaches only /adapter/*, claimed or unclaimed channels, /channels/{id} (DECISIONS.md:2286-2330; docs/agent-inbox.md:34-150,313-339). conversation.type `channel` (docs/agent-inbox.md:82)
- The example multi slot `channels` (docs/elements.md:117; workspace-template/AGENTS.md:695)
- Status reporting's 'self-scoped channel' (docs/elements.md:482; workspace-template/AGENTS.md:493). Code PRs as the 'suggestion channel' (docs/bx.md:194)
- About 458 hits in builtin-templates/agent

**Builder-facing:** Medium. 39 hits in 8 files, dominated by the agent-inbox contract.

**Note:** 'Release channel' or 'dev channel' would collide with D86.

### stage / staging — FREE

- D40's per-session staged VIEW directory for restricted terminals (DECISIONS.md:851-856; docs/isolation.md:100; internal/term/term.go:127-130,282)
- Boot steps, described as 'named stages' (docs/maintenance.md:197; internal/boot/boot.go:74)
- Agent attachment uploads are 'staged' (DECISIONS.md:2366; builtin-templates/agent/_backend/channel_files.go:2-45)
- The traefik builtin's Let's Encrypt 'staging' toggle (builtin-tiles/traefik/API.md:10-11; builtin-tiles/traefik/index.html:45; builtin-tiles/traefik/xbin.json:19). The git index (internal/broker/templaterepo.go:88)

**Builder-facing:** Essentially none. 1 hit in the builder docs (maintenance).

**Note:** Usable as a tier or deployment word. The only builder-visible use is the traefik tile's ACME staging toggle.

### preview — TAKEN

- `bx preview --native` (D98 DECISIONS.md:2945-2964; docs/bx.md:159,300-365; docs/native.md:95)
- The runtime document ?native=1&preview=1 and /vendor/xb/preview-host.js (docs/protocol.md:279-291; docs/elements.md:389-398; docs/frontend-kit.md:51-60). This is part of the compat surface (docs/compat.md:123)
- GET /owner/preview, the transfer impact report (D39 DECISIONS.md:830-835; docs/protocol.md:1159; docs/auth.md:1141-1142)
- /git/remote-info 'preview versions' (docs/protocol.md:1411), the agent session `preview` field (docs/protocol.md:784), and the native image `preview` prop (docs/native.md:374)

**Builder-facing:** Medium. 60 hits in 11 files.

**Note:** `preview=1` is a frozen URL query in the compat contract.

### variant — FREE

- Per-arch bundle variants in the release manifest (docs/overview/15-operations.md:105-112; D47 DECISIONS.md:1005-1015)
- D86: 'each deployment wants its own variant' of a chat adapter (DECISIONS.md:2361)

**Builder-facing:** None in the tile docs. 2 hits in 1 operator file.

### slot — TAKEN

- Interface slots: interfaces and provides are keyed by slot, bindings are stored as bindings[component][slot], and slots are injected as XBIN_IFACE_<SLOT>_* (internal/registry/registry.go:92-98,112-116; docs/overview/11-interfaces.md:4-58,164-200; docs/elements.md:78-124; `bx bind <comp> <slot>=<ref>` at docs/bx.md:177-191)
- Exposes slots (D79 DECISIONS.md:2018-2049). The `@archive` pseudo-slot (docs/overview/11-interfaces.md:240-245; internal/broker/backup.go:31). Stream refs of the form provider#expose-slot (docs/overview/11-interfaces.md:196)
- The agent's drive slots (D81 DECISIONS.md:2072-2075). The Lit `<slot>` element

**Builder-facing:** High. 153 hits in 20 files. D67 lists it as docs vocabulary that stays off the website (DECISIONS.md:1665).

**Note:** Azure-style 'deployment slots' would collide head-on.

### alias — SAFE-EXTEND

- Bus role aliases: subscriber/publisher = reader/writer (docs/auth.md:278; docs/overview/06-authorization.md:44; docs/overview/10-resources.md:88; sdk/xbin.go:146-161; internal/broker/broker.go:373)
- The admin tab alias map (workspace-template/tiles/admin/admin.js:133). Shell aliases (workspace-template/home/zshrc:19-25; docs/getting-started.md:142)

**Builder-facing:** Low. 7 hits in 7 files.

**Note:** It means another name for the same thing. That is compatible if used, say, for a name that points at whichever deployment is primary.

### pin — TAKEN

- The builtin-update modes pin/unpin (docs/protocol.md:1344-1345; internal/builtins/updates.go:806)
- Tile-window pin and unpin in the shell's tile menu (docs/overview/04-frontend.md:195; docs/maintenance.md:384). The `<bx-frame>` pins in root/index.html (docs/getting-started.md:97; D37 DECISIONS.md:816)
- A terminal layer pinned to its base image (docs/overview/09-terminals.md:342-356; docs/overview/02-workspace.md:138)
- DNS pinning (D35 DECISIONS.md:791-794; docs/auth.md:1023-1025). `none` 'pins a tile offline' (docs/bx.md:185; docs/auth.md:1193). An iface allowance 'pins' a provider or instance (docs/auth.md:1109; docs/protocol.md:1573)
- XBIN_VERSION release pinning (README.md:154,285) and the hack pins (docs/maintenance.md:507-539)

**Builder-facing:** Medium. 63 hits in 16 files.

### promote — SAFE-EXTEND

- Only admin promotion: the 'hand-promoted admin' and SSO adminGroups (docs/auth.md:771,798; internal/users/groups.go:9; internal/users/defaults.go:24; workspace-template/tiles/admin/tabs/signin.js:168-171; DECISIONS.md:1154,1195)

**Builder-facing:** Minimal. 2 hits in 1 file.

**Note:** The verb is free for code. Promoting a user stays a different object.

### publish — TAKEN

- Ingress publishing, where binding an exposed endpoint = publishing (docs/ingress.md:1,24-33,187-196; docs/protocol.md:1532,1609; docs/auth.md:25,43; README.md:71; D41)
- Bus publish: xbin.Publish and POST /api/xbin/bus/publish (docs/resources.md:127-141; docs/sdk.md:88,248; docs/protocol.md:1739)
- D55: 'Save and update for everyone' publishes a screen (DECISIONS.md:1262-1263; docs/auth.md:993)
- Release publishing (docs/overview/15-operations.md:118-123). The allowance row kind 'publish hostname/zone/port' (D57 DECISIONS.md:1367)

**Builder-facing:** High. 85 hits in 24 files. It is in the title of docs/ingress.md.

### live — TAKEN

- Live reload (docs/elements.md:356,405; docs/overview/04-frontend.md:112; docs/native.md:125; docs/getting-started.md:85; README.md:57; workspace-template/AGENTS.md:34,323)
- 'Live backend' as opposed to a fixture in bx preview (docs/bx.md:305,336,349). A template is 'not a live tile' (docs/elements.md:177)
- 'Live tile API access' and the live tile-API token, which sit in the same terminal bar the toggle would go in (web/frame-titlebar.js:163; docs/auth.md:659; docs/overview/07-users-orgs.md:97)
- Live Activities (docs/protocol.md:2017; D102), live sessions and routes (docs/isolation.md:272; docs/bx.md:114), and the personal defaults 'live layer' (docs/auth.md:1281)
- Pitch: 'Code and live state' (README.md:41; website/index.html:333), 'Edit it live', '✓ live', 'Live, no deploy step.' (website/index.html:329,341,376)

**Builder-facing:** Very high. 171 hits in 31 files, plus the website hero.

**Note:** 'The live deployment' would clash with live reload, especially when live reload is attached to a non-primary deployment.

### draft — TAKEN

- D55 screen and folder drafts: 'edit layout' opens a local draft, 'Save and update for everyone' publishes it (DECISIONS.md:1252-1280; docs/auth.md:992-996; docs/overview/04-frontend.md:169,183; workspace-template/shell/rev-draft.js; docs/maintenance.md:368-372)
- An unsaved SSO config draft (docs/protocol.md:1051). The agent's held-ask draft keys (D106 DECISIONS.md:3202-3207; docs/native.md:462). Form-state drafts in the admin, webhooks and agent UIs

**Builder-facing:** Medium-low. 25 hits in 7 files.

### main — SAFE-EXTEND

- Git `main`: every tile repo is created with `git init -b main` (internal/broker/code.go:96-104). Template repos and instance seeding use it too (internal/broker/templaterepo.go:81-123; D50 DECISIONS.md:1081), as do `git merge template/main` (docs/protocol.md:1370-1389; workspace-template/AGENTS.md:131; cmd/bx/template.go:62) and `origin/main..HEAD` (docs/bx.md:206)
- The agent inbox's shared DM session, dm.scope "main" (docs/agent-inbox.md:266; D86 DECISIONS.md:2304)
- Go `main` and HTML `<main>`

**Builder-facing:** Low. 25 hits in 13 files, almost all git.

**Note:** It matches the tile repo's default branch, which suits the branch-backed variant. Git-imported tiles keep their remote's branches, whose default may not be main.

### prod / production — TAKEN

- How xbind itself runs: production = tier 3 with --isolate, as opposed to --dev / make dev (docs/overview/08-sandbox.md:31; docs/overview/01-model.md:182; docs/auth.md:494-497; docs/overview/06-authorization.md:221-225; docs/overview/15-operations.md:285)
- Vault production boot modes (docs/auth.md:431; docs/config.md:39,61; internal/boot/vault.go:6-21). 'Dev and prod run the same RBAC' (docs/auth.md:1370; DECISIONS.md:630). 'Env-less production instance' (docs/bx.md:228). RT-1 (DECISIONS.md:53)
- Examples: 'Deploy v2.3 to prod?' (docs/sdk.md:127), the webhooks topic 'deploy/prod' (builtin-tiles/webhooks/backend/main_test.go:120), traefik's 'staging/production' (builtin-tiles/traefik/xbin.json:19)

**Builder-facing:** Medium. 20 hits in 15 files.

**Note:** A tile's 'prod deployment' would read as xbind's production mode.

### head / HEAD — SAFE-EXTEND

- Git HEAD (docs/protocol.md:1402; docs/bx.md:206,214; the D50 ancestry check at DECISIONS.md:1087)
- In /templates/updates, `head` is the template repo HEAD to merge up to (docs/protocol.md:1375-1376; internal/broker/templaterepo.go:158)
- The `<head>` injection (D4). The shell's card head (docs/overview/04-frontend.md:159)

**Builder-facing:** Low. About 24 mixed hits in 9 files.

**Note:** Git's meaning only.

### working tree / worktree — SAFE-EXTEND

- The Code panel's 'Working tree' view of uncommitted changes (web/bx-code.js:4,112,355,469; docs/getting-started.md:52). In /git/diff, an empty rev means uncommitted changes against HEAD (docs/protocol.md:1402)
- The tile directory is the work tree: agent diff snapshots (internal/term/agentdiff.go:7-14,112-116; D77 DECISIONS.md:1964), instantiating 'without touching the working tree' (D50 DECISIONS.md:1082), and confine.Git (internal/confine/git.go:36)
- git worktree appears only in the maintainers' release flow (docs/maintenance.md:107,427)

**Builder-facing:** Low. 4 hits in 3 files, plus the Code panel label.

**Note:** 'Work tree' is the tile directory itself. 'Worktree' (git worktree) is free if a deployment's code is checked out that way.

### source — TAKEN

- Tile source code: backends mount it read-only, `code[:<tile>]` grants read it, the read level can read it, and deps give 'source visibility' (docs/isolation.md:22; docs/overview/08-sandbox.md:4,244; docs/auth.md:588; docs/protocol.md:214; docs/overview/03-components.md:70,230; D17a docs/overview/09-terminals.md:130-137)
- Ingress source: what an exposed slot binds to (the `runtime` builtin or a terminator tile), as in routes:[{source,…}] (docs/ingress.md:49; docs/elements.md:124; docs/overview/13-ingress.md:33; docs/protocol.md:1518,1659; internal/registry/registry.go:243)
- /templates/new {source} (docs/protocol.md:1363). The /owner access `source:` provenance (docs/protocol.md:1178-1192). Push 'Sources' (docs/protocol.md:1975). The agent trigger source/sourceRef (D87 DECISIONS.md:2398-2399)

**Builder-facing:** High. 173 hits in 29 files.

**Note:** 'Source deployment' (the one promoted from) would read as 'ingress source'. Prefer 'from'.

### hot reload / live reload / reload — SAFE-EXTEND

- 'Live reload' is the documented term: on a `reload` event the most specific mounted frame reloads (docs/elements.md:356-358; docs/overview/04-frontend.md:112-116; web/events-socket.js:40-49). Native views reload too (docs/native.md:125; D99). Also README.md:57 and workspace-template/AGENTS.md:34 ('live-reloads the frontend and rebuilds/swaps the backend')
- 'Hot-reload' appears only in docs/index.md:6, workspace-template/apps/welcome/notes.js:81 and DECISIONS.md:87
- The `reload` event type is a wire contract (docs/protocol.md:2192; internal/events/events.go:12). Grant changes also reload frames (docs/overview/04-frontend.md:118). D55's 'Reload theirs' (workspace-template/shell/rev-draft.js:49)
- UI glyphs: tile-admin uses ⟳ for reload (workspace-template/shell/bx-tile-admin.js:516). The terminal's ⟲ means reset the dev layer (web/frame-titlebar.js:211)

**Builder-facing:** 'Live reload' has 12 hits in 7 files, 'reload' 61 in 19, 'hot reload' 1.

**Note:** This is exactly what gets paused. Prefer 'live reload' over 'hot reload'. The docs use it for frontends and 'rebuild/swap' for backends, so the toggle's name must cover both. Do not use ⟲ for 'reload now'.

### pause — TAKEN

- `bx enable|disable` is documented as 'pause/resume a tile', i.e. the lifecycle state disabled (docs/bx.md:138)
- Account disable 'pauses/restores the whole account' (docs/bx.md:39; docs/auth.md:964 'Two pause switches'; D34 'contractor pause' DECISIONS.md:782). Membership suspension: 'pause this membership' (workspace-template/tiles/organisations/organisations.js:302; workspace-template/tiles/admin/tabs/orgs.js:155)
- Bus subscriptions 'pause while the component is disabled' (docs/resources.md:171). The agent halt tells senders it is 'paused' (docs/agent-inbox.md:144,329-331; D86 DECISIONS.md:2323). The native icon `pause` (docs/native.md:563)

**Builder-facing:** Low-medium. 13 hits in 9 files.

**Note:** A 'paused tile' already means a disabled one. Always say 'live reload paused', never 'pause the tile'.

### freeze — TAKEN

- Compat: 'Shipped URLs are frozen' (docs/compat.md:29; docs/frontend-kit.md:65; docs/index.md:43; DECISIONS.md:1407,1431) and 'freeze their chrome forever' (docs/compat.md:23)
- The frozen window.xbin object (web/xbin-client.js:412; docs/overview/04-frontend.md:62). The native runtime is 'FROZEN once shipped' (web/xb-native.js:32)

**Builder-facing:** Low. 6 hits in 4 files, all in compat language.

**Note:** 'Frozen' means 'never changes' in the compat contract.

### lock — TAKEN

- The vault's `locked` boot state (docs/config.md:61; docs/auth.md:441; docs/bx.md:228; internal/boot/vault.go:21)
- The terminal layer lock: one live holder at a time (docs/overview/09-terminals.md:270-276,372; docs/isolation.md:272; D89 DECISIONS.md:2516)
- The KV database file lock (docs/maintenance.md:206). Rootless mount locks (docs/isolation.md:135-138). D55 rejected 'per-tile locks' (DECISIONS.md:1287)

**Builder-facing:** Medium-low. 24 hits in 12 files.

### rollout — FREE

- A single 'Rollout.' paragraph about the tile-asset-gating modes (docs/auth.md:237), plus internal/assetscan/assetscan.go:4

**Builder-facing:** 1 hit.

### rollback — FREE

- 'Recover without a full rollback' contrasts a single-file restore with a whole-version backup restore (docs/protocol.md:1696). 'Roll back by pinning the previous version' of xbin (README.md:285). The agent template's rounds are 'rolled back' (builtin-templates/agent/API.md:496)

**Builder-facing:** 1 hit.

**Note:** Compatible with returning a deployment to an earlier state. Backup restore is the nearest existing mechanism.

### track — SAFE-EXTEND

- Builtin-update tracking, i.e. provenance; template instances 'aren't tracked' (workspace-template/AGENTS.md:123-126,601; internal/builtins/updates.go:4-11,127; docs/overview/14-lifecycle.md:255; workspace-template/tiles/manager/index.html:181)
- Git upstream / remote-tracking branches (docs/protocol.md:1407; internal/broker/code.go:306,365; web/bx-code.js:6)
- runner.Track for in-flight connections (internal/runner/runner.go:239-243; docs/overview/03-components.md:186). The 'git-tracked workspace manifest' (docs/overview/07-users-orgs.md:25)

**Builder-facing:** Low. 9 hits in 5 files.

**Note:** 'A deployment tracks branch X' fits git's upstream sense. Bare 'tracked' already means builtin-update provenance. 'Follow' is used for streaming (?follow=1, docs/protocol.md:516,749).

### lane — TAKEN

- The agent's private and web lanes, a toolset firewall (docs/agent-inbox.md:12,287,313-320; D72 DECISIONS.md:1796-1798; D86 DECISIONS.md:2287,2297,2316-2319; D87 DECISIONS.md:2389,2410; about 166 hits in builtin-templates/agent)

**Builder-facing:** Low in the docs (6 hits in 1 file), but central to the agent template.

### ring — FREE

- Ring buffers only: the agent event log ring (docs/protocol.md:2258; internal/agent/event.go:63-76), the relay flow ring (docs/overview/12-egress.md:97), the VMM log ring (internal/sandbox/vm/host/vmm_linux.go:59-67), and the admin bus-rate ring (workspace-template/tiles/admin/tabs/runtime.js:75-88)

**Builder-facing:** 2 hits.

**Note:** There is no rollout-ring meaning.

### copy — TAKEN

- A separate tile at a new path. Template instantiation goes 'into a named copy' (docs/protocol.md:1366; docs/elements.md:179-180; docs/overview/03-components.md:266). Clone 'registers the copy as its own component' (workspace-template/AGENTS.md:30-33). Imported builtins are 'copies you own' (workspace-template/AGENTS.md:117). 'Copy-shaped creation' needs read on the source (docs/auth.md:606-608; docs/overview/06-authorization.md:188)
- Native copy actions and xbin.native.copy (docs/native.md:279,364,406)

**Builder-facing:** Medium. 57 hits in 22 files.

**Note:** It implies a new path, and so a new identity.

### twin — FREE

- Informal 'counterpart': 'the HTTP twin of `bx logs`' (docs/protocol.md:518,678; internal/obs/logs.go:18), the 'inbound twin' of the egress links (docs/overview/12-egress.md:11,185-187; docs/overview/13-ingress.md:252), and the 'binding-plane twin' (D57 DECISIONS.md:1356)

**Builder-facing:** 6 informal hits in 3 files.

### fork — TAKEN

- Clone = fork a component: copy it with its .git to a new path (docs/protocol.md:1328 'Forks a component'; docs/overview/14-lifecycle.md:229,264,286; workspace-template/tiles/manager/index.html:2,115-128,292)
- 'cp -r is fork' (docs/elements.md:5; docs/overview/01-model.md:41; docs/overview/03-components.md:37; docs/overview/00-index.md:54; docs/getting-started.md:201; website/index.html:327)
- Template instances are forks (BU-4 DECISIONS.md:130-131; D50 DECISIONS.md:1076,1090; workspace-template/AGENTS.md:126; cmd/bx/template.go:62). A D55 draft is forked into a personal screen (DECISIONS.md:1272)

**Builder-facing:** Medium. 25 hits in 14 files, plus the website.

### dev (extra: the proposed deployment name) — TAKEN

- The terminal 'dev layer' and 'dev box' (docs/isolation.md:250-275; docs/overview/09-terminals.md:260-330; docs/getting-started.md:162-165; README.md:90)
- xbind's --dev and make dev (docs/auth.md:1369-1370; docs/overview/05-identity.md:268-271; CLAUDE.md). The vault's 'dev key' (docs/resources.md:49; internal/boot/boot.go:472)
- The `devbox` builtin tile (builtin-tiles/devbox; README.md:87). 'The dev instance of the api' is an iface instance (docs/auth.md:1110; D32 DECISIONS.md:760). The website role 'developer' (website/index.html:410)

**Builder-facing:** High. 136 hits in 27 files: dev layer 25, --dev 20, make dev 16, dev key 6, devbox 6, dev box 5, dev mode 3, dev instance 1.

**Note:** Fine as a deployment name a user chooses (data). Do not build product vocabulary on it: 'dev deployment' sits next to 'dev layer' in the same terminal window.

### primary (extra) — FREE

- The UI button role only: native role="primary" (docs/native.md:406; web/xb/vocab.js:163) and bx-dialog `primary` (web/bx-dialog.js:15)
- SSO's 'verified primary email' (docs/protocol.md:150; D51 DECISIONS.md:1104)

**Builder-facing:** 6 hits in 4 files, none conceptual.

**Note:** Free for 'the deployment that receives incoming bindings and traffic'.

### swap (extra) — TAKEN

- The blue/green swap of backend generations (docs/elements.md:508-519,539; docs/overview/03-components.md:157; docs/protocol.md:2316-2317; docs/index.md:67; README.md:58). The installer's upgrade swap (docs/overview/15-operations.md:366)

**Builder-facing:** Medium. 33 hits in 17 files.

**Note:** An Azure-style 'swap deployments' would read as blue/green.

### remote (extra) — SAFE-EXTEND

- Git remotes. The `template` remote is served by xbind as read-only dumb-HTTP at http://xbin/api/xbin/templates/<name>.git and fetched with the terminal bearer (internal/broker/templaterepo.go:145-150; docs/protocol.md:1368,1384-1389; docs/overview/09-terminals.md:228; workspace-template/AGENTS.md:127). Git import keeps `origin` (docs/protocol.md:1397-1399,1417-1418)
- The app's remote config (D101 DECISIONS.md:3041-3053)

**Builder-facing:** Low-medium. 30 hits in 11 files.

**Note:** The `template` remote is the existing precedent for a blessed remote that xbind injects and serves.

### fabric / plane (extra) — FREE

- 'Fabric': no hits anywhere
- 'Plane' is used in many compounds: the personal plane (D88; docs/auth.md:1283), runtime plane (docs/overview/09-terminals.md:21), static plane, owner plane, binding/grant plane (D57 DECISIONS.md:1356), and the asset-token plane (internal/server/tileassets.go:253)

**Builder-facing:** 'Fabric' 0 hits. 'Plane' about 40 hits across compounds.

**Note:** 'Binding fabric' is free. Avoid a new '… plane', since 'plane' is heavily compounded; that part is TAKEN.
