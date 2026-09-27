import Foundation

// AgentSessionFeed keeps one session's transcript current (plans/native.md
// §13: "applies by seq, refetches on gaps, reconnect and foreground"): it
// replays the log, follows it (`?follow=1`, reconnecting with backoff), takes
// `/ws/events` `session` frames as they come, and re-reads `?since=` whenever
// a seq is skipped, a stream drops, or the app returns to the foreground.
// The screen observes `updates()`; the actions answer through the client and
// then catch up at once (the web does the same), so the answer shows even
// before the live stream carries it. `followStates()` says whether the live
// stream is open, so a screen can say so when it isn't.

public actor AgentSessionFeed {
    public nonisolated let client: AgentClient
    public nonisolated let sessionID: String
    public private(set) var transcript = AgentTranscript()
    /// The session is gone server-side (404) or reported exited/error: no
    /// more polling; the transcript stays readable.
    public private(set) var ended = false
    /// The last refusal an action or a catch-up hit (shown in the status line).
    public private(set) var lastError: AgentAPIError?
    /// Plan feedback waiting for the rejected turn to settle.
    public private(set) var followUp: FollowUp?
    /// Text handed back to the composer (a follow-up that could not be sent).
    public private(set) var returnedDraft: String?

    public struct FollowUp: Sendable, Hashable {
        public var text: String
        /// Send once a status after this seq says idle.
        public var after: UInt64
    }

    /// The live stream (`run()`), as a screen shows it.
    public enum FollowState: Sendable, Hashable {
        /// Opening it (the first time, or again after it ended).
        case connecting
        /// Open: events arrive as the agent logs them.
        case live
        /// It can't be opened (retrying with backoff): why. Also when an
        /// attempt has had no answer for `stallAfter`.
        case failing(String)
        /// The session is gone: nothing more to follow.
        case ended
    }

    /// The live stream's state now.
    public private(set) var followState = FollowState.connecting

    private var subscribers: [Int: AsyncStream<AgentTranscript>.Continuation] = [:]
    private var followSubscribers: [Int: AsyncStream<FollowState>.Continuation] = [:]
    private var nextSub = 0
    private var attempt = 0
    private let sleep: @Sendable (Duration) async throws -> Void
    private let stallAfter: Duration

    /// `sleep` is injectable for tests (reconnect backoff); `stallAfter` is
    /// how long an attempt to open the stream may go unanswered before it
    /// counts as failing (it keeps waiting).
    public init(client: AgentClient, sessionID: String, stallAfter: Duration = .seconds(10),
                sleep: @escaping @Sendable (Duration) async throws -> Void = { try await Task.sleep(for: $0) }) {
        self.client = client
        self.sessionID = sessionID
        self.stallAfter = stallAfter
        self.sleep = sleep
    }

    /// The transcript now and after every change.
    public func updates() -> AsyncStream<AgentTranscript> {
        let (stream, cont) = AsyncStream<AgentTranscript>.makeStream(bufferingPolicy: .bufferingNewest(1))
        let k = nextSub
        nextSub += 1
        subscribers[k] = cont
        cont.yield(transcript)
        cont.onTermination = { [weak self] _ in
            Task { await self?.unsubscribe(k) }
        }
        return stream
    }

    private func unsubscribe(_ k: Int) { subscribers[k] = nil }

    /// The live stream's state now and after every change.
    public func followStates() -> AsyncStream<FollowState> {
        let (stream, cont) = AsyncStream<FollowState>.makeStream(bufferingPolicy: .bufferingNewest(1))
        let k = nextSub
        nextSub += 1
        followSubscribers[k] = cont
        cont.yield(followState)
        cont.onTermination = { [weak self] _ in
            Task { await self?.unfollow(k) }
        }
        return stream
    }

    private func unfollow(_ k: Int) { followSubscribers[k] = nil }

    private func setFollow(_ s: FollowState) {
        guard s != followState else { return }
        followState = s
        for c in followSubscribers.values { c.yield(s) }
    }

    /// An attempt still unanswered after `stallAfter` counts as failing.
    private func stalled(_ n: Int) {
        guard n == attempt, followState == .connecting else { return }
        setFollow(.failing("no answer from the server"))
    }

    private func publish() {
        for c in subscribers.values { c.yield(transcript) }
    }

    // MARK: sync

    /// Re-reads the log after the cursor (on a skipped seq, a reconnect, a
    /// return to the foreground). A 404 ends the feed.
    public func catchUp() async {
        do {
            let since = transcript.lastSeq
            let page = try await client.events(sessionID, since: since)
            transcript.apply(page: page, since: since)
            lastError = nil
            afterApply()
        } catch let e as AgentAPIError {
            if e.isNotFound { markEnded() } else { lastError = e }
            publish()
        } catch {
            publish()
        }
    }

    /// A `/ws/events` frame (ignored unless it is this session's).
    public func receive(_ hub: SessionHubEvent) async {
        guard hub.sessionID == sessionID else { return }
        if case .refetch = transcript.receiveLive(hub.event) {
            await catchUp()
            return
        }
        afterApply()
    }

    /// Follows the log until the session ends or the task is cancelled:
    /// replay from the cursor, stream, and on a drop re-read and reconnect
    /// with backoff (0.5 s doubling to 15 s; reset once events flow).
    ///
    /// It asks from one event before the cursor: the answer then starts with
    /// an event it already has (skipped by seq), so the stream opens at once
    /// even on a session with nothing new to say — xbind before 2026-09-27
    /// sent a follow's response head only with its first event, and a client
    /// waiting for it hung until its request timed out.
    public func run() async {
        var backoff = Duration.milliseconds(500)
        while !Task.isCancelled, !ended {
            if case .failing = followState {} else { setFollow(.connecting) }
            let stream: AsyncThrowingStream<AgentEvent, any Error>
            do {
                stream = try await open()
            } catch let e as AgentAPIError where e.isNotFound {
                markEnded()
                publish()
                return
            } catch {
                if Task.isCancelled { return }
                // Not opened: the screen says why while it retries.
                setFollow(.failing((error as? AgentAPIError)?.description ?? error.localizedDescription))
                try? await sleep(backoff)
                backoff = min(backoff * 2, .seconds(15))
                continue
            }
            setFollow(.live)
            do {
                for try await e in stream {
                    backoff = .milliseconds(500)
                    if case .refetch = transcript.receiveStream(e) {
                        await catchUp()
                    } else {
                        afterApply()
                    }
                }
                // the server ends the stream when the session goes; a proxy may cut it too
                await catchUp()
                if transcript.state.isEnded { markEnded(); publish() }
            } catch {
                // A drop (an idle stream timing out included): reconnect
                // quietly — the stream was open, and the next one replays
                // from the cursor.
                if Task.isCancelled { return }
            }
            if ended || Task.isCancelled { return }
            try? await sleep(backoff)
            backoff = min(backoff * 2, .seconds(15))
        }
    }

    /// Opens the stream from one event before the cursor. An attempt with no
    /// answer after `stallAfter` counts as failing while it keeps waiting.
    private func open() async throws -> AsyncThrowingStream<AgentEvent, any Error> {
        attempt += 1
        let n = attempt, stallAfter = self.stallAfter
        let watchdog = Task { [weak self] in
            try? await Task.sleep(for: stallAfter)
            if Task.isCancelled { return }
            await self?.stalled(n)
        }
        defer { watchdog.cancel() }
        let last = transcript.lastSeq
        return try await client.follow(sessionID, since: last > 0 ? last - 1 : 0)
    }

    /// Loads a past session (read-only; nothing to follow).
    public func load(history: HistoryTranscript) {
        transcript = AgentTranscript(events: history.events)
        ended = true
        setFollow(.ended)
        publish()
    }

    private func markEnded() {
        ended = true
        setFollow(.ended)
        if let f = followUp { // not lost: back to the composer
            followUp = nil
            returnedDraft = f.text
        }
    }

    private func afterApply() {
        if transcript.state.isEnded { markEnded() }
        publish()
        guard let f = followUp else { return }
        switch transcript.lastStatus(after: f.after) {
        case .idle?:
            followUp = nil
            Task { await self.sendFollowUp(f.text) }
        case .error?, .exited?:
            followUp = nil
            returnedDraft = f.text
            publish()
        default:
            break
        }
    }

    private func sendFollowUp(_ text: String) async {
        do {
            _ = try await client.prompt(sessionID, text: text)
            await catchUp()
        } catch let e as AgentAPIError {
            lastError = e
            returnedDraft = text
            publish()
        } catch {
            returnedDraft = text
            publish()
        }
    }

    /// The composer took the returned text.
    public func takeReturnedDraft() -> String? {
        defer { returnedDraft = nil }
        return returnedDraft
    }

    // MARK: actions (each answers, then catches up)

    /// Sends a prompt (or a slash command as text). Throws the refusal (409
    /// while a turn runs; the composer keeps the draft).
    @discardableResult
    public func send(_ text: String) async throws -> PromptAccepted {
        let r = try await act { try await self.client.prompt(self.sessionID, text: text) }
        return r
    }

    /// Sends a prompt with files (text may then be empty). Over a limit it
    /// throws without a request (``PromptAttachment/check(_:)``).
    @discardableResult
    public func send(_ text: String, attachments: [PromptAttachment]) async throws -> PromptAccepted {
        try await act { try await self.client.prompt(self.sessionID, text: text, attachments: attachments) }
    }

    public func cancel() async throws { try await act { try await self.client.cancel(self.sessionID) } }

    /// Answers a permission (404: another client answered first — the
    /// catch-up shows who).
    public func answer(_ card: PermissionCard, choice: PermissionChoice) async throws {
        try await act { try await self.client.answerPermission(self.sessionID, pid: card.pid, choice.answer) }
    }

    /// "Keep planning" with feedback: rejects the plan, then sends the
    /// feedback as the next prompt once the rejected turn settles (the
    /// prompt route refuses while it runs); if the session ends first the
    /// text comes back as `returnedDraft`.
    public func keepPlanning(_ card: PermissionCard, choice: PermissionChoice, feedback: String) async throws {
        let text = trim(feedback)
        if !text.isEmpty { followUp = FollowUp(text: text, after: transcript.lastSeq) }
        do {
            try await act { try await self.client.answerPermission(self.sessionID, pid: card.pid, choice.answer) }
        } catch {
            if !text.isEmpty { followUp = nil }
            throw error
        }
    }

    /// Answers a question: accept with the form's values (required fields
    /// checked first — `FormError` lists the missing), decline or cancel.
    public func answer(_ card: QuestionCard, action: QuestionAction, values: [String: JSONValue] = [:]) async throws {
        var content: JSONValue?
        if action == .accept {
            let c = FormField.content(card.fields, values: values)
            let missing = FormField.missingRequired(card.fields, content: c)
            if !missing.isEmpty { throw FormError(missing: missing) }
            content = c
        }
        let body = content
        try await act { try await self.client.answerQuestion(self.sessionID, eid: card.eid, action: action, content: body) }
    }

    /// Changes a setting (a picker's id and value; "mode" for the mode picker).
    public func set(_ picker: String, to value: String) async throws {
        try await act { try await self.client.setOption(self.sessionID, option: picker, value: value) }
    }

    private func act<T: Sendable>(_ f: @Sendable () async throws -> T) async throws -> T {
        do {
            let r = try await f()
            lastError = nil
            await catchUp()
            return r
        } catch let e as AgentAPIError {
            lastError = e
            publish()
            throw e
        }
    }
}

/// An accept whose required answers are missing ("answer Name, Port").
public struct FormError: Error, Sendable, Hashable, CustomStringConvertible {
    public var missing: [String]
    public var description: String { "answer " + missing.joined(separator: ", ") }
}
