# 2026-09-26 — agent session events reach people, not tiles (D97)

## What changed

`term` and `session` events on `/ws/events` — the terminal and agent
session directory (open, close, rename, and the new `status`) and every
agent session's event stream (messages, tool calls and their output,
permission requests, patches) — now reach only:

- the session's **owner**, on their own browser or app sessions (and the
  bootstrap owner's cookie or token for the owner's sessions);
- **admins**;
- a shell's **terminal token**, for sessions on that terminal's own tile
  (what `bx agent` in that tile needs).

They never reach a tile's **frame token**, a backend's **instance token**,
or a cron or bus principal. Before, a tile's frontend received the
transcripts of the user driving it, and every tile backend — whose instance
token names no user, which read as "owner" — received the owner's.

Also: an agent session's own sandbox token (the `XBIN_TOKEN` inside the
agent's sandbox) gets **403** on `prompt`, `permissions`, `elicitations`,
`options`, `restart` and `diff` of **any** agent session — its own and
every other — and on `POST /term/sessions`, so an agent can't approve its
own permission requests or change its own settings, and can't do it
through a sibling either (opening one in a bypass mode, or using one
already open on the tile, and having the two answer each other). A shell
terminal of the same user on the same tile still drives the sessions.

## Who's affected

- A tile (frontend or backend) that subscribed to `/ws/events` and read
  `term` or `session` events. No tile or template in this repository does;
  the shell and the app subscribe as the user.
- A script run *inside an agent session's sandbox* that opened or drove
  agent sessions through the API (for example an agent calling `bx agent
  permit` on itself, or `bx agent run` to start a helper agent).

## How to migrate

- A tile that needs to know about agent sessions should ask the person:
  open the Agent tab or use `bx agent` from a terminal on the tile. There is
  no supported way for a tile to follow a user's sessions — that was the
  leak.
- Drive and open agent sessions from a shell terminal on the tile (its
  token is not an agent's), not from inside an agent.

## Why

Default-deny for element principals ([auth.md](/docs/auth.md)): a tile acts
for the people who use it, not as them. A frame token carries the driving user's
id, so the old owner-only filter handed every tile a user opened that
user's agent transcripts — tool output, file contents, patches — whether or
not the tile could read the files involved. The status events added for the
app's inbox (D97) would have widened it.
