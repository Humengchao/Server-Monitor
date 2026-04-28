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
    "last_seen_at": "2024-01-01T12:00:00Z",
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

## 五、SSH WebSocket 终端

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

## 六、通用说明

### 认证错误

| 状态码 | 响应 |
|--------|------|
| 401 | `{"error": "missing authorization header"}` |
| 401 | `{"error": "invalid token"}` |

### 数据隔离

所有资源（服务器、标签、指标）均按当前登录用户隔离，用户 A 无法访问用户 B 的数据。

### 指标采集

后端每 30 秒通过 SSH 登录各服务器采集 CPU、内存、网络、运行时长数据，存入 `server_metrics` 表。
