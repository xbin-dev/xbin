// live_probe.go — port-forward diagnostics for the people in a
// conversation (API.md §Live previews): what its live previews answer now,
// and what any port of a sandbox bound to it answers.
//
//	GET /runs/{id}/ports                      the tree's live previews (its `live` steps), each probed now
//	GET /runs/{id}/ports/{sbx}/{port}?path=   one probe of a port of a sandbox bound to the run
//
// Participants only, as the live route (viewers 403, strangers 404). A
// probe goes the live route's way — liveUse (the binding, the class, the
// manager, the binder's right) and the manager's ports route as the binder
// (probePort) — and answers what came back: the status, the content type,
// the refusal and its words, how long it took. Never the page's body: this
// says why a preview is blank, it doesn't show the page.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func probeRoutes() []routeDef {
	return []routeDef{
		{"GET /runs/{id}/ports", needParticipant, handlePorts},
		{"GET /runs/{id}/ports/{sbx}/{port}", needParticipant, handlePortProbe},
	}
}

// portProbe is one probe's answer (and, listed, the live step it is of).
type portProbe struct {
	Sandbox     string `json:"sandbox"`        // the sandbox's id at its manager
	Name        string `json:"name,omitempty"` // its name
	Port        int    `json:"port"`
	Path        string `json:"path"`
	Run         int64  `json:"run,omitempty"` // the run whose live step showed it (listed)
	At          int64  `json:"at,omitempty"`  // when (listed)
	OK          bool   `json:"ok"`            // the server answered below 400
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Refusal     string `json:"refusal,omitempty"` // the contract's (not-listening, state, unsupported, …) or the agent's
	Error       string `json:"error,omitempty"`
	Ms          int64  `json:"ms"`
}

// probeLive probes page on port of sandbox sbx (its id) bound to runID, as
// the live route would serve it.
func (ag *Agent) probeLive(ctx context.Context, runID int64, sbx string, port int, page string) (pr portProbe) {
	pr = portProbe{Sandbox: sbx, Port: port, Path: page}
	start := time.Now()
	defer func() { pr.Ms = time.Since(start).Milliseconds() }()
	p, q, err := livePagePath(page)
	if err != nil {
		pr.Refusal, pr.Error = "invalid", err.Error()
		return pr
	}
	pr.Path = "/" + p + qsuffix(q)
	use, err := ag.liveUse(ctx, runID, sbx)
	if err != nil {
		pr.Refusal, pr.Error = orStr(sbxRefusal(err), "unavailable"), err.Error()
		return pr
	}
	pr.Name = use.Box.Name
	status, ctype, err := probePort(ctx, use, port, p, q)
	if err != nil {
		pr.Refusal, pr.Error = orStr(sbxRefusal(err), "unavailable"), err.Error()
		return pr
	}
	pr.Status, pr.ContentType, pr.OK = status, ctype, status < 400
	return pr
}

// maxListedProbes bounds GET /runs/{id}/ports: the newest distinct previews.
const maxListedProbes = 8

func handlePorts(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	rows, err := agent.db.q.Query(`SELECT s.run_id, s.detail, s.created FROM steps s JOIN runs r ON r.id=s.run_id
		WHERE s.kind='live' AND r.root_id=? ORDER BY s.id DESC LIMIT 200`, rootOf(run))
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	var list []portProbe
	seen := map[string]bool{}
	for rows.Next() {
		var p portProbe
		var detail string
		if rows.Scan(&p.Run, &detail, &p.At) != nil {
			continue
		}
		var d struct {
			Sandbox string `json:"sandbox"`
			Name    string `json:"name"`
			Port    int    `json:"port"`
			Path    string `json:"path"`
		}
		if json.Unmarshal([]byte(detail), &d) != nil || d.Sandbox == "" || d.Port == 0 {
			continue
		}
		key := d.Sandbox + "\x00" + strconv.Itoa(d.Port) + "\x00" + d.Path
		if seen[key] || len(list) >= maxListedProbes {
			continue
		}
		seen[key] = true
		p.Sandbox, p.Name, p.Port, p.Path = d.Sandbox, d.Name, d.Port, orStr(d.Path, "/")
		list = append(list, p)
	}
	rows.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(p *portProbe) {
			defer wg.Done()
			run, at, name := p.Run, p.At, p.Name
			*p = agent.probeLive(ctx, run, p.Sandbox, p.Port, p.Path)
			p.Run, p.At, p.Name = run, at, orStr(p.Name, name)
		}(&list[i])
	}
	wg.Wait()
	if list == nil {
		list = []portProbe{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"previews": list})
}

func handlePortProbe(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != r.PathValue("port") {
		xbin.WriteError(w, http.StatusBadRequest, "the port is a number from 1 to 65535")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	xbin.WriteJSON(w, 200, agent.probeLive(ctx, pathID(r), r.PathValue("sbx"), port, orStr(r.URL.Query().Get("path"), "/")))
}
