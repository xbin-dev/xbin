package acp

import (
	"strings"
	"testing"
)

// A strict start (Config.Strict: the session is opened for someone who
// isn't there to watch it — xbind's workspace agent) is in the mode that
// was asked for or isn't at all, and sets only a model or a thinking level:
// a set_mode that fails, or an option of any other category, ends it. A
// start that isn't strict logs and goes on, as it always did.
func TestStrictStart(t *testing.T) {
	start := func(setup func(*peer), cfg Config) (*Client, *peer, error) {
		p := newPeer(t)
		setup(p)
		c, _, err := p.start(ClientOptions{}, NewPermissions(), 0, cfg)
		return c, p, err
	}
	sets := func(p *peer) (n int) {
		for _, m := range p.seen() {
			if m.Method == MSessionSetConfig {
				n++
			}
		}
		return n
	}
	perm := ConfigOption{ID: "sandbox", Name: "Sandbox", Category: "permissions", Type: "select", CurrentValue: "on",
		Options: []ConfigValue{{Value: "on", Name: "On"}, {Value: "off", Name: "Off"}}}
	model := ConfigOption{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: "a",
		Options: []ConfigValue{{Value: "a", Name: "A"}, {Value: "b", Name: "B"}}}
	uncat := ConfigOption{ID: "autoApprove", Name: "Auto-approve", Type: "select", CurrentValue: "no",
		Options: []ConfigValue{{Value: "no", Name: "No"}, {Value: "yes", Name: "Yes"}}}

	// the mode that was asked for can't be set
	c, _, err := start(func(p *peer) { p.noSetMode = true }, Config{Mode: "plan", Strict: true})
	if err == nil {
		c.Close()
		t.Fatal("a strict start in a mode the agent refused went on")
	}
	if !strings.Contains(err.Error(), "plan") {
		t.Errorf("the error doesn't name the mode: %v", err)
	}
	if c, _, err := start(func(p *peer) { p.noSetMode = true }, Config{Mode: "plan"}); err != nil {
		t.Errorf("a start that isn't strict: %v", err)
	} else {
		c.Close()
	}

	// an option that isn't a model or a thinking level
	for name, opt := range map[string]ConfigOption{"another category": perm, "no category": uncat} {
		c, p, err := start(func(p *peer) { p.extraOpts = []ConfigOption{opt, model} }, Config{Mode: "ask", Strict: true, Options: map[string]string{opt.ID: opt.Options[1].Value}})
		if err == nil {
			c.Close()
			t.Errorf("%s: a strict start set %s", name, opt.ID)
		}
		if sets(p) != 0 {
			t.Errorf("%s: set_config_option was sent", name)
		}
	}
	c, p, err := start(func(p *peer) { p.extraOpts = []ConfigOption{perm, model} }, Config{Mode: "ask", Strict: true, Options: map[string]string{"model": "b", "nosuch": "x"}})
	if err != nil || sets(p) != 1 {
		t.Fatalf("a strict start with a model: %v, %d set_config_option", err, sets(p))
	}
	c.Close()
	// the mode as an option of category mode is the mode's, never an option's
	c, p, err = start(func(p *peer) { p.modeOpt = true }, Config{Strict: true, Options: map[string]string{"agent": "plan"}})
	if err == nil {
		c.Close()
		t.Error("a strict start moved the mode through an option")
	}
	if sets(p) != 0 {
		t.Error("set_config_option was sent for the mode option")
	}
}
