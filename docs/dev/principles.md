# 项目定位、非目标与红线

改动任何东西之前先读这一页。其他开发文档讲「怎么做」，这一页讲「什么不能做」。

## 定位

Polyglot 是一个轻量的 LLM API 协议转换网关，默认一个人运行；团队是可选的隔离层。

- 它做的事：把模型名路由到正确的 provider，转换协议时不丢字段，告诉运维者一次请求花了多少钱。
- 它不是 API 转售平台，也不在网关之上再加 AI 功能。
- 判断一个改动该不该做，只问一句：这是协议转换真正需要的吗？不是就别加。
- 竞争力在转换准确度、流式稳定性、工具调用兼容、部署简单、能用的 WebUI 和低资源占用，不在功能数量。

长期方向是做 LLM API 协议之间的通用兼容层：任何一个支持的客户端协议都能到达任何一个支持的上游协议。加第六个协议只需要写一个 codec，它和其余协议的所有配对自动可用；canonical 模型要保护的就是这一点。

## 技术栈（已定，不换）

| 层 | 选型 |
|---|---|
| 后端 | Go、`net/http`、`chi`，标准库优先 |
| 前端 | React 19、Vite、TypeScript、Tailwind v4、手写的 shadcn/ui 组件 |
| 数据库 | SQLite，`modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`） |
| 打包 | WebUI 用 `go:embed` 嵌入；一个二进制、一个容器、一个进程 |
| 包管理器 | pnpm，版本由 `web/package.json` 的 `packageManager` 钉住，交给 corepack |

Go 版本以 `go.mod` 为准（`go 1.25.0`），Docker 构建用 `golang:1.26.10`（安全更新，见 `Makefile` 的 `GO_SECURITY_VERSION`）；Node 20+（Dockerfile 用 node 22）。

## 非目标

- 不加基础设施：Redis、PostgreSQL、MySQL、Kafka、RabbitMQ、独立 worker、独立前端服务器、Nginx、微服务、调度器。生产环境就是一个容器、一个进程、一个 SQLite 文件。
- 不实现：支付、充值、兑换码、推荐返利、用户套餐、计费、商店、工单、公告、OAuth/SSO、RBAC、图像/视频/音乐生成、独立的音频生成、WebRTC、向量数据库、RAG、agent、MCP。
- 实时语音只做协议转换：Gemini Live 的双向 WebSocket（会话消息、实时音频、打断、转写、工具调用）已支持 Gemini Live 客户端到 Gemini Live 上游；其他实时协议之间的转换没有实现。不能扩展成 WebRTC 或音频生成产品。普通 HTTP/SSE codec 的音频输入、embeddings、token 计数仍在计划中，不能因为 Live 支持了音频就说它们也实现了。
- 在对应协议实现之前，`audio/*`、`video/*` 附件由 `protocol.ClassifyMedia` 拒绝并报告，不能换个形状（比如当文档）转发。`TestAudioIsReportedNotSmuggledThroughAsADocument`（`internal/protocol/extensions_test.go`）钉住了这一点；等音频实现了，重写这个测试，不要删。

非目标清单上有两个刻意切出来的窄例外，边界写死，不允许从它们身上长出别的东西：

1. 单个 API key 的花费预算可以拒绝请求（见 [pricing.md](pricing.md)）。
2. 团队：只做资源归属的隔离（见 [data-and-storage.md](data-and-storage.md)）。不是组织架构、用户系统或 RBAC，也不引出计费、套餐、席位、SSO。

## 红线

下面每一条都有对应的实现或测试，改动前先看对应文档。

协议转换

1. 不走 canonical 直接写 A → B 转换；不按厂商建 codec（OpenRouter、DeepSeek、Groq、vLLM、Ollama 都是说 OpenAI 协议的 provider，共用一个 codec，不该出现 `protocol/openrouter.go`）。
2. 转换时不能丢字段而不留 `Diagnostics` 备注。`internal/protocol/matrix_test.go` 对全部协议配对强制检查。
3. 工具调用参数是分片到达的，不能把一个分片当 JSON 解析；累积原始字节，结束后再解析（`canonical.Accumulator`）。
4. 不能把某个厂商的 SSE 原样透传；所有流都经过 `canonical.Event`。
5. 运行时代码不 import 厂商 SDK，SDK 也不能进根 `go.mod`（`TestRuntimeUsesNoVendorSDK`，`internal/protocol/vendor_sdk_test.go`）。
6. 不能拿直接调用 codec 函数冒充官方 SDK 兼容性测试；不能为让测试变绿而把钉住的 SDK 版本降级。

供应商与模型

7. 别名和模型映射不能变成必填配置步骤。
8. 重新读取上游模型列表时，不能删除已注册的模型，也不能覆盖运维者的 `enabled` 开关和显示名（`TestSyncDoesNotDeleteOrOverrideOperatorChoices`，`internal/api/discovery_test.go`）。
9. 运维者没有勾选的模型不能进注册表。
10. 同一个模型 id 有多个 provider 提供时，必须按确定的顺序选，不能随机，也不要为此做负载均衡器。

数据与安全

11. 不能改已经执行过的 migration，不能破坏从旧数据库的升级。
12. JSON 响应里不能返回 nil 切片（序列化成 `null`）；初始化成 `[]`。见 `internal/api/nonnull_test.go`。
13. provider 凭据不能发给浏览器，也不能留在错误消息或日志里。
14. prompt、补全文本不能进请求日志的默认路径；完整内容记录必须是显式开启、有保留期，且不捕获传输凭据。
15. 未知成本不能记成零；不能拿转售商的目录条目给模型定价；刷新目录不能覆盖运维者手填的价格。

遥测与外联

16. 不向本项目作者那里发遥测、使用数据或崩溃报告。每加一个新的出站请求都要对照这一条；除了调用运维者配置的 provider，目前允许的出站只有：运维者配置的 OTLP 端点、运维者按下按钮触发的 models.dev 目录刷新、可在设置里关闭的更新检查，以及运维者主动开启的远程媒体下载。
17. 请求 id、trace id、凭据、IP、URL、错误消息不能做 Prometheus 标签；遥测不能阻塞、拖慢或弄失败请求。

前端与流程

18. 不用 `any`、`@ts-ignore`、`eslint-disable` 压类型或 lint 错误（见 [frontend.md](frontend.md) 里列出的现有例外）。
19. 不加第二个 linter 或格式化工具。
20. 没跑 `make check` 就不能说工作完成。

## 开发流程

- 改之前先搞懂现在的实现，不在已有系统旁边再建一个平行的。
- 小步走：每次改动之后仓库都能编译、运行、测试。
- 增量修改优先于重写；不重新 init 仓库、不换框架、不为一个需求重做所有 codec。
- 第二个实现出现之前，不加接口、不做抽象。
- 不用桩端点或一堆 TODO 假装有进展；做不完就说清楚是哪部分、为什么。
- 不建很深的目录树。一个包要有自己的活干才配存在。
