# NVIDIA GPU access via scope grants (multi-GPU aware)

Companion to `plans/isolation.md` (sandbox mechanics) and `plans/component-env.md`
(the env layer that pairs with this to bring CUDA userland). Lets a component or
terminal be granted specific GPUs through the same owner-approved grant model as
`net:*`, with real per-GPU isolation.

## Feasibility — proven (PoC)

xbind runs rootless, and NVIDIA device nodes are world-accessible
(`crw-rw-rw-`), so the xbind user can drive the GPU with no root and no NVIDIA
Container Toolkit. A PoC bound the device nodes + host driver libs + `nvidia-smi`
into a normal xbin sandbox and ran it: it correctly reported the host GPU (RTX
5070 Ti, driver 595.71.05, CUDA 13.2). Omitting `/dev/nvidia0` → "No devices
were found" — i.e. **device-node presence is the isolation boundary**.

## Grant vocabulary — `gpu:*`

Requested in a component's `uses`, owner-approved exactly like `net:*`
(`plans/auth.md`; an element can never self-grant):

- **`gpu:all`** — every GPU on the host.
- **`gpu:0`, `gpu:1`** — by index.
- **`gpu:GPU-7284f97b…`** — by **UUID** (a prefix is enough): stable across
  reboots / PCI reordering, the robust form.

An unapproved `gpu:*` use yields nothing → the sandbox sees no GPU. Grants that
name a device the host doesn't have resolve to nothing (logged), like a dangling
`net:<host>`.

## Isolation model — selective device binding

CUDA enumerates GPUs from the `/dev/nvidiaN` nodes present in its mount namespace.
xbind binds **only the granted** per-GPU nodes, so a component granted `gpu:0`
*physically* cannot see `gpu:1` — no cgroup device filter required (the PoC
confirmed an absent node = invisible GPU). Multi-GPU isolation is just "bind the
granted nodes."

## Mechanics (what xbind binds per grant)

- **Control nodes** (always, when any GPU is granted): `/dev/nvidiactl`,
  `/dev/nvidia-uvm`, `/dev/nvidia-uvm-tools`, `/dev/nvidia-modeset`.
- **Per-granted-GPU**: `/dev/nvidia<index>`.
- **Driver userspace**: the host's `libcuda.so.1` + `libnvidia-*.so.1` (matched
  to the running kernel driver) and `nvidia-smi`, bound into `/opt/nv` with
  `LD_LIBRARY_PATH=/opt/nv`.
- **Env**: `NVIDIA_VISIBLE_DEVICES` / `CUDA_VISIBLE_DEVICES` (granted
  indices/UUIDs), `NVIDIA_DRIVER_CAPABILITIES=compute,utility`.

All of this is computed by xbind (host-specific) and merged into the sandbox
`Binds`/env — the sandbox `init` stays GPU-agnostic (no new device logic there).

### Driver libs: curated list vs CDI (GPU-2)

- **Curated list (start here)** — glob `libcuda.so.1` + `libnvidia-*.so.1` from
  the host lib dirs. Zero extra host deps (works on the PoC box today); we own
  the list; may miss exotic libs (Vulkan/EGL, some JIT).
- **CDI (preferred when present)** — if `nvidia-ctk` produced a CDI spec
  (`/etc/cdi`, `/var/run/cdi`), parse it and apply its device nodes + mounts.
  The standard, most complete, future-proof path. Optional dependency.

Start curated; prefer CDI automatically when a spec is found.

## Multi-GPU awareness

xbind builds a **GPU inventory** at startup via `nvidia-smi --query-gpu=index,
uuid,name --format=csv` (index → UUID → name → `/dev/nvidiaN`). Used to: resolve
`gpu:all`/`gpu:<uuid>` to concrete nodes, validate grants, and populate the grant
UI / terminal picker. Exposed read-only at `GET /api/xbin/gpus` (admin).

## Terminals

A terminal is a per-component dev sandbox (`plans/component-env.md`); it can take
a GPU too, chosen per session via `?gpu=` (`none` default | `all` | `<index>`)
and a picker in the terminal window (like the network-scope menu), so an agent
can `nvidia-smi` / train interactively. Owner plane, so no grant needed — it's
the owner's box.

## CUDA userland pairing

Only the **driver** lib (`libcuda`) must come from the host. The **CUDA runtime**
(libcudart, cuDNN, framework wheels) rides in the component's `setup` env layer —
so "GPU + PyTorch" is `"uses":[{"target":"gpu:0"}]` + `"setup":"pip install torch"`.

## Requirements & limits

- Host NVIDIA driver installed; `/dev/nvidia*` readable by the xbind user (0666
  default). xbind logs the discovered GPU count at startup.
- **MIG** is out of scope initially: `/dev/nvidia-caps/nvidia-cap1` is root-only
  (`0400`), so MIG instances aren't reachable rootless without extra privilege.
- Only NVIDIA to start (AMD ROCm / Intel are analogous `/dev/dri` + libs, later).
- Optional hardening: a cgroup v2 device filter as defense-in-depth (the mount-ns
  device absence is already the hard boundary).

## Decisions (GPU-*)

- **GPU-1 — `gpu:*` grants** (`all` / index / UUID), owner-approved like `net:*`;
  isolation by selective `/dev/nvidiaN` binding.
- **GPU-2 — Driver libs: curated list now, CDI when a spec is present.**
- **GPU-3 — xbind enumerates GPUs** (`nvidia-smi -L`), exposes `GET /api/xbin/gpus`.
- **GPU-4 — Terminals pick a GPU per session** (`?gpu=`, owner plane, no grant).
- **GPU-5 — MIG / non-NVIDIA are later**; NVIDIA full-GPU first.

## Touchpoints

- `internal/gpu` — inventory, grant resolution, bind/env computation.
- `internal/broker` — `GPUFor(c)` (granted `gpu:*` → devices); `gpu:` added to
  the spawn-restart grant classes.
- `internal/runner` — `Runner.GPU` hook; merge binds/env in `sandboxCmd`.
- `internal/term` — `?gpu=` → GPU binds in the terminal sandbox.
- `internal/server` — `GET /api/xbin/gpus`.
- `web/` — GPU picker in the terminal window.
- `docs/` + `AGENTS.md` + README — the `gpu:*` vocab, terminal picker, host reqs.
