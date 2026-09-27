package tilesbx

import (
	"net/http"
	"testing"
)

// Sizes: 0 is the policy default; above a cap is clamped (the answer says
// what applied); negative is invalid. A lowered cap shows at once.
func TestSizes(t *testing.T) {
	e := newEnv(t)
	in := e.create(map[string]any{"name": "big", "mode": "namespace", "memMiB": 99999, "vcpus": 100, "diskGiB": 5000})
	if in.MemMiB != 8192 || in.VCPUs != 8 || in.DiskGiB != 200 {
		t.Fatalf("clamped %+v", in)
	}
	in = e.create(map[string]any{"name": "small", "mode": "namespace", "memMiB": 1, "vcpus": 1, "diskGiB": 1})
	if in.MemMiB != minMemMiB || in.VCPUs != 1 || in.DiskGiB != 1 {
		t.Fatalf("floored %+v", in)
	}
	for _, f := range []string{"memMiB", "vcpus", "diskGiB", "idleStopMin"} {
		e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "neg", "mode": "namespace", f: -1}), http.StatusBadRequest, RefInvalid)
	}
	e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "idle", "mode": "namespace", "idleStopMin": 1441}), http.StatusBadRequest, RefInvalid)
	e.want(e.do(admin, "PUT", "/sandboxes/policy", `{"perSandbox":{"maxMemMiB":4096}}`), http.StatusOK, "")
	if in := e.info(e.do(mgr, "GET", "/sandboxes/big", nil)); in.MemMiB != 4096 {
		t.Fatalf("under a lowered cap %+v", in)
	}
}

// perTile.max counts definitions; VM disks sum within perTile.diskGiB.
func TestPerTileLimits(t *testing.T) {
	e := newEnv(t)
	e.want(e.do(admin, "PUT", "/sandboxes/policy", `{"perTile":{"max":2,"diskGiB":50}}`), http.StatusOK, "")
	e.create(map[string]any{"name": "vm-1", "mode": "vm", "diskGiB": 30})
	e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "vm-2", "mode": "vm", "diskGiB": 30}), http.StatusTooManyRequests, RefLimit)
	e.create(map[string]any{"name": "vm-2", "mode": "vm", "diskGiB": 20})
	e.want(e.do(mgr, "POST", "/sandboxes", ns("ns-3")), http.StatusTooManyRequests, RefLimit)
	// Growing a VM disk past the tile's is refused; shrinking one is invalid.
	e.want(e.do(mgr, "PATCH", "/sandboxes/vm-2", map[string]any{"diskGiB": 21}), http.StatusTooManyRequests, RefLimit)
	e.want(e.do(mgr, "PATCH", "/sandboxes/vm-2", map[string]any{"diskGiB": 10}), http.StatusBadRequest, RefInvalid)
}

func TestMounts(t *testing.T) {
	e := newEnv(t)
	mk := func(name string, mounts ...map[string]any) map[string]any {
		return map[string]any{"name": name, "mode": "namespace", "mounts": mounts}
	}
	in := e.create(mk("ok",
		map[string]any{"res": "res:apps/mgr/work", "path": "shared/a", "at": "/mnt/shared"},
		map[string]any{"res": "res:apps/mgr/ro", "at": "/mnt/ro"},
		map[string]any{"source": true, "at": "/opt/manager"}))
	if len(in.Mounts) != 3 || in.Mounts[0].Path != "shared/a" || in.Mounts[0].RO || !in.Mounts[2].RO {
		t.Fatalf("mounts %+v", in.Mounts)
	}
	for i, bad := range []map[string]any{
		{"res": "res:apps/other/x", "at": "/mnt/x"},              // not held
		{"res": "res:apps/mgr/db", "at": "/mnt/x"},               // sqlite
		{"res": "res:apps/mgr/work", "path": "../x", "at": "/x"}, // escapes
		{"res": "res:apps/mgr/work", "path": "/abs", "at": "/x"},
		{"res": "res:apps/mgr/work", "path": "a//b", "at": "/x"},
		{"res": "res:apps/mgr/work", "at": "/"},
		{"res": "res:apps/mgr/work", "at": "relative"},
		{"res": "res:apps/mgr/work", "at": "/mnt/../etc"},
		{"res": "res:apps/mgr/work", "at": "/proc/x"},
		{"res": "res:apps/mgr/work", "at": "/sys"},
		{"res": "res:apps/mgr/work", "at": "/dev/shm"},
		{"res": "res:apps/mgr/work", "at": "/run/xbin/gateway.sock"},
		{"res": "res:apps/mgr/work", "at": "/opt/xbin/bin"},
		{"res": "res:apps/mgr/work", "at": "/.xbin-vm/run"},
		{"source": true, "path": "x", "at": "/x"},
		{"source": true, "res": "res:apps/mgr/work", "at": "/x"},
		{"at": "/x"},
	} {
		w := e.do(mgr, "POST", "/sandboxes", mk("bad", bad))
		if w.Code != http.StatusBadRequest {
			t.Errorf("mount %d %v: %d %s, want 400", i, bad, w.Code, w.Body)
		}
	}
	e.want(e.do(mgr, "POST", "/sandboxes", mk("dup", map[string]any{"source": true, "at": "/x"}, map[string]any{"res": "res:apps/mgr/work", "at": "/x"})),
		http.StatusBadRequest, RefInvalid)
	// PATCH replaces the list, validated the same way.
	e.want(e.do(mgr, "PATCH", "/sandboxes/ok", map[string]any{"mounts": []map[string]any{{"res": "res:apps/mgr/db", "at": "/db"}}}), http.StatusBadRequest, RefInvalid)
	w := e.do(mgr, "PATCH", "/sandboxes/ok", map[string]any{"mounts": []map[string]any{}})
	e.want(w, http.StatusOK, "")
	if in := e.info(w); len(in.Mounts) != 0 || in.Version != 2 {
		t.Fatalf("mounts cleared %+v", in)
	}
}

func TestEgress(t *testing.T) {
	e := newEnv(t)
	in := e.create(map[string]any{"name": "net", "mode": "namespace", "net": map[string]any{"egress": "class:internet"}})
	if in.Net.Egress != "class:internet" || in.Net.Reach != "internet" {
		t.Fatalf("net %+v", in.Net)
	}
	for _, bad := range []string{"class:lab", "internet", "class:", "inherit"} {
		e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "bad", "mode": "namespace", "net": map[string]any{"egress": bad}}), http.StatusBadRequest, RefInvalid)
	}
	// A class the tile no longer declares: reach none, with a note — and
	// an unrelated PATCH still works.
	e.m.deps.Net = fakeNet{}
	w := e.do(mgr, "PATCH", "/sandboxes/net", map[string]any{"labels": map[string]string{"a": "b"}})
	e.want(w, http.StatusOK, "")
	if in := e.info(w); in.Net.Reach != "none" || in.Net.Note == "" {
		t.Fatalf("undeclared class %+v", in.Net)
	}
	e.want(e.do(mgr, "PATCH", "/sandboxes/net", map[string]any{"net": map[string]any{"egress": "class:internet"}}), http.StatusBadRequest, RefInvalid)
	w = e.do(mgr, "PATCH", "/sandboxes/net", map[string]any{"net": map[string]any{"egress": ""}})
	if in := e.info(w); in.Net.Egress != "none" {
		t.Fatalf("egress reset %+v", in.Net)
	}
}

// No xbin identity: XBIN_* variables are refused; ids must be runnable.
func TestDefaults(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.UIDRange = false })
	for _, env := range []map[string]string{{"XBIN_TOKEN": "x"}, {"xbin_url": "x"}, {"A=B": "x"}, {"": "x"}, {"A": "a\x00b"}} {
		e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "env", "mode": "namespace", "defaults": map[string]any{"env": env}}),
			http.StatusBadRequest, RefInvalid)
	}
	for _, d := range []map[string]any{{"cwd": "work"}, {"cwd": "/work/../etc"}, {"shell": "bash"}, {"uid": 1000}, {"gid": 5}} {
		e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "d", "mode": "namespace", "defaults": d}), http.StatusBadRequest, RefInvalid)
	}
	in := e.create(map[string]any{"name": "root", "mode": "namespace", "defaults": map[string]any{"uid": 0, "gid": 0, "cwd": "/work"}})
	if in.Users != "root" || *in.Defaults.UID != 0 {
		t.Fatalf("root %+v", in)
	}
	// VM mode runs as anyone, and a range-mapped namespace host too.
	in = e.create(map[string]any{"name": "vm", "mode": "vm", "defaults": map[string]any{"uid": 1000, "gid": 1000}})
	if in.Users != "any" {
		t.Fatalf("vm users %q", in.Users)
	}
	e2 := newEnv(t)
	e2.create(map[string]any{"name": "dev", "mode": "namespace", "defaults": map[string]any{"uid": 1000, "gid": 1000}})
	e2.want(e2.do(mgr, "POST", "/sandboxes", map[string]any{"name": "far", "mode": "namespace", "defaults": map[string]any{"uid": 70000}}), http.StatusBadRequest, RefInvalid)
	// Claims and labels are bounded.
	long := string(make([]byte, 129))
	for _, b := range []map[string]any{{"for": long}, {"forUser": "a\nb"}, {"labels": map[string]string{"k": string(make([]byte, 1024))}}, {"labels": map[string]string{"": "v"}}} {
		b["name"], b["mode"] = "claim", "namespace"
		e2.want(e2.do(mgr, "POST", "/sandboxes", b), http.StatusBadRequest, RefInvalid)
	}
	for _, mode := range []string{"", "container", "VM"} {
		e2.want(e2.do(mgr, "POST", "/sandboxes", map[string]any{"name": "m", "mode": mode}), http.StatusBadRequest, RefInvalid)
	}
}

func TestPatch(t *testing.T) {
	e := newEnv(t)
	e.create(map[string]any{"name": "sb-1", "mode": "vm", "memMiB": 1024, "labels": map[string]string{"a": "1"}})
	e.want(e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"name": "sb-2"}), http.StatusBadRequest, RefInvalid)
	e.want(e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"mode": "namespace"}), http.StatusBadRequest, RefInvalid)
	e.want(e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"from": map[string]any{"sandbox": "x"}}), http.StatusBadRequest, RefInvalid)
	e.want(e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"version": 7, "memMiB": 2048}), http.StatusPreconditionFailed, RefPrecondition)
	// The same values change nothing — the version stays.
	w := e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"name": "sb-1", "mode": "vm", "memMiB": 1024, "labels": map[string]string{"a": "1"}, "version": 1})
	e.want(w, http.StatusOK, "")
	if in := e.info(w); in.Version != 1 {
		t.Fatalf("no-op patch bumped %+v", in)
	}
	w = e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"memMiB": 4096, "vcpus": 4, "diskGiB": 40, "autoStart": false, "version": 1})
	e.want(w, http.StatusOK, "")
	if in := e.info(w); in.Version != 2 || in.MemMiB != 4096 || in.VCPUs != 4 || in.DiskGiB != 40 || in.AutoStart || in.Labels["a"] != "1" {
		t.Fatalf("patched %+v", in)
	}
	// memMiB 0 goes back to the default.
	if in := e.info(e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"memMiB": 0})); in.MemMiB != 2048 {
		t.Fatalf("default %+v", in)
	}
}
