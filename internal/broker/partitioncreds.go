package broker

// partitioncreds.go — credentials an admin makes for a person who holds
// partitions (plans/partitions/06 §9; PD-07, S6). Taking over a person's
// account is the one way left for an admin into their partitions (00 §3 G2,
// remaining path 2), so it is never silent:
//
//   - a sign-in link minted for them (POST /users/{id}/invite — an admin's,
//     or an org admin's reset by link), a password set on them or an SSO
//     email bound to them (PATCH /users/{id}) by anyone but themselves is
//     audited, pushed to them ("a sign-in link for your account was created
//     by <admin> at <time>"), and kept as a notice their sockets get (the
//     partitions event, op notice) and /xbin/partitions shows;
//   - with the workspace policy credentialResetConfirm on (PD-55) the new
//     credential is minted HELD: the link answers "waiting for <person> to
//     confirm" when redeemed, a password or email is kept aside while the
//     old one keeps working. The person allows or refuses it from any
//     signed-in session, app or device (POST /partitions/credential-confirm
//     {id, allow}, PersonOnly); with no answer it takes effect 24 hours
//     after the notice (which covers the person who lost every device).
//
// A person without partitions is unaffected, as is a person changing their
// own credentials. What is held lives in the person's document
// (partitionpeople.go), never in users.json.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// credentialHoldFor is how long a held credential waits for its person.
const credentialHoldFor = 24 * time.Hour

// The kinds of credential an admin makes.
const (
	credInvite   = "invite"
	credPassword = "password"
	credEmail    = "email"
	credSSO      = "sso-provider" // the workspace's SSO provider changed (partitioncreds_sso.go)
)

// heldCredential is one credential waiting for its person.
type heldCredential struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	By         string    `json:"by"`
	At         time.Time `json:"at"` // the notice's time; it takes effect credentialHoldFor later
	InviteHash string    `json:"inviteHash,omitempty"`
	PassHash   string    `json:"passHash,omitempty"`
	Email      string    `json:"email,omitempty"`
	Issuer     string    `json:"issuer,omitempty"` // credSSO: the new provider
}

// heldView is a held credential as its person sees it (never a hash).
type heldView struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Until  time.Time `json:"until"`
	Email  string    `json:"email,omitempty"`
	Issuer string    `json:"issuer,omitempty"`
}

func (h heldCredential) view() heldView {
	return heldView{ID: h.ID, Kind: h.Kind, By: h.By, At: h.At, Until: h.At.Add(credentialHoldFor), Email: h.Email, Issuer: h.Issuer}
}

// credentialPerson answers whether target holds partitions (a credential
// made for them is noticed), and their uid.
func (b *Broker) credentialPerson(target string) (string, bool) {
	uid := b.storedPartitionUID(target)
	return uid, uid != "" && b.heldPartitions(target, uid)
}

// credentialBy names who made a credential; "" when it is the person's own
// act.
func credentialBy(p auth.Principal, target string) string {
	switch {
	case p.Component == "" && p.Impersonator == "" && p.UserID == target:
		return ""
	case p.UserID != "" && p.Component == "":
		return p.UserID
	case p.Component != "" && p.UserID != "":
		return p.UserID + " (through " + p.Component + ")"
	case p.Component != "":
		return p.Component
	}
	return "owner"
}

// inviteHeldFor is the invite route's question before it mints a link for
// target: noticed — a partition holder, made by someone else; held — and
// the policy credentialResetConfirm is on, so the link is minted held
// (users.Store.CreateHeldInvite): no xbind redeems it until its person
// allows it.
func (b *Broker) inviteHeldFor(r *http.Request, target string) (noticed, held bool) {
	if _, holder := b.credentialPerson(target); !holder || credentialBy(auth.PrincipalOf(r), target) == "" {
		return false, false
	}
	return true, b.Policies().CredentialResetConfirm
}

// credentialInvite is the invite route's hook, after the link was minted
// (held when inviteHeldFor said so): the notice, and the hold, which out
// (the route's answer) then says. A hold that can't be kept revokes the
// link it would have held (the error: the route answers 500), so a held
// link never waits unnamed.
func (b *Broker) credentialInvite(r *http.Request, target, tok string, held bool, out map[string]any) error {
	by := credentialBy(auth.PrincipalOf(r), target)
	h := heldCredential{ID: newNoticeID(), Kind: credInvite, By: by, At: peopleNow().UTC(), InviteHash: users.InviteHashOf(tok)}
	if held {
		if err := b.holdCredential(target, h); err != nil {
			if _, rerr := b.Users.RevokeInvite(target, h.InviteHash); rerr != nil {
				slog.Error("partitions: a held sign-in link whose hold failed couldn't be revoked", "user", target, "err", rerr)
			}
			return fmt.Errorf("the sign-in link couldn't be held for %s to confirm, so it was revoked: %w", target, err)
		}
		out["held"], out["heldUntil"] = true, h.At.Add(credentialHoldFor)
	}
	b.credentialNotice(target, h, held)
	return nil
}

// credentialChange is PATCH /users/{id}'s hook, before the store's update:
// a new password (*password) or SSO email (nu.Email) made for cur by
// someone else is noticed; with the policy on it is taken out of the update
// and held. The answer, called once the update succeeded, sends the notices
// and marks the response (X-XBin-Credential-Held).
func (b *Broker) credentialChange(w http.ResponseWriter, r *http.Request, cur, nu *users.User, password *string) func() {
	by := credentialBy(auth.PrincipalOf(r), cur.ID)
	email := strings.ToLower(strings.TrimSpace(nu.Email))
	var made []heldCredential
	if *password != "" {
		hash, err := users.HashPassword(*password)
		if err == nil {
			made = append(made, heldCredential{Kind: credPassword, PassHash: hash})
		}
	}
	if email != "" && email != cur.Email {
		made = append(made, heldCredential{Kind: credEmail, Email: email})
	}
	if _, holder := b.credentialPerson(cur.ID); by == "" || !holder || len(made) == 0 {
		return func() {}
	}
	held := b.Policies().CredentialResetConfirm
	if held {
		*password, nu.Email = "", cur.Email // the old ones keep working meanwhile
	}
	return func() {
		var kinds []string
		for _, h := range made {
			h.ID, h.By, h.At = newNoticeID(), by, peopleNow().UTC()
			ok := held
			if held {
				if err := b.holdCredential(cur.ID, h); err != nil {
					slog.Error("partitions: a held credential can't be kept; it is dropped", "user", cur.ID, "kind", h.Kind, "err", err)
					kinds = append(kinds, h.Kind+"-dropped")
					continue
				}
				kinds = append(kinds, h.Kind)
			}
			b.credentialNotice(cur.ID, h, ok)
		}
		if len(kinds) > 0 {
			w.Header().Set("X-XBin-Credential-Held", strings.Join(kinds, ","))
		}
	}
}

// holdCredential keeps h for target; an error when it isn't kept.
func (b *Broker) holdCredential(target string, h heldCredential) error {
	kept := false
	err := b.editPersonDoc(target, func(d *personDoc) bool {
		if h.Kind == credInvite { // a new link replaces the held one: the store keeps one invite
			d.Held = slices.DeleteFunc(d.Held, func(o heldCredential) bool { return o.Kind == credInvite })
		}
		d.Held = append(d.Held, h)
		kept = true
		return true
	})
	if err == nil && !kept {
		err = errors.New(target + "'s notices file names someone else")
	}
	return err
}

// credentialText is what the person is told of h.
func credentialText(h heldCredential, held bool) (title, text string) {
	when := h.At.UTC().Format("2006-01-02 15:04 UTC")
	switch h.Kind {
	case credInvite:
		title, text = "A sign-in link for your account", fmt.Sprintf("A sign-in link for your account was created by %s at %s.", h.By, when)
	case credPassword:
		title, text = "A new password for your account", fmt.Sprintf("A new password for your account was set by %s at %s.", h.By, when)
	case credSSO:
		return ssoCredentialText(h, held, when) // partitioncreds_sso.go
	default:
		title, text = "A sign-in email for your account", fmt.Sprintf("The single sign-on email %s was bound to your account by %s at %s.", h.Email, h.By, when)
	}
	if held {
		text += fmt.Sprintf(" It works only once you allow it, or from %s if you don't answer. Not you? Refuse it.",
			h.At.Add(credentialHoldFor).UTC().Format("2006-01-02 15:04 UTC"))
	}
	return title, text
}

// credentialNotice audits h, pushes it to the person and keeps the notice.
func (b *Broker) credentialNotice(target string, h heldCredential, held bool) {
	slog.Info("audit", "who", h.By, "credential", h.Kind, "for", target, "held", held, "partitions", true)
	title, text := credentialText(h, held)
	n := personNotice{Kind: noticeKindCredit, Text: text}
	if held {
		n.Hold = h.ID
	}
	b.addNotice(target, n)
	b.pushPerson(target, "account.credential", title, text, "xbin/partitions", "credential:"+h.ID)
}

// inviteGate is the users store's gate (SetInviteGate), asked only for a
// link minted held: it redeems once its hold's 24 hours passed (the lapse
// loop then records it); while it waits, and whenever the person's file
// can't say — unreadable, another schema, another person's, no hold naming
// the link — it is refused (fail closed: an admin mints another). It runs
// under the store's lock, so it reads the person's file without the people
// lock (written atomically) and never asks the store.
func (b *Broker) inviteGate(u users.User) error {
	held := &users.InviteHeldError{Person: u.ID}
	hash := strings.TrimPrefix(u.InviteHash, users.HeldInvitePrefix)
	path, ok := b.personDocPath(u.UID)
	if !ok {
		return held
	}
	d, err := readPersonDocAt(path, u.UID)
	if err != nil || d == nil || d.User != u.ID {
		return held
	}
	for _, h := range d.Held {
		if h.Kind == credInvite && h.InviteHash == hash && !peopleNow().Before(h.At.Add(credentialHoldFor)) {
			return nil
		}
	}
	return held
}

// InstallCredentialGate installs the invite gate in the users store (boot).
func (b *Broker) InstallCredentialGate() {
	if b.Users != nil {
		b.Users.SetInviteGate(b.inviteGate)
		b.Users.SetSSOGate(b.ssoGate) // partitioncreds_sso.go
	}
}

// CredentialGateInstalled reports whether the users store asks this
// broker's gate (boot's wiring test).
func (b *Broker) CredentialGateInstalled() bool {
	return b.Users != nil && b.Users.InviteGateInstalled()
}

var credLocks sync.Map // workspace root → *sync.Mutex

// credLock serializes the decisions on held credentials: a person's answer
// and the 24 h rule's lapse never interleave.
func (b *Broker) credLock() *sync.Mutex {
	v, _ := credLocks.LoadOrStore(b.Reg.Root, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// heldCredentialOf is user's held credential id.
func (b *Broker) heldCredentialOf(user, id string) (heldCredential, bool) {
	uid := b.storedPartitionUID(user)
	path, ok := b.personDocPath(uid)
	if !ok {
		return heldCredential{}, false
	}
	d, _ := readPersonDocAt(path, uid)
	if d == nil || d.User != user {
		return heldCredential{}, false
	}
	i := slices.IndexFunc(d.Held, func(h heldCredential) bool { return h.ID == id })
	if i < 0 {
		return heldCredential{}, false
	}
	return d.Held[i], true
}

// takeHeld removes held credential id (every lapsed one when id is "") of
// user from their document under the people lock, and answers them.
func (b *Broker) takeHeld(user, id string, now time.Time) ([]heldCredential, error) {
	var out []heldCredential
	err := b.editPersonDoc(user, func(d *personDoc) bool {
		d.Held = slices.DeleteFunc(d.Held, func(h heldCredential) bool {
			if id == h.ID || id == "" && !now.Before(h.At.Add(credentialHoldFor)) {
				out = append(out, h)
				return true
			}
			return false
		})
		return len(out) > 0
	})
	return out, err
}

// applyHeld makes h take effect (allow), or revokes it (!allow), in the
// users store — outside the people lock. effective false: a link that
// is no longer pending (spent, replaced, expired), so neither happened.
func (b *Broker) applyHeld(user string, h heldCredential, allow bool) (effective bool, err error) {
	switch {
	case h.Kind == credInvite && !allow:
		revoked, err := b.Users.RevokeInvite(user, h.InviteHash)
		return revoked, err
	case h.Kind == credInvite:
		state := b.Users.HeldInviteState(user, h.InviteHash)
		if state == users.InviteGoneState {
			return false, nil
		}
		return true, b.Users.ReleaseInvite(user, h.InviteHash)
	case h.Kind == credSSO:
		return b.applyHeldSSO(user, h, allow) // partitioncreds_sso.go
	case !allow:
		return true, nil // a held password or email was never stored: refusing drops it
	case h.Kind == credPassword:
		return true, b.Users.SetPassHash(user, h.PassHash)
	}
	u, ok := b.Users.Get(user)
	if !ok {
		return false, errors.New("no such user " + user)
	}
	nu := *u
	nu.Email = h.Email
	_, err = b.Users.Upsert(nu, "")
	return true, err
}

// apiCredentialConfirm — POST /partitions/credential-confirm {id, allow}:
// the person allows or refuses a credential an admin made for them. A
// refusal is honoured while the credential is unused — a password or email
// the 24 h rule hasn't stored yet, a link not yet redeemed, even past its 24
// hours. A link that is no longer pending (redeemed, replaced or expired)
// answers 409 decision "already-effective": it may have been used, so the
// person is told to change their password and sign out everywhere.
func (b *Broker) apiCredentialConfirm(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Allow *bool  `json:"allow"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.ID == "" || body.Allow == nil {
		server.WriteError(w, http.StatusBadRequest, "need {id, allow: true|false}", "/docs/protocol.md")
		return
	}
	mu := b.credLock()
	mu.Lock()
	defer mu.Unlock()
	h, ok := b.heldCredentialOf(p.UserID, body.ID)
	if !ok {
		server.WriteError(w, http.StatusNotFound, "no credential "+body.ID+" waits for you: it was decided, or took effect after 24 hours without an answer "+
			"(your notices say which) — if you didn't allow it, change your password and sign out everywhere", "/docs/partitions.md")
		return
	}
	effective, err := b.applyHeld(p.UserID, h, *body.Allow)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, "the credential couldn't be "+map[bool]string{true: "activated", false: "revoked"}[*body.Allow]+": "+err.Error())
		return
	}
	if _, err := b.takeHeld(p.UserID, h.ID, peopleNow()); err != nil {
		slog.Warn("partitions: a decided credential stays listed", "user", p.UserID, "err", err)
	}
	if !effective {
		text := fmt.Sprintf("The %s %s made for your account was no longer waiting when you answered: it was used, replaced or expired. "+
			"If you didn't use it, change your password and sign out everywhere.", credNoun(h.Kind), h.By)
		slog.Info("audit", "who", p.From(), "method", "POST", "path", "/partitions/credential-confirm", "status", http.StatusConflict,
			"credential", h.Kind, "by", h.By, "decision", "already-effective")
		b.addNotice(p.UserID, personNotice{Kind: noticeKindCredit, Text: text})
		server.WriteJSON(w, http.StatusConflict, map[string]any{"error": text, "id": h.ID, "kind": h.Kind, "decision": "already-effective",
			"docs": "/docs/partitions.md"})
		return
	}
	verb := map[bool]string{true: "allowed", false: "refused"}[*body.Allow]
	slog.Info("audit", "who", p.From(), "method", "POST", "path", "/partitions/credential-confirm", "status", http.StatusOK,
		"credential", h.Kind, "by", h.By, "decision", verb)
	b.addNotice(p.UserID, personNotice{Kind: noticeKindCredit, Text: fmt.Sprintf("You %s the %s %s made for your account.", verb, credNoun(h.Kind), h.By)})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": h.ID, "kind": h.Kind, "decision": verb})
}

func credNoun(kind string) string {
	switch kind {
	case credInvite:
		return "sign-in link"
	case credEmail:
		return "sign-in email"
	case credSSO:
		return "single sign-on provider change"
	}
	return "password"
}

// heldOf are user's credentials waiting for them.
func (b *Broker) heldOf(user string) []heldView {
	out := []heldView{}
	uid := b.storedPartitionUID(user)
	path, ok := b.personDocPath(uid)
	if !ok {
		return out
	}
	d, _ := readPersonDocAt(path, uid)
	if d == nil || d.User != user {
		return out
	}
	for _, h := range d.Held {
		out = append(out, h.view())
	}
	return out
}

// LapseHeldCredentials makes every held credential whose wait is over take
// effect (06 §9's 24 h rule): boot runs it every few minutes.
func (b *Broker) LapseHeldCredentials() { b.lapseHeldCredentials(peopleNow()) }

func (b *Broker) lapseHeldCredentials(now time.Time) {
	files, _ := filepath.Glob(filepath.Join(b.Reg.Root, "data", partitionsDir, peopleDir, "*.json")) // walk-ok: xbind's own
	for _, f := range files {
		uid := strings.TrimSuffix(filepath.Base(f), ".json")
		d, err := readPersonDocAt(f, uid)
		if err != nil || d == nil || len(d.Held) == 0 || b.storedPartitionUID(d.User) != uid {
			continue
		}
		b.lapseHeldOf(d.User, d.Held, now)
	}
}

// lapseHeldOf makes user's held credentials whose 24 hours passed take
// effect, each before it leaves their document (a failure keeps it for the
// next pass) — under credLock, so no answer of the person's interleaves.
func (b *Broker) lapseHeldOf(user string, held []heldCredential, now time.Time) {
	mu := b.credLock()
	mu.Lock()
	defer mu.Unlock()
	for _, seen := range held {
		if now.Before(seen.At.Add(credentialHoldFor)) {
			continue
		}
		h, ok := b.heldCredentialOf(user, seen.ID) // still waiting: not answered meanwhile
		if !ok {
			continue
		}
		effective, err := b.applyHeld(user, h, true)
		if err != nil {
			slog.Warn("partitions: a held credential couldn't take effect", "user", user, "kind", h.Kind, "err", err)
			continue
		}
		if _, err := b.takeHeld(user, h.ID, now); err != nil {
			slog.Warn("partitions: a lapsed credential stays listed", "user", user, "err", err)
		}
		slog.Info("audit", "who", h.By, "credential", h.Kind, "for", user, "decision", "took effect: no answer in 24h", "pending", effective)
		b.addNotice(user, personNotice{Kind: noticeKindCredit, Text: fmt.Sprintf("The %s %s made for your account took effect: you didn't answer within 24 hours.", credNoun(h.Kind), h.By)})
	}
}
