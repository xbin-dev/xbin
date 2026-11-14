package server

// openapi_llm.go — the LLM connector's routes (D226, docs/protocol/llm.md,
// docs/llm.md). endpoints() appends them.

const llmTag = "LLM"

func llmEndpoints() []ep {
	const (
		caller = "a person the workspace's people switch admits, a component whose llm slot is bound to xbin:llm, or an LLM key (Authorization: Bearer xbl_…)"
		admin  = "admin"
		view   = "{configured, enabled, people, upstreams:[{name, kind, baseURL, key:{set, source?, hint?}, models, private, prices?, source, locked, reason?}], defaults:{model?, roles}, aliases, quotas, concurrency, sessions:{agents, terminals, openaiEnv}, locks:{<path>:{locked, reason}}, host:{active, lockAll?, error?}, vault, problems, storeError?, notices?, set?, by?}"
		oaiErr = " Errors are OpenAI-shaped, {error: {message, type, param, code}}: 400 xbin_llm_body | xbin_llm_model | xbin_llm_unsupported; 401 invalid_api_key; 403 xbin_llm_not_bound | xbin_llm_people_off | xbin_llm_policy | xbin_llm_edge (a non-primary deployment under its tile's edge policy); 404 model_not_found; 429 xbin_llm_quota_exceeded (with quota, resets, limit, used, and Retry-After) | xbin_llm_busy (the caller's calls in flight are at the workspace's concurrency); 503 xbin_llm_not_configured | xbin_llm_disabled | xbin_vault_sealed; 502/504 xbin_llm_upstream…; an upstream's own error passes with its status (its 401/403 as 502 xbin_llm_upstream_auth)."
	)
	quota := oapi{"type": "object", "description": "{tokensPerDay?, tokensPerMonth?, costPerDay?, costPerMonth?, action?: stop|warn} — 0 not set, -1 unlimited; null removes"}
	return []ep{
		{"POST", "/llm/v1/chat/completions", llmTag, "Chat Completions through the workspace's LLM connector", caller,
			"OpenAI's Chat Completions wire, relayed to the upstream the model names (<upstream>/<model>, an alias, xbin/default, xbin/<role>, or none: the default). Every member sent reaches the upstream (written anew, each once) except model (the upstream's own name) and, on a stream, stream_options.include_usage (always true: the last chunk carries usage); model is a string of at most 200 characters without spaces and stream a boolean. The answer streams as it arrives; a client that goes away ends the upstream call after a grace of 5 s in which the answer is still read for its usage, and a call that ends without usage is metered as an estimate. Body at most 32 MiB. Metered to the acting person and the component; X-XBin-LLM-Quota on the answer when a warn quota is reached." + oaiErr,
			nil, jsonBody("an OpenAI Chat Completions request", oapi{"model": str("<upstream>/<model>, an alias, xbin/default, xbin/<role>; absent: the default model"), "messages": oapi{"type": "array"}, "stream": boolean()}),
			"an OpenAI chat completion, or its SSE stream"},
		{"POST", "/llm/v1/responses", llmTag, "Responses API through the workspace's LLM connector", caller,
			"OpenAI's Responses wire, relayed like chat completions; only model is rewritten, and the stream's events pass untouched (usage is read from response.completed). background: true is refused (400 xbin_llm_unsupported)." + oaiErr,
			nil, jsonBody("an OpenAI Responses request", oapi{"model": str("as for chat completions"), "input": freeSchema("the input"), "stream": boolean()}),
			"an OpenAI response, or its SSE stream"},
		{"GET", "/llm/v1/models", llmTag, "The models the connector offers", caller,
			"OpenAI's model list: every upstream's models as <upstream>/<model> (an upstream's own model objects kept: context sizes pass through), the aliases and xbin/default, xbin/<role> with alias_of. upstream_errors names upstreams whose own list couldn't be read (detail for admins)." + oaiErr,
			nil, nil, "{object: \"list\", data: [{id, object, owned_by, alias_of?, …}], upstream_errors?}"},
		{"GET", "/llm", llmTag, "The connector as the caller meets it", "authenticated",
			"configured (an upstream exists), enabled, callable (the caller's calls would be admitted; reason and code when not), url (the OpenAI base, /api/xbin/llm/v1), ref (xbin:llm, what an llm slot binds to), people, host (a deployment file is in force), defaults, the caller's user and component, and quota {user, orgs?, limits, workspace, state: ok|warn|stop} — each limit {name, value, source, action, used, resets, state}. Always 200.",
			nil, nil, "{configured, enabled, callable, reason?, code?, url, ref, people, host, defaults, sessions, user?, component?, admin?, quota?}"},
		{"POST", "/llm/keys", llmTag, "An LLM key for a harness a bound tile runs", "a component whose llm slot is bound to xbin:llm, with its own credential",
			"A narrow key (xbl_…, good for /llm/v1/… only) for a coding tool the tile runs where its own credential must not go — a sandbox. It is metered as the tile's own calls are: to the person the credential acts for (a partition's person, the person driving the frame or terminal, nobody for a backend that serves everyone) and to the tile. user, when named, must be that person (403 otherwise). ttlSec: default 24 h, at most 30 days. label and sandbox (≤200 characters) are recorded for the log. The token is answered once. 403 a person's sign-in, an LLM key, or a tile that isn't bound; 503 the connector is not configured or off.",
			nil, jsonBody("the key", oapi{"user": str("the person it is for (default, and only: the one this credential acts for)"), "ttlSec": integer(),
				"label": str("what it is for"), "sandbox": str("the sandbox it is handed to")}),
			"{id, token, user, component, expires, url}"},
		{"DELETE", "/llm/keys/{id}", llmTag, "End an LLM key", "the component that asked for it",
			"Revokes the key now. 204 whether or not it still lived (a key of another component is not touched).",
			[]oapi{pathParam("id", "the key's id")}, nil, ""},
		{"DELETE", "/llm/keys", llmTag, "End the LLM keys asked for under a label", "the component that asked for them",
			"Revokes every key this component asked for with that label for the person its credential acts for: a consumer that names a run in the label needn't keep key ids, and can end a run's key after its own restart. 204 whatever lived; 400 without label.",
			[]oapi{queryParam("label", "the label the keys were asked for with", true)}, nil, ""},
		{"GET", "/llm/preferred", llmTag, "The workspace's default models (the llm-gw convention)", caller,
			"use=<role>: {use, model} — that role's model as <upstream>/<model>, the default model when the role has none, \"\" when neither. No use: {preferred: {default?, <role>…}, useTypes}. The shape the llm-gw tile answers, so a tile resolves its default model from either provider." + oaiErr,
			[]oapi{queryParam("use", "a role: agent, harness, … (default: all)", false)}, nil, "{use, model} | {preferred, useTypes}"},
		{"GET", "/llm/config", llmTag, "The connector's configuration in force", admin,
			"The admin's settings and the host's deployment file (XBIN_LLM_CONFIG) merged. locks maps each setting the file sets (enabled, people, upstreams.<name>, defaults.model, defaults.roles.<role>, aliases.<alias>, quotas.workspace|default|users|orgs; \"*\": all of them) to {locked: true, reason: \"set by your host\"}; a host's upstream is locked too. No key is ever answered: key {set, source: vault|host, hint} (hint: an admin's key's last four characters, while the vault is open). vault: open | sealed | locked | plaintext. problems: defaults and aliases that resolve to nothing.",
			nil, nil, view},
		{"PUT", "/llm/config", llmTag, "Change the connector's switches, default models and aliases", admin,
			"Only what is named changes: enabled, people (admins | everyone | nobody: who may call directly), defaults {model?, roles?: {<role>: <model>}}, aliases {<alias>: <model>} (\"*\": every unknown model name; never a name under an upstream or a listed model's), concurrency (calls one caller may have in flight, 1–1024; 0: the default, 16), sessions {agents?, terminals?, openaiEnv?} (what agent sessions and terminals get of it; all booleans). \"\" removes a role, an alias or the default. 400 invalid; 409 {error, locked: [<path>…], reason} when a named setting is the host's (sending a locked setting back unchanged is fine). → the view. Audited.",
			nil, jsonBody("the changes", oapi{"enabled": boolean(), "people": str("admins | everyone | nobody"),
				"defaults": oapi{"type": "object", "description": "{model?, roles?: {<role>: <model>}}"}, "aliases": oapi{"type": "object", "description": "{<alias>: <model>}"}, "concurrency": integer(),
				"sessions": oapi{"type": "object", "description": "{agents?: bool, terminals?: bool, openaiEnv?: bool}"}}), view},
		{"PUT", "/llm/upstreams/{name}", llmTag, "Add or change an upstream", admin,
			"name: lowercase letters, digits, . _ - (not xbin). A new upstream needs baseURL (http(s), the provider's OpenAI-compatible base, with or without /v1); what isn't named stays. key is stored in xbind's vault and never answered (\"\" removes it), and a change of baseURL's scheme, host or port without key removes the stored key (notices); models lists what is offered (empty: the upstream's own /models and any model name pass through); private marks it for org constraints; prices {<model>: {in, out, cachedIn?}} in USD per million tokens. 400 invalid; 409 {locked} for a host's upstream, or any new one when the host locks all; 409 when a key is given and the vault is sealed or not set up. → the view. Audited.",
			[]oapi{pathParam("name", "the upstream")}, jsonBody("the upstream", oapi{"baseURL": str("https://api.openai.com/v1"), "key": str("the provider's API key"),
				"models": arr(), "private": boolean(), "prices": oapi{"type": "object"}, "kind": str("openai")}), view},
		{"DELETE", "/llm/upstreams/{name}", llmTag, "Remove an upstream", admin,
			"The upstream and its key go; usage already metered under its name stays. 404 unknown; 409 {locked} for a host's. → the view. Audited.",
			[]oapi{pathParam("name", "the upstream")}, nil, view},
		{"POST", "/llm/upstreams/{name}/test", llmTag, "Test an upstream with a small call", admin,
			"The smallest chat completion (one word in, one token out) with model (default: the upstream's first listed model); for an upstream without a model list and no model given, its /models list instead. A call like any other: metered to the admin who ran it, 429 at their quota or the workspace's, 409 while the connector is off. 200 whatever the upstream answered: {ok, model?, models?, status?, ms, tokens?, error?}. 400 a model off the upstream's list; 404 unknown.",
			[]oapi{pathParam("name", "the upstream")}, jsonBody("the model to call", oapi{"model": str("the upstream's own model name")}), "{ok, model?, models?, status?, ms, tokens?, error?}"},
		{"GET", "/llm/usage", llmTag, "What was used", "a person's own sign-in (their own rows), or admin (every row)",
			"Counters per UTC day (period=day, default: this month so far) or month (period=month, default: the last 12), person, component and model: rows [{day|month, user, component, model, requests, failed?, inputTokens, outputTokens, cachedTokens?, reasoningTokens?, cost, unpriced?, estimated?}], totals, scope: own | all. estimated counts the requests charged an estimate (their answer ended without the provider's usage); model is <upstream>/(unknown) for names neither the configuration nor the upstream lists. user \"owner\" is the root token, \"\" a component acting for nobody; component \"\" a direct call. cost in USD covers the requests whose model has a price (unpriced counts the others). Filters: user (admins), component, model. 403 for a tile's credentials and for another person's rows.",
			[]oapi{queryParam("period", "day | month", false), queryParam("from", "YYYY-MM-DD (UTC)", false), queryParam("to", "YYYY-MM-DD (UTC)", false),
				queryParam("user", "one person's rows (admins)", false), queryParam("component", "one component's rows", false), queryParam("model", "one model's rows (<upstream>/<model>)", false)},
			nil, "{period, from, to, scope, rows, totals}"},
		{"GET", "/llm/quotas", llmTag, "The quota rules in force", admin,
			"quotas {workspace?, default?, users?, orgs?} and locks (quotas.workspace|default|users|orgs the host's file sets). user=<id>: also effective, that person's limits as they stand ({user, orgs?, limits, workspace, state}).",
			[]oapi{queryParam("user", "also answer this person's limits", false)}, nil, "{quotas, locks, effective?}"},
		{"PUT", "/llm/quotas", llmTag, "Change quota rules", admin,
			"Only what is named changes: workspace (everyone together), default (each person), users and orgs {<id>: quota}. A person's quota is their own override, else the most generous of their orgs' per limit, else the default — each limit on its own; a default the host's file sets is a ceiling these entries only narrow. Windows are UTC days and months. 400 invalid; 409 {locked} for a part the host's file sets. → as GET. Audited.",
			nil, jsonBody("the rules to change", oapi{"workspace": quota, "default": quota,
				"users": oapi{"type": "object", "description": "{<user id>: quota | null}"}, "orgs": oapi{"type": "object", "description": "{<org id>: quota | null}"}}), "{quotas, locks}"},
		{"POST", "/llm/setup/dismiss", llmTag, "Dismiss the setup alert", "admin, or the shell an admin drives",
			"While no upstream exists and no admin has changed the connector, GET /alerts carries one alert of kind llm-setup for admins and the shell an admin drives, naming /xbin/llm (its dismiss is this route). A POST ends it for the workspace, for good. Nothing else changes.",
			nil, nil, "{ok}"},
	}
}
