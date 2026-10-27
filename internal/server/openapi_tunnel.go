package server

// openapi_tunnel.go — the app tunnel and device-limit routes (D204,
// docs/protocol.md §Devices, docs/app-tunnel.md). endpoints() appends them.

func appTunnelEndpoints() []ep {
	limitRow := "{user, name?, override?, limit, count}"
	return []ep{
		{"GET", "/devices/limits", "Devices", "App device limits", "xbin:users",
			"How many app devices one person may have paired: default is the workspace's (1 unless set), max the cap (32). atLimit: the people at or past their limit (past it only when it was lowered — devices already paired stay); overrides: the people with their own (PATCH /users/{id} {maxDevices}). Rows: " + limitRow + ".",
			nil, nil, "{default, max, atLimit:[row], overrides:[row]}"},
		{"PUT", "/devices/limits", "Devices", "Set the workspace device limit", "xbin:users",
			"{default: 1–32}: enforced when a device is added — nobody loses a device to it. 400 out of range. → the GET view.",
			nil, jsonBody("the default", oapi{"default": integer()}, "default"), "{default, max, atLimit, overrides}"},
	}
}
