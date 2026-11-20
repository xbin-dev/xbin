import SwiftUI
import XbinCore
import XbinRenderer

// The two nodes of the agent's conversation that the app draws itself
// (D233; docs/native.md §The workspace agent's nodes): the agent's view
// places a `request` or a `session` and names an id — what the card says
// and the one button on it come from xbind, read here with the device's
// session. Nothing the view's tree says is shown as the request.

/// A card's frame: a left rule in its tone, the theme's surface.
private struct AgentCardFrame<Content: View>: View {
    var tone: String
    @ViewBuilder var content: Content

    var body: some View {
        content
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(XbinColor.surface)
            .overlay(alignment: .leading) { Rectangle().fill(AgentTone.color(tone)).frame(width: 3) }
            .overlay { RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1) }
            .clipShape(.xbinPlate)
            .padding(.vertical, 4) // (the transcript gives its rows their side margins)
    }
}

/// A status tone as a colour — never the only sign: every status is a
/// glyph and a word too.
enum AgentTone {
    static func color(_ tone: String) -> Color {
        switch tone {
        case "warn": return XbinColor.warn
        case "ok": return XbinColor.ok
        case "danger", "bad": return XbinColor.danger
        default: return XbinColor.borderStrong
        }
    }

    static func symbol(_ glyph: String) -> String {
        switch glyph {
        case "shield": return "checkmark.shield"
        case "clock": return "clock"
        case "check": return "checkmark.square"
        case "xmark": return "xmark.square"
        case "bell": return "bell"
        case "pause": return "pause"
        case "error": return "exclamationmark.octagon"
        case "warning": return "exclamationmark.triangle"
        default: return "square"
        }
    }
}

/// A request for the person's approval, in the conversation: that it
/// waits (for what, for which tile, until when) and **Review…** — the
/// app's own sheet — or what came of it. It cannot approve.
struct AgentRequestCard: View {
    let workspace: WorkspaceModel
    let id: String
    let isChangeSet: Bool
    @Environment(SceneModel.self) private var scene
    @ScaledMetric private var buttonHeight: CGFloat = 32

    var body: some View {
        let agent = workspace.agent
        Group {
            if let card = card(agent) {
                AgentCardFrame(tone: card.tone) {
                    VStack(alignment: .leading, spacing: 6) {
                        Label {
                            Text(verbatim: card.head).font(.subheadline.weight(.semibold))
                        } icon: {
                            Image(systemName: AgentTone.symbol(card.glyph)).foregroundStyle(AgentTone.color(card.tone))
                        }
                        if !card.what.isEmpty { Text(verbatim: card.what).font(.callout) }
                        if !card.parts.isEmpty {
                            AgentFacts(parts: card.parts) // each fact its own element (D250)
                        } else if !card.line.isEmpty {
                            Text(verbatim: card.line).font(.caption.monospacedDigit()).foregroundStyle(XbinColor.muted)
                        }
                        if !card.button.isEmpty {
                            Button {
                                AgentKeyboard.dismiss()
                                scene.review(isChangeSet ? .changeset(id) : .request(id))
                            } label: {
                                Text(verbatim: card.button).frame(maxWidth: .infinity, minHeight: buttonHeight)
                            }
                            .xbinSecondary()
                            .disabled(agent.unreachable)
                            // what it reviews, said: "Review: Approve a change to Note"
                            .accessibilityLabel(Text(verbatim: card.button.hasPrefix("Review") ? "Review: \(card.what.isEmpty ? card.head : card.what)" : "\(card.button): \(card.head)"))
                            .accessibilityHint(agent.unreachable ? Text("Approvals need a connection.") : Text("Opens xbin's own sheet. Nothing is allowed by this."))
                            .accessibilityIdentifier("agent-review:\(id)")
                            if agent.unreachable {
                                Text("Approvals need a connection.").font(.caption).foregroundStyle(XbinColor.muted)
                            }
                        }
                    }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("agent-request:\(id)")
            } else if agent.missing.contains(id) {
                AgentCardFrame(tone: "") {
                    Label("This request is gone.", systemImage: "xmark.square").font(.callout).foregroundStyle(XbinColor.muted)
                }
            } else {
                AgentCardFrame(tone: "") { ProgressView() }
            }
        }
        .task(id: id) {
            if isChangeSet { await agent.changeset(id) } else { await agent.request(id) }
        }
    }

    private func card(_ agent: WorkspaceAgentModel) -> DelegationWords.RequestCard? {
        if isChangeSet { return agent.knownSets[id].map { DelegationWords.changeSetCard($0) } }
        return agent.known[id].map { DelegationWords.requestCard($0) }
    }
}

/// A tile agent the workspace agent started, in the conversation: the
/// tile, the coding agent, its status as a glyph and a word, its last
/// line, and **Open** — the app's own agent screen for that session — or
/// **Answer** when it waits: only the person can.
struct AgentSessionCard: View {
    let workspace: WorkspaceModel
    let id: String
    let tileHint: String
    var opened: () -> Void = {}
    @Environment(WorkspaceNav.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric private var buttonHeight: CGFloat = 32

    var body: some View {
        let agent = workspace.agent
        let row = agent.sessionRows[id]
        let tilePath = row.map { $0.tile.isEmpty ? tileHint : $0.tile } ?? tileHint
        let card = row.map { DelegationWords.sessionCard($0, tileName: agent.tileName(tilePath), harnessName: agent.harnessName($0.provider)) }
        AgentCardFrame(tone: card?.tone ?? "") {
            let stacked = typeSize.isAccessibilitySize
            let layout = stacked ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(alignment: .top, spacing: 8))
            layout {
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Image(systemName: XbinGlyphs.symbol("agent")).foregroundStyle(XbinColor.partAgent)
                        Text(verbatim: agent.tileName(tilePath)).font(.subheadline.weight(.semibold)).lineLimit(1)
                        if let card { Text(verbatim: card.harness).font(.caption).foregroundStyle(XbinColor.muted).lineLimit(1) }
                    }
                    if let card {
                        Label {
                            Text(verbatim: card.word.prefix(1).uppercased() + card.word.dropFirst()).font(.caption.weight(.semibold))
                        } icon: {
                            Image(systemName: AgentTone.symbol(card.glyph))
                        }
                        .foregroundStyle(card.tone.isEmpty ? XbinColor.muted : AgentTone.color(card.tone))
                        if !card.line.isEmpty {
                            Text(verbatim: card.line).font(.callout).lineLimit(stacked ? 4 : 1).truncationMode(.tail)
                        }
                        if !card.note.isEmpty { Text(verbatim: card.note).font(.caption).foregroundStyle(XbinColor.muted) }
                    } else {
                        Text("Looking it up…").font(.caption).foregroundStyle(XbinColor.muted)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if let card, workspace.tile(tilePath) != nil {
                    Button {
                        workspace.open(.agent(cwd: tilePath, session: id), in: nav)
                        opened()
                    } label: {
                        Text(verbatim: card.button).frame(minWidth: 56, minHeight: buttonHeight)
                            .frame(maxWidth: stacked ? .infinity : nil)
                    }
                    .xbinSecondary()
                    // "Open Note's tile agent" / "Answer Note's tile agent"
                    .accessibilityLabel(Text(verbatim: "\(card.button) \(agent.tileName(tilePath))'s tile agent"))
                    .accessibilityHint(card.waits ? Text("It waits for you. Only you can answer.") : Text(verbatim: ""))
                    .accessibilityIdentifier("agent-session-open:\(id)")
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("agent-session:\(id)")
        .onAppear { agent.watchSession(id) }
    }
}
