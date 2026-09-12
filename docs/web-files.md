# Web 文件管理

## 入口与使用

- 左侧「文件管理」可选择服务器，再选择「主机文件系统」或该服务器的运行中容器。
- 机器详情页的「文件管理」按钮直接选择该主机；Docker 表格的同名按钮直接选择该容器。
- 支持目录导航、手工输入绝对路径、目录内名称筛选、文件大小/修改时间/权限显示。
- 文本可以在线查看和编辑，用「保存」或 Ctrl/Cmd+S 保存；支持空文件，保留 UTF-8 BOM 和常见的 LF/CRLF 换行格式。
- 二进制文件使用下载操作。上传可一次选择多个文件，逐个传输；同名文件需要明确确认覆盖，不会直接截断原文件。
- 编辑器关闭、侧栏跳转和退出登录会提醒未保存编辑或未完成的传输；浏览器刷新/关闭也会提醒。请先保存再使用浏览器前进/后退。

## 支持范围

主机文件使用已有 SSH 连接和 SFTP 子系统，不需要保存另一套密码。Linux/OpenSSH 与 Windows OpenSSH（需开启 SFTP）均使用服务器配置的账户权限，不通过 sudo 自动提升主机文件权限。Windows 路径可使用 OpenSSH 返回的绝对路径，例如 /C:/Users/Administrator。

容器文件需要 **Linux 主机上的运行中 Linux 容器**，容器中需有 sh、stat、cat、chmod、chown、mv、ln、rm、wc 等标准文件工具。Docker 连接沿用已有的密码免交互 sudo 回退；文件操作以容器默认用户执行。不会为了访问文件自动启动、重启容器，也不会在容器里安装工具。Windows 容器及缺少 shell 的 distroless 容器不在此实现的支持范围内。

容器可写层内的修改可能随容器重建而丢失；需要持久化的配置或数据应存放在数据卷中。

## 限制与安全

- 在线文本：UTF-8、不含 NUL、最多 2 MiB；其他编码/二进制文件可下载。
- 单次上传：一个文件、最多 64 MiB；前端的多选上传会拆成多个请求。
- 单次下载：一个普通文件、最多 256 MiB。
- 目录最多展示 5000 项，超限会明确提示；仍可输入具体子目录路径。
- 文件接口使用现有 Bearer 登录认证，并按当前用户校验服务器归属；容器 ID 限定为十六进制。
- 保存提交读取时得到的 SHA-256 revision。远端内容在保存前已变化则返回 409，保留编辑器内容而不强制覆盖。同一后端进程对同一文件的写入会串行执行。
- 上传/保存先写同目录临时文件，完成后再替换目标；新上传文件权限为 0644，覆盖时保留已有的 Unix 所有者、组及普通权限。Windows 使用其账户和 ACL 权限。
- 符号链接可浏览/读取，但不允许直接覆盖符号链接；请打开实际目标路径进行编辑。目录和设备文件不能当作普通文件覆盖。
- 同时最多 8 个文件请求占用远程文件会话；单请求远程操作/请求读取限时 120 秒，HTTP 写出最多 150 秒。保存和上传接口分别按客户端 IP 限流（各 60 次/分钟，IPv6 按 /64 合并）。
- 上传和下载在后端使用有界临时文件缓冲，并在请求结束后删除；后端临时目录应预留相应空间。远程连接中断时，无法删除的临时文件可能保留为 .server-monitor-*，确认没有正在传输后可清理。
- 前端 nginx 已放宽 API 上传请求体至 65 MiB；如果外层另有 nginx/CDN/网关，也需允许相应上传大小及超时时间。

## API

所有接口均位于 /api/servers/:id/files，要求 Authorization: Bearer 登录令牌。

| 方法 | 路径 | 参数/行为 |
| --- | --- | --- |
| GET | / | query path：绝对目录；省略则主机用户主目录或容器根目录 |
| GET | /text | query path：普通 UTF-8 文本；返回 content、revision |
| PUT | /text | query path；JSON body 为 content、revision；内容冲突返回 409 |
| GET | /download | query path；以附件响应原始字节 |
| POST | /upload | query path：目录；multipart 的 file 字段为单个文件；明确覆盖时 query overwrite=1 |

上述接口均可附加 query container=<12–64 位容器 ID> 选择容器，不传则访问主机。目录响应包含 path、parent、entries、truncated；entry 包含 name、path、size、modified_at、mode、is_dir、is_file、is_symlink。

常见错误 code：file_exists、file_changed、not_text、file_too_large、permission_denied、not_found、invalid_path、unsupported、timeout、remote_failed。错误正文不会返回文件内容。

## Docker 自动加载

打开 Docker 页后，自动以最多 4 个并发请求加载所有已检测到 Docker 的主机，不需要逐台展开。每台主机独立显示加载/失败状态；失败可单独重试。汇总值区分「尚未加载」和「确实没有容器」。展开后的主机继续按原有 15 秒轮询资源用量，后台标签页暂停轮询；离开或刷新列表会取消旧加载，避免过期响应覆盖新数据。
