using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// The private keys in <c>%USERPROFILE%\\.ssh</c>.
/// </summary>
/// <remarks>
/// Reads metadata only — fingerprints, types, comments — never key material.
/// Keys are created and imported into the directory OpenSSH already owns, so a
/// key made here works immediately in <c>ssh.exe</c>, in <c>~/.ssh/config</c>
/// and for every other tool on the machine.
/// </remarks>
public sealed class SshKeysPage : UserControl
{
    private readonly StackPanel _root = Ui.Rows(0);
    private List<SshKeyFile> _keys = [];

    public SshKeysPage()
    {
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);
        _ = ReloadAsync();
    }

    private async Task ReloadAsync()
    {
        _root.Children.Clear();
        _root.Children.Add(Ui.Spinner());
        // ssh-keygen once per file, so a directory with a dozen keys is a
        // dozen process launches — off the UI thread.
        _keys = await SshKeyScanner.ScanAsync();
        Rebuild();
    }

    private void Rebuild()
    {
        _root.Children.Clear();

        var toolbar = Ui.Columns(8,
            Ui.Accent(Strings.Get("keys.generate"), Generate),
            Ui.Button(Strings.Get("keys.importFile"), ImportFile),
            Ui.Button(Strings.Get("keys.importClipboard"), ImportClipboard),
            Ui.Quiet(Strings.Get("common.refresh"), () => _ = ReloadAsync()));
        toolbar.HorizontalAlignment = HorizontalAlignment.Left;
        toolbar.Margin = new Thickness(0, 0, 0, 14);
        _root.Children.Add(toolbar);

        if (SshLocator.FindKeygen() is null)
        {
            var warning = Ui.Wrapped(
                Strings.IsChinese
                    ? "找不到 ssh-keygen.exe，无法读取密钥信息或生成新密钥。安装方式：Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0"
                    : "No ssh-keygen.exe found, so key metadata cannot be read and new keys cannot be generated. Install it with: Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0",
                "Text.Caption");
            warning.Foreground = Theme.Ink.Brush(Theme.Palette.Warning);
            _root.Children.Add(Ui.Card(warning));
        }

        if (_keys.Count == 0)
        {
            _root.Children.Add(Ui.Card(Ui.Rows(6,
                Ui.Wrapped(Strings.Get("keys.empty")),
                Ui.Wrapped(Strings.Get("keys.writeNote"), "Text.Tertiary"))));
            return;
        }

        foreach (var key in _keys) _root.Children.Add(CardFor(key));
        // Not keys.footer: that string says the app never modifies
        // %USERPROFILE%\\.ssh, which was true of the macOS page it came from
        // and is contradicted by the four buttons above — generate, import,
        // delete and tighten all write there. What is still true is that key
        // material is never read or shown, which is the part worth promising.
        _root.Children.Add(Ui.Wrapped(
            Strings.IsChinese
                ? "密钥留在 %USERPROFILE%\\.ssh 由 OpenSSH 自己管，所以在 ssh.exe、~/.ssh/config 和其他工具里同样可用。本应用只读取指纹、类型、注释这类元信息，从不读取或显示私钥内容；生成与导入的密钥会立即用 icacls 收紧 ACL。"
                : "Keys stay in %USERPROFILE%\\.ssh where OpenSSH owns them, so a key here works the same in ssh.exe, in ~/.ssh/config and in every other tool. This app reads only metadata — fingerprint, type, comment — and never reads or displays key material; a key it generates or imports has its ACL tightened with icacls straight away.",
            "Text.Tertiary"));
    }

    private UIElement CardFor(SshKeyFile key)
    {
        var heading = Ui.Columns(10, Ui.Title(key.Name), Ui.Caption($"{key.Type} {key.Bits}"));

        if (key.IsEncrypted) heading.Children.Add(Badge("keys.protected", Theme.Palette.Online));
        // DSA is the one to warn about: modern sshd refuses it outright, while
        // RSA still works because OpenSSH negotiates rsa-sha2.
        if (key.IsLegacyAlgorithm) heading.Children.Add(Badge("keys.legacy", Theme.Palette.Warning));
        if (!key.AclIsTight)
        {
            // Not "error": the key is fine, its permissions are not. Saying
            // "error" next to a fingerprint reads as a corrupt key.
            heading.Children.Add(BadgeText(
                Strings.IsChinese ? "权限过宽" : "Permissions", Theme.Palette.Offline));
        }

        foreach (var child in heading.Children.OfType<FrameworkElement>())
        {
            child.VerticalAlignment = VerticalAlignment.Center;
        }

        var actions = Ui.Columns(6);
        if (key.HasPublicKey)
        {
            actions.Children.Add(Ui.Quiet(Strings.Get("keys.copyPublic"), () => CopyPublic(key)));
            actions.Children.Add(Ui.Quiet(Strings.Get("keys.export"), () => Export(key)));
        }
        actions.Children.Add(Ui.Quiet(Strings.Get("keys.reveal"), () => Reveal(key)));
        actions.Children.Add(Ui.Danger(Strings.Get("common.delete"), () => Delete(key)));
        actions.HorizontalAlignment = HorizontalAlignment.Right;

        var children = new List<UIElement>
        {
            Ui.Grid("*,auto", heading, actions),
            Ui.Mono(key.Fingerprint),
        };
        if (key.Comment.Length > 0) children.Add(Ui.Caption(key.Comment));
        children.Add(Ui.Tertiary(key.Path));

        if (!key.AclIsTight)
        {
            // R11: Windows OpenSSH checks ACLs, not a permission bitmask, and
            // refuses a key other users can read — with an error that does not
            // say so.
            var warning = Ui.Wrapped(
                Strings.IsChinese
                    ? "这个私钥的 ACL 对其他用户开放，ssh 会拒绝使用它。点击「收紧权限」用 icacls 去掉继承、只保留当前用户。"
                    : "This key's ACL is open to other users, so ssh will refuse it. Tighten permissions runs icacls to remove inheritance and leave only you.",
                "Text.Caption");
            warning.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
            children.Add(warning);
            var fix = Ui.Button(
                Strings.IsChinese ? "收紧权限" : "Tighten permissions",
                () =>
                {
                    SshKeyManager.RestrictPermissions(key.Path);
                    _ = ReloadAsync();
                });
            fix.HorizontalAlignment = HorizontalAlignment.Left;
            children.Add(fix);
        }

        var card = Ui.Card(Ui.Rows(6, [.. children]));
        card.Margin = new Thickness(0, 0, 0, 10);
        return card;
    }

    private static UIElement Badge(string key, System.Windows.Media.Color colour) =>
        BadgeText(Strings.Get(key), colour);

    private static UIElement BadgeText(string label, System.Windows.Media.Color colour)
    {
        var text = Ui.Caption(label);
        text.Foreground = Theme.Ink.Brush(colour);
        return new Border
        {
            Background = Theme.Ink.Brush(
                System.Windows.Media.Color.FromArgb(0x28, colour.R, colour.G, colour.B)),
            CornerRadius = new CornerRadius(8),
            Padding = new Thickness(6, 1, 6, 2),
            Child = text,
        };
    }

    // MARK: - Actions

    private void CopyPublic(SshKeyFile key)
    {
        var text = SshKeyScanner.PublicKey(key);
        if (text is null)
        {
            Ui.Complain(Window.GetWindow(this), Strings.Get("keys.exportNoPublic"));
            return;
        }
        if (Ui.Copy(text)) Ui.Inform(Window.GetWindow(this), Strings.Get("keys.copied"));
    }

    private void Reveal(SshKeyFile key)
    {
        try
        {
            System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo("explorer.exe")
            {
                // /select, so the file is highlighted rather than the folder
                // merely opened.
                Arguments = $"/select,\"{key.Path}\"",
                UseShellExecute = true,
            });
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }

    private void Delete(SshKeyFile key)
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("keys.deleteConfirm", key.Name))) return;
        try
        {
            SshKeyManager.Delete(key);
            _ = ReloadAsync();
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }

    private void Generate()
    {
        var window = new GenerateKeyWindow { Owner = Window.GetWindow(this) };
        if (window.ShowDialog() == true) _ = ReloadAsync();
    }

    private void ImportFile()
    {
        var dialog = new Microsoft.Win32.OpenFileDialog
        {
            Title = Strings.Get("keys.importFile"),
            CheckFileExists = true,
        };
        if (dialog.ShowDialog(Window.GetWindow(this)) != true) return;
        try
        {
            var path = SshKeyManager.ImportFile(dialog.FileName);
            Ui.Inform(
                Window.GetWindow(this),
                Strings.Get("keys.imported", System.IO.Path.GetFileName(path)));
            _ = ReloadAsync();
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }

    private void ImportClipboard()
    {
        string text;
        try
        {
            text = Clipboard.GetText();
        }
        catch (Exception)
        {
            text = string.Empty;
        }
        if (text.Trim().Length == 0)
        {
            Ui.Complain(Window.GetWindow(this), Strings.Get("keys.clipboardEmpty"));
            return;
        }

        var name = new NameKeyWindow { Owner = Window.GetWindow(this) };
        if (name.ShowDialog() != true) return;

        try
        {
            var path = SshKeyManager.ImportKey(text, name.KeyName);
            Ui.Inform(
                Window.GetWindow(this),
                Strings.Get("keys.imported", System.IO.Path.GetFileName(path)));
            _ = ReloadAsync();
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }

    /// <summary>
    /// Appends the public key to a host's <c>authorized_keys</c>.
    /// </summary>
    /// <remarks>
    /// Over the connection the app already has, so it works for hosts reached
    /// through a ProxyJump or a config alias — which <c>ssh-copy-id</c> on
    /// Windows cannot do, since there is no such tool in the box.
    /// </remarks>
    private async void Export(SshKeyFile key)
    {
        var publicKey = SshKeyScanner.PublicKey(key);
        if (publicKey is null)
        {
            Ui.Complain(Window.GetWindow(this), Strings.Get("keys.exportNoPublic"));
            return;
        }
        if (App.Current.Monitor.Servers.Count == 0)
        {
            Ui.Inform(Window.GetWindow(this), Strings.Get("dashboard.empty"));
            return;
        }

        var picker = new PickServerWindow(Strings.Get("keys.exportHelp"))
        {
            Owner = Window.GetWindow(this),
        };
        if (picker.ShowDialog() != true || picker.Selected is not { } server) return;

        try
        {
            var monitor = App.Current.Monitor;
            var installer = new PublicKeyInstaller(new SingleTransport(monitor, server));
            var outcome = await installer.InstallAsync(publicKey, monitor.Target(server));
            Ui.Inform(
                Window.GetWindow(this),
                Strings.Get(outcome == InstallOutcome.Added ? "keys.exported" : "keys.exportAlready"));
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }

    /// <summary>
    /// Routes one host's commands through the service, which owns the
    /// transport choice.
    /// </summary>
    /// <remarks>
    /// <see cref="PublicKeyInstaller"/> takes an <see cref="ISshTransport"/>,
    /// and the app's transports are private to <c>App</c> because the choice is
    /// per host (D3). This adapter borrows the service's own routing rather
    /// than exposing them.
    /// </remarks>
    private sealed class SingleTransport(Core.Collect.MonitorService monitor, Server server) : ISshTransport
    {
        public string Name => "routed";

        public Task<string> RunAsync(
            string command, SshTarget target, int timeoutSeconds = 30,
            CancellationToken cancellationToken = default) =>
            monitor.RunAsync(command, server, timeoutSeconds, cancellationToken);

        public Task DisconnectAsync(SshTarget target) => Task.CompletedTask;

        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}

public sealed class GenerateKeyWindow : Window
{
    private readonly TextBox _name = Ui.Input("id_ed25519");
    private readonly TextBox _comment = Ui.Input($"{Environment.UserName}@{Environment.MachineName}");
    private readonly PasswordBox _passphrase = Ui.Secret();
    private readonly TextBlock _error = Ui.Wrapped("", "Text.Caption");
    private SshKeyManager.KeyType _type = SshKeyManager.KeyType.Ed25519;

    public GenerateKeyWindow()
    {
        Title = Strings.Get("keys.generate");
        Width = 520;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _error.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
        _error.Visibility = Visibility.Collapsed;

        var buttons = Ui.Columns(8,
            Ui.Cancels(Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); })),
            Ui.Default(Ui.Accent(Strings.Get("keys.generate"), () => _ = GenerateAsync())));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0,
            Ui.Field("keys.keyName", _name),
            Ui.Field("keys.keyType", Ui.Picker(
                new[] { SshKeyManager.KeyType.Ed25519, SshKeyManager.KeyType.Rsa4096 },
                _type,
                type => type == SshKeyManager.KeyType.Ed25519 ? "Ed25519" : "RSA 4096",
                type =>
                {
                    _type = type;
                    // The default filename follows the type, since that is
                    // what every other tool on the machine expects to find.
                    _name.Text = type == SshKeyManager.KeyType.Ed25519 ? "id_ed25519" : "id_rsa";
                })),
            Ui.Field("keys.comment", _comment),
            Ui.Field("keys.passphrase", _passphrase),
            Ui.Wrapped(Strings.Get("keys.writeNote"), "Text.Tertiary"),
            _error,
            buttons);
        root.Margin = new Thickness(20);
        Content = root;
    }

    private async Task GenerateAsync()
    {
        IsEnabled = false;
        try
        {
            var path = await SshKeyManager.GenerateAsync(
                _name.Text.Trim(), _type, _comment.Text.Trim(), _passphrase.Password);
            DialogResult = true;
            Close();
            Ui.Inform(Owner, Strings.Get("keys.generated", System.IO.Path.GetFileName(path)));
        }
        catch (Exception error)
        {
            _error.Text = error.Message;
            _error.Visibility = Visibility.Visible;
        }
        finally
        {
            IsEnabled = true;
        }
    }
}

/// <summary>Asks for a filename, for a key pasted from the clipboard.</summary>
public sealed class NameKeyWindow : Window
{
    private readonly TextBox _name = Ui.Input("imported_key");

    public string KeyName => _name.Text.Trim();

    public NameKeyWindow()
    {
        Title = Strings.Get("keys.keyName");
        Width = 420;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        var buttons = Ui.Columns(8,
            Ui.Cancels(Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); })),
            Ui.Default(Ui.Accent(Strings.Get("common.save"), () => { DialogResult = true; Close(); })));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0, Ui.Field("keys.keyName", _name), buttons);
        root.Margin = new Thickness(20);
        Content = root;
    }
}

/// <summary>Picks one host, for an action that needs a target.</summary>
public sealed class PickServerWindow : Window
{
    public Server? Selected { get; private set; }

    public PickServerWindow(string prompt)
    {
        Selected = App.Current.Monitor.Servers.FirstOrDefault();

        Title = Strings.Get("nav.machines");
        Width = 460;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        var picker = Ui.Picker(
            App.Current.Monitor.Servers.ToList(),
            Selected,
            server => $"{server.Name} — {server.DisplayTarget}",
            server => Selected = server);

        var buttons = Ui.Columns(8,
            Ui.Cancels(Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); })),
            Ui.Default(Ui.Accent(Strings.Get("common.save"), () => { DialogResult = true; Close(); })));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(10, Ui.Wrapped(prompt, "Text.Secondary"), picker, buttons);
        root.Margin = new Thickness(20);
        Content = root;
    }
}
