# 2026-09-24 — tools on your tile run in a sandbox, never as xbind (D78)

## What changed

Everything xbind itself runs on workspace data now runs in a throwaway
sandbox when the workspace is isolated (`--isolate`, the default install):
git for the Code panel (history, diffs, activity), a new tile's repo, fork,
import from a git URL, template instances and their update check, builtin
update proposals and merges, the Agent tab's "changed files" snapshots —
and **`go build` of Go backends**. A terminal or agent session whose sandbox
cannot be set up now fails with an error; it used to fall back to a shell
on the host.

For a Go backend build that means (docs/isolation.md → "Confined tool
runs"):

- the host's Go toolchain, read-only — same version as before;
- the workspace read-only (`go.work` and its `use`d modules resolve as
  before) with `.xbin/`, `data/`, `homes/` masked, plus the SDK;
- per-tile build and module caches under `.xbin/cache/tile/<tile>/` (the
  first build of each tile compiles the standard library once);
- modules already in the host's module cache are read from it offline; new
  ones download over **public addresses only**;
- `-buildvcs=false`: binaries carry no `vcs.*` build settings;
- a `replace` to a path outside the workspace and the SDK does not resolve.

An import from a URL still runs on the host's network with the daemon's
`~/.ssh` (read-only), but without the daemon's global git config.

## Who's affected

- Go tiles whose `go.mod`/`go.work` `replace` a directory outside the
  workspace (not the SDK) — the build fails with "replacement directory …
  does not exist".
- Workspaces whose Go builds fetch modules from a GOPROXY or VCS host on a
  private network (LAN, VPN) — new modules fail to download.
- Tiles reading `vcs.revision`/`vcs.time` from `runtime/debug.ReadBuildInfo`
  — the settings are absent.
- Operators who relied on a credential helper in the daemon user's global
  git config for HTTPS imports of private repos.
- Hosts where the terminal sandbox cannot start (no rootfs, no user
  namespaces) under `--isolate` — terminals now refuse to open instead of
  giving everyone a host shell.

Nothing changes for workspaces running without isolation, and nothing for
tiles that use only the SDK and public modules.

## How to migrate

- Outside-workspace `replace`: move that module into the workspace (e.g.
  under `lib/`) and point the `replace` there, or vendor it (`go mod
  vendor`).
- Private network modules: set `XBIN_BUILD_NET=host` in xbind's
  environment (`/etc/xbin/xbin.env`, or `~/.config/xbin/xbin.env` for a
  user install) and restart xbind — builds then share the host's network.
- VCS info: stamp it yourself at build time if you need it — e.g. a
  `version.txt` your backend embeds, written by your commit flow.
- Private HTTPS imports: use an `ssh://` or `git@host:path` URL with a
  deploy key in the daemon user's `~/.ssh`, or clone in a tile terminal.
- Terminal refuses to open: fix the sandbox (`bx doctor`, `journalctl -u
  xbin` names the reason) — or run without `--isolate` if the host cannot
  sandbox, knowing tiles then run as the xbind user.

## Why

A tile directory is written from inside sandboxes — terminals, coding
agents. Git treats a repo's `.git/config` as instructions: `core.fsmonitor`,
filter drivers and `diff.external` are commands it runs. So a Code panel
`git diff` on a tile, run by xbind on the host, executed whatever the last
writer of that tile put there — with xbind's privileges: every tile's vault,
every user's data, the host. `go build` does the same through VCS stamping.
The fix is structural: xbind never runs a tool on sandbox-writable data with
its own privileges (`internal/confine`, enforced by `TestNoDirectExec`).
