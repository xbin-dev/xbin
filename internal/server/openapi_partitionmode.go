package server

// openapi_partitionmode.go — the partition mode decision's route
// (plans/partitions/01 §2.5, docs/partitions.md): endpoints() appends it.
// The other partition routes keep their entries beside their packs' (a
// merge folds the lists into endpoints()'s one append).

const partModeTag = "Partitions"

func partitionModeEndpoints() []ep {
	spec := func(d string) oapi {
		return oapi{"type": "object", "description": d + ": {user, global}; null = unpartitioned",
			"properties": oapi{"user": boolean(), "global": boolean()}}
	}
	return []ep{
		{"POST", "/partitions/mode", partModeTag, "Decide a partition mode switch request: keep the current mode, or switch",
			"a tile manager (the tile's user-owner, an admin of its owning org, a workspace admin) as a person — own session, app, device, the root token, or the admin tile's frame under their login",
			"A tile that holds data whose code asks for another partition mode (Q) than the recorded one (R) is paused until a tile manager decides (docs/partitions.md §The mode). from/to must still be R and Q, else 409 with the current partition {state, from, to?, declined?}. keep (an open request): R runs again, nothing is deleted. switch (an open or declined request): confirm must be the tile's path; user partitions need --isolate (409); an offloaded tile is 409; bound sandbox managers whose hello caps lack \"partitions\" are 409 {managers} unless yes. The switch holds the tile's namespaces, stops every instance and deletes the tile's data — every namespace, vault file and registration between user partitions and unpartitioned (erasing its ns: backup keys), global's alone when \"global\" goes, nothing when it comes; never the workspace-level resources — then records R := Q and tells each person whose partition went. dryRun counts and deletes nothing. A wipe, or an erase of the backup keys, that fails is 500: nothing recorded, the request stays open, a retry finishes it. eraseError and wiped.keyFilesLeft: keys erased whose files aren't removed yet.",
			nil, jsonBody("the decision", oapi{"tile": str("apps/x"), "act": str("keep|switch"), "from": spec("R"), "to": spec("Q"),
				"confirm": str("switch: the tile's path, typed"), "yes": boolean(), "dryRun": boolean()}, "tile", "act"),
			"{ok, tile, act, mode, declined} | {ok, tile, act, from, to, deletes, wiped, keeps, people?, managers?, archiver?, eraseError?}"},
	}
}
