package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// gateBox is a sandbox as a manager would report it: alice's own, homed in
// her partition (user mode) or at the agent's own identity (global).
func gateBox(home string) *sbxSandbox {
	b := &sbxSandbox{ID: "b1", Name: "work", Visibility: visPrivate, Home: "/home/dev", Workdir: "/work"}
	b.Owner.User, b.Owner.Via = "alice", "apps/agent"
	if home != "" {
		b.Owner.PartitionID, b.Owner.Partition = home, "user:alice"
	}
	return b
}

// The credential gate, clause by clause: a person's token only in their own
// private sandbox homed in their partition, never one a hosted
// conversation used (nor its fork's source), never a seed clone; the bot
// only where the sandbox is homed at this agent and every one of its users
// is a participant of the project — never a team's seed, never by a label.
func TestCredGateMatrix(t *testing.T) {
	t.Run("person", func(t *testing.T) {
		fx := credFixture(t, modeUser)
		db := fx.ag.db
		base := *fx.p
		cases := []struct {
			name string
			p    func(p *Project)
			box  func(b *sbxSandbox)
			ref  string
			want string
		}{
			{"private, own, homed", nil, nil, "", ""},
			{"shared", nil, func(b *sbxSandbox) { b.Shared = true }, "", whyNotPrivate},
			{"member-shared", nil, func(b *sbxSandbox) { b.Members = []string{"bob"} }, "", whyNotPrivate},
			{"team-visible", nil, func(b *sbxSandbox) { b.Visibility = visTeam }, "", whyNotPrivate},
			{"shares", nil, func(b *sbxSandbox) { b.Shares = json.RawMessage(`[{"consumer":"apps/x"}]`) }, "", whyNotPrivate},
			{"another owner's", nil, func(b *sbxSandbox) { b.Owner.User = "bob" }, "", whyNotPrivate},
			{"not homed here", nil, func(b *sbxSandbox) { b.Owner.PartitionID = "u-other" }, "", whyNotHomed},
			{"at its own global identity", nil, func(b *sbxSandbox) { b.Owner.PartitionID, b.Owner.Partition = "", "" }, "", whyNotHomed},
			{"hosted-used", nil, nil, "apps/cs|hosted", whyHosted},
			{"a fork of a hosted-used one", func(p *Project) { p.SandboxRef = "apps/cs|hosted" }, nil, "apps/cs|fork", whyHosted},
			{"a fork", nil, nil, "apps/cs|fork", ""},
			{"a seed clone", func(p *Project) { p.FromSeed = true }, nil, "", whySeedClone},
			{"a team seed", func(p *Project) { p.Kind = projTeam }, nil, "", whyKind},
			{"a team-visible project", func(p *Project) { p.Visibility = visTeam }, nil, "", whyShared},
			{"a project with a member", func(p *Project) { p.ID = 99 }, nil, "", whyShared},
			{"another person's project", func(p *Project) { p.Owner = "bob" }, nil, "", whyNotOwn},
			{"a membership", func(p *Project) { p.Kind = projMembership }, nil, "", ""},
			{"a home a helper line can't carry", nil, func(b *sbxSandbox) { b.Home = "/home/o'brien" }, "", whyHome},
		}
		db.noteHostedUse("apps/cs|hosted")
		if _, err := db.q.Exec(`INSERT INTO project_members (project_id, user) VALUES (99, 'bob')`); err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			p := base
			box := gateBox(alicePID)
			if c.p != nil {
				c.p(&p)
			}
			if c.box != nil {
				c.box(box)
			}
			ref := c.ref
			if ref == "" {
				ref = p.SandboxRef
			}
			if got := scmCredWhy(&p, scmAsPerson, ref, box); got != c.want {
				t.Errorf("%s: %q, want %q", c.name, got, c.want)
			}
		}
		// the bot in a person's partition: only as the policy says
		p := base
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, gateBox(alicePID)); got != whyPolicyBot {
			t.Errorf("bot without policy.as: %q", got)
		}
		p.Policy = json.RawMessage(`{"as":"bot"}`)
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, gateBox(alicePID)); got != "" {
			t.Errorf("bot with policy.as bot: %q", got)
		}
		p.Kind = projMembership
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, gateBox(alicePID)); got != whyPolicyBot {
			t.Errorf("a membership as the bot without membersAsBot: %q", got)
		}
		p.Policy = json.RawMessage(`{"as":"bot","membersAsBot":true}`)
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, gateBox(alicePID)); got != "" {
			t.Errorf("a membership as the bot under membersAsBot: %q", got)
		}
		p.FromSeed = true // the seed's clone: the bot's, never a person's
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, gateBox(alicePID)); got != "" {
			t.Errorf("the bot in a seed clone: %q", got)
		}
		if got := scmCredWhy(&p, scmAsBot, "apps/cs|fork", gateBox(alicePID)); got != "" {
			t.Errorf("the bot in a fork: %q", got)
		}
		box := gateBox(alicePID)
		box.Shared = true
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, box); got != whyNotHomed {
			t.Errorf("the bot in a sandbox seen through a share: %q", got)
		}
		if got := scmCredWhy(&p, scmAsBot, p.SandboxRef, gateBox("u-other")); got != whyNotHomed {
			t.Errorf("bot, not homed: %q", got)
		}
		if got := scmCredWhy(&p, "admin", p.SandboxRef, gateBox(alicePID)); got != whyBadIdentity {
			t.Errorf("an unknown identity: %q", got)
		}
	})

	t.Run("bot at global", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		db := fx.ag.db
		db.noteHostedUse("apps/cs|hosted")
		base := *fx.p
		cases := []struct {
			name   string
			p      func(p *Project)
			box    func(b *sbxSandbox)
			levels map[string]level
			ref    string
			want   string
		}{
			{"its owner, private", nil, nil, nil, "", ""},
			{"no person (the agent made it)", nil, func(b *sbxSandbox) { b.Owner.User = "" }, nil, "", ""},
			{"another owner, no level (the default lvNone)", nil, func(b *sbxSandbox) { b.Owner.User = "bob" }, nil, "", whyNotPart},
			{"another owner, a viewer", nil, func(b *sbxSandbox) { b.Owner.User = "bob" }, map[string]level{"bob": lvViewer}, "", whyNotPart},
			{"another owner, a participant", nil, func(b *sbxSandbox) { b.Owner.User = "bob" }, map[string]level{"bob": lvParticipant}, "", ""},
			{"a member below participant", nil, func(b *sbxSandbox) { b.Members = []string{"carol"} }, map[string]level{"carol": lvViewer}, "", whyNotPart},
			{"a member participant", nil, func(b *sbxSandbox) { b.Members = []string{"carol"} }, map[string]level{"carol": lvParticipant}, "", ""},
			{"team-visible, a private project", nil, func(b *sbxSandbox) { b.Visibility = visTeam }, nil, "", whyNotPart},
			{"team-visible, team viewers", func(p *Project) { p.Visibility, p.TeamRole = visTeam, roleViewer }, func(b *sbxSandbox) { b.Visibility = visTeam }, nil, "", whyNotPart},
			{"team-visible, team participants", func(p *Project) { p.Visibility, p.TeamRole = visTeam, roleParticipant }, func(b *sbxSandbox) { b.Visibility = visTeam }, nil, "", ""},
			{"shares", nil, func(b *sbxSandbox) { b.Shares = json.RawMessage(`[{"consumer":"apps/x","users":"*"}]`) }, nil, "", whyNotPrivate},
			{"seen through a share", nil, func(b *sbxSandbox) { b.Shared = true }, nil, "", whyNotHomed},
			{"another tile's", nil, func(b *sbxSandbox) { b.Owner.Via = "apps/other" }, nil, "", whyNotHomed},
			{"homed in a person's partition", nil, func(b *sbxSandbox) { b.Owner.PartitionID = alicePID }, nil, "", whyNotHomed},
			{"hosted-used", nil, nil, nil, "apps/cs|hosted", whyHosted},
			{"a fork of a hosted-used one", func(p *Project) { p.SandboxRef = "apps/cs|hosted" }, nil, nil, "apps/cs|fork", whyHosted},
			{"a team seed", func(p *Project) { p.Kind = projTeam }, nil, nil, "", whyKind},
		}
		for _, c := range cases {
			p := base
			box := gateBox("")
			if c.p != nil {
				c.p(&p)
			}
			if c.box != nil {
				c.box(box)
			}
			fx.levels = c.levels
			if fx.levels == nil {
				fx.levels = map[string]level{}
			}
			ref := c.ref
			if ref == "" {
				ref = p.SandboxRef
			}
			if got := scmCredWhy(&p, scmAsBot, ref, box); got != c.want {
				t.Errorf("%s: %q, want %q", c.name, got, c.want)
			}
		}
		if got := scmCredWhy(&base, scmAsPerson, base.SandboxRef, gateBox("")); got != whyNoPerson {
			t.Errorf("a person at global: %q", got)
		}
		// the default projectLevelOf (Projects not in) fails closed for anyone but no one
		projectLevelOf = func(*Project, string) level { return lvNone }
		if got := scmCredWhy(&base, scmAsBot, base.SandboxRef, gateBox("")); got != whyNotPart {
			t.Errorf("with no project levels known: %q", got)
		}
		if scmProjectAs(&base) != scmAsBot {
			t.Error("a project at global works as the bot")
		}
	})
}

// A refusal blocks the credential, empties what was there, and fails the
// task with the gate's words.
func TestCredGateBlocks(t *testing.T) {
	fx := credFixture(t, modeUser)
	run := fx.task(t, 1, statusIdle)
	_ = run
	fx.ensure(t)
	tok := fx.scm.Tokens()[0].Value
	setBox(t, fx.box.ID, func(b *fsbBox) { b.Visibility = visTeam }) // shared at the manager, behind the agent's back
	err := ensureCreds(t.Context(), fx.p, &ProjectTask{ProjectID: fx.p.ID, N: 1}, "", scmMinLeft)
	if err == nil {
		t.Fatal("ensure in a shared sandbox")
	}
	if got := readFile(t, fx.credFile("github.com.cred")); got != "" {
		t.Fatalf("the credential is still there: %q", got)
	}
	row := credRowOf(t, fx)
	if row.State != credBlocked || row.Why != whyNotPrivate {
		t.Fatalf("row: %+v", row)
	}
	var ws, msg string
	_ = fx.ag.db.q.QueryRow(`SELECT ws, error FROM project_tasks WHERE n=1`).Scan(&ws, &msg)
	if ws != wsFailed || msg != "credentials can't go into work: "+scmWhyWords(whyNotPrivate) {
		t.Fatalf("task: %s %q", ws, msg)
	}
	if got := fx.scm.Revoked(); len(got) != 1 || got[0] != "proj:k3x9qa:"+fx.ref {
		t.Fatalf("revoked: %v", got)
	}
	if scmLiveGet(scmLiveKey(fx.p.ID, fx.ref, "github.com")) != nil || redactText(tok) == tok {
		t.Fatal("the blocked token is still live, or no longer masked")
	}
	// private again: the next ensure writes it, and the task goes back
	setBox(t, fx.box.ID, func(b *fsbBox) { b.Visibility = visPrivate })
	if err := ensureCreds(t.Context(), fx.p, nil, "", scmMinLeft); err != nil {
		t.Fatal(err)
	}
	_ = fx.ag.db.q.QueryRow(`SELECT ws, error FROM project_tasks WHERE n=1`).Scan(&ws, &msg)
	if ws != wsReady || msg != "" {
		t.Fatalf("task after: %s %q", ws, msg)
	}
}

// The bot rule: managers (and every element) may name any repo for the
// bot; a person only as the rule names them, for a repo a glob matches;
// view-as never; a person's partition never reads it. The routes are the
// managers', and the rule can't be set in a person's partition; the repo
// list at a bot home holds only what the caller may name.
func TestScmBotRule(t *testing.T) {
	fx := credFixture(t, modeGlobal)
	mux := fx.h.(*http.ServeMux)
	fx.scm.AddRepo("acme/api", false)
	fx.scm.AddRepo("other/x", false)
	w := func(c caller) who {
		return who{kind: map[bool]whoKind{true: whoUser, false: whoElement}[c.user != ""], user: c.user, level: c.level, el: c.from, viewedBy: c.viewedBy}
	}
	if !scmBotAllowed(w(asMgr), "acme/web") || !scmBotAllowed(w(asElement), "other/x") {
		t.Fatal("managers and elements may name any repo")
	}
	if scmBotAllowed(w(asBob), "acme/web") {
		t.Fatal("an unnamed person may not")
	}
	if got := callAs(t, mux, asBob, "GET", "/projects/scm/bot", nil); got.Code != 403 {
		t.Fatalf("GET by a non-manager: %d", got.Code)
	}
	if got := callAs(t, mux, asBob, "PUT", "/projects/scm/bot", scmBotRule{Users: []string{"bob"}, Repos: []string{"acme/*"}}); got.Code != 403 {
		t.Fatalf("PUT by a non-manager: %d", got.Code)
	}
	for _, bad := range []scmBotRule{{Users: []string{"Bob Smith"}}, {Repos: []string{"acme"}}, {Repos: []string{"acme/[x"}}, {Repos: []string{"a/b/c"}}} {
		if got := callAs(t, mux, asMgr, "PUT", "/projects/scm/bot", bad); got.Code != 400 {
			t.Fatalf("PUT %+v: %d %s", bad, got.Code, got.Body)
		}
	}
	var rule scmBotRule
	got := callAs(t, mux, asMgr, "PUT", "/projects/scm/bot", scmBotRule{Users: []string{"bob", "bob"}, Repos: []string{"ACME/*"}})
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &rule) != nil || len(rule.Users) != 1 || rule.Repos[0] != "acme/*" {
		t.Fatalf("PUT: %d %s", got.Code, got.Body)
	}
	if got := callAs(t, mux, asMgr, "GET", "/projects/scm/bot", nil); got.Code != 200 || !json.Valid(got.Body.Bytes()) {
		t.Fatalf("GET: %d %s", got.Code, got.Body)
	}
	if !scmBotAllowed(w(asBob), "acme/web") || !scmBotAllowed(w(asBob), "Acme/API") {
		t.Fatal("bob, named, for acme/*")
	}
	if scmBotAllowed(w(asBob), "other/x") || scmBotAllowed(w(asCarol), "acme/web") || scmBotAllowed(w(caller{user: "bob", level: "read", viewedBy: "mgr"}), "acme/web") {
		t.Fatal("outside the glob, another person, or view-as")
	}
	var page scmPage[scmRepo]
	got = callAs(t, mux, asBob, "GET", "/projects/scm/repos", nil)
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
		t.Fatalf("bob's repos: %d %s", got.Code, got.Body)
	}
	got = callAs(t, mux, asCarol, "GET", "/projects/scm/repos", nil)
	if json.Unmarshal(got.Body.Bytes(), &page) != nil || len(page.Items) != 0 {
		t.Fatalf("carol's repos: %s", got.Body)
	}
	got = callAs(t, mux, asMgr, "GET", "/projects/scm/repos?scm=apps/scm-github", nil)
	if json.Unmarshal(got.Body.Bytes(), &page) != nil || len(page.Items) != 3 {
		t.Fatalf("a manager's repos: %s", got.Body)
	}

	t.Run("in a person's partition", func(t *testing.T) {
		fx := credFixture(t, modeUser)
		mux := fx.h.(*http.ServeMux)
		_ = fx.ag.db.putSetting(settingSCMBotRule, mustJSON(scmBotRule{Users: []string{"alice"}, Repos: []string{"*/*"}}))
		if scmBotAllowed(w(asAlice), "acme/web") {
			t.Fatal("a person's partition read the rule")
		}
		if got := callAs(t, mux, asMgr, "PUT", "/projects/scm/bot", scmBotRule{}); got.Code != 409 {
			t.Fatalf("PUT in a partition: %d", got.Code)
		}
		got := callAs(t, mux, asAlice, "GET", "/projects/scm/repos", nil)
		if json.Unmarshal(got.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
			t.Fatalf("the person's own repos: %s", got.Body)
		}
	})
}

// The hosted-used clause the other way round: a hosted (non-secure)
// conversation doesn't work in a sandbox that already holds a project's
// credential — its members could have the agent read the files or push
// with them — and the credential stays where it is, its person's. Once it
// is scrubbed the conversation may, and the sandbox is hosted-used from
// then on: the gate refuses the next write. A use waits on a write in
// progress (the sandbox's lock), so neither slips past the other.
func TestHostedUseRefusedWhereCred(t *testing.T) {
	fx := credFixture(t, modeUser)
	fx.ensure(t)
	tok := fx.scm.Tokens()[0].Value
	cfg := defaultConfig()
	bd := SandboxBinding{Ref: fx.ref, Name: fx.box.Name, Egress: fx.box.Egress}
	cfg.Class, cfg.Sandbox, cfg.Attached = "coding", &bd, []SandboxBinding{bd}
	hosted := teamIDBase + 7
	_, err := fx.ag.sandboxUse(context.Background(), hosted, cfg, "")
	if sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "a project's credential for its code host is there") {
		t.Fatalf("a hosted conversation in a sandbox holding her credential: %v", err)
	}
	if fx.ag.db.hostedUsed(fx.ref) {
		t.Fatal("a refused use was noted as hosted-used")
	}
	if row := credRowOf(t, fx); row.State != credLive || !strings.Contains(readFile(t, fx.credFile("github.com.cred")), tok) {
		t.Fatalf("her credential was disturbed: %+v", row)
	}
	// her own conversation works there as before
	if _, err := fx.ag.sandboxUse(context.Background(), partitionIDBase+50, cfg, ""); err != nil && strings.Contains(err.Error(), "non-secure") {
		t.Fatalf("her own conversation: %v", err)
	}
	// a live row whose files couldn't be emptied refuses the same
	if err := fx.ag.db.scmCredUnemptied(fx.p.ID, fx.ref, "github.com", scrubShare); err != nil {
		t.Fatal(err)
	}
	if why := scmHostedUse(fx.ag.db, fx.ref, "work"); why == "" {
		t.Fatal("an unemptied credential")
	}
	// scrubbed: the conversation may, and the gate refuses from then on
	if err := scmScrub(context.Background(), fx.p, fx.ref, scrubStop); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ag.sandboxUse(context.Background(), hosted, cfg, ""); err != nil && strings.Contains(err.Error(), "non-secure") {
		t.Fatalf("after the scrub: %v", err)
	}
	if !fx.ag.db.hostedUsed(fx.ref) {
		t.Fatal("the use wasn't noted")
	}
	if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err == nil || !strings.Contains(err.Error(), "non-secure (hosted)") {
		t.Fatalf("a write after the hosted use: %v", err)
	}
	// a write in progress (its lock held) holds the use back until it ends
	other := sandboxRef("apps/cs", "another")
	release := scmHoldSandbox(other)
	done := make(chan string, 1)
	go func() { done <- scmHostedUse(fx.ag.db, other, "another") }()
	time.Sleep(150 * time.Millisecond)
	if fx.ag.db.hostedUsed(other) {
		release()
		t.Fatal("the use was noted while a write held the sandbox")
	}
	release()
	if why := <-done; why != "" || !fx.ag.db.hostedUsed(other) {
		t.Fatalf("after the write: %q", why)
	}
	// a note that can't be written refuses the use (fail closed): no
	// sandbox is used unnoted
	if _, err := fx.ag.db.q.Exec(`CREATE TRIGGER no_note BEFORE INSERT ON hosted_sandboxes BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	third := sandboxRef("apps/cs", "third")
	if why := scmHostedUse(fx.ag.db, third, "third"); why == "" || fx.ag.db.hostedUsed(third) {
		t.Fatalf("a use whose note failed: %q", why)
	}
}
