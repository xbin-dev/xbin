package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

const testHolder = "xbin/agent"

// delegateFx is an Auth with alice (an admin), bob (a user) and dave
// (disabled), the sealed holder marked, every partition covered and
// delegation on.
func delegateFx(t *testing.T) (*Auth, *users.Store) {
	t.Helper()
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "alice", Role: users.RoleAdmin}, {ID: "bob", Role: users.RoleUser}, {ID: "dave", Role: users.RoleUser, Disabled: true}} {
		if _, err := st.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	t.Cleanup(TestSealDelegate(testHolder))
	a.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })
	a.SetDelegationGate(func(string, string) error { return nil })
	return a, st
}

func delegateReq(scheme, tok string) *http.Request {
	r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
	r.Header.Set("Authorization", scheme+" "+tok)
	return r.WithContext(WithGateway(r.Context()))
}

// A person's partition of a sealed holder resolves, under the delegate
// scheme, to that person's principal — and to nothing anywhere the scheme
// isn't asked for.
func TestDelegatePrincipal(t *testing.T) {
	a, _ := delegateFx(t)
	a.RegisterInstancePartition("tok-bob", testHolder, "", util.UserPartition("bob"), "uid-bob")
	a.RegisterInstancePartition("tok-alice", testHolder, "", util.UserPartition("alice"), "uid-alice")

	p, present, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-bob"))
	if !present || err != nil {
		t.Fatalf("bob's delegate: present %v, err %v", present, err)
	}
	if p.UserID != "bob" || p.User == nil || p.Component != "" || p.Via != "delegate" || p.Agent != testHolder || p.Partition != "" || p.Gen != "" || p.Owner || p.ReadOnly() {
		t.Fatalf("bob's delegate principal: %+v", p)
	}
	if p.From() != "user:bob" {
		t.Fatalf("From %q", p.From())
	}
	// an admin's delegate is the admin's person, never an admin
	p, _, err = a.DelegateFromRequest(delegateReq("xbin-delegate", "tok-alice")) // the scheme is case-insensitive
	if err != nil || p.UserID != "alice" || !p.User.IsAdmin() || p.IsAdmin() {
		t.Fatalf("alice's delegate: %+v err %v (IsAdmin %v)", p, err, p.IsAdmin())
	}
	// the same token as a Bearer is the component itself, in its partition
	c, ok := a.FromRequest(delegateReq("Bearer", "tok-bob"))
	if !ok || c.Component != testHolder || c.Via != "instance" || c.Partition != util.UserPartition("bob") || c.UserID != "" || c.Agent != "" {
		t.Fatalf("the Bearer: %+v ok %v", c, ok)
	}
	// FromRequest doesn't know the scheme: on every plane that doesn't ask
	// DelegateFromRequest the credential is nobody's
	if q, ok := a.FromRequest(delegateReq(DelegateScheme, "tok-bob")); ok {
		t.Fatalf("FromRequest resolved a delegate credential: %+v", q)
	}
	// a request without the scheme isn't a delegate's
	if _, present, _ := a.DelegateFromRequest(delegateReq("Bearer", "tok-bob")); present {
		t.Fatal("a Bearer is no delegate request")
	}
	if IsDelegateRequest(httptest.NewRequest("GET", "/", nil)) || !IsDelegateRequest(delegateReq(DelegateScheme, "x")) {
		t.Fatal("IsDelegateRequest")
	}
}

// Everything that isn't a live person's partition of a sealed holder, with
// delegation on, delegates nothing.
func TestDelegateRefusals(t *testing.T) {
	a, st := delegateFx(t)
	a.RegisterInstancePartition("tok-bob", testHolder, "", util.UserPartition("bob"), "uid-bob")
	a.RegisterInstancePartition("tok-dave", testHolder, "", util.UserPartition("dave"), "uid-dave")
	a.RegisterInstancePartition("tok-global", testHolder, "", util.PartitionGlobal, "")
	a.RegisterInstancePartition("tok-other", "apps/agent", "", util.UserPartition("bob"), "uid-bob") // a workspace tile, partitioned too
	a.RegisterInstance("tok-plain", "apps/x")
	term := a.MintTerminal(testHolder, "bob")
	session := a.NewBearerSessionOpts("bob", BearerOpts{}).Token

	not := func(name, tok string) {
		t.Helper()
		if p, present, err := a.DelegateFromRequest(delegateReq(DelegateScheme, tok)); !present || !errors.Is(err, ErrNotDelegate) {
			t.Errorf("%s: present %v err %v principal %+v, want ErrNotDelegate", name, present, err, p)
		}
	}
	not("an unknown token", "nope")
	not("the holder's global instance", "tok-global")
	not("another partitioned tile's partition (not sealed)", "tok-other")
	not("an unpartitioned tile's instance", "tok-plain")
	not("a disabled person's partition", "tok-dave")
	not("the owner token", a.OwnerTokenValue())
	not("a terminal token on the holder", term)
	not("the person's own app session", session)

	// the off switches, asked on every request: the gate's reason is the 403's
	a.SetDelegationGate(func(c, u string) error { return errors.New(u + " paused their agent") })
	_, _, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-bob"))
	if !errors.Is(err, ErrDelegateOff) || errors.Is(err, ErrNotDelegate) {
		t.Fatalf("paused: %v", err)
	}
	a.SetDelegationGate(nil) // no gate installed: nothing delegates
	if _, _, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-bob")); !errors.Is(err, ErrDelegateOff) {
		t.Fatalf("no gate: %v", err)
	}
	a.SetDelegationGate(func(string, string) error { return nil })
	if _, _, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-bob")); err != nil {
		t.Fatalf("on again: %v", err)
	}

	// the partition stops being covered (the person lost read on the
	// holder, was re-created, the tile's mode changed): at once
	a.SetPartitionCoverage(func(string, util.Partition, string) bool { return false })
	not("an uncovered partition", "tok-bob")
	a.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })

	// disabling the person: at once
	b, _ := st.Get("bob")
	b.Disabled = true
	if _, err := st.Upsert(*b, ""); err != nil {
		t.Fatal(err)
	}
	not("a person disabled since", "tok-bob")

	// the table decides: a holder no longer in it delegates nothing
	b.Disabled = false
	if _, err := st.Upsert(*b, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-bob")); err != nil {
		t.Fatalf("enabled again: %v", err)
	}
	prev := delegateHolders.Load()
	SetDelegateHolders(func(string) bool { return false })
	defer delegateHolders.Store(prev)
	not("a holder no longer in the table", "tok-bob")
}

// The scheme is honoured on the gateway socket alone: on the console
// listener (no mark) and on a device's tunnel the token delegates nothing.
func TestDelegateOnlyOnTheGateway(t *testing.T) {
	a, _ := delegateFx(t)
	a.RegisterInstancePartition("tok-bob", testHolder, "", util.UserPartition("bob"), "uid-bob")
	r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
	r.Header.Set("Authorization", DelegateScheme+" tok-bob")
	if _, present, err := a.DelegateFromRequest(r); !present || !errors.Is(err, ErrNotDelegate) {
		t.Fatalf("off the gateway: present %v err %v", present, err)
	}
	r = delegateReq(DelegateScheme, "tok-bob")
	r = r.WithContext(WithTransport(r.Context(), Transport{Tunnel: true, DeviceID: "d1", UserID: "bob"}))
	if _, _, err := a.DelegateFromRequest(r); !errors.Is(err, ErrNotDelegate) {
		t.Fatalf("over a tunnel: %v", err)
	}
	// with no lookup installed nothing delegates
	prev := delegateHolders.Load()
	SetDelegateHolders(nil)
	defer delegateHolders.Store(prev)
	if _, _, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-bob")); !errors.Is(err, ErrNotDelegate) || HoldsDelegation(testHolder) {
		t.Fatalf("no table: %v", err)
	}
}

// Developing an agent on a single-user xbind (--dev --no-auth): nobody
// signs in and there are no user partitions, so the holder runs as ONE
// global instance — and that instance is the implicit owner's delegate.
// Today delegateOf refuses a global instance outright (the global instance
// is nobody's delegate), which is right while holders are partitioned per
// person only. The case is kept here, as it is meant to hold, for the
// change that brings the sealed-component table in.
func TestDelegateNoAuthGlobalHolder(t *testing.T) {
	a, err := Load(t.TempDir(), true) // --no-auth
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(TestSealDelegate(testHolder))
	a.SetDelegationGate(func(string, string) error { return nil }) // server.DelegationGate(store, isolated, open=true): --dev
	a.RegisterInstancePartition("tok-global", testHolder, "", util.PartitionGlobal, "")

	p, present, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-global"))
	if !present || err != nil {
		t.Fatalf("the global holder under --no-auth: present %v, err %v", present, err)
	}
	// the owner as a person: named, attributed to the holder, never an
	// admin — the cap, tickets and the audit log apply as for anyone
	if p.Via != "delegate" || p.Agent != testHolder || p.Component != "" || p.UserID != "owner" || p.Owner || p.IsAdmin() {
		t.Fatalf("its principal: %+v", p)
	}
	// only the holder's, only on the gateway, and never with sign-in on
	a.RegisterInstancePartition("tok-other", "apps/x", "", util.PartitionGlobal, "")
	if _, _, err := a.DelegateFromRequest(delegateReq(DelegateScheme, "tok-other")); !errors.Is(err, ErrNotDelegate) {
		t.Errorf("another tile's global instance: %v", err)
	}
	r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
	r.Header.Set("Authorization", DelegateScheme+" tok-global")
	if _, _, err := a.DelegateFromRequest(r); err == nil {
		t.Error("off the gateway socket")
	}
	withAuth, _ := delegateFx(t)
	withAuth.RegisterInstancePartition("tok-global", testHolder, "", util.PartitionGlobal, "")
	if _, _, err := withAuth.DelegateFromRequest(delegateReq(DelegateScheme, "tok-global")); !errors.Is(err, ErrNotDelegate) {
		t.Errorf("a global instance where people sign in: %v", err)
	}
}
