# Server Monitor Agent

The Agent initiates authenticated HTTPS/WebSocket connections to the panel.
It opens no listening port and needs no SSH account, private key or password
configured in the panel. Its embedded SSH protocol carries exec commands,
interactive terminals and SFTP over separate outbound data connections.

Supported targets:

| System | Architectures | Terminal and service |
|---|---|---|
| Linux | amd64, arm64 | Unix PTY; systemd installer |
| Windows 10 1809 / Server 2019 or newer | amd64, arm64 | ConPTY; Windows Service Control Manager |

The service executes commands and accesses files with its own OS permissions.
The installers default to root on Linux and LocalSystem on Windows, matching
the panel's machine-management features. The Agent never invokes sudo itself.
Linux `--user` can choose an existing account. A Windows service account can be
changed through Services; grant it access to the config and log directory.
Operations requiring unavailable privileges report the host's permission error.
Linux-only host features, such as systemd or Linux container filesystem commands,
remain Linux-only; Windows uses the existing Windows collectors and service APIs.

Use the panel's expiring installation command for first enrollment. Tickets are
single-use; the saved Agent token and signing key are independent of the ticket.
For manual registration, avoid putting the ticket in process arguments:

```sh
server-monitor-agent enroll --server-url https://monitor.example.com \
  --ticket-stdin --config /etc/server-monitor-agent/config.json
server-monitor-agent run --config /etc/server-monitor-agent/config.json
```

`install.sh` accepts `install`, `reenroll`, `upgrade`, and `uninstall`.
`install.ps1` accepts the corresponding `-Action` values. Use **upgrade** to
preserve the saved identity; **reenroll** uses a new panel ticket and replaces
it. Failed Linux upgrades or re-enrollment restore the preceding executable,
unit and config and attempt to restart a previously running service. Failed
first installs remove the new unit and executable instead of leaving a broken
service enabled. These are local rollbacks: once the panel
accepts a new enrollment it revokes the old token: restoring old local files
does not reverse that revocation. Obtain another ticket if recovery needs a
fresh enrollment.

Both installers verify the downloaded binary's SHA-256 over the panel's HTTPS
connection before executing it. This checksum detects inconsistent downloads;
it is not a signature from a separate release authority. TLS certificate
verification remains enabled. Plain HTTP is accepted only for loopback tests.

Linux config: `/etc/server-monitor-agent/config.json` (0600).
Windows config: `%ProgramData%\ServerMonitorAgent\config.json` (restricted ACL).
The JSON contains the Agent token and private host key; treat it as a secret.
Normal logs contain neither token nor enrollment ticket. Windows service logs
are in the same directory as `agent.log`; Linux uses
`journalctl -u server-monitor-agent`.

Uninstall retains configuration unless `--purge-config` / `-PurgeConfig` is
supplied. Revoke the Agent in the panel as well to invalidate its token and
close its active connections. Losing a control connection closes that
generation's sessions and the Agent reconnects with jittered exponential
backoff. Ordinary command cancellation terminates its Linux process group or
Windows Job Object; commands that deliberately detach on Linux have the same
limits as detached commands over an ordinary SSH session.

The panel Hub is process-local. Deploy the panel as a single backend instance
unless a shared Agent routing layer is added; a load balancer independently
routing control/data sockets and ordinary API calls across replicas cannot
reach an Agent attached to another process.

Tests exercise authenticated HTTP enrollment, real SSH framing, PTY resize and
Ctrl+C, SFTP, revoked/renewed identities, independent deadlines, and service
shutdown. `install_test.sh` uses local curl/systemctl mocks and must only run
in an explicitly disposable Linux container. The PowerShell installer is not
run on the developer's real services by automated tests.

Implementation references: [Gorilla WebSocket](https://pkg.go.dev/github.com/gorilla/websocket),
[creack/pty](https://pkg.go.dev/github.com/creack/pty),
[pkg/sftp server](https://pkg.go.dev/github.com/pkg/sftp), and
[Microsoft ConPTY lifecycle](https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session).
