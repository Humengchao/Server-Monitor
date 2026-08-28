# Server Monitor

[English](README_EN.md)

基于 Web 的服务器监控平台，支持实时 SSH 终端。

使用 Claude Code & DeepSeek-v4-pro 构建

---

## 在线站点

- 🌐 [公开探针 / 服务状态页](http://svr.hmchxd.com/status)

公开探针每 15 秒刷新一次，只展示匿名节点编号、在线状态及取整后的 CPU、内存和运行时间。接口不会返回真实服务器名称、IP / 主机名、端口、SSH 用户、凭据、备注或数据库 ID。

> **安全提示：** 管理功能涉及登录令牌和 SSH 凭据，请仅在域名已配置有效 HTTPS 证书并强制跳转 HTTPS 后使用。上面的链接仅指向匿名公开状态页。

---

## 技术栈

| 层级 | 技术 |
|---|---|
| 后端 | Go + Gin + PostgreSQL |
| 前端 | React 19 + TypeScript + Vite + Ant Design |
| 实时通信 | WebSocket（SSH 终端） |
| 状态管理 | Zustand |
| 图表 | Recharts |
| 容器化 | Docker + Docker Compose |
| CI/CD | GitHub Actions → GitHub Container Registry |

---

## 主要功能

| 模块 | 能力 |
|---|---|
| 服务器面板 | 卡片 / 列表双视图（记忆选择）、搜索、按标签与在线状态筛选、按 CPU / 内存 / 磁盘 / 运行时长 / 到期时间排序 |
| 概览指标 | 总数、在线 / 离线、平均 CPU 与内存、按月折算并换算币种的支出汇总 |
| 机器详情 | 资源指标磁贴、CPU / 内存 / 网络 / 磁盘 I/O / 负载 / 延迟六组图表（自适应单位、随明暗主题切换）、时间范围预设与自定义区间、历史数据导出 CSV |
| 告警中心 | 阈值规则 + 告警时间线，支持 Webhook 通知与连通性自测 |
| 批量操作 | 多选服务器后批量打/去标签、批量删除、导出资产清单 CSV、在最多 50 台上并发执行同一条命令并逐台查看结果 |
| 进程管理 | 详情页实时进程表，可按 PID / 用户 / CPU / 内存 / RSS / 运行时长排序，支持搜索、暂停自动刷新、结束进程（SIGTERM / SIGKILL） |
| 运维操作 | SSH 网页终端、Docker 容器管理（启停 / 日志 / 交互终端）、SSH 凭据集中管理、备注 |
| 账号安全 | 修改密码，并即时登出其他设备上的会话 |
| 公开状态页 | 匿名化探针页，接口层面即排除身份与连接信息 |

### 告警

告警规则在后端独立评估（默认每 30 秒一轮，`ALERT_INTERVAL`），只读取数据库中的最新采样，不会额外连接被监控主机。

- **指标**：CPU、内存、磁盘使用率（%），1 分钟负载、网络延迟（ms），以及主机离线。
- **持续时长**：阈值需连续满足设定时长才会开启告警，避免瞬时毛刺造成误报；离线规则最短 2 分钟，与面板的在线判定保持一致。
- **作用范围**：可指定单台服务器，或留空以覆盖全部服务器（含之后新增的）。
- **通知**：触发与恢复各向配置的 Webhook 地址 POST 一次 JSON（载荷格式见 [API.md](API.md)），保存前可点「发送测试」验证链路。
- **状态持久化**：进行中的告警以数据库记录表示，服务重启既不会重复通知，也不会漏掉恢复通知；主机长时间失联时阈值类告警自动收敛为已恢复，不会永久挂起。

### 批量操作

在服务器面板点击「批量选择」进入多选，卡片与列表视图均可勾选，底部浮出操作栏。

- **批量标签 / 批量删除 / 导出清单**：标签添加是幂等的；删除会级联清理历史指标、告警记录与标签关联；导出的清单不含任何密钥（接口本身就不返回）。
- **批量执行命令**：最多 50 台，8 台并发，单台限时 60 秒、输出上限 64 KB。执行前的确认弹窗会完整列出命令和全部目标主机；命令必须为单行，含 `rm -rf`、`reboot`、写块设备等模式时会额外提示。
- 逐台返回退出状态与合并输出（stdout + stderr），**单台失败不影响其余主机**。以服务器配置的 SSH 用户执行，不会尝试 sudo 提权。

---

## 安全设计

### 密码安全

- 用户密码使用 **bcrypt**（cost factor 12）哈希存储，即使数据库泄露也无法还原明文密码
- 密码要求最少 6 位，用户名 3-64 位，防止弱口令

### SSH 凭据加密

- 所有服务器的 SSH 密码和密钥在写入数据库前使用 **AES-256-GCM** 加密
- 加密密钥独立于数据库存储（通过环境变量 `ENCRYPTION_KEY` 注入），数据库泄露也无法解密凭据
- AES-256-GCM 提供认证加密（Authenticated Encryption），同时保证机密性和完整性

### 认证与授权

- 基于 **JWT（HS256）** 的无状态认证，Token 有效期 72 小时
- **修改密码会立即吊销该账号此前签发的全部 Token**：用户表记录一个失效时间点，鉴权时拒绝 `iat` 更早的 Token。发起修改的那个会话会收到新签发的 Token，不会被自己的操作切断
- 所有 API 请求通过 Bearer Token 认证，WebSocket 通过 Query Token 认证
- 所有受保护的管理 API 查询强制按 `user_id` 过滤，**用户之间数据完全隔离**；公开探针使用独立的最小化数据模型，只返回匿名健康指标

### 接口防护

- 登录和注册接口启用 **令牌桶限流**（5 次/分钟/IP），防止暴力破解和恶意注册
- 请求参数通过结构体绑定自动校验，防止非法输入
- 支持配置 CORS 白名单，限制跨域请求来源
- 告警 Webhook **默认拒绝内网目标**：解析到 loopback / RFC1918 / link-local / CGNAT 地址的地址会在保存时被拒绝，且请求不跟随重定向，避免被当作 SSRF 跳板探测服务端内网（可用 `ALLOW_PRIVATE_WEBHOOKS=true` 显式放开）。Webhook 自测接口另有 6 次/分钟/IP 限流

### 传输安全

- 支持 TLS/HTTPS（可配置证书和私钥文件）
- 生产环境建议通过反向代理（Nginx）启用 HTTPS

### 部署安全

- Docker 镜像基于 **Alpine Linux 最小化构建**，减少攻击面
- 敏感配置（数据库密码、JWT 密钥、加密密钥）通过 **GitHub Secrets** 管理，不写入代码
- 数据库使用 `ON DELETE CASCADE` 外键约束，确保数据一致性

---

## 快速开始

```bash
# 后端
cd backend
cp .env.example .env   # 编辑配置文件
go run ./cmd/server

# 前端
cd frontend
npm install && npm run dev
```

---

## 部署

项目通过 GitHub Actions 自动构建 Docker 镜像并部署到服务器。Push 到 `main` 分支或手动触发 workflow 即可。

### 前置条件

- 服务器已安装 Docker 和 Docker Compose
- 服务器已配置 PostgreSQL 数据库
- 域名已解析到服务器 IP

### 配置 GitHub Secrets

在仓库的 **Settings → Secrets and variables → actions** 中添加以下 Secrets：

| Secret | 说明 | 示例 |
|---|---|---|
| `DEPLOY_HOST` | 服务器 IP 或域名 | `1.2.3.4` |
| `DEPLOY_USER` | SSH 登录用户名 | `root` |
| `DEPLOY_PASSWORD` | SSH 登录密码 | - |
| `POSTGRES_HOST` | PostgreSQL 地址 | `127.0.0.1` |
| `POSTGRES_PORT` | PostgreSQL 端口 | `5432` |
| `POSTGRES_USER` | PostgreSQL 用户名 | `postgres` |
| `POSTGRES_PASSWORD` | PostgreSQL 密码 | - |
| `POSTGRES_DB` | 数据库名称 | `svrmonitor` |
| `JWT_SECRET` | JWT 签名密钥（随机字符串） | `openssl rand -hex 32` |
| `ENCRYPTION_KEY` | SSH 凭据加密密钥（32 字节） | `openssl rand -hex 16` |
| `DOMAIN` | 网站域名 | `svr.hmchxd.com` |
| `GHCR_PAT` | GitHub 个人访问令牌（read:packages 权限） | 见下方说明 |

> `GITHUB_TOKEN` 由 GitHub 自动提供，无需手动配置。
>
> **GHCR_PAT 说明**：部署服务器需要从 GitHub Container Registry 拉取私有镜像，请到 [GitHub Settings → Tokens](https://github.com/settings/tokens) 创建一个 Personal Access Token (classic)，勾选 `read:packages` 权限，将生成的 token 填入此 Secret。

---

## 效果图

| 主页 / 仪表盘 |
|:---:|
| ![dashboard](screenshots/dashboard.png) |

| 机器详情 | SSH 终端 |
|:---:|:---:|
| ![server-detail](screenshots/server-detail.png) | ![ssh-terminal](screenshots/ssh-terminal.png) |


---

## TODO

- [x] **CI/CD 集成** — GitHub Actions 自动 lint / build / test / deploy
- [x] **修改机器信息** — 已添加的服务器支持修改 host / port / SSH 凭据等配置
- [x] **登录历史** — 登录成功后右下角弹窗显示上次登录的 IP、时间和地理位置
- [x] **Docker 管理** — 机器详情页可查看 Docker 容器列表及状态
- [x] **SSH 密钥管理** — 独立管理 SSH 密钥（创建、命名、关联服务器），避免重复粘贴
- [x] **SSH 账号密码管理** — 像管理 SSH 密钥一样管理通用的账号密码
- [x] **账号密码管理** — 设置页可修改密码，并即时吊销其他设备上的会话
- [x] **机器组 / 批量操作** — 标签已承担分组职责，本项落地为批量操作：批量标签、批量删除、批量执行命令、导出清单
- [x] **进程列表** — 详情页实时进程表，支持排序、搜索与结束进程
- [x] **磁盘占用** — 详情页展示当前磁盘使用占比
- [x] **告警通知** — 阈值规则（CPU / 内存 / 磁盘 / 负载 / 延迟 / 离线）+ Webhook 推送，详见下文
- [x] **公开探针** — 提供匿名化公开状态页和最小化状态 API，隐藏服务器身份与连接信息
