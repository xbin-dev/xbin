// live_person_test.go — live checks of a person's sign-in (person.json,
// the App's device flow): registration at global, scoped person tokens and
// S2 — whether a scoped token survives its parent's refresh, and whether
// /token/scoped takes basic auth with the client id and secret — reads,
// a comment and a rerun as the person, revoking one token; and, LAST and
// only with XBIN_GH_LIVE_FORGET=1, Forget (the grant revoked: the owner
// runs the device flow again before any further person check).
package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

// personTok asks the person's partition for a token as their consumer would.
func (l *live) personTok(purpose, access string) (tokenResp, secretString) {
	l.t.Helper()
	r := l.call(l.uH, personC("live"), "POST", "/scm/token", map[string]any{"repo": l.c.repo, "access": access, "purpose": purpose})
	if r.Code != 200 {
		l.t.Fatalf("POST /scm/token as the person: %d %s", r.Code, redact(r.Body.String()))
	}
	var raw struct {
		tokenResp
		Token string `json:"token"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &raw); err != nil {
		l.t.Fatal(err)
	}
	return raw.tokenResp, newSecret(raw.Token)
}

// checkTok is GitHub's POST /applications/{cid}/token for a token: its
// status and the expiry and login it names.
func (l *live) checkTok(tok secretString) (int, string, string) {
	r := l.raw("POST", "/applications/"+l.c.clientID+"/token", secretString{}, true, map[string]string{"access_token": tok.Reveal()}, nil)
	var m struct {
		ExpiresAt string `json:"expires_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	_ = r.json(&m)
	return r.Status, m.ExpiresAt, m.User.Login
}

// TestLivePersonS2: registration, a scoped token through the relay (S2's
// basic auth), what it may do, then the parent refreshed and the scoped
// token tried again (S2's survival); reads, a comment, a rerun and one
// token revoked, as the person.
func TestLivePersonS2(t *testing.T) {
	l := newLive(t)
	u, uH := l.personSrv()
	n := &liveNote{t: t}
	ctx := context.Background()
	rec := u.personRecord()
	n.add("person.json signs in %s (id %d); parent expires in %s, refresh token in %s", rec.Login, rec.ID,
		time.Until(time.UnixMilli(rec.ExpiresAt)).Round(time.Minute), time.Until(time.UnixMilli(rec.RefreshExpiresAt)).Round(time.Hour))

	var h helloResp
	decode(t, l.call(uH, personC("live"), "GET", "/scm/hello", nil), &h)
	n.add("hello in the partition: caps %v, you %+v", h.Caps, h.You)

	parent := l.parent()
	st, exp, login := l.checkTok(parent)
	n.add("POST /applications/{cid}/token (basic auth) for the parent: %d, expires_at %s, user %s", st, exp, login)

	tr, scoped := l.personTok("live:s2", "read")
	n.add("S2b: POST /scm/token as the person → scoped through global's POST /applications/{cid}/token/scoped with basic auth: ok; token %s (%d chars), repos %v, permissions %v, expiresAt in %s (parent less the epoch's hour)",
		redact(scoped.Reveal()), len(scoped.Reveal()), tr.Repos, tr.Permissions, time.Until(time.UnixMilli(tr.ExpiresAt)).Round(time.Minute))
	if g := l.global.ident("live"); g == nil || g.Login != rec.Login {
		t.Errorf("global's identity directory: %+v", g)
	} else {
		n.add("POST /partition/identity registered %s from GitHub's own answer", g.Login)
	}
	// GitHub's own answer for the scoped token, raw.
	sc := l.raw("POST", "/applications/"+l.c.clientID+"/token/scoped", secretString{}, true, map[string]any{
		"access_token": parent.Reveal(), "target": l.owner(), "repositories": []string{l.name()}, "permissions": map[string]string{"contents": "read", "metadata": "read"}}, nil)
	var scm map[string]any
	_ = sc.json(&scm)
	keys := []string{}
	for k := range scm {
		keys = append(keys, k)
	}
	n.add("raw /token/scoped: %d, keys %v, expires_at %v, permissions %v", sc.Status, keys, scm["expires_at"], scm["permissions"])
	if s, _ := scm["token"].(string); s != "" {
		l.raw("DELETE", "/applications/"+l.c.clientID+"/token", secretString{}, true, map[string]string{"access_token": s}, nil)
	}
	noBasic := l.raw("POST", "/applications/"+l.c.clientID+"/token/scoped", parent, false, map[string]any{"access_token": parent.Reveal(), "target": l.owner()}, nil)
	n.add("/token/scoped with the person's bearer token instead of basic auth: %d", noBasic.Status)
	st, exp, _ = l.checkTok(scoped)
	n.add("POST /applications/{cid}/token for the scoped token: %d, expires_at %s", st, exp)
	again := l.raw("POST", "/applications/"+l.c.clientID+"/token/scoped", secretString{}, true, map[string]any{"access_token": scoped.Reveal(), "target": l.owner(), "repositories": []string{l.name()}}, nil)
	n.add("/token/scoped from a scoped token: %d %s", again.Status, clip(string(again.Body), 160))

	// What the read-scoped token may do.
	n.add("scoped read token: GET repo %d; PUT contents %d (want 403)",
		l.raw("GET", "/repos/"+l.c.repo, scoped, false, nil, nil).Status,
		l.raw("PUT", "/repos/"+l.c.repo+"/contents/live-person-denied.txt", scoped, false, map[string]string{"message": "x", "content": "eA=="}, nil).Status)

	// S2: refresh the parent, then use the scoped token again.
	newParent, _, err := u.refresh(ctx, parent.Reveal())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	n.add("refreshed the parent (pair written back to person.json); new parent %s", redact(newParent.Reveal()))
	n.add("after the refresh: old parent GET /user %d; new parent GET /user %d",
		l.raw("GET", "/user", parent, false, nil, nil).Status, l.raw("GET", "/user", newParent, false, nil, nil).Status)
	sAfter := l.raw("GET", "/repos/"+l.c.repo, scoped, false, nil, nil)
	st, exp, _ = l.checkTok(scoped)
	n.add("S2a: the scoped token after its parent's refresh: GET repo %d; GitHub's check of it %d (expires_at %s)", sAfter.Status, st, exp)
	time.Sleep(10 * time.Second)
	n.add("S2a: …and 10 s later: GET repo %d", l.raw("GET", "/repos/"+l.c.repo, scoped, false, nil, nil).Status)

	// The template after the refresh: a new token, the old one not reused.
	tr2, scoped2 := l.personTok("live:s2", "read")
	n.add("POST /scm/token same purpose after the refresh: a new token %v, expiresAt in %s", scoped2.Reveal() != scoped.Reveal(), time.Until(time.UnixMilli(tr2.ExpiresAt)).Round(time.Minute))

	// Reads as the person.
	repo := url.QueryEscape(l.c.repo)
	for _, p := range []string{"/scm/repos", "/scm/repo?repo=" + repo, "/scm/pulls/1?repo=" + repo, "/scm/issues?repo=" + repo, "/scm/checks?repo=" + repo + "&ref=main"} {
		r := l.call(uH, personC("live"), "GET", p, nil)
		var m map[string]any
		_ = json.Unmarshal(r.Body.Bytes(), &m)
		n.add("GET %s as the person: %d permission=%v protected=%v items=%d state=%v", p, r.Code, m["permission"], m["protected"], lenOf(m["items"]), m["state"])
	}
	r := l.call(uH, personC("live"), "POST", "/scm/pulls/1/comments", map[string]string{"repo": l.c.repo, "body": "live check (as the person) " + time.Now().UTC().Format(time.RFC3339)})
	n.add("POST /scm/pulls/1/comments as the person: %d", r.Code)

	// A rerun as the person; the bot's is refused.
	var ck checksResp
	decode(t, l.call(uH, personC("live"), "GET", "/scm/checks?repo="+repo+"&ref=main", nil), &ck)
	runID := ""
	for _, wr := range ck.WorkflowRuns {
		if wr.Status == "completed" && wr.Conclusion == "failure" {
			runID = wr.ID
			break
		}
	}
	if runID != "" {
		r = l.call(uH, personC("live"), "POST", "/scm/checks/rerun", map[string]any{"repo": l.c.repo, "runId": runID, "failedOnly": true})
		n.add("POST /scm/checks/rerun failedOnly as the person (run %s): %d %s", runID, r.Code, r.Body.String())
		r = l.call(uH, personC("live"), "POST", "/scm/checks/rerun", map[string]any{"repo": l.c.repo, "runId": runID, "failedOnly": true})
		n.add("…the same again at once (the run is going again): %d %s", r.Code, clip(r.Body.String(), 200))
	} else {
		n.add("no completed failed run on main: rerun not tried")
	}
	r = l.call(l.gH, agentC, "POST", "/scm/checks/rerun", map[string]any{"repo": l.c.repo, "runId": "1", "failedOnly": true})
	n.add("POST /scm/checks/rerun as the bot at global: %d", r.Code)

	// One person token revoked (POST /scm/token/revoke → global's
	// DELETE /applications/{cid}/token), then again raw.
	_, gone := l.personTok("live:revoke-one", "read")
	r = l.call(uH, personC("live"), "POST", "/scm/token/revoke", map[string]string{"token": gone.Reveal()})
	n.add("POST /scm/token/revoke {token} as the person: %d", r.Code)
	n.add("…the revoked scoped token: GET repo %d; check %d", l.raw("GET", "/repos/"+l.c.repo, gone, false, nil, nil).Status, first(l.checkTok(gone)))
	d := l.raw("DELETE", "/applications/"+l.c.clientID+"/token", secretString{}, true, map[string]string{"access_token": gone.Reveal()}, nil)
	n.add("DELETE /applications/{cid}/token for an already revoked token: %d %s", d.Status, clip(string(d.Body), 160))
	n.add("…the parent after one scoped token's revocation: GET /user %d; the other scoped token: GET repo %d",
		l.raw("GET", "/user", l.parent(), false, nil, nil).Status, l.raw("GET", "/repos/"+l.c.repo, scoped2, false, nil, nil).Status)
}

func first(a int, _, _ string) int { return a }

// TestLiveForget is Forget (DELETE /scm/signin): global revokes the grant
// (DELETE /applications/{cid}/grant). It ends person.json's pair for good:
// run it last, and only on purpose (XBIN_GH_LIVE_FORGET=1).
func TestLiveForget(t *testing.T) {
	if os.Getenv("XBIN_GH_LIVE_FORGET") != "1" {
		t.Skip("Forget revokes the owner's sign-in: set XBIN_GH_LIVE_FORGET=1 to run it (last)")
	}
	l := newLive(t)
	u, uH := l.personSrv()
	n := &liveNote{t: t}
	_, scoped := l.personTok("live:forget", "read")
	parent := l.parent()
	var ref vaultTok
	_ = vaultJSON(u.vault, vaultUserRefresh, &ref)
	refresh := newSecret(ref.Token)

	r := l.call(uH, pageC("live"), "DELETE", "/scm/signin", nil)
	n.add("DELETE /scm/signin (Forget → DELETE /applications/{cid}/grant): %d", r.Code)
	if r.Code != 204 {
		t.Fatalf("Forget: %d %s", r.Code, r.Body)
	}
	if u.personRecord() != nil || l.global.ident("live") != nil {
		t.Error("Forget left the sign-in or the identity")
	}
	n.add("after Forget: parent GET /user %d; scoped GET repo %d; check of the parent %d",
		l.raw("GET", "/user", parent, false, nil, nil).Status, l.raw("GET", "/repos/"+l.c.repo, scoped, false, nil, nil).Status, first(l.checkTok(parent)))
	var out oauthTokenResp
	err := u.oauthPost(context.Background(), liveWeb+"/login/oauth/access_token", url.Values{"client_id": {l.c.clientID}, "grant_type": {"refresh_token"}, "refresh_token": {refresh.Reveal()}}, &out)
	n.add("the refresh token after Forget: error %q (err %v, a token answered: %v)", out.Error, err, !out.AccessToken.Empty())
	for _, what := range []string{"grant", "token"} {
		d := l.raw("DELETE", "/applications/"+l.c.clientID+"/"+what, secretString{}, true, map[string]string{"access_token": parent.Reveal()}, nil)
		n.add("DELETE /applications/{cid}/%s again with the revoked parent: %d %s", what, d.Status, clip(string(d.Body), 160))
	}
	r = l.call(l.gH, relayC("live", "write"), "POST", "/partition/revoke-grant", map[string]string{"accessToken": parent.Reveal()})
	n.add("POST /partition/revoke-grant with the already revoked parent: %d", r.Code)
	n.add("the owner must run the App's device flow again (a new person.json) before any further person check; the run at %s", strconv.FormatInt(time.Now().Unix(), 10))
}
