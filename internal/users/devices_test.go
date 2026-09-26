package users

import (
	"strings"
	"testing"
)

// Devices are store-owned: enrolled through AddDevice only, kept across a
// plain Upsert, persisted, hidden from Public(), and gone with the user.
func TestDevicesStoreOwned(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(User{ID: "ann"}, "password123"); err != nil {
		t.Fatal(err)
	}
	d, err := st.AddDevice("ann", Device{Name: "  Ann's\x07 iPhone  ", Platform: "iOS", PublicKey: "k", Origin: "https://x.example"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.ID, "dev-") || d.Name != "Ann's iPhone" || d.Platform != "ios" || d.Created == 0 {
		t.Fatalf("device: %+v", d)
	}
	if _, err := st.AddDevice("ann", Device{PublicKey: "k"}); err == nil {
		t.Fatal("a device without an origin")
	}
	if _, err := st.AddDevice("ghost", Device{PublicKey: "k", Origin: "o"}); err == nil {
		t.Fatal("device for an unknown user")
	}
	// A users-API update (Upsert) can neither drop nor inject devices.
	if _, err := st.Upsert(User{ID: "ann", Name: "Ann", Devices: []Device{{ID: "forged"}}}, ""); err != nil {
		t.Fatal(err)
	}
	if ds := st.Devices("ann"); len(ds) != 1 || ds[0].ID != d.ID {
		t.Fatalf("after Upsert: %+v", ds)
	}
	if _, err := st.Upsert(User{ID: "neo", Devices: []Device{{ID: "forged"}}}, "password123"); err != nil {
		t.Fatal(err)
	}
	if len(st.Devices("neo")) != 0 {
		t.Fatal("a new account came with devices")
	}
	if u, _ := st.Get("ann"); len(u.Public().Devices) != 0 {
		t.Fatal("Public() leaks device keys")
	}
	if err := st.TouchDevice(d.ID, "10.1.2.3"); err != nil {
		t.Fatal(err)
	}
	// Persisted, and reloaded intact.
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	uid, got, ok := st2.FindDevice(d.ID)
	if !ok || uid != "ann" || got.LastIP != "10.1.2.3" || got.LastUsed == 0 || got.Origin != "https://x.example" {
		t.Fatalf("reloaded: %q %+v %v", uid, got, ok)
	}
	// Disabled accounts can't enroll.
	u, _ := st2.Get("ann")
	u.Disabled = true
	if _, err := st2.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.AddDevice("ann", Device{PublicKey: "k", Origin: "o"}); err == nil {
		t.Fatal("disabled account enrolled a device")
	}
	if owner, ok, err := st2.RemoveDevice(d.ID); err != nil || !ok || owner != "ann" {
		t.Fatalf("remove: %q %v %v", owner, ok, err)
	}
	if _, ok, _ := st2.RemoveDevice(d.ID); ok {
		t.Fatal("removed twice")
	}
	// The cap.
	for i := 0; i < MaxDevicesPerUser; i++ {
		if _, err := st2.AddDevice("neo", Device{PublicKey: "k", Origin: "o"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st2.AddDevice("neo", Device{PublicKey: "k", Origin: "o"}); err == nil {
		t.Fatal("device cap not enforced")
	}
	// Deleting the user takes the devices with it.
	d2 := st2.Devices("neo")[0]
	if _, err := st2.Delete("neo"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := st2.FindDevice(d2.ID); ok {
		t.Fatal("device outlived its user")
	}
}
