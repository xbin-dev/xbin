package server

// openapi_partitionops.go — the operations on people's partitions
// (plans/partitions 06 §5-§9; docs/partitions.md §Operating partitions):
// the listing, stop, reset and purge, log shares and credential
// confirmations. endpoints() appends them.

func partitionOpsEndpoints() []ep {
	const person = "a person's own session, app or device (PersonOnly: never tile code, view-as or the root token)"
	const actor = "a person's own session, app or device, the root token, or the admin tile's frame driven by one; never a tile's backend, frame, terminal or agent"
	op := func(extra oapi, req ...string) oapi {
		props := oapi{"tile": str("the partitioned tile"), "partition": str("user:<id> (purge: also the partition id u-<32 hex>)")}
		for k, v := range extra {
			props[k] = v
		}
		return jsonBody("the partition", props, req...)
	}
	return []ep{
		{"GET", "/partitions", partModeTag, "Partitioned tiles and their people's partitions", "any caller; what it lists depends on who asks",
			"features: what this xbind's partitions API serves (partitions/1, mode-switch/1, consents/1, personal-binds/1, global-address/1, partition-ops/1, log-share/1, credential-confirm/1, partition-mail/1, …) — a 404 means an xbind without partitions. " +
				"policies (partitionConsent, credentialResetConfirm) for people and admins, never tile code. " +
				"With tile (one the caller can read): state (partitioned|unpartitioned|pending|invalid), spec {user, global}, request {spec, since, declined} | null, limits {maxRunning, partitionBytes}, reviewedOnly {on, by?, at?, unprotected?}; " +
				"partitions: the caller's own row (state active|dormant, running, instance {tile, deployment, partition, state, gen, uptimeSec, rssKb, restarts, error?, errorClass?}, lastStarted, bytes, registrations counts, logShare, ledger totals, mail? {pending, bytes, expired, undeliverable?}: their inbox's counts) — for admins every person's metadata row (never content, key names, log lines or mail: its counts only; instance.errorClass, never the error's text), with orphaned ones; bytes are measured at most once a minute; " +
				"totals {people, running, bytes, cron, bus} for the tile's writers, managers and admins; trust (who can change the code that runs on the person's data, live reload, bound providers, warnings) for its people; " +
				"consents (the person's, with partitionConsent on); binds (personal binds on the tile: the person's own, every one for admins); orphans (admins); notices (the person's); " +
				"for admins also history (the tile's mode history, newest first, at most 50: [{op: auto|request|switch|keep|withdrawn|backup-erase|partition-restore, from, to, by?, at, wiped?, reason?, partition?}]), lastWipe? {from, to, at} (the last switch that deleted data) and globalMail? {pending, bytes, expired, undeliverable?} (the global instance's inbox: counts only); " +
				"for a tile manager who isn't an admin, in their own session, logShares? [{user, until}] (who shares their partition's log now: the logs panel offers each). " +
				"A tile's own credentials get the tile-level fields and features only — except the admin tile's frame driven by an admin's login (the admin console), which reads as an admin, with and without tile. Without tile: tiles [{tile, state, spec, request, mine?, totals? (admins), trust?, globalBinds?, reviewedOnly?, untracked?, untrackedCount?, untrackedError?}], and for admins isolated and orphans; for a person their credentials waiting (held) and notices. " +
				"untracked=1 (admins, bx doctor): each partitioned tile's files its own repository doesn't track (a confined git per tile).",
			[]oapi{queryParam("tile", "one tile", false), queryParam("untracked", "1: list untracked files of partitioned tiles (admins)", false)}, nil,
			"{features, policies?, tile?, state?, spec?, request?, limits?, reviewedOnly?, partitions?, totals?, trust?, consents?, binds?, orphans?, history?, lastWipe?, globalMail?, logShares?, notices?, tiles?, isolated?, credentials?}"},
		{"POST", "/partitions/stop", partModeTag, "Stop a person's partition instance", actor + ": the person (their own), a tile manager or an admin (anyone's)",
			"Stops the instance (its token revoked first) and waits for it; its data stays and the next request starts it again. A tile the caller can't read answers 404 (unless they name their own partition of it); someone else's partition 403 before anything of it is said; 409 on a tile that isn't partitioned; 404 for a person without a partition of the tile. Audited.",
			nil, op(nil, "tile", "partition"), "{ok, tile, partition}"},
		{"POST", "/partitions/reset", partModeTag, "Delete one person's partition of a tile", actor + ": the person (their own) or an admin (anyone's)",
			"Needs confirm = \"<tile> <partition>\" (else 409 with the text to type). Stops the instance, ends the person's terminal and agent sessions on the tile (their new sessions answer 409 meanwhile), then deletes — in every deployment it has data in — the partition's namespaces (only when the tile roots its scope: a tile in another's scope uses none), vault, registrations, records, ledger, log share, backend log, terminal layers and agent-session history, and erases the tile's own backup keys of it (part:<tile>/<deployment>/<partition id>; its archives become unreadable — never the scope root's). The same rights and 404/403 order as stop. 409 while the tile is paused. An admin's reset is audited and tells the person (push and notice).",
			nil, op(oapi{"confirm": str("\"<tile> <partition>\"")}, "tile", "partition", "confirm"), "{ok, tile, partition, deleted: {namespaces, layers, histories, subkeys, bytes}}"},
		{"POST", "/partitions/purge", partModeTag, "Delete orphaned partitions now", "admin (" + actor + ")",
			"Orphaned partitions — their person deleted (or their id someone else's now), their tile removed — are otherwise deleted 30 days after (the retention). Purges every orphan, a tile's, or one (partition: its partition id, or user:<id> of the person it was). A live person's partition: 409 (reset it instead); none: 404. A paused tile's orphans are skipped. A removed tile left with nothing loses its mode record. Audited.",
			nil, op(nil), "{ok, purged: [{tile, deployment, partition, user, reason, since, deleted, error?}]}"},
		{"POST", "/partitions/share-log", partModeTag, "Share your partition's backend log", person,
			"{tile, days?}: days 1–14 (default 7). While shared, the tile's managers and admins read it (GET /logs?component=<tile>&user=<you>). Kept in the partition (a reset takes it). 404 without a partition of the tile.",
			nil, jsonBody("the share", oapi{"tile": str("the partitioned tile"), "days": integer()}, "tile"), "{ok, tile, shared, until}"},
		{"DELETE", "/partitions/share-log", partModeTag, "Stop sharing your partition's backend log", person,
			"{tile}: the share ends at once.",
			nil, jsonBody("the tile", oapi{"tile": str("the partitioned tile")}, "tile"), "{ok, tile, shared}"},
		{"POST", "/partitions/credential-confirm", partModeTag, "Allow or refuse a credential an admin made for you", person,
			"{id, allow}: a sign-in link, password or SSO email an admin made for you, or a change of the workspace's SSO provider (kind sso-provider), while the workspace policy credentialResetConfirm was on (GET /partitions lists them in credentials). allow activates it; refusing revokes it (a link stops working, a password or email is dropped, a provider change unbinds your SSO email) — while it is unused, even past its 24 hours. Unanswered, it activates 24 hours after you were told. 409 decision already-effective: a link no longer pending (redeemed, replaced or expired) — change your password and sign out everywhere. 404 for one that isn't waiting.",
			nil, jsonBody("the decision", oapi{"id": str("the held credential's id"), "allow": boolean()}, "id", "allow"), "{ok, id, kind, decision: allowed|refused|already-effective}"},
		{"POST", "/partitions/reviewed", partModeTag, "Turn a tile's reviewed-code-only switch on or off", "admin (" + actor + ")",
			"{tile, on}: on needs the tile partitioned and the primary of the tile and of every non-partitioned provider bound to it protected (409 with unprotected: [...]). While on (and the tile partitioned), unprotecting any of them answers 409, and binding into the tile a provider that isn't partitioned and whose primary isn't protected answers 409. off always succeeds. Audited.",
			nil, jsonBody("the switch", oapi{"tile": str("the partitioned tile"), "on": boolean()}, "tile", "on"), "{ok, tile, reviewedOnly: {on, by?, at?, unprotected?}}"},
	}
}
