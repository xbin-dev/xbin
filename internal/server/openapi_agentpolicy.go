package server

// openapi_agentpolicy.go — org constraints on agents, the connection view
// and a person's agent preferences (D229; docs/protocol/orgs.md,
// docs/protocol/agents.md). endpoints() appends them.

func agentPolicyEndpoints() []ep {
	rule := "\"any\", or a list of ids"
	rules := oapi{"workspaceAgent": str("on | off (null: on)"), "harnesses": freeRule(rule + " of GET /agent/providers (null: any)"),
		"models": freeRule("\"any\", \"private\" (a model of an upstream the LLM connector marks private), or a list of LLM connector ids — an upstream for all its models, or upstream/model (null: any)")}
	bounds := oapi{"credentials": str("own-first (a person's own sign-in when they have one, else the LLM connector) | connector (forced for everyone)")}
	for k, v := range rules {
		bounds[k] = v
	}
	return []ep{
		{"GET", "/agents/policy", "Orgs", "The workspace's bounds on agents", "a person, or xbin:users; never a tile's own credentials",
			"{workspaceAgent, harnesses, models, credentials, knownHarnesses, canSet}: the outer rules every tile falls under (an org only narrows them), whose credentials serve a coding agent's model calls by default, the harness ids a list may name, and whether the caller may PUT. With nothing set: on, any, any, own-first.",
			nil, nil, "{workspaceAgent, harnesses, models, credentials, knownHarnesses, canSet}"},
		{"PUT", "/agents/policy", "Orgs", "Set the workspace's bounds on agents", "xbin:users",
			"Each key named is set, the rest keep their value; null puts a rule back to its default. 400 an unknown harness or a malformed id. Orgs' own rules are left as they are: every call is asked against the bounds and the tile's orgs, so a tightened bound holds at once. Sessions already running are never ended. Audited; publishes users and agents events.",
			nil, jsonBody("the bounds", bounds), "{workspaceAgent, harnesses, models, credentials, knownHarnesses, canSet}"},
		{"PUT", "/orgs/{org}/agents", "Orgs", "Set an org's constraints on agents", "xbin:users, or an admin of the org signed in themselves (never a tile driven by one)",
			"What agents may do on the tiles the org owns and the tiles shared into it: whether people's workspace agents may work there, which coding-agent harnesses an agent session may run, which models may see their data. Each key named is set, the rest keep their value; null puts a rule back to its default. The result must sit within the workspace's bounds — an org narrows them, never widens (400 says which entry reaches past). A tile under several orgs gets the strictest of them. 404 no such org. Sessions already running are never ended. GET /orgs carries agents. Audited; publishes users and agents events.",
			[]oapi{pathParam("org", "org id")}, jsonBody("the org's rules", rules), "{agents, bounds}"},
		{"GET", "/agents/connection", "Agent sessions", "What a workspace agent session is connected to", "a person, for themselves (their session, app, device or workspace agent); never a tile's credentials",
			"Every org the caller has to do with and every tile they can read, each connected | hidden (left out of the scope) | blocked (a rule of the workspace or of one of the tile's orgs refuses it: reason, by, rule) | no-access (the scope names it, the caller can't see it — or it doesn't exist). " +
				"{model?, scope, bounds, orgs: [{id, name, kind: org|workspace, state, reason?, rule?, tiles, connected, rules?}], tiles: [{tile, name?, org, orgs?, state, reason?, by?, rule?, harnesses}], counts: {tiles, orgs, notConnected, hidden, blocked, noAccess, summary}, whatIf?: {model, connect: [tile row], disconnect: [tile row], counts}}. " +
				"counts.summary reads like \"14 tiles · 3 orgs connected · 6 not connected\". The model is resolved by the LLM connector: a caller can't claim an upstream is private; with no model, only a models rule of any is satisfied. Blocked wins over hidden.",
			[]oapi{queryParam("model", "the session's model, upstream/model", false),
				queryParam("orgs", "the scope's orgs, comma-separated or repeated (workspace: the tiles in no org); with tiles empty too: everything the caller can see", false),
				queryParam("tiles", "the scope's single tiles", false),
				queryParam("whatIf", "another model: adds what changing to it would connect and disconnect", false)}, nil,
			"{model?, scope, bounds, orgs, tiles, counts, whatIf?}"},
		{"GET", "/agent/prefs", "Agent sessions", "The caller's agent preferences", "a person's own session, app or device; never a tile's credentials, view-as or the root token (403)",
			"{harness, lastKind, harnesses: {<id>: {mode?, model?, options?, credentials?}}, agentSessions: {start: ask|allow, modes?: {<id>: mode}, net?, api?}} — defaults filled in. harness: the harness a new agent session starts with; harnesses: what each starts with (credentials: own | connector, absent follows the workspace default); agentSessions: what the person's workspace agent may start a tile agent session with — start ask (the default) asks them first.",
			nil, nil, "{harness, lastKind, harnesses, agentSessions}"},
		{"PUT", "/agent/prefs", "Agent sessions", "Change the caller's agent preferences", "a person's own session, app or device; never a tile's credentials, view-as or the root token (403)",
			"A JSON merge patch (RFC 7386): keys present replace, null removes, objects merge. 400: an unknown key, a harness this xbind doesn't run, a mode that isn't one of the harness's, or a bypass mode (explicit: picked per session, never saved; agentSessions.modes takes only modes known to keep asking). → the preferences now.",
			nil, jsonBody("the change", oapi{"harness": str("a harness id, or \"\""), "lastKind": str("shell | agent"),
				"harnesses":     object("{<id>: {mode?, model?, options?: {<id>: value}, credentials?: own|connector} | null}"),
				"agentSessions": object("{start?: ask|allow, modes?: {<id>: mode}, net?, api?}")}), "{harness, lastKind, harnesses, agentSessions}"},
	}
}

// freeRule is a rule that is a word or a list.
func freeRule(desc string) oapi { return oapi{"description": desc} }
