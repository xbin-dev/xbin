package main

// sudo_test.go — an image's sudo (API.md §Images, D182): its sandboxes'
// prepare gives the layout's user sudo where the mode allows it (a VM on
// KVM only: not a namespace, not an emulated VM), a clone keeps its
// source's, a changed sudo rebuilds an image with a setup script, an explicit namespace mode refuses it, and the
// automatic mode on a substrate without VMs says so in hello's notes.

import (
	"path/filepath"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

func TestSudo(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	// the fake's "sandbox" is a host directory with no /etc: these images
	// give theirs an account of the layout's uid, which prepare renames
	const passwd = `mkdir -p ../etc && printf 'ubuntu:x:%s:%s::/home/ubuntu:/bin/sh\n' "$(id -u)" "$(id -g)" > ../etc/passwd`
	tm.setConfig(t, func(c *Config) {
		c.Images = append(c.Images, Image{ID: "dev", Setup: passwd, Sudo: true}, Image{ID: "plain", Setup: passwd})
	})
	a := tm.tg.As(t, "apps/sudo-a")
	root := func(sb sandboxcontract.Sandbox) string { return filepath.Dir(sb.Workdir) }
	sudoOf := func(id string) bool {
		tm.m.mu.Lock()
		defer tm.m.mu.Unlock()
		return tm.m.recs[id].Sudo
	}

	var dev sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "dev", "image": "dev"}, 201, &dev)
	if dev.State != "running" || !sudoOf(dev.ID) {
		t.Fatalf("a sandbox of a sudo image: %+v (sudo %v)", dev, sudoOf(dev.ID))
	}
	if got := a.Read(dev.ID, root(dev)+"/etc/sudoers.d/dev"); got != "dev ALL=(ALL:ALL) NOPASSWD: ALL\n" {
		t.Errorf("its sudoers file: %q", got)
	}
	if got := a.Read(dev.ID, root(dev)+"/etc/xbin-vm-devices"); !strings.Contains(got, "0666 /dev/fuse\n") {
		t.Errorf("its device list: %q", got)
	}
	var plain sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "plain", "image": "plain"}, 201, &plain)
	if sudoOf(plain.ID) {
		t.Error("a sandbox of an image without sudo has it")
	}
	if out := a.Sh(plain.ID, "ls ../etc"); strings.Contains(out, "sudoers") || strings.Contains(out, "xbin-vm-devices") {
		t.Errorf("a sandbox of an image without sudo has its files: %q", out)
	}

	// a clone has its source's root, sudo included
	a.Call("POST", "/sandboxes/"+dev.ID+"/stop?wait=30", nil, 200, nil)
	clone := a.Create(map[string]any{"name": "clone", "from": map[string]any{"sandbox": dev.ID}})
	if !sudoOf(clone.ID) || a.Read(clone.ID, root(clone)+"/etc/sudoers.d/dev") == "" {
		t.Errorf("the clone of a sudo sandbox: sudo %v", sudoOf(clone.ID))
	}

	// the build is the script's and the sudo's: without sudo the hash is the
	// script's alone (an existing build stays current), and the sudo build
	// doesn't serve the image without it
	tm.m.mu.Lock()
	built := *tm.m.imgs["dev"]
	tm.m.mu.Unlock()
	if built.SetupHash != setupHash(Image{Setup: passwd, Sudo: true}) || setupHash(Image{Setup: passwd}) != hashOf([]string{passwd}) ||
		setupHash(Image{Setup: passwd}) == built.SetupHash {
		t.Errorf("setup hashes: built %s, with sudo %s, without %s", built.SetupHash, setupHash(Image{Setup: passwd, Sudo: true}), setupHash(Image{Setup: passwd}))
	}
	if tm.m.imageReady(Image{ID: "dev", Setup: passwd}, "vm") {
		t.Error("the sudo build serves the image without sudo")
	}

	// an explicit namespace mode refuses a sudo image; the automatic one, on
	// a substrate without VMs, makes sandboxes without it and says so
	cfg := tm.m.config()
	if _, err := cfg.merge([]byte(`{"mode": "namespace"}`)); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Errorf("the namespace mode with a sudo image: %v", err)
	}
	tm.setConfig(t, func(c *Config) { c.Mode = "auto" })
	tm.fb.mu.Lock()
	tm.fb.Modes = []string{"namespace"}
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	notes := strings.Join(strs(hello(t, a)["notes"]), "\n")
	if !strings.Contains(notes, "dev give their user sudo") || !strings.Contains(notes, "namespace") {
		t.Errorf("hello's notes: %q", notes)
	}
	var ns sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "ns", "image": "dev"}, 201, &ns)
	if ns.Isolation != "namespace" || sudoOf(ns.ID) {
		t.Errorf("a namespace sandbox of the sudo image: %s, sudo %v", ns.Isolation, sudoOf(ns.ID))
	}
	if out := a.Sh(ns.ID, "ls ../etc"); strings.Contains(out, "sudoers") {
		t.Errorf("a namespace sandbox of the sudo image has its grant: %q", out)
	}

	// an emulated VM (QEMU, D90) gets no sudo either: KVM-backed sandboxes
	// only (the owner's ruling at land, D182); hello says so
	tm.setConfig(t, func(c *Config) { c.Mode = "vm" })
	tm.fb.mu.Lock()
	tm.fb.Modes, tm.fb.Accel = []string{"vm"}, "emulate"
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	notes = strings.Join(strs(hello(t, a)["notes"]), "\n")
	if !strings.Contains(notes, "dev give their user sudo") || !strings.Contains(notes, "emulated VM") {
		t.Errorf("hello's notes on an emulated substrate: %q", notes)
	}
	var em sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "em", "image": "dev"}, 201, &em)
	if sudoOf(em.ID) {
		t.Error("an emulated VM sandbox of the sudo image has sudo")
	}
	if !sudoWorks(&xbin.SandboxRuntime{Modes: []xbin.SandboxMode{{Mode: "vm", Accel: "kvm"}}}, "vm") ||
		sudoWorks(&xbin.SandboxRuntime{Modes: []xbin.SandboxMode{{Mode: "vm", Accel: "emulate"}}}, "vm") ||
		sudoWorks(&xbin.SandboxRuntime{Modes: []xbin.SandboxMode{{Mode: "cloud-vm", Accel: "kvm"}}}, "cloud-vm") ||
		sudoWorks(nil, "vm") {
		t.Error("sudoWorks: only vm on kvm")
	}
}
