# 2026-08-03 — container image stores: `plain: true` filesystem resources

A `cap:containers` tile that keeps its image store on a regular (encrypted)
`filesystem` resource fails at `podman run` / image pull with errors like:

```
ApplyLayer … setting up pivot dir: mkdir …/store/…/.pivot_root…: permission denied
```

Fix: declare the store resource **plain**, and move the store into it fresh
(re-pull images — layers are re-fetchable by design):

```jsonc
// scope.json
{ "resources": { "store": { "type": "filesystem", "plain": true } } }
```

## Why encrypted resources can't host a container store

Encrypted resources are gocryptfs FUSE mounts, and the FUSE daemon — an
unprivileged host process running as xbind's uid — is the one physically
doing every read and write. A container runtime's layer store breaks that
model three ways:

- image layers contain **read-only (0555) directories**; the mirror makes
  them 0555 for the *daemon too*, so its own `mkdir .pivot_root` inside them
  is `EACCES` — the error above;
- layer extraction **chowns to arbitrary sub-uids**, which an unprivileged
  process cannot do (`EPERM`);
- files owned by **sub-uids** of the tile can't be read back through a mount
  the daemon serves without `allow_other` tricks the kernel refuses here.

The tile's user-namespace capabilities (`cap:containers`) never apply — the
daemon does its I/O on the host, outside that namespace. This is structural,
not a bug to fix, hence the declared opt-out.

## Semantics of `plain: true`

- Type **`filesystem` only** — on any other type the flag warns and is
  ignored (the resource stays encrypted).
- Provisions as a plaintext kernel directory under `data/resources/…`; same
  env delivery (`XBIN_RES_<NAME>` = the dir), rw bind, usage accounting,
  and backup coverage as before.
- **Never held on vault state**: a component whose only file resources are
  plain spawns while the vault is sealed, and **seal does not stop it** —
  the flag declares the data needs no vault protection. Bytes on disk are
  not encrypted; don't put secrets in a plain resource.
- Flipping a previously-encrypted resource to plain starts with a **fresh
  empty dir**. The old ciphertext is left untouched at
  `data/resources-enc/<scope-key>/<name>` — delete it to reclaim the space
  (`bx doctor` flags the remnant, and lists every plain resource as a
  deliberate opt-out; the admin tile shows a `plain` pill).

## Also in this change

xbind now mounts a **private cgroup2 view** at `/sys/fs/cgroup` inside
`cap:containers` sandboxes (own cgroup namespace, best-effort). libpod stats
that path at container create even with cgroups disabled; tiles that
self-mounted it in their entrypoint as a workaround can drop that step.

See `docs/resources.md` §filesystem and `plans/DECISIONS.md` D43.
