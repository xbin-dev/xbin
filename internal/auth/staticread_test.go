package auth

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// The /c/ read gate for element principals (2026-08-02 re-review finding):
// an element reads its OWN tile always; beyond it the attributed human's
// access decides; owner-driven elements keep owner reach; unattributed
// instance tokens stay self-only. Previously ANY element passed CanReadTile
// for ANY tile — one tile's frame token could read every other tile's
// static files.
func TestElementTileReadGate(t *testing.T) {
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "ana", Role: users.RoleUser,
		Tiles: map[string]string{"apps/mine": users.LevelRead}}, "password"); err != nil {
		t.Fatal(err)
	}
	acc, _ := st.Access("ana")

	frame := Principal{Component: "apps/crm", UserID: "ana", Access: acc, Via: "frame"}
	if !frame.CanReadTile("apps/crm") {
		t.Error("element must read its own tile")
	}
	if !frame.CanReadTile("apps/mine") {
		t.Error("attributed user's readable tile must pass")
	}
	if frame.CanReadTile("apps/warehouse") {
		t.Error("frame token must NOT read tiles the driving user can't")
	}
	if frame.CanTerminalTile("apps/mine") {
		t.Error("elements never hold terminal")
	}

	instance := Principal{Component: "apps/crm", Via: "instance"}
	if !instance.CanReadTile("apps/crm") || instance.CanReadTile("apps/other") {
		t.Error("instance tokens are self-only")
	}

	ownerFrame := Principal{Component: "apps/crm", Via: "frame"} // owner-minted: no user id
	if !ownerFrame.CanReadTile("apps/other") {
		t.Error("owner-driven frames keep owner reach")
	}
}
