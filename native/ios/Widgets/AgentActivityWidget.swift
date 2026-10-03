import ActivityKit
import SwiftUI
import UIKit
import WidgetKit
import XbinAgent

// The agent turn's Live Activity (native/spec/push.md §7): the lock-screen
// banner and the Dynamic Island. What it says comes from XbinAgent's
// AgentActivityDisplay (tested on Linux); here is only the layout. Each
// view is its own small struct and the configuration's closures are one
// call each — Xcode's type checker gives up on big inline builders.

// Base Two (D185): the accent — cobalt, periwinkle on dark — for an agent
// that works or waits for you, ok for one that is done.
private let accentColor = Color(uiColor: UIColor { traits in
    traits.userInterfaceStyle == .dark
        ? UIColor(red: 0x8C / 255, green: 0x9B / 255, blue: 0xFF / 255, alpha: 1)
        : UIColor(red: 0x1F / 255, green: 0x3D / 255, blue: 0xFF / 255, alpha: 1)
})
private let okColor = Color(uiColor: UIColor { traits in
    traits.userInterfaceStyle == .dark
        ? UIColor(red: 0xA3 / 255, green: 0xCF / 255, blue: 0x5E / 255, alpha: 1)
        : UIColor(red: 0x43 / 255, green: 0x6C / 255, blue: 0x0C / 255, alpha: 1)
})

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
        case .waiting: return accentColor
        case .idle: return okColor
        case .running: return display.stale ? .secondary : accentColor
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
