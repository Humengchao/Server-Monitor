import Foundation
import ServiceManagement

/// Registers the app as a login item.
///
/// `SMAppService` needs a real bundle, so this reports failure rather than
/// pretending when run from a bare binary.
public enum LoginItem {
    public static var isEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    /// Returns the state actually achieved, which may differ from what was
    /// asked for if the user has denied the app in System Settings.
    @discardableResult
    public static func setEnabled(_ enabled: Bool) -> Bool {
        do {
            if enabled {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
        } catch {
            return isEnabled
        }
        return isEnabled
    }
}
