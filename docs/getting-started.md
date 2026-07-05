# Getting started

## Run it

xbin runs as a single binary on a Linux host it controls, with `--isolate`
turning on per-component OS sandboxing (see the repo README → Running it for
the host requirements — unprivileged user namespaces, sub-id delegation,
`/dev/fuse`, `/dev/net/tun`):

```sh
xbind --isolate --rootfs /var/lib/xbin/rootfs \
       --workspace ~/xbin-ws --listen 127.0.0.1:8642
# prints the one-time login URL
```

Open the login URL. It sets a cookie and drops you on the workspace root
page. Binding to `127.0.0.1` is deliberate: xbin is remote code execution by
design, so expose it via Tailscale/WireGuard or a TLS reverse proxy, never raw
to the internet.

The base rootfs (`make rootfs`) ships the toolchains (go, node, python, vim,
git, …) that terminals and backends use. For quick local hacking, `make dev`
runs xbind from source, isolated, against `./devws`.

**Host sysctl** (once, on the host): xbind watches every directory in the
workspace; the kernel default inotify budget is too small for big trees.

```sh
sudo sysctl -w fs.inotify.max_user_watches=524288   # persist in /etc/sysctl.d/
```

`bx doctor` (in any workspace terminal) checks this.

## First component

Every frame carries a **7×7 blue square** in its **top-right corner** — the
**edit button**. Click the one on the welcome frame and a floating terminal
window opens (drag it by the title bar, resize from the corner, ctrl+scroll for
font size) with a shell *in that component's directory*.

That terminal is **scoped to its component**: it can write its own directory and
`$HOME`, but the rest of the workspace is **read-only**. So you don't `mkdir` a
new app here — you create components through xbind, which isn't bound by that
scope: the **Tile Manager** on the root page, or `bx new` from a *root* terminal.

In the **Tile Manager**'s *create* tab, name it (e.g. `hello`) and hit
**Create** — it scaffolds a static `apps/hello` (equivalently:
`bx new apps/hello --runtime static`). It appears in the sidebar at once — click
it to open it as a card. Now open **its** terminal (the 7×7 button on that card,
where you *can* write) and make it yours:

```sh
cat > index.html <<'EOF'   # you start in apps/hello — your own, writable dir
<!doctype html>
<html><body style="color-scheme:dark; font-family:sans-serif">
  hello from a directory
</body></html>
EOF
```

Save and the card live-reloads. That's the whole loop: **create → edit → save →
see it**. The layout around everything is itself a component (`shell/`) with the
theme in `/vendor/theme.css` — edit either (from its own card's terminal) and
watch the whole workspace restyle. Drag any card by its title bar to rearrange
it; organise work into named **screens** (the tabs at the top — add with `+`,
double-click to rename); your whole layout is **saved per user** (server-side, so
it follows you across browsers and devices). The `<bx-frame>` pins in
`root/index.html` only seed the *first* screen a brand-new user sees.

## First backend

Create another one, this time with a real backend. The Tile Manager's *create*
tab makes static tiles only, so scaffold a backend from a *root* terminal:

```sh
bx new apps/counter --runtime go --expose
```

This scaffolds `xbin.json`, `index.html`, `backend/main.go`, `go.mod`, and an
`API.md`. Frame it, then edit `backend/main.go` (from the counter card's own
terminal) and save — xbind recompiles and swaps the running backend in about a
second (typically ~200 ms warm). Compile errors appear as an overlay on the frame
and clear on the next good save. Backend logs: `bx logs -f apps/counter`.

Try it from the shell too — your terminal has an owner token in the
environment:

```sh
curl -H "Authorization: Bearer $XBIN_TOKEN" $XBIN_URL/api/apps/counter/hello
```

## What's in a terminal

Terminals are the privileged **editing plane**: a real shell (your `$SHELL`)
with the workspace toolchains on PATH, `git`, `vim`, and:

| Env | Meaning |
|-----|---------|
| `XBIN_WORKSPACE` | workspace root path |
| `XBIN_COMPONENT` | the component this terminal was opened on |
| `XBIN_URL`, `XBIN_TOKEN` | how to call xbind as the owner (`bx` uses these) |
| `HOME` | `<workspace>/home` — dotfiles persist across upgrades |
| `IN_SANDBOX=1` | set when the terminal runs in the rootfs sandbox (isolated mode) |

`home/` is deliberately **not** your host home: it's a contained, persistent
$HOME seeded with a minimal `.zshrc`/`.bashrc` (component-aware prompt,
history, aliases). Bring your own config by copying it in:
`cp ~/.zshrc $XBIN_WORKSPACE/home/` from a host shell — it's gitignored,
so it stays local to this workspace.

A terminal opened on a component is **scoped to it**: that component's dir and
`$HOME` are writable, the rest of the workspace is read-only, and `git commit`
still works because each component is its own repo. A **root terminal** (on the
workspace root) is the owner plane — the whole tree writable — for cross-component
work and creating components. Full model: [isolation.md](/docs/isolation.md).

Sessions survive browser disconnects (reattach happens automatically) but
not xbind restarts — run `tmux` inside if that matters to you.

The base rootfs is read-only, but each terminal stacks a **persistent
per-component overlay** on top (`.xbin/term/<component>/`). So an `apt install`,
an `/etc` tweak, or an extra toolchain **survives across sessions and xbind
restarts** — each component gets its own long-lived dev box. The ⟲ button in the
terminal window resets that layer back to a clean base. (Only one live session
may hold a component's layer at a time; a second concurrent terminal on the same
component gets an ephemeral layer.) This dev layer is the terminal's alone — the
**deployed backend never sees it**; a backend's system deps come from the
component's `setup` ([elements.md](/docs/elements.md)), so install there for
runtime, not just in the shell.

To land a tool in **every** terminal *and* every backend instead, extend the base
image (`docker/rootfs.Dockerfile`) and rebuild it (`make rootfs`). Terminals map a
full uid range, so `sudo`, `apt install`, `useradd`, and running as a non-root
in-container user all work: xbind mounts each sandbox with its own
**fuse-overlayfs** (built from source by `make`, shipped alongside xbind). If
it's ever missing, xbind falls back to kernel overlayfs — then `apt update` still
works but a full install fails with `Invalid cross-device link`.

The title bar has a **network scope** menu. By default a terminal has its own
network namespace with **internet-only** egress — the host's interfaces aren't
visible (`ip addr` shows just `lo` and `bx0`), but the public internet and
xbind (`$XBIN_URL`, so `bx`/`curl`) work. `ping` works too (real reachability);
`traceroute` needs host scope. Switch to **host net** to reach the LAN / services
on the host, or **offline** for no network at all. Switching scope restarts the
terminal (it can't change live).

## Versioning

Git is the only history. **Each component is its own repo**, so from a
component's terminal you commit its own dir directly:

```sh
git add -A && git commit -m "counter app"   # in apps/counter — its own repo
```

The workspace root is *also* a repo (`.xbin/`, `data/`, `home/` ignored) for
workspace-level files and layout — commit that from a **root terminal**, which
has the whole tree writable. Per-component repos are what make components
**installable**: the Tile Manager can import one straight from a git URL, and
`cp -r apps/counter apps/counter2` (from a root terminal) forks an app locally.
Paths are identities; there is no other registry.

## Where to next

- The component contract in full: [elements.md](/docs/elements.md)
- Letting apps call each other safely: [auth.md](/docs/auth.md)
- Shared state (kv, bus, cron, …): [resources.md](/docs/resources.md)
- Sandboxes, terminal scoping, the dev layer: [isolation.md](/docs/isolation.md)
