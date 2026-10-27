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
		{"GET", "/app-tunnel", tunnelTag, "The app tunnel's settings and state", "admin",
			"enabled, tunnelOnly (the app's traffic refused off the tunnel), relay {mode: n0|custom, url?, tokenHint?, next?: {url?, tokenHint?}} (tokens only as hints), env (XBIN_IROH_RELAY, when it sets the relay), the sidecar's state (disabled | unavailable | starting | running | degraded | failed), confined, exec, restarts, error?, endpointId, homeRelay, relayConnected, version, devices {total, tunnelPaired, classic, connected, synced}, conns [{endpoint, conn, path, rttMs}].",
			nil, nil, tunnelView},
		{"PUT", "/app-tunnel", tunnelTag, "Change the app tunnel's settings", "admin",
			"{enabled?, relay?: {mode: n0|custom, url?}, tunnelOnly?}: a custom relay is https (http only on loopback); changing it or enabled restarts the sidecar; tunnelOnly needs enabled, and disabling clears it; turning it on answers lockedOut, the devices not paired with the tunnel it locks out. 400 invalid; 409 when XBIN_IROH_RELAY sets the relay. → the view. Publishes app-tunnel.",
			nil, jsonBody("the changes", oapi{"enabled": boolean(), "tunnelOnly": boolean(),
				"relay": oapi{"type": "object", "description": "{mode: n0|custom, url?}"}}), tunnelView + " + lockedOut?"},
		{"POST", "/app-tunnel/relay-tokens", tunnelTag, "Rotate a custom relay's access token", "admin",
			"action: generate-next (a new token, answered once as token), set-next {token?, url?}, promote (next becomes current; the sidecar restarts), drop-next, set-current {token}, clear. Tokens 16–256 printable characters. Devices learn next from GET /app-tunnel/client before a promote (devices.synced counts them). 409 for n0's relays. → the view.",
			nil, jsonBody("the action", oapi{"action": str("generate-next | set-next | promote | drop-next | set-current | clear"), "token": str(""), "url": str("")}, "action"),
			tunnelView + " + token? (generate-next)"},
		{"POST", "/app-tunnel/test", tunnelTag, "Test the tunnel through its relay", "admin",
			"A throwaway endpoint dials this one through the relay.", nil, nil, "{reachable, path?, rttMs?, error?}"},
		{"GET", "/app-tunnel/client", tunnelTag, "What a paired device needs (the app)", "a device's own session, over its tunnel",
			"Stored by the device after every connect; next is a staged relay change it tries when the current relay refuses it. 403 anywhere else.",
			nil, nil, "{endpointId, ticket, serverKey, relay: {url?, token?, next?: {url?, token?}}, required}"},
		{"GET", "/devices/pairing/{id}", "Devices", "A pairing's state", "the login that minted its code",
			"state minted | handshaken | confirm | enrolled | rejected | abandoned | expired; sas (the confirmation code, six digits) while in confirm; device {name, platform} once the phone said; expires (unix: the code's, then the confirmation window's); reason (mismatch | expired | limit | refused) and error when it ended so; deviceId and replaced once enrolled. 404 for anyone else, or once it is forgotten.",
			[]oapi{pathParam("id", "tunnel.pairing of the enroll-code answer")}, nil, "{state, sas?, device?, expires, reason?, error?, deviceId?, replaced?}"},
		{"POST", "/devices/pairing/{id}/confirm", "Devices", "Confirm or refuse a pairing's code", "the login that minted its code",
			"{match: true} enrolls the device (replace: one of yours it replaces, at your limit — it goes in the same step); {match: false} (\"They don't\") burns the code. 409 {error, limit, count} at the limit without replace (the pairing keeps waiting); 409 {error, state} when it is no longer waiting (abandoned: the phone left).",
			[]oapi{pathParam("id", "the pairing")}, jsonBody("the answer", oapi{"match": boolean(), "replace": str("a device id")}, "match"), "{state, deviceId?, replaced?, reason?}"},
		{"POST", "/devices/{id}/tunnel", "Devices", "Pair an enrolled device with the tunnel (the app)", "the device's own session, signed in within 10 minutes, direct",
			"{noiseKey (32 bytes, base64url), endpoint (its EndpointId, 64 hex)} → as GET /app-tunnel/client. 403 over the tunnel, while the workspace requires it, or for another session; 409 when the tunnel is off or the key is another device's.",
			[]oapi{pathParam("id", "the device id")}, jsonBody("its tunnel keys", oapi{"noiseKey": str(""), "endpoint": str("")}, "noiseKey", "endpoint"),
			"{endpointId, ticket, serverKey, relay, required}"},
	}
}

const (
	tunnelTag  = "App tunnel"
	tunnelView = "{enabled, tunnelOnly, relay, env?, state, confined, exec, restarts, error?, endpointId, homeRelay, relayConnected, version, devices, conns}"
)
