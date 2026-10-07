// Stubs of the XCTest and XCUITest API that native/ios/UITests uses, as the
// iOS SDK declares them (Xcode 16+: the UI-automation classes are
// @MainActor; XCTestCase and XCTAttachment are not). Written from the SDK's
// headers as remembered — see ../README.md for what a pass means.
@_exported import Foundation
#if canImport(FoundationNetworking)
@_exported import FoundationNetworking // URLSession/URLRequest off Apple platforms
#endif

// MARK: XCTest

open class XCTestCase {
    public init() {}
    open func setUpWithError() throws {}
    open func tearDownWithError() throws {}
    open var continueAfterFailure: Bool = true
    open func add(_ attachment: XCTAttachment) {}
}

public struct XCTSkip: Error {
    public init(_ message: String? = nil, file: StaticString = #filePath, line: UInt = #line) {}
}

/// One element of an XCUIElement's snapshot (a whole subtree in one query).
public protocol XCUIElementSnapshot {
    var identifier: String { get }
    var frame: CGRect { get }
    var label: String { get }
    var elementType: XCUIElement.ElementType { get }
    var children: [any XCUIElementSnapshot] { get }
}

public func XCTSkipIf(_ expression: @autoclosure () throws -> Bool, _ message: @autoclosure () -> String? = nil,
                      file: StaticString = #filePath, line: UInt = #line) throws {}
public func XCTSkipUnless(_ expression: @autoclosure () throws -> Bool, _ message: @autoclosure () -> String? = nil,
                          file: StaticString = #filePath, line: UInt = #line) throws {}
public func XCTAssertTrue(_ expression: @autoclosure () throws -> Bool, _ message: @autoclosure () -> String = "",
                          file: StaticString = #filePath, line: UInt = #line) {}
public func XCTAssertFalse(_ expression: @autoclosure () throws -> Bool, _ message: @autoclosure () -> String = "",
                           file: StaticString = #filePath, line: UInt = #line) {}
public func XCTAssertEqual<T: Equatable>(_ expression1: @autoclosure () throws -> T, _ expression2: @autoclosure () throws -> T,
                                         _ message: @autoclosure () -> String = "", file: StaticString = #filePath, line: UInt = #line) {}
public func XCTFail(_ message: String = "", file: StaticString = #filePath, line: UInt = #line) {}
public func XCTUnwrap<T>(_ expression: @autoclosure () throws -> T?, _ message: @autoclosure () -> String = "",
                         file: StaticString = #filePath, line: UInt = #line) throws -> T {
    guard let v = try expression() else { throw XCTSkip() }
    return v
}

open class XCTAttachment {
    public enum Lifetime: Int, Sendable { case keepAlways, deleteOnSuccess }
    public init(screenshot: XCUIScreenshot) {}
    public init(string: String) {}
    open var name: String?
    open var lifetime: Lifetime = .deleteOnSuccess
}

// MARK: XCUITest (CGVector lives in CoreGraphics, which XCTest re-exports on iOS)

/// NSPredicate(format:) — swift-corelibs-foundation marks it unavailable, so
/// run.sh renames the sources' NSPredicate( to this (same signature).
public final class StubPredicate: @unchecked Sendable {
    public init(format predicateFormat: String, _ args: any CVarArg...) {}
}

public struct CGVector: Sendable {
    public var dx: CGFloat
    public var dy: CGFloat
    public init(dx: CGFloat, dy: CGFloat) {
        self.dx = dx
        self.dy = dy
    }
}

@MainActor
open class XCUIScreenshot {
    open var pngRepresentation: Data { Data() }
    open var image: UIImage { UIImage() }
}

// MARK: UIKit and CoreGraphics (what a test reads a screenshot's pixels with)

open class UIImage: @unchecked Sendable {
    public init() {}
    open var cgImage: CGImage? { nil }
}

open class CGImage: @unchecked Sendable {
    open var width: Int { 0 }
    open var height: Int { 0 }
}

open class CGColorSpace: @unchecked Sendable {
    /// A CFString on Apple platforms.
    public static let sRGB = "kCGColorSpaceSRGB"
    public init?(name: String) {}
}

public enum CGImageAlphaInfo: UInt32, Sendable {
    case none = 0, premultipliedLast = 1, premultipliedFirst = 2, last = 3, first = 4, noneSkipLast = 5, noneSkipFirst = 6
}

open class CGContext {
    public init?(data: UnsafeMutableRawPointer?, width: Int, height: Int, bitsPerComponent: Int, bytesPerRow: Int,
                 space: CGColorSpace, bitmapInfo: UInt32) {}
    open func draw(_ image: CGImage, in rect: CGRect) {}
}

@MainActor
open class XCUIScreen {
    public static var main: XCUIScreen { XCUIScreen() }
    open func screenshot() -> XCUIScreenshot { XCUIScreenshot() }
}

@MainActor
open class XCUICoordinate {
    open func tap() {}
    open func press(forDuration duration: TimeInterval) {}
    open func press(forDuration duration: TimeInterval, thenDragTo other: XCUICoordinate) {}
    open func press(forDuration duration: TimeInterval, thenDragTo other: XCUICoordinate, withVelocity velocity: XCUIGestureVelocity,
                    thenHoldForDuration holdDuration: TimeInterval) {}
    open func withOffset(_ offsetVector: CGVector) -> XCUICoordinate { self }
}

public struct XCUIGestureVelocity: Sendable {
    public init(_ rawValue: CGFloat) {}
    public static let `default` = XCUIGestureVelocity(0), slow = XCUIGestureVelocity(0), fast = XCUIGestureVelocity(0)
}

@MainActor
public protocol XCUIElementTypeQueryProvider {
    var buttons: XCUIElementQuery { get }
    var staticTexts: XCUIElementQuery { get }
    var textFields: XCUIElementQuery { get }
    var secureTextFields: XCUIElementQuery { get }
    var textViews: XCUIElementQuery { get }
    var searchFields: XCUIElementQuery { get }
    var webViews: XCUIElementQuery { get }
    var navigationBars: XCUIElementQuery { get }
    var alerts: XCUIElementQuery { get }
    var cells: XCUIElementQuery { get }
    var otherElements: XCUIElementQuery { get }
    var windows: XCUIElementQuery { get }
    var keyboards: XCUIElementQuery { get }
    func descendants(matching type: XCUIElement.ElementType) -> XCUIElementQuery
    func children(matching type: XCUIElement.ElementType) -> XCUIElementQuery
}

@MainActor
open class XCUIElement: XCUIElementTypeQueryProvider {
    public enum ElementType: UInt, Sendable {
        case any, other, application, button, staticText, textField, secureTextField, textView, searchField, webView, cell
    }
    open var exists: Bool { false }
    open var isHittable: Bool { false }
    open var isEnabled: Bool { false }
    open var label: String { "" }
    open var identifier: String { "" }
    open var value: Any? { nil }
    open var placeholderValue: String? { nil }
    open var elementType: ElementType { .any }
    open var frame: CGRect { .zero }
    open func snapshot() throws -> any XCUIElementSnapshot { throw XCTSkip() }
    open func waitForExistence(timeout: TimeInterval) -> Bool { false }
    open func waitForNonExistence(timeout: TimeInterval) -> Bool { false }
    open func tap() {}
    open func doubleTap() {}
    open func press(forDuration duration: TimeInterval) {}
    open func typeText(_ text: String) {}
    open func swipeUp() {}
    open func swipeDown() {}
    open func pinch(withScale scale: CGFloat, velocity: CGFloat) {}
    open func coordinate(withNormalizedOffset normalizedOffset: CGVector) -> XCUICoordinate { XCUICoordinate() }
    open func screenshot() -> XCUIScreenshot { XCUIScreenshot() }
    open var buttons: XCUIElementQuery { XCUIElementQuery() }
    open var staticTexts: XCUIElementQuery { XCUIElementQuery() }
    open var textFields: XCUIElementQuery { XCUIElementQuery() }
    open var secureTextFields: XCUIElementQuery { XCUIElementQuery() }
    open var textViews: XCUIElementQuery { XCUIElementQuery() }
    open var searchFields: XCUIElementQuery { XCUIElementQuery() }
    open var webViews: XCUIElementQuery { XCUIElementQuery() }
    open var navigationBars: XCUIElementQuery { XCUIElementQuery() }
    open var alerts: XCUIElementQuery { XCUIElementQuery() }
    open var cells: XCUIElementQuery { XCUIElementQuery() }
    open var otherElements: XCUIElementQuery { XCUIElementQuery() }
    open var windows: XCUIElementQuery { XCUIElementQuery() }
    open var keyboards: XCUIElementQuery { XCUIElementQuery() }
    open func descendants(matching type: ElementType) -> XCUIElementQuery { XCUIElementQuery() }
    open func children(matching type: ElementType) -> XCUIElementQuery { XCUIElementQuery() }
}

@MainActor
open class XCUIElementQuery: XCUIElementTypeQueryProvider {
    open subscript(key: String) -> XCUIElement { XCUIElement() }
    open func matching(_ predicate: StubPredicate) -> XCUIElementQuery { self }
    open func matching(identifier: String) -> XCUIElementQuery { self }
    open func containing(_ predicate: StubPredicate) -> XCUIElementQuery { self }
    open func containing(_ elementType: XCUIElement.ElementType, identifier: String?) -> XCUIElementQuery { self }
    open var firstMatch: XCUIElement { XCUIElement() }
    open var debugDescription: String { "" }
    open var element: XCUIElement { XCUIElement() }
    open var count: Int { 0 }
    open func element(boundBy index: Int) -> XCUIElement { XCUIElement() }
    open var buttons: XCUIElementQuery { self }
    open var staticTexts: XCUIElementQuery { self }
    open var textFields: XCUIElementQuery { self }
    open var secureTextFields: XCUIElementQuery { self }
    open var textViews: XCUIElementQuery { self }
    open var searchFields: XCUIElementQuery { self }
    open var webViews: XCUIElementQuery { self }
    open var navigationBars: XCUIElementQuery { self }
    open var alerts: XCUIElementQuery { self }
    open var cells: XCUIElementQuery { self }
    open var otherElements: XCUIElementQuery { self }
    open var windows: XCUIElementQuery { self }
    open var keyboards: XCUIElementQuery { self }
    open func descendants(matching type: XCUIElement.ElementType) -> XCUIElementQuery { self }
    open func children(matching type: XCUIElement.ElementType) -> XCUIElementQuery { self }
}

@MainActor
open class XCUIApplication: XCUIElement {
    public override init() {}
    public init(bundleIdentifier: String) {}
    open var launchArguments: [String] = []
    open var launchEnvironment: [String: String] = [:]
    open func launch() {}
    open func activate() {}
    open func terminate() {}
    open func open(_ url: URL) {}
}

@MainActor
open class XCUIDevice {
    public static var shared: XCUIDevice { XCUIDevice() }
    public enum Button: Int, Sendable { case home = 1 }
    open func press(_ button: Button) {}
}
