package server

// openapi_partitionconsent.go — cross-tile partition edges' routes
// (plans/partitions 05 §2, 06 §6.1; docs/partitions.md §Calls between
// partitioned tiles): a person's consents and egress ledger, and the
// admins' view of the edges. endpoints() appends them.

func partitionConsentEndpoints() []ep {
	edge := jsonBody("the edge", oapi{"from": str("the calling partitioned tile, apps/z"), "to": str("the partitioned tile whose data it uses, apps/x")}, "from", "to")
	const person = "a person's own session, app or device (PersonOnly: never tile code, view-as or the root token)"
	view := "{policy: {partitionConsent}, consents: [{from, to, at, via}], asked: [{from, to, at}]}"
	return []ep{
		{"GET", "/partitions/consents", partModeTag, "A person's consents to cross-tile partition edges", person,
			"The workspace setting partitionConsent, the person's recorded consents (kept while the policy is off, applied again when it returns) and the edges they were asked about within the last day and haven't allowed.",
			nil, nil, view},
		{"POST", "/partitions/consents", partModeTag, "Let a partitioned tile use your data in another", person,
			"Only while the workspace setting partitionConsent is on (else 409): from and to must both be partitioned (409), exist (404) and be readable by the person (403); a path holding the arrow → is refused (400). Recorded in data/partitions/consents/<uid>.json — a person recreated under the same id inherits none; a tile deleted, moved or switching mode takes every consent naming it. A record this xbind can't read is kept as it is: 409 (/alerts kind partition-consents). Audited; publishes `partitions` op consent to the person's own sockets.",
			nil, edge, view},
		{"DELETE", "/partitions/consents", partModeTag, "Take a consent back", person,
			"In either setting. {from, to} in the body or as ?from=&to=. The next call and data reach from from's partition of the person into to is refused (with the policy on), and from's backend instance of the person is stopped (a proxied stream a page, terminal or agent session of from opened before lasts until it closes). The answer adds revoked: whether there was a consent to take back. Audited; 409 as for POST.",
			[]oapi{queryParam("from", "the calling tile (instead of the body)", false), queryParam("to", "the callee tile (instead of the body)", false)}, edge, "{policy, consents, asked, revoked}"},
		{"GET", "/partitions/ledger", partModeTag, "The egress ledger of people's partitions", person,
			"Counts per day, never contents: kind edge (an allowed call or data reach into the same person's partition of another partitioned tile), provider (a call to a tile that isn't partitioned, or to a partitioned tile's deployment beyond its primary, <tile>+<name>), bus and trigger; a shared resource's reach isn't counted. rows: the person's own. With tile: totals (per kind and target, with the number of people) for the tile's writers, managers and admins — for all but admins every personal tile's target reads \"(a personal tile)\". Admins: people — per-target totals per person. Kept 90 days.",
			[]oapi{queryParam("tile", "one tile's rows and totals", false), queryParam("days", "the window, 1–90 (default 30)", false)}, nil,
			"{days, rows: [{tile, day, kind, target, count}], totals?: [{kind, target, count, people}], people?: [{user, tile, kind, target, count}]}"},
		{"GET", "/partitions/edges", partModeTag, "Edges between partitioned tiles", "admin",
			"The edges a partitioned tile has into another's people's data — granted now, or counted in the people's ledgers in the window — with how many people used and allowed each: what turning partitionConsent on starts asking about.",
			[]oapi{queryParam("days", "the window, 1–90 (default 30)", false)}, nil,
			"{days, policy: {partitionConsent}, edges: [{from, to, granted, people, calls, consented}]}"},
	}
}
