import SwiftUI
import XbinCore
import XbinRenderer

// The approval sheet's rows for the web and MCP tools (D239), as the web
// chrome draws them (web/bx-agent-approve.js): a web page by its whole
// address, why xbin asks, asked every time; an MCP tool by its server, the
// tool, its read-only mark (the server's word), the server's own words
// quoted, the arguments exactly as they will be sent. Split from
// AgentApproval.swift for its size; the words are XbinCore's
// (DelegationNetWords.swift, held to the web's).

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
            ApprovalRow(label: "Site") { Text(verbatim: w.host).font(XbinFont.code) }
            ApprovalRow(label: "Why xbin asks") { Text(verbatim: w.why).font(.callout) }
        }
        Text(verbatim: w.once).font(.caption).foregroundStyle(XbinColor.muted)
    }

    @ViewBuilder private var mcp: some View {
        let m = DelegationWords.mcpRows(request)
        VStack(alignment: .leading, spacing: 0) {
            ApprovalRow(label: "Server") {
                Text(verbatim: m.call.server).font(.body.weight(.semibold))
                if !m.call.path.isEmpty, m.call.path != m.call.server { Text(verbatim: m.call.path).font(XbinFont.code).foregroundStyle(XbinColor.muted) }
            }
            ApprovalRow(label: "Tool") {
                Text(verbatim: m.call.tool).font(XbinFont.code.weight(.semibold))
                if !m.call.title.isEmpty { Text(verbatim: m.call.title).font(.callout).foregroundStyle(XbinColor.muted) }
                Text(verbatim: m.marked).font(.caption).foregroundStyle(m.call.readOnly ? XbinColor.muted : XbinColor.warn)
            }
            .accessibilityIdentifier("approval-tool")
            ApprovalRow(label: "Arguments") {
                Text(AgentReveal.attributed(m.call.arguments.isEmpty ? "{}" : m.call.arguments, font: XbinFont.code))
                    .textSelection(.enabled)
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                    .background(XbinColor.surface2, in: .xbinPlate)
                Text("sent exactly like this").font(.caption).foregroundStyle(XbinColor.muted)
            }
            ApprovalRow(label: "Undo") { Text(verbatim: m.undo).font(.callout) }
        }
        if !m.call.description.isEmpty { AgentQuote(label: "The server's own words — not checked by xbin", text: m.call.description) }
    }
}
