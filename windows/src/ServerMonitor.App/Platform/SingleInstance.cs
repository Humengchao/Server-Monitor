using System.IO;
using System.IO.Pipes;
using Microsoft.Win32;

namespace ServerMonitor.App.Platform;

/// <summary>
/// One instance, with the second one activating the first.
/// </summary>
/// <remarks>
/// Free on macOS; on Windows it is a named mutex plus a pipe. The mutex alone
/// would make a second launch exit silently, which reads as the app being
/// broken — the user double-clicked and nothing happened, because the running
/// copy is a tray icon they did not notice. The pipe is what turns that into
/// "the window came forward".
///
/// The mutex name is also what the installer checks (see
/// <c>scripts/installer.iss</c>) to refuse installing over a running copy,
/// whose single-file exe is locked.
/// </remarks>
public sealed class SingleInstance : IDisposable
{
    public const string MutexName = "ServerMonitor.SingleInstance";
    private const string PipeName = "ServerMonitor.Activate";

    private readonly Mutex? _mutex;
    private CancellationTokenSource? _listener;

    public bool IsFirstInstance { get; }

    private SingleInstance(Mutex? mutex, bool first)
    {
        _mutex = mutex;
        IsFirstInstance = first;
    }

    public static SingleInstance Acquire()
    {
        // Local rather than Global: two users signed in at once each get their
        // own app, watching their own servers, and neither should block the
        // other. Global\ would also need an explicit ACL to be reachable
        // across sessions.
        var mutex = new Mutex(initiallyOwned: true, MutexName, out var created);
        if (created) return new SingleInstance(mutex, first: true);
        mutex.Dispose();
        return new SingleInstance(null, first: false);
    }

    /// <summary>
    /// Tells the running instance to show itself. Best effort: if it is busy
    /// or gone, this returns and the caller exits anyway.
    /// </summary>
    public static void SignalExistingInstance()
    {
        try
        {
            using var client = new NamedPipeClientStream(
                ".", PipeName, PipeDirection.Out, PipeOptions.None);
            client.Connect(2000);
            using var writer = new StreamWriter(client) { AutoFlush = true };
            writer.WriteLine("activate");
        }
        catch (Exception)
        {
            // The other instance may be mid-shutdown, having released the
            // mutex but not yet closed the pipe. Nothing useful to do: the
            // user can click the tray icon.
        }
    }

    /// <summary>
    /// Listens for a second launch and calls <paramref name="onActivate"/>.
    /// </summary>
    public void ListenForActivation(Action onActivate)
    {
        if (!IsFirstInstance) return;
        _listener = new CancellationTokenSource();
        var token = _listener.Token;
        _ = Task.Run(async () =>
        {
            while (!token.IsCancellationRequested)
            {
                try
                {
                    // A fresh server per connection. Reusing one across
                    // connections leaves it in a broken state after the client
                    // disconnects, and every later launch is then ignored.
                    using var server = new NamedPipeServerStream(
                        PipeName, PipeDirection.In, 1, PipeTransmissionMode.Byte, PipeOptions.Asynchronous);
                    await server.WaitForConnectionAsync(token).ConfigureAwait(false);
                    using var reader = new StreamReader(server);
                    var line = await reader.ReadLineAsync(token).ConfigureAwait(false);
                    if (line == "activate") onActivate();
                }
                catch (OperationCanceledException)
                {
                    return;
                }
                catch (IOException)
                {
                    // A client that connected and vanished. Loop round.
                }
            }
        }, token);
    }

    public void Dispose()
    {
        _listener?.Cancel();
        _listener?.Dispose();
        _mutex?.ReleaseMutex();
        _mutex?.Dispose();
    }
}

/// <summary>
/// "Start with Windows", as an HKCU Run value.
/// </summary>
/// <remarks>
/// The macOS build uses <c>SMAppService</c>; the registry key is the
/// equivalent for an unpackaged app. HKCU rather than HKLM so it needs no
/// elevation and applies to this user only, which is what the setting says.
///
/// The value matches what the installer's optional task writes, so ticking the
/// box in the installer and the one in Settings end up as one value rather
/// than two competing ones.
/// </remarks>
public static class StartupRegistration
{
    private const string KeyPath = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string ValueName = "ServerMonitor";

    public static bool IsEnabled
    {
        get
        {
            try
            {
                using var key = Registry.CurrentUser.OpenSubKey(KeyPath);
                return key?.GetValue(ValueName) is string value && value.Length > 0;
            }
            catch (Exception)
            {
                return false;
            }
        }
    }

    /// <summary>
    /// Turns it on or off. Returns false when the registry refused, so the UI
    /// can put the toggle back rather than showing a state that is not real.
    /// </summary>
    public static bool Set(bool enabled)
    {
        try
        {
            using var key = Registry.CurrentUser.CreateSubKey(KeyPath, writable: true);
            if (key is null) return false;
            if (enabled)
            {
                var executable = Environment.ProcessPath;
                if (string.IsNullOrEmpty(executable)) return false;
                // --minimised, or starting with Windows means a window in the
                // user's face at every sign-in, which is not what a background
                // monitor should do.
                key.SetValue(ValueName, $"\"{executable}\" --minimised");
            }
            else
            {
                key.DeleteValue(ValueName, throwOnMissingValue: false);
            }
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }
}
