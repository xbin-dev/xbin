# apps/expenses API

Each person's own expense book. The tile is partitioned (`"partition":
["user"]`, /docs/partitions.md): a person's calls reach their own backend
instance and their own `data`, so the routes below only ever touch the
caller's book. A call with no person behind it (another tile, the workspace
token) is refused, as is an admin viewing the workspace as someone.

| Method & path | Purpose |
|---|---|
| `GET /expenses` | `{name, items, totals: {draft, submitted, approved, reimbursed}, categories, rate}` |
| `POST /expenses` | `{date, merchant, category, amount \| miles, note?, receipt?, trip?}` → a draft |
| `POST /submit` | `{ids?}` → drafts (all, or those) become submitted |
| `DELETE /expenses/{id}` | a draft |
| `POST /import` | `{name, items}`: replace your own book (the demo seed signs in as each person) |

Mileage is paid at $0.70 a mile.
