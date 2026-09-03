# Server Monitor for macOS

A native macOS app that monitors Linux servers over SSH. **Fully local**: it
talks straight to your hosts, stores everything on this Mac, and needs no
backend, no database server and no account.

Not related to the `backend/` + `frontend/` web deployment in this repo other
than sharing its collection logic — this app is standalone.

```
┌── SwiftUI (sidebar · charts · terminal · SFTP)
├── MonitorService      polling loop, all UI state (main actor)
├── SessionManager      open terminal/SFTP sessions + history
├── AlertService        offline/threshold notifications, debounced
├── MetricsCollector    one SSH round trip -> parsed snapshot (actor)
├── SSHRunner           system /usr/bin/ssh + ControlMaster multiplexing
├── SFTPClient          remote browsing (ls) and transfer (scp)
└── Database            SQLite via GRDB: servers, snippets, identities,
                        metric history, session history
```

**Connections go through the system OpenSSH client, not an in-process SSH
library.** That is a deliberate choice, not a shortcut: modern servers disable
the SHA-1 `ssh-rsa` signature algorithm and require `rsa-sha2-256/512`, which
the pure-Swift SSH libraries do not implement — an RSA key that works in
Terminal simply fails in-process. Shelling out also inherits everything already
configured in `~/.ssh`: config aliases, the agent, `known_hosts`, `ProxyJump`
and per-host options. Connection reuse comes from OpenSSH's own
`ControlMaster`/`ControlPersist`, so a poll costs one round trip rather than a
full handshake.

## What it does

- **Live metrics** over SSH: CPU, memory, disk usage, load, network and disk
  I/O rates, uptime, latency. One batched command per poll, not one per metric.
- **Honest latency.** ICMP, sent over the primary physical interface
  (`ping -b en0`). The binding matters: when a VPN or proxy owns the route, an
  unbound ping never leaves the machine — the tunnel answers it locally and a
  server on another continent appears to reply in under a millisecond. A TCP
  handshake is no better, since the tunnel accepts the connection locally too.
  Hosts that filter ICMP fall back to a second method: the host timestamps the
  start and end of each collection run, so its own work can be subtracted from
  the locally measured elapsed time. That figure always works but reads higher,
  as it carries the local `ssh` launch and the proxy hop.
- **History and charts** — Swift Charts over 15 min / 1 h / 6 h / 24 h, kept in
  a local SQLite file and pruned on a schedule.
- **SSH terminal** — a real PTY running `ssh`, so `sudo`, `vim`, job control,
  colours and resizing behave exactly as they do in Terminal.app.
- **SFTP browser** — navigate, upload, download, rename, delete, make folders,
  with multi-select and a real progress bar (sampled from the destination's
  growing size, since scp only draws its own bar on a TTY).
- **Docker** — an overview card per engine (version, images, running, stopped)
  from a single `docker info` call, drilling into the container list where you
  can start/stop/restart, read logs, and open a shell inside a container.
- **Snippets** — save commands and run them on any host with output inline, or
  type one straight into an open terminal without submitting it.
- **Identities** — one login shared by many machines instead of restating it.
- **Machine groups** — colour-coded groups; deleting a group keeps its machines
  and simply ungroups them.
- **SSH keys** — inventory of `~/.ssh`: type, size, fingerprint, whether a
  passphrase is set, and one-click copy of the public key. Generate new keys
  (Ed25519 or RSA-4096, with an optional passphrase) or import them from the
  clipboard or a file. Everything is written into `~/.ssh` at `0600`, so a key
  made here works in Terminal and every other tool immediately.
- **Sessions** — open terminal/SFTP sessions live in the sidebar; closed ones
  land in a searchable history.
- **Menu bar presence** — the app keeps polling with its window closed; the
  icon carries the busiest host's CPU, or a count when something is down, and
  its panel lists every host at a glance.
- **Alerts** — desktop notifications when a host goes offline or recovers, and
  on sustained CPU / memory / disk thresholds, globally or per server. An alert needs three consecutive
  breaching polls and then goes quiet for 15 minutes, so a build spike or a
  flapping host does not produce a notification storm.
- **Launch at login** via `SMAppService`, for genuinely always-on collection.
- **Import from `~/.ssh/config`** — adopt existing hosts in one pass.
- **Windows hosts** alongside Linux. Collection auto-detects with a single
  `uname -s`, then remembers the answer so later polls go straight to the right
  script. (Speculatively running the `/proc` batch first is worse than it
  sounds: it contains `sleep` and several `cat`s, and a Windows shell does not
  fail it quickly — the poll just hangs until the timeout.) The PowerShell path
  uses CIM classes rather than `Get-Counter`, whose counter *names* are
  localised, and is passed as UTF-16LE base64 via `-EncodedCommand` so neither
  cmd.exe nor PowerShell quoting can mangle it. Its output is CRLF, which is
  its own trap in Swift — see `Lines.swift`.
- **A machine screen of status cards** — overall CPU with every logical core
  beside it, memory split into used/buffers/cache with swap underneath, each
  mount with its own bar, per-interface traffic, the top processes with a
  filter, and the machine's own identity (OS, kernel, arch, addresses). Almost
  all of it comes out of output the poll already fetched.
- **Terminal font** — six monospaced faces and six sizes, with a live preview.
- **Bilingual** (中文 / English) and follows the system appearance.
- **One publish per poll tick.** Hosts finish a tick about a second apart, so
  results are held until the last poll the tick launched returns (capped at
  2 s), then applied as one change: one dashboard pass per tick instead of the
  3.4 measured before. A host with nothing on screen yet is shown at once.
  Nothing is published while every window is hidden or covered; the menu bar
  and the database keep going regardless.
- **Backs off from hosts that keep failing** — geometrically, capped at five
  minutes, cleared by any answer, by editing the host, and by the network
  coming back (`NWPathMonitor`), since nine hosts failing at once is almost
  always this machine's link. A manual refresh always means "try now".
- **Keyboard.** A Go menu (⌘1…⌘7) for the sidebar; ⌘N for "new whatever this
  screen is about", ⇧⌘N new group, ⇧⌘I import, ⌘T / ⇧⌘T terminal or SFTP on
  the selected machine, ⌘R refresh. The window reopens where it was left.

## Requirements

- macOS 15 or later (Citadel's interactive PTY API is 15+).
- Command Line Tools for Xcode. Full Xcode is *not* required.

## Build

```bash
cd macos
swift build            # compile
swift test             # 305 tests, fully offline
./scripts/bundle.sh    # -> dist/Server Monitor.app
MAKE_DMG=1 ./scripts/bundle.sh   # also produce a .dmg

# Render-cost benchmarks (cold ImageRenderer frames, plus an offscreen
# NSHostingView resized 2pt at a time — the closest thing to a live drag
# without a window server). Prints ms/frame; nothing is asserted.
SM_BENCH=1 swift test --filter RenderBenchmarkTests

# Opt-in end-to-end checks against real hosts:
SM_LIVE_ALIAS=myhost swift test                                   # Linux, via ~/.ssh/config
SM_WIN_HOST=… SM_WIN_USER=… SM_WIN_PASSWORD=… swift test          # Windows, password auth
```

That last line puts a password in the environment, and SwiftPM records the
environment of a build in `.build/plugins/cache/*-state.json` — one file per
build plugin, so run `rm .build/plugins/cache/*-state.json` afterwards rather
than leaving the password sitting in the build directory. Those files are
ignored by git, but they are still plaintext on disk.

With full Xcode installed but `xcode-select` still pointing at the Command Line
Tools, prefix builds with
`DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer`.

`scripts/bundle.sh` assembles the `.app` by hand — SwiftPM cannot emit one and
`xcodebuild` needs full Xcode. The icon is rasterised from `Resources/AppIcon.svg`
with headless Chrome and packed by `iconutil`, because `actool` also ships only
with Xcode. If Chrome is missing the app builds without an icon.

### Continuous builds

`.github/workflows/macos-app.yml` builds, tests and packages the app on every
push to `main` that touches `macos/`. The `.zip` and `.dmg` are attached to the
run as an artifact (kept 30 days) — Actions tab → the run → *Artifacts*. Pushing
a tag `v1.2.3` additionally publishes a GitHub Release with both files.

The app is ad-hoc signed — there is no Developer ID, so no notarisation — and
Gatekeeper will refuse a copy that arrived through a browser. Either right-click
it, choose *Open*, and allow it under *System Settings › Privacy & Security*, or
clear the quarantine flag once:

```bash
xattr -dr com.apple.quarantine "Server Monitor.app"
```

Builds are versioned `<tag>` on a tag push and `0.1.0-dev+<short sha>` otherwise,
so a download from the Actions tab still says which commit it is.

## Security notes

- **Private keys are never copied.** They stay in `~/.ssh` under OpenSSH's own
  permissions; the database stores only *which* key file or config alias a
  server uses.
- **Passwords**, which have nowhere else to live, go to the login Keychain and
  reach ssh through an `SSH_ASKPASS` helper: a script in a `0700` directory that
  reads a `0600` file, both removed once the connection is up. The password
  never appears in an argument list, so it cannot be read out of `ps`, and it is
  needed only for the first connection because ControlMaster keeps that one
  open.
- **Host keys are verified by OpenSSH** against the user's real `known_hosts`,
  so hosts already trusted in Terminal stay trusted. Polling forces
  `StrictHostKeyChecking=accept-new`: an unknown host is recorded on first
  contact and a *changed* key is still refused. The default `ask` cannot work
  here — with `SSH_ASKPASS_REQUIRE=force` ssh routes the fingerprint question
  to the askpass helper, which answers with the password, and the connection
  hangs instead of asking anyone.
- Remote paths and container ids are shell-quoted before they reach a command;
  they are filesystem data, not trusted input.
- The build is **ad-hoc signed**, which is enough to run locally. Distributing
  it to anyone else needs a Developer ID signature plus notarisation.
- Metric polling runs `ssh` with `BatchMode=yes` so it can never block on a
  prompt; the interactive terminal deliberately drops that so it *can* ask for a
  passphrase or host-key confirmation.

## Known gaps

- Raw samples only — the server build's 1m/15m rollups are unnecessary for a
  single-user store with a bounded retention window.
- The terminal font is a global preference, not per session.
- The ICMP fallback (used only where ping is filtered) reads high: it includes
  the local `ssh` process launch and, where a proxy carries the route, the proxy
  hop.
- Latency is sampled every 30 seconds rather than every poll, since it moves
  slowly and each sample costs a `ping` process.
- Open terminal and SFTP sessions do not survive a relaunch (their
  connections die with the process); the sidebar selection does.
- The vnStat install button's install step has only been exercised as far as
  its read-only probe against real hosts; the install itself puts software on
  someone's server and has not been run unattended.
- The GPU card is tested against captured `nvidia-smi` output only — no NVIDIA
  host was available.

## Layout

```
Sources/ServerMonitorKit/
  Model/      Server, MetricSample, MetricSnapshot, Snippet, Identity,
              MachineGroup, SessionRecord
  Store/      Database (GRDB), AppSettings, LoginItem, Keychain (passwords)
  SSH/        SSHRunner, SSHTarget, SFTPClient, DockerClient,
              SSHConfigImporter, SSHKeyScanner, SSHKeyManager
  Collect/    ProcParsers and WindowsMetrics (both ported from the Go
              collector), PingProbe, MetricsCollector, MonitorService
  Session/    SessionManager
  Alerts/     AlertService (delivery injected, so the rules are testable)
  Terminal/   SwiftTerm bridge over a PTY running ssh
  View/       RootView, DashboardView, ServerDetailView, ServerEditorView,
              MachinesView, SnippetsView, IdentitiesView, SSHKeysView,
              SFTPView, SessionHistoryView, DockerPane, TerminalPane,
              SettingsView, MenuBarPanel, Components/
Sources/ServerMonitor/    @main app entry
Tests/                    109 tests: parsers, database, SFTP listing, key
                          scanning and naming, Docker summaries, groups,
                          sessions, alert debouncing, plus an opt-in live SSH
                          check
```
