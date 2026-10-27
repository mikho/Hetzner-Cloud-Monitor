import Foundation

// Shapes of the backend's JSON. Everything optional that the backend may
// leave out, so a newer backend doesn't break an older app.

struct MonitorResponse: Decodable {
    var servers: [MonitorServer]
    var warnings: [String]
}

struct MonitorServer: Decodable, Identifiable {
    var key: String
    var name: String
    var project: String?
    var hetzner: HetznerServer?
    var agent: AgentInfo?
    var level: String
    var alerts: [Alert]
    var id: String { key }
}

struct Alert: Decodable, Hashable {
    var metric: String
    var label: String
    var unit: String
    var level: String
    var value: Double
    var threshold: Double?
    var detail: String?
}

struct HetznerServer: Decodable {
    struct Cost: Decodable { var total: Double }
    struct Traffic: Decodable {
        var included: Double?
        var out: Double?
    }
    var id: Int
    var name: String
    var status: String
    var type: String?
    var cores: Int?
    var memory: Double?
    var disk: Double?
    var location: String?
    var ipv4: String?
    var ipv6: String?
    var image: String?
    var backups: Bool?
    var cost: Cost?
    var traffic: Traffic?
}

struct AgentInfo: Decodable {
    var id: String
    var name: String
    var sshHost: String
    var sshPort: Int
    var sshUser: String
    var hostKeyFingerprint: String?
    var agentHost: String
    var agentPort: Int
    var listen: String?
    var allowFrom: [String]?
    var docker: Bool
    var fingerprint: String
    var version: String?
    var arch: String?
    var os: String?
    var hetznerId: Int?
    var createdAt: Double
    var status: String
    var error: String?
    var lastOk: Double?
    var snapshot: Snapshot?
}

struct Snapshot: Decodable {
    struct Host: Decodable {
        var hostname: String
        var os: String
        var kernel: String
        var arch: String
        var serverID: Int?
        var bootTime: Double
        var cores: Int
        var load: [Double]
    }
    struct Memory: Decodable {
        var total: Double
        var available: Double
        var swapTotal: Double
        var swapFree: Double
    }
    struct Disk: Decodable, Identifiable {
        var mount: String
        var device: String
        var fs: String
        var total: Double
        var used: Double
        var available: Double
        var inodesTotal: Double
        var inodesUsed: Double
        var readOnly: Bool
        var id: String { device }
    }
    struct Network: Decodable {
        var rxBytesPerSec: Double
        var txBytesPerSec: Double
    }
    struct System: Decodable {
        var rebootRequired: Bool
        var failedUnits: [String]
    }
    struct Updates: Decodable {
        var manager: String
        var total: Int
        var security: Int
        var packages: [String]
        var listsTime: Double
        var checkedAt: Double
        var error: String?
    }
    struct Docker: Decodable {
        var installed: Bool
        var enabled: Bool
        var error: String?
        var containers: [Container]
    }
    struct Container: Decodable, Identifiable {
        struct Port: Decodable, Hashable {
            var `private`: Int
            var `public`: Int?
            var type: String
        }
        var id: String
        var name: String
        var image: String
        var project: String?
        var state: String
        var status: String
        var health: String?
        var exitCode: Int
        var restartCount: Int
        var cpu: Double?
        var memory: Double?
        var memoryLimit: Double?
        var ports: [Port]
    }
    var version: String
    var time: Double
    var host: Host
    var cpu: Double
    var cpu5: Double
    var memory: Memory
    var disks: [Disk]
    var network: Network
    var system: System
    var updates: Updates?
    var docker: Docker?
}

struct HistoryResponse: Decodable {
    struct Sample: Decodable, Identifiable {
        var t: Double
        var cpu: Double
        var memory: Double
        var swap: Double
        var disk: Double
        var load: Double
        var id: Double { t }
    }
    var step: Int
    var samples: [Sample]
}

// MARK: Thresholds

struct MetricDef: Decodable {
    var label: String
    var unit: String
    var warn: Double?
    var crit: Double?
    var max: Double
}

struct Levels: Codable, Equatable {
    var enabled: Bool
    var warn: Double?
    var crit: Double?

    init(enabled: Bool, warn: Double?, crit: Double?) {
        self.enabled = enabled
        self.warn = warn
        self.crit = crit
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
        warn = try c.decodeIfPresent(Double.self, forKey: .warn)
        crit = try c.decodeIfPresent(Double.self, forKey: .crit)
    }

    // Explicit nulls: "no level" must reach the backend as null, not be left out.
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(enabled, forKey: .enabled)
        try c.encode(warn, forKey: .warn)
        try c.encode(crit, forKey: .crit)
    }

    enum CodingKeys: String, CodingKey { case enabled, warn, crit }
}

struct Thresholds: Decodable {
    var metrics: [String: MetricDef]
    var defaults: [String: Levels]
    var servers: [String: [String: Levels]]
}

/// The order metrics are shown in.
let metricOrder = [
    "serverStopped", "agentOffline", "cpu", "load", "memory", "swap", "disk", "inodes",
    "failedUnits", "rebootRequired", "securityUpdates", "updates", "containersDown", "traffic",
]

// MARK: Install

struct HostKey: Decodable, Hashable {
    var type: String
    var fingerprint: String
}

struct ProbeResult: Decodable {
    var arch: String?
    var machine: String
    var os: String
    var systemd: Bool
    var docker: Bool
    var installedVersion: String?
    var rootOk: Bool
    var rootError: String?
}

struct InstallResult: Decodable {
    var agent: AgentInfo
    var firewall: String
}

struct AgentResult: Decodable {
    var agent: AgentInfo?
    var removed: Bool?
}
