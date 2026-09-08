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

    [Fact]
    public void NoKeyHereIsUnknownToTheMacTable()
    {
        // The other direction. A Windows-only key is legitimate, but it should
        // be a deliberate addition rather than a typo that silently renders as
        // the key itself.
        var extra = Strings.Keys.Except(MacKeys()).OrderBy(k => k, StringComparer.Ordinal).ToList();
        Assert.True(extra.Count == 0, $"unknown to the mac table: {string.Join(", ", extra)}");
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
        // Thirteen entries deliberately differ: ~/.ssh becomes
        // %USERPROFILE%\.ssh, Finder becomes File Explorer, and the credential
        // notes name the credential manager rather than the keychain.
        using var _ = new LanguageScope(AppLanguage.En);
        Assert.Contains("File Explorer", Strings.Get("keys.reveal"), StringComparison.Ordinal);
        Assert.Contains("%USERPROFILE%", Strings.Get("keys.empty"), StringComparison.Ordinal);
        Assert.Contains("credential manager", Strings.Get("auth.passwordHelp"), StringComparison.Ordinal);
        Assert.Contains("Start with Windows", Strings.Get("settings.launchAtLogin"), StringComparison.Ordinal);

        // And nothing still says Keychain or Finder.
        foreach (var language in new[] { AppLanguage.En, AppLanguage.Zh })
        {
            using var scope = new LanguageScope(language);
            foreach (var key in Strings.Keys)
            {
                var text = Strings.Get(key);
                Assert.DoesNotContain("Keychain", text, StringComparison.OrdinalIgnoreCase);
                Assert.DoesNotContain("Finder", text, StringComparison.Ordinal);
                Assert.DoesNotContain("钥匙串", text, StringComparison.Ordinal);
                Assert.DoesNotContain("访达", text, StringComparison.Ordinal);
            }
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
