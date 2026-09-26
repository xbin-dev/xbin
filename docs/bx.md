# bx — the workspace CLI

`bx` is on PATH in every xbin terminal. It talks to xbind with the
session's credentials (`XBIN_URL` + `XBIN_TOKEN` — in a terminal, a token
scoped to that terminal's tile; on the host, the owner token) and does local
scaffolding. Anything bx does, you can also do with curl
([protocol.md](/docs/protocol.md)) or plain file edits — bx is convenience,
not magic.

```
bx ls                                  list components (runtime, exposed roles, manifest errors)
bx status [<component>] [--all]         backend states (building/healthy/failed, generations);
                                       --all = workspace-wide, else this terminal's tile
bx new <path> [--runtime R] [--expose] [--title "Pretty Name"] [--owner user:U|org:O]
                                       scaffold a component you'll own, at any
                                       free path outside tiles/ and others'
                                       scopes (org-owned needs the org's
                                       Create knob; D25, D82)
bx tile ls | import <name> [as <path>] list/install builtin tiles
bx template ls | new <source> [as <path>] | updates
                                       list/instantiate template components (blueprints)
bx builtin updates | update <id> [--replace|--merge|--pr]
                                       offer/apply newer embedded scaffold + tiles;
                                       also lists/installs MISSING essential tiles
                                       (upgraded workspaces predating them, D41)
bx user ls | add <id> [flags] | set <id> [flags] | invite <id> | signout <id> [--devices] | rm <id>
                                       manage users (admin/xbin:users); add with
                                       an empty password (or --invite) prints a
                                       single-use invite link (D22); --email
                                       binds an SSO identity (docs/auth.md §SSO);
                                       add --sso --email a@b pre-provisions an
                                       SSO-only account (no password, no link;
                                       D52); add --org o[:level[:create[:admin]]]
                                       (repeatable) joins orgs at creation;
                                       signout ends every session, terminal
                                       and frame token ("sign out everywhere",
                                       D53) — app devices stay enrolled unless
                                       --devices removes them too; set
                                       --disable/--enable pauses/restores the
                                       whole account (D34). ls shows last sign-in,
                                       a * on admins/memberships that come from
                                       IdP-group rules. --create (path patterns)
                                       is deprecated and ignored (D82). The
                                       personal plane (D88): --no-personal-tiles
                                       / --personal-tiles, --no-terminal /
                                       --allow-terminal, --sets / --net-sets
                                       s,+s,-s (sets for the tiles they own)
bx defaults [set …]                    provisioning defaults (admin): what every
                                       NEW account starts with — --tiles p=level,
                                       --org o[:level[:create]]
                                       (repeatable), --term-api/--term-net —
                                       plus --default-tiles (the D27 baseline)
                                       and --tile-creation any|org-only (D52);
                                       seed --no-personal-tiles/--no-terminal
                                       /--sets/--net-sets, and the LIVE personal
                                       defaults --personal-sets /
                                       --personal-net-sets (D88)
bx org ls|add|set|rm <id> [flags]      organizations (docs/auth.md, D24-D28)
bx org member <org> [<user> --level L [--create] [--admin]
                     [--suspend|--unsuspend] [--detach] | rm <user>]
                                       one membership per call (org admins too);
                                       --detach turns an IdP-synced membership
                                       manual (suspend: D34; sync: D53)
bx org sso-groups <org> [--add g[:level[:create[:admin]]]]… [--rm g]… [--set '<json>']
                                       IdP-group → membership rules (ws-admin):
                                       Google group email, GitHub org/team-slug,
                                       or the OIDC claim value; applied at each
                                       member's next SSO sign-in (D53)
bx org set <id> [--sets +s|-s] [--net +n|-n] [--allow +t|-t]   delegation + network sets (ws-admin)
bx netset ls | set <name> [--rules a,b|--add r|--rm r] | rm <name>
                                       organisation network sets (D54): named
                                       reach rules — internet, internet:<host|
                                       host-glob|ip|cidr>[:port], lan:<cidr>,
                                       host, provider:<tile-glob> — attached to
                                       orgs with `bx org set --net +<name>`; for
                                       an org's own tiles the union is the
                                       ceiling on net bindings, what its admins
                                       may bind without asking, the default
                                       binding `org`, and the egress of terminals
                                       opened on them (docs/auth.md §Network sets)
bx org policy [<org>] [--set '<json>'] policy-ceiling rows (workspace / org)
bx owner <tile> [--transfer user:U|org:O|workspace]   tile ownership (D24)
bx permset ls|set|rm <name> [--allow a,b] [--term-net]  permission sets (D28)
bx access <tile> [set|rm user:…|org:…=level | request [level] | approve <user> [level]]
                                       per-tile access entries — exact entries
                                       are authoritative (D31); user level
                                       `none` = explicit exclude; request files
                                       a human access request, approve grants
                                       it (D36; pending requests show in the
                                       plain listing)
bx logs [-f] <component>               backend logs (tail -f style with -f)
bx code prs [<component>|--from] [--all|--state=S]
                                       change proposals ("code PRs"): a tile's
                                       inbox (defaults to this terminal's tile),
                                       or --from = your outgoing ones
bx code pr <target> --title <t> [-m <msg>] [--base <rev>] <patch.mbox>…
                                       propose changes to a tile you can read
bx code pr show|fetch|comment|close <n> [<component>] [flags]
                                       review · fetch the series · discuss ·
                                       close (--merged|--rejected|--withdrawn)
bx api <component>                     roles + API.md — how to integrate with it
bx grants                              grant table + pending requests
bx grant <caller> <target>:<role>      approve/add a grant
bx grant --revoke <caller> <target>:<role>
bx iface                               interface requests, providers, bindings
bx bind <comp> <slot>=<p> | <slot>+=<p[#i]> | <slot>-=<p[#i]>
                                       wire interface slots (# = provider instance)
bx expose <tile> <slot>=<source> [--host H|--zone '*.Z'|--listen :P] [--add]
                                       publish an exposed endpoint (docs/ingress.md);
                                       --add adds a route (another hostname or
                                       host port) instead of replacing them
bx unexpose <tile> <slot> [--host H|--zone '*.Z'|--listen :P]
                                       remove that route, or every route
bx ingress [routes]                    published endpoints + live routes/listeners
bx vault status|unseal|seal|rekey      encryption-at-rest barrier
bx vault ls|set|rm <component> [key] [value]
                                       write-only management — values are
                                       readable only by the tile's backend
                                       (D30; `get` lists/403s for humans)
bx agent run [--tile p] [--provider claude|codex|gemini|opencode] [--mode m] [--model m] [--option id=v] [--net s] [--vm] "<prompt>"
                                       an AGENT SESSION on a tile: the coding
                                       agent runs in the tile's sandbox, its
                                       stream lands here (D74); --vm: in a VM
                                       sandbox, root in its own kernel (D89)
bx agent send <id> "<text>" | permit <id> <pid> once|always|deny|<option>
                                       prompt a running one · answer a
                                       permission request (first answer wins)
bx agent attach <id> [--since n] | ls [--tile p] | stop <id>
                                       replay + follow · list yours · end one
bx agent set <id> <option> <value>    change a setting the agent offers
                                       (model, effort, …) for its next turn
bx agent history [--tile p]            your PAST sessions — the transcripts
                                       kept when one ended (resumable or
                                       read-only per the agent)
bx agent resume <past-id> ["<prompt>"] reopen one: the agent replays the
                                       earlier turns, then continues
bx cron ls                             scheduled jobs
bx enable | disable <component>        lifecycle: pause/resume a tile (docs/overview/14-lifecycle.md)
bx hide | unhide <component>           hidden = disabled + out of sidebars (D42)
bx offload <component> [--full]        archive + free local bytes (--full incl. source)
bx backup <component>                  snapshot to the bound @archive provider
bx backups <component>                 list archived versions
bx restore <component> [--version V] [--file PATH]
                                       restore a whole version, or one file
bx backup-schedule [<component> --every 24h|--cron "…" [--keep N]|--rm]
                                       owner-scheduled backups
bx doctor                              workspace health checks
bx fix assets [<tile>] [--write] [--dir PATH]
                                       rewrite a tile's absolute /c/ asset URLs
                                       to relative ones (strict tile asset
                                       gating); dry run unless --write
bx native tree <tile> [--data d.json] [--steps s.json]
                                       the tile's rendered native UI as tree
                                       JSON (cheapest, diffable)
bx lint --native [tile…] [--static] [--json]
                                       check native UIs: static checks + a
                                       headless run; no tile = the whole
                                       workspace, with its native coverage
bx preview --native <tile> [--dark] [--size 390x844] [--large-text]
           [--data d.json] [--steps s.json] [--full] [--out shot.png]
                                       a picture of the native UI, drawn by
                                       the reference renderer
```

## Notes per command

**`bx new`** — runtimes: `go` (module + `backend/main.go` + SDK wiring via
the generated go.work), `node`, `python`, `cgi` (executable shell script),
`static` (default). `--expose` adds a roles block to the manifest and the
standard `API.md` skeleton. Never overwrites existing files. After
scaffolding, frame it somewhere:
`<bx-frame src="apps/thing"></bx-frame>`.

**`bx grant`** — the role goes after the *last* colon, so resource targets
read naturally: `bx grant apps/email res:apps/calendar/bus:reader`.
Grants are rows in the workspace `xbin.json`; revoking is deleting the row.

**`bx bind`** — wires a component's interface slots (docs/overview/11-interfaces.md).
Net slots take the builtin refs `internet`, `host`, `lan:<cidr>` — or the
FILTERED form `internet:<host|ip|cidr>[:port][,…]` (D35), restricting egress
to the named destinations (hostnames enforced by the relay's DNS pinning) —
plus `org` and `none` (D54): on an org-owned tile whose org has network
sets the slot **defaults to `org`** (the live union of the sets; `bx iface`
shows it as satisfied) and any explicit ref must be inside the sets — the
server refuses others naming the set; `none` pins a tile offline;
`set:<name>` binds one named network set (workspace admin only; inside the
org's sets on org tiles; provider-only sets refused — D65). A binding
that a later set edit or transfer leaves outside the sets goes **inert**
(`bx iface` / `bx status` say why).
`slot=provider` replaces; on a `multi:true` http slot `slot+=ref` adds and
`slot-=ref` removes, where a ref is `provider[#instance]` — instances are the
runtime-registered sub-slots of a provider (`bx iface` lists them).

**`bx code pr`** — the cross-tile suggestion channel (D48).
You can *read* sibling tiles but write only your own, so changes to another
tile travel as a PR: clone its repo out of the read-only mount, commit,
`git format-patch`, file the series. Opening needs no approval — the
capability to suggest is exactly the capability to read, and xbind never
applies a patch; the *target's* terminal/agent reviews the diff and runs
`git am --3way` itself. Typical flows:

```sh
# suggest (from your tile's terminal):
git clone "$XBIN_WORKSPACE/apps/b" /tmp/b && cd /tmp/b
# …edit, commit…
git format-patch --base=auto origin/main..HEAD
bx code pr apps/b --title "fix overflow" -m "why + how tested" 000*.patch

# receive (in apps/b's terminal):
bx code prs                                  # inbox (open PRs)
bx code pr show 3                            # message, thread, base rev
bx code pr fetch 3 | git apply --stat --check # review the shape FIRST
bx code pr fetch 3 | git am --3way           # apply with authorship kept
bx code pr close 3 --merged -m "applied as $(git rev-parse --short HEAD)"
bx code pr close 3 --rejected -m "why"       # or push back
```

`--merged`/`--rejected` are the target side's call, `--withdrawn` the
author's; comments go both ways and both agents should read them.

**`bx vault set`** — with no value argument, reads the secret from stdin
(so it stays out of shell history):
`pass show imap | bx vault set apps/email imap-pass`.

**`bx vault unseal`** — prompts for the passphrase without echo (or reads it
piped). First run **creates** the encryption barrier and encrypts existing
plaintext; later runs unlock it after a restart. This is how you bring an
env-less production instance online: boot leaves the vault locked, then an
admin unseals once after login (the admin tile's vault tab does the same
in the UI). `bx vault rekey` changes the passphrase (re-wraps the data key —
nothing re-encrypted). `bx vault status` reports the mode
(unsealed / sealed / plaintext / unconfigured); the boot modes are in
docs/auth.md §vault.

**`bx doctor`** — checks: xbind reachable; manifest parse errors; dangling
`deps`; `expose` without `API.md`; roles without descriptions; ownership/org
sanity (orphaned owner entries, admin-less or member-less orgs, allowance
entries that can never match, dead defaultTiles/share patterns); network
sets (unknown attachments, rules that can't parse, orgs granted HOST
networking, inert net bindings); go.work ownership; strict tile asset
gating (tiles whose absolute `/c/` URLs, `inject:false` or escaping symlinks
the strict modes refuse — from `GET /api/xbin/tile-assets`; under the
default legacy mode these are what the coming enforcement will refuse);
host inotify budget; toolchains present for the runtimes in use.
Run it first when something "doesn't reload".

**`bx fix assets`** — the codemod for strict tile asset gating
([auth.md §Tile asset gating](/docs/auth.md), [elements.md §Asset
URLs](/docs/elements.md)). It rewrites a tile's absolute `/c/` URLs in HTML
attributes (`src`, `href`, `srcset`, …), `<style>`/`style=""` and `.css`
files (`url()`, `@import`), the document's own import map, and module
`import` specifiers to **relative** URLs — which resolve to the very same
path in every mode, so the tile loads exactly what it loaded before, now
with a credential. It never edits other JavaScript strings, references to
workspace chrome, or documents that set their own `<base>`: those are
listed under "needs a look" with what to do. Dry run by default (prints
`file:line:col  old → new`); `--write` applies (atomically, refusing files
that changed since the scan). The tile defaults to the terminal's own; the
files are found under the workspace root, in the tile's terminal from the
working directory up, or at `--dir`. Symlinked files are fixed at their
target.

**`bx agent`** — drives an **agent session** (docs/overview/09-terminals.md
§Agent sessions): `run` opens one on a tile (inside a tile's terminal the
tile is implied and the terminal's own token is accepted for it), sends the
prompt and follows the stream until the turn ends — exit 0 on `end_turn`,
3 on a refusal or error, 130 when cancelled. Kill the client any time: the
session keeps running, `bx agent attach <id>` replays it from the start
(`--since <seq>` from a cursor) and continues live. A permission request
prints as a block naming the answer command — `bx agent permit <id> <pid>
once|always|deny` — and, when stdin is a terminal, a line `a` / `s` / `d`
answers the latest one. `always` is **allow for the session**: later
requests of the same kind and title are answered automatically; nothing is
written to `xbin.json`. Any of the request's own option ids works as the
answer too. A **plan approval** (Claude's "Ready to code?") prints the plan
and its choices — they are modes ("Yes, and use auto mode", "No, keep
planning"), so answer with an option id (`a` / `d` still work); it is never
remembered for the session. A **question** from the agent (Claude's
AskUserQuestion) prints with its fields; it is answered in the Agent tab (or
`POST …/elicitations/<eid>`, docs/protocol.md). The agent authenticates from its `$HOME` — the same
per-user home a shell terminal gets — so a `claude /login` (or `codex
login`, `opencode auth login`, …) done once in a shell terminal signs the
agent in on every tile; no per-tile API key, no vault. Bypass modes
(`bypassPermissions`, `agent-full-access`, `yolo`) are never defaults: pass
`--mode` explicitly. The agent's own settings — the model, the reasoning
effort, whatever it advertises — are shown on the `[ready]` line
(`[ready] mode default · model default · effort default`); pick one at
start with `--model sonnet` / `--option effort=high`, or change it mid-session
with `bx agent set <id> model sonnet` (applies to the next turn).
`XBIN_AGENT_PROVIDER` sets the default provider (else `claude`). A session's
transcript outlives it: once it took a prompt and ended (or the daemon
stopped), `bx agent history` lists it — `resumable` when the agent can reopen
its own session, else `read-only` — and `bx agent resume <past-id>
["<prompt>"]` continues it on the same tile (provider, mode and name carry
over; the agent replays the earlier turns first). The Agent tab shows the same
list under **Recent sessions**.

**Native UIs** (`bx native tree`, `bx lint --native`, `bx preview
--native`) — how an agent sees the `native.js` it writes
([elements.md §Native app UI](/docs/elements.md)). Each loads the tile's
runtime document, `/c/<tile>/?native=1&preview=1`, in headless Chromium —
the tile's own code, identity and frame token, against its **live backend**
— and reads what it rendered. `tree` prints the tree JSON the app would
draw; `preview` screenshots the reference renderer at 390×844 points @2x
(`--dark`, `--large-text`, `--size WxH`; `--full` grows the picture to the
content; without `--out` it writes a PNG in `$TMPDIR` and prints the path —
look at it); `lint` adds static checks and reports:

- the entry exists (a broken `native` declaration in `xbin.json` is an
  error);
- every module of the tile's parses (`node --check`, when node is there)
  and every import resolves the way the runtime document resolves it
  (relative modules in the tile, `/vendor/…`, bare names through the import
  map), and something imports `/vendor/xb-native.js`;
- raw colours (`tone="#f00"`, `rgb(…)`, a `'#ff3b30'` literal in the entry)
  where the vocabulary takes tokens;
- the runtime's errors and diagnostics (unknown primitives or props, bad
  tokens, uncaught exceptions, a module that fails to load), page errors;
- how long the first tree took (the app falls back to the web page after
  5 s without one), the tree's size, and which app revision each primitive
  and feature flag needs.

With no tile, `lint` checks every native tile and ends with the
workspace's **native coverage** (which tiles have a native UI, which render
cleanly, which are web only). It exits 1 on any error; `--json` prints the
report as JSON. The headless run needs `node` and Playwright's Chromium —
the terminal rootfs has both; elsewhere `npm i -g playwright && npx
playwright install chromium`, or `PLAYWRIGHT_DIR` naming a directory whose
`node_modules` has playwright. Without them `tree` and `preview` fail with
that message and `lint` keeps its static checks (`--static` asks for just
those).

`--data` replays a fixture instead of the live backend — a `data.json` (or
a fixture directory holding one, plus an optional `steps.json`), the format
the xbin repository's native fixtures use: `routes` maps `"METHOD
/path?query"`, `"METHOD /path"` or `"/path"` to a response `{status?, json |
text | sse: [{event?, id?, data}], headers?, delay?, error?}` (an array
answers successive calls in turn), and those routes answer the tile's
`/api/` calls — written for the fixture's `self` (default `apps/tile`), they
also match `/api/<tile>/…`; an unanswered call gets a 404 and is reported.
`now` (ms since the epoch) pins the clock, `tz`/`locale` set the browser's.
`--steps` (or the data's own `steps`) then drives it, naming nodes by the
keys `bx native tree` prints: `{"tap": key}`, `{"input": [key, value]}`,
`{"event": [key, type, payload]}`, `{"wait": ms}`, `{"visibility": …}`,
`{"resolve": [id, value]}` (a `bus` step is skipped with a warning). Steps
work without `--data` too, and against the live backend they are real
actions — a tap on "+1" increments the counter. Credentials: bx lends its own (the terminal's `XBIN_TOKEN`, or the
owner token on the host) only to the tile's document and files; the tile's
code talks to xbind with the frame token that document was minted, exactly
as in the app, and never holds bx's token. So a tile's terminal previews
that tile with its live data; another tile opened from there loads without
a frame token (one is minted only for the tile itself or a human), so its
API calls fail — give it `--data`, or run bx on the host.

```sh
bx lint --native                          # the workspace: problems + coverage
bx native tree apps/counter               # what the app would draw
bx preview --native apps/counter --dark --out /tmp/counter.png
bx preview --native apps/counter --data fixtures/busy.json --out /tmp/busy.png
```

**`bx logs`** — reads `.xbin/log/<compkey>.log` directly; each backend
generation is delimited by a `--- gen N start …` line.

## Unknown flags

A flag a command does not know is an error (`unknown flag --x`), so a typo
never turns into a positional argument or a silent no-op. Four commands
used to ignore unknown flags — `bx restore`, `bx backup-schedule`,
`bx builtin update`, `bx org add` — and for one release they print a
warning on stderr and carry on, so scripts get told before they break; the
next release makes them errors like every other command. A flag that
needs a value and is last on the line is `--x needs a value`, never a crash.

## Environment

| Var | Default | Meaning |
|-----|---------|---------|
| `XBIN_URL` | `http://127.0.0.1:8642` | xbind address |
| `XBIN_TOKEN` | (set in terminals) | bearer token — tile-scoped in terminals; the owner token on the host (`.xbin/token`) |
| `XBIN_WORKSPACE` | walk up from cwd to a dir with `xbin.json` + `.xbin` | workspace root |

**On the host** (a root/operator shell — not a xbin terminal) nothing is
injected, so `bx` reads the workspace **owner token** from `.xbin/token` — which
requires being able to read that 0600 file, i.e. run as root or the `xbin` user:

```
sudo -u xbin bx ls
```

It locates the workspace via `XBIN_WORKSPACE`, else by walking up from the
current directory, else the default `/opt/xbin/workspace`. A non-privileged user
can't read the token, so `bx` there stays unauthenticated (by design). To point
at a non-default listener, also set `XBIN_URL`.
