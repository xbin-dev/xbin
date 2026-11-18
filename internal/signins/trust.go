package signins

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

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
	// OwnerKind "you" | "person" | "org" | "workspace", OwnerID the
	// account or org id — what xbind enforces, and what a card shows — and
	// OwnerName its display name where it has one (shown beside the id,
	// never instead: a display name is its bearer's to set).
	Owner     string
	OwnerKind string
	OwnerID   string
	OwnerName string
	// Admin: the person is a workspace admin by their STORED role. A
	// workspace-owned tile is every admin's to change — theirs too — so it
	// is not someone else's (NotOwn false); a demotion makes it so.
	Admin bool
	// NotOwn: condition 1 — the tile is owned by someone else.
	NotOwn bool
	// Editors: condition 2 — the other people, not workspace admins, who
	// can change the tile's code: account ids, sorted, all of them. Names
	// are display names by id, for a card to show beside the ids.
	Editors []string
	Names   map[string]string
	// Past: condition 3, the sticky one (past.go, D245) — the others who
	// COULD change the tile's code before and can't now, as far back as
	// xbind's record of this tile identity goes: account ids and
	// "org:<id>", sorted. PastUntil is when the last of them was last seen
	// able to (Unix ms). Someone who had write and lost it may have left a
	// file behind, so the tile asks for trust until the person answers.
	Past      []string
	PastUntil int64
}

// NeedsTrust reports a tile whose source is not only the person's own —
// or was not always: the sign-in goes in, and the person's real home is
// mounted, only once they trust it.
func (s Source) NeedsTrust() bool {
	return s.NotOwn || (CoEditorsNeedTrust && len(s.Editors) > 0) || len(s.Past) > 0
}

// TrustMeans is what a trust answer grants, as the card that asked said
// it. 1 (and absent): the person's saved sign-in is used on the tile. 2,
// since D245: that, and the tile's code can change the person's home — a
// session there mounts their real home. An answer given for less is void
// (VoidMeans): the person is asked again, with the words as they are now.
const TrustMeans = 2

// Basis is what a trust answer is given for, as one token: the tile itself
// (its identity and its path), its owner, whether it is someone else's,
// and everyone who can change it. xbind hands it out with the question (a
// card, a start's preview) and takes it back with the answer: an answer
// for another basis than the tile's now is refused — the person trusts
// what they were shown, not what stands there an hour later.
//
// What the answer MEANS is part of it (TrustMeans): a card drawn by an
// xbind that asked for less — an open page, a stored approval's preview —
// carries another basis, and its answer is refused the same way.
func (s Source) Basis() string {
	h := sha256.New()
	for _, part := range []string{s.ID, s.Tile, s.Owner, strconv.FormatBool(s.NotOwn)} {
		fmt.Fprintf(h, "%d:%s\n", len(part), part)
	}
	if CoEditorsNeedTrust {
		for _, e := range s.Editors {
			fmt.Fprintf(h, "e%d:%s\n", len(e), e)
		}
	}
	for _, p := range s.Past {
		fmt.Fprintf(h, "p%d:%s\n", len(p), p)
	}
	return fmt.Sprintf("tb%d_", TrustMeans) + hex.EncodeToString(h.Sum(nil)[:16])
}

// Trust is one answer: the person trusts this tile's source, as it stood —
// this tile (its identity) at this path, with this owner, changeable by
// these people. It is VOID from the moment xbind sees any of that differ —
// the tile moved or was made again, another owner, someone able to change
// it who couldn't when it was given — and stays void until the person
// answers again: someone who gained write and lost it again may have left
// a file behind. People who can no longer change the tile are dropped
// from Editors when that is seen, so their coming back is someone new.
type Trust struct {
	ID      string   `json:"id"`
	Tile    string   `json:"tile"`
	Name    string   `json:"name,omitempty"`
	Ident   string   `json:"ident"`
	Owner   string   `json:"owner"`
	NotOwn  bool     `json:"notOwn,omitempty"`
	Editors []string `json:"editors,omitempty"`
	At      int64    `json:"at"`
	// Means is what the card said the answer grants (TrustMeans); absent
	// on an answer given before D245, which is void. Past: the others the
	// card named as having been able to change the tile before.
	Means int      `json:"means,omitempty"`
	Past  []string `json:"past,omitempty"`
	// Via: "card" (the tile's sign-in card), "approval" (the trust line of
	// a start the person approved), "plan" (a plan's start line).
	Via string `json:"via,omitempty"`
	// VoidAt, VoidWhy: its basis changed (when xbind saw it, and what) —
	// it answers for nothing any more, and is kept for the person's list.
	VoidAt  int64  `json:"voidAt,omitempty"`
	VoidWhy string `json:"voidWhy,omitempty"`
}

// Void reports an answer whose basis changed.
func (t Trust) Void() bool { return t.VoidAt != 0 }

// Why a basis no longer holds, for the person's list.
const (
	VoidGone   = "the tile is gone, or was moved"
	VoidRemade = "another tile was made at this path since"
	VoidMoved  = "the tile was moved"
	VoidOwner  = "the tile has another owner now"
	VoidEditor = "someone new could change the tile"
	VoidMeans  = "trusting a tile means more now — your sign-in is used there, and its code can change your home — so xbin asks again"
)

// against judges a live answer against src, the tile as it stands: why it
// no longer holds ("" = it does), and — when it does — the editors still
// there (fewer than recorded: the record shrinks to them).
func (t Trust) against(src Source) (why string, still []string) {
	switch {
	case t.Means < TrustMeans:
		return VoidMeans, nil // given for less than a trust is now (D245)
	case src.ID == "" || t.Ident != src.ID && t.Tile == src.Tile:
		return VoidRemade, nil
	case t.Ident != src.ID:
		return VoidGone, nil
	case t.Tile != src.Tile:
		return VoidMoved, nil
	case t.Owner != src.Owner || t.NotOwn != src.NotOwn:
		return VoidOwner, nil
	}
	if !CoEditorsNeedTrust {
		return "", t.Editors
	}
	for _, e := range src.Editors {
		if !slices.Contains(t.Editors, e) {
			return VoidEditor, nil
		}
	}
	return "", src.Editors
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
	// ErrTrustChanged: the answer was for the tile as it was shown, and it
	// is no longer that.
	ErrTrustChanged = &Error{Status: 409, Msg: "this tile changed since you were asked — who owns it or who can change it: look again"}
)

// TrustTile records that user trusts src's source as it stands now. basis
// is the token the person's question carried (Source.Basis): an answer for
// another basis than src's is ErrTrustChanged — nothing is recorded.
func (s *Store) TrustTile(user string, src Source, via, basis string) (Trust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pol := s.policy()
	// (the switch on SAVED SIGN-INS does not gate this: a trust also decides
	// which home the person's sessions mount on the tile — term/home.go,
	// D245 — whether or not anyone may save a sign-in)
	switch {
	case !pol.Trust.Allowed:
		return Trust{}, ErrTrustOff
	case src.ID == "" || src.Tile == "":
		return Trust{}, ErrNoIdentity
	case basis != src.Basis():
		return Trust{}, ErrTrustChanged
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
	t := Trust{ID: "tr_" + hex.EncodeToString(id[:]), Tile: src.Tile, Name: src.Name, Ident: src.ID, Owner: src.Owner, NotOwn: src.NotOwn,
		Editors: append([]string(nil), src.Editors...), At: s.o.Now().UnixMilli(), Via: via,
		Means: TrustMeans, Past: append([]string(nil), src.Past...)}
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

// Trusts reports whether user's answer covers src as it stands now — and
// voids the answer for good when it does not (the backstop at every
// judgement of the rule, for a change no event announced). With trusting
// switched off nothing is trusted; the answers are kept as they are.
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
	ok, changed := false, false
	for i := range f.Trust {
		t := &f.Trust[i]
		if t.Void() || t.Ident != src.ID && t.Tile != src.Tile {
			continue
		}
		if s.judge(t, src, true) {
			changed = true
		}
		ok = ok || !t.Void()
	}
	if changed {
		if err := s.write(f, nil); err != nil {
			slog.Error("signins: a trusted tile's record could not be updated", "user", user, "err", err)
			return false // never trusted on a record that could not be voided
		}
	}
	return ok
}

// judge holds t — a live answer — to src (found: the tile exists): voided
// when its basis changed, shrunk when people left. It reports a change to
// write.
func (s *Store) judge(t *Trust, src Source, found bool) bool {
	why, still := VoidGone, []string(nil)
	if found {
		why, still = t.against(src)
	}
	if why != "" {
		t.VoidAt, t.VoidWhy = s.o.Now().UnixMilli(), why
		return true
	}
	if len(still) != len(t.Editors) {
		t.Editors = append([]string(nil), still...)
		return true
	}
	return false
}

// Revalidate holds every person's live answers to their tiles as they
// stand now (source: the broker's SigninSource), voiding each whose basis
// changed — called when people, their access, a tile's owner or the tiles
// themselves change, with or without a session running: a person who
// gained write and lost it again is seen while they have it. It answers
// how many it voided.
func (s *Store) Revalidate(source func(user, tile string) (Source, bool)) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, _ := os.ReadDir(s.o.Dir)
	voided := 0
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		raw, err := hex.DecodeString(name)
		if !ok || err != nil {
			continue
		}
		voided += s.recheck(string(raw), source)
	}
	return voided
}

// Recheck is Revalidate for one person (their list reads true).
func (s *Store) Recheck(user string, source func(user, tile string) (Source, bool)) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recheck(user, source)
}

func (s *Store) recheck(user string, source func(user, tile string) (Source, bool)) (voided int) {
	f, err := s.read(user)
	if err != nil || len(f.Trust) == 0 {
		return 0
	}
	changed := false
	for i := range f.Trust {
		t := &f.Trust[i]
		if t.Void() {
			continue
		}
		src, found := source(f.User, t.Tile)
		if s.judge(t, src, found) {
			changed = true
			if t.Void() {
				voided++
				slog.Info("audit", "event", "signin.trust.void", "user", f.User, "tile", t.Tile, "why", t.VoidWhy)
			}
		}
	}
	if changed {
		if err := s.write(f, nil); err != nil {
			slog.Error("signins: a trusted tile's record could not be voided", "user", f.User, "err", err)
		}
	}
	return voided
}

// Trusted is user's answers, newest first — the void ones too.
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

// policyFile is policy.json: the admins' switch on saved sign-ins.
type policyFile struct {
	Enabled *bool `json:"enabled,omitempty"`
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

// TrustPolicyPath is where the admins' switch on trusting is kept: a file
// of its own, policy.trust.json. An xbind from before the switch rewrites
// policy.json with the one key it knows whenever an admin sets that one —
// a ban on trusting kept there would be lost by a downgrade. This file it
// never touches.
func (s *Store) TrustPolicyPath() string { return filepath.Join(s.o.Dir, "policy.trust.json") }

// trustAllowed is the admins' word on trusting (true: nobody said no).
func (s *Store) trustAllowed() bool {
	var f struct {
		Allowed *bool `json:"allowed"`
	}
	if b, err := os.ReadFile(s.TrustPolicyPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f.Allowed == nil || *f.Allowed
}

// SetTrustPolicy writes the admins' switch on trusting. Refused (409)
// while the host holds it.
func (s *Store) SetTrustPolicy(allowed bool) (Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.policy(); cur.Trust.Locked {
		return cur, refuse(409, "whether people may trust a tile's source is set by your host")
	}
	b, _ := json.MarshalIndent(map[string]bool{"allowed": allowed}, "", "  ")
	if err := os.MkdirAll(s.o.Dir, 0o700); err != nil {
		return Policy{}, err
	}
	if err := fsutil.WriteFileAtomic(s.TrustPolicyPath(), append(b, '\n'), 0o600); err != nil {
		return Policy{}, err
	}
	return s.policy(), nil
}
