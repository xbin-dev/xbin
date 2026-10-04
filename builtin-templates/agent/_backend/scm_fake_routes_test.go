package main

// scm_fake_routes_test.go — the fake scm provider's reads (scm_fake_test.go
// has the fake, its levers and credentials): pulls, checks with job logs,
// annotations and rerun, issues, poll and subscriptions. Called with f.mu
// held.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (f *fakeSCM) notFound(w http.ResponseWriter, what string) {
	f.refuse(w, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such " + what})
}

func (f *fakeSCM) pull(repo string, n int) *scmPull {
	for _, p := range f.pulls[repo] {
		if p.Number == n {
			return p
		}
	}
	return nil
}

func (f *fakeSCM) pullRoute(w http.ResponseWriter, r *http.Request, parts []string, q url.Values, in map[string]json.RawMessage) {
	as := q.Get("as")
	if r.Method != "GET" {
		_ = json.Unmarshal(in["as"], &as)
	}
	if _, ok := f.who(w, as); !ok {
		return
	}
	switch {
	case r.Method == "POST" && len(parts) == 1:
		var req scmPullReq
		b, _ := json.Marshal(in)
		_ = json.Unmarshal(b, &req)
		for _, p := range f.pulls[req.Repo] {
			if p.Head.Ref == req.Head && p.State == "open" {
				cp := *p
				cp.Existing = true
				fakeJSON(w, 200, cp)
				return
			}
		}
		p := &scmPull{Number: len(f.pulls[req.Repo]) + 1, Title: req.Title, Body: req.Body, State: "open", Draft: req.Draft,
			MergeableState: "unknown", Head: scmRef{Ref: req.Head, SHA: "9fceb02", Repo: req.Repo}, Base: scmRef{Ref: orStr(req.Base, "main")}}
		p.URL = "https://github.com/" + req.Repo + "/pull/" + strconv.Itoa(p.Number)
		f.pulls[req.Repo] = append(f.pulls[req.Repo], p)
		fakeJSON(w, 201, p)
	case r.Method == "GET" && len(parts) == 1:
		out := scmPage[scmPull]{Items: []scmPull{}}
		for _, p := range f.pulls[q.Get("repo")] {
			if st := q.Get("state"); st != "" && st != "all" && p.State != st {
				continue
			}
			if h := q.Get("head"); h != "" && p.Head.Ref != h {
				continue
			}
			out.Items = append(out.Items, *p)
		}
		fakeJSON(w, 200, out)
	case len(parts) >= 2:
		n, _ := strconv.Atoi(parts[1])
		repo := q.Get("repo")
		if r.Method == "PATCH" || r.Method == "POST" {
			_ = json.Unmarshal(in["repo"], &repo)
		}
		p := f.pull(repo, n)
		if p == nil {
			f.notFound(w, "pull request")
			return
		}
		switch {
		case len(parts) == 2 && r.Method == "GET":
			f.conditional(w, r, p)
		case len(parts) == 2 && r.Method == "PATCH":
			var pp scmPullPatch
			b, _ := json.Marshal(in)
			_ = json.Unmarshal(b, &pp)
			if pp.Title != nil {
				p.Title = *pp.Title
			}
			if pp.State != nil {
				p.State = *pp.State
			}
			if pp.Draft != nil {
				p.Draft = *pp.Draft
			}
			fakeJSON(w, 200, p)
		case len(parts) == 3 && parts[2] == "comments" && r.Method == "GET":
			out := scmPage[scmComment]{Items: append([]scmComment{}, f.comments[repo+"#"+parts[1]]...)}
			f.conditional(w, r, out)
		case len(parts) == 3 && parts[2] == "comments" && r.Method == "POST":
			var body string
			_ = json.Unmarshal(in["body"], &body)
			c := scmComment{ID: strconv.Itoa(len(f.comments[repo+"#"+parts[1]]) + 1), Kind: "comment", Body: body, CreatedAt: nowMs()}
			f.comments[repo+"#"+parts[1]] = append(f.comments[repo+"#"+parts[1]], c)
			fakeJSON(w, 201, c)
		default:
			f.notFound(w, "route")
		}
	default:
		f.notFound(w, "route")
	}
}

func (f *fakeSCM) checkRoute(w http.ResponseWriter, r *http.Request, parts []string, q url.Values, in map[string]json.RawMessage) {
	switch {
	case r.Method == "GET" && len(parts) == 1:
		if _, ok := f.who(w, q.Get("as")); !ok {
			return
		}
		c := f.checks[q.Get("repo")+"@"+q.Get("ref")]
		if c == nil {
			c = &scmChecks{Ref: q.Get("ref"), State: "none", Checks: []scmCheck{}, Statuses: []scmStatus{}}
		}
		f.conditional(w, r, c)
	case r.Method == "GET" && len(parts) == 4 && parts[1] == "jobs" && parts[3] == "log":
		if _, ok := f.who(w, q.Get("as")); !ok {
			return
		}
		l := f.logs[parts[2]]
		url := "https://github.com/" + q.Get("repo") + "/actions/runs/1/job/" + parts[2]
		switch {
		case l == nil:
			f.notFound(w, "job")
		case l.running:
			f.refuse(w, &scmError{Status: 409, Refusal: scmRefInProgress, Message: "the job is still running", URL: url})
		default:
			fakeJSON(w, 200, fakeLogSlice(parts[2], l.text, q, url))
		}
	case r.Method == "GET" && len(parts) == 4 && parts[1] == "runs" && parts[3] == "annotations":
		if _, ok := f.who(w, q.Get("as")); !ok {
			return
		}
		fakeJSON(w, 200, scmPage[scmAnnotation]{Items: append([]scmAnnotation{}, f.notes[parts[2]]...)})
	case r.Method == "POST" && len(parts) == 2 && parts[1] == "rerun":
		if !f.needCap(w, scmCapRerun) {
			return
		}
		var req scmRerunReq
		b, _ := json.Marshal(in)
		_ = json.Unmarshal(b, &req)
		as, ok := f.who(w, req.As)
		if !ok {
			return
		}
		if as != scmAsPerson {
			f.refuse(w, &scmError{Status: 403, Refusal: scmRefIdentity, Message: "a re-run is a person's", Identities: []string{scmAsPerson}})
			return
		}
		fakeJSON(w, 202, scmRerun{RunID: req.RunID, Attempt: 2})
	default:
		f.notFound(w, "route")
	}
}

// fakeLogSlice is GET …/log's answer: the bytes from max(since,
// end-tailBytes) to end (until), cut forward to a line start.
func fakeLogSlice(id, text string, q url.Values, url string) scmJobLog {
	end := int64(len(text))
	if u, _ := strconv.ParseInt(q.Get("until"), 10, 64); u > 0 && u < end {
		end = u
	}
	tail, _ := strconv.ParseInt(q.Get("tailBytes"), 10, 64)
	if tail <= 0 {
		tail = 65536
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	from := max(since, end-tail, 0)
	if from > 0 && from < end && text[from-1] != '\n' {
		if i := strings.IndexByte(text[from:end], '\n'); i >= 0 {
			from += int64(i) + 1
		} else {
			from = end
		}
	}
	return scmJobLog{ID: id, Text: text[from:end], Bytes: int64(len(text)), From: from, Complete: true, Truncated: from > 0, URL: url}
}

func (f *fakeSCM) issueRoute(w http.ResponseWriter, r *http.Request, parts []string, q url.Values) {
	if _, ok := f.who(w, q.Get("as")); !ok {
		return
	}
	repo := q.Get("repo")
	switch {
	case r.Method == "GET" && len(parts) == 1:
		out := scmPage[scmIssue]{Items: []scmIssue{}}
		for _, i := range f.issues[repo] {
			if st := q.Get("state"); st == "" || st == "all" || i.State == st {
				cp := *i
				cp.Comments = nil
				out.Items = append(out.Items, cp)
			}
		}
		fakeJSON(w, 200, out)
	case r.Method == "GET" && len(parts) == 2:
		n, _ := strconv.Atoi(parts[1])
		for _, i := range f.issues[repo] {
			if i.Number == n {
				cp := *i
				if q.Get("comments") != "1" {
					cp.Comments = nil
				}
				f.conditional(w, r, cp)
				return
			}
		}
		f.notFound(w, "issue")
	default:
		f.notFound(w, "route")
	}
}

// poll answers each item as its route's conditional GET would.
func (f *fakeSCM) poll(w http.ResponseWriter, body []byte) {
	var req scmPollReq
	_ = json.Unmarshal(body, &req)
	if _, ok := f.who(w, req.As); !ok {
		return
	}
	out := scmPollResp{Items: []scmPollResult{}}
	for _, it := range req.Items {
		var v any
		switch it.Kind {
		case "pull":
			if p := f.pull(it.Repo, it.Number); p != nil {
				v = p
			}
		case "checks":
			v = f.checks[it.Repo+"@"+it.Ref]
		case "issue":
			for _, i := range f.issues[it.Repo] {
				if i.Number == it.Number {
					v = i
				}
			}
		case "comments":
			v = scmPage[scmComment]{Items: append([]scmComment{}, f.comments[it.Repo+"#"+strconv.Itoa(it.Number)]...)}
		}
		if v == nil || v == (*scmChecks)(nil) {
			out.Items = append(out.Items, scmPollResult{ID: it.ID, Error: &scmError{Refusal: scmRefNotFound, Message: "not found"}})
			continue
		}
		et := fakeETag(v)
		res := scmPollResult{ID: it.ID, ETag: et, Changed: et != it.ETag}
		if res.Changed {
			res.Value, _ = json.Marshal(v)
		}
		out.Items = append(out.Items, res)
	}
	fakeJSON(w, 200, out)
}

func (f *fakeSCM) subRoute(w http.ResponseWriter, method string, parts []string, body []byte) {
	switch {
	case method == "POST" && len(parts) == 1:
		var s scmSubscription
		_ = json.Unmarshal(body, &s)
		status := 201
		if old := f.subs[s.Key]; s.Key != "" && old != nil {
			s.ID, status = old.ID, 200
		} else {
			f.nextSub++
			s.ID = "s_" + strconv.Itoa(f.nextSub)
		}
		s.For, s.Expires = "global", nowMs()+30*24*3600*1000
		key := orStr(s.Key, s.ID)
		f.subs[key] = &s
		fakeJSON(w, status, s)
	case method == "GET" && len(parts) == 1:
		var items []scmSubscription
		for _, s := range f.subs {
			items = append(items, *s)
		}
		fakeJSON(w, 200, map[string]any{"items": items})
	case method == "DELETE" && len(parts) == 2:
		for k, s := range f.subs {
			if s.ID == parts[1] {
				delete(f.subs, k)
				w.WriteHeader(204)
				return
			}
		}
		f.notFound(w, "subscription")
	default:
		f.notFound(w, "route")
	}
}
