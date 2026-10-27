import SwiftUI

@main
struct HetznerDashboardApp: App {
    @State private var store = Store()

    init() {
        // Started from Terminal with `swift run` the process is not a regular app.
        NSApplication.shared.setActivationPolicy(.regular)
    }

    var body: some Scene {
        WindowGroup("Hetzner Dashboard") {
            ContentView()
                .environment(store)
                .frame(minWidth: 900, minHeight: 560)
                .onAppear { store.start() }
        }
        Settings {
            SettingsView().environment(store)
        }
        MenuBarExtra {
            MenuSummary().environment(store)
        } label: {
            Image(systemName: menuIcon)
        }
    }

    private var menuIcon: String {
        if store.servers.contains(where: { $0.level == "critical" }) { return "exclamationmark.octagon.fill" }
        if store.servers.contains(where: { $0.level == "warning" }) { return "exclamationmark.triangle.fill" }
        return "server.rack"
    }
}

struct MenuSummary: View {
    @Environment(Store.self) private var store
    var body: some View {
        let bad = store.servers.filter { $0.level != "ok" }
        Text(bad.isEmpty ? "All \(store.servers.count) servers OK" : "\(bad.count) of \(store.servers.count) servers need attention")
        ForEach(bad) { s in
            Text("\(s.level == "critical" ? "🔴" : "🟠") \(s.name): \(s.alerts.map(\.label).joined(separator: ", "))")
        }
        Divider()
        Button("Refresh") { Task { await store.refresh() } }
        Button("Quit") { NSApplication.shared.terminate(nil) }
    }
}

struct ContentView: View {
    @Environment(Store.self) private var store
    @State private var selection: String?
    @State private var showInstall = false
    @State private var showThresholds = false

    private var groups: [(String, [MonitorServer])] {
        let byProject = Dictionary(grouping: store.servers) { $0.project ?? "Other servers" }
        return byProject.keys.sorted { a, b in
            a == "Other servers" ? false : b == "Other servers" ? true : a < b
        }.map { ($0, byProject[$0]!) }
    }

    var body: some View {
        NavigationSplitView {
            List(selection: $selection) {
                if let e = store.error {
                    Label(e, systemImage: "wifi.exclamationmark").foregroundStyle(.red).font(.callout)
                }
                ForEach(groups, id: \.0) { name, servers in
                    Section(name) {
                        ForEach(servers) { s in
                            HStack {
                                LevelDot(level: s.level)
                                VStack(alignment: .leading) {
                                    Text(s.name)
                                    Text(subtitle(s)).font(.caption).foregroundStyle(.secondary)
                                }
                                Spacer()
                                if s.agent != nil {
                                    Image(systemName: s.agent?.status == "online" ? "antenna.radiowaves.left.and.right" : "antenna.radiowaves.left.and.right.slash")
                                        .foregroundStyle(.secondary)
                                        .help("Agent \(s.agent?.status ?? "")")
                                }
                            }
                            .tag(s.key)
                        }
                    }
                }
            }
            .navigationSplitViewColumnWidth(min: 240, ideal: 280)
            .safeAreaInset(edge: .bottom) { footer }
        } detail: {
            if let s = store.server(selection) {
                ServerDetailView(server: s)
            } else {
                ContentUnavailableView("Select a server", systemImage: "server.rack",
                                       description: Text(store.servers.isEmpty ? "Set the dashboard URL and password in Settings (⌘,)." : ""))
            }
        }
        .toolbar {
            ToolbarItemGroup {
                Button { showInstall = true } label: { Label("Add server", systemImage: "plus") }
                    .help("Install the agent on any Linux server")
                Button { showThresholds = true } label: { Label("Alert levels", systemImage: "slider.horizontal.3") }
                    .help("Default warning and critical levels")
                Button { Task { await store.refresh() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                    .disabled(store.loading)
            }
        }
        .sheet(isPresented: $showInstall) { InstallSheet(prefill: nil) }
        .sheet(isPresented: $showThresholds) { ThresholdsView(serverKey: nil, serverName: nil) }
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 2) {
            let total = store.servers.compactMap { $0.hetzner?.cost?.total }.reduce(0, +)
            if total > 0 { Text(String(format: "€%.2f / month (servers)", total)) }
            if let d = store.lastUpdate { Text("Updated \(d.formatted(date: .omitted, time: .standard))") }
            ForEach(store.warnings.prefix(3), id: \.self) { Text($0).foregroundStyle(.orange).lineLimit(2) }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
    }

    private func subtitle(_ s: MonitorServer) -> String {
        if let a = s.alerts.first { return describe(a) }
        if let h = s.hetzner { return [h.type, h.location, h.status].compactMap { $0 }.joined(separator: " · ") }
        return s.agent?.agentHost ?? ""
    }
}

struct SettingsView: View {
    @Environment(Store.self) private var store
    @AppStorage("backendURL") private var url = "http://127.0.0.1:3000"
    @AppStorage("notifications") private var notifications = true
    @State private var password = Keychain.get("dashboardPassword") ?? ""
    @State private var result: String?

    var body: some View {
        Form {
            Section {
                TextField("Dashboard URL", text: $url)
                SecureField("Dashboard password", text: $password)
                if let u = URL(string: url), u.scheme == "http", !["127.0.0.1", "localhost", "::1"].contains(u.host ?? "") {
                    Label("Plain HTTP to another machine sends the password unencrypted. Use https://. Agent installs are blocked over this connection.",
                          systemImage: "lock.open").foregroundStyle(.red).font(.callout)
                }
                HStack {
                    Button("Save and test") {
                        Keychain.set("dashboardPassword", password)
                        Task {
                            await store.refresh()
                            result = store.error ?? "Connected, \(store.servers.count) servers"
                        }
                    }
                    if let result { Text(result).foregroundStyle(.secondary) }
                }
            } footer: {
                Text("The password is stored in the macOS Keychain. Agent management needs DASHBOARD_PASSWORD to be set on the dashboard.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Toggle("Notify when an alert starts, gets worse or clears", isOn: $notifications)
        }
        .formStyle(.grouped)
        .frame(width: 520)
        .padding()
    }
}
