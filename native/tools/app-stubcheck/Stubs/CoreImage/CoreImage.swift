@_exported import UIKit
public struct CGAffineTransform: Sendable { public init(scaleX: CGFloat, y: CGFloat) {} }
public final class CIImage: @unchecked Sendable {
    public var extent: CGRect { .zero }
    public func transformed(by t: CGAffineTransform) -> CIImage { self }
}
public final class CIContext: @unchecked Sendable {
    public init() {}
    public func createCGImage(_ image: CIImage, from rect: CGRect) -> CGImage? { nil }
}
public protocol CIQRCodeGenerator: AnyObject {
    var message: Data { get set }
    var correctionLevel: String { get set }
    var outputImage: CIImage? { get }
}
public final class _QR: CIQRCodeGenerator {
    public var message = Data()
    public var correctionLevel = "M"
    public var outputImage: CIImage? { nil }
}
open class CIFilter {
    public static func qrCodeGenerator() -> any CIQRCodeGenerator { _QR() }
}
