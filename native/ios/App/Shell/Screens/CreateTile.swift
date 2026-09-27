import SwiftUI
import XbinAgent
import XbinCore

/// "+ Create tile" (D125): a name and an owner — the choices the web
/// shell offers (TileCreate.owners) — then `POST /api/xbin/create`. For a
/// personal screen the tile also lands on the screen's web layout; the
/// caller puts it on the phone's and opens it on the build chooser.
struct CreateTileSheet: View {
    let workspace: WorkspaceModel
    let screen: ScreenInfo
    let created: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var owner = ""
    @State private var error: String?
    @State private var working = false
    @FocusState private var focused: Bool

    var body: some View {
        let who = workspace.whoami ?? Whoami(json: [:])
        let owners = TileCreate.owners(who)
        NavigationStack {
            Form {
                Section {
                    TextField("Tile name", text: $name)
                        .focused($focused)
                        .submitLabel(.done)
                        .onSubmit { Task { await create() } }
                } footer: {
                    if let p = TileCreate.tilePath(name: name) {
                        Text(verbatim: p).font(.caption.monospaced())
                    }
                }
                if owners.count > 1 {
                    Section {
                        Picker("Owner", selection: $owner) {
                            ForEach(owners) { o in Text(verbatim: o.label).tag(o.value) }
                        }
                    } footer: {
                        Text("Personal tiles: capability requests (net, containers, ports) need a workspace admin. Org-owned: the org's admins can approve within their allowance.")
                    }
                }
                if let error {
                    Section { Label { Text(verbatim: error) } icon: { Image(systemName: "exclamationmark.triangle") }.foregroundStyle(.red) }
                }
            }
            .navigationTitle("New tile")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if working {
                        ProgressView()
                    } else {
                        Button("Create") { Task { await create() } }
                            .disabled(TileCreate.tilePath(name: name) == nil)
                    }
                }
            }
            .onAppear {
                owner = TileCreate.defaultOwner(who)
                focused = true
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func create() async {
        guard !working, TileCreate.tilePath(name: name) != nil else { return }
        working = true
        defer { working = false }
        do {
            let path = try await workspace.createTile(name: name, owner: owner, onScreen: screen.id)
            dismiss()
            created(path)
        } catch {
            self.error = workspace.describe(error)
        }
    }
}

/// "+ Add tile": a tile that exists, onto this phone's screen.
struct AddTileSheet: View {
    let workspace: WorkspaceModel
    let excluded: Set<String>
    let add: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        NavigationStack {
            List {
                ForEach(workspace.catalog.search(query).filter { !excluded.contains($0.path) }) { t in
                    Button {
                        add(t.path)
                        dismiss()
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(verbatim: t.title).foregroundStyle(.primary)
                            Text(verbatim: t.path).font(.caption.monospaced()).foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .searchable(text: $query, prompt: "Search tiles")
            .navigationTitle("Add a tile")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }
    }
}

/// A tile just made on the phone opens here: "What should this tile be?"
/// A prompt and an agent (GET /api/xbin/agent/providers) — the agent
/// session starts in the tile with the prompt as its first message — or a
/// terminal there instead, or the tile as it is.
struct BuildChooser: View {
    let workspace: WorkspaceModel
    let tile: String

    @Environment(WorkspaceNav.self) private var nav
    @State private var prompt = ""
    @State private var providers: [AgentProvider] = []
    @State private var provider: String?
    @State private var loaded = false
    @State private var error: String?
    @State private var starting = false

    var body: some View {
        Form {
            Section {
                TextField("Describe what it should do and show…", text: $prompt, axis: .vertical)
                    .lineLimit(3...10)
            } header: {
                Text("What should this tile be?").font(.title3.weight(.semibold)).foregroundStyle(.primary).textCase(nil)
                    .padding(.bottom, 4)
            } footer: {
                Text("An agent builds it in \(tile) — you can watch and steer it as it works.")
            }
            Section("Agent") {
                if providers.isEmpty {
                    if loaded {
                        Text("No agents are set up in this workspace.").foregroundStyle(.secondary)
                    } else {
                        ProgressView()
                    }
                }
                ForEach(providers) { p in
                    Button { provider = p.id } label: {
                        HStack {
                            Text(verbatim: p.name).foregroundStyle(.primary)
                            Spacer()
                            if provider == p.id { Image(systemName: "checkmark").foregroundStyle(Color.xbinAmber) }
                        }
                    }
                    .tint(.primary) // a choice, not an action
                    .accessibilityAddTraits(provider == p.id ? .isSelected : [])
                }
            }
            if let error {
                Section { Label { Text(verbatim: error) } icon: { Image(systemName: "exclamationmark.triangle") }.foregroundStyle(.red) }
            }
            Section {
                Button {
                    Task { await start() }
                } label: {
                    HStack {
                        Label("Start building", systemImage: "sparkles")
                        if starting { Spacer(); ProgressView() }
                    }
                }
                .disabled(prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || provider == nil || starting)
            }
            Section {
                Button { nav.replace(with: .terminal(cwd: tile, session: nil)) } label: {
                    Label("Open a terminal instead", systemImage: "apple.terminal")
                }
                Button { nav.replace(with: .tile(tile)) } label: {
                    Label("Just open the tile", systemImage: "square")
                }
            }
        }
        .navigationTitle(Text(verbatim: workspace.tile(tile)?.title ?? TileInfo.humanize(tile)))
        .task {
            guard !loaded else { return }
            do {
                providers = try await workspace.agents.providers()
                provider = provider ?? providers.first?.id
            } catch {
                self.error = workspace.describe(error)
            }
            loaded = true
        }
    }

    /// Starts the session and hands the prompt to its screen (which sends
    /// it once attached — and keeps it as a draft if it can't).
    private func start() async {
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let provider, !text.isEmpty, !starting else { return }
        starting = true
        defer { starting = false }
        do {
            let info = try await workspace.agents.create(CreateAgentSession(cwd: tile, provider: provider))
            workspace.setPendingPrompt(text, session: info.id)
            nav.replace(with: .agent(cwd: tile, session: info.id))
        } catch {
            self.error = workspace.describe(error)
        }
    }
}
