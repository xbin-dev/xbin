// SwiftTerm 1.20's API as the app uses it (from the v1.20.0 sources).
@_exported import UIKit

public struct Position: Equatable, Sendable {
    public var col, row: Int
    public init(col: Int, row: Int) { self.col = col; self.row = row }
}
public struct SearchOptions: Equatable {
    public var caseSensitive: Bool, regex: Bool, wholeWord: Bool
    public init(caseSensitive: Bool = false, regex: Bool = false, wholeWord: Bool = false) {
        self.caseSensitive = caseSensitive; self.regex = regex; self.wholeWord = wholeWord
    }
}
public struct TerminalOptions {
    public static let `default` = TerminalOptions()
    public var scrollback = 500
    public var cols = 80, rows = 25
}
public struct CharData { public var width: Int8 { 1 } }
public final class BufferLine {
    public internal(set) var isWrapped = false
    public var count: Int { 0 }
    public subscript(index: Int) -> CharData { CharData() }
}
public final class Buffer {
    public var totalLinesTrimmed: Int { 0 }
    public var yDisp: Int { 0 }
}
public protocol TerminalDelegate: AnyObject {}
open class Terminal {
    public var cols: Int { 80 }
    public var rows: Int { 25 }
    public private(set) var buffer = Buffer()
    public var applicationCursor = false
    public func resetToInitialState() {}
    public func getCharData(col: Int, row: Int) -> CharData? { nil }
    public func getCharacter(for charData: CharData) -> Character { " " }
    public func getCursorLocation() -> (x: Int, y: Int) { (0, 0) }
    public func getScrollInvariantLine(row: Int) -> BufferLine? { nil }
}
public class SelectionService {
    public var active = false
    public func startSelection(row: Int, col: Int) {}
    public func dragExtend(bufferPosition: Position) {}
    public func getSelectedText() -> String { "" }
}
public protocol TerminalViewDelegate: AnyObject {
    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int)
    func setTerminalTitle(source: TerminalView, title: String)
    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?)
    func send(source: TerminalView, data: ArraySlice<UInt8>)
    func scrolled(source: TerminalView, position: Double)
    func requestOpenLink(source: TerminalView, link: String, params: [String: String])
    func rangeChanged(source: TerminalView, startY: Int, endY: Int)
    func clipboardCopy(source: TerminalView, content: Data)
}
@MainActor open class TerminalView: UIScrollView {
    public init(frame: CGRect, font: UIFont? = nil, options: TerminalOptions) { super.init(frame: frame) }
    public required init?(coder: NSCoder) { super.init(coder: coder) }
    public weak var terminalDelegate: TerminalViewDelegate?
    public var optionAsMetaKey = true
    public var allowMouseReporting = true
    public var font: UIFont = UIFont.monospacedSystemFont(ofSize: 12, weight: .regular)
    public var selection: SelectionService!
    public override var inputAccessoryView: UIView? { get { nil } set {} }
    public func getTerminal() -> Terminal { Terminal() }
    public func feed(byteArray: ArraySlice<UInt8>) {}
    open func getOptimalFrameSize() -> CGRect { .zero }
    open func showCursor(source: Terminal) {}
    open func hideCursor(source: Terminal) {}
    open func bufferActivated(source: Terminal) {}
    open func insertText(_ text: String) {}
    open override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {}
    open override func copy(_ sender: Any?) {}
    @discardableResult public func findNext(_ term: String, options: SearchOptions = SearchOptions(), scrollToResult: Bool = true) -> Bool { false }
    @discardableResult public func findPrevious(_ term: String, options: SearchOptions = SearchOptions(), scrollToResult: Bool = true) -> Bool { false }
    public func searchMatchSummary(_ term: String, options: SearchOptions = SearchOptions(), limit: Int = 1000) -> (index: Int, total: Int) { (0, 0) }
    public func clearSearch() {}
    public func clearSelection() {}
    public var hasActiveSelection: Bool { false }
    public func setSelectionRange(start: Position, end: Position) {}
    public func scrollTo(row: Int, notifyAccessibility: Bool = true) {}
    public func scrollUp(lines: Int) {}
    public func scrollDown(lines: Int) {}
}
