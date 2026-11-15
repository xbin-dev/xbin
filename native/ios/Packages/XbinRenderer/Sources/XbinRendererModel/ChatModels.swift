import Foundation
import XbinCore

// Plain view models of the chat family (plans/native.md §8.4). The SwiftUI
// components (TranscriptView, MessageView, ThinkingView, ToolCardView,
// ApprovalView, QuestionView, PlanView, DiffView, ActivityView, StepView,
// ComposerView) take these, so the app's ACP agent screen (§13) builds them
// from its own session model and a native tile builds them from its tree
// (the `init(props:)` adapters here) — one set of components for both.

public enum ChatRole: String, Sendable, CaseIterable {
    case user, assistant, system
}

public struct ChatFile: Sendable, Hashable {
    public var name: String
    public var mime: String
    /// Tile-relative (tree) or app-resolved (agent screen) source.
    public var src: String?

    public init(name: String, mime: String = "", src: String? = nil) {
        self.name = name
        self.mime = mime
        self.src = src
    }

    public var isImage: Bool { mime.hasPrefix("image/") }

    /// The source to draw a thumbnail from: an image with a `src` (the
    /// reference renderer's rule); nil draws the file as a chip.
    public var thumbnailSource: String? {
        guard isImage, let src, !src.isEmpty else { return nil }
        return src
    }
}

public struct ChatMessage: Sendable, Equatable, Identifiable {
    public var id: String
    public var role: ChatRole
    public var sender: String?
    /// The text: shown verbatim unless ``markdown`` holds its blocks.
    public var text: String
    /// Markdown blocks (a `markdown` message's tokens); nil for plain text.
    public var markdown: [MarkdownBlock]?
    public var streaming: Bool
    /// Display time ("18:02", or a formatted epoch).
    public var time: String?
    public var files: [ChatFile]
    public var queued: Bool

    public init(id: String, role: ChatRole, sender: String? = nil, text: String, markdown: [MarkdownBlock]? = nil,
                streaming: Bool = false, time: String? = nil, files: [ChatFile] = [], queued: Bool = false) {
        self.id = id
        self.role = role
        self.sender = sender
        self.text = text
        self.markdown = markdown
        self.streaming = streaming
        self.time = time
        self.files = files
        self.queued = queued
    }

    /// From a `message` node's props. A markdown message without tokens
    /// (nothing lexed yet) shows its text verbatim.
    public init(id: String, props p: Props) {
        let blocks = p.bool("markdown") ? Markdown.blocks(p["tokens"]) : []
        self.init(
            id: id, role: ChatRole(rawValue: p.string("role") ?? "") ?? .assistant, sender: p.nonEmpty("sender"),
            text: p.string("text") ?? "", markdown: blocks.isEmpty ? nil : blocks,
            streaming: p.bool("streaming"), time: ChatFormat.time(p["time"]),
            files: p.objects("files").map { ChatFile(name: Props.text($0["name"]) ?? "", mime: Props.text($0["mime"]) ?? "", src: Props.text($0["src"])) },
            queued: p.bool("queued"))
    }
}

public struct ChatThinking: Sendable, Equatable, Identifiable {
    public var id: String
    public var text: String
    /// Still thinking: shimmers, and reads "Thinking…".
    public var live: Bool
    public var seconds: Double?

    public init(id: String, text: String, live: Bool = false, seconds: Double? = nil) {
        self.id = id
        self.text = text
        self.live = live
        self.seconds = seconds
    }

    public init(id: String, props p: Props) {
        self.init(id: id, text: p.string("text") ?? "", live: p.bool("live"), seconds: p.number("seconds"))
    }

    /// "Thinking…", "Thought for 4s", "Thought for 1m 5s" or "Thought".
    public var label: String {
        if live { return "Thinking…" }
        if let seconds { return "Thought for " + ChatFormat.seconds(seconds) }
        return "Thought"
    }
}

public struct ChatChip: Sendable, Hashable {
    public var text: String
    public var tone: XbinTone?

    public init(text: String, tone: XbinTone? = nil) {
        self.text = text
        self.tone = tone
    }
}

public enum ToolState: String, Sendable, CaseIterable {
    case writing, running, ok, error, canceled

    public var isActive: Bool { self == .writing || self == .running }
}

public struct ChatToolCard: Sendable, Equatable, Identifiable {
    public var id: String
    public var title: String
    /// An icon name (§10.4); nil draws the wrench.
    public var icon: String?
    /// The tool family (`read`, `edit`, `shell`, `agent`, …).
    public var family: String?
    public var state: ToolState?
    public var chips: [ChatChip]

    public init(id: String, title: String, icon: String? = nil, family: String? = nil, state: ToolState? = nil,
                chips: [ChatChip] = []) {
        self.id = id
        self.title = title
        self.icon = icon
        self.family = family
        self.state = state
        self.chips = chips
    }

    public init(id: String, props p: Props) {
        self.init(id: id, title: p.string("title") ?? "", icon: p.nonEmpty("icon"), family: p.nonEmpty("family"),
                  state: ToolState(rawValue: p.string("state") ?? ""),
                  chips: p.objects("chips").compactMap { c in
                      guard let t = Props.text(c["text"]) else { return nil }
                      return ChatChip(text: t, tone: XbinTone(Props.text(c["tone"])))
                  })
    }
}

public struct ApprovalOption: Sendable, Hashable, Identifiable {
    public var id: String
    public var label: String
    /// The ACP option kind (`allow_once`, `allow_always`, `reject_once`, …).
    public var kind: String

    public init(id: String, label: String, kind: String = "") {
        self.id = id
        self.label = label
        self.kind = kind
    }

    /// How the option's button looks.
    public enum Emphasis: Sendable, Equatable { case prominent, standard, destructive }
}

public struct ChatApproval: Sendable, Equatable, Identifiable {
    public var id: String
    public var title: String
    public var text: String?
    /// In the agent's own order (D77: never re-sorted).
    public var options: [ApprovalOption]
    public var note: String?
    /// Offer a feedback box (plan approval).
    public var feedback: Bool
    /// Answered: by whom, and which option.
    public var settled: Settled?

    public struct Settled: Sendable, Equatable {
        public var by: String?
        public var id: String

        public init(by: String? = nil, id: String) {
            self.by = by
            self.id = id
        }
    }

    public init(id: String, title: String, text: String? = nil, options: [ApprovalOption], note: String? = nil,
                feedback: Bool = false, settled: Settled? = nil) {
        self.id = id
        self.title = title
        self.text = text
        self.options = options
        self.note = note
        self.feedback = feedback
        self.settled = settled
    }

    public init(id: String, props p: Props) {
        let opts = p.objects("options").map { o in
            ApprovalOption(id: Props.text(o["id"]) ?? "", label: Props.text(o["label"]) ?? Props.text(o["id"]) ?? "",
                           kind: Props.text(o["kind"]) ?? "")
        }
        var settled: Settled?
        if let s = p.object("settled") { settled = Settled(by: Props.text(s["by"]), id: Props.text(s["id"]) ?? "") }
        self.init(id: id, title: p.nonEmpty("title") ?? "Permission needed", text: p.nonEmpty("text"), options: opts,
                  note: p.nonEmpty("note"), feedback: p.bool("feedback"), settled: settled)
    }

    /// The reference renderer's rule: reject/deny/cancel kinds are
    /// destructive; the first allowing option that isn't "always" is the
    /// prominent one (the first option when none qualifies).
    public func emphasis(_ index: Int) -> ApprovalOption.Emphasis {
        guard options.indices.contains(index) else { return .standard }
        func matches(_ k: String, _ words: [String]) -> Bool { words.contains { k.lowercased().contains($0) } }
        if matches(options[index].kind, ["reject", "deny", "cancel"]) { return .destructive }
        let first = options.firstIndex { !matches($0.kind, ["reject", "deny", "cancel", "always"]) } ?? 0
        return index == first ? .prominent : .standard
    }

    /// "Allow once · answered by Ana".
    public var settledText: String? {
        guard let s = settled else { return nil }
        let label = options.first { $0.id == s.id }?.label ?? s.id
        if let by = s.by, !by.isEmpty { return "\(label) · answered by \(by)" }
        return label
    }
}

public struct ChatQuestion: Sendable, Equatable, Identifiable {
    public var id: String
    public var title: String
    public var form: QuestionForm
    /// Answered: the fields are read-only. ``answer`` holds what was
    /// submitted when the tile says (a `settled` object).
    public var settled: Bool
    public var answer: [String: JSONValue]
    /// Rev 2: choices as rows, "Something else", pages, the buttons' words.
    public var options: QuestionOptions

    public init(id: String, title: String, form: QuestionForm, settled: Bool = false, answer: [String: JSONValue] = [:],
                options: QuestionOptions = QuestionOptions()) {
        self.id = id
        self.title = title
        self.form = form
        self.settled = settled
        self.answer = answer
        self.options = options
    }

    public init(id: String, props p: Props) {
        let form = QuestionForm(schema: p["schema"])
        let s = p["settled"]
        let settled: Bool
        switch s {
        case nil, .null?, .bool(false)?: settled = false
        default: settled = true
        }
        self.init(id: id, title: p.nonEmpty("title") ?? form.title ?? "Question", form: form, settled: settled,
                  answer: s?.objectValue ?? [:], options: QuestionOptions(props: p, form: form))
    }

    /// The values the form starts with: the answer when settled, else the
    /// schema defaults.
    public var initialValues: [String: JSONValue] { settled && !answer.isEmpty ? answer : form.defaults }

    /// The form's state as the person fills it in (``QuestionSession``).
    public var session: QuestionSession { QuestionSession(form: form, options: options, values: initialValues) }
}

public enum PlanStatus: String, Sendable, CaseIterable {
    case pending
    case inProgress = "in_progress"
    case completed
}

public struct ChatPlan: Sendable, Equatable {
    public struct Entry: Sendable, Hashable {
        public var text: String
        public var status: PlanStatus

        public init(text: String, status: PlanStatus) {
            self.text = text
            self.status = status
        }
    }

    /// Rev 2: what a step does or where it stands ("reads only", "asks
    /// you", "needs your OK"), a word with an optional glyph and tone.
    public struct Tag: Sendable, Hashable {
        public var text: String
        /// An icon name (§10.4).
        public var icon: String?
        public var tone: XbinTone?

        public init(text: String, icon: String? = nil, tone: XbinTone? = nil) {
            self.text = text
            self.icon = icon
            self.tone = tone
        }
    }

    public var entries: [Entry]
    /// Rev 2: the card's title (nil: "Plan").
    public var title: String?
    /// Rev 2: the entries are numbered steps.
    public var numbered: Bool
    /// Rev 2: a tag per entry, by position (nil: none for that entry).
    public var tags: [Tag?]
    /// Rev 2: a sentence under the entries.
    public var note: String?
    /// Rev 2: a quiet line beside the title (in place of the done count).
    public var meta: String?

    public init(entries: [Entry], title: String? = nil, numbered: Bool = false, tags: [Tag?] = [], note: String? = nil,
                meta: String? = nil) {
        self.entries = entries
        self.title = title
        self.numbered = numbered
        self.tags = tags
        self.note = note
        self.meta = meta
    }

    public init(props p: Props) {
        entries = p.objects("entries").map {
            Entry(text: Props.text($0["text"]) ?? "", status: PlanStatus(rawValue: Props.text($0["status"]) ?? "") ?? .pending)
        }
        title = p.nonEmpty("title")
        numbered = p.bool("numbered")
        // by position: an item that is no object, or has no text, is no tag
        tags = p.array("tags").map { v in
            guard let o = v.objectValue, let t = Props.text(o["text"]), !t.isEmpty else { return nil }
            return Tag(text: t, icon: Props.text(o["icon"]).flatMap { $0.isEmpty ? nil : $0 }, tone: XbinTone(Props.text(o["tone"])))
        }
        note = p.nonEmpty("note")
        meta = p.nonEmpty("meta")
    }

    public var done: Int { entries.filter { $0.status == .completed }.count }
    /// "2 of 4 done".
    public var summary: String { "\(done) of \(entries.count) done" }

    /// The card's heading: its `title`, else "Plan".
    public var heading: String { title ?? "Plan" }
    /// The quiet line beside the heading: `meta`, else the done count.
    public var trailing: String { meta ?? summary }

    /// Entry `i`'s tag, if it has one.
    public func tag(_ i: Int) -> Tag? { tags.indices.contains(i) ? tags[i] : nil }

    /// Entry `i` as it reads: "2. Start a tile agent in Orders" when
    /// numbered, else its text.
    public func line(_ i: Int) -> String {
        guard entries.indices.contains(i) else { return "" }
        return numbered ? "\(i + 1). \(entries[i].text)" : entries[i].text
    }

    /// Entry `i` for VoiceOver: its line, where it stands, its tag —
    /// "2. Start a tile agent in Orders, in progress, asks you".
    public func accessibilityLine(_ i: Int) -> String {
        guard entries.indices.contains(i) else { return "" }
        let status: String
        switch entries[i].status {
        case .completed: status = "done"
        case .inProgress: status = "in progress"
        case .pending: status = "not started"
        }
        return [line(i), status, tag(i)?.text].compactMap { $0 }.joined(separator: ", ")
    }
}

public struct ChatActivity: Sendable, Equatable {
    public var text: String
    public var live: Bool

    public init(text: String, live: Bool = false) {
        self.text = text
        self.live = live
    }

    public init(props p: Props) { self.init(text: p.string("text") ?? "", live: p.bool("live")) }
}

public struct ChatStep: Sendable, Equatable {
    public var glyph: String
    public var text: String
    public var tone: XbinTone?

    public init(glyph: String = "•", text: String, tone: XbinTone? = nil) {
        self.glyph = glyph
        self.text = text
        self.tone = tone
    }

    public init(props p: Props) { self.init(glyph: p.nonEmpty("glyph") ?? "•", text: p.string("text") ?? "", tone: p.tone()) }
}

public struct ChatAttachment: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var mime: String
    /// Upload progress 0…1; nil or 1 = done.
    public var progress: Double?

    public init(id: String, name: String, mime: String = "", progress: Double? = nil) {
        self.id = id
        self.name = name
        self.mime = mime
        self.progress = progress
    }

    public var isUploading: Bool { (progress ?? 1) < 1 }
}

public struct SlashCommand: Sendable, Hashable, Identifiable {
    /// With or without the leading `/`.
    public var name: String
    public var hint: String?
    public var description: String?

    public init(name: String, hint: String? = nil, description: String? = nil) {
        self.name = name
        self.hint = hint
        self.description = description
    }

    public var id: String { name }
    /// The name without a leading `/`.
    public var bare: String { name.hasPrefix("/") ? String(name.dropFirst()) : name }
}

/// A composer's props (its text is a binding the view owns).
public struct ChatComposer: Sendable, Equatable {
    public var placeholder: String
    public var busy: Bool
    public var disabled: Bool
    public var attachments: [ChatAttachment]
    /// `accept` for the pickers (`image/*,.pdf`); nil: anything.
    public var accept: String?
    /// Attaching is offered (the tile gave an upload target, or the app
    /// handles uploads itself).
    public var canAttach: Bool
    public var slash: [SlashCommand]
    /// While busy, Send stays beside Stop: the message goes to the running
    /// turn or waits for its end (the Agent tab, D197). Off: Stop alone.
    public var sendWhileBusy: Bool
    /// Rev 2: what the next message is about, as chips in the box ("About
    /// Orders").
    public var context: [ChatContextChip]
    /// Rev 2: quick replies in a row above the box.
    public var replies: [ChatReply]
    /// Rev 2: a quiet line under the box.
    public var hint: String?

    public init(placeholder: String = "Message", busy: Bool = false, disabled: Bool = false, attachments: [ChatAttachment] = [],
                accept: String? = nil, canAttach: Bool = false, slash: [SlashCommand] = [], sendWhileBusy: Bool = false,
                context: [ChatContextChip] = [], replies: [ChatReply] = [], hint: String? = nil) {
        self.placeholder = placeholder
        self.busy = busy
        self.disabled = disabled
        self.attachments = attachments
        self.accept = accept
        self.canAttach = canAttach
        self.slash = slash
        self.sendWhileBusy = sendWhileBusy
        self.context = context
        self.replies = replies
        self.hint = hint
    }

    public init(props p: Props) {
        self.init(
            placeholder: p.nonEmpty("placeholder") ?? "Message", busy: p.bool("busy"), disabled: p.bool("disabled"),
            attachments: p.objects("attachments").map {
                ChatAttachment(id: Props.text($0["id"]) ?? "", name: Props.text($0["name"]) ?? "", mime: Props.text($0["mime"]) ?? "",
                           progress: $0["progress"]?.doubleValue)
            },
            accept: p.nonEmpty("accept"), canAttach: p.object("upload")?["path"]?.stringValue != nil,
            slash: p.objects("slash").compactMap { s in
                guard let n = Props.text(s["name"]), !n.isEmpty else { return nil }
                return SlashCommand(name: n, hint: Props.text(s["hint"]), description: Props.text(s["description"]))
            },
            context: ChatComposer.identified(p.objects("context")) { id, c in
                guard let label = Props.text(c["label"]), !label.isEmpty else { return nil }
                return ChatContextChip(id: id, label: label, icon: Props.text(c["icon"]).flatMap { $0.isEmpty ? nil : $0 },
                                       tone: XbinTone(Props.text(c["tone"])))
            },
            replies: ChatComposer.identified(p.objects("replies")) { id, r in
                guard let label = Props.text(r["label"]), !label.isEmpty else { return nil }
                return ChatReply(id: id, label: label)
            },
            hint: p.nonEmpty("hint"))
    }

    /// The items of a rev-2 list that report by id: one without an id, or
    /// with an id already taken, is not drawn (its tap could name nothing).
    static func identified<T>(_ items: [[String: JSONValue]], _ make: (String, [String: JSONValue]) -> T?) -> [T] {
        var seen = Set<String>()
        var out: [T] = []
        for item in items {
            guard let id = Props.text(item["id"]), !id.isEmpty, !seen.contains(id), let v = make(id, item) else { continue }
            seen.insert(id)
            out.append(v)
        }
        return out
    }

    /// The slash commands matching a draft that is one `/word` (at most 6),
    /// as the reference renderer offers them.
    public func slashMatches(_ draft: String) -> [SlashCommand] {
        guard draft.hasPrefix("/"), !draft.contains(where: \.isWhitespace) else { return [] }
        let q = String(draft.dropFirst())
        return Array(slash.filter { $0.bare.hasPrefix(q) }.prefix(6))
    }

    /// Whether `draft` may be sent now.
    public func canSend(_ draft: String) -> Bool {
        !disabled && !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
}

/// A composer's context chip (rev 2): what the next message is about. A tap
/// reports `context {id}`, its ✕ `remove-context {id}`.
public struct ChatContextChip: Sendable, Hashable, Identifiable {
    public var id: String
    public var label: String
    /// An icon name (§10.4).
    public var icon: String?
    /// `warn`: it can't be used as it is ("About Ledger — not connected").
    public var tone: XbinTone?

    public init(id: String, label: String, icon: String? = nil, tone: XbinTone? = nil) {
        self.id = id
        self.label = label
        self.icon = icon
        self.tone = tone
    }
}

/// A composer's quick reply (rev 2): a tap reports `reply {id}`.
public struct ChatReply: Sendable, Hashable, Identifiable {
    public var id: String
    public var label: String

    public init(id: String, label: String) {
        self.id = id
        self.label = label
    }
}

public enum ChatFormat {
    /// `4` → "4s", `65` → "1m 5s".
    public static func seconds(_ s: Double) -> String {
        let t = max(0, Int(s.rounded()))
        return t < 60 ? "\(t)s" : "\(t / 60)m \(t % 60)s"
    }

    /// A message `time`: a string as given; a number (ms since the epoch)
    /// as a short local time.
    public static func time(_ v: JSONValue?, timeZone: TimeZone = .current) -> String? {
        switch v {
        case .string(let s)?: return s.isEmpty ? nil : s
        case .int, .double:
            guard let ms = v?.doubleValue, ms.isFinite else { return nil }
            let f = DateFormatter()
            f.locale = Locale(identifier: "en_US_POSIX")
            f.timeZone = timeZone
            f.dateFormat = "HH:mm"
            return f.string(from: Date(timeIntervalSince1970: ms / 1000))
        default: return nil
        }
    }
}
