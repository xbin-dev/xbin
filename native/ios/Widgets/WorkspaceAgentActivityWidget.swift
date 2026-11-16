import ActivityKit
import SwiftUI
import UIKit
import WidgetKit
import XbinAgent

// The workspace agent's Live Activity (D241; plans/wsagent/ios-stage2.md
// §5): one card per workspace — the lock screen and the Dynamic Island.
// What it says comes from XbinAgent's WorkspaceAgentActivityDisplay (tested
// on Linux): counts and a phase, never a tile's name or a request's text.
// Here is only the layout; each view a small struct of its own.

private let agentAccent = Color(uiColor: UIColor { traits in
    traits.userInterfaceStyle == .dark
        ? UIColor(red: 0x8C / 255, green: 0x9B / 255, blue: 0xFF / 255, alpha: 1)
        : UIColor(red: 0x1F / 255, green: 0x3D / 255, blue: 0xFF / 255, alpha: 1)
})
private let agentWarn = Color(uiColor: UIColor { traits in
    traits.userInterfaceStyle == .dark
        ? UIColor(red: 0xF2 / 255, green: 0xB0 / 255, blue: 0x4A / 255, alpha: 1)
        : UIColor(red: 0x9A / 255, green: 0x5B / 255, blue: 0x00 / 255, alpha: 1)
})
private let agentOk = Color(uiColor: UIColor { traits in
    traits.userInterfaceStyle == .dark
        ? UIColor(red: 0xA3 / 255, green: 0xCF / 255, blue: 0x5E / 255, alpha: 1)
        : UIColor(red: 0x43 / 255, green: 0x6C / 255, blue: 0x0C / 255, alpha: 1)
})

struct WorkspaceAgentActivityWidget: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: WorkspaceAgentActivityAttributes.self) { context in
            WorkspaceAgentLockScreen(card: WorkspaceAgentCard(context))
        } dynamicIsland: { context in
            WorkspaceAgentIsland.make(WorkspaceAgentCard(context))
        }
    }
}

/// One render's model of the card.
struct WorkspaceAgentCard {
    let display: WorkspaceAgentActivityDisplay
    let url: URL?
    let agentURL: URL?

    init(_ context: ActivityViewContext<WorkspaceAgentActivityAttributes>) {
        let a = context.attributes
        let names = (a.workspace.isEmpty || a.appWorkspace.isEmpty) ? WidgetNames.lookup(ws: a.ws) : nil
        let d = WorkspaceAgentActivityDisplay(attributes: a, state: context.state, stale: context.isStale,
                                              workspaceTitle: names?.title, appWorkspace: names?.appWorkspace)
        display = d
        url = d.link.flatMap { URL(string: $0) }
        agentURL = d.agentLink.flatMap { URL(string: $0) }
    }

    var accent: Color {
        switch display.phase {
        case .waiting: return agentWarn
        case .idle: return agentOk
        case .paused: return .secondary
        case .running: return display.stale ? .secondary : agentAccent
        }
    }
}

// MARK: lock screen

struct WorkspaceAgentLockScreen: View {
    let card: WorkspaceAgentCard

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                WorkspaceAgentGlyph(card: card).font(.headline)
                // "Workspace agent" stays whole; the workspace's name gives way first
                Text(card.display.title)
                    .font(.subheadline.weight(.semibold))
                    .lineLimit(1)
                    .layoutPriority(2)
                Text("· \(card.display.subtitle)")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: 4)
                // a timer text takes all the width it is offered: a width of its own
                WorkspaceAgentElapsed(card: card).font(.caption).foregroundStyle(.secondary)
                    .multilineTextAlignment(.trailing)
                    .frame(width: 64, alignment: .trailing)
            }
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(card.display.status)
                    .font(.title3.weight(.bold))
                    .foregroundStyle(card.accent)
                    .lineLimit(1)
                if !card.display.detail.isEmpty {
                    Text(card.display.detail)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
        }
        .padding(16)
        .widgetURL(card.url)
        .accessibilityElement(children: .combine)
    }
}

/// The phase's glyph: the agent's squares while it works, a hand while
/// something waits, a pause, a check — never a sparkle.
struct WorkspaceAgentGlyph: View {
    let card: WorkspaceAgentCard

    var body: some View {
        Image(systemName: card.display.symbol)
            .foregroundStyle(card.accent)
            .accessibilityHidden(true)
    }
}

/// Time since the stretch began, counting by itself (`.timer` text).
struct WorkspaceAgentElapsed: View {
    let card: WorkspaceAgentCard

    var body: some View {
        if let start = card.display.timerStart {
            Text(start, style: .timer).monospacedDigit()
        } else {
            EmptyView()
        }
    }
}

// MARK: Dynamic Island

enum WorkspaceAgentIsland {
    static func make(_ card: WorkspaceAgentCard) -> DynamicIsland {
        let island = DynamicIsland {
            DynamicIslandExpandedRegion(.leading) {
                WorkspaceAgentIslandLeading(card: card)
            }
            DynamicIslandExpandedRegion(.trailing) {
                WorkspaceAgentIslandTrailing(card: card)
            }
            DynamicIslandExpandedRegion(.bottom) {
                WorkspaceAgentIslandBottom(card: card)
            }
        } compactLeading: {
            WorkspaceAgentGlyph(card: card)
        } compactTrailing: {
            WorkspaceAgentCompactTrailing(card: card)
        } minimal: {
            WorkspaceAgentGlyph(card: card)
        }
        return island.widgetURL(card.url).keylineTint(card.accent)
    }
}

struct WorkspaceAgentIslandLeading: View {
    let card: WorkspaceAgentCard

    var body: some View {
        Label {
            Text(card.display.status).lineLimit(1).fixedSize()
        } icon: {
            WorkspaceAgentGlyph(card: card)
        }
        .font(.subheadline.weight(.semibold))
        .foregroundStyle(card.accent)
        // "Needs you (2)" whole: under the camera when the leading slot is too narrow
        .dynamicIsland(verticalPlacement: .belowIfTooWide)
    }
}

struct WorkspaceAgentIslandTrailing: View {
    let card: WorkspaceAgentCard

    var body: some View {
        WorkspaceAgentElapsed(card: card)
            .font(.subheadline)
            .frame(maxWidth: 64, alignment: .trailing)
    }
}

struct WorkspaceAgentIslandBottom: View {
    let card: WorkspaceAgentCard

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Text(card.display.detail.isEmpty ? card.display.title : card.display.detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: 4)
                Text(card.display.subtitle).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            HStack(spacing: 8) {
                if let review = card.display.reviewLabel, let url = card.url {
                    Link(destination: url) {
                        Text(review).font(.subheadline.weight(.semibold)).frame(maxWidth: .infinity, minHeight: 32)
                    }
                    .background(agentWarn.opacity(0.25), in: RoundedRectangle(cornerRadius: 2))
                }
                if let url = card.agentURL {
                    Link(destination: url) {
                        Text("Open agent").font(.subheadline.weight(.semibold)).frame(maxWidth: .infinity, minHeight: 32)
                    }
                    .background(Color.white.opacity(0.12), in: RoundedRectangle(cornerRadius: 2))
                }
            }
        }
    }
}

/// The compact trailing slot: what waits, else the tile agents working.
struct WorkspaceAgentCompactTrailing: View {
    let card: WorkspaceAgentCard

    var body: some View {
        Text(card.display.badge)
            .font(.caption.weight(.bold))
            .foregroundStyle(card.accent)
            .monospacedDigit()
    }
}
