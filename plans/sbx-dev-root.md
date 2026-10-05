# Coding sandboxes that can develop xbin (branch `sbx/dev-root`)

> Status: live — built on `sbx/dev-root` (from master `4a9b5b47`); unit,
> template and UI tests pass here; the end-to-end VM boot on a rebuilt
> rootfs is owed (below). Decision: D182.

A coding sandbox that is a VM guest should be able to develop xbin itself:
`sudo`, rootless podman, FUSE. It couldn't, though root inside a VM guest
is part of the design (D89): the base rootfs installs `sudo`, but every
setuid, setgid and sticky bit was gone. This records what the branch built
to fix that, the choices (D182 has the reasoning), what was tested here and
what is still owed.

## The cause

- `hack/build-rootfs.sh` unpacks `docker export` with an unprivileged
  `tar` (no `-p`): special bits and owners go. `deploy/install.sh`'s unpack
  and its `chown -R` strip them again on every install.
- The VM image keeps whatever modes the tree has (`internal/vm/image.go`,
  `mkfs.erofs --all-root`), so `sudo.ws`, `su`, `passwd`, `mount` were
  0755 in every VM guest and `/var/tmp` wasn't sticky.
- Namespace sandboxes set `NO_NEW_PRIVS` (`internal/sandbox/init_linux.go`):
  sudo can never work there, by design.

## What was built

1. **The image records its special modes** — `docker/rootfs.Dockerfile`'s
   last step writes `/etc/xbin-rootfs-modes` (`<mode> <uid> <gid> <f|d>
   <path>` a line, from `find / -xdev … -perm /7000`). The host tree stays
   without them; `hack/build-rootfs.sh` says why at its `tar`.
2. **The VM guest re-applies them at every boot**
   (`internal/sandbox/vm/guest/modes_linux.go`, called from
   `assembleRoot`): listed files are copied with their modes and owners
   into a tmpfs layer over the image (`lowerdir=/fixup:/lower`), listed
   directories are set in the assembled root while they still show the
   unpacked mode. The VM disk is never written for a file. Bounded,
   symlink-free (`openat2`), a missing list is no change.
3. **Device modes per boot** (`devices_linux.go`): the guest applies
   `/etc/xbin-vm-devices` (`<mode> /dev/<node>`) from the sandbox's own
   root to the boot's devtmpfs — character devices only.
4. **coding-sandbox: an image's `sudo`** — `Image.Sudo`;
   `prepareScript` writes `/etc/sudoers.d/<user>` (`ALL=(ALL:ALL)
   NOPASSWD: ALL`, 0440, naming the user) and `/etc/xbin-vm-devices`
   (`0666 /dev/fuse`, `0666 /dev/net/tun`); `setupHash` includes it (only
   when set: existing builds stay current); `record.Sudo` is fixed at
   creation, a clone takes its source's; an explicit `namespace` mode
   refuses a sudo image, `auto` without VMs says so in hello's `notes`;
   `/ops/state` sandboxes carry `sudo`. The page: the image editor's
   toggle, the image's sudo (and why it gives nothing in namespace mode),
   the sandbox's — web and native (`images.sudo`).
5. **Docs** — the template's API.md (§Images: `sudo` and the `xbin-dev`
   example; Inside; the table; the page; Testing on xbind) and AGENTS.md;
   docs/isolation.md §VM sandboxes; docs/overview/08-sandbox.md;
   docs/changelog.md (2026-10-03); D182.
6. **The live test** — `test/isolated/codingsandbox_test.go`
   `testCSSudo`: in a VM, `sudo -n id -u` is 0 (on a rootfs with the list;
   skipped, saying so, on an older one), `/dev/fuse` and `/dev/net/tun`
   0666 at the first boot and after a restart; in a namespace sandbox no
   grant and hello's note.

## Choices (D182)

- Guest-side re-application over `mkfs.erofs --tar`: the export stream
  exists only where the image is exported; everywhere else the rootfs is a
  directory (bundle, install, preserved old bases).
- Files in a tmpfs layer, not `chmod` in the merged root: no copy-up into
  the VM disk, so a rebase finds the newer base's programs.
- `sudo` per image, not per layout: one manager offers both kinds; the
  grant rides in the build's snapshot.
- Devices by the guest from a file in the sandbox's root, not a root run
  at the manager's start (the runtime reboots VMs the manager doesn't see:
  snapshots, restores) nor a new runtime API field.
- No "run as root" in the manager contract: ACP's `terminal/create` has no
  user field (`sdk/acp/types.go`).

## Tested here

This machine is itself a VM guest without docker, podman or the VM
helpers (and `sudo` fails in it — the bug this fixes).

- `go test -race ./internal/sandbox/vm/... ./internal/vm/...` — pass. The
  guest's new tests: the list's parsing and bounds; planning and staging
  against temporary trees (symbolic links refused, a budget, a changed
  file never left half-copied, a refused chown); the Dockerfile's
  recording step run on a tree; a real overlay over the layer in a user
  and mount namespace booted three times; the device file's parsing and
  refusals, and a mode set on a real character device (a
  pseudo-terminal's). The guest package also builds for arm64 and darwin.
- `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh coding-sandbox` —
  pass (`TestPrepareScriptSudo`, `TestSudo`, the whole suite).
- `node --test hack/coding-sandbox-ui.test.mjs` — pass; the template's
  browser test (`test/web.mjs`, Chromium at
  `PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/ms-playwright`) — pass.
- `go vet -tags integration ./test/isolated/` — compiles `testCSSudo`.
- `go test ./internal/docscheck`, `make fmt-check vet` — pass.
- `make -k check`: fmt-check, vet, js-check, native-check, theme-check,
  shellcheck, pins-offline pass, and `go test ./sdk/... ./relay/...`.
  Fails that are this machine's, not the branch's: js-test's 4 in
  `hack/helpers.test.mjs` (no `zstd`), `large-files` (no `/dev/fd`), and
  in `go test ./...` `internal/tilesbx`'s `TestAdmissionCaps/running` and
  `TestResourceReconcile` — they fail the same on master `4a9b5b47` here
  (the workspace's sandbox memory budget comes from this 8 GiB host);
  every other package passes.
- Each commit was checked on its own tree (`git archive`): the guest
  package and docscheck after the modes commit, the template's tile-check,
  UI tests and docscheck after the sudo commit.
- Not run: the UI harness (`hack/ui-harness`, needs an isolated xbind),
  `make integration`.

## Owed

- **An end-to-end VM boot on a rebuilt rootfs**: `make rootfs` (docker or
  podman), the VM helpers (`make integration-deps`) and KVM, then
  `go test -tags integration ./test/isolated/ -run 'TestCodingSandboxVM/sudo'`
  — sudo as the layout's user, the devices after a restart, `su` 4755 and
  `/var/tmp` 1777 in the guest. Nothing here could boot a VM.
- The rootfs build itself with the new step (its `find` line is tested on
  a tree, not inside a docker build).
- A follow-up worth weighing: file capabilities (`ping`'s `cap_net_raw`)
  are lost the same way; the list carries modes and owners only.

**At land (2026-10-05, master 087b6d2d, the landing session's workstation):**

- Ran:
  - `make test`: the guest's modes and devices tests pass, except
    `TestApplyDevModesPty`. It fails here because this session mounts
    `/dev/pts` read-only (chmod gives EROFS, checked by hand), an
    environment the test doesn't skip. It needs a normal host or CI.
  - `hack/coding-sandbox-ui.test.mjs` (16/16) and the template's web
    tests (4/4), which cover the image editor's sudo switch.
  - `TILE_TEST_FLAGS=-race hack/tile-check.sh coding-sandbox`: passes.
- Not run, still owed: the rootfs build, the VM end-to-end
  (`TestCodingSandboxVM/sudo`) and the UI harness's coding-sandbox pass.
  Docker isn't running on this host, and the session can't mount FUSE,
  so every encrypted resource is held and the coding-sandbox backend
  never starts.

## Questions for the owner

- `/dev/fuse` and `/dev/net/tun` come with an image's `sudo` (one switch:
  "this image is for development"). A separate `devices` setting is easy
  if an operator should get one without the other.
- `hack/build-rootfs.sh` keeps no special bit only while it runs
  unprivileged; run as root, GNU tar keeps them (and setuid-root files
  land on the host). Forcing `--no-same-owner --no-same-permissions`
  there would make that hold always — left as it was, per the plan.
- `sudoWorks` treats every mode but `namespace` as able to run setuid
  programs (a cloud backend's `cloud-vm`, `container`). A backend that
  can't names its mode `namespace` (AGENTS.md says so); a per-mode flag in
  the backend's runtime would be the stricter alternative.
