# 2026-07-09 — terminal `$HOME` is per user (`homes/<user>`)

**Behavior change** for every workspace with terminals; automatic migration at
startup, with one manual case.

## What changed

Terminals used to share one `<workspace>/home` — every user's agent-CLI config
(`~/.claude`, credentials), shell history, and dotfiles were commingled. Now
each signed-in user gets their own `<workspace>/homes/<user>`, created and
skeleton-seeded (`.zshrc`/`.bashrc`/`.bash_profile`) on their first terminal.
The root token principal uses `homes/owner`. A session mounts its creator's
home; another (non-admin) user cannot attach to it.

This is filesystem hygiene (terminals share one unix user); the API
credential is separately scoped — see the same-day terminal-token change
(`2026-07-09-terminal-scoped-tokens.md`).

## Migration (runs at xbind startup)

- **`home/` only** → renamed to `homes/<user>`, all data preserved. The target
  is the workspace's one human when unambiguous — the sole user, else the sole
  admin — otherwise `homes/owner`.
- **neither form** → `homes/` is created empty (nothing at risk).
- **both `home/` and `homes/`** → `home/` is removed only if it is a pristine
  seeded skeleton (e.g. recreated by a downgraded xbind's backfill). If it
  holds real data, **xbind refuses to start** with instructions — merge by
  hand, it never guesses:

  ```sh
  mv home/.claude homes/<user>/       # and anything else you care about
  rm -r home && systemctl restart xbin
  ```

The migration also appends `homes/` to the workspace `.gitignore` (user homes
hold credentials and must never land in the workspace repo).

## Who's affected

- **Single-user / single-admin workspaces** (the recommended prod setup): your
  home moves to `homes/<your-user>` and your terminals keep working with all
  config intact. Nothing to do.
- **Multiple admins**: the legacy home lands in `homes/owner` (xbind can't know
  whose config it was). Reassign with `mv homes/owner homes/<user>`; the other
  admins start fresh on their next terminal.
- **Token-only workspaces** (no users): the home becomes `homes/owner`, which
  is exactly what token-authenticated terminals use. Nothing to do.
- Scripts that hard-coded `<workspace>/home` should use `$HOME` (always correct
  inside a terminal) or `homes/<user>`.
