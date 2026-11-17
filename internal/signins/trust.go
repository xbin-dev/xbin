package signins

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"sort"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// trust.go — "I trust this tile's source" (D230, the owner's rule of
// 2026-11-17). A person's saved sign-in is used in their own sessions by
// default. Code in a tile runs with the sign-in while the coding agent
// works, so on a tile whose source is not only the person's own the
// sign-in stays out until the person says, once per tile, that they trust
// its source. This file is what a tile's source is (Source), the person's
// answers (Trust, kept with their sign-ins) and the workspace's switch on
// trusting at all.
//
// A tile's source is not only the person's own under either of TWO
// conditions, kept apart here so one can go without the other:
//
//  1. Source.NotOwn — it is owned by someone else: another person, an
//     org, or the workspace (what "created by a different user" comes to:
//     a tile belongs to whoever made it, until it is handed on);
//  2. Source.Editors — someone else who is not a workspace admin can
//     change its code (write level or more, by any source; a partitioned
//     tile too — the code directory is shared). CoEditorsNeedTrust turns
//     this condition off in one line.

// CoEditorsNeedTrust: condition 2 counts. false ⇒ only a tile owned by
// someone else asks for trust, and who else may edit it is not looked at
// (NeedsTrust, Trusts).
const CoEditorsNeedTrust = true

// Source is where a tile's code comes from, as the rule sees it for one
// person (the broker's SigninSource answers it).
type Source struct {
	// Tile is the registered tile; ID its identity — its directory's own,
	// which tells it from a tile made later at the same path ("" when it
	// can't be read: such a tile can't be trusted). Name is its title.
	Tile string
	ID   string
	Name string
	// Owner is the owner ref ("" the workspace, user:<id>, org:<id>);
	// OwnerKind "you" | "person" | "org" | "workspace" and OwnerName say it
	// for a card.
	Owner     string
	OwnerKind string
	OwnerName string
	// NotOwn: condition 1 — the tile is owned by someone else.
	NotOwn bool
	// Editors: condition 2 — the other people, not workspace admins, who
	// can change the tile's code. Sorted.
	Editors []string
}

// NeedsTrust reports a tile whose source is not only the person's own: the
// sign-in goes in only once they trust it.
func (s Source) NeedsTrust() bool {
	return s.NotOwn || (CoEditorsNeedTrust && len(s.Editors) > 0)
}

// Trust is one answer: the person trusts this tile's source, as it stood.
// It holds while its basis does — the same tile (Ident), the same owner,
// and nobody able to change it who couldn't when it was given.
type Trust struct {
	ID      string   `json:"id"`
	Tile    string   `json:"tile"`
	Name    string   `json:"name,omitempty"`
	Ident   string   `json:"ident"`
	Owner   string   `json:"owner"`
	Editors []string `json:"editors,omitempty"`
	At      int64    `json:"at"`
	// Via: "card" (the tile's sign-in card), "approval" (the trust line of
	// a start the person approved), "plan" (a plan's start line).
	Via string `json:"via,omitempty"`
}

// Holds reports whether t still answers for src.
func (t Trust) Holds(src Source) bool {
	if src.ID == "" || t.Ident != src.ID || t.Owner != src.Owner {
		return false
	}
	if CoEditorsNeedTrust {
		for _, e := range src.Editors {
			if !slices.Contains(t.Editors, e) {
				return false // someone new can change it
			}
		}
	}
	return true
}

// MaxTrusted is how many tiles a person's answers are kept for (the oldest
// goes first).
const MaxTrusted = 200

var (
	// ErrTrustOff: the workspace doesn't let people trust a tile's source.
	ErrTrustOff = &Error{Status: 403, Msg: "trusting a tile's source is switched off for this workspace (a workspace admin's setting)"}
	// ErrNoIdentity: the tile can't be told from another at its path.
	ErrNoIdentity = &Error{Status: 409, Msg: "xbind can't tell this tile from another made at the same path, so it can't keep your trust in it"}
	// ErrNoTrust: the person has no such answer.
	ErrNoTrust = &Error{Status: 404, Msg: "no such trusted tile"}
)

// TrustTile records that user trusts src's source as it stands now.
func (s *Store) TrustTile(user string, src Source, via string) (Trust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pol := s.policy()
	switch {
	case !pol.Enabled:
		return Trust{}, ErrOff
	case !pol.Trust.Allowed:
		return Trust{}, ErrTrustOff
	case src.ID == "" || src.Tile == "":
		return Trust{}, ErrNoIdentity
	}
	if _, ok := s.person(user); !ok {
		return Trust{}, ErrNotFound
	}
	f, err := s.read(user)
	if err != nil {
		return Trust{}, err
	}
	var id [9]byte
	_, _ = rand.Read(id[:])
	t := Trust{ID: "tr_" + hex.EncodeToString(id[:]), Tile: src.Tile, Name: src.Name, Ident: src.ID, Owner: src.Owner,
		Editors: append([]string(nil), src.Editors...), At: s.o.Now().UnixMilli(), Via: via}
	// one answer per tile: the one for this tile, or for whatever stood at
	// its path before, is replaced
	f.Trust = slices.DeleteFunc(f.Trust, func(x Trust) bool { return x.Ident == t.Ident || x.Tile == t.Tile })
	f.Trust = append(f.Trust, t)
	if len(f.Trust) > MaxTrusted {
		f.Trust = f.Trust[len(f.Trust)-MaxTrusted:]
	}
	if err := s.write(f, nil); err != nil {
		return Trust{}, err
	}
	return t, nil
}

// Trusts reports whether user's answer covers src as it stands now: given
// for this very tile, its owner unchanged, nobody new able to change it —
// and the workspace still lets people trust.
func (s *Store) Trusts(user string, src Source) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.policy().Trust.Allowed {
		return false
	}
	f, err := s.read(user)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(f.Trust, func(t Trust) bool { return t.Holds(src) })
}

// Trusted is user's answers, newest first.
func (s *Store) Trusted(user string) []Trust {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return nil
	}
	out := append([]Trust(nil), f.Trust...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// Untrust removes user's answer id.
func (s *Store) Untrust(user, id string) (Trust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return Trust{}, err
	}
	i := slices.IndexFunc(f.Trust, func(t Trust) bool { return t.ID == id })
	if i < 0 {
		return Trust{}, ErrNoTrust
	}
	t := f.Trust[i]
	f.Trust = slices.Delete(f.Trust, i, i+1)
	return t, s.write(f, nil)
}

// ---- the workspace's switch on trusting ------------------------------------------

// TrustPolicy is the workspace's word on trusting a tile's source.
type TrustPolicy struct {
	// Allowed: people may say they trust a tile whose source isn't only
	// their own, and have their sign-in used there. On by default.
	Allowed bool `json:"allowed"`
	// Locked: the host's file sets it (Reason says so).
	Locked bool   `json:"locked,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// policyFile is policy.json: the admins' two switches.
type policyFile struct {
	Enabled *bool `json:"enabled,omitempty"`
	Trust   *bool `json:"trust,omitempty"`
}

func (s *Store) policyFile() policyFile {
	var f policyFile
	if b, err := os.ReadFile(s.PolicyPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

func (s *Store) writePolicy(f policyFile) error {
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(s.o.Dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.PolicyPath(), append(b, '\n'), 0o600)
}

// SetTrustPolicy writes the admins' switch on trusting. Refused (409)
// while the host holds it.
func (s *Store) SetTrustPolicy(allowed bool) (Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.policy(); cur.Trust.Locked {
		return cur, refuse(409, "whether people may trust a tile's source is set by your host")
	}
	f := s.policyFile()
	f.Trust = &allowed
	if err := s.writePolicy(f); err != nil {
		return Policy{}, err
	}
	return s.policy(), nil
}
