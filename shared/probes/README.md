# Shared probes

The commands and scripts sent to monitored hosts, as one source for all three
collectors:

| Consumer | How it reads these |
|---|---|
| `windows/src/ServerMonitor.Core` | `EmbeddedResource`, read by `Probes.cs` |
| `web/backend` (Go) | Generated `probes_generated.go`, compiled into the binary |
| `macos/Sources/ServerMonitorKit` (Swift) | Generated `SharedProbes.generated.swift`, compiled into the app |

Edit the files here, then run `node shared/scripts/generate-probes.mjs` from
the repository root. Commit both generated files with the source change.
All three CI workflows run `node shared/scripts/generate-probes.mjs --check`
and reject stale generated copies. Node is needed only when updating probes;
ordinary Go/Swift builds and the backend Docker context remain self-contained.

All three Windows collectors now use the same two-sample PerfRawData script,
UTF-8 output, suppressed progress stream, and raw-DEFLATE delivery. The additive
`dockerpresent` key distinguishes installation from an unavailable daemon.
The native Linux clients consume all 15 sections; Go selects the common core
sections and the explicitly named web root-disk/Docker-presence variants in
`probes.txt`. Section order is validated by the generator and parser tests.

## Files

| File | What it is |
|---|---|
| `linux-metrics.txt` | The batched Linux collection, one section per line |
| `windows-metrics.ps1` | The Windows CIM collection, one `key=value` per line |
| `probes.txt` | Everything that fits on one line, as `key = command` |

## `linux-metrics.txt`

One shell command per line, in the order the parser's section enum expects.
Blank lines and lines starting with `#` are comments and are **not** sections —
adding one does not shift the section numbering.

The reader joins the lines with `; echo ---SM-SECTION---; ` so the whole thing
is a single SSH round trip, which is what makes this affordable on a
high-latency link. Three placeholders are substituted before sending:

- `%PROCESSES%` — the `ps` line, or `true` when no machine screen is open.
  `ps` over every process costs the host ~30 ms per poll.
- `%DOCKER%` — the `docker info` line, or `true` between the 30 s samples.
  It costs the host ~89 ms, nearly all of it the Go CLI starting up.
- `%DF_EXCLUDE%` — the `-x <type>` flags built from `pseudo-filesystem-types`
  in `probes.txt`, so the exclusion list and the local check that has to
  repeat it for hosts whose `df` lacks `-x` cannot disagree.

A skipped section still emits its separator, so the section count never moves.

## `windows-metrics.ps1`

Sent deflated (RFC 1951) and base64'd inside a self-extracting stub, then
handed to `powershell -EncodedCommand`, which takes UTF-16LE base64. Two
reasons, both load-bearing:

- `-EncodedCommand` sidesteps quoting entirely. The default shell on Windows
  OpenSSH may be `cmd.exe` or PowerShell and they disagree about almost every
  metacharacter.
- UTF-16LE base64 nearly triples what it wraps, and the result still has to fit
  in `cmd.exe`'s ~8191-character command line. Uncompressed, this script
  crossed that limit as soon as the machine-screen detail was added, and hosts
  answered `命令行太长` instead of running anything.

CIM classes are used rather than `Get-Counter` because counter *names* are
localised — on a Chinese or German Windows the English names do not exist —
while CIM class and property names are invariant.
