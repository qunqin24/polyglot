# 新增一种协议 codec

先确认它确实是一种新协议：线上格式和现有的真的不同（请求里装消息的字段不同、流是另一套事件序列），才算协议。只是换了个厂商、同样说 OpenAI 格式的，是 provider，不是协议。OpenAI 出了 Chat Completions 和 Responses 两种格式，Google 出了 generateContent 和 Interactions，所以各算两个协议，不是一个协议加模式开关。

加协议的收益是：它和已有的每个协议自动互通（codec 只做「协议 ↔ canonical」），代价是要把它的每个字段对上 canonical，对不上的要么扩 canonical，要么记保真度备注。

## 接口

`internal/protocol/protocol.go` 里的 `Codec`：

| 方法 | 做什么 |
|---|---|
| `Name()` | 返回协议名 |
| `DecodeRequest(body, d)` | 客户端请求体 → `canonical.Request` |
| `EncodeRequest(req, d)` | `canonical.Request` → 本协议请求体；此时 `req.Model` 已经是上游模型名 |
| `DecodeResponse(body, d)` | 非流式上游响应 → `canonical.Response` |
| `EncodeResponse(resp, req, d)` | `canonical.Response` → 本协议响应体 |
| `DecodeStream(ctx, r, emit)` | 读上游流，产出 `canonical.Event`；ctx 取消时要尽快返回，`emit` 返回错误表示客户端已断开 |
| `NewStreamEncoder(w, req)` | 返回 `StreamEncoder`：`Write(ev)` 写一帧并 flush，`Close()` 写终止符 |
| `EncodeError(err)` | 把 `canonical.Error` 渲染成本协议的错误形状 |

同一个 codec 既服务入站（我们做服务端）也服务出站（我们读上游的回复），所以 Decode 和 Encode 两个方向都要写全，哪怕你只打算用其中一边。

## 步骤

1. **协议名**：在 `internal/protocol/protocol.go` 加一个 `Name` 常量，并更新 `Valid()` 和 `Display()`。
2. **codec 包**：新建 `internal/protocol/<名字>/`，文件结构照现有的：`codec.go`、`stream.go`、`types.go`（线上格式的结构体）。在 `init()` 里 `protocol.Register(Codec{})`（重复注册会 panic）。
3. **解码时记录保真度**：`d = d.WithStage("decode:<名字>")`，编码时 `d.WithStage("encode:<名字>")`；对不上或有损的字段调用 `d.Note(field, fidelity, ...)`。等级和含义见 [architecture.md](architecture.md) 的「保真度与诊断」。
4. **不认识的字段**：解码请求/响应时调用 `protocol.Capture(Name, body, protocol.Top(yourStruct{}), protocol.Nested(path, ...))`，编码时调用 `protocol.Merge`（工具调用用 `MergeNativeTools`）。结构体里命名了的字段就不会被 `Capture` 收进 extension，所以不要声明一个没人读的成员。
5. **注册到服务**：`internal/api/server.go` 里加空白 import（`_ ".../internal/protocol/<名字>"`），这样才会执行 `init()`；同时在 `Handler()` 里加一组路由：`r.Use(auth.Gateway(s.store, protocol.<名字>))` + 端点，handler 里调用 `s.gw.Chat(w, r, gateway.Options{ClientProtocol: ...})`，写在 `internal/api/protocol_routes.go`。模型名或是否流式不在 body 里的协议（像 Gemini 在 URL 里）用 `Options.ModelOverride`、`ForceStream`。
6. **driver**：`internal/provider/driver.go` 里加一个实现 `Driver` 的类型（上游聊天请求的路径、鉴权头），放进 `drivers` 表。想支持模型发现就再实现 `ModelDiscoverer`；不实现也能用，运维者手动加模型。
7. **管理端的协议列表**：`GET /api/protocols` 来自 `protocol.Registered()`，不用另改。但前端有几处写死了协议：`web/src/lib/api.ts` 的 `ProtocolName`、`web/src/pages/logs.tsx` 的协议筛选下拉、`web/src/lib/provider-form.ts` 的预设（需要的话）。
8. **测试**：
   - `internal/protocol/<名字>/codec_test.go`：协议 JSON ↔ canonical 的单测。
   - `internal/protocol/matrix_test.go`：把新协议加进 `allProtocols()` 和文件头部的空白 import；它会遍历全部配对，检查「字段存活或有备注」。
   - `internal/protocol/sameprotocol_test.go`：同协议往返。
   - `internal/api/`：线上行为测试（用 harness，见 [testing.md](testing.md)），新协议和现有协议互相转换的端到端用例。
   - `tests/compatibility/`：如果该协议有官方 Go SDK，加一个 SDK 测试（SDK 在独立 module 里，见 testing.md）。没有 Go SDK 的（Gemini Interactions 就是），在测试里写明原因，不要用直接调 codec 的测试冒充。
9. **文档**：README 和 `docs/api*.md` 里的协议列表、端点表。

## 写 codec 时容易踩的坑

这些都有已经发生过的 bug 对应，细节在 [architecture.md](architecture.md)：

- 流里一个只带签名、没有文本的事件不能被 `if text == ""` 丢掉。
- 回放令牌可能挂在你想不到的 part 上（Gemini 把思考块的签名放在后面的文本 part 上）。逐个 part 判断会丢文本、重复报损失，要像 `signedThoughtRuns` 那样把一整串当一个单位。
- 缓存 token 的计数方式各协议不同，转进 canonical 时要换算成「`InputTokens` 含缓存部分」的含义。
- 工具调用参数的分片不是合法 JSON，不能解析；目标协议需要完整对象时在编码器里缓冲。
- 在别的协议里有精确对应的字段要建进 canonical，别丢给 `Capture`。
- 附件：内联 base64 必须无损；远程 URL、`file_id` 的处理见 `internal/protocol/media.go`；音频/视频没实现之前要报告，不能当文档发出去。
