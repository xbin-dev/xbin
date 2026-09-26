import Foundation
import UIKit
public struct TerminalOptions: Sendable {
    public static let `default` = TerminalOptions()
    public var scrollback = 500
}
public final class Terminal {
    public var cols: Int { 80 }
    public var rows: Int { 24 }
    public var applicationCursor: Bool { false }
    public func resetToInitialState() {}
}
public protocol TerminalViewDelegate: AnyObject {
    func send(source: TerminalView, data: ArraySlice<UInt8>)
    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int)
    func setTerminalTitle(source: TerminalView, title: String)
    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?)
    func scrolled(source: TerminalView, position: Double)
    func requestOpenLink(source: TerminalView, link: String, params: [String: String])
    func rangeChanged(source: TerminalView, startY: Int, endY: Int)
    func clipboardCopy(source: TerminalView, content: Data)
}
@MainActor open class TerminalView: UIScrollView {
    public init(frame: CGRect, font: UIFont?, options: TerminalOptions) { super.init() }
    public weak var terminalDelegate: (any TerminalViewDelegate)?
    public func getTerminal() -> Terminal { Terminal() }
    public func feed(byteArray: ArraySlice<UInt8>) {}
    open func insertText(_ text: String) {}
    public var font: UIFont { get { UIFont() } set {} }
}
