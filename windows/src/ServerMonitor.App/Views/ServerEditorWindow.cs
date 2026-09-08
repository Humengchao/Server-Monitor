using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;

namespace ServerMonitor.App.Views;

/// <summary>
/// Add or edit a monitored host.
/// </summary>
/// <remarks>
/// The one place a password is typed, and the reason it is a modal: the fields
/// that matter depend on the auth method, and a form that reshapes itself
/// inside a list is harder to follow than one that owns the screen.
/// </remarks>
public sealed class ServerEditorWindow : Window
{
    private static MonitorService Monitor => App.Current.Monitor;
    private static ICredentialStore Credentials => App.Current.Credentials;

    private readonly Server _server;
    private readonly bool _isNew;

    private readonly TextBox _name = Ui.Input();
    private readonly TextBox _host = Ui.Input();
    private readonly TextBox _port = Ui.Input("22", 70);
    private readonly TextBox _username = Ui.Input();
    private readonly TextBox _alias = Ui.Input();
    private readonly TextBox _identityFile = Ui.Input();
    private readonly PasswordBox _password = Ui.Secret();
    private readonly TextBox _country = Ui.Input("", 70);
    private readonly TextBox _tags = Ui.Input();
    private readonly TextBox _notes = Ui.Input();

    private readonly StackPanel _authFields = Ui.Rows(0);
    private readonly TextBlock _error = Ui.Wrapped("", "Text.Caption");
    private readonly TextBlock _hint = Ui.Wrapped("", "Text.Tertiary");

    private AuthKind _authKind;
    private OSKind _osKind;
    private Guid? _identityId;
    private Guid? _groupId;
    private int? _cpuThreshold;
    private int? _memoryThreshold;
    private int? _diskThreshold;
    private bool _forceOpenSshExe;

    public ServerEditorWindow(Server? existing)
    {
        _isNew = existing is null;
        _server = existing?.Clone() ?? new Server
        {
            AuthKind = AuthKind.SshConfigAlias,
            SortIndex = Monitor.Database.NextSortIndex(),
        };

        _authKind = _server.AuthKind;
        _osKind = _server.OsKind;
        _identityId = _server.IdentityId;
        _groupId = _server.GroupId;
        _cpuThreshold = _server.CpuThreshold;
        _memoryThreshold = _server.MemoryThreshold;
        _diskThreshold = _server.DiskThreshold;
        _forceOpenSshExe = App.Current.Settings.ForceOpenSshExe.Contains(_server.Id);

        Title = Strings.Get(_isNew ? "server.add" : "server.edit");
        Width = 620;
        SizeToContent = SizeToContent.Height;
        MaxHeight = 900;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _name.Text = _server.Name;
        _host.Text = _server.Host;
        _port.Text = _server.Port.ToString(System.Globalization.CultureInfo.InvariantCulture);
        _username.Text = _server.Username;
        _alias.Text = _server.SshAlias;
        _identityFile.Text = _server.IdentityFile;
        _country.Text = _server.CountryCode;
        _tags.Text = _server.TagList;
        _notes.Text = _server.Notes;

        _error.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
        _error.Visibility = Visibility.Collapsed;

        Content = Build();
        RefreshAuthFields();
    }

    private UIElement Build()
    {
        var identities = new List<Identity?> { null };
        identities.AddRange(Monitor.Identities.Cast<Identity?>());

        var groups = new List<MachineGroup?> { null };
        groups.AddRange(Monitor.Groups.Cast<MachineGroup?>());

        var body = Ui.Rows(0,
            Ui.Field("server.name", _name),
            Ui.Field("server.host", _host),
            Ui.Field("server.port", _port),
            Ui.Field("server.username", _username),

            Ui.Field("server.authMethod", Ui.Picker(
                new[] { AuthKind.SshConfigAlias, AuthKind.IdentityFile, AuthKind.Agent, AuthKind.Password },
                _authKind,
                LabelFor,
                kind =>
                {
                    _authKind = kind;
                    RefreshAuthFields();
                })),
            _authFields,
            _hint,

            Ui.Field("identity.name", Ui.Picker(
                identities,
                identities.FirstOrDefault(i => i?.Id == _identityId),
                identity => identity is null ? Strings.Get("common.none") : identity.Summary,
                identity => _identityId = identity?.Id)),

            Ui.Field("group.title", Ui.Picker(
                groups,
                groups.FirstOrDefault(g => g?.Id == _groupId),
                group => group?.Name ?? Strings.Get("group.none"),
                group => _groupId = group?.Id)),

            Ui.Field("server.osKind", Ui.Picker(
                new[] { OSKind.Auto, OSKind.Linux, OSKind.Windows },
                _osKind,
                kind => kind switch
                {
                    OSKind.Linux => Strings.Get("server.osLinux"),
                    OSKind.Windows => Strings.Get("server.osWindows"),
                    _ => Strings.Get("server.osAuto"),
                },
                kind => _osKind = kind), "server.osHelp"),

            Ui.Field("server.country", _country, "server.countryHelp"),
            Ui.Field("server.tags", _tags, "server.tagsHelp"),
            Ui.Field("server.notes", _notes),

            Ui.Separator(),
            Ui.Title(Strings.Get("server.thresholds")),
            Ui.Wrapped(Strings.Get("server.thresholdHelp"), "Text.Tertiary"),
            ThresholdField("settings.cpuThreshold", _cpuThreshold, value => _cpuThreshold = value),
            ThresholdField("settings.memoryThreshold", _memoryThreshold, value => _memoryThreshold = value),
            ThresholdField("settings.diskThreshold", _diskThreshold, value => _diskThreshold = value),

            Ui.Separator(),
            // R13's per-host escape hatch, in the one place a host's
            // connection is configured.
            Ui.Toggle(
                $"{Strings.Get("settings.general")}: ssh.exe",
                _forceOpenSshExe,
                value => _forceOpenSshExe = value),
            Ui.Wrapped(
                Strings.IsChinese
                    ? "改用系统 ssh.exe 连接这台主机。适用于需要 Match 块、证书、PKCS#11 或真正的 ssh-agent 的主机；代价是没有连接复用，每次采集都要完整握手。"
                    : "Reach this host through the system ssh.exe instead. For hosts needing Match blocks, a certificate, PKCS#11 or a real ssh-agent; the cost is no connection reuse, so every poll is a full handshake.",
                "Text.Tertiary"),

            _error);

        var buttons = Ui.Columns(8,
            Ui.Button(Strings.Get("common.testConnection"), TestConnection),
            Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); }),
            Ui.Accent(Strings.Get("common.save"), Save));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0, Ui.Scroll(body), buttons);
        ((ScrollViewer)root.Children[0]).MaxHeight = 640;
        root.Margin = new Thickness(20);
        return root;
    }

    private static string LabelFor(AuthKind kind) => kind switch
    {
        AuthKind.SshConfigAlias => Strings.Get("auth.sshConfigAlias"),
        AuthKind.IdentityFile => Strings.Get("auth.identityFile"),
        AuthKind.Agent => Strings.Get("auth.agent"),
        _ => Strings.Get("server.password"),
    };

    /// <summary>
    /// A threshold picker where "follow global" and "off" are different
    /// answers.
    /// </summary>
    /// <remarks>
    /// null inherits the global setting; 0 means no alert for this metric at
    /// all. Collapsing the two — which a plain number box would — loses the
    /// ability to silence one noisy host without changing the fleet.
    /// </remarks>
    private static UIElement ThresholdField(string key, int? current, Action<int?> onChange)
    {
        var options = new List<int?> { null };
        options.AddRange(AppSettings.ThresholdChoices.Cast<int?>());
        return Ui.Field(key, Ui.Picker(
            options,
            options.FirstOrDefault(o => o == current),
            value => value switch
            {
                null => Strings.Get("server.thresholdInherit"),
                0 => Strings.Get("settings.thresholdOff"),
                _ => $"{value}%",
            },
            onChange));
    }

    /// <summary>Shows only the fields the chosen method actually uses.</summary>
    private void RefreshAuthFields()
    {
        _authFields.Children.Clear();
        switch (_authKind)
        {
            case AuthKind.SshConfigAlias:
                _authFields.Children.Add(Ui.Field("server.alias", _alias));
                _hint.Text = Strings.Get("auth.aliasHelp");
                break;

            case AuthKind.IdentityFile:
                _authFields.Children.Add(Ui.Field(
                    "server.identityPath",
                    Ui.Grid("*,auto", _identityFile, Ui.Button(Strings.Get("common.browse"), BrowseForKey))));
                _hint.Text = Strings.Get("auth.identityHelp");
                break;

            case AuthKind.Agent:
                // Say plainly what "agent" means on the library route rather
                // than letting the user discover it as an auth failure.
                _hint.Text = Strings.IsChinese
                    ? "SSH.NET 没有 agent 认证，因此这里会尝试 %USERPROFILE%\\.ssh 下的默认密钥。密钥只存在于 agent 中的主机请勾选下方的 ssh.exe。"
                    : "SSH.NET has no agent authentication, so this offers the default keys in %USERPROFILE%\\.ssh. For a host whose key only lives in the agent, tick ssh.exe below.";
                break;

            case AuthKind.Password:
                _authFields.Children.Add(Ui.Field(
                    "server.password",
                    _password,
                    _isNew ? null : "server.secretUnchanged"));
                _hint.Text = Strings.Get("auth.passwordHelp");
                break;
        }
    }

    private void BrowseForKey()
    {
        var dialog = new Microsoft.Win32.OpenFileDialog
        {
            Title = Strings.Get("server.privateKey"),
            InitialDirectory = SshConfig.SshDirectory,
            CheckFileExists = true,
        };
        if (dialog.ShowDialog(this) == true) _identityFile.Text = dialog.FileName;
    }

    // MARK: - Save

    /// <summary>
    /// Reads the form into the row, or reports what is missing.
    /// </summary>
    /// <remarks>
    /// The validation lives here rather than on the model because it is about
    /// what this <em>form</em> needs: a row loaded from a database written by
    /// an older build may legitimately be missing a field this form now
    /// insists on, and refusing to load it would be worse.
    /// </remarks>
    private bool Collect()
    {
        void Fail(string key)
        {
            _error.Text = Strings.Get(key);
            _error.Visibility = Visibility.Visible;
        }

        _error.Visibility = Visibility.Collapsed;

        var name = _name.Text.Trim();
        if (name.Length == 0)
        {
            Fail("server.nameRequired");
            return false;
        }

        if (_authKind == AuthKind.SshConfigAlias)
        {
            if (_alias.Text.Trim().Length == 0)
            {
                Fail("server.aliasRequired");
                return false;
            }
        }
        else
        {
            // Every other method dials an address itself.
            if (_host.Text.Trim().Length == 0 || _username.Text.Trim().Length == 0)
            {
                Fail("server.hostRequired");
                return false;
            }
        }

        if (_authKind == AuthKind.IdentityFile && _identityFile.Text.Trim().Length == 0)
        {
            Fail("server.keyRequired");
            return false;
        }

        // A new password-auth host must have a password; an existing one may
        // leave the box empty to keep the stored one.
        if (_authKind == AuthKind.Password
            && _isNew
            && _password.Password.Length == 0)
        {
            Fail("server.passwordRequired");
            return false;
        }

        _server.Name = name;
        _server.Host = _host.Text.Trim();
        _server.Port = _port.Text.Trim().ToIntOrNull() is { } port and > 0 and < 65536 ? port : 22;
        _server.Username = _username.Text.Trim();
        _server.AuthKind = _authKind;
        _server.SshAlias = _alias.Text.Trim();
        _server.IdentityFile = SshConfig.ExpandPath(_identityFile.Text.Trim());
        _server.IdentityId = _identityId;
        _server.GroupId = _groupId;
        _server.OsKind = _osKind;
        _server.CountryCode = _country.Text.Trim().ToUpperInvariant();
        // Through the property, so duplicates and blanks are dropped and the
        // stored form is normalised.
        _server.Tags = Server.ParseTags(_tags.Text);
        _server.Notes = _notes.Text.Trim();
        _server.CpuThreshold = _cpuThreshold;
        _server.MemoryThreshold = _memoryThreshold;
        _server.DiskThreshold = _diskThreshold;
        return true;
    }

    private void Save()
    {
        if (!Collect()) return;

        if (_authKind == AuthKind.Password && _password.Password.Length > 0)
        {
            try
            {
                Credentials.SetPassword(_server.Id, _password.Password);
            }
            catch (Exception error)
            {
                Ui.Complain(this, error.Message);
                return;
            }
        }

        var forced = App.Current.Settings.ForceOpenSshExe;
        if (_forceOpenSshExe && !forced.Contains(_server.Id)) forced.Add(_server.Id);
        else if (!_forceOpenSshExe) forced.Remove(_server.Id);
        // The list is mutated in place, so the debounced save has to be
        // nudged: it hangs off property setters, and this is not one.
        App.Current.Settings.Flush();

        if (_isNew) Monitor.AddServer(_server);
        else Monitor.UpdateServer(_server);

        DialogResult = true;
        Close();
    }

    private async void TestConnection()
    {
        if (!Collect()) return;

        // Saved first when it is a password, or the test authenticates with
        // whatever was stored before — which for a new host is nothing.
        if (_authKind == AuthKind.Password && _password.Password.Length > 0)
        {
            Credentials.SetPassword(_server.Id, _password.Password);
        }

        IsEnabled = false;
        try
        {
            // Through the service's target resolution, so an identity's
            // username and key apply exactly as they will when polling.
            var target = Monitor.Target(_server);
            await Monitor.TestConnectionAsync(target, _osKind);
            Ui.Inform(this, Strings.Get("server.connectionOK"));
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

/// <summary>Create or rename a machine group.</summary>
public sealed class GroupEditorWindow : Window
{
    private readonly MachineGroup _group;
    private readonly bool _isNew;
    private readonly TextBox _name = Ui.Input();
    private readonly TextBlock _error = Ui.Wrapped("", "Text.Caption");
    private string _colour;

    public GroupEditorWindow(MachineGroup? existing)
    {
        _isNew = existing is null;
        _group = existing?.Clone() ?? new MachineGroup
        {
            SortIndex = App.Current.Monitor.NextGroupSortIndex(),
        };
        _colour = _group.ColorName;
        _name.Text = _group.Name;

        Title = Strings.Get(_isNew ? "group.new" : "common.edit");
        Width = 420;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _error.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
        _error.Visibility = Visibility.Collapsed;

        var buttons = Ui.Columns(8,
            Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); }),
            Ui.Accent(Strings.Get("common.save"), Save));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0,
            Ui.Field("group.name", _name),
            Ui.Field("group.color", Ui.Picker(
                MachineGroup.Palette,
                _colour,
                name => name,
                name => _colour = name)),
            _error,
            buttons);
        root.Margin = new Thickness(20);
        Content = root;
    }

    private void Save()
    {
        var name = _name.Text.Trim();
        if (name.Length == 0)
        {
            _error.Text = Strings.Get("group.required");
            _error.Visibility = Visibility.Visible;
            return;
        }
        _group.Name = name;
        _group.ColorName = _colour;
        App.Current.Monitor.Save(_group);
        DialogResult = true;
        Close();
    }
}
