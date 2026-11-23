# 测试与检查

## 命令

| 命令 | 做什么 |
|---|---|
| `make test` | `go test ./...`，根目录的 Go 测试 |
| `make test-race` | 同一套，加 `-race` |
| `make compatibility-test` | 官方 OpenAI / Anthropic / Google SDK 驱动真实构建的二进制（`cd tests/compatibility && go test -count=1 ./...`）|
| `make web-check` | 前端测试 + `tsc --noEmit` + eslint |
| `make lint` | `web-check`，再加 `gofmt -l .`（有未格式化的文件就失败）、根目录和 `tests/compatibility` 各一次 `go vet` |
| `make check` | `test` + `compatibility-test` + `lint` + 前端生产构建（`pnpm --dir web run build`）|
| `make vulncheck` | 用钉住版本的 `govulncheck` 扫描（版本和工具链在 `Makefile` 里）|

提交前、以及声称一项工作完成之前，跑 `make check`。另外：

- 动了流式、gateway、usage logger，跑 `make test-race`。
- 用 `make build` 确认 WebUI 仍然能嵌进二进制。
- 加了迁移，拿上一个版本的数据库启动二进制，确认能升级、老数据仍能路由（单元层面见 [data-and-storage.md](data-and-storage.md) 里的 `openAtMigration`）。

`compatibility-test` 用 `-count=1` 是有意的：这些测试从父 module 构建并运行二进制，测试缓存不跟踪它，缓存命中的「通过」可能掩盖被测代码的变化。

## 三层测试，互相不能替代

| 层 | 位置 | 证明什么 |
|---|---|---|
| codec 测试 | `internal/protocol/*/` 和 `internal/protocol/*_test.go` | 协议 JSON ↔ canonical，外加全协议配对矩阵 |
| 集成 / 线上行为 | `internal/api/` | HTTP、SSE、router、provider、gateway 整条管线 |
| 官方 SDK 兼容性 | `tests/compatibility/` | 真实的厂商 SDK 能把 Polyglot 当厂商 API 用 |

Go 测试命名成 `TestBehavior`（描述行为，不是函数名），作为 `*_test.go` 放在被测代码旁边。

### codec 层

- `matrix_test.go`：`TestRequestMatrix`、`TestResponseMatrix`、`TestStreamMatrix`、`TestEveryCodecEncodesErrors` 走遍全部协议配对。新增协议要加进 `allProtocols()`。它强制「字段要么在往返中存活，要么产生保真度备注」。
- `sameprotocol_test.go`：`TestSameProtocolRoundTrip` 用同一个协议解码再编码，对 canonical 模型最锋利。
- `extensions_test.go`、`parent_regression_test.go`、`review_regression_test.go`：extension、媒体、各种回归。
- 各协议子包的 `codec_test.go`，以及 `gemini/signature_test.go`（思考签名）。
- `vendor_sdk_test.go`：`TestRuntimeUsesNoVendorSDK` 检查根 `go.mod` 里没有厂商 SDK。

### 集成层：harness

`internal/api/gateway_test.go` 的 `newHarness(t, upstreamHandler, protocolName, opts...)` 起一个完整的 Polyglot 实例，指向一个假的上游，走的是真实的管线，不是它的 mock：

- 临时目录里的真实 SQLite（`store.Open`），真实的价格目录（嵌入的快照），真实的 `usage.Logger`、`telemetry`、`api.NewServer(...).Handler()`，外面套 `httptest.NewServer`。
- 默认建好：一个名为 `fake` 的 provider（协议是你传的 `protocolName`，base URL 指向假上游，API key `sk-upstream-secret-value`）、一个别名 `my-model` → 上游模型 `upstream-model-x`、一个 API key（明文在 `h.clientKey`）。
- 设置用 `settings.Defaults()` 起步，再改成适合测试的值（`MaxRequestMB=1`、`UpstreamTimeoutSecs=10`）并关掉更新检查。
- 选项：`withSettings(func(*settings.Values))` 调整运行参数；`withTelemetry(telemetry.Config)` 打开遥测（默认关，让协议转换的测试只关心转换）；`withSetup(func(t, st, defaultProviderID))` 在服务器启动前加第二个 provider、改路由结构。
- 辅助方法：`h.post(path, body, headers)` 以客户端 key 调协议端点；`h.adminSession(t)` 建管理员并登录，返回带会话 cookie 和 CSRF token 的 `adminClient`（`get`、`send`、`do`），和 WebUI 的做法一致；`h.team()` 取默认团队的 `*store.Scope`；`h.serverHealth()` 拿到 `provider.Health`，可以不真的失败就把某个 provider 放进冷却；`httptestServer(t, handler)` 再起一个额外的上游；`readAll(t, resp)`。
- 请求日志由缓冲 logger 每 2 秒 flush，所以断言日志的测试要像 `TestRequestIsLogged` 那样轮询等一下。

典型用法：

```go
h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
    // 断言上游收到了什么（路径、鉴权头、body），再写上游的响应
    io.WriteString(w, `{...}`)
}, "openai")
resp := h.post("/v1/chat/completions", `{"model":"my-model","messages":[...]}`, nil)
```

测试**不得访问外网**：上游一律是 `httptest` 服务；harness 关掉更新检查；vendor 测试用 `provider.RebaseVendor` 把 vendor 的 URL 指向本地；更新检查器的测试用假的 `RoundTripper`；不要在测试里触发 models.dev 刷新。整套测试永远不需要付费的 API key。

其他集成测试文件按主题分：`crossprotocol_test.go`（跨协议）、`responses_test.go`、`gemini_test.go`、`interactions_test.go`、`live_test.go`、`discovery_test.go`（模型发现）、`vendor_test.go`、`api_key_limits_test.go`、`pricing_test.go`、`telemetry_test.go`、`content_logs_test.go`、`nonnull_test.go`、`setup_security_test.go`、`web_performance_test.go` 等。`internal/store/` 的测试直接开临时库，`scope_test.go` 覆盖团队隔离，`migrate_test.go` 覆盖升级。

### 官方 SDK 兼容性（`tests/compatibility/`）

**运行时永远不用厂商 SDK，测试永远用。** Polyglot 自己实现每一个协议（`net/http`、`encoding/json`、自己的 SSE 解析器和写入器），运行时代码不能 import OpenAI、Anthropic、Google 的 SDK。但兼容性必须用真实的 SDK 证明：codec 单测看不到写错的请求头、解析错的 URL、格式错误的 SSE 帧、缺失的终止符，或者 SDK 拒绝解析的错误体。官方客户端是协议兼容性探针。

所以 `tests/compatibility/` 是**它自己的 Go module**（自己的 `go.mod`）：SDK 不在根 `go.mod` 里，不会被链接进二进制，根目录的 `go build ./...` 也不会去解析它们。它不 import Polyglot，路径是：

```
官方 SDK → HTTP → Polyglot（真正构建出来的二进制）→ HTTP → mock 上游
```

`harness_test.go` 的 `TestMain`：用 `go build` 构建 `cmd/polyglot`、选一个空闲端口、以临时 `DATA_DIR` 启动进程、等 `/health`、通过管理 API 走一遍真实的首次启动流程（创建管理员、登录、注册上游、创建 API key），然后把 SDK 指向它。上游是本地 mock，按路径区分协议；四个 provider（`up-openai`、`up-responses`、`up-anthropic`、`up-gemini`）指向同一个 mock，测试用 `provider::model` 选择上游协议，从而覆盖同协议和跨协议。

- **不要直接调一个 codec 函数然后把它标成 SDK 兼容性测试**：那对序列化、请求头、状态码、SSE 分帧和流终止什么都证明不了。
- 只覆盖 Polyglot 确实声称支持的东西，不要为了有东西可测就去实现一个功能。
- Gemini Interactions 没有官方 Go SDK（`google.golang.org/genai` 在任何已发布版本里都没有 Interactions 客户端），所以这个协议没有 SDK 兼容性测试；它的线上类型来自官方 TypeScript provider 校验用的 schema 加录下来的真实流量，Google 文字文档和录制结果冲突时以录制为准。等 Go SDK 支持了再补。
- SDK 版本钉在 `tests/compatibility/go.mod`。升级时跑完整套测试，挂了就去搞清楚是厂商协议变了还是 SDK 行为变了，然后修 Polyglot。**不要为了让测试变绿把版本钉回旧的**：一个调不通 Polyglot 的新 SDK 是真实的兼容性信号。

### 前端测试

`pnpm --dir web test` 跑 `web/scripts/` 下的两个 Node 脚本（时区偏移、查询缓存），不依赖测试框架。UI 本身靠 `tsc`、eslint 和构建把关。
