package signins

// flow.go — the guided sign-ins under way (D230): xbind runs a coding
// agent's credential-printing sign-in for a person (Runner), shows them the
// page it names, types the code they bring back, and keeps the credential
// it prints — straight into the store, never in an answer.
//
// A flow belongs to the person who started it: nobody else can address it,
// and a person has one per coding agent. It ends when it saved its
// sign-in, when the CLI gave up, when its person cancels, and by itself
// after Life; at every end its command is killed and what was read of it
// zeroed.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// Flow is a guided sign-in under way, as its person sees it.
type Flow struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	// URL is the sign-in page to open: https, on the coding agent's own
	// hosts. Paste: the CLI asked for the code the page shows.
	URL       string `json:"url"`
	Paste     bool   `json:"paste"`
	ExpiresAt int64  `json:"expiresAt"`
}

type flow struct {
	id, user, harness, name string
	def                     bool
	g                       *acp.Guided
	expires                 time.Time
	timer                   *time.Timer
}

// Flows are the guided sign-ins under way.
type Flows struct {
	store *Store
	run   Runner
	// Life bounds a flow; StartWait how long the CLI has to print its page;
	// CodeWait how long it has to answer a code.
	Life, StartWait, CodeWait time.Duration
	// Max is how many flows run at once, everyone's together.
	Max int

	mu       sync.Mutex
	byID     map[string]*flow
	starting map[string]bool // user\x00harness: a start under way
}

// NewFlows is the flows over store, their commands run by run.
func NewFlows(store *Store, run Runner) *Flows {
	return &Flows{store: store, run: run, Life: 15 * time.Minute, StartWait: 30 * time.Second, CodeWait: 60 * time.Second, Max: 8,
		byID: map[string]*flow{}, starting: map[string]bool{}}
}

func (fl *flow) view() Flow {
	l := fl.g.Link()
	return Flow{ID: fl.id, Harness: fl.harness, URL: l.URL, Paste: l.Paste, ExpiresAt: fl.expires.UnixMilli()}
}

// Of lists user's flows.
func (f *Flows) Of(user string) []Flow {
	f.mu.Lock()
	var mine []*flow
	for _, fl := range f.byID {
		if fl.user == user {
			mine = append(mine, fl)
		}
	}
	f.mu.Unlock()
	out := []Flow{}
	for _, fl := range mine {
		out = append(out, fl.view())
	}
	return out
}

// Start starts user's guided sign-in to harness — or answers the one they
// already have for it — and waits for the CLI to print its page. name ("":
// the next free one) is what the sign-in will be saved as; def makes it
// the coding agent's default.
func (f *Flows) Start(ctx context.Context, user, harness, name string, def bool) (Flow, error) {
	s := f.store
	if _, ok := s.person(user); !ok {
		return Flow{}, refuse(403, "saved sign-ins are a signed-in person's")
	}
	if !s.Policy().Enabled {
		return Flow{}, ErrOff
	}
	prov, ok := s.o.Provider(harness)
	if !ok {
		return Flow{}, refuse(400, "harness: %q is not a coding agent this xbind runs", harness)
	}
	if prov.Mint == nil {
		return Flow{}, refuse(409, "%s has no sign-in xbind can keep for you — paste a key instead, or sign in on a tile in a terminal (%s)", prov.Name, prov.Login)
	}
	if name != "" {
		if name = CleanName(name); name == "" {
			return Flow{}, refuse(400, "name: one line, up to %d characters", NameMax)
		}
	}
	switch s.VaultState() { // before the person signs in anywhere: could it be kept?
	case VaultSealed:
		return Flow{}, ErrSealed
	case VaultLocked:
		return Flow{}, ErrLocked
	}
	all, _, err := s.List(user)
	if err != nil {
		return Flow{}, err
	}
	n, renames := 0, false
	for _, sg := range all {
		if sg.Harness == harness {
			n++
			renames = renames || (name != "" && strings.EqualFold(sg.Name, name))
		}
	}
	if n >= MaxPerHarness && !renames {
		return Flow{}, refuse(409, "you keep at most %d sign-ins for %s: remove one first", MaxPerHarness, prov.Name)
	}

	key := user + "\x00" + harness
	f.mu.Lock()
	for _, fl := range f.byID {
		if fl.user == user && fl.harness == harness {
			f.mu.Unlock()
			return fl.view(), nil // theirs, still open: the same page again
		}
	}
	if f.starting[key] {
		f.mu.Unlock()
		return Flow{}, refuse(409, "a sign-in to %s is already starting for you", prov.Name)
	}
	if len(f.byID)+len(f.starting) >= f.Max {
		f.mu.Unlock()
		return Flow{}, refuse(429, "too many sign-ins are under way on this workspace right now — try again in a minute")
	}
	f.starting[key] = true
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.starting, key)
		f.mu.Unlock()
	}()

	exec, err := f.run.Start(*prov.Mint, f.Life)
	if err != nil {
		return Flow{}, refuse(502, "%s's sign-in could not be started here: %v", prov.Name, err)
	}
	g := &acp.Guided{Name: prov.Name, Spec: *prov.Mint, Exec: exec}
	if _, err := g.Start(ctx, f.StartWait); err != nil {
		_ = g.Close()
		return Flow{}, flowErr(err)
	}
	fl := &flow{id: "sf_" + newID()[3:], user: user, harness: harness, name: name, def: def, g: g, expires: s.o.Now().Add(f.Life)}
	f.mu.Lock()
	f.byID[fl.id] = fl
	fl.timer = time.AfterFunc(f.Life, func() { f.end(fl) }) // never left waiting for nobody
	f.mu.Unlock()
	return fl.view(), nil
}

// flowErr is a driver's error as the API answers it.
func flowErr(err error) error {
	var ge *acp.GuidedError
	if !errors.As(err, &ge) {
		return refuse(502, "%v", err)
	}
	switch ge.Kind {
	case acp.GuidedInvalid:
		return refuse(409, "%s", ge.Msg)
	case acp.GuidedTimeout:
		return refuse(504, "%s", ge.Msg)
	case acp.GuidedOver:
		return refuse(404, "%s", ge.Msg)
	}
	return refuse(502, "%s", ge.Msg)
}

// get is user's flow id.
func (f *Flows) get(user, id string) *flow {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl := f.byID[id]; fl != nil && fl.user == user {
		return fl
	}
	return nil
}

// end ends fl: its command killed, what it read zeroed. false: ended already.
func (f *Flows) end(fl *flow) bool {
	f.mu.Lock()
	cur := f.byID[fl.id] == fl
	if cur {
		delete(f.byID, fl.id)
	}
	if fl.timer != nil {
		fl.timer.Stop()
	}
	f.mu.Unlock()
	if cur {
		_ = fl.g.Close()
	}
	return cur
}

// Code types code into user's flow id and waits for the CLI's word: the
// sign-in it saved (replaced: it took another secret's place), or why not.
// A refusal with status 409 leaves the flow open for another code; every
// other end closes it. It runs on a context of its own: a request gone
// mid-way doesn't leave a printed credential waiting.
func (f *Flows) Code(user, id, code string) (Signin, bool, error) {
	fl := f.get(user, id)
	if fl == nil {
		return Signin{}, false, refuse(404, "no sign-in of yours is under way with that id — start one")
	}
	if code = acp.CleanCode(code); code == "" {
		return Signin{}, false, refuse(400, "code: the code the sign-in page showed")
	}
	done, err := fl.g.Code(context.Background(), code, f.CodeWait)
	if err != nil {
		var ge *acp.GuidedError
		if !errors.As(err, &ge) || !ge.Open() {
			f.end(fl)
		}
		return Signin{}, false, flowErr(err)
	}
	f.end(fl)
	if done.Token == "" {
		return Signin{}, false, refuse(502, "the sign-in finished without a credential to keep")
	}
	sg, replaced, err := f.store.Save(user, fl.harness, fl.name, done.Token, fl.def)
	done.Token = ""
	return sg, replaced, err
}

// Cancel ends user's flow id; false when they have none such.
func (f *Flows) Cancel(user, id string) bool {
	fl := f.get(user, id)
	return fl != nil && f.end(fl)
}

// DropUser ends every flow of user's (the account signed out, disabled or
// deleted).
func (f *Flows) DropUser(user string) {
	f.mu.Lock()
	var mine []*flow
	for _, fl := range f.byID {
		if fl.user == user {
			mine = append(mine, fl)
		}
	}
	f.mu.Unlock()
	for _, fl := range mine {
		f.end(fl)
	}
}

// Close ends every flow (xbind stops).
func (f *Flows) Close() {
	f.mu.Lock()
	all := make([]*flow, 0, len(f.byID))
	for _, fl := range f.byID {
		all = append(all, fl)
	}
	f.mu.Unlock()
	for _, fl := range all {
		f.end(fl)
	}
}
