# Side findings from the dev-lifecycle research

> Status: live — defects and doc drift found while researching the
> dev-lifecycle design ([../README.md](../README.md)). None of these is part of
> the design; each needs its own fix or a decision. Facts as of master 926046d
> (2026-09-27). The source research file is named after each item.

## Security

1. **cgi tiles run on the host as xbind, even under `--isolate`.** Needs an
   urgent, separate fix.
   - **What happens:** `Proxy.ServeHTTP` sends `runtime:"cgi"` to `serveCGI`
     (`internal/proxy/proxy.go:175-178`). That runs the tile's
     `backend/handler` with the standard library's `net/http/cgi.Handler`
     (`proxy.go:239-261`), which execs the script directly with
     `Dir: comp.Dir` and the daemon's `PATH`/`HOME`. There is no sandbox.
   - **Why it matters:** anyone who can write the tile — a tile terminal user,
     or a coding agent in the tile's sandbox — can set `"runtime":"cgi"` and
     request `/api/<tile>/…` with their own terminal token (self access) to run
     code as xbind. That breaks D78 ("nothing runs with xbind's privileges on
     data a sandbox can write").
   - **Why no test caught it:** `TestNoDirectExec`
     (`internal/confine/guard_test.go`) only matches direct exec calls, so it
     never sees `cgi.Handler`. No test covers cgi.
   - **Fix direction:**
     - run cgi handlers through the backend sandbox (a per-request confined
       exec, or a sandboxed long-lived CGI host);
     - fail closed under isolation until that exists;
     - teach the guard test to flag `cgi.Handler`.
   - Sources: runner-hot-reload.md (security finding), serving-fabric.md
     (hazards); verified by reading `proxy.go`.
2. **Non-bus hub events reach every subscriber, including users who can't
   read the tile.** A build error's compiler output and tile names go out to
   everyone (`internal/server/server.go:592-610`). This is an info disclosure,
   and deployments would multiply it. Source: ui-surfaces.md (hazards).

## Data-handling defects

3. **Offload never frees encrypted file-resource data.** `removeScopeData`
   drops the kv buckets and `data/resources/<scopeKey>`, but leaves
   `data/resources-enc/<scopeKey>` and the live mount
   (`internal/broker/backup.go:449-512`, `:471-489`). The docs' "archived +
   removed" is untrue for fs/sqlite/blob. Sources: data-plane.md,
   identity-resources.md.
4. **Restore merges instead of replacing.**
   - `loadKV` puts into existing buckets, and `writeFileFrom` replaces files
     one by one (`backup.go:330-371`).
   - So a restore leaves stale keys and files behind.
   - Source: data-plane.md.
5. **Backups drop links and race the running backend.**
   - `backup.Writer.Tree` drops symlinks, special files and xattrs.
   - It opens each path after a `WalkDir` type check, so a running backend can
     swap a symlink in between (`internal/backup/backup.go:127-130`).
   - Scheduled backups don't stop the backend (`backup_cron.go:83-94`).
   - Source: data-plane.md.
6. **A re-created tile inherits `lifecycle[path]`,** e.g. `hidden` or
   `disabled`. D82's leftover check doesn't look at it, and nothing clears it
   (`internal/broker/policy.go:211-258`). Source: identity-resources.md.
7. **`util.ScopeKey` is not injective:** `apps~x` and `apps/x` map to the same
   data key (`internal/util/util.go:127-132`). Sources: data-plane.md,
   terminology-census.md.
8. **Inference (not verified):** tier-2 scope-uid backends may be unable to
   open normal-mode gocryptfs mounts owned by root xbind, because those mounts
   have no `allow_other` (`internal/resenc/resenc.go:177-181`). Source:
   data-plane.md.

## Behaviour that contradicts the docs

9. **`"vm": true` without `--isolate` runs as a plain host process.**
   `docs/elements.md:56-61` says it fails with a reason (`runner/vm.go:40-42`;
   `boot/vm.go:17-36`). Source: runner-hot-reload.md.
10. **Instance tokens die at process exit, not at the swap.** D8 and
    `docs/elements.md:539` say otherwise. The old generation keeps a live
    token through the 30 s drain (`runner.go:591-610`). Sources:
    runner-hot-reload.md, identity-resources.md, inbound-edges.md.
11. **Compiler output is not written to the tile log.**
    `plans/dev-flow.md:92-93` says compiler errors appear in the log tail.
    Source: runner-hot-reload.md.
12. **`plans/implementation.md:168-183` describes a runner that doesn't
    exist.**
    - It describes a `next` build slot, a global build semaphore, a `/healthz`
      check, rotated logs streamed to the hub, and in-flight build
      cancellation.
    - Reality: one `bin` per tile, no semaphore beyond alwaysOn's limit of 2,
      a socket-dial health check, append-only logs, and queued builds.
    - Source: runner-hot-reload.md.
13. **A disabled or hidden tile's page still loads.** Static serving never
    checks lifecycle, and the watcher keeps sending `reload` for disabled
    tiles. The "placeholder" in `docs/overview/14-lifecycle.md:39-41` is
    whatever the tile does with a 409. Source: runner-hot-reload.md.
14. **Template remote access is wider than documented.**
    `plans/templates.md:89,93` says the template endpoint is admin-only and
    uses the owner's token. The code allows any authenticated principal, with
    the tile-scoped token. Source: git-confine.md.
15. **Code-PR routes shipped as query-parameter routes,** not the path-style
    ones in `plans/code-prs.md:147-154`. Source: git-confine.md.
16. **D74's title mentions vault keys,** which its own text says were
    reverted. Source: git-confine.md.
17. **The workspace repo does not ignore component subtrees.** CM-1 (amended)
    says it does, so every tile shows as an embedded repo in the root's
    `git status`. Source: git-confine.md.
18. **`bx new <path>` without `--owner` creates no repo.** The repo appears
    only at the next `EnsureComponentRepos` (a restart or another structure
    change). Source: git-confine.md.
19. **Stale path-reservation docs.**
    - The o/u positional path reservations are still described in
      `docs/overview/02-workspace.md:55`,
      `docs/overview/03-components.md:45-46`,
      `docs/overview/07-users-orgs.md:116` and `README.md:66`, though D24
      dropped them.
    - The reserved-top table omits `owner`.
    - Source: terminology-census.md.
20. **`website/index.html:340` shows `bx grant` with three positionals,**
    which bx rejects. Source: terminology-census.md.

## Tooling and test gaps

21. **The `confine` helpers mount more than needed.**
    - `confine.GitRead` has no production caller.
    - `runGitIn` binds the tile read-write even for read-only commands
      (`internal/broker/code.go:64-80`).
    - Source: git-confine.md.
22. **`bx logs` reads `.xbin/log/<CompKey>.log` directly**
    (`cmd/bx/main.go:507-515`), but `.xbin` is masked in isolated tile
    terminals. So, by inference, it already fails there; `GET /logs` works.
    Source: builder-contract.md.
23. **The runner and watcher have no unit tests.** `plans/dev-flow.md`
    promises table-driven suites and a fake exec. Hot swap is covered only
    end-to-end, on a non-isolated daemon (`test/integration_test.go:179-233`).
    Source: delivery-infra.md.
24. **Some integration tests never run under `make integration`.**
    `internal/sandbox`'s integration tests are outside the package list in
    `Makefile:110-117`. CI never builds a rootfs with git, so the sandboxed
    tool-run tests skip there. Source: delivery-infra.md.
25. **Some handlers ignore the documented error shape.** Handlers in
    `internal/boot/*.go` use `http.Error` instead of `server.WriteError`, and
    no guard enforces it. Source: delivery-infra.md.
