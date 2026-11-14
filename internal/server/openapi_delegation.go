package server

// openapi_delegation.go — the delegation API (delegateapi.go, changeset.go;
// docs/protocol/delegation.md): endpoints() appends them.

func delegationEndpoints() []ep {
	const (
		tag     = "Delegation"
		person  = "the person, in their own session, app or device"
		orPage  = "the person, or their agent's own page under their login"
		orAgent = "the person, their agent's page, or their agent (a delegate call)"
		agent   = "the person's workspace agent only (a delegate call)"
	)
	id := []oapi{pathParam("id", "the id")}
	return []ep{
		{"GET", "/delegate/state", tag, "A person's agent: its switches and what waits for them", orAgent,
			"{user, workspace (the workspace's switch), paused, adminChanges, canAdminChanges (the person is a workspace or org admin), pendingRequests, standingApprovals, openChangeSets}.", nil, nil, "state"},
		{"PUT", "/delegate/state", tag, "Pause or resume your agent; let it propose admin changes", orPage,
			"{paused?, adminChanges?} → the state. Pausing refuses the agent's every call at once and cuts the ones running. adminChanges: the person's own session only (not the agent's page), and only a workspace or org admin can turn it on (403).", nil,
			jsonBody("switches", oapi{"paused": boolean(), "adminChanges": boolean()}), "state"},
		{"GET", "/delegate/workspace", tag, "The workspace's switch for workspace agents", "admin", "{enabled, by?, at?}. Off until an admin turns it on.", nil, nil, "{enabled, by, at}"},
		{"PUT", "/delegate/workspace", tag, "Turn workspace agents on or off for the workspace", "admin",
			"{enabled}. Off refuses every person's agent at once and cuts the calls running.", nil, jsonBody("switch", oapi{"enabled": boolean()}, "enabled"), "{enabled, by, at}"},
		{"GET", "/delegate/audit", tag, "What my agent did", person + "; an admin with ?user=",
			"{user, calls: [{ts, user, agent, method, path, tile?, class, effect?, dryRun?, status, refused?, ticket?, under?}]}, newest first: every side effect, tile call, refusal, read of code, logs or admin state, approval decision and applied change-set operation. Never a body, a query or a header. 13 months are kept. Never readable by the agent, its page, a tile or a view-as session.",
			[]oapi{queryParam("before", "rows older than this RFC 3339 time", false), queryParam("limit", "at most this many (default 200, max 1000)", false), queryParam("user", "admin: another person's log", false)}, nil, "{user, calls}"},
		{"POST", "/delegate/requests", tag, "Ask the person to approve one side effect", agent,
			"{method, path, body? | bodyBase64?, summary?} → the request, pending. path is the call exactly as it will be sent (under /api/, with its query); body what it will carry (≤ 256 KiB). Only a call that needs approval: a non-GET call to a tile's API, POST /term/sessions, POST /templates/new (400 otherwise). xbind judges it as the gate will (403 when the tile or its org refuses it anyway), stores it with its own preview — for a session start the tile, harness, mode, whether that mode asks before acting, network and API access — and tells the person (a `delegate` event, a push). 409 past 20 pending.", nil,
			jsonBody("the call", oapi{"method": str("POST | PUT | PATCH | DELETE"), "path": str("/api/<tile>/… or /api/xbin/term/sessions"), "body": str("the body, as text"), "bodyBase64": str("…or as standard base64"), "summary": str("the agent's own words, shown as the agent's (≤ 500 chars)")}, "method", "path"), "request"},
		{"GET", "/delegate/requests", tag, "The requests waiting for me", orPage,
			"{requests: [request]}, oldest first: {id, user, agent, kind (call | session | template), method, target, tile?, body?, bodyShown, bodySize, bodySha256, summary?, preview, state (pending | approved | denied | used | expired | withdrawn), grant?, created, decided?, expires, decidedVia?}. ?all=1 adds the ones decided in the last day.", []oapi{queryParam("all", "1: decided ones too", false)}, nil, "{requests}"},
		{"GET", "/delegate/requests/{id}", tag, "One request", orAgent, "The agent polls this: state approved means its id is a ticket now.", id, nil, "request"},
		{"POST", "/delegate/requests/{id}/approve", tag, "Approve a request", orPage,
			"{grant?: once | hour | always} → {request, approval?}. once (the default): the request becomes a one-shot ticket for exactly that method, path and body, good for 15 minutes — the agent sends its id as X-XBin-Ticket. hour / always (a tile API call only): also a standing approval of that tile's API. 409 when it is no longer pending.", id,
			jsonBody("grant", oapi{"grant": str("once | hour | always")}), "{request, approval?}"},
		{"POST", "/delegate/requests/{id}/deny", tag, "Deny a request", orPage, "→ {request}. 409 when it is no longer pending.", id, nil, "{request}"},
		{"DELETE", "/delegate/requests/{id}", tag, "Withdraw a request", orAgent, "A pending or approved request the agent no longer wants. → the request.", id, nil, "request"},
		{"GET", "/delegate/approvals", tag, "The standing approvals", orAgent, "{approvals: [{id, user, tile, until? (absent: until revoked), created, via?, request?}]}: the tiles whose API the agent may write to without a ticket.", nil, nil, "{approvals}"},
		{"POST", "/delegate/approvals", tag, "Let the agent write to a tile's API without asking", orPage,
			"{tile, grant: hour | always} → the approval. One a tile (a new one replaces it). A tile the person can read (404 otherwise). Never covers a session start or a tile create.", nil, jsonBody("approval", oapi{"tile": str("tile path"), "grant": str("hour | always")}, "tile", "grant"), "approval"},
		{"DELETE", "/delegate/approvals/{id}", tag, "Revoke a standing approval", orPage, "204. The agent's next write needs a ticket again.", id, nil, "204"},
		{"POST", "/delegate/changesets", tag, "Propose admin changes", agent,
			"{title, items: [{kind, args, why?}]} → the change set, proposed; nothing is applied. Only for an admin who opted in (adminChanges; 403). kind: grant.add | grant.revoke {from, target, role}; binding.set {component, slot, provider | providers, host?, zone?, listen?, add?}; binding.remove {component, slot, provider?, host?, zone?, listen?}; org.share {org, tile, level}; limits.set {tile, kind, value} — no other (400). Each item comes back with xbind's own line, before, after, effects, warnings and blocked (the handler's refusal, when it would refuse). At most 20 items; 409 past 10 open sets.", nil,
			jsonBody("change set", oapi{"title": str("what it is for"), "items": oapi{"type": "array", "items": oapi{"type": "object"}}}, "items"), "change set"},
		{"GET", "/delegate/changesets", tag, "My agent's change sets", orAgent, "{changesets: [{id, user, agent?, title, undoOf?, state (proposed | applying | applied | partial | rejected | expired | withdrawn), created, decided?, expires, items: [{n, kind, args, why?, line, before?, after?, effects?, warnings?, blocked?, inverse?, result?, error?}]}]}. A proposal expires after 7 days.", nil, nil, "{changesets}"},
		{"GET", "/delegate/changesets/{id}", tag, "One change set", orAgent, "", id, nil, "change set"},
		{"DELETE", "/delegate/changesets/{id}", tag, "Withdraw a change set", orAgent, "A proposal nobody decided yet. → the change set.", id, nil, "change set"},
		{"POST", "/delegate/changesets/{id}/approve", tag, "Approve a change set and apply it", person + " — the person whose agent proposed it",
			"{items?: [n, …], password?} → the change set with each item's result: applied | failed | stale | skipped | rejected | blocked. Step-up, as for adding a device: the sign-in is under 10 minutes old or the body carries the password (403 {error, stepUp: password | signin}). xbind applies the chosen items (absent: all that aren't blocked) in order as the person, through the admin handlers everyone uses. An item whose state changed since it was proposed is stale and not applied; after the first failure the rest are skipped; what was applied stays (…/undo proposes the set that undoes it).", id,
			jsonBody("approval", oapi{"items": oapi{"type": "array", "items": integer()}, "password": str("step-up: the account password")}), "change set"},
		{"POST", "/delegate/changesets/{id}/reject", tag, "Reject a change set", person, "→ the change set. 409 when it is no longer proposed.", id, nil, "change set"},
		{"POST", "/delegate/changesets/{id}/undo", tag, "Propose the set that undoes an applied one", person, "→ a new change set of the person's own (undoOf), holding the inverse of each applied item that has one, newest first — approved like any other. 409 when nothing of it can be undone by xbind.", id, nil, "change set"},
	}
}
