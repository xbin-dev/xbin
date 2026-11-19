package server

// delegateupdates.go — GET /delegate/updates[?tile=]: what has an update
// waiting, for a person's agent, in one read (D248; plans/wsagent/
// updates.md §1; docs/protocol/delegation.md §Updates).
//
// Nothing here is new reach. Each row is a row of the person's own by-hand
// listings — GET /builtins/updates (any signed-in principal; the broker's
// BuiltinUpdateRows is its function) and GET /templates/updates (the tiles
// the person can read; TemplateUpdateRows is its function) — held, row by
// row, to the agent's gates: the tile readable, inside the conversation's
// scope, its orgs' rules letting the agent in; a row that is shown taints
// the conversation with the tile's orgs, as a listing's row does. What a
// row adds is cheap and exact: the files edited here only (the by-hand
// row's own file statuses, counted), an open builtin-update proposal on
// the tile (the person's GET /code/prs?target=), an instance's commits
// behind its template and of its own (its git history), and the next
// step, with xbind's brief where it is a tile agent's.
//
// The brief is built from xbind's own names only — the tile's path, the
// template's catalog name, the builtin's id, counts, a proposal's number:
// never a title, a commit message or anything else a tile wrote.
// TestDelegateUpdatesIsTheByHandListing holds the rows to the by-hand
// listings for every agent level, scope and org rule.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/registry"
)

// UpdatesSource is the broker's part of the read (the file comment).
type UpdatesSource interface {
	// BuiltinUpdateRows are GET /builtins/updates' rows, each with the open
	// builtin-update proposal on its tile (0: none).
	BuiltinUpdateRows() ([]BuiltinUpdateRow, error)
	// TemplateUpdateRows are GET /templates/updates' rows for p, of the
	// tiles keep lets through (asked only of rows the by-hand listing
	// has), with their commit counts.
	TemplateUpdateRows(p auth.Principal, keep func(tile string) bool) []TemplateUpdateRow
}

// BuiltinUpdateRow is one row of GET /builtins/updates, as the read needs it.
type BuiltinUpdateRow struct {
	ID, Kind, Name, Tile     string
	From, To                 int
	Missing, Adopted         bool
	Clean, Conflicts, Edited int
	Proposal                 int
}

// TemplateUpdateRow is one row of GET /templates/updates, with its counts:
// Behind, the template's commits the instance lacks; Local, its own
// non-merge commits that are no template's and not xbind's instantiate
// commit (-1 each: not counted — a history past the bound).
type TemplateUpdateRow struct {
	Tile, Template string
	Behind, Local  int
	Legacy         bool
}

// UpdateBrief is xbind's brief for a tile agent that applies an update:
// the fields of tile_agent_brief, and the commands it names, in order.
type UpdateBrief struct {
	Goal       string   `json:"goal"`
	DoneWhen   string   `json:"doneWhen"`
	LeaveAlone string   `json:"leaveAlone"`
	CheckBy    string   `json:"checkBy"`
	Known      string   `json:"known"`
	Commands   []string `json:"commands"`
}

// The next steps a row names.
const (
	updateNextPropose   = "propose"    // a builtin.update line
	updateNextTileAgent = "tile-agent" // the tile's agent, with xbind's brief
	updateNextByHand    = "by-hand"    // nothing a workspace agent can take further: why says who can
)

type builtinUpdateView struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Tile      string       `json:"tile"`
	Title     string       `json:"title,omitempty"`
	From      int          `json:"from"`
	To        int          `json:"to"`
	Missing   bool         `json:"missing,omitempty"`
	Adopted   bool         `json:"adopted,omitempty"`
	Clean     int          `json:"clean"`
	Conflicts int          `json:"conflicts"`
	Edited    int          `json:"edited"`
	Level     string       `json:"level"`
	Agent     string       `json:"agent,omitempty"`
	Proposal  int          `json:"proposal,omitempty"`
	Next      string       `json:"next"`
	Why       string       `json:"why,omitempty"`
	Brief     *UpdateBrief `json:"brief,omitempty"`
}

type instanceUpdateView struct {
	Tile     string       `json:"tile"`
	Title    string       `json:"title,omitempty"`
	Template string       `json:"template"`
	Behind   int          `json:"behind"`
	Local    int          `json:"local"`
	Legacy   bool         `json:"legacy,omitempty"`
	Level    string       `json:"level"`
	Agent    string       `json:"agent,omitempty"`
	Next     string       `json:"next"`
	Why      string       `json:"why,omitempty"`
	Brief    *UpdateBrief `json:"brief,omitempty"`
}

func (s *Server) registerUpdatesAPI() {
	s.RegisterAPI("GET /delegate/updates", s.apiDelegateUpdates)
}

// apiDelegateUpdates — GET /delegate/updates[?tile=]: the person's agent
// (the file comment). With ?tile= the gate judged that tile (byTileQuery);
// the rows are held one by one either way.
func (s *Server) apiDelegateUpdates(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	only := strings.Trim(r.URL.Query().Get("tile"), "/")
	if only != "" && (s.tileHidden(p, only) || s.delegateHides(p, only)) {
		delegateRefuse(w, http.StatusForbidden, "tile", DelegateRead.String(), noTileFor(p.UserID))
		return
	}
	u, err := s.delegateUpdates(p, only)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the builtins' updates can't be read: "+err.Error(), delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"builtins": u.builtins, "instances": u.instances,
		"leftOut": map[string]int{"hiddenByYou": u.hidden, "blocked": u.blocked}})
}

// updatesRead is GET /delegate/updates' answer.
type updatesRead struct {
	builtins        []builtinUpdateView
	instances       []instanceUpdateView
	hidden, blocked int
}

// delegateUpdates is the read for p's agent (only: one tile, "" all) —
// each row held to the agent's gates (the file comment).
func (s *Server) delegateUpdates(p auth.Principal, only string) (updatesRead, error) {
	src, ok := s.ChangeSets.(UpdatesSource)
	if !ok {
		return updatesRead{builtins: []builtinUpdateView{}, instances: []instanceUpdateView{}}, nil
	}
	person := personOf(p)
	h, hasHandle := s.handleOf(p)
	hidden, blocked := 0, 0
	// keep is the agent's gate on a by-hand row's tile: readable (a missing
	// page: the path the person could read), in scope, let in by its orgs'
	// rules — and a row that is shown taints the conversation
	keep := func(tile string, exists bool) bool {
		switch {
		case only != "" && tile != only:
			return false
		case exists && !s.tileExists(tile), !p.CanReadTile(tile):
			return false
		case hasHandle && !s.inSessionScope(h, tile):
			hidden++
			return false
		case s.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}) != nil:
			blocked++
			return false
		}
		s.taint(p, tile)
		return true
	}
	rows, err := src.BuiltinUpdateRows()
	if err != nil {
		return updatesRead{}, err
	}
	builtinsOut := []builtinUpdateView{}
	for _, row := range rows {
		c, exists := s.Reg.Component(row.Tile)
		if !keep(row.Tile, !row.Missing) {
			continue
		}
		v := builtinUpdateView{ID: row.ID, Kind: row.Kind, Tile: row.Tile, From: row.From, To: row.To, Missing: row.Missing, Adopted: row.Adopted,
			Clean: row.Clean, Conflicts: row.Conflicts, Edited: row.Edited, Level: levelOf(person, row.Tile), Proposal: row.Proposal}
		agent := ""
		if exists {
			v.Title = c.Manifest.DisplayTitle()
			agent, _ = s.agentLevelOf(c)
			v.Agent = agent
		}
		switch {
		case agent == registry.AgentOff:
			v.Next, v.Why = updateNextByHand, fmt.Sprintf("%s's agent level is off: a workspace agent takes no line about it — an admin updates it by hand, in the Tile Manager's updates tab (or bx builtin update %s --safe)", row.Tile, row.ID)
		case row.Proposal > 0:
			v.Next, v.Why, v.Brief = s.tileAgentNext(person, row.Tile, func() UpdateBrief { return BuiltinProposalBrief(row.Tile, row.ID, row.Proposal) })
		default:
			v.Next = updateNextPropose
		}
		builtinsOut = append(builtinsOut, v)
	}
	instances := []instanceUpdateView{}
	for _, row := range src.TemplateUpdateRows(p, func(tile string) bool { return keep(tile, true) }) {
		c, _ := s.Reg.Component(row.Tile)
		v := instanceUpdateView{Tile: row.Tile, Template: row.Template, Behind: row.Behind, Local: row.Local, Legacy: row.Legacy, Level: levelOf(person, row.Tile)}
		if c != nil {
			v.Title = c.Manifest.DisplayTitle()
			v.Agent, _ = s.agentLevelOf(c)
		}
		if v.Agent == registry.AgentOff {
			v.Next, v.Why = updateNextByHand, row.Tile+"'s agent level is off: a workspace agent starts no tile agent there — whoever has terminal access to it merges the template in a terminal there"
		} else {
			v.Next, v.Why, v.Brief = s.tileAgentNext(person, row.Tile, func() UpdateBrief { return TemplateUpdateBrief(row.Tile, row.Template, row.Behind, row.Legacy) })
		}
		instances = append(instances, v)
	}
	return updatesRead{builtins: builtinsOut, instances: instances, hidden: hidden, blocked: blocked}, nil
}

// updateBriefOf is xbind's brief for the update of tile that a tile agent
// applies — the brief of tile's row in p's agent's read — or why there is
// none.
func (s *Server) updateBriefOf(p auth.Principal, tile string) (*UpdateBrief, string) {
	if s.tileHidden(p, tile) || s.delegateHides(p, tile) {
		return nil, noTileFor(p.UserID)
	}
	u, err := s.delegateUpdates(p, tile)
	if err != nil {
		return nil, "the updates can't be read: " + err.Error()
	}
	why := ""
	for _, r := range u.builtins {
		switch {
		case r.Brief != nil:
			return r.Brief, ""
		case r.Next == updateNextPropose:
			why = "its update is a builtin.update line, not a tile agent's"
		case why == "":
			why = r.Why
		}
	}
	for _, r := range u.instances {
		if r.Brief != nil {
			return r.Brief, ""
		}
		why = cmpOr(why, r.Why)
	}
	return nil, cmpOr(why, "xbin lists no update for "+tile+" that a tile agent applies")
}

// The brief a start names instead of its text (D248 review, finding 7):
// {"template": "update"} is xbind's update brief for the start's tile,
// written by xbind — so the person's review can tell xbin's words from
// the agent's.
const briefTemplateUpdate = "update"

// Who wrote a start's first prompt, as the review labels it.
const (
	BriefByXbin  = "xbin"  // xbind's update brief, resolved from {"template": "update"}
	BriefByAgent = "agent" // text the agent sent — whatever it looks like
)

// startBrief resolves a start body's {"brief": {"template": "update"}}:
// the body with xbind's brief as its "prompt" (and no "brief"), and who
// wrote the prompt — xbind, or the agent for a body that carries its own
// text. A body with neither is returned as it is, by "". The rewrite is
// deterministic: the same body and the same update give the same bytes
// (a ticket binds them).
func (s *Server) startBrief(p auth.Principal, body []byte) ([]byte, string, string) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body, "", ""
	}
	ref, named := m["brief"]
	if !named {
		var prompt string
		if json.Unmarshal(m["prompt"], &prompt) == nil && strings.TrimSpace(prompt) != "" {
			return body, BriefByAgent, ""
		}
		return body, "", ""
	}
	var r struct {
		Template string `json:"template"`
	}
	dec := json.NewDecoder(bytes.NewReader(ref))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || r.Template != briefTemplateUpdate {
		return nil, "", `a start's brief is {"template": "update"} (xbin's update brief for its tile), or its text as "prompt"`
	}
	if _, both := m["prompt"]; both {
		return nil, "", `a start names its brief or sends its text as "prompt" — not both`
	}
	var cwd string
	_ = json.Unmarshal(m["cwd"], &cwd)
	tile := strings.Trim(cwd, "/")
	b, why := s.updateBriefOf(p, tile)
	if b == nil {
		return nil, "", `the brief {"template": "update"}: ` + why
	}
	delete(m, "brief")
	m["prompt"], _ = json.Marshal(updateBriefText(personOf(p).UserID, *b))
	out, err := json.Marshal(m)
	if err != nil {
		return nil, "", err.Error()
	}
	return out, BriefByXbin, ""
}

// updateBriefText is an update brief as the tile agent is told it — the
// layout the workspace agent's own briefs have (wsBriefText).
func updateBriefText(person string, b UpdateBrief) string {
	var t strings.Builder
	fmt.Fprintf(&t, "This is a piece of work in this tile, handed to you by %s's workspace agent on their behalf, with xbin's own brief for it.\n\n", cmpOr(person, "your person"))
	fmt.Fprintf(&t, "Goal: %s\nDone when: %s\n", b.Goal, b.DoneWhen)
	for _, f := range [][2]string{{"Leave alone", b.LeaveAlone}, {"Check by", b.CheckBy}, {"Already known", b.Known}} {
		if strings.TrimSpace(f[1]) != "" {
			fmt.Fprintf(&t, "%s: %s\n", f[0], f[1])
		}
	}
	t.WriteString("\nWork inside this tile only. When you are done, answer with: what you changed (each file in a sentence), what you ran to check it and how it ended, and anything you could not do or could not verify. " +
		"If you need a decision or a permission, ask: your questions go to " + cmpOr(person, "the person") + ", not to the workspace agent.")
	return t.String()
}

// tileAgentNext is the next step of a row whose update is a tile agent's
// to apply: that tile's agent with xbind's brief — or by hand where no
// tile agent of the person's can open there (the shell and root have
// none; a tile agent opens as its person, at terminal level).
func (s *Server) tileAgentNext(person auth.Principal, tile string, brief func() UpdateBrief) (string, string, *UpdateBrief) {
	switch {
	case !briefPathOK(tile):
		return updateNextByHand, "its path holds characters a command can't carry: whoever has terminal access to it merges it there", nil
	case isChrome(tile):
		return updateNextByHand, "no tile agent opens on " + tile + ": an admin merges it by hand (bx code prs " + tile + ")", nil
	case levelOf(person, tile) != "terminal":
		return updateNextByHand, person.UserID + " has no terminal access to " + tile + ", and a tile agent opens as them: whoever has (its owner, an admin) merges it there", nil
	}
	if s.Term != nil { // the brief fetches through xbind: a session that can't reach it can't do the work (D17's terminal grants)
		if ok, why := s.Term.SessionReachesXbind(person, tile); !ok {
			return updateNextByHand, "a tile agent opens as " + person.UserID + ", and " + why + ": whoever's sessions reach xbin there (an admin) merges it", nil
		}
	}
	b := brief()
	return updateNextTileAgent, "", &b
}

// briefGit is git as a brief runs it in a tile: none of the tile's own
// hooks, no fsmonitor command, no ext:: transport. What is still the tile
// writer's in a session there — its .gitattributes, filters and merge
// drivers, the manifest's merge driver among them — is bounded by the
// session's confinement (the tile's own sandbox, as the person; v0.3.82's
// untrusted-tile confinement): plans/wsagent/updates.md §The brief.
const briefGit = "git -c core.fsmonitor= -c core.hooksPath=/dev/null -c protocol.ext.allow=never"

// shq is s as one sh word: single-quoted, each ' closed, escaped and
// reopened — nothing in it expands.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// briefPathOK: a tile path a brief's commands can carry — single-quoted,
// so anything but a control character or broken UTF-8 is inert.
func briefPathOK(tile string) bool {
	if tile == "" || !utf8.ValidString(tile) {
		return false
	}
	for _, r := range tile {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// templateURL is the URL xbind serves a builtin template's repository at,
// as AddTemplateRemote writes it (the session's git reaches it through the
// gateway: its own credential, D17).
func templateURL(name string) string { return "http://xbin/api/xbin/templates/" + name + ".git" }

// TemplateUpdateBrief is xbind's brief for merging tile's template — the
// builtin template named template, behind commits ahead of it (legacy: an
// instance whose history shares nothing with the template's). Built from
// those names alone. It never relies on the tile's remote of that name
// (the tile's writers can point it anywhere): it fetches xbind's own URL
// into FETCH_HEAD. The commands are what a session in the tile runs, in
// its directory; TestTemplateUpdateInATileSession runs exactly them.
func TemplateUpdateBrief(tile, template string, behind int, legacy bool) UpdateBrief {
	merge := briefGit + " merge --no-edit FETCH_HEAD"
	if legacy {
		merge = briefGit + " merge --no-edit --allow-unrelated-histories FETCH_HEAD"
	}
	commits := "commits"
	switch {
	case behind == 1:
		commits = "1 commit"
	case behind > 1:
		commits = fmt.Sprintf("%d commits", behind)
	}
	cmds := []string{
		briefGit + " status --short",
		briefGit + " fetch " + shq(templateURL(template)) + " main",
		briefGit + " log --oneline HEAD..FETCH_HEAD",
		merge,
		"bx status",
		`curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/$XBIN_COMPONENT/"`,
	}
	known := fmt.Sprintf("This tile was made from xbin's template %q, which xbin serves; this session's git reaches it with the session's own credential. Run the commands exactly as written — they fetch xbin's own copy by its URL, not whatever this tile's remotes name, and keep this tile's git hooks out. "+
		"Run, in this tile's directory: (1) `%s` — if anything is uncommitted, commit it first and say so; (2) `%s`; (3) `%s` — what the template changed; (4) `%s`. "+
		"xbin.json merges by keys (git runs `bx template merge-manifest` for it): what stays in conflict there is a value both sides changed. "+
		"On a conflict you can't resolve with confidence: `"+briefGit+" merge --abort`, and report which files and why — don't guess. "+
		"xbin builds and restarts the backend by itself after the merge: (5) `%s` says where it stands, and (6) a call to the tile's own API — `%s` — starts it; a build error is in `bx logs $XBIN_COMPONENT`.",
		template, cmds[0], cmds[1], cmds[2], cmds[3], cmds[4], cmds[5])
	if legacy {
		known += " This tile was made before xbin seeded instances from their template, so its history shares nothing with the template's: the first merge needs --allow-unrelated-histories (in step 4 already) and conflicts wherever both have a file — keep this tile's version where you can't tell."
	}
	return UpdateBrief{
		Goal:       fmt.Sprintf("Bring this tile up to date with its template %q: merge the template's newest version (%s this tile doesn't have) into this tile, keeping this tile's own changes.", template, commits),
		DoneWhen:   "`git log -1` in this tile shows the merge of the template's main, `git status --short` shows nothing uncommitted, the tile's backend (if it has one) builds and answers, and you reported what the template changed and how each conflict was resolved.",
		LeaveAlone: "this tile's own changes, unless one conflicts with the template's — then keep this tile's version unless the template's change needs it; the manifest's \"partition\" and \"template\" keys; everything outside this tile; and don't push anywhere.",
		CheckBy:    "`bx status`, the call to the tile's own API in step 6, `bx logs $XBIN_COMPONENT` for a build error, and the tile's own tests if it has any (`go test ./...` where its go.mod is).",
		Known:      known,
		Commands:   cmds,
	}
}

// BuiltinProposalBrief is xbind's brief for merging change proposal n on
// tile: the builtin id's update, which xbin filed there because it
// conflicts with the tile's own changes. Built from those names alone;
// TestBuiltinProposalInATileSession runs exactly its commands.
func BuiltinProposalBrief(tile, id string, n int) UpdateBrief {
	// the copy lives in the tile's own .git: writable and readable in every
	// session on the tile (the session's /tmp may not be — a workspace that
	// lives under /tmp), never a live file, and nothing git tracks
	// every path single-quoted (the tile's path is a directory name its
	// parent's writers chose); only $XBIN_WORKSPACE expands
	dir := `"$XBIN_WORKSPACE"/` + shq(tile)
	work := `"$XBIN_WORKSPACE"/` + shq(tile+"/.git/xbin-update")
	mbox := `"$XBIN_WORKSPACE"/` + shq(tile+"/.git/xbin-update.mbox")
	cmds := []string{
		briefGit + " status --short",
		"rm -rf " + work + " " + mbox + " && " + briefGit + " clone -q " + dir + " " + work,
		fmt.Sprintf("bx code pr fetch %d %s > %s", n, shq(tile), mbox),
		briefGit + " -C " + work + " am --3way " + mbox,
		briefGit + " -C " + dir + " pull --ff-only " + work,
		fmt.Sprintf(`bx code pr close %d %s --merged -m 'merged: <what you resolved>'`, n, shq(tile)),
		"rm -rf " + work + " " + mbox,
		"bx status",
	}
	known := fmt.Sprintf("xbin filed the update of the builtin %s as change proposal #%d on this tile, because it conflicts with changes made here; nothing of it is applied yet. "+
		"Merge it in a copy, so no conflict marker lands in the live files, running the commands exactly as written (they keep this tile's git hooks out): (1) `%s` — if anything is uncommitted, commit it first and say so; (2) `%s`; (3) `%s`; (4) `%s` — on a conflict: resolve each file in the copy, `%s -C %s add <file>`, `%s -C %s am --continue`; on one you can't resolve with confidence: `%s -C %s am --abort`, and report which files and why — don't guess; "+
		"(5) `%s` brings the merged result into the tile; (6) `%s` — closing it merged records in xbin the files that now hold the update; any file that doesn't stays offered as an update; (7) `%s` removes the copy. "+
		"xbin builds and restarts the backend by itself: (8) `%s` says where it stands; a build error is in `bx logs $XBIN_COMPONENT`. `bx code pr show %d %s` shows the proposal.",
		id, n, cmds[0], cmds[1], cmds[2], cmds[3], briefGit, work, briefGit, work, briefGit, work, cmds[4], cmds[5], cmds[6], cmds[7], n, shq(tile))
	return UpdateBrief{
		Goal:       fmt.Sprintf("Merge xbin's update of this tile — the builtin %s — which xbin filed as change proposal #%d on this tile because it conflicts with this tile's own changes: take the update and keep this tile's changes.", id, n),
		DoneWhen:   fmt.Sprintf("proposal #%d is closed merged (`bx code pr show %d %s` says so), the tile holds the merged files with no conflict marker, its backend (if it has one) builds and answers, and you reported how each conflict was resolved — and which files you kept different from the update, which xbin keeps offering as an update.", n, n, shq(tile)),
		LeaveAlone: "this tile's own changes, unless one conflicts with the update — then keep what this tile needs and take the rest of the update; the manifest's \"partition\" key; everything outside this tile; and don't push anywhere.",
		CheckBy:    "`bx status`, `bx logs $XBIN_COMPONENT` for a build error, a call to the tile's own API, and the tile's own tests if it has any.",
		Known:      known,
		Commands:   cmds,
	}
}
