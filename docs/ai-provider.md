# AI 提供商与自备密钥

AI 管理使用当前用户自己配置的模型服务与 API Key。每个账户最多保存 10 条配置，配置和密钥按用户隔离。面板不会提供、代购或自动选择第三方密钥。

## 配置字段

| 字段 | 含义 |
| --- | --- |
| `name` | 显示名称，1–128 字节 |
| `kind` | `deepseek` 或 `compatible` |
| `base_url` | 模型服务的 HTTPS 基础地址，最多 2048 字节 |
| `model` | 服务接受的模型 ID，最多 128 字节 |
| `api_key` | 自备 API Key，首次保存必填，最多 4096 个可见 ASCII 字符 |
| `enabled` | 是否允许测试和调用，创建时默认启用 |

DeepSeek 类型默认使用 `https://api.deepseek.com` 与 `deepseek-flash`。按照当前[官方模型说明](https://api-docs.deepseek.com/quick_start/pricing/)，`deepseek-flash` 是推荐的 Flash 模型名称；旧版 Flash 名称属于兼容别名。模型可由用户改为账户可用的其他模型，面板不硬编码费用或余额。

`compatible` 类型用于支持 Chat Completions 和函数工具调用的兼容服务，需要填写基础地址及模型 ID。例如：

| 基础地址 | 实际请求地址 |
| --- | --- |
| `https://api.example.com` | `https://api.example.com/chat/completions` |
| `https://api.example.com/v1` | `https://api.example.com/v1/chat/completions` |
| `https://api.example.com/custom/api` | `https://api.example.com/custom/api/chat/completions` |
| `https://api.example.com/v1/chat/completions` | 原地址，不重复追加 |

不会自动插入第二个 `/v1`。基础地址不允许用户名/密码、查询参数、fragment、明文 HTTP 或含混的转义路径；认证统一放在 `Authorization: Bearer …` 请求头中。

## 密钥保存与修改

数据库中的 `api_key_encrypted` 使用部署实例现有的 `ENCRYPTION_KEY` 经 AES-GCM 加密。列表、创建与更新响应只返回 `has_api_key`，不返回密钥、密文或伪装的占位密钥。

修改名称、模型或启停状态时，省略 `api_key` 或填写空字符串会保留旧密钥。**任何基础地址变更都要求本次请求显式提交 API Key**，包括只改路径；面板不会自动把保存的密钥转发到新地址。检查在数据库事务内进行，另一请求抢先更改地址时，旧页面的空密钥更新同样会被拒绝。

停用提供商会阻止新的测试及调用；已经开始的外部 HTTP 请求无法由配置修改撤回。删除配置不等于撤销第三方平台上的密钥，需要时请另外在提供商控制台撤销。

## 地址与网络策略

默认只允许公网 HTTPS 目标。每次调用都会重新解析 DNS，检查全部返回地址，并将这次连接固定到已批准的 IP；混合公网/私网 DNS 答案会被拒绝。回环、私网、链路本地、云元数据地址、CGNAT、组播及保留地址均不作为默认公网目标。

客户端不跟随重定向，不使用环境 HTTP 代理，不复用上次解析的连接。TLS 至少为 1.2，证书信任与主机名必须通过系统验证。生产实现没有 localhost 特例或关闭证书验证的开关。

确需对接私有模型网关时，部署管理员可以显式设置 `AI_ALLOW_PRIVATE_ENDPOINTS=true`。这个开关只影响 AI 提供商层，与通知模块的 `ALLOW_PRIVATE_WEBHOOKS` 独立；开启后仍要求 HTTPS 和有效 TLS 证书。

## 请求与工具调用

实现直接调用 Chat Completions HTTP 接口，不依赖第三方模型 SDK。请求使用非流式响应；提供工具时设置 `tool_choice: auto`。DeepSeek 请求显式附加 `thinking: {type: "disabled"}`，兼容类型不附加 DeepSeek 专用参数，参见[工具调用](https://api-docs.deepseek.com/guides/tool_calls)和[Chat Completions 协议](https://api-docs.deepseek.com/api/create-chat-completion)。

单次调用的总时限为 90 秒，调用方取消会传到 DNS、建连及响应读取。请求与响应体均限制为 2 MiB；一般调用最多请求 4096 个输出 token，连通测试只请求 32 个。模型服务可能按请求计费，面板不自动重试失败请求。

只有完整的 `stop` 文本结果或有效的 `tool_calls` 结果会被接受。工具调用必须拥有唯一 ID、`function` 类型、已声明的函数名和 JSON 对象参数；截断、内容过滤、资源不足、未知结束原因及不完整工具调用会报错，不交给执行层。参数仍是模型生成的不可信输入，后续动作层还需验证实际操作和权限，协议正确不表示操作获得授权。

响应允许 `content: null`，常见于仅返回工具调用的消息。若服务返回 `reasoning_content`，适配层保留它供同一会话后续工具调用继续使用；任务的公开响应不展示该字段。

## 测试与错误

「测试」会向已保存且启用的提供商发送一条要求回复 `OK` 的简短消息，不提供执行工具。成功表示已取得符合协议的模型文本回复，不代表所有模型工具能力都已验证。

提供商返回的 401/403 会映射为安全的 `authentication_failed` 错误，面板接口返回 502，不会把它当作当前用户登录失效。429、网络/TLS 错误、响应超限及协议错误使用固定错误分类；不将远端原始 body、带认证的 URL、密钥或任意网络异常直接返回给浏览器。

## API

全部接口要求已登录；账户身份来自认证上下文。

| 方法与路径 | 行为 |
| --- | --- |
| `GET /api/ai/providers` | 当前用户的无秘密配置列表 |
| `POST /api/ai/providers` | 创建配置；成功返回 201 |
| `PUT /api/ai/providers/:id` | 更新；省略普通字段保留原值，空密钥按上述规则处理 |
| `DELETE /api/ai/providers/:id` | 删除；不存在或不属于当前用户返回 404 |
| `POST /api/ai/providers/:id/test` | 测试已保存配置；成功返回 `{"ok":true,"model":"…"}` |

创建与更新请求体上限 16 KiB，拒绝未知字段及多个拼接 JSON 文档。测试接口限流，并限制同时运行的测试数量。跨用户读取、修改、删除或测试不会触发模型请求。
