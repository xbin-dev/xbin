import SwiftUI
import XbinRenderer

// Settings → About → Open-source licences (D204's App Store list): the code
// the app ships that isn't ours, and the terms it ships under — SwiftTerm,
// the faces, and IrohLib (n0's iroh-ffi) with the Rust crates it builds into
// the app. The list is App/Resources/Licences/OpenSourceLicences.json, made by
// native/ios/scripts/licences.sh from the pins in project.yml (rerun it when
// a pin moves); nothing here is fetched.

/// OpenSourceLicences.json: groups of entries; each entry names its licence
/// texts by id, so the hundreds of crates under the same few licences share
/// them.
struct LicenceFile: Decodable, Sendable {
    struct Group: Decodable, Sendable, Identifiable {
        var title: String
        var footer: String?
        var entries: [Entry]
        var id: String { title }
    }

    struct Entry: Decodable, Sendable, Identifiable, Hashable {
        var name: String
        var version: String?
        /// The SPDX expression the project declares (`MIT OR Apache-2.0`).
        var licence: String
        var url: String?
        /// Ids into ``LicenceFile/texts``: the licence text(s) it ships with.
        var texts: [String]
        var id: String { name + "@" + (version ?? "") }
        var title: String { version.map { "\(name) \($0)" } ?? name }
    }

    var groups: [Group]
    var texts: [String: String]

    static let resource = "OpenSourceLicences"

    /// The bundled file; nil when the build has none (it always should).
    static let bundled: LicenceFile? = {
        guard let url = Bundle.main.url(forResource: resource, withExtension: "json"),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(LicenceFile.self, from: data)
    }()

    func text(of e: Entry) -> String {
        e.texts.compactMap { texts[$0] }.joined(separator: "\n\n")
    }
}

struct LicencesView: View {
    /// A group longer than this opens as its own searchable list.
    static let inlineLimit = 8

    var body: some View {
        Form {
            if let file = LicenceFile.bundled {
                Section {
                    Text("xbin uses these open-source projects. Each is used under the licence shown; tap one for its full text.")
                        .font(.footnote).foregroundStyle(XbinColor.muted)
                }
                ForEach(file.groups) { g in
                    Section {
                        if g.entries.count > Self.inlineLimit {
                            ForEach(g.entries.prefix(1)) { e in row(e, file) }
                            NavigationLink {
                                LicenceListView(group: g, file: file)
                            } label: {
                                LabeledContent("All \(g.entries.count) components") { EmptyView() }
                            }
                        } else {
                            ForEach(g.entries) { e in row(e, file) }
                        }
                    } header: {
                        Text(verbatim: g.title)
                    } footer: {
                        if let f = g.footer { Text(verbatim: f) }
                    }
                }
            } else {
                ContentUnavailableView("No licence list in this build", systemImage: "doc.questionmark",
                                       description: Text("It is made by native/ios/scripts/licences.sh."))
            }
        }
        .concreteBackground()
        .navigationTitle("Open-source licences")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func row(_ e: LicenceFile.Entry, _ file: LicenceFile) -> some View {
        NavigationLink { LicenceTextView(entry: e, file: file) } label: { LicenceRow(entry: e) }
    }
}

/// One component: its name and version over its licence expression.
struct LicenceRow: View {
    let entry: LicenceFile.Entry

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(verbatim: entry.title)
            Text(verbatim: entry.licence).font(.caption).foregroundStyle(XbinColor.muted)
        }
        .accessibilityElement(children: .combine)
    }
}

/// A long group (the Rust crates), searchable by name or licence.
struct LicenceListView: View {
    let group: LicenceFile.Group
    let file: LicenceFile
    @State private var query = ""

    var body: some View {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        let shown = q.isEmpty ? group.entries
            : group.entries.filter { $0.name.lowercased().contains(q) || $0.licence.lowercased().contains(q) }
        List {
            ForEach(shown) { e in
                NavigationLink { LicenceTextView(entry: e, file: file) } label: { LicenceRow(entry: e) }
            }
        }
        .searchable(text: $query, prompt: Text("Name or licence"))
        .concreteBackground()
        .navigationTitle(group.title)
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A licence's full text, as the project ships it.
struct LicenceTextView: View {
    let entry: LicenceFile.Entry
    let file: LicenceFile
    @Environment(\.openURL) private var openURL

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: entry.title).font(.headline)
                    Text(verbatim: entry.licence).font(.subheadline).foregroundStyle(XbinColor.muted)
                }
                if let s = entry.url, let u = URL(string: s), u.scheme == "https" {
                    Button { openURL(u) } label: {
                        Label { Text(verbatim: s).lineLimit(1).truncationMode(.middle) } icon: { Image(systemName: "safari") }
                    }
                    .font(.footnote)
                }
                Text(verbatim: file.text(of: entry))
                    .font(XbinFont.mono(.caption2))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
        }
        .concreteBackground()
        .navigationTitle(entry.name)
        .navigationBarTitleDisplayMode(.inline)
    }
}
