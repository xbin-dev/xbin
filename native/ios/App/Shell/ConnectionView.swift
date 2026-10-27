import SwiftUI
import XbinCore

/// Settings → Connection (D204): how this workspace's requests travel — its
/// app tunnel (the path, the relay, the round trip, the layers of
/// encryption, both ends' ids, Reconnect) or its address.
struct ConnectionView: View {
    let workspace: WorkspaceModel

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
            }
        }
        .concreteBackground()
        .navigationTitle("Connection")
        .task {
            // The path and its round trip change as iroh finds a direct path
            // or falls back to the relay: read them while the screen shows.
            while !Task.isCancelled, workspace.isTunnelled {
                await TunnelManager.shared.refreshPath(workspace.id)
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
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
