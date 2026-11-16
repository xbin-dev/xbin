import Foundation

// The web and MCP tools of a person's workspace agent (D236), in the web
// chrome's words (/vendor/agent-chrome-words.js: webOf, mcpOf, webRows,
// mcpRows, netPrefs, and the web and MCP branches of needTitle, heading,
// grantChoices, allowLabel and did) — held to it by DelegationWordsTests.
// Split from DelegationWords.swift for its size.

/// A web request's page as xbind previews it.
public struct WebPage: Sendable, Equatable {
    public var url: String
    public var host: String
    /// Why xbin asks (the conversation holds an organisation's data).
    public var why: String
    public var orgs: [String]
}

/// An MCP request's tool as xbind previews it.
public struct MCPToolCall: Sendable, Equatable {
    /// The server's name ("Dice"), else its path.
    public var server: String
    public var path: String
    public var tool: String
    public var title: String
    /// The server's own words — shown quoted, as unchecked.
    public var description: String
    /// The server's mark, its word only.
    public var readOnly: Bool
    /// The arguments as they will be sent.
    public var arguments: String
    /// What "always, this tool" covers, in xbind's words ("" none).
    public var standing: String
}

/// One of the Preferences controls of the web and MCP tools.
public struct NetPref: Sendable, Equatable, Identifiable {
    /// "web" | "mcp" | "mcpReadOnly" — the field of `PUT /delegate/state`.
    public var id: String
    public var label: String
    public var note: String
    public var on: Bool
    /// Whether the person may turn it on now; `why` says why not.
    public var allowed: Bool
    public var why: String
    /// Drawn under the one before it.
    public var sub: Bool
}

extension DelegationWords {
    /// What a sheet says when the conversation that asks has read the web
    /// (`preview.readWeb`).
    public static let readWeb = "This conversation has read the web: what your agent asks may come from a page it read."

    /// The host of a URL ("" when none) — the web's `webHost`.
    public static func webHost(_ url: String) -> String {
        guard let sep = url.range(of: "://") else { return "" }
        let scheme = url[..<sep.lowerBound]
        guard !scheme.isEmpty, scheme.unicodeScalars.allSatisfy({ ("a"..."z").contains($0) || ("A"..."Z").contains($0) }) else { return "" }
        let host = url[sep.upperBound...].prefix { !"/?#:".contains($0) }
        return String(host)
    }

    public static func webOf(_ r: DelegateRequest) -> WebPage {
        let p = r.preview
        let url = p["url"]?.stringValue ?? ""
        let host = p["host"]?.stringValue ?? ""
        return WebPage(url: url, host: host.isEmpty ? webHost(url) : host, why: p["why"]?.stringValue ?? "",
                       orgs: (p["orgs"]?.arrayValue ?? []).compactMap(\.stringValue))
    }

    public static func mcpOf(_ r: DelegateRequest) -> MCPToolCall {
        let p = r.preview
        let first = [p["serverName"]?.stringValue, p["server"]?.stringValue, lastOf(r.tilePath)].compactMap { $0 }.first { !$0.isEmpty }
        let path = p["server"]?.stringValue ?? ""
        return MCPToolCall(server: first ?? "an MCP server", path: path.isEmpty ? r.tilePath : path, tool: p["tool"]?.stringValue ?? "",
                           title: p["title"]?.stringValue ?? "", description: p["description"]?.stringValue ?? "",
                           readOnly: p["readOnly"]?.boolValue == true, arguments: p["arguments"]?.stringValue ?? "",
                           standing: p["standing"]?["tool"]?.stringValue ?? "")
    }

    /// A web request as the sheet's rows.
    public static func webRows(_ r: DelegateRequest) -> (url: String, host: String, why: String, once: String) {
        let w = webOf(r)
        return (w.url, w.host, w.why.isEmpty ? "This conversation holds data of an organisation: the address itself can carry it out of the workspace." : w.why,
                "Reading a page is asked every time: the address is the data.")
    }

    /// An MCP request as the sheet's rows: the tool, its mark in words, and who can undo it.
    public static func mcpRows(_ r: DelegateRequest) -> (call: MCPToolCall, marked: String, undo: String) {
        let m = mcpOf(r)
        return (m, m.readOnly ? "Its server marks it read-only — the server’s word, not xbin’s." : "Its server doesn’t mark it read-only: it may change things.",
                "xbin can't undo this. Whether \(m.server) can is up to \(m.server).")
    }

    static func netNeedTitle(_ r: DelegateRequest) -> String {
        if r.kind == .web { return "Read a web page on \(nonEmpty(webOf(r).host, "the web"))" }
        let m = mcpOf(r)
        return "Run \(nonEmpty(m.tool, "a tool")) on \(m.server)"
    }

    static func netHeading(_ r: DelegateRequest) -> ApprovalHeading {
        if r.kind == .web { return ApprovalHeading(before: "Your agent wants to read a page on ", strong: nonEmpty(webOf(r).host, "the web"), after: "") }
        let m = mcpOf(r)
        return ApprovalHeading(before: "Your agent wants to run ", strong: nonEmpty(m.tool, "a tool"), after: " on \(m.server)")
    }

    /// An MCP tool's two ways to allow: once, or always this tool.
    static func mcpChoices(_ r: DelegateRequest) -> [GrantChoice] {
        let m = mcpOf(r)
        return [
            GrantChoice(id: .once, label: "Once", text: "This call only. The agent has 15 minutes to make it.", wider: false),
            GrantChoice(id: .tool, label: "Always, this tool",
                        text: m.standing.isEmpty ? "\(m.tool) on \(m.server), with any arguments, without asking again." : m.standing, wider: false),
        ]
    }

    static func netAllowLabel(_ r: DelegateRequest, choice: GrantChoice.ID) -> String {
        r.kind == .web ? "Read this page" : choice == .tool ? "Allow always" : "Allow once"
    }

    /// The web's and MCP's rows of the log ("" class: nil, the general sentence).
    static func netDid(_ row: AuditRow) -> Did? {
        let t = lastOf(row.tile)
        switch row.cls {
        case "web":
            let host = webHost(row.path)
            if !row.refused.isEmpty { return Did(text: "Was refused a web page on ", strong: host, detail: row.refused, icon: "lock", group: "refused", at: row.ts) }
            let detail = [row.path, row.bytes > 0 ? bytes(row.bytes) : "", row.ticket.isEmpty ? "" : "you allowed it once"].filter { !$0.isEmpty }
            return Did(text: "Read a web page on ", strong: host, detail: detail.joined(separator: " · "), icon: "globe", group: "change", at: row.ts)
        case "mcp" where row.refused.isEmpty:
            let detail = [row.under == "preference" ? "without asking: marked read-only, your preference" : underWords(row), row.status >= 400 ? "failed" : ""]
            return Did(text: "Ran a tool of ", strong: t, detail: detail.filter { !$0.isEmpty }.joined(separator: " · "), icon: "plug", group: "change", at: row.ts)
        case "mcp-read" where row.refused.isEmpty:
            return Did(text: "Read the tools of ", strong: t, detail: "", icon: "eye", group: "read", at: row.ts)
        default:
            return nil
        }
    }

    /// The Preferences controls of the web and MCP tools, from
    /// `GET /delegate/state`: the person's own settings and what the
    /// workspace policy lets them turn on. Turning one on widens — armed
    /// like Allow (``AgentWidening``); off is never held.
    public static func netPrefs(_ state: DelegateState?) -> [NetPref] {
        let web = state?.netWeb ?? DelegateState.NetReach(), mcp = state?.netMCP ?? DelegateState.NetReach()
        let webOk = !web.policy.isEmpty && web.policy != "off", mcpOk = !mcp.policy.isEmpty && mcp.policy != "off"
        let person = (web: state?.web ?? false, mcp: state?.mcp ?? false, readOnly: state?.mcpReadOnly ?? false)
        let only = web.only.isEmpty ? "" : " Only \(web.only.joined(separator: ", "))."
        let off = "Off for this workspace — an admin can change it."
        return [
            NetPref(id: "web", label: "Let my agent read web pages",
                    note: "It reads public pages by their address, through xbin: nothing is sent but the address. Once a conversation holds your organisation’s data, each page is one you approve.\(only)",
                    on: person.web && webOk, allowed: webOk, why: webOk ? "" : nonEmpty(web.why, off), sub: false),
            NetPref(id: "mcp", label: "Let my agent use MCP tools from this workspace’s tiles",
                    note: "As you, on the tiles you can reach. Each tool asks you the first time.",
                    on: person.mcp && mcpOk, allowed: mcpOk, why: mcpOk ? "" : nonEmpty(mcp.why, off), sub: false),
            NetPref(id: "mcpReadOnly", label: "MCP tools marked read-only don’t ask me",
                    note: "The mark is the tool’s server’s word. Tools that aren’t marked still ask.",
                    on: person.readOnly && mcpOk && person.mcp, allowed: mcpOk && person.mcp,
                    why: !mcpOk || person.mcp ? "" : "Turn on MCP tools first.", sub: true),
        ]
    }

    private static func nonEmpty(_ s: String, _ or: String) -> String { s.isEmpty ? or : s }
}
