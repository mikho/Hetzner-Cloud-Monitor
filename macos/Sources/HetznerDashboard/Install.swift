import AppKit
import SwiftUI

/// Login details typed into a sheet. Cleared when the sheet goes away.
struct SSHAuthState {
    var useKey = false
    var password = ""
    var keyText = ""
    var keyName = ""
    var passphrase = ""
    var sudoPassword = ""

    func credentials(host: String? = nil, port: Int? = nil, user: String? = nil, fingerprint: String? = nil) -> SSHCredentials {
        SSHCredentials(
            host: host, port: port, user: user, hostKeyFingerprint: fingerprint,
            auth: useKey
                ? .init(type: "key", privateKey: keyText, passphrase: passphrase.isEmpty ? nil : passphrase)
                : .init(type: "password", password: password),
            sudoPassword: sudoPassword.isEmpty ? nil : sudoPassword
        )
    }

    var isComplete: Bool { useKey ? !keyText.isEmpty : !password.isEmpty }

    mutating func clear() { self = SSHAuthState() }
}

struct SSHAuthForm: View {
    @Binding var state: SSHAuthState
    var user: String

    var body: some View {
        Picker("Sign in with", selection: $state.useKey) {
            Text("Password").tag(false)
            Text("Private key").tag(true)
        }
        .pickerStyle(.segmented)
        if state.useKey {
            HStack {
                Text(state.keyName.isEmpty ? "No key chosen" : state.keyName).foregroundStyle(.secondary)
                Spacer()
                Button("Choose key…", action: chooseKey)
            }
            SecureField("Key passphrase (if any)", text: $state.passphrase)
        } else {
            SecureField("Password", text: $state.password)
        }
        if user != "root" {
            SecureField(state.useKey ? "sudo password (empty for passwordless sudo)" : "sudo password (empty to use the login password)",
                        text: $state.sudoPassword)
        }
    }

    private func chooseKey() {
        let panel = NSOpenPanel()
        panel.showsHiddenFiles = true
        panel.directoryURL = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".ssh")
        panel.message = "Choose the private key (not the .pub file)"
        guard panel.runModal() == .OK, let url = panel.url,
              let text = try? String(contentsOf: url, encoding: .utf8) else { return }
        guard text.contains("PRIVATE KEY-----") else {
            state.keyName = "That file is not a private key"
            state.keyText = ""
            return
        }
        state.keyText = text
        state.keyName = url.lastPathComponent
    }
}

struct InstallPrefill {
    var name: String
    var host: String
    var hetznerId: Int?
}

/// Install in three steps: confirm the host key, sign in and check the
/// server, choose options and install.
struct InstallSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(Store.self) private var store
    var prefill: InstallPrefill?

    @State private var name = ""
    @State private var host = ""
    @State private var sshPort = "22"
    @State private var user = "root"
    @State private var keys: [HostKey] = []
    @State private var chosenKey: String?
    @State private var verified = false
    @State private var auth = SSHAuthState()
    @State private var probe: ProbeResult?
    @State private var docker = false
    @State private var agentPort = "7411"
    @State private var listen = ""
    @State private var agentHost = ""
    @State private var allowFrom = ""
    @State private var busy: String?
    @State private var error: String?
    @State private var result: InstallResult?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(prefill == nil ? "Add a server" : "Install agent on \(prefill!.name)")
                .font(.title2.bold()).padding([.horizontal, .top], 20)
            Form {
                if let result { done(result) } else {
                    connection
                    if chosenKey != nil && verified { signIn }
                    if let probe { options(probe) }
                }
                if let error {
                    Text(error).foregroundStyle(.red).textSelection(.enabled)
                }
            }
            .formStyle(.grouped)
            HStack {
                if let busy { ProgressView().controlSize(.small); Text(busy).foregroundStyle(.secondary) }
                Spacer()
                Button(result == nil ? "Cancel" : "Close") { close() }.keyboardShortcut(.cancelAction)
            }
            .padding(16)
        }
        .frame(width: 600, height: 680)
        .onAppear {
            if let p = prefill { name = p.name; host = p.host }
        }
        .onDisappear { auth.clear() }
    }

    private var connection: some View {
        Section("1. Server") {
            TextField("Name", text: $name)
            TextField("Host or IP", text: $host)
            TextField("SSH port", text: $sshPort)
            TextField("SSH user", text: $user)
            Button("Fetch host key") { fetchKeys() }.disabled(host.isEmpty || busy != nil)
            if !keys.isEmpty {
                Picker("Host key", selection: $chosenKey) {
                    ForEach(keys, id: \.fingerprint) { k in
                        Text("\(k.type)  \(k.fingerprint)").font(.callout.monospaced()).tag(Optional(k.fingerprint))
                    }
                }
                .pickerStyle(.radioGroup)
                Toggle("I have checked that this fingerprint is the server's", isOn: $verified)
                Text("Check it on the server, for example in the Hetzner Console: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub. Credentials are only sent to a server with this key.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .disabled(probe != nil)
    }

    private var signIn: some View {
        Section("2. Sign in") {
            SSHAuthForm(state: $auth, user: user)
            Button("Check server") { runProbe() }.disabled(!auth.isComplete || busy != nil)
        }
        .disabled(probe != nil && busy != nil)
    }

    @ViewBuilder
    private func options(_ p: ProbeResult) -> some View {
        Section("3. Install") {
            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 4) {
                row("System", "\(p.os) (\(p.machine))")
                row("systemd", p.systemd ? "yes" : "no, the agent needs systemd")
                row("Root access", p.rootOk ? "yes" : (p.rootError ?? "no"))
                if let v = p.installedVersion { row("Installed", "v\(v), will be replaced and re-paired") }
            }
            if p.docker {
                Toggle("Monitor Docker", isOn: $docker)
                Text("Docker was found. Monitoring adds the agent to the docker group so it can read the Docker socket. The agent only reads, but docker group membership is close to root. You can turn this on later too.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            TextField("Agent port", text: $agentPort)
            TextField("Listen only on IP (optional, e.g. Tailscale IP)", text: $listen)
            TextField("Dashboard connects to (default: listen IP or host)", text: $agentHost)
            TextField("Allow only from IPs/CIDRs (optional, comma separated)", text: $allowFrom)
            Button("Install agent") { install() }
                .buttonStyle(.borderedProminent)
                .disabled(!p.systemd || !p.rootOk || p.arch == nil || busy != nil)
        }
    }

    private func done(_ r: InstallResult) -> some View {
        Section("Installed") {
            Label("Agent \(r.agent.status) on \(r.agent.agentHost):\(r.agent.agentPort)",
                  systemImage: r.agent.status == "online" ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                .foregroundStyle(r.agent.status == "online" ? .green : .orange)
            if r.agent.status != "online" {
                Text(r.agent.error ?? "").foregroundStyle(.secondary)
            }
            Text(r.firewall).font(.callout).textSelection(.enabled)
            Text("TLS pin: \(r.agent.fingerprint)").font(.caption.monospaced()).textSelection(.enabled)
        }
    }

    private func run(_ label: String, _ work: @escaping () async throws -> Void) {
        busy = label
        error = nil
        Task {
            do { try await work() } catch { self.error = error.localizedDescription }
            busy = nil
        }
    }

    private func fetchKeys() {
        keys = []
        chosenKey = nil
        verified = false
        run("Fetching host key…") {
            keys = try await API.current().hostKeys(host: host.trimmingCharacters(in: .whitespaces), port: Int(sshPort) ?? 22)
            chosenKey = keys.first { $0.type == "ssh-ed25519" }?.fingerprint ?? keys.first?.fingerprint
        }
    }

    private var creds: SSHCredentials {
        auth.credentials(host: host.trimmingCharacters(in: .whitespaces), port: Int(sshPort) ?? 22,
                         user: user.trimmingCharacters(in: .whitespaces), fingerprint: chosenKey)
    }

    private func runProbe() {
        run("Signing in…") {
            let p = try await API.current().probe(creds)
            docker = false
            probe = p
        }
    }

    private func install() {
        let opts = InstallOptions(
            name: name.isEmpty ? host : name,
            agentHost: agentHost.isEmpty ? nil : agentHost,
            agentPort: Int(agentPort) ?? 7411,
            listen: listen.trimmingCharacters(in: .whitespaces),
            allowFrom: allowFrom.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty },
            docker: docker,
            hetznerId: prefill?.hetznerId
        )
        run("Installing…") {
            result = try await API.current().install(creds, options: opts)
            auth.clear()
            await store.refresh()
        }
    }

    private func close() {
        auth.clear()
        dismiss()
    }
}

/// Asks for SSH credentials for one change on an installed agent.
struct AgentActionSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(Store.self) private var store
    var agent: AgentInfo
    var action: AgentAction
    @State private var auth = SSHAuthState()
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(action.title).font(.title2.bold()).padding([.horizontal, .top], 20)
            Form {
                Section {
                    Text(action.explanation)
                    Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 4) {
                        row("SSH", "\(agent.sshUser)@\(agent.sshHost):\(agent.sshPort)")
                        if let fp = agent.hostKeyFingerprint { row("Host key", fp) }
                    }
                    .font(.callout)
                }
                Section("Sign in") { SSHAuthForm(state: $auth, user: agent.sshUser) }
                if let error { Text(error).foregroundStyle(.red).textSelection(.enabled) }
            }
            .formStyle(.grouped)
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("Cancel") { auth.clear(); dismiss() }.keyboardShortcut(.cancelAction)
                Button(action.title) { perform() }
                    .buttonStyle(.borderedProminent)
                    .tint(action == .uninstall ? .red : .accentColor)
                    .disabled(!auth.isComplete || busy)
            }
            .padding(16)
        }
        .frame(width: 520, height: 460)
        .onDisappear { auth.clear() }
    }

    private func perform() {
        busy = true
        error = nil
        Task {
            do {
                _ = try await API.current().action(action.rawValue, agent: agent.id, ssh: auth.credentials())
                auth.clear()
                await store.refresh()
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}
