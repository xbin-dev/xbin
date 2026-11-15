# Registering `xbin/wsagent` (decision text for D231 / D227)

Written 2026-11-15 by the sealed-components package for the coordinator to
place in `plans/DECISIONS.md` (the log is at its size guard and is being
split). It extends D231 (the workspace agent) on D227 (sealed components).

- **The row.** `xbin/wsagent` is the release's first sealed component: the
  embedded `builtin-templates/agent` tree (`Authored: apps/agent`,
  `Template`), `Env: XBIN_AGENT_MODE=workspace`, `Notify:
  /delegate/notify`, `Capabilities{Delegate, Agent}`, the `llm` slot (the
  template's slot of service `openai`) with `Defaults` and `Providers`
  both `xbin:llm`, `FrameReads: /api/xbin/agents/connection`, the
  `cap:open-links` grant (its transcript opens links in a new tab), data
  format 1. `TestTheReleasesTable` pins it.
- **`Partition: ["user"]` — no shared instance.** A person's agent is
  their partition and nothing of it is anyone else's; the template's
  shared instance (managers, the tile-wide settings it mirrors into
  `conf`, team conversations) has no meaning for it. Chosen over `["user",
  "global"]`: a shared instance would be one more backend holding
  delegation for nobody. The mode of a released row never changes, so
  this is the one to get right. Consequence, fixed in the template: a
  partition read its halt switch from `conf`, which only a shared instance
  writes, and parked every run for good; a workspace agent reads no
  `conf` — its settings are its own db's and its brake is xbind's (the
  person's pause, the workspace's switch).
- **Nobody has a sealed component until it is given to them.** An access
  entry reaches a sealed path only by naming it (a person's entries, an
  org's shares, the workspace defaults); `*` and every other pattern were
  written about tiles and give nothing there. Admins read it, as they
  manage it. This replaces D227's "a pattern that reaches it is clamped to
  read": with that rule a workspace whose people hold `*` would have given
  everyone an agent the moment the switch went on, and the third setup
  step would have decided nothing. It holds for every sealed component.
- **Three gates, each xbind's.** (a) the LLM connector: the slot's default
  binding applies only while the connector is configured, and a session's
  handle needs a model; (b) the delegation switch, off by default: every
  delegate call is refused while it is off; (c) access. A workspace that
  does nothing sees nothing new: no row in a plain listing, no chip, no
  notice, nothing built or started (`TestWorkspaceAgentIsInertUntilSetUp`).
- **Who gets one is access, and the chrome's third step is one call.**
  `PUT /delegate/workspace {who: everyone | me | chosen}` writes the
  workspace-defaults entry (everyone), the admin's own entry (me) or
  removes the defaults' (chosen); one person or org at a time stays an
  ordinary access entry. No new store, no second notion of membership.
  `GET /delegate/state` answers `access`, `agent` only with access, `held`
  (`older` | `locked` | `unavailable`, `heldWhy` for an admin) and, for a
  workspace admin, `who {everyone, entries, chosen}`; the setup state stays
  for an admin until someone was chosen. The notice that names
  `/xbin/agent` goes only to people who have an agent.
- **A row may ask for limits below a tile's (`Spec.Limits`), and the
  agent's does.** Memory 512 MiB for each person's backend (a tile: 2
  GiB): measured on an install-like xbind, 57–63 MiB idle and 74 MiB
  after a 216-tool-call conversation and a compaction, so the cap is
  about seven times its heaviest measured use, and a quarter of what the
  32 partitions that may run at once could otherwise claim. Idle stop
  three minutes (a partition: ten): a stopped partition answers again in
  0.1 s, 0.4 s when its volumes mount anew, so stopping early costs a
  person nothing they notice. The running cap is left at the default. The
  memory cap is where an admin's raise starts (D203), not a ceiling.
- **The partition mode is fixed for the life of the entry.** The data
  stamp records the mode the data was made in; a table that later gives
  the component another mode holds it (`partition-mode`), touches nothing
  and registers it in the mode its data has, so nothing asks for the
  switch that would delete it.
- **The build.** `make build` / `make sealed` / the release scripts build
  `bin/sealed/wsagent-<digest>` with the agent template's own `go.sum`
  (`-mod=readonly`): they need its modules in the module cache or a
  `GOPROXY`, as `make tile-check` already does. `make check` builds no
  sealed backend and stays offline. linux/amd64 44.8 MB, linux/arm64
  41.9 MB; 16.4 MB and 15.1 MB of a release bundle (zstd -6).
