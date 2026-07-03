# Per-component isolation — implementation research & plan

Implementation companion to `plans/isolation.md` (mechanics) and
`plans/runtime.md` (deployment). This is the concrete build: package layout,
the rootless-namespace launch pattern, rootfs assembly, and the egress relay.

Production target (RT-1): buxond runs **as a normal user on a dedicated VM** and
manages **per-component namespaces** rootlessly (user namespaces). Verified on
the dev host: unprivileged `CLONE_NEWUSER|NEWNS|NEWPID|NEWNET|NEWIPC|NEWUTS`,
rootless `overlayfs`, and rootless **TUN** creation inside the netns all work.

## 0. What "rootless" forces

No host root ⇒ no veth into the host netns, no host NAT/masquerade, no
`newuidmap` unless suid helpers exist. Consequences that shape the design:

- **Networking must be userspace.** A component netns has *no* route to the host;
  egress happens through a **TUN device + a userspace TCP/IP stack** buxond runs
  (the slirp4netns/gVisor model), *not* veth+iptables. This is also exactly the
  seam where per-destination `net:*` egress policy lives.
- **UID mapping is single-range by default.** Map container `uid 0 → buxond's
  uid`, and (optionally, if `/etc/subuid` + `newuidmap` exist) a sub-uid range so
  the component runs as a distinct in-ns uid. Start single-uid; add subuid later.
- **cgroups need delegation.** Rootless cgroup v2 limits require a delegated
  subtree (systemd user session `Delegate=yes`, or `--isolate` degrades limits to
  best-effort/off). Not on the critical path for correctness.

## 1. Package layout

```
internal/sandbox/
  sandbox.go     Spec + Launch (parent side): build exec.Cmd with namespaces
  init_linux.go  the re-exec entrypoint: mount assembly + pivot_root + exec
  mount_linux.go overlay + bind helpers
  net_linux.go   netns tun creation (parent setns), fd hand-off
  relay/         userspace egress relay (TUN ⇄ policy ⇄ host dial)   [phase 2]
  policy.go      EgressPolicy: net:internet / net:<cidr|host>[:port] matching
  sandbox_stub.go  //go:build !linux — Launch returns ErrUnsupported
```

`cmd/buxond` gains a hidden subcommand `__sandbox-init` dispatched **before flag
parsing** (like runc's `init`): it is the re-exec target that runs inside the new
namespaces and never returns (it `exec`s the backend).

## 2. The launch pattern (parent → re-exec init → backend)

Standard "reexec init" (runc/nsenter style), all in Go with `os/exec` +
`golang.org/x/sys/unix`:

1. **Parent (buxond `runner`)** builds `exec.Cmd`:
   - `cmd.Path = /proc/self/exe`, `cmd.Args = ["buxond", "__sandbox-init"]`.
   - `SysProcAttr.Cloneflags = CLONE_NEWUSER|NEWNS|NEWPID|NEWNET|NEWIPC|NEWUTS`.
   - `UidMappings/GidMappings` (single: `{ContainerID:0, HostID:<uid>, Size:1}`;
     the process re-parents as PID 1 of the new pid ns).
   - `GidMappingsEnableSetgroups = false` (rootless: `setgroups=deny` is written
     for us by the runtime when no subgid helper).
   - The **Spec** (rootfs path, binds, env, argv, netns/egress mode, cgroup path)
     is passed to init over an **extra pipe fd** (`cmd.ExtraFiles`) as JSON — keeps
     it off argv/env where a peek could read it.
2. **Init (in-namespace, container-root)**:
   - `unix.Mount("", "/", "", MS_REC|MS_PRIVATE, "")` — detach mount propagation.
   - Assemble the new root (overlay + binds, §3) under a scratch dir.
   - `mount --move`/`pivot_root` into it; `umount` the old root; `chdir("/")`.
   - Mount fresh `/proc` (works because we have a new pid ns), minimal `/dev`,
     `/tmp` tmpfs, `/sys` (ro, optional).
   - Bring `lo` up in the netns (`ip link set lo up` via netlink).
   - Drop to the target in-ns uid/gid, clear ambient caps, apply the **seccomp**
     profile, set `no_new_privs`.
   - `unix.Exec(entry, argv, env)` — become the backend. PID 1 of the ns reaps
     nothing extra since the backend is the only process (a tiny init-reaper can
     be added if backends fork).
3. **Parent** keeps the child's pid; if `net:*` egress is granted, it **setns**es
   into the child's netns to create the TUN and starts the relay (§4). It also
   places the child into its cgroup (§5).

Failure isolation: any init step failure writes a one-line reason to fd 2 (the
component log) and exits non-zero → surfaces as a build/boot error like today.

## 3. Rootfs assembly (mount namespace)

One **shared, read-only base rootfs** (RT-2), an unpacked OCI image dir pointed at
by `--rootfs` (default `/opt/buxon/rootfs`, or `.buxon/rootfs` in dev). Per
component we overlay:

```
lowerdir = <rootfs>            (ro, shared)   [+ granted deps dirs, ro]
upperdir = <tmpfs or .buxon/overlay/<comp>/up>   (per-component writable)
workdir  = <.../work>
merged   = the new root
```

Then **bind** into `merged`:

- the component's **source dir** → `/component` (ro; runtime can't edit source).
- granted `deps/<x>` → visible under the component's own `deps/` (ro).
- granted **resource files** (same-scope sqlite/blob) → their expected paths (rw).
- the **gateway unix socket** → `BUXON_GATEWAY` path (rw) — the one door out.
- the component's **listen socket dir** (rw) so buxond can dial in.
- `/dev` (bind host `/dev/null,zero,full,random,urandom,tty` or a fresh minimal
  devtmpfs-lite), `/proc` (fresh), `/tmp` (tmpfs), `/etc/resolv.conf` (points at
  the relay's DNS, §4).

Everything not bound is **absent** — sibling source, `home/`, `data/vault/`,
`.git` are not reachable because they were never mounted. `WORKDIR=/component`,
`BUXON_COMPONENT` unchanged. Env `BUXON_SOCKET`/`BUXON_GATEWAY` point at the
in-sandbox bind paths.

**Customizing the rootfs (RT-2):** users bring their own OCI image —
`buxond --rootfs <dir>` (unpacked), or a future `bx rootfs pull <ref>` that
unpacks an OCI ref via a vendored puller. Because we only ever *overlay* on top,
there is **no `-slim` vs `-fat` split to maintain** (D1 revisited): the base is
whatever image the operator chooses; buxon mounts it read-only and layers the
component on top. A component needing extra tooling ships a Dockerfile that
`FROM`s the base — normal OCI customization.

## 4. Networking & the egress relay

**Default (no `net:*` grant): empty netns.** Only `lo` (up) + the bind-mounted
gateway unix socket. No IP route ⇒ **all IP egress fails closed**; buxond is still
reachable (unix). This is implemented and testable now, and is the correct floor.

**Granted egress: TUN + userspace relay.** When a component has `net:*` grants,
buxond:

1. `setns` into the child's netns, creates a **TUN** `bx0`, assigns it an address
   (e.g. `10.0.2.15/24`), adds a default route via a virtual gateway
   (`10.0.2.2`), sets `/etc/resolv.conf` → `10.0.2.3`. Keeps the tun **fd** and
   `setns` back. (Rootless TUN-in-netns verified working.)
2. Runs a **userspace TCP/IP stack** on that fd (gVisor `pkg/tcpip` — the same
   engine behind slirp4netns/gvisor-tap-vsock/tailscale). Register
   `tcp.NewForwarder` + `udp.NewForwarder`: **every new flow** is handed to a
   policy check `EgressPolicy.Allow(dstIP, dstPort, proto)`:
   - allow ⇒ `net.Dial` the real destination **from the host netns** and splice
     the gonet conn ⇄ host conn bidirectionally.
   - deny ⇒ send RST / drop.
   A built-in **DNS responder** at `10.0.2.3:53` resolves names (host resolver),
   then policy is applied to the resolved IP (and hostname rules matched at
   resolve time). ICMP echo optional.

**`EgressPolicy`** (`policy.go`, no gVisor dep — pure matching, usable now):

```go
type Rule struct { Net netip.Prefix; Host string; Port int /*0=any*/; Internet bool }
type EgressPolicy struct{ rules []Rule }
func Parse(target string) (Rule, error)     // "net:internet:443" | "net:10.0.0.0/24:5432" | "net:db.internal"
func (p EgressPolicy) Allow(ip netip.Addr, port int) bool
```

- `net:internet[:port]` ⇒ `Internet:true`; `Allow` true iff `ip` is **global
  unicast and not private** (`!ip.IsPrivate() && !ip.IsLoopback() && !LinkLocal
  && !ULA`) and port matches. **Never** matches RFC1918/LAN.
- `net:<cidr>[:port]` / `net:<host>[:port]` ⇒ prefix/host match (+ optional port).
- Grants are ordinary `uses` targets at role `egress`, parsed by the broker into
  an `EgressPolicy` handed to the sandbox at spawn (like `EnvFor`). Owner-approved,
  never self-approved.

**Why gVisor over slirp4netns:** slirp4netns is allow-all NAT (no per-CIDR
policy) and an external C binary; the gVisor forwarder gives exact per-flow
policy in-process and is pure Go. Cost: a big module — pulled only when the relay
is built (phase 2), and the relay can also run as a **separate helper process**
to keep it out of the core binary's import graph if we prefer.

## 5. cgroups, seccomp, limits

- **cgroup v2** (best-effort): if a delegated subtree exists
  (`/sys/fs/cgroup/.../buxon.slice/`), create `comp-<key>`, write
  `memory.max`/`pids.max`/`cpu.max` from scope config, and put the child's pid in
  `cgroup.procs`. Absent delegation ⇒ warn, skip.
- **seccomp**: a default allow-list (deny `keyctl`, `add_key`, `ptrace` of others,
  `bpf`, mount/pivot after init, etc.) via `libseccomp`-free BPF (hand-built
  `prog` with `x/sys/unix` `SECCOMP_SET_MODE_FILTER`). Phase 2; `no_new_privs`
  first.

## 6. Runner integration

- New flags: `--isolate[=tier2|tier3]` and `--rootfs <dir>`. `--dev`/`--no-auth`
  force Tier 0 (no sandbox). Off by default → zero behaviour change.
- `runner.Runner` gains `Sandbox func(c) *sandbox.Spec` (nil ⇒ legacy spawn),
  mirroring the existing `SpawnUser`/`EnvForComponent` hooks. When set, `start()`
  builds the backend command via `sandbox.Launch(spec)` instead of a bare
  `exec.Command`, keeping the same log/wait/token wiring.
- Resource paths (`EnvFor` sqlite file path) must be **inside** the sandbox →
  the broker's resource dir is added to the spec's rw binds. Cross-scope stays
  brokered over the gateway (unchanged).
- Build (`go build`, `npm i`) still runs **outside** the sandbox (editing/build
  plane) — unaffected by runtime egress policy.

## 7. Phasing (implementation order)

1. **`internal/sandbox` foundation** — reexec init, userns+mnt+pid+ipc+uts+**empty
   netns**, overlay rootfs + binds + pivot_root + exec; `EgressPolicy` parsing.
   **DONE** — tested: a probe sees only the assembled tree (host `/home`,
   `/etc/passwd` absent), the component ro bind + rw resource bind behave, the
   netns has only `lo`, and a bind-mounted unix socket is reachable.
2. **Runner wiring** behind `--isolate`/`--rootfs` (`runner.Sandbox`/`sandboxCmd`,
   buxond `__sandbox-init` dispatch). **DONE** — tested: a real HTTP backend
   serves on its unix socket inside the sandbox and is reachable from the host via
   the bound run dir; default path unchanged (isolation off by default). Live
   buxond-with-`--isolate` needs a real OCI rootfs (phase 4), since Go backends
   build against glibc by default — the fat rootfs provides it.
3. **Egress relay** — TUN + gVisor forwarder + DNS + `net:*` policy; broker maps
   `net:*` grants → `EgressPolicy`. (Next; big dep. `EgressPolicy` already lands.)
4. **OCI base rootfs** — `hack/build-rootfs.sh` + `docker/rootfs.Dockerfile`
   (Ubuntu + go/node/python/git + `bx` + agent CLIs) → unpacked dir for
   `--rootfs`; isolated Go backends are built **static** (`CGO_ENABLED=0`) so
   they run on any base. **DONE / validated live**: `buxond --isolate --rootfs
   <ubuntu>` builds, sandboxes, and serves the counter-go example end-to-end; the
   backend runs in its own user+net+mount ns (host `/home` hidden, netns lo-only).
   Still to do here: `bx rootfs pull` (unpack an OCI ref without docker), cgroups
   + seccomp + subuid, and the hardened appliance image (`plans/runtime.md`).
5. **wasm/wazero** runtime (tabled; architecture keeps the runtime pluggable so it
   slots in beside go/node/python as a no-rootfs sandbox).

## 8. Testing

- Unit: `EgressPolicy` matching (internet excludes RFC1918; cidr/host/port).
- Integration (Linux, userns available): launch a probe in a sandbox and assert
  (a) `/` is the overlay (host paths like `/home` absent), (b) the component dir
  is present ro, (c) a rw resource bind is writable, (d) the netns has only `lo`
  and no default route, (e) a bind-mounted unix socket is reachable. Gate the test
  on userns availability (skip in CI runners that forbid it).
- Keep the existing non-isolated path as the default so all current tests pass
  unchanged.
