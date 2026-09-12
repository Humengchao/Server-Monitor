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

配置键：POSTGRES_HOST/PORT/USER/PASSWORD/DB/SSLMODE、JWT_SECRET、ENCRYPTION_KEY、DOMAIN，以及 ALLOW_REGISTRATION、TRUSTED_PROXIES、POLL_INTERVAL、ALERT_INTERVAL、ALLOW_PRIVATE_WEBHOOKS。数据库地址须可从容器访问；远端数据库通常应配置 POSTGRES_SSLMODE=require。首次建好账户后建议 ALLOW_REGISTRATION=false。**保留现有 ENCRYPTION_KEY/JWT_SECRET；不要为升级重新生成密钥。**

GitHub Secrets 只保留部署连接与验收所需：DEPLOY_HOST、DEPLOY_USER、DEPLOY_PASSWORD、DOMAIN（HTTPS 冒烟域名）和私有镜像所需 GHCR_PAT。服务器 shared/.env 的 DOMAIN 应与该 Secret 一致。旧数据库/应用密钥 Secrets 不再自动写入服务器；修改 GitHub Secret 不会自动修改持久配置。

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
