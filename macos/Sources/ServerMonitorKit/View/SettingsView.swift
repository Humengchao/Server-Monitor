import SwiftUI

/// The ⌘, settings window.
public struct SettingsView: View {
    @EnvironmentObject private var settings: AppSettings
    @EnvironmentObject private var alerts: AlertService
    @EnvironmentObject private var loc: Localization

    public init() {}

    public var body: some View {
        TabView {
            general.tabItem { Label(loc.t("settings.general"), systemImage: "gearshape") }
            alertSettings.tabItem { Label(loc.t("settings.alerts"), systemImage: "bell") }
            terminalSettings.tabItem { Label(loc.t("settings.terminal"), systemImage: "terminal") }
        }
        .frame(width: 480)
        .task { await alerts.refreshAuthorization() }
    }

    private var general: some View {
        Form {
            Section {
                Picker(loc.t("settings.pollInterval"), selection: $settings.pollInterval) {
                    ForEach(AppSettings.allowedIntervals, id: \.self) { value in
                        Text(verbatim: "\(Int(value)) \(loc.t("settings.seconds"))").tag(value)
                    }
                }
                Text(loc.t("settings.pollIntervalHelp"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                Picker(loc.t("settings.retention"), selection: $settings.retentionDays) {
                    ForEach(AppSettings.allowedRetention, id: \.self) { value in
                        Text(verbatim: "\(value) \(loc.t("settings.days"))").tag(value)
                    }
                }
                Text(loc.t("settings.retentionHelp"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                Picker(loc.t("settings.language"), selection: $loc.language) {
                    Text(loc.t("settings.languageSystem")).tag(AppLanguage.system)
                    Text("中文").tag(AppLanguage.zh)
                    Text("English").tag(AppLanguage.en)
                }
            }

            Section {
                Label(loc.t("settings.credentialsNoteLocal"), systemImage: "lock.shield")
                    .font(.caption)
                if let path = try? Database.defaultURL().deletingLastPathComponent().path {
                    LabeledContent(loc.t("settings.storage")) {
                        Text(path)
                            .font(.caption)
                            .textSelection(.enabled)
                            .lineLimit(2)
                            .truncationMode(.middle)
                    }
                }
            }
        }
        .formStyle(.grouped)
    }

    private var terminalSettings: some View {
        Form {
            Section {
                Picker(loc.t("settings.terminalFont"), selection: $settings.terminalFontName) {
                    ForEach(AppSettings.terminalFonts, id: \.self) { name in
                        Text(name).font(.custom(name, size: 12)).tag(name)
                    }
                }
                Picker(loc.t("settings.terminalFontSize"), selection: $settings.terminalFontSize) {
                    ForEach(AppSettings.terminalFontSizes, id: \.self) { size in
                        Text(verbatim: "\(Int(size)) pt").tag(size)
                    }
                }
            }
            Section {
                // A live sample, so the choice can be judged before opening a
                // session rather than after.
                Text("root@web-1:~$ tail -f /var/log/syslog")
                    .font(.custom(settings.terminalFontName, size: settings.terminalFontSize))
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(.black.opacity(0.85), in: RoundedRectangle(cornerRadius: 6))
                    .foregroundStyle(.green)
            }
        }
        .formStyle(.grouped)
    }

    private var alertSettings: some View {
        Form {
            Section {
                Toggle(loc.t("settings.notificationsEnabled"), isOn: $settings.notificationsEnabled)
                    .onChange(of: settings.notificationsEnabled) { _, enabled in
                        // Ask only when the user opts in, so the system prompt
                        // has a visible cause.
                        if enabled { Task { await alerts.requestAuthorization() } }
                    }

                if settings.notificationsEnabled {
                    if alerts.authorized {
                        Label(loc.t("settings.permissionGranted"), systemImage: "checkmark.circle.fill")
                            .font(.caption)
                            .foregroundStyle(.green)
                    } else {
                        HStack {
                            Label(loc.t("settings.permissionNeeded"), systemImage: "exclamationmark.triangle.fill")
                                .font(.caption)
                                .foregroundStyle(.orange)
                            Spacer()
                            Button(loc.t("settings.grant")) {
                                Task { await alerts.requestAuthorization() }
                            }
                            .controlSize(.small)
                        }
                    }
                }
            }

            Section {
                Toggle(loc.t("settings.notifyOffline"), isOn: $settings.notifyOnOffline)
                thresholdPicker(loc.t("settings.cpuThreshold"), selection: $settings.cpuThreshold)
                thresholdPicker(loc.t("settings.memoryThreshold"), selection: $settings.memoryThreshold)
                thresholdPicker(loc.t("settings.diskThreshold"), selection: $settings.diskThreshold)
                Text(loc.t("settings.thresholdHelp"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .disabled(!settings.notificationsEnabled)

            Section {
                Toggle(loc.t("settings.launchAtLogin"), isOn: $settings.launchAtLogin)
                    .onChange(of: settings.launchAtLogin) { _, enabled in
                        let achieved = LoginItem.setEnabled(enabled)
                        // Reflect what the system actually did: registration
                        // can be refused in System Settings.
                        if achieved != enabled { settings.launchAtLogin = achieved }
                    }
                Text(loc.t("settings.launchAtLoginHelp"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
    }

    private func thresholdPicker(_ title: String, selection: Binding<Int>) -> some View {
        Picker(title, selection: selection) {
            ForEach(AppSettings.thresholdChoices, id: \.self) { value in
                Text(value == 0 ? loc.t("settings.thresholdOff") : "\(value)%").tag(value)
            }
        }
    }
}
