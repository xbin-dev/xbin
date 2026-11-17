package server

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
)

// agentllm.go — what GET /agent/providers says about the workspace's LLM
// connector (D226 part 2, term/llmhook.go): per provider, whether a session
// of the caller's would run on the connector, on their own sign-in, or
// needs one — so a launcher can say it before the session opens.

// providerRow is a catalog row with what the route adds to it: why the
// harness can't start on the tile asked about (D229: ?all=1), and its `llm`
// member. With neither it marshals exactly as the catalog's (the route's
// golden).
type providerRow struct {
	agent.Provider
	Unavailable string            `json:"unavailable,omitempty"`
	LLM         *term.ProviderLLM `json:"llm,omitempty"`
	// Saved: the caller's saved sign-in for the harness (D230), when they
	// have one — and whether a session of theirs on the tile asked about
	// would get it.
	Saved *providerSaved `json:"saved,omitempty"`
	// Savable: xbind keeps sign-ins for this harness and the caller is a
	// person who may save one (GET /agent/signins has the rest).
	Savable bool `json:"savable,omitempty"`
}

// providerSaved is a provider row's `saved`: the person's default saved
// sign-in for the harness — metadata, never a secret.
type providerSaved struct {
	// (id, name and kind are left out for a person's workspace agent: it
	// learns that one exists, nothing of it)
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	// Inject: a session of the caller's on the tile would run on it. false:
	// Why says why it would be left out.
	Inject bool   `json:"inject"`
	Why    string `json:"why,omitempty"`
	// Trust: it is left out because the tile's source isn't only the
	// caller's own and they haven't said they trust it — the question
	// xbin's sign-in card asks (D230).
	Trust *term.SigninTrust `json:"trust,omitempty"`
}

// withAgentLLM is rows with each one's `llm`, while the workspace's LLM
// connector is offered to agent sessions; rows as they are otherwise. tile
// is the tile the session would open on ("" none: the caller named none,
// or one they can't read).
func (s *Server) withAgentLLM(r *http.Request, tile string, rows []providerRow) []providerRow {
	if s.Term == nil {
		return rows
	}
	p := auth.PrincipalOf(r)
	provs := make([]agent.Provider, len(rows))
	for i, row := range rows {
		provs[i] = row.Provider
	}
	llm := s.Term.AgentLLM(p.UserID, term.HomeKey(p), tile, provs)
	for i := range rows {
		rows[i].LLM = llm[rows[i].ID]
		rows[i].Saved = s.providerSaved(p, tile, rows[i].Provider, rows[i].LLM)
		rows[i].Savable = s.Signins != nil && len(rows[i].Keys) > 0 && s.signinsAvail().OK && p.UserID != "" && p.Component == "" && !p.ReadOnly() && p.Agent == ""
		if sv, l := rows[i].Saved, rows[i].LLM; sv != nil && sv.Inject && l != nil && (l.Use == "signin" || l.Use == "own-first") {
			l.Use = "own" // their saved sign-in is their own: it serves
		}
	}
	return rows
}

// providerSaved is p's saved sign-in for prov and whether a session of
// theirs on tile would get it (term's SigninPreview: the one rule); nil
// when they have none, or xbind keeps none.
func (s *Server) providerSaved(p auth.Principal, tile string, prov agent.Provider, llm *term.ProviderLLM) *providerSaved {
	if s.Term == nil || s.Signins == nil || !s.signinsAvail().OK {
		return nil
	}
	if p.Agent != "" {
		// a person's workspace agent reads this route too: it learns that a
		// saved sign-in exists — never its label, its kind, or who can
		// change the tile. (A session it starts has one only by the
		// person's tick on that start's approval.)
		if sg, err := s.Signins.Peek(p.UserID, prov.ID, ""); err == nil && sg != nil {
			return &providerSaved{}
		}
		return nil
	}
	sg, why, trust := s.Term.SigninPreview(p, tile, prov, llm != nil && llm.Forced)
	if sg == nil {
		return nil
	}
	return &providerSaved{ID: sg.ID, Name: sg.Name, Kind: sg.Kind, ExpiresAt: sg.ExpiresAt, Inject: why == "", Why: why, Trust: trust}
}
