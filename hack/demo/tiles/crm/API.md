# apps/crm API

Larkspan's CRM: accounts (customers and prospects), their contacts, the
deals in the pipeline and the activity logged against them.

## Roles

| Role   | Grants |
|--------|--------|
| reader | every `GET` route and the MCP tools |
| writer | `PATCH /deals/{id}` and `POST /activity` (implies reader) |

The tile's own page acts as the tile: people with write access to the tile
change deals there, everyone who can open it reads.

## Endpoints

| Method & path | Purpose |
|---|---|
| `GET /summary` | `{pipeline, weighted, openDeals, won30, winRate, arr, customers, atRisk, renewing90, byStage}` |
| `GET /accounts?q=&status=` | accounts with `openDeals`, `openAmount`, `contacts`, `lastActivity` |
| `GET /accounts/{id}` | `{account, contacts, deals, activity}` |
| `GET /deals?stage=&account=&owner=` | `{stages, deals}`; stages: lead, qualified, demo, proposal, negotiation, won, lost |
| `PATCH /deals/{id}` | `{stage?, next?, amount?, probability?}`; a stage change is logged as activity |
| `GET /contacts?q=` | contacts with their account's name |
| `GET /activity?account=&limit=` | newest first |
| `POST /activity` | `{account, type: call\|email\|meeting\|note, text, deal?}` |
| `GET /team` | the people deals and activity refer to |
| `GET /me` | `{user, name, canWrite}` for the page |
| `POST /mcp` | MCP over streamable HTTP: `search_accounts`, `get_account`, `list_deals`, `pipeline_summary` |
| `POST /import` | the workspace owner only: replace all data (the demo seed) |

## Use it

```jsonc
// caller's xbin.json
{ "uses": [{ "target": "apps/crm", "role": "reader" }] }
```

```js
const { accounts } = await (await xbin.fetch('/api/apps/crm/accounts?status=customer')).json();
```
