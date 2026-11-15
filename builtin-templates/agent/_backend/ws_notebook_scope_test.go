package main

import (
	"strings"
	"testing"
)

// research's save_as is a write of a notebook page like notebook_write: a
// page that exists is written only by a session that may read it —
// preferences included —, refused with the reason otherwise, and its
// stamps only grow. A page closed to the session while the research ran is
// left alone.
func TestWsResearchSaveAsReadsFirst(t *testing.T) {
	fx := newWsFix(t)
	main := fx.main(t) // on big: Finance isn't connected
	books := fx.session(t, map[string]any{"name": "Books", "model": "local-llama"})
	fx.tool(t, books, "workspace_map", nil)
	fx.tool(t, books, "notebook_write", map[string]any{"slug": "ledger-notes", "body": "The ledger closes on the 5th.", "summary": "When the ledger closes"})
	fx.tool(t, books, "notebook_write", map[string]any{"slug": "preferences", "body": "Ana likes bullet points.", "summary": "How Ana likes things"})
	fx.tool(t, main, "workspace_map", nil)
	fx.tool(t, main, "notebook_write", map[string]any{"slug": "orders-notes", "body": "Orders takes the shop's orders.", "summary": "What Orders does"})
	store, _ := fx.ag.db.knowID(wsNotebook("alice"), false)
	st := fx.ag.db.wsStamp(store, "orders-notes")
	st.Orgs = append(st.Orgs, "legal") // an org of the page's own, which big may see
	_ = fx.ag.db.wsPutStamp(store, "orders-notes", st)
	fx.x.code["apps/orders"] = map[string]string{"main.go": "package main\n"}

	f := fakeOf(fx.ag)
	isChild := func(r LLMRequest) bool {
		return r.Run != main.Run && strings.Contains(systemOf(r), "research subagent")
	}
	for i, slug := range []string{"ledger-notes", "preferences"} {
		q := "research into " + slug
		f.on(forRun(main.Run, lastUser(q)), callTools(tc("r"+slug, "research", `{"tile":"Orders","question":"What does main.go do?","save_as":"`+slug+`"}`))).once()
		f.on(forRun(main.Run, lastTool("research")), say("Noted.")).once()
		fx.say(t, main, q)
		fx.idle(t, main)
		if got := fx.toolResult(t, main, "research"); !strings.Contains(got, `save_as: the page "`+slug+`" holds data of an org that doesn't allow this session's model (Finance allows private models only`) {
			t.Fatalf("%d: research saving over %s, which Main can't read: %q", i, slug, got)
		}
		if p, _ := fx.ag.db.wikiPage(store, slug); p.Version != 1 || p.UpdatedBy != "the workspace agent" {
			t.Fatalf("%d: %s was written: %+v", i, slug, p)
		}
	}
	if n := len(fx.ag.db.wsAgents(`WHERE run_id=?`, main.Run)); n != 0 || fx.x.count("CodeTree") != 0 {
		t.Fatalf("a refused save_as started research anyway: %v", fx.x.calls)
	}

	// a page Main may read: the answer goes over it, and its stamps grow
	f.on(forRun(main.Run, lastUser("what does orders do")), callTools(tc("r3", "research", `{"tile":"Orders","question":"What does main.go do?","save_as":"orders-notes"}`))).once()
	f.on(forRun(main.Run, lastTool("research")), say("Saved.")).once()
	f.on(isChild, callTools(tc("c1", "finish", `{"result":"main.go starts the Orders backend."}`))).once()
	fx.say(t, main, "what does orders do?")
	fx.idle(t, main)
	p, _ := fx.ag.db.wikiPage(store, "orders-notes")
	if st := fx.ag.db.wsStamp(store, "orders-notes"); !strings.Contains(p.Body, "starts the Orders backend") || !hasStr(st.Orgs, "legal") || !hasStr(st.Orgs, "sales") {
		t.Fatalf("the page research saved over: %q %+v", p.Body, st)
	}

	// closed to Main while the research ran: left as it was
	f.on(forRun(main.Run, lastUser("look again")), callTools(tc("r4", "research", `{"tile":"Orders","question":"Anything else?","save_as":"orders-notes"}`))).once()
	f.on(forRun(main.Run, lastTool("research")), say("Done.")).once()
	f.on(isChild, func(r LLMRequest) (LLMReply, error) {
		st := fx.ag.db.wsStamp(store, "orders-notes")
		st.Orgs = append(st.Orgs, "finance") // a session that read Finance wrote to it meanwhile
		_ = fx.ag.db.wsPutStamp(store, "orders-notes", st)
		return callTools(tc("c2", "finish", `{"result":"Nothing else."}`))(r)
	}).once()
	fx.say(t, main, "look again")
	fx.idle(t, main)
	if p2, _ := fx.ag.db.wikiPage(store, "orders-notes"); p2.Version != p.Version || strings.Contains(p2.Body, "Nothing else") {
		t.Fatalf("research wrote over a page closed to its session: %+v", p2)
	}
}
