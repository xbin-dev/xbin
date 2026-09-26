# VM sandboxes — rootless Firecracker microVMs for terminals and backends

> Status: **live** (D89) — VM terminals, VM agent sessions, VM backends, the
> persistent VM disk, the policy and the toggle are built as described below,
> and so are emulated VMs for hosts without KVM (D90, §Emulated VMs);
> snapshot templates, virtio-mem sizing, the FUSE-over-vsock transport and
> the other items under **Not yet** are designed, not built.

## Why

The namespace sandbox (plans/isolation.md) shares the host kernel with every
tile. Its stated residual risk is a user-namespace kernel escape
(plans/containers.md). A microVM puts a second kernel between a tile and
the host. It also gives the workload a real root: docker/podman without
`cap:containers`, kernel knobs, and apt without userns quirks.

## Shape

**The VM is just another `Spec.Entry`.** xbind builds the same sandbox spec
it always builds (binds, masks, D40 views, network scope). `vm.Apply` then
turns it into a VM sandbox:

```
sandbox.Launch(Spec{VM: …})    userns+mntns+pidns+netns = the rootless jail
  root: bare tmpfs + the same binds + /.xbin-vm/{bin/bx, bin/firecracker,
        boot/vmlinux, boot/initrd, img/rootfs.erofs, img/disk.img}
  netns: bx0 TUN (no address) ◄─routed─► vmtap0 TAP (persistent, owned by the jail's root)
  PID 1 = `bx __vm-host` (static) — stdio = the host PTY, or pipes
     ├─ firecracker (child; stdin /dev/null; console into a ring)
     ├─ FUSE server ◄── vsock 564, one stream per mount (exports = the binds; beneath-only walks)
     └─ vsock bridges: PTY/pipes, signals, resize, the backend's sockets
          guest: vmlinux + initramfs (xbin-vmagent = PID 1)
            root = overlay(erofs rootfs image, tmpfs | the ext4 VM disk)
            the binds mounted (FUSE) at their host paths; eth0 10.0.2.15/32
```

Firecracker's jailer needs root, so the namespace sandbox takes its place.
It has everything a VMM needs: its own mount, pid and net namespaces, no
nested namespaces, a capability set of the five file caps, a seccomp
block-list and the mount guard. Firecracker adds its own per-thread filters.
The data a VMM escape reaches is the bind set, the same as a namespace-mode
workload; the kernel attack surface is what shrinks.

## Pieces

- **Assets** (`internal/vm/assets.go`): `firecracker` (pinned upstream
  static release), `vmlinux` (6.18 LTS on Firecracker's CI config plus
  `hack/vmkernel/xbin.config`: erofs), `xbin-vmagent`, a static
  `mkfs.erofs`, and the static `bx`.
  - Resolved the fuse-overlayfs way: `$XBIN_*`, then next to xbind, then
    `PATH`.
  - Dynamically linked shims and agents are refused: they run where there
    is no libc.
- **Guest image:** the unpacked rootfs becomes an lz4 erofs image through
  a confined `mkfs.erofs --all-root` (about 4 s for the 5 GB rootfs).
  - Keyed by the base version, a content identity and the mkfs build.
  - Garbage-collected at boot.
  - Built on first use: the first VM on a workspace waits for it.
- **Initramfs:** a newc cpio holding only the agent as `/init`, built in Go
  and cached by the agent's hash.
- **Networking:** the init makes the netns a router. The egress TUN goes to
  xbind's relay unchanged; the netns has no address of its own; permanent
  neighbours replace ARP on both sides; strict `rp_filter` drops guest
  spoofing. The guest owns 10.0.2.15, so the relay, host-forwards, DialIn
  and DNS work as ever.
  - Refused: `host` networking, provider splices, lan-ingress legs.
- **Files:** Firecracker has no virtio-fs. The guest mounts each bind as a
  FUSE filesystem; the agent pumps `/dev/fuse` over a vsock stream (messages
  framed by their own length field).
  - The server (`internal/sandbox/vm/fusefs`, a go-fuse `RawFileSystem`) runs
    inside the jail, reading whole requests from a SOCK_SEQPACKET socketpair
    as it would from `/dev/fuse`. Its view *is* the bind set: read-only binds
    fail with EROFS from the host kernel, and masks read empty.
  - Only a mount named in the spec can be attached.
  - Each lookup is one `openat2(RESOLVE_BENEATH|NO_SYMLINKS|NO_MAGICLINKS)`
    from the parent's O_PATH fd, without NO_XDEV so nested binds and masks
    are crossed. Nodes are keyed by (mount id, inode), so a read-only and a
    writable bind of one inode never alias. Nested exports keep their own
    read-only flag.
  - **Caching is the design.** Entries, attributes, negative lookups and
    symlinks live for an hour. Opens are zero-message (OPEN/OPENDIR/CREATE/
    FLUSH answer ENOSYS), so page cache and directory listings survive
    reopening, and READDIRPLUS brings attributes with listings.
  - inotify on every directory the guest looked into turns host-side
    changes into FUSE invalidations; the guest's own changes are skipped,
    and a queue overflow invalidates everything.
  - Locks are host OFD locks, one open file description per guest lock
    owner.
  - Host fsnotify still fires on guest writes. Guest watchers don't see
    host edits.
  - No writeback cache: with it the kernel drops block counts on every close,
    turning each later `stat` into a round trip.
  - 9P was tried first and dropped: the kernel's `trans=fd` client is
    uncached-coherent at ~250 µs per operation (`git status` on 12k files:
    5 s against 27 ms).

- **Terminals** (`term/vm.go`): `?vm=1` on `/ws/term`, and `vm` on agent
  create/restart.
  - The shim puts the host PTY slave in raw mode and bridges it to a guest
    PTY, with SIGWINCH going to resize.
  - Closing a session sends SIGHUP: the guest syncs its disk, then the VM
    is killed.
- **The VM disk:** `.xbin/term/<key>/vm/disk.img`, sparse, sized by the
  policy (`diskGiB`, default 20).
  - It lives in the tile's existing layer, so it shares the layer's lock,
    base pin and Reset.
  - The guest formats it only when it is blank, grows it offline when the
    host grew it, and mounts it with `commit=1` and fast writeback.
  - Backups skip `vm/`.
  - Root filesystem changes survive the session. The VM disk and the
    namespace upper are separate: packages installed in one mode aren't
    seen in the other.
- **Backends** (`runner/vm.go`): `"vm": true | {memory, vcpus}` in
  `xbin.json`.
  - The run dir is a guest-local tmpfs, so sockets are the guest's own.
  - The agent serves `XBIN_GATEWAY` in the guest (bridged to vsock 1025,
    which the shim links to `gateway.sock`; no other port exists) before the
    backend starts.
  - The agent reports "listening" once `XBIN_SOCKET` answers; only then
    does the shim serve the host socket, so the proxy and health check are
    untouched.
  - Health timeout 60 s. Two generations share the tile's cgroup leaf,
    capped for two VMs.
  - VM tiles with file-backed resources stop before the next generation:
    two guests' caches aren't coherent (a sqlite WAL).
  - `setup` + `vm` is refused.
- **Policy** (`internal/vm/policy.go`, `.xbin/vm/policy.json`):
  - Off by default.
  - `terminals` and `backends` switches.
  - Per-VM `memMiB` (default 2048) and `vcpus` (default 2).
  - `maxVMs` and a memory budget, which admission (`Reserve`) enforces.
  - Each VM gets a cgroup leaf capped at guest memory plus overhead.
  - `GET /vm` (anyone) and `PUT /vm/policy` (admin).
- **Balloon:** free-page reporting on, so freed guest memory goes back to
  the host.

## Measured (this dev box)

| | VM | namespace |
|---|---|---|
| Firecracker cold boot to the agent | ~130 ms | — |
| terminal: WS open → prompt (warm image) | ~180 ms | ~65 ms |
| backend first request (boot + node + health) | ~370 ms | — |
| rootfs → erofs image, once per base | ~4 s | — |
| vsock round trip (Firecracker) / one FUSE request | ~90 µs / ~133 µs | — |
| `git status`, 12k-file repo | 72 ms | 27 ms |
| `find`, warm | 27 ms | 27 ms |
| `grep -r` over 244 MB: cold / next / warm | 2.3 s / 1.7 s / 161 ms | 174 ms |
| create 2000 small files | 1.3 s | 39 ms |

First touches are bound by Firecracker's in-VMM vsock (~90 µs round trip);
everything the guest has seen is local after that. The second `grep`
still pays one GETATTR per file: the kernel marks atime stale after
fetching a file's pages.

## Emulated VMs (no KVM) — D90

Small cloud VMs rarely expose nested virtualization, and Firecracker is
KVM-only. Where `/dev/kvm` isn't usable, the shim runs the *same* guest under
QEMU's software emulation (TCG) instead:

- **QEMU:** static, x86_64-softmmu, TCG only (`--disable-kvm`),
  `--without-default-devices` plus exactly the microvm board, virtio-mmio
  blk/net/balloon and vhost-user-vsock (hack/build-qemu.sh, ~10 MB). Its two
  boot blobs (qboot, the PVH option ROM) come from the same sha256-pinned
  tarball and are bound into the jail at `/.xbin-vm/fw`. `-sandbox on` adds
  QEMU's own seccomp filter. Memory is a shared memfd; the balloon reports
  free pages; `tb-size=256` caps the translation cache (the cgroup leaf
  allows 512 MiB of overhead instead of 192).
- **vsock:** QEMU has no userspace vsock device, and host vhost-vsock needs
  root and global CIDs. rust-vmm's vhost-device-vsock (static musl build)
  serves the guest's vsock over Firecracker's hybrid protocol (`CONNECT
  <port>` in, `<uds>_<port>` out) at the same `v.sock`, so nothing past the
  VMM's start differs: the agent, the file server, the gateway and the
  listen bridge are untouched.
- **Board quirks:** `pic=off` (the kernel never programs the legacy PIC, so
  its first timer interrupt would arrive as vector 0, #DE); `reboot=t`
  (no keyboard controller to reset through; `-no-reboot` turns the triple
  fault into QEMU's exit); `tsc_early_khz=<host TSC rate>`, measured by the
  shim over 50 ms — under TCG the guest TSC *is* the host's, and a guest
  that has to calibrate it against the emulated PIT fails now and then
  (jitter) and then waits forever for a PIT tick that never arrives (2 of
  96 parallel boots hung without it, 0 of 336 with it); the initrd is
  padded to whole pages — microvm loads it flush against the top of RAM,
  where the firmware keeps the ACPI tables, and a tail past 0xd00 of that
  page corrupted the DSDT (the guest then checksums "gigabytes" forever).
- **Ready call:** a host vsock connection made before the guest's vsock
  driver is up can wedge vhost-device-vsock for good, so the agent calls
  the shim on `ReadyPort` (1026) once it listens and the shim connects only
  after that — for Firecracker as well (no more 5 ms polling).
- **Choice:** automatic — KVM usable ⇒ Firecracker; else the emulation
  pieces present ⇒ emulated; else unavailable with both reasons.
  `XBIN_VM_ACCEL=kvm|emulate` forces one. `Status.emulated` + `note` reach
  `GET /vm`, `/ws/term/env` and the toggle's tooltip.
- **Timeouts** stretch 6× in the shim (boot, stream dials) and 3× for a
  backend's health check.
- **Cost** (4-vCPU KVM guest standing in for a VPS): boot to prompt ~1.8 s;
  a bash loop 18× slower than a namespace terminal, 300 fork+exec 6×. The
  isolation argument is unchanged (a separate guest kernel); the VMM is a
  bigger program than Firecracker, inside the same jail.

## Not yet

- **Snapshot templates:** restore a booted guest, `MAP_PRIVATE`
  copy-on-write, for about 30 ms starts.
  - Deferred because a cold boot already costs about 150 ms.
  - The agent already holds nothing tile-specific before `config`.
  - The disk is attached by path and flushed with BLKFLSBUF.
  - The control protocol survives a vsock reset.
- **virtio-mem:** plug memory after a template restore; sizes are per boot
  today.
- **A faster channel under FUSE:** vsock is the floor today. A shared-memory
  ring (a pmem region both sides map) with adaptive polling, or a VMM with
  vhost-user / virtio-fs, would cut the per-request cost roughly tenfold.
- A nested mount namespace for Firecracker alone.
- More terminals per VM: the protocol has session ids from day one.
- VM env layers, so `setup` works with `vm`.
- Disk resources: an image file inside a gocryptfs resource, attached as
  virtio-blk.
- arm64, IPv6, idle shrink and hibernate, guest stats in `/runtime`.
- Lima on macOS, which needs nested virtualization.

## Sub-sandboxes

A VM backend can't nest VMs (no nested KVM). Backend-managed sub-sandboxes
will be host-side siblings that xbind starts through the same `Launch` +
`vm.Apply` path (D82: tiles never nest). Their memory is carved from the
parent's budget, and exec into them uses the session-id'd protocol.
