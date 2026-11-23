# Polyglot 开发指南

Polyglot 是一个轻量的 LLM API 协议转换网关：客户端用任何一种支持的协议（OpenAI Chat Completions、OpenAI Responses、Anthropic、Gemini、Gemini Interactions）发请求，Polyglot 把它转成上游供应商说的协议再转发，响应原路转回来。Go 后端 + React WebUI + SQLite，WebUI 用 `go:embed` 嵌进一个二进制。`CLAUDE.md` 用 `@AGENTS.md` 引入这个文件。

## 核心概念

- **协议（protocol）**：一种线上格式，如 `openai`、`anthropic`。每种协议只写一个 codec，负责「协议 ↔ canonical」，不存在 A → B 的直接转换。
- **canonical**：协议中立的中间表示（`internal/canonical`）。所有请求、响应、流事件都先解码成它，再编码成目标协议；转换中损失的字段记成 diagnostics，写进请求日志。
- **供应商（provider）**：一个被调用的服务，如 OpenRouter、DeepSeek。它说某一种协议，用 base URL 和 API key 访问。
- **模型（model）**：属于某个 provider 的真实上游模型。
- **别名（alias）**：可选的逻辑名字，指向 provider + model。

每个请求的路径：`客户端协议 → canonical → 路由 → 上游协议 → 上游`，响应反向走一遍。

## 跑起来

```bash
make web-deps   # 第一次：安装前端依赖
make web-dev    # 终端 1：Vite，:5173
make dev        # 终端 2：Go API，:3000，把 WebUI 代理到 Vite
```

提交前跑 `make check`（Go 测试、官方 SDK 兼容性测试、gofmt、go vet、tsc、eslint、前端生产构建）。动了流式、gateway 或 usage logger，再跑 `make test-race`。

## 文档索引

开发文档（`docs/dev/`）：

| 想知道什么 | 看这里 |
|---|---|
| 项目定位、非目标、不能碰的红线 | [docs/dev/principles.md](docs/dev/principles.md) |
| 请求管线、包的职责和依赖方向、流式、保真度 | [docs/dev/architecture.md](docs/dev/architecture.md) |
| 目录里每个包、每个目录放什么 | [docs/dev/repo-layout.md](docs/dev/repo-layout.md) |
| 供应商、内置多协议供应商、路由、别名、健康与冷却 | [docs/dev/providers-and-routing.md](docs/dev/providers-and-routing.md) |
| 新增一种协议 codec | [docs/dev/adding-a-protocol.md](docs/dev/adding-a-protocol.md) |
| 环境变量、WebUI 运行参数、新增一个设置项 | [docs/dev/configuration.md](docs/dev/configuration.md) |
| SQLite、迁移、加密、团队作用域 | [docs/dev/data-and-storage.md](docs/dev/data-and-storage.md) |
| 价格目录、成本计算、单 key 预算 | [docs/dev/pricing.md](docs/dev/pricing.md) |
| WebUI：目录约定、i18n、API client、图标 | [docs/dev/frontend.md](docs/dev/frontend.md) |
| 测试分层、harness、兼容性测试 | [docs/dev/testing.md](docs/dev/testing.md) |
| 编码约定、错误处理、提交信息 | [docs/dev/conventions.md](docs/dev/conventions.md) |

面向使用者的文档：

- [README.md](README.md) / [README.zh-CN.md](README.zh-CN.md)：项目介绍、部署
- [docs/api.md](docs/api.md) / [docs/api.zh-CN.md](docs/api.zh-CN.md)：对外 API 参考
- [docs/log-api.md](docs/log-api.md)：只读日志 API
- [docs/webui-performance.md](docs/webui-performance.md)：WebUI 在高延迟线路上的加载
