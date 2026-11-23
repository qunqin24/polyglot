# 供应商与路由

## 术语

- **协议**：线上格式，一共五个，名字见 `protocol.Name`。
- **供应商（provider）**：运维者在 WebUI 里配置的一个上游服务，存在 `providers` 表。OpenRouter、DeepSeek、SiliconFlow、Groq、vLLM、Ollama 都是说 OpenAI 协议的 provider，共用一个 codec。
- **模型**：属于某个 provider 的上游模型，存在 `models` 表（`provider_id` + `upstream_model_id`）。
- **别名**：`model_aliases` 表里的一行，给一个 provider 上的某个上游模型起逻辑名字。同名的多行互为备选。别名是可选的，永远不要变成必填项。

`internal/provider` 只管「怎么调」：`Target` 是解析好的一次调用所需的一切（协议、base URL、API key、自定义头、超时、`StrictFields`、`AutoDisableOnAuthError`、`Vendor`、`BearerAuth`）；`Driver` 按协议决定聊天请求的路径和鉴权头，**不碰请求体**（体由 codec 生成）。五个协议各有一个 driver（`driver.go`）。

## 单协议供应商

最常见的情况：一个 base URL，一种协议。`ValidateBaseURL` 在保存 provider 时和每次上游调用之前都会检查：必须是 http/https、有 host、不能内嵌凭据。`web/src/lib/provider-form.ts` 里的预设（见下）只是 base URL + 协议的组合，不是专门的集成代码。

provider 上的其他字段：

- `priority`：数字大的优先（`ORDER BY p.priority DESC`）。注意 `web/src/lib/api.ts` 里 `Provider.priority` 的注释写的是「Lower wins」，和代码相反，见文末「已知不一致」。
- `strict_fields`：上游会拒绝不认识的成员时打开。打开后同协议路由也不回放 extensions，改为报告 unsupported。默认关，让 provider 自己的参数能通过。
- `auto_disable_on_auth_error`：凭据被连续拒绝后自动停用（见「健康与冷却」）。默认关，因为 401/403 不一定是 key 的问题。
- `timeout_secs`：0 表示用设置里的 `upstream_timeout_secs`。
- `headers`：自定义头；`authorization`、`x-api-key`、`x-goog-api-key` 不允许在这里设，凭据只能走 API Key 字段（`providerInput.validate`）。`applyCustom` 也不会覆盖 driver 已设的鉴权头。
- `api_key`：加密存储，`json:"-"`，永远不会到达浏览器（见 [data-and-storage.md](data-and-storage.md)）。

## 内置多协议供应商（vendor）

有的服务用一个账号同时提供几种 API，并按模型决定哪个协议是原生的（Claude 走 `/messages`、GPT 走 `/responses`、开源模型走 `/chat/completions`）。如果一个 provider 只能有一种协议，运维者就得建好几个 provider，并且自己记得哪个模型在哪个上。`internal/provider/vendor.go` 解决这个：运维者选一次预设、贴一个 API key，Polyglot 知道每个协议的 base URL、怎么鉴权、每个模型用哪种协议。

目前有两个：`opencode-go`（OpenCode Go）和 `commandcode`（Command Code）。

数据模型（迁移 `0022_model_protocol.sql`）：

- `providers.vendor`：空表示普通 provider（走自己的 `protocol` 和 `base_url`，永远不会进 `vendor.go` 的代码路径）；非空是 vendor id。此时 `protocol` 存 vendor 的 `Primary` 协议、`base_url` 存 vendor 的 `Root`，都只是展示用，`providerInput.validate` 会覆盖掉用户提交的值。
- `models.protocol`：模型自己的协议覆盖，空表示用 provider 的。

一次路由怎么确定协议：`Router.targetFor`（`internal/router/router.go`）对 vendor provider 调 `Vendor.ProtocolFor(override, modelID)`，优先级是：

1. 模型行（或别名所指模型）的协议覆盖，前提是 vendor 提供该协议（`Bases` 里有）；
2. vendor 的 `fallback(modelID)` 按 id 前缀猜（例如 OpenCode Go：`claude*`、`qwen*` 走 Anthropic，`gpt-*`、`grok-*` 走 Responses，`gemini*` 走 Gemini，其余走 OpenAI）；
3. `Primary` 协议。

然后 `Vendor.Apply` 把 `Target` 的协议和 base URL 改成该协议对应的 `Bases[proto]`，设置 `BearerAuth`（两个 vendor 的所有端点都要求 `Authorization: Bearer`，所以 Anthropic、Gemini driver 也会同时发 Bearer），并附加必须的头（OpenCode Go 要求每个请求带稳定的 `x-opencode-session`，由 provider id 和 key 的哈希派生，不用运维者提供）。`Apply` 会复制 `Headers` 再改，因为原 map 属于存储里的 provider。

模型发现：vendor 实现 `ModelDiscoverer`，只请求一次 `ModelsURL`（列表与协议无关），`parse` 读出每个模型该用哪种协议（OpenCode Go 看 `recommended_api`/`api`，Command Code 看 `supported_endpoints`）。`ParseModels` 会把 vendor 不提供的协议改回 `ProtocolFor` 的结果。登记模型时（`registerChosenModels`）把协议存进 `models.protocol`；手动添加的模型（`handleCreateModel`）则按 vendor 的规则选协议。列表读不到时用规则兜底，下次同步会修正。

别名的协议：`aliasCols` 通过子查询取别名所指注册模型的 `protocol`，路由时同样传给 `targetFor`。

当前限制：vendor provider 不支持 Gemini Live（`DialLive` 要求 `t.Vendor == nil`）。

### 新增一个内置供应商预设

后端：

1. `internal/provider/vendor.go`：加一个 `Vendor{...}` 到 `vendors`，并加常量（如 `VendorXxx`）；字段含 `Label`、`Root`、`Primary`、`Bases`（每个协议的 base URL，用对应 driver 期望的形式）、`Bearer`、`ModelsURL`、`parse`（解析模型列表并标出每个模型的协议）、`fallback`（列表不可用时按 id 猜协议）、可选的 `session`（必须的额外头）。
2. 把 id 加进 `VendorIDs()`，它同时用于校验报错信息（`vendor must be empty or one of: ...`）。
3. 测试：参考 `vendor_test.go`（provider 包）、`router/vendor_test.go`、`api/vendor_test.go`；`provider.RebaseVendor(id, origin)`（`vendor_testing.go`）把 vendor 的所有 URL 指向 `httptest` 服务，保持真实的路径布局，测试不碰外网。

前端：

1. `web/src/lib/provider-form.ts`：在 `PRESETS` 里加一项，设 `vendor: "<id>"`，`group: "platform"`；`protocol` 和 `base_url` 只是展示，要和后端 `Primary`、`Root` 一致。
2. `web/src/components/brand-icon.tsx`：给 `brand` 字段加对应的 `BRANDS` 条目。图标包里没有的品牌不要借用别人的（Command Code 用的是 `web/src/assets/brand/terminal.svg`）。
3. 新增的文案要同时加进 `en.ts` 和 `zh.ts`。

## 路由

`Router.Resolve(ctx, scope, model)` 的顺序，最具体的优先：

1. `provider::model`：直接点名 provider。用 `::` 而不是 `/`，因为模型 id 里常有斜杠。provider 不存在、被停用、或没有该模型都是 `not_found`。
2. 别名：`AliasesFor`，同名多行按 `priority DESC, id` 排。别名没写上游模型名时，用别名本身当上游模型名。
3. 注册表里的真实上游模型 id：`ModelsByUpstreamID`，按 provider 的 `priority DESC, id`。

三条都不匹配就是错误，不会因为「上游说不定认识」就转发。打错的名字应当以 Polyglot 说不认识这个模型的形式返回，而不是变成某个厂商的 404。别名在真实 id 之前检查，所以运维者可以用别名遮住一个令人困惑的上游名字。

**歧义必须是确定的。** 多个 provider 提供同一个模型 id 时，顺序是 provider `priority`（大的在前）再按 `id`，是全序、稳定的。不随机，也不为此做负载均衡。运维者想要别的，用 `provider::model` 或别名。

`router.PreferProtocol`（`prefer.go`）在 **同一优先级内** 把协议与客户端相同的 provider 排到前面，因为只有同协议路由才带 extension、原生工具和不加信封的推理签名。它是偏好，不是过滤：协议不匹配的 provider 仍然是候选（所有协议都能互相转换），也不会跨优先级重排，因为优先级是运维者明确表达的意图。

`Router.ListModels` 生成客户端看到的模型列表（`/v1/models` 等）：所有启用的别名加所有启用的注册表模型；有歧义的 id 额外列出 `provider::model` 形式。

`Router.Resolve` 返回的候选，由 gateway 依次尝试：不可重试的错误或已经写出字节后停止，否则换下一个（流程见 [architecture.md](architecture.md)）。

## 模型发现与注册

**发现只是提议，决定权在运维者。** 列出上游模型只展示有什么可选，什么都不写入；只有运维者在选择器里勾上的模型才会注册。

- 发现是可选能力：driver 实现 `provider.ModelDiscoverer` 才支持。列举只在被请求时发生：`POST /api/providers/discover`，在 provider 还不存在时也能用（用表单里的凭据）；`POST /api/providers/test` 是连接测试。
- 列举失败不能让保存失败。作为信息返回（`ok:false` 加上 `supported`），不是创建错误。一个没法列模型的上游很正常，手动输入模型就行。
- 一个没有模型的 provider 是合法配置。
- 手动输入的模型和选出来的模型调用方式完全一样。
- 归属关系属于 provider：模型在 provider 上增删（WebUI 里是 provider 的模型面板，`components/provider-models.tsx`）。Models 页回答的是「这个网关对外提供什么」，所以没有新增和删除。
- 重新读取列表绝不删除。某次列表里没出现的模型保留它的行和更旧的 `last_seen_at`；永远不覆盖运维者的 `enabled` 和显示名（`SyncModels`；`TestSyncDoesNotDeleteOrOverrideOperatorChoices`）。

## 健康与冷却

`provider.Health`（`health.go`）记住刚刚失败的上游，避免把请求再送进上一个请求刚撞上的墙。

- 状态按 provider 记（不按模型），因为值得响应的失败（连不上、凭据被拒、限流）是上游的性质。
- 只在进程内，不持久化：冷却是对过去 30 秒的判断，带着它重启就是带着过时的看法启动。
- `Failed` 开始冷却（时长来自设置 `provider_cooldown_secs`，默认 30 秒，运行时可改，已经在冷却的保留原截止时间），`Succeeded` 清空，provider 被编辑或删除时 `Forget`。
- 路由时 `Gateway.healthy` 跳过冷却中的候选；只有一个候选时不跳；全部在冷却时忽略冷却，这样短暂的全局故障不会让网关把自己锁在上游之外。跳过会记一条 `route` 阶段的备注。
- 凭据被拒（`ErrAuthentication`）单独计数。连续 `AuthStrikesBeforeDisable`（2）次且 provider 开了 `auto_disable_on_auth_error` 时，`DisableProvider` 把它停用并写下 `disabled_reason`、`disabled_at`；凭据不会自己恢复，每 30 秒重试一次只是持续的失败。
- 调用者自己的错误（`invalid_request`、`not_found`、`unsupported`）不计入，否则一个发了坏 JSON 的客户端会让健康的 provider 被标成不健康。
- 管理 API 在 provider 列表里返回 `cooling_until`，WebUI 据此显示「正在被跳过」。

## 已知不一致

- `web/src/lib/api.ts` 里 `Provider.priority` 的注释是「Lower wins when several providers offer the same model id」，而后端、`en.ts` 和 `docs/api.md` 都是数字大的优先。以代码为准：大的优先。
- `web/src/lib/api.ts` 里 `ProtocolName` 只有四个值，缺 `gemini-interactions`；`web/src/pages/logs.tsx` 的协议筛选下拉也没有它。后端五个协议都已注册。
