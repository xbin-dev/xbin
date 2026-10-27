package registry

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Interfaces (net/sandbox-net slots), exposes and the runtime key: what a
// manifest declares about how the tile is reached and run, and the checks
// that turn a malformed declaration into ManifestErr.

// KindSandboxNet is a request-side interface kind: one class of network a
// sandbox manager's tile sandboxes may use ("class:<slot>"), bound by an
// approver like a net slot but never the tile's own egress
// (plans/tile-sandbox-runtime.md §4).
const KindSandboxNet = "sandbox-net"

// sandboxNetSlot is the grammar of a sandbox-net slot name: the class a
// sandbox names is "class:<slot>".
var sandboxNetSlot = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidSandboxNetSlot reports whether slot may name a sandbox-net class.
func ValidSandboxNetSlot(slot string) bool { return sandboxNetSlot.MatchString(slot) }

// SandboxNetSlotErr says why a request-side sandbox-net slot is no class
// (nil = it is one): a name outside the class grammar, or multi, service,
// role or instances. Load-time validation, the broker's class list and
// binding all judge a slot by it.
func SandboxNetSlotErr(slot string, def Iface) error {
	if !ValidSandboxNetSlot(slot) {
		return fmt.Errorf("interfaces.%s: a sandbox-net slot is named [a-z0-9][a-z0-9_-]{0,31} (sandboxes select it as class:<slot>)", slot)
	}
	if def.Multi || def.Service != "" || def.Role != "" || def.Instances {
		return fmt.Errorf("interfaces.%s: a sandbox-net slot takes no multi, service, role or instances", slot)
	}
	return nil
}

// ValidateInterfaces checks the interface kinds that carry rules of their
// own — today only sandbox-net, which is request-side, single and plain.
// Other kinds keep their bind-time checks. The error names the slot.
func ValidateInterfaces(m Manifest) error {
	for _, slot := range slices.Sorted(maps.Keys(m.Interfaces)) {
		if def := m.Interfaces[slot]; def.Kind == KindSandboxNet {
			if err := SandboxNetSlotErr(slot, def); err != nil {
				return err
			}
		}
	}
	for _, slot := range slices.Sorted(maps.Keys(m.Provides)) {
		if m.Provides[slot].Kind == KindSandboxNet {
			return fmt.Errorf("provides.%s: sandbox-net is request-side only (a manager declares it under interfaces)", slot)
		}
	}
	return nil
}

// Iface declares one interface slot (requested or provided).
type Iface struct {
	Kind    string `json:"kind"`              // net | http | gpu | resource | stream | lan-ingress | sandbox-net | ingress (provide)
	Service string `json:"service,omitempty"` // for kind=http: the service contract (e.g. "openai")
	// Role is which exposed role a kind=http PROVIDER grants bound requesters
	// (so the binding is also the call grant). Defaults to "reader".
	Role string `json:"role,omitempty"`
	// Multi (REQUEST side, kind=http only) declares the slot accepts a SET of
	// bindings — the requester explicitly supports multiple inputs and gets a
	// JSON list injected instead of a single URL (plans/interfaces.md
	// §Multiplicity).
	Multi bool `json:"multi,omitempty"`
	// Instances (PROVIDE side) marks the provide as a template: the provider
	// registers concrete instances at runtime (PUT /api/xbin/iface-instances),
	// each addressable as "<provider>#<instance>" in bindings.
	Instances bool `json:"instances,omitempty"`
}

// ExposeDef is one entry of a manifest "exposes" map: an endpoint the
// component offers to the OUTSIDE world (plans/ingress.md). Declaring it is
// inert — the endpoint becomes reachable only when the owner binds the slot
// to an ingress source (the `runtime` builtin or a terminator tile).
type ExposeDef struct {
	Kind string `json:"kind"` // http | stream
	// Paths (kind=http) is the public path allowlist: exact paths, or
	// subtree patterns ending in "/*" ("/*" publishes everything). Requests
	// outside it are refused at the terminator — default-deny.
	Paths []string `json:"paths,omitempty"`
	// Proto/Port (kind=stream): the L4 protocol (tcp default, or udp) and the
	// in-netns port the backend listens on (ordinary net.Listen).
	Proto string `json:"proto,omitempty"`
	Port  int    `json:"port,omitempty"`
}

// ValidateExposes checks a manifest's exposes section; the error names the
// offending slot (surfaced by bx doctor and refused at bind time).
func ValidateExposes(m Manifest) error {
	for slot, def := range m.Exposes {
		if _, dup := m.Interfaces[slot]; dup {
			return fmt.Errorf("exposes slot %q collides with an interfaces slot of the same name", slot)
		}
		switch def.Kind {
		case "http":
			if len(def.Paths) == 0 {
				return fmt.Errorf("exposes.%s: kind=http needs a non-empty \"paths\" allowlist (use [\"/*\"] to publish everything)", slot)
			}
			for _, p := range def.Paths {
				if !strings.HasPrefix(p, "/") {
					return fmt.Errorf("exposes.%s: path %q must start with /", slot, p)
				}
			}
			if def.Port != 0 || def.Proto != "" {
				return fmt.Errorf("exposes.%s: proto/port are for kind=stream", slot)
			}
		case "stream":
			if def.Port < 1 || def.Port > 65535 {
				return fmt.Errorf("exposes.%s: kind=stream needs \"port\" (1-65535), the in-sandbox port the backend listens on", slot)
			}
			switch def.Proto {
			case "", "tcp", "udp":
			default:
				return fmt.Errorf("exposes.%s: proto must be tcp or udp", slot)
			}
			if len(def.Paths) > 0 {
				return fmt.Errorf("exposes.%s: paths are for kind=http", slot)
			}
		default:
			return fmt.Errorf("exposes.%s: kind must be http or stream", slot)
		}
	}
	return nil
}

// ValidateRuntime refuses a runtime xbin no longer runs. There is one:
// "cgi" (D117) executed the tile's backend/handler through net/http/cgi on
// the host, as xbind, outside every sandbox — so anyone who could write the
// tile ran code as the daemon. A tile still declaring it gets this as its
// manifest error (bx ls/doctor, /components); it keeps serving its files,
// its backend never runs, and its /api answers 410 with the same text.
// Other unknown runtimes stay tolerated (a tile without a backend).
func ValidateRuntime(m Manifest) error {
	if m.Runtime == "cgi" {
		return errCGIRemoved
	}
	return nil
}

var errCGIRemoved = errors.New(`runtime "cgi" was removed (unsafe: it ran tile code outside the sandbox) — ` +
	`port the handler to a go/node/python backend (/docs/changes/2026-09-27-cgi-removed.md)`)

// StreamProto returns a stream expose's protocol (tcp default).
func (d ExposeDef) StreamProto() string {
	if d.Proto == "" {
		return "tcp"
	}
	return d.Proto
}
