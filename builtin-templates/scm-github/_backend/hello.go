// hello.go — GET /scm/hello (docs/scm.md §hello): what this instance
// offers and what this caller may be; and the conditional answer every
// GET gives (an ETag, and 304 to a request that has it).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
)

func (s *srv) handleHello(w http.ResponseWriter, r *http.Request, c who) {
	if p := r.URL.Query().Get("protocol"); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n != protocolVersion {
			e := refuse(refProtocol, "this provider speaks protocol 1")
			e.Protocols = []int{protocolVersion}
			fail(w, e)
			return
		}
	}
	writeGET(w, r, s.hello(c))
}

func (s *srv) hello(c who) helloResp {
	host, _, _ := s.hosts()
	pub := s.public()
	h := helloResp{
		Protocol: protocolVersion, Protocols: []int{protocolVersion},
		SCM:    providerInfo{Name: "scm-github", Title: "GitHub", Version: version, Kind: "github"},
		Hosts:  []string{host},
		Caps:   []string{capCredentials, capRepos, capPulls, capIssues, capChecks},
		Events: eventsHealth(s),
		Limits: limitsInfo{ReposPerToken: reposPerToken, MinTTLSec: minTTLSec, MaxTTLSec: maxTTLSec, PageMax: pageMax, PollItems: pollItemsMax},
		Notes:  []string{},
	}
	if s.rerunOffered() {
		h.Caps = append(h.Caps, capRerun)
	}
	for _, f := range extraCaps {
		h.Caps = append(h.Caps, f(s)...)
	}
	h.Caps = append(h.Caps, capPoll)
	if s.mode != modeLegacy {
		h.Caps = append(h.Caps, capPartitions)
	}
	if s.mode == modeUser {
		h.Identities = []string{asPerson}
		if s.policy().BotForPeople != "off" {
			h.Identities = append(h.Identities, asBot)
		}
	} else {
		h.Identities = []string{asBot}
	}
	ids, def := s.identities(c)
	h.You = youInfo{Identities: ids, Default: def}
	if s.mode == modeUser {
		if rec := s.personRecord(); rec != nil && s.signedIn() != nil {
			h.You.Person = &account{Login: rec.Login, ID: rec.ID}
			h.You.SigninExpiresAt = rec.RefreshExpiresAt
		} else {
			h.Notes = append(h.Notes, "You haven't signed in to GitHub here yet: POST /scm/signin starts it.")
		}
	} else if s.mode == modeLegacy {
		h.Notes = append(h.Notes, "This copy isn't partitioned, so it keeps no one's GitHub sign-in: it hands out the App's bot only.")
	}
	if s.mode == modeUser {
		h.App = appInfo{Slug: pub.Slug, InstallURL: pub.InstallURL, Configured: pub.Configured}
	} else if a, _ := s.app(); a != nil {
		h.App = appInfo{Slug: a.Slug, InstallURL: s.installURL(a), Configured: true}
	}
	if !h.App.Configured {
		h.Notes = append(h.Notes, "No GitHub App is set up yet: a manager creates or pastes one on this tile's page.")
	} else if pub.DeviceFlow == "off" && s.mode != modeLegacy {
		h.Notes = append(h.Notes, "The GitHub App's Device Flow is off, so nobody can sign in: a manager enables it in the App's settings.")
	}
	return h
}

// rerunOffered: checks.rerun is offered while the App has actions: write
// (preset ci) and the policy's allowRerun is on — and only where a person
// can ask (a rerun is never the bot's).
func (s *srv) rerunOffered() bool {
	if s.mode == modeUser {
		return s.public().Rerun
	}
	if s.mode == modeLegacy {
		return false
	}
	a, _ := s.app()
	return a != nil && a.Permissions["actions"] == "write" && s.policy().AllowRerun
}

// etagOf is a weak validator of an answer's JSON.
func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `W/"` + hex.EncodeToString(sum[:12]) + `"`
}

// ifNoneMatch is the request's validator: the query's ifNoneMatch or the
// If-None-Match header.
func ifNoneMatch(r *http.Request) string {
	if v := r.URL.Query().Get("ifNoneMatch"); v != "" {
		return v
	}
	return r.Header.Get("If-None-Match")
}

// withETag answers v's JSON with its etag inside (an object gains an
// "etag" field) and the validator itself.
func withETag(v any) ([]byte, string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, "", err
	}
	tag := etagOf(b)
	if len(b) > 1 && b[0] == '{' {
		t, _ := json.Marshal(tag)
		sep := []byte(",")
		if bytes.Equal(b, []byte("{}")) {
			sep = nil
		}
		b = append(append(append([]byte(`{"etag":`), t...), sep...), b[1:]...)
	}
	return b, tag, nil
}

// writeGET answers a GET: 304 when the request already has this answer.
func writeGET(w http.ResponseWriter, r *http.Request, v any) {
	b, tag, err := withETag(v)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("ETag", tag)
	if inm := ifNoneMatch(r); inm != "" && inm == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}
