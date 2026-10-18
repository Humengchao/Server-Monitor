# Web Agent 接入与维护

Web 支持两种接入方式：已有的 SSH 凭据连接，以及由目标机器主动连接面板的 Agent。
Agent 支持 **Linux / Windows，amd64 / arm64**。它通过 HTTPS/WebSocket 建立出站反向通道，
无需在目标机器开放 SSH 入站端口，也无需把该机器的 SSH 密码或私钥上传到面板。

Agent 承载同一套 SSH channel 协议：采集、网页终端、文件管理、Docker、进程、服务、端口和远端网络探测
继续使用原有操作服务。能执行的操作取决于**目标操作系统、已安装工具和 Agent 服务身份**。

## 系统与权限

| 项目 | Linux | Windows |
| --- | --- | --- |
| 安装前提 | Bash、systemd、curl、sha256sum；root 或可用 sudo | Windows 10 1809 / Windows Server 2019 或更新版本，系统构建号 ≥17763；管理员 PowerShell |
| 默认服务身份 | `root` | `LocalSystem` |
| 持久服务 | `server-monitor-agent.service` | `ServerMonitorAgent` Windows 服务 |
| 交互终端 | PTY | ConPTY，不要求安装 OpenSSH Server |
| 配置文件 | `/etc/server-monitor-agent/config.json` | `%ProgramData%\ServerMonitorAgent\config.json` |
| 可执行文件 | `/usr/local/bin/server-monitor-agent` | `%ProgramFiles%\Server Monitor Agent\server-monitor-agent.exe` |
| Agent 自身日志 | `journalctl -u server-monitor-agent` | `%ProgramData%\ServerMonitorAgent\agent.log` |

默认的 root / LocalSystem 安装均用于完整主机管理。面板中的命令、终端和文件操作使用该服务身份，
不是浏览器所在机器的用户，也不是 Windows 当前登录桌面的用户。普通用户运行不会自动提权；
权限不足的操作会被操作系统拒绝。

Linux 安装脚本可通过 `--user <已有用户>` 指定服务用户。Windows 脚本首次创建服务时使用 LocalSystem；
需要普通账户时，可通过 Windows 服务管理器配置服务登录身份及所需目录 ACL，脚本没有 `--user` 参数。
升级保留现有服务身份，重新登记会为现有 Windows 服务身份补充配置访问权限。

Docker 操作仍需目标主机已安装 Docker，且服务用户可访问 Docker daemon。网络检查的 Linux 来源需具备
可用的 `ping` 及 ICMP 权限。日志排障页的系统日志入口使用 Linux journald；Windows 系统日志目前显示
“不支持”，不会伪装成空日志。Windows 的 Docker 日志仍支持 stdout/stderr 读取。

## 面板部署要求

将面板部署为具有有效证书的 HTTPS 站点，并配置后端：

```dotenv
PUBLIC_URL=https://monitor.example.com
AGENT_ASSETS_DIR=/app/agent-assets
```

`PUBLIC_URL` 是包含协议的站点 origin，不包含子路径、查询参数或用户信息。
Agent 仅允许 HTTPS；本机开发的 localhost/127.0.0.1 可使用 HTTP。目标机器须能访问该站点，
反向代理须允许 `/api/agent/control` 和 `/api/agent/data` 的 WebSocket Upgrade。
项目自带的前端 Nginx 配置包含相应代理规则。

当前 Agent 通道注册表保存在后端进程内，**仅支持单个 backend 实例**。
普通多副本负载均衡可能把控制连接、数据连接和管理请求分配到不同进程，无法共享 Agent 通道；
扩展为多实例前需要增加共享会话路由能力，不能只增加 backend 副本数。

后端镜像随构建打包四种平台二进制及安装脚本，安装时从**当前面板**下载并校验 SHA-256。
自行运行后端时，须提供同样的 assets 目录；否则下载接口会报告二进制或安装器不可用。
安装脚本源码位于 [web/backend/agent-assets](../web/backend/agent-assets/)，Agent 入口位于
[web/backend/cmd/agent](../web/backend/cmd/agent/)。

## 首次接入

1. 在服务器面板选择“添加服务器”，接入方式选择“安装 Agent”，填写名称并选择 Linux 或 Windows。
   连接地址可留空；Agent 接入不需要填写 SSH 用户、密码或私钥。新服务器默认公开，可手动关闭“在公开状态页显示”。
   如果要将它设为网络检查的**目标**，请填写探测来源可达的该服务器 IP 或域名；不要把 NAT 网关或面板代理地址当作目标主机地址。
   留空时不会从登记请求的对端 IP 自动推断地址，网络检查会提示目标地址无效；Agent 采集、终端和文件操作不受影响。
2. 保存后页面会进入服务器详情。在“Agent 连接”卡片点击“生成安装命令”。
3. 在目标 Linux 主机执行 Linux 命令；Windows 使用**管理员 PowerShell**执行 Windows 命令。
   面板生成的命令会先下载安装器、检查失败状态，再执行登记安装；命令使用 `reenroll`，兼容首次安装和重新登记。
4. 等待卡片显示“已连接”，核对版本与运行用户；随后检查采集、终端、文件和所需管理功能。

登记命令有效期为 **15 分钟**，票据只能使用一次。再次生成会使同一主机之前尚未使用的票据失效。
页面会显示到期时间，过期后禁用复制并提示重新生成。不要把带票据的命令放进公开工单或仓库。
长期 Agent 令牌保存在目标机器配置中；数据库仅保存其哈希。

## 升级、重新登记与卸载

以下示例中的 `install.sh` / `install.ps1` 是从你自己的面板下载的安装器。
把示例域名替换为实际 HTTPS origin；这些命令在目标机器执行，不在面板容器中执行。

Linux 下载安装器：

```bash
curl -fsSL https://monitor.example.com/api/agent/install.sh -o install.sh
```

Windows 管理员 PowerShell 下载安装器：

```powershell
Invoke-WebRequest -UseBasicParsing https://monitor.example.com/api/agent/install.ps1 -OutFile .\install.ps1
```

| 操作 | Linux | Windows 管理员 PowerShell |
| --- | --- | --- |
| 升级，保留登记身份 | `sudo bash ./install.sh upgrade` | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Action upgrade` |
| 重新登记到指定面板 | `sudo bash ./install.sh reenroll --server-url https://monitor.example.com` | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Action reenroll -ServerUrl https://monitor.example.com` |
| 卸载服务与程序，保留配置 | `sudo bash ./install.sh uninstall` | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Action uninstall` |
| 卸载并清理配置 | `sudo bash ./install.sh uninstall --purge-config` | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Action uninstall -PurgeConfig` |

已是 root 时省略 `sudo`。不传票据时，重新登记命令会交互询问票据；也可直接使用详情页生成的完整命令。
`upgrade` 从现有配置读取面板地址，下载当前面板提供的版本，校验文件与配置兼容性后替换程序并重启服务。
它不申请新登记票据。Agent 目前不会自行定时更新。

重新登记需要先在面板生成新命令/票据，会替换该服务器原有 Agent 令牌和主机身份并断开旧通道。
`install` 是仅针对无配置机器的脚本动作；已有配置时应使用 `upgrade` 或 `reenroll`。

卸载主机上的程序不会代替面板撤销授权。在详情页点击“撤销 Agent 访问”并确认，才会撤销已登记令牌和未使用票据、
关闭当前通道。仅在面板撤销不会删除目标机器上的服务或配置；重新使用需新票据。

## 检查与排障

Linux：

```bash
systemctl status server-monitor-agent
journalctl -u server-monitor-agent -n 100 --no-pager
sudo /usr/local/bin/server-monitor-agent check --config /etc/server-monitor-agent/config.json
```

Windows 管理员 PowerShell：

```powershell
Get-Service ServerMonitorAgent
Get-Content -LiteralPath "$env:ProgramData\ServerMonitorAgent\agent.log" -Tail 100
& "$env:ProgramFiles\Server Monitor Agent\server-monitor-agent.exe" check --config "$env:ProgramData\ServerMonitorAgent\config.json"
```

`check` 只验证本地配置能否读取，不表示面板在线；在线状态以详情页连接状态和实际采集为准。
如果服务正常但未连接，检查 HTTPS 证书、目标机器出站网络、代理 WebSocket 配置及票据/令牌是否已撤销。
如果只有某类管理操作失败，先核对服务身份及主机上相应工具和权限。

## 配置恢复后的 Agent

[配置备份](backups.md)不导出 Agent 长期令牌、主机私钥或活跃通道。恢复到当前或新面板会创建新的服务器 ID，
Agent 主机需要从**恢复后那条服务器记录**生成命令并重新登记。
恢复记录默认私有、暂停采集和自动续期；核对接入身份后再按需启用。旧面板的 Agent 连接不会自动迁移到新记录。
