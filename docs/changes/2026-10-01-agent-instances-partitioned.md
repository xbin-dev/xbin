# 2026-10-01 — existing agent instances ask to become partitioned when they take the template's update

## What changed

New agent-template instances already start partitioned — each person's
conversations, memory and schedules in their own partition, plus a global
instance for shared ones ([partitions.md](/docs/partitions.md)). Until now
an existing instance kept its mode through every template update.

Now **every agent instance becomes partitioned** (D177). The agent
template's block sets `"partitionOnUpdate": true`, so on an xbind started
with `--isolate` its served repository (every instance's `template` remote)
asks each instance for `"partition": ["user", "global"]` — as the line
right after the manifest's opening brace, where a new instance already has
it. When an instance takes the update (`git fetch template && git merge
template/main`, by you or an agent in its terminal), its `xbin.json` gains
that line, and xbind applies its usual rule for a mode change
([partitions.md](/docs/partitions.md) §The mode):

- **An instance that holds data** (any conversation, memory, schedule,
  vault key or registration) gets a **partition mode switch request**: it
  pauses — its window greys out, saying a partition mode switch is
  requested and that switching deletes all its data, with its
  `partitionNote` naming its conversations, memory and schedules — and
  nothing is deleted until a tile manager decides. **Switch**
  deletes all of its data and starts it partitioned: nothing is migrated.
  **Keep the current mode** deletes nothing and runs it unpartitioned
  again, as before.
- **An empty instance** switches at once.
- **Without `--isolate`** nothing asks: people's partitions can't run
  there, so the agent stays one instance, as before.

The merge driver xbind names in each template instance's repository now
takes an upstream `partition` where neither the merge base nor your
manifest names one, as git's line merge does; a mode you wrote yourself
stays yours (upstream's ask is then a conflict). Once the served
repository asks, the ask never changes or goes away. No other builtin
template, and nothing `bx builtin update` reaches, asks for a mode.

## Who's affected

Workspaces on an `--isolate` xbind with agent-template instances
(`apps/agent` and any other copy of the template) made before the
partitioned default, or opted out of it — once someone merges the
template's update into one. Until then nothing changes.

## How to migrate

- **To partition an instance,** merge the template's update and have a
  tile manager switch it (the greyed-out window, `bx partition switch
  <tile>`, or the admin console's partitions tab). Its conversations,
  memory, schedules, skills, triggers, channel links and settings are
  deleted, for everyone; export what you need first. Sandboxes stay with
  their sandbox manager.
- **To keep an instance unpartitioned,** choose **Keep the current mode**
  after the merge (`bx partition keep <tile>`) — it runs again at once —
  or take the `"partition"` line back out of its `xbin.json` and commit:
  later template updates then leave it alone.
- **To postpone,** don't merge the template's update yet.

## Why

The owner ruled that after this update every agent instance should be
partitioned, with no legacy-to-partition data migration: one agent per
person's partition keeps each person's conversations and memory theirs,
and the switch rule (a manager confirms, which deletes the data, or keeps
the mode) is what keeps a request from deleting anyone's data unasked.
