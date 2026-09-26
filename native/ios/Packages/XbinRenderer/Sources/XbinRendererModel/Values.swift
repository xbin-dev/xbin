import Foundation
import XbinCore

/// One option of a `picker` (`options [{value, label, icon}]`).
public struct PickerOption: Sendable, Hashable, Identifiable {
    /// The value reported in `change {value}`: a string, number or bool.
    public var value: JSONValue
    public var label: String
    /// An icon name (§10.4).
    public var icon: String?

    public init(value: JSONValue, label: String, icon: String? = nil) {
        self.value = value
        self.label = label
        self.icon = icon
    }

    public var id: JSONValue { value }

    /// The options of a picker's props (non-objects and options without a
    /// value skipped; a missing label reads as the value).
    public static func list(_ p: Props) -> [PickerOption] {
        p.objects("options").compactMap { o in
            guard let v = o["value"], !v.isNull else { return nil }
            return PickerOption(value: v, label: Props.text(o["label"]) ?? Props.text(v) ?? "", icon: Props.text(o["icon"]))
        }
    }
}

/// `data:` image sources (plans/native.md §7.5: rasters up to 256 KiB may
/// travel in the tree; anything bigger is refused, not decoded).
public enum DataURL {
    public static let maxBytes = 256 * 1024

    /// The bytes of a `data:[<mime>][;base64],<payload>` URL; nil when it
    /// isn't one, doesn't decode, or is over ``maxBytes``.
    public static func decode(_ src: String) -> Data? {
        guard src.hasPrefix("data:"), let comma = src.firstIndex(of: ",") else { return nil }
        let meta = src[src.index(src.startIndex, offsetBy: 5)..<comma]
        let payload = String(src[src.index(after: comma)...])
        guard payload.count <= maxBytes * 4 / 3 + 8 else { return nil }
        let data: Data?
        if meta.split(separator: ";").contains("base64") {
            data = Data(base64Encoded: payload, options: .ignoreUnknownCharacters)
        } else {
            data = payload.removingPercentEncoding.map { Data($0.utf8) }
        }
        guard let data, data.count <= maxBytes else { return nil }
        return data
    }

    /// The MIME type a data URL declares (`image/png`), if any.
    public static func mime(_ src: String) -> String? {
        guard src.hasPrefix("data:"), let comma = src.firstIndex(of: ",") else { return nil }
        let meta = src[src.index(src.startIndex, offsetBy: 5)..<comma]
        let m = meta.split(separator: ";").first.map(String.init) ?? ""
        return m.isEmpty ? nil : m
    }
}
