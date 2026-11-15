package server

// openapi_sealed.go — the sealed components route (D227,
// docs/protocol/sealed.md). endpoints() appends it.

const sealedTag = "System components"

func sealedEndpoints() []ep {
	return []ep{
		{"GET", "/sealed", sealedTag, "The system (sealed) components of this xbind", "admin",
			"The components whose code is the running xbind release's own (docs/sealed.md): each one's path (xbin/<name>), title, the digest of the code this release runs for it, where its backend comes from (build: release | development | none), its lifecycle state, why it is held when it can't run (hold: {code: no-build | foreign-data | newer-data | stamp | needs-sandboxes, reason}), its backend's state, its partition mode and whether people's partitions run in this xbind, the data format this release reads and the stamp of its data ({data, release, digest, at}), its declared interface slots with what each is bound to, the grants the release gives it and whether it may act for its people (delegate). Read-only: an admin changes who has access (PUT /access: read or none), enables and disables it (POST /lifecycle), sets its limits (POST /limits) and binds its declared slots (POST /bindings) with the routes a tile has; every other route answers a system component's path as docs/protocol/sealed.md lists. The page /xbin/system and `bx system` read this.",
			nil, nil, "{release, components: [{path, name, title, runtime, digest, build?, state, hold?, backend?, partition, partitioned, dataFormat, stamp?, slots: [{slot, kind, service?, multi?, bound}], grants, delegate?}]}"},
	}
}

// sealedQuery is the ?sealed=1 switch of a listing (WantsSealed).
func sealedQuery() oapi {
	return queryParam("sealed", "1: include system (sealed) components' rows, each marked sealed:true where the row has the field — left out otherwise, so a client that predates them never lists one (docs/sealed.md)", false)
}
