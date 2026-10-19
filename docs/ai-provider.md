# AI 提供商与自备密钥

AI 管理使用当前用户自己配置的模型服务与 API Key。每个账户最多保存 10 条配置，配置和密钥按用户隔离。面板不会提供、代购或自动选择第三方密钥。

## 配置字段

| 字段 | 含义 |
| --- | --- |
| `name` | 显示名称，1–128 字节 |
| `kind` | `deepseek`、`openai`、`qwen`、`moonshot`、`zhipu`、`siliconflow`、`gemini`、`compatible` 或 `cpa` |
| `base_url` | 模型服务的 HTTPS 基础地址，最多 2048 字节 |
| `model` | 服务接受的模型 ID，最多 128 字节 |
| `api_key` | 自备 API Key，首次保存必填，最多 4096 个可见 ASCII 字符 |
| `enabled` | 是否允许测试和调用，创建时默认启用 |

DeepSeek 类型默认使用 `https://api.deepseek.com` 与 `deepseek-flash`。按照当前[官方模型说明](https://api-docs.deepseek.com/quick_start/pricing/)，`deepseek-flash` 是推荐的 Flash 模型名称；旧版 Flash 名称属于兼容别名。模型可由用户改为账户可用的其他模型，面板不硬编码费用或余额。

供应商预设填入以下基础地址；除 DeepSeek 保留上述默认模型外，其余供应商需要同步后选择模型或手动填写 ID。预设不限制可修改的 HTTPS 地址，也不保证目录中的所有模型都支持 Chat Completions 和函数工具调用。不同地区、账户、套餐可能需要使用控制台提供的专用地址。

| 类型 | 默认基础地址 | 官方依据 |
| --- | --- | --- |
| `deepseek` | `https://api.deepseek.com` | [模型目录](https://api-docs.deepseek.com/api/list-models/) |
| `openai` | `https://api.openai.com/v1` | [模型目录](https://developers.openai.com/api/reference/resources/models/methods/list) |
| `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | [兼容地址与地区说明](https://help.aliyun.com/en/model-studio/error-code) |
| `moonshot` | `https://api.moonshot.cn/v1` | [Kimi 快速开始](https://platform.kimi.com/docs/get-api-key) |
| `zhipu` | `https://open.bigmodel.cn/api/paas/v4` | [OpenAI API 兼容](https://docs.bigmodel.cn/cn/guide/develop/openai/introduction) |
| `siliconflow` | `https://api.siliconflow.cn/v1` | [模型目录](https://docs.siliconflow.cn/docs/api/models-get) |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | [兼容端点及模型目录](https://ai.google.dev/gemini-api/docs/openai) |
| `compatible`、`cpa` | 手动填写 | 支持 Chat Completions 与函数工具调用的 HTTPS 网关 |

`compatible` 和 `cpa` 类型用于兼容服务和 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 等反向代理，填写网关自己的访问密钥与基础地址。面板不会接触代理背后的上游登录信息。例如：

| 基础地址 | 实际请求地址 |
| --- | --- |
| `https://api.example.com` | `https://api.example.com/chat/completions` |
| `https://api.example.com/v1` | `https://api.example.com/v1/chat/completions` |
| `https://api.example.com/custom/api` | `https://api.example.com/custom/api/chat/completions` |
| `https://api.example.com/v1/chat/completions` | 原地址，不重复追加 |

不会自动插入第二个 `/v1`。基础地址不允许用户名/密码、查询参数、fragment、明文 HTTP 或含混的转义路径；认证统一放在 `Authorization: Bearer …` 请求头中。

## 自动模型目录

打开已有供应商配置时，前端会请求该配置的最新模型目录，也可手动刷新。新建配置可以在填写密钥后、保存前同步。每次同步都从已配置地址读取 `GET /models`，返回模型 ID、所属组织和同步时间；没有内置的静态模型清单，也不会因上游新增或下线模型而自动更换已选模型。

若基础地址是完整的 `/chat/completions`，目录会使用同一基础路径下的 `/models`，例如 `https://gateway.example/v1/chat/completions` 对应 `https://gateway.example/v1/models`。目录读取与对话共用 HTTPS、DNS 校验和固定 IP 连接策略，保持 90 秒、2 MiB 上限，最多接受 10,000 条目录记录。接口响应禁止缓存，浏览器不会拿到已保存密钥。

供应商或网关不提供兼容目录时，页面保留手填模型 ID 的入口。上游 404、405、501 返回稳定错误码 `models_not_supported`；空目录返回空数组，认证/网络错误也不会冒充成功。千问的[原生模型目录](https://help.aliyun.com/en/model-studio/list-models)使用单独的 `/api/v1/models` 及分页结构，当前面板不替换为该原生接口；配置的兼容端点不支持 `/models` 时请手填。目录出现一个模型并不证明该账号有调用额度，也不证明该模型支持工具调用。

CPA 反代需要提供兼容的 `/chat/completions`，可选提供 `/models`，并保留工具调用及续轮字段。面板连接的这一段必须为 HTTPS；如果 CPA 仅监听本机 HTTP，可在它前面配置有有效证书的 HTTPS 入口。私网 HTTPS 还需部署管理员设置 `AI_ALLOW_PRIVATE_ENDPOINTS=true`。

## 密钥保存与修改

数据库中的 `api_key_encrypted` 使用部署实例现有的 `ENCRYPTION_KEY` 经 AES-GCM 加密。列表、创建与更新响应只返回 `has_api_key`，不返回密钥、密文或伪装的占位密钥。

修改名称、模型或启停状态时，省略 `api_key` 或填写空字符串会保留旧密钥。**任何基础地址变更都要求本次请求显式提交 API Key**，包括只改路径；面板不会自动把保存的密钥转发到新地址。检查在数据库事务内进行，另一请求抢先更改地址时，旧页面的空密钥更新同样会被拒绝。

停用提供商会阻止新的测试及调用；已经开始的外部 HTTP 请求无法由配置修改撤回。删除配置不等于撤销第三方平台上的密钥，需要时请另外在提供商控制台撤销。

## 地址与网络策略

默认只允许公网 HTTPS 目标。每次调用都会重新解析 DNS，检查全部返回地址，并将这次连接固定到已批准的 IP；混合公网/私网 DNS 答案会被拒绝。回环、私网、链路本地、云元数据地址、CGNAT、组播及保留地址均不作为默认公网目标。

客户端不跟随重定向，不使用环境 HTTP 代理，不复用上次解析的连接。TLS 至少为 1.2，证书信任与主机名必须通过系统验证。生产实现没有 localhost 特例或关闭证书验证的开关。

确需对接私有模型网关时，部署管理员可以显式设置 `AI_ALLOW_PRIVATE_ENDPOINTS=true`。这个开关只影响 AI 提供商层，与通知模块的 `ALLOW_PRIVATE_WEBHOOKS` 独立；开启后仍要求 HTTPS 和有效 TLS 证书。

## 请求与工具调用

实现直接调用 Chat Completions HTTP 接口，不依赖第三方模型 SDK。请求使用非流式响应；提供工具时设置 `tool_choice: auto`。DeepSeek 请求显式附加 `thinking: {type: "disabled"}`，其他类型不附加 DeepSeek 专用参数，参见[工具调用](https://api-docs.deepseek.com/guides/tool_calls)和[Chat Completions 协议](https://api-docs.deepseek.com/api/create-chat-completion)。OpenAI 类型使用 `max_completion_tokens`，兼容其将推理 token 计入生成预算的接口；其他类型保持 `max_tokens`，参见 [OpenAI Chat Completions 参数](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)。

单次调用的总时限为 90 秒，调用方取消会传到 DNS、建连及响应读取。请求与响应体均限制为 2 MiB；一般调用最多请求 4096 个输出 token。DeepSeek 连通测试只请求 32 个；其他提供商测试上限 1024 个，为可能启用的推理留出预算，避免在回复之前就被截断。模型服务可能按请求计费，面板不自动重试失败请求。

只有完整的 `stop` 文本结果或有效的 `tool_calls` 结果会被接受。工具调用必须拥有唯一 ID、`function` 类型、已声明的函数名和 JSON 对象参数；截断、内容过滤、资源不足、未知结束原因及不完整工具调用会报错，不交给执行层。参数仍是模型生成的不可信输入，后续动作层还需验证实际操作和权限，协议正确不表示操作获得授权。

响应允许 `content: null`，常见于仅返回工具调用的消息。若服务返回 `reasoning_content`，适配层保留它供同一会话后续工具调用继续使用；任务的公开响应不展示该字段。

Gemini 兼容协议的工具调用还会携带 `extra_content.google.thought_signature`；面板在私有会话中保留并原样回传这个不透明签名，使工具结果能够继续下一轮，参见 [Gemini 思考签名](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)。该字段不作为工具参数、执行授权或展示内容。

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
| `GET /api/ai/providers/:id/models` | 读取自己的已保存且启用的配置，使用后端密钥同步模型目录 |
| `POST /api/ai/providers/models` | 使用显式提交的 `kind`、可选 `base_url`、必填 `api_key` 预览目录；不读取或保存数据库配置，不需要 `model` |

目录成功响应示例：`{"models":[{"id":"provider-model","owned_by":"provider"}],"fetched_at":"2026-10-19T12:00:00Z","source":"provider"}`。`owned_by` 可省略。目录接口共用每分钟 20 次限流，并与连通测试共用每进程 4 个并发请求额度。

创建与更新请求体上限 16 KiB，拒绝未知字段及多个拼接 JSON 文档。测试接口限流，并限制同时运行的测试数量。跨用户读取、修改、删除或测试不会触发模型请求。
