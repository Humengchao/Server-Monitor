using System.Text;
using Microsoft.Terminal.Wpf;
using Renci.SshNet;

namespace ServerMonitor.App.Terminal;

/// <summary>
/// An interactive shell on a host, shaped as the terminal control's
/// connection.
/// </summary>
/// <remarks>
/// D5's adapter: SSH.NET's <see cref="ShellStream"/> is a byte stream and
/// <see cref="ITerminalConnection"/> wants strings in and strings out, so this
/// sits between them and does three jobs the naive version gets wrong.
///
/// It decodes incrementally. A <see cref="Decoder"/> keeps the state, because
/// a read can end halfway through a multi-byte character — which for a Chinese
/// locale's shell is not an edge case but the common one, and a fresh
/// <c>Encoding.UTF8.GetString</c> per read turns it into a replacement
/// character every few hundred bytes.
///
/// It reads on its own thread rather than off <c>DataReceived</c>. The event
/// exists, but a full-screen program (vim, htop) produces a burst per frame,
/// and a blocking read into a reused buffer is both simpler to reason about
/// and cheaper than an event per packet.
///
/// It marshals to the UI thread. The control writes what it is handed straight
/// into the native renderer, which belongs to the thread that created it.
/// </remarks>
internal sealed class ShellStreamConnection : ITerminalConnection, IDisposable
{
    private readonly ShellStream _stream;
    private readonly Action<Action> _dispatch;
    private readonly Action<string>? _log;
    private readonly CancellationTokenSource _stopping = new();
    private readonly Decoder _decoder = new UTF8Encoding(false).GetDecoder();

    private Thread? _reader;
    private bool _closed;

    /// <summary>SGR red, and back to the default. Written out because a raw
    /// escape character in source is invisible to everything that reads it.
    /// </summary>
    private const string Red = "\u001b[31m";
    private const string Reset = "\u001b[0m";

    /// <summary>Raised when the far side sends something, or hangs up.</summary>
    public event EventHandler<TerminalOutputEventArgs>? TerminalOutput;

    /// <summary>Raised once, when the shell ends.</summary>
    public event Action? Ended;

    public ShellStreamConnection(ShellStream stream, Action<Action> dispatch, Action<string>? log = null)
    {
        _stream = stream;
        _dispatch = dispatch;
        _log = log;
    }

    public void Start()
    {
        if (_reader is not null) return;
        _reader = new Thread(ReadLoop)
        {
            IsBackground = true,
            Name = "ssh-shell",
        };
        _reader.Start();
    }

    private void ReadLoop()
    {
        var bytes = new byte[16 * 1024];
        var characters = new char[bytes.Length + 1];
        try
        {
            while (!_stopping.IsCancellationRequested)
            {
                var read = _stream.Read(bytes, 0, bytes.Length);
                if (read <= 0) break;
                var decoded = _decoder.GetChars(bytes, 0, read, characters, 0);
                if (decoded == 0) continue;
                var text = new string(characters, 0, decoded);
                Emit(text);
            }
        }
        catch (Exception error) when (!_stopping.IsCancellationRequested)
        {
            _log?.Invoke($"terminal: {error.Message}");
            // Shown in the terminal itself rather than a dialog: the session is
            // where the user is looking, and a modal over a disconnected shell
            // just hides what it last printed.
            Emit($"\r\n{Red}{error.Message}{Reset}\r\n");
        }
        finally
        {
            Finish();
        }
    }

    private void Emit(string text) =>
        _dispatch(() => TerminalOutput?.Invoke(this, new TerminalOutputEventArgs(text)));

    private void Finish() => _dispatch(() =>
    {
        if (_closed) return;
        _closed = true;
        Ended?.Invoke();
    });

    public void WriteInput(string data)
    {
        if (data.Length == 0 || _stopping.IsCancellationRequested) return;
        try
        {
            _stream.Write(data);
            _stream.Flush();
        }
        catch (Exception error)
        {
            _log?.Invoke($"terminal write: {error.Message}");
        }
    }

    /// <summary>
    /// Tells the far side the window changed size.
    /// </summary>
    /// <remarks>
    /// Columns and rows are the arguments that matter; the pixel pair is
    /// what <c>SIGWINCH</c> carries for programs that ask, and zero is what
    /// every terminal that does not know sends.
    /// </remarks>
    public void Resize(uint rows, uint columns)
    {
        if (rows == 0 || columns == 0 || _stopping.IsCancellationRequested) return;
        try
        {
            _stream.ChangeWindowSize(columns, rows, 0, 0);
        }
        catch (Exception error)
        {
            _log?.Invoke($"terminal resize: {error.Message}");
        }
    }

    public void Close()
    {
        if (_stopping.IsCancellationRequested) return;
        _stopping.Cancel();
        try
        {
            _stream.Close();
            _stream.Dispose();
        }
        catch (Exception)
        {
            // The usual reason for closing is that it is already gone.
        }
        Finish();
    }

    public void Dispose()
    {
        Close();
        _stopping.Dispose();
    }
}
