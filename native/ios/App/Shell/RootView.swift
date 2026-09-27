import SwiftUI
import UIKit
import XbinCore

/// One window's root: its workspace full screen (the Welcome when there is
/// none, Onboarding.swift), the switcher over it (a two-finger swipe down,
/// a tap on the title, ⌘1…⌘9 — never a docked rail, §4), the add-workspace
/// sheet, the lock. Every window (iPad, Stage
/// Manager, the Mac) has its own selection and navigation — windows are
/// tabs — kept across launches in `@SceneStorage`; a window opened with
/// `openWindow(value:)` (a dragged tile, "Open in New Window") starts on
/// that value.
struct RootView: View {
    /// The window's `WindowGroup(for:)` value (nil for the first window).
    @Binding var target: WindowTarget?

    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var phase
    @State private var scene = SceneModel()
    @State private var restored = false
    @SceneStorage("xbin.workspace") private var storedWorkspace = ""
    @SceneStorage("xbin.surface") private var storedTarget = ""

    var body: some View {
        @Bindable var scene = scene
        ZStack {
            if let w = scene.selected {
                WorkspaceView(workspace: w, nav: scene.nav(for: w))
                    .id(w.id)
            } else {
                WelcomeView()
            }
            if scene.showSwitcher {
                SwitcherOverlay()
                    .transition(.move(edge: .top).combined(with: .opacity))
                    .zIndex(1)
            }
            if app.locked {
                LockView().zIndex(2)
            }
        }
        .animation(.snappy, value: scene.showSwitcher)
        .background(TwoFingerSwipeDown { withAnimation { scene.showSwitcher = true } })
        .background { WorkspaceShortcuts() }
        .background(KeyWindowReporter { app.focus(scene) })
        .sheet(item: $scene.addRequest) { request in
            AddWorkspaceSheet(request: request).environment(scene)
        }
        .sheet(isPresented: $scene.showInbox) { InboxView().environment(scene) }
        .sheet(isPresented: $scene.showSettings) { SettingsView().environment(scene) }
        // Links and Handoff go to a window already open (the one in front)
        // rather than making a new one; a dragged tile still makes its own.
        .handlesExternalEvents(preferring: ["*"], allowing: ["*"])
        .onOpenURL { scene.open(url: $0) }
        .onContinueUserActivity(HandoffActivity.type) { activity in
            restored = true
            if let link = HandoffActivity.link(activity) { scene.open(link: link) }
        }
        .userActivity(HandoffActivity.type, isActive: AppSettings.handoff && scene.selected != nil) { activity in
            if let w = scene.selected { HandoffActivity.fill(activity, w, scene.existingNav(w.id)?.surface) }
        }
        .onAppear(perform: appeared)
        .onChange(of: phase) { _, p in phaseChanged(p) }
        .onChange(of: scene.current) { _, t in remember(t) }
        // Outermost, so the backgrounds (the ⌘ shortcuts) and the sheets
        // above see this window's model too.
        .environment(scene)
    }

    private func appeared() {
        if app.register(scene) { restored = true }
        phaseChanged(phase)
        guard !restored else { return }
        restored = true
        // This window's own last place, else the value it was opened with,
        // else where the user was last. After a run that ended in the
        // foreground (a crash), just the workspace until the remote switch
        // has been read (AppModel.cautiousRestore).
        if let t = WindowTarget(encoded: storedTarget), app.workspace(t.workspace) != nil {
            scene.show(t.restoring(afterUncleanExit: app.cautiousRestore))
        } else if !storedWorkspace.isEmpty, app.workspace(storedWorkspace) != nil {
            scene.select(storedWorkspace)
        } else if let t = target, app.workspace(t.workspace) != nil {
            scene.show(t.restoring(afterUncleanExit: app.cautiousRestore))
        } else if let id = app.lastSelectedID ?? app.workspaces.first?.id {
            scene.select(id)
        }
    }

    private func phaseChanged(_ p: ScenePhase) {
        scene.isForeground = p != .background
        if p == .active { app.focus(scene) }
        app.updateSockets()
    }

    private func remember(_ t: WindowTarget?) {
        storedWorkspace = t?.workspace ?? ""
        storedTarget = t?.encoded ?? ""
        // The window's value follows what it shows, so "open in a new
        // window" for a place already open brings that window forward.
        if target != t { target = t }
    }
}

/// ⌘1…⌘9 jump straight to a workspace (hardware keyboards).
private struct WorkspaceShortcuts: View {
    @Environment(SceneModel.self) private var scene

    var body: some View {
        ZStack {
            ForEach(0..<9, id: \.self) { i in
                Button("") { scene.select(index: i) }
                    .keyboardShortcut(KeyEquivalent(Character("\(i + 1)")), modifiers: .command)
            }
            Button("") { withAnimation { scene.showSwitcher.toggle() } }
                .keyboardShortcut("k", modifiers: [.command, .shift])
        }
        .opacity(0)
        .accessibilityHidden(true)
    }
}

/// One workspace in one window: its panels (PanelStack) — Home, a screen,
/// a tile, terminal or agent full screen. The window's navigation is in
/// the environment for the screens under it (`@Environment(WorkspaceNav.self)`).
struct WorkspaceView: View {
    let workspace: WorkspaceModel
    @Bindable var nav: WorkspaceNav
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene

    var body: some View {
        PanelStack(nav: nav, enabled: !scene.showSwitcher && !app.locked) { panel in
            switch panel {
            case .home:
                NavigationStack {
                    HomeView(workspace: workspace).modifier(PanelBar(workspace: workspace, level: 0))
                }
            case .screen(let id):
                NavigationStack {
                    ScreenView(workspace: workspace, screenID: id).modifier(PanelBar(workspace: workspace, level: 1))
                }
            case .surface(let s):
                NavigationStack(path: $nav.windows) {
                    surface(s)
                        .modifier(PanelBar(workspace: workspace, level: 2))
                        .navigationDestination(for: PushedWindow.self) { w in WindowScreen(workspace: workspace, window: w) }
                }
            }
        }
        .environment(nav)
        .overlay(alignment: .bottom) {
            if let p = workspace.signInProblem {
                SignInProblemBar(workspace: workspace, problem: p)
            }
        }
        .task { if workspace.whoami == nil { await workspace.refresh() } }
        .onChange(of: nav.surface) { _, s in
            if let s { app.visited(workspace, s, title: s.title) }
        }
    }

    @ViewBuilder private func surface(_ s: Surface) -> some View {
        switch s {
        case .tile(let path, let sub, let fragment):
            TileScreen(workspace: workspace, path: path, subpath: sub, fragment: fragment)
                .id(path + "|" + sub)
        case .terminal(let cwd, let session):
            TerminalScreen(workspace: workspace, cwd: cwd, sessionID: session)
                .id("term|" + cwd + "|" + (session ?? ""))
        case .agent(let cwd, let session):
            AgentScreen(workspace: workspace, cwd: cwd, sessionID: session)
                .id("agent|" + (cwd ?? "") + "|" + (session ?? ""))
        case .build(let tile):
            BuildChooser(workspace: workspace, tile: tile)
                .id("build|" + tile)
        }
    }
}

/// A panel's bar, leading side (D117): the workspace switcher — small, with
/// what needs you — then the way back: ▦ Home on a screen, ‹ the screen's
/// name on a tile. The title and the trailing items are the panel's own.
struct PanelBar: ViewModifier {
    let workspace: WorkspaceModel
    let level: Int
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(WorkspaceNav.self) private var nav

    func body(content: Content) -> some View {
        content
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { withAnimation { scene.showSwitcher = true } } label: {
                        HStack(spacing: 3) {
                            Image(systemName: "arrow.left.arrow.right").font(.footnote.weight(.semibold))
                            if app.needsYouCount > 0 {
                                Text(verbatim: "\(app.needsYouCount)").font(.caption2.bold())
                                    .padding(.horizontal, 5).padding(.vertical, 1)
                                    .background(Color.xbinAmber, in: Capsule()).foregroundStyle(.black)
                            }
                        }
                    }
                    .accessibilityLabel("Workspaces")
                    .accessibilityValue(app.needsYouCount > 0 ? Text("\(app.needsYouCount) need you") : Text(verbatim: workspace.title))
                }
                if level == 1 {
                    ToolbarItem(placement: .topBarLeading) {
                        Button { nav.back() } label: { Image(systemName: "square.grid.2x2") }
                            .accessibilityLabel("Home")
                            .accessibilityIdentifier("panel-home")
                    }
                } else if level == 2 {
                    ToolbarItem(placement: .topBarLeading) {
                        Button { nav.back() } label: {
                            HStack(spacing: 2) {
                                Image(systemName: "chevron.backward").font(.body.weight(.semibold))
                                Text(verbatim: backTitle).lineLimit(1)
                            }
                        }
                        .accessibilityLabel(Text(verbatim: backTitle))
                        .accessibilityIdentifier("panel-back")
                    }
                }
            }
    }

    /// Where back goes: the screen under the surface, or Home.
    private var backTitle: String {
        if case .screen(let id)? = nav.below { return workspace.home.screen(id)?.name ?? "Screen" }
        return "Home"
    }
}

/// Signing in needs the user: re-enroll, SSO, or just try again.
private struct SignInProblemBar: View {
    let workspace: WorkspaceModel
    let problem: SignInError
    @Environment(SceneModel.self) private var scene

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(verbatim: problem.description).font(.footnote)
            HStack {
                if problem.needsEnrollment || problem == .ssoRequired {
                    Button("Sign in again") {
                        scene.addRequest = .signInAgain(workspace: workspace.id, server: workspace.origin)
                    }
                    .buttonStyle(.borderedProminent)
                } else {
                    Button("Try again") { Task { await workspace.signIn() } }.buttonStyle(.borderedProminent)
                }
                Button("Dismiss") { workspace.signInProblem = nil }
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.regularMaterial)
    }
}

/// The workspace's branding icon (D76), or its initial.
struct BrandIcon: View {
    let workspace: WorkspaceModel
    var size: CGFloat = 28

    var body: some View {
        let title = workspace.title
        ZStack {
            RoundedRectangle(cornerRadius: size * 0.25).fill(Color.xbinAmber.opacity(0.9))
            if let icon = workspace.record.branding?.icon, icon.count <= 4, !icon.isEmpty {
                Text(verbatim: icon).font(.system(size: size * 0.6))
            } else {
                Text(verbatim: String(title.prefix(1)).uppercased())
                    .font(.system(size: size * 0.55, weight: .bold)).foregroundStyle(.black)
            }
        }
        .frame(width: size, height: size)
    }
}

/// "Require Face ID when opening the app".
private struct LockView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        ZStack {
            Rectangle().fill(.background).ignoresSafeArea()
            VStack(spacing: 16) {
                Image(systemName: "lock.fill").font(.largeTitle)
                Button("Unlock") { Task { await app.unlock() } }.buttonStyle(.borderedProminent)
            }
        }
    }
}

/// A two-finger swipe down anywhere opens the switcher: a recognizer on
/// the window, alongside everything else (web views and the terminal keep
/// their own gestures).
struct TwoFingerSwipeDown: UIViewRepresentable {
    let action: () -> Void

    func makeUIView(context: Context) -> Installer { Installer(action: action) }
    func updateUIView(_ uiView: Installer, context: Context) { uiView.action = action }

    final class Installer: UIView, UIGestureRecognizerDelegate {
        var action: () -> Void
        private var recognizer: UISwipeGestureRecognizer?

        init(action: @escaping () -> Void) {
            self.action = action
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if let r = recognizer { r.view?.removeGestureRecognizer(r) }
            guard let window else { return }
            let r = UISwipeGestureRecognizer(target: self, action: #selector(fire))
            r.direction = .down
            r.numberOfTouchesRequired = 2
            r.cancelsTouchesInView = false
            r.delegate = self
            window.addGestureRecognizer(r)
            recognizer = r
        }

        @objc private func fire() { action() }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
            true
        }
    }
}

/// Tells its window's model when the window becomes key (the user is in
/// it): code outside a window then acts on this one (AppModel.focus).
struct KeyWindowReporter: UIViewRepresentable {
    let becameKey: () -> Void

    func makeUIView(context: Context) -> Reporter { Reporter(becameKey: becameKey) }
    func updateUIView(_ uiView: Reporter, context: Context) { uiView.becameKey = becameKey }

    final class Reporter: UIView {
        var becameKey: () -> Void

        init(becameKey: @escaping () -> Void) {
            self.becameKey = becameKey
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        // A selector observer: NotificationCenter drops it with the view.
        override func didMoveToWindow() {
            super.didMoveToWindow()
            NotificationCenter.default.removeObserver(self, name: UIWindow.didBecomeKeyNotification, object: nil)
            guard let window else { return }
            NotificationCenter.default.addObserver(self, selector: #selector(keyChanged(_:)),
                                                   name: UIWindow.didBecomeKeyNotification, object: window)
            if window.isKeyWindow { becameKey() }
        }

        @objc private func keyChanged(_ note: Notification) { becameKey() }
    }
}
