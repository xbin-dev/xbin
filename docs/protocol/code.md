# Protocol: code, git and change proposals

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
reading a tile's code and git history, importing a
repository, and cross-tile change proposals (code PRs).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Code, git and change proposals

```
GET    /code/tree                  read on the tile, admin OR code[:<component>]. ?component=<path> → {component, files:
                                   [{path,size}]} — its files.
GET    /code/file                  read on the tile, admin OR code[:<component>]. ?component=<path>&file=<rel> →
                                   {path, content|binary|truncated}; a link out of the tile: 404.
GET    /git/log                    read on the tile, admin OR code[:<component>]. ?component=<path>&limit=N → {repo,
                                   commits:[{hash,short,author,date,subject,
                                   add,del,files}], remote}. From the component's
                                   OWN repo (each component is its own git repo);
                                   add/del/files = that commit's churn; remote =
                                   origin.
GET    /git/diff                   read on the tile, admin OR code[:<component>]. ?component=<path>&rev=<hash> → {repo,
                                   diff}. rev empty = uncommitted changes vs HEAD.
GET    /git/activity               read on the tile, admin OR code[:<component>]. ?component=<path> → {repo,
                                   remote, upstreamRef, local:[{t,a}],
                                   upstream:[{t,a}]|null}. Author-date (t, unix)
                                   + author (a) timeline of the component's
                                   history and, if a remote-tracking branch
                                   exists, its upstream — for activity charts.
GET    /git/remote-info            xbin:writer. ?url=<git-url> → {defaultBranch,
                                   tags:[…] (newest first), remote}. git ls-remote
                                   on a URL to preview versions before install.
POST   /git/import                 same authority as /create on the
                                   resolved path. body {url, path?, ref?,
                                   owner?} — clone a
                                   component in from a git remote (GitHub/GitLab/
                                   any git URL); path defaults to apps/<repo>, ref
                                   = a tag/branch. Its origin remote is kept (so
                                   it's updatable). → {path, remote, ref,
                                   pendingGrants}. Rejects local/file:// URLs and
                                   repos with no xbin.json/index.html.

Cross-tile change proposals ("code PRs", docs/bx.md §code pr). READ visibility
on the target is the whole gate (read = suggest, D48): admins; the target's
own principals; terminals/frames whose driving user can read the tile (D40);
elements holding code[:<target>]. xbind stores proposals under data/prs/ and
NEVER applies one — the target's own plane reviews and `git am`s it in its
terminal. A `pr` event (read-filtered like the mounts) fires on every
open/comment/state change.

POST   /code/prs                   read gate (above). body {target, title,
                                   message?, base?, series} — open a proposal.
                                   series = git format-patch mbox (≤4 MiB, must
                                   contain a diff), stored verbatim; base = the
                                   target rev it was formatted against (lifted
                                   from the base-commit trailer by bx). → the
                                   PR meta {number, target, from:{component,
                                   user,via} (verified, never self-reported),
                                   title, state:"open", created, …}.
GET    /code/prs                   ?target=<path>[&state=…] → {target, prs:[…]}
                                   (read gate); or ?from=1 → {prs:[…]} — the
                                   caller's own outgoing PRs across targets;
                                   admins with neither param get everything.
GET    /code/prs/summary           any authenticated → {counts: {path: n}} —
                                   open-PR counts, filtered to targets the
                                   caller can read (the shell's proposal badges).
GET    /code/pr                    read gate. ?target=<path>&n=<n> → full meta
                                   + events (the review thread).
GET    /code/pr/series             read gate. ?target=<path>&n=<n> → the raw
                                   mbox, text/plain, byte-exact (review first,
                                   then pipe into `git am --3way`).
POST   /code/pr/comment            read gate. {target, n, body} — append to
                                   the review thread (author ↔ maintainer).
POST   /code/pr/state              {target, n, state, comment?}. merged and
                                   rejected are the TARGET side's call (its
                                   own principals, write-level users, admins);
                                   withdrawn is the author's. Only open PRs
                                   close; no reopen — refile instead. 409 on
                                   double-close. A non-primary deployment's
                                   backend decides none: 403 "deciding a PR
                                   is the primary's act: a non-primary
                                   deployment's backend can't do it
                                   (<deployment>)".
```
