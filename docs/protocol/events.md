# Protocol: the event stream

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: `/ws/events` — the event types, who receives which, and
the filters. An agent session's events are in
[agents.md](/docs/protocol/agents.md).

## `/ws/events` — event stream


```
GET    /ws/events?frame=<token>    WebSocket upgrade → the event stream (below;
                                   ?frame= only for elements — humans use the cookie)
```

Auth: cookie (owner), a bearer session (`Authorization: Bearer` — the xbin
app's own socket, with its device session: the human's events, never handed
to a tile; D99) or `?frame=<frame-token>` (element; standalone — no cookie
required). JSON text frames:

```jsonc
{"type":"reload","component":"apps/thing"}          // source changed
{"type":"build-start","component":"apps/thing"}     // the build; with text ("setting up environment…") its environment layer's setup first
{"type":"backend-starting","component":"apps/thing"} // D196: built (or cached), its backend spawned and waited for until healthy; build-ok or build-error follows
{"type":"build-error","component":"apps/thing","text":"compiler output"}
{"type":"build-ok","component":"apps/thing"}
{"type":"grants"}                                    // grant table changed
{"type":"branding"}                                  // the workspace title/icon changed (D76): re-read GET /branding
{"type":"native"}                                    // the native-runtime switch changed (D101): re-read whoami (native.runtime)
{"type":"policies"}                                  // a partitioned tiles' switch changed (PD-55; kept for clients before D180, beside workspace-settings): re-read GET /workspace-policies
{"type":"limits","component":"apps/x"}               // a tile's limits changed — a decision, a cap hit, a new manifest ask (D203): re-read GET /limits (and /alerts)
{"type":"partitions","component":"apps/x","partition":"user:<id>", // to that person's own sockets (never apps/x's frames,
 "data":{"op":"consent-needed","from":"apps/z","to":"apps/x"}}     //   terminals or instance): apps/z's call into their apps/x data was refused (partitionConsent on; once a day)
{"type":"partitions","component":"apps/x","partition":"user:<id>", // to that person's own sockets, likewise: their consent changed
 "data":{"op":"consent","from":"apps/z","to":"apps/x","allowed":true}} //   (POST/DELETE /partitions/consents)
{"type":"partitions","component":"apps/x","partition":"user:<id>", // to that person's sockets and apps/x's credentials acting in their partition:
 "data":{"op":"state","partition":"user:<id>","event":"build-ok"}} //   their instance's build-start|backend-starting|build-ok|build-error|reload (text?: the detail)
{"type":"partitions","component":"apps/x",                         // to apps/x's readers (and admins, and apps/x's own credentials):
 "data":{"op":"mode","tile":"apps/x","state":"pending","spec":{"user":false,"global":false},
         "request":{"spec":{"user":true,"global":true},"since":"…","declined":false}}} // its mode changed; re-read GET /partitions?tile=
{"type":"partitions","component":"apps/x",                         // to the person's own session, app or device only:
 "data":{"op":"notice","notice":{"id":"…","at":"…","kind":"partition-deleted|partition-reset|credential","tile":"apps/x","text":"…","hold":"…"}}}
{"type":"app-tunnel"}                                // the app tunnel's settings, its sidecar's state or a pairing changed (D204): an admin console re-reads GET /app-tunnel, a pairing dialog its GET /devices/pairing/<id>
{"type":"workspace-settings","data":{"baseAutoUpdate":false,"changed":["baseAutoUpdate"]}} // an admin changed workspace settings (D175, D180): base auto-update as it applies and the keys the write set (every socket hears it: no partitioned tiles' switch rides it — re-read GET /workspace-settings); a terminal window re-reads GET /ws/term/env
{"type":"bus","topic":"res:<scope>/<name>/<topic>","data":…}
{"type":"status","component":"apps/thing",           // a tile reported its condition
 "data":{"level":"error","message":"…","ts":1785…,"transient":false}}
{"type":"term","component":"apps/thing",             // a terminal session of yours was
 "data":{"op":"open|close|rename","id":"…","user":"…"}} // opened/ended/renamed (D73): re-list
{"type":"term","component":"apps/thing",             // an agent session of yours changed state:
 "data":{"op":"status","id":"…","user":"…","status":"waiting_permission",
         "pending":1,"questions":0,"turn":3}}        // its summary, inline — no re-list needed
{"type":"session","topic":"session.<id>","component":"apps/thing", // an agent session event (D74):
 "data":{"seq":7,"ts":1789…,"type":"message.delta","data":{…},"user":"…","id":"<id>"}}
{"type":"deployments","component":"apps/thing",      // the tile's deployment record changed (paused,
 "data":{"op":"record","seq":19,"by":"user:ana",     //   resumed, …): what names the fields
         "what":["liveReload","deployments"]}}
{"type":"deployments","component":"apps/thing",      // a deploy attempt's result or phase
 "data":{"op":"deploy","id":43,"deployment":"main","how":"reload-now","checkpoint":"c:3f2a1c9",
         "result":"running","phase":"build","by":"user:ana","session":"<id>"}}
{"type":"deployments","component":"apps/thing",      // while live reload is paused: files the work
 "data":{"op":"work-tree","changed":3}}               //   tree differs in from the checkpoint
{"type":"deployments","component":"apps/thing",      // the work tree's branch left (or came back to)
 "data":{"op":"branch","deployment":"dev","assigned":"feature", //   the one deployment requires (D131):
         "workTree":"release","related":"qa","paused":true}} //   related is assigned workTree's
{"type":"deployments","component":"apps/thing",      // a non-primary deployment's frames reload once
 "data":{"op":"reload","deployment":"dev"}}
{"type":"deployments","component":"apps/thing",      // a non-primary build of the work tree or restart;
 "data":{"op":"build","deployment":"dev","phase":"error","text":"compiler output"}}  // phase: start | starting (D196: built, its backend starting) | error | ok
{"type":"deployments","component":"apps/thing",      // a seed, reset or restore moved
 "data":{"op":"data","deployment":"dev","busy":"","state":"seeded"}}
{"type":"deployments","component":"apps/thing",      // a non-primary deployment reported its condition
 "data":{"op":"status","deployment":"dev","level":"error","message":"…","ts":1790…,"transient":false}}
{"type":"deployments","component":"apps/thing",      // a notification it held instead of pushing
 "data":{"op":"notify","deployment":"dev","to":"user:bob","title":"…","at":"…"}}
{"type":"prefs","component":"root",                  // one of YOUR pref buckets changed:
 "data":{"key":"layout","writer":"…"}}               // re-read GET /prefs/<key> if you care
```

**Tile deployments** ([tile-deployments.md](/docs/tile-deployments.md)).
Today's types speak only of a tile's primary, whichever deployment it is,
with the bare path; no event ever carries a qualified `component`, and
everything about another deployment rides the `deployments` type, which
names it in `data.deployment`. A save in a tile whose live reload is paused
publishes no `reload` and rebuilds nothing; op `work-tree` reports, at most
once every 2 s per tile and only when the number changed, how many files the
work tree differs in from the checkpoint live reload was paused at (the
state's `workTree.changed`). A save while live reload follows a non-primary
deployment sends op `reload` and op `build` phases for it, and nothing of
today's types. A deploy of a checkpoint — reload now, deploy, promote, roll
back, and the pin that pausing, attaching elsewhere, protecting or
reassigning takes — rides op `deploy` on each phase (`checkpoint`,
`materialize`, `build`, `start`, `swap`) and result (`queued`, `running`,
`ok`, `failed`, `cancelled`), never `build-*`, and never carries compiler
output (op `build`'s `text` does, for a non-primary build, as `build-error`
does for the primary). After a swap that changed the code a deployment
serves, the primary gets one bare `reload`, another deployment op `reload`.
A resume or attach onto the primary builds the work tree with today's
`build-*`; a restart of a non-primary deployment's code sends op `build`. A
reassignment of the primary sends op `record` and one bare `reload`, since the
bare URL now serves other code; a lifecycle change sends the bare `reload` and
op `reload` for each non-primary deployment. `session` names the terminal or
agent session that acted, so its own window can skip the line it would
print. A tile's status also clears when a deploy swaps the code its primary
runs (a deploy emits no `build-start`); a failed deploy leaves it. A
non-primary deployment's tile-report and held notifications ride ops `status`
and `notify`, never `status` or a push. Op `branch` (`branches/1`): a tile
with an assigned branch saw the work tree's branch change — `deployment` is
the one live reload follows (or last followed), `assigned` its branch (`""`
for none), `workTree` the work tree's now (`""` for none), `related` the
deployment assigned `workTree` (`""` for none), and `paused` whether this
save paused live reload on `deployment` (it deployed nothing). A client
offers to follow: attach live reload to (or resume it on) `related`, resume
it on `deployment` once `workTree` is its branch again, or else keep
`deployment` on `workTree` this time (`confirm: "other-branch"`) or add a
deployment for it. Old clients ignore the unknown type.

`prefs` events are per-user and not even admins see another user's: the
bucket's owner's human sessions (browsers, the app) get the events of all
their buckets — `component` names the bucket (`root` = the shell's) — and a
tile principal (frame token, terminal, backend) only its own bucket's, the
reach `GET /prefs` gives it. `data.writer` is the `X-Prefs-Writer` header of
the write when it had one (absent otherwise). The shell skips its own writes
by that id and reloads its layout when another client (the app) wrote it.
A write to a tile deployment's bucket beyond `main` (above) is heard only by
that deployment's own principals, those whose `GET /prefs` reads the bucket:
never by main's frames or the user's human sessions, since today's types
don't speak of another deployment. Main's bucket's events never reach
another deployment's principals. The event is the same, with the bare tile
path.

Non-bus events go to every subscriber, except `prefs` (above) and `term` and `session` events
(D97), which reach the session's owner (`data.user`) — their signed-in browsers,
and a shell's terminal token for the sessions on its own tile — and admins;
never a tile (its frame token names the user it runs for, its backend's
token no one: neither follows anybody's sessions) — re-list `GET
/term/sessions` on a `term` one; the id and op are enough to update a tab
bar in place. An agent session also sends `term` op `status` whenever its
summary changes — `status` (starting \| idle \| running \|
waiting_permission \| cancelling \| error \| exited), `pending`
(unanswered permission requests), `questions` (unanswered elicitations),
`turn` (prompts taken, so a turn that ran and finished between two
summaries still shows as a change) — coalesced over a few tens of
milliseconds and never repeated; the last one precedes the `close`. It is
what an inbox follows ("waiting for you", "done") without following each
session's log; `GET /term/sessions/<id>` has the requests themselves. Older
clients that re-list on every `term` event keep working (one more re-list
per change). `bus` events
are delivered only to
the owner and to elements holding a reader grant on the resource.
A `bus` event of a deployment's data beyond `main` carries `deployment`
(`{"type":"bus","topic":…,"deployment":"dev","data":…}`) and reaches admins
and the credentials bound to that deployment's data only — never a
primary-bound principal of the same scope. `term` events (open, rename) of a
session with a named target carry `deployment` in `data`, and those of a
guided sign-in's shell carry `purpose: "signin"` (`/ws/term`, above).
`deployments` events are filtered by the tile and the deployment they name,
at your current level on each delivery. A fact about the tile's primary
reaches everyone who may read the tile: ops `record` and `deploy` (onto the
primary) in their full form only to its write audience (admins, people with
write, the tile's own terminal and agent sessions while their user has
write), and to a non-primary deployment's own frame and backend tokens when
the deploy came `from` it; in a reader form everyone else (`record` with only
the reader view's fields in `what`, sent only when one changed; `deploy`
without `id`, `how`, `from` and `session`). Ops `work-tree` and `branch`
reach the write audience. Anything naming another deployment — ops `deploy`, `reload`,
`build`, `data`, `status` and `notify` about it — reaches only the write
audience and that deployment's own frame and backend tokens: never the
primary's frame token (minted for readers too), never another tile. `status`
events broadcast like the build events (the shell renders each only for tiles
it shows; the `GET /tile-report` snapshot below is read-filtered per caller).
Slow consumers are disconnected; reconnect with backoff (the bundled clients
do).

**Partitioned tiles** ([partitions.md](/docs/partitions.md)). An event that
belongs to a person's partition carries `"partition":"user:<id>"` — a `bus`
event in that partition's data, that partition's `status`, runner and
partitions events — and reaches only that partition: the person's own
sockets and the tile's credentials acting in it, never admins' (there is no
blanket admin pass for them), another person, another tile or view-as. A
`bus` one still needs the reader grant. A `bus` event on a partitioned
scope's own (not shared) bus that the global instance published carries
`"partition":"global"` and likewise reaches only subscribers acting in the
global instance there, with no admin pass. `term` and `session` events of
a partitioned tile reach only the session's person (their browsers, and
their terminal on the tile): admins don't receive them there. Events
without `partition` — every event of a tile that isn't partitioned, a
shared bus's, and the global instance's others — are delivered as above;
a partitioned scope's shared bus reaches another tile's credential acting
in a person's partition only while that person can read the scope.
