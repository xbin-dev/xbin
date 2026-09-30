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
			"features: what this xbind's partitions API serves (partitions/1, mode-switch/1, consents/1, personal-binds/1, global-address/1, partition-ops/1, log-share/1, credential-confirm/1, …) — a 404 means an xbind without partitions. " +
				"With tile (one the caller can read): state (partitioned|unpartitioned|pending|invalid), spec {user, global}, request {spec, since, declined} | null, policies, limits {maxRunning, partitionBytes}; " +
				"partitions: the caller's own row (state active|dormant, running, instance, lastStarted, bytes, registrations counts, logShare, ledger totals) — for admins every person's metadata row (never content, key names, log lines or mail), with orphaned ones; " +
				"totals {people, running, bytes, cron, bus} for the tile's writers, managers and admins; trust (who can change the code that runs on the person's data, live reload, bound providers, warnings) for its people; " +
				"consents (the person's, with partitionConsent on); binds (personal binds on the tile: the person's own, every one for admins); orphans (admins); notices (the person's). " +
				"A tile's own credentials get the tile-level fields and features only. Without tile: tiles [{tile, state, spec, request, mine?, totals? (admins), trust?, globalBinds?}], and for admins isolated and orphans; for a person their credentials waiting (held) and notices.",
			[]oapi{queryParam("tile", "one tile", false)}, nil,
			"{features, policies, tile?, state?, spec?, request?, limits?, partitions?, totals?, trust?, consents?, binds?, orphans?, notices?, tiles?, isolated?, credentials?}"},
		{"POST", "/partitions/stop", partModeTag, "Stop a person's partition instance", actor + ": the person (their own), a tile manager or an admin (anyone's)",
			"Stops the instance (its token revoked first) and waits for it; its data stays and the next request starts it again. 404 for a person without a partition of the tile; 409 on a tile that isn't partitioned. Audited.",
			nil, op(nil, "tile", "partition"), "{ok, tile, partition}"},
		{"POST", "/partitions/reset", partModeTag, "Delete one person's partition of a tile", actor + ": the person (their own) or an admin (anyone's)",
			"Needs confirm = \"<tile> <partition>\" (else 409 with the text to type). Stops the instance, ends the person's terminal and agent sessions on the tile (their new sessions answer 409 meanwhile), then deletes the partition's namespaces (when the tile roots its scope), vault, registrations, records, ledger, log share, backend log, terminal layers and agent-session history, and erases its backup subkey (its archives become unreadable). 409 while the tile is paused. An admin's reset is audited and tells the person (push and notice).",
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
			"{id, allow}: a sign-in link, password or SSO email an admin made for you while the workspace policy credentialResetConfirm was on (GET /partitions lists them in credentials). allow activates it; refusing revokes it (a link stops working, a password or email is dropped). Unanswered, it activates 24 hours after you were told. 404 for one that isn't waiting.",
			nil, jsonBody("the decision", oapi{"id": str("the held credential's id"), "allow": boolean()}, "id", "allow"), "{ok, id, kind, decision: allowed|refused}"},
	}
}
