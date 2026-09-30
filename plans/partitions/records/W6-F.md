# W6-F — fabric: who the person is on a manager's sockets, their suite cases, AR-23

W6's fabric pack ([96-agtt-merge.md](../96-agtt-merge.md) §B1 F-M3, F-M4,
and the fabric part of §B4), on `pt/w6-base` (B0's merge). Branch
`pt/w6-f`. No xbind code, no new HTTP/WS surface: the contract text,
the conformance suite, the reference manager's test, the SDK's comments.
It edits neither `docs/changelog.md` nor `plans/DECISIONS.md`: their
texts are below.

Commits (oldest first):

- `35227227` sandboxcontract: `user-partitions/sockets` (F-M4), the fake's
  two skip lists, and the suite paragraph of docs/sandbox-manager.md and
  coding-sandbox API.md's "Testing on xbind".
- `04fe95cb` docs: from a user partition the person is the partition's,
  verified — terminals and stdio too; "§Consumers, sharing and people"
  everywhere AgTT cited the old name (F-M3).
- `a0c065f4` docs: stdio takeover and harness sign-ins in shared sandboxes
  (AR-23), the harness commands' trust base, coding-sandbox API.md's
  bullets (B4's fabric part).
- `333d7d6f` fakesandbox: its contract run requires `stdio` in hello.
- `eb537353` this record.

The review fixes, on `pt/w6-f-fix` ("## Review fixes"):

- `5667717d` sandboxcontract: a person's partition of another consumer,
  and an exec under another sandbox.
- `5b155764` docs: the partition's verified person holds with a
  `partitions` manager; sign-ins a consumer's rule; "home directory"; a
  partition id alone names no consumer.
- `18a7ddcb` agent API.md: in a person's partition the manager verifies
  the person.
- this record's update.

## F-M1 and F-M2 (B0's), verified

- **F-M1** (`4f57cd9b`): `GET /sandboxes/{name}/execs/{id}/stdio` is
  `GlobalOnlyRefused` in `internal/server/partitionclass.go`, beside
  `…/stdin`, `…/signal`, `…/resize`; `TestPartitionRouteClasses`
  (`internal/apicheck`) passes. A route without a class would have been
  refused to a person's partition anyway (fail closed); the row makes it
  the documented global-only refusal.
- **F-M2** (`43af5a2a`): `mutates` in coding-sandbox's `access.go` is
  AgTT's widened rule (any `Upgrade`, a path ending `/tty` or `/stdio`);
  `TestPageReaders` passes in `hack/tile-check.sh coding-sandbox` (race).

## F-M3 — who the person is

The rule is D140's (§Partitioned consumers: a user partition's person is
the partition's, verified; an `Sbx-User` naming anyone else is `403
not-allowed`). AgTT (D147) wrote the `tty`/`stdio` texts for an
unpartitioned consumer — "asserted, recorded, not verified", "the
partitions hold" in the sense of consumers. Now said, in each place the
plan lists and the ones next to them:

- docs/sandbox-manager.md: §Who is asking (the asserted bullet names the
  exception); §Consumers, sharing and people (the backend-call sentence);
  §Partitioned consumers' person bullet ("its backend's calls and sockets
  (`tty`, `stdio`) as much as its page's"); §stdio's "Who dials it" (who
  may, and the user partition's verified person); §Terminals (the
  backend bullet: consumers stay apart, and from a user partition the
  manager applies the person rules; the relay bullet cites §Consumers,
  sharing and people and says the manager applies the first check itself
  there, the terminal-at-all check staying the consumer's).
- docs/sdk.md: the consumer-terminals intro, "Your checks come first"
  (was "Your checks are the only ones about the person"), and the stdio
  paragraph.
- docs/changes/2026-09-30-manager-terminals-for-backends.md: "keeps
  consumers apart", the section's new name, the user-partition exception
  in "What changed" and in "How to migrate".
- `sdk/manager_tty.go` (file comment, `ManagerTTYOptions.User`),
  `sdk/manager_stdio.go` (file comment, `ManagerStdioOptions.User`: `""`
  there is the partition's person).
- `sdk/sandboxcontract/tty.go`: the `tty/backend` check's comment and
  `backendTTYWarn`'s.
- The old section name: sandbox-manager.md §Terminals, sdk.md, the
  migration note, and `plans/agtt-harness.md` §5.2's "§Partitions" (the
  only other citation; the changelog's quote of the old name is the
  rename's own history, left).

## F-M4 — the suite's socket cases

`user-partitions/sockets` (`sdk/sandboxcontract/partitions.go`), run when
hello offers `tty` or `stdio` (each half gated on its own):

- alice's partition makes a `team` sandbox, starts a tty exec and a split
  `cat` with stdin, and attaches the latter's stdio socket;
- on `…/tty`, `…/execs/{id}/tty` and `…/execs/{id}/stdio`: bob's partition
  (backend, page, backend naming bob), alice's partition of another
  consumer (`e.as("b").InPartition("alice", …)`: backend, page, backend
  naming alice — the same partition id, as xbind's `PartitionKey` is the
  person's, not the tile's), the consumer's instance without a partition
  and as `global`, and both naming alice get `404 not-found`;
  `alice.Asserting("mallory")` gets `403 not-allowed` — all before the
  upgrade (review fix: the other consumer's alice; `apart` has her too, on
  the HTTP routes);
- alice's socket still holds stdin after those dials (a refused dial
  takes nothing over);
- her partition's backend naming her attaches the tty exec, her page opens
  a terminal;
- carol's private sandbox at the consumer's non-personal identity: 404 to
  alice's partition on `…/tty` and on an exec's `…/stdio`; a `team` one
  there: her partition opens a terminal and drives a stdio exec;
- (review fix) an exec of alice's sandbox named under that `team` sandbox,
  which bob's partition does see: a tty exec on `…/execs/{id}/tty`, a
  split `cat` on `…/stdio`, each started (`execNotIn`) with an id the team
  sandbox doesn't hold — exec ids are per sandbox and collide (`e1`) — get
  `404 not-found` from bob's partition (backend, naming bob) and the
  consumer's global instance.

Mutation runs (throwaway edits of `hack/fakesandbox/fsb.go`, reverted, not
committed) show it has teeth: a fake whose socket routes took every dial
as alice's partition fails at bob's `…/tty` (upgraded, want 404); one that
dropped `Sbx-User` on socket routes fails at mallory (upgraded, want 403);
one whose `home()` compares only the partition id for a partition-homed
sandbox fails `sockets` (the other consumer's alice on `…/tty`) and `apart`
(her GET); one whose exec lookup falls back to every sandbox's execs fails
`sockets` on the `…/tty` probe, and with the fallback on `/stdio` only, on
the `…/stdio` probe.

Skips: fakesandbox's `TestContractBeforeStdio` and
`TestContractPolicingTTY` skip `user-partitions` (TestContract runs it).
`test/isolated`'s `csPartitionSkips` needed nothing: every caller the check
makes is one xbind makes (it names no verified person without their
partition), so it **runs through a real isolated xbind** against
coding-sandbox — the plan's F-S4 ("another partition's stdio dial gets
404") for the manager half; the agent half is B5's.

## B4, the fabric part

- **§Partitioned consumers (AR-23):** "Isolation stops at the sandbox"
  adds the stdio attach (it takes the exec's stdin from the socket before:
  a coding agent then takes its input from that partition) and the
  sandbox's home directory (`home`, `$HOME`: a coding agent's sign-in there
  serves whoever runs the agent afterwards, from any partition that sees it
  or in a clone, as the person who signed in); a consumer should keep
  sign-ins with a person's private work, in their partition's sandboxes.
- **Hello's `harnesses[].login`:** the same note; "a partitioned consumer
  should offer a person the sign-in only in a sandbox homed in their own
  partition … That is the consumer's to keep: to the manager a sign-in is
  a terminal like any other" (a rule for consumers, not a fact about them:
  review fix); and, after the list, `login` and `argv` run beside the
  person's credentials, so whoever may set them (operators, whoever may
  change the manager's code) is in the trust base of everyone using the
  image's coding agents. The builtin manager's partitioned-consumers bullet
  names the harness commands among what its writers hold.
- **coding-sandbox API.md:** the stdio bullet says "the consumer (a user
  partition is its own), the person rules", and so does the ports bullet
  (the same wording, side by side); its Images section says where sign-ins
  live and who is in their trust base.

## Changelog entry (docs/changelog.md, under 2026-09-30)

- **Sandbox managers: a user partition's person on terminals and stdio
  sockets, and what one shared sandbox shares**
  ([sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned consumers,
  §Terminals, §stdio, §hello). The contract now says what its partitions
  rule already meant for the routes coding agents added: from a
  partitioned consumer's user partition the person is the partition's and
  verified on the `tty` and `stdio` routes too — a manager with
  `partitions` applies the person rules itself, and an `Sbx-User` naming
  anyone else is `403 not-allowed`; "asserted, the consumer's to check" is
  an unpartitioned consumer's backend call (or a global instance's).
  A partition id alone names no consumer — one person's partitions of two
  consumers may carry the same id — so a manager keys on both.
  `sdk/sandboxcontract`'s `user-partitions` section gains `sockets` (run
  where hello offers `tty` or `stdio`): another partition, the same
  person's partition of another consumer, the consumer's global instance
  and a person it names get `404` on a partition-homed sandbox's terminals
  and stdio sockets, the partition naming someone else `403`, a refused
  dial takes no stdin, and a partition's exec named under a sandbox the
  caller does see is `404`; `apart` checks the other consumer's partition
  of the same person on the HTTP routes. `hack/fakesandbox` and
  `coding-sandbox` pass. §Partitioned consumers names two more things the
  partitions that see one sandbox share: an exec's stdio socket, whose
  attach takes over its stdin (a coding agent's), and the sandbox's home
  directory, where a coding agent's sign-in serves whoever runs that agent
  there afterwards; hello's `harnesses[].login` adds that a partitioned
  consumer should offer the sign-in only in a sandbox homed in the
  person's own partition (the manager can't tell a sign-in from another
  terminal), and that whoever sets `argv`/`login` is in its users' trust
  base. `ManagerTTYOptions.User` and `ManagerStdioOptions.User` say the
  same, for a manager with `partitions` (a partitioned tile uses no other
  from a person's partition: one without it takes the call as the tile's,
  the person asserted). Nothing changes for an unpartitioned consumer or a
  manager without `partitions`.

Corrections to fold into existing 2026-09-30 lines (the fold owns
docs/changelog.md):

- master's "Sandbox managers: coding agents in hello, terminals for
  consumer backends, …" bullet, **Terminals for consumer backends**: "the
  consumer checks that person first, the manager keeps its partitions" →
  "the consumer checks that person first, the manager keeps consumers
  apart (from a partitioned consumer's user partition the person is the
  partition's, verified: the manager checks them)".
- partitions' "Sandbox managers key partitioned consumers per person"
  (D140): "Isolation stops at the sandbox: partitions that see one sandbox
  share its execs and terminals." → "… share its execs, terminals and
  stdio sockets, and what its home holds (a coding agent's sign-in)."

## Decision entry

None of W6-F's own: F-M3 states D140's rule where D147's text contradicted
it, and F-M4 checks it on the routes D147 added. For the fold's one W6
decision ("coding agents under partitions", 96 §B4, the next free
D-number), the fabric part, to take in as its bullets:

- **The contract, unchanged in rule:** from a partitioned consumer's user
  partition the person is the partition's and verified on every route,
  the `tty` and `stdio` sockets included (D140); D147's "asserted, the
  consumer's to check" is an unpartitioned consumer's, or a global
  instance's. The consumer is the pair (`X-XBin-From`, partition id):
  xbind's partition id is the person's, the same at every partitioned
  tile. `sdk/sandboxcontract` checks it (`user-partitions/sockets`, and
  `apart` for the pair).
- **AR-23 grows:** partitions that see one sandbox share its stdio sockets
  (an attach takes over a coding agent's stdin) and its `$HOME` (a
  sign-in serves whoever runs the agent there, and its clones). So a
  partitioned consumer should offer sign-ins only in sandboxes homed in
  the person's partition — the consumer's rule to keep, the manager can't
  tell a sign-in from a terminal; the agent keeps it with §I15 (never at
  global, in shared or hosted chats) — and whoever sets an image's
  `harnesses` commands is in the trust base of their users.

## Seams for the other packs

- **W6-A / W6-U:** sandbox-manager.md now says a partitioned consumer
  should offer the sign-in only in a sandbox homed in the person's
  partition (a rule for consumers; it claims nothing about the agent).
  A-S2 (the relay through `managerHello` and `partitionBoxRefusal`), A-S3
  (a partition keeps only its homed sandboxes for harnesses), U-M3 and
  U-M4 make the agent keep it; on this branch `personSandbox` (the
  `login=1` relay) doesn't yet.
- **W6-A / W6-U, agent API.md:** this pack touched four passages of
  `builtin-templates/agent/API.md` (review fix), each a small in-line
  addition — §Coding sandboxes' "Where they come from" (its
  **partition** → "what it sees as this consumer; a partition sees its
  own"), its "People (D83)" sentence (a person's partition: verified, the
  manager refuses any other `Sbx-User`), §Coding agents' sign-in "Only a
  person who may use the sandbox themself" (the manager verifies them in
  a person's partition), and "Terminal relays and the log" (the same, and
  `Sbx-User: <you>` asserted, or verified in your partition). A-S2 or
  U-M4 may rewrite the same lines; merge keeping both.
- **W6-A:** from a user partition, every manager call's `Sbx-User` is the
  partition's person or empty, or the manager answers 403. The agent's
  harness pipe dials stdio with `User: p.t.Conn.User` and its relays with
  `c.user`: under §I15 (a harness only in the person's own conversations)
  those are the partition's person; a harness run for anyone else in a
  partition would fail closed (403, a `manager-error`), never act as them.
- **The fold:** `plans/agtt-harness.md` §2.2's line on partitions'
  D-numbers (96 §B4) is untouched here; D147's dated amendment is the
  fold's.

## Fold checklist (what this pack leaves to the fold)

- [ ] Apply "## Changelog entry" above to docs/changelog.md under
  2026-09-30, verbatim, and both "Corrections" beside it — until then the
  2026-09-30 line "the manager keeps its partitions" contradicts the docs.
- [ ] Take "## Decision entry"'s two bullets into the W6 decision.
- [ ] Confirm that W6-A's A-S2/A-S3 and W6-U's U-M3/U-M4 make
  sandbox-manager.md's "should offer a person the sign-in only in a
  sandbox homed in their own partition" true of the agent on every
  sign-in path: the `login=1` terminal relay, `POST …/authenticate`, and
  the device sign-in. Then (optionally) add "the agent template does:
  builtin-templates/agent/API.md §Coding agents" to hello's `login`
  bullet.
- [ ] Merge the four agent API.md passages ("Seams") with W6-A/W6-U's
  edits of the same lines.
- [ ] Rerun `TestCodingSandboxContract` and `TestCodingSandboxContractVM`
  (`test/isolated`, sandbox disabled) on the merged tree:
  `user-partitions/sockets` must PASS, not skip.

## Review fixes (`pt/w6-f-fix`)

- **(medium) A partition id alone:** `sockets` and `apart` now call as
  alice's partition of another consumer (`e.as("b").InPartition("alice",
  pid("alice"))`, and its page, and naming alice on the sockets): the same
  partition id, since xbind's is per person. A manager that finds a
  partition-homed sandbox by partition id alone fails both (mutant above).
  sandbox-manager.md §Partitioned consumers says a partition id alone
  names no consumer, and the suite paragraph lists the caller.
- **(medium) An exec under another sandbox:** `sockets` names an exec of
  alice's sandbox under the `team` sandbox bob's partition sees
  (`…/execs/{id}/tty` and `…/stdio`, ids the team sandbox doesn't hold:
  `execNotIn`) and wants 404 from bob and the global instance (mutant
  above). `stdio/refusals` is left as it is: it has one consumer's
  sandbox and another consumer that sees none of it, so the cross-sandbox
  case belongs where one caller sees two sandboxes.
- **(low) The sign-in sentence:** now a rule for consumers ("should
  offer … That is the consumer's to keep: to the manager a sign-in is a
  terminal like any other"); the fold's checklist confirms the agent.
- **(low) The SDK's precondition:** `ManagerTTYOptions.User`,
  `ManagerStdioOptions.User` and sdk.md's intro and "Your checks come
  first" say the partition's verified person holds only with a manager
  whose hello has `partitions` (a partitioned tile uses no other from a
  person's partition; one without it takes the call as the tile's, the
  person asserted — check `hello.caps` first). The endpoint bullet's
  `""` is "the consumer itself — in a person's partition, that person".
- **(low) agent API.md:** fixed in place rather than handed on (see
  Seams): the user-partition exception in the three passages about the
  person, and "(its **partition**)" → "(what it sees as this consumer; a
  partition sees its own, §Partitioned instances)".
- **(low) "home":** "The sandbox's home directory (`home`, `$HOME`) is
  shared the same way"; "A consumer should keep …" beside it.
- **(low) Changelog:** the fold checklist above; the entry and its
  corrections stay here (the pack may not edit docs/changelog.md).
- **(low) The isolated run:** run here, see Tests — both variants.

## Deviations

- One check, `sockets`, holds the plan's cases for both sockets (each half
  gated on its capability), plus two it didn't list: a refused dial takes
  no stdin, and the partition's own backend (naming its person) and page
  attach.
- The plan's "an asserted person" is the consumer's non-personal identity
  naming alice (with and without `X-XBin-Partition: global`); its verified
  alice without a partition (in `apart`) is left out, so the check runs
  through xbind too (xbind never makes that caller of a partitioned
  consumer).
- Beyond the plan's list of places: §Who is asking's and §Consumers'
  backend-call sentences, §Partitioned consumers' person bullet, the
  builtin manager's trust-base bullet, coding-sandbox API.md's ports
  bullet (the stdio bullet's wording, side by side) and Images sentence,
  its stale "Testing on xbind" sentence (it said `user-partitions` runs
  only in-process; I1 runs `global`, `global-home` — and now `sockets` —
  through xbind), and the fake's must-offer `stdio` (it always offered
  it; the list didn't require it, so a fake that lost it would skip the
  stdio half silently).
- `hack/fakesandbox/fsb.go` is untouched, so its mirrors are too.

## Bugs found

None in code. Docs: coding-sandbox API.md's "Testing on xbind" said the
`user-partitions` section runs only in-process (stale since I1); fixed.

## Tests

On `pt/w6-f`:

- `go test -race -count=1 ./hack/fakesandbox` — ok (TestContract with
  `user-partitions/sockets` run, tty and stdio halves; the two skip-list
  tests skip the section).
- `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh coding-sandbox` — ok
  (`TestContract/user-partitions/sockets` PASS in-process, `TestPageReaders`
  for F-M2); `hack/tile-check.sh sandbox-terminal` — ok (it imports the
  suite).
- `test/isolated` `TestCodingSandboxContract/user-partitions` through an
  isolated xbind (.dev.mk's env, sandbox disabled, `-parallel 3`): global,
  global-home, **sockets** and user-partitions-xbind PASS; apart, shares,
  person, recreated the declared skips. `TestCodingSandboxContractVM` (the
  same suite on VM sandboxes) not run.
- `cd sdk && go vet ./... && go test -count=1 ./...` — ok.
- `go test ./internal/docscheck ./internal/sizebudget ./internal/builtins`
  and `-run TestPartitionRouteClasses ./internal/apicheck` — ok.
- `make fmt-check vet` — ok.
- Not run: the agent template's checks — this pack changes no agent
  file, and of what the agent imports only comments changed
  (`sdk/manager_tty.go`, `sdk/manager_stdio.go`).

On `pt/w6-f-fix` (the review fixes):

- `go test -race -count=1 ./hack/fakesandbox` — ok; `TestContract`'s
  `user-partitions/*` all PASS, `sockets` with the new callers and probes.
- The four mutants of "F-M4" above, each a throwaway edit of
  `hack/fakesandbox/fsb.go` reverted with `git checkout` — each fails as
  said; `fsb.go` is untouched in the commits, so its mirrors are too.
- `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh coding-sandbox` — ok
  (and `-run TestContract/user-partitions -v`: all seven PASS in-process,
  `sockets` included); `hack/tile-check.sh sandbox-terminal` (race) — ok.
- `test/isolated`, .dev.mk's env, Bash sandbox disabled, `-parallel 3`:
  `TestCodingSandboxContract` — ok (`user-partitions/sockets` PASS through
  a real isolated xbind, with the other consumer's alice — xbind's own
  partition id for her — and the cross-sandbox exec probes;
  `user-partitions-xbind`, `global`, `global-home` PASS; `apart`,
  `shares`, `person`, `recreated` the declared skips);
  `TestCodingSandboxContractVM` (KVM) — ok, the same.
- `cd sdk && go vet ./... && go test -count=1 ./...` — ok.
- `go test ./internal/docscheck ./internal/sizebudget ./internal/builtins`
  and `-run TestPartitionRouteClasses ./internal/apicheck` — ok.
- `make fmt-check vet` — ok.
- Agent template: only API.md prose changed (no code, no golden); its
  checks not rerun.
