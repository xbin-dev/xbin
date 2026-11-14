// coord_workspace.go — the workspace profile's slot (coord_profile.go): the
// second kind of coordinator, registered and empty. It has no class, no
// tool on its allowlist and no hook, and nothing stamps a conversation with
// its origin — no route, no schedule, no channel, no import — so no run is
// a workspace coordinator in this build and nothing here is reachable. What
// fills it adds to this profile (its class, its allowlist, its hooks and
// the code that makes its conversation); it changes nothing in the kit.
package main

// originWorkspace is runs.origin for a workspace coordinator's
// conversation. Reserved: only the code that makes one may stamp it.
const originWorkspace = "workspace"

// coordWorkspace is the workspace profile.
var coordWorkspace *coordProfile

func init() {
	coordWorkspace = registerCoordProfile(&coordProfile{ID: "workspace", Origin: originWorkspace})
}
