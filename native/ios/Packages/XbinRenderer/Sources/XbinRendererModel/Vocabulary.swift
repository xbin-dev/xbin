import Foundation
import XbinCore

/// The native vocabulary as the SwiftUI renderer implements it: every
/// primitive with its revision, the events that report a controlled prop,
/// the token sets and the feature flags — the parts of `web/xb/vocab.js`
/// (exported as `native/spec/vocab.json`) the app needs at run time.
/// `VocabularyTests` holds this table to vocab.json, so the renderer and the
/// runtime can't drift apart silently.
///
/// ADDITIVE ONLY once shipped (docs/compat.md): a primitive the app learns
/// is added here with the view that draws it; its revision rises with the
/// props the view learns.
public enum XbinVocabulary {
    /// The vocabulary / tree format major version this renderer speaks.
    public static let version = 1

    /// Every primitive this renderer draws → its revision (`caps.prims`).
    public static let primitives: [String: Int] = [
        // 8.1 structure and navigation
        "fragment": 1, "nav": 1, "screen": 1, "toolbar": 1, "section": 1, "stack": 1, "list": 1,
        "row": 1, "actions": 1, "disclosure": 1, "tabs": 1, "tab": 1, "sheet": 1, "split": 1,
        "spacer": 1, "divider": 1,
        // 8.2 content
        "text": 1, "markdown": 1, "image": 1, "icon": 1, "badge": 1, "notice": 1, "progress": 1,
        "chart": 1, "code": 1, "empty": 1,
        // 8.3 controls
        "button": 1, "toggle": 1, "field": 1, "picker": 1, "menu": 1,
        // 8.4 the chat family
        "transcript": 1, "message": 1, "thinking": 1, "toolcard": 1, "approval": 1, "question": 1,
        "plan": 1, "diff": 1, "activity": 1, "step": 1, "composer": 1,
        // 8.5 escape hatches
        "terminal": 1, "canvas": 1,
    ]

    /// Feature flags within primitives (`chart.area`, `markdown.tables`).
    public static let features = ["chart.area", "markdown.tables"]

    /// The caps to inject into each runtime document
    /// (``RuntimeScript/documentStart(caps:state:)``): everything this
    /// renderer draws.
    public static func caps(app: String, renderer: String = "swiftui") -> NativeCaps {
        NativeCaps(v: version, renderer: renderer, app: app, prims: primitives, features: features)
    }

    /// An event that carries the app-side value of a controlled prop
    /// (vocab.js `reports`, tree.md §6).
    public struct Report: Sendable, Equatable {
        /// The controlled prop.
        public let prop: String
        /// The payload field holding the value (nil: the prop's own name).
        public let from: String?
        /// A constant value instead of a payload field (`sheet` `dismiss` →
        /// `open: false`).
        public let value: JSONValue?

        public init(prop: String, from: String? = nil, value: JSONValue? = nil) {
            self.prop = prop
            self.from = from
            self.value = value
        }

        /// The reported value in `payload`.
        public func value(in payload: JSONValue) -> JSONValue? {
            if let value { return value }
            return payload[from ?? prop]
        }
    }

    /// primitive → event → the prop it reports.
    public static let reports: [String: [String: Report]] = [
        "screen": ["search": Report(prop: "search", from: "value")],
        "section": ["toggle": Report(prop: "collapsed")],
        "disclosure": ["toggle": Report(prop: "open")],
        "tabs": ["change": Report(prop: "selected", from: "key")],
        "sheet": ["dismiss": Report(prop: "open", value: false)],
        "toggle": ["change": Report(prop: "value")],
        "field": ["input": Report(prop: "value"), "change": Report(prop: "value"), "submit": Report(prop: "value")],
        "picker": ["change": Report(prop: "value")],
        "thinking": ["toggle": Report(prop: "open")],
        "toolcard": ["toggle": Report(prop: "open")],
        "composer": ["input": Report(prop: "value"), "send": Report(prop: "value")],
    ]

    /// The prop `event` on a `type` node reports, if any.
    public static func report(_ type: String, _ event: String) -> Report? { reports[type]?[event] }

    /// Token sets (§10): the values a `token` prop may take.
    public static let tokens: [String: [String]] = [
        "tone": XbinTone.allCases.map(\.rawValue),
        "noticeTone": XbinNoticeTone.allCases.map(\.rawValue),
        "type": XbinTypeRole.allCases.map(\.rawValue),
        "gap": XbinGap.allCases.map(\.rawValue),
        "height": XbinHeight.allCases.map(\.rawValue),
        "icon": XbinIcons.symbols.keys.sorted(),
    ]
}
