#if canImport(UIKit)
import Observation
import SwiftUI
import XbinRendererModel

/// The composer's dictation as the renderer draws it (D241): the app's
/// engine — on-device system speech, in the app (App/Shell/AgentDictation)
/// — sets these, and the composer shows the mic, the level, the time and
/// the words so far as a dashed draft; ✓ puts them in the composer to edit
/// or send. Nothing is sent by voice.
@MainActor
@Observable
open class XbinDictationModel {
    /// The mic is offered: on-device recognition is there and allowed.
    public var available = false
    public var listening = false
    /// The words so far.
    public var transcript = ""
    /// The input's level, 0…1 (the meter).
    public var level: Double = 0
    public var startedAt: Date?
    /// Why it stopped or couldn't start, in a line.
    public var problem: String?

    public init() {}

    /// Starts listening (asks for the permissions the first time).
    open func start() async {}

    /// Stops and answers the words heard.
    open func finish() -> String { listening = false; return transcript }

    /// Stops and drops what was heard.
    open func cancel() { listening = false; transcript = "" }
}

/// What replaces the composer's field while dictating: the level, the time,
/// the words so far as a dashed draft, Cancel and ✓.
struct DictationBar: View {
    let model: XbinDictationModel
    let done: @MainActor (String) -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        HStack(alignment: .center, spacing: 8) {
            Button { model.cancel() } label: {
                Image(systemName: XbinIcons.UI.close).font(.footnote.weight(.bold)).foregroundStyle(XbinColor.muted)
                    .frame(width: 36, height: 36)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Cancel dictation")
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    if !reduceMotion { LevelMeter(level: model.level) }
                    TimelineView(.periodic(from: .now, by: 1)) { ctx in
                        Text(verbatim: "Listening… " + Self.clock(model.startedAt, now: ctx.date)).font(.caption.monospacedDigit())
                            .foregroundStyle(XbinColor.muted)
                    }
                }
                Text(verbatim: model.transcript.isEmpty ? "tap ✓ to stop, then edit or send" : model.transcript)
                    .font(.callout)
                    .foregroundStyle(model.transcript.isEmpty ? XbinColor.muted : XbinColor.text)
                    .lineLimit(4)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(8)
                    .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
                    .accessibilityLabel(Text(verbatim: model.transcript.isEmpty ? "Listening" : "Heard: \(model.transcript)"))
            }
            Button { done(model.finish()) } label: {
                Image(systemName: "checkmark").font(.body.weight(.semibold)).foregroundStyle(XbinColor.onAccent)
                    .frame(width: 36, height: 36).background(XbinColor.accent, in: .xbinPlate)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Stop dictation")
            .accessibilityIdentifier("composer-dictation-done")
        }
        .accessibilityElement(children: .contain)
    }

    static func clock(_ start: Date?, now: Date) -> String {
        let s = max(0, Int(now.timeIntervalSince(start ?? now)))
        return "\(s / 60):" + String(format: "%02d", s % 60)
    }
}

/// Five bars that follow the input's level (the one moving element; a
/// static line with reduced motion).
private struct LevelMeter: View {
    let level: Double

    var body: some View {
        HStack(alignment: .bottom, spacing: 2) {
            ForEach(0..<5, id: \.self) { i in
                Rectangle()
                    .fill(Double(i) / 5 < level ? XbinColor.accent : XbinColor.border)
                    .frame(width: 3, height: 4 + CGFloat(i) * 2)
            }
        }
        .accessibilityHidden(true)
    }
}
#endif
