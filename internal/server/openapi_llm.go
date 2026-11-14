package server

// openapi_llm.go — the LLM connector's routes (D226, docs/protocol/llm.md,
// docs/llm.md). endpoints() appends them.

const llmTag = "LLM"

func llmEndpoints() []ep {
	const (
		caller = "a person the workspace's people switch admits, a component whose llm slot is bound to xbin:llm, or an LLM key (Authorization: Bearer xbl_…)"
		admin  = "admin"
		view   = "{configured, enabled, people, upstreams:[{name, kind, baseURL, key:{set, source?, hint?}, models, private, prices?, source, locked, reason?}], defaults:{model?, roles}, aliases, quotas, locks:{<path>:{locked, reason}}, host:{active, lockAll?, error?}, vault, problems, storeError?, set?, by?}"
		oaiErr = " Errors are OpenAI-shaped, {error: {message, type, param, code}}: 401 invalid_api_key; 403 xbin_llm_not_bound | xbin_llm_people_off | xbin_llm_policy; 404 model_not_found; 429 xbin_llm_quota_exceeded (with quota, resets, limit, used, and Retry-After); 503 xbin_llm_not_configured | xbin_llm_disabled | xbin_vault_sealed; 502/504 xbin_llm_upstream…; an upstream's own error passes with its status (its 401/403 as 502 xbin_llm_upstream_auth)."
	)
	quota := oapi{"type": "object", "description": "{tokensPerDay?, tokensPerMonth?, costPerDay?, costPerMonth?, action?: stop|warn} — 0 not set, -1 unlimited; null removes"}
	return []ep{
		{"POST", "/llm/v1/chat/completions", llmTag, "Chat Completions through the workspace's LLM connector", caller,
			"OpenAI's Chat Completions wire, relayed to the upstream the model names (<upstream>/<model>, an alias, xbin/default, xbin/<role>, or none: the default). The body passes as sent except model (the upstream's own name) and, on a stream, stream_options.include_usage (always true: the last chunk carries usage). The answer streams as it arrives; a client that goes away ends the upstream call. Body at most 32 MiB. Metered to the acting person and the component; X-XBin-LLM-Quota on the answer when a warn quota is reached." + oaiErr,
			nil, jsonBody("an OpenAI Chat Completions request", oapi{"model": str("<upstream>/<model>, an alias, xbin/default, xbin/<role>; absent: the default model"), "messages": oapi{"type": "array"}, "stream": boolean()}),
			"an OpenAI chat completion, or its SSE stream"},
		{"POST", "/llm/v1/responses", llmTag, "Responses API through the workspace's LLM connector", caller,
			"OpenAI's Responses wire, relayed like chat completions; the body passes as sent except model, and the stream's events untouched (usage is read from response.completed)." + oaiErr,
			nil, jsonBody("an OpenAI Responses request", oapi{"model": str("as for chat completions"), "input": freeSchema("the input"), "stream": boolean()}),
			"an OpenAI response, or its SSE stream"},
		{"GET", "/llm/v1/models", llmTag, "The models the connector offers", caller,
			"OpenAI's model list: every upstream's models as <upstream>/<model> (an upstream's own model objects kept: context sizes pass through), the aliases and xbin/default, xbin/<role> with alias_of. upstream_errors names upstreams whose own list couldn't be read (detail for admins)." + oaiErr,
			nil, nil, "{object: \"list\", data: [{id, object, owned_by, alias_of?, …}], upstream_errors?}"},
		{"GET", "/llm", llmTag, "The connector as the caller meets it", "authenticated",
			"configured (an upstream exists), enabled, callable (the caller's calls would be admitted; reason and code when not), url (the OpenAI base, /api/xbin/llm/v1), ref (xbin:llm, what an llm slot binds to), people, host (a deployment file is in force), defaults, the caller's user and component, and quota {user, orgs?, limits, workspace, state: ok|warn|stop} — each limit {name, value, source, action, used, resets, state}; a person's own sign-in reads user and quota also when their direct calls aren't admitted (the calls tiles make for them count against the same limits). Always 200.",
			nil, nil, "{configured, enabled, callable, reason?, code?, url, ref, people, host, defaults, user?, component?, admin?, quota?}"},
		{"GET", "/llm/preferred", llmTag, "The workspace's default models (the llm-gw convention)", caller,
			"use=<role>: {use, model} — that role's model as <upstream>/<model>, the default model when the role has none, \"\" when neither. No use: {preferred: {default?, <role>…}, useTypes}. The shape the llm-gw tile answers, so a tile resolves its default model from either provider." + oaiErr,
			[]oapi{queryParam("use", "a role: agent, harness, … (default: all)", false)}, nil, "{use, model} | {preferred, useTypes}"},
		{"GET", "/llm/config", llmTag, "The connector's configuration in force", admin,
			"The admin's settings and the host's deployment file (XBIN_LLM_CONFIG) merged. locks maps each setting the file sets (enabled, people, upstreams.<name>, defaults.model, defaults.roles.<role>, aliases.<alias>, quotas.workspace|default|users|orgs; \"*\": all of them) to {locked: true, reason: \"set by your host\"}; a host's upstream is locked too. No key is ever answered: key {set, source: vault|host, hint} (hint: an admin's key's last four characters, while the vault is open). vault: open | sealed | locked | plaintext. problems: defaults and aliases that resolve to nothing.",
			nil, nil, view},
		{"PUT", "/llm/config", llmTag, "Change the connector's switches, default models and aliases", admin,
			"Only what is named changes: enabled, people (admins | everyone | nobody: who may call directly), defaults {model?, roles?: {<role>: <model>}}, aliases {<alias>: <model>} (\"*\": every unknown model name). \"\" removes a role, an alias or the default. 400 invalid; 409 {error, locked: [<path>…], reason} when a named setting is the host's (sending a locked setting back unchanged is fine). → the view. Audited.",
			nil, jsonBody("the changes", oapi{"enabled": boolean(), "people": str("admins | everyone | nobody"),
				"defaults": oapi{"type": "object", "description": "{model?, roles?: {<role>: <model>}}"}, "aliases": oapi{"type": "object", "description": "{<alias>: <model>}"}}), view},
		{"PUT", "/llm/upstreams/{name}", llmTag, "Add or change an upstream", admin,
			"name: lowercase letters, digits, . _ - (not xbin). A new upstream needs baseURL (http(s), the provider's OpenAI-compatible base, with or without /v1); what isn't named stays. key is stored in xbind's vault and never answered (\"\" removes it); models lists what is offered (empty: the upstream's own /models and any model name pass through); private marks it for org constraints; prices {<model>: {in, out, cachedIn?}} in USD per million tokens. 400 invalid; 409 {locked} for a host's upstream, or any new one when the host locks all; 409 when a key is given and the vault is sealed or not set up. → the view. Audited.",
			[]oapi{pathParam("name", "the upstream")}, jsonBody("the upstream", oapi{"baseURL": str("https://api.openai.com/v1"), "key": str("the provider's API key"),
				"models": arr(), "private": boolean(), "prices": oapi{"type": "object"}, "kind": str("openai")}), view},
		{"DELETE", "/llm/upstreams/{name}", llmTag, "Remove an upstream", admin,
			"The upstream and its key go; usage already metered under its name stays. 404 unknown; 409 {locked} for a host's. → the view. Audited.",
			[]oapi{pathParam("name", "the upstream")}, nil, view},
		{"POST", "/llm/upstreams/{name}/test", llmTag, "Test an upstream with a small call", admin,
			"A one-line chat completion with model (default: the upstream's first listed model), metered to the caller; for an upstream without a model list and no model given, its /models list instead. Always 200 for a known upstream: {ok, model?, models?, status?, ms, tokens?, error?}. 404 unknown.",
			[]oapi{pathParam("name", "the upstream")}, jsonBody("the model to call", oapi{"model": str("the upstream's own model name")}), "{ok, model?, models?, status?, ms, tokens?, error?}"},
		{"GET", "/llm/usage", llmTag, "What was used", "a person's own sign-in (their own rows), or admin (every row)",
			"Counters per UTC day (period=day, default: this month so far) or month (period=month, default: the last 12), person, component and model: rows [{day|month, user, component, model, requests, failed?, inputTokens, outputTokens, cachedTokens?, reasoningTokens?, cost, unpriced?}], totals, scope: own | all. user \"owner\" is the root token, \"\" a component acting for nobody; component \"\" a direct call. cost in USD covers the requests whose model has a price (unpriced counts the others). Filters: user (admins), component, model. 403 for a tile's credentials and for another person's rows.",
			[]oapi{queryParam("period", "day | month", false), queryParam("from", "YYYY-MM-DD (UTC)", false), queryParam("to", "YYYY-MM-DD (UTC)", false),
				queryParam("user", "one person's rows (admins)", false), queryParam("component", "one component's rows", false), queryParam("model", "one model's rows (<upstream>/<model>)", false)},
			nil, "{period, from, to, scope, rows, totals}"},
		{"GET", "/llm/quotas", llmTag, "The quota rules in force", admin,
			"quotas {workspace?, default?, users?, orgs?} and locks (quotas.workspace|default|users|orgs the host's file sets). user=<id>: also effective, that person's limits as they stand ({user, orgs?, limits, workspace, state}).",
			[]oapi{queryParam("user", "also answer this person's limits", false)}, nil, "{quotas, locks, effective?}"},
		{"PUT", "/llm/quotas", llmTag, "Change quota rules", admin,
			"Only what is named changes: workspace (everyone together), default (each person), users and orgs {<id>: quota}. A person's quota is their own override, else the most generous of their orgs' per limit, else the default — each limit on its own. Windows are UTC days and months. 400 invalid; 409 {locked} for a part the host's file sets. → as GET. Audited.",
			nil, jsonBody("the rules to change", oapi{"workspace": quota, "default": quota,
				"users": oapi{"type": "object", "description": "{<user id>: quota | null}"}, "orgs": oapi{"type": "object", "description": "{<org id>: quota | null}"}}), "{quotas, locks}"},
		{"POST", "/llm/setup/dismiss", llmTag, "Dismiss the setup alert", "admin, or the shell an admin drives",
			"While no upstream exists and no admin has changed the connector, GET /alerts carries one alert of kind llm-setup for admins and the shell an admin drives, naming /xbin/llm (its dismiss is this route). A POST ends it for the workspace, for good. Nothing else changes.",
			nil, nil, "{ok}"},
	}
}
