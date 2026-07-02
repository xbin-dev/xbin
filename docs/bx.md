# bx — the workspace CLI

`bx` is on PATH in every buxon terminal. It talks to buxond with the
session's owner credentials (`BUXON_URL` + `BUXON_TOKEN`) and does local
scaffolding. Anything bx does, you can also do with curl
([protocol.md](/docs/protocol.md)) or plain file edits — bx is convenience,
not magic.

```
bx ls                                  list components (runtime, exposed roles, manifest errors)
bx status                              backend states (building/healthy/failed, generations)
bx new <path> [--runtime R] [--expose] [--title "Pretty Name"]
                                       scaffold a component
bx logs [-f] <component>               backend logs (tail -f style with -f)
bx api <component>                     roles + API.md — how to integrate with it
bx grants                              grant table + pending requests
bx grant <caller> <target>:<role>      approve/add a grant
bx grant --revoke <caller> <target>:<role>
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
Grants are rows in the workspace `buxon.json`; revoking is deleting the row.

**`bx vault set`** — with no value argument, reads the secret from stdin
(so it stays out of shell history):
`pass show imap | bx vault set apps/email imap-pass`.

**`bx doctor`** — checks: buxond reachable; manifest parse errors; dangling
`deps`; `expose` without `API.md`; roles without descriptions; go.work
ownership; host inotify budget; toolchains present for the runtimes in use.
Run it first when something "doesn't reload".

**`bx logs`** — reads `.buxon/log/<compkey>.log` directly; each backend
generation is delimited by a `--- gen N start …` line.

## Environment

| Var | Default | Meaning |
|-----|---------|---------|
| `BUXON_URL` | `http://127.0.0.1:8642` | buxond address |
| `BUXON_TOKEN` | (set in terminals) | owner bearer token |
| `BUXON_WORKSPACE` | walk up from cwd to a dir with `buxon.json` + `.buxon` | workspace root |

Outside a buxon terminal (ssh into the container, host shell in dev), set
these yourself or run from inside the workspace tree.
