# apps/telematics API

The feeds live dispatch will read — van positions from the customers'
telematics providers, weather alerts for the depots' counties — listed in
`feeds.json`. On the film set the tile is new: its `egress` interface (a
`net`) is left
for an admin to bind (the shell's "interfaces to bind"), and it makes no
network calls of its own; the providers' API keys and the sync itself
aren't part of the set.

| Method & path | Purpose |
|---|---|
| `GET /feeds` | `{feeds: [{id, name, what, every}]}` — feeds.json |
