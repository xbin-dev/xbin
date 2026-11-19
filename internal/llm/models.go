package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// models.go — which upstream and model a call means, and the model list.
//
// A model is named "<upstream>/<model>". What a request's model resolves
// to, first match wins (docs/llm.md "Model names"):
//
//  1. no model, or xbin/default → the workspace's default model;
//     xbin/<role> → that role's model, else the default;
//  2. "<upstream>/<model>" where <upstream> is configured → that upstream.
//     With a model list the model must be on it; without one any name is
//     passed through and the upstream judges it;
//  3. a bare name on exactly one upstream's model list → that upstream;
//  4. an alias → its target, resolved from the top (aliases may chain a few
//     steps);
//  5. any other name, when the workspace has a single upstream and it
//     passes names through → that upstream;
//  6. anything else → the alias "*" when there is one (an admin's choice to
//     map every unknown name, e.g. a CLI's own default, to one model);
//  7. else refused with 404 model_not_found — never silently mapped to the
//     default: a client that asked for one model must not be billed for
//     another without an admin having said so.
//
// Aliases come after everything configuration names (2, 3): an alias can
// add a name, never take one over. So an admin's alias can't redirect a
// model of the host's upstream, or the host's default written as one.

// modelError is why a model name resolves to nothing.
type modelError struct {
	status int
	code   string
	msg    string
}

func (e *modelError) Error() string { return e.msg }

func notFound(format string, a ...any) *modelError {
	return &modelError{http.StatusNotFound, "model_not_found", fmt.Sprintf(format, a...)}
}

const maxAliasHops = 5

// resolve answers the upstream and the upstream's own model name for a
// request's model.
func (s *snapshot) resolve(model string) (effUpstream, string, error) {
	u, m, err := s.resolveNamed(model)
	if err == nil {
		return u, m, nil
	}
	if any := s.aliases[AliasAny]; any != "" {
		if u, m, err2 := s.resolveNamed(any); err2 == nil {
			return u, m, nil
		}
	}
	return effUpstream{}, "", err
}

func (s *snapshot) resolveNamed(model string) (effUpstream, string, error) {
	asked := model
	for hop := 0; ; hop++ {
		if hop > maxAliasHops {
			return effUpstream{}, "", &modelError{http.StatusBadRequest, "xbin_llm_alias_loop", "model " + clip(asked) + ": its aliases go in a circle (an admin fixes them: bx llm alias)"}
		}
		role, isRole := strings.CutPrefix(model, pseudoUpstream+"/")
		switch {
		case model == "" || (isRole && role == RoleDefault):
			if s.defaults.Model == "" {
				return effUpstream{}, "", &modelError{http.StatusBadRequest, "xbin_llm_no_default",
					"the request names no model and the workspace has no default model (an admin sets one: bx llm default set <model>)"}
			}
			model = s.defaults.Model
			continue
		case isRole:
			next := s.defaults.Roles[role]
			if next == "" {
				next = s.defaults.Model
			}
			if next == "" {
				return effUpstream{}, "", notFound("model %s: the workspace has no such model and no default model (an admin sets one: bx llm default set <model> --role <role>)", clip(model))
			}
			model = next
			continue
		}
		// what configuration names comes before any alias
		if up, rest, ok := strings.Cut(model, "/"); ok && rest != "" {
			if u, has := s.upstreams[up]; has {
				if len(u.Models) > 0 && !slices.Contains(u.Models, rest) {
					return effUpstream{}, "", notFound("model %s: the upstream %s doesn't offer it (it offers: %s)", clip(asked), up, strings.Join(u.Models, ", "))
				}
				return u, rest, nil
			}
		}
		switch offering := s.offering(model); {
		case len(offering) == 1:
			return s.upstreams[offering[0]], model, nil
		case len(offering) > 1:
			return effUpstream{}, "", &modelError{http.StatusBadRequest, "xbin_llm_ambiguous_model",
				fmt.Sprintf("model %s is offered by the upstreams %s: name it <upstream>/%s", clip(asked), strings.Join(offering, ", "), clip(model))}
		}
		if t, ok := s.aliases[model]; ok && model != AliasAny {
			model = t
			continue
		}
		break
	}
	if len(s.upstreams) == 1 {
		if u := s.upstreams[s.upstreamNames()[0]]; len(u.Models) == 0 {
			return u, model, nil // the single upstream passes names through
		}
	}
	return effUpstream{}, "", notFound("model %s: no upstream offers it — name a model as <upstream>/<model> (GET /api/xbin/llm/v1/models lists them; upstreams: %s)",
		clip(asked), strings.Join(s.upstreamNames(), ", "))
}

// offering are the upstreams whose model list has the bare name model.
func (s *snapshot) offering(model string) []string {
	var out []string
	for _, name := range s.upstreamNames() {
		if slices.Contains(s.upstreams[name].Models, model) {
			out = append(out, name)
		}
	}
	return out
}

// clip shortens a caller's model name for an error message.
func clip(model string) string {
	if len(model) > 80 {
		return model[:80] + "…"
	}
	return model
}

// unknownModel is the name every call for a model nothing lists is metered
// and logged under, per upstream: a caller picks the name it sends, and
// must not pick how many rows the usage store keeps.
const unknownModel = "(unknown)"

// publicName is how a resolved model is named in listings and usage.
func publicName(u effUpstream, model string) string { return u.Name + "/" + model }

// ---- the model list ----------------------------------------------------------

const (
	modelsTTL    = time.Minute      // an upstream's own list is asked for at most this often
	modelsErrTTL = 10 * time.Second // …and after a failure, this soon again
)

// modelCache keeps each passthrough upstream's /models answer.
type modelCache struct {
	mu         sync.Mutex
	by         map[string]modelEntry
	refreshing map[string]bool
}

type modelEntry struct {
	at     time.Time
	target string // base URL + whether a key: a changed upstream is asked again
	models []sdkllm.Model
	err    string
}

func (c *modelCache) get(name, target string, now time.Time) (modelEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.by[name]
	if !ok || e.target != target {
		return modelEntry{}, false
	}
	ttl := modelsTTL
	if e.err != "" {
		ttl = modelsErrTTL
	}
	return e, now.Sub(e.at) < ttl
}

// peek is name's entry however old (ok false: never fetched for this
// target), and whether it is stale.
func (c *modelCache) peek(name, target string, now time.Time) (e modelEntry, stale, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok = c.by[name]
	if !ok || e.target != target {
		return modelEntry{}, false, false
	}
	ttl := modelsTTL
	if e.err != "" {
		ttl = modelsErrTTL
	}
	return e, now.Sub(e.at) >= ttl, true
}

// refresh claims the one background refresh of name (false: one runs).
func (c *modelCache) refresh(name string, running bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing == nil {
		c.refreshing = map[string]bool{}
	}
	if running && c.refreshing[name] {
		return false
	}
	c.refreshing[name] = running
	return true
}

func (c *modelCache) put(name string, e modelEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[string]modelEntry{}
	}
	c.by[name] = e
}

func (c *modelCache) drop() {
	c.mu.Lock()
	c.by = nil
	c.mu.Unlock()
}

// upstreamModels are the models offered through u: its configured list, or
// the upstream's own (cached), each as the upstream names it. A configured
// model carries what the upstream's own list says of it — its context
// window above all — when the upstream has such a list and answers: the
// list an admin wrote names models, it doesn't describe them, and a
// consumer that sizes its context by the window (the agent template's
// compaction) would otherwise treat a 1 M-token model as an unknown one.
func (s *Service) upstreamModels(ctx context.Context, u effUpstream) ([]sdkllm.Model, string) {
	if len(u.Models) > 0 {
		out := make([]sdkllm.Model, len(u.Models))
		fetch, cancel := context.WithTimeout(ctx, 5*time.Second)
		own, _ := s.upstreamOwn(fetch, u) // only for what it says of them: a failure leaves the names
		cancel()
		for i, m := range u.Models {
			out[i] = sdkllm.Model{ID: m, Object: "model"}
			for _, o := range own {
				if o.ID == m {
					out[i].Created, out[i].Extra = o.Created, o.Extra
					break
				}
			}
		}
		return out, ""
	}
	return s.upstreamOwn(ctx, u)
}

// upstreamOwn is the upstream's own model list, cached.
func (s *Service) upstreamOwn(ctx context.Context, u effUpstream) ([]sdkllm.Model, string) {
	t, err := s.target(u)
	if err != nil {
		return nil, err.Error()
	}
	fp := t.BaseURL + "|" + hint(t.Key)
	now := s.now()
	if e, fresh := s.models.get(u.Name, fp, now); fresh {
		return e.models, e.err
	}
	models, err := s.plane.Models(ctx, t)
	e := modelEntry{at: now, target: fp, models: models}
	if err != nil {
		e.err = err.Error()
	}
	s.models.put(u.Name, e)
	return e.models, e.err
}

// meterName is the name a call for model through u is metered and logged
// under: "<upstream>/<model>" when configuration or the upstream's own list
// names the model, "<upstream>/(unknown)" otherwise. A pass-through
// upstream's list is the cached one — fetched now when there is none yet
// (bounded), refreshed in the background when it is old — so the names a
// caller can make rows of are the names the upstream itself has.
func (s *Service) meterName(ctx context.Context, u effUpstream, model string) string {
	if len(u.Models) > 0 { // resolve only passes a listed model
		return publicName(u, model)
	}
	t, err := s.target(u)
	if err != nil {
		return publicName(u, unknownModel)
	}
	fp := t.BaseURL + "|" + hint(t.Key)
	e, stale, ok := s.models.peek(u.Name, fp, s.now())
	switch {
	case !ok:
		fetch, cancel := context.WithTimeout(ctx, 5*time.Second)
		models, _ := s.upstreamOwn(fetch, u)
		cancel()
		e.models = models
	case stale && s.models.refresh(u.Name, true):
		go func() {
			defer s.models.refresh(u.Name, false)
			fetch, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			s.upstreamOwn(fetch, u)
		}()
	}
	for _, m := range e.models {
		if m.ID == model {
			return publicName(u, model)
		}
	}
	return publicName(u, unknownModel)
}

// effortFor is what is known of model's reasoning on u for a call (D249,
// effort.go): the configuration's entry, else the upstream's own list as
// cached, over xbind's table. nil: nothing. It never asks the upstream: a
// pass-through upstream's list is fetched for the call anyway (meterName),
// and one with a configured list is read when models are listed — a call
// makes no request it didn't make before.
func (s *Service) effortFor(u effUpstream, model string) *EffortRecord {
	if _, ok := lookupModel(u.Reasoning, model); ok {
		return effortOf(u, model, nil)
	}
	var extra map[string]json.RawMessage
	if t, err := s.target(u); err == nil {
		e, _, _ := s.models.peek(u.Name, t.BaseURL+"|"+hint(t.Key), s.now())
		for _, m := range e.models {
			if m.ID == model {
				extra = m.Extra
				break
			}
		}
	}
	return effortOf(u, model, extra)
}

// listModels is GET /v1/models for snap: every upstream's models as
// "<upstream>/<model>" (the upstream's own model object kept, so context
// sizes and the like pass through), then the aliases and the default
// models as aliases. errs says which upstream's list couldn't be read.

func (s *Service) listModels(ctx context.Context, snap *snapshot, c Caller) (sdkllm.ModelList, map[string]string) {
	names := snap.upstreamNames()
	type res struct {
		models []sdkllm.Model
		err    string
	}
	results := make([]res, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].models, results[i].err = s.upstreamModels(ctx, snap.upstreams[name])
		}()
	}
	wg.Wait()
	list := sdkllm.ModelList{Object: "list", Data: []sdkllm.Model{}}
	errs := map[string]string{}
	listing := c
	listing.Listing = true                  // the policy is asked what to show, not whether a call runs (Caller.Listing)
	efforts := map[string]json.RawMessage{} // a listed model's effort, by its public name: its aliases carry it too
	for i, name := range names {
		u := snap.upstreams[name]
		if results[i].err != "" {
			errs[name] = results[i].err
		}
		for _, m := range results[i].models {
			if m.ID == "" || s.refusedByPolicy(listing, u, m.ID) {
				continue
			}
			// what the connector knows of its reasoning (D249): the upstream's own effort member is replaced by
			// the record made of it, the configuration's and xbind's table; with none, its list's members pass as they came
			if rec := effortOf(u, m.ID, m.Extra); rec != nil {
				extra := make(map[string]json.RawMessage, len(m.Extra)+1)
				maps.Copy(extra, m.Extra)
				extra["effort"] = rec.listing()
				m.Extra = extra
				efforts[publicName(u, m.ID)] = extra["effort"]
			}
			m.ID = publicName(u, m.ID)
			if m.Object == "" {
				m.Object = "model"
			}
			if m.OwnedBy == "" {
				m.OwnedBy = name
			}
			list.Data = append(list.Data, m)
		}
	}
	alias := func(id, target string) {
		u, m, err := snap.resolve(target)
		if err != nil || s.refusedByPolicy(listing, u, m) {
			return
		}
		a := sdkllm.Model{ID: id, Object: "model", OwnedBy: "alias", AliasOf: publicName(u, m)}
		if e, ok := efforts[a.AliasOf]; ok {
			a.Extra = map[string]json.RawMessage{"effort": e}
		} else if rec := effortOf(u, m, nil); rec != nil { // a model its upstream doesn't list: what configuration and the table say
			a.Extra = map[string]json.RawMessage{"effort": rec.listing()}
		}
		list.Data = append(list.Data, a)
	}
	for _, a := range slices.Sorted(maps.Keys(snap.aliases)) {
		if a != AliasAny {
			alias(a, snap.aliases[a])
		}
	}
	if snap.defaults.Model != "" {
		alias(pseudoUpstream+"/"+RoleDefault, snap.defaults.Model)
	}
	for _, r := range slices.Sorted(maps.Keys(snap.defaults.Roles)) {
		alias(pseudoUpstream+"/"+r, snap.defaults.Roles[r])
	}
	return list, errs
}

// RoleModelWindow is the model a role resolves to ("<upstream>/<model>";
// "" when none does) and its context window as its upstream's list says
// (0: unknown) — what an admin's view of the workspace agent's context
// budget is computed for (D246; delegation.EffectiveBudget). It may ask the
// upstream for its list (cached).
func (s *Service) RoleModelWindow(ctx context.Context, role string) (string, int) {
	snap := s.current()
	if ready(snap) != nil {
		return "", 0
	}
	u, m, err := snap.resolve(pseudoUpstream + "/" + role)
	if err != nil {
		return "", 0
	}
	list, _ := s.upstreamModels(ctx, u)
	for _, x := range list {
		if x.ID == m {
			return publicName(u, m), x.ContextWindow() // the SDK's reading: the agent template's own (llm_providers.go)
		}
	}
	return publicName(u, m), 0
}
