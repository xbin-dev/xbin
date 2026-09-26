import Foundation

/// base64url (RFC 4648 §5): `-` and `_` instead of `+` and `/`, no padding
/// on output.
public enum Base64URL {
    /// Encodes without padding.
    public static func encode(_ data: Data) -> String {
        var s = data.base64EncodedString()
        s = s.replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
        while s.hasSuffix("=") { s.removeLast() }
        return s
    }

    /// Encodes UTF-8 text without padding.
    public static func encode(_ text: String) -> String { encode(Data(text.utf8)) }

    /// Decodes, with or without padding. Nil for anything else: the standard
    /// alphabet's `+`/`/`, whitespace, a bad length or bad padding.
    public static func decode(_ string: String) -> Data? {
        var body = Substring(string)
        var pad = 0
        while body.hasSuffix("=") {
            body.removeLast()
            pad += 1
        }
        guard pad <= 2 else { return nil }
        for u in body.utf8 {
            switch u {
            case UInt8(ascii: "A")...UInt8(ascii: "Z"), UInt8(ascii: "a")...UInt8(ascii: "z"),
                 UInt8(ascii: "0")...UInt8(ascii: "9"), UInt8(ascii: "-"), UInt8(ascii: "_"):
                continue
            default:
                return nil
            }
        }
        let rem = body.utf8.count % 4
        if rem == 1 { return nil }
        if pad > 0, (body.utf8.count + pad) % 4 != 0 { return nil }
        var s = body.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        if rem > 0 { s += String(repeating: "=", count: 4 - rem) }
        return Data(base64Encoded: s)
    }
}

extension Data {
    /// base64url without padding.
    public var base64URLEncodedString: String { Base64URL.encode(self) }

    /// Decodes base64url (padding optional).
    public init?(base64URLEncoded string: String) {
        guard let d = Base64URL.decode(string) else { return nil }
        self = d
    }
}
