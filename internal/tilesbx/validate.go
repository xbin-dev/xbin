package tilesbx

// validate.go — what a create or a PATCH may say (plans/tile-sandbox-runtime.md
// §3.3, §8.2). Everything a manager sends is checked here before it is
// stored; the lifecycle checks it again against the tile's reach at every
// start.

import (
	"fmt"
	"math"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// Modes a sandbox runs in.
const (
	ModeNamespace = "namespace"
	ModeVM        = "vm"
)

// CreateRequest is POST /sandboxes's body.
type CreateRequest struct {
	Name        string            `json:"name"`
	Mode        string            `json:"mode"`
	MemMiB      int               `json:"memMiB,omitempty"`
	VCPUs       int               `json:"vcpus,omitempty"`
	DiskGiB     int               `json:"diskGiB,omitempty"`
	Net         *NetDef           `json:"net,omitempty"`
	Mounts      []Mount           `json:"mounts,omitempty"`
	Defaults    Defaults          `json:"defaults"`
	Labels      map[string]string `json:"labels,omitempty"`
	For         string            `json:"for,omitempty"`
	ForUser     string            `json:"forUser,omitempty"`
	IdleStopMin int               `json:"idleStopMin,omitempty"`
	AutoStart   *bool             `json:"autoStart,omitempty"` // nil = true
	ClientID    string            `json:"clientId,omitempty"`
	Start       bool              `json:"start,omitempty"`
	From        *From             `json:"from,omitempty"` // a clone
}

// PatchRequest is PATCH /sandboxes/{name}'s body: a field that is present
// changes. Name, mode and from can't; version guards a lost update.
type PatchRequest struct {
	Name        *string            `json:"name,omitempty"`
	Mode        *string            `json:"mode,omitempty"`
	From        *From              `json:"from,omitempty"`
	MemMiB      *int               `json:"memMiB,omitempty"`
	VCPUs       *int               `json:"vcpus,omitempty"`
	DiskGiB     *int               `json:"diskGiB,omitempty"`
	Net         *NetDef            `json:"net,omitempty"`
	Mounts      *[]Mount           `json:"mounts,omitempty"`
	Defaults    *Defaults          `json:"defaults,omitempty"`
	Labels      *map[string]string `json:"labels,omitempty"`
	For         *string            `json:"for,omitempty"`
	ForUser     *string            `json:"forUser,omitempty"`
	IdleStopMin *int               `json:"idleStopMin,omitempty"`
	AutoStart   *bool              `json:"autoStart,omitempty"`
	Version     *int64             `json:"version,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// reservedNames are fixed route segments under /sandboxes/.
var reservedNames = map[string]bool{"runtime": true, "policy": true, "copy": true}

func validName(n string) error {
	if !nameRE.MatchString(n) {
		return refuse(RefInvalid, "name %q must match [a-z0-9][a-z0-9-]{0,31}", n)
	}
	if reservedNames[n] {
		return refuse(RefInvalid, "name %q is reserved (runtime, policy and copy are routes)", n)
	}
	return nil
}

// Bounds on what a definition may carry.
const (
	maxMounts   = 16
	maxEnv      = 256
	maxLabels   = 1024 // bytes, keys and values together
	maxClaim    = 128  // for, forUser
	maxClientID = 128
	maxPath     = 4096
)

// masked are where a mount may never land: the kernel's views and xbin's
// own — a VM's plumbing (sandbox.VMDir) too, in either mode, so a
// definition never names a mount no VM could export.
var masked = []string{"/proc", "/sys", "/dev", "/run/xbin", "/opt/xbin", sandbox.VMDir}

// resolveSize: 0 is the default; the result is clamped to [lo, cap] — a
// request above a cap is clamped, never refused (the answer says what applied).
func resolveSize(req, def, lo, cap int) int {
	v := req
	if v == 0 {
		v = def
	}
	return min(max(v, lo), cap)
}

// define validates a create and resolves it into a definition (without
// Created, Version and the clientId bookkeeping).
func (m *Manager) define(k Key, req *CreateRequest, lim Limits) (*Def, error) {
	if err := validName(req.Name); err != nil {
		return nil, err
	}
	if req.From != nil {
		return nil, refuse(RefUnsupported, "clones (from) aren't supported by this xbind yet")
	}
	if _, err := m.modeAvailable(req.Mode); err != nil {
		return nil, err
	}
	for _, v := range []struct {
		name string
		v    int
	}{{"memMiB", req.MemMiB}, {"vcpus", req.VCPUs}, {"diskGiB", req.DiskGiB}, {"idleStopMin", req.IdleStopMin}} {
		if v.v < 0 {
			return nil, refuse(RefInvalid, "%s must not be negative", v.name)
		}
	}
	ps := lim.PerSandbox
	d := &Def{
		Name:        req.Name,
		Mode:        req.Mode,
		MemMiB:      resolveSize(req.MemMiB, ps.MemMiB, minMemMiB, ps.MaxMemMiB),
		VCPUs:       resolveSize(req.VCPUs, ps.VCPUs, 1, ps.MaxVCPUs),
		DiskGiB:     resolveSize(req.DiskGiB, ps.DiskGiB, 1, ps.MaxDiskGiB),
		Net:         NetDef{Egress: "none"},
		Defaults:    req.Defaults,
		Labels:      req.Labels,
		For:         req.For,
		ForUser:     req.ForUser,
		IdleStopMin: req.IdleStopMin,
		AutoStart:   req.AutoStart == nil || *req.AutoStart,
	}
	if req.Net != nil && req.Net.Egress != "" {
		d.Net = *req.Net
	}
	mounts, err := m.checkMounts(k, req.Mounts)
	if err != nil {
		return nil, err
	}
	d.Mounts = mounts
	if err := m.checkRest(k, d); err != nil {
		return nil, err
	}
	if err := checkClientID(req.ClientID); err != nil {
		return nil, err
	}
	return d, nil
}

// amend applies a PATCH to a copy of d. changed reports whether anything did
// (an empty or same-valued PATCH keeps the version).
func (m *Manager) amend(k Key, d *Def, p *PatchRequest, lim Limits) (n *Def, changed bool, err error) {
	switch {
	case p.Name != nil && *p.Name != d.Name:
		return nil, false, refuse(RefInvalid, "a sandbox's name can't change")
	case p.Mode != nil && *p.Mode != d.Mode:
		return nil, false, refuse(RefInvalid, "a sandbox's mode can't change")
	case p.From != nil:
		return nil, false, refuse(RefInvalid, "from is for create")
	case p.Version != nil && *p.Version != d.Version:
		return nil, false, refuse(RefPrecondition, "version %d isn't the current one (%d)", *p.Version, d.Version)
	}
	n = d.clone()
	ps := lim.PerSandbox
	for _, s := range []struct {
		name         string
		req          *int
		dst          *int
		def, lo, cap int
	}{
		{"memMiB", p.MemMiB, &n.MemMiB, ps.MemMiB, minMemMiB, ps.MaxMemMiB},
		{"vcpus", p.VCPUs, &n.VCPUs, ps.VCPUs, 1, ps.MaxVCPUs},
		{"diskGiB", p.DiskGiB, &n.DiskGiB, ps.DiskGiB, 1, ps.MaxDiskGiB},
	} {
		if s.req == nil {
			continue
		}
		if *s.req < 0 {
			return nil, false, refuse(RefInvalid, "%s must not be negative", s.name)
		}
		*s.dst = resolveSize(*s.req, s.def, s.lo, s.cap)
	}
	if n.Mode == ModeVM && n.DiskGiB < d.DiskGiB {
		return nil, false, refuse(RefInvalid, "a VM sandbox's disk only grows (it is %d GiB)", d.DiskGiB)
	}
	fields := []string{"-"} // what the PATCH changes, for checkRest
	if p.Net != nil {
		n.Net, fields = *p.Net, append(fields, "net")
		if n.Net.Egress == "" {
			n.Net.Egress = "none"
		}
	}
	if p.Mounts != nil {
		if n.Mounts, err = m.checkMounts(k, *p.Mounts); err != nil {
			return nil, false, err
		}
	}
	if p.Defaults != nil {
		n.Defaults, fields = *p.Defaults, append(fields, "defaults")
	}
	if p.Labels != nil {
		n.Labels, fields = *p.Labels, append(fields, "labels")
	}
	if p.For != nil {
		n.For, fields = *p.For, append(fields, "for")
	}
	if p.ForUser != nil {
		n.ForUser, fields = *p.ForUser, append(fields, "forUser")
	}
	if p.IdleStopMin != nil {
		if *p.IdleStopMin < 0 {
			return nil, false, refuse(RefInvalid, "idleStopMin must not be negative")
		}
		n.IdleStopMin, fields = *p.IdleStopMin, append(fields, "idleStopMin")
	}
	if p.AutoStart != nil {
		n.AutoStart = *p.AutoStart
	}
	if err := m.checkRest(k, n, fields...); err != nil {
		return nil, false, err
	}
	if reflect.DeepEqual(n, d) {
		return d, false, nil
	}
	n.Version++
	return n, true, nil
}

// checkRest validates what create and PATCH share past the sizes and
// mounts. A PATCH checks only what it changes (fields), so an unrelated edit
// never fails on something the tile's reach lost since; the start checks the
// whole definition again anyway. Empty maps are normalised away (an empty
// PATCH value is no change).
func (m *Manager) checkRest(k Key, d *Def, fields ...string) error {
	if len(d.Labels) == 0 {
		d.Labels = nil
	}
	if len(d.Defaults.Env) == 0 {
		d.Defaults.Env = nil
	}
	check := func(f string) bool { return len(fields) == 0 || slices.Contains(fields, f) }
	if check("net") {
		if err := m.checkEgress(k, d.Net.Egress); err != nil {
			return err
		}
	}
	if check("defaults") {
		if err := m.checkDefaults(d.Mode, d.Defaults); err != nil {
			return err
		}
	}
	if check("labels") {
		n := 0
		for key, v := range d.Labels {
			if key == "" {
				return refuse(RefInvalid, "a label needs a key")
			}
			n += len(key) + len(v)
		}
		if n > maxLabels {
			return refuse(RefInvalid, "labels are %d bytes; at most %d in all", n, maxLabels)
		}
	}
	for _, c := range [][2]string{{"for", d.For}, {"forUser", d.ForUser}} {
		if check(c[0]) && (len(c[1]) > maxClaim || hasControl(c[1])) {
			return refuse(RefInvalid, "%s must be at most %d printable characters", c[0], maxClaim)
		}
	}
	if check("idleStopMin") && d.IdleStopMin > 1440 {
		return refuse(RefInvalid, "idleStopMin must be at most 1440")
	}
	return nil
}

// modeAvailable checks a mode is known and can run here now; for a VM, the
// answer is its acceleration.
func (m *Manager) modeAvailable(mode string) (accel string, err error) {
	switch mode {
	case ModeNamespace:
		if !m.isolated {
			return "", refuse(RefInvalid, "namespace mode is unavailable: tile sandboxes need isolation (xbind --isolate)")
		}
		return "", nil
	case ModeVM:
		accel, reason := m.vmMode()
		if reason != "" {
			return "", refuse(RefInvalid, "VM mode is unavailable: %s", reason)
		}
		return accel, nil
	case "":
		return "", refuse(RefInvalid, `mode is required: "namespace" or "vm"`)
	}
	return "", refuse(RefInvalid, `mode %q isn't "namespace" or "vm"`, mode)
}

// vmMode is VM mode's acceleration now, or why it can't run.
func (m *Manager) vmMode() (accel, reason string) {
	switch {
	case !m.isolated:
		return "", "tile sandboxes need isolation (xbind --isolate)"
	case m.deps.Modes == nil:
		return "", "this xbind can't run VM tile sandboxes"
	}
	return m.deps.Modes.VM()
}

// users is who commands in a sandbox of mode may run as: "any", or "root"
// on a namespace host that maps a single uid.
func (m *Manager) users(mode string) string {
	if mode == ModeNamespace && !m.uidRange {
		return "root"
	}
	return "any"
}

// classes are the tile's sandbox-net classes now.
func (m *Manager) classes(k Key) []EgressClass {
	if m.deps.Net == nil {
		return nil
	}
	return m.deps.Net.Classes(k.Tile)
}

func (m *Manager) checkEgress(k Key, egress string) error {
	if egress == "none" {
		return nil
	}
	slot, ok := strings.CutPrefix(egress, "class:")
	if !ok || slot == "" {
		return refuse(RefInvalid, `net.egress is "none" or "class:<slot>" (a sandbox-net slot of the tile)`)
	}
	for _, c := range m.classes(k) {
		if c.Class == egress {
			return nil
		}
	}
	return refuse(RefInvalid, "the tile declares no sandbox-net slot %q (interfaces: {%q: {kind: \"sandbox-net\"}})", slot, slot)
}

// checkMounts validates mounts (§5) and returns them as stored: a source
// mount is always read-only. A resource must be a filesystem resource the
// tile holds now; its role is applied at start (a reader's is read-only).
func (m *Manager) checkMounts(k Key, ms []Mount) ([]Mount, error) {
	if len(ms) > maxMounts {
		return nil, refuse(RefInvalid, "at most %d mounts", maxMounts)
	}
	out := make([]Mount, 0, len(ms))
	seen := map[string]bool{}
	for i, mt := range ms {
		switch {
		case mt.Source && mt.Res != "":
			return nil, refuse(RefInvalid, "mounts[%d]: a mount is a res or the tile's source, not both", i)
		case mt.Source:
			if mt.Path != "" {
				return nil, refuse(RefInvalid, "mounts[%d]: a source mount takes no path", i)
			}
			mt.RO = true
		case mt.Res == "":
			return nil, refuse(RefInvalid, "mounts[%d]: name a res (a filesystem resource) or source: true", i)
		default:
			if err := checkSubPath(mt.Path); err != nil {
				return nil, refuse(RefInvalid, "mounts[%d].path: %v", i, err)
			}
			if _, err := m.ResourceMount(k, mt.Res); err != nil {
				return nil, refuse(RefInvalid, "mounts[%d]: %v", i, err)
			}
		}
		if err := checkAt(mt.At); err != nil {
			return nil, refuse(RefInvalid, "mounts[%d].at: %v", i, err)
		}
		if seen[mt.At] {
			return nil, refuse(RefInvalid, "mounts[%d].at: %s is mounted twice", i, mt.At)
		}
		seen[mt.At] = true
		out = append(out, mt)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// checkSubPath: "" (the resource's root) or a clean relative path that
// stays beneath it. The start resolves it beneath the root without
// following symlinks; this is only its grammar.
func checkSubPath(p string) error {
	switch {
	case p == "":
		return nil
	case len(p) > maxPath || strings.ContainsRune(p, 0):
		return fmt.Errorf("not a path")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q must be relative to the resource", p)
	case path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%q must be a clean path beneath the resource", p)
	}
	return nil
}

// checkAt: an absolute, clean path inside the sandbox — not / and nowhere
// under the kernel's views or xbin's own.
func checkAt(at string) error {
	if err := checkAbs(at); err != nil {
		return err
	}
	if at == "/" {
		return fmt.Errorf("a mount can't replace /")
	}
	for _, p := range masked {
		if at == p || strings.HasPrefix(at, p+"/") {
			return fmt.Errorf("%s is under %s", at, p)
		}
	}
	return nil
}

// checkAbs: an absolute, clean path.
func checkAbs(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("an absolute path is required")
	case len(p) > maxPath || strings.ContainsRune(p, 0):
		return fmt.Errorf("not a path")
	case !strings.HasPrefix(p, "/") || path.Clean(p) != p:
		return fmt.Errorf("%q must be absolute and clean", p)
	}
	return nil
}

// checkDefaults validates a sandbox's command defaults for its mode.
func (m *Manager) checkDefaults(mode string, d Defaults) error {
	if d.Cwd != "" {
		if err := checkAbs(d.Cwd); err != nil {
			return refuse(RefInvalid, "defaults.cwd: %v", err)
		}
	}
	if d.Shell != "" {
		if err := checkAbs(d.Shell); err != nil {
			return refuse(RefInvalid, "defaults.shell: %v", err)
		}
	}
	if err := m.checkIDs(mode, d.UID, d.GID, "defaults."); err != nil {
		return err
	}
	return checkEnv(d.Env, "defaults.env")
}

// checkIDs: a uid/gid the sandbox can run as ("users": root allows only 0).
func (m *Manager) checkIDs(mode string, uid, gid *uint32, field string) error {
	for _, id := range []struct {
		name string
		v    *uint32
	}{{"uid", uid}, {"gid", gid}} {
		switch {
		case id.v == nil:
		case m.users(mode) == "root" && *id.v != 0:
			return refuse(RefInvalid, "%s%s: this host maps a single uid, so %s sandboxes run as root (users: root)", field, id.name, mode)
		case mode == ModeNamespace && *id.v > 65535, *id.v == math.MaxUint32:
			return refuse(RefInvalid, "%s%s %d isn't mapped in the sandbox", field, id.name, *id.v)
		}
	}
	return nil
}

// checkEnv: well-formed variables, and none of xbin's: a sandbox never gets
// an xbin identity (§8.3).
func checkEnv(env map[string]string, field string) error {
	if len(env) > maxEnv {
		return refuse(RefInvalid, "%s: at most %d variables", field, maxEnv)
	}
	for k, v := range env {
		switch {
		case k == "" || strings.ContainsAny(k, "=\x00"):
			return refuse(RefInvalid, "%s: %q isn't a variable name", field, k)
		case strings.HasPrefix(strings.ToUpper(k), "XBIN_"):
			return refuse(RefInvalid, "%s: %s — XBIN_* variables are xbind's; a sandbox never gets an xbin identity", field, k)
		case strings.ContainsRune(v, 0):
			return refuse(RefInvalid, "%s: %s's value holds a NUL byte", field, k)
		}
	}
	return nil
}

func checkClientID(id string) error {
	if len(id) > maxClientID {
		return refuse(RefInvalid, "clientId must be at most %d characters", maxClientID)
	}
	for _, r := range id {
		if r <= ' ' || r > '~' {
			return refuse(RefInvalid, "clientId must be printable ASCII without spaces")
		}
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < ' ' || r == 0x7f {
			return true
		}
	}
	return false
}
