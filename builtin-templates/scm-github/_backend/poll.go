// poll.go — POST /scm/poll (docs/scm.md §Poll): many conditional reads in
// one call, each item the GET of its route; at most 4 upstream calls at
// once across the whole poll.
package main

import (
	"context"
	"net/http"
	"strconv"
	"sync"
)

type upstreamSemKey struct{}

// withUpstreamLimit bounds the upstream calls made under ctx (gh_client's
// do takes a slot around each).
func withUpstreamLimit(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, upstreamSemKey{}, make(chan struct{}, n))
}

func upstreamSlot(ctx context.Context) func() {
	sem, _ := ctx.Value(upstreamSemKey{}).(chan struct{})
	if sem == nil {
		return func() {}
	}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return func() {}
	}
	return func() { <-sem }
}

func (s *srv) handlePoll(w http.ResponseWriter, r *http.Request, c who) {
	var req pollReq
	if err := readBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if len(req.Items) > pollItemsMax {
		fail(w, refuse(refInvalid, "at most %d items per poll", pollItemsMax))
		return
	}
	if _, err := s.resolveAs(c, req.As); err != nil {
		fail(w, err)
		return
	}
	ctx := withUpstreamLimit(r.Context(), 4)
	out := pollResp{Items: make([]pollResult, len(req.Items))}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, it := range req.Items {
		wg.Add(1)
		go func(i int, it pollItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out.Items[i] = s.pollOne(ctx, c, req.As, it)
		}(i, it)
	}
	wg.Wait()
	for _, it := range out.Items {
		if it.Error != nil && it.Error.RetryAfterMs > out.RetryAfterMs {
			out.RetryAfterMs = it.Error.RetryAfterMs
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *srv) pollOne(ctx context.Context, c who, as string, it pollItem) pollResult {
	res := pollResult{ID: it.ID}
	var v any
	var err error
	if (it.Kind == "pull" || it.Kind == "issue" || it.Kind == "comments") && it.Number <= 0 {
		res.Error = refuse(refInvalid, "number is required for %s", it.Kind)
		return res
	}
	switch it.Kind {
	case "pull":
		v, err = s.getPull(ctx, c, it.Repo, it.Number, as)
	case "checks":
		ref := it.Ref
		if ref == "" && it.Number > 0 {
			ref = "pull/" + strconv.Itoa(it.Number)
		}
		v, err = s.getChecks(ctx, c, it.Repo, ref, as)
	case "issue":
		v, err = s.getIssue(ctx, c, it.Repo, it.Number, false, as)
	case "comments":
		var all []comment
		if all, err = s.timeline(ctx, c, it.Repo, it.Number, it.Since, as); err == nil {
			v = pageOf(all, pageMax, 0)
		}
	default:
		err = refuse(refInvalid, "kind is pull, checks, issue or comments")
	}
	if err != nil {
		res.Error = asRefusal(err)
		return res
	}
	b, tag, err := withETag(v)
	if err != nil {
		res.Error = asRefusal(err)
		return res
	}
	res.ETag = tag
	if it.ETag != tag {
		res.Changed, res.Value = true, b
	}
	return res
}
