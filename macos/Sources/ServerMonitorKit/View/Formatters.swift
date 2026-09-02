import Foundation

/// Display helpers shared by the tiles, charts and tables.
public enum Format {
    /// Binary byte sizes: 1 KB = 1024 B, matching what the shell tools report.
    public static func bytes(_ value: Int64) -> String {
        bytes(Double(value))
    }

    public static func bytes(_ value: Double) -> String {
        guard value.isFinite, value > 0 else { return "0 B" }
        let units = ["B", "KB", "MB", "GB", "TB", "PB"]
        let index = min(Int(log(value) / log(1024)), units.count - 1)
        let amount = value / pow(1024, Double(index))
        return String(format: index == 0 ? "%.0f %@" : "%.1f %@", amount, units[index])
    }

    public static func rate(_ bytesPerSecond: Double) -> String {
        "\(bytes(bytesPerSecond))/s"
    }

    /// Rate split into number and unit so a card can typeset them at different
    /// weights, e.g. ("5.66", "K/s").
    public static func rateParts(_ bytesPerSecond: Double) -> (amount: String, unit: String) {
        guard bytesPerSecond.isFinite, bytesPerSecond > 0 else { return ("0", "B/s") }
        let units = ["B", "K", "M", "G", "T"]
        let index = min(Int(log(bytesPerSecond) / log(1024)), units.count - 1)
        let amount = bytesPerSecond / pow(1024, Double(index))
        // Three significant figures keeps the column width stable.
        let text = amount >= 100 ? String(format: "%.0f", amount)
                 : amount >= 10 ? String(format: "%.1f", amount)
                 : String(format: "%.2f", amount)
        return (text, "\(units[index])/s")
    }

    /// Whole days, as the cards show uptime ("126 Days").
    public static func uptimeDays(_ seconds: Int64, chinese: Bool) -> String {
        let days = max(0, seconds / 86_400)
        return chinese ? "\(days) 天" : "\(days) Days"
    }

    /// Regional-indicator flag for an ISO 3166-1 alpha-2 code, or "" if unset.
    public static func flag(_ countryCode: String) -> String {
        let code = countryCode.trimmingCharacters(in: .whitespaces).uppercased()
        guard code.count == 2, code.allSatisfy({ $0.isLetter }) else { return "" }
        return String(code.unicodeScalars.compactMap {
            UnicodeScalar(127_397 + $0.value).map(Character.init)
        })
    }

    public static func percent(_ value: Double) -> String {
        String(format: "%.1f%%", max(0, min(value, 100)))
    }

    public static func load(_ value: Double) -> String {
        String(format: "%.2f", value)
    }

    public static func latency(_ milliseconds: Double) -> String {
        milliseconds <= 0 ? "—" : String(format: "%.0f ms", milliseconds)
    }

    /// Compact uptime, e.g. "12d 4h" or "3h 20m".
    public static func uptime(_ seconds: Int64, chinese: Bool) -> String {
        guard seconds > 0 else { return "—" }
        let days = seconds / 86_400
        let hours = (seconds % 86_400) / 3600
        let minutes = (seconds % 3600) / 60
        if days > 0 {
            return chinese ? "\(days) 天 \(hours) 小时" : "\(days)d \(hours)h"
        }
        if hours > 0 {
            return chinese ? "\(hours) 小时 \(minutes) 分" : "\(hours)h \(minutes)m"
        }
        return chinese ? "\(minutes) 分钟" : "\(minutes)m"
    }

    /// "3.2 GB / 16.0 GB"
    public static func usage(used: Int64, total: Int64) -> String {
        total > 0 ? "\(bytes(used)) / \(bytes(total))" : bytes(used)
    }
}
