# 编码约定

## 语言

- 代码、标识符、注释、提交信息、PR 描述用英文。开发文档（`docs/dev/`、`AGENTS.md`）用简体中文。
- WebUI 里每个用户可见的字符串都走 `t()`，`en.ts` 和 `zh.ts` 一起更新（见 [frontend.md](frontend.md)）。

## Go

- 提交前 `gofmt`（`make lint` 会检查 `gofmt -l .` 非空即失败）和 `go vet`。包名短、全小写；导出标识符 `PascalCase`。
- 标准库优先，依赖很少（`chi`、`modernc.org/sqlite`、`coder/websocket`、`x/crypto`）。加依赖前先想想标准库能不能做。
- 写地道的 Go。只有一个实现就不要接口，等第二个实现出现再抽象。已有的少数接口（`provider.Driver`、`provider.ModelDiscoverer`、`pricing.Store`、`settings.Store`、`usage.Pricer`）要么本来就有多个实现，要么是消费方定义的，用来避免包之间循环依赖。
- 不为还没到来的未来做抽象，不为写模板而写模板。
- 并发：被 WebUI 运行时修改的值（超时、冷却时长、保留天数）用 `atomic` 持有，提供 `SetXxx`，不要在热路径上加锁等 exporter 或数据库。

## 注释

看现有代码就知道风格：**注释解释为什么，不复述代码做了什么。** 值得写的是：

- 这个约束从哪来，不这么做会怎样。例：`PreferProtocol` 的注释说明它为什么是偏好而不是过滤，以及为什么不能跨优先级；`Health` 的注释说明为什么状态不持久化。
- 一个看起来可以改、其实不能改的选择。例：`NOT NULL DEFAULT 0` 而不是 `1` 的原因写在迁移 `0020` 里；`providers.name` 仍然全局唯一的原因。
- 失败时的取舍和后果：哪种失败是可以接受的（遥测丢一个计数），哪种不行（丢一个请求）。
- 每个包以 `// Package xxx ...` 开头，说明这个包负责什么、**不**负责什么。

不写的：`// increment i`、对着函数名换个说法的注释、已经被 git 记录的修改历史。

## 错误处理

- 返回错误时带上下文并用 `%w`：`fmt.Errorf("decode request: %w", err)`、`fmt.Errorf("open sqlite %s: %w", dbPath, err)`。
- 面向 API 客户端的错误用 `canonical.Errorf(canonical.ErrXxx, ...)`，类型决定 HTTP 状态码（`canonical.StatusForType`），各 codec 的 `EncodeError` 把它渲染成自己协议的形状。网关内部的 Go error 在边界处用 `toCanonicalError` 转换。
- 管理 API 用 `writeErr(w, status, format, ...)`，响应体是 `{"error": "..."}`；`store.ErrNotFound` 经 `storeErrStatus` 映射成 404（团队隔离的约定，见 [data-and-storage.md](data-and-storage.md)）。请求体用 `decodeJSON`：有大小上限，并且拒绝未知字段，所以前端的拼写错误会暴露出来而不是被忽略。
- **凭据不能出现在错误里。** 任何要到达客户端或日志的上游错误文本，先过 `redact`（`gateway`）/`redactSecret`（`api`）。
- 失败了但不该影响主流程的事（计价、遥测、刷新目录、更新检查），只记警告、继续，不要让它变成请求失败。
- 不用桩端点或一堆 TODO 装作做完了。做不完就在代码和文档里说清楚是哪一部分、为什么。

## 「null 不等于 0」

贯穿全项目的一条原则：**不知道、没测到、没设置，和 0 是不同的答案，类型和存储都要能分清。**

- 价格：`pricing.Price` 的四个字段是 `*float64`。没人说过这个价格是 nil，不是 0；0 是运维者明确声明免费。成本未知时 `request_logs.cost_usd` 为 NULL，UI 显示短横线，总额要说明有多少行没算进去。
- 指标：`RequestLog.TTFTMS`、`GenerationMS`、`OutputTPS` 是指针，非流式响应和没产出内容的流是 null，不是 0。
- 限额和预算：`api_keys` 上的 `rpm`、`tpd`、`budget_usd` 等为 NULL 表示没有这个限制，不是限制为 0。
- canonical：`Temperature`、`TopP`、`MaxTokens` 等可选参数是指针（`canonical.Ptr`），「没传」和「传了 0」会走到上游的不同行为。
- JSON：切片永远初始化成 `[]`，不要让 nil 切片序列化成 `null`（`internal/api/nonnull_test.go`）。
- 前端：和后端的 nullable 字段保持一致的 `number | null` 类型，显示时区分「未设置」和「0」。

## 不要悄悄丢东西

- 协议转换里丢字段必须有保真度备注（[architecture.md](architecture.md)）。
- 遇到不认识的输入，倾向于报告，而不是转发或吞掉：不认识的模型名返回「不认识」，不转发给上游碰运气；不支持的媒体类型被报告，不当文档发出去。
- 去重、排序、选择必须确定：同一个请求总是落到同一个 provider，没有随机和隐含的创建顺序。

## 安全与隐私清单

改到下面这些地方时过一遍：

- provider 凭据、API key 的明文只在明确的接口里出现（管理员 POST），不出现在列表、日志、错误、遥测里。
- 默认只记元数据；内容记录是显式开启的、有保留期的、不含传输凭据的。
- 管理员会话：HttpOnly cookie + 每个状态变更请求的 CSRF token。
- 保留输入大小限制、上游响应上限和超时；校验 provider base URL；不带鉴权头跟随跨主机重定向。
- `internal/media` 是唯一拨客户端指定地址的出站请求，私有网段无条件拒绝。
- 每个新增的出站请求都要对照 [principles.md](principles.md) 的遥测红线。

## 提交信息

遵循 Conventional Commits，历史里就是这么写的（`git log --oneline` 可见）：

```
feat(keys): make key names unique
fix(ui): move toasts to the top right
fix(security): update Go toolchain to 1.26.10 (#2)
docs: consolidate development rules in AGENTS.md
chore: release v0.1.1
feat(logs)!: trust proxy headers by default
```

- 格式 `type(scope): subject`，subject 用祈使句、类型之后小写、不加句号。常用类型：`feat`、`fix`、`docs`、`chore`；常见 scope：`keys`、`ui`、`logs`、`settings`、`providers`、`teams`、`live`、`docker`、`security`。
- 破坏性变更在类型（或 scope）后加 `!`（`feat(logs)!:`、`feat!:`），并在正文里说清楚迁移办法。
- 每个提交只做一件事；改了之后仓库必须能编译、能测试。
- 早期历史（v0.1.0 前后）里有不带类型前缀的标题，如 `Add structured issue templates`，那是旧写法，新提交不要沿用。

## Pull Request

- 说明行为变化和风险，列出跑过的命令（至少 `make check`），关联相关 issue。
- WebUI 的改动附截图。
- 永远不要提交 API key、`data/`、生成出来的 `web/dist/`、本地二进制（这些都在 `.gitignore` 里）。
- CI：`.github/workflows/` 里有 Docker 发布和安全扫描（`govulncheck`）。
