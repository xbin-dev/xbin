# XbinCore

The part of the xbin app that doesn't draw — Foundation only, so it builds
and tests on Linux (native/AGENTS.md, loop 3):

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
cd native/ios/Packages/XbinCore && swift build && swift test
```

Design: plans/native.md §4 (workspaces, deep links), §5 (device login),
§9 (the tree and the bridge), §17 (fixtures). The wire contracts it
implements: native/spec/tree.md (the runtime's tree, patches and bridge;
reference `applyOps` in web/xb/rt-diff.js) and native/spec/device-login.md.

| File | What |
|---|---|
| `JSONValue.swift`, `JSONParser.swift`, `JSONEncoding.swift` | `JSONValue`: booleans never numbers, `.int` vs `.double` kept; a strict parser (depth-limited, lossless) and a canonical encoder (sorted keys, JS-safe) |
| `Node.swift` | `Node` — the wire node `{k, t, p?, e?, c?}`; `NodeKey` — the §9 key rules |
| `Tree.swift` | `Tree` — the tree stored flat with a key index (`Tree.Entry`: type, props, events, child keys, parent) |
| `Patch.swift` | `PatchOp` (set, unset, events, insert, remove, move-within-parent), `Tree.apply`/`Tree.mount`, `TreeDelta`, `PatchError` |
| `Bridge.swift` | `BridgeMessage` (runtime → app: mount, patch, meta, error, diag, state, call), `RuntimeCall` (app → runtime JavaScript), `RuntimeScript.documentStart`, `NativeCaps` |
| `TreeStore.swift` | `TreeStore` — applies messages, counts revisions, keeps the tree sequence `n`, the saved state and diagnostics, tells closure observers; `failure` means "fall back to the web tile" |
| `DeepLink.swift`, `ServerOrigin.swift` | `xbin://` links; the server origin as the web serializes it |
| `WorkspaceRecord.swift` | `WorkspaceRecord`, `WorkspaceList` — the persisted workspace list (no secrets; the enrollment's `deviceOrigin` is what logins sign) |
| `DeviceLogin.swift`, `Base64URL.swift` | the signed device-login message, SPKI/DER helpers, enrollment codes, PKCE, the sign-in routes (`AppAuthRoute`) and their wire types (`DeviceEnrollRequest`, `DeviceEnrollment`, `DeviceChallenge`, `DeviceLoginRequest`, `AppSession`, …) |
| `Fixtures.swift` | `FixtureTree`, `FixtureSet` — `native/fixtures/<name>/expected.json` |

Rules the code keeps:

- **Parse bridge JSON with `JSONValue(parsing:)`**, never `JSONSerialization`
  or `JSONDecoder` (both lose `true` vs `1` or `1` vs `1.0`). The runtime
  posts JSON strings.
- **A patch that doesn't apply, or a fatal runtime error** (`module`,
  `exception`, `unsupported`), ends the native view: the store sets
  `failure` and ignores everything but the next mount; the app shows the web
  tile (§7.6). Each op is checked before it mutates, so the tree is always
  consistent. The app is stricter than the runtime's reference `applyOps`
  (an out-of-range index or a duplicate key is an error there, where the
  reference clamps or overwrites) so a divergence surfaces instead of
  rendering something wrong.
- **Keys are opaque** and unique tree-wide; the index, not the key text,
  says who the parent is.
- **Everything additive**: unknown node fields (`Node.extra`), unknown
  message ops (`.unknown`) and trailing op elements are kept or ignored;
  an unknown *op name* fails (the runtime only sends what `caps` allows).

Renderer sketch (SwiftUI, in XbinRenderer — not here):

```swift
let store = TreeStore()
let token = store.observe { event in
    switch event {
    case .tree(_, let delta):   model.apply(delta, from: store.tree) // drop removed, build inserted, refresh the rest
    case .call(let call):       handle(call)                          // then webView.callAsyncJavaScript(RuntimeCall.resolve(id: call.id, value: …).javaScript, …)
    case .state(let blob):      persist(blob)                         // injected again via RuntimeScript.documentStart
    case .failed:               showWebTile()
    default:                    break
    }
}
// WKScriptMessageHandler "xbn":
store.receive(body: message.body)
// document start (WKUserScript, .atDocumentStart):
RuntimeScript.documentStart(caps: caps, state: store.savedState)
// a tap (the store adds the tree sequence n):
webView.callAsyncJavaScript(store.event(key, "tap").javaScript, arguments: [:], in: nil, in: .page)
```
