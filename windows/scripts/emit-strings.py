"""Emits the Strings table body from the JSON that extract-l10n.py produced.

Run once to seed L10n/Strings.Table.cs; after that the C# file is the source
and the LocalizationKeys test is what keeps it aligned with the Swift table.
"""

import io
import json
import sys

WINDOWS_OVERRIDES = {
    # The macOS text names macOS things. Same key, Windows wording.
    "keys.reveal": ("在文件资源管理器中显示", "Show in File Explorer"),
    "keys.footer": (
        "只读：本应用不会修改 %USERPROFILE%\\.ssh，也不会读取密钥内容，仅显示指纹等元信息。",
        "Read-only: this app never modifies %USERPROFILE%\\.ssh or reads key material — only metadata.",
    ),
    "keys.writeNote": (
        "密钥会写入 %USERPROFILE%\\.ssh，因此在终端和其他工具里也能直接用；写入后会用 icacls 收紧 ACL。",
        "Keys are written to %USERPROFILE%\\.ssh so they work in Terminal and every other tool too; icacls then tightens the ACL.",
    ),
    "keys.empty": (
        "%USERPROFILE%\\.ssh 下没有找到私钥。",
        "No private keys found in %USERPROFILE%\\.ssh.",
    ),
    "auth.identityHelp": (
        "私钥文件路径，例如 %USERPROFILE%\\.ssh\\id_ed25519。",
        "Path to a private key, e.g. %USERPROFILE%\\.ssh\\id_ed25519.",
    ),
    "auth.passwordHelp": (
        "密码保存在 Windows 凭据管理器，可在控制面板里查看和删除；连接时直接交给 SSH 库，不会出现在命令行里。",
        "The password lives in the Windows credential manager — visible and removable in Control Panel — and is handed straight to the SSH library, never to a command line.",
    ),
    "settings.credentialsNoteLocal": (
        "连接直接由本应用发起，私钥始终留在 %USERPROFILE%\\.ssh，本应用不会复制或存储密钥。",
        "Connections are made by this app directly; keys stay in %USERPROFILE%\\.ssh and are never copied here.",
    ),
    "import.subtitle": (
        "读取 %USERPROFILE%\\.ssh\\config 中的主机。连接时直接复用该配置，私钥始终留在 %USERPROFILE%\\.ssh。",
        "Reads hosts from %USERPROFILE%\\.ssh\\config and connects through it; keys never leave %USERPROFILE%\\.ssh.",
    ),
    "import.noneHelp": (
        "%USERPROFILE%\\.ssh\\config 不存在或没有可导入的 Host 条目。",
        "%USERPROFILE%\\.ssh\\config is missing or has no importable Host entries.",
    ),
    "auth.aliasHelp": (
        "直接复用 %USERPROFILE%\\.ssh\\config 中该 Host 的全部设置。",
        "Uses the whole Host block from %USERPROFILE%\\.ssh\\config.",
    ),
    "settings.launchAtLogin": ("开机自动启动", "Start with Windows"),
    "settings.terminalFont": ("字体", "Font"),
}


def escape(text: str) -> str:
    return text.replace(chr(92), chr(92) * 2).replace('"', chr(92) + '"')


def main() -> int:
    rows = json.load(io.open(sys.argv[1], encoding="utf-8"))
    out = []
    for key, zh, en in rows:
        if key in WINDOWS_OVERRIDES:
            zh, en = WINDOWS_OVERRIDES[key]
        out.append('        ["%s"] = ("%s", "%s"),' % (key, escape(zh), escape(en)))
    io.open(sys.argv[2], "w", encoding="utf-8", newline="\n").write("\n".join(out) + "\n")
    print("emitted", len(out), "rows;", len(WINDOWS_OVERRIDES), "overridden for Windows")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
