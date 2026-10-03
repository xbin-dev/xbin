# The demo film set

A workspace dressed as a real company using xbin — for promo footage and
website screenshots. **Larkspan** (route planning and live dispatch for
regional delivery fleets; fictional, `larkspan.example`) has thirteen people
in four teams, its branding in place of xbin's, and the apps a company like
it runs, every one of them filled with frozen, believable content: a CRM, a
nightly ops report, live metrics, a team calendar and inbox, an AI agent
with conversations, automations and a team-chat channel, an expenses app
with a partition per person, and an app the agent built.

Nothing here calls a model at seed time: the agent's and the chat's answers
come from `hack/fakeopenai` playing `data/lark-script.json`, through the real
LLM gateway, MCP tools and agent engine — so every tool card and number in
an answer is what the CRM or the ops report really returned.

## Run it

**On its own** (a fresh workspace, built from this checkout):

```sh
hack/demo/up.sh                 # PORT=9400 by default; http://127.0.0.1:9400
hack/demo/up.sh --isolate       # real tile sandboxes, people's partitions, VMs
PORT=9400 hack/demo/reset.sh    # between takes: back to as-seeded, in seconds
PORT=9400 hack/demo/up.sh --stop
```

`up.sh` builds `bin/xbind`, `bin/bx` and `bin/fakeopenai`, makes a fresh
workspace at `.demo/$PORT/ws` (a btrfs subvolume where it can), starts the
scripted model and xbind (`--dev`), seeds it (`seed.sh`, log in
`.demo/$PORT/seed.log`, about 65 s; 75 s under `--isolate`) and takes the
snapshot `reset.sh` restores. `--isolate` loads the machine
settings `hack/dev-setup.sh` wrote (`.dev.mk`: the rootfs, fuse-overlayfs,
gocryptfs, the VM assets) — from the main checkout when run in a git
worktree. Both modes need gocryptfs (`make gocryptfs`, or `XBIN_GOCRYPTFS`):
the demo's tiles keep their data in encrypted resources. Where the shell
can't mount FUSE (a user namespace that maps only your uid, where
`fusermount3` has no root to become), `up.sh` and `reset.sh` run xbind in a
user namespace of their own (`DEMO_USERNS=0` or `1` overrides the check).

Under `--isolate` Lark and the expenses app keep a partition per person
(the `yours` chip), and the tiles Lark calls — the CRM, the ops report, the
coding sandboxes — run a checkpoint rather than their live work trees
(live reload paused): code their teams can change would otherwise run on
everyone's calls, which xbind flags on every screen (docs/partitions.md
§Trust warnings). To change one on camera, resume its live reload.

Put the set on a disk with more than 10% free (`DEMO_DIR=…`): below that,
xbind shows "Workspace disk low" across the top of every screen. `up.sh`
warns.

**In the UI harness** (the stills; `hack/ui-harness/README` for the rest):

```sh
HARNESS_SEED=demo PORT=9301 HARNESS_DIR=/tmp/film/h DEMO_STILLS=/tmp/film/stills \
  PLAYWRIGHT_DIR=… hack/ui-harness/run.sh --keep
```

`HARNESS_SEED=demo` runs `seed.sh` instead of the test seed, starts
fakeopenai with the demo script, and runs the `demoStills` pass instead of
the test passes (`stills.js`): it signs in as a regular person and as the
admin, walks their seeded screens and saves 1440×900 frames at device scale
2 to `$DEMO_STILLS` (default `$HARNESS_DIR/out/stills`). The harness has no
`--isolate`-only parts here unless `HARNESS_ISOLATE=1` — with it, people's
partitions and the VM coding sandboxes are real (on this machine, run it in
`unshare --user --map-root-user --mount` for FUSE, as `up.sh` does itself).

**The website's stills**: `hack/demo/cam/site-stills.sh` films the set's
subjects (the canvas, an app changed live, the agent at work, a terminal,
the coding sandboxes, network approvals, the admin console, a person's
partitions, the phone) on a desk and a phone, with a `shots.json` of what
each frame shows (`hack/demo/cam/README.md` §Website stills).

**Sign in** as `tomas` (Tomás Reyes, CTO, a workspace admin) or `priya`
(Priya Raman, customer success); everyone in `company.json` has the password
`larkspan-demo`. xbind `--dev`'s own `admin`/`admin` account is removed at
the end of seeding (`DEMO_KEEP_ADMIN=1` keeps it); the owner token in
`ws/.xbin/token` still works.

**Real models** later: `DEMO_LLM=real` with `ANTHROPIC_API_KEY` and/or
`OPENAI_API_KEY` in the environment of `up.sh` (or the harness). The seed
still plays its conversations through the script, then points the gateway
at the providers' OpenAI-compatible endpoints under the same model names the
agent and the chat use (`assistant-large` → `claude-opus-5` or `gpt-5`,
`assistant-small` → `claude-haiku-4-5` or `gpt-5-mini`; `DEMO_MODEL_LARGE`,
`DEMO_MODEL_SMALL` change them). The keys go into the gateway's vault
through its API, never into a file.

## Resetting between takes

`reset.sh` stops xbind, swaps the workspace for the snapshot `up.sh` took
right after seeding, and starts xbind again exactly as it was running (its
command line, directory and environment — less anything secret-shaped —
kept in `ws.snap.meta/`). It works for the harness's set too:
`hack/demo/reset.sh --snapshot "$HARNESS_DIR/ws"` once, then
`hack/demo/reset.sh "$HARNESS_DIR/ws"`.

| Where the workspace is | Snapshot | Restore, measured (xbind up / the apps answering) |
|---|---|---|
| btrfs, a subvolume (`up.sh`'s default `.demo/`) | `btrfs subvolume snapshot` | 2.3 s / 2.9 s |
| a filesystem with reflinks (btrfs, xfs) | `cp --reflink=always` | the copy 0.2 s, then the same restart: about 3 s |
| tmpfs, anything else | `cp -a` | 2.5 s / 3.0 s (the harness's set in `/tmp`; the copy 0.6 s); under `--isolate` 5.0 s / 5.6 s |

(A seeded set is about 370 MB in 6,000 files.) Nearly all of a restore is
xbind stopping and starting — under `--isolate`, its sandboxes too. Sign in
again after one: xbind's sessions don't outlive it.

The set is dated: relative times ("18 min ago", "last night's" report) are
laid down relative to when it was seeded, in working hours (a set seeded at
night reads like that day's late afternoon, on a weekend like Friday's), and
the calendar around the demo's day: today on a weekday, the coming Monday on
a weekend (the week the calendar opens on). That day's 10:00 is the
Brightwell renewal call Lark preps Priya for; the CRM's next steps, the inbox
and Lark's answers name the days around it ("Demo with Rita on Tuesday").
Seed on the shooting day; `reset.sh` warns when its snapshot is older. Times
of day ("15:10", the 05:30 report) are in the seeding machine's time zone
(`DEMO_TZ` overrides it): film with a browser in the same zone. Larkspan is
American: seeding and filming with `TZ` set to a US zone (the first stills
used `America/Los_Angeles`) keeps its "today" and the zone the pages name
(a partition's "last started … PDT") true to it — and a European evening
is still a working afternoon there.

## What's in it

| Tile | What it is | Built from |
|---|---|---|
| `apps/crm` | pipeline board (drag deals between stages), accounts, activity, an account drawer; MCP tools for the agent | `tiles/crm`, `data/crm.json` |
| `apps/ops-report` | the nightly ops report: a cron job at 05:30 renders the night (14 nights of history); `report/published` on its bus; MCP tools | `tiles/ops-report`, `data/ops-report.json` |
| `apps/metrics` | builtin prometheus-viewer on two real sources: the LLM gateway's counters and `apps/host-exporter` (this machine's load, memory, CPU, network, disk from /proc) | builtin, `tiles/host-exporter` |
| `apps/calendar`, `apps/email` | `examples/calendar` and `examples/email` (their backends as they are) with a week-view page and a kv-backed team inbox | `tiles/calendar`, `tiles/email`, `data/calendar.json`, `data/email.json` |
| `apps/chat` | builtin chat on the gateway, with the CRM's and the ops report's MCP tools | builtin |
| `apps/lark` | the agent (builtin template): Larkspan's system prompt, the CRM and ops report bound as MCP servers, five conversations (Maya's pipeline question among them), two schedules (a morning ops brief, a Friday pipeline digest), the team-chat channel claimed with `#sales`/`#ops` trusted. Under `--isolate` partitioned: each person's conversations and schedules are their own, the channel's at its global instance | builtin template, `data/lark-script.json` |
| `apps/llm-gw` | the gateway: one backend `inference` (the scripted model) or the real providers; preferred models per use | builtin |
| `apps/team-chat` | agent-messaging-bridge; its console stands in for a chat platform; its egress goes through the egress approver | builtin template |
| `apps/coding-sandbox` | sandbox manager bound to Lark: xbind's runtime under `--isolate` (VM tile sandboxes switched on where xbind runs VMs), else the template's own test backend (host directories), named `local`; three engineers' sandboxes, two running | builtin template |
| `apps/egress-approver`, `apps/traefik` | builtins, wired (traefik needs `--isolate` to fetch its binary); the approver is the team chat's and the telematics tile's way out | builtin |
| `apps/telematics` | Operations' newest tile: the telematics and weather feeds dispatch will read; its `net` goes through the egress approver (the `approved-egress` network set lets Operations' tiles bind there). It makes no network calls | `tiles/telematics`, `data/telematics.json` |
| `apps/expenses` | each person's own book — `"partition": ["user"]` under `--isolate` (one instance, books keyed by person, without it); Daniel's and Priya's are filled | `tiles/expenses`, `data/expenses.json` |
| `apps/onboarding` | the app Lark built from Priya's brief: no backend, kv state, CRM facts through a grant; its git history is Lark's commits | `tiles/onboarding`, `data/onboarding.json` |

People and teams (`company.json`): Leadership, Engineering, Sales &
Success, Operations, each with its tiles and network sets (`internet`,
`office-lan`, `routing-apis`, `depot-iot`, `approved-egress`). Each person
signs in to their own screens (`data/layouts.json`) at a 17 px font (the
shell's settings pref), or their own size there (Maya's four-app Company
screen and Lukas's sandboxes at 15 px).

## Changing it

- Content lives in `data/` (JSON); the tiles in `tiles/` (plain ES modules
  and lit pages, node backends; `tiles/_lib/` is copied into each).
- Every relative date in the fixtures is `{daysAgo, at}`, `{minutesAgo}`,
  `{inDays}` or a day offset, resolved when seeding (a day that lands on a
  weekend moves to the Monday after or the Friday before). Text names a day
  relative to the demo's day, in working days: `{{weekday:+1}}` "Tuesday",
  `{{date:+4}}` "Oct 9", `{{longdate:+4}}` "October 9", `{{nth:+4}}` "9th",
  `{{iso:-2}}` (the calendar's dates) — in `data/*.json` and in the model's
  script alike.
- A new conversation: a `lark_ask` line in `seed.sh` and a reply in
  `data/lark-script.json` (`match` words, `steps` with tool calls, `{{…}}`
  placeholders filled from the tool results; `hack/fakeopenai/demo.go`).
- Footage that types new questions to Lark or the chat gets the script's
  replies when they match, else its fallback — or real models
  (`DEMO_LLM=real`). A step's `delayMs` paces a reply like a model
  thinking: the website's "agent at work" question (Lakeshore and Alder
  Street) waits 30 s before its answer, long enough to film Lark mid-task.
