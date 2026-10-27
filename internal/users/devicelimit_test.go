package users

import (
	"errors"
	"sync"
	"testing"
)

func limitStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ann", "bob"} {
		if _, err := st.Upsert(User{ID: id, Name: id}, "pw-"+id+"-long"); err != nil {
			t.Fatal(err)
		}
	}
	return st, dir
}

func dev(key string) Device {
	return Device{PublicKey: "k", Origin: "https://x.example", TunnelKey: key}
}

func TestDeviceLimitDefaultAndOverride(t *testing.T) {
	st, dir := limitStore(t)
	if st.DeviceLimit() != DefaultDeviceLimit || st.UserDeviceLimit("ann") != 1 {
		t.Fatal("default limit is 1")
	}
	first, err := st.AddDevice("ann", dev(""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AddDevice("ann", dev(""))
	var le *DeviceLimitError
	if !errors.As(err, &le) || le.Count != 1 || le.Limit != 1 {
		t.Fatalf("at the limit: %v", err)
	}
	if err.Error() != "you have 1 device; this workspace allows 1 per person — remove one first (an admin can raise your limit)" {
		t.Fatalf("text: %s", err)
	}
	// replacing at the limit: one step, the old one returned
	nd, removed, err := st.AddDeviceReplacing("ann", dev("tk1"), first.ID)
	if err != nil || removed == nil || removed.ID != first.ID || len(st.Devices("ann")) != 1 || st.Devices("ann")[0].ID != nd.ID {
		t.Fatalf("replace: %v %+v", err, removed)
	}
	if _, _, err := st.AddDeviceReplacing("ann", dev(""), "dev-nope"); err == nil {
		t.Fatal("replaced a device that isn't theirs")
	}
	// another user's device can't be named
	bd, _ := st.AddDevice("bob", dev(""))
	if _, _, err := st.AddDeviceReplacing("ann", dev(""), bd.ID); err == nil {
		t.Fatal("replaced bob's device")
	}
	// an override raises it; the workspace default raises everyone else's
	three := 3
	if _, err := st.SetMaxDevices("ann", &three); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := st.AddDevice("ann", dev("")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddDevice("ann", dev("")); !errors.As(err, &le) || le.Limit != 3 {
		t.Fatalf("override: %v", err)
	}
	// grandfathered: lowering keeps the devices, refuses new ones
	one := 1
	if _, err := st.SetMaxDevices("ann", &one); err != nil {
		t.Fatal(err)
	}
	if len(st.Devices("ann")) != 3 {
		t.Fatal("lowering the limit removed devices")
	}
	if _, err := st.AddDevice("ann", dev("")); !errors.As(err, &le) || le.Count != 3 {
		t.Fatalf("grandfathered: %v", err)
	}
	zero := 0
	if _, err := st.SetMaxDevices("bob", &zero); err != nil || st.UserDeviceLimit("bob") != 0 {
		t.Fatal("override 0")
	}
	for _, bad := range []int{-1, 33} {
		if _, err := st.SetMaxDevices("ann", &bad); err == nil {
			t.Fatalf("override %d accepted", bad)
		}
		if err := st.SetDeviceLimit(bad); err == nil {
			t.Fatalf("default %d accepted", bad)
		}
	}
	if err := st.SetDeviceLimit(0); err == nil {
		t.Fatal("default 0 accepted")
	}
	if err := st.SetDeviceLimit(4); err != nil {
		t.Fatal(err)
	}
	// a users-API update keeps the override; everything survives a reload
	if _, err := st.Upsert(User{ID: "ann", Name: "Ann"}, ""); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st2.DeviceLimit() != 4 || st2.UserDeviceLimit("ann") != 1 || st2.UserDeviceLimit("bob") != 0 {
		t.Fatalf("reload: %d %d %d", st2.DeviceLimit(), st2.UserDeviceLimit("ann"), st2.UserDeviceLimit("bob"))
	}
	u, _ := st2.Get("ann")
	if u.MaxDevices == nil || *u.MaxDevices != 1 {
		t.Fatal("maxDevices lost on reload")
	}
	rows := st2.DeviceLimitRows()
	if len(rows) != 2 || rows[0].User != "ann" || rows[0].Count != 3 || rows[1].User != "bob" {
		t.Fatalf("rows %+v", rows)
	}
	if _, err := st2.SetMaxDevices("ann", nil); err != nil || st2.UserDeviceLimit("ann") != 4 {
		t.Fatal("clearing the override")
	}
}

func TestTunnelKeys(t *testing.T) {
	st, dir := limitStore(t)
	st.SetDeviceLimit(5)
	a, err := st.AddDevice("ann", Device{PublicKey: "k", Origin: "o", TunnelKey: "key-a", Endpoint: "ep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice("bob", dev("key-a")); !errors.Is(err, ErrTunnelKeyTaken) {
		t.Fatalf("duplicate key: %v", err)
	}
	b, _ := st.AddDevice("bob", dev(""))
	if err := st.SetDeviceTunnel(b.ID, "key-a", "ep2"); !errors.Is(err, ErrTunnelKeyTaken) {
		t.Fatalf("upgrade to a taken key: %v", err)
	}
	if err := st.SetDeviceTunnel(b.ID, "key-b", "ep"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDeviceTunnel(a.ID, "key-a", "ep"); err != nil {
		t.Fatal("re-registering its own key")
	}
	st2, _ := Open(dir)
	td := st2.TunnelDevices()
	if len(td) != 2 || td[0].UserID != "ann" || td[0].Device.TunnelKey != "key-a" || td[1].Device.Endpoint != "ep" {
		t.Fatalf("tunnel devices %+v", td)
	}
}

// Concurrent enrollments at a limit of 2: exactly two win.
func TestDeviceLimitRace(t *testing.T) {
	st, _ := limitStore(t)
	st.SetDeviceLimit(2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.AddDevice("ann", dev("")); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 2 || len(st.Devices("ann")) != 2 {
		t.Fatalf("%d enrolled, %d stored", ok, len(st.Devices("ann")))
	}
}
