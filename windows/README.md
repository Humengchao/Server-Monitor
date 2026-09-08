# Server Monitor for Windows

通过 SSH 监控 Linux 与 Windows 主机的原生 Windows 应用。**完全本地**：直连主机、
数据存在本机、不需要后端、不需要数据库、不需要账号。

与本仓库 `web/` 下的网页版除了共用采集脚本之外没有关系 —— 这是一个独立的桌面程序，
功能对标 [`macos/`](../macos/README.md) 下的 macOS 客户端。

*English below · [English version](#server-monitor-for-windows-english)*

```
┌── WPF（侧栏 · 自绘图表 · 终端 · SFTP）
├── MonitorService      轮询循环与全部界面状态（UI 线程）
├── MetricsCollector    一次往返换一份快照
├── SshNetTransport     SSH.NET 长连接池，每台主机一条（相当于 ControlMaster）
├── OpenSshExeTransport 备选：系统 ssh.exe，逐条命令
├── SftpBrowser         远端浏览与传输（SSH.NET SftpClient）
├── TerminalHost        Windows Terminal 控件 + ShellStream
└── Database            SQLite：主机、片段、身份、指标历史、会话历史
```

## 功能

- **实时指标**：CPU、内存、磁盘、负载、网络与磁盘 I/O 速率、运行时间、延迟。
  每次轮询一条批量命令，而不是每个指标一条。
- **诚实的延迟**：ICMP 原始套接字。隧道会在本地代答，让另一个大洲的主机看起来
  1 毫秒就回包，所以公网地址上低于 2ms 的回包会被丢弃（R12）。
- **机器详情**：环形仪表、每核心占用、CPU 细分、内存细分、挂载点、网卡、
  进程列表、GPU、四张历史曲线（15 分钟 / 1 小时 / 6 小时 / 24 小时）、
  vnStat 流量统计、IP 归属地查询（按需，且会说明请求会发给 ipwho.is）。
- **终端**：Windows Terminal 的渲染控件，喂 SSH.NET 的 `ShellStream`。复用采集
  那条连接，所以开终端只多一个 channel，不是又一次握手和又一条 auth.log。
  片段只「键入」不「回车」，`docker exec` 与容器 shell 共用同一个窗口。
- **SFTP**：浏览、上传、下载、进度、多选、重命名、删除、新建目录。远端名字里
  合法而 Windows 不接受的字符（`aux`、结尾空格、冒号）在保存时可见地替换。
- **Docker**：五张表（容器 / 镜像 / 数据卷 / 网络 / Compose）、`stats`、日志、
  启停重启、容器 shell。
- **SSH 密钥**：扫描 `%USERPROFILE%\.ssh`（只读元数据，不读密钥本体）、生成、
  导入、复制公钥、把公钥装到主机的 `authorized_keys`（按密钥本体查重）、
  用 `icacls` 收紧 ACL —— Windows 的 OpenSSH 检查的是 ACL 而不是权限位，
  一把「别人能读」的私钥会被拒绝，而报错并不会告诉你这一点（R11）。
- **常驻**：托盘图标带离线计数、关窗驻留、Toast 告警点击跳转到对应主机、
  开机自启、单实例；睡眠唤醒与网络恢复立刻重试；省电模式放慢轮询；
  窗口隐藏时不刷新界面，但采集、入库与告警照常。
- **双语与深浅色**：跟随系统，也可以固定。331 条文案与 macOS 端同源。

## 要求

- Windows 10 1809（17763）或更新；Windows 11 22H2 起可以打开 Mica 材质。
- 不需要安装 .NET —— 发布包是自包含单文件。
- 生成或读取密钥信息需要 `ssh-keygen.exe`（`Add-WindowsCapability -Online
  -Name OpenSSH.Client~~~~0.0.1.0`）；ssh.exe 备选传输同理。默认的内置传输不需要。

## 构建

```powershell
dotnet build windows/ServerMonitor.slnx
dotnet test  windows/ServerMonitor.slnx      # 322 通过，14 条 live 用例跳过
dotnet run --project windows/src/ServerMonitor.App
```

打包（便携 zip + Inno Setup 安装包，x64 与 arm64）：

```powershell
cd windows
./scripts/package.ps1 -Version 0.1.0          # 本地可加 -SkipInstaller
```

`windows/v*` 标签会触发 [CI](../.github/workflows/windows-app.yml) 出 Release。

### 命令行采集

`smctl` 不带界面跑一遍采集，用来隔离「是采集不对还是界面不对」：

```powershell
dotnet run --project windows/src/ServerMonitor.Cli -- poll   my-host --alias
dotnet run --project windows/src/ServerMonitor.Cli -- detail 10.0.0.5 -u ops -i ~\.ssh\id_ed25519
dotnet run --project windows/src/ServerMonitor.Cli -- probe  my-host --alias -v
```

### 测试

离线用例不碰网络，任何机器上都能跑。需要真机的用例靠环境变量开启，
**凭据只进环境变量，不进仓库**：

| 变量 | 用途 |
|---|---|
| `SM_LIVE_ALIAS` | Linux 主机的 ssh 别名，跑采集、Docker、SFTP 往返 |
| `SM_LIVE_PUBKEY` | 一把**一次性**公钥，用来验证导出公钥的幂等（会真的写 `authorized_keys`） |
| `SM_WIN_HOST` / `SM_WIN_USER` / `SM_WIN_PASSWORD` | Windows 主机，密码认证 |
| `SM_RENDER_CARDS` | 离屏渲染测试把每张卡片存成 PNG 的目录 |

## 安全说明

- **密码进 Windows 凭据管理器**（`CredRead`/`CredWrite`），不进 SQLite、不进
  设置文件、不进日志。密钥留在磁盘上，由 OpenSSH 自己管。
- **主机密钥按 `known_hosts` 校验**。首次连接记下；变更时**拒绝连接**并写日志，
  不会静默接受。
- **未签名**（D7）。首次运行 SmartScreen 会拦一次：「更多信息 → 仍要运行」。
  部分杀毒软件也会对未签名的自包含 exe 报毒（R7）。安装包与便携 zip 都是这样，
  签名证书不在本项目预算里。
- IP 归属地查询是唯一的出站请求，且只在点「查询位置」时发生，会先告知目标地址
  会发给 ipwho.is。

## 已知缺口

- **终端还没在真机上跑过**：控件能加载、窗口能开、失败会如实报错，但输入法、
  `vim`、`Ctrl+C`、resize 这几项要等有主机凭据时才算验过（计划里的 S3）。
- **Mica 默认关闭**。它需要窗口自身透明，而在虚拟显示器、部分远程串流环境下
  系统并不真的绘制那层材质，客户端区会整片变黑 —— 系统 API 在这种情况下仍然
  返回成功，程序无法自己发现。设置里可以打开。
- **后台 soak 有一段没解释清楚的漂移**：最小化跑 30 分钟（五台不可达的主机），
  采集没有中断，但句柄数从 614 涨到 701（约每分钟 3 个），RSS 从 112 MB 涨到
  120 MB。已经缩小到「轮询路径」：**一台主机也不加**时最小化跑 5 分钟句柄是
  552→536（不涨），而传输层单独量一百次连接被拒、二十次超时也不涨
  （`HandleTests`，且强制 GC 后不涨说明不是永久泄漏，更像是等一次 gen2 回收）。
  托盘图标在摘要不变时根本不重绘，所以不是它。也不是套接字：句柄涨的同时
  进程的 TCP 端点数一直是 0–2，说明涨的是事件/等待类句柄，最可能是 SSH.NET
  的 session 对象在等回收。剩下的定位要靠 PerfView，按计划归 P7。
  原始采样在 `windows/artifacts/soak*.csv`。
- **告警 Toast 也没法在这里验**：告警按「状态跳变」发，而不是按「当前状态」，
  所以一开始就不可达的主机不会触发（这是对的：否则每次启动都会弹一串）。
  要看到 Toast 需要一台先在线后离线的主机。
- **打包后的常驻内存明显更高**：自包含单文件跑起来是 private 约 200 MB
  （framework-dependent 直接 `dotnet run` 约 115 MB）。原因是
  `EnableCompressionInSingleFile`，压缩过的镜像启动时要解到内存里 —— 用下载
  体积换常驻内存。这个取舍要不要改，归 P7。
- **P7 的加固与性能还没做**：高 DPI 与多显示器、中文 Windows 的 GBK 代码页、
  PerfView 量主线程，都还没逐项核对。
- **干净机器上的安装 / 卸载全程**（P6 出口）还没在一台干净的 Windows 11 上走过。
- 密钥本体不显示、不导出；两端数据互导、便携模式、MSIX 是 P8 的可选项。

## 目录

| 路径 | 内容 |
|---|---|
| `src/ServerMonitor.Core/` | 采集、解析、SSH、SQLite、文案。`net10.0`，不依赖 Windows |
| `src/ServerMonitor.App/` | WPF 外壳：`Views/`、`Controls/`（自绘）、`Theme/`、`Platform/`、`Terminal/` |
| `src/ServerMonitor.Cli/` | `smctl`，不带界面的采集 |
| `tests/` | 322 条：解析器、数据库、轮询循环、SFTP 路径、离屏渲染、live |
| `scripts/package.ps1` · `scripts/installer.iss` | 发布与安装包 |
| [`PLAN.md`](PLAN.md) | 技术决策（D1–D9）、既有事实（F1–F11）、分阶段计划、风险（R1–R13） |
| [`../shared/probes/`](../shared/probes/) | 三端共用的采集脚本，改这里而不是各自复制 |

---

## Server Monitor for Windows (English)

A native Windows app that monitors Linux and Windows hosts over SSH. **Fully
local**: it talks straight to your hosts, stores everything on this machine, and
needs no backend, no database server and no account.

Not related to the `web/` deployment in this repo other than sharing its
collection scripts — this app is standalone, and is the counterpart of the
[macOS client](../macos/README.md).

### What it does

- **Live metrics** over SSH: CPU, memory, disk, load, network and disk I/O
  rates, uptime, latency. One batched command per poll, not one per metric.
- **Honest latency.** Raw-socket ICMP. A tunnel answers a ping locally, which
  makes a host on another continent appear to reply in under a millisecond, so
  a sub-2ms reply from a public address is discarded (R12).
- **Machine detail**: ring gauges, per-core load, CPU and memory breakdowns,
  mounts, interfaces, processes, GPU, four history charts (15 min / 1 h / 6 h /
  24 h), vnStat traffic, and an on-demand IP location lookup that says where the
  request goes before it goes.
- **Terminal**: Windows Terminal's own renderer fed by SSH.NET's `ShellStream`,
  riding the connection the poll already holds — so opening one costs a channel
  rather than a handshake and another line in the host's auth log. Snippets are
  typed at the prompt, not sent; `docker exec` and a container shell reuse the
  same window.
- **SFTP**: browse, upload, download, progress, multi-select, rename, delete,
  make directory. Remote names that are legal on ext4 and unrepresentable here
  (`aux`, a trailing space, a colon) are substituted visibly in the save dialog.
- **Docker**: five tables (containers, images, volumes, networks, compose),
  `stats`, logs, start/stop/restart, container shell.
- **SSH keys**: scan `%USERPROFILE%\.ssh` (metadata only — never key material),
  generate, import, copy the public key, install it into a host's
  `authorized_keys` (deduplicated on the key body), and tighten the ACL with
  `icacls` — Windows' OpenSSH checks the ACL rather than a permission bitmask
  and refuses a key others can read, with an error that does not say so (R11).
- **Residency**: a tray icon carrying the offline count, close-to-tray, toast
  alerts that open the host they are about, start with Windows, single instance.
  Wake from sleep and a network change retry immediately; energy-saver slows the
  cadence; a hidden window stops redrawing while collection, storage and alerts
  carry on.
- **Bilingual and light/dark**, following the system or pinned. The 331 strings
  come from the same table as the macOS build.

### Requirements

- Windows 10 1809 (17763) or newer. Mica is available from Windows 11 22H2.
- No .NET install needed — the published build is self-contained and single-file.
- `ssh-keygen.exe` is needed to read key metadata or generate keys
  (`Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0`), and for the
  ssh.exe fallback transport. The default transport needs neither.

### Build

```powershell
dotnet build windows/ServerMonitor.slnx
dotnet test  windows/ServerMonitor.slnx      # 322 pass, 14 live cases skipped
dotnet run --project windows/src/ServerMonitor.App

cd windows; ./scripts/package.ps1 -Version 0.1.0   # zip + installer, x64 + arm64
```

A `windows/v*` tag publishes a Release through
[CI](../.github/workflows/windows-app.yml).

The offline tests touch no network and run anywhere. The live suites are gated
on environment variables — `SM_LIVE_ALIAS`, `SM_LIVE_PUBKEY` (use a throwaway
key: it really does write to `authorized_keys`), `SM_WIN_HOST` / `SM_WIN_USER` /
`SM_WIN_PASSWORD`, and `SM_RENDER_CARDS` for the offscreen card PNGs.
**Credentials belong in environment variables, never in the repository.**

### Security notes

- **Passwords go to Windows Credential Manager** (`CredRead`/`CredWrite`), not
  into SQLite, the settings file, or the log. Keys stay on disk where OpenSSH
  owns them.
- **Host keys are checked against `known_hosts`.** A first connection is
  recorded; a changed key is **refused** and logged rather than accepted
  quietly.
- **Not code-signed** (D7). SmartScreen warns on first run — "More info" →
  "Run anyway" — and some antivirus products flag unsigned self-contained
  executables (R7). This applies to both the installer and the portable zip; a
  signing certificate is out of scope for this project.

### Known gaps

- **The terminal has not been exercised against a live host.** The control
  loads, the window opens and a failure is reported honestly, but IME input,
  `vim`, `Ctrl+C` and resize are unverified until host credentials are available
  (the plan's S3 spike).
- **Mica is off by default.** It needs the window's own background transparent,
  and where DWM declines to composite the material — a virtual display adapter,
  some remote-streaming setups — the client area renders entirely black while
  the API still reports success. There is a switch for it in Settings.
- **The background soak shows an unexplained drift.** Thirty minutes minimised
  against five unreachable hosts collected without interruption, but the handle
  count went from 614 to 701 (about three a minute) and RSS from 112 MB to
  120 MB. It is narrowed to the polling path: **with no hosts at all**, five
  minutes minimised went 552 → 536, and the transport measured on its own does
  not move either — a hundred refused connections and twenty timeouts stay flat
  (`HandleTests`), and staying flat *after a forced GC* says this is not a
  permanent leak so much as something waiting for a gen2 collection. The tray
  icon does not redraw at all while the summary is unchanged, so it is not
  that, and it is not sockets either: the process's TCP endpoint count stays
  at 0–2 while the handle count climbs, so what is growing is event/wait
  handles — most likely SSH.NET session objects waiting to be collected.
  Pinning it down wants PerfView, which is P7's job. Raw samples are in
  `windows/artifacts/soak*.csv`.
- **Toast alerts are unverifiable here too.** Alerts fire on a status
  *transition*, not a state, so a host that was already unreachable when the
  app started does not raise one — which is right, or every launch would fire a
  volley. Seeing a toast needs a host that goes from online to offline.
- **The packaged build's resident memory is markedly higher**: about 200 MB
  private for the self-contained single file, against about 115 MB running
  framework-dependent through `dotnet run`. The cause is
  `EnableCompressionInSingleFile` — the compressed image is decompressed into
  memory at startup, trading resident memory for download size. Whether that
  trade is the right one belongs to P7.
- **P7's hardening and performance pass is outstanding**: high DPI and multiple
  monitors, the GBK code page on Chinese Windows, and a PerfView measurement of
  the UI thread have not been worked through.
- **The clean-machine install/uninstall run** (P6's exit criterion) has not been
  done on a fresh Windows 11.
- Key material is never displayed or exported. Cross-platform data exchange, a
  portable mode and MSIX are optional P8 items.
