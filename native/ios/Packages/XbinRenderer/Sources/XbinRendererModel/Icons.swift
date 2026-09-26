import Foundation

/// Icon names (plans/native.md §10.4) → SF Symbols: the curated set of
/// `web/xb/vocab.js` `ICONS` (`VocabularyTests` keeps them equal). An
/// unknown name draws ``placeholder`` — the runtime already sent a
/// diagnostic; it is never a reason to fall back to the web tile.
public enum XbinIcons {
    public static let symbols: [String: String] = [
        "plus": "plus", "minus": "minus", "check": "checkmark", "xmark": "xmark",
        "copy": "doc.on.doc", "share": "square.and.arrow.up", "refresh": "arrow.clockwise", "gear": "gearshape",
        "terminal": "apple.terminal", "box": "shippingbox", "key": "key", "lock": "lock",
        "unlock": "lock.open", "globe": "globe", "shield": "checkmark.shield", "bolt": "bolt",
        "clock": "clock", "chart": "chart.xyaxis.line", "link": "link", "paperclip": "paperclip",
        "send": "arrow.up.circle.fill", "stop": "stop.circle.fill", "play": "play.fill", "pause": "pause.fill",
        "sparkles": "sparkles", "wrench": "wrench.and.screwdriver", "warning": "exclamationmark.triangle", "trash": "trash",
        "list": "list.bullet", "ellipsis": "ellipsis", "search": "magnifyingglass", "filter": "line.3.horizontal.decrease",
        "pencil": "pencil", "folder": "folder", "file": "doc", "doc": "doc.text",
        "photo": "photo", "bell": "bell", "person": "person", "people": "person.2",
        "star": "star", "heart": "heart", "pin": "pin", "archive": "archivebox",
        "tag": "tag", "calendar": "calendar", "mail": "envelope", "chat": "bubble.left",
        "info": "info.circle", "question": "questionmark.circle", "error": "xmark.octagon", "home": "house",
        "download": "arrow.down.circle", "upload": "arrow.up.circle", "cloud": "cloud", "server": "server.rack",
        "database": "cylinder", "cpu": "cpu", "network": "network", "code": "chevron.left.forwardslash.chevron.right",
        "branch": "arrow.triangle.branch", "eye": "eye", "eye-slash": "eye.slash", "external": "arrow.up.right.square",
        "back": "chevron.left", "forward": "chevron.right", "expand": "chevron.down", "collapse": "chevron.up",
        "power": "power", "sun": "sun.max", "moon": "moon", "agent": "person.crop.circle.badge.checkmark",
    ]

    /// What an unknown icon name draws.
    public static let placeholder = "questionmark.square.dashed"

    /// The SF Symbol for `name`; nil for a missing name, ``placeholder``
    /// for an unknown one.
    public static func symbol(_ name: String?) -> String? {
        guard let name, !name.isEmpty else { return nil }
        return symbols[name] ?? placeholder
    }

    /// Symbols the renderer draws on its own (not tile icon names).
    public enum UI {
        public static let chevronForward = "chevron.right"
        public static let chevronDown = "chevron.down"
        public static let checkmark = "checkmark"
        public static let close = "xmark"
        public static let stateOK = "checkmark.circle.fill"
        public static let stateError = "xmark.circle.fill"
        public static let stateCanceled = "nosign"
        public static let planDone = "checkmark.circle.fill"
        public static let planActive = "circle.inset.filled"
        public static let planPending = "circle"
        public static let expandFull = "arrow.up.left.and.arrow.down.right"
        public static let send = "arrow.up.circle.fill"
        public static let stop = "stop.circle.fill"
        public static let attach = "plus"
        public static let question = "questionmark.bubble"
        public static let approval = "checkmark.shield"
        public static let answered = "checkmark.seal"
        public static let queued = "clock"
        public static let image = "photo"
        public static let attachment = "paperclip"
        public static let terminal = "apple.terminal"
        public static let canvas = "rectangle.dashed"
        public static let unknown = "questionmark.square.dashed"
    }
}
