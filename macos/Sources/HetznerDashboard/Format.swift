import SwiftUI

func formatBytes(_ v: Double) -> String {
    ByteCountFormatter.string(fromByteCount: Int64(v), countStyle: .binary)
}

func formatRate(_ bytesPerSec: Double) -> String {
    formatBytes(bytesPerSec) + "/s"
}

func formatValue(_ v: Double, unit: String) -> String {
    switch unit {
    case "%": return String(format: "%.0f%%", v)
    case "min": return "\(Int(v)) min"
    case "x": return String(format: "%.2f", v)
    case "bool": return v >= 1 ? "yes" : "no"
    default: return v == v.rounded() ? "\(Int(v))" : String(format: "%.1f", v)
    }
}

func relative(_ unixSeconds: Double) -> String {
    RelativeDateTimeFormatter().localizedString(for: Date(timeIntervalSince1970: unixSeconds), relativeTo: Date())
}

func levelColor(_ level: String) -> Color {
    switch level {
    case "critical": return .red
    case "warning": return .orange
    default: return .green
    }
}

struct LevelDot: View {
    var level: String
    var body: some View {
        Circle().fill(levelColor(level)).frame(width: 9, height: 9)
    }
}

/// A labelled bar for a used fraction.
struct UsageBar: View {
    var label: String
    var fraction: Double
    var detail: String
    var warn: Double = 0.8

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text(label)
                Spacer()
                Text(detail).foregroundStyle(.secondary).monospacedDigit()
            }
            .font(.callout)
            ProgressView(value: min(max(fraction, 0), 1))
                .tint(fraction >= 0.9 ? .red : fraction >= warn ? .orange : .accentColor)
        }
    }
}
