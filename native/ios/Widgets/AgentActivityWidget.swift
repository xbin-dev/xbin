import ActivityKit
import SwiftUI
import WidgetKit
import XbinAgent

// The agent turn's Live Activity (native/spec/push.md §7): the lock-screen
// banner and the Dynamic Island. What it says comes from XbinAgent's
// AgentActivityDisplay (tested on Linux); here is only the layout. Each
// view is its own small struct and the configuration's closures are one
// call each — Xcode's type checker gives up on big inline builders.

private let amber = Color(red: 0xF5 / 255, green: 0xA6 / 255, blue: 0x23 / 255)

struct AgentActivityWidget: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: AgentActivityAttributes.self) { context in
            AgentLockScreenView(card: AgentCard(context))
        } dynamicIsland: { context in
            AgentIsland.make(AgentCard(context))
        }
    }
}

/// One render's model of the card.
struct AgentCard {
    let display: AgentActivityDisplay
    let url: URL?

    init(_ context: ActivityViewContext<AgentActivityAttributes>) {
        let a = context.attributes
        // a card the app started names itself; one xbind started by push
        // carries only `ws`
        let names = (a.workspace.isEmpty || a.appWorkspace.isEmpty) ? WidgetNames.lookup(ws: a.ws) : nil
        let d = AgentActivityDisplay(attributes: a, state: context.state, stale: context.isStale,
                                     workspaceTitle: names?.title, appWorkspace: names?.appWorkspace)
        display = d
        url = d.link.flatMap { URL(string: $0) }
    }

    var accent: Color {
        switch display.phase {
        case .waiting: return amber
        case .idle: return .green
        case .running: return display.stale ? .secondary : amber
        }
    }
}

// MARK: lock screen

struct AgentLockScreenView: View {
    let card: AgentCard

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            PhaseSymbol(card: card)
                .font(.title2)
            VStack(alignment: .leading, spacing: 2) {
                Text(card.display.title)
                    .font(.headline)
                    .lineLimit(1)
                Text(card.display.subtitle)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text(card.display.status)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(card.accent)
                    .lineLimit(1)
                ElapsedText(card: card)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .widgetURL(card.url)
    }
}

struct PhaseSymbol: View {
    let card: AgentCard

    var body: some View {
        Image(systemName: card.display.symbol)
            .foregroundStyle(card.accent)
    }
}

/// Elapsed time since the turn started, counting on its own (the system
/// redraws `.timer` text; no update is needed for it).
struct ElapsedText: View {
    let card: AgentCard

    var body: some View {
        if let start = card.display.timerStart {
            Text(start, style: .timer)
                .monospacedDigit()
        } else {
            EmptyView()
        }
    }
}

// MARK: Dynamic Island

enum AgentIsland {
    static func make(_ card: AgentCard) -> DynamicIsland {
        let island = DynamicIsland {
            DynamicIslandExpandedRegion(.leading) {
                IslandLeading(card: card)
            }
            DynamicIslandExpandedRegion(.trailing) {
                IslandTrailing(card: card)
            }
            DynamicIslandExpandedRegion(.bottom) {
                IslandBottom(card: card)
            }
        } compactLeading: {
            PhaseSymbol(card: card)
        } compactTrailing: {
            CompactTrailing(card: card)
        } minimal: {
            PhaseSymbol(card: card)
        }
        return island.widgetURL(card.url).keylineTint(card.accent)
    }
}

struct IslandLeading: View {
    let card: AgentCard

    var body: some View {
        Label {
            Text(card.display.status)
                .lineLimit(1)
        } icon: {
            PhaseSymbol(card: card)
        }
        .font(.subheadline.weight(.semibold))
        .foregroundStyle(card.accent)
    }
}

struct IslandTrailing: View {
    let card: AgentCard

    var body: some View {
        ElapsedText(card: card)
            .font(.subheadline)
            .frame(maxWidth: 64, alignment: .trailing)
    }
}

struct IslandBottom: View {
    let card: AgentCard

    var body: some View {
        HStack(spacing: 6) {
            Text(card.display.title)
                .font(.headline)
                .lineLimit(1)
            Spacer(minLength: 4)
            Text(card.display.subtitle)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }
}

/// The compact trailing slot: how many wait, else the elapsed time.
struct CompactTrailing: View {
    let card: AgentCard

    var body: some View {
        if !card.display.badge.isEmpty {
            Text(card.display.badge)
                .font(.caption.weight(.bold))
                .foregroundStyle(card.accent)
        } else {
            ElapsedText(card: card)
                .font(.caption)
                .frame(maxWidth: 44)
        }
    }
}
