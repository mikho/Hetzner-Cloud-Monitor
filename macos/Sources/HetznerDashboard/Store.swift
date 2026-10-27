import Foundation
import Observation
import UserNotifications

/// Polls the backend and sends a notification when an alert appears or gets
/// worse. Alert levels are evaluated by the backend, so every client agrees.
@MainActor
@Observable
final class Store {
    var servers: [MonitorServer] = []
    var warnings: [String] = []
    var error: String?
    var loading = false
    var lastUpdate: Date?

    private var known: [String: String] = [:]
    private var labels: [String: String] = [:]
    private var firstLoad = true
    private var timer: Timer?

    var notificationsEnabled: Bool {
        UserDefaults.standard.object(forKey: "notifications") as? Bool ?? true
    }

    /// Notifications need a real app bundle; `swift run` has none.
    private var canNotify: Bool { Bundle.main.bundleIdentifier != nil && Bundle.main.bundlePath.hasSuffix(".app") }

    func start() {
        if canNotify {
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
        }
        Task { await refresh() }
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in
            Task { await self?.refresh() }
        }
    }

    func refresh() async {
        loading = true
        defer { loading = false }
        do {
            let r = try await API.current().monitor()
            servers = r.servers.sorted { rank($0) != rank($1) ? rank($0) > rank($1) : $0.name < $1.name }
            warnings = r.warnings
            error = nil
            lastUpdate = Date()
            notifyChanges()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func server(_ key: String?) -> MonitorServer? {
        servers.first { $0.key == key }
    }

    private func rank(_ s: MonitorServer) -> Int {
        s.level == "critical" ? 2 : s.level == "warning" ? 1 : 0
    }

    private func notifyChanges() {
        var now: [String: String] = [:]
        for s in servers {
            for a in s.alerts {
                let id = "\(s.key)|\(a.metric)"
                now[id] = a.level
                labels[id] = "\(s.name): \(a.label)"
                let before = known[id]
                let worse = before == nil || (before == "warning" && a.level == "critical")
                if worse && !firstLoad && notificationsEnabled {
                    post(id: id, title: "\(s.name): \(a.label)", body: describe(a))
                }
            }
        }
        for (id, _) in known where now[id] == nil && !firstLoad && notificationsEnabled {
            post(id: id, title: labels[id] ?? "Alert", body: "Back to normal")
        }
        known = now
        firstLoad = false
    }

    private func post(id: String, title: String, body: String) {
        guard canNotify else { return }
        let c = UNMutableNotificationContent()
        c.title = title
        c.body = body
        c.sound = .default
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: id + "|\(Date().timeIntervalSince1970)", content: c, trigger: nil))
    }
}

func describe(_ a: Alert) -> String {
    let level = a.level == "critical" ? "Critical" : "Warning"
    var text = "\(level): \(formatValue(a.value, unit: a.unit))"
    if let t = a.threshold, a.unit != "bool" { text += " (limit \(formatValue(t, unit: a.unit)))" }
    if let d = a.detail, !d.isEmpty { text += ", \(d)" }
    return text
}
