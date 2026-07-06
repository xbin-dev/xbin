# bx — the workspace CLI

`bx` is on PATH in every xbin terminal. It talks to xbind with the
session's owner credentials (`XBIN_URL` + `XBIN_TOKEN`) and does local
scaffolding. Anything bx does, you can also do with curl
([protocol.md](/docs/protocol.md)) or plain file edits — bx is convenience,
not magic.

```
bx ls                                  list components (runtime, exposed roles, manifest errors)
bx status                              backend states (building/healthy/failed, generations)
bx new <path> [--runtime R] [--expose] [--title "Pretty Name"]
                                       scaffold a component
bx tile ls | import <name> [as <path>] list/install builtin tiles
bx template ls | new <source> [as <path>]
                                       list/instantiate template components (blueprints)
bx user ls | add <id> [flags] | set <id> [flags] | rm <id>
                                       manage users (admin/xbin:users)
bx logs [-f] <component>               backend logs (tail -f style with -f)
bx api <component>                     roles + API.md — how to integrate with it
bx grants                              grant table + pending requests
bx grant <caller> <target>:<role>      approve/add a grant
bx grant --revoke <caller> <target>:<role>
bx iface                               interface requests, providers, bindings
bx bind <comp> <slot>=<p> | <slot>+=<p[#i]> | <slot>-=<p[#i]>
                                       wire interface slots (# = provider instance)
bx vault status|unseal|seal|rekey      encryption-at-rest barrier
bx vault ls|get|set|rm <component> [key] [value]
bx cron ls                             scheduled jobs
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
`slot=provider` replaces; on a `multi:true` http slot `slot+=ref` adds and
`slot-=ref` removes, where a ref is `provider[#instance]` — instances are the
runtime-registered sub-slots of a provider (`bx iface` lists them).

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
`deps`; `expose` without `API.md`; roles without descriptions; go.work
ownership; host inotify budget; toolchains present for the runtimes in use.
Run it first when something "doesn't reload".

**`bx logs`** — reads `.xbin/log/<compkey>.log` directly; each backend
generation is delimited by a `--- gen N start …` line.

## Environment

| Var | Default | Meaning |
|-----|---------|---------|
| `XBIN_URL` | `http://127.0.0.1:8642` | xbind address |
| `XBIN_TOKEN` | (set in terminals) | owner bearer token |
| `XBIN_WORKSPACE` | walk up from cwd to a dir with `xbin.json` + `.xbin` | workspace root |

Outside a xbin terminal (ssh into the container, host shell in dev), set
these yourself or run from inside the workspace tree.
