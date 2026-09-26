import Foundation

// Agent session events (docs/protocol.md §Agent session events): the log
// entries `{seq, ts, type, data}` that `GET …/events`, `?follow=1` (NDJSON),
// `/ws/events` `session` frames and a past session's transcript all carry.
// `AgentEvent` keeps the raw payload; `payload` is its typed reading
// (EventPayloads.swift, with the open enums of EventEnums.swift).

public struct AgentEvent: JSONBacked, Sendable, Hashable {
    public let json: JSONValue
    /// From 1 per session; 0 only on a follow stream's synthetic `gap`.
    public let seq: UInt64
    /// Unix milliseconds.
    public let ts: Int64
    public let type: String
    public let data: JSONValue

    public init?(json: JSONValue) {
        guard json.object != nil, let type = json["type"]?.string, !type.isEmpty else { return nil }
        self.json = json
        seq = json["seq"]?.uint64 ?? 0
        ts = json["ts"]?.int64 ?? 0
        self.type = type
        data = json["data"] ?? .null
    }

    public init(seq: UInt64, ts: Int64 = 0, type: String, data: JSONValue) {
        var o = JSONObject()
        if seq > 0 { o["seq"] = .number(Double(seq)) }
        if ts != 0 { o["ts"] = .number(Double(ts)) }
        o["type"] = .string(type)
        if !data.isNull { o["data"] = data }
        json = .object(o)
        self.seq = seq
        self.ts = ts
        self.type = type
        self.data = data
    }

    public var eventType: AgentEventType? { AgentEventType(rawValue: type) }
    public var payload: AgentEventPayload { AgentEventPayload(type: type, data: data) }
}

public enum AgentEventType: String, Sendable, CaseIterable {
    case messageDelta = "message.delta"
    case thoughtDelta = "thought.delta"
    case plan
    case toolCall = "tool.call"
    case toolUpdate = "tool.update"
    case permissionRequest = "permission.request"
    case permissionResolved = "permission.resolved"
    case elicitationRequest = "elicitation.request"
    case elicitationResolved = "elicitation.resolved"
    case filesChanged = "files.changed"
    case turnEnd = "turn.end"
    case status
    case gap
}

public enum AgentEventPayload: Sendable, Hashable {
    case messageDelta(MessageDelta)
    case thoughtDelta(ThoughtDelta)
    case plan([PlanEntry])
    case toolCall(ToolCallUpdate)
    case toolUpdate(ToolCallUpdate)
    case permissionRequest(PermissionRequest)
    case permissionResolved(PermissionResolved)
    case elicitationRequest(ElicitationRequest)
    case elicitationResolved(ElicitationResolved)
    case filesChanged(FilesChanged)
    case turnEnd(TurnEnd)
    case status(StatusUpdate)
    case gap(before: UInt64)
    /// A type this client does not know, or a known type whose data is unreadable.
    case unknown(type: String, data: JSONValue)

    public init(type: String, data: JSONValue) {
        let d = data
        switch AgentEventType(rawValue: type) {
        case .messageDelta: self = .messageDelta(MessageDelta(json: d))
        case .thoughtDelta: self = .thoughtDelta(ThoughtDelta(json: d))
        case .plan: self = .plan(d["entries"]?.list(PlanEntry.self) ?? [])
        case .toolCall: self = ToolCallUpdate(json: d).map { .toolCall($0) } ?? .unknown(type: type, data: d)
        case .toolUpdate: self = ToolCallUpdate(json: d).map { .toolUpdate($0) } ?? .unknown(type: type, data: d)
        case .permissionRequest: self = PermissionRequest(json: d).map { .permissionRequest($0) } ?? .unknown(type: type, data: d)
        case .permissionResolved: self = PermissionResolved(json: d).map { .permissionResolved($0) } ?? .unknown(type: type, data: d)
        case .elicitationRequest: self = ElicitationRequest(json: d).map { .elicitationRequest($0) } ?? .unknown(type: type, data: d)
        case .elicitationResolved: self = ElicitationResolved(json: d).map { .elicitationResolved($0) } ?? .unknown(type: type, data: d)
        case .filesChanged: self = .filesChanged(FilesChanged(json: d))
        case .turnEnd: self = .turnEnd(TurnEnd(json: d))
        case .status: self = .status(StatusUpdate(json: d))
        case .gap: self = .gap(before: d["before"]?.uint64 ?? 0)
        case nil: self = .unknown(type: type, data: d)
        }
    }
}

// MARK: - /ws/events

/// A `session` frame on `/ws/events`: an agent event of one of the user's
/// sessions, `{type:"session", topic:"session.<id>", component, data:{seq,
/// ts, type, data, user, id}}`.
public struct SessionHubEvent: Sendable, Hashable {
    public let sessionID: String
    public let user: String
    /// The tile (cwd) the session runs on.
    public let component: String
    public let event: AgentEvent

    public init?(json: JSONValue) {
        guard json["type"]?.string == "session", let d = json["data"], let ev = AgentEvent(json: d), ev.seq > 0 else { return nil }
        let topic = json["topic"]?.string ?? ""
        sessionID = d["id"]?.string ?? (topic.hasPrefix("session.") ? String(topic.dropFirst(8)) : "")
        guard !sessionID.isEmpty else { return nil }
        user = d["user"]?.string ?? ""
        component = json["component"]?.string ?? ""
        event = ev
    }
}
