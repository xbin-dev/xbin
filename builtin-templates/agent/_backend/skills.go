// skills.go — a self-improving skill library (Hermes-inspired, plans/agent-v2.md).
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
}

func (d *DB) upsertSkill(s *Skill) error {
	t := now()
	_, err := d.sql.Exec(
		`INSERT INTO skills (name, description, content, created, updated) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET description=excluded.description, content=excluded.content, updated=excluded.updated`,
		s.Name, s.Description, s.Content, t, t)
	return err
}

func (d *DB) getSkill(name string) (*Skill, error) {
	s := &Skill{}
	err := d.sql.QueryRow(`SELECT name, description, content, created, updated FROM skills WHERE name=?`, name).
		Scan(&s.Name, &s.Description, &s.Content, &s.Created, &s.Updated)
	return s, err
}

func (d *DB) listSkills() ([]*Skill, error) {
	rows, err := d.sql.Query(`SELECT name, description, content, created, updated FROM skills ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Skill
	for rows.Next() {
		s := &Skill{}
		if err := rows.Scan(&s.Name, &s.Description, &s.Content, &s.Created, &s.Updated); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) deleteSkill(name string) error {
	_, err := d.sql.Exec(`DELETE FROM skills WHERE name=?`, name)
	return err
}

// --- tool implementation -------------------------------------------------

// runSkillTool handles the skill_* tools. cfg.feature("skills") gates whether
// they're advertised; this still executes if called.
func (ag *Agent) runSkillTool(name string, args map[string]any) (string, error) {
	switch name {
	case "skills_list":
		skills, err := ag.db.listSkills()
		if err != nil {
			return "", err
		}
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
		if err != nil {
			return "(no such skill)", nil
		}
		return s.Content, nil

	case "skill_manage":
		action := strings.TrimSpace(fmtStr(args["action"]))
		sname := strings.TrimSpace(fmtStr(args["name"]))
		if sname == "" {
			return "", fmt.Errorf("skill_manage needs a name")
		}
		switch action {
		case "remove", "delete":
			if err := ag.db.deleteSkill(sname); err != nil {
				return "", err
			}
			return "removed skill " + sname, nil
		default: // save / add / replace
			s := &Skill{Name: sname, Description: strings.TrimSpace(fmtStr(args["description"])), Content: fmtStr(args["content"])}
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

func handleListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := agent.db.listSkills()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if skills == nil {
		skills = []*Skill{}
	}
	writeJSON(w, 200, skills)
}

func handleSaveSkill(w http.ResponseWriter, r *http.Request) {
	var s Skill
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeErr(w, 400, "need JSON body: {name, description?, content}")
		return
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || s.Content == "" {
		writeErr(w, 400, "need {name, content}")
		return
	}
	if err := agent.db.upsertSkill(&s); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s)
}

func handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := agent.db.deleteSkill(r.PathValue("name")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleLearn injects the "distill this run into a skill" prompt and resumes the
// run — the /learn flow (the agent authors a SKILL from its own conversation).
func handleLearn(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := agent.db.getRun(id); err != nil {
		writeErr(w, 404, "no such run")
		return
	}
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "user", Content: learnPrompt})
	_ = agent.db.setStatus(id, statusIdle, 0, "", "")
	agent.driveAsync(id)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

const learnPrompt = "Review what you accomplished in this run. If it's a reusable procedure worth keeping, author a concise skill — a short name, a one-line description, and the steps/knowledge needed to repeat it — and save it with skill_manage(action:\"save\", name, description, content). If nothing here is worth saving as a skill, just say so."
