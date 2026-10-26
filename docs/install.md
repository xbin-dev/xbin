# Installing and running xbin

This page covers installing xbin, the first login, and day-to-day running.
[overview/15-operations.md](/docs/overview/15-operations.md) is the full
reference: the deployment's layout, the systemd unit, what boots in what
order, every flag, observability and upgrades. Building in a running
workspace starts at [getting-started.md](/docs/getting-started.md).

## Where it runs

xbind runs the workspace on a Linux host, and it **is the sandbox runtime**
for its components: it builds each one's namespaces, overlay rootfs and
egress relay itself. It runs rootless, using unprivileged user namespaces.
Building sandboxes from *inside* someone else's unprivileged container is
fragile (nested user namespaces, missing devices), so xbin runs on a **Linux
VM or bare-metal host it controls**. The host or hypervisor is the outer
boundary, and each component's namespaces are the inner one.

No root is needed, but the host must provide:

| Requirement | Why |
|---|---|
| **Unprivileged user namespaces** (`kernel.unprivileged_userns_clone=1`; on by default on Debian/Arch) | Builds the sandbox user namespaces. xbind logs whether isolation came up. |
| **`/etc/subuid` + `/etc/subgid`** delegating a range to xbind's user (e.g. `xbin:100000:65536`) | Maps a *full uid range*, so `apt`, `sudo` and non-root users inside containers work. Without it xbind falls back to a single uid: backends still run, but apt and switching users won't. |
| **`newuidmap` / `newgidmap`** (the `uidmap` package) | Apply that range rootlessly (setuid, or file caps `cap_setuid,cap_setgid`). |
| **`/dev/fuse`** | Each sandbox root is mounted with fuse-overlayfs, so unprivileged directory renames work (`apt install`). xbind ships a static fuse-overlayfs. Without `/dev/fuse` it falls back to the kernel overlay. Encrypted resources (gocryptfs) need FUSE too. |
| **`/dev/net/tun`** | The per-network-namespace egress relay. Needed for any `net:*` grant and for the terminal's internet scope. |
| **KVM** (optional) | VM sandboxes: terminals and backends in a Firecracker microVM, root in their own kernel. Needs `/dev/kvm` usable by xbind's user (the `kvm` group). Without it (most small cloud VMs) VMs run emulated under QEMU, much slower. See [isolation.md](/docs/isolation.md) §VM sandboxes. |
| **cgroup v2** (optional) | Per-component memory, pids and CPU **limits**, plus accounting. Under systemd, `Delegate=yes`. |
| **NVIDIA driver** (optional) | Enables `gpu:*` grants. Components and terminals get GPUs by binding the world-readable `/dev/nvidia*` and the host's driver libraries (rootless, no container toolkit). |

The base rootfs (Go, Node, Python, the agent CLIs and `bx`) is mounted
read-only under every sandbox and terminal. Release bundles ship it; from
source it is `make rootfs`, built from `docker/rootfs.Dockerfile`.

## Install

One command, in either of two modes:

```sh
# system-wide: a system service under a dedicated `xbin` user in /opt/xbin
curl -fsSL https://xbin.dev/install.sh | sudo bash

# user-only: no root anywhere. Runs as YOU, in ~/.local/opt/xbin, as a
# systemd *user* unit (with lingering, so it survives logout)
curl -fsSL https://xbin.dev/install.sh | bash -s -- --user
```

The bootstrap at `xbin.dev/install.sh` runs the latest release's
`deploy/install.sh`. Pin a release with `XBIN_VERSION=vX.Y.Z`. To run
master's copy instead, fetch
`https://raw.githubusercontent.com/xbin-dev/xbin/master/deploy/install.sh`.

Run it with no mode flag and no sudo and it explains the difference (system
mode creates a dedicated `xbin` user, for better separation). It shows
**both** numbered plans, probing for system mode read-only, and asks whether
to escalate to the system install via sudo, do the user install, or quit.

**On a Mac**, the same one-liner (no sudo) runs
[`deploy/install-macos.sh`](https://github.com/xbin-dev/xbin/blob/master/deploy/install-macos.sh)
instead:
- It sets up a lightweight Linux VM with [Lima](https://lima-vm.dev). You pick
  the size at install time; the defaults are a 32 GiB thin-provisioned disk,
  4 GiB RAM and 4 CPUs (`XBIN_VM_DISK`, `XBIN_VM_MEM`, `XBIN_VM_CPUS`).
- Inside the VM it runs the regular Linux installer in system mode, pinned to
  the same release.
- It forwards the UI to `http://127.0.0.1:8642` on the Mac, on loopback only.
- It needs Homebrew (to install Lima) but never installs Homebrew itself.

Re-run it to upgrade; `limactl delete xbin` removes everything.

**Before touching anything**, the installer prints a **numbered plan of
exactly what this run will do**: the user it will create or reuse, the
packages, the subuid range, the unit path, the bundle or build steps, and the
workspace directory. Steps already in place are listed as skipped. It then
asks once. `--yes` skips the question; `--check-only` stops after the
preflight report and the plan.

**User mode** never escalates:
- Anything that would need root is found in preflight and reported with the
  exact one-line root command to run, and the installer stops before changing
  anything. That covers missing packages (`uidmap`, `fuse3`, …), a missing
  `/etc/subuid` range for your user, and an AppArmor userns restriction.
- Most distros pre-provision subids for normal users, so on a typical machine
  it just works.
- If lingering can't be enabled without root, it says so (the service then
  runs only while you're logged in) and prints the `sudo loginctl
  enable-linger` line.

**A machine that already runs a system-wide xbin** is detected (read-only, no
root needed), and the installer leads with upgrading it: plain Enter upgrades
via sudo. User mode is then offered as a *separate second instance*, with its
own workspace and the next free port (e.g. `127.0.0.1:8643`). An explicit
`XBIN_LISTEN` that is already in use fails the plan up front.

The installer is interactive and idempotent (re-run to upgrade). It:
- **Preflights** the kernel features above. `-s -- --check-only` prints just
  that report.
- **Installs** `uidmap`, `fuse3` and `git` for your distro
  (apt/dnf/pacman/zypper).
- **Downloads and verifies the release's prebuilt bundle** for the host's
  architecture (amd64, arm64): `xbind`, `bx`, a static `fuse-overlayfs` and
  `gocryptfs`, the base rootfs and the Go SDK. It needs no Go and no podman.
  It builds from source only for an untagged ref (master), an architecture
  without a bundle, or `--build-from-source`; that path also installs Go and
  podman.
- **Creates** the `xbin` system user with home `/opt/xbin`, delegates it a
  `/etc/subuid`+`/etc/subgid` range, and verifies a user namespace comes up
  for that user.
- **Lets AppArmor's `fusermount3` mount encrypted resources** under the
  workspace, with one marked block in `/etc/apparmor.d/local/fusermount3`,
  replaced in place on every run.
- **Turns VM sandboxes on** for a workspace that has no VM policy yet:
  terminals, plus backends where KVM is usable. It never touches a policy an
  admin set.
- **Installs and starts** the service, waits for `/healthz`, and prints your
  one-time login URL.

What it leaves on disk in system mode:

```
/opt/xbin/bin/{xbind,bx,fuse-overlayfs,gocryptfs}   # owned by the xbin user
/opt/xbin/rootfs/                          # the unpacked base rootfs
/opt/xbin/sdk/                             # Go SDK source (go.work + terminal builds)
/opt/xbin/workspace/                       # your data (initialised on first boot)
/etc/systemd/system/xbin.service           # rendered from deploy/xbin.service
/etc/xbin/xbin.env                         # optional vault passphrase, mode 0600
```

User mode uses `~/.local/opt/xbin`, `~/.config/systemd/user/xbin.service`
and `~/.config/xbin/xbin.env`, and links `bx` into `~/.local/bin`.

From there it's plain systemd: `systemctl status xbin`, `journalctl -u xbin
-f`. The login URL is in the log: `journalctl -u xbin | grep login`.

The service binds **loopback only** by design; see
[Exposure](#exposure) to reach it.

Upgrade by re-running the script. It takes a fast path: it swaps the
binaries, rootfs and SDK and restarts. The user, the vault and the workspace
are untouched, except for a VM policy nobody ever set.

Uninstall with `systemctl disable --now xbin && rm
/etc/systemd/system/xbin.service && userdel -r xbin`.

## First login, then an account, then lock the door

The login URL, `http://127.0.0.1:8642/login?token=…`, carries the
**owner/bootstrap** credential: the root key, not a person's account. Move to
real accounts and close that door:

1. **Open the token URL.** You are now the owner, a full admin.
   - If you chose **manual unseal** at install, the vault boots
     *unconfigured*. Open **admin tile → vault** and set the passphrase once:
     it creates the barrier and cannot be recovered.
   - After every daemon restart, unseal there again, or with `bx vault
     unseal` ([auth.md](/docs/auth.md)).
2. **Create an admin user**: **admin tile → Users → add user**, role
   `admin`. The `admin` role reaches **every** tile. Regular users get an
   explicit tile allow-list and no terminal by default.
3. **Sign out, then sign back in** as that user, so your session is a real
   account and not the owner-token cookie.
4. **Disable token login**: **user management → sign-in → Disable token-URL
   login**.
   - The `…/login?token=` URL and the owner-token cookie stop working; from
     then on everyone signs in with an account.
   - The `bx` CLI's `Bearer` token is deliberately unaffected. To revoke it
     too, rotate `.xbin/token` and restart.

Skipping steps 2–4 is fine on a solo machine behind Tailscale, where the
login token is the only lock. Do them before you invite anyone else in.

**Wiring it up by hand?** The unit is
[`deploy/xbin.service`](https://github.com/xbin-dev/xbin/blob/master/deploy/xbin.service)
and the installer is
[`deploy/install.sh`](https://github.com/xbin-dev/xbin/blob/master/deploy/install.sh);
both are short and commented. By hand, xbind runs as:

```sh
xbind --isolate --rootfs /opt/xbin/rootfs \
       --workspace /opt/xbin/workspace --listen 127.0.0.1:8642
```

The one rule: **don't add systemd namespace or filesystem hardening**
(`PrivateUsers`, `RestrictNamespaces`, `ProtectSystem=strict`,
`NoNewPrivileges=yes`, …). xbind builds the sandboxes itself, and those
directives break it. The boundary is the VM, kept on loopback behind
Tailscale or a proxy.

## Operating

**Data.** Everything is the workspace directory:
- Source, manifests and the grant table are plain files you can commit.
- `.xbin/` (build cache, sockets, logs) and `data/` (resource state, vault,
  kv) are the runtime parts.
- To back up, copy the workspace directory (`tar` it, minus `.xbin/`), or
  run `bx backup <tile>` to snapshot one tile to a bound **archiver tile**.
  That covers its source, resource data and dev layer, scheduled or on
  demand, with offload and restore on the same path.
- Upgrades migrate the workspace schema forward untouched; roll back by
  pinning the previous version.

**Vault.** Production never stores secrets in the clear. There are two ways
to run the encryption barrier:
- **Env auto-unseal**: set `XBIN_VAULT_PASSPHRASE` (a systemd
  `EnvironmentFile`, or a secret store). Hands-off; the passphrase lives in
  the process environment.
- **Manual unseal** (stronger): leave it unset. xbind boots with the vault
  **locked**: it runs and you can sign in, but secret storage is refused until
  an admin unseals it once (`bx vault unseal` or the admin console). The
  passphrase never touches the environment or disk; you re-unseal after each
  restart.

The barrier also encrypts **resource data** (kv, filesystem, sqlite, blob),
not just secrets: see [resources.md](/docs/resources.md).
- `--insecure-vault` (or `--no-auth`) stores everything in plaintext, for
  throwaway setups.
- A bare `--dev` encrypts with a built-in, insecure dev key, so `make dev`
  exercises encryption.

Lose the passphrase and encrypted data is unrecoverable.

<a id="exposure"></a>**Exposure.** xbin runs arbitrary code by design and
does no TLS itself. Bind it to `127.0.0.1` and reach it over **Tailscale**,
or through a **TLS reverse proxy** (Caddy or Traefik; the session cookie
becomes `Secure` behind `X-Forwarded-Proto: https`). Never expose the port
raw to the internet. To publish a tile itself (HTTP(S), TCP or UDP), use
ingress ([ingress.md](/docs/ingress.md)), not the workspace port.

**Sysctl** (on the host, once, for large workspaces): raise
`fs.inotify.max_user_watches` ([getting-started.md](/docs/getting-started.md)).
`bx doctor` checks the effective limit.
