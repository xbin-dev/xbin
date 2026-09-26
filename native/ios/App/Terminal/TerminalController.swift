import Observation
import SwiftTerm
import UIKit
import XbinCore
import XbinTerm

/// The terminal (plans/native.md §12): SwiftTerm draws, XbinTerm runs the
/// `/ws/term` session — framing, reattach with replay, pings/RTT, predictive
/// echo (D70/D71) — and this controller glues them: the emulator adapter,
/// the socket, the accessory row and hardware shortcuts, font size, and the
/// prediction overlay.
@MainActor
@Observable
final class TerminalController: NSObject {
    let workspace: WorkspaceModel
    private(set) var cwd: String
    @ObservationIgnored let terminalView: XbinTerminalView
    @ObservationIgnored let overlay = PredictionOverlayView()
    @ObservationIgnored let container = UIView()
    @ObservationIgnored private(set) var session: TermSession?
    @ObservationIgnored var keyboard = TermKeyboard()
    @ObservationIgnored private var token: String?
    @ObservationIgnored private var resignedForThisSocket = false
    @ObservationIgnored private let initialInput: [UInt8]?

    var phase: TermSession.Phase = .idle
    var info: TermSessionInfo?
    var rtt: Double?
    var lagging = false
    var netNote = ""
    var notice: String?
    var pendingLink: URL?
    var title = ""
    var env: TermEnvState?
    var fontSize = AppSettings.terminalFontSize
    /// Bumped when the sticky modifiers change (the accessory row redraws).
    var stickyRevision = 0

    init(workspace: WorkspaceModel, cwd: String, initialInput: String? = nil) {
        self.workspace = workspace
        self.cwd = cwd
        self.initialInput = initialInput.map { Array($0.utf8) }
        var options = TerminalOptions.default
        options.scrollback = 10_000
        terminalView = XbinTerminalView(frame: CGRect(x: 0, y: 0, width: 400, height: 600),
                                        font: UIFont.monospacedSystemFont(ofSize: AppSettings.terminalFontSize, weight: .regular),
                                        options: options)
        super.init()
        terminalView.controller = self
        terminalView.terminalDelegate = self
        terminalView.optionAsMetaKey = keyboard.settings.optionAsMeta
        terminalView.inputAccessoryView = AccessoryBar(controller: self)
        overlay.isUserInteractionEnabled = false
        container.backgroundColor = .black
        for v in [terminalView, overlay] as [UIView] {
            v.translatesAutoresizingMaskIntoConstraints = false
            container.addSubview(v)
            NSLayoutConstraint.activate([
                v.leadingAnchor.constraint(equalTo: container.leadingAnchor),
                v.trailingAnchor.constraint(equalTo: container.trailingAnchor),
                v.topAnchor.constraint(equalTo: container.topAnchor),
                v.bottomAnchor.constraint(equalTo: container.bottomAnchor),
            ])
        }
        terminalView.addGestureRecognizer(UIPinchGestureRecognizer(target: self, action: #selector(pinched(_:))))
    }

    // MARK: Sessions

    /// Opens (or reattaches to) a session on this tile.
    func start(session id: String? = nil, options: TermNewSession? = nil) async {
        session?.close()
        do {
            token = try await workspace.auth.session(replacing: nil).token
        } catch {
            notice = workspace.describe(error)
            phase = .failed(.unauthorized)
            return
        }
        let opts = options ?? TermNewSession(cwd: cwd)
        let target: TermTarget = id.map { .reattach(id: $0) } ?? .new(opts)
        let origin = workspace.origin
        let s = TermSession(target: target, fresh: opts, initialInput: id == nil ? initialInput : nil, emulator: self,
                            clock: TermSystemClock()) { [weak self] path, events in
            WebSocketTransport(origin: origin, path: path, token: self?.token ?? "", events: events)
        }
        s.delegate = self
        s.predictMode = PredictMode(rawValue: AppSettings.predictMode) ?? .auto
        session = s
        resignedForThisSocket = false
        s.start()
        await loadEnv()
    }

    func close() { session?.close() }

    /// A new session with other options (network scope, VM): the old one is
    /// ended first, as the web does after asking.
    func restart(with options: TermNewSession) async {
        if let id = info?.id { _ = try? await workspace.auth.send(APIRequest("DELETE", TermPaths.kill(session: id))) }
        session?.restart(options)
    }

    func newSession() async { await start(session: nil) }

    func attach(_ id: String) async { await start(session: id) }

    func loadEnv() async {
        guard let r = try? await workspace.auth.call(APIRequest("GET", TermPaths.env(cwd: cwd))) else { return }
        env = try? JSONDecoder().decode(TermEnvState.self, from: r.body)
    }

    /// Wipes the tile's persistent terminal layer (`DELETE /ws/term/env`),
    /// then starts fresh.
    func resetEnvironment() async {
        if let id = info?.id { _ = try? await workspace.auth.send(APIRequest("DELETE", TermPaths.kill(session: id))) }
        _ = try? await workspace.auth.send(APIRequest("DELETE", TermPaths.env(cwd: cwd)))
        await start(session: nil)
    }

    func cycleSession(_ step: Int) async {
        await workspace.refreshSessions()
        let list = TermDirectory.forTile(workspace.sessions, cwd: cwd).filter { $0.kind == .shell }
        guard !list.isEmpty else { return }
        let i = list.firstIndex { $0.id == info?.id } ?? 0
        let next = list[(i + step + list.count) % list.count]
        if next.id != info?.id { await attach(next.id) }
    }

    func setVisible(_ on: Bool) {
        session?.visible = on
        if on, phase == .failed(.disconnected) { session?.reconnect() }
    }

    // MARK: Input

    func accessory(_ key: AccessoryKey) {
        let app = terminalView.getTerminal().applicationCursor
        if let bytes = keyboard.accessory(key, applicationCursor: app) { session?.send(bytes) }
        stickyChanged()
    }

    private func stickyChanged() {
        stickyRevision += 1
        (terminalView.inputAccessoryView as? AccessoryBar)?.refresh()
    }

    /// Soft-keyboard text: with a sticky ctrl/alt it becomes a control
    /// sequence here; otherwise SwiftTerm handles it (IME, dead keys).
    func handleText(_ text: String) -> Bool {
        guard !keyboard.sticky.active.isEmpty else { return false }
        session?.send(keyboard.text(text))
        stickyChanged()
        return true
    }

    /// Hardware keys: the ⌘ shortcuts; everything else is SwiftTerm's.
    func handlePresses(_ presses: Set<UIPress>) -> Bool {
        for press in presses {
            guard let key = press.key, key.modifierFlags.contains(.command) else { continue }
            var mods: HardwareKeyEvent.Modifiers = [.command]
            if key.modifierFlags.contains(.shift) { mods.insert(.shift) }
            if key.modifierFlags.contains(.control) { mods.insert(.control) }
            if key.modifierFlags.contains(.alternate) { mods.insert(.option) }
            let ev = HardwareKeyEvent(usage: UInt16(key.keyCode.rawValue), characters: key.characters,
                                      charactersIgnoringModifiers: key.charactersIgnoringModifiers, modifiers: mods)
            switch keyboard.hardware(ev, applicationCursor: terminalView.getTerminal().applicationCursor) {
            case .send(let b): session?.send(b); return true
            case .shortcut(let s): perform(s); return true
            case .passthrough: continue
            }
        }
        return false
    }

    func perform(_ s: TermShortcut) {
        switch s {
        case .clear:
            terminalView.getTerminal().resetToInitialState()
            session?.send([0x0c])
        case .newSession: Task { await newSession() }
        case .previousSession: Task { await cycleSession(-1) }
        case .nextSession: Task { await cycleSession(1) }
        case .copy: terminalView.copy(nil)
        case .paste:
            if let s = UIPasteboard.general.string { session?.send(s) }
        case .fontBigger: setFont(fontSize + 1)
        case .fontSmaller: setFont(fontSize - 1)
        case .fontReset: setFont(13)
        case .find: break // scrollback search: not yet (see the WP notes)
        }
    }

    func setFont(_ size: Double) {
        fontSize = min(max(size, 7), 32)
        AppSettings.terminalFontSize = fontSize
        terminalView.font = UIFont.monospacedSystemFont(ofSize: fontSize, weight: .regular)
        session?.redraw()
    }

    @objc private func pinched(_ g: UIPinchGestureRecognizer) {
        guard g.state == .changed || g.state == .ended else { return }
        if abs(g.scale - 1) > 0.12 {
            setFont(fontSize * Double(g.scale))
            g.scale = 1
        }
    }

    // MARK: Emulator events (from XbinTerminalView)

    func cursorVisibility(hidden: Bool) { session?.cursorVisibilityChanged(hidden: hidden) }
    func bufferChanged() { session?.bufferChanged() }
    func screenMoved() {
        overlay.hidden(whenScrolledBack: isScrolledBack)
        session?.redraw()
    }

    var isScrolledBack: Bool {
        let v = terminalView
        return v.contentOffset.y + v.bounds.height < v.contentSize.height - 4
    }
}

// MARK: - XbinTerm's emulator

extension TerminalController: TermEmulator {
    func write(_ bytes: [UInt8]) { terminalView.feed(byteArray: bytes[...]) }
    /// SwiftTerm parses synchronously in `feed`.
    func afterParsed(_ body: @escaping @MainActor () -> Void) { body() }
    func reset() { terminalView.getTerminal().resetToInitialState() }
    var framebuffer: (any TermFramebuffer)? { isScrolledBack ? nil : SwiftTermScreen(terminal: terminalView.getTerminal()) }
    var size: TermSize {
        let t = terminalView.getTerminal()
        return TermSize(cols: t.cols, rows: t.rows)
    }
}

extension TerminalController: TermSessionDelegate {
    func termSession(_ s: TermSession, phaseChanged phase: TermSession.Phase) {
        self.phase = phase
        // A refused upgrade with 401: the session behind the token ended
        // (an xbind restart). Re-sign once — one Face ID prompt — and retry.
        if case .failed(.unauthorized) = phase, !resignedForThisSocket {
            resignedForThisSocket = true
            Task {
                do {
                    token = try await workspace.auth.session(replacing: token).token
                    session?.reconnect()
                } catch {
                    notice = workspace.describe(error)
                }
            }
        }
        if case .live = phase { resignedForThisSocket = false }
    }

    func termSession(_ s: TermSession, attached info: TermSessionInfo) {
        self.info = info
        netNote = info.netNote
        Task { await workspace.refreshSessions() }
    }

    func termSession(_ s: TermSession, netNote: String) { self.netNote = netNote }

    func termSession(_ s: TermSession, overlay: PredictionRender, lagging: Bool) {
        self.lagging = lagging
        self.overlay.render(overlay, in: terminalView, hidden: isScrolledBack)
    }

    func termSession(_ s: TermSession, rtt: Double) { self.rtt = rtt }

    func termSession(_ s: TermSession, notice: TermSession.Notice) {
        if notice == .previousSessionGone { self.notice = "That session had ended — this is a new one." }
    }
}

// MARK: - SwiftTerm's view delegate

extension TerminalController: TerminalViewDelegate {
    nonisolated func send(source: TerminalView, data: ArraySlice<UInt8>) {
        let bytes = Array(data)
        MainActor.assumeIsolated { self.session?.send(bytes) }
    }

    nonisolated func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        MainActor.assumeIsolated { self.session?.resized(TermSize(cols: newCols, rows: newRows)) }
    }

    nonisolated func setTerminalTitle(source: TerminalView, title: String) {
        MainActor.assumeIsolated { self.title = title }
    }

    nonisolated func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}

    nonisolated func scrolled(source: TerminalView, position: Double) {
        MainActor.assumeIsolated { self.screenMoved() }
    }

    /// Links in the output open in Safari after a confirmation.
    nonisolated func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        guard let u = URL(string: link), ["http", "https"].contains(u.scheme?.lowercased() ?? "") else { return }
        MainActor.assumeIsolated { self.pendingLink = u }
    }

    nonisolated func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}

    nonisolated func clipboardCopy(source: TerminalView, content: Data) {
        let s = String(decoding: content, as: UTF8.self)
        MainActor.assumeIsolated { UIPasteboard.general.string = s }
    }
}

/// SwiftTerm's view, taking the few events XbinTerm needs.
final class XbinTerminalView: TerminalView {
    weak var controller: TerminalController?

    override func showCursor(source: Terminal) {
        super.showCursor(source: source)
        controller?.cursorVisibility(hidden: false)
    }

    override func hideCursor(source: Terminal) {
        super.hideCursor(source: source)
        controller?.cursorVisibility(hidden: true)
    }

    override func bufferActivated(source: Terminal) {
        super.bufferActivated(source: source)
        controller?.bufferChanged()
    }

    override func insertText(_ text: String) {
        if controller?.handleText(text) != true { super.insertText(text) }
    }

    override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        if controller?.handlePresses(presses) != true { super.pressesBegan(presses, with: event) }
    }
}

/// The predicted text drawn over the terminal (XbinTerm's PredictionRender):
/// each run at its cell, underlined when uncertain, and the predicted cursor.
final class PredictionOverlayView: UIView {
    private var current = PredictionRender.empty
    private var cell = CGSize(width: 8, height: 16)
    private var font = UIFont.monospacedSystemFont(ofSize: 13, weight: .regular)
    private var scrolledBack = false

    override init(frame: CGRect) {
        super.init(frame: frame)
        isOpaque = false
        backgroundColor = .clear
        contentMode = .redraw
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    func hidden(whenScrolledBack back: Bool) {
        scrolledBack = back
        isHidden = back || current.cells.isEmpty && current.cursor == nil
    }

    func render(_ r: PredictionRender, in view: TerminalView, hidden: Bool) {
        current = r
        let t = view.getTerminal()
        let frame = view.getOptimalFrameSize()
        if t.cols > 0, t.rows > 0 {
            cell = CGSize(width: frame.width / CGFloat(t.cols), height: frame.height / CGFloat(t.rows))
        }
        font = view.font
        scrolledBack = hidden
        isHidden = hidden || (r.cells.isEmpty && r.cursor == nil)
        setNeedsDisplay()
    }

    override func draw(_ rect: CGRect) {
        guard !scrolledBack, let ctx = UIGraphicsGetCurrentContext() else { return }
        let fg = UIColor.label
        let bg = UIColor.black
        for run in current.cells {
            for (i, ch) in run.cells.enumerated() {
                let r = CGRect(x: CGFloat(run.col + i) * cell.width, y: CGFloat(run.row) * cell.height,
                               width: cell.width, height: cell.height)
                ctx.setFillColor(bg.cgColor)
                ctx.fill(r)
                var attrs: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: fg]
                if run.underline { attrs[.underlineStyle] = NSUnderlineStyle.single.rawValue }
                (ch as NSString).draw(at: r.origin, withAttributes: attrs)
            }
        }
        if let c = current.cursor {
            let r = CGRect(x: CGFloat(c.col) * cell.width, y: CGFloat(c.row) * cell.height, width: cell.width, height: cell.height)
            ctx.setFillColor(UIColor.systemOrange.withAlphaComponent(0.55).cgColor)
            ctx.fill(r)
        }
    }
}
