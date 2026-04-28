# Server Monitor

Web-based server monitoring with real-time SSH terminal.

Built with ❤️  Claude Code & DeepSeek-v4-pro
## Quick Start

```bash
# Backend
cd backend
cp .env.example .env   # edit with your config
go run ./cmd/server

# Frontend
cd frontend
npm install && npm run dev
```

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

- [ ] **CI/CD 集成** — GitHub Actions 自动 lint / build / test / deploy
- [ ] **修改机器信息** — 已添加的服务器支持修改 host / port / SSH 凭据等配置
- [ ] **登录历史** — 登录成功后右下角弹窗显示上次登录的 IP、时间和地理位置
- [ ] **Docker 管理** — 机器详情页可查看 Docker 容器列表及状态
- [ ] **SSH 密钥管理** — 独立管理 SSH 密钥（创建、命名、关联服务器），避免重复粘贴
- [ ] **SSH 账号密码管理** — 像管理 SSH 密钥一样管理通用的账号密码
- [ ] **账号密码管理** — 用户可修改密码、管理账号安全设置
- [ ] **机器组** — 支持创建机器分组，按组筛选、批量操作
- [ ] **进程列表** — 详情页展示当前进程，支持按 CPU / Memory / 名称 / PID 排序
- [ ] **磁盘占用** — 详情页展示当前磁盘使用占比
- [ ] **告警通知** — 服务器离线或 CPU 超过 80% 时，通过邮件或 Bark 推送通知
