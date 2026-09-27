package server

import (
	"strings"
	"testing"
)

// covers PO-14 NP-14-4 — the tile-deployments rows of the OpenAPI document:
// every route of the contract (docs/protocol.md, "Tile deployments") with the
// capability it needs, the ?deployment= each existing route gains (optional,
// marked reserved, never a body field: no existing request body grows), the
// field notes on the listings, and a reserved route answering 501. What is
// mounted and what protocol.md lists is TestRouteInventory's.
func TestOpenAPIDeploymentRows(t *testing.T) {
	doc := OpenAPI()
	paths, _ := doc["paths"].(oapi)
	op := func(method, path string) oapi {
		t.Helper()
		item, _ := paths[path].(oapi)
		o, _ := item[strings.ToLower(method)].(oapi)
		if o == nil {
			t.Errorf("no %s %s in the OpenAPI document", method, path)
		}
		return o
	}

	routes := []struct{ method, path, cap string }{
		{"GET", "/deployments", capDeployRead},
		{"GET", "/deployments/log", capDeployWrite},
		{"GET", "/deployments/diff", capDeployWrite},
		{"POST", "/deployments/live-reload/pause", capDeployTerminal},
		{"POST", "/deployments/live-reload/resume", capDeployTerminal},
		{"POST", "/deployments/live-reload/now", capDeployTerminal},
		{"POST", "/deployments/live-reload/attach", capDeployTerminal},
		{"POST", "/deployments/add", capDeployTerminal},
		{"POST", "/deployments/remove", capDeployTerminal},
		{"POST", "/deployments/deploy", capDeployTerminal},
		{"POST", "/deployments/promote", capDeployTerminal},
		{"POST", "/deployments/rollback", capDeployTerminal},
		{"POST", "/deployments/reset", capDeployTerminal},
		{"POST", "/deployments/run-now", capDeployTerminal},
		{"POST", "/deployments/primary", capDeployManager},
		{"POST", "/deployments/protect", capDeployManager},
		{"POST", "/deployments/edge", capDeployManager},
		{"POST", "/deployments/deliveries", capDeployManager},
		{"POST", "/deployments/always-on", capDeployManager},
		{"POST", "/deployments/limits", capDeployManager},
		{"POST", "/deployments/seed", capDeployManager},
		{"POST", "/deployments/vault-copy", capDeployManager},
		{"POST", "/deployments/backup", capDeployAdmin},
		{"GET", "/deployments/backups", capDeployAdmin},
		{"POST", "/deployments/restore", capDeployAdmin},
		{"POST", "/deployments/backup-schedule", capDeployAdmin},
		{"GET", "/checkpoints/{tile}.git/{path}", capDeployFetch},
	}
	info := doc["info"].(oapi)["description"].(string)
	for _, r := range routes {
		o := op(r.method, r.path)
		if o == nil {
			continue
		}
		if o["x-xbin-capability"] != r.cap {
			t.Errorf("%s %s capability = %v, want %q", r.method, r.path, o["x-xbin-capability"], r.cap)
		}
		if !strings.Contains(info, "**"+r.cap+"**") {
			t.Errorf("apiInfo doesn't list the capability %q", r.cap)
		}
		if tags, _ := o["tags"].([]string); len(tags) != 1 || tags[0] != "Deployments" {
			t.Errorf("%s %s tags = %v, want [Deployments]", r.method, r.path, o["tags"])
		}
		if r.method != "POST" {
			continue
		}
		// Every body of the family names the tile and takes seq and dryRun.
		body, _ := o["requestBody"].(oapi)
		schema, _ := body["content"].(oapi)["application/json"].(oapi)["schema"].(oapi)
		props, _ := schema["properties"].(oapi)
		for _, f := range []string{"tile", "seq", "dryRun"} {
			if props[f] == nil {
				t.Errorf("%s %s body lacks %q", r.method, r.path, f)
			}
		}
		if req, _ := schema["required"].([]string); len(req) == 0 || req[0] != "tile" {
			t.Errorf("%s %s body: tile isn't required (%v)", r.method, r.path, schema["required"])
		}
	}

	// A reserved operation says so, answers 501, and nothing else claims 501.
	for p, item := range paths {
		for m, o := range item.(oapi) {
			o := o.(oapi)
			resp := o["responses"].(oapi)
			isReserved := o["x-xbin-reserved"] == true
			if isReserved != (resp["501"] != nil) {
				t.Errorf("%s %s: x-xbin-reserved %v but a 501 response %v", m, p, o["x-xbin-reserved"], resp["501"] != nil)
			}
			if isReserved && !strings.Contains(o["description"].(string), reserved) {
				t.Errorf("%s %s: reserved without the Reserved marker in its description", m, p)
			}
		}
	}

	// The ?deployment= existing routes gain: optional, a query parameter,
	// never a new field of a body an older xbind decodes strictly; reserved
	// until this xbind reads it, and no longer once it does (§4.3 step 5).
	withParam := [][2]string{
		{"GET", "/tile-status"}, {"GET", "/logs"}, {"GET", "/frame-token"},
		{"GET", "/cron/jobs"}, {"PUT", "/cron/jobs"}, {"DELETE", "/cron/jobs/{name}"},
		{"GET", "/bus/subscriptions"}, {"PUT", "/bus/subscriptions"}, {"DELETE", "/bus/subscriptions/{name}"},
	}
	builtParam := map[[2]string]bool{
		{"POST", "/term/sessions"}: true, {"POST", "/term/sessions/{id}/restart"}: true, {"GET", "/sandboxes"}: true,
		{"GET", "/vault/{component}"}: true, {"GET", "/vault/{component}/{key}"}: true,
		{"PUT", "/vault/{component}/{key}"}: true, {"DELETE", "/vault/{component}/{key}"}: true,
	}
	for r := range builtParam {
		withParam = append(withParam, r)
	}
	for _, r := range withParam {
		o := op(r[0], r[1])
		if o == nil {
			continue
		}
		var found oapi
		params, _ := o["parameters"].([]oapi)
		for _, p := range params {
			if p["name"] == "deployment" {
				found = p
			}
		}
		switch {
		case found == nil:
			t.Errorf("%s %s has no ?deployment= parameter", r[0], r[1])
		case found["in"] != "query" || found["required"] != false || (found["x-xbin-reserved"] == true) == builtParam[r]:
			t.Errorf("%s %s ?deployment= = %v, want an optional query parameter, reserved %v", r[0], r[1], found, !builtParam[r])
		}
		if body, _ := o["requestBody"].(oapi); body != nil {
			schema, _ := body["content"].(oapi)["application/json"].(oapi)["schema"].(oapi)
			if props, _ := schema["properties"].(oapi); props["deployment"] != nil {
				t.Errorf("%s %s: deployment is a body field; existing bodies never gain one", r[0], r[1])
			}
		}
	}

	// The fields existing answers gain are noted, marked reserved (on
	// /tile-status, /logs and /frame-token the parameter carries the note)
	// until this xbind sets them; then the note is plain prose.
	note := strings.TrimSpace(reservedField)
	for _, r := range [][2]string{
		{"GET", "/backends"}, {"GET", "/runtime"},
		{"GET", "/whoami"},
		{"GET", "/cron/jobs"}, {"PUT", "/cron/jobs"}, {"GET", "/bus/subscriptions"}, {"PUT", "/bus/subscriptions"},
		{"POST", "/tile-report"}, {"POST", "/notify"}, {"PUT", "/iface-instances"},
		{"PUT", "/ingress-hosts"},
	} {
		if o := op(r[0], r[1]); o != nil && !strings.Contains(o["description"].(string), note) {
			t.Errorf("%s %s: no reserved field note", r[0], r[1])
		}
	}
	for _, r := range [][2]string{{"GET", "/term/sessions"}, {"GET", "/status"}, {"GET", "/agent/history"}, {"POST", "/grants"}, {"GET", "/sandboxes"}} {
		if o := op(r[0], r[1]); o != nil && (strings.Contains(o["description"].(string), note) || !strings.Contains(o["description"].(string), "deployment")) {
			t.Errorf("%s %s: its deployment field is built: noted in plain prose, not as reserved", r[0], r[1])
		}
	}
}
