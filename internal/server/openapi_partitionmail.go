package server

// openapi_partitionmail.go — partition mail's routes (plans/partitions 04
// §3; docs/partitions.md §Partition mail): the xbind-owned inboxes between a
// partitioned tile's global instance and its people's partitions.
// endpoints() appends them.

func partitionMailEndpoints() []ep {
	const who = "a partitioned tile's global instance (its backend, on the primary) or a person's partition of it (its backend, that person's frames, terminals and agent sessions); everyone else 403 — admins, the root token and other tiles included"
	const reader = who + "; the global instance's inbox is also read and acknowledged by the tile's frames, terminals and agent sessions acting as global (the owner token's frames, root terminals — on the primary, never view-as), which send nothing"
	return []ep{
		{"POST", "/partitions/mail", partModeTag, "Send partition mail", who,
			"The global instance mails a person (user:<id> — live on the tile, else 404 \"no such person here\") or global; a person's partition mails global only (403 otherwise; 403 on a tile without a global instance). from is stamped by xbind — global or user:<id> — never read from the body. An item (topic and data) is at most 1 MiB (413); an inbox holds at most 1000 items and 64 MiB (507 to the sender), and each sender at most 100 items and 8 MiB of the global instance's (507 to that sender only). ttl: seconds, 1 to 2592000 (default 7 days). source (the global instance to a person only): a private trigger's source, counted as trigger in the person's egress ledger — never the content. Stored sealed in data/partitions/<tile-key>/<dep>/mail.db and never backed up; while the tile is paused, 409; while the vault is sealed, 503. The addressee's doorbell rings (the tile's partitionMail).",
			nil, jsonBody("the item", oapi{"to": str(`"global" or "user:<id>"`), "topic": str("≤ 256 bytes"), "data": freeSchema("any JSON"),
				"ttl": oapi{"type": "integer", "description": "seconds, 1–2592000 (default 604800)"}, "source": str("optional: a private trigger's source, for the person's ledger")}, "to"), "{ok, id}"},
		{"GET", "/partitions/mail", partModeTag, "Read your partition's inbox", reader,
			"The caller's own inbox only — its partition's, or the global instance's — oldest first, expired items dropped, and items that can't be opened dropped as undeliverable. A page stops at limit or past ~8 MiB of data: only more false ends the inbox. 503 while the vault is sealed and the page holds items. No route reads another inbox; admins see counts only (GET /partitions).",
			[]oapi{queryParam("after", "the last id already read", false), queryParam("limit", "1–1000 (default 100)", false)}, nil,
			"{items: [{id, from, topic, data, at, expires}], more}"},
		{"POST", "/partitions/mail/ack", partModeTag, "Acknowledge partition mail", reader,
			"Removes the listed items of the caller's own inbox (at most 1000 ids; unknown ids are nothing to do); it works while the vault is sealed. Delivery is at-least-once until acked or expired: handlers dedupe by id.",
			nil, jsonBody("the ids", oapi{"ids": arr()}, "ids"), "{ok}"},
	}
}
