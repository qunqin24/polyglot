# 配置与设置

配置分两层：启动前就要知道的，用环境变量；其余都在 WebUI「设置」页里编辑，存在数据库里。没有配置文件，命令行也没有 flag（见 `cmd/polyglot/cli.go`）。

## 环境变量

只剩这几个：

| 变量 | 默认 | 说明 |
|---|---|---|
| `LISTEN` | `127.0.0.1:3000` | 监听地址，如 `:3000` 监听所有网卡 |
| `DATA_DIR` | `/data`（目录存在或能创建时），否则 `./data` | 数据库、加密密钥和一切运行时产物 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error`；由 `main.go` 的 `newLogger` 直接读，在其他一切之前 |
| `POLYGLOT_DEV` | `false` | 开发模式：WebUI 由 Vite dev server 提供，不用嵌入的 bundle |
| `POLYGLOT_DEV_PROXY` | `http://127.0.0.1:5173` | 开发模式下代理到的 Vite 地址 |

规则：

- `internal/config/config.go` 的 `Load` 读取它们。`LISTEN`、`DATA_DIR` 和两个 `POLYGLOT_DEV*` 都接受带不带 `POLYGLOT_` 前缀两种写法，带前缀的优先（`envStr`）。`LOG_LEVEL` 不带前缀。
- `config.Variables` 列出所有仍在读的变量。`cmd/polyglot/settings.go` 的 `envSettings` 是 `polyglot help` 和 `polyglot config` 共用的清单（只含 `LISTEN`、`DATA_DIR`、`LOG_LEVEL` 三个对运维者有意义的）。`cli_test.go` 的 `TestEverySettingIsDocumented` 扫 `internal/config/config.go` 和 `cmd/polyglot/main.go` 里的 `envStr(...)`、`envBool(...)`、`Getenv(...)` 调用，读了变量却没进 `envSettings` 就失败（测试里的 `notSettings` 豁免了 `POLYGLOT_DEV`、`POLYGLOT_DEV_PROXY`、`PORT`、`DB_PATH`）；`TestSettingsTableHasNoInvention` 反向检查：`envSettings` 里列了但代码没读的变量会失败。新增环境变量要同时改这几处。
- 数据库固定是 `$DATA_DIR/polyglot.db`。`DATA_DIR` 里还有 `secret.key`（加密密钥）、`setup.token`（首次启动的 setup 令牌；创建管理员时被消耗，之后启动会删掉残留文件）、`log-content/`（完整内容记录）。挂一个 volume 就等于迁移了整个安装。
- 两个已删除的变量不能静默忽略，会让启动失败并说明怎么改：`PORT`（改用 `LISTEN=:端口`）、`DB_PATH`（指向别处时报错，要求把文件挪到 `$DATA_DIR/polyglot.db`）。`LOG_FORMAT`、`POLYGLOT_SETUP_TOKEN` 已不再读取，设置了只会打一条警告。`POLYGLOT_SECRET_KEY` 只在加密层兼容地读最后一次（见 [data-and-storage.md](data-and-storage.md)）。
- `MaxUpstreamBytes`（非流式上游响应上限，64 MiB）是 `Config` 里写死的值，不是设置。

`polyglot config` 打印当前生效的环境变量（来源是 default 或 env）和数据库里的运行参数；凭据只显示「已设置」，不显示值。

## WebUI 运行参数（`internal/settings`）

「设置 → 运行参数」对应的值。它们原先都是环境变量，现在存在数据库的 `settings` 表里，key 是 `runtime`，值是一整个 JSON，用和 provider 凭据相同的密钥加密（因为里面有 metrics token 和 OTLP 头）。读写走 `Store.GetSecretSetting` / `SetSecretSetting`。

### 结构

- `settings.Values`（`settings.go`）：全部可编辑的值，JSON 字段名就是管理 API 的字段名。`Defaults()` 是新安装的默认值，`Validate()` 拒绝会弄坏网关的值，报错信息原样显示给运维者。
- `settings.Manager`：用 `atomic.Pointer` 持有当前值。`Get()` 返回副本；`Update(ctx, v)` 先校验、再持久化、再替换、最后通知所有订阅者；`Subscribe(f)` 注册回调，并**立刻用当前值调用一次**，所以组件不用单独读初始值。
- `settings.Load`：启动时读取已存的值，在默认值上叠加（所以后来新增的字段缺省时拿默认值）。如果库里还没有这一行，就从旧环境变量导入一次（`legacy.go`，`legacyNames` 列出旧变量，带不带 `POLYGLOT_` 前缀都认，带前缀的优先），并打一条警告告诉运维者可以删掉它们；之后这些变量都被忽略，仍然设置着的会被警告点名。读出来的值校验不过（比如新版本收紧了规则）时用默认值启动并警告，不拒绝启动，WebUI 里可以改好。首次启动还会生成 metrics token。
- `settings.Static(v)`：不带存储的 Manager，给测试和不保存的工具用。

### 生效方式

| 设置 | 怎么生效 |
|---|---|
| `log_retention_days`（默认 30，0 = 不清理）| 订阅：`usage.Logger.SetRetentionDays` |
| `provider_cooldown_secs`（30）| 订阅：`provider.Health.SetCooldown` |
| `upstream_timeout_secs`（600）| 订阅：`router.SetDefaultTimeout`，provider 自己没设超时时用；已在途的请求沿用启动时的值 |
| `max_request_mb`（32）| 每个请求读：`Gateway.Settings.Get().MaxRequestBytes()` |
| `fetch_remote_media`（false）、`max_media_mb`（20）| 前者每个请求读；后者订阅：`media.Fetcher.SetMaxBytes` |
| `trust_proxy_headers`（true）| 每个请求读（`server.go` 里包装 `middleware.RealIP` 的中间件，以及 `secureCookies`）|
| `block_private_upstream`（false）| 订阅：`provider.SetBlockPrivate` |
| `update_check_enabled`（true）、`update_repository`（`qunqin24/polyglot`）| 订阅：`update.Checker.Configure` |
| `metrics_token` | 订阅：`telemetry.SetMetricsToken`；只能通过 `POST /api/settings/runtime/metrics-token` 重新生成，不在表单里 |
| `metrics_enabled`（true）、`tracing_enabled`、`otlp_endpoint`、`otlp_headers`、`trace_sample_ratio`（1）、`service_name`（`polyglot`）| **重启后生效**：遥测对象在进程启动时由 `telemetryConfig(...)` 构造一次 |

重启才生效的那组，`api.telemetryDiffers` 对比「启动时的值」（`Server.startSettings`）和当前值，响应里的 `restart_pending` 为 true 时前端显示「等待重启」。

### 管理 API

- `GET /api/settings/runtime`：返回 `{settings, restart_pending}`。
- `PUT /api/settings/runtime`：用请求体**整体替换**所有可编辑的值（`metrics_token` 除外，服务端保留原值，免得没显示它的表单把它清空）。所以请求里缺的字段会变成零值，可能校验失败或意外把布尔设成 false；前端必须发全量。同时 `decodeJSON` 拒绝未知字段，前端多发一个后端 `Values` 里没有的字段也会得到 400，两边字段必须一致。
- `POST /api/settings/runtime/metrics-token`：重新生成 scrape 凭据，旧的立刻失效。
- 另有一个和这些无关的小设置：`PUT /api/settings` 只改 `timezone`（IANA 名，只影响显示，存储一律 UTC），明文存在 `settings` 表里。
- 完整内容记录的开关不在 `runtime` 里：它是每个团队一行（`team_content_logging` 表），通过 `/api/content-logging` 读写，见 [data-and-storage.md](data-and-storage.md)。

### 新增一个设置项

假设要加 `foo_limit`：

1. `internal/settings/settings.go`：给 `Values` 加字段和 `json:"foo_limit"` 标签；在 `Defaults()` 给默认值；在 `Validate()` 加范围检查，报错信息要能直接给运维者看。如果有换算（秒 → `time.Duration`、MB → 字节），照 `UpstreamTimeout()`、`MaxRequestBytes()` 加个方法。新设置**不需要**加进 `legacy.go`：那里只负责导入历史上存在过的环境变量。
2. 使用方：
   - 每次用到时读，`Settings.Get().FooLimit`（gateway 就是这样，改完下个请求就生效）；
   - 或者组件持有自己的原子字段，提供 `SetFoo(...)`，在 `internal/api/server.go` 的 `NewServer` 里 `rs.Subscribe(func(v settings.Values){ ... })` 推送给它。`provider.Health`、`router.Router` 是例子，注意并发安全。
   - 如果只能在启动时应用（像遥测），就像 `telemetryConfig` 那样在 `cmd/polyglot/main.go` 里读，并把它加进 `api.telemetryDiffers`，这样前端才会提示需要重启。
3. `cmd/polyglot/settings.go` 的 `settingRows`：加一行，让 `polyglot config` 能显示（凭据类只显示是否已设置）。
4. 前端：`web/src/lib/api.ts` 的 `RuntimeSettings` 加字段；`web/src/components/runtime-settings.tsx` 里给表单加控件，数值类要在 `Draft` 类型、`toDraft`、`fromDraft` 里做字符串和数字的转换；`en.ts`、`zh.ts` 加标签和说明（键以 `runtime.` 开头）。
5. 测试：`internal/settings/settings_test.go` 覆盖默认值和校验；API 层测试里用 harness 的 `withSettings(func(*settings.Values){...})` 改设置（见 [testing.md](testing.md)）。
6. 文档：README 里提到该参数的地方。

## 命令行与首次启动

- 首次启动没有管理员时，`setup.LoadOrCreate` 在 `DATA_DIR/setup.token` 生成一个令牌并在日志里打印路径；设置管理员时要通过请求头 `X-Polyglot-Setup-Token` 提供（放在头里而不是 query 参数，避免进浏览器历史和访问日志）。已有管理员后 `RemoveStaleFile` 删除残留的令牌文件。
- `polyglot reset-password`：重置管理员密码并让所有会话失效，密码只显示一次。信任边界是文件系统访问：能对 `DATA_DIR` 运行这个二进制的人本来就能读数据库。
