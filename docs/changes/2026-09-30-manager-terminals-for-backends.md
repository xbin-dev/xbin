# 2026-09-30 — the conformance suite checks the terminals a consumer's backend opens (D147): a warning now, a failure from the next release

## What changed

A consumer's backend calls a sandbox manager with its own credential and
names the person it acts for in `Sbx-User`: an **asserted** person, whom
the manager records and does not verify. The manager keeps its partitions
— the consumer's own sandboxes and those shared with it — and leaves who
among the consumer's people may use one to the consumer
([sandbox-manager.md](/docs/sandbox-manager.md) §Who is asking,
§Partitions, sharing and people). The conformance suite checked that for
reads and `run`, not for terminals.

Terminals are now part of every manager's interface for a consumer's
backend as well as its pages ([sandbox-manager.md](/docs/sandbox-manager.md)
§Terminals): a backend opens one — `GET /sbx/sandboxes/{id}/tty`, or `POST
…/execs {"tty": true}` then `GET …/execs/{eid}/tty` — to drive it or to
relay it to its own page or app view. The agent template does, for the
xbin app's terminals and a coding agent's sign-in.

`sdk/sandboxcontract` gains the check **`tty/backend`** (run when the
manager offers `tty`):

- a consumer's backend, asserting a person who is neither the owner nor a
  member of a private sandbox, opens a terminal there — the session frame,
  a resize, input, the exit code — and finds it listed as a `tty` exec;
- it attaches to a tty exec it started itself;
- a consumer the sandbox is shared with opens one for a person the share's
  `users` doesn't list;
- a consumer it isn't shared with gets `404 not-found`, whoever it names.

**In this release a manager that refuses those terminals (403 or 404) is
warned, not failed**: `tty/backend` skips with `WARNING (a failure from the
next release): the manager refused a consumer backend's terminal for …`.
**From the next release it fails.** `Target.Strict` (new) fails it now; the
reference managers set it.

## Who's affected

Only authors of a sandbox manager of their own that runs the suite
(`sandboxcontract.Run`) **and** refuses a backend's terminal for an
asserted person it wouldn't admit on a verified call — a manager that
polices `Sbx-User` on its `tty` routes. Its `go test -v` shows the warning
as a skipped `TestContract/tty/backend`; after the next release's SDK it
fails. `coding-sandbox` and `hack/fakesandbox` pass it (strictly).
Consumers and pages change nothing.

At run time nothing that worked stops working: such a manager refuses the
terminals the agent template relays (the app's terminals, a coding agent's
terminal sign-in in the app); the web's terminals dial the manager as the
verified person and are unaffected.

## How to migrate

Treat `Sbx-User` on the `tty` routes as you treat it on every other route:

- record the asserted person (pass it on as the exec's person — `forUser`
  on xbind's runtime), and let a verified `X-XBin-User` win over it;
- apply your partitions: the calling consumer's sandboxes and those shared
  with it, `404 not-found` otherwise;
- leave owner, members, `team` and a share's `users` to the consumer for a
  backend call — it checks its person before it dials;
- if your substrate asks about the person (xbind refuses a person with
  `noTerminal`, through `forUser`), ask about the asserted one.

Then set `Strict: true` on your `Target` so the suite holds you to it. If
your manager must keep refusing, say so before the check fails — name it in
`Target.Skip` with why:

```go
sandboxcontract.Run(t, sandboxcontract.Target{URL: srv.URL,
	Skip: map[string]string{"tty/backend": "backend terminals are verified-only here: …"}})
```

## Why

The owner's decision (D147): terminals are part of the sandbox interface.
The xbin app's `terminal` primitive dials only its own tile's routes, so a
tile that shows a manager's terminal in the app
relays it from its backend; and a coding agent that isn't signed in needs a
terminal in its sandbox to run its sign-in. Policing asserted persons in
the manager instead would change the rule every manager already implements
for every other route; the consumer knows its people, and checks the person
against the sandbox's owner, members, `team` and shares — fresh from the
manager — before it relays (the agent template does). The check warns for a
release first because the SDK's semantics change only permissively
([compat.md](/docs/compat.md) rules 8 and 11): a manager's conformance run
that passed yesterday must not fail on an SDK upgrade without notice.
