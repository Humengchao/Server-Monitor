import Foundation

/// Installs vnStat on a Linux host over the connection the app already has.
///
/// Two round trips on purpose. The first only asks which package manager the
/// host has and whether we are root; the exact command that would run is then
/// shown to the user, and nothing is installed until they confirm it. Changing
/// software on somebody's server is a thing they should see happen.
public enum VnstatInstaller {

    public enum PackageManager: String, CaseIterable, Sendable {
        case apt = "apt-get", dnf, yum, apk, pacman, zypper

        /// The install line as root. `-y`/`--noconfirm` everywhere: there is
        /// nobody at a terminal to answer a prompt.
        var installLine: String {
            switch self {
            case .apt:
                return "DEBIAN_FRONTEND=noninteractive apt-get update -qq && "
                    + "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq vnstat"
            case .dnf: return "dnf install -y vnstat"
            // vnStat lives in EPEL on RHEL/CentOS; enabling it is harmless where
            // it already is.
            case .yum: return "yum install -y epel-release; yum install -y vnstat"
            case .apk: return "apk add vnstat"
            case .pacman: return "pacman -Sy --noconfirm vnstat"
            case .zypper: return "zypper -n install vnstat"
            }
        }
    }

    /// What the confirmation shows and what then runs.
    public struct Plan: Equatable, Sendable {
        public let manager: PackageManager
        public let asRoot: Bool

        /// Human-readable: the line a person would type.
        public var displayCommand: String {
            asRoot ? manager.installLine : "sudo " + manager.installLine.replacingOccurrences(of: " && ", with: " && sudo ")
        }
    }

    /// Finds the package manager and whether we are root. Read-only.
    public static let probeCommand =
        "for m in apt-get dnf yum apk pacman zypper; do command -v $m >/dev/null 2>&1 && { echo $m; break; }; done; "
        + "echo SM_PROBE_END; id -u"

    /// Nil when no supported package manager answered.
    public static func plan(fromProbe output: String) -> Plan? {
        let lines = output.lines().map { $0.trimmingCharacters(in: .whitespaces) }
        guard let end = lines.firstIndex(of: "SM_PROBE_END") else { return nil }
        let manager = lines[..<end].compactMap { PackageManager(rawValue: $0) }.first
        guard let manager else { return nil }
        let uid = end + 1 < lines.count ? lines[end + 1] : ""
        return Plan(manager: manager, asRoot: uid == "0")
    }

    /// The script that runs after confirmation. Everything is folded into
    /// stdout and the exit code is always 0, so the result is read from the
    /// markers rather than from a thrown error with the output lost.
    public static func remoteScript(for plan: Plan) -> String {
        // `sudo -n`: fail at once rather than wait on a password prompt nothing
        // will answer. `env` because the apt line sets a variable.
        let sudo = plan.asRoot ? "" : "sudo -n env "
        let install = plan.manager.installLine
            .components(separatedBy: " && ")
            .map { sudo + $0 }
            .joined(separator: " && ")
            .replacingOccurrences(of: "; yum", with: "; \(sudo)yum")
        let systemd = "command -v systemctl >/dev/null 2>&1 && \(sudo)systemctl enable --now vnstat >/dev/null 2>&1"
        let openrc = "command -v rc-update >/dev/null 2>&1 && \(sudo)rc-update add vnstatd default >/dev/null 2>&1 && \(sudo)rc-service vnstatd start >/dev/null 2>&1"
        return """
            { \(install); } 2>&1; rc=$?; \
            { \(systemd) || \(openrc) || true; } 2>&1; \
            if command -v vnstat >/dev/null 2>&1; then echo SM_INSTALLED; else echo SM_FAILED rc=$rc; fi
            """
    }

    public enum Outcome: Equatable, Sendable {
        case installed
        /// `sudo -n` refused: the user needs a password we cannot type.
        case needsSudoPassword
        case failed(String)
    }

    public static func outcome(from output: String) -> Outcome {
        if output.contains("SM_INSTALLED") { return .installed }
        if output.contains("sudo: a password is required")
            || output.contains("sudo: a terminal is required")
            || output.contains("sudo: command not found") {
            return .needsSudoPassword
        }
        // The last few lines are where a package manager says what went wrong.
        let tail = output.lines().suffix(4).joined(separator: "\n")
        return .failed(tail.isEmpty ? "no output" : tail)
    }
}
