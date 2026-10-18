# 全端代码审查记录（2026-10-18）

后续修复见 [修复与验证记录](review-fixes-2026-10-18.md)。下文保留审查时的原始发现与当时测试结果，代码行号可能已因修复移动。

本次以工作区当前内容为准，包括审查开始前已有的未提交修改。覆盖 Web 前后端、Windows Core/WPF/CLI、macOS SwiftUI 客户端、共享探针与发布配置。只新增本报告，未修改业务代码、现有测试或用户原有修改。

以下优先级结合后果与触发条件：P1 优先处理，涉及错误文件操作、信任校验、配置覆盖或核心操作失败；P2 随后处理，涉及状态准确性、竞态和资源效率。代码路径后的行号对应本次工作区，后续修改可能使行号移动。静态确认不代表已经完成实机复现；具体证据分别注明。

**验证结果**

| 范围 | 结果 | 边界 |
|---|---|---|
| Go 后端 | `go test ./...`、`go vet ./...` 通过 | 测试使用本地替身；本轮未执行 race 检测或真实 PostgreSQL 压测 |
| Windows | 508 通过，14 项 live 测试跳过，0 失败 | Core 397、App 111；关闭 live 环境变量，未操作真实主机 |
| Web 前端 | build、lint 通过；Playwright 82 通过、1 跳过、1 失败，共 84 项 | 跳过生产数据库 readiness；失败是菜单定位器不匹配，见文末 |
| 发布脚本 | `web/scripts/deploy_test.sh` 通过 | Docker/网络操作为 mock；Windows 缺少 flock，真实锁行为需 Linux CI 验证 |
| macOS | 完成静态调用链与测试审查 | 当前 Windows 环境无法运行 AppKit/SwiftUI 测试 |
| 补充验证 | 临时 Go overlay、离线 .NET harness、Chromium mocked API | 未向仓库增加临时测试，未执行真实 SSH、Docker 或远端文件操作 |

**优先处理的问题**

**R01 · P1 · macOS：文件名解析改变实际操作路径，可能误删另一个目录。**

位置：[SFTPClient.swift:78](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/SSH/SFTPClient.swift:78)、[删除操作:132](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/SSH/SFTPClient.swift:132)。目录列表把 `ls` 行按空格拆开，再用单空格拼回文件名。例如 `keep  backup` 被还原为 `keep backup`，后续删除、重命名用的是重建后的路径；如果两个目录同时存在，可能作用于错误对象。Shell 引号无法修复已经失真的路径。

建议改用真实 SFTP 目录协议，或能原样保留文件名的结构化格式；对连续空格、换行、制表符和符号链接名称补覆盖。证据为完整调用链及本地 split/join 演示，未执行远端删除。

**R02 · P1 · Windows：主机密钥校验在读取错误时允许连接。**

位置：[KnownHosts.cs:142](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/KnownHosts.cs:142)、[SshNetTransport.cs:548](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SshNetTransport.cs:548)。`known_hosts` 读取异常返回空或部分记录，随后将未读到的已有主机判断为 `Unknown`，自动信任。同时，校验调用没有携带端口，无法匹配已有 `[host]:2222` 记录；`@revoked` 记录也被忽略。

离线实测：同一变更密钥在文件可读时为 `Changed`，独占锁定文件后为 `Unknown`；非默认端口及 revoked 案例也返回 `Unknown`，调用方会设置 `CanTrust=true`。建议将文件不存在、读取失败、未知密钥、变更密钥、已撤销密钥分开处理，读取失败拒绝连接，并统一端点标识。

**R03 · P1 · Windows：批量下载的文件名冲突会静默覆盖文件。**

位置：[SftpPane.cs:453](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/Views/SftpPane.cs:453)、[SftpBrowser.cs:117](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SftpBrowser.cs:117)、[完成下载:273](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SftpBrowser.cs:273)。Linux 文件名 `a:b.txt` 与 `a?b.txt` 经当前函数都变成 `a_b.txt`，最后以 `overwrite:true` 写入。大小写差异也可能在 Windows 上冲突。多选目录路径没有单文件保存对话框的覆盖提醒。

已离线调用实际名称转换函数确认冲突。建议传输前同时检查批内冲突与本地已有文件，使用唯一名称或明确的覆盖选择，按目标文件系统规则比较。

**R04 · P1 · Windows：SSH 重连成功后仍可能必然抛出已释放对象异常。**

位置：[SshNetTransport.cs:128](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SshNetTransport.cs:128)、[释放 gate:602](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SshNetTransport.cs:602)。执行方持有旧 `pooled.Gate` 时遇到断链，重连路径先驱逐并 Dispose 旧 gate；新连接执行成功后，外层 `finally` 仍对旧 gate 调用 `Release()`，覆盖成功结果。

离线调用实际 `Close()` 后再释放 gate，得到 `ObjectDisposedException`。建议分离连接退役与同步对象生命周期，待持有者和等待者退出后再释放；补断链后成功重试的完整回归。

**R05 · P1 · Windows：活动中的长文件传输会被空闲回收器关闭。**

位置：[SshNetTransport.cs:648](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SshNetTransport.cs:648)、[租用连接:693](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SshNetTransport.cs:693)、[SftpBrowser.cs:254](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SftpBrowser.cs:254)。SFTP `LastUsed` 仅在租用时更新，传输期间没有活动计数或续租。若没有其他操作续用该连接，300 秒空闲阈值加 60 秒扫描周期会在约 5–6 分钟后关闭仍在传输的连接。上传直接覆盖目标，还可能留下不完整文件。

静态确认完整租用、传输、回收路径，未跑长时间远端传输。建议用活动租约覆盖整个传输，只有活动数为零才允许回收；上传采用临时文件完成后替换。

**R06 · P2 · Windows / macOS：隐藏窗口的渲染优化冻结了托盘和菜单栏状态。**

位置：[Windows MonitorService.cs:1015](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Collect/MonitorService.cs:1015)、[托盘订阅:211](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/App.xaml.cs:211)、[macOS MonitorService.swift:751](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Collect/MonitorService.swift:751)、[MenuBarPanel.swift:155](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/View/MenuBarPanel.swift:155)。两端隐藏窗口时都暂缓发布 status/latest，托盘或菜单栏恰好读取同一份状态，因此驻留期间显示旧 CPU、旧离线数。采集、历史与独立告警路径仍继续。

Windows fake 轮询实测：隐藏时失败结果对应 `offline=0, publications=0`，重新显示后变为 `offline=1, publications=1`；macOS 为静态确认。建议持续发布轻量监控状态，将昂贵主窗口重绘单独节流。

**R07 · P1 · Web 全链路：批量命令时间预算不一致，报超时后还会继续执行。**

位置：[前端 servers.ts:215](C:/Users/81024/code/Server-Monitor/web/frontend/src/api/servers.ts:215)、[nginx.conf:110](C:/Users/81024/code/Server-Monitor/web/frontend/nginx.conf:110)、[后端 batch.go:107](C:/Users/81024/code/Server-Monitor/web/backend/internal/services/batch.go:107)。前端 120 秒、代理 150 秒超时，但后端最多 50 台、8 并发、每台命令 60 秒，接近超时时仅命令执行就可能需要 7 批约 420 秒，连接耗时另算。等待槽和命令执行没有继承 HTTP 请求取消，用户看到失败后剩余主机仍可能开始执行，重试会重复操作。

小批次快命令通常正常；该问题针对大批次慢命令。建议改成返回 job ID 的异步任务，展示进度并可查询最终结果；若保留同步模式，应统一预算并定义排队取消、已执行结果与幂等语义。关闭 SSH channel 不等于保证远端进程已经结束。

**R08 · P1 · Web：保存备注会覆盖另一标签页更新的连接配置。**

位置：[ServerDetail.tsx:263](C:/Users/81024/code/Server-Monitor/web/frontend/src/pages/ServerDetail.tsx:263)、[server.go:263](C:/Users/81024/code/Server-Monitor/web/backend/internal/models/server.go:263)。备注保存将页面加载时的旧 name、host、port、credential_id、主机密钥、计费等字段一起 PUT，后端没有版本条件。A 页编辑备注期间，B 页修改主机地址，A 保存备注就会把地址改回去。

Chromium mocked API 捕获真实请求：服务端已改成 `edited-in-other-tab / 192.0.2.2`，保存备注仍提交 `before-edit / 192.0.2.1`；后端 SQL 已独立核对。建议提供备注专用 PATCH，一般编辑增加版本号或 ETag 冲突检查。

**R09 · P2 · Windows：Docker 切换主机后旧响应污染新页面。**

位置：[DockerPage.cs:79](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/Views/DockerPage.cs:79)、[加载:455](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/Views/DockerPage.cs:455)、[操作:357](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/Views/DockerPage.cs:357)。A 加载未完成时切 B，两套请求直接写共享字段，没有请求代次校验。A 的迟到列表能显示在 B 名下，操作又使用当前 `_host`，形成 A 的容器 ID 发往 B。通常报不存在，显示与执行目标已经不一致。

建议切换时取消读取并校验 host/generation，每个操作绑定产生该行的主机。证据为静态调用链，未操作真实 Docker。

**R10 · P2 · Windows：关闭 SFTP 页不能取消已开始的传输。**

位置：[SftpPane.cs:175](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/Views/SftpPane.cs:175)、[SftpBrowser.cs:263](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.Core/Ssh/SftpBrowser.cs:263)。页面取消 token，但 token 仅用于租连接，实际 BeginUploadFile/BeginDownloadFile 没有取消注册。会话已记结束，文件仍可能继续读写。

建议使用真正可取消的传输流程，等待传输退出后结束会话；取消一个操作时应保留其他共享会话。静态确认，未做真实上传。

**R11 · P2 · macOS：手动设置非默认端口时 scp 参数错误。**

位置：[SSHRunner.swift:155](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/SSH/SSHRunner.swift:155)、[SFTPClient.swift:195](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/SSH/SFTPClient.swift:195)。SSH 参数中的 `-p 2222` 被直接复用于 scp；scp 的端口参数是 `-P`，小写 `-p` 表示保留属性，数字会被当作路径。通过 ssh config alias 提供端口的路径不同，不应概括为所有非 22 端口都失败。

建议共用 `-o Port=...` 或分别构造参数，并覆盖上传和下载。已核对代码与 [OpenSSH 官方 scp 手册](https://man.openbsd.org/OpenBSD-7.7/scp.1)。

**R12 · P2 · macOS：唤醒通知订阅了错误的通知中心。**

位置：[MonitorService.swift:560](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Collect/MonitorService.swift:560)。当前在 `NotificationCenter.default` 注册 `NSWorkspace.didWakeNotification`；该通知应使用 `NSWorkspace.shared.notificationCenter`，否则无法收到。唤醒后的退避清理不会按设计触发，恢复采集可能继续等待退避。

建议修正事件来源，补通知驱动测试，而不仅直接调用 `systemDidWake()`。依据为代码与 [Apple 官方说明](https://developer.apple.com/documentation/appkit/nsworkspace/didwakenotification)，未进行 macOS 睡眠唤醒实测。

**R13 · P2 · macOS：“测试连接”没有使用尚未保存的表单密码。**

位置：[ServerEditorView.swift:317](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/View/ServerEditorView.swift:317)、[SSHRunner.swift:219](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/SSH/SSHRunner.swift:219)。测试构造密码认证目标，但执行器从 Keychain 读取密码，没有接收当前表单值。新建时可能测试失败，编辑时测试旧密码；复用已有 ControlMaster 还可能绕过本次认证验证。

建议使用独立临时认证上下文和新连接验证，不覆盖已保存凭据。证据为静态数据流追踪。

**R14 · P2 · macOS：冷却期内的新一轮超限可能在冷却结束后也不通知。**

位置：[AlertService.swift:131](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Alerts/AlertService.swift:131)、[冷却判断:151](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Alerts/AlertService.swift:151)。仅在连续超限次数恰好等于 3 时尝试发送。首次报警、恢复、冷却期内再次持续超限时，第 3 次被冷却阻止，此后次数超过 3，再也不尝试；持续异常跨过冷却期也无法补发。

建议记录本轮是否成功通知，达到门槛后在冷却结束时仍可发送；注入时钟验证完整序列。证据为状态机静态推演，未运行 Swift 测试。

**R15 · P2 · Web：旧请求的迟到 401 会删除刚轮换的新 token。**

位置：[client.ts:42](C:/Users/81024/code/Server-Monitor/web/frontend/src/api/client.ts:42)、[Settings.tsx:94](C:/Users/81024/code/Server-Monitor/web/frontend/src/pages/Settings.tsx:94)。改密码成功安装新 token 后，携带旧 token 的在途请求返回 401，拦截器不核对请求会话，直接 expire 当前会话。

Chromium 已复现：先确认 replacement token 已保存，再释放旧请求的 401，结果 localStorage token 变为 null 并显示会话过期。建议比较失败请求的 Authorization 与当前 token，或使用会话代次，忽略过期会话的迟到响应。

**R16 · P2 · Web：图表等距抽样会漏掉峰值和最后一个点。**

位置：[MetricsChart.tsx:19](C:/Users/81024/code/Server-Monitor/web/frontend/src/components/MetricsChart.tsx:19)、[调用:164](C:/Users/81024/code/Server-Monitor/web/frontend/src/components/MetricsChart.tsx:164)。超过 300 点后按索引等距取样。直接提取当前源码函数执行：600 点中只有索引 301 的 CPU 为 100，显示后的最大值为 0；源末点 599 也被丢弃，显示止于 598。这是对已经返回的 API 数据再次丢峰，不包含后端聚合造成的信息损失。

建议保留首尾，使用保峰分桶 min/max 等时序抽样，并考虑不同指标的峰值可能出现在不同点。

**R17 · P2 · Web：告警中心同时超过 200 个活动事件时计数和列表不完整。**

位置：[Alerts.tsx:124](C:/Users/81024/code/Server-Monitor/web/frontend/src/pages/Alerts.tsx:124)、[计数:409](C:/Users/81024/code/Server-Monitor/web/frontend/src/pages/Alerts.tsx:409)、[后端排序:274](C:/Users/81024/code/Server-Monitor/web/backend/internal/models/alert.go:274)。页面固定取 200 条，再用这页数组推导活动总数，没有服务端分页。实际 SQL 活动事件优先，因此不是“新恢复记录挤掉旧活动记录”；正确触发条件是 201+ 同时活动。活动记录还可能占满结果，使已恢复历史无法查看。

遵守真实 SQL 排序的 Chromium mock 已复现 201 个实际活动事件只显示 200。规则徽标使用 firing_count，本身正确。建议总数来自摘要接口，活动/已恢复历史分别过滤并提供游标分页，不能只提高 limit。

**R18 · P2 · Web 后端：公共状态页缓存失效时会重复执行重查询。**

位置：[public_status.go:85](C:/Users/81024/code/Server-Monitor/web/backend/internal/handlers/public_status.go:85)、[查询模型:55](C:/Users/81024/code/Server-Monitor/web/backend/internal/models/public_status.go:55)。缓存 miss 后先解锁，查询完成才回填，没有合并正在进行的刷新。并发访客同时错过两秒缓存，会各自查询全量节点及 30 天可用率。

临时 Go overlay 使用真实 handler 和 database/sql mock，确认 12 个同时访问的请求进入了 12 次 SQL；每 IP 限流不能合并这些请求。建议 singleflight 合并刷新，或返回旧值并后台单次更新，同时给查询传递超时 context。查询减少可确定，绝对延迟收益仍需真实数据压测。

**进一步优化，按收益验证后安排**

| 方向 | 现状和建议 | 证据与位置 |
|---|---|---|
| Webhook 投递可靠性 | 告警先落库并进入 firing，Webhook 只尝试一次，失败只记日志；同一持续事件以后不再发送。事件本身没有丢，丢的是外部通知。可先做有限退避重试；需要跨重启可靠性时使用持久化 outbox 和幂等事件 ID。 | [alerts.go:199](C:/Users/81024/code/Server-Monitor/web/backend/internal/services/alerts.go:199)、[Send:432](C:/Users/81024/code/Server-Monitor/web/backend/internal/services/alerts.go:432)，静态确认并经独立复核。 |
| Windows 传输进度节流 | 当前在 Progress 回调内部每 100 次更新一次，但所有 Report 已先排入 UI 队列。应在 Post 前按时间合并，只保留最新进度及完成事件。 | [SftpPane.cs:496](C:/Users/81024/code/Server-Monitor/windows/src/ServerMonitor.App/Views/SftpPane.cs:496)。离线实测 10,000 次 Report 仍产生 10,000 次 Post；实际 CPU 收益需传输基准。 |
| 统一三端探针和数据契约 | shared/probes 当前只有 Windows 嵌入，Go 与 Swift 仍保留副本；Go Windows CPU 脚本仍用 LoadPercentage，shared/Swift 使用 PerfRawData 双采样。应共享生成来源、明确输出版本，并用同一 fixture 验证各端解析。接入共享资源时同步更新构建上下文与 CI 触发路径。 | [shared/probes/README.md](C:/Users/81024/code/Server-Monitor/shared/probes/README.md)、[Go 脚本:1071](C:/Users/81024/code/Server-Monitor/web/backend/internal/services/metrics.go:1071)、[共享脚本:26](C:/Users/81024/code/Server-Monitor/shared/probes/windows-metrics.ps1:26)。这是已存在的实现分歧，不能直接推断哪端具体误差多少。 |
| 大规模 Web 渲染 | 每秒时钟更新会带动 Dashboard 过滤、排序与汇总，卡片比较还会序列化指标/标签。建议以 100/500/1000 台样本测量，再决定结构共享、列表虚拟化和状态过期调度。 | [Dashboard.tsx:259](C:/Users/81024/code/Server-Monitor/web/frontend/src/pages/Dashboard.tsx:259)。未做规模基准，不标为已经证明的卡顿。 |
| 大文本编辑 | 每次击键全量转字符串，父组件继续计算行数、UTF-8 长度与换行格式；对接近 2 MiB 文件应测试输入延迟，再考虑增量计算与节流。 | `web/frontend/src/components/FileCodeEditor.tsx`、`FileManager.tsx`。未做输入延迟基准。 |

现有实现已经包含 SSH 连接复用、采集退避、指标分层存储、请求去重/取消、路由拆包和编辑器按需加载。应保留这些收益。AntD 最大构建块约 1,244 kB、gzip 381 kB，有体积警告，但相关 bundle 测试通过；仅凭警告不足以证明首屏性能存在问题。

**较低优先级及需实机补验的项目**

- Web Docker 操作完成后缺少页面作用域校验，离开页面后仍可能发起一次容器列表读取。[Docker.tsx:510](C:/Users/81024/code/Server-Monitor/web/frontend/src/pages/Docker.tsx:510)。Chromium mock 已确认离开到 Credentials 后 inventory 请求由 1 次增至 2 次；建议操作完成后检查页面/主机代次再刷新。
- macOS 终端的 view 拆除会终止 SSH，而侧栏选择会切换 view；SessionManager 主要保存元数据。[RootView.swift:316](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/View/RootView.swift:316)、[SSHTerminalView.swift:112](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Terminal/SSHTerminalView.swift:112)。需实机验证切页后终端进程、工作目录和缓冲区是否保留，再将会话生命周期与视图分开。
- macOS 编辑目标时旧对象取值和缓存失效范围值得修正；previous 从新对象构造，forget 未清理全部 CPU 型号/Docker 缓存。[MonitorService.swift:112](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Collect/MonitorService.swift:112)、[MetricsCollector.swift:354](C:/Users/81024/code/Server-Monitor/macos/Sources/ServerMonitorKit/Collect/MetricsCollector.swift:354)。建议按连接目标版本清理，并丢弃旧版本在途结果；需实机验证换主机流程。

**测试缺口与后续顺序**

当前前端回归唯一失败在 [firing-alerts.spec.ts:81](C:/Users/81024/code/Server-Monitor/web/frontend/tests/firing-alerts.spec.ts:81)：测试精确寻找 `Credentials`，实际可访问名包含图标，成为 `key Credentials`；后面的 `Servers` 同样存在图标名前缀。测试尚未点击导航，因此不能据此认定请求取消机制失败。应修正定位器，并补覆盖 token 轮换、跨页修改与告警分页的行为测试。

建议先处理 R01–R05、R07–R08 的数据安全与操作可靠性，再处理双端后台状态、Docker 竞态和告警准确性，最后做缓存、进度和规模渲染的性能测量。原生端应补异常网络、活动连接回收与文件名边界案例；Web 应补覆盖真实排序和更新语义的接口集成测试，避免 mock 与后端契约不一致。

本次没有做生产数据库执行计划分析、依赖漏洞全量审计、真实服务器网络压测、macOS 实机终端/睡眠测试。临时验证未支持的疑点未列为确定问题，例如取消 SFTP 子系统初始化在本地测试中正常返回，已排除该初步猜测。
