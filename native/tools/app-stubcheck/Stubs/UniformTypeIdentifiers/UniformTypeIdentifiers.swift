import Foundation
public struct UTType: Hashable, Sendable {
    public init?(mimeType: String) {}
    public init?(filenameExtension: String) {}
    public static let item = UTType(), image = UTType(), movie = UTType(), audio = UTType(), text = UTType(), data = UTType()
    public static let jpeg = UTType(), png = UTType(), gif = UTType(), heic = UTType(), pdf = UTType()
    init() {}
    public var identifier: String { "" }
    public var preferredMIMEType: String? { nil }
    public var preferredFilenameExtension: String? { nil }
    public func conforms(to other: UTType) -> Bool { false }
}
