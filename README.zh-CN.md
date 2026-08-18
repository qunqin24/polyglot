<div align="center">

<img src="web/public/favicon.svg" alt="" width="76" height="76">

# Polyglot

**不同大模型 API 协议之间的通用兼容层。**

*你的 AI，不该被任何一家供应商定义。*

[English](README.md)

</div>

---

Polyglot 是一个轻量的、自托管的 LLM API 协议转换网关。它坐在你的客户端和供应
商之间，把任何一种被支持的协议翻译成另一种——让你喜欢的客户端和你想用的模型不再
需要来自同一家公司。

用 OpenAI SDK 调 Gemini。用 Claude Code 调 DeepSeek。用 Gemini 客户端调
Anthropic。一个二进制、一个进程、一个 SQLite 文件。不需要 Redis、Postgres，运
行时也不需要 Node.js。

```
OpenAI ─────────────┐                          ┌───── OpenAI 兼容
OpenAI Responses ───┤                          ├───── OpenAI Responses
Anthropic ──────────┼──▶  Polyglot  ──▶ 路由 ──┼───── Anthropic
Gemini ─────────────┤                          ├───── Gemini
Gemini Interactions ┘                          └───── Gemini Interactions
      客户端                                          供应商
```

## 为什么是 Polyglot

大多数 LLM 代理把一切都包成 OpenAI 格式就算完事了。在遇到问题之前这确实管用
——直到 Anthropic 的 `cache_control` 消失了、Gemini 的 `thought_signature` 被
丢了、tool call 的参数不再流式推送而是整块到达、成本估算因为没有价格就显示"免
费"。

Polyglot 走了一条不同的路。

### 和同类工具的对比

| | **Polyglot** | **LiteLLM** | **One API / New API** |
|---|---|---|---|
| **架构** | Canonical 模型——N 个 codec，不是 N² 个转换器 | Provider 适配器，映射到 OpenAI | 渠道路由，只输出 OpenAI |
| **协议输出** | 5 种协议，每种都双向 | 只有 OpenAI | 只有 OpenAI |
| **字段保真度** | 每个丢失的字段都被记录并展示 | 尽力映射 | 尽力映射 |
| **重放凭据** | Gemini `thought_signature`、Anthropic `thinking` 签名穿透传递 | 不处理 | 不处理 |
| **供应商参数** | `guided_json`、`provider`、`prefix` 等在同协议路由上透传 | 需用 `extra_body` | 丢失 |
| **供应商内置工具** | `googleSearch`、`web_search`、`file_search` 在同协议路由上转发 | 部分支持 | 不处理 |
| **成本追踪** | 逐请求，来自一手厂商目录，绝不是计费 | 逐请求，自有目录 | 基于余额的计费系统 |
| **部署** | 单个静态二进制，内嵌 UI，SQLite | Python 进程 + 可选代理 | Go/Python + MySQL/PostgreSQL + Redis |
| **运行时依赖** | 无 | Redis 可选，PostgreSQL 可选 | MySQL 或 PostgreSQL，Redis |
| **遥测** | 零上报，只发到运营者自己的 OTLP | 默认匿名使用统计 | 无 |
| **目标用户** | 个人运营者 | 团队和企业 | 做 API 转售的团队 |
| **内置工具** | 从原始请求中读取——未来厂商新增的工具自动透传 | 固定列表 | 固定列表 |

Polyglot 不是上述工具的超集。它没有计费、没有多租户、没有用户套餐、没有 RBAC
——也永远不会有。它是一个为个人流量服务的协议转换网关，所有功能都围绕这个定位。

## 快速开始

### Docker（推荐）

直接运行 Docker Hub 上的正式版镜像：

```bash
docker volume create polyglot-data
docker run -d \
  --name polyglot \
  --restart unless-stopped \
  -p 3000:3000 \
  -v polyglot-data:/data \
  qunqin45/polyglot:latest

# 读取一次性首次安装口令
docker exec polyglot cat /data/setup.token
```

随后打开 `http://服务器IP:3000`，输入安装口令并创建管理员。初始化成功后口令会立即
失效并从数据卷删除。请在服务器防火墙中只向需要访问的网络放行 TCP 3000 端口。如果同一台服务器上已有
Nginx、Caddy 等反向代理，则应改用 `127.0.0.1:3000:3000`，只对外暴露反向代理。

使用 Docker Compose：

```bash
mkdir polyglot && cd polyglot
curl -fsSLO https://raw.githubusercontent.com/qunqin24/polyglot/main/docker-compose.yml
docker compose pull
docker compose up -d
docker compose exec polyglot cat /data/setup.token
```

容器启动后打开 `http://服务器IP:3000`。Compose 文件默认将 3000 端口发布到服务器外部；
面向公网部署时，请通过防火墙限制访问，或放在启用 TLS 的反向代理后面。

同一个镜像支持 `linux/amd64` 和 `linux/arm64`，并镜像到
`ghcr.io/qunqin24/polyglot:latest`。`latest` 永远指向最新正式版；
`preview-latest` 是独立的预览通道，不会覆盖正式版。

### 二进制

```bash
make build
DATA_DIR=./data ./bin/polyglot
```

打开 <http://localhost:3000>。使用二进制部署时，从 `data/setup.token` 读取一次性安装
口令（也可以设置 `POLYGLOT_SETUP_TOKEN`），在初始化表单中输入。随后添加供应商并创
建 API Key 即可。

### 第一个供应商

供应商对话框要求填名字、base URL 和凭据。保存时 Polyglot 列出供应商的模型并显
示选择器——勾选你要的，它们就注册了。也可以手动输入模型 id，手工添加的模型和自
动发现的完全一样。

![供应商对话框](docs/screenshots/providers.png)

### 第一条请求

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Authorization: Bearer pg_xxx" \
  -d '{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"你好"}]}'
```

模型名就是上游的真实 id——不需要映射，不需要别名。

## 支持的协议

五种协议，每种既是客户端输入也是上游输出，共 25 种转换配对：

| 协议 | 客户端端点 | 上游 |
|---|---|---|
| **OpenAI Chat Completions** | `POST /v1/chat/completions` | 任何 OpenAI 兼容 API |
| **OpenAI Responses** | `POST /v1/responses` | OpenAI Responses API |
| **Anthropic Messages** | `POST /v1/messages` | Anthropic Messages API |
| **Gemini generateContent** | `POST /v1beta/models/{model}:generateContent` | Google Gemini API |
| **Gemini Interactions** | `POST /v1beta/interactions` | Google Interactions API |

每个端点都接受 Polyglot 自己的 API Key，放在客户端惯用的请求头里
（`Authorization: Bearer`、`x-api-key`、`x-goog-api-key`）。

流式在五种协议上都能工作——SSE 帧逐事件转换，包括被切碎的工具调用参数。客户端断
开连接时上游连接立即关闭。

## 使用示例

### 把任何 SDK 指向 Polyglot

**Python — OpenAI SDK：**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:3000/v1",
    api_key="pg_xxx",
)

# 这可以打到 Gemini、Anthropic、DeepSeek——
# 取决于模型路由到哪里。你的代码不用改。
response = client.chat.completions.create(
    model="gemini-2.5-flash",
    messages=[{"role": "user", "content": "用一段话解释 Protocol Buffers"}],
)
print(response.choices[0].message.content)
```

**Python — Anthropic SDK：**

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:3000",
    api_key="pg_xxx",
)

message = client.messages.create(
    model="gpt-4.1",  # OpenAI 的模型，通过 Anthropic SDK 调用
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
)
```

**Python — Google GenAI SDK：**

```python
from google import genai

client = genai.Client(
    api_key="pg_xxx",
    http_options=genai.types.HttpOptions(api_version="v1beta", base_url="http://localhost:3000"),
)

response = client.models.generate_content(
    model="claude-sonnet-4-20250514",  # Anthropic 的模型，用 Gemini SDK 调
    contents="生命的意义是什么？",
)
```

**Node.js — OpenAI SDK：**

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://localhost:3000/v1",
  apiKey: "pg_xxx",
});

const response = await client.responses.create({
  model: "gemini-2.5-pro",
  input: "写一首关于协议转换的俳句",
});
```

### CLI 工具

**Claude Code：**

```bash
export ANTHROPIC_BASE_URL=http://localhost:3000
export ANTHROPIC_AUTH_TOKEN=pg_xxx
# Claude Code 现在通过 Polyglot 路由
```

**任何 OpenAI 兼容 CLI（aider、llm 等）：**

```bash
export OPENAI_BASE_URL=http://localhost:3000/v1
export OPENAI_API_KEY=pg_xxx
```

### 流式 + 工具调用

```bash
curl -N http://localhost:3000/v1/chat/completions \
  -H "Authorization: Bearer pg_xxx" \
  -d '{
    "model": "gemini-2.5-flash",
    "stream": true,
    "messages": [{"role": "user", "content": "东京现在天气怎么样？"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "parameters": {"type":"object","properties":{"city":{"type":"string"}}}
      }
    }]
  }'
```

工具调用参数作为片段到达，积累完整后才做 JSON 解析。Gemini 需要完整的
`functionCall` 对象，所以它的编码器会缓冲——这是目标格式的特性，对调用方透明。

### 跨协议工具调用

同一个工具定义不管客户端说什么协议、上游期望什么协议，都能正常工作。Polyglot
在 OpenAI 的 `tools[].function`、Anthropic 的 `tools[].input_schema` 和 Gemini
的 `tools[].functionDeclarations` 之间转换——定义、调用和结果始终保持配对。

### 直接指定供应商

同一个模型 id 存在于多个供应商时，用 `provider::model`：

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Authorization: Bearer pg_xxx" \
  -d '{"model":"deepseek::deepseek-chat","messages":[{"role":"user","content":"你好"}]}'
```

分隔符是 `::` 而不是 `/`，因为模型 id 里经常有斜杠。

## 架构

### Canonical 模型

让这套系统长期可维护的规则：**任何协议都不直接转换成另一种协议。**

```
             decode                          encode
OpenAI ─────────────┐                    ┌────────▶ OpenAI
OpenAI Responses ───┤                    ├────────▶ OpenAI Responses
Anthropic ──────────┼──▶  Canonical  ──▶ ┼────────▶ Anthropic
Gemini ─────────────┤                    ├────────▶ Gemini
Gemini Interactions ┘                    └────────▶ Gemini Interactions
```

每种协议只实现一件事——`协议 ↔ Canonical`——所以五种协议是五个 codec，不是二十五
个转换器。加第六种协议就是一个包，和其它所有协议的十种新配对白送。

三个概念**严格分开**：

- **协议（Protocol）**——线上格式：`openai`、`openai-responses`、`anthropic`、
  `gemini`、`gemini-interactions`
- **供应商（Provider）**——你要调的服务：OpenRouter、DeepSeek、Google、Anthropic
- **模型（Model）**——供应商下的真实模型，自动发现或手工输入

**别名（Alias）** 是可选的第四层：一个指向某个模型的短名字，以后改指向不用动
客户端。

OpenRouter、DeepSeek、SiliconFlow、Groq、vLLM、Ollama 都是说 *OpenAI 协议* 的
*供应商*。它们共用一个 codec——只有 base URL 和凭据不同。

### 模型解析

请求里的 `model` 按从具体到宽泛的顺序解析：

1. `provider::model`——直接点名了供应商
2. 别名页定义的**别名**
3. 注册表里的真实**模型 id**

不匹配就是错误。Polyglot 不会抱着"上游说不定认识"的心态转发——打错字得到的是
Polyglot 自己的报错，不是某个厂商的 404。

同一个模型 id 存在于多个供应商时，`priority` 决定顺序——全序、稳定、绝不随机。
同优先级下，说客户端同种协议的供应商优先，因为只有同协议路由才转发扩展参数和
内置工具。这个偏好不跨越优先级。

### 保真度

协议之间不等价。不能原样转换的字段会被记录：

| 级别 | 含义 |
|---|---|
| `exact` | 目标协议有直接对应物 |
| `semantic` | 表达方式不同，语义相同 |
| `lossy` | 带着信息损失传递 |
| `unsupported` | 目标协议无法表达 |

这些说明出现在请求日志和协议检查器里。**丢字段却没有说明就是 bug**——一个遍历全
部 5×5 协议配对的矩阵测试每次构建都守着这条规则。

### 供应商参数透传

供应商自有参数——OpenRouter 的 `provider`、vLLM 的 `guided_json`、DeepSeek 的
`prefix`——在解码时捕获，在编码时**仅在源协议和目标协议相同时**重放。跨协议时报
为不支持，而不是硬塞给一个不认识它的上游换回 400。

这不是 passthrough 模式。每条请求仍然经过 canonical。检查器仍然有 canonical 形
态可看，用量仍然被计量。

### 重放凭据

一些供应商会签名它们的输出，下一次请求如果签名丢失就拒绝。Gemini 3 签名每一个
function call：

```
Function call ... is missing a thought_signature.
```

Polyglot 把签名透传过去。在不同协议上，它搭载在 Google 为自己 OpenAI 兼容层定
义的 `extra_content` 信封里。没有签名的历史记录会用 Google 文档中规定的占位符
填充，让请求正常工作，同时把丢失的推理连续性记为保真度说明。

Anthropic `thinking` 签名和 OpenAI Responses 推理项遵循同样的原则。

### 多模态

图片和 PDF 在五种协议之间转换。内联 base64 永远无损。远程 URL 转发给能自己获取
的协议；Gemini 不行，所以这种配对默认报为不支持——除非开了
`FETCH_REMOTE_MEDIA=true`，此时 Polyglot 下载并内联，带 SSRF 防护（私有 IP 拒
绝、大小限制、类型校验、重定向限制）。

音频已计划但尚未实现。`audio/*` 附件直接拒绝，不会伪装成文档转发。

### 供应商内置工具

Gemini 的 `googleSearch`、Anthropic 的 `web_search`、OpenAI 的 `file_search`
跑在供应商内部。它们没有跨协议等价物，所以在同协议路由上转发，跨协议时报为不支
持。Gemini 的服务端工具从原始请求中读取，不是固定列表——Google 以后新增的工具自
动透传。

## WebUI

Polyglot 内置 Web 界面，编译进二进制——不需要单独的前端服务器、CDN 或运行时
Node.js。自动检测浏览器语言（中文和英文），也可在侧边栏切换。

### 概览

![概览](docs/screenshots/overview.png)

仪表盘：请求量、成功率、token 用量和成本汇总。显示有多少请求缺少价格未计入总
额，所以有缺口的数字不会被误读为完整的。

### 供应商

![供应商](docs/screenshots/providers.png)

添加、编辑、删除上游供应商。凭据加密存储，**永远不发给浏览器**——编辑表单也不
会。保存时自动发现模型并展示选择器。供应商没有模型是合法的配置——之后再添加。

显示当前因故障正在冷却跳过的供应商。

### 模型

![模型](docs/screenshots/models.png)

网关提供的所有模型，跨全部供应商。标记模型 id 有歧义（同 id 在多个供应商下）
并显示解析顺序。缓存命中率按 token 份额展示，不是按请求数。

### API Key

![API Key](docs/screenshots/keys.png)

创建和管理 API Key。Key 以 SHA-256 哈希存储，创建时只显示一次。每个 Key 展示使
用过它的地址，按量排序——一个长期单地址使用的 Key 突然从新地址被调用，在这里一
目了然。

可选的限制：每分钟请求数、每日 token 数、花费预算（你选的时间窗口内的美元上
限）。

### 请求日志

![请求日志](docs/screenshots/logs.png)

每条请求记录：协议对、供应商、模型、状态、耗时、token 数（输入、输出、缓存、推
理）、成本、首 token 时间、每秒 token 数、调用地址、保真度说明。按以上全部字段
加 `X-Title`、referer 和 user agent 过滤。

**永远不存储 prompt 或补全内容。**

### 协议检查器

![协议检查器](docs/screenshots/inspector.png)

并排查看一条请求的入参、canonical 形态和出参。精确展示转换做了什么、什么被保
留、什么丢失、为什么。这是排查转换问题的调试工具。

### 定价

![定价](docs/screenshots/pricing.png)

按模型定价，来自内嵌的 models.dev 目录，支持手工覆盖。显示有多少模型没有价格。
刷新会从 models.dev 拉取最新目录（GET 一个公开文件，不发送任何内容）；你的覆盖
价格单独存储，不受影响。覆盖是四个可为空的字段——空白跟随目录，所以修正一个价格
仍然跟踪其余的官方调整。

### 别名

可选的短名字，指向供应商 + 模型。改一个别名的指向，所有用它的客户端自动跟随
——不改代码，不重新部署。

### 设置

![设置](docs/screenshots/settings.png)

显示时区（按管理员存储，时间戳内部始终 UTC）、语言、修改密码。

## 配置

每个部署会变的东西都是环境变量，其余都在数据库里，通过 WebUI 管理。

| 变量 | 默认值 | 用途 |
|---|---|---|
| `PORT` | `3000` | 监听端口 |
| `LISTEN` | `127.0.0.1:3000` | 完整监听地址（覆盖 `PORT`） |
| `DATA_DIR` | `/data` | SQLite 文件、加密密钥、运行时数据 |
| `DB_PATH` | `$DATA_DIR/polyglot.db` | 只覆盖数据库位置 |
| `LOG_RETENTION_DAYS` | `30` | `0` 永久保留日志 |
| `UPSTREAM_TIMEOUT` | `10m` | 单次上游请求时间上限 |
| `PROVIDER_COOLDOWN` | `30s` | 失败供应商被跳过的时长 |
| `MAX_REQUEST_MB` | `32` | 客户端请求体限制 |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `LOG_FORMAT` | `text` | `json` 用于结构化日志 |
| `PUBLIC_URL` | — | 填 `https://` 值会打开 Secure Cookie |
| `SECURE_COOKIES` | `false` | 不设 `PUBLIC_URL` 也强制 Secure Cookie |
| `BLOCK_PRIVATE_UPSTREAM` | `false` | 拒绝私网地址的供应商 |
| `TRUST_PROXY_HEADERS` | `false` | 信任 `X-Forwarded-For` |
| `POLYGLOT_SECRET_KEY` | — | 覆盖 `$DATA_DIR/secret.key` |
| `POLYGLOT_SETUP_TOKEN` | `$DATA_DIR/setup.token` | 覆盖一次性首次安装口令 |
| `FETCH_REMOTE_MEDIA` | `false` | 为不获取 URL 的上游下载客户端链接的图片 |
| `MAX_MEDIA_MB` | `20` | 单个附件下载上限 |
| `UPDATE_CHECK_ENABLED` | `true` | 管理员打开 WebUI 时检查公开 GitHub 标签 |
| `UPDATE_REPOSITORY` | `qunqin24/polyglot` | 更新检查使用的 GitHub `owner/repository` |

### 遥测变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `TELEMETRY_ENABLED` | `true` | 指标和 tracing 总开关 |
| `METRICS_ENABLED` | `true` | 进程内收集 Prometheus 指标 |
| `METRICS_TOKEN` | — | `GET /metrics` 的 Bearer token（不设无端点） |
| `TRACING_ENABLED` | `false` | 输出 OpenTelemetry span |
| `OTLP_ENDPOINT` | — | 你的 collector，如 `http://collector:4318` |
| `OTLP_HEADERS` | — | 额外 collector 头，`k=v,k2=v2` |
| `TRACE_SAMPLE_RATIO` | `1` | 自发起 trace 的比例，`0` 到 `1` |
| `OTEL_SERVICE_NAME` | `polyglot` | 导出 span 上的 `service.name` |

### HTTPS

Polyglot 只提供明文 HTTP。把 Caddy、Cloudflare、Nginx 或 1Panel 放在前面。

### 迁移

迁移一个实例就是拷贝 `/data`。这个目录有数据库和加密密钥——没有其它有状态的东
西。

## 可观测性

> **Polyglot 不向任何人上报使用遥测。** 没有使用统计、匿名分析、崩溃上传或安装计
> 数。唯一的出站遥测发送到**你**运行的系统，由**你**配置。

管理员登录并打开 WebUI 时，服务端会通过公开 GitHub Tags API 检查兼容通道的新版本；
成功结果缓存六小时，不发送 prompt、供应商数据或实例标识。这个请求必然会向 GitHub
暴露服务器 IP 和包含 Polyglot 版本的 User-Agent。设置
`UPDATE_CHECK_ENABLED=false` 可完全关闭该请求。

### 指标

默认在进程内收集（每请求几次原子操作）。`GET /metrics` 只在设了
`METRICS_TOKEN` 时开放——端口经常暴露在公网，一个开放的 `/metrics` 会向任何人
公开你的供应商名、模型列表和流量。

```yaml
# prometheus.yml
scrape_configs:
  - job_name: polyglot
    static_configs: [{ targets: ["polyglot:3000"] }]
    authorization:
      credentials: "<METRICS_TOKEN 的值>"
```

主要指标：

| 指标 | 说明 |
|---|---|
| `polyglot_requests_total` | 按协议、上游协议、供应商、模型、流式、状态 |
| `polyglot_request_duration_seconds` | 端到端耗时直方图 |
| `polyglot_ttft_seconds` | 首 token 时间（仅流式） |
| `polyglot_output_tokens_per_second` | 在生成窗口内测量，不是整个请求 |
| `polyglot_input_tokens_total` | 上游报告的输入 token |
| `polyglot_output_tokens_total` | 输出 token |
| `polyglot_retries_total` | 首次之后的重试 |
| `polyglot_fallbacks_total` | 切换到另一个供应商的重试 |

输出速度在生成窗口（首个内容 token 到最后一个）内测量，不是 HTTP 总耗时——这样
供应商排队时间不会拉低模型速度数字。

### 成本追踪

每条请求从两个来源计价，优先级从高到低：

1. 你在定价页为该模型设的价
2. 内嵌在二进制里的 [models.dev](https://models.dev) 官方厂商价格

两处都没有的模型**没有价格**——成本记录为 null，不是零。零意味着免费，但没人这
么说过。概览页报告有多少条请求未计入总额。

成本在请求结束时快照到日志行。事后改价格不改写历史。缓存部分按缓存价格计费
（没有缓存价格时退回输入价格并记录）。超长上下文按厂商的阶梯价格自动适用。

除上面的可关闭更新检查外，目录刷新是 Polyglot 对非你配置地址发出的另一种请求。
它只在你按按钮时发生，且不发送任何内容——它是一次对公开文件的 GET。

### Tracing

默认关闭。指向你自己的 collector：

```bash
TRACING_ENABLED=true
OTLP_ENDPOINT=http://collector:4318
```

传输：**仅 OTLP over HTTP + JSON 编码。** 没有 gRPC。指标不通过 OTLP 导出——用
Prometheus 抓取。

每条请求产生一个 `gateway.request` span，子 span 包括 `router.resolve` 和
`upstream.request`。有效的入站 `traceparent` 继续调用者的 trace。导出异步且尽力
而为——collector 挂了只影响计数器，不影响请求。

### 绝不记录的内容

不是过滤——**压根不传给遥测层**：prompt、补全、工具参数、请求/响应体、API Key、
请求头、cookie、查询字符串、客户端 IP、上游错误文本。

### 供应商容错

刚出错的供应商在有其他供应商提供同模型时会被**跳过 30 秒**
（`PROVIDER_COOLDOWN`）。只有一个供应商的模型永远不跳过。所有供应商都在冷却时，
忽略冷却。

供应商可选在**连续两次 401 后自动关闭**——按供应商单独启用，因为 401 也可能来自
地域限制和配额耗尽。

### 请求识别

每条请求有 `X-Request-Id`。`X-Title`（OpenRouter 的约定）、`HTTP-Referer` 和
`User-Agent` 记录下来用于过滤。自定义标签放在请求的 `metadata` 字段里：

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Authorization: Bearer pg_xxx" -H "X-Title: docs-site" \
  -d '{"model":"my-model","metadata":{"task":"summarise"},
       "messages":[{"role":"user","content":"你好"}]}'
```

## 安全

- 供应商凭据：AES-256-GCM 加密存储。**永远不发给浏览器**——编辑表单也不会。
- API Key：SHA-256 哈希存储，创建时只显示一次。
- 错误路径：上游凭据在到达客户端或日志之前被抹掉。
- 请求日志：只记录元数据。**不存储 prompt，不存储补全，不记录请求头。**
- 管理会话：HttpOnly Cookie + 双提交 CSRF token。
- 首次安装：一次性安装口令，并由数据库保证只能有一个管理员。
- 上游存储：OpenAI Responses 和 Gemini Interactions 的 `store` 字段缺省为
  `true`。Polyglot 显式发送 `false`。
- 媒体下载：私有 IP 无条件拒绝，有大小限制、类型校验和重定向限制。
- 不向本项目作者发送任何遥测或数据。

### 密码恢复

一个本地管理员，没有邮箱，没有密码重置链接。如果被锁在外面：

```bash
docker compose exec polyglot polyglot reset-password
```

打印用户名和新密码。网关继续运行，现有 API Key 继续工作，所有管理会话失效。

## 从源码构建

需要 Go 1.26.6 和 Node 20+。pnpm 由 `web/package.json` 的 `packageManager` 锁
定——`corepack` 会拉取正确的版本。

```bash
make build          # WebUI + 单个静态二进制 bin/polyglot
make test           # Go 测试套件
make check          # 全部：测试 + SDK 兼容性 + 类型检查 + lint + vet + 构建
make vulncheck      # 使用固定 Go 和 govulncheck 扫描可达漏洞
```

本地 UI 开发：

```bash
make web-dev        # 终端 1: Vite :5173，热更新
make dev            # 终端 2: Go API :3000，把 UI 代理到 Vite
```

### SDK 兼容性测试

Polyglot 自己实现每种协议——`net/http`、`encoding/json`、自己的 SSE 解析器和写入
器。运行时代码不导入任何供应商 SDK。但兼容性必须用真的 SDK 证明，所以官方
OpenAI、Anthropic 和 Google GenAI SDK 对构建好的 Polyglot 二进制走真实 HTTP：

```
官方 SDK → HTTP → Polyglot（构建好的二进制）→ HTTP → mock 上游
```

`tests/compatibility/` 是独立的 Go module——SDK 依赖不进入根 `go.mod` 也不进入
生产二进制。`make compatibility-test` 运行它。不需要 API Key；上游是本地 mock。

### Docker 构建

```bash
docker build -t polyglot:latest .
```

多阶段：Node 构建 UI，Go 构建二进制，Alpine 运行。`CGO_ENABLED=0`——SQLite 驱动
是纯 Go 的，二进制完全静态。

### 发布 Docker 版本

推送 SemVer Git tag 会触发 `.github/workflows/docker-release.yml`，将相同标签同时发布
到 `ghcr.io/qunqin24/polyglot` 和
`docker.io/<DOCKERHUB_USERNAME>/polyglot`。请先配置仓库 Secrets：
`DOCKERHUB_USERNAME` 与 `DOCKERHUB_TOKEN`。

| Git tag | 发布的镜像 tag |
|---|---|
| `v1.2.3` | `latest`、`v1.2.3`、`1.2.3` |
| `v1.3.0-preview.1` | `preview-latest`、`v1.3.0-preview.1`、`1.3.0-preview.1` |

```bash
# 正式版
git tag v1.2.3
git push origin v1.2.3

# Preview 版
git tag v1.3.0-preview.1
git push origin v1.3.0-preview.1
```

工作流会构建并冒烟验证 AMD64 与 ARM64 两个版本，然后发布包含 SBOM 和 provenance
证明的共享 manifest。`vX.Y.Z` 和 `vX.Y.Z-preview.N` 之外的标签会被拒绝。手动触发
还可以补发已有标签，无需移动标签。

## 命令行

```bash
polyglot                  # 启动网关
polyglot reset-password   # 恢复被锁的管理员
polyglot config           # 当前配置及每个值的来源
polyglot version          # 构建版本和 commit
polyglot help             # 所有环境变量及默认值
```

## 计划中，但还没做

| | |
|---|---|
| **音频输入** | 图片和 PDF 今天就能转；音频还不行 |
| **Embeddings** | 还没有 `/v1/embeddings` 端点 |
| **Token 计数** | 还没有 `count_tokens` 端点 |

遇到这些情况，Polyglot 会在保真度记录里说明，然后继续处理请求剩下的部分。音频
附件直接拒绝，不会伪装成文档转发。

**Gemini Interactions** 的 Go SDK 尚不支持它。它由 codec 测试和 HTTP 集成测试覆
盖，但没有像其它四种那样的官方 SDK 兼容性测试。线上格式取自官方 TypeScript
provider schema 和真实抓包。在这一点改变之前，请把它当作五种协议里最未经检验的
那个。

## 边界

Polyglot 是一个协议转换网关，刻意**不做** API 转售平台。

**不包含，不打算做：** 计费、充值、兑换码、推广返利、用户套餐、多租户、RBAC、
图片/视频/音频生成、RAG、agent、MCP。不引入 Redis、PostgreSQL、Kafka、独立
worker 或调度器。

显示一条请求花了多少钱不是计费。没有余额、没有配额、没有扣款、没有账单。

服务端会话状态是设计上的边界。Polyglot 无状态，不保存任何历史轮次。Responses
API 的 `store` 和 `previous_response_id` 报为不支持。会话归你的客户端管。

## 项目结构

```
cmd/polyglot/          入口：配置、存储、HTTP 服务器、关闭
internal/
  canonical/           协议中立的请求、响应和事件模型
  protocol/            codec 接口 + 每种协议一个包
  provider/            调上游：URL、鉴权、HTTP transport
  router/              模型名 → 供应商 + 上游模型
  gateway/             请求管线
  stream/              SSE 编解码，双向
  pricing/             models.dev 目录 + 成本计算
  store/               全部 SQLite 操作
  auth/                API Key 和管理会话
  usage/               缓冲的请求日志
  telemetry/           请求生命周期、Prometheus、OpenTelemetry
  api/                 HTTP 表面：协议端点、管理 API、WebUI
  config/ idgen/ version/
migrations/            按文件名顺序执行的 *.sql，编译进二进制
tests/compatibility/   官方 SDK 测试（独立 module）
web/                   React 19 + Vite + Tailwind v4 + shadcn/ui
  src/lib/i18n/        带类型的翻译目录（en、zh）
```

## 许可证

[Apache License 2.0](LICENSE)。
