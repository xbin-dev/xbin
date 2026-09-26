import Foundation

/// Quick Look previews of images (`image preview`, a message's image
/// file): the bytes the renderer already loaded are written to a private
/// temporary file whose name and extension Quick Look can read. This part
/// decides the name; the app-side view writes and removes the file.
public enum PreviewFile {
    /// The image format of `data` from its first bytes, as a file
    /// extension (`png`, `jpg`, `gif`, `webp`, `heic`, `bmp`, `tiff`), or
    /// nil when unknown.
    public static func imageExtension(_ data: Data) -> String? {
        let b = [UInt8](data.prefix(16))
        func at(_ i: Int, _ bytes: [UInt8]) -> Bool {
            b.count >= i + bytes.count && Array(b[i..<i + bytes.count]) == bytes
        }
        if at(0, [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]) { return "png" }
        if at(0, [0xFF, 0xD8, 0xFF]) { return "jpg" }
        if at(0, Array("GIF8".utf8)) { return "gif" }
        if at(0, Array("RIFF".utf8)) && at(8, Array("WEBP".utf8)) { return "webp" }
        if at(4, Array("ftyp".utf8)) {
            let brand = b.count >= 12 ? String(decoding: b[8..<12], as: UTF8.self) : ""
            if ["heic", "heix", "hevc", "mif1", "msf1"].contains(brand) { return "heic" }
            if brand == "avif" { return "avif" }
        }
        if at(0, Array("BM".utf8)) { return "bmp" }
        if at(0, [0x49, 0x49, 0x2A, 0x00]) || at(0, [0x4D, 0x4D, 0x00, 0x2A]) { return "tiff" }
        return nil
    }

    /// A file name for the preview: `name` (an image's `alt`, a file's
    /// `name`) made safe — no path separators or control characters, not
    /// hidden, at most 80 characters — or "image", with the extension of
    /// the bytes (else the name's own, else `png`).
    public static func name(_ name: String?, data: Data) -> String {
        var base = (name ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        base = String(base.map { ch -> Character in
            if ch == "/" || ch == "\\" || ch == ":" { return "-" }
            if ch.unicodeScalars.contains(where: { $0.properties.generalCategory == .control }) { return " " }
            return ch
        })
        while base.hasPrefix(".") { base.removeFirst() }
        base = base.trimmingCharacters(in: .whitespaces)
        var own: String?
        if let dot = base.lastIndex(of: "."), dot != base.startIndex {
            let e = base[base.index(after: dot)...].lowercased()
            if (1...5).contains(e.count), e.allSatisfy({ $0.isLetter || $0.isNumber }) {
                own = e
                base = String(base[..<dot])
            }
        }
        if base.count > 80 { base = String(base.prefix(80)) }
        if base.isEmpty { base = "image" }
        let ext = imageExtension(data) ?? own ?? "png"
        return base + "." + ext
    }
}
