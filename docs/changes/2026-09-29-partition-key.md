# 2026-09-29 — `xbin.json`'s `"partition"` is xbind's key now

## What changed

`"partition"` at the top level of a tile's `xbin.json` asks xbind to run the
tile as a partitioned tile: `["user"]` or `["user", "global"]`
([elements.md](/docs/elements.md) §Manifest). Until this release xbind
ignored the key, as it ignores every key it doesn't know (compat rule 7).

Any other value now makes the tile's partition request **invalid**: its
backend doesn't run, calls to its API answer 409 with the reason, and its
`manifestError` (and `partitionError` in `/api/xbin/components`) starts
with `partition:`. That covers a value of another shape (a string, an
object, a number, `true`), `[]`, `["global"]` alone, a duplicate and an
unknown word. xbind logs one warning per tile naming it. The tile's files
keep serving, and nothing is deleted or recorded.

The key fails closed on purpose: a later xbind may add a word (`"org"`),
and an older one must run no backend rather than run the tile in a mode
it didn't ask for.

`partitionMail` and `partitionNote` are read only beside a valid
`"partition"`, and `scope.json`'s `"shared"` only in a scope a tile
partitions: a tile that uses those names for something else is unaffected.

## Who's affected

Only a tile whose `xbin.json` already carried a top-level `"partition"` key
for its own purposes. None of the shipped tiles, templates or examples do.

## How to migrate

1. After the upgrade, check `bx doctor` or the xbind log for "the tile's
   partition request is invalid".
2. Rename the tile's own key (say, to `"myPartition"`) in `xbin.json` and in
   the code that reads it. The backend starts again at the next request.

## Why

Partitioned tiles keep each person's data apart. Running such a tile in a
mode other than the one its code asks for would mix people's data, or hand
one person's data to another. An unknown request therefore stops the
backend instead of being ignored.
