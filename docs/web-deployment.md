# Web 发布验证与回滚

## 流水线

1. 校验：后端 `go test -race ./...`、`go vet ./...`、隔离发布脚本测试；前端 lint、生产构建与 Chromium 浏览器测试。未通过不构建/发布镜像。
2. 镜像使用本次完整 commit SHA 标签。服务器 fetch/archive 该 SHA 到独立发布目录，不执行 pull main，不覆盖线上工作树。
3. GitHub Actions 整条生产流水线串行（不取消运行中的发布）；服务器另有 `flock`，手动发布与自动发布不会同时切换容器。
4. 切换前保存旧 Compose 配置，并将旧容器实际镜像 ID 打为本地回滚标签；先拉完新镜像，再启动并等待健康。
5. 验证前端代理后的 `/api/ready`（真实数据库 ping）及 SPA 页面；再从 Actions 对生产 HTTPS 域名运行浏览器冒烟。任何一步失败恢复上一版本；首次部署无旧版本时停止失败的候选容器。

浏览器测试使用生产构建/生产域名的真实静态资源，覆盖登录、列表、机器详情、Docker、文件编辑、防丢与操作交互，并捕获运行时错误。业务 API 使用隔离模拟数据，**不会登录真实账户或更改线上文件/容器**；只有 `/api/ready` 请求真实后端。真实 HTTP 上传进度测试在发布前的本地接收器执行，生产冒烟跳过此项；完整真实 SSH/SFTP 权限验收仍需运维账户验证。失败的截图/trace 随 workflow artifact 保留。

## 配置与首次迁移

默认根目录 `/opt/server-monitor`：

- `shared/.env`：长期生产配置，部署不重写；发布脚本不会恢复注册开关、可信代理或采集间隔的默认值。
- `releases/<SHA>/`：本次提交代码及仅包含 BACKEND_IMAGE/FRONTEND_IMAGE 的 `images.env`。
- `current`：当前已通过服务器本地验收的发布目录符号链接；外部冒烟失败仍会切回上一版。
- `shared/deploy.lock`：服务器发布互斥锁。

从旧部署升级时，只在 `shared/.env` 不存在时复制原 `web/.env`（权限 0600），此后始终保留 shared 版本。旧 .env 中即使存在镜像变量，也由后加载的 images.env 覆盖。工作树本身不再更新，运维命令应指向 current，不要在旧 web 目录直接 up。

全新部署必须由运维预先创建 `shared/.env`；缺少时流水线明确失败，不会生成默认开启注册的配置。数据库、域名反向代理及外部 Docker `proxy` 网络也须事先准备。要求 Linux、Bash、git、tar、curl、flock，以及支持多份 --env-file、up --wait 的 Docker Compose v2。

配置键：POSTGRES_HOST/PORT/USER/PASSWORD/DB/SSLMODE、JWT_SECRET、ENCRYPTION_KEY、DOMAIN，以及 ALLOW_REGISTRATION、TRUSTED_PROXIES、POLL_INTERVAL、ALERT_INTERVAL、ALLOW_PRIVATE_WEBHOOKS。数据库地址须可从容器访问；远端数据库通常应配置 POSTGRES_SSLMODE=require。首次建好账户后建议 ALLOW_REGISTRATION=false。**保留现有 ENCRYPTION_KEY；不要直接替换，否则历史凭证无法解密。JWT_SECRET 则必须是独立生成的强随机秘密，至少 32 字节，弱密钥将拒绝启动。**

GitHub Secrets 只保留部署连接与验收所需：DEPLOY_HOST、DEPLOY_USER、DEPLOY_KEY（专用部署私钥）、DEPLOY_HOST_FINGERPRINT（独立可信渠道核验的 SHA256 指纹）、DOMAIN（HTTPS 冒烟域名）和私有镜像所需 GHCR_PAT。缺少密钥或指纹时流水线在连接前失败；部署和回滚都校验同一指纹，不再使用 DEPLOY_PASSWORD。服务器 shared/.env 的 DOMAIN 应与该 Secret 一致。旧数据库/应用密钥 Secrets 不再自动写入服务器；修改 GitHub Secret 不会自动修改持久配置。

## 2026-10 安全迁移

本次升级会要求用户重新登录：JWT 必须携带有效期限、签发时间及数据库会话版本；改密码会撤销旧 token 并关闭旧终端。弱 JWT_SECRET、公开样例或与 ENCRYPTION_KEY 相同的值不能启动。先在受保护的配置文件中换成独立随机签名密钥，再部署；不要把秘密放入日志或提交到仓库。

SSH 默认要求预先固定可信主机公钥，Windows 密码登录也一样。通过已有可信 known_hosts 或主机控制台取得并核对公钥后，填写服务器的主机密钥字段。不要把未经验证的即时 ssh-keyscan 结果自动当作可信密钥。确需分阶段迁移时，可以明确设置 `SSH_ALLOW_UNVERIFIED_HOST_KEYS=true` 暂时保留旧行为，日志会告警；完成固定后关闭该例外。它只用于迁移，不提供主机身份保护。

Compose 的 19080 / 19527 现在仅绑定宿主机 loopback；域名 HTTPS 入口仍经 Docker `proxy` 网络访问前端。更新前确认外层代理已连到该网络。项目没有管理共用 PostgreSQL 实例的 Compose；数据库公网端口应在核对其他应用、备份和管理连接后，单独改为私有网络、VPN 或来源白名单，不能在应用升级中盲目关闭。

发布脚本会收紧原 `web/.env` 和发布配置快照的权限。文件权限修复不能撤销历史已读出的秘密：数据库账号密码及 JWT 密钥应在独立维护窗口轮换；ENCRYPTION_KEY 若需轮换，必须先制定完整重加密、备份恢复和回滚步骤，禁止直接修改环境变量。SSH/API 等外部凭证轮换需要各服务的授权渠道。

AI 标准模式下，普通文件读取先逐文件审批，只有少量公开系统标识文件自动读取；Redis/ACL 等凭据文件禁止进入模型。配置文件写入需审批，包括 Nginx 上游改变。通知目的地身份改变必须重新提供秘密或明确清除；携带 Bearer Token 的 webhook 只允许 HTTPS。

运维修改 shared/.env 后，若要立即应用当前版本：

```bash
cd /opt/server-monitor
docker compose --project-name server-monitor --env-file shared/.env --env-file current/images.env -f current/web/docker-compose.yml up -d --wait --wait-timeout 180
```

配置变更也应备份并避开正在进行的发布；上述手动命令不自动回滚配置，建议优先在下一次流水线发布中应用。

## 回滚与维护

生产冒烟失败自动执行：

```bash
bash /opt/server-monitor/deploy-runner.sh rollback <失败发布的40位SHA>
```

仅当该 SHA 仍是 current 才回滚，避免旧任务影响新发布；已回滚的请求会安全跳过。回滚使用保存的配置和本地镜像，`--pull never`，不依赖镜像仓库。回滚命令失败会明确输出 ROLLBACK FAILED，并保留当前链接供排障；请立即检查 Compose 状态、端口、数据库和主机资源。未通过本地验收的发布本身返回失败，即使恢复成功也不会把流水线标成成功。

同一 SHA 已上线时重跑脚本为幂等操作（仍由 Actions 做外部冒烟），不是强制重启；此前失败的同 SHA 可重新尝试，重新记录真实前一版。

**回滚仅覆盖应用代码、镜像及容器配置，不回退数据库或服务器文件。**本次数据库变更为新增审计表；未来破坏性迁移必须单独制定备份/恢复和兼容发布方案。此方案不是蓝绿零停机发布，切换可能短暂中断服务。

发布目录中的 previous.yml/rollback.yml 是包含秘密的完整配置快照，脚本以 umask 077 创建，不能上传为公开 artifact。不要随意 prune 回滚镜像；保留 current、其前一版本、回滚配置/镜像与 shared/.env，确认没有发布运行后再按保留策略清理旧发布。备份 shared/.env 和数据库至安全位置，磁盘不足时不要继续发布。

## 本地验证

```bash
(cd web/backend && go test ./... && go vet ./...)
bash web/scripts/deploy_test.sh
(cd web/frontend && npm ci && npm run lint && npm run build && npx playwright install chromium && npm run test:smoke)
```

发布脚本测试使用临时目录以及 mock git/docker/curl，不访问生产。Linux 使用真实 flock；Windows Git Bash 环境仅模拟锁调用参数，Linux CI 会再运行完整测试。
