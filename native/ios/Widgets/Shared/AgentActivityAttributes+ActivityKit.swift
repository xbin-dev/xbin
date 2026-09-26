import ActivityKit
import XbinAgent

// The ActivityKit side of XbinAgent's AgentActivityAttributes, compiled into
// the app (which starts and updates the card) and the widget extension
// (which draws it). The type's name is what a push-to-start names
// ("attributes-type": "AgentActivityAttributes", relay/liveactivity.go);
// its JSON, and its ContentState's, are the relay's (native/spec/push.md §7).
//
// A conformance of a package's type to an SDK protocol: Swift warns that it
// is retroactive. `@retroactive` would silence that, but is an error
// wherever the compiler counts XbinAgent as the same package — a warning
// is the safer failure.
extension AgentActivityAttributes: ActivityAttributes {}
