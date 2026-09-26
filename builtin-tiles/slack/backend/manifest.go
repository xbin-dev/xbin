package main

import (
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// botScopes are what the adapter uses: read DMs, mentions and the threads it
// follows, post, show "is thinking…" in the assistant pane, look up names,
// and the /agent command.
var botScopes = []string{
	"app_mentions:read", "assistant:write", "channels:history", "channels:read", "chat:write", "commands",
	"groups:history", "groups:read", "im:history", "im:read", "im:write", "mpim:history", "mpim:read", "users:read",
}

var botEvents = []string{"app_mention", "message.im", "message.channels", "message.groups", "message.mpim", "assistant_thread_started"}

// appManifest is a Slack app manifest for this adapter: paste it at
// api.slack.com/apps → Create New App → From an app manifest.
func appManifest(name string) map[string]any {
	return map[string]any{
		"display_information": map[string]any{"name": name, "description": "An AI agent from an xbin workspace"},
		"features": map[string]any{
			"app_home":       map[string]any{"home_tab_enabled": false, "messages_tab_enabled": true, "messages_tab_read_only_enabled": false},
			"bot_user":       map[string]any{"display_name": name, "always_online": true},
			"assistant_view": map[string]any{"assistant_description": "Ask " + name + " anything."},
			"slash_commands": []map[string]any{{"command": "/agent", "description": "Talk to the agent: new, reset, status, stop, help",
				"usage_hint": "new [message] | status | stop | help", "should_escape": false}},
		},
		"oauth_config": map[string]any{"scopes": map[string]any{"bot": botScopes}},
		"settings": map[string]any{
			"event_subscriptions":    map[string]any{"bot_events": botEvents},
			"interactivity":          map[string]any{"is_enabled": false},
			"org_deploy_enabled":     false,
			"socket_mode_enabled":    true,
			"token_rotation_enabled": false,
		},
	}
}

// handleManifest: GET /manifest?name=Agent — the manifest to paste.
func handleManifest(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || len(name) > 35 {
		name = "Agent"
	}
	xbin.WriteJSON(w, 200, appManifest(name))
}
