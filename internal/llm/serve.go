package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// DroppedHeader, on the answer to a translated Responses call (translate.go),
// names what of the request was not sent to the chat upstream: advisory
// fields and hosted tools merely offered, comma-separated and sorted.
const DroppedHeader = "X-XBin-LLM-Dropped"

// ModelHeader, on an answer, names the model the connector called for the
// request: "<upstream>/<model>" (an alias's target). ExpectModelHeader, on
// a request, names the one its caller expects — one whose reasoning the
// request sends back: when the request's model now resolves to another,
// the call is refused (409 xbin_llm_model_moved) before any upstream call.
const (
	ModelHeader       = "X-XBin-LLM-Model"
	ExpectModelHeader = "X-XBin-LLM-Expect-Model"
)

// usageSink picks the usage reader of an OpenAI-wire answer.
func usageSink(stream bool) sdkllm.UsageSink {
	if stream {
		return &sdkllm.StreamUsage{}
	}
	return &sdkllm.BodyUsage{}
}

// apiCall serves POST /llm/v1/chat/completions and /llm/v1/responses
// (path: the wire's path under an upstream's base URL). What costs nothing
// is checked first — who is calling, the caller's slot among its calls in
// flight, the quotas — and only then is the body read.
func (s *Service) apiCall(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.current()
		c, ref := s.admit(r, snap)
		if ref == nil && c.ReadOnly != "" {
			ref = refuse(http.StatusForbidden, "xbin_llm_edge", c.ReadOnly)
		}
		if ref != nil {
			ref.write(w)
			return
		}
		release, ref := s.enter(snap, c)
		if ref != nil {
			ref.write(w)
			return
		}
		defer release()
		answer := http.Header{}
		if ref := s.checkQuota(snap, c, answer); ref != nil {
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
		req, ref := openRequest(path, body)
		if ref != nil {
			ref.write(w)
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
		// the model the request names now resolves to this one (an alias's target may have changed): a caller
		// whose request carries reasoning of the one it expected says so, and is refused before any upstream
		// call when it is another — reasoning goes back only to the model that produced it (D249)
		answer.Set(ModelHeader, publicName(u, model))
		if exp := r.Header.Get(ExpectModelHeader); exp != "" && exp != publicName(u, model) {
			refuse(http.StatusConflict, "xbin_llm_model_moved", clip(req.Model)+" is served by "+publicName(u, model)+" now, not "+clip(exp)+
				": send the request without the reasoning of that model").write(w)
			return
		}
		t, err := s.target(u)
		if err != nil {
			s.keyRefusal(u, err).write(w)
			return
		}
		name := s.meterName(r.Context(), u, model)
		// the caller's reasoning effort, mapped onto the model's own levels when something describes it (D249,
		// effort.go); an undescribed model's request is relayed exactly as it came
		eff := s.effortFor(u, model)
		if dropped, said := effortRewrite(eff, path == sdkllm.PathChat, req.Member, req.SetMember); said != "" || len(dropped) > 0 {
			if said != "" {
				answer.Set(EffortHeader, said)
			}
			if len(dropped) > 0 {
				sort.Strings(dropped)
				answer.Set(DroppedHeader, strings.Join(dropped, ","))
			}
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
		if path == sdkllm.PathResponses && u.ResponsesVia != ViaUpstream {
			// an upstream without a Responses API is served by translation (translate.go, D244). An explicit
			// "chat" always translates. Otherwise the upstream is asked, and its own "no such route" — judged on
			// its own status and body — makes this call go again as chat; that is remembered, for this model,
			// only once such a call has worked: a refusal alone is something a caller can provoke.
			translate, probed := u.ResponsesVia == ViaChat, false
			if !translate && !s.wires.isChatOnly(t, model, start) {
				res := s.plane.Relay(r.Context(), w, Call{Target: t, Path: path, Body: out, Header: h, Sink: usageSink, Answer: answer, Withhold: routeMissing})
				if !res.Withheld {
					s.account(c, u, model, name, res, len(out))
					s.logCall(c, name, res, s.now().Sub(start))
					return
				}
				probed = true // nothing was generated and nothing answered: the call is made again, and metered once
			}
			// what this caller found this upstream's model refusing (replay.go): one caller's requests never
			// change what another sends
			tag, who := mintTag(t, model), replayCaller(c)
			xo := XlateOptions{Effort: eff, NoReplay: s.noReplay.refused(tag, who), Refused: func(field string) {
				// only a refusal that named the field, with the call then working without it
				s.noReplay.remember(u.Name, model, tag, who, field, s.now())
				s.log.Info("llm: upstream refused the reasoning sent back to this model; this caller's translated calls send none until reset",
					"upstream", u.Name, "model", model, "field", field, "user", c.User, "component", c.Component)
			}}
			res, sent, ok := ResponsesViaChatWith(r.Context(), s.plane, w, t, body, req.Model, model, h, answer, xo)
			if d := w.Header().Get(DroppedHeader); d != "" {
				// what a client's Responses calls lose on this upstream, in the log once per kind of loss
				if _, said := s.droppedSaid.LoadOrStore(u.Name+"\x00"+d, true); !said && s.droppedKinds.Add(1) <= 64 {
					s.log.Info("llm: translated Responses calls to this upstream leave fields out (said once)", "upstream", u.Name, "dropped", d)
				}
			}
			if ok {
				if probed && res.OK() {
					s.wires.setChatOnly(t, model, start)
					s.log.Info("llm: upstream has no Responses API for this model; its Responses calls are translated to chat completions", "upstream", u.Name, "model", model)
				}
				s.account(c, u, model, name, res, sent)
				s.logCall(c, name, res, s.now().Sub(start))
			}
			return
		}
		res := s.plane.Relay(r.Context(), w, Call{Target: t, Path: path, Body: out, Header: h, Sink: usageSink, Answer: answer})
		s.account(c, u, model, name, res, len(out))
		s.logCall(c, name, res, s.now().Sub(start))
	}
}

// openRequest reads a request body of the wire at path, refusing what the
// connector can't relay and meter: a model that is no model name, a stream
// flag that is no boolean (an upstream reading "true" where the relay read
// "not streaming" would go unmetered), a Responses call run in the
// background (its usage never passes here).
func openRequest(path string, body []byte) (*sdkllm.Request, *refusal) {
	req, err := sdkllm.OpenRequest(body)
	switch {
	case errors.Is(err, sdkllm.ErrBadStream):
		return nil, refuse(http.StatusBadRequest, "xbin_llm_body", `"stream" must be true or false`)
	case errors.Is(err, sdkllm.ErrBadModel):
		return nil, refuse(http.StatusBadRequest, "xbin_llm_model", `"model" must be a string`)
	case err != nil:
		return nil, refuse(http.StatusBadRequest, "xbin_llm_body", "the request body is not a JSON object")
	}
	if req.Model != "" && validModelName("model", req.Model) != nil {
		return nil, refuse(http.StatusBadRequest, "xbin_llm_model", "model: a model name is 1–"+strconv.Itoa(maxModelName)+" characters without spaces")
	}
	if bg := string(req.Member("background")); path == sdkllm.PathResponses && bg != "" && bg != "false" && bg != "null" {
		return nil, refuse(http.StatusBadRequest, "xbin_llm_unsupported",
			"background responses are not relayed: the connector can't meter an answer it doesn't carry — send the request without \"background\"")
	}
	return req, nil
}

// callerKey names c among the callers that have calls in flight: the
// acting person, or the component for calls nobody acts in.
func callerKey(c Caller) string {
	if c.User != "" {
		return "user:" + c.User
	}
	return "component:" + c.Component
}

// enter takes one of c's slots among its calls in flight, or refuses with
// 429 when the caller already has as many as the workspace allows one
// caller (Settings.Concurrency). release gives the slot back.
func (s *Service) enter(snap *snapshot, c Caller) (release func(), ref *refusal) {
	key := callerKey(c)
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	if s.flight == nil {
		s.flight = map[string]int{}
	}
	if s.flight[key] >= snap.concurrency {
		ref := refuse(http.StatusTooManyRequests, "xbin_llm_busy",
			"too many LLM calls at once: "+strconv.Itoa(s.flight[key])+" of yours are in flight, and this workspace allows one caller "+strconv.Itoa(snap.concurrency)+" — wait for one to end")
		ref.header = http.Header{"Retry-After": {"1"}}
		return nil, ref
	}
	s.flight[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.flightMu.Lock()
			if s.flight[key]--; s.flight[key] <= 0 {
				delete(s.flight, key)
			}
			s.flightMu.Unlock()
		})
	}, nil
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

// estimate is the usage charged for an answer that ended without the
// provider's own: one input token per four bytes of the request, one output
// token per stream event read from the upstream (providers send about a
// token an event), or per four bytes of a plain answer.
func estimate(requestBytes int, res Outcome) sdkllm.Usage {
	u := sdkllm.Usage{InputTokens: int64(requestBytes+3) / 4}
	if res.Stream {
		u.OutputTokens = res.Events
	} else {
		u.OutputTokens = (res.Bytes + 3) / 4
	}
	u.TotalTokens = u.InputTokens + u.OutputTokens
	return u
}

// account meters one finished call under name (meterName). The usage is
// the provider's; when a successful answer ended without it — the client
// went away, the stream broke off, the provider reports none — or the
// client left a request the upstream never answered, it is an estimate, and
// the row says so. The cost is the upstream's price table's when it prices
// the model, else what the upstream itself reported.
func (s *Service) account(c Caller, u effUpstream, model, name string, res Outcome, requestBytes int) {
	row := Counters{Requests: 1}
	usage := res.Usage
	switch {
	case res.HasUsage:
	case res.OK(), res.Canceled && res.Sent && !res.Upstream:
		usage, row.Estimated = estimate(requestBytes, res), 1
	}
	if !res.OK() {
		row.Failed = 1
	}
	row.InputTokens, row.OutputTokens, row.CachedTokens, row.ReasoningTokens = usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.ReasoningTokens
	cost, priced := u.Prices.Cost(model, usage)
	if !priced && usage.Cost != nil {
		cost, priced = *usage.Cost, true
	}
	switch {
	case usage.Zero():
	case priced:
		row.Cost = cost
	default:
		row.Unpriced = 1
	}
	s.meter.record(s.now(), c.User, c.Component, name, row)
}

// logCall is the one line a call leaves: who, which model (as it is
// metered: never a name only the caller chose), how it went, how much —
// never a body, never a key.
func (s *Service) logCall(c Caller, name string, res Outcome, took time.Duration) {
	args := []any{"user", c.User, "component", c.Component, "via", c.Via, "model", name, "status", res.Status,
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

// testCall makes the smallest chat completion through t — one word in, one
// token out — and answers how it went, with the upstream's (or the plane's)
// error message when it failed. A model that takes max_completion_tokens in
// place of max_tokens (OpenAI's reasoning models) is asked again that way.
func (s *Service) testCall(ctx context.Context, t Target, model string) (out Outcome, msg string, sent int) {
	for _, limit := range []string{"max_tokens", "max_completion_tokens"} {
		n := 1
		if limit == "max_completion_tokens" {
			n = 16 // a reasoning model spends its first tokens thinking
		}
		body, _ := json.Marshal(map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": "ping"}}, limit: n})
		rec := &memAnswer{h: http.Header{}}
		out, sent = s.plane.Relay(ctx, rec, Call{Target: t, Path: sdkllm.PathChat, Body: body, Sink: usageSink}), len(body)
		if out.OK() {
			return out, "", sent
		}
		msg = ""
		if e, ok := sdkllm.ParseError(rec.body); ok {
			msg = e.Message
		}
		if out.Status != http.StatusBadRequest || !strings.Contains(msg, "max_completion_tokens") {
			break
		}
	}
	return out, msg, sent
}
