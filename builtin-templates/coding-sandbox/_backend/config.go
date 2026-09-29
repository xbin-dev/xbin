// config.go — what the tile's operators set (API.md §Config): the backend,
// the images and sizes consumers pick from, the quotas, and the layout every
// sandbox gets (its user, home and working directory).
package main

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Config is the manager's settings (settings "config").
type Config struct {
	// Backend is the substrate: a registered backend's name ("" = "xbin", on
	// xbind's own tile-sandbox runtime). BackendConfig is its own settings.
	Backend       string         `json:"backend,omitempty"`
	BackendConfig map[string]any `json:"backendConfig,omitempty"`
	// Mode is how a new sandbox is isolated: "auto" (or ""): a VM where the
	// substrate offers VMs now, else a namespace; "vm" or "namespace": that
	// one, or no new sandbox while the substrate lacks it — never another.
	// A new manager's is "vm"; one made before that default keeps "auto"
	// (store.config, newManager).
	Mode        string  `json:"mode,omitempty"`
	Images      []Image `json:"images"`
	Sizes       []Size  `json:"sizes"`
	Quotas      Quotas  `json:"quotas"`
	Layout      Layout  `json:"layout"`
	AutoStopMin int     `json:"autoStopMin,omitempty"` // a new sandbox's idle stop, minutes (0 = the substrate's default)
	// Mounts are filesystems every new sandbox gets: resources this tile
	// holds (its own scope's, or granted to it), none by default.
	Mounts []Mount `json:"mounts,omitempty"`
}

// Mount is a filesystem resource of this tile's (Res, "res:<scope>/<name>";
// optionally a clean relative sub-path of it) mounted at At in every new
// sandbox; RO read-only (a reader grant is read-only whatever it says).
type Mount struct {
	Res  string `json:"res"`
	Path string `json:"path,omitempty"`
	At   string `json:"at"`
	RO   bool   `json:"ro,omitempty"`
}

// Image is what a sandbox starts from: the substrate's base, plus a setup
// script run once (as root, in a sandbox of its own) and snapshotted — every
// later sandbox of the image is a clone of that snapshot (API.md §Images).
type Image struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Setup   string   `json:"setup,omitempty"` // "" = the plain base, no build
	Tools   []string `json:"tools,omitempty"` // what hello says it has beyond a POSIX shell
	Default bool     `json:"default,omitempty"`
	// BuildEgress is the setup's network while it builds: none | internet |
	// open ("" = internet where that class is bound, else none).
	BuildEgress string `json:"buildEgress,omitempty"`
	// Harnesses are the coding agents speaking ACP that hello says the image
	// has (hello.images[].harnesses; none: hello says nothing of them).
	Harnesses []Harness `json:"harnesses,omitempty"`
}

// Harness is one of an image's coding agents (docs/sandbox-manager.md
// §hello): its id — claude, codex, gemini, opencode are the ones consumers
// know — and, over what a consumer knows of it, its name, the command that
// speaks ACP and the shell command that signs it in at a terminal.
type Harness struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	Login string   `json:"login,omitempty"`
}

// baseHarnesses are the coding agents xbin's base rootfs installs
// (docker/rootfs.Dockerfile; /etc/xbin-rootfs-tools lists what did), each
// through its ACP adapter as xbind's Agent tab runs it, and a sign-in that
// needs no browser in the sandbox: a code to paste, a device code.
var baseHarnesses = []Harness{
	{ID: "claude", Title: "Claude Code", Argv: []string{"claude-agent-acp"}, Login: "claude /login"},
	{ID: "codex", Title: "Codex", Argv: []string{"codex-acp"}, Login: "codex login --device-auth"},
	{ID: "gemini", Title: "Gemini CLI", Argv: []string{"gemini", "--acp"}, Login: "NO_BROWSER=true gemini"},
	{ID: "opencode", Title: "OpenCode", Argv: []string{"opencode", "acp"}, Login: "opencode auth login"},
}

// Size is a sandbox's resources.
type Size struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	MemMiB  int    `json:"memMiB"`
	VCPUs   int    `json:"vcpus"`
	DiskGiB int    `json:"diskGiB"`
	Default bool   `json:"default,omitempty"`
}

// Layout is where people work in every sandbox: its user (uid/gid), home,
// working directory and login shell. On a substrate that runs everything as
// root (the runtime's users: "root"), the user is root at /root.
type Layout struct {
	Workdir string `json:"workdir"`
	Home    string `json:"home"`
	User    string `json:"user"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
	Shell   string `json:"shell"`
}

// Quota bounds the sandboxes of one consumer or one person; 0 is no limit.
// Memory and vCPUs count running sandboxes, disk every sandbox.
type Quota struct {
	Sandboxes int `json:"sandboxes,omitempty"`
	Running   int `json:"running,omitempty"`
	MemMiB    int `json:"memMiB,omitempty"`
	VCPUs     int `json:"vcpus,omitempty"`
	DiskGiB   int `json:"diskGiB,omitempty"`
}

// Quotas are each consumer's and each person's (a sandbox's owner.user),
// with overrides by consumer path and by user id — an override replaces
// the default whole.
type Quotas struct {
	Consumer  Quota            `json:"consumer"`
	Person    Quota            `json:"person"`
	Consumers map[string]Quota `json:"consumers,omitempty"`
	People    map[string]Quota `json:"people,omitempty"`
}

func (q Quotas) forConsumer(c string) Quota {
	if o, ok := q.Consumers[c]; ok {
		return o
	}
	return q.Consumer
}

func (q Quotas) forPerson(u string) Quota {
	if o, ok := q.People[u]; ok {
		return o
	}
	return q.Person
}

// cloneHarnesses is hs with argvs of their own: store.config decodes a
// saved config into defaultConfig's, and a saved argv must never land in
// baseHarnesses' (a shallow copy's Argv is the same array).
func cloneHarnesses(hs []Harness) []Harness {
	out := slices.Clone(hs)
	for i := range out {
		out[i].Argv = slices.Clone(out[i].Argv)
	}
	return out
}

// baseTools are the commands xbin's base rootfs has beyond a POSIX shell
// (docker/rootfs.Dockerfile; its best-effort installs are listed in
// /etc/xbin-rootfs-tools), the coding agents' own CLIs included. Not bun (a
// bundle built on another CPU may not run it) nor chromium (Playwright's,
// under PLAYWRIGHT_BROWSERS_PATH — not a command).
var baseTools = []string{"git", "go", "node", "npm", "pnpm", "yarn", "python3", "rg", "make", "gcc", "curl", "jq",
	"gh", "gopls", "dlv", "playwright", "claude", "codex", "gemini", "opencode"}

// defaultConfig is a new manager's: VM sandboxes only.
func defaultConfig() Config {
	return Config{
		Mode: "vm",
		Images: []Image{{ID: "base", Title: "Ubuntu with git, Go, Node and Python", Default: true,
			Tools: slices.Clone(baseTools), Harnesses: cloneHarnesses(baseHarnesses)}},
		Sizes: []Size{
			{ID: "small", Title: "Small", MemMiB: 2048, VCPUs: 2, DiskGiB: 20, Default: true},
			{ID: "medium", Title: "Medium", MemMiB: 4096, VCPUs: 4, DiskGiB: 40},
			{ID: "large", Title: "Large", MemMiB: 8192, VCPUs: 8, DiskGiB: 80},
		},
		Layout: Layout{Workdir: "/work", Home: "/home/dev", User: "dev", UID: 1000, GID: 1000, Shell: "/bin/bash"},
	}
}

func (c Config) backendName() string {
	if c.Backend == "" {
		return "xbin"
	}
	return c.Backend
}

var (
	catalogID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	userName  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// validate checks c and fills what is implied (one default image and size).
func (c *Config) validate() error {
	if !isolationWord[c.Mode] && c.Mode != "" && c.Mode != "auto" {
		return fmt.Errorf("mode is auto (a VM where available, else a namespace), vm, namespace — or another backend's own: container, cloud-vm")
	}
	for _, mt := range c.Mounts {
		if err := mt.check(); err != nil {
			return err
		}
	}
	if c.AutoStopMin < 0 || c.AutoStopMin > 1440 {
		return fmt.Errorf("autoStopMin is 0–1440")
	}
	if len(c.Images) == 0 {
		return fmt.Errorf("at least one image")
	}
	seen, defaults := map[string]bool{}, 0
	for i := range c.Images {
		im := &c.Images[i]
		im.Title = strings.TrimSpace(im.Title)
		switch {
		case !catalogID.MatchString(im.ID):
			return fmt.Errorf("image id %q: letters, digits, '.', '_' and '-', 1–32", im.ID)
		case seen[im.ID]:
			return fmt.Errorf("image %q twice", im.ID)
		case len(im.Setup) > 64<<10:
			return fmt.Errorf("image %s: the setup script is over 64 KiB", im.ID)
		case im.BuildEgress != "" && im.BuildEgress != "none" && im.BuildEgress != "internet" && im.BuildEgress != "open":
			return fmt.Errorf("image %s: buildEgress is none, internet or open", im.ID)
		}
		if err := checkHarnesses(im); err != nil {
			return err
		}
		if im.Title == "" {
			im.Title = im.ID
		}
		seen[im.ID] = true
		if im.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("one default image")
	}
	if defaults == 0 {
		c.Images[0].Default = true
	}
	if len(c.Sizes) == 0 {
		return fmt.Errorf("at least one size")
	}
	seen, defaults = map[string]bool{}, 0
	for _, s := range c.Sizes {
		switch {
		case !catalogID.MatchString(s.ID):
			return fmt.Errorf("size id %q: letters, digits, '.', '_' and '-', 1–32", s.ID)
		case seen[s.ID]:
			return fmt.Errorf("size %q twice", s.ID)
		case s.MemMiB < 128 || s.VCPUs < 1 || s.DiskGiB < 1:
			return fmt.Errorf("size %s: memMiB ≥ 128, vcpus ≥ 1, diskGiB ≥ 1", s.ID)
		}
		seen[s.ID] = true
		if s.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("one default size")
	}
	if defaults == 0 {
		c.Sizes[0].Default = true
	}
	l := c.Layout
	for _, p := range []string{l.Workdir, l.Home, l.Shell} {
		if !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" {
			return fmt.Errorf("layout: %q is not a clean absolute path (workdir, home and shell are)", p)
		}
	}
	if !userName.MatchString(l.User) || l.UID < 0 || l.GID < 0 {
		return fmt.Errorf("layout: user is a login name, uid and gid are 0 or more")
	}
	for _, q := range quotaList(c.Quotas) {
		if q.Sandboxes < 0 || q.Running < 0 || q.MemMiB < 0 || q.VCPUs < 0 || q.DiskGiB < 0 {
			return fmt.Errorf("quotas are 0 (no limit) or more")
		}
	}
	return nil
}

// checkHarnesses: each of an image's harnesses has an id (the catalog's
// grammar), once; an argv of at most 32 non-empty words; a one-line login;
// a title of at most 64 characters (both trimmed).
func checkHarnesses(im *Image) error {
	seen := map[string]bool{}
	for i := range im.Harnesses {
		h := &im.Harnesses[i]
		h.Title, h.Login = strings.TrimSpace(h.Title), strings.TrimSpace(h.Login)
		switch {
		case !catalogID.MatchString(h.ID):
			return fmt.Errorf("image %s: harness id %q: letters, digits, '.', '_' and '-', 1–32", im.ID, h.ID)
		case seen[h.ID]:
			return fmt.Errorf("image %s: harness %s twice", im.ID, h.ID)
		case len(h.Title) > 64 || len(h.Login) > 1024 || strings.ContainsAny(h.Title+h.Login, "\x00\n\r"):
			return fmt.Errorf("image %s: harness %s: a title of at most 64 characters and a login of one line", im.ID, h.ID)
		case len(h.Argv) > 32 || slices.ContainsFunc(h.Argv, func(a string) bool { return a == "" || len(a) > 1024 || strings.ContainsRune(a, 0) }):
			return fmt.Errorf("image %s: harness %s: argv is at most 32 non-empty words", im.ID, h.ID)
		}
		seen[h.ID] = true
	}
	return nil
}

// reservedMount are where no mount goes (the runtime's own places, too).
var reservedMount = []string{"/proc", "/sys", "/dev", "/run/xbin", "/opt/xbin"}

func (mt Mount) check() error {
	if !strings.HasPrefix(mt.Res, "res:") || strings.TrimPrefix(mt.Res, "res:") == "" {
		return fmt.Errorf("mount %q: res is a filesystem resource this tile holds, res:<scope>/<name>", mt.Res)
	}
	if mt.Path != "" && (strings.HasPrefix(mt.Path, "/") || path.Clean(mt.Path) != mt.Path || mt.Path == ".." || strings.HasPrefix(mt.Path, "../")) {
		return fmt.Errorf("mount %s: path is a clean relative path inside the resource", mt.Res)
	}
	if !strings.HasPrefix(mt.At, "/") || path.Clean(mt.At) != mt.At || mt.At == "/" {
		return fmt.Errorf("mount %s: at is a clean absolute path, not /", mt.Res)
	}
	for _, r := range reservedMount {
		if mt.At == r || strings.HasPrefix(mt.At, r+"/") {
			return fmt.Errorf("mount %s: nothing mounts under %s", mt.Res, r)
		}
	}
	return nil
}

// sandboxMounts are the mounts a new sandbox gets (nil: none).
func (c Config) sandboxMounts() []xbin.SandboxMount {
	var out []xbin.SandboxMount
	for _, mt := range c.Mounts {
		out = append(out, xbin.SandboxMount{Res: mt.Res, Path: mt.Path, At: mt.At, RO: mt.RO})
	}
	return out
}

// isolationWord: the contract's words for what keeps a sandbox from its host
// (docs/sandbox-manager.md), which a backend's modes are named by.
var isolationWord = map[string]bool{"vm": true, "namespace": true, "container": true, "cloud-vm": true}

// autoMode: the operators leave the mode to the substrate's offer.
func (c Config) autoMode() bool { return c.Mode == "" || c.Mode == "auto" }

func quotaList(q Quotas) []Quota {
	out := []Quota{q.Consumer, q.Person}
	for _, o := range q.Consumers {
		out = append(out, o)
	}
	for _, o := range q.People {
		out = append(out, o)
	}
	return out
}

// merge applies the top-level fields present in a PUT body onto c: each
// replaces the stored one whole (so an older page can't zero a newer field).
func (c Config) merge(body []byte) (Config, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return c, fmt.Errorf("bad body: %v", err)
	}
	out := c
	fields := map[string]any{"backend": &out.Backend, "backendConfig": &out.BackendConfig, "mode": &out.Mode,
		"images": &out.Images, "sizes": &out.Sizes, "quotas": &out.Quotas, "layout": &out.Layout, "autoStopMin": &out.AutoStopMin,
		"mounts": &out.Mounts}
	for k, v := range raw {
		dst, ok := fields[k]
		if !ok {
			continue // unknown fields are ignored
		}
		switch d := dst.(type) { // start from zero: the field is replaced, not merged into
		case *[]Image:
			*d = nil
		case *[]Size:
			*d = nil
		case *Quotas:
			*d = Quotas{}
		case *map[string]any:
			*d = nil
		case *[]Mount:
			*d = nil
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return c, fmt.Errorf("%s: %v", k, err)
		}
	}
	if err := out.validate(); err != nil {
		return c, err
	}
	return out, nil
}

func (c Config) image(id string) (Image, bool) {
	for _, im := range c.Images {
		if im.ID == id || id == "" && im.Default {
			return im, true
		}
	}
	return Image{}, false
}

func (c Config) size(id string) (Size, bool) {
	for _, s := range c.Sizes {
		if s.ID == id || id == "" && s.Default {
			return s, true
		}
	}
	return Size{}, false
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func joinOr(xs []string, none string) string {
	if len(xs) == 0 {
		return none
	}
	return strings.Join(xs, ", ")
}
