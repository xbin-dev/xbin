// scm-github — GitHub as an scm provider (docs/scm.md, protocol 1): repo
// credentials, repos, pull requests, issues and CI for the tiles bound to
// its `scm` provide (the agent's `scm` slot). A TEMPLATE, partitioned: the
// global instance holds the GitHub App (its keys, installation tokens,
// setup, policy, the identity directory); each person's partition holds
// their own GitHub sign-in. An unpartitioned copy is bot-only. See API.md.
//
// Layout: mode.go decides the instance and the caller; errors.go and
// types.go are the contract's refusals and shapes; gh_client.go and
// gh_jwt.go talk to GitHub; app.go, setup.go and policy.go are the App and
// what managers set; bot.go and person.go mint tokens, signin.go runs the
// device flow and relay.go carries a person's partition's calls to global;
// hello.go, tokens.go, repos.go, pulls.go, checks.go, issues.go and poll.go
// are the /scm/* routes; page.go is the page's own API; store.go the kv
// resources and the vault.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// version is this provider's own version (hello's scm.version).
const version = "1.0.0"

// srv is one instance of the tile.
type srv struct {
	mode string // modeLegacy | modeGlobal | modeUser
	self string // this tile's path
	user string // modeUser: the person

	state kvStore    // this instance's own kv
	conf  kvStore    // global writes, partitions read (conf "public")
	vault vaultStore // this instance's own vault

	gh  *ghClient
	now func() time.Time

	// GitHub's addresses until the App says otherwise (GitHub Enterprise
	// Server: the App's own apiBase and webBase).
	defaultAPI, defaultWeb string

	// relayCall reaches this tile's global instance from a person's
	// partition (xbin.GlobalURL): the call arrives there as the person.
	relayCall func(ctx context.Context, method, path string, body []byte) (*http.Response, error)

	// bgPoll runs the device flow's poller in the background (off in
	// tests, which poll through GET /scm/signin/{pollId}).
	bgPoll bool

	mu      sync.Mutex
	appC    *appState // global, legacy: the App (nil until set up)
	keyC    *appKeys  // the App's secrets, loaded from the vault once
	jwt     jwtCache
	bot     *tokenCache // bot tokens handed out (global, legacy) and person tokens (user)
	intl    *tokenCache // installation tokens the tile uses itself, never handed out
	pulls   *clientIDs  // POST /scm/pulls clientIds
	checksC *shortCache // GET /scm/checks per (identity, repo, sha), 5 s
	reposC  *shortCache // GET /scm/repos per identity, 5 min
	logsC   *shortCache // completed jobs' logs, 10 min (a few: each up to 8 MiB)

	refreshMu sync.Mutex // a person's token refresh: one at a time
	signin    *deviceFlow
}

// Seams the events half of the tile (hook, normalise, subscriptions,
// outbox, delivery) fills from its own files' init(): its routes, its caps,
// the events health hello reports, the ticks it needs, and what a person's
// new partition (or Forget) wipes.
var (
	extraRoutes  []func(s *srv, mux *http.ServeMux)
	extraCaps    []func(s *srv) []string
	eventsHealth = func(s *srv) eventsHealthInfo { return eventsHealthUnknown() }
	tickHooks    []func(ctx context.Context, s *srv)
	wipePerson   []func(s *srv, person string) // global: a person's identity is gone (Forget, a new partition id)
)

func eventsHealthUnknown() eventsHealthInfo {
	return eventsHealthInfo{Webhooks: "unknown", PollMinMs: pollMinMs}
}

func newSrv(partition, self string, state, conf kvStore, vault vaultStore, hc *http.Client, now func() time.Time) *srv {
	s := &srv{
		mode: modeOf(partition), self: self, state: state, conf: conf, vault: vault, now: now,
		defaultAPI: "https://api.github.com", defaultWeb: "https://github.com",
		bot: newTokenCache(2000), intl: newTokenCache(256), pulls: newClientIDs(),
		checksC: newShortCache(500), reposC: newShortCache(200), logsC: newShortCache(4),
	}
	if s.mode == modeUser {
		s.user = partition[len("user:"):]
	}
	s.gh = newGHClient(hc, now)
	s.signin = &deviceFlow{}
	return s
}

func main() {
	part := xbin.Partition()
	state := newSDKKV("state")
	if state == nil {
		log.Fatal("no state resource (grant res:<self>/state writer) — see scope.json")
	}
	conf := newSDKKV("conf")
	if conf == nil || part == "" {
		// An unpartitioned copy never opens a shared resource: it is its
		// own global, so its "conf" is a kv of its own.
		conf = &prefixKV{state, "conf/"}
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	s := newSrv(part, xbin.Self(), state, conf, sdkVault{}, hc, time.Now)
	s.bgPoll = true
	s.relayCall = func(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, xbin.GlobalURL(path), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return xbin.Client().Do(req)
	}
	s.start()
	xbin.Serve(s.routes())
}

// start resumes what a restart interrupted: a person's pending device flow.
func (s *srv) start() {
	if s.mode == modeUser && s.bgPoll {
		s.resumeSignin()
	}
	if s.mode != modeUser {
		// conf "public" in this version's shape (an upgrade may add fields
		// people's partitions read).
		if a, err := s.app(); err == nil && a != nil {
			if err := s.writePublic(); err != nil {
				log.Printf("conf public: %v", err)
			}
		}
	}
}

func (s *srv) routes() http.Handler {
	mux := http.NewServeMux()
	page := true
	mux.HandleFunc("GET /scm/hello", s.scmGuard(page, s.handleHello))
	mux.HandleFunc("POST /scm/token", s.scmGuard(!page, s.handleToken))
	mux.HandleFunc("POST /scm/token/revoke", s.scmGuard(!page, s.handleRevoke))
	mux.HandleFunc("POST /scm/signin", s.scmGuard(page, s.handleSigninStart))
	mux.HandleFunc("GET /scm/signin", s.scmGuard(page, s.handleSigninGet))
	mux.HandleFunc("DELETE /scm/signin", s.scmGuard(page, s.handleSigninForget))
	mux.HandleFunc("GET /scm/signin/{pollId}", s.scmGuard(page, s.handleSigninPoll))
	mux.HandleFunc("GET /scm/repos", s.scmGuard(!page, s.handleRepos))
	mux.HandleFunc("GET /scm/repo", s.scmGuard(!page, s.handleRepo))
	mux.HandleFunc("POST /scm/pulls", s.scmGuard(!page, s.handlePullCreate))
	mux.HandleFunc("GET /scm/pulls", s.scmGuard(!page, s.handlePulls))
	mux.HandleFunc("GET /scm/pulls/{n}", s.scmGuard(!page, s.handlePull))
	mux.HandleFunc("PATCH /scm/pulls/{n}", s.scmGuard(!page, s.handlePullPatch))
	mux.HandleFunc("GET /scm/pulls/{n}/comments", s.scmGuard(!page, s.handleComments))
	mux.HandleFunc("POST /scm/pulls/{n}/comments", s.scmGuard(!page, s.handleCommentCreate))
	mux.HandleFunc("GET /scm/checks", s.scmGuard(!page, s.handleChecks))
	mux.HandleFunc("GET /scm/checks/jobs/{id}/log", s.scmGuard(!page, s.handleJobLog))
	mux.HandleFunc("GET /scm/checks/runs/{id}/annotations", s.scmGuard(!page, s.handleAnnotations))
	mux.HandleFunc("POST /scm/checks/rerun", s.scmGuard(!page, s.handleRerun))
	mux.HandleFunc("GET /scm/issues", s.scmGuard(!page, s.handleIssues))
	mux.HandleFunc("GET /scm/issues/{n}", s.scmGuard(!page, s.handleIssue))
	mux.HandleFunc("POST /scm/poll", s.scmGuard(!page, s.handlePoll))

	// The relay: global's routes for people's own partitions (relay.go).
	mux.HandleFunc("POST /partition/identity", s.relayGuard(s.handleRelayIdentity))
	mux.HandleFunc("DELETE /partition/identity", s.relayGuard(s.handleRelayIdentityDelete))
	mux.HandleFunc("POST /partition/scope", s.relayGuard(s.handleRelayScope))
	mux.HandleFunc("POST /partition/revoke-token", s.relayGuard(s.handleRelayRevokeToken))
	mux.HandleFunc("POST /partition/revoke-grant", s.relayGuard(s.handleRelayRevokeGrant))
	mux.HandleFunc("POST /partition/bot-token", s.relayGuard(s.handleRelayBotToken))

	// Setup and policy (managers, at global or legacy; setup.go, policy.go).
	mux.HandleFunc("GET /setup/app", s.managerGuard(s.handleSetupGet))
	mux.HandleFunc("POST /setup/app", s.managerGuard(s.handleSetupPaste))
	mux.HandleFunc("POST /setup/manifest", s.managerGuard(s.handleManifestStart))
	mux.HandleFunc("POST /setup/manifest/code", s.managerGuard(s.handleManifestCode))
	mux.HandleFunc("GET /setup/github", s.handleManifestCallback)
	mux.HandleFunc("POST /setup/check", s.managerGuard(s.handleSetupCheck))
	mux.HandleFunc("GET /setup/installations", s.managerGuard(s.handleInstallations))
	mux.HandleFunc("GET /api/policy", s.managerGuard(s.handlePolicyGet))
	mux.HandleFunc("PUT /api/policy", s.managerGuard(s.handlePolicyPut))
	mux.HandleFunc("POST /api/revoke-all", s.managerGuard(s.handleRevokeAll))
	mux.HandleFunc("GET /api/page", s.pageGuard(s.handlePage))

	mux.HandleFunc("POST /tick", s.handleTick)
	for _, f := range extraRoutes {
		f(s, mux)
	}
	// The events capability's routes, until the events half mounts its own
	// (method patterns, more specific than these): 501, as for any cap
	// hello doesn't list.
	for _, p := range []string{"/scm/subscriptions", "/scm/subscriptions/{id}", "/scm/events"} {
		mux.HandleFunc(p, s.scmGuard(false, func(w http.ResponseWriter, _ *http.Request, _ who) {
			fail(w, refuse(refUnsupported, "this scm-github doesn't deliver events yet: poll (POST /scm/poll)"))
		}))
	}
	return mux
}

// handleTick is the `tick` cron (registered by what needs it).
func (s *srv) handleTick(w http.ResponseWriter, r *http.Request) {
	if c := s.classify(r); c.cls != clsSelf && c.cls != clsOwner {
		fail(w, refuse(refNotAllowed, "only xbind's cron ticks this tile"))
		return
	}
	for _, f := range tickHooks {
		f(r.Context(), s)
	}
	s.pruneManifestStates()
	w.WriteHeader(http.StatusNoContent)
}

// relay calls this tile's global instance from a person's partition, with
// the person's identity (xbind's, never a body field). A refusal there is
// a refusal here.
func (s *srv) relay(ctx context.Context, method, path string, body, out any) error {
	if s.relayCall == nil {
		return refuse(refUnavailable, "this partition can't reach its global instance")
	}
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := s.relayCall(ctx, method, path, b)
	if err != nil {
		e := refuse(refUnavailable, "this tile's global instance didn't answer")
		e.RetryAfterMs = 5000
		return e
	}
	defer resp.Body.Close()
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(http.MaxBytesReader(nil, resp.Body, 1<<20)); err != nil {
		return refuse(refUnavailable, "this tile's global instance's answer broke off")
	}
	if resp.StatusCode >= 300 {
		var e scmErr
		if json.Unmarshal(raw.Bytes(), &e) != nil || e.Refusal == "" {
			return refuse(refUnavailable, "this tile's global instance answered %d", resp.StatusCode)
		}
		e.Status = resp.StatusCode
		return &e
	}
	if out != nil && raw.Len() > 0 {
		return json.Unmarshal(raw.Bytes(), out)
	}
	return nil
}

// readBody decodes a JSON request body (≤ 1 MiB).
func readBody(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v); err != nil {
		return refuse(refInvalid, "the body isn't the JSON this route takes")
	}
	return nil
}

// prefixKV is a kv under a key prefix of another: an unpartitioned copy's
// "conf" inside its own state.
type prefixKV struct {
	kv     kvStore
	prefix string
}

func (p *prefixKV) Get(key string, v any) error { return p.kv.Get(p.prefix+key, v) }
func (p *prefixKV) Put(key string, v any) error { return p.kv.Put(p.prefix+key, v) }
func (p *prefixKV) Delete(key string) error     { return p.kv.Delete(p.prefix + key) }
func (p *prefixKV) List(prefix string) ([]string, error) {
	keys, err := p.kv.List(p.prefix + prefix)
	for i, k := range keys {
		keys[i] = k[len(p.prefix):]
	}
	return keys, err
}
