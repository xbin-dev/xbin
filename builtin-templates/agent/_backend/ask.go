// ask.go — quick asks: the low-friction way in. A question becomes a fresh run
// (kind=quick) that is driven immediately; follow-ups continue in that run like
// any other conversation. The tile's home view is built on this.
package main

import (
	"encoding/json"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text, Toolset string
		// Hold creates the run WITHOUT the user message or a drive, so the tile
		// can upload attachments into it first and then send the message with
		// POST /runs/{id}/message — a quick ask has no run to attach to until
		// this call makes one.
		Hold bool
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		xbin.WriteError(w, 400, "need {text}")
		return
	}
	cfg := parseConfig(agent.db.getSetting("config"))
	cfg.Toolset = normalizeToolset(body.Toolset)
	note := "quick ask"
	if body.Hold {
		note = "quick ask (waiting for attachments)"
	} else {
		agent.resumeIfHalted(0)
	}
	run, err := agent.startRun(clip(body.Text, 60), "quick", cfg, body.Text, body.Hold, note)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, run)
}
