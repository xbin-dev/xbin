# 2026-09-26 — agent template: conversations are per user (D83)

## What changed

The agent template's conversations now belong to the person who started
them. The backend reads who is calling from `X-XBin-User`, which xbind sets
for the tile's own frame and terminals (D29):

- **A new chat is private.** Only its owner sees it until they share it with
  the team or with named people (members, as viewer or participant).
- **Other people's private conversations answer 404,** and are missing from
  `GET /runs` and the stream. Admins are no exception: a workspace admin
  using the agent sees their own conversations and what is shared with them.
  Viewing the workspace as a user (D64) shows only what that user's team can
  see.
- **Tile-wide settings need write access to the tile:** `/config`, `/models`,
  saving or deleting shared skills, and `PUT /halt`. People who can only
  open the tile (`read`) can still chat, and they no longer see the ⚙
  settings or the halt button.
- **While the agent is halted,** only a manager's message lifts the brake.
  Anyone else gets **423** ("the agent is paused by a manager").
- **Schedules belong to whoever created them.** Only the owner may edit one
  or "Run now". Managers see every schedule and can switch any off or delete
  it, but can't read or change another person's private one. The agent's
  `unschedule` tool only removes schedules of its own conversation's owner.
- **Another component calling the agent's API** (with a grant) sees only the
  runs it started itself. The owner token still sees everything.
- **Views no longer include static MCP servers' `headers`.**

Runs from before this change have no owner and stay **visible to everyone**,
exactly as before. The tile's managers own them: they can delete them, or
claim one by making it private.

## Who's affected

- **Teams sharing one agent tile.** Each person's new chats are now private
  to them. Old runs stay shared.
- **People with `read` on the tile,** who could previously change settings,
  halt the agent or edit any schedule.
- **Scripts or other tiles that call the agent's API** with a component
  grant (not the owner token) and expect to see every run.
- **Forked instances that changed `_backend/main.go`'s route list** (the
  routes now live in `_backend/routes.go`, each with its access need), or
  that call handlers assuming everyone is admin.

## How to migrate

- **Nothing to do** for single-user workspaces: you own your new chats, and
  your old runs are still listed.
- **To share a conversation:** set its visibility to `team`, or add members.
  (The sharing UI lands next, in the same release series.)
- **Grant write access** on the agent tile to the people who should manage
  it (settings, halt, everyone's schedules).
- **API callers** that must see everything should use the owner token. A
  component sees the runs it started.
- **Forks:** declare new routes in `routeTable()` with a `need`
  (`needViewer`, `needParticipant`, `needOwner`, `needManager`, …). Inside a
  handler, `callerOf(r)` is the resolved caller and `levelOf(r)` their
  access to the route's run.

## Why

The template is a team tool. Before this change every user who could open it
saw, and could delete, everyone's chats, and anyone could pause the agent or
rewrite someone else's schedule. Chats should be private by default and shared
on purpose, as in ChatGPT.

This is privacy between people who **use** the agent, enforced by the tile's
own code. Anyone with write or terminal access to the tile can change its
backend or read its database, so they can read every conversation. Give team
members `read`.
