# 调用 Polyglot API

调用模型时使用 **Polyglot API Key**（`pg_…`）。供应商密钥保存在 Polyglot 中，不要放进客户端请求。先在供应商上注册模型，再在 **API 密钥** 页面创建 Key。下文的 `BASE` 是 Polyglot 地址，例如 `http://localhost:3000`；所有路径都相对于它。

```sh
export BASE=http://localhost:3000
export KEY=pg_your_key
```

**客户端端点决定请求和响应格式**，不是上游供应商。发往 `/v1/chat/completions` 的请求，即使路由到 Gemini 或 Anthropic，返回的仍是 OpenAI Chat Completions 格式。五种 HTTP 协议经过 canonical 模型转换；Gemini Live 使用独立的 WebSocket 会话协议。

## 鉴权与模型名称

OpenAI 风格的接口用 `Authorization: Bearer $KEY`，Anthropic 用 `x-api-key: $KEY`，Gemini 用 `x-goog-api-key: $KEY`。它们认证的是同一种 Polyglot Key。Gemini 客户端也可以用 `?key=...`，但请求头不会把凭据留在 URL 中。

`model` 可以是下面三种形式：

| 写法 | 含义 |
|---|---|
| `gemini-2.5-flash` | 已注册的上游模型 ID；多个供应商提供同名模型时按优先级路由。 |
| `my-fast-model` | 在 Polyglot 中配置的可选别名。 |
| `google::gemini-2.5-flash` | 指定供应商上的已注册模型；用 `::` 是因为模型 ID 本身可以含 `/`。 |

未注册的名称返回 `404`，不会盲目转发给上游。API Key 也可能限制允许调用的模型名称。

## 列出可用模型

```sh
curl "$BASE/v1/models" -H "Authorization: Bearer $KEY"
curl "$BASE/v1/models/gemini-2.5-flash" -H "Authorization: Bearer $KEY"
curl "$BASE/v1beta/models" -H "x-goog-api-key: $KEY"
```

`GET /v1/models` 返回 OpenAI 风格的 `{"object":"list","data":[{"id":"…","object":"model"}],"has_more":false}`。`GET /v1/models/{model}` 返回单个模型或 `404`；Gemini 列表返回 `{"models":[{"name":"models/…"}]}`。列表只含当前 Key 可见的已注册模型和别名；供应商发现列表里的模型不会自动注册。把模型 ID 放在 URL 路径中时，要做 URL 编码。

## OpenAI Chat Completions

`POST /v1/chat/completions` 接收 Chat Completions JSON。`model` 和 `messages` 必填。加上 `"stream":true` 可接收 SSE 流，而非一次性 JSON 响应。

```sh
curl "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"你好"}]}'
```

响应包含 `choices[].message.content`；上游报告用量时还会有 `usage`。流式调用：

```sh
curl -N "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","stream":true,"messages":[{"role":"user","content":"你好"}]}'
```

这个端点也接收 OpenAI function tools。工具调用按客户端协议格式返回；流式参数可能分片到达。

## OpenAI Responses

`POST /v1/responses` 接收 `model` 和 `input`（字符串或受支持的结构化输入）。`stream:true` 返回 Responses 风格的 SSE 事件。

```sh
curl "$BASE/v1/responses" \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","instructions":"请简短回答。","input":"你好","max_output_tokens":128}'
```

响应是带 `output[]`、`status` 和可选 `usage` 的 Responses 对象。Polyglot 不保存会话历史；后续请求要自行发送完整上下文。`store:true` 和 `previous_response_id` 不能在这里实现服务端续聊，相关损失会写入请求日志的保真度备注。

## Anthropic Messages

`POST /v1/messages` 接收 Anthropic Messages JSON。要提供 `max_tokens` 和至少一条消息。`stream:true` 返回 Anthropic SSE。

```sh
curl "$BASE/v1/messages" \
  -H "x-api-key: $KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","max_tokens":256,"messages":[{"role":"user","content":"你好"}]}'
```

即使上游使用另一种协议，客户端看到的仍是 Anthropic 格式的 `content[]` 和 `usage`。

## Gemini generateContent

Gemini 把模型放在 URL 中。使用 `POST /v1beta/models/{model}:generateContent`（也支持 `/v1/`），请求体包含 `contents[]`。流式方法是 `:streamGenerateContent?alt=sse`，返回 Gemini SSE 帧。

```sh
curl "$BASE/v1beta/models/gemini-2.5-flash:generateContent" \
  -H "x-goog-api-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}],"generationConfig":{"maxOutputTokens":128}}'

curl -N "$BASE/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" \
  -H "x-goog-api-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}'
```

非流式响应包含 `candidates[].content.parts[]` 和可选的 `usageMetadata`。`countTokens`、`embedContent`、`batchEmbedContents` 尚未实现。

## Gemini Interactions

`POST /v1beta/interactions`（也支持 `/v1/interactions`）使用独立的 Interactions 格式：`model` 和 `input` 必填。`input` 可以是字符串，完整对话可以传入 step 数组。

```sh
curl "$BASE/v1beta/interactions" \
  -H "x-goog-api-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","input":"你好"}'
```

响应包含 `steps[]` 时间线。设置 `"stream":true` 时返回 SSE，事件类型在 JSON 内的 `event_type` 字段。Polyglot 会向上游明确发送 `store:false`，也不会使用 `previous_interaction_id`；请自行提供完整历史。

## Gemini Live（WebSocket）

Live **不是**给 `generateContent` 填一个 Live 模型名。先在 Gemini 供应商上注册 `gemini-3.8-live`，再让 Google Gen AI Go SDK 的 `Live.Connect` 连接到：

```text
ws://localhost:3000/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent
```

HTTPS 环境使用 `wss://`。在 `x-goog-api-key` 中提交 Polyglot Key。Go SDK 会根据 `BaseURL` 拼出上述路径：本地 HTTP 开发请把 `BaseURL` 设成 `ws://localhost:3000`（否则该 SDK 会把 `http://` 升级成 `wss://`）；HTTPS 部署则使用 HTTPS 地址。`APIVersion` 设为 `v1beta`，`Backend` 设为 `BackendGeminiAPI`。连接时 SDK 发送含 `model` 的 `setup` 并等待 `setupComplete`；之后可发送 `realtimeInput.audio`、`clientContent`、`toolResponse`，接收音频、转写、打断和工具调用。一个会话结束后记一行日志；只有上游报告用量时才能计价。

配置说明见 [README 的 Live 章节](../README.zh-CN.md)。其它实时客户端协议尚未实现。

## 错误和限制

错误响应体遵循**客户端端点**的协议格式。先看 HTTP 状态码，再按对应协议解析 JSON：

| 状态码 | 常见原因 |
|---|---|
| `400` | JSON 无效、缺少必填字段或调用了未支持的方法。 |
| `401` | Polyglot Key 缺失、无效、已禁用或已过期。 |
| `403` | Key 不允许调用该模型，或花费预算已用完。 |
| `404` | 模型／别名未注册，或当前 Key 无法访问。 |
| `429` | Key 的请求／token 限额或会重置的预算窗口；有 `Retry-After` 时按它重试。 |
| `502` / `503` / `504` | 上游失败、过载或超时。 |

Live WebSocket 升级完成后，错误通过关闭连接报告，不再返回另一个 HTTP 响应。未知模型或 Key 策略拒绝会以策略错误关闭。输入和上游响应有大小上限，客户端断开时上游连接也会取消。不能无损转换的字段会记入请求日志的**保真度备注**，不会悄悄丢掉。

## 管理与日志 API

运维者使用的 `/api/*` 与模型调用分开，鉴权用**管理员会话 Cookie**，不能用 `pg_…` 模型 Key。`POST /api/auth/login` 接收 `{"username":"…","password":"…"}` 并设置会话及 `polyglot_csrf` Cookie。后续请求携带会话 Cookie；修改状态的请求还须把 CSRF Cookie 的值放进 `X-CSRF-Token` 请求头。

登录后，`POST /api/providers` 可以提交 `{"name":"google","protocol":"gemini","base_url":"https://generativelanguage.googleapis.com","api_key":"…","models":[{"id":"gemini-2.5-flash"}]}`。`POST /api/keys` 接收 `{"name":"my-client"}`，返回 `{"key":{…},"secret":"pg_…"}`；请按凭据保护这个明文。`GET /api/providers`、`/api/models`、`/api/aliases`、`/api/keys`、`/api/logs` 可查询管理员的资源，均需管理员会话。`GET /api/setup` 可查看是否需要初始化，首次 `POST /api/setup` 还需 `X-Polyglot-Setup-Token`。

需要通过**单独的只读日志 Key** 查询请求日志时，请看[内容日志 API 指南](log-api.md)。日志 Key 不能调用模型，也不能管理供应商。
