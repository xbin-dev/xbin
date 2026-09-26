import Foundation

// A native tile's escape hatches reach its own resources (plans/native.md
// §8.4, §8.5; docs/native.md "Escape hatches", "Security rules"): the
// composer's uploads and a `terminal`'s pty socket go to the tile's backend
// (`/api/<self>/…`), images and shared files to its page or backend, a
// `canvas` to one of its pages (`/c/<self>/…`). The app sends these with the
// tile's frame token, so every reference a tile hands over is resolved here
// and CONFINED to that tile: anything with a scheme or a host, another
// tile's path, xbind's own API, a traversal (`..`, `%2e%2e`, an encoded
// slash) or a path a longer tile owns is refused (nil). xbind authorizes
// every request anyway; this keeps the app from being the tile's proxy to
// anything else.

public enum TileResource {
    /// What a reference may resolve to.
    public enum Base: Sendable, Equatable {
        /// The tile's backend: `/api/<self>[/…]`.
        case api
        /// The tile's pages and assets: `/c/<self>[/…]`.
        case page
    }

    /// An upload target or a pty socket: `/api/<self>/…` (path and query,
    /// percent-encoded). A relative `ref` — or one starting with `/` but not
    /// `/api/` — is under `/api/<self>/`; `/api/…` must be the tile's own.
    /// `{name}` becomes `name`, encoded as one query/path component
    /// (docs/native.md: the composer's `upload.path`). `known`: the
    /// workspace's tile paths (a path a nested tile owns is refused).
    public static func apiPath(_ ref: String, tile: String, known: [String] = [], name: String? = nil) -> String? {
        var r = ref
        if let name { r = r.replacingOccurrences(of: "{name}", with: URLComponent.encode(name)) }
        return resolve(r, tile: tile, known: known, relativeTo: .api, allowed: [.api], rootRelative: .api)?.path
    }

    /// An image `src` or a shared file: a relative `ref` is a file of the
    /// tile's page (`/c/<self>/…`, as the runtime document's base URL makes
    /// it); `/c/<self>/…` and `/api/<self>/…` are the tile's own.
    public static func assetPath(_ ref: String, tile: String, known: [String] = []) -> String? {
        resolve(ref, tile: tile, known: known, relativeTo: .page, allowed: [.page, .api], rootRelative: nil)?.path
    }

    /// A `canvas src`: a page of the tile (`/c/<self>/…`, relative or
    /// absolute), with its fragment.
    public static func pagePath(_ ref: String, tile: String, known: [String] = []) -> String? {
        guard let r = resolve(ref, tile: tile, known: known, relativeTo: .page, allowed: [.page], rootRelative: nil) else { return nil }
        return r.fragment.map { r.path + "#" + $0 } ?? r.path
    }

    /// The composer's `upload.method`: PUT by default (docs/native.md);
    /// POST and PATCH too. Anything else is refused (nil).
    public static func uploadMethod(_ m: String?) -> String? {
        let u = (m ?? "").trimmingCharacters(in: .whitespaces).uppercased()
        if u.isEmpty { return "PUT" }
        return ["PUT", "POST", "PATCH"].contains(u) ? u : nil
    }

    /// A picked file's name as the tile sees it (`{name}`, `uploaded
    /// {name}`): the last path element, control characters replaced, at
    /// most 200 bytes (the extension kept); `fallback` when nothing is left.
    public static func fileName(_ raw: String, fallback: String) -> String {
        var s = raw
        if let i = s.lastIndex(where: { $0 == "/" || $0 == "\\" }) { s = String(s[s.index(after: i)...]) }
        s = String(String.UnicodeScalarView(s.unicodeScalars.map { $0.value < 0x20 || $0.value == 0x7f ? "_" : $0 }))
        s = s.trimmingCharacters(in: .whitespaces)
        if s.isEmpty || s == "." || s == ".." { s = fallback }
        if s.utf8.count > 200 {
            let ext = (s as NSString).pathExtension
            let keep = ext.isEmpty || ext.utf8.count > 16 ? "" : "." + ext
            var stem = String(s.dropLast(keep.count))
            while stem.utf8.count + keep.utf8.count > 200 { stem.removeLast() }
            s = stem + keep
        }
        return s
    }

    /// A tile's WebSocket (a `terminal`'s pty): the workspace's `ws(s)://`
    /// origin, the resolved path, and the frame token as `?frame=` (sockets
    /// can't carry the header; xbind consumes it and never forwards it).
    public static func socketURL(origin: ServerOrigin, path: String, frameToken: String) -> URL? {
        let sep = path.contains("?") ? "&" : "?"
        return URL(string: origin.webSocketOrigin + path + sep + "frame=" + URLComponent.encode(frameToken))
    }

    /// A resolved page path on the workspace's scheme
    /// (`xbin-ws://<workspace>/c/<self>/…`): what a canvas island loads
    /// through the scheme handler.
    public static func schemeURL(workspace: String, path: String) -> URL? {
        guard path.hasPrefix("/") else { return nil }
        return URL(string: "\(TileScheme.scheme)://\(workspace.lowercased())\(path)")
    }

    // MARK: - resolution

    struct Resolved: Equatable {
        var path: String
        var fragment: String?
    }

    /// - relativeTo: the base of a relative reference.
    /// - allowed: the bases an absolute reference may name.
    /// - rootRelative: where `/x` (not under an allowed base) goes; nil: refused.
    static func resolve(_ ref: String, tile: String, known: [String], relativeTo: Base, allowed: [Base],
                        rootRelative: Base?) -> Resolved? {
        let tileSegs = tile.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
        // `/api/xbin/…` is xbind's own API, whatever follows.
        guard !tile.isEmpty, !tileSegs.contains(where: { $0.isEmpty || $0 == "." || $0 == ".." }),
              tileSegs.first != "xbin" else { return nil }
        let s = ref.trimmingCharacters(in: .whitespaces)
        guard !s.isEmpty, !s.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7f || $0 == "\\" }),
              !hasScheme(s), !s.hasPrefix("//") else { return nil }

        var rest = Substring(s)
        var fragment: String?
        if let h = rest.firstIndex(of: "#") {
            fragment = String(rest[rest.index(after: h)...])
            rest = rest[..<h]
        }
        var query: Substring?
        if let q = rest.firstIndex(of: "?") {
            query = rest[rest.index(after: q)...]
            rest = rest[..<q]
        }

        // Segments of the whole server path, still encoded.
        var raw: [Substring]
        let encodedTile = tileSegs.map { URLComponent.encode($0)[...] }
        if rest.hasPrefix("/") {
            raw = rest.dropFirst().split(separator: "/", omittingEmptySubsequences: false)
            let head = raw.first.flatMap { decode($0) }
            let base: Base? = head == "api" ? .api : head == "c" ? .page : nil
            if let base, allowed.contains(base) {
                // an absolute path: checked below to be the tile's own
            } else if let rr = rootRelative, base != .api {
                // `/x` — under the tile's API (the composer's upload.path)
                raw = [prefix(rr)[...]] + encodedTile + raw
            } else {
                return nil
            }
        } else {
            var r = rest
            while r.hasPrefix("./") { r = r.dropFirst(2) }
            if r == "." { r = "" }
            raw = [prefix(relativeTo)[...]] + encodedTile + (r.isEmpty ? [""] : r.split(separator: "/", omittingEmptySubsequences: false))
        }

        // Every segment decodes to a plain name: no traversal, no encoded
        // separators; only the last may be empty (a trailing slash).
        var decoded: [String] = []
        for (i, seg) in raw.enumerated() {
            guard let d = decode(seg), d != ".", d != "..", !d.contains("/"), !d.contains("\\"),
                  !d.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7f }) else { return nil }
            if d.isEmpty && i != raw.count - 1 { return nil }
            decoded.append(d)
        }
        // The tile's own: `<api|c>/<tile segments>[/…]`, and no longer
        // known tile under it claims the rest (xbind resolves the longest).
        guard decoded.count >= 1 + tileSegs.count, decoded.first == "api" || decoded.first == "c",
              Array(decoded[1..<(1 + tileSegs.count)]) == tileSegs else { return nil }
        let after = decoded[(1 + tileSegs.count)...].filter { !$0.isEmpty }
        for k in known where k.hasPrefix(tile + "/") {
            let kSegs = k.split(separator: "/").map(String.init)
            let extra = Array(kSegs.dropFirst(tileSegs.count))
            if !extra.isEmpty, after.count >= extra.count, Array(after.prefix(extra.count)) == extra { return nil }
        }

        var path = "/" + raw.map { canonical($0) }.joined(separator: "/")
        if let query {
            let q = canonicalQuery(query)
            if !q.isEmpty { path += "?" + q }
        }
        return Resolved(path: path, fragment: fragment.map { canonical($0[...], keep: fragmentSafe) })
    }

    static func prefix(_ b: Base) -> String { b == .api ? "api" : "c" }

    /// A `:` before any `/`, `?` or `#`: a scheme (`https:`, `javascript:`,
    /// `data:`) — a relative reference's first segment never has one
    /// (RFC 3986 §4.2), so `a:b` is refused too.
    static func hasScheme(_ s: String) -> Bool {
        s.first { $0 == ":" || $0 == "/" || $0 == "?" || $0 == "#" } == ":"
    }

    /// Strict percent-decoding: nil on a malformed escape or invalid UTF-8.
    static func decode(_ seg: Substring) -> String? {
        var bytes: [UInt8] = []
        var it = Array(seg.utf8)[...]
        while let b = it.popFirst() {
            if b == UInt8(ascii: "%") {
                guard it.count >= 2, let hi = hex(it[it.startIndex]), let lo = hex(it[it.startIndex + 1]) else { return nil }
                bytes.append(hi << 4 | lo)
                it = it.dropFirst(2)
            } else {
                bytes.append(b)
            }
        }
        return String(validating: bytes, as: UTF8.self)
    }

    static func hex(_ c: UInt8) -> UInt8? {
        switch c {
        case UInt8(ascii: "0")...UInt8(ascii: "9"): return c - UInt8(ascii: "0")
        case UInt8(ascii: "a")...UInt8(ascii: "f"): return c - UInt8(ascii: "a") + 10
        case UInt8(ascii: "A")...UInt8(ascii: "F"): return c - UInt8(ascii: "A") + 10
        default: return nil
        }
    }

    // RFC 3986 pchar minus what would change the meaning; `%XX` escapes are kept.
    static let pathSafe = Set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~!$&'()*+,;=:@".utf8)
    static let querySafe = pathSafe.union("/?".utf8)
    static let fragmentSafe = querySafe

    /// A component as a server sees it: escapes kept, everything else that
    /// may not appear raw (spaces, non-ASCII, quotes) percent-encoded.
    static func canonical(_ s: Substring, keep: Set<UInt8> = pathSafe) -> String {
        var out = ""
        for b in s.utf8 {
            if keep.contains(b) || b == UInt8(ascii: "%") { out.unicodeScalars.append(Unicode.Scalar(b)) }
            else { out += String(format: "%%%02X", b) }
        }
        return out
    }

    /// The query, canonical, without any `frame` parameter (the app adds the
    /// tile's own token; a tile never names one).
    static func canonicalQuery(_ q: Substring) -> String {
        q.split(separator: "&", omittingEmptySubsequences: true).filter { p in
            let key = p.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false).first ?? ""
            let k = decode(key[...].replacingOccurrences(of: "+", with: " ")[...]) ?? String(key)
            return k != "frame"
        }.map { canonical($0, keep: querySafe) }.joined(separator: "&")
    }
}

// MARK: - The composer's pickers

/// The composer's `accept` (an `<input type=file>` accept list:
/// `image/*,.pdf,text/plain`): what the app's pickers offer.
public struct AcceptFilter: Sendable, Equatable {
    /// Media types and wildcards (`image/*`), lower-cased.
    public var types: [String]
    /// Extensions without the dot, lower-cased.
    public var extensions: [String]

    public init(_ accept: String?) {
        var t: [String] = []
        var e: [String] = []
        for part in (accept ?? "").split(separator: ",") {
            let p = part.trimmingCharacters(in: .whitespaces).lowercased()
            if p.hasPrefix("."), p.count > 1 { e.append(String(p.dropFirst())) }
            else if p.contains("/") { t.append(p) }
        }
        types = t
        extensions = e
    }

    /// No filter: anything goes.
    public var isAny: Bool { types.isEmpty && extensions.isEmpty || types.contains("*/*") }
    /// Photos and the camera are offered.
    public var allowsImages: Bool { isAny || types.contains { $0 == "image/*" || $0.hasPrefix("image/") } }
    /// Videos from the photo library.
    public var allowsVideos: Bool { isAny || types.contains { $0 == "video/*" || $0.hasPrefix("video/") } }
    /// Files is offered: something other than images is accepted (or
    /// anything is).
    public var allowsFiles: Bool { isAny || !extensions.isEmpty || types.contains { !$0.hasPrefix("image/") } }

    /// Whether a picked file passes (by type, else by extension).
    public func accepts(name: String, mime: String) -> Bool {
        if isAny { return true }
        let m = mime.lowercased()
        let ext = (name as NSString).pathExtension.lowercased()
        for t in types {
            if t == m { return true }
            if t.hasSuffix("/*"), m.hasPrefix(String(t.dropLast(1))) { return true }
        }
        return !ext.isEmpty && extensions.contains(ext)
    }
}

/// Upload limits and the answer handed to the tile.
public enum TileUpload {
    /// Largest file the app uploads for a tile (the scheme handler's body
    /// cap): a bigger one is not sent and the tile gets a 413-shaped answer.
    public static let maxBytes = 64 << 20

    /// The `response` of `uploaded {name, response}`: the backend's body,
    /// parsed when it is JSON, else its text (the preview host's rule).
    public static func response(body: Data) -> JSONValue {
        if let v = try? JSONValue(parsing: body) { return v }
        return .string(String(decoding: body, as: UTF8.self))
    }

    /// What the tile gets for a file the app refused or could not send:
    /// `{error, status?}` (a 413 for "too large", as a backend would say it).
    public static func failure(_ message: String, status: Int? = nil) -> JSONValue {
        var o: [String: JSONValue] = ["error": .string(message)]
        if let status { o["status"] = .int(Int64(status)) }
        return .object(o)
    }

    /// Too big for the app to send.
    public static func tooLarge(_ bytes: Int) -> JSONValue {
        failure("too large: \(bytes) bytes (the app sends at most \(maxBytes >> 20) MiB)", status: 413)
    }
}

// MARK: - canvas html

/// A `canvas html=` island: static markup, no scripts, no network. The app
/// loads it with JavaScript off, no base URL and every navigation refused;
/// this is the document, with a CSP that allows only inline styles and
/// `data:` images.
public enum CanvasDocument {
    public static let csp = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; form-action 'none'; base-uri 'none'"

    public static func wrap(_ html: String) -> String {
        """
        <!doctype html><html><head><meta charset="utf-8">\
        <meta http-equiv="Content-Security-Policy" content="\(csp)">\
        <meta name="viewport" content="width=device-width,initial-scale=1">\
        <meta name="color-scheme" content="light dark">\
        <meta name="referrer" content="no-referrer">\
        <style>html,body{margin:0;padding:0;background:transparent;font:-apple-system-body;-webkit-text-size-adjust:100%}</style>\
        </head><body>\(html)</body></html>
        """
    }
}
