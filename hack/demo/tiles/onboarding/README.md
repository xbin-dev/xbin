# Onboarding tracker

A card per customer we're onboarding — a pilot, a new depot, a driver-app
rollout — with progress toward go-live, the next step, and the checklist.

- **Data:** one kv key per customer, `customer/<CRM account id>`, in this
  tile's `state` resource: `{id, name, title, kind, csm, csmName, kickoff,
  goLive, steps: [{title, due, done, doneAt}]}` (times in unix ms). The page
  reads and writes it directly; there is no backend.
- **CRM:** plan, fleet size, depots and health come from `apps/crm`
  (`GET /api/apps/crm/accounts/<id>`; this tile holds a reader grant).
- **New onboarding:** copy a checklist from `templates.js` into a new key.

Built by Merrow from Priya's brief (BRIEF.md).
