import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// The workspace agent's nodes (D233) as the SwiftUI components draw them,
/// built from the `workspace-agent` fixtures' nodes: `card`, `connection`,
/// the transcript's `empty` slot, `plan` rev 2, `composer` rev 2, `question`
/// rev 2 — and `request` and `session`, of which the tree gives an id and
/// nothing else.
@Suite struct AgentModelTests {
    func node(_ fixture: String, _ key: String) throws -> Node {
        try #require(try fixtureSet().expected(fixture).root.find(key), "\(fixture) \(key)")
    }

    func props(_ fixture: String, _ key: String) throws -> Props { Props(try node(fixture, key).props) }

    func props(_ json: String) throws -> Props { Props(try #require(try JSONValue(parsing: json).objectValue)) }

    // MARK: card

    @Test func cards() throws {
        let brief = CardModel(props: try props("workspace-agent", "r.0.1.0:b1"))
        #expect(brief.title == "Brief for Orders’ agent" && brief.subtitle == "Claude Code · asks before acting")
        let badge = try #require(brief.badge) // where the brief stands, a word
        #expect(brief.icon == "doc" && brief.tone == .accent && brief.accent == .accent)
        #expect(brief.note?.hasPrefix("This is what the tile agent is told first.") == true && brief.hasHeader)
        #expect(brief.accessibilityHeader == "Brief for Orders’ agent, \(badge), Claude Code · asks before acting")
        // its buttons are the button row, wherever they sit; the rest is the body, in order
        let kids = try node("workspace-agent", "r.0.1.0:b1").children
        let parts = CardModel.split(kids) { $0.type }
        #expect(parts.body.map(\.type) == ["field", "field", "field", "field", "picker", "toggle"])
        #expect(parts.buttons.map(\.type) == ["button"])
        let refusal = try node("workspace-agent", "r.0.1.0:c1")
        #expect(CardModel.split(refusal.children) { $0.type }.buttons.count == 2)
        #expect(CardModel.split(["button", "text", "button", "row"]) { $0 }.body == ["text", "row"])
        // tones: info and none are neutral; the rule's colour is never the only signal
        #expect(CardModel(props: try props("workspace-agent", "r.0.1.0:c3")).accent == nil)
        #expect(CardModel(props: try props("workspace-agent", "r.0.1.0:c2")).accent == .muted)
        #expect(CardModel(props: try props("workspace-agent", "r.0.1.0:c5")).accent == .danger)
        let bare = CardModel(props: Props(["tone": "loud", "title": ""]))
        #expect(bare.tone == nil && bare.title == nil && !bare.hasHeader && bare.accessibilityHeader == "")
    }

    // MARK: connection

    @Test func connections() throws {
        let c = ConnectionModel(props: try props("workspace-agent", "r.0.0"))
        #expect(c.connected == "14 tiles · 3 orgs" && c.not == "6 not connected" && c.model == "big" && !c.isPrivate && !c.disabled)
        #expect(c.modelText == "big")
        // the tile's own sentence is what VoiceOver reads
        #expect(c.accessibilityLabel == "Scope: 14 tiles in 3 orgs connected, 6 not connected. Model big.")
        let e = ConnectionModel(props: try props("workspace-agent-empty", "r.0"))
        #expect(e.not == nil && e.isPrivate && e.disabled && e.modelText == "local-llama · private")
        // without one, the segments in order
        let plain = ConnectionModel(connected: "14 tiles · 3 orgs", not: "6 not connected", model: "big", isPrivate: true)
        #expect(plain.accessibilityLabel == "Scope: 14 tiles · 3 orgs connected. 6 not connected. Model big, private.")
        #expect(ConnectionModel(props: Props([:])).accessibilityLabel == "Scope")
        #expect(ConnectionModel(props: Props([:])).modelText == nil && ConnectionModel(isPrivate: true).modelText == "private")
    }

    // MARK: request, session — drawn by the app

    @Test func trustedNodesCarryAnIdAndNothingElse() throws {
        #expect(XbinTrustedNode(type: "request", props: try props("workspace-agent", "r.0.1.0:r1")) == .request(id: "rq_7f3a91c2", kind: "session"))
        #expect(XbinTrustedNode(type: "request", props: try props("workspace-agent", "r.0.1.0:r3")) == .request(id: "cs_19e2", kind: "changeset"))
        #expect(XbinTrustedNode(type: "session", props: try props("workspace-agent", "r.0.1.0:s1")) == .session(id: "ts_41b0", tile: "apps/orders"))
        // Whatever else a tree says about one is never read: no text, no state, no button.
        let dressed = try props(#"{"id":"rq_1","kind":"call","text":"Allowed by you","state":"approved","title":"Approve","options":[{"id":"allow"}]}"#)
        #expect(XbinTrustedNode(type: "request", props: dressed) == .request(id: "rq_1", kind: "call"))
        #expect(XbinTrustedNode(type: "session", props: try props(#"{"id":"s1","status":"finished","line":"all done"}"#)) == .session(id: "s1", tile: ""))
        // an unknown kind reads as a call; no id, or another primitive: nothing to draw
        #expect(XbinTrustedNode(type: "request", props: try props(#"{"id":"rq_1","kind":"grant"}"#)) == .request(id: "rq_1", kind: "call"))
        #expect(XbinTrustedNode(type: "request", props: Props([:])) == nil)
        #expect(XbinTrustedNode(type: "request", props: Props(["id": ""])) == nil)
        #expect(XbinTrustedNode(type: "approval", props: Props(["id": "x"])) == nil)
        // the placeholder where no app draws them: a name, never a state
        #expect(XbinTrustedNode.request(id: "a", kind: "call").placeholderTitle == "Approval request")
        #expect(XbinTrustedNode.request(id: "a", kind: "changeset").placeholderTitle == "Admin changes")
        #expect(XbinTrustedNode.session(id: "a", tile: "apps/orders").placeholderTitle == "Tile agent · apps/orders")
        #expect(XbinTrustedNode.session(id: "a", tile: "").placeholderTitle == "Tile agent")
        #expect(XbinTrustedNode.placeholderTitle(type: "session") == "Tile agent" && XbinTrustedNode.placeholderTitle(type: "request") == "Approval request")
        #expect(XbinTrustedNode.session(id: "ts_1", tile: "t").id == "ts_1")
        // the vocabulary gives them no props beyond the id (and a kind, a tile) and no events
        let prims = try #require(try vocabJSON()["prims"]?.objectValue)
        #expect(Set(prims["request"]?["props"]?.objectValue?.keys.map { $0 } ?? []) == ["id", "kind"])
        #expect(Set(prims["session"]?["props"]?.objectValue?.keys.map { $0 } ?? []) == ["id", "tile"])
        #expect(prims["request"]?["events"]?.objectValue?.isEmpty == true && prims["session"]?["events"]?.objectValue?.isEmpty == true)
        #expect(Set((prims["request"]?["props"]?["kind"]?["enum"]?.arrayValue ?? []).compactMap(\.stringValue)) == XbinTrustedNode.requestKinds)
    }

    // MARK: the transcript's empty slot

    @Test func emptySlot() throws {
        let e = try #require(TranscriptEmpty(props: try props("workspace-agent-empty", "r.2")))
        #expect(e.title == "Ask for an outcome" && e.text?.hasPrefix("The workspace agent plans the work") == true)
        #expect(e.lists.map(\.title) == ["It can", "It can’t"] && e.lists[0].items.count == 3 && e.lists[1].items.last == "Use the web")
        #expect(e.suggestionsTitle == "Try one — from your tiles")
        #expect(e.suggestions.map(\.id) == ["ask", "fix", "change"] && e.suggestions[1].kind == "Fix")
        #expect(e.suggestions[1].accessibilityLabel == "Fix: Team calendar’s last sync failed. Find out why.")
        // no slot, an empty one, and suggestions that could report nothing
        #expect(TranscriptEmpty(props: try props("workspace-agent", "r.0.1")) == nil)
        #expect(TranscriptEmpty(props: try props(#"{"empty":{}}"#)) == nil)
        #expect(TranscriptEmpty(props: try props(#"{"empty":{"title":"","lists":[{"items":[]}]}}"#)) == nil)
        let odd = try #require(TranscriptEmpty(props: try props(
            #"{"empty":{"suggestions":[{"id":"a","text":"A"},{"text":"no id"},{"id":"a","text":"again"},{"id":"b","text":""},{"id":"c","kind":"","text":"C"}],"lists":[{"items":["x",3,""]}]}}"#)))
        #expect(odd.suggestions.map(\.id) == ["a", "c"] && odd.suggestions[1].kind == nil && odd.suggestions[1].accessibilityLabel == "C")
        #expect(odd.lists == [TranscriptEmpty.List(id: 0, title: nil, items: ["x", "3"])])
    }

    // MARK: plan rev 2

    @Test func planTagsNumbersAndNote() throws {
        let plan = ChatPlan(props: try props("workspace-agent", "r.0.1.0:p1"))
        #expect(plan.heading == "Plan" && plan.numbered && plan.trailing == "you said go ahead · 10:02")
        #expect(plan.entries.count >= 3 && plan.line(1) == "2. Have Orders’ own agent fix the rounding")
        #expect(plan.tags.count == plan.entries.count)
        let old = ChatPlan(props: try props("old-app-agent", "r.1.0"))
        #expect(old.tag(0) == ChatPlan.Tag(text: "done", icon: "check", tone: .ok) && old.tag(1)?.text == "asks you" && old.tag(1)?.tone == .warn)
        #expect(old.accessibilityLine(1) == "2. Rebuild the customer links in Invoices, not started, asks you")
        #expect(old.trailing == "you said go ahead · 10:02" && old.summary == "1 of 2 done" && old.tag(7) == nil && old.line(7) == "")
        #expect(old.note == "Each step that changes something asks when it is reached.")
        // tags are by position: an item without text is no tag for that entry
        let gaps = ChatPlan(props: try props(#"{"entries":[{"text":"a","status":"completed"},{"text":"b"},{"text":"c"}],"tags":[{"text":""},null,{"text":"asks you"}],"note":"n"}"#))
        #expect(gaps.tag(0) == nil && gaps.tag(1) == nil && gaps.tag(2)?.text == "asks you" && gaps.note == "n")
        #expect(gaps.line(0) == "a" && gaps.accessibilityLine(0) == "a, done")
        // a rev-1 plan reads as before
        let rev1 = ChatPlan(props: try props("chat-tools", "r.0.0:e3"))
        #expect(rev1.heading == "Plan" && rev1.trailing == "2 of 4 done" && !rev1.numbered && rev1.tags.isEmpty && rev1.note == nil)
        #expect(rev1.line(0) == rev1.entries[0].text)
    }

    // MARK: composer rev 2

    @Test func composerContextRepliesAndHint() throws {
        let c = ChatComposer(props: try props("workspace-agent", "r.0.2"))
        #expect(c.context == [ChatContextChip(id: "apps/orders", label: "About Orders", icon: "box", tone: .accent),
                              ChatContextChip(id: "apps/ledger", label: "About Ledger — not connected", icon: "lock", tone: .warn)])
        #expect(c.replies == [ChatReply(id: "diff", label: "Show me what changed"), ChatReply(id: "stop", label: "Stop the tile agents")])
        #expect(c.hint?.hasPrefix("Say what you want") == true && c.placeholder == "Reply, or ask for something else…")
        // items that could report nothing are not drawn
        let odd = ChatComposer(props: try props(
            #"{"context":[{"id":"a","label":"A"},{"label":"no id"},{"id":"a","label":"twice"},{"id":"b","label":""},7],"replies":[{"id":"go","label":"Go ahead"},{"id":"go","label":"again"},{"id":"","label":"x"}],"hint":""}"#))
        #expect(odd.context == [ChatContextChip(id: "a", label: "A")] && odd.replies == [ChatReply(id: "go", label: "Go ahead")] && odd.hint == nil)
        // a rev-1 composer has none of them
        let rev1 = ChatComposer(props: try props("chat-transcript", "r.1"))
        #expect(rev1.context.isEmpty && rev1.replies.isEmpty && rev1.hint == nil)
    }

    // MARK: question rev 2

    @Test func questionOptions() throws {
        let q = ChatQuestion(id: "q2", props: try props("workspace-agent", "r.0.1.0:q2"))
        #expect(q.options.style == .rows && q.options.paged && q.options.other == ["which": "whichOther", "notify": "notifyOther"])
        #expect(q.options.submitTitle == "Answer" && q.options.skipTitle == "Skip — let the agent look around first")
        // rev 1 reads as before: a form, "Submit", "Skip", no riders, one page
        let rev1 = ChatQuestion(id: "q1", props: try props("chat-tools", "r.0.0:q1"))
        #expect(rev1.options == QuestionOptions() && rev1.options.submitTitle == "Submit" && rev1.options.skipTitle == "Skip")
        #expect(rev1.session.questions == rev1.form.fields && !rev1.session.isPaged && rev1.session.pageLabel == nil)
        #expect(QuestionOptions(style: .rows).submitTitle == "Answer")
        // an `other` pair counts only between a choice and a text field of the form, a text field once
        let form = QuestionForm(schema: try JSONValue(parsing:
            #"{"properties":{"a":{"enum":["x","y"]},"b":{"enum":["p"]},"t":{"type":"string"},"n":{"type":"number"},"u":{"type":"string"}}}"#))
        let o = QuestionOptions(props: try props(#"{"style":"grid","other":{"a":"t","b":"t","t":"u","a2":"u","b2":7,"n":"u"}}"#), form: form)
        #expect(o.style == .form && o.other == ["a": "t"])
        #expect(QuestionOptions(props: try props(#"{"other":"t","paged":"yes"}"#), form: form) == QuestionOptions())
    }

    @Test func aChoiceOrSomethingElse() throws {
        let q = ChatQuestion(id: "q", props: try props("old-app-agent", "r.1.4"))
        var s = q.session
        let which = try #require(s.questions.first)
        #expect(s.questions.map(\.name) == ["which"] && s.rider(of: which)?.name == "other" && !s.isPaged)
        guard case .choice(let choices) = which.kind else { Issue.record("not a choice"); return }
        // nothing yet: an empty answer (nothing is required)
        #expect(s.content() == .success([:]) && !s.isAnswered(which))
        // choosing…
        s.choose("which", choices[1].value)
        #expect(s.isChosen(which, choices[1]) && !s.isChosen(which, choices[0]) && s.isAnswered(which))
        #expect(s.content() == .success(["which": "Everything since Monday"]))
        // …typing clears the choice: the answer is the text, under its own name
        s.set("other", "only invoice 1184")
        #expect(!s.isChosen(which, choices[1]) && s.value("which") == nil && s.text("other") == "only invoice 1184")
        #expect(s.content() == .success(["other": "only invoice 1184"]) && s.answerText(which) == "only invoice 1184")
        // …and choosing clears the text
        s.choose("which", choices[0].value)
        #expect(s.text("other") == "" && s.content() == .success(["which": "The three that are overdue"]))
        #expect(s.answerText(which) == "The three that are overdue")
        // un-choosing leaves nothing; emptied text leaves nothing
        s.choose("which", nil)
        #expect(s.content() == .success([:]))
        s.set("other", "x")
        s.set("other", "")
        #expect(s.value("other") == nil && s.content() == .success([:]))
        // spaces alone are no answer
        s.set("other", "   ")
        #expect(!s.isAnswered(which) && s.content() == .success([:]))
    }

    @Test func pagesAndRequiredAnswers() throws {
        let schema = try JSONValue(parsing: #"""
        {"type":"object","x-order":["which","whichOther","notify","notifyOther","note"],"required":["which","notify"],"properties":{
          "which":{"type":"string","title":"Which invoices?","enum":["Overdue","All"]},
          "whichOther":{"type":"string","title":"Something else"},
          "notify":{"type":"string","title":"Who should hear?","oneOf":[{"const":"nobody","title":"Nobody yet"},{"const":"finance","title":"Finance"}]},
          "notifyOther":{"type":"string","title":"Something else"},
          "note":{"type":"string","title":"Anything to add?"}}}
        """#)
        let form = QuestionForm(schema: schema)
        let options = QuestionOptions(style: .rows, other: ["which": "whichOther", "notify": "notifyOther"], paged: true, submitLabel: "Answer")
        var s = QuestionSession(form: form, options: options)
        #expect(s.questions.map(\.name) == ["which", "notify", "note"] && s.isPaged && s.pageCount == 3)
        #expect(s.visible.map(\.name) == ["which"] && s.pageLabel == "1 of 3" && s.primaryTitle == "Next" && !s.canGoBack && !s.isLastPage)
        // a required question holds the page until it has an answer — a choice or its text
        #expect(s.next()?.message == "Required: Which invoices?" && s.page == 0)
        s.set("whichOther", "only 1184")
        #expect(s.next() == nil && s.page == 1 && s.visible.map(\.name) == ["notify"] && s.pageLabel == "2 of 3" && s.canGoBack)
        // the whole form still says what is missing
        #expect(s.content() == .failure(QuestionForm.Missing(titles: ["Who should hear?"])))
        s.choose("notify", "finance")
        #expect(s.next() == nil && s.isLastPage && s.primaryTitle == "Answer" && s.visible.map(\.name) == ["note"])
        #expect(s.next() == nil && s.page == 2) // nothing past the last
        s.set("note", "before Friday")
        #expect(s.content() == .success(["whichOther": "only 1184", "notify": "finance", "note": "before Friday"]))
        #expect(s.answerText(form.field("notify")!) == "Finance")
        // back keeps what was answered
        s.back()
        s.back()
        s.back()
        #expect(s.page == 0 && s.text("whichOther") == "only 1184")
        // not paged: one page with every question, the submit's words at once
        let flat = QuestionSession(form: form, options: QuestionOptions(style: .rows, other: options.other))
        #expect(!flat.isPaged && flat.visible.count == 3 && flat.primaryTitle == "Answer" && flat.pageLabel == nil)
        #expect(flat.missingHere?.titles == ["Which invoices?", "Who should hear?"])
        // paged with one question is one page
        let one = QuestionSession(form: QuestionForm(schema: try JSONValue(parsing: #"{"properties":{"a":{"enum":["x"]}}}"#)),
                                  options: QuestionOptions(paged: true))
        #expect(!one.isPaged && one.pageLabel == nil && one.primaryTitle == "Submit")
        // values made by hand with both set: the text wins, never both
        #expect(form.content(["which": "All", "whichOther": "typed", "notify": "nobody"], other: options.other)
            == .success(["whichOther": "typed", "notify": "nobody"]))
        // no riders: exactly rev 1's content
        #expect(form.content(["which": "All"], other: [:]) == form.content(["which": "All"]))
    }

    @Test func settledQuestionsShowWhatWasAnswered() throws {
        let q1 = ChatQuestion(id: "q1", props: try props("workspace-agent", "r.0.1.0:q1"))
        #expect(q1.settled && q1.options.style == .form && !q1.options.paged)
        let s = q1.session
        #expect(s.answerText(try #require(s.questions.first)) == "This conversation")
        // settled with the text: the choice is not marked, the text is the answer
        let typed = ChatQuestion(id: "q", props: try props(
            #"{"schema":{"properties":{"a":{"enum":["x","y"]},"o":{"type":"string"}}},"style":"rows","other":{"a":"o"},"settled":{"o":"mine"}}"#))
        let a = try #require(typed.session.questions.first)
        guard case .choice(let cs) = a.kind else { Issue.record("not a choice"); return }
        #expect(typed.settled && !typed.session.isChosen(a, cs[0]) && typed.session.answerText(a) == "mine")
    }
}
