package llm

import (
	"context"
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
//  2. an alias → its target (aliases may chain a few steps);
//  3. "<upstream>/<model>" where <upstream> is configured → that upstream.
//     With a model list the model must be on it; without one any name is
//     passed through and the upstream judges it;
//  4. a bare name → the one upstream whose list has it; else, when the
//     workspace has a single upstream and it passes names through, that
//     upstream;
//  5. anything else → the alias "*" when there is one (an admin's choice to
//     map every unknown name, e.g. a CLI's own default, to one model);
//  6. else refused with 404 model_not_found — never silently mapped to the
//     default: a client that asked for one model must not be billed for
//     another without an admin having said so.

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
			return effUpstream{}, "", &modelError{http.StatusBadRequest, "xbin_llm_alias_loop", "model " + asked + ": its aliases go in a circle (an admin fixes them: bx llm alias)"}
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
				return effUpstream{}, "", notFound("model %s: the workspace has no %s model and no default model (an admin sets one: bx llm default set <model> --role %s)", model, role, role)
			}
			model = next
			continue
		}
		if t, ok := s.aliases[model]; ok && model != AliasAny {
			model = t
			continue
		}
		break
	}
	if up, rest, ok := strings.Cut(model, "/"); ok && rest != "" {
		if u, has := s.upstreams[up]; has {
			if len(u.Models) > 0 && !slices.Contains(u.Models, rest) {
				return effUpstream{}, "", notFound("model %s: the upstream %s doesn't offer %s (it offers: %s)", asked, up, rest, strings.Join(u.Models, ", "))
			}
			return u, rest, nil
		}
	}
	var offering []string
	for _, name := range s.upstreamNames() {
		if slices.Contains(s.upstreams[name].Models, model) {
			offering = append(offering, name)
		}
	}
	switch {
	case len(offering) == 1:
		return s.upstreams[offering[0]], model, nil
	case len(offering) > 1:
		return effUpstream{}, "", &modelError{http.StatusBadRequest, "xbin_llm_ambiguous_model",
			fmt.Sprintf("model %s is offered by the upstreams %s: name it <upstream>/%s", asked, strings.Join(offering, ", "), model)}
	case len(s.upstreams) == 1:
		if u := s.upstreams[s.upstreamNames()[0]]; len(u.Models) == 0 {
			return u, model, nil // the single upstream passes names through
		}
	}
	return effUpstream{}, "", notFound("model %s: no upstream offers it — name a model as <upstream>/<model> (GET /api/xbin/llm/v1/models lists them; upstreams: %s)",
		asked, strings.Join(s.upstreamNames(), ", "))
}

// publicName is how a resolved model is named in listings and usage.
func publicName(u effUpstream, model string) string { return u.Name + "/" + model }

// ---- the model list ----------------------------------------------------------

const (
	modelsTTL    = time.Minute      // an upstream's own list is asked for at most this often
	modelsErrTTL = 10 * time.Second // …and after a failure, this soon again
)

// modelCache keeps each passthrough upstream's /models answer.
type modelCache struct {
	mu sync.Mutex
	by map[string]modelEntry
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
// the upstream's own (cached), each as the upstream names it.
func (s *Service) upstreamModels(ctx context.Context, u effUpstream) ([]sdkllm.Model, string) {
	if len(u.Models) > 0 {
		out := make([]sdkllm.Model, len(u.Models))
		for i, m := range u.Models {
			out[i] = sdkllm.Model{ID: m, Object: "model"}
		}
		return out, ""
	}
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
	for i, name := range names {
		u := snap.upstreams[name]
		if results[i].err != "" {
			errs[name] = results[i].err
		}
		for _, m := range results[i].models {
			if m.ID == "" || s.refusedByPolicy(c, u, m.ID) {
				continue
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
		if err != nil || s.refusedByPolicy(c, u, m) {
			return
		}
		list.Data = append(list.Data, sdkllm.Model{ID: id, Object: "model", OwnedBy: "alias", AliasOf: publicName(u, m)})
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
