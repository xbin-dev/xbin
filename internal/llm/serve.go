package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// serve.go — one OpenAI-compatible call, start to finish: admit the caller,
// read the body (bounded), resolve the model, ask the policy and the quotas,
// find the key, relay, meter.

// MaxRequestBody bounds a request body (a long conversation with images is
// megabytes; nothing legitimate is more).
const MaxRequestBody = 32 << 20

// QuotaHeader marks an answer whose caller reached a quota that only warns.
const QuotaHeader = "X-XBin-LLM-Quota"

// usageSink picks the usage reader of an OpenAI-wire answer.
func usageSink(stream bool) sdkllm.UsageSink {
	if stream {
		return &sdkllm.StreamUsage{}
	}
	return &sdkllm.BodyUsage{}
}

// apiCall serves POST /llm/v1/chat/completions and /llm/v1/responses
// (path: the wire's path under an upstream's base URL).
func (s *Service) apiCall(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.current()
		c, ref := s.admit(r, snap)
		if ref != nil {
			ref.write(w)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
			refuse(http.StatusUnsupportedMediaType, "xbin_llm_body", "the request body is JSON (Content-Type: application/json)").write(w)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				refuse(http.StatusRequestEntityTooLarge, "xbin_llm_body", "the request body is over "+strconv.Itoa(MaxRequestBody>>20)+" MiB").write(w)
				return
			}
			refuse(http.StatusBadRequest, "xbin_llm_body", "the request body could not be read").write(w)
			return
		}
		req, err := sdkllm.OpenRequest(body)
		if err != nil {
			refuse(http.StatusBadRequest, "xbin_llm_body", "the request body is not a JSON object").write(w)
			return
		}
		u, model, err := snap.resolve(req.Model)
		if err != nil {
			var me *modelError
			if errors.As(err, &me) {
				refuse(me.status, me.code, me.msg).write(w)
			} else {
				refuse(http.StatusBadRequest, "xbin_llm_model", err.Error()).write(w)
			}
			return
		}
		if s.opt.Policy != nil {
			if err := s.opt.Policy(c, UpstreamInfo{Name: u.Name, Private: u.Private, Host: u.Host}, model); err != nil {
				refuse(http.StatusForbidden, "xbin_llm_policy", err.Error()).write(w)
				return
			}
		}
		answer := http.Header{}
		if ref := s.checkQuota(snap, c, answer); ref != nil {
			ref.write(w)
			return
		}
		t, err := s.target(u)
		if err != nil {
			s.keyRefusal(u, err).write(w)
			return
		}
		out, err := req.Body(model, path == sdkllm.PathChat) // a chat stream always ends with its usage chunk: metering needs it
		if err != nil {
			refuse(http.StatusBadRequest, "xbin_llm_body", "the request body could not be rewritten for the upstream").write(w)
			return
		}
		h := http.Header{}
		if a := r.Header.Get("Accept"); a != "" {
			h.Set("Accept", a)
		}
		start := s.now()
		res := s.plane.Relay(r.Context(), w, Call{Target: t, Path: path, Body: out, Header: h, Sink: usageSink, Answer: answer})
		s.account(c, u, model, res)
		s.logCall(c, u, model, res, s.now().Sub(start))
	}
}

// keyRefusal answers a call whose upstream key can't be had.
func (s *Service) keyRefusal(u effUpstream, err error) *refusal {
	switch {
	case errors.Is(err, ErrVaultSealed):
		return refuse(http.StatusServiceUnavailable, "xbin_vault_sealed",
			"the workspace vault is sealed, and the key of the upstream "+u.Name+" is kept in it: a workspace admin unseals the vault (bx vault unseal, or the admin console)")
	case errors.Is(err, ErrVaultLocked):
		return refuse(http.StatusServiceUnavailable, "xbin_vault_sealed",
			"the workspace vault isn't set up, and the key of the upstream "+u.Name+" is kept in it: a workspace admin sets the vault up (bx vault unseal)")
	}
	return refuse(http.StatusServiceUnavailable, "xbin_llm_upstream_key", "the upstream "+u.Name+": "+err.Error())
}

// limitsFor are c's limits and the workspace's, judged against what is
// metered now.
func (s *Service) limitsFor(snap *snapshot, c Caller) (person, workspace []Limit) {
	now := s.now()
	pw, ww := s.meter.windows(now, c.User)
	if c.User != "" {
		var orgs []string
		if s.opt.OrgsOf != nil && c.User != OwnerUser {
			orgs = s.opt.OrgsOf(c.User)
		}
		person = judge(snap.quotas.resolve(c.User, orgs), pw, now)
	}
	return person, judge(snap.quotas.workspaceLimits(), ww, now)
}

// checkQuota refuses a call past a stopping quota (429, naming the quota
// and when it resets) and marks one past a warning quota.
func (s *Service) checkQuota(snap *snapshot, c Caller, answer http.Header) *refusal {
	person, workspace := s.limitsFor(snap, c)
	v := verdictOf(person, workspace)
	if l := v.Stop; l != nil {
		ref := refuse(http.StatusTooManyRequests, "xbin_llm_quota_exceeded", "LLM quota reached: "+l.describe())
		ref.err.Type = sdkllm.ErrTypeQuota
		ref.err.Extra = map[string]any{"quota": l.quotaName(), "resets": l.Resets, "limit": l.Value, "used": l.Used}
		if at, err := time.Parse(time.RFC3339, l.Resets); err == nil {
			ref.header = http.Header{"Retry-After": {strconv.FormatInt(max(int64(at.Sub(s.now()).Seconds()), 1), 10)}}
		}
		return ref
	}
	if l := v.Warn; l != nil {
		answer.Set(QuotaHeader, "warn; quota="+l.quotaName()+"; resets="+l.Resets)
	}
	return nil
}

// account meters one finished call. The cost is the upstream's price table's
// when it prices the model, else what the upstream itself reported.
func (s *Service) account(c Caller, u effUpstream, model string, res Outcome) {
	cost, priced := u.Prices.Cost(model, res.Usage)
	if !priced && res.Usage.Cost != nil {
		cost, priced = *res.Usage.Cost, true
	}
	failed := !res.OK() || (res.Canceled && !res.HasUsage)
	s.meter.record(s.now(), c.User, c.Component, publicName(u, model), res.Usage, cost, priced && res.HasUsage, failed)
}

// logCall is the one line a call leaves: who, which model, how it went, how
// much — never a body, never a key.
func (s *Service) logCall(c Caller, u effUpstream, model string, res Outcome, took time.Duration) {
	args := []any{"user", c.User, "component", c.Component, "via", c.Via, "model", publicName(u, model), "status", res.Status,
		"in", res.Usage.InputTokens, "out", res.Usage.OutputTokens, "ms", took.Milliseconds()}
	if c.Label != "" {
		args = append(args, "key", c.Label)
	}
	switch {
	case res.Canceled:
		s.log.Debug("llm: call canceled", append(args, "why", res.Err)...)
	case res.Err != "":
		s.log.Warn("llm: call failed", append(args, "err", res.Err)...)
	default:
		s.log.Debug("llm: call", args...)
	}
}

// memAnswer is a ResponseWriter that keeps a (bounded) answer: the test
// call's.
type memAnswer struct {
	h      http.Header
	status int
	body   []byte
}

func (m *memAnswer) Header() http.Header { return m.h }
func (m *memAnswer) WriteHeader(code int) {
	if m.status == 0 {
		m.status = code
	}
}
func (m *memAnswer) Write(p []byte) (int, error) {
	if m.status == 0 {
		m.status = http.StatusOK
	}
	if room := 64<<10 - len(m.body); room > 0 {
		m.body = append(m.body, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// testCall makes the smallest chat completion through t and answers how it
// went, with the upstream's (or the plane's) error message when it failed.
func (s *Service) testCall(ctx context.Context, t Target, model string) (Outcome, string) {
	body, _ := json.Marshal(sdkllm.ChatRequest{Model: model, Messages: []sdkllm.ChatMessage{{Role: "user", Content: "Reply with the single word: ok"}}})
	rec := &memAnswer{h: http.Header{}}
	out := s.plane.Relay(ctx, rec, Call{Target: t, Path: sdkllm.PathChat, Body: body, Sink: usageSink})
	if out.OK() {
		return out, ""
	}
	if e, ok := sdkllm.ParseError(rec.body); ok {
		return out, e.Message
	}
	return out, ""
}
