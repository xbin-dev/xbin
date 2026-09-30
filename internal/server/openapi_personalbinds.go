package server

// openapi_personalbinds.go — the personal binds' routes (plans/partitions/05
// §3, docs/partitions.md §Bind types): endpoints() appends them.

func personalBindEndpoints() []ep {
	bind := func(d string, required ...string) oapi {
		return jsonBody(d, oapi{"requester": str("apps/agent — a partitioned tile"), "slot": str("mcp — a multi:true http slot"),
			"provider": str("users/alice/mcp — a tile you own personally"), "id": str("DELETE: the bind's id instead of the triple"),
			"user": str("DELETE, admins: whose bind (default: your own)")}, required...)
	}
	return []ep{
		{"GET", "/partitions/binds", "Partitions", "List personal binds", "a person (their own), or admin (every person's); tile principals 403",
			"A personal bind wires a tile its person owns personally into their own partition of a partitioned tile: only that partition's XBIN_IFACE_<SLOT> and that person's frames list it (personal: true), and it lets calls through only from that partition while they still own the provider. live says whether it holds now; why, when not.",
			nil, nil, "{binds: [{id, user, requester, slot, provider, at, live, why?}]}"},
		{"POST", "/partitions/binds", "Partitions", "Add a personal bind", "a person, with their own session, app or device (PersonOnly: never tile code, view-as or the root token)",
			"The caller must own provider personally (403), read requester (403), and the policy ceiling must allow the edge (403). 409: requester isn't partitioned or is paused, slot isn't a multi:true http slot, provider is partitioned, doesn't provide the slot's service or exposes instances, or is already bound on the slot for everyone. Restarts only the caller's partition instance of requester; adding the same bind twice answers the existing one.",
			nil, bind("the bind", "requester", "slot", "provider"), "{ok, bind: {id, user, requester, slot, provider, at, live}}"},
		{"DELETE", "/partitions/binds", "Partitions", "Remove a personal bind", "its person, or admin",
			"{id} or {requester, slot, provider[, user]}. Restarts that person's partition instance of requester. 404 when nothing matches.",
			nil, bind("the bind to remove"), "{ok, removed: [{id, user, requester, slot, provider, at}]}"},
	}
}
