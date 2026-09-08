using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using ServerMonitor.App.Controls;
using ServerMonitor.Core;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// The remote filesystem of one host.
/// </summary>
/// <remarks>
/// One pane, not two. The local side is the system's own file dialogs, which
/// already know about drives, network paths, OneDrive and the places list — a
/// hand-built local pane would be a worse Explorer sitting next to the real
/// one. The macOS build made the same call.
/// </remarks>
public sealed class SftpWindow : Window
{
    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    private readonly Server _server;
    private readonly SftpBrowser _browser;
    private readonly TextBox _path = Ui.Input();
    private readonly ListView _list;
    private readonly TextBlock _message = Ui.Caption("");
    private readonly TextBlock _hint = Ui.Tertiary("");
    private readonly ProgressBar _progress = new()
    {
        Height = 3,
        Minimum = 0,
        Maximum = 1,
        Visibility = Visibility.Collapsed,
    };

    private readonly List<string> _back = [];
    private string _directory = "/";
    private bool _showHidden;
    private bool _busy;
    private SessionRecord? _session;
    private CancellationTokenSource? _transfer;

    /// <summary>One row of the listing.</summary>
    private sealed record Row(SftpEntry Entry)
    {
        public string Name => Entry.IsDirectory ? Entry.Name + "/" : Entry.Name;
        public string Size => Entry.IsDirectory ? "" : Format.Bytes(Entry.Size);
        public string Modified => Entry.Modified.ToString(
            "yyyy-MM-dd HH:mm", System.Globalization.CultureInfo.InvariantCulture);
        public string Mode => Entry.Permissions;
    }

    public SftpWindow(Server server)
    {
        _server = server;
        _browser = new SftpBrowser((target, token) =>
            App.Current.LibraryTransport is { } transport
                ? transport.LeaseSftpAsync(target, token)
                : throw new InvalidOperationException(
                    Strings.IsChinese
                        ? "内置 SSH 传输不可用。"
                        : "The built-in SSH transport is unavailable."));

        Title = $"SFTP — {server.Name}";
        Width = 900;
        Height = 620;
        MinWidth = 520;
        MinHeight = 320;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _list = BuildList();
        _path.KeyDown += (_, e) =>
        {
            if (e.Key != Key.Enter) return;
            _ = RunAsync(() => GoAsync(SftpPath.Normalise(_path.Text.Trim())));
        };

        var toolbar = Ui.Columns(6,
            Ui.Quiet(Strings.Get("sftp.back"), Back),
            Ui.Quiet(Strings.Get("sftp.up"), () => _ = RunAsync(() => GoAsync(SftpPath.Parent(_directory)))),
            Ui.Quiet(Strings.Get("common.refresh"), () => _ = RunAsync(ReloadAsync)));
        toolbar.VerticalAlignment = VerticalAlignment.Center;

        var actions = Ui.Columns(6,
            Ui.Quiet(Strings.Get("sftp.newFolder"), NewFolder),
            Ui.Quiet(Strings.Get("sftp.upload"), Upload),
            Ui.Quiet(Strings.Get("sftp.download"), Download),
            Ui.Quiet(Strings.Get("sftp.rename"), Rename),
            Ui.Danger(Strings.Get("common.delete"), Delete));
        actions.VerticalAlignment = VerticalAlignment.Center;

        var hidden = Ui.Toggle(
            Strings.IsChinese ? "显示隐藏文件" : "Show hidden files",
            _showHidden,
            value =>
            {
                _showHidden = value;
                _ = RunAsync(ReloadAsync);
            });
        hidden.VerticalAlignment = VerticalAlignment.Center;

        var top = Ui.Rows(8,
            Ui.Grid("auto,*", toolbar, _path),
            Ui.Grid("*,auto", hidden, actions));
        top.Margin = new Thickness(14, 12, 14, 10);

        var bottom = Ui.Rows(4, _progress, Ui.Grid("*,auto", _message, _hint));
        bottom.Margin = new Thickness(14, 8, 14, 12);

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        Grid.SetRow(top, 0);
        Grid.SetRow(_list, 1);
        Grid.SetRow(bottom, 2);
        root.Children.Add(top);
        root.Children.Add(_list);
        root.Children.Add(bottom);
        _list.Margin = new Thickness(14, 0, 14, 0);
        Content = root;

        // Dropping files on the listing uploads them into the directory being
        // shown. The gesture people try first, and the reason the local side
        // of this window is Explorer rather than a second pane.
        AllowDrop = true;
        DragOver += OnDragOver;
        Drop += OnDrop;
        _hint.Text = Strings.IsChinese
            ? "把文件拖进来即可上传到当前目录（文件夹暂不支持）"
            : "Drop files here to upload into this directory (folders are not taken)";

        Loaded += (_, _) => _ = StartAsync();
        Closed += (_, _) =>
        {
            _transfer?.Cancel();
            EndSession();
        };
    }

    private ListView BuildList()
    {
        var list = new ListView
        {
            Style = (Style)FindResource("Table"),
            SelectionMode = SelectionMode.Extended,
        };

        var view = new GridView { AllowsColumnReorder = false };
        void Column(string headerKey, string path, double width) =>
            view.Columns.Add(new GridViewColumn
            {
                Header = Strings.Get(headerKey),
                DisplayMemberBinding = new System.Windows.Data.Binding(path),
                Width = width,
            });

        Column("sftp.name", nameof(Row.Name), 340);
        Column("sftp.size", nameof(Row.Size), 100);
        Column("sftp.modified", nameof(Row.Modified), 150);
        Column("sftp.mode", nameof(Row.Mode), 110);
        list.View = view;

        list.MouseDoubleClick += (_, _) =>
        {
            if (Selected().FirstOrDefault() is not { } entry) return;
            if (entry.IsDirectory) _ = RunAsync(() => GoAsync(entry.Path));
            else Download();
        };
        list.KeyDown += (_, e) =>
        {
            switch (e.Key)
            {
                case Key.Enter when Selected().FirstOrDefault() is { IsDirectory: true } directory:
                    _ = RunAsync(() => GoAsync(directory.Path));
                    break;
                case Key.Back:
                    _ = RunAsync(() => GoAsync(SftpPath.Parent(_directory)));
                    break;
                case Key.Delete:
                    Delete();
                    break;
            }
        };
        return list;
    }

    private List<SftpEntry> Selected() =>
        [.. _list.SelectedItems.OfType<Row>().Select(row => row.Entry)];

    // MARK: - Navigation

    private Task StartAsync()
    {
        Say(Strings.Get("terminal.connect"));
        return RunAsync(async () =>
        {
            var home = await _browser.HomeAsync(Monitor.Target(_server)).ConfigureAwait(true);
            StartSession();
            await GoAsync(home).ConfigureAwait(true);
        });
    }

    private void Back()
    {
        if (_back.Count == 0) return;
        var previous = _back[^1];
        _back.RemoveAt(_back.Count - 1);
        // Popped before navigating, and GoAsync is told not to push it back.
        _ = RunAsync(() => GoAsync(previous, remember: false));
    }

    private async Task GoAsync(string path, bool remember = true)
    {
        var destination = SftpPath.Normalise(path.Length == 0 ? "/" : path);
        if (remember && destination != _directory) _back.Add(_directory);
        _directory = destination;
        _path.Text = destination;
        await ReloadAsync().ConfigureAwait(true);
    }

    /// <summary>
    /// Re-lists the current directory.
    /// </summary>
    /// <remarks>
    /// No in-flight guard of its own, deliberately: every action ends by
    /// refreshing, and a guard here would be held by the action that is
    /// calling it — so the listing would silently never update after a
    /// delete, an upload or a rename.
    /// </remarks>
    private async Task ReloadAsync()
    {
        Say(Strings.Get("common.refresh"));
        var entries = await _browser
            .ListAsync(Monitor.Target(_server), _directory, _showHidden)
            .ConfigureAwait(true);
        _list.ItemsSource = entries.Select(entry => new Row(entry)).ToList();
        Say(entries.Count == 0
            ? Strings.Get("sftp.emptyDir")
            : $"{entries.Count} — {_directory}");
    }

    // MARK: - Actions

    private void NewFolder()
    {
        var window = new TextPromptWindow(Strings.Get("sftp.newFolder"), "") { Owner = this };
        if (window.ShowDialog() != true) return;
        var name = window.Value;
        if (!SftpPath.IsValidName(name))
        {
            Ui.Complain(this, Strings.Get("server.nameRequired"));
            return;
        }
        _ = RunAsync(async () =>
        {
            await _browser
                .CreateDirectoryAsync(Monitor.Target(_server), SftpPath.Combine(_directory, name))
                .ConfigureAwait(true);
            await ReloadAsync().ConfigureAwait(true);
        });
    }

    private void Rename()
    {
        if (Selected().FirstOrDefault() is not { } entry) return;
        var window = new TextPromptWindow(Strings.Get("sftp.rename"), entry.Name) { Owner = this };
        if (window.ShowDialog() != true) return;
        var name = window.Value;
        if (!SftpPath.IsValidName(name) || name == entry.Name) return;
        _ = RunAsync(async () =>
        {
            await _browser
                .RenameAsync(
                    Monitor.Target(_server), entry.Path, SftpPath.Combine(_directory, name))
                .ConfigureAwait(true);
            await ReloadAsync().ConfigureAwait(true);
        });
    }

    private void Delete()
    {
        var chosen = Selected();
        if (chosen.Count == 0) return;

        var message = chosen.Count == 1
            ? Strings.Get("sftp.deleteConfirm", chosen[0].Name)
            : Strings.Get(
                "sftp.deleteSelectedConfirm",
                chosen.Count.ToString(System.Globalization.CultureInfo.InvariantCulture));
        if (!Ui.Confirm(this, message)) return;

        _ = RunAsync(async () =>
        {
            foreach (var entry in chosen)
            {
                await _browser.DeleteAsync(Monitor.Target(_server), entry.Path).ConfigureAwait(true);
            }
            await ReloadAsync().ConfigureAwait(true);
        });
    }

    private void OnDragOver(object sender, DragEventArgs e)
    {
        e.Effects = Droppable(e) is { Count: > 0 } ? DragDropEffects.Copy : DragDropEffects.None;
        e.Handled = true;
    }

    private void OnDrop(object sender, DragEventArgs e)
    {
        e.Handled = true;
        if (Droppable(e) is not { Count: > 0 } files) return;
        _ = UploadAsync(files);
    }

    /// <summary>
    /// The files in a drop that this window can actually send.
    /// </summary>
    /// <remarks>
    /// Directories are dropped as often as files and SFTP has no recursive
    /// put, so they are filtered out rather than half-handled: uploading a
    /// folder tree is a feature, and silently uploading nothing when someone
    /// drags one would look like a bug. The message says how many were taken.
    /// </remarks>
    private static List<string>? Droppable(DragEventArgs e)
    {
        if (!e.Data.GetDataPresent(DataFormats.FileDrop)) return null;
        if (e.Data.GetData(DataFormats.FileDrop) is not string[] paths) return null;
        return [.. paths.Where(System.IO.File.Exists)];
    }

    private void Upload()
    {
        var dialog = new Microsoft.Win32.OpenFileDialog
        {
            Title = Strings.Get("sftp.upload"),
            Multiselect = true,
            CheckFileExists = true,
        };
        if (dialog.ShowDialog(this) != true) return;
        _ = UploadAsync([.. dialog.FileNames]);
    }

    private Task UploadAsync(List<string> files) =>
        RunAsync(async () =>
        {
            _transfer = new CancellationTokenSource();
            try
            {
                foreach (var file in files)
                {
                    var remote = SftpPath.Combine(_directory, System.IO.Path.GetFileName(file));
                    await _browser.UploadAsync(
                        Monitor.Target(_server),
                        file,
                        remote,
                        Report(Strings.Get("sftp.uploading")),
                        _transfer.Token).ConfigureAwait(true);
                }
                await ReloadAsync().ConfigureAwait(true);
            }
            finally
            {
                _transfer?.Dispose();
                _transfer = null;
                _progress.Visibility = Visibility.Collapsed;
            }
        });

    /// <summary>
    /// Copies the selection to a local folder.
    /// </summary>
    /// <remarks>
    /// One file goes through a save dialog, so it can be renamed on the way —
    /// which matters here more than on other platforms, because a perfectly
    /// ordinary Linux filename can be one Windows refuses (see
    /// <see cref="SftpPath.LocalNameFor"/>). Several files go to a folder the
    /// user picks, each with the same substitution applied.
    /// </remarks>
    private void Download()
    {
        var chosen = Selected().Where(entry => !entry.IsDirectory).ToList();
        if (chosen.Count == 0) return;

        if (chosen.Count == 1)
        {
            var dialog = new Microsoft.Win32.SaveFileDialog
            {
                Title = Strings.Get("sftp.download"),
                FileName = SftpPath.LocalNameFor(chosen[0].Name),
            };
            if (dialog.ShowDialog(this) != true) return;
            _ = TransferDownAsync([(chosen[0], dialog.FileName)]);
            return;
        }

        var folder = new Microsoft.Win32.OpenFolderDialog
        {
            Title = Strings.Get("sftp.downloadSelected"),
        };
        if (folder.ShowDialog(this) != true) return;
        _ = TransferDownAsync([.. chosen.Select(entry => (
            entry,
            System.IO.Path.Combine(folder.FolderName, SftpPath.LocalNameFor(entry.Name))))]);
    }

    private Task TransferDownAsync(List<(SftpEntry Entry, string Local)> transfers) =>
        RunAsync(async () =>
        {
            _transfer = new CancellationTokenSource();
            try
            {
                foreach (var (entry, local) in transfers)
                {
                    await _browser.DownloadAsync(
                        Monitor.Target(_server),
                        entry.Path,
                        local,
                        Report(Strings.Get("sftp.downloading")),
                        _transfer.Token).ConfigureAwait(true);
                }
                Say(Strings.Get("keys.copied"));
            }
            finally
            {
                _transfer?.Dispose();
                _transfer = null;
                _progress.Visibility = Visibility.Collapsed;
            }
        });

    /// <summary>
    /// A progress sink that updates the bar without flooding the dispatcher.
    /// </summary>
    /// <remarks>
    /// SSH.NET reports per chunk, which for a large file is thousands of
    /// callbacks a second; a dispatcher post each would starve the UI thread
    /// it is trying to keep informed. One in every hundred is still smoother
    /// than the eye can follow.
    /// </remarks>
    private IProgress<TransferProgress> Report(string verb)
    {
        var seen = 0;
        _progress.Visibility = Visibility.Visible;
        return new Progress<TransferProgress>(progress =>
        {
            if (++seen % 100 != 0 && progress.Fraction < 1) return;
            _progress.Value = progress.Fraction;
            Say($"{verb} {progress.Name} — {Format.Percent(progress.Fraction * 100)}");
        });
    }

    private async Task RunAsync(Func<Task> work)
    {
        if (_busy) return;
        _busy = true;
        try
        {
            await work().ConfigureAwait(true);
        }
        catch (OperationCanceledException)
        {
            Say(Strings.Get("common.cancel"));
        }
        catch (Exception error)
        {
            Fail(error);
        }
        finally
        {
            _busy = false;
        }
    }

    private void Say(string text)
    {
        _message.Text = text;
        _message.Foreground = Theme.Ink.Brush(Theme.Palette.Secondary);
    }

    private void Fail(Exception error)
    {
        _message.Text = error.Message;
        _message.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
    }

    // MARK: - Session history

    private void StartSession()
    {
        if (_session is not null) return;
        _session = new SessionRecord
        {
            ServerId = _server.Id,
            ServerName = _server.Name,
            Kind = SessionKind.Sftp,
        };
        Monitor.Database.Save(_session);
    }

    private void EndSession()
    {
        if (_session is not { } session) return;
        session.EndedAt = DateTime.UtcNow;
        Monitor.Database.Save(session);
        _session = null;
    }
}

/// <summary>Asks for one line of text.</summary>
public sealed class TextPromptWindow : Window
{
    private readonly TextBox _input;

    public string Value => _input.Text.Trim();

    public TextPromptWindow(string prompt, string initial)
    {
        _input = Ui.Input(initial);

        Title = prompt;
        Width = 420;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        var buttons = Ui.Columns(8,
            Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); }),
            Ui.Accent(Strings.Get("common.save"), Accept));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        _input.KeyDown += (_, e) =>
        {
            if (e.Key == Key.Enter) Accept();
        };

        var root = Ui.Rows(8, Ui.Wrapped(prompt, "Text.Secondary"), _input, buttons);
        root.Margin = new Thickness(20);
        Content = root;
        Loaded += (_, _) =>
        {
            _input.Focus();
            _input.SelectAll();
        };
    }

    private void Accept()
    {
        if (Value.Length == 0) return;
        DialogResult = true;
        Close();
    }
}
