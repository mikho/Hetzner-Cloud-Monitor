import Charts
import SwiftUI

struct ServerDetailView: View {
    @Environment(Store.self) private var store
    var server: MonitorServer
    @State private var showInstall = false
    @State private var showThresholds = false
    @State private var pendingAction: AgentAction?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                if !server.alerts.isEmpty { alertsBox }
                if let h = server.hetzner { hetznerBox(h) }
                if let a = server.agent {
                    AgentPanel(agent: a, pendingAction: $pendingAction)
                } else {
                    GroupBox {
                        HStack {
                            VStack(alignment: .leading, spacing: 4) {
                                Text("No agent installed").font(.headline)
                                Text("Install the read-only agent to see memory, disks, updates, failed services and Docker.")
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            Button("Install agent…") { showInstall = true }
                        }
                        .padding(6)
                    }
                }
            }
            .padding(20)
        }
        .navigationTitle(server.name)
        .toolbar {
            Button { showThresholds = true } label: { Label("Alert levels for this server", systemImage: "bell.badge") }
        }
        .sheet(isPresented: $showInstall) {
            InstallSheet(prefill: .init(name: server.name, host: server.hetzner?.ipv4 ?? "", hetznerId: server.hetzner?.id))
        }
        .sheet(isPresented: $showThresholds) { ThresholdsView(serverKey: server.key, serverName: server.name) }
        .sheet(item: $pendingAction) { action in
            if let a = server.agent { AgentActionSheet(agent: a, action: action) }
        }
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline) {
            LevelDot(level: server.level)
            Text(server.name).font(.title.bold())
            if let p = server.project { Text(p).foregroundStyle(.secondary) }
            Spacer()
            Text(server.level.uppercased()).font(.caption.bold()).foregroundStyle(levelColor(server.level))
        }
    }

    private var alertsBox: some View {
        GroupBox("Alerts") {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(server.alerts, id: \.metric) { a in
                    HStack {
                        Image(systemName: a.level == "critical" ? "exclamationmark.octagon.fill" : "exclamationmark.triangle.fill")
                            .foregroundStyle(levelColor(a.level))
                        Text(a.label).bold()
                        Text(describe(a)).foregroundStyle(.secondary)
                        Spacer()
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(4)
        }
    }

    private func hetznerBox(_ h: HetznerServer) -> some View {
        GroupBox("Hetzner Cloud") {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
                row("Status", h.status)
                row("Type", [h.type, h.cores.map { "\($0) vCPU" }, h.memory.map { "\(Int($0)) GB RAM" }, h.disk.map { "\(Int($0)) GB disk" }].compactMap { $0 }.joined(separator: " · "))
                row("Location", h.location ?? "")
                row("IPv4", h.ipv4 ?? "none")
                row("IPv6", h.ipv6 ?? "none")
                row("Backups", h.backups == true ? "enabled" : "off")
                if let c = h.cost { row("Cost", String(format: "€%.2f / month", c.total)) }
                if let inc = h.traffic?.included, inc > 0 {
                    row("Traffic out", "\(formatBytes(h.traffic?.out ?? 0)) of \(formatBytes(inc))")
                }
            }
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(4)
        }
    }
}

func row(_ label: String, _ value: String) -> some View {
    GridRow {
        Text(label).foregroundStyle(.secondary)
        Text(value)
    }
}

enum AgentAction: String, Identifiable {
    case upgrade, dockerOn = "docker-on", dockerOff = "docker-off", rotate, uninstall
    var id: String { rawValue }
    var title: String {
        switch self {
        case .upgrade: return "Update agent"
        case .dockerOn: return "Enable Docker monitoring"
        case .dockerOff: return "Disable Docker monitoring"
        case .rotate: return "Rotate keys"
        case .uninstall: return "Uninstall agent"
        }
    }
    var explanation: String {
        switch self {
        case .upgrade: return "Uploads the agent bundled with the dashboard and restarts it. Key, certificate and settings stay."
        case .dockerOn: return "Gives the agent's user read access to the Docker socket (the docker group). The agent only sends GET requests to Docker, but membership of the docker group is close to root access if the agent were ever compromised."
        case .dockerOff: return "Removes the agent's access to the Docker socket."
        case .rotate: return "Creates a new pairing key and a new TLS certificate. The old ones stop working right away."
        case .uninstall: return "Stops the agent and removes its binary, user, configuration and history from the server."
        }
    }
}

struct AgentPanel: View {
    @Environment(Store.self) private var store
    var agent: AgentInfo
    @Binding var pendingAction: AgentAction?
    @State private var editingAddress = false
    @State private var address = ""
    @State private var actionError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            GroupBox {
                VStack(alignment: .leading, spacing: 8) {
                    HStack {
                        Image(systemName: agent.status == "online" ? "checkmark.circle.fill" : "xmark.octagon.fill")
                            .foregroundStyle(agent.status == "online" ? .green : .red)
                        Text("Agent \(agent.status)").font(.headline)
                        if let v = agent.version { Text("v\(v)").foregroundStyle(.secondary) }
                        Spacer()
                        menu
                    }
                    if let e = agent.error { Text(e).foregroundStyle(.red).font(.callout).textSelection(.enabled) }
                    Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 4) {
                        row("Address", "\(agent.agentHost):\(agent.agentPort)")
                        row("SSH", "\(agent.sshUser)@\(agent.sshHost):\(agent.sshPort)")
                        if let s = agent.snapshot { row("System", "\(s.host.os), \(s.host.kernel), \(s.host.arch)") }
                        if let s = agent.snapshot { row("Up since", relative(s.host.bootTime)) }
                        if let allow = agent.allowFrom, !allow.isEmpty { row("Allowed from", allow.joined(separator: ", ")) }
                        row("TLS pin", agent.fingerprint)
                        if let fp = agent.hostKeyFingerprint { row("SSH host key", fp) }
                    }
                    .font(.callout)
                    .textSelection(.enabled)
                    if let actionError { Text(actionError).foregroundStyle(.red).font(.callout) }
                }
                .padding(4)
            }
            if let s = agent.snapshot {
                MetricsView(snapshot: s)
                HistoryChart(agentID: agent.id, lastOk: agent.lastOk)
                UpdatesView(snapshot: s)
                DockerView(docker: s.docker, enabled: agent.docker, pendingAction: $pendingAction)
            }
        }
        .alert("Agent address", isPresented: $editingAddress) {
            TextField("IP or hostname", text: $address)
            Button("Save") { save(address) }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("For example a Tailscale or private network IP. The agent must listen on it.")
        }
    }

    private var menu: some View {
        Menu("Manage") {
            Button(AgentAction.upgrade.title) { pendingAction = .upgrade }
            Button(AgentAction.rotate.title) { pendingAction = .rotate }
            Button("Change address…") { address = agent.agentHost; editingAddress = true }
            Divider()
            Button(AgentAction.uninstall.title) { pendingAction = .uninstall }
            Button("Forget without uninstalling") {
                Task {
                    do { try await API.current().forgetAgent(agent.id); await store.refresh() }
                    catch { actionError = error.localizedDescription }
                }
            }
        }
        .fixedSize()
    }

    private func save(_ host: String) {
        Task {
            do { _ = try await API.current().updateAgent(agent.id, name: nil, agentHost: host); await store.refresh() }
            catch { actionError = error.localizedDescription }
        }
    }
}

struct MetricsView: View {
    var snapshot: Snapshot
    var body: some View {
        let s = snapshot
        GroupBox("Now") {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 20) {
                    stat("CPU", String(format: "%.0f%%", s.cpu), String(format: "5 min %.0f%%", s.cpu5))
                    stat("Load", s.host.load.map { String(format: "%.2f", $0) }.joined(separator: " "), "\(s.host.cores) cores")
                    stat("Network", "↓ \(formatRate(s.network.rxBytesPerSec))", "↑ \(formatRate(s.network.txBytesPerSec))")
                }
                UsageBar(label: "Memory", fraction: s.memory.total > 0 ? 1 - s.memory.available / s.memory.total : 0,
                         detail: "\(formatBytes(s.memory.total - s.memory.available)) of \(formatBytes(s.memory.total))")
                if s.memory.swapTotal > 0 {
                    UsageBar(label: "Swap", fraction: 1 - s.memory.swapFree / s.memory.swapTotal,
                             detail: "\(formatBytes(s.memory.swapTotal - s.memory.swapFree)) of \(formatBytes(s.memory.swapTotal))", warn: 0.5)
                }
                ForEach(s.disks) { d in
                    UsageBar(label: "\(d.mount)\(d.readOnly ? " (read-only)" : "")", fraction: d.total > 0 ? d.used / d.total : 0,
                             detail: "\(formatBytes(d.used)) of \(formatBytes(d.total)), \(d.fs)")
                }
                if s.system.rebootRequired {
                    Label("A reboot is required", systemImage: "arrow.clockwise.circle").foregroundStyle(.orange)
                }
                if !s.system.failedUnits.isEmpty {
                    Label("Failed: \(s.system.failedUnits.joined(separator: ", "))", systemImage: "xmark.octagon").foregroundStyle(.red)
                }
            }
            .padding(4)
        }
    }

    private func stat(_ label: String, _ value: String, _ sub: String) -> some View {
        VStack(alignment: .leading) {
            Text(label).font(.caption).foregroundStyle(.secondary)
            Text(value).font(.title3.monospacedDigit())
            Text(sub).font(.caption).foregroundStyle(.secondary)
        }
    }
}

struct HistoryChart: View {
    var agentID: String
    /// Time of the backend's last successful poll; a new value reloads the chart.
    var lastOk: Double?
    @State private var hours = 24
    @State private var samples: [HistoryResponse.Sample] = []
    @State private var error: String?

    var body: some View {
        GroupBox {
            VStack(alignment: .leading) {
                HStack {
                    Text("History").font(.headline)
                    Spacer()
                    Picker("", selection: $hours) {
                        Text("1h").tag(1)
                        Text("24h").tag(24)
                        Text("7d").tag(168)
                    }
                    .pickerStyle(.segmented)
                    .fixedSize()
                }
                if let error { Text(error).foregroundStyle(.secondary).font(.callout) }
                Chart {
                    ForEach(samples) { p in
                        LineMark(x: .value("Time", Date(timeIntervalSince1970: p.t)), y: .value("%", p.cpu))
                            .foregroundStyle(by: .value("Series", "CPU"))
                        LineMark(x: .value("Time", Date(timeIntervalSince1970: p.t)), y: .value("%", p.memory * 100))
                            .foregroundStyle(by: .value("Series", "Memory"))
                        LineMark(x: .value("Time", Date(timeIntervalSince1970: p.t)), y: .value("%", p.disk * 100))
                            .foregroundStyle(by: .value("Series", "Disk"))
                    }
                }
                .chartYScale(domain: 0...100)
                .chartYAxisLabel("%")
                .frame(height: 180)
            }
            .padding(4)
        }
        .task(id: "\(agentID)-\(hours)-\(lastOk ?? 0)") { await load() }
    }

    private func load() async {
        do {
            samples = try await API.current().history(agent: agentID, hours: hours).samples
            error = samples.isEmpty ? "No history yet." : nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct UpdatesView: View {
    var snapshot: Snapshot
    var body: some View {
        GroupBox("Updates") {
            VStack(alignment: .leading, spacing: 6) {
                if let u = snapshot.updates {
                    if let e = u.error {
                        Text(e).foregroundStyle(.red).font(.callout)
                    } else if u.total == 0 {
                        Label("Up to date", systemImage: "checkmark.seal").foregroundStyle(.green)
                    } else {
                        HStack {
                            Label("\(u.total) updates available", systemImage: "arrow.down.circle")
                            if u.security > 0 {
                                Text("\(u.security) security").bold().foregroundStyle(.red)
                            }
                        }
                        DisclosureGroup("Packages") {
                            Text(u.packages.joined(separator: ", ") + (u.packages.count < u.total ? " …" : ""))
                                .font(.callout.monospaced()).textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    Text("From the \(u.manager) cache\(u.listsTime > 0 ? ", lists refreshed \(relative(u.listsTime))" : ""). The agent reads only, it never installs updates.")
                        .font(.caption).foregroundStyle(.secondary)
                } else {
                    Text("This server's package manager is not supported.").foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(4)
        }
    }
}

struct DockerView: View {
    var docker: Snapshot.Docker?
    var enabled: Bool
    @Binding var pendingAction: AgentAction?

    var body: some View {
        if let d = docker {
            GroupBox("Docker") {
                VStack(alignment: .leading, spacing: 8) {
                    if !d.enabled {
                        HStack(alignment: .top) {
                            Image(systemName: "shippingbox").foregroundStyle(.blue)
                            VStack(alignment: .leading, spacing: 4) {
                                Text("Docker is installed on this server").bold()
                                Text("Monitoring is off. Turning it on gives the agent read access to the Docker socket.")
                                    .foregroundStyle(.secondary).font(.callout)
                            }
                            Spacer()
                            Button("Enable…") { pendingAction = .dockerOn }
                        }
                    } else if let e = d.error {
                        Text(e).foregroundStyle(.red)
                    } else {
                        Table(d.containers) {
                            TableColumn("Name") { c in Text(c.name) }
                            TableColumn("Project") { c in Text(c.project ?? "") }
                            TableColumn("State") { c in
                                Text(c.health.map { "\(c.state), \($0)" } ?? c.state)
                                    .foregroundStyle(c.health == "unhealthy" || (c.state == "exited" && c.exitCode != 0) || c.state == "restarting" ? .red : .primary)
                            }
                            TableColumn("CPU") { c in Text(c.cpu.map { String(format: "%.1f%%", $0) } ?? "") }
                            TableColumn("Memory") { c in Text(c.memory.map(formatBytes) ?? "") }
                            TableColumn("Restarts") { c in Text("\(c.restartCount)") }
                            TableColumn("Image") { c in Text(c.image).foregroundStyle(.secondary) }
                        }
                        .frame(minHeight: CGFloat(min(d.containers.count, 10)) * 24 + 32)
                        Button("Disable Docker monitoring…") { pendingAction = .dockerOff }
                    }
                }
                .padding(4)
            }
        }
    }
}
