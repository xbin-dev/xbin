package acp

// Mode escalation: does a permission option take the session past the
// agent's own asks? A plan approval offers "yes, and switch to ‹mode›" as
// options (ACP's switch_mode: an allow_always there means "approve and
// raise the mode", never "remember this answer"), and one of those modes
// may be a bypass. A host that keeps such modes to some people refuses the
// option from the others (the agent template: the conversation's owner
// only); one that doesn't still tells its clients, which ask twice (xbind:
// the option's `explicit` in a permission.request). Both judge with
// Provider.OptionRaises.

import "strings"

// ModeOption is the config option of category mode among opts — how an
// adapter that speaks its modes only as an option says them (opencode's
// build and plan agents); nil when there is none.
func ModeOption(opts []ConfigOption) *ConfigOption {
	for i := range opts {
		if opts[i].Category == "mode" && len(opts[i].Options) > 0 {
			return &opts[i]
		}
	}
	return nil
}

// Mode is the session's mode now: its own, else its option of category
// mode's value ("" when it says neither).
func (st SessionState) Mode() string {
	if st.Modes != nil && st.Modes.CurrentModeID != "" {
		return st.Modes.CurrentModeID
	}
	if o := ModeOption(st.Options); o != nil {
		return o.CurrentValue
	}
	return ""
}

// ModeOpen reports whether mode is one a session may be switched to
// without a second look (default-deny): the catalog knows it never takes
// the agent past its own asks (Safe), or it is start — the mode the
// session began in (the adapter's own first mode, or the one it was opened
// with: a provider the catalog lacks knows no other). Every other mode —
// an Explicit one, one a newer adapter added, any other mode of a provider
// the catalog lacks — is not.
func (p Provider) ModeOpen(mode, start string) bool {
	return p.Safe(mode) || (mode != "" && mode == start)
}

// ModeIDs are the ids that name one of the agent's modes: the catalog's
// (Modes, SafeModes) and the session's (its modes, and its option of
// category mode's values).
func (p Provider) ModeIDs(st SessionState) map[string]bool {
	ids := map[string]bool{}
	for _, m := range p.Modes {
		ids[m.ID] = true
	}
	for _, m := range p.SafeModes {
		ids[m] = true
	}
	if st.Modes != nil {
		for _, m := range st.Modes.AvailableModes {
			ids[m.ID] = true
		}
	}
	if o := ModeOption(st.Options); o != nil {
		for _, v := range o.Options {
			ids[v.Value] = true
		}
	}
	return ids
}

// OptionRaises reports whether answering a permission request with o
// raises the session's mode: it switches the session to a mode ModeOpen
// does not open. callKind is the kind of the call the request is about;
// st the session as it is (its modes and options — a zero one knows the
// catalog's only); start as for ModeOpen.
//
// Only an allow can raise. Its mode is the one it names — its id when that
// is one of the agent's modes (ModeIDs), else the catalog's OptionModes
// (claude's plan approval: exit-plan-bypass is bypassPermissions). An
// allow_always of a mode switch (KindSwitchMode: a plan approval) that
// names no mode the catalog knows raises too (default-deny: that is how an
// adapter offers "yes, and switch to ‹mode›"); any other option that names
// no mode raises nothing (an allow_once of a plan approval keeps the mode
// the session had before planning).
func (p Provider) OptionRaises(o PermissionOption, callKind string, st SessionState, start string) bool {
	if !strings.HasPrefix(o.Kind, "allow") {
		return false
	}
	mode := ""
	if p.ModeIDs(st)[o.OptionID] {
		mode = o.OptionID
	} else if m := p.OptionModes[o.OptionID]; m != "" {
		mode = m
	}
	if mode == "" {
		return callKind == KindSwitchMode && o.Kind == AllowAlways
	}
	return !p.ModeOpen(mode, start)
}
