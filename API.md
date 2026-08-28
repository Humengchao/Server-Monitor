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

## 三、标签管理接口

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

## 四、监控指标接口

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

## 五、告警接口

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

## 六、SSH WebSocket 终端

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

## 七、通用说明

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
