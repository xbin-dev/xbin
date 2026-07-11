// Package users is the multi-user store (plans/multi-user.md): human users
// with a role, per-tile access levels, and terminal-plane grants, persisted
// to data/users.json (xbind-owned, 0600). Passwords are hashed with Argon2id.
//
// The root token (XBIN_TOKEN) is separate and always admin — this store only
// holds the human users layered on top. No users configured ⇒ single-user
// mode (the root token is the only principal).
package users

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Per-tile access levels (plans/DECISIONS.md D16), monotone:
// terminal ⊇ write ⊇ read. `read` = see the tile + its source; `write` =
// edit/drive it; `terminal` = a shell on it.
const (
	LevelRead     = "read"
	LevelWrite    = "write"
	LevelTerminal = "terminal"
)

// levelRank orders levels for the monotone comparison; unknown ranks 0 (none).
func levelRank(l string) int {
	switch l {
	case LevelRead:
		return 1
	case LevelWrite:
		return 2
	case LevelTerminal:
		return 3
	}
	return 0
}

// User is a human account. PassHash is never serialized outward (API strips it).
type User struct {
	ID   string `json:"id"`   // stable, lowercase; the login name
	Name string `json:"name"` // display name
	Role string `json:"role"` // admin | user
	// Tiles maps a component path — or a `prefix/*` / `*` pattern — to that
	// user's access level (read|write|terminal). Levels union: the highest
	// matching entry wins, so patterns widen access and can't narrow it.
	Tiles map[string]string `json:"tiles"`
	// CanCreate lists path patterns (`sales/*`, `*`, or an exact path) under
	// which the user may create tiles; creating one auto-grants them terminal
	// on it ("create ≈ own a namespace", D16).
	CanCreate []string `json:"canCreate,omitempty"`
	// TermAPI / TermNet are the terminal-plane grants for non-admins (D17):
	// without TermAPI their terminals get no live tile-API token (api=0 forced);
	// without TermNet no internet egress (net=none forced). Admins have both
	// implicitly.
	TermAPI  bool   `json:"termApi,omitempty"`
	TermNet  bool   `json:"termNet,omitempty"`
	PassHash string `json:"passHash,omitempty"`
	Created  int64  `json:"created"`
}

// UnmarshalJSON accepts both the current shape (tiles as a path→level map) and
// the legacy one (tiles as a []string allow-list + a global "terminal" bool):
// legacy entries load as `write`, or `terminal` when the flag was set — the
// exact power they had under the old model. Rewritten to the new shape on the
// next save (D15-style).
func (u *User) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Role      string          `json:"role"`
		Tiles     json.RawMessage `json:"tiles"`
		Terminal  bool            `json:"terminal"` // legacy global flag
		CanCreate []string        `json:"canCreate"`
		TermAPI   bool            `json:"termApi"`
		TermNet   bool            `json:"termNet"`
		PassHash  string          `json:"passHash"`
		Created   int64           `json:"created"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	tiles, err := ParseTiles(raw.Tiles, raw.Terminal)
	if err != nil {
		return fmt.Errorf("user %q: %w", raw.ID, err)
	}
	*u = User{
		ID: raw.ID, Name: raw.Name, Role: raw.Role, Tiles: tiles,
		CanCreate: raw.CanCreate, TermAPI: raw.TermAPI, TermNet: raw.TermNet,
		PassHash: raw.PassHash, Created: raw.Created,
	}
	return nil
}

// ParseTiles decodes a tiles field that may be either shape (see
// User.UnmarshalJSON); the API uses it too, so old clients that still POST
// {tiles: [...], terminal: bool} keep working. nil/absent ⇒ empty map.
func ParseTiles(raw json.RawMessage, legacyTerminal bool) (map[string]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]string{}, nil
	}
	if raw[0] == '[' { // legacy allow-list
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("tiles: %w", err)
		}
		level := LevelWrite
		if legacyTerminal {
			level = LevelTerminal
		}
		tiles := make(map[string]string, len(list))
		for _, t := range list {
			if t = strings.TrimSpace(t); t != "" {
				tiles[t] = level
			}
		}
		return tiles, nil
	}
	var tiles map[string]string
	if err := json.Unmarshal(raw, &tiles); err != nil {
		return nil, fmt.Errorf("tiles: %w", err)
	}
	for k, v := range tiles {
		if levelRank(v) == 0 {
			return nil, fmt.Errorf("tiles[%q]: unknown level %q (want read|write|terminal)", k, v)
		}
	}
	if tiles == nil {
		tiles = map[string]string{}
	}
	return tiles, nil
}

// IsAdmin reports the admin role.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// matchTile reports whether allow-list entry t covers component path: exact,
// `prefix/*` (the prefix itself or anything under it), or `*` (everything).
func matchTile(t, path string) bool {
	if t == "*" || t == path {
		return true
	}
	if strings.HasSuffix(t, "/*") {
		prefix := strings.TrimSuffix(t, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return false
}

// TileLevel returns the user's access level for component `path`: the highest
// level among matching Tiles entries ("" = none). Admins: terminal everywhere.
func (u *User) TileLevel(path string) string {
	if u.IsAdmin() {
		return LevelTerminal
	}
	best := ""
	for t, l := range u.Tiles {
		if matchTile(t, path) && levelRank(l) > levelRank(best) {
			best = l
		}
	}
	return best
}

// CanReadTile / CanWriteTile / CanTerminalTile are the monotone level gates
// (read: see the tile + its source; write: edit/drive; terminal: a shell).
func (u *User) CanReadTile(path string) bool {
	return levelRank(u.TileLevel(path)) >= levelRank(LevelRead)
}
func (u *User) CanWriteTile(path string) bool {
	return levelRank(u.TileLevel(path)) >= levelRank(LevelWrite)
}
func (u *User) CanTerminalTile(path string) bool {
	return levelRank(u.TileLevel(path)) >= levelRank(LevelTerminal)
}

// CanCreateTile reports whether the user may create a component at `path`
// (admins: anywhere; others: a CanCreate pattern must cover it).
func (u *User) CanCreateTile(path string) bool {
	if u.IsAdmin() {
		return true
	}
	for _, t := range u.CanCreate {
		if matchTile(t, path) {
			return true
		}
	}
	return false
}

// CanTerminal reports whether the user may open any terminal at all — the
// coarse pre-gate on /ws/term (the per-tile CanTerminalTile decides which).
func (u *User) CanTerminal() bool {
	if u.IsAdmin() {
		return true
	}
	for _, l := range u.Tiles {
		if levelRank(l) >= levelRank(LevelTerminal) {
			return true
		}
	}
	return false
}

// Public is the outward form (no hash).
func (u *User) Public() User {
	c := *u
	c.PassHash = ""
	return c
}

type Store struct {
	path string
	mu   sync.RWMutex
	byID map[string]*User

	// orgs and policy are the org/team layer and the workspace-level policy
	// ceiling rows (orgs.go; plans/orgs.md).
	orgs   map[string]*Org
	policy []PolicyRow

	// tokenLoginDisabled turns off the bootstrap owner-token *browser* login
	// (the /login?token= URL and the owner-token cookie) once real accounts
	// exist. The Bearer owner token still works for tooling (bx). Enforced in
	// internal/auth and the login handler.
	tokenLoginDisabled bool
}

// Open loads (or starts empty) the user store under dataDir.
func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, "users.json"), byID: map[string]*User{}, orgs: map[string]*Org{}}
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Users              []*User     `json:"users"`
		Orgs               []*Org      `json:"orgs"`
		Policy             []PolicyRow `json:"policy"`
		TokenLoginDisabled bool        `json:"tokenLoginDisabled"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("users.json: %w", err)
	}
	for _, u := range doc.Users {
		s.byID[u.ID] = u
	}
	for _, o := range doc.Orgs {
		s.orgs[o.ID] = o
	}
	s.policy = doc.Policy
	s.tokenLoginDisabled = doc.TokenLoginDisabled
	return s, nil
}

// TokenLoginDisabled reports whether owner-token browser login has been turned
// off (see the field doc). The Bearer owner token is unaffected.
func (s *Store) TokenLoginDisabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokenLoginDisabled
}

// HasAdmin reports whether at least one admin user exists — a password login
// that can't be locked out by disabling token login.
func (s *Store) HasAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasAdminLocked()
}

func (s *Store) hasAdminLocked() bool {
	for _, u := range s.byID {
		if u.Role == RoleAdmin {
			return true
		}
	}
	return false
}

// SetTokenLoginDisabled toggles owner-token browser login. Enabling it requires
// an existing admin user, so the operator can't lock everyone out.
func (s *Store) SetTokenLoginDisabled(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v && !s.hasAdminLocked() {
		return fmt.Errorf("create an admin user before disabling token login")
	}
	s.tokenLoginDisabled = v
	return s.persistLocked()
}

// Count is the number of configured users (0 ⇒ single-user/root-only mode).
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

func (s *Store) Get(id string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[normalizeID(id)]
	if !ok {
		return nil, false
	}
	c := *u
	return &c, true
}

func (s *Store) List() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, u.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Verify checks a login, returning the user on success.
func (s *Store) Verify(id, password string) (*User, bool) {
	u, ok := s.Get(id)
	if !ok || u.PassHash == "" {
		// Constant-ish work even for unknown users (blunt username probing).
		_ = hashPassword(password, make([]byte, 16))
		return nil, false
	}
	if !verifyPassword(password, u.PassHash) {
		return nil, false
	}
	return u, true
}

// Upsert creates or replaces a user. If password is non-empty it is (re)hashed;
// if empty on an existing user the current hash is kept.
func (s *Store) Upsert(u User, password string) (*User, error) {
	u.ID = normalizeID(u.ID)
	if u.ID == "" {
		return nil, fmt.Errorf("user id required")
	}
	if u.Role != RoleAdmin && u.Role != RoleUser {
		u.Role = RoleUser
	}
	if u.Tiles == nil {
		u.Tiles = map[string]string{}
	}
	for t, l := range u.Tiles {
		if levelRank(l) == 0 {
			return nil, fmt.Errorf("tiles[%q]: unknown level %q (want read|write|terminal)", t, l)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.byID[u.ID]
	if existing == nil { // new user: the id is a permanent key — validate it once, here
		if err := validID(u.ID); err != nil {
			return nil, err
		}
	}
	if password != "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		u.PassHash = hashPassword(password, salt)
	} else if existing != nil {
		u.PassHash = existing.PassHash
	} else {
		return nil, fmt.Errorf("password required for new user")
	}
	if existing != nil {
		u.Created = existing.Created
	}
	nu := u
	s.byID[u.ID] = &nu
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := nu
	return &c, nil
}

// GrantTile raises a user's level on one tile (never lowers — levels union,
// and an explicit wider grant must survive incidental re-grants). Used for the
// create auto-grant: whoever creates a tile gets a terminal on it (D16).
func (s *Store) GrantTile(id, path, level string) error {
	if levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q", level)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return fmt.Errorf("no such user %q", id)
	}
	if levelRank(u.Tiles[path]) >= levelRank(level) {
		return nil // already at or above
	}
	nu := *u
	nu.Tiles = make(map[string]string, len(u.Tiles)+1)
	for k, v := range u.Tiles {
		nu.Tiles[k] = v
	}
	nu.Tiles[path] = level
	s.byID[nu.ID] = &nu
	return s.persistLocked()
}

// Delete removes a user and strips them from every org (admins, members) and
// team — the same cleanup GitHub does when an account leaves.
func (s *Store) Delete(id string) error {
	id = normalizeID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
	other := func(m string) bool { return m != id }
	for oid, o := range s.orgs {
		if !o.effMember(id) {
			continue
		}
		no := *o
		no.Admins = filter(o.Admins, other)
		no.Members = filter(o.Members, other)
		no.Teams = make([]*Team, 0, len(o.Teams))
		for _, t := range o.Teams {
			nt := *t
			nt.Members = filter(t.Members, other)
			no.Teams = append(no.Teams, &nt)
		}
		s.orgs[oid] = &no
	}
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	users := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	doc := map[string]any{"users": users, "tokenLoginDisabled": s.tokenLoginDisabled}
	if len(s.orgs) > 0 {
		orgs := make([]*Org, 0, len(s.orgs))
		for _, o := range s.orgs {
			orgs = append(orgs, o)
		}
		sort.Slice(orgs, func(i, j int) bool { return orgs[i].ID < orgs[j].ID })
		doc["orgs"] = orgs
	}
	if len(s.policy) > 0 {
		doc["policy"] = s.policy
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// userIDRe / reservedUserIDs enforce the durable-key contract. A user id is
// the PERMANENT key for three things that outlive any one session and can't be
// renamed without orphaning data: the terminal home dir (homes/<user>), the
// per-user prefs bucket, and the "user:<id>" attribution in X-XBin-From / logs
// (plans/multi-user.md, DECISIONS). So an id must be a safe directory/label,
// and once real users exist the rules can't be tightened retroactively — hence
// they're set here, before general availability. Because the charset is a
// subset of what homes' sanitizeHomeKey preserves, id == home key exactly (no
// two ids can fold onto one home). Existing users loaded from disk are never
// re-validated; this gates *creation* only.
var (
	userIDRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	reservedUserIDs = map[string]bool{
		// "owner" is the bootstrap-token principal's home (homes/owner) and the
		// dots-only/empty fallback in sanitizeHomeKey — a human "owner" would
		// share the token principal's $HOME and agent config.
		"owner": true,
	}
)

func validID(id string) error {
	if !userIDRe.MatchString(id) {
		return fmt.Errorf("invalid user id %q: 1–32 chars of a–z, 0–9, dot, dash, underscore, starting with a letter or digit", id)
	}
	if reservedUserIDs[id] {
		return fmt.Errorf("user id %q is reserved", id)
	}
	return nil
}

// --- password hashing (Argon2id, PHC-ish format) ---

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

func hashPassword(password string, salt []byte) string {
	h := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(h))
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
