# apps/ops-report API

The nightly ops report: what the routing platform planned for each
customer, how deliveries went, the API's and driver app's health, support
load, incidents and notes. A cron job renders the night that just ended at
05:30 every day; a report is rendered from the imported model with a
variation seeded by its date, so it reads the same whenever it is rendered.

## Roles

| Role   | Grants |
|--------|--------|
| reader | the `GET` routes and the MCP tools |
| writer | `POST /run` (implies reader) |

## Endpoints

| Method & path | Purpose |
|---|---|
| `GET /reports` | `{reports: [{date, status, routes, stops, onTime, apiP95ms, incidents, renderedAt}], runs}`, newest first |
| `GET /reports/{YYYY-MM-DD}` | one night's full report |
| `GET /latest` | the newest report |
| `POST /run?date=` | render a night now (default: the night that ended); the cron target |
| `POST /mcp` | MCP over streamable HTTP: `latest_report`, `get_report`, `list_reports` |
| `POST /import` | the workspace owner only: the report model (the demo seed) |

## Bus topics — `res:apps/ops-report/bus`

| Topic | Payload |
|---|---|
| `report/published` | a report's headline numbers, its notes and incidents |
