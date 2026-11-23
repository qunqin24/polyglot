# 仓库结构

```
cmd/polyglot/          入口：配置、store、HTTP server、关闭流程；命令行子命令
internal/
  canonical/           协议中立的 Request/Response/Event、错误、保真度、Live 消息
  protocol/            Codec 接口 + 注册表；passthrough、媒体、usage 的公共辅助
    openai/            OpenAI Chat Completions codec
    responses/         OpenAI Responses codec
    anthropic/         Anthropic Messages codec
    gemini/            Gemini generateContent codec
    interactions/      Gemini Interactions codec
    live/              Gemini Live 会话 codec（WebSocket，不实现 Codec 接口）
  provider/            怎么「调用」一个上游：URL、鉴权头、HTTP transport、健康/冷却、内置多协议供应商、Live 拨号
  router/              模型名 → provider + 上游模型；同优先级内偏向同协议
  gateway/             请求管线（Chat、Live）、失败重试、请求日志填充
  stream/              SSE 分帧，两个方向
  store/               所有 SQLite 访问；加密；团队作用域
  settings/            WebUI 里可编辑的运行参数：加密存储、订阅推送、旧环境变量导入
  config/              启动前需要的少量环境变量（监听地址、数据目录、开发模式）
  auth/                API key 认证、管理员会话、key 限额与预算
  pricing/             官方价格目录（models.dev 快照）、一次请求花了多少
  usage/               带缓冲的请求日志，以及在 flush 时计价
  telemetry/           请求生命周期、Prometheus 指标、OTLP tracing
  capture/             完整内容记录：把真实 payload 增量写成压缩的事件流
  media/               下载客户端用 URL 引用的附件（唯一一处拨客户端指定地址的出站请求）
  update/              对照公开的 Git tag 检查是否有新版本
  setup/               首次启动的一次性 setup token
  api/                 HTTP 表面：协议端点、管理 API、WebUI 服务
  idgen/ version/      随机 id / 版本信息（构建时用 ldflags 注入）
migrations/            *.sql，按文件名顺序执行，已嵌入
tests/compatibility/   官方 SDK 测试 —— 一个独立的 Go module
web/                   前端（见下），web/embed.go 负责把 web/dist 嵌进二进制
docs/                  文档：面向使用者的在 docs/ 下，开发文档在 docs/dev/
```

不要建很深的目录树。一个包要有自己的活干才配存在。

## cmd/polyglot

- `main.go`：`run()` 的顺序是 加载 `config` → 打开 `store`（跑迁移、加载密钥）→ 首次启动的 setup token → 加载价格目录 → `settings.Load` → 启动 `usage.Logger` → 构造 `telemetry` → `api.NewServer(...).Handler()` → `http.Server`。没有 `WriteTimeout`，因为流式响应会超过任何固定期限；每个请求的上限来自请求 context。收到 SIGINT/SIGTERM 后先 `srv.Shutdown`（15 秒），再停 usage logger 把缓冲的日志刷进库。
- `cli.go`：命令行刻意很小，没有 flag 库、没有配置文件。子命令：`reset-password`（找回管理员密码，靠文件系统访问权限，不靠邮件）、`config`（打印当前生效配置和来源）、`version`、`help`。未知参数是错误，不会静默启动服务。
- `settings.go`：`envSettings` 表是 `polyglot help` 和 `polyglot config` 共用的环境变量清单，细节见 [configuration.md](configuration.md)。

## internal/api

- `server.go`：`NewServer` 把各组件接起来（订阅设置、绑定限额与花费），`Handler()` 注册所有路由：协议端点（每组挂 `auth.Gateway`）、`/api/*` 管理 API（`auth.Admin` 会话 + CSRF）、`/health`、`/metrics`、WebUI。协议子包用空白 import 注册 codec。
- `protocol_routes.go`：协议端点的 handler，几乎都是 `s.gw.Chat(w, r, gateway.Options{...})`。
- `admin*.go`：管理 API，按资源分文件（`admin_resources.go` 是 providers、aliases、keys 等，`admin_models.go`、`admin_pricing.go`、`admin_settings.go`、`admin_logs.go`）。`content_logs.go` 是完整内容记录相关接口，`update.go` 是版本检查。
- `http.go` / `requestid.go`：JSON 读写辅助、请求 id 中间件。
- 测试在同目录，`gateway_test.go` 里有整套 harness（见 [testing.md](testing.md)）。

## internal/protocol

`protocol.go` 定义 `Name`、`Codec`、`StreamEncoder` 和注册表。其余文件是各 codec 共用的辅助：

- `passthrough.go`：`Capture` / `Merge` / `MergeNativeTools`，不认识字段的带过去与回放。
- `extension.go`：`extra_content.google.thought_signature` 信封。
- `media.go`：data URI、附件分类（`ClassifyMedia`）、哪些协议会自己抓远程 URL（`AcceptsMediaURL`）。
- `usage.go`：缓存相关的保真度备注。
- 各子包的结构都一样：`codec.go`（请求/响应 + 注册）、`stream.go`（流解码与编码）、`types.go`（线上格式的结构体）。`live/` 只有 `codec.go`。

## web/

```
web/src/
  main.tsx             入口：Provider 层层包裹 + 路由
  routes/              TanStack Router 的文件路由；routeTree.gen.ts 是生成的，不要手改
  pages/               一个页面一个文件（overview、providers、models、aliases、keys、logs、pricing、settings、auth、docs）
  components/ui/       shadcn 风格的基础组件
  components/kit/      Polyglot 自己封装的组件（Dialog/Sheet/ConfirmDialog、Input/Field、toast、Badge/Select/Table/EmptyState、LoadingButton、图表）
  components/layout/   侧边栏、顶栏、页面骨架
  components/data-table/ 数据表格的工具栏、分页、筛选
  components/provider-create/  新增供应商的对话框
  components/          其余共用组件（brand-icon、model-icon、model-picker、runtime-settings…）
  lib/                 api client、数据加载 hook、query-cache、i18n、provider 预设、工具函数
  context/ hooks/      主题、字体、布局等 Provider；通用 hook
  features/errors/     错误页
  config/ styles/ assets/
```

细节见 [frontend.md](frontend.md)。
