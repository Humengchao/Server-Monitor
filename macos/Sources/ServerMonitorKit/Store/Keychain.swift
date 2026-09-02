import Foundation
import Security

/// Storage for SSH *passwords* only.
///
/// Private keys deliberately never come here — they stay in `~/.ssh` under
/// OpenSSH's own permissions. A password has nowhere else to live, so it goes
/// to the login keychain rather than into the app's database.
public enum Keychain {
    public enum Failure: LocalizedError {
        case unexpectedStatus(OSStatus)

        public var errorDescription: String? {
            switch self {
            case .unexpectedStatus(let status):
                let message = SecCopyErrorMessageString(status, nil) as String? ?? "status \(status)"
                return "Keychain error: \(message)"
            }
        }
    }

    private static let service = "com.hmchxd.ServerMonitor.password"

    public static func savePassword(_ password: String, serverID: UUID) throws {
        let account = serverID.uuidString
        let data = Data(password.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]

        let updateStatus = SecItemUpdate(
            query as CFDictionary,
            [kSecValueData as String: data] as CFDictionary
        )
        if updateStatus == errSecSuccess { return }
        guard updateStatus == errSecItemNotFound else { throw Failure.unexpectedStatus(updateStatus) }

        var insert = query
        insert[kSecValueData as String] = data
        // Polling happens in the background, so the item must be readable
        // whenever the machine is unlocked, without a per-read prompt.
        insert[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlocked
        let addStatus = SecItemAdd(insert as CFDictionary, nil)
        guard addStatus == errSecSuccess else { throw Failure.unexpectedStatus(addStatus) }
    }

    public static func password(serverID: UUID) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: serverID.uuidString,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data
        else { return nil }
        return String(data: data, encoding: .utf8)
    }

    public static func deletePassword(serverID: UUID) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: serverID.uuidString,
        ]
        SecItemDelete(query as CFDictionary)
    }
}
