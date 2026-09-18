// ask.go — quick asks: the low-friction way in. A question becomes a fresh run
// (kind=quick) that is driven immediately; follow-ups continue in that run like
// any other conversation. The tile's home view is built on this.
package main

import (
	"encoding/json"
	xbin "github.com/xbin-dev/xbin/sdk"
	"net/http"
	"strings"
)

func handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct{ Text, Toolset string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		xbin.WriteError(w, 400, "need {text}")
		return
	}
	cfg := parseConfig(agent.db.getSetting("config"))
	cfg.Toolset = normalizeToolset(body.Toolset)
	cfgJSON, _ := json.Marshal(cfg)
	id, err := agent.db.createRun(clip(body.Text, 60), string(cfgJSON), 0)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	agent.db.setRunKind(id, "quick")
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System})
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "user", Content: body.Text})
	agent.db.journal(id, "note", map[string]string{"text": "quick ask"})
	agent.resumeIfHalted(id)
	agent.driveAsync(id)
	run, _ := agent.db.getRun(id)
	xbin.WriteJSON(w, 200, run)
}
