# Component environment layers (per-component build-time deps)

Companion to `plans/isolation.md` (sandbox mechanics) and `plans/runtime.md`
(base rootfs). This is how a component declares extra system/runtime dependencies
— "this component needs Ruby / imagemagick / a gem / anything" — without bloating
the shared base rootfs or losing them to the ephemeral overlay.

## Problem

A component backend runs over an overlay whose upper is an **ephemeral tmpfs**,
rebuilt per generation. The base rootfs (Go/Node/Python + tools) is shared and
immutable. So there is no place for a component's own dependencies: `apt install`
in a terminal is per-session and never reaches the backend; extending the base
rootfs is global, not per-component. Agents building a component can't express
"give me Ruby."

## Model — a middle overlay layer

Add one persisted, cached layer between the base rootfs and the runtime scratch:

```
┌─ ephemeral upper (tmpfs, per generation)     runtime scratch, discarded
├─ component env layer (ro lower, cached)   ★  the component's declared deps
└─ base rootfs (ro lower, shared)              go/node/python + tools
```

`Spec.Lower = [envLayer, baseRootfs]` (env wins on conflicts); ephemeral upper on
top. No new sandbox plumbing — `Spec.Lower []string` and a persisted `Spec.Upper`
already exist; the env build just writes to a persisted upper that later runtime
sandboxes consume as a lower. "Docker, but one layer."

## Declaring the environment — `setup`

`xbin.json` gains an optional **`setup`**: a freeform shell script, run once at
build time. Inline for one-liners; a `setup.sh` in the component dir for anything
bigger (the component dir is mounted during the build, so `./setup.sh` works).

```jsonc
{
  "runtime": "cgi",                         // any runtime; env is runtime-agnostic
  "setup": "apt-get update && apt-get install -y --no-install-recommends ruby && gem install --no-document sinatra"
}
```

Freeform, not a structured `{apt:[…]}` schema (CE-1): agents can install from any
source and the "compile X from source / anything else" long tail just works. The
tradeoff is that `setup` is arbitrary code — mitigated by running it fully
sandboxed (below) and by agent guidance on safe supply chains.

## Build — lazy, hash-keyed, a fresh layer per change

- **Key**: `sha256(setup script content + base-rootfs id)`. The layer lives at
  `.xbin/env/<compkey>/<hash>/` (upper + work). Per-component (CE-2); gitignored
  cache, rebuildable, not backed up — same semantics as the rest of `.xbin`.
- **Lazy**: built only when the current hash's layer is missing — i.e. **first
  run or after a `setup` change** (CE-4). A code save that doesn't touch `setup`
  reuses the existing layer (fast); a `setup` change mints a **new** `<hash>`
  directory built clean from the base rootfs — never an in-place re-apply onto the
  old layer (CE-3).
- **Where it runs**: a throwaway sandbox — `Lower:[baseRootfs]`,
  `Upper:.xbin/env/<compkey>/<hash>/upper` (persisted), the component source
  bound read-only, `Net:"relay"` under **`net:internet`** always (CE-5, no
  separate per-import approval), entry `/bin/sh -exc "<setup>"`, run as
  container-root (the range-uid map makes `apt`/`useradd`/etc. work). On success
  the upper *is* the frozen layer; on failure the partial dir is removed and the
  error is surfaced — the backend does not start.
- **Lifecycle reuse**: folded into the runner's existing build single-flight and
  surfaced through the current `build-start` / `build-error` events ("setting up
  environment…"), so the frame overlay shows progress and setup failures inline.
- **GC**: on a successful new build, older `<hash>` dirs for that component are
  removed (keep current). `.xbin` can be blown away and rebuilt at any time.

## Runtime

`runner.sandboxCmd` sets `Lower:[envLayer, baseRootfs]` when a current env layer
exists (else just the base). Overlay whiteouts/opaque dirs produced by the env
build are honored when it's used as a lower, so a `setup` that removes/replaces a
base file behaves. The env layer is stable across backend generations — only a
`setup` change rebuilds it, so hot-reload on code save stays cheap.

## Terminals: their own persistent, resettable dev layer

Terminals do **not** stack the component *env* layer (that changing lower under a
live shell / rw workspace would be surprising). Instead each terminal gets its
**own** per-component overlay upper — a persistent, resettable **dev sandbox**
(CE-6):

- **Persistent** at `.xbin/term/<key>/` (per component; `_root` for a
  workspace-root shell). System-level changes — `apt install`, `/etc` configs,
  toolchains outside the workspace — survive across terminal sessions, so an
  agent's project setup sticks. (Workspace files and `$HOME` already persist via
  their bind mounts; this layer adds the rest.)
- **One live holder per component**: two overlay mounts of the same upperdir
  would corrupt it, so only one live session mounts a component's layer;
  concurrent sessions on the same component fall back to an ephemeral upper.
  Reattaching to an existing session keeps its layer; a xbind restart frees it.
- **Resettable**: `DELETE /ws/term/env?cwd=<path>` wipes the layer back to the
  base rootfs (killing any live holder first), surfaced as a "reset sandbox"
  action in the terminal window.

This is separate from the component env layer: the running backend's environment
comes from `setup`; the terminal's comes from what the agent installs in it. An
agent that wants parity runs the same installs in both (often the terminal is
where it works out the `setup` script in the first place).

## Agent guidance (AGENTS.md)

When implementing, AGENTS.md must teach agents to make `setup` a **safe,
reproducible supply chain** (CE-7):

- **Update first**: start with `apt-get update` (and consider `apt-get upgrade`)
  so installs resolve and pick up security fixes.
- **Pin + verify**: pin versions (`ruby=1:3.2*`, `gem install foo -v X`), prefer
  lockfile-integrity installs (`npm ci`, `pip install --require-hashes`,
  `bundle install` w/ `Gemfile.lock`, `cargo --locked`), and verify checksums or
  signatures for anything fetched directly. Avoid unpinned `curl … | sh`.
- **Prefer distro/registry packages** over ad-hoc downloads; keep `setup` minimal
  and deterministic so the cached layer is stable and auditable.

## Security / trust

`setup` is arbitrary code with `net:internet` at build time. It runs in a **full
sandbox** (own user/mount/pid/ipc/uts/net namespaces, writing only to the env
upper), so blast radius = that sandbox + outbound internet; it cannot touch the
host or other components. Owner-authored components are within the owner's trust
(the owner already runs `apt` in terminals). Per CE-5 there is **no extra
approval gate for imported tiles' `setup`** — an imported tile runs arbitrary
networked build code (sandboxed) without a prompt. Accepted tradeoff; revisit if
tile-sharing hardens (`plans/tile-sharing.md`).

## Decisions (CE-*)

- **CE-1 — Freeform `setup` script**, not a structured package schema. Max
  flexibility ("Ruby or anything"); safety via sandboxing + agent guidance.
- **CE-2 — Per-component cache** at `.xbin/env/<compkey>/<hash>/`.
  Content-addressed cross-component dedup is a later optimization (needs
  refcount/GC).
- **CE-3 — A `setup` change builds a fresh layer** from the base rootfs, never an
  in-place re-apply onto the previous layer.
- **CE-4 — Lazy build**: only on first run or `setup`-hash change; code-only saves
  reuse the layer.
- **CE-5 — Build egress is `net:internet`, always** — no per-import approval.
- **CE-6 — Terminals get their own persistent, resettable per-component layer**
  (`.xbin/term/<key>/`, one live holder each, `DELETE /ws/term/env` to reset) —
  a dev sandbox. They do *not* stack the component env layer.
- **CE-7 — AGENTS.md encourages safe supply-chain installs + system update.**

## Touchpoints (for implementation)

- `internal/registry` — add `Setup` to the manifest; parse from `xbin.json`.
- `internal/runner` — env-layer build step before backend build/start (reuse
  single-flight + events); `Lower` wiring in `sandboxCmd`; GC.
- `internal/sandbox` — none (Lower/Upper already support it).
- `docs/elements.md` + `AGENTS.md` (workspace-template) — document `setup` + the
  supply-chain guidance; `docs/getting-started.md` example.
