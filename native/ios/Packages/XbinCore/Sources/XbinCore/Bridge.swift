import Foundation

// The runtime bridge of plans/native.md §9. Runtime → app: messages posted to
// the `xbn` WKScriptMessageHandler, decoded by ``BridgeMessage``. App →
// runtime: JavaScript for `callAsyncJavaScript`, built by ``RuntimeCall``.

/// A message from a native tile's runtime to the app.
///
/// The runtime should post each message as a JSON **string**
/// (`webkit.messageHandlers.xbn.postMessage(JSON.stringify(msg))`): the
/// string path is lossless (booleans stay booleans, integers stay integers),
/// where WebKit's object bridging turns everything into `NSNumber`s.
/// ``BridgeMessage/init(body:)`` accepts either.
public enum BridgeMessage: Sendable, Equatable {
    /// `{"op":"mount","v":1,"root":node}` — replace the tree.
    case mount(version: Int, root: Node)
    /// `{"op":"patch","ops":[op, …]}` — apply ops in order.
    case patch([PatchOp])
    /// `{"op":"meta","title"?,"icon"?,"badge"?}` — `xbin.native.meta(…)`.
    case meta(NativeMeta)
    /// `{"op":"error","kind","message","where"}` — the runtime reports a
    /// problem (a module error, a diagnostic, a render timeout).
    case error(RuntimeError)
    /// `{"op":"call","id","what","args"}` — the tile asks the app for
    /// something (copy, share, open, …); answer with
    /// ``RuntimeCall/resolve(id:value:)``.
    case call(BridgeCall)
    /// An op this version doesn't know — ignored (the bridge only grows).
    case unknown(op: String, body: JSONValue)

    /// The wire `op`.
    public var op: String {
        switch self {
        case .mount: return "mount"
        case .patch: return "patch"
        case .meta: return "meta"
        case .error: return "error"
        case .call: return "call"
        case .unknown(let op, _): return op
        }
    }
}

/// Why a bridge message could not be decoded.
public struct BridgeDecodingError: Error, Equatable, Sendable, CustomStringConvertible {
    public var reason: String
    public init(_ reason: String) { self.reason = reason }
    public var description: String { "bad bridge message: \(reason)" }
}

extension BridgeMessage {
    /// Decodes a message from its JSON.
    public init(json: JSONValue) throws {
        guard case .object(let o) = json else { throw BridgeDecodingError("expected an object, got \(json.kindName)") }
        guard case .string(let op)? = o["op"] else { throw BridgeDecodingError("op must be a string") }
        switch op {
        case "mount":
            var v = 1
            if let raw = o["v"], !raw.isNull {
                guard let n = raw.intValue, let i = Int(exactly: n) else { throw BridgeDecodingError("mount: v must be an integer") }
                v = i
            }
            guard let root = o["root"] else { throw BridgeDecodingError("mount: root missing") }
            do { self = .mount(version: v, root: try Node(json: root)) } catch let e as NodeDecodingError {
                throw BridgeDecodingError("mount: \(e)")
            }
        case "patch":
            guard let ops = o["ops"] else { throw BridgeDecodingError("patch: ops missing") }
            do { self = .patch(try PatchOp.list(json: ops)) } catch let e as PatchError {
                throw BridgeDecodingError("patch: \(e)")
            }
        case "meta":
            self = .meta(NativeMeta(fields: o))
        case "error":
            self = .error(RuntimeError(fields: o))
        case "call":
            guard let id = o["id"], !id.isNull else { throw BridgeDecodingError("call: id missing") }
            guard case .string(let what)? = o["what"] else { throw BridgeDecodingError("call: what must be a string") }
            self = .call(BridgeCall(id: id, what: what, args: o["args"] ?? .null))
        default:
            self = .unknown(op: op, body: json)
        }
    }

    /// Parses and decodes a message from JSON text.
    public init(parsing text: String) throws {
        try self.init(json: JSONValue(parsing: text))
    }

    /// Decodes a `WKScriptMessage.body`: a JSON string (preferred), UTF-8
    /// `Data`, or a Foundation JSON object (a dictionary), which is
    /// re-serialized first — on Apple platforms `JSONSerialization` writes
    /// `CFBoolean`s as `true`/`false`, but integral doubles come back as
    /// integers.
    public init(body: Any) throws {
        switch body {
        case let s as String: try self.init(parsing: s)
        case let d as Data: try self.init(json: JSONValue(parsing: d))
        default:
            guard JSONSerialization.isValidJSONObject(body) else {
                throw BridgeDecodingError("body is neither a JSON string nor a JSON object")
            }
            let data = try JSONSerialization.data(withJSONObject: body)
            try self.init(json: JSONValue(parsing: data))
        }
    }

    /// The wire JSON.
    public var json: JSONValue {
        switch self {
        case .mount(let v, let root): return ["op": "mount", "v": .int(Int64(v)), "root": root.json]
        case .patch(let ops): return ["op": "patch", "ops": .array(ops.map(\.json))]
        case .meta(let m):
            var o = m.fields
            o["op"] = "meta"
            return .object(o)
        case .error(let e):
            var o = e.fields
            o["op"] = "error"
            return .object(o)
        case .call(let c): return ["op": "call", "id": c.id, "what": .string(c.what), "args": c.args]
        case .unknown(_, let body): return body
        }
    }
}

/// `xbin.native.meta({title, icon, badge})`: what the navigator and the
/// switcher show for the tile. Each message carries the fields it changes;
/// ``TreeStore`` merges them (a `null` clears one).
public struct NativeMeta: Sendable, Equatable {
    /// Every field of the message except `op`, verbatim.
    public var fields: [String: JSONValue]

    public init(fields: [String: JSONValue] = [:]) {
        var f = fields
        f["op"] = nil
        self.fields = f
    }

    public var title: String? { fields["title"]?.stringValue }
    /// An icon name (plans/native.md §10.4).
    public var icon: String? { fields["icon"]?.stringValue }
    /// The badge as text: a string as is, a number formatted (`3`, `2.5`).
    public var badge: String? {
        switch fields["badge"] {
        case .string(let s)?: return s
        case .int(let i)?: return String(i)
        case .double(let d)?: return d.rounded() == d && abs(d) < 1e15 ? String(Int64(d)) : String(d)
        default: return nil
        }
    }

    /// This meta with `update`'s fields applied: present keys replace,
    /// `null` removes.
    public func merging(_ update: NativeMeta) -> NativeMeta {
        var f = fields
        for (k, v) in update.fields {
            if v.isNull { f.removeValue(forKey: k) } else { f[k] = v }
        }
        return NativeMeta(fields: f)
    }
}

/// A problem the runtime reports (`{"op":"error","kind","message","where"}`).
public struct RuntimeError: Sendable, Equatable {
    /// Every field except `op`, verbatim.
    public var fields: [String: JSONValue]

    public init(fields: [String: JSONValue]) {
        var f = fields
        f["op"] = nil
        self.fields = f
    }

    public init(kind: String, message: String, where location: String? = nil) {
        var f: [String: JSONValue] = ["kind": .string(kind), "message": .string(message)]
        if let location { f["where"] = .string(location) }
        fields = f
    }

    /// The error class (e.g. `module`, `render`, `timeout`, `unsupported`).
    public var kind: String { fields["kind"]?.stringValue ?? "" }
    public var message: String { fields["message"]?.stringValue ?? "" }
    /// Where it happened (a template location, a module URL); objects are
    /// rendered as JSON.
    public var location: String? {
        switch fields["where"] {
        case nil, .null?: return nil
        case .string(let s)?: return s
        case let other?: return other.jsonString
        }
    }
}

/// A request from tile code to the app (`xbin.native.copy/share/open/…`).
/// Answer every call exactly once with ``RuntimeCall/resolve(id:value:)``.
public struct BridgeCall: Sendable, Equatable {
    /// The runtime's id for the call, echoed verbatim in the answer.
    public var id: JSONValue
    /// What is asked (`copy`, `share`, `open`, `saveState`, …).
    public var what: String
    /// The arguments as the runtime sent them.
    public var args: JSONValue

    public init(id: JSONValue, what: String, args: JSONValue = .null) {
        self.id = id
        self.what = what
        self.args = args
    }

    /// The first argument: `args[0]` when args is an array, else args
    /// itself.
    public var firstArgument: JSONValue {
        if case .array(let a) = args { return a.first ?? .null }
        return args
    }

    /// A named argument: `field` of the first argument when it's an object.
    public func argument(_ field: String) -> JSONValue? { firstArgument[field] }

    /// A string argument: the first argument when it's a string, else its
    /// `field`.
    public func stringArgument(_ field: String) -> String? {
        if case .string(let s) = firstArgument { return s }
        return argument(field)?.stringValue
    }
}

// MARK: - App → runtime

/// `document.visibilityState` values the app sets on a runtime.
public enum RuntimeVisibility: String, Sendable, CaseIterable {
    case visible
    case hidden
}

/// A call from the app into a runtime document, as the JavaScript to pass to
/// `WKWebView.callAsyncJavaScript` (as the function body; its value is
/// returned, a returned promise awaited). Every argument is a canonical JSON
/// literal (``JSONValue/jsLiteral(htmlSafe:)``), so no string the tile or the
/// user controls can escape its argument.
public enum RuntimeCall: Sendable, Equatable {
    /// `xbn.event(k, type, payload)` — a user event on node `key`.
    case event(key: String, type: String, payload: JSONValue)
    /// `xbn.visibility(state)` — the tile went on or off screen.
    case visibility(RuntimeVisibility)
    /// `xbn.resolve(id, value)` — the answer to a ``BridgeCall``.
    case resolve(id: JSONValue, value: JSONValue)
    /// `xbn.frame()` — a tick of the renderer's frame clock (drives
    /// `requestAnimationFrame`).
    case frame

    /// The call expression, e.g. `xbn.event("r.0.1","tap",{})`.
    public var javaScript: String {
        switch self {
        case .event(let k, let t, let p):
            return "xbn.event(\(JSONValue.string(k).jsLiteral()),\(JSONValue.string(t).jsLiteral()),\(p.jsLiteral()))"
        case .visibility(let s):
            return "xbn.visibility(\(JSONValue.string(s.rawValue).jsLiteral()))"
        case .resolve(let id, let v):
            return "xbn.resolve(\(id.jsLiteral()),\(v.jsLiteral()))"
        case .frame:
            return "xbn.frame()"
        }
    }

    /// `return <call>;` — for callers that want the call's value (or its
    /// promise) back from `callAsyncJavaScript`.
    public var functionBody: String { "return \(javaScript);" }
}

/// `xbin.native.caps` (plans/native.md §7.4, §11): what this app renders,
/// injected into each runtime before `native.js` runs.
public struct NativeCaps: Sendable, Equatable {
    /// The vocabulary major version.
    public var v: Int
    /// The renderer (`swiftui`, `lit`, …).
    public var renderer: String
    /// The app's version string.
    public var app: String
    /// Primitive name → revision (additive props bump it).
    public var prims: [String: Int]
    /// Feature flags (`chart.area`, `markdown.tables`, …).
    public var features: [String]

    public init(v: Int = 1, renderer: String, app: String, prims: [String: Int], features: [String] = []) {
        self.v = v
        self.renderer = renderer
        self.app = app
        self.prims = prims
        self.features = features
    }

    public var json: JSONValue {
        [
            "v": .int(Int64(v)),
            "renderer": .string(renderer),
            "app": .string(app),
            "prims": .object(prims.mapValues { .int(Int64($0)) }),
            "features": .array(features.sorted().map(JSONValue.string)),
        ]
    }

    public init(json: JSONValue) throws {
        guard case .object(let o) = json else { throw BridgeDecodingError("caps: expected an object") }
        v = o["v"]?.intValue.flatMap { Int(exactly: $0) } ?? 1
        renderer = o["renderer"]?.stringValue ?? ""
        app = o["app"]?.stringValue ?? ""
        var p: [String: Int] = [:]
        for (k, rev) in o["prims"]?.objectValue ?? [:] {
            if let r = rev.intValue.flatMap({ Int(exactly: $0) }) { p[k] = r }
        }
        prims = p
        features = (o["features"]?.arrayValue ?? []).compactMap(\.stringValue)
    }

    /// `xbin.native.supports(name[, rev])`, app side.
    public func supports(_ name: String, revision: Int = 0) -> Bool {
        if let r = prims[name] { return r >= revision }
        return features.contains(name)
    }
}
