import Foundation

// The tile ↔ app bridge (plans/native.md §6.2): the existing tile ↔ shell
// postMessage protocol (docs/protocol.md "Tile ↔ shell messaging") and
// nothing new. In the app a tile page is a top-level WebView, so
// xbin-client.js posts its `xbin:*` requests to its own window; a small user
// script relays them to the app's `xbin` message handler, and the app answers
// with `xbin:reply` posted to the same window — which passes xbin-client's
// sender check (`event.source === window.parent`, the window itself). No
// device API is exposed (§2 invariant 3): the app renders dialogs from data
// and opens windows as screens.

public enum TileBridge {
    /// The WKScriptMessageHandler name (page content world). xbin-client
    /// treats its presence as "embedded" (docs/protocol.md).
    public static let handlerName = "xbin"
    /// Largest message relayed (a dialog spec is small; this bounds abuse).
    public static let maxMessageBytes = 256 * 1024

    /// The user script, injected at document start into the main frame:
    /// relays the page's own `xbin:*` requests as JSON strings.
    public static let userScript = """
    (() => {
      const h = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.\(handlerName);
      if (!h || window.__xbinAppBridge) return;
      Object.defineProperty(window, '__xbinAppBridge', { value: true });
      const relay = new Set(['xbin:dialog', 'xbin:window', 'xbin:window-close', 'xbin:contextmenu']);
      addEventListener('message', (e) => {
        if (e.source !== window) return;
        const d = e.data;
        if (!d || typeof d !== 'object' || !relay.has(d.type)) return;
        let s;
        try { s = JSON.stringify(d); } catch (_) { return; }
        if (typeof s === 'string' && s.length <= \(maxMessageBytes)) h.postMessage(s);
      });
    })();
    """

    /// A request from the tile.
    public enum Request: Sendable, Equatable {
        case dialog(id: String, spec: DialogSpec)
        case window(id: String, spec: WindowSpec)
        case windowClose(id: String)
        /// A long-press on the page body (xbin-client's tile menu request).
        case contextMenu(x: Double, y: Double, selection: String)
    }

    /// Decodes one relayed message (a JSON string, or already-parsed JSON).
    public static func parse(_ body: JSONValue) -> Request? {
        var msg = body
        if let s = body.stringValue {
            guard s.utf8.count <= maxMessageBytes, let v = try? JSONValue(parsing: s) else { return nil }
            msg = v
        }
        guard let type = msg["type"]?.stringValue else { return nil }
        let id = requestID(msg["id"])
        switch type {
        case "xbin:dialog":
            guard let id else { return nil }
            return .dialog(id: id, spec: DialogSpec(json: msg["spec"] ?? [:]))
        case "xbin:window":
            guard let id else { return nil }
            return .window(id: id, spec: WindowSpec(json: msg["spec"] ?? [:]))
        case "xbin:window-close":
            guard let id else { return nil }
            return .windowClose(id: id)
        case "xbin:contextmenu":
            let sel = msg["selection"]?.stringValue ?? ""
            return .contextMenu(x: msg["x"]?.doubleValue ?? 0, y: msg["y"]?.doubleValue ?? 0,
                                selection: String(sel.prefix(65536)))
        default:
            return nil
        }
    }

    private static func requestID(_ v: JSONValue?) -> String? {
        guard let s = v?.stringValue, !s.isEmpty, s.count <= 512 else { return nil }
        return s
    }

    /// The JavaScript that answers request `id` (evaluate in the page world
    /// of the frame that asked): `window.postMessage({type:'xbin:reply', …})`.
    public static func replyScript(id: String, result: JSONValue?) -> String {
        var msg: [String: JSONValue] = ["type": "xbin:reply", "id": .string(id)]
        if let result { msg["result"] = result }
        return "window.postMessage(\(JSONValue.object(msg).jsLiteral(htmlSafe: true)), '*');"
    }
}

/// `xbin.dialog(spec)` — the data `<bx-dialog>` renders: plain text only.
public struct DialogSpec: Sendable, Equatable {
    public struct Field: Sendable, Equatable, Identifiable {
        public enum Kind: String, Sendable { case text, password, number, textarea, select, checkbox, email, url }
        public var name: String
        public var label: String
        public var kind: Kind
        /// The initial value: a string, or a Bool for a checkbox.
        public var value: JSONValue
        public var placeholder: String
        /// `select` options: value (as the form reports it: a string) + label.
        public var options: [(value: String, label: String)]
        public var id: String { name }

        public static func == (a: Field, b: Field) -> Bool {
            a.name == b.name && a.label == b.label && a.kind == b.kind && a.value == b.value
                && a.placeholder == b.placeholder && a.options.map(\.value) == b.options.map(\.value)
                && a.options.map(\.label) == b.options.map(\.label)
        }

        /// The value as the form would report it before any edit.
        public var initial: JSONValue {
            switch kind {
            case .checkbox: return .bool(DialogSpec.truthy(value))
            case .select:
                let v = DialogSpec.text(value)
                if options.contains(where: { $0.value == v }) { return .string(v) }
                return .string(options.first?.value ?? "")
            default: return .string(DialogSpec.text(value))
            }
        }
    }

    public struct Button: Sendable, Equatable {
        public var label: String
        /// Reported as `result.button` (any JSON; `null` reads as dismiss).
        public var value: JSONValue
        public var primary: Bool
        public var danger: Bool
    }

    public static let maxFields = 24
    public static let maxButtons = 6

    public var title: String
    public var message: String
    public var error: String
    public var fields: [Field]
    /// As given; ``resolvedButtons`` adds bx-dialog's Cancel/OK default.
    public var buttons: [Button]

    public init(json: JSONValue) {
        title = DialogSpec.text(json["title"])
        message = DialogSpec.text(json["message"])
        error = DialogSpec.text(json["error"])
        fields = (json["fields"]?.arrayValue ?? []).prefix(Self.maxFields).compactMap { f in
            guard let name = f["name"]?.stringValue ?? f["name"].map({ DialogSpec.text($0) }), !name.isEmpty else { return nil }
            let kind = Field.Kind(rawValue: f["type"]?.stringValue ?? "text") ?? .text
            let options: [(String, String)] = (f["options"]?.arrayValue ?? []).map { o in
                if o.objectValue != nil {
                    let v = DialogSpec.text(o["value"])
                    return (v, o["label"].map { DialogSpec.text($0) } ?? v)
                }
                let v = DialogSpec.text(o)
                return (v, v)
            }
            return Field(name: name, label: f["label"].map { DialogSpec.text($0) } ?? name, kind: kind,
                         value: f["value"] ?? .null, placeholder: DialogSpec.text(f["placeholder"]), options: options)
        }
        buttons = (json["buttons"]?.arrayValue ?? []).prefix(Self.maxButtons).map { b in
            Button(label: DialogSpec.text(b["label"]), value: b["value"] ?? .null,
                   primary: b["primary"]?.boolValue ?? false, danger: b["danger"]?.boolValue ?? false)
        }
    }

    /// bx-dialog's buttons: the spec's, or Cancel (null) + OK ("ok").
    public var resolvedButtons: [Button] {
        buttons.isEmpty
            ? [Button(label: "Cancel", value: .null, primary: false, danger: false),
               Button(label: "OK", value: "ok", primary: true, danger: false)]
            : buttons
    }

    /// The button Return submits: the primary one, else the last.
    public var submitButton: Button? { resolvedButtons.first(where: \.primary) ?? resolvedButtons.last }

    /// Fits a plain alert (no fields, at most three buttons).
    public var isSimpleAlert: Bool { fields.isEmpty && resolvedButtons.count <= 3 }

    /// The values before any edit.
    public var initialValues: [String: JSONValue] {
        Dictionary(fields.map { ($0.name, $0.initial) }, uniquingKeysWith: { a, _ in a })
    }

    /// The reply: `{button, values}` (button null = dismissed).
    public static func result(button: JSONValue?, values: [String: JSONValue]) -> JSONValue {
        ["button": button ?? .null, "values": .object(values)]
    }

    static func text(_ v: JSONValue?) -> String {
        switch v {
        case .string(let s)?: return s
        case .int(let i)?: return String(i)
        case .double(let d)?: return d == d.rounded() && abs(d) < 1e15 ? String(Int64(d)) : String(d)
        case .bool(let b)?: return b ? "true" : "false"
        default: return ""
        }
    }

    static func truthy(_ v: JSONValue) -> Bool {
        switch v {
        case .bool(let b): return b
        case .int(let i): return i != 0
        case .double(let d): return d != 0
        case .string(let s): return !s.isEmpty
        case .null: return false
        default: return true
        }
    }
}

/// `xbin.window(spec)` — a sub-path of the tile itself (default) or another
/// tile's path, opened as a screen (pushed when compact, a new window on
/// iPad/Duo). Framing runs the usual frame-token and tile-access checks.
public struct WindowSpec: Sendable, Equatable {
    public var path: String
    public var src: String
    public var title: String

    public init(path: String = "", src: String = "", title: String = "") {
        self.path = path
        self.src = src
        self.title = title
    }

    public init(json: JSONValue) {
        path = DialogSpec.text(json["path"])
        src = DialogSpec.text(json["src"])
        title = DialogSpec.text(json["title"])
    }

    /// The shell's traversal stripping (bx-shell.js `_spawn`): `..` segments
    /// removed, leading and trailing slashes trimmed.
    public static func stripTraversal(_ p: String) -> String {
        var s = p
        // `..` followed by `/` or the end, repeatedly (bx-shell's regex is global).
        while let r = s.range(of: #"\.\.(/|$)"#, options: .regularExpression) { s.removeSubrange(r) }
        while s.hasPrefix("/") { s.removeFirst() }
        while s.hasSuffix("/") { s.removeLast() }
        return s
    }

    /// The component path the window frames, from the tile that opened it.
    public func target(from tile: String) -> String {
        if !src.isEmpty { return Self.stripTraversal(src) }
        return [tile, Self.stripTraversal(path)].filter { !$0.isEmpty }.joined(separator: "/")
    }

    /// The window's title: the spec's, else the framed path.
    public func displayTitle(from tile: String) -> String { title.isEmpty ? target(from: tile) : title }
}

/// Per-tile caps the shell enforces: one dialog at a time (an extra one
/// resolves as dismissed), at most six windows (an extra one closes at once).
public struct SpawnLimits: Sendable, Equatable {
    public static let maxWindows = 6
    public private(set) var openDialog: String?
    public private(set) var windows: [String] = []

    public init() {}

    /// Admits a dialog; false = answer it as dismissed right away.
    public mutating func admitDialog(_ id: String) -> Bool {
        guard openDialog == nil else { return false }
        openDialog = id
        return true
    }

    public mutating func dialogClosed(_ id: String) { if openDialog == id { openDialog = nil } }

    /// Admits a window; false = reply (closed) right away.
    public mutating func admitWindow(_ id: String) -> Bool {
        guard windows.count < Self.maxWindows, !windows.contains(id) else { return false }
        windows.append(id)
        return true
    }

    /// A window closed: true when it was open (reply to its `closed`).
    @discardableResult
    public mutating func windowClosed(_ id: String) -> Bool {
        guard let i = windows.firstIndex(of: id) else { return false }
        windows.remove(at: i)
        return true
    }
}
