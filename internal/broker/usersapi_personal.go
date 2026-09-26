package broker

import (
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// personalBody is the personal-plane part of a POST/PATCH /users body (D88):
// pointers for PATCH presence — absent keeps the current value.
type personalBody struct {
	NoPersonalTiles *bool     `json:"noPersonalTiles"`
	NoTerminal      *bool     `json:"noTerminal"`
	Sets            *[]string `json:"sets"`
	NetSets         *[]string `json:"netSets"`
}

func (pb personalBody) present() bool {
	return pb.NoPersonalTiles != nil || pb.NoTerminal != nil || pb.Sets != nil || pb.NetSets != nil
}

// check validates the set names before anything is written, so a typo on
// POST never leaves a half-provisioned account behind.
func (pb personalBody) check(st *users.Store) error {
	var sets, nets []string
	if pb.Sets != nil {
		sets = *pb.Sets
	}
	if pb.NetSets != nil {
		nets = *pb.NetSets
	}
	return st.CheckSetNames(sets, nets)
}

// patch is the store update. seeded (POST) merges onto what the new-account
// defaults already put on the row: the switches OR'd, the sets unioned — a
// request can add restrictions and sets, never lift the seed's (D52/D88).
func (pb personalBody) patch(seeded *users.User) users.PersonalPatch {
	p := users.PersonalPatch{NoPersonalTiles: pb.NoPersonalTiles, NoTerminal: pb.NoTerminal, Sets: pb.Sets, NetSets: pb.NetSets}
	if seeded == nil {
		return p
	}
	or := func(req *bool, seed bool) *bool {
		if req == nil {
			return nil
		}
		v := *req || seed
		return &v
	}
	merge := func(req *[]string, seed []string) *[]string {
		if req == nil {
			return nil
		}
		v := append(append([]string(nil), seed...), *req...)
		return &v
	}
	p.NoPersonalTiles, p.NoTerminal = or(pb.NoPersonalTiles, seeded.NoPersonalTiles), or(pb.NoTerminal, seeded.NoTerminal)
	p.Sets, p.NetSets = merge(pb.Sets, seeded.Sets), merge(pb.NetSets, seeded.NetSets)
	return p
}

// applyPersonal writes the personal-plane part of a users request after the
// row exists, and — when it switches noTerminal on — ends the user's live
// terminal and agent sessions (the level cap already refuses new ones and
// reattach; this closes the ones already open). A changed personal network
// restarts the user's net tiles (their default egress follows it).
func (b *Broker) applyPersonal(srv *server.Server, st *users.Store, u *users.User, pb personalBody, seeded bool) (*users.User, error) {
	if !pb.present() {
		return u, nil
	}
	var seed *users.User
	if seeded {
		seed = u
	}
	before := *u
	nu, err := st.SetUserPersonal(u.ID, pb.patch(seed))
	if err != nil {
		return u, err
	}
	if nu.NoTerminal && !before.NoTerminal && srv != nil && srv.Term != nil {
		for _, s := range srv.Term.ListFor(term.HomeKey(auth.Principal{UserID: nu.ID}), "", nil) {
			srv.Term.Kill(s.ID)
		}
	}
	if pb.NetSets != nil {
		b.netSetsChanged("", nil, users.OwnerKindUser+":"+nu.ID)
	}
	return nu, nil
}

// userListRow is one GET /users row: the account plus its resolved personal
// plane (non-admins only — admins bypass every gate it feeds).
type userListRow struct {
	users.User
	InvitePending bool            `json:"invitePending,omitempty"`
	Personal      *users.Personal `json:"personal,omitempty"`
	DeviceCount   int             `json:"deviceCount,omitempty"` // enrolled app devices (devicesapi.go)
}
