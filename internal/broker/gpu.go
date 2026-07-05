package broker

import (
	"strings"

	"github.com/magik6k/xbin/internal/gpu"
	"github.com/magik6k/xbin/internal/registry"
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
