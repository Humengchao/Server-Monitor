# Web 文件管理

## 入口与使用

- 左侧「文件管理」可选择服务器，再选择「主机文件系统」或该服务器的运行中容器。
- 机器详情页的「文件管理」按钮直接选择该主机；Docker 表格的同名按钮直接选择该容器。
- 支持目录导航、手工输入绝对路径、目录内名称筛选、文件大小/修改时间/权限显示。
- 文本可以在线查看和编辑，用「保存」或 Ctrl/Cmd+S 保存；支持空文件，保留 UTF-8 BOM 和常见的 LF/CRLF 换行格式。
- 编辑器按文件名/扩展名自动识别 JSON、YAML、TOML、INI、XML、HTML、CSS、JavaScript/TypeScript、Python、Go、Shell、PowerShell 等格式，并识别 Dockerfile、Containerfile、.env、Nginx 和 systemd 配置。未识别格式的文件以纯文本打开，可在「语法高亮」下拉框手动选择语言或关闭高亮。
- 支持行号、代码折叠、查找替换和撤销/重做，并跟随明暗主题。高亮和语言切换仅影响显示，不执行代码或自动格式化文件；编辑器及语法包按需加载，语法包加载失败时仍可编辑纯文本。
- 二进制文件使用下载操作。上传可一次选择多个文件，逐个传输；同名文件需要明确确认覆盖，不会直接截断原文件。
- 编辑器关闭、侧栏跳转、退出登录、浏览器前进/后退统一保护未保存编辑或未完成传输；刷新/关闭使用浏览器原生提示。取消离开保留当前编辑，明确放弃后才跳转。
- 登录过期不会立即卸载编辑器；可先将未保存内容导出为本地文件，再确认放弃并重新登录。草稿仅保存在当前页面内存，不写入 localStorage、不自动发送到其他服务；强制关页、进程崩溃或忽略离开提示仍可能丢失。
- 支持新建空文件/目录、同目录重命名、确认后删除文件或空目录；不提供递归删除。保存文本前自动备份原内容，操作记录中可下载备份。

## 支持范围

主机文件使用已有 SSH 连接和 SFTP 子系统，不需要保存另一套密码。Linux/OpenSSH 与 Windows OpenSSH（需开启 SFTP）均使用服务器配置的账户权限，不通过 sudo 自动提升主机文件权限。Windows 路径可使用 OpenSSH 返回的绝对路径，例如 /C:/Users/Administrator。

容器文件需要 **Linux 主机上的运行中 Linux 容器**，容器中需有 sh、stat、find（支持 -print0/-maxdepth）、mkdir、rmdir、cat、chmod、chown、mv（支持 -nT）、ln、rm、wc 等标准文件工具。Docker 连接沿用已有的密码免交互 sudo 回退；文件操作以容器默认用户执行。不会为了访问文件自动启动、重启容器，也不会在容器里安装工具。Windows 容器及缺少 shell 的 distroless 容器不在此实现的支持范围内。

容器可写层内的修改可能随容器重建而丢失；需要持久化的配置或数据应存放在数据卷中。

## 限制与安全

- 在线文本：UTF-8、不含 NUL、最多 2 MiB；其他编码/二进制文件可下载。
- 单次上传：一个文件、最多 64 MiB；前端的多选上传会拆成多个请求。
- 单次下载：一个普通文件、最多 256 MiB。
- 目录每页默认 100 项、最多 200 项，使用上一页/下一页按需加载；搜索和排序仅作用于当前页。SFTP v3 分批 READDIR，容器流式读取名称、每页一次批量 stat（符号链接按需补目标属性），不会先完整读取整个目录。
- 游标为目录遍历偏移（最大 1,000,000），不是目录快照或全局排序；后续页仍须跳过之前的条目，远端增删可能使分页重复/遗漏，刷新可重置。网络传输和元数据查询按请求页截止，不能承诺远端文件系统的内部读取量。
- 文件接口使用现有 Bearer 登录认证，并按当前用户校验服务器归属；容器 ID 限定为十六进制。
- 保存提交读取时得到的 SHA-256 revision。保存前校验内容、备份、再次校验后原子替换；失败保留编辑器内容。同一后端进程对同一文件的写入串行执行。
- 上传先查 metadata，同名覆盖先确认再发送文件体；收到冲突不会自动重传。后台在写入前再次比较 metadata version；重命名和删除也必须提交预检版本。版本基于路径/大小/修改时间/类型和权限，不是远端原子 CAS；无法完全避免外部进程在检查后修改，敏感配置仍建议暂停其他写入者。
- 上传进度分别显示准备、发送到后台、等待写入远端；字节发送完毕不代表远端保存完成，收到成功响应前不显示操作完成。网关启用请求缓冲时，上传完成仅表示浏览器已交付数据。
- 上传/保存先写同目录临时文件，完成后再替换目标；新上传文件权限为 0644，覆盖时保留已有的 Unix 所有者、组及普通权限。Windows 使用其账户和 ACL 权限。
- 符号链接可浏览/读取，但不允许直接覆盖符号链接；请打开实际目标路径进行编辑。目录和设备文件不能当作普通文件覆盖。
- 保存前备份位于原文件同目录的 .server-monitor-backups 下，目录权限设为 0700（Windows 仍以账户 ACL 为准）。备份失败则取消保存；备份并非删除/上传的自动回收站。
- 修改类操作先写数据库审计记录；审计不可用返回 503 并取消操作。记录用户、服务器、容器、路径、目标、备份及结果，不记录正文或凭据。历史显示最近 100 条；进程中断或审计回写失败可能留下 pending。备份/日志不自动清理，请按空间和审计政策定期维护。
- 同时最多 8 个文件请求占用远程文件会话；单请求远程操作/请求读取限时 120 秒，HTTP 写出最多 150 秒。保存和上传接口分别按客户端 IP 限流（各 60 次/分钟，IPv6 按 /64 合并）。
- 上传和下载在后端使用有界临时文件缓冲，并在请求结束后删除；后端临时目录应预留相应空间。远程连接中断时，无法删除的临时文件可能保留为 .server-monitor-*，确认没有正在传输后可清理。
- 前端 nginx 已放宽 API 上传请求体至 65 MiB；如果外层另有 nginx/CDN/网关，也需允许相应上传大小及超时时间。

## API

所有接口均位于 /api/servers/:id/files，要求 Authorization: Bearer 登录令牌。

| 方法 | 路径 | 参数/行为 |
| --- | --- | --- |
| GET | / | query path、cursor（默认 0）、limit（默认 100，1–200）；path 省略则主机用户主目录或容器根目录 |
| GET | /text | query path：普通 UTF-8 文本；返回 content、revision |
| PUT | /text | query path；JSON body 为 content、revision；返回 revision、backup_path，内容冲突返回 409 |
| GET | /download | query path；以附件响应原始字节 |
| GET | /metadata | query path；返回 exists、version、entry；不存在时 version 为 missing |
| POST | /upload | query path：目录，version：预检目标文件版本；multipart 单个 file；明确覆盖需 overwrite=1 |
| POST | /change | query path；body action=create（kind=file/directory）、rename（name、version）或 delete（version） |
| GET | /audit | 最近 100 条当前用户/服务器/容器的修改日志，包含 backup_path |

上述接口均可附加 query container=<12–64 位容器 ID> 选择容器，不传则访问主机。目录响应包含 path、parent、entries、next_cursor、truncated（兼容字段）；next_cursor 缺省表示没有下一页；entry 包含 name、path、size、modified_at、mode、is_dir、is_file、is_symlink。

常见错误 code：file_exists、file_changed、not_text、file_too_large、permission_denied、not_found、invalid_path、unsupported、timeout、remote_failed、audit_unavailable。错误正文不会返回文件内容。

## Docker 自动加载

打开 Docker 页后，自动以最多 4 个并发请求加载所有已检测到 Docker 的主机，不需要逐台展开。基础列表接口不执行 --size 或 stats，先显示容器，再由独立请求补齐资源数据。后端列表/统计各有 4 个并发槽，分别缓存 3 秒/10 秒，同键请求合并；慢统计不会占满基础列表槽，容器操作使两份缓存失效。统计失败保留列表并显示警告，不伪装成零用量。每台主机独立显示加载/失败状态；失败可单独重试。汇总值区分「尚未加载」和「确实没有容器」。展开后的主机继续按原有 15 秒轮询资源用量，后台标签页暂停轮询；离开或刷新列表会取消旧加载，避免过期响应覆盖新数据。
