// Package users is the multi-user store (plans/multi-user.md): human users
// with a role, a tile allow-list, and a terminal permission, persisted to
// data/users.json (xbind-owned, 0600). Passwords are hashed with Argon2id.
//
// The root token (XBIN_TOKEN) is separate and always admin — this store only
// holds the human users layered on top. No users configured ⇒ single-user
// mode (the root token is the only principal).
package users

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// User is a human account. PassHash is never serialized outward (API strips it).
type User struct {
	ID       string   `json:"id"`   // stable, lowercase; the login name
	Name     string   `json:"name"` // display name
	Role     string   `json:"role"` // admin | user
	Tiles    []string `json:"tiles"`
	Terminal bool     `json:"terminal"`
	PassHash string   `json:"passHash,omitempty"`
	Created  int64    `json:"created"`
}

// IsAdmin reports the admin role.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// CanUseTile reports whether the user may open/drive component `path`.
// Admins: everything. Others: exact match or a `prefix/*` / `*` entry.
func (u *User) CanUseTile(path string) bool {
	if u.IsAdmin() {
		return true
	}
	for _, t := range u.Tiles {
		if t == "*" || t == path {
			return true
		}
		if strings.HasSuffix(t, "/*") {
			prefix := strings.TrimSuffix(t, "/*")
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
	}
	return false
}

// CanTerminal reports terminal (root-shell) permission.
func (u *User) CanTerminal() bool { return u.IsAdmin() || u.Terminal }

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

	// tokenLoginDisabled turns off the bootstrap owner-token *browser* login
	// (the /login?token= URL and the owner-token cookie) once real accounts
	// exist. The Bearer owner token still works for tooling (bx). Enforced in
	// internal/auth and the login handler.
	tokenLoginDisabled bool
}

// Open loads (or starts empty) the user store under dataDir.
func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, "users.json"), byID: map[string]*User{}}
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Users              []*User `json:"users"`
		TokenLoginDisabled bool    `json:"tokenLoginDisabled"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("users.json: %w", err)
	}
	for _, u := range doc.Users {
		s.byID[u.ID] = u
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.byID[u.ID]
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

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, normalizeID(id))
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	users := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	b, err := json.MarshalIndent(map[string]any{"users": users, "tokenLoginDisabled": s.tokenLoginDisabled}, "", "  ")
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
