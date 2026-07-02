# Getting started

## Run it

```sh
mkdir -p ~/buxon-ws
docker run -d --name buxon \
  -v ~/buxon-ws:/workspace \
  -p 127.0.0.1:8642:8642 \
  --restart unless-stopped \
  ghcr.io/magik6k/buxon:latest

docker logs buxon    # prints the one-time login URL
```

Open the login URL. It sets a cookie and drops you on the workspace root
page. The mapping to `127.0.0.1` is deliberate: buxon is remote code
execution by design, so expose it via Tailscale/WireGuard or a TLS reverse
proxy, never raw to the internet (see the deployment notes in the repo).

Without docker (dev / power user): build `buxond` from the repo and run
`buxond --workspace ~/buxon-ws`. The container is the supported path — it
ships the toolchains (go, node, python, vim, git, …) that terminals and
backends use.

**Host sysctl** (once, on the docker host): buxond watches every directory in
the workspace; the kernel default inotify budget is too small for big trees.

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
| `HOME` | `<workspace>/home` — dotfiles persist across container upgrades |

`home/` is deliberately **not** your host home: it's a contained, persistent
$HOME seeded with a minimal `.zshrc`/`.bashrc` (component-aware prompt,
history, aliases). Bring your own config by copying it in:
`cp ~/.zshrc $BUXON_WORKSPACE/home/` from a host shell — it's gitignored,
so it stays local to this workspace.

Sessions survive browser disconnects (reattach happens automatically) but
not buxond restarts — run `tmux` inside if that matters to you.

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
