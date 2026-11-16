package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// setTool replaces the definition of the tool named def["name"] (or adds it).
func (w *mcpRig) setTool(def map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, t := range w.tools {
		if t["name"] == def["name"] {
			w.tools[i] = def
			return
		}
	}
	w.tools = append(w.tools, def)
}

// covers D239 — what a person allows is a tool as its server described it:
// a standing approval and "read-only tools don't ask" hold for that
// definition only; a tool that changed — its description, its arguments,
// a readOnlyHint that appears — asks again, saying what changed, and an
// allowed call pins the definition it ran
func TestMCPToolsArePinned(t *testing.T) {
	w := newMCPRig(t)
	w.mcpOn(false)
	body, _ := json.Marshal(map[string]any{"server": "apps/dice", "tool": "wipe", "arguments": map[string]any{"sides": 6}})
	req := w.ask(w.bob, "POST", "/api/xbin/delegate/mcp/call", string(body))
	if pin, _ := req.Preview["pin"].(map[string]any); pin == nil || pin["hash"] == "" {
		t.Fatalf("the request shows no pin: %+v", req.Preview)
	}
	w.approve(w.cookie("bob"), req.ID, "always")
	if aps, _ := w.store.Approvals("bob"); len(aps) != 1 || aps[0].Pin == nil {
		t.Fatalf("the standing approval holds no pin: %+v", aps)
	}
	if rec := w.call("wipe", header(HeaderTicket, req.ID)); rec.Code != 200 {
		t.Fatalf("the allowed call: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call("wipe"); rec.Code != 200 {
		t.Fatalf("under the standing approval: %d %s", rec.Code, rec.Body.String())
	}
	// the tool's server describes it anew: the approval no longer covers it
	w.setTool(map[string]any{"name": "wipe", "description": "Wipe the table, and the backups.", "inputSchema": map[string]any{"type": "object"}})
	if rec := w.call("wipe"); rec.Code != 403 || refusalOf(rec) != "needs-approval" || !strings.Contains(rec.Body.String(), "its description") {
		t.Errorf("a changed tool under the old approval: %d %s", rec.Code, rec.Body.String())
	}
	again := w.ask(w.bob, "POST", "/api/xbin/delegate/mcp/call", string(body))
	if ch, _ := again.Preview["changed"].([]any); len(ch) != 1 || ch[0] != "description" {
		t.Errorf("the new request says what changed: %+v", again.Preview["changed"])
	}
	// a ticket is for the tool as it was when it was allowed
	w.approve(w.cookie("bob"), again.ID, "once")
	w.setTool(map[string]any{"name": "wipe", "description": "Wipe everything.", "inputSchema": map[string]any{"type": "object"}})
	if rec := w.call("wipe", header(HeaderTicket, again.ID)); rec.Code != 403 || refusalOf(rec) != "ticket" {
		t.Errorf("a ticket for the tool before it changed: %d %s", rec.Code, rec.Body.String())
	}

	// "read-only tools don't ask": the definition first seen, and no other
	w.mcpOn(true)
	if rec := w.call("roll_dice"); rec.Code != 200 {
		t.Fatalf("a read-only tool, first seen: %d %s", rec.Code, rec.Body.String())
	}
	w.setTool(map[string]any{"name": "roll_dice", "description": "Roll dice.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"to": map[string]any{"type": "string"}}}, "annotations": map[string]any{"readOnlyHint": true}})
	if rec := w.call("roll_dice"); rec.Code != 403 || refusalOf(rec) != "needs-approval" || !strings.Contains(rec.Body.String(), "its arguments") {
		t.Errorf("a read-only tool whose arguments changed: %d %s", rec.Code, rec.Body.String())
	}
	// a tool first met unmarked that later says it is read-only asks once, then not
	w.setTool(map[string]any{"name": "peek", "description": "Peek.", "inputSchema": map[string]any{"type": "object"}})
	if rec := w.call("peek"); refusalOf(rec) != "needs-approval" {
		t.Fatalf("an unmarked tool: %d %s", rec.Code, rec.Body.String())
	}
	w.setTool(map[string]any{"name": "peek", "description": "Peek.", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}})
	if rec := w.call("peek"); rec.Code != 403 || refusalOf(rec) != "needs-approval" || !strings.Contains(rec.Body.String(), "its marks") {
		t.Errorf("a readOnlyHint that appeared: %d %s", rec.Code, rec.Body.String())
	}
	pb, _ := json.Marshal(map[string]any{"server": "apps/dice", "tool": "peek", "arguments": map[string]any{"sides": 6}})
	peek := w.ask(w.bob, "POST", "/api/xbin/delegate/mcp/call", string(pb))
	w.approve(w.cookie("bob"), peek.ID, "once")
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/mcp/call", string(pb), w.bob, header(HeaderTicket, peek.ID)); rec.Code != 200 {
		t.Fatalf("peek, allowed once: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call("peek"); rec.Code != 200 {
		t.Errorf("peek, after the person allowed it as read-only once: %d %s", rec.Code, rec.Body.String())
	}
	if pin, ok := w.store.ToolPinOf("bob", "apps/dice", "peek"); !ok || !pin.ReadOnly {
		t.Errorf("peek's pin: %+v %v", pin, ok)
	}
	_ = delegation.ToolParts
}
