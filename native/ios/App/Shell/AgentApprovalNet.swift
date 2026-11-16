import SwiftUI
import XbinCore
import XbinRenderer

// The approval sheet's rows for the web and MCP tools (D239), as the web
// chrome draws them (web/bx-agent-approve.js): a web page by its whole
// address, why xbin asks, asked every time; an MCP tool by its server — an
// outside one's host said plainly (D240) — the tool, its read-only mark (the server's word), the server's own words
// quoted, the arguments exactly as they will be sent — every name and word
// a tile or the model wrote revealed (AgentReveal), as the web's shown(). Split from
// AgentApproval.swift for its size; the words are XbinCore's
// (DelegationNetWords.swift, held to the web's).

extension AgentReveal {
    /// A sheet's heading with every part revealed — a tile's, a server's or
    /// a tool's name is the tile's own text — the strong part bold.
    static func heading(_ h: ApprovalHeading) -> AttributedString {
        var a = attributed(h.before, font: .title3)
        a += attributed(h.strong, font: .title3.bold())
        a += attributed(h.after, font: .title3)
        return a
    }
}

/// Above any request whose conversation has read the web: what it asks may
/// come from a page it read.
struct AgentReadWebBanner: View {
    var body: some View {
        Label { Text(verbatim: DelegationWords.readWeb).font(.callout) } icon: { Image(systemName: "exclamationmark.triangle") }
            .foregroundStyle(XbinColor.warn)
            .padding(10).frame(maxWidth: .infinity, alignment: .leading)
            .background(XbinColor.warnBackground, in: .xbinPlate)
            .accessibilityIdentifier("approval-read-web")
    }
}

/// A web page's or an MCP tool call's rows.
struct AgentNetRows: View {
    let request: DelegateRequest

    var body: some View {
        if request.kind == .web { web } else { mcp }
    }

    @ViewBuilder private var web: some View {
        let w = DelegationWords.webRows(request)
        VStack(alignment: .leading, spacing: 0) {
            ApprovalRow(label: "Address") {
                Text(AgentReveal.attributed(w.url, font: XbinFont.code))
                    .textSelection(.enabled)
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                    .background(XbinColor.surface2, in: .xbinPlate)
                Text("fetched exactly like this — a GET, nothing else sent, no sign-in").font(.caption).foregroundStyle(XbinColor.muted)
            }
            .accessibilityIdentifier("approval-url")
            ApprovalRow(label: "Site") { Text(AgentReveal.attributed(w.host, font: XbinFont.code)) }
            ApprovalRow(label: "Why xbin asks") { Text(AgentReveal.attributed(w.why, font: .callout)) }
        }
        Text(verbatim: w.once).font(.caption).foregroundStyle(XbinColor.muted)
    }

    @ViewBuilder private var mcp: some View {
        let m = DelegationWords.mcpRows(request)
        if !m.outsideLine.isEmpty { // an outside server (D240): said plainly, its host xbind's
            Label {
                VStack(alignment: .leading, spacing: 2) {
                    Text(AgentReveal.attributed(m.outsideLine, font: .callout.weight(.semibold)))
                    Text("What is sent goes out of the workspace, to the server an admin listed.").font(.caption)
                }
            } icon: { Image(systemName: "globe") }
                .foregroundStyle(XbinColor.warn)
                .padding(10).frame(maxWidth: .infinity, alignment: .leading)
                .background(XbinColor.warnBackground, in: .xbinPlate)
                .accessibilityIdentifier("approval-outside")
        }
        if !m.eachLine.isEmpty {
            Text(AgentReveal.attributed(m.eachLine, font: .caption.weight(.semibold))).foregroundStyle(XbinColor.warn)
                .accessibilityIdentifier("approval-each")
        }
        let shownPath = m.call.outside ? m.call.host : m.call.path
        VStack(alignment: .leading, spacing: 0) {
            ApprovalRow(label: "Server") {
                Text(AgentReveal.attributed(m.call.server, font: .body.weight(.semibold)))
                if !shownPath.isEmpty, shownPath != m.call.server { Text(AgentReveal.attributed(shownPath, font: XbinFont.code)).foregroundStyle(XbinColor.muted) }
            }
            ApprovalRow(label: "Tool") {
                Text(AgentReveal.attributed(m.call.tool, font: XbinFont.code.weight(.semibold)))
                if !m.call.title.isEmpty { Text(AgentReveal.attributed(m.call.title, font: .callout)).foregroundStyle(XbinColor.muted) }
                Text(verbatim: m.marked).font(.caption).foregroundStyle(m.call.readOnly ? XbinColor.muted : XbinColor.warn)
                if !m.changedLine.isEmpty {
                    Text(verbatim: m.changedLine).font(.caption.weight(.semibold)).foregroundStyle(XbinColor.danger)
                        .accessibilityIdentifier("approval-tool-changed")
                }
            }
            .accessibilityIdentifier("approval-tool")
            ApprovalRow(label: "Arguments") {
                Text(AgentReveal.attributed(m.call.arguments.isEmpty ? "{}" : m.call.arguments, font: XbinFont.code))
                    .textSelection(.enabled)
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                    .background(XbinColor.surface2, in: .xbinPlate)
                Text("sent exactly like this").font(.caption).foregroundStyle(XbinColor.muted)
            }
            ApprovalRow(label: "Undo") { Text(AgentReveal.attributed(m.undo, font: .callout)) }
        }
        if !m.call.description.isEmpty { AgentQuote(label: "The server's own words — not checked by xbin", text: m.call.description) }
    }
}
