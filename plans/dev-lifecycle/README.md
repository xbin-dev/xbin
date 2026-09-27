# Tile dev lifecycle: pause live reload, tile deployments, promotion

> Status: live — design proposal (2026-09-27), not yet implemented. This set
> of documents is the input to the implementation workflow. P1–P4 are
> ratified. P7, P18, P19 and P22–P24 are owner-confirmed (2026-09-27). The
> rest are proposed (see [16-open-questions.md](16-open-questions.md)).
> D-numbers are assigned only at ratification, because `make test`'s docscheck
> refuses undefined IDs.

## What this is

Today every tile runs under live reload: a save in the tile directory
rebuilds and swaps its backend and reloads every open frame, for every user at
once. That stays the default. This design adds, for tiles that opt in:

- **Pause live reload**, with **reload now**. The tile keeps serving a pinned
  checkpoint while its work tree moves.
- **Tile deployments** (`main`, `dev`, …). Named runtimes of one tile, each
  with its own code pointer, data and vault. Code moves between them by
  **deploy**, **promote** and **roll back**. Exactly one deployment is the
  **primary** and receives everything from outside. Authority (grants,
  bindings, capabilities) stays with the tile.
- **Feeds** for checkpoints: the work tree now; a tracked git branch and a
  push-to-deploy remote later, on the same core.

Tiles that never opt in change in no way at all (P5).

## Reading order

| # | Document | Read it for |
|---|---|---|
| — | [01-glossary.md](01-glossary.md) | The vocabulary. Read it first; every other document uses it verbatim. |
| — | [05-model.md](05-model.md) | The recommended model: objects, operations, routing, authority, invariants P1–P29, worked flows. |
| 1 | [02-goals.md](02-goals.md) | Problem, scenarios, non-goals, success criteria, the zero-change guarantee. |
| 2 | [03-current-state.md](03-current-state.md) | How things work today, with file:line, as the baseline the design changes. |
| 3 | [04-options.md](04-options.md) | The tradeoff surface: every axis, its options, and why the model chose what it chose. |
| 4 | [06-security.md](06-security.md) | Threat model and mitigations. |
| 5 | [07-runtime.md](07-runtime.md) | Runner, watcher, builds, checkpoints, serving, restart paths. |
| 6 | [08-data.md](08-data.md) | Data namespaces, seeding, reset, vault, backup, quotas. |
| 7 | [09-fabric.md](09-fabric.md) | Inbound edges, outbound edge policy, self-calls, dormant registrations. |
| 8 | [10-ux.md](10-ux.md) | Terminal window, shell, `bx`, agent guidance, wording. |
| 9 | [11-contract.md](11-contract.md) | Exact wire: endpoints, events, env, headers, tokens, CLI. |
| 10 | [12-compat.md](12-compat.md) | Compatibility proof obligations, downgrade, fixtures. |
| 11 | [13-surfaces.md](13-surfaces.md) | Every package and file touched, with budget headroom. |
| 12 | [14-implementation.md](14-implementation.md) | Milestones and work packages for the implementation swarm. |
| 13 | [15-test-plan.md](15-test-plan.md) | How each property is verified. |
| 14 | [16-open-questions.md](16-open-questions.md) | Remaining calls and the proposed-decision list. |
| — | [research/](research/README.md) | The evidence base: the codebase maps, prior art from 27 platforms, the terminology census, side findings. |

## Conventions for this set

- **Vocabulary:** [01-glossary.md](01-glossary.md) is normative. The banned
  words (identity, instance, environment, "pause the tile", …) are banned in
  normative text.
- **Baseline:** master, where D112's sandbox registry is landed and D113's
  tile-managed sandboxes are designed ([plans/tile-sandboxes.md](../tile-sandboxes.md);
  [research/sandbox-visibility.md](research/sandbox-visibility.md)).
  Deployments are not built on tile-managed sandboxes; they change the shared
  sandbox mechanics one layer up. cgi no longer exists: its removal is its
  own change, which lands first ([14-implementation.md](14-implementation.md)
  PRE-5).
- **Facts about today** carry `file:line` references and point at the
  research file they came from. Line numbers drift, so re-check them before
  editing code.
- **Decisions:** P-numbers are local to this set. `plans/DECISIONS.md`
  receives D-numbers at ratification. Only D1–D114 exist on this branch. Work
  on other branches (the sandbox-managers contract and agent classes, cgi's
  removal) is described in words, never cited by ID, because docscheck refuses
  undefined IDs.
- **`16-open-questions.md` must exist before this set is committed:**
  `TestRelativeLinksResolve` fails until then on every link to it (this
  README's status note and reading order, and 06-security.md). It is written
  from the triage's proposal list and open questions.
- **Plans aren't embedded** in xbind. Builder docs (`docs/`) are written from
  these documents at implementation time, following the rules in the repo's
  AGENTS.md (a changelog entry, protocol.md rows, a migration note if
  anything breaks, which this design is built to avoid).
