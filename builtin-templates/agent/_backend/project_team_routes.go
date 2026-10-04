// project_team_routes.go — team projects' routes at the global instance
// (API.md §Team projects): the board (read, a member's row PUT from their
// own partition, the owner hiding a row) and the definition's seed sandbox.
// The membership routes (a person's partition) are project_team_member.go.
//
// A board row is the member's own word about their task and is shown to
// every viewer of the definition, so global takes it only from the member's
// own partition (personFromPartition, with its partition id), names the
// member itself, clips its text, keeps a URL only when it is https on the
// definition's host, and offers `run` — a run id in the member's partition —
// to that member alone.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { routeTables = append(routeTables, teamRoutes) }

func teamRoutes() []routeDef {
	return []routeDef{
		{"GET /projects/{pid}/board", needAny, projectNeed(lvViewer, handleTeamBoard)},
		{"PUT /projects/{pid}/board/{n}", needAny, projectNeed(lvParticipant, handleTeamBoardPut)},
		{"POST /projects/{pid}/board/{member}/{n}/hide", needAny, projectNeed(lvOwner, handleTeamBoardHide)},
		{"POST /projects/{pid}/seed", needAny, projectNeed(lvOwner, handleTeamSeed)},
		{"GET /memberships", needUser, handleListMemberships},
		{"POST /memberships", needUser, handleNewMembership},
		{"GET /memberships/{pid}/pending", needUser, handleMembershipPending},
		{"POST /memberships/{pid}/accept", needUser, handleMembershipAccept},
	}
}

// teamOnly answers 409 for a project that isn't a team definition.
func teamOnly(w http.ResponseWriter, p *Project) bool {
	if p.Kind == projTeam {
		return true
	}
	xbin.WriteError(w, http.StatusConflict, "only a team project's definition has a board and a seed")
	return false
}

// --- reading the board ---------------------------------------------------------------------

const boardCols = `member, n, title, col, state, waiting, branch, prs, ci, run, updated_ms, stale, hidden`

func scanBoardRow(scan func(dest ...any) error) (BoardRow, error) {
	var r BoardRow
	var prs, ci string
	var stale, hidden int
	if err := scan(&r.Member, &r.N, &r.Title, &r.Column, &r.State, &r.Waiting, &r.Branch, &prs, &ci, &r.Run,
		&r.UpdatedMs, &stale, &hidden); err != nil {
		return r, err
	}
	r.Stale, r.Hidden = stale != 0, hidden != 0
	if json.Unmarshal([]byte(prs), &r.PRs) != nil || r.PRs == nil {
		r.PRs = []TaskPR{}
	}
	if ci != "" && ci != "null" {
		var s CISummary
		if json.Unmarshal([]byte(ci), &s) == nil {
			r.CI = &s
		}
	}
	return r, nil
}

// teamMarkStale marks the rows of members who are no longer participants
// of p stale (and a member taken back in not), as the board is read.
func teamMarkStale(d *DB, p *Project) {
	members := []string{}
	rows, err := d.q.Query(`SELECT DISTINCT member FROM project_board WHERE project_id=?`, p.ID)
	if err != nil {
		return
	}
	for rows.Next() {
		var m string
		if rows.Scan(&m) == nil {
			members = append(members, m)
		}
	}
	rows.Close()
	for _, m := range members {
		stale := b2i(projectLevelOf(p, m) < lvParticipant)
		if res, err := d.q.Exec(`UPDATE project_board SET stale=? WHERE project_id=? AND member=? AND stale<>?`, stale, p.ID, m, stale); err == nil && rowsAffected(res) > 0 {
			emitBoard(p.ID)
		}
	}
}

// emitBoard sends the definition's `project` stream event, change board.
func emitBoard(pid int64) { projStream.post(pid, "board", 0) }

// handleTeamBoard: GET /projects/{pid}/board — the visible rows, newest
// first; `run` only on the caller's own.
func handleTeamBoard(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	if !teamOnly(w, p) {
		return
	}
	c := callerOf(r)
	d := projAg().db
	teamMarkStale(d, p)
	off, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	const page = 100
	rows, err := d.q.Query(`SELECT `+boardCols+` FROM project_board WHERE project_id=? AND hidden=0
		ORDER BY updated_ms DESC, member, n LIMIT ? OFFSET ?`, p.ID, page+1, max(off, 0))
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []BoardRow{}
	for rows.Next() {
		row, err := scanBoardRow(rows.Scan)
		if err != nil {
			continue
		}
		if c.kind != whoUser || c.viewedBy != "" || c.user != row.Member {
			row.Run = 0 // the member's own run: only they open it
		}
		items = append(items, row)
	}
	next := ""
	if len(items) > page {
		items, next = items[:page], strconv.Itoa(max(off, 0)+page)
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items, "next": next})
}

// --- a member's row -----------------------------------------------------------------------------

// boardText is what a board row's free text may be: one line, at most 200
// characters, control characters dropped. Shown as plain text.
func boardText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, invisibles.ReplaceAllString(s, ""))
	return clipRunes(strings.TrimSpace(projRedact(s)), 200)
}

// boardURL is u when it is https on host, else "" (dropped).
func boardURL(u, host string) string {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil || pu.Scheme != "https" || host == "" || !strings.EqualFold(pu.Hostname(), host) || pu.User != nil || pu.Port() != "" {
		return ""
	}
	return pu.String()
}

// boardRef says whether s can be a membership's uid as a board row names
// it: 1 to 64 letters, digits, '-' or '_'.
func boardRef(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// boardWord is one of the words a field may hold, else "".
func boardWord(s string, ok ...string) string {
	if hasStr(ok, s) {
		return s
	}
	return ""
}

// teamSanitize is the row global keeps from a member's body (API.md §Team
// projects): the text clipped, the URLs held to https on the definition's host,
// the words to their sets; an error is the 400 to answer.
func teamSanitize(in BoardRow, host string) (BoardRow, error) {
	deleted := in.State == taskDeleted
	if !deleted && in.Run < partitionIDBase {
		return BoardRow{}, fmt.Errorf("run: a conversation of your own space (an id from 2^40)")
	}
	out := BoardRow{Title: boardText(in.Title), Column: boardWord(in.Column, colQueued, colWorking, colNeedsYou, colPR, colDone),
		State: boardWord(in.State, taskQueued, taskPreparing, taskSignin, taskWorking, taskNeedsYou, taskCI, taskCIFailed,
			taskAwaitingReview, taskMerged, taskClosed, taskDone, taskFailed, taskCancelled, taskBlocked, taskDeleted),
		Waiting: boardText(in.Waiting), Branch: boardText(in.Branch), Run: in.Run, PRs: []TaskPR{},
		UpdatedMs: min(max(in.UpdatedMs, 0), nowMs())}
	if deleted {
		out.Run = 0
		return out, nil
	}
	for i, pr := range in.PRs {
		if i == 20 {
			break
		}
		out.PRs = append(out.PRs, TaskPR{Repo: boardText(pr.Repo), Number: max(pr.Number, 0), URL: boardURL(pr.URL, host),
			State: boardWord(pr.State, "open", "closed", "merged"), Draft: pr.Draft, HeadSHA: clipRunes(boardText(pr.HeadSHA), 64),
			Checks: boardWord(pr.Checks, "none", "pending", "success", "failure"), ChecksAt: max(pr.ChecksAt, 0)})
	}
	if ci := in.CI; ci != nil {
		out.CI = &CISummary{State: boardWord(ci.State, "none", "pending", "success", "failure"), Current: boardText(ci.Current),
			StartedAt: max(ci.StartedAt, 0), UpdatedAt: max(ci.UpdatedAt, 0), URL: boardURL(ci.URL, host),
			Jobs: CIJobs{Total: max(ci.Jobs.Total, 0), Done: max(ci.Jobs.Done, 0), Failed: max(ci.Jobs.Failed, 0),
				Running: max(ci.Jobs.Running, 0), Queued: max(ci.Jobs.Queued, 0)}}
	}
	return out, nil
}

// handleTeamBoardPut: PUT /projects/{pid}/board/{n} — a member's row for
// their task n, from their own partition only. The member is the caller;
// a PUT from another partition id of the same person deletes their rows
// first (a person re-created starts fresh). The row belongs to the
// membership the body names (its uid): a row from another membership —
// the member's new one, the old deleted — replaces it, shown again; state
// "deleted" hides the row only when it is that membership's (a late one
// from a deleted membership hides nothing of the new one's).
func handleTeamBoardPut(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	part := xbin.Caller(r).PartitionID
	if !personFromPartition(r) || part == "" || c.kind != whoUser || c.viewedBy != "" || c.user == "" {
		xbin.WriteError(w, http.StatusForbidden, "a board row comes only from its member's own space")
		return
	}
	if p.Kind != projTeam {
		xbin.WriteError(w, http.StatusNotFound, "no such team project")
		return
	}
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n < 1 {
		xbin.WriteError(w, 400, "n: a task's number")
		return
	}
	var in teamOutRow
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		xbin.WriteError(w, 400, "bad JSON: "+err.Error())
		return
	}
	ref := in.Membership
	if !boardRef(ref) {
		xbin.WriteError(w, 400, "membership: the uid of the membership whose task this is")
		return
	}
	row, err := teamSanitize(in.BoardRow, p.Host)
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	row.N, row.Member, row.MemberPid = n, c.user, part
	prs, _ := json.Marshal(row.PRs)
	ci := ""
	if row.CI != nil {
		b, _ := json.Marshal(row.CI)
		ci = string(b)
	}
	err = projAg().db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`DELETE FROM project_board WHERE member=? AND member_pid<>?`, c.user, part); err != nil {
			return err
		}
		if row.State == taskDeleted {
			_, err := t.q.Exec(`UPDATE project_board SET hidden=1, state=?, run=0, updated_ms=? WHERE project_id=? AND member=? AND n=?
				AND member_ref=?`, taskDeleted, nowMs(), p.ID, c.user, n, ref)
			return err
		}
		// a row of another membership is replaced and shown again; the
		// owner's hide holds for the membership's own later rows
		_, err := t.q.Exec(`INSERT INTO project_board (project_id, member, member_pid, member_ref, n, title, col, state, waiting, branch,
			prs, ci, run, updated_ms, stale, hidden) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
			ON CONFLICT(project_id, member, n) DO UPDATE SET member_pid=excluded.member_pid, title=excluded.title, col=excluded.col,
			state=excluded.state, waiting=excluded.waiting, branch=excluded.branch, prs=excluded.prs, ci=excluded.ci,
			run=excluded.run, updated_ms=excluded.updated_ms, stale=0,
			hidden=CASE WHEN project_board.member_ref=excluded.member_ref THEN project_board.hidden ELSE 0 END,
			member_ref=excluded.member_ref`,
			p.ID, c.user, part, ref, n, row.Title, row.Column, row.State, row.Waiting, row.Branch, string(prs), ci, row.Run, row.UpdatedMs)
		return err
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	emitBoard(p.ID)
	xbin.WriteJSON(w, 200, row)
}

// handleTeamBoardHide: POST /projects/{pid}/board/{member}/{n}/hide — the
// owner hides a row (a member who left, say).
func handleTeamBoardHide(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	if !teamOnly(w, p) {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	res, err := projAg().db.q.Exec(`UPDATE project_board SET hidden=1 WHERE project_id=? AND member=? AND n=?`, p.ID, r.PathValue("member"), n)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if rowsAffected(res) == 0 {
		xbin.WriteError(w, 404, "no such board row")
		return
	}
	emitBoard(p.ID)
	w.WriteHeader(http.StatusNoContent)
}

// --- the seed -----------------------------------------------------------------------------------

// handleTeamSeed: POST /projects/{pid}/seed {sandbox: {ref} | {new}} — the
// definition's seed sandbox, prepared by its worker (sandbox, repo, fetch;
// never a credential: a seed's repo jobs clone only what needs none). A
// definition has one seed: 409 once it has.
func handleTeamSeed(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	if !teamOnly(w, p) {
		return
	}
	c := callerOf(r)
	var body struct {
		Sandbox *struct {
			Ref string      `json:"ref"`
			New *sandboxNew `json:"new"`
		} `json:"sandbox"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if p.SandboxRef != "" || projAg().db.getSetting(sbxNewKey(p.ID)) != "" {
		xbin.WriteError(w, http.StatusConflict, "this team project has a seed sandbox already")
		return
	}
	ref := ""
	var newReq *sandboxNew
	switch {
	case body.Sandbox != nil && body.Sandbox.Ref != "":
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := checkProjectSandbox(ctx, c, body.Sandbox.Ref)
		cancel()
		if err != nil {
			writeProjErr(w, err)
			return
		}
		ref = body.Sandbox.Ref
	case body.Sandbox != nil && body.Sandbox.New != nil:
		if _, ok := boundManager(body.Sandbox.New.Provider); !ok {
			xbin.WriteError(w, 400, fmt.Sprintf("sandbox.new.provider: %q isn't a sandbox manager bound to this agent", body.Sandbox.New.Provider))
			return
		}
		newReq = body.Sandbox.New
	default:
		xbin.WriteError(w, 400, "need {sandbox: {ref} or {new: {provider}}}: the seed")
		return
	}
	jobs := []*ProjectJob{}
	err := projAg().db.Tx(func(t *DB) error {
		if ref != "" {
			res, err := t.q.Exec(`UPDATE projects SET sandbox_ref=?, version=version+1, updated_ms=? WHERE id=? AND sandbox_ref=''`, ref, nowMs(), p.ID)
			if err != nil {
				return err
			}
			if rowsAffected(res) != 1 {
				return perr(409, "this team project has a seed sandbox already")
			}
			if err := t.shareClash(p.ID, ref); err != nil {
				return err
			}
		} else {
			b, _ := json.Marshal(newReq)
			if err := t.putSetting(sbxNewKey(p.ID), string(b)); err != nil {
				return err
			}
		}
		j, err := t.queueJob(p.ID, 0, "", pjSandbox, c.tag(), 0)
		if err != nil {
			return err
		}
		jobs = append(jobs, j)
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	xbin.WriteJSON(w, http.StatusAccepted, map[string]any{"jobs": jobs})
}
