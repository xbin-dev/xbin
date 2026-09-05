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
                                       scaffold a component (org-owned needs
                                       the org's Create knob, D25)
bx tile ls | import <name> [as <path>] list/install builtin tiles
bx template ls | new <source> [as <path>] | updates
                                       list/instantiate template components (blueprints)
bx builtin updates | update <id> [--replace|--merge|--pr]
                                       offer/apply newer embedded scaffold + tiles;
                                       also lists/installs MISSING essential tiles
                                       (upgraded workspaces predating them, D41)
bx user ls | add <id> [flags] | set <id> [flags] | invite <id> | signout <id> | rm <id>
                                       manage users (admin/xbin:users); add with
                                       an empty password (or --invite) prints a
                                       single-use invite link (D22); --email
                                       binds an SSO identity (docs/auth.md §SSO);
                                       add --sso --email a@b pre-provisions an
                                       SSO-only account (no password, no link;
                                       D52); add --org o[:level[:create[:admin]]]
                                       (repeatable) joins orgs at creation;
                                       signout ends every session + terminal
                                       token ("sign out everywhere", D53); set
                                       --disable/--enable pauses/restores the
                                       whole account (D34). ls shows last sign-in,
                                       a * on admins/memberships that come from
                                       IdP-group rules
bx defaults [set …]                    provisioning defaults (admin): what every
                                       NEW account starts with — --tiles p=level,
                                       --create p, --org o[:level[:create]]
                                       (repeatable), --term-api/--term-net —
                                       plus --default-tiles (the D27 baseline)
                                       and --tile-creation any|org-only (D52)
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
bx expose <tile> <slot>=<source> [--host H|--zone '*.Z'|--listen :P]
                                       publish an exposed endpoint (docs/ingress.md)
bx unexpose <tile> <slot>              unpublish it
bx ingress [routes]                    published endpoints + live routes/listeners
bx vault status|unseal|seal|rekey      encryption-at-rest barrier
bx vault ls|set|rm <component> [key] [value]
                                       write-only management — values are
                                       readable only by the tile's backend
                                       (D30; `get` lists/403s for humans)
bx cron ls                             scheduled jobs
bx enable | disable <component>        lifecycle: pause/resume a tile (plans/lifecycle.md)
bx hide | unhide <component>           hidden = disabled + out of sidebars (D42)
bx offload <component> [--full]        archive + free local bytes (--full incl. source)
bx backup <component>                  snapshot to the bound @archive provider
bx backups <component>                 list archived versions
bx restore <component> [--version V] [--file PATH]
                                       restore a whole version, or one file
bx backup-schedule [<component> --every 24h|--cron "…" [--keep N]|--rm]
                                       owner-scheduled backups
bx doctor                              workspace health checks
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

**`bx bind`** — wires a component's interface slots (plans/interfaces.md).
Net slots take the builtin refs `internet`, `host`, `lan:<cidr>` — or the
FILTERED form `internet:<host|ip|cidr>[:port][,…]` (D35), restricting egress
to the named destinations (hostnames enforced by the relay's DNS pinning) —
plus `org` and `none` (D54): on an org-owned tile whose org has network
sets the slot **defaults to `org`** (the live union of the sets; `bx iface`
shows it as satisfied) and any explicit ref must be inside the sets — the
server refuses others naming the set; `none` pins a tile offline. A binding
that a later set edit or transfer leaves outside the sets goes **inert**
(`bx iface` / `bx status` say why).
`slot=provider` replaces; on a `multi:true` http slot `slot+=ref` adds and
`slot-=ref` removes, where a ref is `provider[#instance]` — instances are the
runtime-registered sub-slots of a provider (`bx iface` lists them).

**`bx code pr`** — the cross-tile suggestion channel (plans/code-prs.md).
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
networking, inert net bindings); go.work ownership; host inotify budget;
toolchains present for the runtimes in use.
Run it first when something "doesn't reload".

**`bx logs`** — reads `.xbin/log/<compkey>.log` directly; each backend
generation is delimited by a `--- gen N start …` line.

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
