# Runtime & deployment model — the holy-grail runtime

Companion to `plans/isolation.md` (the isolation *mechanics*). This is the
*where it runs*: the deployment shift that makes proper per-component isolation
possible, and the component userland (a fat OCI rootfs) that keeps terminals a
great place to build.

## The problem with "the container is the boundary"

Today ARCHITECTURE §7 makes **one Docker container the security boundary**, with
toolchains baked in and buxond as PID 1. That was right while intra-workspace
isolation was attribution-only — but Tier 3 (`isolation.md`) needs buxond to
**create namespaces** (mount / net / user), attach **cgroups**, and run an
**egress relay**. Doing that *inside* an unprivileged container is exactly the
fragile part: nested user namespaces, missing `CAP_NET_ADMIN`, seccomp/AppArmor
double-confinement, cgroup-delegation and `/dev` friction. The container is the
boundary *because* we couldn't build boundaries inside it.

**So drop the container-as-boundary.** Move buxond down one level: it runs on a
host it controls, where it has (real or userns-delegated) authority to build
sandboxes. Two nested boundaries, each doing one job:

- **Outer boundary = the VM / host** (was: the container). The hypervisor is the
  hard wall against a full compromise. buxond is the box's primary service.
- **Inner boundary = per-component namespaces** buxond builds (new). This is
  where component-vs-component isolation and egress policy live.

buxond effectively *becomes the container runtime* for its components, instead of
living inside someone else's.

## Deployment targets (recommended → eventual)

1. **VM (recommended).** Run buxond on a Linux VM — a cloud instance, libvirt/
   Proxmox, or a **Firecracker/Cloud-Hypervisor microVM** for a tight, fast
   boundary. buxond runs as the primary workload (a systemd unit, or PID 1 under
   a tiny init). The hypervisor is the security boundary; namespaces are the
   intra-workspace boundary.
2. **Bare metal / a dedicated Linux host** — same shape, for self-hosters who
   want no hypervisor tax.
3. **Virtual appliance (eventual).** Ship a prebuilt, mostly-immutable image:
   `qcow2`/`OVA` for hypervisors, an installer/live **ISO** for bare metal, and
   cloud images (AMI/GCE/…). Contents: a minimal immutable host OS + kernel +
   buxond + the component base rootfs. Boot it, attach a data disk for
   `/workspace`, open the login URL. Built with mkosi/osbuild; **A/B** image
   updates so a bad upgrade rolls back.
4. **Docker (dev / interim, Tier 1 only).** The existing image stays for
   `make dev` and low-stakes single-tenant use, explicitly at **Tier 1**
   (container-as-boundary, no per-component isolation). Once the appliance
   exists, docs stop recommending it for production.

VMs are the honest answer once you want rootless per-component isolation, and an
appliance ISO/image is the "five minutes to running" story that `docker run`
gave us — just one level down.

## The component base rootfs (a fat OCI image)

Every sandbox — and every terminal — needs a userland. Ship one:

- A **fat OCI image, Ubuntu-based** (glibc, familiar, broad toolchain support):
  Go, Node LTS, Python 3, `build-essential`, git, ripgrep, curl, vim/nano, `bx`,
  **and the agent CLIs (`opencode`, `claude-code`)** so a freshly-opened terminal
  is a first-class AI-assisted builder shell with zero setup.
- Published as `ghcr.io/magik6k/buxon-rootfs:<tag>`, **unpacked** (skopeo/umoci,
  or buxond's own puller) into a content dir on the host. buxond bind-mounts it
  **read-only** as the base layer of every sandbox. Baked into the appliance;
  updatable by pulling a new tag (A/B, like the OS).
- **Keep it separate from the host OS.** The appliance OS stays minimal and
  security-sensitive (boot + kernel + buxond + namespaces); the fat dev userland
  is a *swappable workload layer*. Small trusted base, big convenient userland,
  no coupling.
- **Why OCI, not baked-only:** users can extend/swap it (a component needs Rust →
  add a layer), it reuses the ecosystem's build tooling instead of buxon shipping
  a package manager, and it fits the "it's just images/files" ethos of
  `tile-sharing.md`.

## The holy-grail component runtime (target)

For each component backend, buxond assembles a sandbox from:

- **user namespace** — map the component's uid; makes the rest work without host
  root where possible (rootless-capable).
- **mount namespace** — an **overlayfs** root: *lower* = base rootfs (ro) +
  granted `deps/*` (ro); *upper* = a per-component writable layer (ephemeral
  tmpfs, or persisted `.buxon/overlay/<comp>`); the component's **source dir**
  bind-mounted (ro from Tier 2 — runtime can't edit source); granted **resource
  files** rw; the **gateway unix socket**; private `/tmp`, minimal `/dev`,
  private `/proc`. (This realizes `isolation.md` §1/§5.)
- **network namespace** — default-deny IP; gateway socket always reachable; IP
  egress only through the transparent relay under the `net:*` grants
  (`isolation.md` §3).
- **pid + ipc + uts namespaces** — no sight of other processes, own IPC, own
  hostname.
- **cgroup v2 limits** — CPU / memory / pids per component (or per scope), so one
  runaway backend can't starve the box (ARCHITECTURE §7 already wanted this).
- **seccomp profile** — a sane default syscall allow-list; drop the exotic stuff.
- **runtimes plug in as today** (go/node/python/cgi) on this base — **plus
  `wasm`/wazero as a first-class lightweight option**: capability-pure, no rootfs
  at all, the lightest sandbox for components that fit it.

Hot-reload stays cheap by **reusing the scope's sandbox** (ns set + overlay +
cgroup) across backend generations — only the process is re-execed
(`isolation.md` ISO-4). A cold component costs one sandbox build; a save costs a
re-exec.

## Terminals — the same base, owner's power

Terminals get the **same base rootfs** (so Go/Node/Python + `opencode`/
`claude-code` + `bx` are simply *there*, consistent on VM and appliance alike),
but:

- the **full workspace mounted rw** (the owner edits any component), and
- **no egress restriction** — terminals are the owner's plane, deliberately
  **not** sandboxed from the workspace (auth.md non-goal).

They still run in a mount/pid namespace with the base rootfs and cgroup limits —
for a clean, consistent environment, not for security-from-owner. The fat rootfs
is what makes "open a terminal, the agent and toolchains are ready" true by
construction.

## Data & lifecycle

- `/workspace` is a persistent data disk/volume — **the pet**. The host OS image
  and the base rootfs are **cattle**: immutable, A/B-upgradable. Same "container
  is cattle, workspace is pet" spirit as today, one level down.
- Upgrade = new appliance image (buxond) and/or new base-rootfs tag; the
  workspace is untouched and buxond migrates manifests if needed.
- Backup/restore is still just the `/workspace` volume (`plans/deployment.md`).

## Migration / phasing

1. **Base rootfs first (UX win, no isolation yet).** Publish the fat OCI rootfs
   and teach buxond to unpack + mount-ns it for **terminals** — instantly nicer
   builder shells (agents/toolchains everywhere), independent of the boundary.
2. **Tier-3 sandboxing** (`isolation.md`) using the rootfs as the overlay base:
   mount+net ns, egress relay, cgroups, seccomp.
3. **VM appliance**: build + ship `qcow2`/`OVA`/ISO/cloud images; make it the
   recommended production deployment; demote Docker to dev/Tier-1 in the docs.
4. **microVM option** (Firecracker) and `wasm` runtime as follow-ons.

Meanwhile the **Docker image keeps working** for dev and Tier-1 — nothing breaks;
the recommendation moves.

## Decisions (RT-*)

- **RT-1 — Production boundary is a VM/host buxond controls**, not the Docker
  container. buxond becomes the sandbox runtime for its components; Docker is
  demoted to dev / Tier-1.
- **RT-2 — Component userland is a fat, Ubuntu-based OCI rootfs** (Go/Node/Python/
  git + `opencode`/`claude-code` + `bx`), bind-mounted ro as every sandbox's base,
  overlayfs per component. Kept separate from the minimal host OS.
- **RT-3 — Ship a virtual appliance** (qcow2/OVA/ISO/cloud) eventually; immutable
  OS + A/B updates; workspace on a data disk.
- **RT-4 — Terminals share the base rootfs** (nice builder env with agents) but
  stay unsandboxed-from-workspace (owner plane).
- **RT-5 — `wasm`/wazero is a first-class lightweight runtime** alongside the
  rootfs-based runtimes.
- **RT-6 — Two nested boundaries**: VM (hard, outer) + per-component namespaces
  (inner). The appliance OS is small and trusted; the fat rootfs is a swappable
  workload layer.
