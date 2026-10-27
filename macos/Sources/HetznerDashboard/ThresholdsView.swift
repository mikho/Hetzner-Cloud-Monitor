import SwiftUI

/// Edits warning and critical levels: the defaults when serverKey is nil,
/// otherwise the overrides for one server.
struct ThresholdsView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(Store.self) private var store
    var serverKey: String?
    var serverName: String?

    @State private var doc: Thresholds?
    @State private var edits: [String: Levels] = [:]
    @State private var overridden: Set<String> = []
    @State private var error: String?
    @State private var saving = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 4) {
                Text(serverName.map { "Alert levels for \($0)" } ?? "Default alert levels").font(.title2.bold())
                Text(serverKey == nil
                     ? "Used by every server that doesn't override a metric. An alert fires when a value reaches the level."
                     : "Turn on Override to set this server's own levels. Other metrics use the defaults.")
                    .foregroundStyle(.secondary)
            }
            .padding([.horizontal, .top], 20)
            if let doc {
                Form {
                    ForEach(metricOrder.filter { doc.metrics[$0] != nil }, id: \.self) { key in
                        metricRow(key, doc.metrics[key]!, defaults: doc.defaults[key]!)
                    }
                }
                .formStyle(.grouped)
            } else {
                Spacer()
                ProgressView().frame(maxWidth: .infinity)
                Spacer()
            }
            HStack {
                if let error { Text(error).foregroundStyle(.red) }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") { save() }.buttonStyle(.borderedProminent).disabled(doc == nil || saving)
            }
            .padding(16)
        }
        .frame(width: 640, height: 640)
        .task { await load() }
    }

    @ViewBuilder
    private func metricRow(_ key: String, _ m: MetricDef, defaults: Levels) -> some View {
        let isOverride = serverKey != nil
        let active = !isOverride || overridden.contains(key)
        let value = edits[key] ?? defaults
        HStack(alignment: .center, spacing: 12) {
            if isOverride {
                Toggle("", isOn: Binding(
                    get: { overridden.contains(key) },
                    set: { on in
                        if on { overridden.insert(key); edits[key] = edits[key] ?? defaults } else { overridden.remove(key) }
                    }
                ))
                .labelsHidden()
                .help("Override")
            }
            Toggle(m.label, isOn: bind(key, defaults, \.enabled)).frame(width: 260, alignment: .leading)
            Spacer()
            if m.unit == "bool" {
                Picker("", selection: Binding(
                    get: { value.crit != nil ? 2 : value.warn != nil ? 1 : 0 },
                    set: { n in
                        var v = edits[key] ?? defaults
                        v.warn = n == 1 ? 1 : nil
                        v.crit = n == 2 ? 1 : nil
                        edits[key] = v
                    }
                )) {
                    Text("Warning").tag(1)
                    Text("Critical").tag(2)
                }
                .labelsHidden()
                .frame(width: 180)
            } else {
                levelField("Warn ≥", key, defaults, \.warn, unit: m.unit)
                levelField("Crit ≥", key, defaults, \.crit, unit: m.unit)
            }
        }
        .disabled(!active)
        .opacity(active ? 1 : 0.5)
    }

    private func bind(_ key: String, _ defaults: Levels, _ path: WritableKeyPath<Levels, Bool>) -> Binding<Bool> {
        Binding(
            get: { (edits[key] ?? defaults)[keyPath: path] },
            set: { var v = edits[key] ?? defaults; v[keyPath: path] = $0; edits[key] = v }
        )
    }

    private func levelField(_ label: String, _ key: String, _ defaults: Levels, _ path: WritableKeyPath<Levels, Double?>, unit: String) -> some View {
        let text = Binding<String>(
            get: { (edits[key] ?? defaults)[keyPath: path].map { $0 == $0.rounded() ? String(Int($0)) : String($0) } ?? "" },
            set: { s in
                var v = edits[key] ?? defaults
                v[keyPath: path] = Double(s.replacingOccurrences(of: ",", with: "."))
                edits[key] = v
            }
        )
        return HStack(spacing: 4) {
            Text(label).foregroundStyle(.secondary)
            TextField("off", text: text).frame(width: 56).multilineTextAlignment(.trailing)
            Text(unit == "count" ? "" : unit).foregroundStyle(.secondary).frame(width: 28, alignment: .leading)
        }
    }

    private func load() async {
        do {
            let d = try await API.current().thresholds()
            doc = d
            if let key = serverKey {
                let own = d.servers[key] ?? [:]
                overridden = Set(own.keys)
                edits = [:]
                for (k, v) in own { edits[k] = Levels(enabled: v.enabled, warn: v.warn ?? d.defaults[k]?.warn, crit: v.crit ?? d.defaults[k]?.crit) }
            } else {
                edits = d.defaults
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func save() {
        guard let doc else { return }
        saving = true
        error = nil
        var defaults = doc.defaults
        var servers = doc.servers
        if let key = serverKey {
            servers[key] = edits.filter { overridden.contains($0.key) }
        } else {
            defaults = edits
        }
        Task {
            do {
                _ = try await API.current().saveThresholds(defaults: defaults, servers: servers)
                await store.refresh()
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            saving = false
        }
    }
}
