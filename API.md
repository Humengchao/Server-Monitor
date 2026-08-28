# Server Monitor API 文档

## 概述

- **Base URL**: `http://localhost:8080/api`
- **认证方式**: JWT Bearer Token（`Authorization: Bearer <token>`）
- **Content-Type**: `application/json`

---

## 一、认证接口

### 1. 注册

```
POST /api/auth/register
```

**请求体**:

```json
{
  "username": "string (3-64字符, 必填)",
  "password": "string (最少6字符, 必填)"
}
```

**响应**:

| 状态码 | 说明 |
|--------|------|
| 201 | `{"message": "user created"}` |
| 400 | `{"error": "..."}` 参数校验失败 |
| 409 | `{"error": "username already exists"}` |

---

### 2. 登录

```
POST /api/auth/login
```

**请求体**:

```json
{
  "username": "string (必填)",
  "password": "string (必填)"
}
```

**响应**:

```json
{
  "token": "eyJhbGci...",
  "user": {
    "id": "uuid",
    "username": "string"
  }
}
```

| 状态码 | 说明 |
|--------|------|
| 200 | 登录成功 |
| 401 | `{"error": "invalid credentials"}` |

---

### 3. 获取当前用户信息

```
GET /api/auth/me
Authorization: Bearer <token>
```

**响应**:

```json
{
  "id": "uuid",
  "username": "string",
  "created_at": "2024-01-01T00:00:00Z"
}
```

---

### 4. 修改密码

```
POST /api/auth/password
Authorization: Bearer <token>
```

**请求体**:

```json
{
  "current_password": "string (必填)",
  "new_password": "string (必填, 最少6字符)"
}
```

**响应**:

```json
{
  "message": "password updated",
  "token": "eyJhbGci..."
}
```

修改成功后，**该用户此前签发的所有 Token 立即失效**（详见下方「Token 失效」）。响应里的 `token` 是为当前调用方重新签发的，用它替换本地 Token 即可保持登录；若服务端未能签发，则返回 `reauth_required: true`，客户端应引导用户重新登录。

| 状态码 | 说明 |
|--------|------|
| 200 | 修改成功 |
| 400 | 新密码不足 6 位，或与当前密码相同 |
| 403 | `{"error": "current password is incorrect"}` —— 当前密码错误。**注意是 403 而非 401**：会话本身有效，仅请求体里的密码不对，用 401 会让客户端误判会话失效而把用户登出 |
| 429 | 超过限流（10 次/分钟/IP，与登录接口的额度相互独立） |

---

## 二、服务器管理接口

> 以下接口均需认证 Header: `Authorization: Bearer <token>`
> 所有操作仅限当前用户拥有的服务器

### 4. 获取服务器列表

```
GET /api/servers
```

**响应** (包含最新指标和标签):

```json
[
  {
    "id": "uuid",
    "user_id": "uuid",
    "name": "My Web Server",
    "host": "192.168.1.100",
    "port": 22,
    "ssh_username": "root",
    "created_at": "2024-01-01T00:00:00Z",
    "tags": [
      {
        "id": "uuid",
        "user_id": "uuid",
        "name": "Production",
        "color": "#ff4d4f"
      }
    ],
    "latest_metrics": {
      "cpu_percent": 12.5,
      "memory_used": 8589934592,
      "memory_total": 17179869184,
      "network_rx_bytes": 1024000,
      "network_tx_bytes": 512000,
      "recorded_at": "2024-01-01T12:00:00Z"
    }
  }
]
```

---

### 5. 添加服务器

```
POST /api/servers
```

**请求体**:

```json
{
  "name": "string (必填)",
  "host": "string (必填, IP或域名)",
  "port": 22,
  "ssh_username": "string (必填)",
  "ssh_password": "string (与ssh_key二选一)",
  "ssh_key": "string (私钥内容, 与ssh_password二选一)"
}
```

**响应**: `201` 返回创建的 Server 对象

---

### 6. 更新服务器

```
PUT /api/servers/:id
```

**请求体** (全部字段可选):

```json
{
  "name": "string",
  "host": "string",
  "port": 22,
  "ssh_username": "string",
  "ssh_password": "string",
  "ssh_key": "string"
}
```

**响应**: `200` 返回更新后的 Server 对象

---

### 7. 删除服务器

```
DELETE /api/servers/:id
```

**响应**: `200` `{"message": "deleted"}`

---

### 8. 设置服务器标签

```
PUT /api/servers/:id/tags
```

**请求体**:

```json
{
  "tag_ids": ["uuid", "uuid", ...]
}
```

**响应**: `200` `{"message": "tags updated"}`

---

## 三、批量操作接口

> 需认证。所有操作在 SQL 层按 `user_id` 过滤，不属于当前用户的 ID 会被静默跳过而非报错，因此响应里返回的是**实际生效的数量**。

### 批量标签

```
POST /api/servers/bulk/tags
```

**请求体**:

```json
{
  "server_ids": ["uuid", "uuid"],
  "tag_ids": ["uuid"],
  "action": "add"
}
```

`action` 为 `add` 或 `remove`。添加是幂等的（重复调用不会产生重复关联）。

**响应**: `{"message": "tags updated", "servers": 2}` —— `servers` 是这批 ID 里真正属于当前用户的数量。

| 状态码 | 说明 |
|--------|------|
| 200 | 成功 |
| 400 | `action` 非法、未选服务器，或未选标签 |
| 404 | 所选 ID 没有一个属于当前用户 |

---

### 批量删除

```
POST /api/servers/bulk/delete
```

**请求体**: `{"server_ids": ["uuid", ...]}`

**响应**: `{"message": "deleted", "deleted": 2}` —— `deleted` 为实际删除的行数。关联的历史指标、告警事件、标签关联通过 `ON DELETE CASCADE` 一并清理，缓存的 SSH 连接同时关闭。

---

### 批量执行命令

```
POST /api/servers/bulk/exec
```

**请求体**:

```json
{
  "server_ids": ["uuid", "uuid"],
  "command": "hostname; df -h /"
}
```

**响应**:

```json
{
  "results": [
    {
      "server_id": "uuid",
      "server_name": "node-01",
      "ok": true,
      "output": "node-01\n/dev/vda1  50G  26G  66% /\n",
      "error": "",
      "truncated": false,
      "duration_ms": 147
    }
  ],
  "succeeded": 1,
  "failed": 0
}
```

执行语义与边界：

| 约束 | 值 | 说明 |
|---|---|---|
| 单次目标上限 | 50 台 | 超出返回 400 |
| 并发 | 8 | 其余排队 |
| 单台超时 | 60 秒 | 超时立即返回并释放并发槽位，不等远端命令结束 |
| 单台输出上限 | 64 KB | 超出时 `truncated` 为 `true`，命令仍会跑完 |
| 命令长度上限 | 4096 字符 | |
| 限流 | 10 次/分钟/IP | |

- `output` 是 **stdout + stderr 合并**的结果，命令失败时错误详情就在这里（`error` 只是 `exit status 2` 之类的退出信息）。
- 单台失败不会中断整批：不可达的主机只是自己报错，其余照常执行。
- 命令必须是**单行且不含控制字符**（制表符除外）。否则一条"命令"可以夹带确认弹窗从未展示给用户的额外命令行。
- 以服务器配置的 SSH 用户身份执行，**不会尝试 sudo 提权**；交互式操作请用 SSH 终端。

| 状态码 | 说明 |
|--------|------|
| 200 | 已执行（逐台结果见 `results`，整批可能部分失败） |
| 400 | 命令为空/多行/过长，或选择数量超限 |
| 404 | 所选 ID 没有一个属于当前用户 |
| 429 | 超过限流 |

---

## 四、标签管理接口

> 所有接口均需认证

### 9. 获取标签列表

```
GET /api/tags
```

**响应**:

```json
[
  {
    "id": "uuid",
    "user_id": "uuid",
    "name": "Production",
    "color": "#ff4d4f"
  }
]
```

---

### 10. 创建标签

```
POST /api/tags
```

**请求体**:

```json
{
  "name": "string (必填)",
  "color": "#1890ff (可选, 默认蓝色)"
}
```

**响应**: `201` 返回创建的 Tag 对象

---

### 11. 删除标签

```
DELETE /api/tags/:id
```

**响应**: `200` `{"message": "deleted"}`

---

## 五、监控指标接口

> 所有接口均需认证

### 12. 获取最新指标

```
GET /api/servers/:id/metrics/latest
```

**响应**:

```json
{
  "cpu_percent": 12.5,
  "memory_used": 8589934592,
  "memory_total": 17179869184,
  "network_rx_bytes": 1024000,
  "network_tx_bytes": 512000,
  "uptime_seconds": 864000,
  "recorded_at": "2024-01-01T12:00:00Z"
}
```

| 字段 | 单位 | 说明 |
|------|------|------|
| cpu_percent | % | CPU 使用率 (0-100) |
| memory_used | bytes | 已用内存 |
| memory_total | bytes | 总内存 |
| network_rx_bytes | bytes | 累计接收流量 |
| network_tx_bytes | bytes | 累计发送流量 |
| uptime_seconds | 秒 | 系统运行时长 |
| recorded_at | ISO8601 | 采集时间 |

---

### 13. 获取历史指标

```
GET /api/servers/:id/metrics?since=2024-01-01T00:00:00Z
```

**查询参数**:

| 参数 | 说明 | 默认值 |
|------|------|--------|
| since | RFC3339 格式时间, 从此时间开始查询 | 1小时前 |

**响应**: 返回 `MetricPoint[]` 数组（同上结构）

---

## 六、可用性接口

> 需认证。可用率按 **观测口径**（observed）计算：窗口内实际落库的指标样本数 ÷ 应落库的样本数。
> 面板自身或其数据库不可用的时段会同时计入所有服务器的不可用时间——它衡量的是"我们是否看得到这台主机"，
> 而不是主机自身的 uptime 计数器。响应里固定带上 `basis` 字段，方便调用方如实标注 SLA 徽章的口径。

**窗口与汇总层**

| 窗口 | 读取的汇总层 | 单桶时长 | 满窗桶数 |
|------|--------------|----------|----------|
| `24h` | `server_metrics_1m` | 60 秒 | 1440 |
| `7d` | `server_metrics_1m` | 60 秒 | 10080 |
| `30d` | `server_metrics_15m` | 900 秒 | 2880 |

30 天窗口刻意读取粗粒度层：一分钟层只保留 30 天，且逐台扫描它的索引开销约为 15 分钟层的 20 倍，
而在一个月的时间尺度上这点精度差异并不可见。

**三条计算约定**

1. **窗口起点夹到 `servers.created_at`**：刚加入的主机不会因为"加入之前没有数据"而被算作不可用，此时该窗口返回 `partial: true`。
2. **窗口终点对齐到该层的桶边界**：正在填充中的桶还没有被汇总写入，如果把它算进应采集数，任何健康主机都会永远差一个桶、到不了 100%。
3. **`expected_buckets` 数的是区间内的对齐边界个数**，而不是"时长 ÷ 桶宽"。汇总写入的时间戳是 `FLOOR(EXTRACT(EPOCH FROM recorded_at) / 桶宽) * 桶宽`，
   在非对齐的边界上（例如按天切分出的半天、夹到创建时刻的窗口）两种算法会差 1，导致 `observed_buckets > expected_buckets`。

### 全部服务器可用率

```
GET /api/servers/uptime
```

每个用户的结果在服务端缓存 60 秒——一次调用要扫一个月的汇总桶，而这个数字在一分钟内不会有实质变化。

**响应**:

```json
{
  "servers": [
    {
      "server_id": "uuid",
      "windows": [
        { "window": "24h", "percent": 95.21, "observed_buckets": 1371, "expected_buckets": 1440, "partial": false, "no_data": false },
        { "window": "7d",  "percent": 99.32, "observed_buckets": 10011, "expected_buckets": 10080, "partial": false, "no_data": false },
        { "window": "30d", "percent": 99.86, "observed_buckets": 2876, "expected_buckets": 2880, "partial": false, "no_data": false }
      ]
    }
  ],
  "basis": "observed",
  "basis_note": "Share of expected metric samples that were collected. Panel downtime counts against every server.",
  "generated_at": "2026-08-27T20:49:31Z"
}
```

**字段说明**:

| 字段 | 说明 |
|------|------|
| percent | 保留两位小数，取值夹在 0–100 |
| observed_buckets | 窗口内实际存在的汇总桶数 |
| expected_buckets | 窗口内应存在的汇总桶数 |
| partial | 窗口长度超过该服务器的存在时长，实际统计区间比标签更短 |
| no_data | 该窗口尚无任何应采集量（新加入的主机还没跨过一个完整的桶）。此时 `percent` 为 0，**不应**按 0% 展示 |

### 单台服务器可用率明细

```
GET /api/servers/:id/uptime?days=30
```

**查询参数**:

| 参数 | 说明 | 默认值 |
|------|------|--------|
| days | 统计天数, 取值 1–30, 超出范围则忽略 | 30 |

**响应**:

```json
{
  "server_id": "uuid",
  "since": "2026-07-28T20:45:00Z",
  "until": "2026-08-27T20:45:00Z",
  "days": [
    { "day": "2026-07-28", "percent": 100, "observed_buckets": 13, "expected_buckets": 13, "no_data": false },
    { "day": "2026-07-29", "percent": 100, "observed_buckets": 96, "expected_buckets": 96, "no_data": false }
  ],
  "outages": [
    { "started_at": "2026-08-27T13:52:00Z", "ended_at": "2026-08-27T14:23:00Z", "seconds": 1860, "ongoing": false },
    { "started_at": "2026-08-24T18:22:00Z", "ended_at": "2026-08-24T20:23:00Z", "seconds": 7260, "ongoing": false }
  ],
  "basis": "observed",
  "generated_at": "2026-08-27T20:50:20Z"
}
```

**`days` 每日条带**

按 15 分钟层统计（与 30 天数字同层），因此条带各天之和与 `GET /api/servers/uptime` 的 30 天百分比一致。
区间内**每一个日历日都会出现**，包括查询结果里根本没有行的整天——全天中断的那一天恰好没有行，
若不补齐就会从图上凭空消失。首尾两天以及创建当天会按实际覆盖时长折算 `expected_buckets`。

`no_data: true` 表示这一天服务器尚不存在，与"整天 0%"必须区分开：前者应画成留白/斜纹，后者应画成红色。

**`outages` 中断记录**

由相邻汇总桶之间的间隔推导：间隔超过 `2 分钟`（`services.UptimeOutageGap`）即算一次中断，最多返回 50 条
（`services.UptimeMaxOutages`，倒序取最近的）。若返回正好 50 条，说明可能被截断。

`ongoing: true` 表示窗口结束时仍未恢复：最后一个样本距窗口终点已超过阈值，且没有更晚的桶可以给这段间隔"收尾"。
从未上报过任何样本的服务器不会产生中断记录——没有样本可以作为起点，此时应当展示"暂无历史"而不是"无中断"。

---

### 公开状态页的 SLA 字段

公开状态页（`GET /api/public/status`，无需认证）的每个节点会带上 `availability_30d`：

```json
{ "name": "web-01", "availability_30d": 99.86 }
```

口径与上面的 30 天窗口完全一致（15 分钟层、终点对齐、起点夹到创建时刻）。
当该服务器的应采集桶数少于 16 个（即不足四小时）时，字段为 `null` 而不是一个百分比——
样本太少时给出的数字只是噪声，不如不给。

---

## 七、进程接口

> 需认证。请求通过共享的 SSH 连接缓存执行，轮询列表不会每次都重新握手。

### 进程列表

```
GET /api/servers/:id/processes
```

**响应**:

```json
{
  "processes": [
    {
      "pid": 1748,
      "user": "root",
      "cpu_percent": 64.2,
      "mem_percent": 0.1,
      "rss_bytes": 52633600,
      "state": "Rl",
      "elapsed_seconds": 32,
      "command": "node -e let x=0;setInterval(...)"
    }
  ],
  "total": 137,
  "returned": 137
}
```

| 字段 | 说明 |
|------|------|
| cpu_percent | 来自 `ps` 的 `pcpu`，是进程**整个生命周期的平均值**，不是瞬时占用 |
| mem_percent | 常驻内存占物理内存的比例 |
| rss_bytes | 常驻内存字节数（`ps` 报的是 kB，此处已换算） |
| state | Linux 进程状态码（`R`/`S`/`D`/`Z`/`T` 等）；Windows 恒为 `running` |
| elapsed_seconds | 已运行秒数；主机的 `ps` 不支持该列时为 `0` |
| total | 主机上的进程总数（截断前） |
| returned | 实际返回条数，按 CPU 降序保留前 300 条 |

采集方式：Linux 优先 `ps -eo pid=,user=,pcpu=,pmem=,rss=,stat=,etimes=,args=`，主机的 `ps` 不支持该列表时回退到通用的 `ps aux`（此时无 `elapsed_seconds`）。Windows 使用 `Win32_PerfFormattedData_PerfProc_Process`，CPU 已按逻辑核数归一到 0-100，不采集进程所有者（每个进程一次 `GetOwner` 调用代价过高）。

| 状态码 | 说明 |
|--------|------|
| 200 | 成功 |
| 404 | 服务器不存在或不属于当前用户 |
| 502 | SSH 连接失败，或进程列表读取失败 |

---

### 结束进程

```
DELETE /api/servers/:id/processes/:pid?force=1
```

`force=1` 时发送 `SIGKILL`，否则发送 `SIGTERM`；Windows 使用 `Stop-Process -Force`。PID 以整数格式化后拼接，用户输入不会进入远程 shell。

| 状态码 | 说明 |
|--------|------|
| 200 | `{"message": "signal sent"}` |
| 400 | PID 不是正整数 |
| 502 | 拒绝或失败，`error` 为远端原始信息（如 `kill: (10) - Operation not permitted`） |

> `pid <= 1` 一律拒绝：PID 1 是 init（或容器入口进程），杀掉它会带走整台主机或整个容器。
>
> 该操作受 SSH 登录用户自身权限约束，不会尝试 `sudo` 提权。

---

## 八、告警接口

> 所有接口均需认证。规则按用户隔离；`server_id` 为 `null` 时该规则作用于该用户的全部服务器（包括之后新增的）。

### 14. 获取告警规则列表

```
GET /api/alerts/rules
```

**响应**:

```json
[
  {
    "id": "uuid",
    "user_id": "uuid",
    "name": "CPU saturation",
    "server_id": null,
    "server_name": "",
    "metric": "cpu",
    "comparator": ">",
    "threshold": 90,
    "duration_seconds": 300,
    "enabled": true,
    "webhook_url": "",
    "created_at": "2024-01-01T00:00:00Z",
    "firing_count": 1
  }
]
```

| 字段 | 说明 |
|------|------|
| metric | `cpu` / `memory` / `disk` / `load1` / `latency` / `offline` |
| comparator | `>` 或 `<`（`offline` 规则强制为 `>`） |
| threshold | 阈值。百分比类指标（cpu/memory/disk）限制在 0-100；`offline` 规则忽略该值 |
| duration_seconds | 条件需持续满足的时长，30 ~ 86400 秒。`offline` 规则最短 120 秒（与面板在线判定一致） |
| firing_count | 当前正在触发该规则的服务器数量 |

---

### 15. 创建 / 更新 / 删除告警规则

```
POST   /api/alerts/rules
PUT    /api/alerts/rules/:id
DELETE /api/alerts/rules/:id
```

**请求体**（创建与更新一致）:

```json
{
  "name": "string (必填, ≤128字符)",
  "server_id": "uuid 或 null",
  "metric": "cpu",
  "comparator": ">",
  "threshold": 90,
  "duration_seconds": 300,
  "enabled": true,
  "webhook_url": "https://hooks.example.com/alerts"
}
```

| 状态码 | 说明 |
|--------|------|
| 201 / 200 | 成功，返回规则对象 |
| 400 | 指标、比较符、阈值、时长或 webhook 地址不合法 |
| 404 | 规则不存在或不属于当前用户 |

---

### 16. 获取告警事件

```
GET /api/alerts/events?active=1&limit=100
```

**查询参数**:

| 参数 | 说明 | 默认值 |
|------|------|--------|
| active | `1` 只返回未恢复的事件 | 全部 |
| limit | 返回条数上限（1-500） | 100 |

**响应**:

```json
[
  {
    "id": 1,
    "rule_id": "uuid",
    "rule_name": "CPU saturation",
    "metric": "cpu",
    "server_id": "uuid",
    "server_name": "node-05",
    "value": 96.4,
    "message": "node-05 cpu is 96.4% (> 90% for 5m)",
    "comparator": ">",
    "threshold": 90,
    "duration_seconds": 300,
    "started_at": "2024-01-01T12:00:00Z",
    "resolved_at": null
  }
]
```

`resolved_at` 为 `null` 表示告警仍在进行中。`message` 是采集端记录的英文摘要，前端会用 `metric` / `value` / `threshold` 等字段按当前语言重新渲染。

---

### 17. 告警数量汇总

```
GET /api/alerts/summary
```

**响应**: `{"active": 2}` —— 当前未恢复的告警数量，用于导航栏角标。

---

### 18. 测试 Webhook

```
POST /api/alerts/test
```

**请求体**: `{"webhook_url": "https://hooks.example.com/alerts"}`

向该地址 POST 一条示例载荷，用于在等待真实告警前验证通知链路。该接口按 IP 限流 6 次/分钟。

| 状态码 | 说明 |
|--------|------|
| 200 | `{"message": "sent"}` |
| 400 | 地址不合法（非 http/https，或解析到非公网地址） |
| 502 | 目标端点未返回 2xx |

---

### Webhook 载荷

告警触发和恢复时各发送一次：

```json
{
  "status": "firing",
  "rule": "CPU saturation",
  "server": "node-05",
  "metric": "cpu",
  "value": 96.4,
  "threshold": 90,
  "comparator": ">",
  "message": "node-05 cpu is 96.4% (> 90% for 5m)",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

`status` 为 `firing` 或 `resolved`（测试请求为 `test`）。

> **安全**：默认拒绝解析到 loopback / RFC1918 / link-local / CGNAT 地址的 webhook，避免通过告警链路探测服务端内网。局域网通知端点可用 `ALLOW_PRIVATE_WEBHOOKS=true` 放开。请求不跟随重定向。

---

## 九、SSH WebSocket 终端

```
WS /api/ssh/:id
```

**协议**: WebSocket

**认证**: 通过 URL 查询参数传递 token: `ws://host/api/ssh/:id?token=<jwt>`

**连接后**:
- 服务端校验服务器归属权限后建立 SSH 连接
- 客户端发送的文本消息 → 转发到 SSH stdin
- SSH stdout/stderr → 转发为 WebSocket 文本消息给客户端
- 连接断开时自动关闭

**示例**:

```javascript
const ws = new WebSocket(`ws://localhost:8080/api/ssh/${serverId}?token=${token}`);
ws.onmessage = (e) => terminal.write(e.data);
ws.send('ls -la\n');
```

---

## 十、通用说明

### 认证错误

| 状态码 | 响应 |
|--------|------|
| 401 | `{"error": "missing authorization header"}` |
| 401 | `{"error": "invalid token"}` |
| 401 | `{"error": "session expired, please sign in again"}` —— Token 早于该账号的失效时间点 |
| 503 | `{"error": "authentication temporarily unavailable"}` —— 数据库暂时不可用。**故意不用 401**，否则一次数据库抖动会把所有在线用户登出 |

### Token 失效

Token 本身是无状态的 JWT（HS256，有效期 72 小时），但 `users.tokens_valid_after` 提供了一个吊销时间点：鉴权中间件会拒绝 `iat` 早于该时间的 Token。修改密码时把它推到当前时刻，就实现了「改密码即登出其他设备」。

代价是每个已认证请求多一次按主键的查询——这是无状态方案能支持吊销的唯一办法。几点实现细节：

- 吊销时间点截断到整秒，因为 JWT 的 `iat` 精度就是秒；否则刚签发的 Token 会被自己的吊销点判定为过期。
- 修改密码接口返回的新 Token，其 `iat` 精确等于写入的吊销时间点，所以调用方的会话不会被自己的操作切断。
- 本次改动之前签发的 Token 不含 `iat`，会被放行直到自然过期，升级不会强制所有人重新登录。

### 数据隔离

所有资源（服务器、标签、指标、告警规则与事件）均按当前登录用户隔离，用户 A 无法访问用户 B 的数据。

### CORS

跨域部署时，预检请求允许的请求头为 `Authorization, Content-Type, Cache-Control, Pragma`（前端客户端会带上后两个禁用缓存的头）。

### 指标采集

后端默认每 3 秒（`POLL_INTERVAL`）通过 SSH 登录各服务器采集 CPU、内存、磁盘、网络、负载、运行时长与延迟数据。最新值写入 `server_latest_metrics`，原始样本写入 `server_metrics` 并逐级汇总到 `server_metrics_1m` / `server_metrics_15m`（保留 24 小时 / 30 天 / 1 年）。

### 告警评估

独立的评估循环默认每 30 秒（`ALERT_INTERVAL`）读取一次全部规则与最新样本，在内存中判定，不额外访问被监控主机：

- 阈值类规则需连续满足 `duration_seconds` 才会开启事件；条件恢复后立即关闭。
- 主机停止上报超过 10 分钟时，阈值类告警视为恢复，避免消失的主机留下永久告警。
- `offline` 规则以最后一次采样距今的时长判定，下限 2 分钟。
- 进行中的事件以数据库中 `resolved_at IS NULL` 表示，因此重启不会重复通知，也不会漏掉恢复通知。
