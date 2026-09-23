# Polyglot 开发指南

本仓库的工作规则。改任何东西之前先读。

这个文件是所有编码 agent 的唯一规则来源。`CLAUDE.md` 用 `@AGENTS.md` 引入它，
规则不要在那边重复写一遍。

## 项目定位

**定位 —— 智能化的个人 AI 网关。** 默认一个人运行；团队是可选的隔离层。这里的*智能*
说的是管道本身，不是功能清单：把模型名路由到正确的 provider、转换协议时不丢字段、
告诉运维者一次请求花了多少钱。它永远不表示在网关之上再加一个 AI 功能。

**长期愿景 —— 成为 LLM API 协议之间的通用兼容层。** 任何一个支持的客户端协议都能
到达任何一个支持的上游协议。加第六个协议只需要写一个 codec，它和其余协议的所有配对
都是白送的；这个性质就是愿景，也正是 canonical 模型要保护的东西。

**品牌主张 —— 你的 AI 不该由某一家厂商定义。** 你喜欢的客户端和你想用的模型由不同
公司决定，这就是要跑这个东西的理由。Polyglot 是一个**轻量的 LLM API 协议转换网关**，
它把这件事变成别人的问题。

它**不是**一个 API 转售平台。判断任何一个改动只用一个问题：*这是协议转换真正需要的
吗？* 不是就别加。

竞争力来自转换准确度、稳定的流式、好用的工具调用兼容性、简单的部署方式、能用的 WebUI
和低资源占用 —— 永远不来自功能数量。

## 核心架构

```
              decode                          encode
OpenAI ──────────────┐                    ┌────────▶ OpenAI
OpenAI Responses ────┤                    ├────────▶ OpenAI Responses
Anthropic ───────────┼──▶  Canonical  ──▶ ┼────────▶ Anthropic
Gemini ──────────────┤                    ├────────▶ Gemini
Gemini Interactions ─┘                    └────────▶ Gemini Interactions
```

每个请求都走同一条路径，OpenAI → OpenAI 也不例外：

```
客户端协议 → canonical → 路由 → 上游协议 → 上游
上游 → canonical → 客户端协议
```

三个概念**严格区分**，永远不要混在一起：

- **协议（Protocol）** —— 一种线上格式：`openai`、`openai-responses`、`anthropic`、
  `gemini`、`gemini-interactions`。OpenAI 出了两种确实不同的线上格式，Google 也是，
  所以每一对算两个协议，不是一个协议带一个模式开关。
- **供应商（Provider）** —— 一个你去调用的服务：OpenRouter、DeepSeek、Google
- **模型（Model）** —— 属于某个 provider 的一个真实上游模型

**别名（alias）** 是可选的第四层：一个指向 provider + model 的逻辑名字。永远不要把它
变成必填项。

OpenRouter、DeepSeek、SiliconFlow、Groq、vLLM 和 Ollama 都是说 *OpenAI 协议* 的
*provider*。它们共用一个 codec。永远不该出现 `protocol/openrouter.go`。

## 技术栈

已经定死。任何一项都不要换。

| 层 | 选型 |
|---|---|
| 后端 | Go、`net/http`、`chi` —— 标准库优先 |
| 前端 | React 19、Vite、TypeScript、Tailwind v4、手写的 shadcn/ui 组件 |
| 数据库 | SQLite，用 `modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`） |
| 打包 | WebUI 用 `go:embed` 嵌入；一个二进制、一个容器、一个进程 |
| 包管理器 | pnpm（见 `web/package.json` 里的 `packageManager`） |

Go 1.25+（`go.mod` 要求）和 Node 20+。pnpm 版本由 `web/package.json` 的
`packageManager` 钉住，交给 corepack 去遵守。

## 仓库结构

```
cmd/polyglot/          入口：配置、store、HTTP server、关闭流程
internal/
  canonical/           协议中立的 Request/Response/Event、错误、保真度
  protocol/            Codec 接口 + 注册表
    openai/            OpenAI Chat Completions codec
    responses/         OpenAI Responses codec
    anthropic/         Anthropic Messages codec
    gemini/            Gemini generateContent codec
    interactions/      Gemini Interactions codec
  provider/            怎么"调用"一个上游：URL、鉴权头、HTTP transport
  router/              模型名 → provider + 上游模型
  gateway/             请求管线
  stream/              SSE 分帧，两个方向
  store/               所有 SQLite 访问
  auth/                API key 和管理员会话
  pricing/             官方价格，以及一次请求花了多少
  usage/               带缓冲的请求日志
  telemetry/           请求生命周期、指标、tracing
  api/                 HTTP 表面：协议端点、管理 API、WebUI 服务
  config/ idgen/ version/
migrations/            *.sql，按文件名顺序执行，已嵌入
tests/compatibility/   官方 SDK 测试 —— 一个独立的 Go module
web/src/
  lib/                 api client、hooks、i18n 词条、工具函数
  components/ui/       shadcn 风格的基础组件
  pages/               一个页面一个文件
```

不要建很深的目录树。一个包要有自己的活干才配存在。

## 协议转换规则

**永远不要写 A → B 的转换。** 不要 `openaiToAnthropic`，不要 `gemini_to_openai.go`。
每个协议只实现 `Protocol ↔ Canonical`，所以 N 个协议是 N 个 codec，而不是 N²。现在五
个协议；加第六个就是一个包，它和其余协议的所有配对都白送。

每个 codec 都实现 `protocol.Codec`（见 `internal/protocol/protocol.go`）：
`DecodeRequest` / `EncodeRequest` / `DecodeResponse` / `EncodeResponse` /
`DecodeStream` / `NewStreamEncoder` / `EncodeError`。

加一个协议意味着：一个包、在 `init()` 里 `protocol.Register`、在
`internal/api/server.go` 里加一个空白 import、在 router 里加端点。没有别的了。

**永远不要悄悄丢字段。** 协议之间并不等价。发生了什么必须通过
`canonical.Diagnostics` 记下来：

- `FidelityExact` —— 有直接、完全对应的对方字段
- `FidelitySemantic` —— 表达方式不同，意思一样
- `FidelityLossy` —— 带过去了，但有损失
- `FidelityUnsupported` —— 目标协议表达不了

这些备注会出现在请求日志里。丢了字段却没有备注，就是 bug。

`internal/protocol/matrix_test.go` 会遍历全部 5×5 协议配对，强制执行上面这条规则：
一个字段要么在往返中存活，**要么**产生一条保真度备注。任何 codec 改动都必须让它保持
绿色。

**不认识的字段是带过去，不是丢掉。** codec 解码进结构体，`encoding/json` 会扔掉结构体
没有命名的一切 —— 那就是悄悄丢弃，按上面的规则就是 bug。每个 codec 解码时调用
`protocol.Capture`、编码时调用 `protocol.Merge`（`internal/protocol/passthrough.go`），
所以 provider 自己的参数 —— OpenRouter 的 `provider`、vLLM 的 `guided_json`、DeepSeek
的 `prefix` —— 都能活下来。

**一个没人读的结构体成员，比没有这个成员更糟。** 给字段起了名字就把它排除在 `Capture`
之外，于是一个声明了却从不使用的字段会被删掉：没有 extension 可以回放它，也没有备注记录
它 —— 正是上面禁止的那种悄悄丢弃，只不过藏在一个看起来很正经的类型后面。只有两个选择：
把它读进 canonical，或者干脆不要放进结构体，让 passthrough 带着它走。

**passthrough 只能到顶层，所以嵌套的厂商字段必须建模。** `Capture` 只遍历 `Top`/`Nested`
里点名的字段；任何在已知成员内部的东西都会从 canonical 重新编码出来，canonical 没有存的
就没了 —— 没有 extension，也没有备注。Anthropic 的 `cache_control` 就是这么消失的：它在
`messages[].content[]` 里面往下三层，于是调用方正在付费的 prompt 缓存被悄悄关掉，而请求
照样成功。`canonical.CacheHint` 存在的理由正是这个。`TestSameProtocolRoundTrip` 是守门
的：它用*同一个*协议解码再重新编码一个内容丰富的请求，要求每一处差异都是列出来并给出理由
的规范化。进出是同一个协议，意味着没有任何"协议不匹配"可以给损失当借口，所以它是对
canonical 模型最锋利的测试。

**在别处有精确对应的字段属于 canonical，不属于 passthrough。** extension 只在同协议路由
上回放，在另外四个协议上都会被报成不支持，所以把一个本可以转换的字段留给 `Capture`，就是
把一次干净的转换变成一份假的损失报告 —— Gemini 的 `createTime` 就是 OpenAI 的 `created`，
以前却被当成"丢掉的 Gemini 专有字段"报出来。

**协议之间对同一件事计数方式不同的地方，canonical 选定一种含义并说清楚。**
`canonical.Usage.InputTokens` 是整个 prompt，包含缓存的部分；`CachedInputTokens` 和
`CacheWriteTokens` 是它的组成部分。OpenAI、Responses 和 Gemini 本来就这么算。Anthropic
不是 —— 它的 `input_tokens` 把两部分缓存都排除在外 —— 所以它的 codec 解码时求和、编码时
拆分。不定义清楚的话，同一个字段传过来会有两种含义，于是一个 OpenAI 客户端会被告知它的
缓存 token 比整个 prompt 还多。`TestAnthropicCacheCountsAreConvertedNotCopied` 钉住了
这一点。

**由 provider 执行的工具用同样的方式携带。** Gemini 的 `googleSearch`、Anthropic 的
`web_search`、OpenAI 的 `file_search` 都在 provider 内部执行，所以没有什么可中继的，也
不存在诚实的跨协议映射。它们进 `canonical.Request.NativeTools`，再通过
`protocol.MergeNativeTools` 回来。永远不要在它们之间发明一种翻译。要从原始请求里读它们，
而不是照着一份固定的名字清单，这样厂商明年发布的新工具才能活下来而不是凭空消失。

这条规则写在 `Merge` 里，所以没有哪个 codec 能搞错：extension **只在源协议和目标协议相同
时**回放，否则一律报成 `FidelityUnsupported`。永远不要把它跨方言转发；一个 Polyglot 没法
翻译的字段会变成上游的 400。extension 永远不会覆盖编码器自己产生的成员 —— 路由后的模型名
胜过 body 里的任何东西。这不是一个 passthrough 模式，也绝不能长成那样：穿过 canonical 的
代码路径依然只有一条。

**回放令牌不是内容，不能当成内容来过滤。** 一个只携带签名的流事件没有文本，而一句
`if ev.Text == ""` 的判断会悄悄把它扔掉 —— 于是客户端从来没收到这个令牌，它自己的历史回来
时就是未签名的，接着 Polyglot 又把这段推理报成一次转换损失，而那损失是它自己上一轮造成的。
要在判空之前先检查令牌，像 `interactions/stream.go` 那样。

**回放令牌可能挂在并不属于它所描述的那个 part 上。** Gemini 给思考块收尾时，把
`thoughtSignature` 放在*后面*那个 part 上 —— 对于一个普通回答来说那就是文本 part，而它在
canonical 里原本没有位置，因为推理和工具调用各有各的字段。`ContentPart.Signature` 和
`Event.Signature` 就是那个位置。加新协议时，要检查令牌可能落在的每一种 part，而不只是那个
显而易见的。

**Gemini 签的是一个思考块，不是一个 part。** 思考是一串 thought part 传过来的，签名在自己
单独的一个 part 上给这一串收尾。逐个 part 判断会丢掉一个本来完全可回放的块的文本，还会按
片段数量报出多次损失；`signedThoughtRuns` 把这一整串当作一个单位。
`TestReplayingAStreamedThoughtLosesNothing` 走完整个回路 —— 流出去、客户端回放、请求再进来
—— 因为这个 bug 的任何一半从单个方向都看不出来。

**绑定 provider 的回放令牌必须能穿过客户端往返一圈。** Gemini 3 会给每个 function call
签名，并且在下一个请求里某一步的第一个调用没带签名时直接拒绝，所以
`canonical.ToolCall.Signature` 会用 Google 定义的 `extra_content.google.thought_signature`
信封（`internal/protocol/extension.go`）传给客户端再传回来 —— 沿用这个写法，不要自己发明一
个。它绝不会发给没有定义过它的上游，那种情况记成 lossy。完全没有签名的历史会拿到 Google 文
档里那个占位符，外加一条备注，因为未签名的调用是一个硬性的 400。

## Provider 与模型规则

**发现只是提议，决定权在运维者手里。** 列一个上游只是展示有什么可选，什么都不写入。只有
运维者在选择器里勾上的模型才会被注册。任何没人选过的东西都不能进注册表 —— 这就是本节存在
要保护的规则。

- **发现是一种能力，不是要求。** driver 通过实现 `provider.ModelDiscoverer` 来选择支持它。
  列举只在被请求时发生：`POST /api/providers/discover`，来自 provider 对话框里的选择器。
  它在 provider 还不存在时也能用，用的是表单里填的凭据。
- **列举失败绝不能让保存失败。** 把它当成信息报出来（`ok:false` 加上 `supported`），不要
  当成创建错误。一个上游没法列模型的 provider 是正常的；那就手动把模型打进去。
- **一个没有模型的 provider 是合法配置**，不是没做完的半成品。模型以后再加。
- **手填的模型是一等公民。** 手打出来的模型，调用方式和从列表里挑出来的完全一样。
- **归属关系属于 provider。** 一个 provider 暴露哪些模型，是在那个 provider 上增删的。
  Models 页回答的是另一个问题 —— 这个网关对外提供什么 —— 所以它没有新增和删除。
- **别名是可选的。** 它存在的意义是给一个短的、稳定的名字，运维者可以随时改它指向哪。
  永远不要把它变成必填项。

`internal/router` 里的解析顺序 —— 最具体的优先：

1. `provider::model` —— 直接点名了 provider（用 `::` 而不是 `/`，因为模型 id 里经常有斜杠）
2. 一个别名
3. 注册表里一个真实的上游模型 id

三条都匹配不上的名字就是错误。不会因为"上游说不定认识"就把它转发出去：打错的名字必须以
Polyglot 说自己不认识这个模型的形式返回，而不是变成某个厂商的 404。

**歧义必须是确定性的。** 当多个 provider 提供同一个模型 id 时，按 provider 的 `priority`
（大的在前）再按 provider `id` 排序 —— 一个全序、稳定的顺序。在同一个优先级内，
`router.PreferProtocol` 会把说客户端协议的 provider 排到需要转换的那个前面，因为只有同协议
路由才携带 extension 和原生工具。它绝不能跨越优先级边界，也绝不能丢掉候选：优先级是运维者
明确表达的意图，而每个协议都能转换成其他任何一个，所以协议不匹配只是一个偏好，不是取消资格
的理由。
永远不要随机挑，也不要为了解决它去造一个负载均衡器。

**重新读取一次列表永远不删除。** 某次列表里没出现的模型保留它的行和更旧的 `last_seen_at`。
永远不要覆盖运维者的 `enabled` 开关或者他们设置的显示名。见
`TestSyncDoesNotDeleteOrOverrideOperatorChoices`。

## 计价规则

Polyglot 给它记录的请求计价，好让运维者看到钱花在哪了。这是**成本可见性，不是计费**，这个
区分就是本节存在的理由：没有余额、没有充值、不从任何东西里扣减、没有账单。非目标清单依然
有效。

**只有一个例外，也只有这一个：单个 key 的预算可以拒绝请求。** 一个交给别人用的 key 需要一个
花费上限，而"盯着仪表盘看"不算上限。`api_keys.budget_usd` 是一个以美元为单位的上限，窗口由
运维者选 —— 一个由他们手动重置的总额，或者按 UTC 的每天、每周、每月。null 表示没有上限，这
也是这个功能出现之前每个 key 的状态，现在也仍然是绝大多数 key 的状态。它在
`internal/auth/limits.go` 里和 RPM、TPD 限制并排执行，不在任何新地方。

**预算是近似的，界面直说这一点，而不是假装不是。** 请求是在结束之后才计价的，所以越线的那一
次已经付掉了；而没有价格的模型不会给总额加任何东西，因为第 21 条在这里同样成立 —— 未知的成本
不是零。所以预算只是粗略地封顶，对没人能计价的流量则完全不封顶。把这件事写在界面上；不要靠
"把缺失的价格当成免费"来"修"它。

**其他任何跟钱有关的东西都不能拒绝任何请求。** provider 的花费不行，全局总额不行，月度账单也
不行。一个上限，在一个 key 上，在一个地方检查。

解析顺序，最具体的优先：

1. 运维者自己给这个模型定的价格
2. 来自 models.dev 目录的官方厂商价格
3. 没有 —— 成本**未知**

**未知永远不是零。** 零是在说这次请求免费，而那是没人做过的断言。`request_logs.cost_usd`
为 null，UI 显示一个短横线，任何总额都要报出有多少行不在里面。明确写下的 0 是另一回事，要保
留：运维者可以声明某个模型是免费的。

**只摄入第一方价格。** models.dev 收录了约 190 个 provider，大部分是报自己加价的转售商 ——
`claude-sonnet-4-5` 在十家下面出现，有三种价格。`internal/pricing/catalog.go` 里的
`firstParty` 是手工维护的厂商列表，排过序所以平局也能分出先后。永远不要拿一个转售商的数字去
给另一个转售商的模型定价；更便宜的线路是 override 该干的事。

**override 是四个可空的数字，绝不是目录行的一份拷贝。** 留空的字段跟随目录，所以改正其中一个
价格之后，其余几个仍然跟着官方降价走。四个全部清空，模型就回到目录当下的价格。

**刷新目录永远不碰运维者定的价格** —— 和模型发现对注册表遵守的是同一条规则。目录放在自己的表
里；override 放在模型行上。

**成本在请求结束时快照到日志行上**，所以改价格不会重写历史，后来才加的价格也不会回填那些当时
没有价格的行。

**缓存部分是 prompt 的组成部分，不是额外加上去的。** 公式是
`UncachedInputTokens x input + cached x cache_read + written x cache_write +
output x output`。缺失的缓存价格回退到 input 价格，并记录 `cache_price_assumed`。推理 token
从不额外累加 —— 厂商之间对它是否已经算在 output 里并没有共识。

**长上下文档位来自目录，永远不来自 override。** 好几家厂商在 prompt 超过某个长度之后加价 ——
`Rates.Tier` 携带那一档，阈值是拿整个 prompt（含缓存部分）去比的。以上下文长度以外的东西作为
键的档位是忽略，而不是去猜。被 override 的模型一律按平价计算：运维者报了一组数字，套用一个他
们从没提过的倍数等于替他们说了一个价。按更高档计价的请求会记录 `long_context_price`。

**计价永远不在请求路径上跑。** resolver 持有一份内存快照，在 usage logger 的 flush goroutine
上读取，价格或目录变化时重新加载。计价失败最多损失一个数字，绝不损失一个请求。

**目录快照是嵌入的，只在需要时刷新。** `make catalog` 重新生成
`internal/pricing/snapshot.json`；运行时的抓取是运维者按下刷新时对一个公开文件发起的 GET，
什么都不上报。刷新失败是信息，不是错误 —— 已加载的目录保持不变。这不是 phone home，也绝不能
变成那个东西。

## 流式规则

流式是核心，不是事后补的补丁。永远不要把某个厂商的 SSE 直接穿过系统透传出去。

所有的流都通过 `canonical.Event` 转换：`message.start`、`text.delta`、`reasoning.delta`、
`tool_call.start`、`tool_call.arguments.delta`、`tool_call.end`、`usage`、`message.end`、
`error`。

**工具调用的参数是分片到达的。永远不要去解析一个分片。** 累积原始字节，只在调用结束之后才把
它当成 JSON（`canonical.Accumulator` 就是干这个的）。Gemini 需要一个完整的 `functionCall`
对象，所以它的流编码器会做缓冲 —— 那是目标协议的性质，不是一条可以抄到别处的捷径。

另外必须做到：

- 传递 `context.Context`；客户端断开必须把上游一起拆掉
- 永远关闭 `resp.Body`
- 复用共享的 `http.Transport`；绝不为每个请求新建一个
- 每个 SSE 帧之后都要 flush
- 在日志里区分客户端断开和上游失败（状态是 `cancelled`，不是 `error`）

**Gemini Interactions 没有 Go SDK。** `google.golang.org/genai` 在任何已发布版本里都没有
Interactions 客户端，所以这个协议拿不到另外四个协议都有的官方 SDK 兼容性测试。不要拿一个
打扮成 SDK 测试的直接 codec 调用来糊弄过去 —— 第 19 条就是为这个存在的。它的线上类型来自官方
TypeScript provider 用来校验的那份 schema 加上录下来的真实流量；Google 的文字文档和录制结果
冲突的地方，以录制结果为准。等哪天 Go SDK 支持了 Interactions，再把缺的那层补上。

## 遥测规则

**绝不 phone home。永远不。** Polyglot 没有遥测服务器，也绝不能长出一个。不要加使用分析、匿名
统计、安装量或版本计数、崩溃上报，或者任何会往本项目作者那里发东西的代码路径。唯一的*遥测*
出站是运维者自己配置的 OTLP 端点，发往运维者自己运行的 collector。（文档里写明的目录刷新和
更新检查不是遥测，但每加一个新的出站请求都要过一遍本条规则。）

**遥测永远没有请求重要。** 每一条记录路径都是非阻塞、尽力而为的。挂掉的 collector、被打满的
注册表、`internal/telemetry` 里的一个 panic，最多损失一个计数器，绝不损失一个请求。请求路径
上没有任何东西等待 exporter。

**隐私是结构上的，不是靠过滤。** prompt、补全、工具参数、请求头、凭据、query string、请求体和
上游错误文本从来就不会被传进 `internal/telemetry`，所以那里根本没有东西可泄漏。只有当一个新属
性是协议、provider、模型、是否流式、状态、错误类别或者一个计数时，才可以加。

**指标标签来自一个有界集合。** provider 和模型可以当标签，因为它们是运维者配置出来的。客户端
自己编的模型名是 `unrouted`。请求 id、trace id、API key、IP、URL 和错误消息永远不做标签 ——
它们属于 span 或者一行日志，那只花一条记录，而不是一整条时间序列。注册表会给每个指标的不同序列
数量封顶，多出来的折叠进一个溢出序列；不要把这个上限拿掉。

**一个生命周期对象，而不是散落各处的计数器。** `telemetry.Request` 把一个请求测量一次，
Prometheus 指标和请求日志行都从它填充。业务代码调用 `StartRequest`、`StartAttempt`、
`ContentToken`、`Usage`、`Finish` —— 绝不直接去碰 Prometheus 计数器或者 span exporter。

**不要有任何按 token 计的东西。** 不要每个 delta 一个 span，不要每个 chunk 一行日志，也不要为
了数数把流式响应缓冲起来。`ContentToken` 就是一次比较加一次读时钟，这是流式热路径的上限。

**没实现的就说没实现。** OTLP over gRPC 和 metrics-over-OTLP 在这里不存在。不要去文档化一个还
没写的 exporter。

## 数据库规则

- **每一次 schema 变更都需要一个 migration**，放在 `migrations/`，命名为
  `NNNN_description.sql`。它们在启动时按文件名顺序执行。
- **永远不要改已经执行过的 migration。** 加一个新的。
- **老数据库必须能干净升级。** 在声称一个 migration 可用之前，拿上一个版本创建的数据库测一遍。
- 所有 SQL 都在 `internal/store` 里。业务代码调用带类型的方法。
- **JSON 响应里永远不要返回 nil 切片** —— 它会被序列化成 `null`，把有类型的客户端弄坏。初始化
  成 `[]`。见 `internal/api/nonnull_test.go`。
- 永远不要按流式 chunk 往 SQLite 里写。一个完成的请求写一行日志，通过 `internal/usage` 缓冲。

## 前端规则

- `strict: true` 是开着的并且要一直开着，`noUnusedLocals` 和 `noUnusedParameters` 也一样。
- ESLint 9 flat config（`web/eslint.config.js`），带类型感知的 `typescript-eslint`、React、
  React Hooks 和 React Refresh。只有一个 linter —— 不要加 Biome 或者第二个格式化工具。
- **不许用 `any`、`@ts-ignore`、`@ts-expect-error` 或 `eslint-disable` 来把问题按下去。**
  去修根因。不可信的外部数据用 `unknown` 然后收窄（`pages/providers.tsx` 里的 `parseHeaders`
  就是范例）。
- i18n：`web/src/lib/i18n/en.ts` 是唯一真源；它的 key 定义了 `TranslationKey`，所以 `zh.ts`
  里缺一个 key 会是编译错误。**每一个用户可见的字符串都要走 `t()`。** 占位符用 `{name}`。
- 不要重型状态库。`lib/hooks.ts` 里的 `useAsync` 就是数据层。
- 界面保持克制：它应该看起来像基础设施，而不是一个密密麻麻的后台管理系统。一个选得好的数字胜
  过一张图表。

## 安全规则

- provider 凭据在静态存储时加密（AES-256-GCM，密钥在 `$DATA_DIR/secret.key`）。
  `store.Provider.APIKey` 标了 `json:"-"`，绝不能到达浏览器 —— 连编辑它的那个表单也不行。
- Polyglot 自己的 API key 用 SHA-256 哈希做认证，同时也会被加密保存（AES-256-GCM，和 provider
  凭据用同一个 `$DATA_DIR/secret.key`），这样运维者可以把自己本来就持有的 key 读回来。这个网关
  只有一个人在运行；因为弄丢了一串字符就逼他删掉 key 再把限速、预算和模型清单重建一遍，比哈希
  换来的那点好处更亏。哈希保留，并且依然是请求路径查的东西 —— 认证本身没有任何变化。读回一个
  key 是管理员会话的 POST（`POST /api/keys/{id}/secret`），绝不是 GET，明文只进入那一个响应体，
  不去任何别的地方 —— WebUI 把它复制到剪贴板而不是显示在屏幕上，所以它不会留在截图里。migration
  0017 之前创建的 key 没有密文，会报 `revealable: false`；那是关于过去的事实，不是一个需要修复
  的状态。
- API key 的名字是唯一的（migration 0018）。名字是列表里和 `request_logs.api_key_name` 里区分两
  个 key 的依据，所以重名会让两个都读不懂。重名返回 409；创建时没填名字的 key 会被自动挑一个空
  闲的默认名，因为拒绝一个运维者故意留空的字段是更糟的答案。
- **每一条错误路径在到达客户端或日志之前都要剥掉上游凭据**（`redact` / `redactSecret`）。这件事
  有测试。
- 请求日志**默认只记录元数据**。完整正文记录是一个单独的选择加入功能
  （Settings → Content logs，默认关闭），保留期只有 3/7/30 天，外部访问走只读的
  log key（见 `docs/log-api.md`）。传输凭据 —— Authorization 头、cookie、
  provider 专用的 secret 头、URL 里的凭据参数 —— 必须被排除在捕获之外，这个边界
  不能放松。除此之外的任何地方（遥测、错误路径、别的表）都不存 prompt 或补全文本。
- 元数据日志永远不记录请求头。内容记录捕获的 stage 头必须排除传输凭据（上一条）；
  遥测层从头到尾就收不到任何头。
- 管理员会话：HttpOnly cookie 加上每个状态变更请求的 double-submit CSRF token。
- 保留输入大小限制、上游响应上限和超时。
- 校验 provider 的 base URL（scheme、不能内嵌凭据）。永远不要带着鉴权头跟随一个跨主机的重定向。
- `TRUST_PROXY_HEADERS` **默认是开的**：`X-Forwarded-For` 决定 `request_logs.client_ip` 里的地
  址。Polyglot 通常跑在反向代理后面，没有这个的话每一行记的都是代理自己的地址，那回答不了这份
  日志存在的任何一个问题。代价是明说而不是藏着：如果部署的端口不经过那个代理也能访问到，调用方
  就可以自己设这个头，决定日志里怎么写他。`TRUST_PROXY_HEADERS=false` 恢复使用对端地址，正是那
  种部署该用的设置。不要再加第二个地方去重新读转发头 —— `middleware.RealIP` 把地址解析一次，
  日志、限流器和 key 来源页读的都是这一个结果。

## Team 规则

Team 是一个**可选的隔离层**：几个人共用同一个网关，而各自的 provider、凭据、模型、key、账本和
日志互不可见。它是非目标清单上被切出来的**第二个也是最后一个**例外，切法和单 key 预算一样窄。

**它存在的理由只有资源归属** —— 一行数据属于谁，因此谁看得见它、谁为它付钱。除此之外它什么都不
回答。它不是组织架构、不是用户系统、不是 RBAC，也不允许从它身上长出计费、套餐、席位或 SSO；那
些还在非目标清单上，而且正是这一节把它们挡在外面。

**永远恰好有一个团队。** 默认部署里它是一个不出现在界面上的默认团队：没有界面、没有概念、没有
行为差异，运维者从头到尾不会学到"团队"这个词。它不是一个开关 —— 开关意味着两套代码路径，而两套
里总有一套没被测过。

**所以 scope 不是"开了团队才有"，是从来就有。** 每一行可拥有的数据都带一个非空的 `team_id`，每
一条碰这些数据的查询都带团队谓词，只是团队只有一个的时候看不见。忘了带 scope 必须是编译错误或者
数据库拒绝，不能只是一条 review 意见。跨团队访问返回 `store.ErrNotFound`，永远不返回权限错误 ——
403 等于确认了资源存在。

## 范围 / 非目标

**不要加基础设施：** Redis、PostgreSQL、MySQL、Kafka、RabbitMQ、独立的 worker、独立的前端服务器、
Nginx、微服务、调度器，都不要。生产环境就是一个容器、一个进程、一个 SQLite 文件。

**不要实现：** 支付、充值、兑换码、推荐返利、用户套餐、计费、商店、工单、公告、OAuth/SSO、
RBAC、图像/视频/音乐生成、实时语音、WebRTC、向量数据库、RAG、agent，或者 MCP。

计价规则里那个单 key 花费预算是这份清单上被切出来的第一个例外，而且它被切得很窄：一个 key 上的
一个上限，和这个 key 的其他限制放在一起执行。它不是一个可以充值的余额，也不允许从它身上长出别的
东西。Team 是第二个，也是最后一个 —— 边界写在上面那一节里。

**计划中但尚未实现 —— 直说，不要假装做了：** 音频输入、embeddings 和 token 计数。这些是推迟，不
是拒绝；它们不属于上面的非目标清单。

音频要特别说：它必须保持在*被报告*的状态，绝不能换个形状转发出去。`audio/*` 和 `video/*` 会被
`protocol.ClassifyMedia` 拒绝，因为没有这一条它们会掉进 "file" 分支，然后作为一份上游会拒绝的文
档发过去 —— 那就把"还没实现"变成了一次看起来毫不相干的失败。
`TestAudioIsReportedNotSmuggledThroughAsADocument` 钉住了这一点；等音频实现了，是重写那个测试，
而不是删掉它。

**多模态：** 图片和 PDF 在五个协议之间都能转换。内联 base64 是所有协议都能表达的形状，绝不能有
损。远程 URL 只转发给自己会去抓取的协议；Gemini 不会，所以那个配对会被报告出来，除非开了
`FETCH_REMOTE_MEDIA`。`file_id` 是绑定 provider 的，遵循回放令牌的规则。`internal/media` 是
Polyglot 唯一会去拨一个由*客户端*选择的地址的地方 —— 私有网段在那里无条件拒绝，不受
`BLOCK_PRIVATE_UPSTREAM` 控制，而且这一点不能放松。

## 代码风格与命名约定

Go：跑 `gofmt`；包名短且全小写，导出标识符用 `PascalCase`，错误要带上下文
（`fmt.Errorf("decode request: %w", err)`）。

TypeScript：两空格缩进，React 组件 `PascalCase`，函数 `camelCase`，页面文件名小写。每一个用户可
见的字符串都走 `t()`，并且 `en.ts` 和 `zh.ts` 一起更新。

## 开发流程

- **改之前先搞懂现在的实现。** 不要在一个已有系统旁边再建一个平行的。
- **小步走。** 每一次改动之后，仓库都必须能编译、能运行、能测试。
- **优先增量修改**而不是重写。永远不要重新 init 仓库、换框架、重写 canonical 模型，或者为了满足
  一个需求把所有 codec 重做一遍。
- **不要为还没到来的未来做抽象。** 等第二个实现出现了再加接口，不要提前加。
- 永远不要用桩端点或者一堆 TODO 来假装有进展。如果有东西做不完，就说清楚是哪一部分、为什么。
- Go：写地道的 Go，错误要包上下文，只有一个实现就不要接口，不要为写模板而写模板。

本地开发，两个终端：

```bash
make web-dev    # Vite 跑在 :5173
make dev        # Go API 跑在 :3000，把 UI 代理到 Vite
```

Make 目标：

- `make web-deps` —— 安装钉住版本的 pnpm 依赖。
- `make build` —— 构建 WebUI 和 `bin/polyglot` 静态二进制。
- `make test` —— 根目录的 Go 测试套件。
- `make test-race` —— 同一套测试，带竞态检测器。
- `make compatibility-test` —— 用官方厂商 SDK 驱动构建好的服务器。
- `make lint` —— `gofmt`、`go vet`、tsc 和 eslint。
- `make catalog` —— 重新生成嵌入的计价快照。
- `make check` —— 上面所有把关一次改动的东西。提交前跑它。

## 测试规则

三层，各自分开。谁也不能替代谁。

| 层 | 在哪 | 证明什么 |
|---|---|---|
| Codec 测试 | `internal/protocol/*/` | 协议 JSON ↔ canonical，外加 5×5 矩阵 |
| 集成 / 线上行为测试 | `internal/api/` | HTTP、SSE、router、provider、gateway |
| 官方 SDK 兼容性 | `tests/compatibility/` | 真实的厂商 SDK 能用 Polyglot |

Go 测试命名为 `TestBehavior`，作为 `*_test.go` 放在被测代码旁边。codec 用例放
`internal/protocol/<name>/`，线上行为放 `internal/api/`，真实 HTTP 的 SDK 覆盖放
`tests/compatibility/`。

### 运行时永远不用厂商 SDK；测试永远用

**Polyglot 自己实现每一个协议** —— `net/http`、`encoding/json`，还有自己的 SSE 解析器和写入器。
永远不要把 OpenAI、Anthropic 或 Google 的 SDK import 进运行时代码去做转换或者调用上游。这个项目
的全部价值就在于自己掌握线上格式。

**但兼容性必须用真实的 SDK 来证明。** codec 单元测试看不到写错的请求头、解析错的 URL、格式错误的
SSE 帧、缺失的终止符，或者 SDK 拒绝解析的错误体。官方客户端是 Polyglot 的协议兼容性探针，不只是
一个测试工具。

所以 `tests/compatibility/` 是**它自己的 Go module**。这些 SDK 只是测试依赖：它们不在根
`go.mod` 里，永远不会被链接进二进制，根目录的 `go build ./...` 也永远不会去解析它们。

兼容性测试必须走一条真实的 HTTP 路径：

```
官方 SDK -> HTTP -> Polyglot（真正构建出来的二进制） -> HTTP -> mock 上游
```

测试框架会构建 `cmd/polyglot`、作为一个进程运行它、通过管理 API 走完真实的首次启动流程，然后把
SDK 指向它。**永远不要直接调一个 codec 函数然后把它标成 SDK 兼容性测试** —— 那对序列化、请求头、
状态码、SSE 分帧和流终止什么都证明不了，而那些恰恰是全部意义所在。上游是本地 mock，所以整套测试
永远不需要付费的 API key。

只覆盖 Polyglot 确实声称支持的东西。不要为了有东西可测就去实现一个功能。

SDK 版本钉在 `tests/compatibility/go.mod` 里。升级其中某一个时：跑完整套测试，如果挂了，就去搞清
楚是厂商的协议变了还是 SDK 的行为变了，然后修 Polyglot。**永远不要为了让测试变绿而把版本钉回旧
的** —— 一个调不通 Polyglot 的新 SDK 是一个真实的兼容性信号。

## 必须跑的检查

在声称任何改动完成之前跑这个：

```bash
make check
```

它按顺序运行：

```bash
go test ./...                    # Go 测试：codec、矩阵、集成/线上行为
cd tests/compatibility && go test ./...   # 官方 OpenAI/Anthropic/Google SDK
pnpm --dir web run typecheck     # tsc --noEmit，strict 模式
pnpm --dir web run lint          # eslint .
gofmt -l .                       # 有没格式化的就失败
go vet ./...                     # tests/compatibility 里面也再跑一遍
pnpm --dir web run build         # 生产构建
```

单跑兼容性套件是 `make compatibility-test`。

全部都必须通过。另外：

- 动了流式、gateway 或 usage logger 之后要跑 `go test -race ./...`
- 用 `make build` 确认 WebUI 仍然能嵌进二进制
- 加了 migration 之后：拿上一个版本的数据库启动二进制，确认它能升级并且老数据仍然能路由

## 提交与 Pull Request 规范

提交遵循 Conventional Commits，历史里就是这么写的：`feat(keys): make key names unique`、
`fix(ui): move toasts to the top right`、`chore: release v0.1.1`。破坏性变更用 `!` 标记
（`feat(logs)!: trust proxy headers by default`）。标题用祈使句，类型之后小写；每个提交只做一件
事。

Pull request 要说明行为变化和风险、列出跑过的命令、关联相关 issue，WebUI 的改动要附截图。

永远不要提交 API key、`data/`、生成出来的 `web/dist/`，或者本地二进制。

## 绝对不能做的事

1. 不走 Canonical，直接写 A → B 的协议转换。
2. 按厂商而不是按协议建 codec。
3. 转换时丢了字段却没有 `Diagnostics` 备注。
4. 把工具调用参数的一个片段当成 JSON 解析。
5. 把模型映射或者别名做成必需的配置步骤。
6. 重新读取上游列表时，删掉已注册的模型，或者覆盖运维者的 `enabled` 开关和显示名。
7. 把运维者没有勾选的模型放进注册表。
8. 模型 id 有歧义时随机挑一个 provider。
9. 在 JSON 响应里把 nil 切片返回成 `null`。
10. 改已经执行过的 migration，或者破坏从旧数据库的升级。
11. 把 provider 凭据发给浏览器，或者把它留在错误消息或日志里。
12. 把 prompt 或补全文本写进请求日志的默认路径，或者把内容记录做成默认开启、无
    保留期、或者捕获传输凭据。
13. 用 `any`、`@ts-ignore` 或 `eslint-disable` 把类型或 lint 错误按下去。
14. 加 Redis、PostgreSQL、worker、调度器，或者第二个 linter。
15. 加任何非目标清单里的功能。
16. 往本项目作者那里发遥测、使用数据或崩溃报告。
17. 把请求 id、trace id、凭据、IP、URL 或错误消息放进 Prometheus 标签，或者让遥测阻塞、拖慢、
    弄失败一个请求。
18. 把厂商 SDK import 进运行时代码，或者让它进根 `go.mod`。
19. 拿一个直接的 codec 调用冒充官方 SDK 兼容性测试。
20. 为了让兼容性套件通过而把钉住的 SDK 降级。
21. 把未知成本记成零、拿转售商的目录条目给模型定价，或者让目录刷新覆盖运维者手填的价格。
22. 没跑 `make check` 就报告工作完成。
