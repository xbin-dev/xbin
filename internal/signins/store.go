// Package signins is the coding-agent sign-in store xbind holds (D230;
// docs/agent-signins.md): per person, the named sign-ins they saved for
// each coding agent — a long-lived token the CLI's own flow printed, or an
// API key they pasted — one of them the default per coding agent.
//
// One file per person, data/signins/<hex(user id)>.json, 0600, under
// xbind's own data directory, which no sandbox mounts. Its metadata (id,
// coding agent, name, kind, the variable it goes to, the times) is in the
// clear; the secrets are sealed whole under a subkey of the vault barrier
// that is the person's own ("signins/<user id>"), the envelope the LLM
// connector's upstream keys use (internal/llm/keys.go). So a list, the
// admins' counts and "the vault is sealed" work while the vault is sealed,
// and no secret can be read then.
//
// Nothing here ever hands a secret to a read: List, People and the routes
// over them carry metadata only. The one way out is Use — called by
// xbind's own session start (internal/term), which puts the credential in
// the coding agent's environment and nowhere else — and each Use is
// written to the person's use log and the workspace's audit log, without
// the secret.
package signins

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// Vault is what the store needs of xbind's vault barrier (*vault.Barrier).
type Vault interface {
	Initialized() bool
	Sealed() bool
	EncryptFor(label string, plaintext []byte) ([]byte, error)
	DecryptFor(label string, blob []byte) ([]byte, error)
}

// The vault states (GET /agent/signins' "vault"), the LLM connector's words.
const (
	VaultOpen      = "open"
	VaultSealed    = "sealed"
	VaultLocked    = "locked"
	VaultPlaintext = "plaintext"
)

// The kinds of sign-in (acp.Key.Kind).
const (
	KindSetupToken = "setup-token"
	KindAPIKey     = "api-key"
)

const (
	// MaxPerHarness is how many sign-ins a person keeps per coding agent.
	MaxPerHarness = 8
	// NameMax is a name's length in characters; SecretMax a secret's in bytes.
	NameMax   = 40
	SecretMax = 4096
	// SetupTokenLife is how long a setup-token is good for (Claude Code's
	// `claude setup-token`: one year).
	SetupTokenLife = 365 * 24 * time.Hour
)

// Error is a refusal the API answers with Status.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func refuse(status int, format string, a ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, a...)}
}

var (
	// ErrSealed: no secret can be read or written now.
	ErrSealed = &Error{Status: 503, Msg: "the workspace vault is sealed: an admin unseals it (bx vault unseal, or the admin console) before saved sign-ins can be used or changed"}
	// ErrLocked: no vault is set up, and a sign-in is never kept in the clear.
	ErrLocked = &Error{Status: 503, Msg: "the workspace vault isn't set up yet: an admin sets it up (bx vault unseal) before a sign-in can be saved"}
	// ErrNotFound: the person has no such sign-in.
	ErrNotFound = &Error{Status: 404, Msg: "no such saved sign-in"}
	// ErrOff: the workspace's switch is off.
	ErrOff = &Error{Status: 403, Msg: "saving coding-agent sign-ins is switched off for this workspace (a workspace admin's setting)"}
	// ErrForeignVault: the secrets were sealed under another vault.
	ErrForeignVault = &Error{Status: 409, Msg: "your saved sign-ins can't be opened with this workspace's vault (it was set up again since they were saved): remove them and sign in again"}
)

// Signin is one saved sign-in as a read shows it: never its secret.
type Signin struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // setup-token | api-key
	Env     string `json:"env"`  // the variable the coding agent reads it from
	// Times in Unix milliseconds; 0 = never / unknown.
	CreatedAt  int64 `json:"createdAt"`
	LastUsedAt int64 `json:"lastUsedAt,omitempty"`
	ExpiresAt  int64 `json:"expiresAt,omitempty"`
	Default    bool  `json:"default"`
	// RefusedAt: the coding agent itself refused it (signed out) when a
	// session last used it; Refused its words, credentials masked.
	RefusedAt int64  `json:"refusedAt,omitempty"`
	Refused   string `json:"refused,omitempty"`
}

// Expired reports a sign-in past its expiry.
func (s Signin) Expired(now time.Time) bool { return s.ExpiresAt > 0 && now.UnixMilli() >= s.ExpiresAt }

// Notice is what the list tells its person once: their sign-ins were
// removed by someone else.
type Notice struct {
	At   int64  `json:"at"`
	Text string `json:"text"`
}

// file is data/signins/<hex>.json.
type file struct {
	V       int      `json:"v"`
	User    string   `json:"user"`
	Account int64    `json:"account,omitempty"` // the account's creation stamp
	Signins []Signin `json:"signins"`
	// the secrets, id → secret: sealed (Enc 1, Data), or in the clear in the
	// plaintext vault mode (Secrets)
	Enc     int               `json:"enc,omitempty"`
	Data    []byte            `json:"data,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
	Notice  *Notice           `json:"notice,omitempty"`
}

// Options configure a Store.
type Options struct {
	// Dir is data/signins.
	Dir string
	// Vault is the barrier the secrets are sealed under; nil with
	// AllowPlaintext is the plaintext vault mode's shape (tests).
	Vault          Vault
	AllowPlaintext bool
	// Person answers an account's creation stamp, and false for one that
	// doesn't exist or is disabled: such a person's sign-ins are unusable.
	// nil: every id is a person (tests).
	Person func(id string) (created int64, ok bool)
	// Provider finds a coding agent of the catalog by id.
	Provider func(id string) (acp.Provider, bool)
	// Host answers the host's file's word on the switch (set: it is the
	// host's). nil: not set.
	Host func() (set, enabled bool)
	// Now is the clock (nil: time.Now).
	Now func() time.Time
}

// Store is the sign-in store.
type Store struct {
	o  Options
	mu sync.Mutex // every read and write of the files
}

// New opens the store over o.Dir (made on first write).
func New(o Options) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Provider == nil {
		o.Provider = acp.Lookup
	}
	return &Store{o: o}
}

// VaultState is the vault as the store meets it.
func (s *Store) VaultState() string {
	switch v := s.o.Vault; {
	case v != nil && v.Initialized() && v.Sealed():
		return VaultSealed
	case v != nil && v.Initialized():
		return VaultOpen
	case s.o.AllowPlaintext:
		return VaultPlaintext
	}
	return VaultLocked
}

func label(user string) string { return "signins/" + user }

func (s *Store) path(user string) string {
	return filepath.Join(s.o.Dir, hex.EncodeToString([]byte(user))+".json")
}

func (s *Store) usePath(user string) string {
	return filepath.Join(s.o.Dir, hex.EncodeToString([]byte(user))+".use.jsonl")
}

// person is user's account stamp; false when the account is gone or
// disabled.
func (s *Store) person(user string) (int64, bool) {
	if user == "" {
		return 0, false
	}
	if s.o.Person == nil {
		return 0, true
	}
	return s.o.Person(user)
}

// read loads user's file (s.mu held): empty for none, and for one another
// account of the same id wrote. A file whose secrets are in the clear (the
// plaintext vault mode) is sealed here as soon as a barrier is open.
func (s *Store) read(user string) (file, error) {
	f := file{V: 1, User: user}
	b, err := os.ReadFile(s.path(user))
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	var got file
	if err := json.Unmarshal(b, &got); err != nil {
		return f, errors.New("the saved sign-ins file is unreadable")
	}
	if stamp, ok := s.person(user); ok && s.o.Person != nil && got.Account != 0 && got.Account != stamp {
		return f, nil // an earlier account under this id: none of it is theirs
	}
	if got.User != user {
		return f, nil
	}
	if got.Enc == 0 && len(got.Secrets) > 0 && s.VaultState() == VaultOpen {
		// kept in the clear while the workspace had no vault barrier: sealed
		// on the first read once one exists, whatever the read is for — not
		// left until the person next saves a sign-in
		if err := s.write(got, got.Secrets); err != nil {
			slog.Warn("signins: sealing a file kept in the clear", "user", user, "err", err)
			return got, nil
		}
		return s.read(user) // sealed now (or gone): no second pass
	}
	return got, nil
}

// secrets opens f's secrets (s.mu held).
func (s *Store) secrets(f file) (map[string]string, error) {
	st := s.VaultState()
	if st == VaultSealed {
		return nil, ErrSealed
	}
	if f.Enc == 0 {
		m := map[string]string{}
		for k, v := range f.Secrets {
			m[k] = v
		}
		return m, nil
	}
	if st != VaultOpen {
		return nil, ErrLocked
	}
	pt, err := s.o.Vault.DecryptFor(label(f.User), f.Data)
	if err != nil {
		return nil, ErrForeignVault
	}
	m := map[string]string{}
	if err := json.Unmarshal(pt, &m); err != nil {
		return nil, ErrForeignVault
	}
	return m, nil
}

// write saves f with secrets m (s.mu held); m nil keeps what f holds
// (a metadata-only change).
func (s *Store) write(f file, m map[string]string) error {
	if m != nil {
		f.Enc, f.Data, f.Secrets = 0, nil, nil
		switch s.VaultState() {
		case VaultSealed:
			return ErrSealed
		case VaultLocked:
			return ErrLocked
		case VaultPlaintext:
			f.Secrets = m
		default:
			pt, err := json.Marshal(m)
			if err != nil {
				return err
			}
			ct, err := s.o.Vault.EncryptFor(label(f.User), pt)
			for i := range pt {
				pt[i] = 0
			}
			if err != nil {
				return err
			}
			f.Enc, f.Data = 1, ct
		}
	}
	f.V = 1
	if stamp, ok := s.person(f.User); ok {
		f.Account = stamp
	}
	if len(f.Signins) == 0 && f.Notice == nil {
		err := os.Remove(s.path(f.User))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.o.Dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path(f.User), append(b, '\n'), 0o600)
}

// List is user's sign-ins — metadata only — newest first within a coding
// agent, and the notice waiting for them.
func (s *Store) List(user string) ([]Signin, *Notice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return nil, nil, err
	}
	out := append([]Signin(nil), f.Signins...)
	sortSignins(out)
	return out, f.Notice, nil
}

func sortSignins(all []Signin) {
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Harness != all[j].Harness {
			return all[i].Harness < all[j].Harness
		}
		return all[i].CreatedAt > all[j].CreatedAt
	})
}

// CleanSecret is a pasted credential as it is kept: trimmed, one line of
// printable ASCII, none of the characters a key file or a shell line would
// have to escape ("" when unfit).
func CleanSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > SecretMax {
		return ""
	}
	for _, r := range v {
		if r <= ' ' || r == 0x7f || r >= utf8.RuneSelf || strings.ContainsRune(`"\<>&'`+"`", r) {
			return ""
		}
	}
	return v
}

// CleanName is a sign-in's name, its spaces folded ("" when unfit).
func CleanName(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" || utf8.RuneCountInString(v) > NameMax || strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ""
	}
	return v
}

// nextName is the name a sign-in saved without one gets: the coding
// agent's own, then "‹name› 2", ….
func nextName(all []Signin, prov acp.Provider) string {
	taken := func(n string) bool {
		for _, s := range all {
			if s.Harness == prov.ID && strings.EqualFold(s.Name, n) {
				return true
			}
		}
		return false
	}
	base := prov.Name
	if utf8.RuneCountInString(base) > NameMax-4 {
		base = prov.ID
	}
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s %d", base, i); !taken(n) {
			return n
		}
	}
}

func newID() string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return "si_" + hex.EncodeToString(b[:])
}

// Save keeps secret as user's sign-in for harness under name ("": the next
// free one). A sign-in of that name is replaced — its id stays. The first
// one of a coding agent is its default, and so is this one with makeDefault.
// replaced reports a secret that took another's place: what runs on the old
// one is the caller's to end.
func (s *Store) Save(user, harness, name, secret string, makeDefault bool) (out Signin, replaced bool, err error) {
	if _, ok := s.person(user); !ok {
		return out, false, refuse(403, "saved sign-ins are a signed-in person's")
	}
	if !s.Policy().Enabled {
		return out, false, ErrOff
	}
	prov, ok := s.o.Provider(harness)
	if !ok {
		return out, false, refuse(400, "harness: %q is not a coding agent this xbind runs", harness)
	}
	secret = CleanSecret(secret)
	if secret == "" {
		return out, false, refuse(400, "secret: one line, up to %d characters, with no spaces, quotes, backslashes, <, > or & — paste the key or token alone", SecretMax)
	}
	key, ok := prov.KeyFor(secret)
	if !ok {
		return out, false, refuse(409, "%s takes no saved credential: sign in on the tile, in a terminal (%s)", prov.Name, prov.Login)
	}
	if name != "" {
		if name = CleanName(name); name == "" {
			return out, false, refuse(400, "name: one line, up to %d characters", NameMax)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return out, false, err
	}
	m, err := s.secrets(f)
	if err != nil {
		return out, false, err
	}
	now := s.o.Now()
	if name == "" {
		name = nextName(f.Signins, prov)
	}
	at, count, hasDefault := -1, 0, false
	for i, sg := range f.Signins {
		if sg.Harness != harness {
			continue
		}
		count++
		hasDefault = hasDefault || sg.Default
		if strings.EqualFold(sg.Name, name) {
			at = i
		}
	}
	sg := Signin{ID: newID(), Harness: harness, Name: name}
	if at >= 0 {
		sg = f.Signins[at]
		sg.Name, replaced = name, m[sg.ID] != secret
	} else if count >= MaxPerHarness {
		return out, false, refuse(409, "you keep at most %d sign-ins for %s: remove one first", MaxPerHarness, prov.Name)
	}
	sg.Kind, sg.Env = key.Kind, key.Env
	sg.CreatedAt, sg.ExpiresAt, sg.RefusedAt, sg.Refused, sg.LastUsedAt = now.UnixMilli(), 0, 0, "", 0
	if key.Kind == KindSetupToken {
		sg.ExpiresAt = now.Add(SetupTokenLife).UnixMilli()
	}
	if !hasDefault || makeDefault {
		for i := range f.Signins {
			if f.Signins[i].Harness == harness {
				f.Signins[i].Default = false
			}
		}
		sg.Default = true
	}
	if at >= 0 {
		f.Signins[at] = sg
	} else {
		f.Signins = append(f.Signins, sg)
	}
	m[sg.ID] = secret
	f.Notice = nil // they signed in again: the notice was read
	if err := s.write(f, m); err != nil {
		return out, false, err
	}
	return sg, replaced, nil
}

// Patch changes a sign-in: each field set is applied.
type Patch struct {
	Name    *string `json:"name"`
	Default *bool   `json:"default"`
	Secret  *string `json:"secret"`
}

// Update renames user's sign-in id, makes it its coding agent's default
// (default false is refused: pick another instead), or replaces its secret.
// replaced reports a new secret.
func (s *Store) Update(user, id string, p Patch) (out Signin, replaced bool, err error) {
	if _, ok := s.person(user); !ok {
		return out, false, refuse(403, "saved sign-ins are a signed-in person's")
	}
	if p.Name == nil && p.Default == nil && p.Secret == nil {
		return out, false, refuse(400, "need {name?, default?: true, secret?} with at least one")
	}
	if p.Default != nil && !*p.Default {
		return out, false, refuse(400, "default: a coding agent keeps one default — make another sign-in the default instead")
	}
	if (p.Default != nil || p.Secret != nil) && !s.Policy().Enabled {
		return out, false, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return out, false, err
	}
	at := -1
	for i := range f.Signins {
		if f.Signins[i].ID == id {
			at = i
		}
	}
	if at < 0 {
		return out, false, ErrNotFound
	}
	sg := f.Signins[at]
	if p.Name != nil {
		name := CleanName(*p.Name)
		if name == "" {
			return out, false, refuse(400, "name: one line, up to %d characters", NameMax)
		}
		for i, o := range f.Signins {
			if i != at && o.Harness == sg.Harness && strings.EqualFold(o.Name, name) {
				return out, false, refuse(409, "you already have a %s sign-in called %s", sg.Harness, o.Name)
			}
		}
		sg.Name = name
	}
	var m map[string]string
	if p.Secret != nil {
		prov, ok := s.o.Provider(sg.Harness)
		secret := CleanSecret(*p.Secret)
		key, fits := prov.KeyFor(secret)
		if !ok || secret == "" || !fits {
			return out, false, refuse(400, "secret: one line, up to %d characters, with no spaces, quotes, backslashes, <, > or &", SecretMax)
		}
		if m, err = s.secrets(f); err != nil {
			return out, false, err
		}
		replaced = m[sg.ID] != secret
		m[sg.ID] = secret
		now := s.o.Now()
		sg.Kind, sg.Env, sg.CreatedAt, sg.ExpiresAt, sg.RefusedAt, sg.Refused, sg.LastUsedAt = key.Kind, key.Env, now.UnixMilli(), 0, 0, "", 0
		if key.Kind == KindSetupToken {
			sg.ExpiresAt = now.Add(SetupTokenLife).UnixMilli()
		}
	}
	if p.Default != nil {
		for i := range f.Signins {
			if f.Signins[i].Harness == sg.Harness {
				f.Signins[i].Default = false
			}
		}
		sg.Default = true
	}
	f.Signins[at] = sg
	if err := s.write(f, m); err != nil {
		return out, false, err
	}
	return sg, replaced, nil
}

// Remove forgets user's sign-in id. It works whatever the vault's state
// (while sealed the secret stays in the sealed blob, unreachable by any id,
// until the next write that opens it drops it).
func (s *Store) Remove(user, id string) (Signin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(user, id)
}

func (s *Store) remove(user, id string) (Signin, error) {
	f, err := s.read(user)
	if err != nil {
		return Signin{}, err
	}
	at := -1
	for i := range f.Signins {
		if f.Signins[i].ID == id {
			at = i
		}
	}
	if at < 0 {
		return Signin{}, ErrNotFound
	}
	gone := f.Signins[at]
	f.Signins = append(f.Signins[:at], f.Signins[at+1:]...)
	if gone.Default { // the newest one left of that coding agent takes over
		best := -1
		for i, sg := range f.Signins {
			if sg.Harness == gone.Harness && (best < 0 || sg.CreatedAt > f.Signins[best].CreatedAt) {
				best = i
			}
		}
		if best >= 0 {
			f.Signins[best].Default = true
		}
	}
	m, err := s.secrets(f)
	if err == nil {
		delete(m, id)
		for k := range m { // secrets no sign-in names any more (a removal made while sealed)
			if !hasID(f.Signins, k) {
				delete(m, k)
			}
		}
	} else {
		m = nil // sealed, or another vault's: the metadata goes now
		if len(f.Signins) == 0 {
			f.Enc, f.Data, f.Secrets = 0, nil, nil
		}
	}
	if err := s.write(f, m); err != nil {
		return Signin{}, err
	}
	return gone, nil
}

func hasID(all []Signin, id string) bool {
	for _, s := range all {
		if s.ID == id {
			return true
		}
	}
	return false
}

// PersonGone deletes everything kept for user (the account was deleted).
func (s *Store) PersonGone(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range []string{s.path(user), s.usePath(user)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			slog.Error("signins: a deleted account's sign-ins could not be removed", "user", user, "err", err)
		}
	}
}

// Clear removes every sign-in of user and leaves them the notice why (a
// credential someone else made for their account; an admin's act). It
// answers what was removed.
func (s *Store) Clear(user, why string) []Signin {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil || len(f.Signins) == 0 {
		return nil
	}
	gone := f.Signins
	f.Signins, f.Enc, f.Data, f.Secrets = nil, 0, nil, nil
	f.Notice = &Notice{At: s.o.Now().UnixMilli(), Text: why}
	if err := s.write(f, nil); err != nil {
		slog.Error("signins: sign-ins could not be cleared", "user", user, "err", err)
		return nil
	}
	return gone
}

// Revoke removes user's sign-in id on someone else's word (an admin's) and
// leaves the person the notice.
func (s *Store) Revoke(user, id, by string) (Signin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gone, err := s.remove(user, id)
	if err != nil {
		return gone, err
	}
	f, err := s.read(user)
	if err == nil {
		f.Notice = &Notice{At: s.o.Now().UnixMilli(), Text: fmt.Sprintf("Your saved sign-in %s was removed by %s.", gone.Name, by)}
		_ = s.write(f, nil)
	}
	return gone, nil
}

// DismissNotice drops user's notice.
func (s *Store) DismissNotice(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, err := s.read(user); err == nil && f.Notice != nil {
		f.Notice = nil
		_ = s.write(f, nil)
	}
}

// MarkRefused notes that the coding agent itself refused sign-in id of
// user's (why: its words; credentials are masked here).
func (s *Store) MarkRefused(user, id, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return
	}
	for i := range f.Signins {
		if f.Signins[i].ID == id {
			f.Signins[i].RefusedAt = s.o.Now().UnixMilli()
			f.Signins[i].Refused = clip(acp.MaskCredentials(why), 300)
			_ = s.write(f, nil)
			return
		}
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// PersonRow is one person's sign-ins as an admin sees them: that, not what
// — no name (the person's own label), no secret.
type PersonRow struct {
	User    string       `json:"user"`
	Total   int          `json:"total"`
	Signins []AdminEntry `json:"signins"`
}

// AdminEntry is one sign-in in an admin's view.
type AdminEntry struct {
	ID         string `json:"id"`
	Harness    string `json:"harness"`
	Kind       string `json:"kind"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
	Default    bool   `json:"default"`
}

// People lists who has sign-ins, by user id.
func (s *Store) People() []PersonRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, _ := os.ReadDir(s.o.Dir)
	var out []PersonRow
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		raw, err := hex.DecodeString(name)
		if !ok || err != nil || name == "policy" {
			continue
		}
		f, err := s.read(string(raw))
		if err != nil || len(f.Signins) == 0 {
			continue
		}
		sortSignins(f.Signins)
		row := PersonRow{User: f.User, Total: len(f.Signins)}
		for _, sg := range f.Signins {
			row.Signins = append(row.Signins, AdminEntry{ID: sg.ID, Harness: sg.Harness, Kind: sg.Kind, CreatedAt: sg.CreatedAt,
				LastUsedAt: sg.LastUsedAt, ExpiresAt: sg.ExpiresAt, Default: sg.Default})
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}
