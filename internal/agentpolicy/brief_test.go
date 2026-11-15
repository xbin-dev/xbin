package agentpolicy

import (
	"strings"
	"testing"
)

// What a conversation holding an org's data may tell a tile agent: the
// session's model source must satisfy the org's model rule — by the
// session's own tile being under that rule (the connector asks it for
// every call), or by a session held to the connector on a model the rule
// allows. A session on the person's own sign-in is never vouched for.
func TestBrief(t *testing.T) {
	ws := Layer{}
	fin := Layer{ID: "fin", Name: "Finance", Rules: Rules{Models: Private()}}
	ops := Layer{ID: "ops", Name: "Ops"}
	listed := Layer{ID: "legal", Name: "Legal", Rules: Rules{Models: Only("local", "hosted/small")}}
	local, hosted, small := &ModelRef{Upstream: "local", Model: "llama", Private: true}, &ModelRef{Upstream: "hosted", Model: "big"}, &ModelRef{Upstream: "hosted", Model: "small"}
	for name, c := range map[string]struct {
		from, to []Layer
		served   string
		model    *ModelRef
		want     string // "" allowed; else part of the reason
	}{
		"no rule: anything":                         {[]Layer{ws, ops}, []Layer{ws}, ServedOwn, nil, ""},
		"own tile, not started":                     {[]Layer{ws, fin}, []Layer{ws, fin}, ServedUnknown, nil, ""},
		"own tile, on the connector":                {[]Layer{ws, fin}, []Layer{ws, fin}, ServedConnector, nil, ""},
		"own tile, yet on the person's sign-in":     {[]Layer{ws, fin}, []Layer{ws, fin}, ServedOwn, nil, "own sign-in"},
		"own tile, connector only offered":          {[]Layer{ws, fin}, []Layer{ws, fin}, ServedOffered, local, "nothing holds"},
		"another tile, not started":                 {[]Layer{ws, fin}, []Layer{ws, ops}, ServedUnknown, nil, "isn't under that rule"},
		"another tile, the person's sign-in":        {[]Layer{ws, fin}, []Layer{ws, ops}, ServedOwn, nil, "Finance allows private models only"},
		"another tile, connector, a private model":  {[]Layer{ws, fin}, []Layer{ws, ops}, ServedConnector, local, ""},
		"another tile, connector, a hosted model":   {[]Layer{ws, fin}, []Layer{ws, ops}, ServedConnector, hosted, "hosted/big"},
		"another tile, connector, model unknown":    {[]Layer{ws, fin}, []Layer{ws, ops}, ServedConnector, nil, "isn't known"},
		"a listed rule, a listed model":             {[]Layer{ws, listed}, []Layer{ws}, ServedConnector, small, ""},
		"a listed rule, another model":              {[]Layer{ws, listed}, []Layer{ws}, ServedConnector, hosted, "Legal allows these models only"},
		"the same org, but the rule there is a pin": {[]Layer{ws, {ID: "fin", Name: "Finance", Rules: Rules{Models: Private()}, Pinned: true}}, []Layer{ws, {ID: "fin", Name: "Finance"}}, ServedUnknown, nil, "Finance"},
		"a workspace rule binds every tile":         {[]Layer{{Rules: Rules{Models: Private()}}, ops}, []Layer{{Rules: Rules{Models: Private()}}}, ServedUnknown, nil, ""},
	} {
		d := Brief(c.from, c.to, c.served, c.model)
		if (c.want == "") != d.Allowed || !strings.Contains(d.Reason, c.want) {
			t.Errorf("%s: allowed %v, %q; want %q", name, d.Allowed, d.Reason, c.want)
		}
		if !d.Allowed && d.Rule != "models" {
			t.Errorf("%s: rule %q", name, d.Rule)
		}
	}
}
