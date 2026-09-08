; Inno Setup script for Server Monitor (D7).
;
; Per-user by default (PrivilegesRequired=lowest): the app writes only to
; %LOCALAPPDATA% and HKCU, so it has no reason to ask for elevation — and
; asking would be one more prompt on top of the SmartScreen warning an
; unsigned build already earns.
;
; Driven by scripts/package.ps1, which passes the paths and versions.

#ifndef MyAppVersion
  #define MyAppVersion "0.1.0"
#endif
#ifndef MyAppFullVersion
  #define MyAppFullVersion MyAppVersion
#endif
#ifndef MySourceDir
  #define MySourceDir "..\dist\win-x64"
#endif
#ifndef MyOutputDir
  #define MyOutputDir "..\dist"
#endif

#define MyAppName "Server Monitor"
#define MyAppExeName "ServerMonitor.exe"
#define MyAppPublisher "Server Monitor"
#define MyAppURL "https://github.com/Humengchao/Server-Monitor"

[Setup]
; A fixed GUID, so an upgrade replaces the previous install rather than
; sitting beside it in Apps & Features.
AppId={{8E6B31A5-6C1F-4E0B-9E3D-2B5A7C4F91D2}
AppName={#MyAppName}
AppVersion={#MyAppFullVersion}
AppVerName={#MyAppName} {#MyAppFullVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}/issues
AppUpdatesURL={#MyAppURL}/releases
VersionInfoVersion={#MyAppVersion}

DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog

; 1809 is the floor (D8): ConPTY and the in-box OpenSSH client both start
; there, and Mica simply degrades to a solid colour on Windows 10.
MinVersion=10.0.17763

OutputDir={#MyOutputDir}
OutputBaseFilename=Server-Monitor-{#MyAppFullVersion}-x64-setup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; The payload is a self-contained single-file exe; x64 only, which an ARM64
; machine runs under emulation.
; x64compatible — which includes ARM64 running x64 under emulation — is Inno
; Setup 6.3 and newer. On 6.2 the same intent is x64, and an ARM64 machine
; there is simply refused rather than emulated.
#if Ver >= EncodeVer(6,3,0,0)
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
#else
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
#endif
UninstallDisplayIcon={app}\{#MyAppExeName}

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"
; Chinese is one of Inno Setup's *unofficial* translations: it is not in the
; Languages folder of a stock install, and naming it unconditionally makes
; ISCC fail with "Could not open file" on any machine that has not downloaded
; it — which is what broke the first CI packaging run. Included when present,
; skipped when not, so the installer builds everywhere and speaks Chinese
; wherever the file has been added.
#if FileExists(CompilerPath + "Languages\ChineseSimplified.isl")
Name: "zh"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"
  #define HaveChinese
#else
  #pragma message "ChineseSimplified.isl not found; the installer wizard will be English only"
#endif

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; Flags: unchecked
Name: "startup"; Description: "{cm:StartAtLogonDesc}"; Flags: unchecked

[CustomMessages]
en.StartAtLogonDesc=Start Server Monitor when I sign in
#ifdef HaveChinese
zh.StartAtLogonDesc=登录时自动启动服务器监控
#endif

[Files]
Source: "{#MySourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Registry]
; The same HKCU Run key the app's own "start with Windows" setting writes, so
; the two agree rather than fighting: ticking the box here and the setting
; later both end up as one value.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; \
    ValueType: string; ValueName: "ServerMonitor"; \
    ValueData: """{app}\{#MyAppExeName}"" --minimised"; \
    Flags: uninsdeletevalue; Tasks: startup

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#MyAppName}}"; \
    Flags: nowait postinstall skipifsilent

[UninstallDelete]
; The uninstaller removes the program. It does NOT remove
; %LOCALAPPDATA%\ServerMonitor: that holds the user's server list and their
; metric history, and an uninstall — which is often a reinstall — must not
; silently delete it. The README says where it is and how to remove it by hand.
Type: dirifempty; Name: "{app}"

[Code]
// Refuse to install over a running copy: the single-file host keeps its exe
// locked, so the copy would fail halfway with a confusing error.
function InitializeSetup(): Boolean;
begin
  Result := True;
  if CheckForMutexes('ServerMonitor.SingleInstance') then
  begin
    MsgBox('Server Monitor is running. Quit it from the notification area (right-click the tray icon) and run this installer again.',
           mbError, MB_OK);
    Result := False;
  end;
end;
