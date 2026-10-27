import Foundation

struct APIError: LocalizedError {
    var message: String
    var errorDescription: String? { message }
}

/// SSH login details for one operation. Held in memory only, sent to the
/// backend once, and cleared by the view when the sheet closes.
struct SSHCredentials: Encodable {
    struct Auth: Encodable {
        var type: String
        var password: String?
        var privateKey: String?
        var passphrase: String?
    }
    var host: String?
    var port: Int?
    var user: String?
    var hostKeyFingerprint: String?
    var auth: Auth
    var sudoPassword: String?
}

struct InstallOptions: Encodable {
    var name: String
    var agentHost: String?
    var agentPort: Int
    var listen: String
    var allowFrom: [String]
    var docker: Bool
    var hetznerId: Int?
}

struct API {
    var baseURL: URL
    var password: String

    static func current() throws -> API {
        let raw = UserDefaults.standard.string(forKey: "backendURL") ?? "http://127.0.0.1:3000"
        guard let url = URL(string: raw), url.scheme == "http" || url.scheme == "https", url.host != nil else {
            throw APIError(message: "Set a valid dashboard URL in Settings.")
        }
        return API(baseURL: url, password: Keychain.get("dashboardPassword") ?? "")
    }

    /// True when the connection is encrypted or never leaves this Mac.
    var isSafeTransport: Bool {
        if baseURL.scheme == "https" { return true }
        return ["127.0.0.1", "localhost", "::1"].contains(baseURL.host ?? "")
    }

    private func request(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil, timeout: TimeInterval = 30) async throws -> Data {
        var comps = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty { comps.queryItems = query }
        var req = URLRequest(url: comps.url!, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: timeout)
        req.httpMethod = method
        req.setValue("1", forHTTPHeaderField: "X-Hdash-Client")
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if !password.isEmpty {
            req.setValue("Basic " + Data("app:\(password)".utf8).base64EncodedString(), forHTTPHeaderField: "Authorization")
        }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = body
        }
        let (data, res) = try await URLSession.shared.data(for: req)
        let status = (res as? HTTPURLResponse)?.statusCode ?? 0
        if status == 401 { throw APIError(message: "The dashboard rejected the password. Check Settings.") }
        if !(200..<300).contains(status) {
            let msg = (try? JSONDecoder().decode([String: String].self, from: data))?["error"]
            throw APIError(message: msg ?? "HTTP \(status)")
        }
        return data
    }

    private func get<T: Decodable>(_ path: String, query: [URLQueryItem] = []) async throws -> T {
        try JSONDecoder().decode(T.self, from: try await request("GET", path, query: query))
    }

    private func send<T: Decodable>(_ method: String, _ path: String, _ body: some Encodable, query: [URLQueryItem] = [], timeout: TimeInterval = 30) async throws -> T {
        let data = try await request(method, path, query: query, body: try JSONEncoder().encode(body), timeout: timeout)
        return try JSONDecoder().decode(T.self, from: data)
    }

    /// Refuses to send SSH credentials over plain HTTP to another machine.
    private func requireSafeTransport() throws {
        guard isSafeTransport else {
            throw APIError(message: "SSH credentials are only sent over HTTPS or to a dashboard on this Mac. Change the dashboard URL to https://.")
        }
    }

    func monitor() async throws -> MonitorResponse { try await get("/api/monitor") }

    func history(agent: String, hours: Int) async throws -> HistoryResponse {
        try await get("/api/agents/history", query: [.init(name: "id", value: agent), .init(name: "hours", value: String(hours))])
    }

    func thresholds() async throws -> Thresholds { try await get("/api/thresholds") }

    func saveThresholds(defaults: [String: Levels], servers: [String: [String: Levels]]) async throws -> Thresholds {
        struct Body: Encodable { var defaults: [String: Levels]; var servers: [String: [String: Levels]] }
        return try await send("PUT", "/api/thresholds", Body(defaults: defaults, servers: servers))
    }

    func hostKeys(host: String, port: Int) async throws -> [HostKey] {
        struct Body: Encodable { var host: String; var port: Int }
        struct Res: Decodable { var keys: [HostKey] }
        let r: Res = try await send("POST", "/api/agents/hostkeys", Body(host: host, port: port))
        return r.keys
    }

    func probe(_ ssh: SSHCredentials) async throws -> ProbeResult {
        try requireSafeTransport()
        struct Body: Encodable { var ssh: SSHCredentials }
        return try await send("POST", "/api/agents/probe", Body(ssh: ssh), timeout: 90)
    }

    func install(_ ssh: SSHCredentials, options: InstallOptions) async throws -> InstallResult {
        try requireSafeTransport()
        struct Body: Encodable { var ssh: SSHCredentials; var options: InstallOptions }
        return try await send("POST", "/api/agents", Body(ssh: ssh, options: options), timeout: 300)
    }

    func action(_ action: String, agent: String, ssh: SSHCredentials) async throws -> AgentResult {
        try requireSafeTransport()
        struct Body: Encodable { var id: String; var action: String; var ssh: SSHCredentials }
        return try await send("POST", "/api/agents/action", Body(id: agent, action: action, ssh: ssh), timeout: 300)
    }

    func updateAgent(_ id: String, name: String?, agentHost: String?) async throws -> AgentResult {
        struct Body: Encodable { var id: String; var name: String?; var agentHost: String? }
        return try await send("POST", "/api/agents/update", Body(id: id, name: name, agentHost: agentHost))
    }

    func forgetAgent(_ id: String) async throws {
        _ = try await request("DELETE", "/api/agents", query: [.init(name: "id", value: id)], body: Data("{}".utf8))
    }
}
