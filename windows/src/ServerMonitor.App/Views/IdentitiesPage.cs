using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// Shared logins: a username plus how it authenticates.
/// </summary>
/// <remarks>
/// Exists so a fleet sharing one key does not restate it per machine. A server
/// pointing at an identity takes its username and auth, so changing the shared
/// login updates every machine using it.
/// </remarks>
public sealed class IdentitiesPage : UserControl
{
    private readonly StackPanel _root = Ui.Rows(0);

    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    public IdentitiesPage()
    {
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);
        Rebuild();
    }

    private void Rebuild()
    {
        _root.Children.Clear();

        var add = Ui.Accent(Strings.Get("identity.new"), () => Edit(null));

        if (Monitor.Identities.Count == 0)
        {
            _root.Children.Add(Ui.Empty(null, "identity.empty", add));
            return;
        }

        add.HorizontalAlignment = HorizontalAlignment.Left;
        add.Margin = new Thickness(0, 0, 0, 14);
        _root.Children.Add(add);

        foreach (var identity in Monitor.Identities)
        {
            var inUse = Monitor.ServerCountUsingIdentity(identity.Id);
            var heading = Ui.Columns(10,
                Ui.Title(identity.Name),
                Ui.Caption(Strings.Get(
                    "identity.usedBy",
                    inUse.ToString(System.Globalization.CultureInfo.InvariantCulture))));
            foreach (var child in heading.Children.OfType<FrameworkElement>())
            {
                child.VerticalAlignment = VerticalAlignment.Center;
            }

            var actions = Ui.Columns(6,
                Ui.Quiet(Strings.Get("common.edit"), () => Edit(identity)),
                Ui.Danger(Strings.Get("common.delete"), () => Delete(identity, inUse)));
            actions.HorizontalAlignment = HorizontalAlignment.Right;

            var card = Ui.Card(Ui.Rows(6,
                Ui.Grid("*,auto", heading, actions),
                Ui.Caption(identity.Summary)));
            card.Margin = new Thickness(0, 0, 0, 10);
            _root.Children.Add(card);
        }
    }

    private void Edit(Identity? existing)
    {
        var window = new IdentityEditorWindow(existing) { Owner = Window.GetWindow(this) };
        if (window.ShowDialog() == true)
        {
            Monitor.Reload();
            Rebuild();
        }
    }

    private void Delete(Identity identity, int inUse)
    {
        // The count matters: deleting an identity in use silently changes how
        // those hosts authenticate, so it is said out loud first.
        var message = Strings.Get("identity.deleteConfirm", identity.Name);
        if (inUse > 0)
        {
            message += "\n\n" + Strings.Get(
                "identity.inUse",
                inUse.ToString(System.Globalization.CultureInfo.InvariantCulture));
        }
        if (!Ui.Confirm(Window.GetWindow(this), message)) return;
        Monitor.DeleteIdentity(identity.Id);
        Rebuild();
    }
}

public sealed class IdentityEditorWindow : Window
{
    private readonly Identity _identity;
    private readonly bool _isNew;
    private readonly TextBox _name = Ui.Input();
    private readonly TextBox _username = Ui.Input();
    private readonly TextBox _identityFile = Ui.Input();
    private readonly StackPanel _authFields = Ui.Rows(0);
    private readonly TextBlock _error = Ui.Wrapped("", "Text.Caption");
    private AuthKind _authKind;

    public IdentityEditorWindow(Identity? existing)
    {
        _isNew = existing is null;
        _identity = existing?.Clone() ?? new Identity { AuthKind = AuthKind.IdentityFile };
        _authKind = _identity.AuthKind;
        _name.Text = _identity.Name;
        _username.Text = _identity.Username;
        _identityFile.Text = _identity.IdentityFile;

        Title = Strings.Get(_isNew ? "identity.new" : "common.edit");
        Width = 520;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _error.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
        _error.Visibility = Visibility.Collapsed;

        var buttons = Ui.Columns(8,
            Ui.Cancels(Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); })),
            Ui.Default(Ui.Accent(Strings.Get("common.save"), Save)));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0,
            Ui.Field("identity.name", _name),
            Ui.Field("server.username", _username),
            Ui.Field("server.authMethod", Ui.Picker(
                // No "ssh config alias" or "password" here: an identity that
                // was either would add nothing the server row does not already
                // carry, which is why MonitorService.Target ignores them.
                new[] { AuthKind.IdentityFile, AuthKind.Agent },
                _authKind,
                kind => kind == AuthKind.Agent
                    ? Strings.Get("auth.agent")
                    : Strings.Get("auth.identityFile"),
                kind =>
                {
                    _authKind = kind;
                    RefreshAuthFields();
                })),
            _authFields,
            _error,
            buttons);
        root.Margin = new Thickness(20);
        Content = root;
        RefreshAuthFields();
    }

    private void RefreshAuthFields()
    {
        _authFields.Children.Clear();
        if (_authKind != AuthKind.IdentityFile) return;
        _authFields.Children.Add(Ui.Field(
            "server.identityPath",
            Ui.Grid("*,auto", _identityFile, Ui.Button(Strings.Get("common.browse"), Browse)),
            "auth.identityHelp"));
    }

    private void Browse()
    {
        var dialog = new Microsoft.Win32.OpenFileDialog
        {
            Title = Strings.Get("server.privateKey"),
            InitialDirectory = SshConfig.SshDirectory,
            CheckFileExists = true,
        };
        if (dialog.ShowDialog(this) == true) _identityFile.Text = dialog.FileName;
    }

    private void Save()
    {
        var name = _name.Text.Trim();
        var username = _username.Text.Trim();
        if (name.Length == 0 || username.Length == 0)
        {
            _error.Text = Strings.Get("identity.required");
            _error.Visibility = Visibility.Visible;
            return;
        }
        if (_authKind == AuthKind.IdentityFile && _identityFile.Text.Trim().Length == 0)
        {
            _error.Text = Strings.Get("server.keyRequired");
            _error.Visibility = Visibility.Visible;
            return;
        }

        _identity.Name = name;
        _identity.Username = username;
        _identity.AuthKind = _authKind;
        _identity.IdentityFile = SshConfig.ExpandPath(_identityFile.Text.Trim());
        App.Current.Monitor.Save(_identity);
        DialogResult = true;
        Close();
    }
}
