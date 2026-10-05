# Work-package records (projects-scm)

> Status: live — the record every work package of plans/projects-scm.md
> leaves at `plans/projects-scm/records/<WP>.md` (S0, G1, P1, K, U1, G2, E,
> C, P2, T, V, U2; LAND for the landing). The integrator builds
> docs/changelog.md and plans/DECISIONS.md from them; reviewers check the
> work against them and the spec.

Copy the template below. Keep it factual and short: what a reviewer, the
integrator and the next builder need, nothing more. Cite the spec as
"projects-scm §x.y". Never write "PR" followed by a number (write "PR #12"),
and never cite a D-number the repo doesn't define yet ("the decision taken
at merge").

---

```markdown
# <WP> — <title>

> Status: live — branch `wp/ps-<id>` (from `projects-scm` at <sha>).

## What was built

- <deliverable> — <files> (projects-scm §x.y)
- …

## Seams for others

What another WP can now call, register or rely on — names, signatures,
routes, shapes, events — and anything that differs from §14.

## Changelog text

The paragraph (or bullets) the integrator folds into docs/changelog.md's
entry, in its voice: what a builder or a person using the tile sees.

## Decision text

What belongs in the program's decision entry (projects-scm §17.3): the
choices this WP made, the alternatives not chosen and why; owner rulings
it relied on.

## Deviations

Each place the work differs from the spec, one line with the reason —
also added, dated, to projects-scm §19. "None" if none.

## Tests run / not run

| Command | Result |
|---|---|
| `go test ./internal/docscheck` | ok / … |
| … | … |

Not run, and why (this machine: no sudo, docker or rootfs; no GitHub;
see projects-scm §15.4): …

## Merge risks

Files edited outside the WP's own (projects-scm §16.2–§16.3), hunks that
may conflict, ordering needs, migrations, anything the integrator must
watch.

## Commits

| Commit | Subject |
|---|---|
| `abc1234` | area: subject |

## Owner questions

Questions only the owner can answer, each with the default this WP built
meanwhile. "None" if none.
```
