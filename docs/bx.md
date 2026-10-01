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
                                       --all = workspace-wide, else this terminal's tile;
                                       <tile>+<name> or --deployment <name>: one tile
                                       deployment's (docs/tile-deployments.md)
bx new <path> [--runtime R] [--expose] [--title "Pretty Name"] [--owner user:U|org:O]
                                       scaffold a component you'll own, at any
                                       free path outside tiles/ and others'
                                       scopes (org-owned needs the org's
                                       Create knob; D25, D82)
bx tile ls | import <name> [as <path>] list/install builtin tiles
bx template ls | new <source> [as <path>] [--no-partition] | updates
                                       list/instantiate template components (blueprints);
                                       --no-partition: the copy doesn't start in
                                       the template's partition mode (docs/partitions.md)
bx template merge-manifest [--marker-size N] [--rename FROM=TO] BASE OURS THEIRS
                                       git's merge driver for a template instance's
                                       xbin.json, which xbind names in the
                                       instance's repo — git runs it, you don't:
                                       where the line merge of the template's
                                       change conflicts, merges by keys
                                       (docs/overview/03-components.md §Templates)
bx builtin updates | update <id> [--replace|--merge|--pr]
                                       offer/apply newer embedded scaffold + tiles;
                                       also lists/installs MISSING essential tiles
                                       (upgraded workspaces predating them, D41);
                                       every mode keeps each xbin.json's installed
                                       "partition" and prints a note when upstream
                                       asks otherwise (docs/partitions.md)
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
bx chrome [ls] | approve <tile> | revoke <tile>
                                       trusted chrome (admin, D118): tiles whose
                                       xbin.json asks for chrome, and approvals
bx policies [ls] [--json] | set partition-consent|credential-reset-confirm on|off
                                       workspace policies for partitioned tiles
                                       (PD-55): read (anyone signed in), set
                                       (admin)
bx partition switch <tile> [--dry-run] [--confirm <tile>] [--yes] [--json]
bx partition keep <tile> [--json]     decide a tile's partition mode switch
                                       request (a tile manager): switch deletes
                                       all its data, keep deletes nothing
bx partition mail ls [--after <id>] [--limit n] [--json] | mail ack <id>...
                                       the partition mail inbox of the
                                       partition bx runs in (a person's
                                       terminal on a partitioned tile; its
                                       root terminal reads global's)
bx partition consent <from> <to> [--revoke] | consent ls [--json]
                                       let partitioned tile <from> use your data
                                       in <to> (while the workspace asks people
                                       first), or take it back
bx partition ledger [<tile>] [--days n] [--json]
                                       your partitions' egress ledger (counts)
bx partition ls [<tile>] [--json]      partitioned tiles, or one tile's partitions
bx partition stop|reset <tile> [--user <id>] [--yes]
                                       stop a partition's instance, or delete its data
bx partition purge [<tile>] [--partition <id>] [--yes]
                                       delete orphaned partitions now (admin);
                                       without --yes, list what it would delete
bx partition limits [<tile>] [--max-running n] [--partition-bytes n]
bx partition share-log <tile> [--days n] [--stop]
                                       share your partition's log with its managers
bx partition credential <id> allow|refuse
                                       answer a credential an admin made for you
bx partition reviewed <tile> on|off    run reviewed code only (admin)
bx settings [ls] | set --base-auto-update[=true|false]
                                       workspace settings (set: admin, D175):
                                       base auto-update, terminals moving to a
                                       new base image at their next start
bx permset ls|set|rm <name> [--allow a,b] [--term-net]  permission sets (D28)
bx access <tile> [set|rm user:…|org:…=level | request [level] | approve <user> [level]]
                                       per-tile access entries — exact entries
                                       are authoritative (D31); user level
                                       `none` = explicit exclude; request files
                                       a human access request, approve grants
                                       it (D36; pending requests show in the
                                       plain listing)
bx logs [-f] <component>               backend logs (tail -f style with -f);
                                       <tile>+<name> or --deployment <name>: a
                                       tile deployment's log; on a partitioned
                                       tile --global (the global instance's) or
                                       --user <id> (a person's shared log)
bx live-reload [<tile>] [--json]       where saves go: live reload's target or
                                       paused (by whom, when), what each
                                       deployment runs (docs/tile-deployments.md)
bx live-reload pause|now|resume [<tile>] [--to <name>]
                                       keep the tile on the code it runs while
                                       you edit · ship the work tree once ·
                                       follow every save again
bx live-reload attach [<tile>] --to <name>
                                       move live reload to another deployment
bx deploy [<tile>] --to <name> [--checkpoint c:<id>]
                                       put a fresh checkpoint of the work tree
                                       (or c:<id>) on it
bx rollback [<tile>] --to <name> [--checkpoint c:<id>]
                                       back to the previous checkpoint in its
                                       deploy log
bx promote [<tile>] <from> <to>        give <to> exactly <from>'s code; its
                                       data stays
bx deployment ls|add|rm|primary|protect|seed|reset|vault-copy|set|edge|run-now|log|diff …
                                       a tile's deployments (§Tile deployments);
                                       changing commands take --dry-run, --yes,
                                       --json, --no-wait
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
bx grants                              grant table + pending requests (a
                                       partitioned tile's on another's people's
                                       data: whose data it would reach)
bx grant <caller> <target>:<role>      approve/add a grant
bx grant --revoke <caller> <target>:<role>
bx iface                               interface requests, providers, bindings
bx bind <comp> <slot>=<p> | <slot>+=<p[#i]> | <slot>-=<p[#i]>
                                       wire interface slots (# = provider instance)
bx bind --personal [--unset] <tile> <slot>=<your tile> [--json]
                                       wire a tile you own into your own partition
                                       of a partitioned tile; alone: list them
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
                                       sandbox, root in its own kernel (D89);
                                       --deployment <name> (or --tile p+<name>):
                                       its calls reach that tile deployment
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
                                       — not live reload: see bx live-reload
bx hide | unhide <component>           hidden = disabled + out of sidebars (D42)
bx offload <component> [--full]        archive + free local bytes (--full incl. source)
bx backup <component>                  snapshot to the bound @archive provider;
                                       a partitioned tile's people's
                                       partitions too — exits 1 when one
                                       isn't backed up (the tile's own
                                       archive is), warns when none can be
                                       (plaintext-vault mode)
bx backups <component>                 list archived versions
bx restore <component> [--version V] [--file PATH] [--confirm DATE]
                                       restore a whole version, or one file;
                                       a backup older than the tile's last
                                       partition mode switch restores only
                                       with --confirm <the switch's date>
                                       (docs/partitions.md §Backups); an
                                       xbind without partitions refuses
                                       --confirm (run it without)
bx backups <tile> --partition [--user ID] [--partition-id u-…]
                                       a person's partition's archived
                                       versions: your own (your id now), or
                                       (an admin) anyone's, an earlier
                                       holder's included
bx restore <tile> --partition [--user ID] [--version V] [--partition-id u-…]
           [--to ID] [--dry-run] [--yes] [--json]
                                       replace a person's partition — its
                                       data, vault and registrations — with
                                       its backup; asks you to type
                                       "<tile> user:<id>" unless --yes; your
                                       own from your own session, anyone's
                                       as an admin; an earlier holder's
                                       (--partition-id, the id deleted and
                                       recreated since) only an admin, with
                                       --to <the id>; exits 6 against an
                                       xbind without it
bx backup-schedule [<component> --every 24h|--cron "…" [--keep N]|--rm]
                                       owner-scheduled backups
bx backup keys status                  are archives sealed; keys in no export
                                       yet; the last export; erasures since
bx backup keys export > keys.xbk       the disaster-recovery key bundle
                                       (an admin in their own session):
                                       useless without the vault
                                       passphrase, the workspace's data key
                                       with the passphrase in force now —
                                       keep them apart; re-export after
                                       erasures and passphrase changes, and
                                       destroy older bundles
bx backup keys import keys.xbk         another workspace's backup keys, so
                                       this one restores its sealed archives
                                       (prompts for that workspace's vault
                                       passphrase; piped: one line on stdin)
bx backup erase <tile> --data|--all [--yes]
                                       crypto-erase a tile's backups in every
                                       archive: --data its data keys (source
                                       stays restorable), --all every key;
                                       asks for the tile's path unless --yes;
                                       says what no key erases (plain
                                       archives made before sealing, a
                                       non-root tile's data: its root's)
bx doctor                              workspace health checks
bx fix assets [<tile>] [--write] [--dir PATH]
                                       rewrite a tile's absolute /c/ asset URLs
                                       to relative ones (strict tile asset
                                       gating); dry run unless --write
bx native tree <tile> [--data d.json] [--steps s.json]
           [--widget [--size small|wide]]
                                       the tile's rendered native UI as tree
                                       JSON (cheapest, diffable); --widget:
                                       its widget's tree
bx lint --native [tile…] [--static] [--json]
                                       check native UIs (the screen and the
                                       widget at both sizes): static checks +
                                       a headless run; no tile = the whole
                                       workspace, with its native coverage
bx preview --native <tile> [--dark] [--size 390x844] [--large-text]
           [--data d.json] [--steps s.json] [--full] [--out shot.png]
           [--widget [--size small|wide]]
                                       a picture of the native UI, drawn by
                                       the reference renderer; --widget: of
                                       its widget's card
```

## Notes per command

**`bx new`** — runtimes: `go` (module + `backend/main.go` + SDK wiring via
the generated go.work), `node`, `python`, `static` (default). `cgi` was
removed ([changes/2026-09-27-cgi-removed.md](/docs/changes/2026-09-27-cgi-removed.md))
and is refused. `--expose` adds a roles block to the manifest and the
standard `API.md` skeleton. Never overwrites existing files. The path may
not hold `+` in any segment (`<tile>+<name>` is a tile deployment's URL):
refused for everyone, locally and through the API, like every other way a
tile is created
([changes/2026-09-28-plus-in-tile-names.md](/docs/changes/2026-09-28-plus-in-tile-names.md)).
After scaffolding, frame it somewhere:
`<bx-frame src="apps/thing"></bx-frame>`.

**`bx grant`** — the role goes after the *last* colon, so resource targets
read naturally: `bx grant apps/email res:apps/calendar/bus:reader`.
Grants are rows in the workspace `xbin.json`; revoking is deleting the row.
Approving a partitioned tile's grant on another partitioned tile's people's
data prints, on stderr, whose data its code will now reach
([partitions.md](/docs/partitions.md)); so does `bx bind` wiring a
partitioned tile's http slot to another partitioned tile.

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
`bx bind --personal <tile> <slot>=<provider>` makes a **personal bind**
([partitions.md §Bind types](/docs/partitions.md)): a tile you own
personally, wired into your own partition of a partitioned tile only — run
it with your own sign-in, not from a tile's terminal; an admin can't (an
admin's bind is always global: `bx bind`). `--unset` removes it;
`bx bind --personal` alone lists yours (an admin's: everyone's).

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
networking, inert net bindings); chrome requests no admin approved (those
tiles run sandboxed) and approvals naming no component; tiles whose name
holds `+` (which can't get deployments; no new name may hold one); go.work
ownership; Go tiles that build with older dependency versions since each
builds with its own `go.mod` (D166), with the `require` lines that keep what
each had and what changed (admin credentials: `GET
/api/xbin/go-build-versions`; a dismissed one is a note, as is a tile the
check couldn't compare — [the migration
note](/docs/changes/2026-09-30-go-build-workspace.md)); strict tile asset gating (tiles whose absolute `/c/` URLs, `inject:false` or escaping symlinks
the strict modes refuse — from `GET /api/xbin/tile-assets`; under the
default legacy mode these are what the coming enforcement will refuse);
partitioned tiles holding a grant on another partitioned tile's people's
data, with whose data their code reaches and how many people used each edge
in 30 days (admin credentials; [partitions.md](/docs/partitions.md));
host inotify budget; toolchains present for the runtimes in use.
Run it first when something "doesn't reload".

**`bx chrome`** — trusted workspace chrome ([auth.md §Who is
calling](/docs/auth.md), D118). A tile whose xbin.json says `"chrome": true`
runs unsandboxed — with the session cookie, acting as whoever opens it —
only once a workspace admin approves it; until then it runs sandboxed.
`bx chrome` lists every tile that asks and every approval, with its state;
`approve <tile>` needs a component at the path; `revoke <tile>` withdraws an
approval (also a removed tile's). Admin credentials (`GET`/`PUT
/api/xbin/chrome`). Approving a tile trusts every writer of it — its
terminal users and their coding agents — as much as the shell.

**`bx policies`** — the workspace policies for partitioned tiles (tiles
where each person has their own data; PD-55), the same two switches as the
admin console's workspace → policies tab, both off by default:
`partition-consent` (ask each person before another partitioned tile uses
their data) and `credential-reset-confirm` (a sign-in link, password or SSO
email an admin sets for someone who holds partitions works only after they
confirm, or 24 h after they're notified). `bx policies` prints them (`--json`:
the `GET /api/xbin/workspace-policies` answer; a person's session, or a
terminal or agent session they drive, or an admin); `bx policies set <switch> on|off` changes one (admin,
`PUT`). Neither changes anything for tiles that aren't partitioned.

**`bx partition switch|keep <tile>`** — decide a partition mode switch
request ([partitions.md §The mode](/docs/partitions.md)): a tile that holds
data whose code asks for another `partition` is paused until a tile manager
— the tile's owner, an admin of its owning org, or a workspace admin, with
bx on the root token or their own login (a tile's terminal can't decide) —
does one of two things. `keep` records "keep the current mode": the tile
runs again at once and nothing is deleted (the code keeps asking, and
`switch` stays possible). `switch` first shows what it deletes — data
namespaces, people's partitions, vault keys, registrations, bytes and backup
keys — and what it keeps, then asks for the tile's path (`--confirm <tile>`
answers without asking; `--dry-run` only shows). It then deletes the tile's
data and takes the mode the code asks for; everyone whose partition was
deleted is told. Adding `"global"` to a partitioned tile deletes nothing;
removing it deletes only the global instance's data and the shared
resources. A switch to user partitions needs xbind's `--isolate`; if the
tile binds sandbox managers that don't keep people apart (their hello lacks
`partitions`), it is refused unless `--yes`. Both read the request from the
tile's `/components` row and send it back, so a request that changed
meanwhile is refused rather than decided blind. They exit 6 against an
xbind without partitioned tiles (one older than them: its rows carry no
partition and it lacks the route). The typed confirmation's prompt goes to
stderr, so `--json` keeps stdout to the JSON answer.

**`bx partition mail ls|ack`** — partition mail
([partitions.md §Partition mail](/docs/partitions.md)) from where it is
read: in your terminal on a partitioned tile, your partition's inbox (the
terminal's credential is your partition's); in the tile's root terminal
(or an agent session acting as global), or with the global instance's
backend token, the global instance's. `ls` lists the waiting items, oldest
first, one row each (`--limit`, default 100; when more wait it prints the
`--after <id>` to read on with; `--json` prints the answer); `ack <id>…`
removes items. Nobody else reads an inbox: an admin's login or the root
token gets 403. It exits 6 against an xbind without partition mail.

**`bx partition consent|ledger`** — calls between partitioned tiles
([partitions.md §Calls between partitioned tiles](/docs/partitions.md)).
While the workspace policy `partition-consent` is on (`bx policies`), a
partitioned tile reaches your data in another partitioned tile only once
you allow it: `bx partition consent <from> <to>` does, `--revoke` takes it
back (at once: `<from>`'s backend instance of you is stopped; it says so
when there was nothing to take back), and `consent ls` lists your consents
and the edges you were asked about. Both are your own acts: bx with your
login, never a tile's terminal. `bx partition ledger` prints your
partitions' egress ledger — per day, how often each of your partitions
called or reached another tile, never what it sent — for one tile or all
(`--days`, default 30); with a tile, its managers also get its totals
(personal tiles unnamed), and admins every person's totals. Both exit 6
against an xbind without them, naming the route it lacks.

**`bx partition ls|stop|reset|purge|limits|share-log|credential|reviewed`** —
operating people's partitions ([partitions.md §Operating people's
partitions](/docs/partitions.md)). `ls` lists the partitioned tiles (and
your partition of each), or one tile's partitions: yours; totals for its
writers and managers; every person's metadata for admins (never what a
partition holds), its orphans and its trust warnings. `stop` stops a
partition's instance — yours by default, anyone's (`--user`) for a tile
manager or admin; its data stays. `reset` deletes a partition's data —
yours, or anyone's for an admin, who tells them — after you type
`<tile> user:<id>` (`--yes` answers for you); its backup keys are erased.
`purge` (admin) deletes orphaned partitions — their person deleted, their
tile removed — now instead of 30 days later: without `--yes` it lists what
it would delete (and exits 1), `--partition <id>` picks one. `limits` shows or sets the
running cap and each partition's byte ceiling (admins; a tile manager may
lower their tile's). `share-log` lets the tile's managers and admins read
your partition's backend log for `--days` (1–14, default 7; they read it
with `GET /api/xbin/logs?component=<tile>&user=<id>`), `--stop` ends it. `credential` answers a
sign-in link, password or SSO email an admin made for you while the
workspace asks people first (`bx policies`: credential-reset-confirm);
`bx partition ls` prints the ones waiting; a link already used answers
"already effective". `reviewed <tile> on|off` (admins) sets the tile to run
reviewed code only: its primary and every provider bound to it must be
protected, and stay so while it is on. All are your own acts — bx with
your login or the root token, never a tile's terminal — and exit 6 against
an xbind without them. In a partition's terminal, `bx status` prints
`partition: user:<id>` and `bx logs` reads your partition's own log;
elsewhere `bx logs <tile> --global` reads a partitioned tile's global
instance's log and `--user <id>` a person's partition's log while they
share it (both from xbind).
`bx doctor` reports tiles waiting for a mode decision, partitioned tiles
without `--isolate`, who can change a partitioned tile's code while it runs
live, its global binds, tiles bound to one without a global instance,
sandbox managers that don't keep people apart, files the tile's own
repository doesn't track (xbind lists them, with a confined git), caps its
people's partitions met in the last day, and orphaned partitions (with the
`bx partition purge … --partition <id> --yes` that deletes each).

**`bx settings`** — the workspace settings an admin sets (D175; the admin
console's workspace → terminals tab sets the same). `bx settings` shows
them; `bx settings set --base-auto-update=false` (or `--no-base-auto-update`)
turns base auto-update off, `--base-auto-update` back on. On — the default —
a tile's terminal layer built on an older base image moves to the current
base at its next session start: everything outside the workspace files and
`$HOME` is reset, for good (installed packages, `/etc`, `/var`, `/opt`…, a
VM terminal's disk), and a running terminal keeps its base until it ends
([09-terminals.md](/docs/overview/09-terminals.md) §Base images).
Off, a layer stays on its base and the terminal window offers the update.
Reads need any credential, the change an admin's (`GET`/`PUT
/api/xbin/workspace-settings`); an xbind without the setting answers 404.

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
tile is implied and the terminal's own token is accepted for it — not
inside an agent session's sandbox: an agent's token opens and drives no
agent session, D97), sends the
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
per-user home a shell terminal gets — so a `claude auth login` (or `codex
login`, `opencode auth login`, …) done once in a shell terminal, or the
Agent tab's guided sign-in (a link and a pasted code, D178), signs the
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
--native`; D98) — how an agent sees the `native.js` it writes
([native.md](/docs/native.md), [elements.md §Native app
UI](/docs/elements.md)). Each loads the tile's runtime document,
`/c/<tile>/?native=1&preview=1`, in headless Chromium —
the tile's own code, identity and frame token, against its **live backend**
— and reads what it rendered. `tree` prints the tree JSON the app would
draw; `preview` screenshots the reference renderer at 390×844 points @2x
(`--dark`, `--large-text`, `--size WxH`; `--full` grows the picture to the
content; without `--out` it writes a PNG in `$TMPDIR` and prints the path —
look at it). With `--widget` both play an app that shows widgets
([native.md §Widgets](/docs/native.md)): `tree` prints the widget's tree
and `preview` pictures its card, at the size class `--size small|wide`
names (default small; `--size small|wide` alone implies `--widget`, and
`preview` still takes `--size WxH` for the page). `lint` adds static checks
and reports:

- the entry exists (a broken `native` declaration in `xbin.json` is an
  error);
- every module of the tile's parses (`node --check`, when node is there)
  and every import resolves the way the runtime document resolves it
  (relative modules in the tile, `/vendor/…`, bare names through the import
  map), and something imports `/vendor/xb-native.js`;
- raw colours (`tone="#f00"`, `rgb(…)`, a `'#ff3b30'` literal in the entry)
  where the vocabulary takes tokens;
- the runtime's errors and diagnostics (unknown primitives or props, bad
  tokens, uncaught exceptions, a module that fails to load), page errors —
  the widget's included: lint renders it small, then wide, and a finding
  about it starts with `widget` (`widget-tag`: a primitive a widget may not
  use);
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
`{"resolve": [id, value]}`, `{"widgetSize": "wide"}` (a `bus` step is
skipped with a warning); `"target": "widget"` on a `tap`/`input`/`event`
step acts on the widget's tree. Steps
work without `--data` too, and against the live backend they are real
actions — a tap on "+1" increments the counter. Credentials: bx lends its own (the terminal's `XBIN_TOKEN`, or the
owner token on the host) only to the tile's document and files; the tile's
code talks to xbind with the frame token that document was minted, exactly
as in the app, and never holds bx's token. Several tiles in one run (`lint
--native` with no tile, or naming several) run one after another, each in
a browser of its own behind a proxy that lends to that tile alone — one
tile's page can't reach another's document with bx's credential. So a tile's terminal previews
that tile with its live data; another tile opened from there loads without
a frame token (one is minted only for the tile itself or a human), so its
API calls fail — give it `--data`, or run bx on the host.

```sh
bx lint --native                          # the workspace: problems + coverage
bx native tree apps/counter               # what the app would draw
bx preview --native apps/counter --dark --out /tmp/counter.png
bx preview --native apps/counter --data fixtures/busy.json --out /tmp/busy.png
bx native tree apps/counter --widget --size wide   # the widget, wide
bx preview --native apps/counter --widget --out /tmp/counter-card.png
```

**`bx logs`** — reads `.xbin/log/<compkey>.log` directly where it can see
it; in an isolated terminal, where `.xbin` is masked, it streams `GET
/api/xbin/logs` instead (the whole log up to 1 MiB; `-f` the last 64 KiB,
then everything appended). Each backend generation is delimited by a
`--- gen N start …` line, and a failed deploy of a checkpoint by
`--- deploy of c:<id> failed <time> ---` followed by the compiler output.
That file is `main`'s log. Another tile deployment's log is xbind's own:
`bx logs apps/crm+dev` (or `--deployment dev`; in a terminal that targets a
deployment, `$XBIN_DEPLOYMENT` is the default) streams it from `GET
/api/xbin/logs?deployment=`, and the output names it. `bx status` takes the
same `+<name>` and `--deployment`, and prints a `deployments` line for a tile
that has any. bx sends `<tile>+<name>` as the tile and `deployment=`, never
as one query value (a `+` in a query string reads as a space); a tile whose
own name holds `+` (created before `+` was refused in tile names) is read by
that name when it sits in the workspace, or when no deployment answers.

## Tile deployments: live reload, deploy, promote

The commands of [tile-deployments.md](/docs/tile-deployments.md): pausing a
tile's live reload, Reload now, resuming, a tile's named deployments,
deploying a checkpoint, promoting, rolling back, and what tile managers
decide. `<tile>+<name>` works wherever `<tile> <name>` does.

```sh
bx live-reload                      # where this tile's saves go
bx live-reload pause                # the target keeps the code it runs; saves stop reaching it
bx live-reload now                  # ship the work tree once to where live reload last was; it stays pinned
bx live-reload resume [--to dev]    # a deployment follows every save again
bx live-reload attach --to dev      # while live reload is on: move it to dev (the one it leaves is pinned)
bx deploy --to dev                  # a fresh checkpoint of the work tree onto dev
bx rollback --to main               # main's previous checkpoint from its deploy log
bx rollback apps/crm --to main --checkpoint c:1e9d0aa
bx promote dev main                 # main gets exactly dev's code; its data stays
```

```
bx deployment ls [<tile>]           what each deployment runs, its status and data, "← this
                                    terminal"; no tile and no $XBIN_COMPONENT: every tile with
                                    deployments
bx deployment add [<tile>] <name> [--from work-tree|primary|c:<id>] [--seed] [--attach]
                  [--branch <b> | --new-branch <b>] [--other-branch]
                                    --branch: <name> requires branch <b>; --new-branch: create <b>
                                    in the tile at its HEAD, check it out, and require it
bx deployment branch [<tile>] <name> <b> | --clear
                                    the work tree's branch <name> requires, or none
bx deployment rm [<tile>] <name>
bx deployment primary [<tile>] --to <name>      (or: primary <tile> <name>)
bx deployment protect [<tile>] on|off
bx deployment seed [<tile>] <name> [--stop]
bx deployment reset [<tile>] <name> [--vault]
bx deployment vault-copy [<tile>] <name> --keys k1,k2 | --all
bx deployment set [<tile>] <name> [--deliveries on|off] [--always-on on|off]
                  [--mem <MiB>|default] [--pids <n>|default] [--disk <GiB>|default]
                                    the deliveries, always-on and limits routes, in that order,
                                    stopping at the first refusal; --json prints the last answer
bx deployment edge [<tile>] [<edge> read|block|inherit|default]
                                    no edge: the tile's edges (slot:<name>, grant:<target>),
                                    their policies, refused and clamped counts
bx deployment run-now [<tile>] <name> <job>
bx deployment log [<tile>] [<name>] [--limit <n>]
bx deployment diff [<tile>] [<from> [<to>]] [--stat] [--path <file>]
                                    from/to: a deployment, c:<id> or work-tree; defaults: the
                                    primary, and the work tree; --json is the --stat answer
bx status|logs <tile>+<name>        also --deployment <name>: that deployment's status or log
bx agent run --deployment <name> …  an agent session whose calls reach that deployment
```

- **Which tile.** The tile is the first positional only when the command has
  its full count of positionals; otherwise it is `$XBIN_COMPONENT`, the
  terminal's own. A positional containing `/` or `+` is always a tile ref, and
  `<tile>+<name>` also fills a missing `--to`. With neither, bx asks
  `which tile?` and exits 2.
- **Which deployment.** A read — `bx status`, `bx logs`, `bx deployment log`
  — on the terminal's own tile defaults to `$XBIN_DEPLOYMENT`, this
  session's target, and its output names the deployment (`apps/crm+dev`). A
  command that changes something never takes its deployment from a
  variable: `deploy` and `rollback` need `--to` (or the qualifier), and
  `promote` names both deployments.
- **Before acting**, every changing command reads the tile's state; if the
  operation isn't allowed it prints why and exits without sending anything.
  Then it sends a dry run of the exact request and prints the report —
  `Code`, `Data`, `Pauses`, `Affects` — naming the checkpoint it will ship.
  The request that follows carries that checkpoint, so what the report showed
  is what ships (a changed work tree answers 409 instead); onto a protected
  primary it also carries the state's `seq`.
- **Confirmation.** Guarded commands ask the operation's question
  (`Promote dev → main? [y/N]`) on a terminal: every code move onto the
  primary — `deploy`, `promote` and `rollback` to it, and `live-reload now`,
  `resume` or `attach` when they change what it runs — and `deployment rm`,
  `primary`, `protect`, `seed`, `reset`, `vault-copy` and `add --seed`.
  Without a terminal they need `--yes`, else bx prints the report and exits
  4: `--yes` is an agent's statement that the user asked, and it also sends
  the route's `confirm` token. Other changing commands print the report and
  proceed. `--dry-run` prints the report and changes nothing.
- **Waiting.** A code move waits for the deploy's result, printing each phase
  to stderr (`apps/crm: deploy 42 main c:7b19e02 … build … start … swap …
  ok (38s)`), for at most 20 minutes; `--no-wait` returns once the request is
  accepted. Commands that move live reload end by saying where saves go.
- **`--json`** prints the route's answer verbatim — the operation's `{state,
  deploy}` as soon as it arrives — as the only thing on stdout; the report,
  the phases and the result go to stderr, and the outcome is the exit code.
- **Assigned branches** (an xbind listing `branches/1`; bx refuses `--branch`,
  `--new-branch` and `deployment branch` against an older one, exit 1).
  `live-reload resume`, `attach` and `now`, `deploy` of the work tree and
  `deployment add` from it refuse a work tree on another branch than the
  deployment requires (the server's 409 names both); `--other-branch` takes
  the work tree's branch this time — kept, for resume and attach, until live
  reload moves or the branch changes again. `bx live-reload` and `bx
  deployment ls` show each deployment's branch and the work tree's
  ([tile-deployments.md](/docs/tile-deployments.md) §Assigned branches).
- **Tile managers' acts** (`primary`, `protect`, `seed`, `vault-copy`,
  `set`, `edge`) run from the host with the root token, or in a human
  session; never from a tile terminal. The root token is a person's
  credential, so `bx deployment protect apps/crm on` or `bx deployment
  primary apps/crm --to dev` on the host (the owner token, `.xbin/token`)
  passes the manager gate for every tile. From a tile's terminal — the admin
  tile's included — bx is refused (exit 3) and says to use the terminal
  window's Deployments panel as a tile manager, or bx on the host. Protection,
  reassigning the primary, deliveries and alwaysOn are also in the admin
  console's runtime → deployments tab ([auth.md](/docs/auth.md) §Tile
  deployments).

| Exit | Meaning | An agent reads it as |
|---|---|---|
| 0 | done: the deploy finished `ok`, nothing needed doing, a dry run, or `--no-wait` and the request was accepted | — |
| 1 | failed: a deploy that ran and failed (the deployment keeps its previous code), an invalid state, a network error, any other refusal | read the message, fix, retry |
| 2 | usage | fix the command |
| 3 | refused by authority or policy (HTTP 403, or a permission the state says is off) | not yours to do: tell the user who can (the message names them) |
| 4 | not confirmed: declined, or no terminal and no `--yes` | ask the user |
| 5 | still running when bx stopped waiting | check later with `bx deployment log` |
| 6 | this xbind has no tile deployments (`this xbind has no tile deployments (no /api/xbin/deployments); upgrade xbind`) | don't retry here |

Every other command keeps exiting 1 on any error and 2 on usage.

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
| `XBIN_COMPONENT` | (set in terminals) | the terminal's tile: the default tile of `bx status`, `bx logs`, `bx live-reload`, `bx deploy`, `bx promote`, `bx rollback`, `bx deployment` |
| `XBIN_DEPLOYMENT` | (set in terminals that target a tile deployment other than the primary) | the session's target: the default deployment of `bx status`, `bx logs` and `bx deployment log` on the terminal's tile; unset means the primary. Never the target of a changing command |

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
