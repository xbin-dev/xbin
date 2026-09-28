import Foundation

/// A `data:` URI decoded: the workspace's branding icon (D76) is one —
/// `data:image/svg+xml|png|jpeg|webp|x-icon;base64,…`, at most 256 KiB
/// (docs/protocol.md `GET /branding`). The app draws the bytes (UIImage;
/// SVG through a web view), this only takes the URI apart.
public struct DataURI: Sendable, Equatable {
    /// The media type, lowercased, without parameters (`image/png`);
    /// `text/plain` when the URI names none (RFC 2397).
    public var mediaType: String
    public var data: Data

    public init(mediaType: String, data: Data) {
        self.mediaType = mediaType
        self.data = data
    }

    /// An image type the app can draw from its bytes directly (ImageIO);
    /// SVG needs a renderer of its own.
    public var isBitmap: Bool {
        ["image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif", "image/x-icon",
         "image/vnd.microsoft.icon", "image/bmp", "image/heic"].contains(mediaType)
    }
    public var isSVG: Bool { mediaType == "image/svg+xml" }

    /// The largest URI decoded (the server's own limit is 256 KiB of image,
    /// about 342 KiB in base64): anything longer is not an icon of ours.
    public static let maxLength = 1 << 20

    /// `data:[<type>][;param]*[;base64],<data>` → its type and bytes; nil
    /// when it isn't one or doesn't decode. Percent-encoded (non-base64)
    /// data is accepted too — an SVG is sometimes written that way.
    public static func parse(_ uri: String) -> DataURI? {
        guard uri.utf8.count <= maxLength else { return nil }
        let trimmed = uri.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.count > 5, trimmed.prefix(5).lowercased() == "data:",
              let comma = trimmed.firstIndex(of: ",") else { return nil }
        let header = trimmed[trimmed.index(trimmed.startIndex, offsetBy: 5)..<comma]
        let body = String(trimmed[trimmed.index(after: comma)...])
        var params = header.split(separator: ";", omittingEmptySubsequences: false).map {
            $0.trimmingCharacters(in: .whitespaces).lowercased()
        }
        let base64 = params.last == "base64"
        if base64 { params.removeLast() }
        let type = params.first.map { $0.isEmpty ? "text/plain" : $0 } ?? "text/plain"
        let data: Data?
        if base64 {
            // Tolerate whitespace and missing padding, as browsers do.
            var b = body.filter { !$0.isWhitespace }
            b = b.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
            if let pct = b.removingPercentEncoding { b = pct }
            let rem = b.count % 4
            if rem == 1 { return nil }
            if rem > 0 { b += String(repeating: "=", count: 4 - rem) }
            data = Data(base64Encoded: b)
        } else {
            data = body.removingPercentEncoding.map { Data($0.utf8) }
        }
        guard let data, !data.isEmpty else { return nil }
        return DataURI(mediaType: type, data: data)
    }
}

/// Decoded `data:` URIs by their text, a few at most (one per workspace in
/// the switcher): a view that draws a branding icon on every render
/// decodes it once. Thread-safe.
public final class DataURICache: @unchecked Sendable {
    public static let shared = DataURICache()

    private let lock = NSLock()
    private var entries: [String: DataURI?] = [:]
    private var order: [String] = []
    private let capacity: Int

    public init(capacity: Int = 16) { self.capacity = max(1, capacity) }

    /// The URI decoded (nil when it isn't one), from the cache when seen.
    public func decode(_ uri: String) -> DataURI? {
        lock.lock()
        if let hit = entries[uri] {
            order.removeAll { $0 == uri }
            order.append(uri)
            lock.unlock()
            return hit
        }
        lock.unlock()
        let d = DataURI.parse(uri)
        lock.lock()
        defer { lock.unlock() }
        if entries[uri] == nil {
            entries[uri] = .some(d)
            order.append(uri)
            while order.count > capacity { entries.removeValue(forKey: order.removeFirst()) }
        }
        return d
    }

    public var count: Int {
        lock.lock()
        defer { lock.unlock() }
        return entries.count
    }
}

extension BrandingCache {
    /// The icon decoded, when it is a `data:` URI of an image.
    public var iconImage: DataURI? {
        guard let icon, !icon.isEmpty else { return nil }
        guard let d = DataURICache.shared.decode(icon), d.mediaType.hasPrefix("image/") else { return nil }
        return d
    }

    /// Whether the workspace set a title or an icon (D76): the app then
    /// shows them; otherwise its address.
    public var isBranded: Bool { !title.isEmpty || !(icon ?? "").isEmpty }
}
