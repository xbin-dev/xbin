# Links, FIFOs and large files in a tile are never followed — templates skip them, builtin updates leave them

**BREAKING for a workspace template that holds a symbolic link**, and for
anyone who relied on a builtin update writing through a link. A security
fix: it closes holes present in every release so far.

## What changed

**Instantiating a workspace template** (`POST /templates/new` with a
workspace component as the source, the Templates gallery) copies each of
the template's files as the file at its path. Before, a link in the
template was followed and *its target's content* was copied — whatever the
link named, a person's home included. Now a symbolic link, a FIFO, a socket
or a device, and a file over 32 MiB, is **not copied**: the instance is
still made, and the answer names each file left out (`skipped`, and a
`notes` line: "3 file(s) of the template not copied — links, FIFOs and
files over 32 MiB are not copied: …"). A file's executable bit is copied.

**Builtin updates** (`GET /builtins/updates`, `POST /builtins/update`, `bx
builtin updates`/`update`, the Tile Manager's Updates tab) read and write a
builtin tile's files as the files at their paths:

- a file that is a symbolic link (anywhere on its path), a FIFO, a socket
  or a device, a directory, a file over 32 MiB, or a file inside a
  component nested in the tile that the tile doesn't ship, is listed as a
  **conflict with a note saying why** ("apps/chat/index.html is a symbolic
  link: not read, and no update writes it") — the Tile Manager and `bx
  builtin updates` show the note beside the file;
- **merge** and **pr** leave such a file as it is and update the others;
  what they update is recorded, and the update stays offered for the files
  left alone only;
- **replace** puts upstream's regular file in place of a link or a FIFO at
  a file's own path — never following it, and saying so in its notes
  ("…: replace put upstream's file in its place (what a link named is
  untouched)") — and leaves a file beneath a link, a directory or a nested
  tile's file alone;
- every write keeps an existing file's permission bits (0755 stays 0755);
  a new file is 0644.

A builtin-update proposal's merged close records the files that then
**contain the update's changes** — equal to upstream's, or carrying them
beside edits of the tile's own — and the update stays offered for the
rest. It doesn't parse code: a change that sits in a comment counts, and a
tile's writers can undo an applied change afterwards (it then lists as a
local edit) — both true before as well.

## Who's affected

- A workspace template that relies on a link (an `index.html` that links
  to a shared page, a `node_modules` link): the link is no longer copied.
- A builtin tile whose files someone replaced with links: the update no
  longer writes through them (merge, pr) or replaces them silently
  (replace).

## How to migrate

- In a workspace template, replace each link with the file itself (copy
  its content in), or have the instance make the link on first run.
  Instantiating names every file it skipped.
- For a builtin tile with links: decide per file — make it a regular file
  again and update, or keep the link and leave the update offered for it
  (pin the builtin to stop the offer). `bx builtin updates` and the Tile
  Manager say which files are left alone and why.

## Why

A link in a tile is the tile writer's to point anywhere — at an admin's
credentials, at another tile's backend. Copying or proposing through one
handed its target's bytes to whoever could read the result; writing
through one wrote outside the tile; a FIFO hung the reader. xbind now
never follows a link in a tile's tree, never opens a FIFO there, and never
reads more than 32 MiB of a file it was not asked to read.
