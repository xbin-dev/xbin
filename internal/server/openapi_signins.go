package server

// openapi_signins.go — the coding-agent sign-ins xbind holds for each
// person (D230; docs/protocol/signins.md). endpoints() appends them.

func signinsEndpoints() []ep {
	const person = "a person's own session, app or device; never a tile's credentials, a terminal's or an agent session's token, view-as, the root token or their workspace agent (403)"
	const admin = "a workspace admin acting themselves (xbin:admin); never view-as or a delegate"
	signin := "{id, harness, name, kind: setup-token|api-key, env, createdAt, lastUsedAt?, expiresAt?, default, refusedAt?, refused?}"
	flow := "{id, harness, url, paste, expiresAt}"
	return []ep{
		{"GET", "/agent/signins", "Agent sessions", "The caller's saved coding-agent sign-ins", person,
			"{available, why?, unisolated?, vault: open|sealed|locked|plaintext, policy: {enabled, locked?, reason?, trust: {allowed, locked?, reason?}}, trusted: [{id, tile, name?, at, via, holds, why?}], notice?: {at, text}, admin?, harnesses: [{id, name, guided, keys: [{env, label, kind}], login, max, signins: [" + signin + "], here?: {inject, why?, trust?, without}}], flows: [" + flow + "]} — metadata only: no route answers a secret or a part of one. available false: this xbind keeps none (why says: it runs without sandboxes). harnesses lists the coding agents that take a credential xbind can keep; guided: xbind can run its own sign-in. With ?tile=, here says whether a session of the caller's on that tile would get a saved sign-in of the harness — the shared-tile rule's answer, whether or not one is saved yet — and why not. notice: sign-ins were removed by someone else. flows: the caller's guided sign-ins under way.",
			[]oapi{queryParam("tile", "a tile the caller may open a terminal on (or a directory in one): adds here to each harness — whether a session there would get a saved sign-in, why not, and trust: the question \"I trust this tile's source\" where the tile's source isn't only the caller's ({tile, name?, basis, ownerKind: you|person|org|workspace, owner?, ownerName?, admin?, editors?, names?, allowed, forbidden?} — account ids, display names beside them)", false)}, nil, "{available, vault, policy, harnesses, flows}"},
		{"POST", "/agent/signins", "Agent sessions", "Save a pasted key or token", person,
			"Keeps secret as the caller's sign-in for harness: the variable it goes to is picked by its prefix (Claude Code: sk-ant-oat… is a subscription token, anything else an API key). The first sign-in of a harness is its default; default: true makes this one. A name already used for the harness replaces that sign-in's secret (its id stays) and ends the sessions running on the old one. Nothing is checked against the vendor. 400 a secret that isn't one line of printable characters without spaces, quotes, backslashes, <, > or &; 409 the harness takes no saved credential, 8 are kept already, or this xbind keeps none; 403 the workspace's switch is off; 503 the vault is sealed or not set up.",
			nil, jsonBody("the sign-in", oapi{"harness": str("a harness id of GET /agent/signins"), "secret": str("the key or token, alone"),
				"name": str("up to 40 characters; absent: the harness's name, then \"… 2\""), "default": boolean()}, "harness", "secret"), "{signin}"},
		{"PUT", "/agent/signins/{id}", "Agent sessions", "Rename a sign-in, make it the default, or replace its secret", person,
			"Each key named is applied. default takes true only (a harness keeps one default: make another one the default instead). A new secret ends the sessions running on the old one. 404 not the caller's.",
			[]oapi{pathParam("id", "sign-in id")}, jsonBody("the change", oapi{"name": str("the new name"), "default": boolean(), "secret": str("a new key or token")}), "{signin}"},
		{"DELETE", "/agent/signins/{id}", "Agent sessions", "Forget a sign-in", person,
			"Removes it — whatever the vault's state — and ends the caller's running agent sessions that carry it (they hold a copy in their environment). The credential stays valid at its vendor until it expires or is revoked there. → {ok, ended}.",
			[]oapi{pathParam("id", "sign-in id")}, nil, "{ok, ended}"},
		{"DELETE", "/agent/signins/notice", "Agent sessions", "Dismiss the notice", person,
			"The notice of GET /agent/signins was read.", nil, nil, "{ok}"},
		{"GET", "/agent/signins/uses", "Agent sessions", "Where the caller's sign-ins were used", person,
			"{uses: [{at, id, name, harness, tile, session, origin: person|agent}]}, newest first, the newest 500: one row per agent session a sign-in was put into.",
			nil, nil, "{uses}"},
		{"POST", "/agent/signins/guided", "Agent sessions", "Start a guided sign-in", person,
			"xbind runs the harness's own credential-printing sign-in (Claude Code: claude setup-token) in a throwaway sandbox that holds nothing of a tile, and answers the page to open once the CLI printed it: 202 {flow: " + flow + "}. url is https on the harness's own hosts; paste: the CLI asks for the code the page shows. One per person and harness (a second start answers the same flow); it ends by itself after 15 minutes. 409 the harness has no such sign-in (paste a key) or this xbind keeps none; 429 too many under way; 502 the CLI could not be started or ended without a link; 504 it printed none in time.",
			nil, jsonBody("the sign-in to make", oapi{"harness": str("a harness id whose guided is true"), "name": str("what to save it as"), "default": boolean()}, "harness"), "{flow}"},
		{"POST", "/agent/signins/guided/{flow}", "Agent sessions", "Hand a guided sign-in its code", person,
			"Types the code into the CLI and waits for its word (60 s): 201 {signin} — the credential it printed is kept, never answered; 409 the CLI refused the code as malformed and asks again (the flow stays open); 502 it refused the sign-in (its reason line, credentials masked) or ended; 504 no word. Every answer but 409 ends the flow. 404 not the caller's flow.",
			[]oapi{pathParam("flow", "flow id")}, jsonBody("the code", oapi{"code": str("the code the sign-in page showed")}, "code"), "{signin}"},
		{"DELETE", "/agent/signins/guided/{flow}", "Agent sessions", "Cancel a guided sign-in", person,
			"Ends the flow and its sandbox. 404 not the caller's flow.", []oapi{pathParam("flow", "flow id")}, nil, "{ok}"},
		{"GET", "/agent/signins/people", "Agent sessions", "Who has saved sign-ins", admin,
			"{available, why?, policy, vault, people: [{user, total, signins: [{id, harness, kind, createdAt, lastUsedAt?, expiresAt?, default}]}]} — that people have sign-ins, never what: no name (a person's own label) and no secret.",
			nil, nil, "{policy, vault, people}"},
		{"DELETE", "/agent/signins/people/{user}/{id}", "Agent sessions", "Revoke one of a person's sign-ins", admin,
			"Removes it, ends that person's sessions running on it, and leaves them a notice naming who did it. 404 no such sign-in. Audited.",
			[]oapi{pathParam("user", "user id"), pathParam("id", "sign-in id")}, nil, "{ok, ended}"},
		{"PUT", "/agent/signins/policy", "Agent sessions", "Set the workspace's switches on saved sign-ins", admin,
			"enabled: whether people may save sign-ins and have them used, on by default. Off: nothing is put into a session, nothing can be saved or made default, and the running sessions that carry a saved sign-in are ended (ended: how many); what is saved stays listed and removable, and works again when the switch returns. trust: whether people may trust a tile whose source isn't only their own (POST /agent/signins/trust) and have their sign-in used there, allowed by default; off, the sessions that ran on such a trust are ended and the card says an admin can change it. 409 the host's file sets the one written (agentSignIns, agentSignInTrust in XBIN_LLM_CONFIG). Audited.",
			nil, jsonBody("the switches", oapi{"enabled": boolean(), "trust": boolean()}), "{policy, ended}"},
		{"POST", "/agent/signins/trust", "Agent sessions", "Trust a tile's source", person,
			"\"I trust this tile's source\": the caller's own answer for a tile — or a directory in one — they may open a terminal on, whose source isn't only their own (it is owned by someone else, or someone else who isn't an admin can change it). Their saved sign-in is then used in their sessions there. basis is the question's own token (GET /agent/signins?tile= → here.trust.basis): the person trusts what they were shown — 409 {error, changed: true, trust} when the tile is no longer that, with the question as it stands now, and nothing recorded. Kept for the tile as it stood: the tile itself at its path, its owner and who could change it — void from the moment xbind sees any of that change (someone new, another owner, a move, a tile made again), and void until answered again. 201 {trust: {id, tile, name?, at, via, holds}}. 409 the tile is the caller's alone (nothing to trust) or can't be told from another at its path; 403 trusting is switched off for the workspace; 404 no such tile. Only a person's own session, app or device: never a tile's credentials, a person's agent, view-as or the root token. Audited.",
			nil, jsonBody("the answer", oapi{"tile": str("a tile path, or a directory inside one"), "basis": str("the question's basis token")}, "tile", "basis"), "{trust}"},
		{"DELETE", "/agent/signins/trust/{id}", "Agent sessions", "Stop trusting a tile's source", person,
			"Removes the caller's answer and ends their sessions on that tile that run on a saved sign-in (ended: how many). 404 not theirs.",
			[]oapi{pathParam("id", "trust id")}, nil, "{ok, ended}"},
	}
}
