// skills.go — a self-improving skill library (Hermes-inspired; see API.md).
// A skill is a named, described procedure the agent authored from experience.
// The compact list (name + description) is injected into context; the agent
// loads a full skill on demand with skill_view, writes one with skill_manage,
// and /learn distills the current run into a skill. Stored in the agent's own
// sqlite (no separate files). A background curator is a future add.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fmtStr coerces a tool argument to a string ("" for nil).
func fmtStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Created     int64  `json:"created"`
	Updated     int64  `json:"updated"`
	// Whose it is and which tool mode it came from (D83). Owner "" is a
	// shared skill (every skill from before, and what managers save); a
	// skill the agent writes belongs to its conversation's owner. Lane "" is
	// any mode; one learned in the web mode is never shown to a run with
	// internal reach, nor the other way round.
	Owner string `json:"owner"`
	Lane  string `json:"lane"`
}

// skillScope is which skills a run sees: the shared ones and its owner's,
// in its own lane.
type skillScope struct{ owner, lane string }

func (sc skillScope) sees(s *Skill) bool {
	return (s.Owner == "" || s.Owner == sc.owner) && (s.Lane == "" || s.Lane == sc.lane)
}

// scopeOf is a run's skill scope.
func (ag *Agent) scopeOf(run *Run, cfg Config) skillScope {
	sc := skillScope{lane: cfg.toolset()}
	if root, err := ag.db.getRun(rootOf(run)); err == nil {
		sc.owner = root.Owner
	}
	return sc
}

func (d *DB) upsertSkill(s *Skill) error {
	t := now()
	_, err := d.q.Exec(
		`INSERT INTO skills (name, description, content, created, updated, owner, lane) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET description=excluded.description, content=excluded.content, updated=excluded.updated,
		   owner=excluded.owner, lane=excluded.lane`,
		s.Name, s.Description, s.Content, t, t, s.Owner, s.Lane)
	return err
}

const skillCols = `name, description, content, created, updated, owner, lane`

func scanSkill(scan func(dest ...any) error) (*Skill, error) {
	s := &Skill{}
	return s, scan(&s.Name, &s.Description, &s.Content, &s.Created, &s.Updated, &s.Owner, &s.Lane)
}

func (d *DB) getSkill(name string) (*Skill, error) {
	return scanSkill(d.q.QueryRow(`SELECT `+skillCols+` FROM skills WHERE name=?`, name).Scan)
}

func (d *DB) listSkills() ([]*Skill, error) {
	rows, err := d.q.Query(`SELECT ` + skillCols + ` FROM skills ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Skill
	for rows.Next() {
		s, err := scanSkill(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// visibleSkills is the skill list a scope sees.
func (d *DB) visibleSkills(sc skillScope) []*Skill {
	all, _ := d.listSkills()
	var out []*Skill
	for _, s := range all {
		if sc.sees(s) {
			out = append(out, s)
		}
	}
	return out
}

func (d *DB) deleteSkill(name string) error {
	_, err := d.q.Exec(`DELETE FROM skills WHERE name=?`, name)
	return err
}

// --- tool implementation -------------------------------------------------

// runSkillTool handles the skill_* tools. cfg.feature("skills") gates whether
// they're advertised; this still executes if called. A run sees and writes
// only its scope's skills: the shared ones (read only) and its owner's.
func (ag *Agent) runSkillTool(run *Run, cfg Config, name string, args map[string]any) (string, error) {
	sc := ag.scopeOf(run, cfg)
	switch name {
	case "skills_list":
		skills := ag.db.visibleSkills(sc)
		if len(skills) == 0 {
			return "(no skills yet)", nil
		}
		var b strings.Builder
		for _, s := range skills {
			b.WriteString("- " + s.Name + ": " + s.Description + "\n")
		}
		return strings.TrimSpace(b.String()), nil

	case "skill_view":
		s, err := ag.db.getSkill(strings.TrimSpace(fmtStr(args["name"])))
		if err != nil || !sc.sees(s) {
			return "(no such skill)", nil
		}
		return s.Content, nil

	case "skill_manage":
		action := strings.TrimSpace(fmtStr(args["action"]))
		sname := strings.TrimSpace(fmtStr(args["name"]))
		if sname == "" {
			return "", fmt.Errorf("skill_manage needs a name")
		}
		// A run changes only its own scope's skills: never a shared one (it
		// would reach everyone's runs) or someone else's.
		if cur, err := ag.db.getSkill(sname); err == nil && (cur.Owner != sc.owner || !sc.sees(cur)) {
			if cur.Owner == "" && sc.owner != "" {
				return "", fmt.Errorf("%q is a shared skill — save yours under another name", sname)
			}
			return "", fmt.Errorf("a skill named %q already exists — pick another name", sname)
		}
		switch action {
		case "remove", "delete":
			if err := ag.db.deleteSkill(sname); err != nil {
				return "", err
			}
			return "removed skill " + sname, nil
		default: // save / add / replace
			s := &Skill{Name: sname, Description: strings.TrimSpace(fmtStr(args["description"])), Content: fmtStr(args["content"]),
				Owner: sc.owner, Lane: sc.lane}
			if s.Content == "" {
				return "", fmt.Errorf("skill_manage save needs content")
			}
			if err := ag.db.upsertSkill(s); err != nil {
				return "", err
			}
			return "saved skill " + sname, nil
		}
	}
	return "", fmt.Errorf("unknown skill tool %q", name)
}

// --- HTTP (for the tile) -------------------------------------------------

// handleListSkills: managers see every skill; others the shared ones and
// their own.
func handleListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := agent.db.listSkills()
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	c := callerOf(r)
	out := []*Skill{}
	for _, s := range skills {
		if c.manager() || s.Owner == "" || s.Owner == c.user {
			out = append(out, s)
		}
	}
	xbin.WriteJSON(w, 200, out)
}

// handleSaveSkill is a manager saving a skill. A new one is shared; editing
// one keeps whose it is and its lane unless the body sets them — owner ""
// publishes a personal skill to everyone.
func handleSaveSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name, Description, Content string
		Owner, Lane                *string
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		xbin.WriteError(w, 400, "need JSON body: {name, description?, content, owner?, lane?}")
		return
	}
	s := Skill{Name: strings.TrimSpace(body.Name), Description: body.Description, Content: body.Content}
	if s.Name == "" || s.Content == "" {
		xbin.WriteError(w, 400, "need {name, content}")
		return
	}
	if cur, err := agent.db.getSkill(s.Name); err == nil {
		s.Owner, s.Lane = cur.Owner, cur.Lane
	}
	if body.Owner != nil {
		s.Owner = *body.Owner
	}
	if body.Lane != nil {
		s.Lane = *body.Lane
	}
	if s.Lane != "" && s.Lane != "private" && s.Lane != "web" {
		xbin.WriteError(w, 400, "lane is private, web or empty")
		return
	}
	if err := agent.db.upsertSkill(&s); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, s)
}

func handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := agent.db.deleteSkill(r.PathValue("name")); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

const learnPrompt = "Review what you accomplished in this run. If it's a reusable procedure worth keeping, author a concise skill — a short name, a one-line description, and the steps/knowledge needed to repeat it — and save it with skill_manage(action:\"save\", name, description, content). If nothing here is worth saving as a skill, just say so."
