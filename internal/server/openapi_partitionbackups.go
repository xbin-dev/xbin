package server

// openapi_partitionbackups.go — a person's partition's archives
// (plans/partitions/11-backup-encryption.md §2-§4, docs/partitions.md
// §Backups): endpoints() appends them, at the end of its chain.

func partitionBackupEndpoints() []ep {
	return []ep{
		{"GET", "/partitions/backups", partModeTag, "List a person's partition's archived versions",
			"the person, in their own session, app or device; an admin for anyone's (the root token, the admin tile's frame under their login)",
			"The archiver's versions of the archive of user's partition of tile (.partitions.<TileKey>.<dep>.<partitionId>, dep the primary), sealed under the partition's own backup key. partitionId defaults to the person's current one, the only one a person may name (403 otherwise, before the archiver is asked, never saying whose it is); an admin names an earlier holder's (the id deleted and recreated since) to list theirs. A tile's backend, frame, terminal or agent session: 403.",
			[]oapi{queryParam("tile", "the tile", true), queryParam("user", "whose partition (default: the caller)", false),
				queryParam("partitionId", "the archive's partition id, u-<32 hex> (default: the person's current one)", false)}, nil,
			"{tile, partition, partitionId, deployment, partitioned, archiver, versions:[{version,time,size}]}"},
		{"POST", "/partitions/restore", partModeTag, "Restore a person's partition from its archive",
			"the person, in their own session, app or device; an admin for anyone's (the root token, the admin tile's frame under their login)",
			"Replaces user's partition of tile — its data, vault and registrations — with an archive of it, after a typed confirm (\"<tile> user:<id>\"); its instance is stopped and its namespace held meanwhile. Only a sealed partition archive, under the partition's own backup key, of the same tile and user id restores: an archive of an earlier holder of the id (partitionId; the id deleted and recreated since) only by an admin with to: the id again (audited, the person told); never into another id (403/400); a person names only their own current partitionId (403). Everything is judged again under the tile's backup lock: a switch, reset, purge, sweep or erase that ran meanwhile wins (409, nothing restored). 409 while the tile isn't partitioned now, and for an archive whose key was erased (\"this backup's data was erased on <date> (<reason>)\") or that another workspace sealed (import its keys). vaultSkipped: a vault another vault sealed. dryRun checks and writes nothing.",
			nil, jsonBody("the restore", oapi{"tile": str("apps/x"), "user": str("whose partition (default: the caller)"),
				"partitionId": str("the archive's partition id (default: the person's current one)"), "version": str("default: the latest"),
				"confirm": str("\"<tile> user:<id>\", typed"), "to": str("an earlier holder's archive: the id again (admin)"), "dryRun": boolean()}, "tile"),
			"{ok, tile, partition, partitionId, from, version, resources, earlierHolder, data, vault, registrations, skipped?, vaultSkipped?}"},
	}
}
