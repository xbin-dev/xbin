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
