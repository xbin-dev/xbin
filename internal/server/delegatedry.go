package server

// delegatedry.go — what a dry run answers for an act its handler narrows
// (delegate.go step 4; docs/protocol/delegation.md §Dry run).
//
// The gate's verdict is not the whole rule for every act: some routes mix
// classes, and their handlers narrow a delegate further (delegateclass.go).
// A dry run that answered from the verdict alone would say "allowed" to a
// call the handler then refuses — after the person approved it. So each
// such row carries a judge: the handler's own check, run without the act,
// from the same function the handler runs. A DelegateAct row has a judge or
// is listed in delegateDryWhole with the reason its verdict is everything
// (TestDelegateDryRunJudgesEveryAct): a new act can't be added without
// deciding which.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// dryJudge judges one call the gate would let through, without making it:
// "" when the handler would run it, else the refusal's code and sentence.
// It may add to the dry run's answer (preview) and name the call's tile.
type dryJudge func(s *Server, p auth.Principal, r *http.Request, body []byte, row *delegation.Call, preview map[string]any) (code, why string)

func (r delegateRow) judged(f dryJudge) delegateRow { r.judge = f; return r }

// delegateDryWhole lists the acts whose gate verdict is the handler's whole
// rule for a delegate, with why: nothing below the gate narrows them.
var delegateDryWhole = map[string]string{
	"POST /delegate/requests":             "it files a request; the call it asks for is judged when it is filed (apiDelegateRequestNew) and again when it is made",
	"DELETE /delegate/requests/{id}":      "the agent's own request",
	"POST /delegate/sessions":             "the agent's own handle",
	"PATCH /delegate/sessions/{id}":       "the agent's own handle",
	"DELETE /delegate/sessions/{id}":      "the agent's own handle",
	"POST /delegate/sessions/{id}/allows": "the agent's own handle",
	"POST /delegate/sessions/{id}/taint":  "the agent's own handle: it only narrows",
	"POST /delegate/changesets":           "a proposal: nothing is applied, and each line is judged when it is described and again when its person applies it",
	"DELETE /delegate/changesets/{id}":    "the agent's own proposal",
}

// CreateChecker answers what a POST /templates/new by p would create, or
// why it is refused (the broker's CreatePreview: the handler's own check).
type CreateChecker interface {
	CreatePreview(p auth.Principal, body []byte) (map[string]any, error)
}

// createPreview runs the creation check for p ("" why: it would be made).
func (s *Server) createPreview(p auth.Principal, body []byte) (map[string]any, string) {
	cc, ok := s.ChangeSets.(CreateChecker)
	if !ok {
		return nil, "this xbind creates no tiles for a person's agent"
	}
	out, err := cc.CreatePreview(p, body)
	if err != nil {
		return nil, err.Error()
	}
	return out, ""
}

func dryCreate(s *Server, p auth.Principal, _ *http.Request, body []byte, row *delegation.Call, preview map[string]any) (string, string) {
	out, why := s.createPreview(p, body)
	if why != "" {
		return "create", why
	}
	preview["create"] = out
	return "", ""
}

// dryStart: what THIS start would need — the person's preference may start
// it unasked — or why it is refused whatever approves it.
func dryStart(s *Server, p auth.Principal, _ *http.Request, body []byte, row *delegation.Call, preview map[string]any) (string, string) {
	ask := sessionAskOf(body)
	row.Tile, preview["tile"] = ask.Cwd, ask.Cwd
	if code, why := s.startRefusal(p, ask); why != "" {
		return code, why
	}
	if _, why := s.sessionPreview(p, body); why != "" {
		return "start", why // no terminal level, an unknown harness or mode, an org's rule
	}
	if s.startsUnasked(p, ask) {
		preview["needsApproval"], preview["under"] = false, "preference"
	}
	return "", ""
}

// sessionIDOf is the session a /term/sessions/<id>… request names.
func sessionIDOf(r *http.Request) string {
	seg := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/xbin/term/sessions/"), "/")
	return seg[0]
}

// dryDrive: a delegate drives only the sessions it opened.
func dryDrive(s *Server, p auth.Principal, r *http.Request, _ []byte, _ *delegation.Call, _ map[string]any) (string, string) {
	if s.Term == nil {
		return "session", "terminals are not enabled"
	}
	id := sessionIDOf(r)
	if err := s.Term.MayDrive(id, p); err != nil {
		return "session", err.Error()
	}
	if why := s.drivesRefusal(p, id); why != "" {
		return "session", why
	}
	return "", ""
}

// drySend: dryDrive, and what the agent may tell the session.
func drySend(s *Server, p auth.Principal, r *http.Request, body []byte, row *delegation.Call, preview map[string]any) (string, string) {
	if code, why := dryDrive(s, p, r, body, row, preview); why != "" {
		return code, why
	}
	var b struct{ Text string }
	_ = json.Unmarshal(body, &b)
	why, code := s.sendsRefusal(p, sessionIDOf(r), b.Text)
	return code, why
}

func dryPreview(s *Server, p auth.Principal, _ *http.Request, body []byte, row *delegation.Call, preview map[string]any) (string, string) {
	var b struct {
		PreviewTarget
		Shell bool `json:"shell,omitempty"`
	}
	if json.Unmarshal(body, &b) != nil {
		return "preview", "need {tile}"
	}
	t := b.PreviewTarget
	t.shell = b.Shell || t.Screen != "" || t.Tile == ""
	if _, err := previewPerson(p, t); err != nil {
		return "preview", err.Error()
	}
	if err := s.previewDelegateRefusal(p, t); err != nil {
		return "preview", err.Error()
	}
	row.Tile, preview["tile"] = strings.Trim(t.Tile, "/"), strings.Trim(t.Tile, "/")
	return "", ""
}
