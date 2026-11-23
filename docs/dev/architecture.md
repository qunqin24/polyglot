# 架构与请求管线

## 总览

```
              decode                          encode
OpenAI ──────────────┐                    ┌────────▶ OpenAI
OpenAI Responses ────┤                    ├────────▶ OpenAI Responses
Anthropic ───────────┼──▶  Canonical  ──▶ ┼────────▶ Anthropic
Gemini ──────────────┤                    ├────────▶ Gemini
Gemini Interactions ─┘                    └────────▶ Gemini Interactions
```

每个协议只实现「协议 ↔ canonical」（`internal/protocol.Codec`），所以 N 个协议是 N 个 codec，不是 N² 个转换。每个请求都走同一条路径，OpenAI → OpenAI 也不例外：

```
客户端协议 → canonical → 路由 → 上游协议 → 上游
上游 → canonical → 客户端协议
```

同协议路由不是单独的代码路径：它照样先解码到 canonical 再编码出去，只是 extension、原生工具这类「只能原样回放」的东西在这条路由上能带过去（见下文「保真度」）。

## 一个请求的生命周期

入口是 `internal/api/server.go` 的 `Handler()`，每个协议端点挂在 `auth.Gateway(store, protocolName)` 中间件后面，然后交给 `gateway.Gateway.Chat`（`internal/gateway/gateway.go`）。顺序如下：

1. **认证**：`auth.Gateway` 按 API key 的 SHA-256 哈希查 key，检查启用状态和过期时间；失败时按客户端协议的错误格式返回（OpenAI SDK 收到的是 OpenAI 形状的 401）。key 带着它所属的团队，后面所有团队数据的查询都通过 `store.ForTeam(key.TeamID)` 得到的 `*store.Scope` 进行。
2. **开始记录**：`Telemetry.StartRequest` 建立请求生命周期对象；如果该团队开了完整内容记录，创建 `capture.Recorder`。
3. **key 限额**：`KeyLimiter.Acquire` 检查 RPM/RPH/RPD、TPM/TPD、并发、花费预算。限速返回 429 加 `Retry-After`；预算耗尽且没有可滚动的窗口时返回权限错误。
4. **读 body、解码**：body 上限来自设置 `max_request_mb`；`clientCodec.DecodeRequest` 得到 `canonical.Request`，同时往 `Diagnostics` 记备注。Gemini 的模型名和是否流式在 URL 里，通过 `Options.ModelOverride` / `ForceStream` 覆盖。然后 `auth.ApplyRequestPolicy` 应用 key 的限制（模型白名单、`max_output_tokens`）。
5. **路由**：`Router.Resolve` 返回候选列表（顺序和规则见 [providers-and-routing.md](providers-and-routing.md)），再依次经过 `router.PreferProtocol`（同优先级内偏向说客户端协议的上游）和 `Gateway.healthy`（跳过冷却中的 provider；全部冷却时忽略冷却）。
6. **逐个候选尝试**（`attempt.run`）：复制请求并换成上游模型名；`StrictFields` 的 provider 去掉 extensions；Gemini 上游且开了 `fetch_remote_media` 时先下载远程附件（`internal/media`）；用上游协议的 codec `EncodeRequest`；`provider.DriverFor(protocol).ChatRequest` 拼出 HTTP 请求（路径、鉴权头）；`provider.Client.Do` 发出。
7. **响应**：上游状态码 ≥ 400 转成 canonical 错误并脱敏；否则非流式走 `bufferedResponse`（`DecodeResponse` → `EncodeResponse`），流式走 `streamResponse`（见下）。成功时响应头带 `X-Polyglot-Provider`。
8. **重试**：失败且错误可重试（`retryable`：upstream / overloaded / timeout / rate_limit 类型，或状态码 ≥ 500）、还没向客户端写出任何字节时，换下一个候选。写出过字节之后不重试。
9. **收尾**：`Gateway.finish` 同时关闭遥测对象和请求日志行（两者来自同一组数字），日志经 `usage.Logger` 缓冲、批量写入 SQLite，并在那里算成本。

失败路径上的几条规则：

- 上游返回 4xx 里的 `invalid_request` / `not_found` / `unsupported` 是调用者的问题，不记入 provider 健康状态（`noteFailure`）。
- 认证类错误会累计「连续拒绝次数」，开了 `auto_disable_on_auth_error` 的 provider 连续 2 次（`provider.AuthStrikesBeforeDisable`）后自动停用并写明原因。
- 流在响应头发出之后才失败，状态码只能记 200，错误通过流内的 error 事件告诉客户端。
- 客户端断开记为 `cancelled`（状态码 499，`error_type=client_disconnect`），不算上游失败。

## 包的职责和依赖方向

下面是 `internal/` 下各包的 import 关系（只列包内依赖，用 `go list` 核对过）。箭头方向是「依赖」。

```
canonical        （叶子：不依赖任何内部包）
stream           （叶子：SSE 分帧）
idgen / pricing / config / update / version   （叶子）
protocol         → canonical
protocol/<名字>  → canonical, protocol, stream, idgen
provider         → protocol
store            → pricing
router           → canonical, protocol, provider, store
auth             → canonical, idgen, protocol, store
usage            → pricing, store
telemetry        → canonical
settings         → idgen
media / capture / setup → canonical 或 idgen
gateway          → 上面几乎所有包
api              → gateway 及全部（含各 protocol 子包的空白 import）
cmd/polyglot     → api, config, settings, setup, store, telemetry, usage, pricing …
```

几条由这张图推出来的约束：

- `canonical` 和 `protocol` 不知道 HTTP、数据库、供应商。codec 里不能出现 provider 名字或 URL。
- `provider` 只管「怎么调」（URL、鉴权头、transport），不碰消息格式；`protocol` 只管消息格式，不知道怎么调。这样 OpenRouter、DeepSeek、SiliconFlow 能共用一个 codec，而在 provider 层各有差别。
- 所有 SQL 都在 `store`。其他包只调用带类型的方法。
- `gateway` 是把这些拼起来的地方；`api` 只负责 HTTP 表面（路由、管理 API、WebUI 服务）。

各包一句话职责见 [repo-layout.md](repo-layout.md)。

## canonical 模型

`internal/canonical` 的主要类型：

- `Request` / `Response` / `Message` / `ContentPart`：消息内容是 part 的列表（文本、图片、文件、工具调用、工具结果、推理等，见 `PartType`）。
- `Event`：流事件，类型是 `message.start`、`text.delta`、`reasoning.delta`、`tool_call.start`、`tool_call.arguments.delta`、`tool_call.end`、`usage`、`message.end`、`error`，另有 `native`（原样携带的 provider 事件，只在同协议路由上转发）。
- `Usage`：`InputTokens` 是整个 prompt（含缓存部分），`CachedInputTokens`、`CacheWriteTokens` 是它的组成部分。Anthropic 的 `input_tokens` 不含缓存，所以它的 codec 解码时求和、编码时拆分（`TestAnthropicCacheCountsAreConvertedNotCopied`）。
- `Error` / `ErrorType`：协议中立的错误类型（`invalid_request`、`authentication`、`permission`、`not_found`、`rate_limit`、`overloaded`、`upstream`、`timeout`、`internal`、`unsupported`），各 codec 的 `EncodeError` 把它渲染成自己协议的错误形状。
- `Extensions` / `NativeTools`：见「保真度」。
- `LiveMessage`（`live.go`）：Gemini Live 会话的消息，见下面「Live」。

## 流式

流式是核心路径，不是补丁。

- **分帧**：`internal/stream` 只管 SSE 的字节层。`stream.Reader.Next` 把字节流切成 `Frame{Event, Data}`，兼容 `\n` 和 `\r\n`、忽略注释行，单个字段上限 8 MiB；`stream.Writer.Event` 写出一帧并立刻 `Flush`；`SetSSEHeaders` 设置 `text/event-stream`、`Cache-Control: no-cache, no-transform`、`X-Accel-Buffering: no`。协议语义在各 codec 里，不在这个包。
- **解码方向**：`Codec.DecodeStream(ctx, r, emit)` 读上游流，产出 `canonical.Event`；`emit` 返回错误表示客户端已断开，必须立刻停止读取。
- **编码方向**：`Codec.NewStreamEncoder(w, req)` 返回 `StreamEncoder`，`Write(ev)` 把事件写成该协议的流帧并 flush，`Close()` 写协议要求的终止符（如 `[DONE]`）。
- **工具调用参数是分片的**：`tool_call.arguments.delta` 的内容不保证是合法 JSON，不能解析一个分片。需要完整对象时累积原始字节、在 `tool_call.end` 之后再解析（`canonical.Accumulator`）。Gemini 需要完整的 `functionCall` 对象，所以它的流编码器会缓冲，这是目标协议的性质，不是通用做法。
- **热路径上只做最少的事**：每个事件一次类型比较，内容事件再读一次时钟（`ContentToken`，用来算 TTFT）。不按 token 建 span，不按 chunk 写日志，不为了计数缓冲整个响应。
- **必须遵守的**：传递 `context.Context`，客户端断开要把上游一起拆掉；总是关闭 `resp.Body`；复用共享的 `http.Transport`（`provider.Client`），不为每个请求新建；每个 SSE 帧之后 flush；日志里区分 `cancelled` 和 `error`。

## 保真度与诊断

协议之间不等价，所以每次转换中发生的事都要记下来，记不下来就是 bug。

`canonical.Diagnostics`（`internal/canonical/fidelity.go`）收集一次请求里的转换备注 `Note{Stage, Field, Fidelity, Detail}`。`Stage` 形如 `decode:openai`、`encode:gemini`，gateway 自己记的用 `route`、`media`。四个等级：

| 等级 | 含义 |
|---|---|
| `FidelityExact` | 有直接、完全对应的字段 |
| `FidelitySemantic` | 表达方式不同，意思一样 |
| `FidelityLossy` | 带过去了，但有损失 |
| `FidelityUnsupported` | 目标协议表达不了 |

`*Diagnostics` 为 nil 时所有方法都是空操作，所以单测里可以不传。`Mode` 字段有 strict / warn / best_effort，目前网关只用 `best_effort`（继续转换、只记录），另外两个是预留。备注由 `gateway.encodeNotes` 序列化成 JSON，存进请求日志的 `fidelity_notes`，WebUI 日志详情里显示。

`internal/protocol/matrix_test.go` 遍历全部 5×5 协议配对，要求一个字段要么在往返中存活，要么产生一条备注；`sameprotocol_test.go` 的 `TestSameProtocolRoundTrip` 用同一个协议解码再编码一个内容丰富的请求，要求每一处差异都是列出来并给出理由的规范化。任何 codec 改动都要让它们保持绿色。

### 不认识的字段：带过去，不是丢掉

codec 解码进结构体，`encoding/json` 会丢掉结构体没命名的成员，这就是悄悄丢弃。所以：

- 解码时调用 `protocol.Capture(proto, body, scopes...)`，把没有被任何 scope 认识的成员收进 `canonical.Extensions`；编码时调用 `protocol.Merge`（都在 `internal/protocol/passthrough.go`）。OpenRouter 的 `provider`、vLLM 的 `guided_json`、DeepSeek 的 `prefix` 就这样能活下来。
- `Merge` 里写死了一条规则：extension **只在源协议和目标协议相同时**回放，否则一律记成 `FidelityUnsupported`，不跨方言转发（Polyglot 没法翻译的字段会变成上游的 400）。extension 永远不覆盖编码器自己产生的成员，路由后的模型名胜过 body 里的任何东西。这不是 passthrough 模式，穿过 canonical 的代码路径仍然只有一条。
- `Capture` 只遍历 `Top` / `Nested` 里点名的对象，**只到顶层或点名的路径**。更深的厂商字段必须建模进 canonical，否则会在重新编码时消失：Anthropic 的 `cache_control` 在 `messages[].content[]` 里，就是靠 `canonical.CacheHint` 才没被悄悄关掉。
- 给字段起名就等于把它排除在 `Capture` 之外。所以结构体里要么有成员并且 canonical 读了它，要么干脆没有这个成员，让 passthrough 带着走。一个声明了却没人读的成员比没有更糟。
- 在别的协议里有精确对应的字段属于 canonical，不属于 extension。extension 在其他四个协议上只会被报成不支持，把本可转换的字段留给 `Capture` 会把一次干净转换变成假的损失报告（Gemini 的 `createTime` 就是 OpenAI 的 `created`）。

### provider 执行的工具

Gemini 的 `googleSearch`、Anthropic 的 `web_search`、OpenAI 的 `file_search` 在 provider 内部执行，没有可中继的内容，也不存在诚实的跨协议映射。它们进 `canonical.Request.NativeTools`，通过 `protocol.MergeNativeTools` 回去，规则和 extension 一样只在同协议路由上回放。不要在它们之间发明翻译；从原始请求里读它们，而不是照一份固定的名字清单，这样厂商新发布的工具才不会凭空消失。

### 回放令牌

推理签名、`thoughtSignature` 这类东西是上游要求下一轮原样带回的令牌，不是内容：

- 判空之前先看令牌。一个只带签名的流事件没有文本，`if ev.Text == ""` 会把它悄悄扔掉（`interactions/stream.go` 是正确写法）。
- 令牌可能挂在不属于它的 part 上：Gemini 把思考块的 `thoughtSignature` 放在后面那个 part 上（普通回答就是文本 part），所以有 `ContentPart.Signature` 和 `Event.Signature`。加新协议时要检查令牌可能落在的每一种 part。
- Gemini 签的是一个思考块（一串 thought part 加一个单独的签名 part），不是单个 part。`gemini/codec.go` 的 `signedThoughtRuns` 把整串当一个单位；`TestReplayingAStreamedThoughtLosesNothing`（`gemini/signature_test.go`）走完整个回路。
- 绑定 provider 的令牌要能穿过客户端往返一圈：`canonical.ToolCall.Signature` 用 Google 定义的 `extra_content.google.thought_signature` 信封（`internal/protocol/extension.go`）传给客户端再传回来。它不会发给没定义过它的上游（记成 lossy）。完全没有签名的历史会拿到 Google 文档里的占位符并加一条备注，因为未签名的调用是硬性 400。

### 多模态

图片和 PDF 在五个协议之间都能转换。内联 base64 是所有协议都能表达的形状，不能有损。远程 URL 只转发给自己会去抓取的协议（`protocol.AcceptsMediaURL`，Gemini 不会），所以到 Gemini 的远程 URL 会被报告，除非设置里开了 `fetch_remote_media`，这时 `internal/media.Fetcher` 先下载并转成内联。`file_id` 绑定 provider，按回放令牌的规则处理。`internal/media` 是 Polyglot 唯一会去拨一个由**客户端**选择的地址的地方，所以私有网段在那里无条件拒绝，不受 `block_private_upstream` 控制，这一点不能放松。

## Live（Gemini WebSocket）

Gemini Live 是双向 WebSocket 会话，不是请求/响应或 SSE，所以它不实现 `protocol.Codec`，而是 `internal/protocol/live` 里的会话 codec（协议名 `gemini-live`），消息类型是 `canonical.LiveMessage`。入口 `gateway.Gateway.Live`（`internal/gateway/live.go`）在升级 WebSocket 之前先解码第一条消息，让路由和 key 限制仍然是普通 HTTP 错误；升级之后每一帧都经过 canonical。上游连接由 `provider.Client.DialLive` 建立，只支持协议为 `gemini` 且不是内置多协议供应商的 provider。目前只支持 Gemini Live 客户端到 Gemini Live 上游。

## 遥测与请求日志

- `telemetry.Request`（`internal/telemetry/request.go`）是一个请求的唯一计量对象，Prometheus 指标和请求日志行都从它填。业务代码只调用 `StartRequest`、`StartAttempt`、`Retrying`、`Streaming`、`ContentToken`、`Usage`、`Finish` 这些方法，不直接碰 Prometheus 计数器或 span exporter。即使遥测关闭，这个对象也存在，因为耗时、请求 id、重试次数是请求日志要用的。
- 隐私是结构上的：prompt、补全、工具参数、请求头、凭据、query string、请求体和上游错误文本不会被传进 `internal/telemetry`，所以那里没有东西可泄漏。只有当一个新属性是协议、provider、模型、是否流式、状态、错误类别或计数时才可以加。
- 指标标签来自有界集合：provider 和模型可以做标签，因为是运维者配置的；客户端自己编的模型名记为 `unrouted`；请求 id、trace id、API key、IP、URL、错误消息从不做标签。注册表给每个指标的不同序列数封顶，多出来的折叠进一个溢出序列，不要拿掉这个上限。
- 请求日志**默认只记录元数据**，每个请求一行，经 `internal/usage` 缓冲后批量写入，不按流式 chunk 写库。完整正文记录是单独的、默认关闭的功能（`internal/capture`，见 [data-and-storage.md](data-and-storage.md)）。
- 目前只实现了 Prometheus 文本格式的指标和 OTLP/HTTP（JSON 编码，发往 `/v1/traces`）的 trace；OTLP over gRPC 和 metrics over OTLP 没有实现，文档里不要写成已有。
