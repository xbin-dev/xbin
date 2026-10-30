import SwiftUI
import UIKit
import XbinCore

/// Settings → Connection (D204): how this workspace's requests travel — its
/// app tunnel (the path, the relay, the round trip, the layers of
/// encryption, both ends' ids, Reconnect) or its address.
struct ConnectionView: View {
    let workspace: WorkspaceModel
    /// The tunnel's recent events (TunnelLog, D211), newest last.
    @State private var events: [String] = []
    @State private var copied = false

    var body: some View {
        let status = workspace.tunnelStatus
        Form {
            Section {
                LabeledContent("Status") { Text(verbatim: status.summary(server: workspace.origin)) }
                    .accessibilityIdentifier("connection-status")
                if let relay = Self.relayLine(status, info: workspace.record.tunnel) {
                    LabeledContent("Relay") { Text(verbatim: relay).font(.callout.monospaced()) }
                }
                if case .connected(_, let rtt?) = status {
                    LabeledContent("Round trip") { Text(verbatim: "\(Int((rtt * 1000).rounded())) ms") }
                }
                LabeledContent("Encryption") { Text(verbatim: Self.encryption(workspace)) }
                if workspace.record.tunnel?.required == true {
                    Label("Required by the workspace: it refuses this device's app traffic outside the tunnel.", systemImage: "lock.shield")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            } header: { Text(verbatim: workspace.title) }

            Section {
                LabeledContent("Workspace") { Text(verbatim: workspace.origin.origin).font(.callout.monospaced()) }
                if workspace.isTunnelled {
                    LabeledContent("Local forwarder") {
                        Text(verbatim: workspace.transportOrigin.authority).font(.callout.monospaced())
                    }
                }
            } header: { Text("Address") } footer: {
                if workspace.isTunnelled {
                    Text("Pages and the app talk to the forwarder on this device, which carries each connection through the tunnel.")
                }
            }

            if workspace.isTunnelled {
                if let ids = TunnelManager.shared.endpointIDs(of: workspace.id) {
                    Section {
                        LabeledContent("This device") { Text(verbatim: Self.short(ids.device)).font(.callout.monospaced()) }
                        LabeledContent("Workspace") { Text(verbatim: Self.short(ids.server)).font(.callout.monospaced()) }
                    } header: { Text("Endpoints") } footer: {
                        Text("iroh's ids of the two ends; the tunnel checks the workspace's on every connection.")
                    }
                }
                Section {
                    Button("Reconnect", systemImage: "arrow.clockwise") { TunnelManager.shared.reconnect(workspace.id) }
                        .accessibilityIdentifier("connection-reconnect")
                }
                diagnostics
            }
        }
        .concreteBackground()
        .navigationTitle("Connection")
        .task {
            // The path and its round trip change as iroh finds a direct path
            // or falls back to the relay: read them while the screen shows.
            while !Task.isCancelled, workspace.isTunnelled {
                await TunnelManager.shared.refreshPath(workspace.id)
                events = TunnelLog.shared.lines(workspace: workspace.id)
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    /// What the tunnel did lately (D211): the newest events here, all of
    /// them a tap away, and the lot — with the app's version and the
    /// tunnel's state, no keys or tokens — copied for a bug report.
    @ViewBuilder private var diagnostics: some View {
        Section {
            ForEach(Array(events.suffix(8).enumerated()), id: \.offset) { _, line in
                Text(verbatim: line).font(.caption2.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if events.count > 8 {
                NavigationLink {
                    TunnelEventsView(workspace: workspace)
                } label: {
                    Text("All \(events.count) events")
                }
            }
            Button(copied ? "Copied" : "Copy diagnostics", systemImage: copied ? "checkmark" : "doc.on.doc") {
                UIPasteboard.general.string = TunnelManager.shared.diagnostics(for: workspace)
                copied = true
            }
            .accessibilityIdentifier("connection-copy-diagnostics")
        } header: { Text("Diagnostics") } footer: {
            Text("What the tunnel did lately: connects, drops, retries, the app going to the background and back. The copy holds no keys or tokens — paste it into a bug report.")
        }
    }

    /// The relay in use: its host when connected through it, else the one
    /// the record names (nil = n0's public relays).
    static func relayLine(_ status: TunnelStatus, info: TunnelInfo?) -> String? {
        guard let info else { return nil }
        if let r = info.relay, let host = URLComponents(string: r)?.host { return host }
        if case .connected(.relay(let host?), _) = status { return host }
        return "n0's public relays"
    }

    /// What protects the bytes on the way.
    static func encryption(_ w: WorkspaceModel) -> String {
        if w.isTunnelled, w.record.tunnel != nil { return "xbin tunnel (Noise) inside iroh (QUIC)" }
        if w.origin.isSecure { return "HTTPS (TLS)" }
        if w.record.isEncryptedInTransit { return "A private path (this device, or Tailscale's WireGuard)" }
        return "None: plain http — anyone on the network path can read it"
    }

    /// `abcd1234…wxyz` — enough to compare by eye.
    static func short(_ hex: String) -> String {
        guard hex.count > 16 else { return hex }
        return "\(hex.prefix(8))…\(hex.suffix(8))"
    }
}

/// Every event the tunnel's diagnostics hold for a workspace, newest first.
struct TunnelEventsView: View {
    let workspace: WorkspaceModel
    @State private var lines: [String] = []

    var body: some View {
        List {
            ForEach(Array(lines.reversed().enumerated()), id: \.offset) { _, line in
                Text(verbatim: line).font(.caption.monospaced()).textSelection(.enabled)
            }
        }
        .navigationTitle("Tunnel events")
        .task {
            while !Task.isCancelled {
                lines = TunnelLog.shared.lines(workspace: workspace.id)
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }
}
