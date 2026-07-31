package broker

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
)

// GPUFor returns the GPUs a component is *granted* (plans/gpu.md): the gpu:*
// targets in its manifest uses that the owner has approved, resolved against the
// host inventory. An element can never self-grant a GPU — an unapproved gpu:*
// use yields nothing, so the sandbox sees no device.
func (b *Broker) GPUFor(c *registry.Component) []gpu.Device {
	var targets []string
	for _, u := range c.Manifest.Uses {
		if !strings.HasPrefix(u.Target, "gpu:") {
			continue
		}
		if _, ok := b.grantedRole(c.Path, u.Target); ok {
			targets = append(targets, u.Target)
		}
	}
	return gpu.Resolve(targets)
}

// NetAdminCap is the reserved capability grant a net-PROVIDER tile needs: the
// sandbox keeps the network-admin caps (NET_ADMIN, NET_RAW, NET_BIND_SERVICE)
// for it instead of dropping all, so it can build its routing/firewall
// dataplane (plans/interfaces.md, DECISIONS D18a). Admin-only to approve (it's
// a reserved target, never same-scope auto-granted); the policy ceiling's
// `net` deny class covers it (a tile denied network can't be a net provider).
const NetAdminCap = "cap:net-admin"

// NetAdminFor reports whether a component holds the cap:net-admin grant — the
// runner's NetCaps hook (wired in main.go). grantedRole applies the policy
// ceiling, so an org/workspace `net` deny strips this too.
func (b *Broker) NetAdminFor(c *registry.Component) bool {
	_, ok := b.grantedRole(c.Path, NetAdminCap)
	return ok
}

// ContainersCap is the reserved capability grant a **container-host tile**
// needs (plans/containers.md): the sandbox keeps the tile's user-namespace
// capabilities and applies only a minimal seccomp floor, so rootless podman
// can create nested namespaces and mount container filesystems. It is a
// powerful, high-surface capability — **admin-only** to approve (a reserved
// target, never same-scope auto-granted), and the policy ceiling's `xbin-caps`
// deny class strips it (an org that forbids system capabilities for its tiles
// forbids container hosts too). Still rootless: no host reach, no other-tile
// reach.
const ContainersCap = "cap:containers"

// ContainersFor reports whether a component holds the cap:containers grant —
// the runner's ContainerCaps hook (wired in main.go). grantedRole applies the
// policy ceiling.
func (b *Broker) ContainersFor(c *registry.Component) bool {
	_, ok := b.grantedRole(c.Path, ContainersCap)
	return ok
}
