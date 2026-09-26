// Package boot is the xbind daemon: everything cmd/xbind does after parsing
// its command line, as a function of one Config — Run(ctx, cfg). The steps
// a boot performs (auto-init, backfills, the privilege drop, the home
// migration, registry, broker, vault, ingress, isolation, the console) are
// an ordered list (Steps) so their edges are visible and testable, and the
// same code boots a workspace in a test process: no os.Exit, no signal
// handling, an injectable listener.
package boot

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Config is everything the daemon reads at boot: the flag block of
// cmd/xbind, the env-only settings boot consumes, and — documented in the
// same list so docs/config.md stays whole — the env-only settings other
// packages read where they are used (`readBy`). Precedence is flag, then
// env, then the default: a flag's default IS its env value. Struct tags:
//
//	flag   the --name (RegisterFlags); absent = env-only
//	env    the XBIN_* variable
//	def    the default when neither is set
//	doc    the help text (flag -h) and the docs/config.md description
//	readBy the package that reads the variable lazily (not boot)
//	secret never printed
type Config struct {
	Workspace     string `flag:"workspace" env:"XBIN_WORKSPACE" def:"/workspace" doc:"workspace directory"`
	Listen        string `flag:"listen" env:"XBIN_LISTEN" def:"127.0.0.1:8642" doc:"listen address"`
	Dev           bool   `flag:"dev" doc:"dev mode: web/docs served from source tree, debug logs"`
	DevOverlay    string `flag:"dev-overlay" env:"XBIN_DEV_OVERLAY" doc:"dev only (needs --dev): a directory whose files shadow the workspace's on the /c/ static plane — \x60make dev\x60 points it at workspace-template/ so the shell and admin tile are served from source without copying them into the workspace; manifests are never overlaid"`
	NoAuth        bool   `flag:"no-auth" doc:"disable auth (dev only; every request is admin)"`
	ScopeUIDs     bool   `flag:"scope-uids" doc:"run each scope's backends under a dedicated uid (requires root; auth tier 2)"`
	InsecureVault bool   `flag:"insecure-vault" doc:"store secrets AND resource data as PLAINTEXT at rest (not recommended; --no-auth implies it; a bare --dev instead auto-encrypts with a dev key)"`
	Isolate       bool   `flag:"isolate" doc:"run each backend in a per-component sandbox (namespaces + overlay rootfs; auth tier 3, needs --rootfs)"`
	Rootfs        string `flag:"rootfs" env:"XBIN_ROOTFS" doc:"base rootfs dir (unpacked OCI image) for --isolate sandboxes"`
	IngressListen string `flag:"ingress-listen" env:"XBIN_INGRESS_LISTEN" doc:"public ingress HTTP listener (docs/ingress.md; \"\" = off). Serves ONLY published tile routes — never the console"`
	IngressCert   string `flag:"ingress-cert" env:"XBIN_INGRESS_CERT" doc:"TLS certificate (PEM) for the ingress listener (with --ingress-key; reloaded on change)"`
	IngressKey    string `flag:"ingress-key" env:"XBIN_INGRESS_KEY" doc:"TLS key (PEM) for the ingress listener"`
	TrustedProxy  string `flag:"trusted-proxies" env:"XBIN_TRUSTED_PROXIES" doc:"comma-separated IPs/CIDRs of trusted reverse proxies whose X-Forwarded-For is honored (login throttle, session IP attribution, /c/ warm-IP gate). Default: trust nobody. REQUIRED when xbind sits behind a proxy, else all clients key on the proxy's IP"`
	ExternalURL   string `flag:"external-url" env:"XBIN_EXTERNAL_URL" doc:"the console's public base URL, e.g. https://xbin.corp.example — the stable address SSO redirect URIs are registered under (required for SSO login); also used for printed login/invite links. Empty on tunnel-only setups"`
	TileAssets    string `flag:"tile-assets" env:"XBIN_TILE_ASSETS" def:"legacy" doc:"how tile frontends' files are authorized (docs/auth.md §Tile asset gating): legacy = today's credential-less subresource rule (Fetch-Metadata + a recently signed-in IP; removed in the next release); tokens = strict, relative URLs carry a path-scoped asset token; origins = strict, each tile on its own origin under --tiles-domain (needs --external-url, wildcard DNS + TLS)"`
	TilesDomain   string `flag:"tiles-domain" env:"XBIN_TILES_DOMAIN" doc:"parent domain of the per-tile origins for --tile-assets=origins, e.g. tiles.xbin.corp.example (tiles at t-<id>.tiles.xbin.corp.example); must be same-site with --external-url; optional :port. Dev: shell at http://xbin.localhost:PORT, --tiles-domain xbin.localhost"`

	// env-only, consumed by boot
	VaultPassphrase string `env:"XBIN_VAULT_PASSPHRASE" secret:"true" doc:"vault passphrase: auto-init/unseal the encryption barrier at boot (docs/auth.md §vault). Unset in production means the daemon starts SEALED (or LOCKED before first setup) until an admin unseals"`
	LimitMem        string `env:"XBIN_LIMIT_MEM" def:"2G" doc:"per-component cgroup v2 memory cap — plain bytes or a K/M/G/T suffix; active only when xbind's cgroup is delegated (systemd Delegate=yes / a container)"`
	Bin             string `env:"XBIN_BIN" doc:"directory holding the bx CLI, put on terminals' PATH; default: next to the xbind binary, the repo's bin/ under --dev, then whatever is already on xbind's PATH"`

	// env-only, read by the package that uses it (listed here so the
	// configuration reference is complete)
	SDKPath       string `env:"XBIN_SDK_PATH" readBy:"internal/deps" doc:"the xbin Go SDK checkout for the generated go.work and the sandbox bind (default: /opt/xbin/sdk, else the repo's sdk/ under --dev)"`
	LimitDisk     string `env:"XBIN_LIMIT_DISK" readBy:"internal/broker" def:"50G" doc:"per-scope disk quota for tile state — plain bytes or a K/M/G/T suffix"`
	Gocryptfs     string `env:"XBIN_GOCRYPTFS" readBy:"internal/resenc" doc:"the gocryptfs binary for encrypted file-backed resources (default: bundled next to xbind, then PATH; none ⇒ those resources stay plaintext)"`
	FuseOverlayfs string `env:"XBIN_FUSE_OVERLAYFS" readBy:"internal/sandbox" doc:"the fuse-overlayfs binary mounting sandbox roots (default: bundled next to xbind, then PATH; none ⇒ the kernel overlay)"`
	SandboxDebug  string `env:"XBIN_SANDBOX_DEBUG" readBy:"internal/sandbox" doc:"set to anything to make the sandbox init log its steps"`
	BuildNet      string `env:"XBIN_BUILD_NET" readBy:"internal/runner" doc:"network of the sandboxed Go build under --isolate (D78): unset = public addresses only; host = the host's network (a GOPROXY or private modules on the LAN)"`
	Firecracker   string `env:"XBIN_FIRECRACKER" readBy:"internal/vm" doc:"the Firecracker binary VM sandboxes run in their namespace jail (default: bundled next to xbind, then PATH; none ⇒ VM sandboxes unavailable)"`
	VMKernel      string `env:"XBIN_VM_KERNEL" readBy:"internal/vm" doc:"the VM sandboxes' guest kernel (default: vmlinux next to xbind)"`
	VMAgent       string `env:"XBIN_VM_AGENT" readBy:"internal/vm" doc:"the guest agent packed as VM sandboxes' initramfs (default: xbin-vmagent next to xbind)"`
	MkfsErofs     string `env:"XBIN_MKFS_EROFS" readBy:"internal/vm" doc:"the static mkfs.erofs that builds the VM guests' read-only rootfs image (default: bundled next to xbind, then PATH)"`
	QEMU          string `env:"XBIN_QEMU" readBy:"internal/vm" doc:"the static qemu-system-x86_64 that emulates VM sandboxes where KVM isn't usable; its boot blobs qemu-bios-microvm.bin and qemu-pvh.bin sit next to it (default: bundled next to xbind; none ⇒ no emulation)"`
	VhostVsock    string `env:"XBIN_VHOST_VSOCK" readBy:"internal/vm" doc:"the static vhost-device-vsock serving an emulated VM's vsock (default: bundled next to xbind)"`
	VMAccel       string `env:"XBIN_VM_ACCEL" readBy:"internal/vm" doc:"how VM sandboxes run: unset = Firecracker on KVM, else emulated when KVM isn't usable; kvm = never emulate; emulate = always (testing)"`

	// Runtime injection — not settings. Version is the build id main
	// resolves; Listener replaces the console listener (tests bind :0 and set
	// Listen to the bound address); Ready runs once the console listener
	// serves; Privileges nil ⇒ the real setuid path; Stdout nil ⇒ os.Stdout.
	Version    string
	Listener   net.Listener
	Ready      func(addr string)
	Privileges Privileges
	Stdout     io.Writer
}

// RegisterFlags declares every `flag`-tagged field on fs, its default being
// the env value when set, else `def` (that is the precedence: flag > env >
// default). Bool flags have no env form.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := f.Tag.Get("flag")
		if name == "" {
			continue
		}
		doc := f.Tag.Get("doc")
		switch f.Type.Kind() {
		case reflect.String:
			fs.StringVar(v.Field(i).Addr().Interface().(*string), name, envOr(f.Tag.Get("env"), f.Tag.Get("def")), doc)
		case reflect.Bool:
			fs.BoolVar(v.Field(i).Addr().Interface().(*bool), name, false, doc)
		}
	}
}

// FromEnv fills the env-only string settings from the environment (or their
// defaults). Flag-tagged fields are left alone: RegisterFlags gave them the
// env value as their default.
func (c *Config) FromEnv() {
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("flag") != "" || f.Tag.Get("env") == "" || f.Type.Kind() != reflect.String {
			continue
		}
		v.Field(i).SetString(envOr(f.Tag.Get("env"), f.Tag.Get("def")))
	}
}

// Setting is one row of the configuration reference (docs/config.md).
type Setting struct {
	Field, Flag, Env, Default, ReadBy, Doc string
	Bool, Secret                           bool
}

// Settings lists every setting in Config order — the source of docs/config.md.
func Settings() []Setting {
	var out []Setting
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("flag") == "" && f.Tag.Get("env") == "" {
			continue
		}
		out = append(out, Setting{
			Field: f.Name, Flag: f.Tag.Get("flag"), Env: f.Tag.Get("env"), Default: f.Tag.Get("def"),
			ReadBy: f.Tag.Get("readBy"), Doc: f.Tag.Get("doc"),
			Bool: f.Type.Kind() == reflect.Bool, Secret: f.Tag.Get("secret") == "true",
		})
	}
	return out
}

// derived is what Validate resolves from the raw settings.
type derived struct {
	ws          string
	trusted     []netip.Prefix
	externalURL string
	overlay     string
}

// Validate checks the settings the way main always did — the same messages,
// as errors instead of exits — and resolves the derived values.
func (c *Config) Validate() (derived, error) {
	var d derived
	trusted, err := ParseTrustedProxies(c.TrustedProxy)
	if err != nil {
		return d, fmt.Errorf("bad --trusted-proxies: %w", err)
	}
	d.trusted = trusted
	d.externalURL = strings.TrimRight(c.ExternalURL, "/")
	if d.externalURL != "" {
		u, err := url.Parse(d.externalURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
			return d, fmt.Errorf("bad --external-url %q: want http(s)://host[:port] with no path", c.ExternalURL)
		}
	}
	if err := c.validateTileAssets(d.externalURL); err != nil {
		return d, err
	}
	if d.ws, err = filepath.Abs(c.Workspace); err != nil {
		return d, err
	}
	if c.DevOverlay != "" {
		if !c.Dev {
			return d, fmt.Errorf("--dev-overlay needs --dev (it serves unreviewed files over the workspace's)")
		}
		if d.overlay, err = filepath.Abs(c.DevOverlay); err != nil {
			return d, err
		}
		if fi, err := os.Stat(d.overlay); err != nil || !fi.IsDir() {
			return d, fmt.Errorf("--dev-overlay %q is not a directory", d.overlay)
		}
	}
	return d, nil
}

// ParseTrustedProxies parses the --trusted-proxies value: comma-separated
// IPs (→ host prefixes) or CIDRs.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(part); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		return nil, fmt.Errorf("%q is neither an IP nor a CIDR", part)
	}
	return out, nil
}

func envOr(k, def string) string {
	if k == "" {
		return def
	}
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// validateTileAssets checks --tile-assets / --tiles-domain: origins needs
// both a tiles domain and the external URL (scheme and port of the tile
// origins, and the site they must share), and the tiles domain must plausibly
// be same-site with it — a tile cookie on another site is third-party in the
// embedding shell and browsers drop it. (A real same-site check needs the
// public-suffix list; this refuses the shapes that are certainly wrong.)
func (c *Config) validateTileAssets(external string) error {
	switch c.TileAssets {
	case "", "legacy", "tokens", "origins":
	default:
		return fmt.Errorf("bad --tile-assets %q: want legacy, tokens or origins", c.TileAssets)
	}
	td := c.tilesDomain()
	if c.TileAssets != "origins" {
		if td != "" {
			return fmt.Errorf("--tiles-domain is only used with --tile-assets=origins")
		}
		return nil
	}
	if td == "" || external == "" {
		return fmt.Errorf("--tile-assets=origins needs --tiles-domain and --external-url (the tile origins share the console's site, scheme and port)")
	}
	host := td
	if h, _, err := net.SplitHostPort(td); err == nil {
		host = h
	}
	if strings.ContainsAny(host, "/:*@ ") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || !strings.Contains(host, ".") && host != "localhost" {
		return fmt.Errorf("bad --tiles-domain %q: want a hostname like tiles.xbin.example.com (optionally :port), no scheme or wildcard", c.TilesDomain)
	}
	u, _ := url.Parse(external)
	ext := strings.ToLower(u.Hostname())
	if net.ParseIP(ext) != nil || !strings.Contains(ext, ".") {
		return fmt.Errorf("--tile-assets=origins needs --external-url on a hostname with a parent domain (not %q): tile origins must be same-site subdomains — dev: http://xbin.localhost:PORT", ext)
	}
	parent := ext[strings.IndexByte(ext, '.')+1:]
	if host != ext && !strings.HasSuffix(host, "."+ext) &&
		!(strings.HasSuffix(host, "."+parent) && strings.Count(host, ".") >= strings.Count(ext, ".")) {
		return fmt.Errorf("--tiles-domain %q is not same-site with --external-url %q: use a subdomain of %s (e.g. tiles.%s)", host, external, parent, ext)
	}
	return nil
}

// tilesDomain is --tiles-domain normalized (lowercase, trimmed).
func (c *Config) tilesDomain() string { return strings.ToLower(strings.TrimSpace(c.TilesDomain)) }
