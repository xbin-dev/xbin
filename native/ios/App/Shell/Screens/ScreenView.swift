import SwiftUI
import XbinCore
import XbinRenderer

/// A screen on the phone, the second panel (plans/native.md §4, D125): its
/// tiles as cards in two columns — small (one) or wide (both) — in the
/// user's phone arrangement (MobileScreens' merge rule). A card is the
/// tile's own widget when it draws one, else the standard card (TileCard);
/// a tap opens the tile as the next panel, a tap on a widget's own control
/// goes to the widget. Edit: reorder, small/wide, hide from this phone,
/// add a tile, create one.
struct ScreenView: View {
    let workspace: WorkspaceModel
    let screenID: String

    @Environment(WorkspaceNav.self) private var nav
    @Environment(\.panelActive) private var active
    @State private var editor: ScreenEditor?
    @State private var problem: String?

    /// A card's height; a wide one is as tall, twice as wide (D128: compact).
    static let cardHeight: CGFloat = XbinWidgetMetrics.cardHeight
    static let spacing: CGFloat = XbinWidgetMetrics.spacing
    static let corner: CGFloat = XbinWidgetMetrics.cornerRadius

    private var screen: ScreenInfo? { workspace.home.screen(screenID) }

    var body: some View {
        Group {
            if let s = screen {
                if let editor {
                    ScreenEditorView(workspace: workspace, screen: s, editor: editor, done: { save(editor) })
                } else {
                    grid(s)
                }
            } else if !workspace.homeLoaded {
                ProgressView().controlSize(.large)
            } else {
                ContentUnavailableView("This screen is gone", systemImage: "square.dashed",
                                       description: Text("It was removed, or you no longer see it."))
            }
        }
        .navigationTitle(Text(verbatim: screen?.name ?? "Screen"))
        .toolbar {
            if let s = screen {
                ToolbarItem(placement: .primaryAction) {
                    if let editor {
                        Button("Done") { save(editor) }.fontWeight(.semibold)
                    } else {
                        Button("Edit") { self.editor = ScreenEditor(screen: s, cards: workspace.cards(for: s),
                                                                     hidden: workspace.mobile.hidden(for: s.id, screenTiles: s.tiles)) }
                    }
                }
            }
        }
        // Leaving the panel mid-edit (a swipe back) keeps the edit.
        .onChange(of: active) { _, now in if !now, let editor { save(editor) } }
        .alert("Couldn't save the screen", isPresented: Binding(get: { problem != nil }, set: { if !$0 { problem = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(verbatim: problem ?? "")
        }
    }

    private func save(_ e: ScreenEditor) {
        editor = nil
        Task {
            do {
                try await workspace.saveArrangement(e.screen.id, cards: e.cards, hidden: e.hidden)
            } catch {
                problem = workspace.describe(error)
            }
        }
    }

    // MARK: The grid

    @ViewBuilder private func grid(_ s: ScreenInfo) -> some View {
        let cards = workspace.cards(for: s)
        ScrollView {
            if cards.isEmpty {
                ContentUnavailableView {
                    Label("No tiles here", systemImage: "square.grid.2x2")
                } description: {
                    Text("Edit the screen to add tiles, or create one.")
                } actions: {
                    Button("Edit") { editor = ScreenEditor(screen: s, cards: [], hidden: workspace.mobile.hidden(for: s.id, screenTiles: s.tiles)) }
                        .buttonStyle(.borderedProminent)
                }
                .padding(.top, 60)
            }
            LazyVStack(spacing: Self.spacing) {
                ForEach(Self.rows(cards)) { row in
                    HStack(spacing: Self.spacing) {
                        ForEach(row.cards) { c in card(c) }
                        if row.cards.count == 1, row.cards[0].size == .small { Color.clear.frame(maxWidth: .infinity) }
                    }
                    .frame(height: Self.cardHeight)
                }
            }
            .padding(XbinWidgetMetrics.margin)
        }
        .background(Color(uiColor: .systemGroupedBackground))
        .refreshable { await workspace.refresh() }
    }

    @ViewBuilder private func card(_ c: MobileScreens.Card) -> some View {
        let tile = workspace.tile(c.path) ?? TileInfo(path: c.path)
        TileCard(tile: tile, size: c.size, workspace: workspace)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Color(uiColor: .secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: Self.corner, style: .continuous))
            .clipShape(RoundedRectangle(cornerRadius: Self.corner, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: Self.corner, style: .continuous))
            .onTapGesture { workspace.open(.tile(c.path), in: nav) }
            .contextMenu { TileMenu(workspace: workspace, tile: tile) { workspace.open($0, in: nav) } }
            .accessibilityElement(children: .contain)
            .accessibilityLabel(Text(verbatim: tile.title))
            .accessibilityAddTraits(.isButton)
            .accessibilityAction { workspace.open(.tile(c.path), in: nav) }
            .accessibilityIdentifier("card:\(c.path)")
    }

    /// Cards in rows of two columns, in order: two small ones share a row,
    /// a wide one has its own (a small one before it stays alone).
    struct Row: Identifiable {
        var cards: [MobileScreens.Card]
        var id: String { cards.map(\.path).joined(separator: "|") }
    }

    static func rows(_ cards: [MobileScreens.Card]) -> [Row] {
        var rows: [Row] = []
        var pending: MobileScreens.Card?
        for c in cards {
            if c.size == .wide {
                if let p = pending { rows.append(Row(cards: [p])); pending = nil }
                rows.append(Row(cards: [c]))
            } else if let p = pending {
                rows.append(Row(cards: [p, c]))
                pending = nil
            } else {
                pending = c
            }
        }
        if let p = pending { rows.append(Row(cards: [p])) }
        return rows
    }
}

/// A screen being edited: the cards in order with their sizes, and what is
/// kept off this phone.
@MainActor
@Observable
final class ScreenEditor {
    let screen: ScreenInfo
    var cards: [MobileScreens.Card]
    var hidden: [String]

    init(screen: ScreenInfo, cards: [MobileScreens.Card], hidden: [String]) {
        self.screen = screen
        self.cards = cards
        self.hidden = hidden
    }

    func hide(_ path: String) {
        cards.removeAll { $0.path == path }
        // A tile of the screen stays hidden; one added on the phone just goes.
        if screen.tiles.contains(path), !hidden.contains(path) { hidden.append(path) }
    }

    func show(_ path: String) {
        hidden.removeAll { $0 == path }
        if !cards.contains(where: { $0.path == path }) { cards.append(MobileScreens.Card(path: path)) }
    }

    func add(_ path: String) { show(path) }

    func setSize(_ path: String, _ size: CardSize) {
        if let i = cards.firstIndex(where: { $0.path == path }) { cards[i].size = size }
    }
}

/// Edit mode: the cards as a list — drag to reorder, small or wide, hide;
/// then + Add tile (one that exists) and + Create tile.
struct ScreenEditorView: View {
    let workspace: WorkspaceModel
    let screen: ScreenInfo
    @Bindable var editor: ScreenEditor
    let done: () -> Void

    @Environment(WorkspaceNav.self) private var nav
    @State private var adding = false
    @State private var creating = false

    var body: some View {
        List {
            Section {
                ForEach(editor.cards) { c in row(c) }
                    .onMove { editor.cards.move(fromOffsets: $0, toOffset: $1) }
            } header: {
                Text("On this phone")
            } footer: {
                Text("Drag to reorder. Hiding a tile takes it off this phone's screen only.")
            }
            if !editor.hidden.isEmpty {
                Section("Hidden on this phone") {
                    ForEach(editor.hidden, id: \.self) { p in
                        HStack {
                            Text(verbatim: workspace.tile(p)?.title ?? TileInfo.humanize(p)).foregroundStyle(.secondary)
                            Spacer()
                            Button("Show") { withAnimation { editor.show(p) } }.buttonStyle(.borderless)
                        }
                    }
                }
            }
            Section {
                // (Rows in edit mode don't take taps: borderless buttons do.)
                Button { adding = true } label: { wideLabel("Add tile", "plus.square.on.square") }
                Button { creating = true } label: { wideLabel("Create tile", "plus") }
            }
            .buttonStyle(.borderless)
        }
        .environment(\.editMode, .constant(.active))
        .sheet(isPresented: $adding) {
            AddTileSheet(workspace: workspace, excluded: Set(editor.cards.map(\.path))) { p in
                withAnimation { editor.add(p) }
            }
        }
        .sheet(isPresented: $creating) {
            CreateTileSheet(workspace: workspace, screen: screen) { path in
                // On this screen, saved, and open on "What should this be?".
                editor.add(path)
                done()
                nav.open(.build(tile: path), on: screen.id)
            }
        }
    }

    private func wideLabel(_ title: LocalizedStringKey, _ symbol: String) -> some View {
        Label(title, systemImage: symbol).frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
    }

    private func row(_ c: MobileScreens.Card) -> some View {
        let title = workspace.tile(c.path)?.title ?? TileInfo.humanize(c.path)
        return HStack(spacing: 10) {
            Button { withAnimation { editor.hide(c.path) } } label: {
                Image(systemName: "minus.circle.fill").symbolRenderingMode(.palette).foregroundStyle(.white, .red).font(.title3)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(Text("Hide \(title)"))
            Text(verbatim: title).lineLimit(1)
            Spacer(minLength: 4)
            Picker(selection: Binding(get: { c.size }, set: { editor.setSize(c.path, $0) })) {
                Text("Small").tag(CardSize.small)
                Text("Wide").tag(CardSize.wide)
            } label: {
                Text("Size of \(title)")
            }
            .pickerStyle(.segmented)
            .fixedSize()
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(Text(verbatim: title))
    }
}
