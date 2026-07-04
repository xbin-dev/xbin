# Getting started

## Run it

buxon runs as a single binary on a Linux host it controls, with `--isolate`
turning on per-component OS sandboxing (see the repo README → Running it for
the host requirements — unprivileged user namespaces, sub-id delegation,
`/dev/fuse`, `/dev/net/tun`):

```sh
buxond --isolate --rootfs /var/lib/buxon/rootfs \
       --workspace ~/buxon-ws --listen 127.0.0.1:8642
# prints the one-time login URL
```

Open the login URL. It sets a cookie and drops you on the workspace root
page. Binding to `127.0.0.1` is deliberate: buxon is remote code execution by
design, so expose it via Tailscale/WireGuard or a TLS reverse proxy, never raw
to the internet.

The base rootfs (`make rootfs`) ships the toolchains (go, node, python, vim,
git, …) that terminals and backends use. For quick local hacking, `make dev`
runs buxond from source, isolated, against `./devws`.

**Host sysctl** (once, on the host): buxond watches every directory in the
workspace; the kernel default inotify budget is too small for big trees.

```sh
sudo sysctl -w fs.inotify.max_user_watches=524288   # persist in /etc/sysctl.d/
```

`bx doctor` (in any workspace terminal) checks this.

## First component

Click the small square in the top-right corner of the welcome frame — that's
the **edit button**; every frame has one. It opens a floating terminal
window (drag it by the title bar, resize from the corner, ctrl+scroll for
font size) with a shell *in that component's directory*. Now:

```sh
cd $BUXON_WORKSPACE            # any terminal starts in its component; this goes to the root
mkdir -p apps/hello
cat > apps/hello/index.html <<'EOF'
<!doctype html>
<html><body style="color-scheme:dark; font-family:sans-serif">
  hello from a directory
</body></html>
EOF
```

It shows up in the shell's sidebar the moment the directory exists — click
it to open it as a card. To pin it permanently, edit `root/index.html` and
add inside the `<bx-shell>`:

```html
<bx-frame src="apps/hello"></bx-frame>
```

The root page live-reloads as you save. So does `apps/hello` when you edit
it. That's the whole loop: **mkdir → edit → save → see it**. The layout
around everything is itself a component (`shell/`) with the theme in
`/vendor/theme.css` — edit either and watch the whole workspace restyle.
Drag any card by its title bar to rearrange it into the column layout.
Organise work into named **screens** (the tabs at the top — add with `+`,
double-click to rename); your whole layout is **saved per user** (server-side,
so it follows you across browsers and devices).

## First backend

```sh
bx new apps/counter --runtime go --expose
```

This scaffolds `buxon.json`, `index.html`, `backend/main.go`, `go.mod`, and
an `API.md`. Frame it, then edit `backend/main.go` and save — buxond
recompiles and swaps the running backend in about a second (typically
~200 ms warm). Compile errors appear as an overlay on the frame and clear on
the next good save. Backend logs: `bx logs -f apps/counter`.

Try it from the shell too — your terminal has an owner token in the
environment:

```sh
curl -H "Authorization: Bearer $BUXON_TOKEN" $BUXON_URL/api/apps/counter/hello
```

## What's in a terminal

Terminals are the privileged **editing plane**: a real shell (your `$SHELL`)
with the workspace toolchains on PATH, `git`, `vim`, and:

| Env | Meaning |
|-----|---------|
| `BUXON_WORKSPACE` | workspace root path |
| `BUXON_COMPONENT` | the component this terminal was opened on |
| `BUXON_URL`, `BUXON_TOKEN` | how to call buxond as the owner (`bx` uses these) |
| `HOME` | `<workspace>/home` — dotfiles persist across upgrades |

`home/` is deliberately **not** your host home: it's a contained, persistent
$HOME seeded with a minimal `.zshrc`/`.bashrc` (component-aware prompt,
history, aliases). Bring your own config by copying it in:
`cp ~/.zshrc $BUXON_WORKSPACE/home/` from a host shell — it's gitignored,
so it stays local to this workspace.

Sessions survive browser disconnects (reattach happens automatically) but
not buxond restarts — run `tmux` inside if that matters to you.

The rootfs is mounted as an **ephemeral overlay**, so anything you `apt install`
in a terminal lasts only for that session. To add a tool permanently, extend the
base image (`docker/rootfs.Dockerfile`) and rebuild it (`make rootfs`) — that
lands it in every terminal and sandbox. Terminals map a full uid range, so
`sudo`, `apt install`, `useradd`, and running as a non-root in-container user all
work: buxond mounts each sandbox with its own **fuse-overlayfs** (built from
source by `make`, shipped alongside buxond). If it's ever missing, buxond falls
back to kernel overlayfs — then `apt update` still works but a full install fails
with `Invalid cross-device link`.

The title bar has a **network scope** menu. By default a terminal has its own
network namespace with **internet-only** egress — the host's interfaces aren't
visible (`ip addr` shows just `lo` and `bx0`), but the public internet and
buxond (`$BUXON_URL`, so `bx`/`curl`) work. `ping` works too (real reachability);
`traceroute` needs host scope. Switch to **host net** to reach the LAN / services
on the host, or **offline** for no network at all. Switching scope restarts the
terminal (it can't change live).

## Versioning

The workspace is a git repo (`buxond init` set it up; `.buxon/`, `data/`,
`home/` are ignored). Commit from any terminal:

```sh
cd $BUXON_WORKSPACE && git add -A && git commit -m "counter app"
```

`cp -r apps/counter apps/counter2` is a legitimate way to fork an app —
paths are identities, git is history, there is no other registry.

## Where to next

- The component contract in full: [elements.md](/docs/elements.md)
- Letting apps call each other safely: [auth.md](/docs/auth.md)
- Shared state (kv, bus, cron, …): [resources.md](/docs/resources.md)
