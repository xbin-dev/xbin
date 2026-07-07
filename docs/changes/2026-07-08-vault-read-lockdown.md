# 2026-07-08 — vault values are readable only by the owning element

**BREAKING** for any admin/owner flow that read *another* element's secret
**value** through the vault API.

## What changed

`GET /api/xbin/vault/<component>/<key>` (fetching a secret's value) is now
**self-only**: it succeeds only when the caller *is* that component. The owner
and `xbin:admin` tiles — which previously could read any vault as a
password-manager — now get **403** on a value read.

Admins keep everything else:

- `GET /api/xbin/vault/<component>` — **list keys** (any vault).
- `PUT /api/xbin/vault/<component>/<key>` — **set / rotate** a secret.
- `DELETE …/<key>` — remove a secret.

So an admin can rotate a tile's credentials without ever seeing them, and a
compromised `xbin:admin` tile can't exfiltrate the whole workspace's secrets.

## Who's affected

- `bx vault get <other-component> <key>` and any tooling that read another
  element's value via the API now get 403. Reading your **own** vault
  (`xbin.Secret(...)`, `bx vault get <self>`) is unchanged.
- The admin console and per-tile mini-admin dropped their "reveal/show" button;
  secret values render masked (`••••••`). Setting/rotating still works.

## Why

A secret is only meant for its owning element's runtime. Letting the admin API
read every value made the admin surface (and any tile holding `xbin:admin`) a
single point of total-secret exfiltration. Values now never leave the barrier
except to the element they belong to. (The human with host + passphrase can
still read `data/vault/` on disk — this locks down the *API*.)

## If you genuinely need a shared secret

Store it in each element's own vault, or have the owning element expose a
role-guarded API that *uses* the secret without returning it (the documented
pattern in `docs/auth.md` §Vault).
