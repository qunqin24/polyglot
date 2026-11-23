# 价格目录与成本

Polyglot 给它记录的请求计价，让运维者看到钱花在哪。这是**成本可见性，不是计费**：没有余额、没有充值、不从任何东西里扣减、没有账单。每个数字都是估算，界面上这么写。代码在 `internal/pricing`，存储在 `internal/store/pricing.go`。

## 唯一的例外：单个 key 的预算

一个交给别人用的 key 需要花费上限，光「盯着仪表盘」不算上限，所以 `api_keys.budget_usd`（迁移 `0015_api_key_budget.sql`）是一个以美元计的上限，可以拒绝请求。窗口由运维者选：`total`（从 `budget_anchor` 起累计，只有运维者手动重置，`POST /api/keys/{id}/budget/reset`）或按 UTC 的每天、每周、每月滚动。`NULL` 表示没有上限，这是这个功能出现之前每个 key 的状态。它在 `internal/auth/limits.go` 里和 RPM、TPD 等限制并排执行，不在任何新地方。

- 预算是近似的，界面直说：请求在结束之后才计价，越线的那一次已经付掉了；没有价格的模型不给总额加任何东西（未知不是零）。不要靠「把缺失的价格当成免费」来「修」它。
- 花费怎么进来的：价格在 `usage.Logger` 的 flush goroutine 上算出来，`OnSpend` 注册的 `auth.KeyLimiter.RecordSpend` 同时收到。窗口内的累计在第一次需要时从请求日志读出（有 `idx_request_logs_key_started` 索引），之后由 `RecordSpend` 保持最新。
- **其他任何跟钱有关的东西都不能拒绝请求**：provider 花费、全局总额、月度账单都不行。一个上限，在一个 key 上，在一个地方检查。

## 价格怎么解析

`Resolver.Resolve(providerID, model)`（`resolver.go`），最具体的优先：

1. 运维者给这个模型定的价格（`models` 表上的 `price_input`、`price_output`、`price_cache_read`、`price_cache_write`，迁移 `0011_pricing.sql`），按「provider + 模型」为键，一个 provider 上的覆盖不会影响另一个 provider 上的同名模型；
2. models.dev 目录里的官方厂商价格（`price_catalog` 表，迁移 `0011`、`0012`）；
3. 没有：成本**未知**。

**未知永远不是零。** 零是在说这次请求免费，没人做过这个断言。`request_logs.cost_usd` 为 NULL，UI 显示短横线，任何总额都要报出有多少行不在里面。运维者明确写下的 0 是另一回事，要保留：他可以声明某个模型是免费的。所以 `pricing.Price` 的四个字段都是 `*float64`，`Compute` 在没有 input 或 output 价格时返回 `ok=false`，调用方必须记未知而不是 0。一个没报任何 token 的请求同样不计价。

**override 是四个可空的数字，不是目录行的拷贝**（`Price.Overlay`）。留空的字段跟随目录，所以只改了 input 价格的运维者仍然跟着官方的 output 降价走。四个全清空，模型就回到目录当下的价格。

**刷新目录永远不碰运维者定的价格**：目录在自己的表里，override 在模型行上。

**成本在请求结束时快照到日志行上**，改价格不会重写历史，后来才有的价格也不会回填当时没有价格的行。

## 计算公式（`pricing.Compute`）

```
uncached = Input - CachedInput - CacheWrite
cost = uncached × input + CachedInput × cache_read + CacheWrite × cache_write + Output × output   （每百万 token）
```

- 缓存部分是 prompt 的组成部分，不是额外加的（`canonical.Usage` 的含义，见 [architecture.md](architecture.md)），所以未缓存部分要先减掉缓存部分。上游报的缓存部分比整个 prompt 还大时，相信更具体的缓存数，未缓存部分取 0。
- 缺 `cache_read` / `cache_write` 价格时回退到 input 价格，并在成本备注里记 `cache_price_assumed`。
- 推理 token 不额外累加：各厂商对它是否已经算在 output 里没有共识。
- **长上下文档位**只来自目录：好几家厂商在 prompt 超过某个长度后整套价格都变（OpenAI 272k、Google 200k、xAI 128k），`Rates.Tier` 携带那一档，阈值拿整个 prompt（含缓存部分）比较。目录里以上下文长度以外的东西做键的档位直接忽略，不猜。被 override 的模型一律按平价算：运维者报了一组数字，套一个他没提过的倍数等于替他说了一个价。按高档计价的请求记 `long_context_price`。
- 备注（`Cost.Notes`）存在请求日志上，让依赖某个假设的数字说明自己的假设。

**计价永远不在请求路径上跑。** `Resolver` 持有内存快照，在 usage logger 的 flush goroutine 里读（`Logger.price`），价格或目录变化时 `Reload`。计价失败最多损失一个数字，不损失请求。

## models.dev 目录

- `internal/pricing/catalog.go` 的 `Parse` 把 models.dev 的 `api.json` 精简成 `Snapshot{Version, Entries, Makers}`；`Entry` 的 id 是 `pricing.Normalize` 之后的模型名（去掉最后一个 `/` 之前的厂商前缀和 `:` 之后的路由后缀，转小写），所以 `anthropic/claude-sonnet-4-5`、`claude-sonnet-4-5:free` 都落到同一条。
- **只摄入第一方价格。** models.dev 收录一百多个 provider，大多是报自己加价的转售商：`claude-sonnet-4-5` 在十家下面出现，有三种价格。`firstParty` 是手工维护的厂商 id 列表，有序，所以平局也有确定的结果（Z.ai 的 GLM 同时出现在 `zai` 和 `zhipuai` 下且数字不一致）。永远不要拿一个转售商的数字去给另一个转售商的模型定价；更便宜的线路是 override 该干的事。加一个厂商是在这个列表里加一行，再重新生成快照。
- **出品方（makers）**：`Snapshot.Makers` 把规范化的模型 id 映射到出品方的 models.dev id（`glm-5.2` → `zhipuai`，即使它是阿里在卖）。它覆盖 models.dev 里所有 provider（不只是第一方），不参与任何价格计算，只用来让 WebUI 给模型挑 logo（`web/src/components/model-icon.tsx` 的 `MAKERS`）。优先用 `canonical_model_id` 的前缀；没有的话，由本身就是出品方的 provider 的条目推断，明确声明的优先于推断的。存在 `model_makers` 表（迁移 `0021_model_makers.sql`），随目录一起整体替换，在 `admin_models.go` 里填到模型列表的 `maker` 字段。
- **快照是嵌入的，只在需要时刷新。** `internal/pricing/snapshot.json` 由 `make catalog`（`go run ./internal/pricing/gen`，会联网拉 models.dev）重新生成并提交进仓库，`embed.go` 用 `go:embed` 嵌入，所以离线部署第一次请求就有价格。启动时 `Store.LoadEmbeddedCatalog` 只在数据库里的版本比嵌入的旧时才替换（版本是日期），所以升级二进制会带来新价格，但不会回滚运维者更近的一次刷新。
- 运行时刷新（`POST /api/pricing/catalog/refresh`，`pricing.Fetch`）是运维者按下按钮时对 `https://models.dev/api.json` 发的一次 GET：没有标识、没有 query、没有 body。失败是信息不是错误，已加载的目录保持不变。它是 Polyglot 唯一会拨一个没有运维者配置过的地址的地方，这不是 phone home，也不能变成它。
- 改了 `Parse`、`firstParty` 之后要跑 `make catalog` 并把 `snapshot.json` 的 diff 一起提交（它缩进、以换行结尾，价格变化显示成一行）。

## 相关的管理 API

`GET /api/pricing`（每个模型的有效价格和来源）、`PUT /api/models/{id}/pricing`（设置 override）、`GET /api/pricing/catalog`（目录状态）、`POST /api/pricing/catalog/refresh`、`GET /api/stats/cost`（成本统计）。实现在 `internal/api/admin_pricing.go`。
