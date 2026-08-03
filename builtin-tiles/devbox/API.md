# Devbox — API

A **container-host tile**: rootless Podman dev sandboxes, each reachable over
SSH by name. The control API below is used by the tile's own page (its
frontend is admin of itself); SSH traffic does **not** go through this API —
it flows through the tile's exposed `:2222` stream port.

## Setup (one time)

1. **Import** the tile, then **approve `cap:containers`** (admin-only — grants
   panel, or `bx grant apps/devbox cap:containers:writer`). Without it, Podman
   can't set up containers. The `storage` resource is same-scope and
   auto-granted.
2. **Bind a network** for container egress: `bx bind apps/devbox net=host`
   (simplest/robust) or `net=internet` (contained).
3. **Publish the SSH port** to a host TCP port: `bx expose apps/devbox
   ssh=runtime --listen :2222` (admin → ingress → services / expose). Host
   ports <1024 would need `AmbientCapabilities=CAP_NET_BIND_SERVICE`; 2222 is
   fine as-is.
4. On the tile's page, **add your SSH public key** (the proxy is default-deny).

Then: `ssh -p 2222 <container-name>@<xbin-host>` — the container name is the
SSH username; you land in a shell inside it (starting it first if stopped).

## Control API (frontend → backend)

- `GET /state` (reader) — `{podmanVersion, containers:[{name,image,state,status,
  created}], keys:[{type,fingerprint,comment}], sshPort, sshError, error}`.
- `POST /containers` (writer) — `{name, image}`: `podman run -d` a dev box kept
  alive (`sleep infinity`; most images carry `sleep`). `name` doubles as the
  SSH username, so it must be a short dns-ish label.
- `POST /containers/{name}/{start|stop}` (writer) — lifecycle without removal.
- `DELETE /containers/{name}` (writer) — `podman rm -f` (its filesystem is lost).
- `POST /keys` (writer) — `{key}`: add an authorized SSH public key.
- `DELETE /keys/{fingerprint}` (writer) — revoke a key.

## Notes

- **Storage** lives in the `storage` filesystem resource (`--root`), so
  images/containers + the SSH host key + authorized keys persist across
  restarts (encrypted at rest). Storage driver: `overlay` via fuse-overlayfs
  when the sandbox has `/dev/fuse` (build steps write diffs — far faster on
  the encrypted store), else `vfs`; override with `DEVBOX_STORAGE_DRIVER`
  (note: switching drivers keeps the old driver's images on disk under the
  store — re-pull under the new one). `DEVBOX_NETWORK`,
  `DEVBOX_CGROUP_MANAGER`, `DEVBOX_PODMAN` tune the rest.
- **Host prerequisite**: nested rootless containers need a delegated
  sub-uid/gid range for the xbind user (`/etc/subuid`+`/etc/subgid` + the
  `uidmap` package) — the same thing `apt` in terminals needs. Without it you
  fall back to single-uid and multi-user images break.
- Security: the tile is still fully sandboxed (own namespaces, rootless, no
  host reach) — `cap:containers` only relaxes the seccomp floor + keeps its
  userns caps (plans/containers.md). SSH is public-key only, default-deny.
