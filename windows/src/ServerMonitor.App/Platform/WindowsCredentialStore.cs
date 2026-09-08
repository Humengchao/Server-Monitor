using System.Runtime.InteropServices;
using System.Text;
using ServerMonitor.Core.Store;

namespace ServerMonitor.App.Platform;

/// <summary>
/// SSH passwords and key passphrases in the Windows credential manager (D4).
/// </summary>
/// <remarks>
/// The counterpart of the macOS build's keychain, and chosen for the same
/// reason plus one more: the user can see and delete these in Control Panel →
/// Credential Manager → Windows Credentials. A secret the app can write but
/// the user cannot find is worse than no secret store at all.
///
/// Generic credentials with <c>CRED_PERSIST_LOCAL_MACHINE</c>: readable
/// whenever this user is logged in, without a per-read prompt, because polling
/// happens in the background. They are DPAPI-protected under the user's
/// profile, so another user on the machine cannot read them.
///
/// Private keys deliberately never come here — they stay in
/// <c>%USERPROFILE%\.ssh</c> under OpenSSH's own ACLs.
/// </remarks>
public sealed class WindowsCredentialStore : ICredentialStore
{
    /// <summary>
    /// The target-name prefix. Matches the macOS service name so the two are
    /// recognisably the same app's entries.
    /// </summary>
    private const string Prefix = "com.hmchxd.ServerMonitor";

    private static string PasswordTarget(Guid serverId) => $"{Prefix}.password:{serverId}";

    /// <summary>
    /// Where a key's passphrase is stored.
    /// </summary>
    /// <remarks>
    /// Keyed by the key <em>path</em> rather than by server: one key is often
    /// shared by a whole fleet through an identity, and typing its phrase once
    /// should cover every host using it. <see cref="GetKeyPassphrase"/> still
    /// checks a server-specific entry first, so a per-host override is
    /// possible.
    /// </remarks>
    private static string PassphraseTarget(string keyPath) =>
        $"{Prefix}.passphrase:{keyPath.ToLowerInvariant()}";

    private static string PassphraseTarget(Guid serverId, string keyPath) =>
        $"{Prefix}.passphrase:{serverId}:{keyPath.ToLowerInvariant()}";

    public string? GetPassword(Guid serverId) => Read(PasswordTarget(serverId));

    public void SetPassword(Guid serverId, string password) =>
        Write(PasswordTarget(serverId), serverId.ToString(), password);

    public void DeletePassword(Guid serverId) => Delete(PasswordTarget(serverId));

    public string? GetKeyPassphrase(Guid serverId, string keyPath) =>
        Read(PassphraseTarget(serverId, keyPath)) ?? Read(PassphraseTarget(keyPath));

    public void SetKeyPassphrase(Guid serverId, string keyPath, string passphrase) =>
        // Written against the path, so it covers every server using this key.
        Write(PassphraseTarget(keyPath), keyPath, passphrase);

    // MARK: - CredRead / CredWrite / CredDelete

    private static string? Read(string target)
    {
        if (!CredRead(target, CRED_TYPE_GENERIC, 0, out var handle)) return null;
        try
        {
            var credential = Marshal.PtrToStructure<CREDENTIAL>(handle);
            if (credential.CredentialBlobSize == 0 || credential.CredentialBlob == IntPtr.Zero)
            {
                return null;
            }
            // The blob is UTF-16 without a terminator, and the size is in
            // bytes — halved for the character count, or a password comes back
            // with a trailing garbage character.
            return Marshal.PtrToStringUni(
                credential.CredentialBlob, (int)credential.CredentialBlobSize / 2);
        }
        finally
        {
            CredFree(handle);
        }
    }

    private static void Write(string target, string userName, string secret)
    {
        var blob = Encoding.Unicode.GetBytes(secret);
        var blobHandle = Marshal.AllocHGlobal(blob.Length);
        try
        {
            Marshal.Copy(blob, 0, blobHandle, blob.Length);
            var credential = new CREDENTIAL
            {
                Type = CRED_TYPE_GENERIC,
                TargetName = target,
                CredentialBlobSize = (uint)blob.Length,
                CredentialBlob = blobHandle,
                // Local machine rather than session: the app has to reach this
                // after a reboot, since it may start with Windows.
                Persist = CRED_PERSIST_LOCAL_MACHINE,
                UserName = userName,
                Comment = "Server Monitor SSH credential",
            };
            if (!CredWrite(ref credential, 0))
            {
                throw new InvalidOperationException(
                    $"Could not save the credential: {Marshal.GetLastWin32Error()}");
            }
        }
        finally
        {
            // Zeroed before it is freed: the secret was copied into unmanaged
            // memory that nothing else will clear.
            for (var i = 0; i < blob.Length; i++) Marshal.WriteByte(blobHandle, i, 0);
            Marshal.FreeHGlobal(blobHandle);
            Array.Clear(blob);
        }
    }

    private static void Delete(string target)
    {
        // A credential that is not there is the normal case — a server that
        // never had a password — so a false return is not an error.
        CredDelete(target, CRED_TYPE_GENERIC, 0);
    }

    private const uint CRED_TYPE_GENERIC = 1;
    private const uint CRED_PERSIST_LOCAL_MACHINE = 2;

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct CREDENTIAL
    {
        public uint Flags;
        public uint Type;
        [MarshalAs(UnmanagedType.LPWStr)] public string TargetName;
        [MarshalAs(UnmanagedType.LPWStr)] public string? Comment;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
        public uint CredentialBlobSize;
        public IntPtr CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public IntPtr Attributes;
        [MarshalAs(UnmanagedType.LPWStr)] public string? TargetAlias;
        [MarshalAs(UnmanagedType.LPWStr)] public string? UserName;
    }

    // DllImport rather than LibraryImport: the source generator cannot marshal
    // CREDENTIAL (it holds a FILETIME and raw pointers), and asking it to
    // wants /unsafe for the whole assembly. These four calls happen when the
    // user saves or a host connects, not in a loop, so the marshalling
    // stub's cost is irrelevant.
    [DllImport("advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredRead(string target, uint type, uint reservedFlag, out IntPtr credential);

    [DllImport("advapi32.dll", EntryPoint = "CredWriteW", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredWrite(ref CREDENTIAL credential, uint flags);

    [DllImport("advapi32.dll", EntryPoint = "CredDeleteW", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredDelete(string target, uint type, uint flags);

    [DllImport("advapi32.dll", EntryPoint = "CredFree")]
    private static extern void CredFree(IntPtr buffer);
}
