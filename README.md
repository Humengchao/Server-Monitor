# Server Monitor

[English](README_EN.md)

基于 Web 的服务器监控平台，支持实时 SSH 终端。

使用 Claude Code & DeepSeek-v4-pro 构建

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
- 所有 API 请求通过 Bearer Token 认证，WebSocket 通过 Query Token 认证
- 所有数据查询强制按 `user_id` 过滤，**用户之间数据完全隔离**，无法越权访问

### 接口防护

- 登录和注册接口启用 **令牌桶限流**（5 次/分钟/IP），防止暴力破解和恶意注册
- 请求参数通过结构体绑定自动校验，防止非法输入
- 支持配置 CORS 白名单，限制跨域请求来源

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
- [ ] **账号密码管理** — 用户可修改密码、管理账号安全设置
- [ ] **机器组** — 支持创建机器分组，按组筛选、批量操作
- [ ] **进程列表** — 详情页展示当前进程，支持按 CPU / Memory / 名称 / PID 排序
- [x] **磁盘占用** — 详情页展示当前磁盘使用占比
- [ ] **告警通知** — 服务器离线或 CPU 超过 80% 时，通过邮件或 Bark 推送通知
- [ ] **探针功能** — 针对账号级别提供类似https://dt.quwa.cc/的探针api或网页
