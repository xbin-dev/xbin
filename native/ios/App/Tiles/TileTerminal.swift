import Foundation
import Observation
import SwiftTerm
import SwiftUI
import UIKit
import XbinCore
import XbinRenderer
import XbinTerm

// A native tile's `terminal` element (plans/native.md §8.5): SwiftTerm on
// the tile's OWN backend's pty WebSocket, connected as the tile — the
// socket goes through xbind's proxy (`/api/<self>/<src>`) with the tile's
// frame token as `?frame=`, never the user's session. XbinTerm's
// TilePtySession runs the socket (the /ws/term framing: bytes + resize).
// Inline it is a live, read-only view of the output; typing happens in a
// large sheet over the tile, with the keyboard and an accessory row.

/// One tile pty socket for TilePtySession: URLSessionWebSocketTask to
/// `ws(s)://<host>/api/<self>/<src>?frame=<token>`, no cookies, no bearer.
/// Events on the main actor, never from inside start/send/close.
@MainActor
final class TileSocketTransport: TermTransport {
    private let workspace: WorkspaceModel
    private let tile: String
    private let path: String
    private let events: @MainActor (TermTransportEvent) -> Void
    private var session: URLSession?
    private var task: URLSessionWebSocketTask?
    private var opened = false
    private var finished = false

    init(workspace: WorkspaceModel, tile: String, path: String, events: @escaping @MainActor (TermTransportEvent) -> Void) {
        self.workspace = workspace
        self.tile = tile
        self.path = path
        self.events = events
    }

    func start() {
        let tokens = workspace.frameTokens
        let tile = self.tile
        Task { @MainActor [weak self] in
            do {
                let token = try await tokens.token(for: tile)
                self?.open(token: token)
            } catch {
                self?.finish(.unreachable(error.localizedDescription))
            }
        }
    }

    private func open(token: String) {
        guard !finished else { return }
        guard let url = TileResource.socketURL(origin: workspace.origin, path: path, frameToken: token) else {
            finish(.failed)
            return
        }
        var req = URLRequest(url: url)
        req.setValue(AppInfo.clientHeader, forHTTPHeaderField: TileScheme.clientHeader)
        req.timeoutInterval = 20
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        let s = URLSession(configuration: config, delegate: TileSocketDelegate(owner: self), delegateQueue: .main)
        session = s
        let t = s.webSocketTask(with: req)
        t.maximumMessageSize = 16 << 20
        task = t
        t.resume()
        receive()
    }

    func send(_ msg: TermWireMessage) {
        guard let task, opened, !finished else { return }
        switch msg {
        case .text(let s): task.send(.string(s)) { _ in }
        case .binary(let b): task.send(.data(Data(b))) { _ in }
        }
    }

    func close() {
        finished = true
        task?.cancel(with: .normalClosure, reason: nil)
        session?.invalidateAndCancel()
        task = nil
        session = nil
    }

    private func receive() {
        task?.receive { [weak self] result in
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    guard let self, !self.finished else { return }
                    switch result {
                    case .success(.string(let s)):
                        self.events(.message(.text(s)))
                        self.receive()
                    case .success(.data(let d)):
                        self.events(.message(.binary([UInt8](d))))
                        self.receive()
                    case .success:
                        self.receive()
                    case .failure:
                        break // didComplete/didClose says why
                    }
                }
            }
        }
    }

    fileprivate func didOpen() {
        guard !finished else { return }
        opened = true
        events(.opened)
    }

    /// The backend (or xbind) sent a close frame: its code tells a pty
    /// that ended (1000, or none) from one to reconnect to (TilePtySession).
    fileprivate func didClose(code: Int) { finish(opened ? .openSocketClosed(code: code) : .dropped) }

    /// `closeCode`: the task's, when a close frame came (0 when none did) —
    /// URLSession may report the end here without calling didClose first.
    fileprivate func didComplete(status: Int?, closeCode: Int, error: (any Error)?) {
        if opened { finish(.openSocketClosed(code: closeCode)); return }
        if let status, status >= 300 { finish(.refused(status: status, body: "")); return }
        if let error { finish(.unreachable(error.localizedDescription)); return }
        finish(.failed)
    }

    private func finish(_ why: TermCloseInfo) {
        guard !finished else { return }
        finished = true
        events(.closed(why))
        session?.invalidateAndCancel()
    }
}

/// URLSession's delegate for one tile socket (callbacks on the main queue).
private final class TileSocketDelegate: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    weak var owner: TileSocketTransport?
    init(owner: TileSocketTransport) { self.owner = owner }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        MainActor.assumeIsolated { owner?.didOpen() }
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask,
                    didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        let code = closeCode.rawValue
        MainActor.assumeIsolated { owner?.didClose(code: code) }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        let status = (task.response as? HTTPURLResponse)?.statusCode
        let closeCode = (task as? URLSessionWebSocketTask)?.closeCode.rawValue ?? 0
        let message = error.map { $0 as NSError }
        MainActor.assumeIsolated { owner?.didComplete(status: status, closeCode: closeCode, error: message) }
    }
}

// MARK: - The controller

/// One `terminal` element: SwiftTerm + the tile's pty session. Lives while
/// its node does (TileHatches keeps it per node key), so re-renders and
/// expanding keep the screen and the socket.
@MainActor
@Observable
final class TileTerminalController: NSObject {
    let workspace: WorkspaceModel
    let tile: String
    let src: String
    let canOpenLinks: Bool
    var requestTitle: String?
    @ObservationIgnored let terminalView: TileTerminalView
    @ObservationIgnored let container = UIView()
    @ObservationIgnored var keyboard = TermKeyboard()
    @ObservationIgnored private var session: TilePtySession?
    @ObservationIgnored private var renewedToken = false
    @ObservationIgnored private let path: String?

    var phase: TilePtySession.Phase = .idle
    var screenTitle = ""
    var expanded = false
    var pendingLink: URL?
    /// Bumped when a sticky modifier changes (the accessory row redraws).
    var stickyRevision = 0

    init(workspace: WorkspaceModel, tile: String, request: XbinTerminalRequest, known: [String], canOpenLinks: Bool) {
        self.workspace = workspace
        self.tile = tile
        src = request.src
        requestTitle = request.title
        self.canOpenLinks = canOpenLinks
        path = TileResource.apiPath(request.src, tile: tile, known: known)
        var options = TerminalOptions.default
        options.scrollback = 5_000
        terminalView = TileTerminalView(frame: CGRect(x: 0, y: 0, width: 360, height: 240),
                                        font: UIFont.monospacedSystemFont(ofSize: 12, weight: .regular),
                                        options: options)
        super.init()
        terminalView.controller = self
        terminalView.terminalDelegate = self
        terminalView.inputAccessoryView = TileTermAccessory(controller: self)
        container.backgroundColor = .black
        terminalView.translatesAutoresizingMaskIntoConstraints = false
        container.addSubview(terminalView)
        NSLayoutConstraint.activate([
            terminalView.leadingAnchor.constraint(equalTo: container.leadingAnchor),
            terminalView.trailingAnchor.constraint(equalTo: container.trailingAnchor),
            terminalView.topAnchor.constraint(equalTo: container.topAnchor),
            terminalView.bottomAnchor.constraint(equalTo: container.bottomAnchor),
        ])
    }

    var displayTitle: String {
        if !screenTitle.isEmpty { return screenTitle }
        if let t = requestTitle, !t.isEmpty { return t }
        return "terminal"
    }

    /// The src is not one of the tile's own sockets.
    var refused: Bool { path == nil }

    /// A line under the title: connecting, reconnecting, ended, refused.
    var note: String? {
        if refused { return "This terminal's address is outside the tile's own API." }
        switch phase {
        case .idle, .connecting(0): return "Connecting…"
        case .connecting, .reconnecting: return "Reconnecting…"
        case .live, .closed: return nil
        case .exited: return "The terminal ended."
        case .failed(.unauthorized): return "Signed out — reconnect to try again."
        case .failed(.refused(let status)):
            return status == 403 ? "This tile may not open this terminal (403)." : "The tile refused the terminal (\(status))."
        case .failed(.disconnected): return "Disconnected."
        }
    }

    var canReconnect: Bool {
        if refused { return false }
        switch phase {
        case .failed, .exited: return true
        default: return false
        }
    }

    func startIfNeeded() {
        guard session == nil, let path else { return }
        let ws = workspace
        let tile = self.tile
        let s = TilePtySession(emulator: self, clock: TermSystemClock()) { events in
            TileSocketTransport(workspace: ws, tile: tile, path: path, events: events)
        }
        s.delegate = self
        session = s
        s.start()
    }

    func reconnect() {
        renewedToken = false
        if session == nil { startIfNeeded() } else { session?.reconnect() }
    }

    func stop() {
        session?.close()
        session = nil
        _ = terminalView.resignFirstResponder()
    }

    func focus() { _ = terminalView.becomeFirstResponder() }

    func unfocus() { _ = terminalView.resignFirstResponder() }

    // MARK: input

    func accessory(_ key: AccessoryKey) {
        let app = terminalView.getTerminal().applicationCursor
        if let bytes = keyboard.accessory(key, applicationCursor: app) { session?.send(bytes) }
        stickyChanged()
    }

    /// Soft-keyboard text with a sticky ctrl/alt becomes a control sequence here.
    func handleText(_ text: String) -> Bool {
        guard !keyboard.sticky.active.isEmpty else { return false }
        session?.send(keyboard.text(text))
        stickyChanged()
        return true
    }

    private func stickyChanged() {
        stickyRevision += 1
        (terminalView.inputAccessoryView as? TileTermAccessory)?.refresh()
    }
}

extension TileTerminalController: TermEmulator {
    func write(_ bytes: [UInt8]) { terminalView.feed(byteArray: bytes[...]) }
    func afterParsed(_ body: @escaping @MainActor () -> Void) { body() }
    func reset() { terminalView.getTerminal().resetToInitialState() }
    /// No predictive echo on a tile's pty (no acks): no framebuffer.
    var framebuffer: (any TermFramebuffer)? { nil }
    var size: TermSize {
        let t = terminalView.getTerminal()
        return TermSize(cols: t.cols, rows: t.rows)
    }
}

extension TileTerminalController: TilePtyDelegate {
    func tilePty(_ p: TilePtySession, phaseChanged phase: TilePtySession.Phase) {
        self.phase = phase
        // A refused upgrade with 401: the frame token died with the session
        // behind it. Renew it once and try again.
        if case .failed(.unauthorized) = phase, !renewedToken {
            renewedToken = true
            let tokens = workspace.frameTokens
            let tile = self.tile
            Task { @MainActor [weak self] in
                _ = try? await tokens.renew(tile)
                self?.session?.reconnect()
            }
        }
        if phase == .live { renewedToken = false }
    }
}

extension TileTerminalController: TerminalViewDelegate {
    nonisolated func send(source: TerminalView, data: ArraySlice<UInt8>) {
        let bytes = Array(data)
        MainActor.assumeIsolated { self.session?.send(bytes) }
    }

    nonisolated func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        MainActor.assumeIsolated { self.session?.resized(TermSize(cols: newCols, rows: newRows)) }
    }

    nonisolated func setTerminalTitle(source: TerminalView, title: String) {
        MainActor.assumeIsolated { self.screenTitle = title }
    }

    nonisolated func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}

    nonisolated func scrolled(source: TerminalView, position: Double) {}

    /// A link in the output: opened in Safari after a confirmation, and only
    /// when the tile may open links (cap:open-links, ND11).
    nonisolated func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        guard let u = URL(string: link), ["http", "https"].contains(u.scheme?.lowercased() ?? "") else { return }
        MainActor.assumeIsolated { if self.canOpenLinks { self.pendingLink = u } }
    }

    nonisolated func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}

    nonisolated func clipboardCopy(source: TerminalView, content: Data) {
        let s = String(decoding: content, as: UTF8.self)
        MainActor.assumeIsolated { UIPasteboard.general.string = s }
    }
}

/// SwiftTerm's view, with sticky ctrl/alt from the accessory row.
final class TileTerminalView: TerminalView {
    weak var controller: TileTerminalController?

    override func insertText(_ text: String) {
        if controller?.handleText(text) != true { super.insertText(text) }
    }
}

/// The keyboard accessory for a tile's terminal: the designed row (esc,
/// sticky ctrl, tab, arrows, | ~ / -), without the shell terminal's
/// customizable slot.
@MainActor
final class TileTermAccessory: UIInputView {
    private weak var controller: TileTerminalController?
    private var buttons: [(AccessoryKey, UIButton)] = []

    init(controller: TileTerminalController) {
        self.controller = controller
        super.init(frame: CGRect(x: 0, y: 0, width: 320, height: 46), inputViewStyle: .keyboard)
        allowsSelfSizing = true
        let stack = UIStackView()
        stack.axis = .horizontal
        stack.distribution = .fillEqually
        stack.spacing = 5
        stack.translatesAutoresizingMaskIntoConstraints = false
        addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: layoutMarginsGuide.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: layoutMarginsGuide.trailingAnchor),
            stack.topAnchor.constraint(equalTo: topAnchor, constant: 5),
            stack.bottomAnchor.constraint(equalTo: bottomAnchor, constant: -5),
            heightAnchor.constraint(equalToConstant: 46),
        ])
        for key in AccessoryKey.defaultRow {
            var cfg = UIButton.Configuration.gray()
            cfg.title = key.label
            cfg.contentInsets = NSDirectionalEdgeInsets(top: 4, leading: 2, bottom: 4, trailing: 2)
            let b = UIButton(configuration: cfg)
            b.accessibilityLabel = key.label
            b.addAction(UIAction { [weak self] _ in self?.tap(key) }, for: .touchUpInside)
            buttons.append((key, b))
            stack.addArrangedSubview(b)
        }
        refresh()
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    private func tap(_ key: AccessoryKey) {
        UIDevice.current.playInputClick()
        controller?.accessory(key)
    }

    /// Sticky ctrl shows its state: off, once (tinted), locked (filled).
    func refresh() {
        guard let kb = controller?.keyboard else { return }
        for (key, b) in buttons {
            guard case .modifier(let m) = key else { continue }
            var cfg = b.configuration ?? .gray()
            switch kb.sticky.state(m) {
            case .off: cfg.baseBackgroundColor = nil; cfg.baseForegroundColor = nil
            case .once: cfg.baseBackgroundColor = .systemOrange.withAlphaComponent(0.35); cfg.baseForegroundColor = .label
            case .locked: cfg.baseBackgroundColor = .systemOrange; cfg.baseForegroundColor = .black
            }
            b.configuration = cfg
        }
    }
}

extension TileTermAccessory: UIInputViewAudioFeedback {
    var enableInputClicksWhenVisible: Bool { true }
}

// MARK: - The views

/// Hosts the controller's terminal. Only one host is on screen at a time
/// (inline or expanded); whichever is made or updated takes the view.
struct TileTerminalHost: UIViewRepresentable {
    let controller: TileTerminalController

    func makeUIView(context: Context) -> UIView {
        let v = UIView()
        v.backgroundColor = .black
        adopt(into: v)
        return v
    }

    func updateUIView(_ uiView: UIView, context: Context) {
        if controller.container.superview !== uiView { adopt(into: uiView) }
    }

    private func adopt(into v: UIView) {
        let c = controller.container
        c.removeFromSuperview()
        c.translatesAutoresizingMaskIntoConstraints = false
        v.addSubview(c)
        NSLayoutConstraint.activate([
            c.leadingAnchor.constraint(equalTo: v.leadingAnchor),
            c.trailingAnchor.constraint(equalTo: v.trailingAnchor),
            c.topAnchor.constraint(equalTo: v.topAnchor),
            c.bottomAnchor.constraint(equalTo: v.bottomAnchor),
        ])
    }
}

/// The element inline: a title bar and the live output (read-only); a tap
/// expands it for typing.
struct TileTerminalElement: View {
    @Bindable var controller: TileTerminalController

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 6) {
                Image(systemName: "apple.terminal")
                Text(verbatim: controller.displayTitle).lineLimit(1)
                Spacer(minLength: 4)
                if controller.phase.isLive {
                    Circle().fill(Color.green).frame(width: 6, height: 6).accessibilityLabel("Connected")
                }
                Image(systemName: "arrow.up.left.and.arrow.down.right").accessibilityHidden(true)
            }
            .font(.caption)
            .foregroundStyle(Color.white.opacity(0.75))
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            ZStack(alignment: .topLeading) {
                if !controller.expanded {
                    TileTerminalHost(controller: controller)
                        .allowsHitTesting(false)
                }
                if let note = controller.note {
                    Text(verbatim: note)
                        .font(.caption)
                        .foregroundStyle(Color.white.opacity(0.7))
                        .padding(8)
                        .background(Color.black.opacity(0.6), in: RoundedRectangle(cornerRadius: 6))
                        .padding(8)
                }
            }
            .frame(maxWidth: .infinity, minHeight: 200, maxHeight: .infinity)
        }
        .background(Color.black, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .contentShape(Rectangle())
        .onTapGesture { controller.expanded = true }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isButton)
        .accessibilityHint("Opens the terminal for typing")
        .task { controller.startIfNeeded() }
        // A large sheet, not a full-screen cover: a cover takes the tile's
        // screen off the hierarchy (its onDisappear stops the runtime).
        .sheet(isPresented: $controller.expanded) {
            TileTerminalFullScreen(controller: controller)
                .presentationDetents([.large])
                .interactiveDismissDisabled()
        }
    }
}

/// The element expanded (a large sheet): the keyboard and the accessory row.
struct TileTerminalFullScreen: View {
    @Bindable var controller: TileTerminalController
    @Environment(\.openURL) private var openURL

    var body: some View {
        NavigationStack {
            ZStack(alignment: .top) {
                Color.black.ignoresSafeArea()
                if controller.expanded {
                    TileTerminalHost(controller: controller)
                }
                if let note = controller.note {
                    Text(verbatim: note)
                        .font(.footnote)
                        .padding(.horizontal, 12).padding(.vertical, 6)
                        .background(.thinMaterial, in: Capsule())
                        .padding(.top, 8)
                }
            }
            .navigationTitle(Text(verbatim: controller.displayTitle))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        controller.unfocus()
                        controller.expanded = false
                    }
                }
                ToolbarItem(placement: .primaryAction) {
                    if controller.canReconnect {
                        Button("Reconnect", systemImage: "arrow.clockwise") { controller.reconnect() }
                    }
                }
            }
            .task {
                try? await Task.sleep(for: .milliseconds(300))
                controller.focus()
            }
            .alert("Open this link?", isPresented: Binding(get: { controller.pendingLink != nil },
                                                          set: { if !$0 { controller.pendingLink = nil } })) {
                Button("Cancel", role: .cancel) { controller.pendingLink = nil }
                Button("Open") {
                    if let u = controller.pendingLink { openURL(u) }
                    controller.pendingLink = nil
                }
            } message: {
                Text(verbatim: controller.pendingLink?.absoluteString ?? "")
            }
        }
    }
}
