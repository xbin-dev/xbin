# Git, confine, code editing and the PR system

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: explore agent a6ea5269.

I mapped every git call site, the confine layer, the editing mounts and the git-related UI, CLI and API. Short version:

- xbind runs git in about 8 subsystems, and every run goes through `internal/confine`.
- xbind itself never pushes, never merges into a tile and never runs `git am`.
- There is no smart-HTTP server, no bare repo per tile, and no record of a "deployed revision": the live tile is simply its working tree.
- The only git server is a read-only dumb-HTTP endpoint for the builtin template repos.

### 1. Workspace git policy (D2)

Decision text is in `/home/magik6k/buxon/plans/DECISIONS.md`:
- **D2** (:18-19): auto `git init`, ignore `.xbin/` and `data/`, never auto-commit.
- **CM-1** (:93-109): the original "no per-component repos" was reversed — each component is now its own repo.
- **CM-2** (:110-114): agents and humans commit; xbind doesn't.
- **CM-3** (:115-117): the code and git endpoints are admin-gated.

**The workspace root is its own repo.**
- `InitWorkspace` (`/home/magik6k/buxon/internal/boot/workspace.go:91-125`) runs `git init -q` at :114-118 and makes no commit.
- It is called by `xbind init <dir>` (`/home/magik6k/buxon/cmd/xbind/main.go:40-44`) and by the auto-init in `stepWorkspace` (`/home/magik6k/buxon/internal/boot/boot.go:145-152`).
- That step runs before the `confine` step (`boot.go:92-95`), so this one `git init` runs directly on the host. It only happens on a fresh directory.
- Ignored paths: `.xbin/ data/ home/ homes/` from `/home/magik6k/buxon/workspace-template/gitignore`, plus `ensureGitignoreLine(root,"homes/")` in `/home/magik6k/buxon/internal/term/homes.go:93,122-142`.
- Component directories are **not** ignored, so each tile shows up as an embedded repo in the root's `git status`. This contradicts CM-1's "workspace repo ignoring component subtrees".
- Only a host shell can write the root repo:
  - the root terminal is disabled (`term.go:221-224`);
  - admin terminals see the root read-only;
  - D40 restricted views don't contain it at all.

**Every component has its own `.git`.**
- A component is any directory with `xbin.json` or `index.html` (`/home/magik6k/buxon/internal/registry/registry.go:456-474`), so this covers `root/`, `shell/`, `tiles/*` and `apps/*`.
- `EnsureComponentRepos` (`/home/magik6k/buxon/internal/broker/code.go:113-119`) calls `gitInitComponent` (:96-108) for any component without a `.git`. That does `init -q -b main`, `add -A`, and commits "initial commit" as `xbin <xbin@localhost>`.
- It runs at boot (`boot.go:411`) and through `OnStructureChange` (`boot.go:404-410`). That hook is fired from `templates.go:154`, `tiles.go:98` and `:201`, `gitimport.go:214`, `clone.go:141`, `backup.go:587` and `lifecycle.go:111`; `clone.go:132` also calls it directly.
- The file watcher never calls it (`/home/magik6k/buxon/internal/boot/serve.go:161-185`).

**What a new tile gets, by creation path** (all broker files under `/home/magik6k/buxon/internal/broker/`):
- **`POST /create`, or `bx new --owner`**: files are scaffolded, then `gitInitComponent` runs (`create.go:59-70`). Result: a fresh "initial commit" on `main`.
- **`bx new <path>` without `--owner`, or a hand-made directory**: local file writes only (`/home/magik6k/buxon/cmd/bx/new.go:63-71`). There is **no repo until the next `EnsureComponentRepos`** (a daemon restart or some other structure change).
- **Builtin tile import**: `CopyTree` skips `.git` (`/home/magik6k/buxon/internal/builtins/builtins.go:189-205`), then a fresh repo is made (`tiles.go:81-99`). Update provenance lives in `.xbin/builtins.json` and `.xbin/builtins/<id>/`, not in git (`updates.go:211-217`, BU-2).
- **Scaffold tiles and essential backfill**: fresh repos at boot's `EnsureComponentRepos` (`boot.go:372-411`).
- **Builtin template instance (D50)**:
  - `SeedInstanceRepo` (`templaterepo.go:106-137`) does `init`, fetches `main` from the template repo by local path, runs `update-ref main FETCH_HEAD` and `reset --mixed`, then commits "instantiate <name> as <path>".
  - `AddTemplateRemote` (:141-151) then adds a `template` remote at `http://xbin/api/xbin/templates/<name>.git`.
  - If seeding fails, it falls back to a fresh root (`templates.go:139-148`).
- **Workspace template instance**: `.git` is skipped (`templates.go:185-194`), so it gets a fresh repo and no `template` remote.
- **Clone**: the source directory is copied **including `.git`** (`clone.go:101,157-205`), then a "fork from <src>" commit is made (:130-138).
- **Git import**: `git clone` keeps `origin`, with an optional checkout of a ref (`gitimport.go:165-181`).
- **Restore from backup**: `.git` comes back from the backup (`backup.go:110-119`).

### 2. Every git operation xbind runs

The shared helper is `runGitIn`/`runGitWith` (`code.go:64-80`), which calls `confine.Git`. That binds the directory **read-write even for read-only commands**. `confine.GitRead` (`/home/magik6k/buxon/internal/confine/git.go:44-47`) exists but has no production caller.

- **Workspace init** — `workspace.go:115`: `init` on the workspace root.
- **Per-tile repo init** — `code.go:100-106`: `init`, `add`, `commit` on the tile directory.
- **Code panel reads** (tile dir, no network), all in `code.go`:
  - `remote get-url origin` (:126, `componentRemote`)
  - `log --numstat` (:264, `apiGitLog`)
  - `log <ref>` (:341, `gitActivitySeries`)
  - `symbolic-ref refs/remotes/origin/HEAD` and `rev-parse --verify origin/main|master` (:369, :375, `upstreamTrackingRef`)
  - `diff --no-ext-diff --no-textconv HEAD` (:403-405) and `show <rev>` (:412), in `apiGitDiff`
- **Clone** — `clone.go:134-137`: `add -A` and `commit` in the new copy.
- **Git import** — `gitimport.go:62-68` (`remoteGit`) runs with host networking and the daemon's `~/.ssh` bound read-only at `/root/.ssh`:
  - `ls-remote --symref` (:83, no directory bound);
  - `clone` (:170, target read-write);
  - then `checkout <ref>` via `runGitIn` (:176, no network).
- **Template repos** — `templaterepo.go`:
  - `materializeTemplateRepo` (:80-93) runs `init`, `add`, `commit "template snapshot"` and `update-server-info` in `.xbin/template-repos/<name>`.
  - `SeedInstanceRepo` (:114-135) runs `init`, `fetch <tpl> main` (template bound read-only), `update-ref`, `reset --mixed`, `add`, `commit`.
  - `AddTemplateRemote` (:145-150) runs `remote get-url`, `set-url` or `add`.
  - `apiTemplateUpdates` (:176-201) runs `remote get-url` on **every readable component**, `rev-parse main` and `rev-list --max-parents=0 main` on the template repo, and `merge-base --is-ancestor` twice.
- **Builtin updates** — `/home/magik6k/buxon/internal/builtins/updates.go`:
  - `patchSeries` (:733-800) works in a temp repo in host tmp: `init`, `add`, two commits, `format-patch --stdout -1 HEAD`.
  - `mergeFile` (:821-843) runs `merge-file -p --diff3` in a temp dir, for the legacy "merge" mode.
- **Agent snapshot diffs (D77)** — `/home/magik6k/buxon/internal/term/agentdiff.go`:
  - `newSnapper` (:89-110) creates a private bare git dir in host `/tmp` (`xbin-agentdiff-*`), init at :103.
  - `snapper.git` (:115-124) runs every command with `--git-dir=<private> --work-tree=<tile>`, the private dir read-write and the tile read-only, 8 s timeout.
  - Commands: `add -A --ignore-errors` and `write-tree` (:273-279), then `diff-tree` with `-p`, `--name-status` and `--numstat` (:295, :314, :347).
  - `agentdiff_full.go:97-103` runs `diff-tree -p` with the private dir read-only (30 s, 16 MiB cap). It backs `GET /term/sessions/{id}/diff` (`/home/magik6k/buxon/internal/server/agentapi.go:39,533-554`).
  - The tile's own `.git`, index and HEAD are never touched.
- **Non-git confined runs**: `go build` (`/home/magik6k/buxon/internal/runner/build.go:38-97`) and `mkfs.erofs` (`/home/magik6k/buxon/internal/vm/image.go:45-77`).
- **Never done anywhere**: push, `am`, merging into a tile, checking out branches (other than the import ref), fetching remotes other than import and the local template fetch.

**Code PRs (D48, `DECISIONS.md:1025-1051`)** — xbind runs no git here at all.
- Code is in `/home/magik6k/buxon/internal/broker/prs.go` (header :23-30). Proposals are stored at `data/prs/<CompKey>/<n>/{meta.json,series.mbox}` (:86-87, :307-327).
- The only validation is a check that the text contains `"diff --git "` (:280), with a 4 MiB cap.
- Marking a PR `merged` is just a declaration (`apiPRState` :552-631); xbind never checks the patch landed. Routes are at :76-84.

**Builtin updates as PRs (D49, `DECISIONS.md:1053-1073`)**
- `mode:"pr"` (`tiles.go:163-173`) calls `ProposeBuiltinPR` (`prs.go:335-386`), which calls `Updater.Propose` (`updates.go:625-700`, recipe text at :682-686), which calls `patchSeries`.
- Closing it merged calls `RecordApplied` (`prs.go:623-628`, `updates.go:705-727`).
- The replace and merge modes instead write files directly on the host (`updates.go:504-604`).

**Template ancestry (D50, `DECISIONS.md:1075-1096`)** — the template-repo operations above. The actual merge is always the builder's own `git fetch template && git merge template/main`.

### 3. `internal/confine`

File: `/home/magik6k/buxon/internal/confine/confine.go`.

**API**
- `Configure(rootfs)` / `Isolated()` (:47-60). `Configure` runs in the boot step `stepConfine` (`boot.go:572-594`), before the registry and broker steps.
- `Net` (:62-69):
  - `NetNone`: an empty network namespace, the default.
  - `NetInternet`: egress relay under a `net:internet` policy, started by confine itself (:189-204).
  - `NetHost`: the host network.
- `Cmd` (:71-84):
  - `Argv`, `Env`, `Net`, `Stdin`.
  - `Dir`: bound at the same path, read-write unless `ReadOnlyDir`.
  - `Binds`: extra mounts, with helpers `RO`/`RW`/`Mask`/`MaskOpen` (:239-248).
  - `Timeout` (default 2 min) and `MaxOutput` (default 64 MiB per stream).

**What each run mounts and how it is locked down** (`Run`, :118-216)
- An overlay with the base rootfs as the lower layer and a throwaway tmpfs upper. It uses fuse-overlayfs when available (`/home/magik6k/buxon/internal/sandbox/launch_linux.go:58`).
- Entry is `/usr/bin/env <argv>`, with PATH, `HOME=/tmp` and LANG set, plus the caller's env.
- xbind's uid is mapped to root inside, which is why `safe.directory=*` is needed.
- `Unprivileged`: no capabilities and a seccomp block-list (`sandbox.go:194-200`).
- Namespaces: user, mount, pid, ipc and uts, plus net unless `NetHost` (`launch_linux.go:122-126`).
- On timeout, PID 1 of the sandbox is killed (:180-183).

**How outputs return**
- `Result{Stdout,Stderr []byte}` is **fully buffered and capped** (:250-265). Nothing streams out, though `Stdin` can stream in.
- A non-zero exit gives `*ExitError{Code,Stderr}`, readable with `ExitCode`.
- A timeout gives a "timed out after …" error.
- If the sandbox can't start, the result is `ErrUnavailable`. With isolation on there is never a host fallback.
- With isolation off, the tool runs directly with xbind's env minus `GIT_*` (:140-143, :229-237).

**Git hardening** (`git.go`)
- Flags (:14-23): fsmonitor off, hooks off, untracked cache off, `safe.directory=*`, `protocol.ext.allow=never`, gpg signing off, `core.pager=cat`.
- Environment (:28-34): no system or global config, no prompts, no optional locks, `LC_ALL=C`.
- Wrappers `Git`/`GitRead`/`GitCmd`/`GitBytes` are at :36-68.

**Cost and latency**
- D78 measured about 45 ms per run with fuse-overlayfs, about 17 ms with kernel overlay (`DECISIONS.md:1995-1997`).
- Each run also writes a spec JSON to host `/tmp` and re-execs xbind as `__sandbox-init` (`launch_linux.go:105-127`).
- It adds up per request:
  - `/git/activity` makes up to about 6 runs;
  - `/templates/updates` makes at least one run per component;
  - an agent tool call makes 2 snapshot runs, plus 3 more when it reports a diff.
- A snapshot slower than 8 s turns diffs off for that session (`agentdiff.go:41,237-244`).

**Enforcement**
- `TestNoDirectExec` (`guard_test.go:13-66`) scans `internal/` and `cmd/xbind`. It exempts `internal/confine`, `internal/sandbox` and `internal/agent/host`, and accepts a call only with `exec-ok:` on that line or the two above.
- There is also a hostile-repo integration test (`sandbox_linux_test.go:40-81`).

**Under VM sandboxes (D89 `DECISIONS.md:2484-2533`, D90 :2535-2579)**
- confine never sets `Spec.VM`, so confined runs are always host namespace sandboxes, whatever the VM policy says.
- That still works for VM terminals and agents: the guest's files are the same host directories, exported over FUSE/vsock (`/home/magik6k/buxon/internal/vm/manager.go:157-294`).
- VM backends are still built through `buildConfined` (`runner.go:370-377`) before the VM is applied (`runner/vm.go:56-83`).
- The only VM-specific confined run is building the guest image (`image.go:62-67`).

### 4. Code-editing surfaces

**Terminals** (`/home/magik6k/buxon/internal/term/term.go`)
- `ServeWS` (:180-246) refuses the root terminal (:221-224) and requires terminal access to the tile (:225-229).
- `openOptsFor` (`openopts.go:14-36`) marks non-admins as restricted and gives them the D40 view.
- `shellCmd` (:426-479) returns an error when the sandbox can't start — never a host shell.

**Mounts** (`sandboxShell` :547-715, bind builders in `binds.go`)
- **Admin terminals** (`scopedBinds`, `binds.go:30-73`):
  - workspace read-only, recursive;
  - `.xbin`, `data` and `homes` masked (:49-53);
  - own `$HOME` read-write;
  - **the tile directory itself read-write at its host path, `.git` included** (:57-58);
  - the SDK read-only.
- **Restricted terminals (D40)** (`stageView` + `scopedBindsView`, `binds.go:84-142`):
  - a staged view dir `.xbin/term/view-*`, read-only at the workspace root;
  - readable tiles read-only;
  - own tile read-write (:136-137);
  - `$HOME` read-write.
- **Guards**: mount guard, restricted mode and Landlock read guard (`term.go:583-608`).
- Readable sibling tiles are mounted with their `.git`, so `git clone "$XBIN_WORKSPACE/apps/b"` works from any terminal that can read `apps/b`.

**Agent sessions (D74, `DECISIONS.md:1828-1878`)**
- `createAgent` uses the same `shellCmd`/`sandboxShell` (`agent.go:242-247`). The only addition is the daemon's `bx`, bound read-only at `/opt/xbin/bin/bx` as the entry point (`term.go:558-560,591-593`).
- The agent host's file writes are confined to the tile directory (`/home/magik6k/buxon/internal/agent/host/fs.go:58-74`).

**Editing in a terminal is editing the live code.**
- Backends bind that same tile directory read-only (`runner.go:629-630`).
- Go is built from the working tree with `-buildvcs=false` (`runner.go:361-377`, `build.go:85-89`).
- `/c/` serves the working tree and blocks `.git` paths (`/home/magik6k/buxon/internal/server/static.go:346-361`).
- The watcher ignores dot-directories (`/home/magik6k/buxon/internal/watch/watch.go:61-74`). A commit on its own triggers no reload; a checkout does, because it changes files.

**"Code sandbox" concepts** — there is no separate checkout, worktree or staging copy. What exists:
- `api=0` gives a "code-only" terminal or agent with no API token (`term.go:185-187`, `agent.go:123`).
- The per-tile terminal dev layer `.xbin/term/<key>/{upper,work}` (`term.go:610-642`, VM disk at :626-632) holds system state such as apt installs, not code.
- The backend's env layer `.xbin/env/<key>/<hash>` (`plans/component-env.md`) sits under a runtime sandbox that sees the source read-only.

**D40 text** is at `DECISIONS.md:846-864`. **Other writers into tile directories** are host-side file writes by xbind, with no git: the builtin replace/merge updates, `CopyTree`, the clone copy, and backup restore (`backup.go:356-369`). There is no browser source editor and no API that writes source.

### 5. Git-related UI, CLI and HTTP API

**UI**
- The terminal window is `bx-frame` (`/home/magik6k/buxon/web/bx-frame.js`). Its layouts are `term|code|split|logs|prs` (:95, :840-856), and each tab has net/api/gpu/vm pickers (:856). **There is no branch UI.**
- `web/bx-code.js` (:1-11, :224-262) is read-only, with three tabs:
  - Files;
  - Changes: working tree and per-commit diffs;
  - Analysis: commit activity, including the upstream ref.
- `web/bx-prs.js` (:1-12, :224-250) is the ⇄ PR panel: list, diff, thread, comment, Merged/Reject. It shows the `bx code pr fetch N | git am --3way` command rather than applying anything.
- The shell's ⇄ badges come from `workspace-template/shell/bx-shell.js:120,1273`.
- The admin tile has a "code & history" drill-in (`workspace-template/tiles/admin/tabs/runtime.js:101-126`). The shell's `bx-tile-admin.js` has no git UI.
- The Tile Manager (`workspace-template/tiles/manager/index.html`) has:
  - import from git (:163-165, :339, :367);
  - the template-updates recipe (:398-408);
  - builtin updates with "Propose as PR" (:454-493).

**CLI**
- `/home/magik6k/buxon/cmd/bx/code.go:40-324`: `bx code prs` and `bx code pr <target> | show | fetch | comment | close`.
- `cmd/bx/template.go:12-62`: `bx template updates`, which just prints the fetch/merge recipe.
- `cmd/bx/builtin.go:75-92`: `bx builtin update --pr`.
- Documented in `docs/bx.md:92-98,194-215`.

**HTTP API** (`/home/magik6k/buxon/docs/protocol.md`)
- :761-772 `/term/sessions/<id>/diff`
- :1344-1358 `/builtins/update` (including `mode:"pr"`)
- :1359-1389 `/templates`, `/templates/new`, `/templates/updates`, `/templates/{repo}/{rest...}`
- :1391-1420 `/code/tree`, `/code/file`, `/git/log`, `/git/diff`, `/git/activity`, `/git/remote-info`, `/git/import`
- :1422-1458 `/code/prs`, `/code/prs/summary`, `/code/pr`, `/code/pr/series`, `/code/pr/comment`, `/code/pr/state`
- :2252 the `files.changed` event

Routes are registered at `code.go:34-42`, `prs.go:76-84`, `templates.go:24-31`, `tiles.go:18-23` and `agentapi.go:39`. Access rules:
- code and git reads: admin, or a `code[:tile]` grant (`code.go:147-164`);
- PRs: anyone who can read the target can propose (`prs.go:95-111`).

### 6. Blessed remote or xbind-hosted git server

**What exists: a read-only dumb-HTTP server for builtin template repos, nothing else.**
- The repos live in `.xbin/template-repos/<name>/` and are refreshed every boot (`boot.go:365`, `templaterepo.go:44-95`).
- `serveTemplateRepo` (`templaterepo.go:214-227`) serves them with plain `http.ServeFile`: no git process, GET only, any authenticated principal.
- Git inside a sandbox reaches it because `http://xbin/` is rewritten via `GIT_CONFIG_COUNT` (`insteadOf` plus an `extraHeader` carrying the session's tile-scoped token) at `term.go:784-795`. That only works with an API token and a reachable `XBIN_URL`, so `api=0` and `net=none` terminals can't fetch.

**What does not exist:**
- no smart HTTP (no upload-pack, receive-pack or http-backend anywhere);
- no per-tile bare repo;
- no push endpoint;
- no `refs/xbin/*`.

The only bare repos are the throwaway per-session snapshot dirs in `/tmp`.

**The `git push` namespace is a plan only.**
- `plans/code-prs.md:86-94` (a later rung: smart-HTTP push restricted to `refs/xbin/pr/*`), :196 and :209-210.
- D48 chose mbox patches and a broker-owned `data/prs/` over refs in the target repo, to avoid "foreign refs polluting component repos" (`DECISIONS.md:1035-1043`).

### Things that matter for the branch-deployment design

1. **confine can't stream output.** stdout comes back only when the run finishes, fully buffered and capped. A smart-HTTP git server run through confine would need a streaming variant.
2. **A tile's `.git` is untrusted.** Its refs, config and branches are writable by that tile's terminals and agents, so any branch xbind reads must be read inside confine. The two existing patterns are the template-repo checks (local plumbing only) and D77's private git dir with the tile mounted read-only as the work tree.
3. **`.xbin/` is the natural home for repos xbind owns.** It is masked or absent in every terminal, and the template repos already live there.
4. **Doc drift found along the way:**
   - `plans/templates.md:89,93` says the template endpoint is admin-only and uses the owner's token; the code allows any authenticated principal and uses the tile-scoped token.
   - The PR routes shipped as query-parameter routes, not the path-style ones in `code-prs.md:147-154`.
   - D74's title still mentions vault keys, which its own text says were reverted.
