# A delegated request's body is read one way only

**BREAKING for a delegate that builds its own request bodies** and sent a
key in another case, a key twice, or a key the call doesn't take. A
security fix (D252): it closes a hole present in v0.3.80 to v0.3.84.

## What changed

Every body xbind describes to a person before they approve it is read
strictly when it is filed, and again when its ticket is used:

- a tile agent's start (`POST /term/sessions`, asked for or started
  without asking), a message to a tile agent (`…/prompt`, `…/queue`), a
  new tile (`POST /templates/new`), a page to read (`…/delegate/web/fetch`)
  and an MCP tool's call;
- each line's `args` in a change set or a plan (`POST /delegate/changesets`,
  `POST /delegate/plans`): grants, bindings, `org.share`, `limits.set`,
  `access.set`, `builtin.update`, `tile.create`, `agent.start`.

Strictly means one JSON object, UTF-8, each member one the call takes,
written exactly as documented (lower case: `cwd`, `prompt`, `from`, …),
none twice at any depth, nothing after the object. Anything else is
refused naming the member: `400` (`403` for a new tile's request), for
example `an unknown member "Prompt" (its members: …)` or `the member
"prompt" twice`. A value of the wrong type (an object where a string
goes) is refused too.

A request filed before the upgrade whose body doesn't read so is refused
when its ticket is used — `403 {refusal: "ticket", reason: "bound"}`,
"…nothing ran — file the request again" — and nothing runs.

A tile API call's body is not read by xbind: it is shown and sent byte for
byte, as before.

## Who's affected

Delegation is held only by the sealed components in xbind's own table —
in a release build, xbin's workspace agent — and, on an xbind run with
`--no-auth --dev`, its owner's dev delegate (`?user=`). So the people
affected are:

- a build whose sealed table holds a component of its own that acts for
  people through the SDK (`xbin.AsPerson`, `RequestApproval`,
  `ProposeChanges`, `ProposePlan`) and writes its own bodies or line
  arguments — a hand-written JSON body, or a Go struct marshalled without
  `json` tags (`{"From": …}`), or with a field the call doesn't take;
- a script that drives a dev xbind's delegate with such bodies.

**Not affected:** xbin's own workspace agent, shell, web chrome, iPhone
app and `bx` of v0.3.80–v0.3.84 — the workspace agent writes start, new
tile, message and page bodies with fixed lower-case keys, and the shell,
the app and `bx` file no delegated requests (they are the person). One
behaviour of xbin's own agent changes: a plan line whose arguments the
model wrote in another case (`{"From": …}`) is now refused naming the key,
and the agent proposes it again.

## How to migrate

Write each body and each line's `args` with exactly the documented keys
(`docs/protocol/delegation.md`, `docs/protocol/*.md` for the call itself),
in lower case as documented, each once, nothing extra. In Go, give every
field a `json:"…"` tag. File a request that was pending across the upgrade
again.

## Why

encoding/json matches keys without regard to case and keeps the last of
two equal ones; a reader of the body's keys as they are written does
neither. v0.3.80–v0.3.84's approval sheets (the web's and the app's) read
a start's brief by its keys, so a start filed as `{"cwd": …, "kind":
"agent", "message": "Tidy README", "Prompt": "…"}` showed "Tidy README" and
the session was told the other text. One reading only — and the sheets now
showing xbind's reading — makes what a person approves what runs.
