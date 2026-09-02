import Foundation
import GRDB

/// How a server authenticates.
///
/// No case holds key material: connections go through the system ssh client,
/// so private keys stay in ~/.ssh under OpenSSH's own permissions rather than
/// being copied into this app's storage.
public enum AuthKind: String, Codable, CaseIterable, Sendable {
    /// Use a Host block from ~/.ssh/config verbatim — the whole entry applies,
    /// including ProxyJump and any per-host options.
    case sshConfigAlias
    /// An explicit private key file path.
    case identityFile
    /// Whatever the ssh agent offers.
    case agent
    /// A password, kept in the login keychain.
    case password

    /// Falls back instead of throwing on an unrecognised stored value.
    ///
    /// Row decoding is all-or-nothing: without this, a single row written by an
    /// older build (or a future one) makes the entire fetch fail and the app
    /// shows no servers at all, with nothing to explain why.
    public init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = AuthKind(rawValue: raw) ?? .sshConfigAlias
    }
}

/// Which collection script a host understands.
public enum OSKind: String, Codable, CaseIterable, Sendable {
    /// Try Linux, fall back to Windows, then remember which worked.
    case auto
    case linux
    case windows
}

/// A monitored host.
///
/// A value type, not a live database object: the UI holds snapshots and writes
/// go through `Database`, which keeps every SQLite access on one queue.
public struct Server: Identifiable, Codable, Hashable, Sendable, FetchableRecord, PersistableRecord {
    public static let databaseTableName = "server"

    /// Also the Keychain account name for this server's secret.
    public var id: UUID
    public var name: String
    public var host: String
    public var port: Int
    public var username: String
    public var authKind: AuthKind
    /// ~/.ssh/config Host alias, used when `authKind` is `.sshConfigAlias`.
    public var sshAlias: String
    /// Private key path, used when `authKind` is `.identityFile`.
    public var identityFile: String
    /// Points at a shared `Identity`; when set, its username and auth win.
    public var identityID: UUID?
    /// Optional `MachineGroup` membership.
    public var groupID: UUID?
    public var osKind: OSKind
    /// Per-server alert limits. nil means "use the global setting".
    public var cpuThreshold: Int?
    public var memoryThreshold: Int?
    public var diskThreshold: Int?
    public var notes: String
    /// ISO 3166-1 alpha-2, rendered as a flag on the dashboard card. Empty when
    /// the user has not set one.
    public var countryCode: String
    /// Comma-separated storage for `tags`; use that instead.
    public var tagList: String
    public var createdAt: Date
    /// Ordering in the sidebar.
    public var sortIndex: Int

    // Host facts, refreshed by the collector rather than typed by the user.
    public var cores: Int
    public var memoryTotal: Int64
    public var diskTotal: Int64
    public var dockerVersion: String

    public init(
        id: UUID = UUID(),
        name: String,
        host: String,
        port: Int = 22,
        username: String,
        authKind: AuthKind,
        sshAlias: String = "",
        identityFile: String = "",
        identityID: UUID? = nil,
        groupID: UUID? = nil,
        osKind: OSKind = .auto,
        notes: String = "",
        countryCode: String = "",
        tagList: String = "",
        sortIndex: Int = 0,
        createdAt: Date = Date(),
        cores: Int = 0,
        memoryTotal: Int64 = 0,
        diskTotal: Int64 = 0,
        dockerVersion: String = ""
    ) {
        self.id = id
        self.name = name
        self.host = host
        self.port = port
        self.username = username
        self.authKind = authKind
        self.sshAlias = sshAlias
        self.identityFile = identityFile
        self.identityID = identityID
        self.groupID = groupID
        self.osKind = osKind
        self.cpuThreshold = nil
        self.memoryThreshold = nil
        self.diskThreshold = nil
        self.notes = notes
        self.countryCode = countryCode
        self.tagList = tagList
        self.createdAt = createdAt
        self.sortIndex = sortIndex
        self.cores = cores
        self.memoryTotal = memoryTotal
        self.diskTotal = diskTotal
        self.dockerVersion = dockerVersion
    }

    public var hasDocker: Bool { !dockerVersion.isEmpty }

    /// The credential in the form the ssh layer wants.
    public var credential: SSHCredential {
        switch authKind {
        case .sshConfigAlias: return .sshConfigAlias
        case .identityFile: return .identityFile(path: identityFile)
        case .agent: return .agent
        case .password: return .password
        }
    }

    /// Free-text labels. Order and case are preserved as typed, but duplicates
    /// and blanks are dropped so the chips never repeat.
    public var tags: [String] {
        get { Server.parseTags(tagList) }
        set { tagList = Server.parseTags(newValue.joined(separator: ",")).joined(separator: ",") }
    }

    static func parseTags(_ raw: String) -> [String] {
        var seen = Set<String>()
        return raw
            .split(whereSeparator: { $0 == "," || $0 == "\n" })
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
            .filter { seen.insert($0.lowercased()).inserted }
    }

    /// Flag emoji for `countryCode`, or "" when unset or malformed.
    public var flag: String { Format.flag(countryCode) }

    /// "user@host", with the port appended only when it is not the default.
    public var displayTarget: String {
        if authKind == .sshConfigAlias, !sshAlias.isEmpty {
            return host.isEmpty ? sshAlias : "\(sshAlias) · \(username)@\(host)"
        }
        return port == 22 ? "\(username)@\(host)" : "\(username)@\(host):\(port)"
    }
}
