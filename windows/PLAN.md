# Server Monitor · Windows 端计划

状态：草案 v2，2026-09-07。取代同日早先交付的 HTML/PDF 初稿；两稿在 D1、D3、D4、D5 上的差异与原因写在各条里。尚未开工，等待第 10 节的答复。

## 1. 目标与非目标

**目标**：在同一仓库的 `windows/` 下做一个与 `macos/` 功能对等的 Windows 原生客户端。纯本地：不连本项目的服务端，直接 SSH 到各主机采集，数据存本机 SQLite，托盘常驻、后台轮询与告警。界面对标 macOS 端，也就是 SwiftServer 的布局：侧栏「仪表板 / 资源：机器 · 身份 · SSH 密钥 / 工具箱：代码片段 · 容器 / 会话」。

**非目标**：不做 WebView 套壳；不做「连后端的客户端」；不改 `backend/`、`frontend/`、`macos/` 的行为。唯一例外是把探测脚本抽成共享资源，见 D9 与附录 A。

## 2. 已核实的前提

后面每个决策都引用这些事实。有一条不成立，对应的决策就要重看。

| # | 事实 | 对计划的影响 | 来源 |
|---|---|---|---|
| F1 | Windows 自带的 OpenSSH（Win32-OpenSSH）**不支持 ControlMaster / ControlPath**：选项被接受但不建 master；用户配置里若有 `Host *` 的 ControlMaster，ssh.exe 报 `getsockname failed: Not a socket`。 | macOS 端「系统 ssh + 复用」的路子在 Windows 上不存在。若走 ssh.exe，必须显式传 `-o ControlMaster=no -o ControlPath=none`，且每次轮询都是完整握手加登录。 | [#1328](https://github.com/PowerShell/Win32-OpenSSH/issues/1328) · [vscode #96](https://github.com/microsoft/vscode-remote-release/issues/96) · [hpc-agent #154](https://github.com/jamesdchen/hpc-agent/issues/154) |
| F2 | `SSH_ASKPASS` 在 Win11 自带的 8.6p1 上**可用**，前提是同时设置 `SSH_ASKPASS_REQUIRE`；issue 里报告者确认有效。Windows 不带 ssh-askpass 程序，helper 要自己提供。 | **修正初稿的 F2/D4**：初稿判定 ASKPASS 不可靠并设计了 ConPTY 应答 `password:` 的方案，现在不需要。macOS 端 `SSH_ASKPASS_REQUIRE=force` 的做法可直接搬到 ssh.exe 路径。 | [#2115](https://github.com/PowerShell/Win32-OpenSSH/issues/2115) |
| F3 | Win10 1809+ / Win11 自带 OpenSSH 客户端在 `C:\Windows\System32\OpenSSH\`，2024-10 累积更新后为 9.5p1；`StrictHostKeyChecking=accept-new`、`ProxyJump`、`BatchMode` 都可用。GitHub 版装在 `%ProgramFiles%\OpenSSH`。 | 不需要打包 ssh。定位顺序 ProgramFiles → System32 → PATH，排除 Git for Windows 自带的 Cygwin 构建。 | [Releases](https://github.com/PowerShell/Win32-OpenSSH/releases/) |
| F4 | **SSH.NET 自 2023.0.0 支持 rsa-sha2-256/512**、OpenSSH 格式私钥（RSA/ECDSA）、SHA256 指纹、异步 SFTP。后端 Go 用的 x/crypto/ssh 同样支持，且已有每主机长连接缓存 `backend/internal/services/ssh_cache.go`。 | macOS 端否决进程内库的理由（Citadel 的 RSA 只有 SHA-1）在 .NET 不成立。 | [SSH.NET 2023.0.0](https://github.com/sshnet/SSH.NET/releases/tag/2023.0.0) |
| F5 | WinUI 3 / Windows App SDK **只能在 Windows 上编译**（XamlCompiler 是 x86 .NET Framework 程序）。WPF 项目设 `EnableWindowsTargeting=true` 可在 macOS 编译、不能运行。Avalonia 在 macOS 上可编译也可运行。 | 直接决定 D1。 | MS Q&A · NETSDK1100 |
| F6 | WPF 在 .NET 9/10 内建的 Fluent 主题仍是实验 API（WPF0001），缺 NavigationView、ToggleSwitch、ProgressRing，Mica 未落地；WPF-UI（lepoco，MIT）补齐。Avalonia 11 自带 Fluent 主题、Mica、TrayIcon；FluentAvalonia 提供 NavigationView 等 WinUI 控件。 | 两条 UI 路线都能做到 SwiftServer 那种侧栏加 Mica 的观感。 | [dotnet/wpf #8545](https://github.com/dotnet/wpf/issues/8545) · WPF-UI · FluentAvalonia |
| F7 | 终端控件：WPF 有 EasyWindowsTerminalControl 1.0.38（封装 Windows Terminal 的 WPF 控件与 ConPTY，net10.0-windows，MIT；有 airspace 限制；微软官方包不上 nuget.org）。Avalonia 有基于 XTerm.NET 的 Iciclecreek.Avalonia.Terminal 与 AvaloniaTerminal，2025-12 仍在更新。XtermSharp 已停滞。 | D5 随 D1 走；两类控件都能接 SSH.NET 的 ShellStream，不必依赖 ConPTY。 | [terminal #6999](https://github.com/microsoft/terminal/issues/6999) · [EasyWindowsTerminalControl](https://github.com/mitchcapper/EasyWindowsTerminalControl) · [XTerm.NET](https://github.com/tomlm/XTerm.NET) · [Iciclecreek.Avalonia.Terminal](https://github.com/tomlm/Iciclecreek.Avalonia.Terminal) |
| F8 | GitHub Actions `windows-latest` 现为 Windows Server 2025，预装 .NET SDK、Inno Setup、WiX。 | CI 直接出安装包，不用额外装工具。 | runner-images |
| F9 | ConPTY 最低 Windows 10 1809；Windows App SDK 最新稳定 2.4.0。 | D8 最低系统定 1809。 | node-pty · App SDK |
| F10 | 本机现状：这台 Mac 没有 .NET SDK，也没有 Windows 虚拟机。仓库里与 Windows 相关的只有「监控 Windows 主机」的采集脚本 `WindowsMetrics.swift`，没有任何 Windows 客户端代码。 | P0 第一步 `brew install --cask dotnet-sdk`。DEFLATE + base64 + `-EncodedCommand` 的脚本投递方案可原样复用（.NET `DeflateStream` 就是 RFC 1951 raw deflate）。 | 本地检查 |
| F11 | 同一份 Windows 采集脚本在仓库里已有两份且**已分叉**：Go 版缺 `$ProgressPreference='SilentlyContinue'`（Server 2016 上进度流混入 stdout 的修复）、缺每核 CPU、`ident`、`ips`。 | 写第三份实现之前先把脚本与真机 fixture 单一来源化，见 D9。 | `metrics.go` 与 `WindowsMetrics.swift` 对比 |

## 3. 关键决策

### D1 · UI 框架 — 待拍板，取决于验证环境

- **候选 A：WPF（.NET 10）+ WPF-UI。** 微软第一方、二十年成熟度、DataGrid 等控件内建、终端控件最成熟（Windows Terminal 的渲染器，中文输入法走 TSF）。在 Mac 上只能编译不能运行，看界面要靠 Windows 机器，或 CI 离屏渲染出 PNG。
- **候选 B：Avalonia 11 + FluentAvalonia。** Skia 自绘，不是微软第一方，但 Fluent、Mica、托盘齐全，在 Mac 上能直接运行，界面迭代不依赖 Windows。终端控件较年轻，中文输入法要在 S3 里验证。
- **否决**：WinUI 3（只能在 Windows 编译、托盘与终端生态弱、App SDK 运行时依赖）；Electron / Tauri / WebView2 主界面（macOS 端已否决套壳）。
- **决策规则**：有一台能让 Claude Code 通过 SSH 驱动的 Windows 机器或虚拟机（本机 Parallels / UTM 跑 Win11 ARM64 即可，产物有 arm64）→ 选 A；只有远程 RDP 的 Windows Server → 选 B。两条路线共用 Core，切换成本只是 App 层的 XAML。初稿直接推荐 A，本稿改为按此规则定。

### D2 · 语言与运行时

C# 14 / .NET 10 LTS（支持到 2028-11），自包含单文件发布，win-x64 + win-arm64。用户机器不需装运行时；ARM64 覆盖 Apple 芯片上跑 Win11 虚拟机的场景。代价：单文件 60–80 MB；未签名的自包含 exe 偶有杀软误报（R7）。

### D3 · SSH 传输与连接复用 — 推荐 SSH.NET 长连接（与初稿不同）

- **推荐**：每台主机一条 SSH.NET `SshClient` 长连接，等价于 macOS 端的 ControlMaster。采集走 exec 通道，SFTP 走同一连接的 `SftpClient`，终端走 `ShellStream`。保活、失效重连、定期清扫照后端 `ssh_cache.go` 的设计。
- **备选**：系统 `ssh.exe` 子进程，无复用，每次轮询一个进程，固定 `-o ControlMaster=no -o ControlPath=none`。
- 两者实现同一个 `ISshTransport`，全局或按主机可切换；默认值由 P0 预研 S1 的数字确认。
- **为什么改**：
  1. Windows 没有 ControlMaster（F1）。ssh.exe 路线每 5 秒一次完整握手加登录，sshd 每台主机每天记约 17,000 条登录（auth.log、wtmp、PAM 会话）；macOS 端靠 `ControlPersist=300` 把这个数压到每天不到 300 条。
  2. 库的 RSA 顾虑在 .NET 不存在（F4）。
  3. 项目在 Go 后端已经做过一次「长连接」的选择并稳定运行。
- **库路线要自己补的**：读 `%USERPROFILE%\.ssh\config`（现有 importer 已解析 Host / HostName / User / Port / IdentityFile，再加 ProxyJump，实现为经跳板机的 `ForwardedPortLocal` 或嵌套连接）；`known_hosts` 按 accept-new 语义校验（含哈希主机名、多密钥类型），认首次、拒绝变更；ssh-agent v1 不支持（Windows 的 agent 是命名管道 `\\.\pipe\openssh-ssh-agent`，后续版本补），v1 直接读密钥文件，口令存凭据管理器。`Match`、证书、PKCS#11 等高级项不支持，需要的用户切 ssh.exe 备选（R13）。

### D4 · 密码认证与凭据存放

- 密码与密钥口令存 **Windows 凭据管理器**（对应 macOS 钥匙串），用户能在控制面板里看见和删除；私钥永远留在 `%USERPROFILE%\.ssh`。
- SSH.NET 路线：`PasswordAuthenticationMethod` + `KeyboardInteractiveAuthenticationMethod`。
- ssh.exe 备选路线：`SSH_ASKPASS` + `SSH_ASKPASS_REQUIRE=force`（F2）。helper 是我们自己的一个小程序，从环境或管道读密码，密码不上命令行。`StrictHostKeyChecking=accept-new` 避开 macOS 端那次「askpass 回答了指纹问题导致死循环」的坑。
- **否决**：ConPTY 伪终端应答 `password:`（初稿方案）。它依赖提示文本，遇到 MFA 或 banner 就脆弱；F2 修正后没有必要。

### D5 · 终端 — ShellStream 喂终端控件，不依赖 ConPTY

- 选 A（WPF）：EasyWindowsTerminalControl，用 `ITerminalConnection` 适配器把 SSH.NET 的 ShellStream 接进 Windows Terminal 控件；ConPTY 只在 ssh.exe 备选路线下用。docker exec、容器 shell 走同一控件。
- 选 B（Avalonia）：Iciclecreek.Avalonia.Terminal 或 AvaloniaTerminal（XTerm.NET），同样接 ShellStream。
- 备选：XtermSharp 引擎 + 自绘渲染器。否决：WebView2 + xterm.js。
- 控件一律隔离在 `TerminalHost` 适配层后面，锁版本。

### D6 · 数据、设置与图表

- SQLite（Microsoft.Data.Sqlite），schema 与 macOS 端一致：六张表（server、metricSample、snippet、identity、sessionRecord、machineGroup）与索引照搬，macOS 端 v1–v9 迁移合成 Windows 的 v1；UUID 同样存 16 字节 BLOB，将来两端数据库可直接互导。历史归约同一条 SQL：按时间桶 GROUP BY，≤ 240 点。
- 数据目录 `%LOCALAPPDATA%\ServerMonitor\`，设置存 JSON，日志写 `logs/` 下的滚动文本文件。
- 环形仪表与四张历史面积图**自绘**（DrawingVisual / Avalonia DrawingContext），零依赖，不做每 tick 动画（macOS 端踩过「周期刷新视图上的动画是主线程大户」）；ScottPlot 作后备。

### D7 · 仓库、CI 与发布

- 同仓库 `windows/`；`windows-app.yml` 在 windows-latest 构建、测试、`dotnet publish`，打便携 zip 与 Inno Setup 安装包；paths 过滤 `windows/**`。
- `deploy.yml` 已加 `paths-ignore: windows/**`（随本计划一起提交），否则改 Windows 端会触发网页版重新部署。
- 版本策略照 macOS：tag `v*` 出正式号，否则 `0.1.0-dev+sha`。同一个 tag 会同时触发 macOS 与 Windows 两个工作流，release 任务要能「已存在则追加资产」，而不是各建一个 release。
- 不签名，README 说明 SmartScreen 提示（对应 macOS 端的 Gatekeeper 说明）。MSIX 可选（P8）。

### D8 · 最低系统

Windows 10 1809 及以上，按 Windows 11 设计。1809 是 ConPTY 与自带 OpenSSH 的起点；Mica 在 Win10 上自动退化为纯色，其他功能不受影响。

### D9 · 探测脚本与 fixture 单一来源（新增）

- 写第三份实现之前，把 Linux 批量命令、Windows PowerShell 脚本、docker / compose / vnStat 探测命令抽到 `shared/probes/`，真机输出样本抽到 `shared/fixtures/`。Go 用 `//go:embed`、Swift 用 SwiftPM resources、C# 用 `EmbeddedResource` 在编译期内嵌，三种语言的解析器测试读同一批 fixture。F11 就是不这么做的后果。
- 若附录 A 的结构调整暂不批准，退一步：C# 的 fixture 从 Swift 测试复制到 `windows/tests/testdata/`，脚本先复制、注明来源。

## 4. 架构与目录

与 macOS 端一样分「库 + 壳」：ServerMonitorKit ↔ ServerMonitor.Core，ServerMonitor ↔ ServerMonitor.App。多出一个无界面 CLI，让整条采集链在 Mac 上就能对真机跑通。

```
windows/
  ServerMonitor.slnx
  Directory.Build.props              net10.0 · Nullable · TreatWarningsAsErrors · EnableWindowsTargeting
  src/ServerMonitor.Core/            无 Windows 依赖：这台 Mac 上可编译、可测
    Model/      Server · Identity · MachineGroup · Snippet · SessionRecord · MetricSample · MetricSnapshot · HostDetail
    Store/      Database（SQLite + 迁移 + 时间桶归约 + recordPoll 单事务）· AppSettings（JSON）· ICredentialStore
    Ssh/        ISshTransport · SshNetTransport（默认）· OpenSshExeTransport（备选；SshLocator 找 ssh.exe）
                SshConfig（~/.ssh/config 解析）· KnownHosts · SshTarget · SftpClient · DockerClient
                SshKeyScanner · SshKeyManager · PublicKeyInstaller
    Collect/    ProcParsers · WindowsMetrics · MetricsCollector · MonitorService · PingProbe · Vnstat · VnstatInstaller
                GeoLookup · HistoryReducer · Lines（CRLF 归一）
    Alerts/     AlertService（投递用委托注入，规则可测）
    L10n/       zh / en 字典，key 与 L10n.swift 一致
  src/ServerMonitor.App/             D1 选 A：net10.0-windows + WPF + WPF-UI；选 B：net10.0 + Avalonia + FluentAvalonia
    Views/ ViewModels/               Dashboard · Machines · ServerDetail（卡片）· Docker · Snippets · Identities
                                     SshKeys · Sftp · Terminal · Sessions · Settings（CommunityToolkit.Mvvm）
    Controls/                        RingGauge · HistoryChart · StatusCard · StaticGrid · TagChip（自绘）
    Terminal/                        TerminalHost（隔离第三方控件）· ShellStreamConnection · ConPty（仅备选路线）
    Platform/                        TrayIcon（动态图标）· Toasts · StartupRegistration · SingleInstance
                                     PowerEvents · NetworkEvents · WindowsCredentialStore
  src/ServerMonitor.Cli/             smctl poll <host> · smctl detail <host> · smctl docker <host>，直出 JSON
  tests/ServerMonitor.Core.Tests/    xUnit：解析器（fixture 与 Swift 测试共用）· 迁移 · tick 屏障 · 退避 · 告警 · L10n key
                                     Live 用例由 SM_LIVE_ALIAS / SM_WIN_HOST 开关，与 macOS 端同名
  tests/ServerMonitor.App.Tests/     仅 Windows：离屏渲染卡片出 PNG（CI 产物）· ViewModel
  scripts/package.ps1                publish x64/arm64 → zip + Inno Setup
  README.md
```

数据流不变：ISshTransport → MetricsCollector → MonitorService（tick 屏障，每 tick 只发布一次）→ ViewModel → View；SQLite 与 AlertService 在旁路；托盘读同一份状态。线程模型：MonitorService 固定在 UI 线程的 SynchronizationContext 上（对应 `@MainActor`），SSH 与数据库工作在线程池，测试里换成单线程上下文。

## 5. macOS 专属能力的对应

| macOS 端 | Windows 端 | 备注 |
|---|---|---|
| Keychain | 凭据管理器，`CredWrite` 通用凭据 | 用户能在控制面板里看见和删除 |
| UNUserNotificationCenter | Windows Toast，CommunityToolkit 的 `ToastNotificationManagerCompat`（非打包应用成熟） | 首次运行注册开始菜单快捷方式；点击激活对应主机 |
| SMAppService 开机自启 | HKCU `Run` 注册表键 | 若做 MSIX 改 StartupTask |
| MenuBarExtra | 托盘图标 + 弹出小窗 | 16 px 位图动态生成：最高 CPU 或离线数 + 状态色；关窗即驻留 |
| occlusionState 门控 | 最小化 / 隐藏到托盘时不更新界面 | Windows 没有遮挡通知；采集、入库、告警不受影响 |
| beginActivity 关 App Nap | 无对应；实测电源节流（EcoQoS）是否拖慢后台轮询 | 必要时 `SetProcessInformation` 退出节流（R10） |
| isLowPowerModeEnabled | 节电模式：`GetSystemPowerStatus` / `PowerManager.EnergySaverStatus` | 同样放慢到 3 倍间隔 |
| NWPathMonitor / 唤醒 | `NetworkChange.NetworkAvailabilityChanged` / `SystemEvents.PowerModeChanged` | 清退避并立即轮询 |
| `ping -b en0` | `IcmpSendEcho2Ex` 指定物理网卡源地址；ICMP 被过滤时回退主机自报时钟法 | 不解析本地化的 ping.exe 输出；公网地址 RTT 低于 2 ms 视为被 TUN 本地应答（R12） |
| `/usr/bin/ssh-keygen` | `System32\OpenSSH\ssh-keygen.exe` | 写入后 `icacls` 去继承、只留当前用户：Windows OpenSSH 检查的是 ACL 不是 chmod（R11） |
| `split(whereSeparator: \.isNewline)` | `Lines()`：按 `\n` 切并去掉尾部 `\r` | Windows 主机输出全是 CRLF |
| Process 读 stdout | `StandardOutputEncoding = UTF8` | 中文 Windows 控制台代码页是 GBK，不能信默认编码（R5） |
| NSPasteboard / NSOpenPanel | Clipboard / 文件对话框 | |
| GRDB | Microsoft.Data.Sqlite + 手写 SQL | 不复刻 record 抽象 |
| SwiftTerm | 见 D5 | |
| Swift Charts | 自绘，见 D6 | |
| 单实例（macOS 天然） | Mutex + 命名管道，二次启动激活已开窗口 | |
| ⌘1–7 等快捷键 | Ctrl+1–7、Ctrl+N、Ctrl+Shift+N/I、Ctrl+T、Ctrl+Shift+T、F5 | |

## 6. 功能对照与阶段归属

「同」表示逻辑原样移植。

- **P1 · Core**：指标采集（Linux `/proc` 批量命令、Windows CIM 脚本）同；OS 探测 `uname -s`，255 或超时不判 Windows，同；延迟见第 5 节；轮询节奏（tick 屏障、每主机在途一个、几何退避上限 300 s、网络恢复与唤醒清零、低电量 ×3）同；对被监控主机的负担（`docker info` 每 30 s、`ps` 只对详情页主机）同；历史存储同一条 SQL。
- **P2 · 外壳与资源页**：仪表板（总计 / 在线 / 离线三卡 + 服务器卡：国旗、延迟、环形仪表、网络与磁盘速率，离线优先）；机器页（表格、分组卡、标签 FNV 哈希配色照搬以保证两端同色、搜索、排序）；机器编辑器与测试连接；身份；导入 ssh config（路径换 `%USERPROFILE%\.ssh\config`）；设置（轮询间隔、保留天数、阈值、通知、终端字体 Cascadia Mono / Consolas、自启）；双语、深浅色、快捷键、窗口状态恢复。
- **P3 · 详情页**：CPU（每核 + user / system / nice / iowait / steal）、负载、内存、每挂载点、每网卡、进程表、主机信息、GPU（无显卡整张不画）、IP 位置、Docker 瓦片、vnStat（一键安装两步确认）；StaticGrid 按预估高度分列、列宽量化到 16 px；历史图自绘 240 点。
- **P4 · 工具箱**：终端（字体、片段键入、docker exec、容器 shell）；SFTP（浏览、上传下载、进度、多选、重命名 / 删除 / 建目录，本地路径与盘符 Windows 化；SSH.NET 的 SftpClient 替代 macOS 端的 ls 解析 + scp）；Docker 五表、stats、日志、启停重启（compose 的 WARN 行绕过照搬）；代码片段；SSH 密钥（扫描、生成、导入、复制公钥、导出公钥到主机：chmod 700/600、按密钥本体查重、拒绝私钥）；会话侧栏与历史。
- **P5 · 系统集成**：托盘、Toast、自启、单实例、睡眠与网络事件、省电、隐藏不更新、后台 soak。
- **P8 · 可选**：两端数据互导 JSON（密码不导）、便携模式、MSIX。

## 7. 分阶段

顺序按依赖排：Core 先于一切界面，终端类页面必须等传输层。每阶段结束跑测试、推 main、盯 CI，沿用「改完自己 push」的约定。时长按 macOS 端的节奏估，合计约三周。

### P0 · 决策、脚手架与三个预研（0.5–1 天）

- 拍板 D1 与验证环境；Mac 上 `brew install --cask dotnet-sdk`。
- 建 `windows/` 骨架：三个项目 + 两个测试项目、`Directory.Build.props`、`windows-app.yml`（先只 build + test）。
- **S1 传输对比**：在 Windows 机上对 km 与一台 ProxyJump 主机各跑 100 次采集，ssh.exe 冷握手 vs SSH.NET 长连接，记每次耗时、客户端 CPU、主机侧 auth.log 增量。
- **S2 认证覆盖**：SSH.NET 用 RSA 与 ed25519 密钥（含加密的 openssh-key-v1 格式）连 km；用密码连 Windows 测试主机，拿到 `uname -s` 的等价输出并跑通 WindowsMetrics 脚本；ProxyJump 走通；known_hosts 的 accept-new 写入与变更拒绝。顺带验证 ssh.exe 备选路线的 ASKPASS。
- **S3 终端控件**：D1 对应的控件接 ShellStream：中文输入法、resize、Ctrl+C、vim、docker exec。
- 出口：CI 绿；S1 有数字；S2、S3 各有一段能跑的代码。任一预研失败就回到 D3 / D5 换备选。

### P1 · Core 移植（3–4 天）

- Model、Database（迁移、UUID BLOB、索引、时间桶归约、recordPoll 单事务）、AppSettings、ICredentialStore。
- `ISshTransport` 两个实现。SshNetTransport：连接缓存、保活、重连、命令超时与取消。OpenSshExeTransport：异步读两条管道、取消即 Kill、看门狗、固定 `ControlMaster=no`、UTF-8 解码、exit 255 + 空 stderr 的特征处理、host key 变更识别。
- SshConfig、KnownHosts。
- ProcParsers、WindowsMetrics、HostDetail、Vnstat、GeoLookup、HistoryReducer、Lines；fixture 来自 `shared/fixtures/` 或 `testdata/`。
- MetricsCollector：detectOS、批量脚本、docker 缓存、按需 ps；memoryTotal 与 cores 同为 0 时抛错而不是返回假数据。
- MonitorService：tick 屏障、inFlight、退避、detailedServers、每 tick 一次发布、删除与在途轮询的竞态守卫。AlertService。
- CLI `smctl`：poll / detail / docker 直出 JSON。
- 出口：`dotnet test` 在 Mac 全绿；`SM_LIVE_ALIAS=km dotnet test` 全链路通过；smctl 对 km 与 Windows 测试主机各拿到一份完整快照。

### P2 · 应用外壳与资源页（3 天）

- 外壳：NavigationView 侧栏、Mica、深浅色跟随系统、中英切换。
- 仪表板、机器页、机器编辑器（测试连接）、身份、导入 ssh config、设置。
- 自绘控件 RingGauge、ServerCard、StatusCard、TagChip；ViewModel 接入「每 tick 一次发布」。
- 渲染测试：选 A 用 RenderTargetBitmap 离屏出 PNG 作 CI 产物（`SM_RENDER_CARDS`）；选 B 在 Mac 上直接运行，另加 Avalonia headless 渲染测试。
- 出口：CI 出 exe；在一台 Windows 上加 km 能看到在线数据与环形仪表。

### P3 · 机器详情页（2–3 天）

- 全部状态卡；StaticGrid 权重分列，Docker 与进程整行；历史图自绘；vnStat 两步确认。
- 出口：与 macOS 端截图逐卡对照；km 上每张卡有数，GPU 卡在无显卡主机上整张不画。

### P4 · 工具箱（4 天）

- 终端：TerminalHost 适配器、字体设置、片段键入、docker exec、容器 shell。
- SFTP、Docker 五表与操作、代码片段、SSH 密钥（含 icacls 收紧 ACL）、会话侧栏与历史。
- 出口：Live 测试在 Windows 上通过：Docker 五表、SFTP 往返、导出公钥 added → alreadyPresent → 清理后无残留。

### P5 · 后台常驻与系统集成（2 天）

- 托盘动态图标与弹窗、关窗驻留、单实例；Toast 告警点击跳转；开机自启。
- 睡眠唤醒与网络变化清退避并立即轮询；省电模式放慢；隐藏时不更新界面。
- 后台 soak：最小化状态下跑一遍，macOS 端「pending 无上界」那类 bug 只有这样才测得到。
- 出口：最小化 30 分钟采集不间断、RSS 平稳；断网恢复立刻轮询；Toast 到达且点击能跳转。

### P6 · 打包、发布与文档（1 天）

- `dotnet publish` 自包含单文件 win-x64 / win-arm64；Inno Setup 安装包 + 便携 zip；tag 触发 Release，与 macOS 共用同一个 release。
- `windows/README.md`（中英）、顶层 README 加「Windows 原生客户端」一节、SmartScreen 说明。
- 出口：干净的 Windows 11 上安装、首启、加机器、卸载全程无残留。

### P7 · 加固与性能（2 天）

- 可空引用、分析器、warnings-as-errors 进 CI。
- PerfView / dotnet-counters 量主线程与内存（对应 macOS 端的 xctrace 方法）；绑定风暴、每 tick 动画、列表虚拟化逐项核对。
- 高 DPI 与多显示器；中文 Windows（GBK 代码页）下的编码；长时间 soak。
- 出口：9 台主机可见态 CPU 低于 3%，最小化低于 0.5%，与 macOS 端基线同量级。

### P8 · 可选

两端数据互导、便携模式、MSIX。不影响主线，按需要挑。

## 8. 在 Mac 上怎么验一个 Windows 应用

这是最容易被低估的部分：开发在这台 Mac 上进行，而 Windows 界面（选 A 时）在这台机器上跑不起来。

- **编译**：Core、Cli、Tests 在 Mac 上 `dotnet build` / `dotnet test`。App 选 A 靠 `EnableWindowsTargeting` 只编译不运行，类型错误和 XAML 错误当场能发现；选 B 直接运行。
- **测试**：Core 测试全部在 Mac 上跑，包括对 km 的 Live 测试。SshNetTransport 是跨平台的，在 Mac 上就能对真机测。
- **看界面**：选 A 靠 CI 的 Windows runner 跑 App.Tests 离屏渲染出 PNG，`gh run download` 拉回来看图，对应 macOS 端 ImageRenderer 的手法，同样的限制：终端控件、DataGrid 这类靠原生窗口支撑的控件离屏可能是空白，要靠真机。选 B 在 Mac 上直接开窗看，Windows 专属部分仍要真机。
- **真机，三条路**：① 本机或局域网的 Windows 电脑 / 虚拟机（Apple 芯片上 Parallels / UTM 跑 Win11 ARM64 也行）；② 那台被监控的 Windows Server 测试主机通过 RDP 跑 App，它同时是被监控的 Windows 主机，正好验 Windows 到 Windows 的采集；③ 后期在 CI 上用 FlaUI 做启动烟测加截图。
- **布局逻辑做成纯函数**：StaticGrid 分列、列宽量化、卡片权重在 macOS 端已是可单测的纯函数，Windows 端照做，界面看不到也能守住。

## 9. 风险

高风险的三条都安排在 P0 预研里先撞一下，撞不过就换备选，不带着悬念进 P1。

| # | 风险 | 等级 | 缓解与预研 |
|---|---|---|---|
| R1 | SSH.NET 对用户的某台主机不兼容：算法协商失败、密钥格式不认、ProxyJump 走不通。 | 高 | S2 用真实的密钥与主机覆盖。任何一台连不上就切 ssh.exe 备选，两条路线同一接口。 |
| R2 | 走 ssh.exe 备选时每次轮询一次握手，sshd 日志噪声。 | 高 | S1 量数字；每主机在途一个进程限流，`ConnectTimeout 10`。这是把 SSH.NET 定为默认的直接原因。 |
| R3 | 终端控件是非正式发布或较年轻的包，API 可能变；WPF 控件有 airspace 限制。 | 中 | S3 先跑通，特别是中文输入法。锁版本，控件藏在 TerminalHost 后；备选 XtermSharp + 自绘渲染器。 |
| R4 | Mac 上看不到界面（选 A），视觉回归靠猜。 | 中 | CI PNG 回路 + 真机；布局逻辑纯函数化单测。选 B 时此风险消失。 |
| R5 | 中文 Windows 的编码与本地化输出。 | 中 | 子进程 stdout 一律按 UTF-8 解码，不信控制台代码页；ping、ipconfig 这类本地化输出不解析，用 .NET API；Windows 主机的 CRLF 走 `Lines()`。 |
| R6 | WPF Fluent 主题实验状态；WPF-UI / FluentAvalonia 是第三方。 | 中 | 主题层集中在 App 的资源字典；升级 .NET 时单独看 breaking changes。 |
| R7 | 未签名自包含 exe 触发 SmartScreen 或杀软误报。 | 低 | README 说明；将来考虑 Azure Trusted Signing。 |
| R8 | 老版 Win10 的 ConPTY bug。 | 低 | 主路线不依赖 ConPTY；最低 1809、推荐 Win11。 |
| R9 | 性能陷阱：绑定风暴（对应 macOS 端的 N² 重渲染）、每 tick 动画、非虚拟化长列表。 | 中 | 每 tick 一次发布；每主机一个稳定的 ViewModel 实例，只对变化字段通知；仪表不做动画；列表虚拟化；图表自绘。 |
| R10 | Windows 电源节流（EcoQoS）拖慢后台轮询。 | 低 | P5 实测；必要时轮询期间用 `SetProcessInformation` 退出执行速度节流。 |
| R11 | `%USERPROFILE%\.ssh` 的 ACL 太开，ssh 拒绝私钥。 | 低 | 生成 / 导入密钥后 `icacls` 去继承、只留当前用户；密钥页显示 ACL 检查结果。 |
| R12 | 本机有 TUN 代理时 ICMP 被本地应答，延迟假小。 | 低 | 公网地址 RTT 低于 2 ms 视为可疑，回退主机时钟法或 `IcmpSendEcho2Ex` 绑物理网卡；在真机上量一次。 |
| R13 | 库路线丢失 ssh config 的高级项（Match、证书、PKCS#11、agent 转发）。 | 低 | 明确文档化不支持项；需要的用户按主机切 ssh.exe 备选。 |

## 10. 需要拍板

没意见的按推荐走。

1. **验证环境**：有没有 Windows 电脑或虚拟机，x64 还是 ARM64？这直接决定 D1。没有的话，以 CI 渲染图加那台 Windows Server 的 RDP 为主，节奏会慢一些。
2. **D1**：若有明确偏好请直说：必须微软第一方 → WPF；优先在 Mac 上能运行 → Avalonia。
3. **发布形式**：便携 zip + Inno Setup 安装包（推荐），要不要再加 MSIX。
4. **最低系统**：Windows 10 1809 及以上（推荐）还是只支持 Windows 11。
5. **附录 A 的仓库结构调整**是否批准，以及现在做还是 iOS 立项时做。
6. **阶段顺序**：按 P0 到 P7。想早点看到界面，可以把 P2 提前到 P1 的 Model 与 Database 做完之后并行，但终端类页面仍要等传输层。

通过后 P0 立刻开始：装 SDK、建骨架、CI 绿、三个预研出结论，然后回来对齐 D3 / D5。

## 附录 A · 仓库结构建议（待拍板）

**现状**：`backend/`、`frontend/`、`macos/`、`screenshots/`，根目录放 `docker-compose.yml`、`API.md`、`README*.md`；工作流 `deploy.yml`（web）、`macos-app.yml`。

**问题不在文件夹名，在两处**：① 顶层按「技术层」（backend / frontend）和「平台」（macos）混排，加上 windows、android、ios 后会有六七个平级目录，web 占两个而每个原生端占一个，谁和谁是一组看不出来；② 真正跨端共享的东西，远端探测脚本和真机输出 fixture，目前在 Go 与 Swift 里各有一份且已分叉（F11），第三、第四份只会更糟。

**建议的目标结构**：

```
Server-Monitor/
├── web/                 # 一个可部署单元：backend/ + frontend/ + docker-compose.yml + Dockerfile
├── apple/               # 一个 Swift Package：Core（无 UI）+ Kit（SwiftUI 共享视图）+ macos/ App；iOS 立项时加 ios/
├── windows/             # 本计划的 .NET 解决方案
├── android/             # 将来：Kotlin / Compose。与 windows/ 不共享代码，所以是平级
├── shared/
│   ├── probes/          # linux-metrics.sh · windows-metrics.ps1 · docker 格式串 · vnstat 探测；各端编译期内嵌
│   └── fixtures/        # 真机输出样本；Go / Swift / C# 的解析器测试读同一批
├── docs/                # API.md · 各端 PLAN.md · screenshots/
└── .github/workflows/   # web.yml · apple.yml · windows.yml，各自 paths 过滤
```

**两条分组原则**：按「可部署单元」分，web 是一个整体，一起用 compose 部署；按「代码共享边界」分，macOS 与 iOS 共享 Swift 包，所以是 `apple/` 而不是 `desktop/` + `mobile/`。后一种切法会把真正共享代码的两个端拆开，把不共享的 windows 与 macos 凑在一起。

**各项成本（已清点）**：

- `web/`：`deploy.yml` 两处 build context（`./backend`、`./frontend`）；部署脚本在 `/opt/server-monitor` 跑 compose，compose 若随之进 `web/` 则脚本要 `cd web`，或 compose 留在根目录不动；`.gitignore` 三行；`macos/README.md` 一处；根 README 的构建说明。这条改动碰的是生产部署链路，必须在分支上用 `workflow_dispatch` 把镜像构建跑绿再合并。deploy 任务有 `if: github.ref == 'refs/heads/main'` 保护，分支上不会真部署。
- `apple/`：`macos-app.yml` 的 paths 与 working-directory；README 中 12 处 `macos/` 链接；`bundle.sh` 路径。前提工作更大：ServerMonitorKit 目前 UI 与核心在同一个 target，7 个非 View 文件 import SwiftUI / AppKit，`SSHRunner` 依赖 spawn `/usr/bin/ssh`，而 iOS 不能起子进程，iOS 端必须换库传输。所以 `apple/` 的重构应与 iOS 立项同时做，不为假设提前付这笔账。
- `shared/`：新增目录不影响任何现有路径。Go 用 `//go:embed`、Swift 用 `resources:`、C# 用 `EmbeddedResource` 读取。SwiftPM 要求资源在 target 目录内，可用符号链接或在 `Package.swift` 里用 `path:` 指向。
- `docs/`：README 里 4 处 screenshots 链接要改。

**建议时机**：随 `windows/` 开工一次性做 `web/` + `shared/`，一次打扰，`git mv` 保留历史；`apple/` 等 iOS 立项；`docs/` 顺手。全部放一个 PR，先 `workflow_dispatch` 验证镜像构建，再合并。若不想动 web，退一步的最小方案是只加 `shared/` 与 `windows/`，顶层 README 加一张目录地图。

## 参考链接

- Win32-OpenSSH [#1328 Support for Control Master](https://github.com/PowerShell/Win32-OpenSSH/issues/1328) · [#405 ControlPath fails](https://github.com/PowerShell/Win32-OpenSSH/issues/405)
- [vscode-remote-release #96 · ControlMaster is not supported on Windows](https://github.com/microsoft/vscode-remote-release/issues/96)
- [hpc-agent #154 · 显式传 ControlMaster=no 覆盖用户配置](https://github.com/jamesdchen/hpc-agent/issues/154)
- Win32-OpenSSH [#2115 · SSH_ASKPASS on Windows 11](https://github.com/PowerShell/Win32-OpenSSH/issues/2115)（评论区：设 `SSH_ASKPASS_REQUIRE` 即可用）· [#1921 · ssh-askpass missing](https://github.com/PowerShell/Win32-OpenSSH/issues/1921)
- [Win32-OpenSSH Releases](https://github.com/PowerShell/Win32-OpenSSH/releases/)
- [SSH.NET 2023.0.0 · rsa-sha2 支持](https://github.com/sshnet/SSH.NET/releases/tag/2023.0.0) · [#825](https://github.com/sshnet/SSH.NET/issues/825) · [#1174](https://github.com/sshnet/SSH.NET/issues/1174)
- [dotnet/wpf #8545 · Mica / Acrylic](https://github.com/dotnet/wpf/issues/8545) · WPF-UI · FluentAvalonia
- [microsoft/terminal #6999 · Productize the WPF/UWP Terminal Controls](https://github.com/microsoft/terminal/issues/6999) · [EasyWindowsTerminalControl](https://github.com/mitchcapper/EasyWindowsTerminalControl)
- [XTerm.NET](https://github.com/tomlm/XTerm.NET) · [Iciclecreek.Avalonia.Terminal](https://github.com/tomlm/Iciclecreek.Avalonia.Terminal) · [Porta.Pty](https://github.com/tomlm/Porta.Pty) · [XtermSharp](https://github.com/migueldeicaza/XtermSharp)

基于 `macos/` 在提交 e946edf 时的状态与 2026-09-07 核实的外部事实整理。时长是估算，预研结论可能改动 D3 / D5。
