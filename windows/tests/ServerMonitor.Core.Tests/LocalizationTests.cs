using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class LocalizationTests
{
    private static List<string> MacKeys() => Fixture.Read("l10n-keys.txt")
        .Lines()
        .Select(l => l.Trim())
        .Where(l => l.Length > 0 && !l.StartsWith('#'))
        .ToList();

    [Fact]
    public void EveryMacKeyExistsHere()
    {
        // A card ported from the macOS file must find its keys already
        // present, so the two clients cannot drift into saying different
        // things. The failure names the gaps rather than just counting them.
        var missing = MacKeys().Except(Strings.Keys).OrderBy(k => k, StringComparer.Ordinal).ToList();
        Assert.True(missing.Count == 0, $"missing {missing.Count}: {string.Join(", ", missing)}");
    }

    /// <summary>
    /// Keys this build has and macOS does not, each one on purpose.
    /// </summary>
    /// <remarks>
    /// Listing them here is the "deliberate" part. Without somewhere to
    /// declare one, a Windows-only string had to be written inline in the view
    /// that used it — which is how the message box came to hold its own "OK"
    /// as a C# conditional on the language, outside the table every other
    /// string lives in and outside every check that runs over it.
    ///
    /// Add a key here only when the divergence is intended and the reason is
    /// written down at the string itself.
    /// </remarks>
    private static readonly string[] WindowsOnlyKeys =
    [
        // The macOS process card says "no process data" while it is still
        // waiting for the first detailed poll. This build separates the wait
        // from the answer, so it needs a word for the wait.
        "card.readingProcesses",
    ];

    [Fact]
    public void NoKeyHereIsUnknownToTheMacTable()
    {
        // The other direction. A Windows-only key is legitimate, but it should
        // be a deliberate addition rather than a typo that silently renders as
        // the key itself.
        var extra = Strings.Keys
            .Except(MacKeys())
            .Except(WindowsOnlyKeys)
            .OrderBy(k => k, StringComparer.Ordinal)
            .ToList();
        Assert.True(extra.Count == 0, $"unknown to the mac table: {string.Join(", ", extra)}");
    }

    [Fact]
    public void EveryDeclaredWindowsOnlyKeyIsRealAndStillWindowsOnly()
    {
        // The allow-list is only safe if it cannot rot. A name that no longer
        // exists means the divergence was undone and the entry is stale; one
        // macOS has since gained means the two tables agree again and the
        // exemption is hiding a real comparison.
        var gone = WindowsOnlyKeys.Except(Strings.Keys).ToList();
        Assert.True(gone.Count == 0, $"declared but absent: {string.Join(", ", gone)}");

        var shared = WindowsOnlyKeys.Intersect(MacKeys()).ToList();
        Assert.True(shared.Count == 0, $"no longer Windows-only: {string.Join(", ", shared)}");
    }

    [Fact]
    public void BothLanguagesAreFilledInForEveryKey()
    {
        using var zh = new LanguageScope(AppLanguage.Zh);
        var blankZh = Strings.Keys.Where(k => Strings.Get(k).Length == 0).ToList();
        Assert.Empty(blankZh);

        using var en = new LanguageScope(AppLanguage.En);
        var blankEn = Strings.Keys.Where(k => Strings.Get(k).Length == 0).ToList();
        Assert.Empty(blankEn);
    }

    [Fact]
    public void NoKeyRendersAsItsOwnName()
    {
        // Get() returns the key for a miss rather than throwing, so a typo
        // ships as a label reading "card.something". This is what catches it.
        using var _ = new LanguageScope(AppLanguage.En);
        var selfNamed = Strings.Keys.Where(k => Strings.Get(k) == k && k.Contains('.')).ToList();
        Assert.Empty(selfNamed);
    }

    [Fact]
    public void AMissingKeyReturnsItselfRatherThanThrowing()
    {
        // An exception here would take down whichever pane is rendering.
        Assert.Equal("no.such.key", Strings.Get("no.such.key"));
    }

    [Fact]
    public void TheLanguageIsSwitchableAtRuntime()
    {
        // The whole reason this is a dictionary rather than satellite
        // assemblies.
        using var zh = new LanguageScope(AppLanguage.Zh);
        Assert.Equal("仪表板", Strings.Get("nav.dashboard"));
        Strings.Language = AppLanguage.En;
        Assert.Equal("Dashboard", Strings.Get("nav.dashboard"));
    }

    [Fact]
    public void PlaceholdersAreSubstituted()
    {
        using var _ = new LanguageScope(AppLanguage.En);
        Assert.Equal("Imported 7", Strings.Get("import.done", "7"));
        Assert.Contains("web-1", Strings.Get("server.deleteConfirm", "web-1"), StringComparison.Ordinal);
    }

    [Fact]
    public void EveryPlaceholderKeyReallyHasAPlaceholderInBothLanguages()
    {
        // A key whose Chinese text lost its {} silently drops the argument,
        // and the message reads as though nothing was substituted.
        string[] withArgument =
        [
            "group.machines", "group.deleteConfirm", "identity.usedBy", "identity.inUse",
            "identity.deleteConfirm", "keys.generated", "keys.imported", "keys.deleteConfirm",
            "import.done", "import.skipped", "server.deleteConfirm", "sftp.deleteConfirm",
            "sftp.deleteSelectedConfirm", "snippet.runCount", "history.clearConfirm",
            "dashboard.lastSeen", "traffic.installConfirmTitle",
        ];
        foreach (var key in withArgument)
        {
            using var zh = new LanguageScope(AppLanguage.Zh);
            var chinese = Strings.Get(key);
            using var en = new LanguageScope(AppLanguage.En);
            var english = Strings.Get(key);
            // history.clearConfirm takes no argument on either side; the list
            // is asserted as a whole so a real omission is not hidden by one
            // exception being tolerated.
            if (key == "history.clearConfirm") continue;
            Assert.Contains("{}", chinese, StringComparison.Ordinal);
            Assert.Contains("{}", english, StringComparison.Ordinal);
        }
    }

    [Fact]
    public void TheWindowsSpecificWordingReplacedTheMacOne()
    {
        // Fourteen entries deliberately differ: ~/.ssh becomes
        // %USERPROFILE%\.ssh, Finder becomes File Explorer, the credential
        // notes name the credential manager rather than the keychain, and the
        // country code is a badge rather than a flag.
        using var _ = new LanguageScope(AppLanguage.En);
        Assert.Contains("File Explorer", Strings.Get("keys.reveal"), StringComparison.Ordinal);
        Assert.Contains("%USERPROFILE%", Strings.Get("keys.empty"), StringComparison.Ordinal);
        Assert.Contains("credential manager", Strings.Get("auth.passwordHelp"), StringComparison.Ordinal);
        Assert.Contains("Start with Windows", Strings.Get("settings.launchAtLogin"), StringComparison.Ordinal);
        Assert.Contains("badge", Strings.Get("server.countryHelp"), StringComparison.Ordinal);
    }

    /// <summary>
    /// No string promises something only macOS can do.
    /// </summary>
    /// <remarks>
    /// The table is seeded from the Swift one, so every row starts out
    /// describing a Mac. Two got through the eye: "Reveal in Finder", caught
    /// during the port, and server.countryHelp, which said the country code
    /// "shows a flag" — true on macOS, where the code renders as an emoji
    /// flag, and false here, because no Windows font has glyphs for
    /// regional-indicator pairs. The app draws a code badge instead, so the
    /// help described a flag the user would never see and the app looked like
    /// it had a missing font.
    ///
    /// A sweep rather than four more Assert.Contains lines: the next borrowed
    /// row will name some other Mac thing, and this fails on it without
    /// anyone thinking to add a case.
    /// </remarks>
    [Fact]
    public void NoStringNamesSomethingOnlyMacOsHas()
    {
        // Word-for-word, not substrings: "flag" appears inside "flags" and
        // any -flag option, and 命令 is an ordinary word that ⌘ is not.
        (string Term, string Why)[] macOnly =
        [
            ("Keychain", "the credential manager is the Windows equivalent"),
            ("钥匙串", "the credential manager is the Windows equivalent"),
            ("Finder", "File Explorer is the Windows equivalent"),
            ("访达", "File Explorer is the Windows equivalent"),
            ("flag", "no Windows font has regional-indicator glyphs; the app draws a badge"),
            ("国旗", "no Windows font has regional-indicator glyphs; the app draws a badge"),
            ("⌘", "Windows has no Command key"),
            ("⌥", "Windows has no Option key"),
            ("Launchpad", "there is no Launchpad"),
            ("Spotlight", "there is no Spotlight"),
            ("Dock", "the taskbar is the Windows equivalent"),
            ("菜单栏", "the notification area is the Windows equivalent"),
            ("menu bar", "the notification area is the Windows equivalent"),
            ("System Settings", "Windows calls it Settings"),
            ("System Preferences", "Windows calls it Settings"),
            ("系统偏好设置", "Windows calls it 设置"),
            ("Terminal.app", "the app has its own terminal"),
        ];

        var found = new List<string>();
        foreach (var language in new[] { AppLanguage.En, AppLanguage.Zh })
        {
            using var scope = new LanguageScope(language);
            foreach (var key in Strings.Keys)
            {
                var text = Strings.Get(key);
                foreach (var (term, why) in macOnly)
                {
                    if (!Mentions(text, term)) continue;
                    found.Add($"{key} ({language}) says \"{term}\" — {why}: {text}");
                }
            }
        }

        Assert.True(found.Count == 0, string.Join("\n", found));
    }

    /// <summary>
    /// Whether <paramref name="text"/> uses <paramref name="term"/> as a word.
    /// </summary>
    /// <remarks>
    /// A plain Contains would fire on "flag" inside "flags" or "--flag", and
    /// on "Dock" inside "Docker" — which the container page says on nearly
    /// every row. CJK terms have no word boundaries, so those match directly.
    /// </remarks>
    private static bool Mentions(string text, string term)
    {
        if (!char.IsAscii(term[0])) return text.Contains(term, StringComparison.Ordinal);

        var from = 0;
        while (true)
        {
            var at = text.IndexOf(term, from, StringComparison.OrdinalIgnoreCase);
            if (at < 0) return false;
            var before = at == 0 || !char.IsLetter(text[at - 1]);
            var afterAt = at + term.Length;
            var after = afterAt >= text.Length || !char.IsLetter(text[afterAt]);
            if (before && after) return true;
            from = at + 1;
        }
    }

    [Fact]
    public void AlertTextFollowsTheSameLanguage()
    {
        // Alerts are produced off the view tree, where the macOS build needed
        // a separate bridge with its own duplicated strings.
        using (var zh = new LanguageScope(AppLanguage.Zh))
        {
            Assert.Equal("已离线", Strings.Offline);
            Assert.Contains("内存", Strings.Threshold(AlertService.Metric.Memory, 91.4, 90), StringComparison.Ordinal);
            Assert.Contains("91", Strings.Threshold(AlertService.Metric.Memory, 91.4, 90), StringComparison.Ordinal);
        }
        using (var en = new LanguageScope(AppLanguage.En))
        {
            Assert.Equal("Offline", Strings.Offline);
            Assert.Equal("Back online", Strings.Recovered);
            Assert.Equal("CPU above 90% (now 95%)", Strings.Threshold(AlertService.Metric.Cpu, 94.6, 90));
        }
    }
}
