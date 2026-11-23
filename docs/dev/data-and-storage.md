# 数据、迁移、加密与团队作用域

## SQLite

数据库是 `$DATA_DIR/polyglot.db`，驱动是 `modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`，二进制保持静态）。所有 SQL 都在 `internal/store`，业务代码只调用带类型的方法，不写 SQL，也不引 ORM。

`store.Open`（`internal/store/store.go`）的连接参数：WAL、`busy_timeout(5000)`、`foreign_keys(ON)`、`synchronous(NORMAL)`；连接池最多 8 个（SQLite 同一时刻只有一个写者，小池子避免锁抖动，同时 WAL 下允许并发读）。`Open` 依次做：连接 → 跑迁移 → 加载（或创建）密钥 → 加载每个团队的内容记录设置。

文件按主题分：`providers.go`（providers、aliases）、`models.go`、`auth.go`（管理员、会话、API key、log key）、`logs.go`、`stats.go`、`pricing.go`、`content_logs.go`、`scope.go`（团队作用域）、`cipher.go`（加密）、`hash.go`。

## 迁移

- 放在 `migrations/`，命名 `NNNN_description.sql`（现在到 `0022_model_protocol.sql`），用 `go:embed` 嵌入（`migrations/embed.go`）。没有单独的迁移工具，启动时 `Store.migrate` 按**文件名排序**逐个执行，已执行的记在 `schema_migrations(name, applied_at)`。
- **每个文件在一个事务里执行**，并且是在 `foreign_keys` 为 ON 的连接上；SQLite 不允许在事务里改这个 PRAGMA。后果：对被外键引用的父表做 `DROP TABLE`，会触发隐式级联删除，把子表的行一起删掉；`ALTER TABLE ... RENAME` 也躲不过去，新版 SQLite 会改写子表的 REFERENCES。所以重建父表的迁移必须在同一个文件里把子表一起重建。`0020_teams.sql` 的注释记录了这个原因，也是 `providers.name` 到现在仍然全局 UNIQUE（而不是 `UNIQUE(team_id, name)`）的原因。
- **每一次 schema 变更都要加一个迁移**，不要改已经执行过的迁移，加一个新的。
- **旧数据库必须能干净升级。** 在声称一个迁移可用之前，拿上一个版本创建的数据库测一遍：`internal/store/migrate_test.go` 的 `openAtMigration` 能把库建到指定迁移为止、放进真实的行、再用新二进制打开；`TestADatabaseFromThePreviousVersionUpgrades`、`TestMigrationsAreIdempotent` 是范例。
- 给已有表加 `NOT NULL` 列要带 `DEFAULT`，并想清楚历史行该是什么值（`0022` 的 `models.protocol` 默认 `''`，表示用 provider 的协议）。
- 动了迁移后，除了测试，再用上一个版本的数据库启动一次二进制，确认能升级、老数据仍能路由。

### 加一个迁移

1. 新建 `migrations/00NN_xxx.sql`，编号接着现有最大的；文件头注释写清楚为什么、历史行怎么处理。
2. 在 `internal/store` 里改对应的结构体、列清单、扫描函数和读写方法。
3. 如果是团队拥有的表，按下面「团队作用域」的规则走。
4. 补测试：`migrate_test.go` 里加一个从上一版升级的用例；新方法在 `*_test.go` 里测。
5. API 返回的 JSON 里不要出现 nil 切片：它会序列化成 `null`，弄坏有类型的客户端。初始化成 `[]`，`internal/api/nonnull_test.go` 检查这一点。

## 写日志

每个完成的请求写一行 `request_logs`，**绝不按流式 chunk 写库**。`usage.Logger`（`internal/usage/logger.go`）有 1024 条的缓冲 channel，`Log` 从不阻塞请求路径：缓冲满了就丢弃并计数（`Dropped()` 显示在设置页，每丢 100 条打一次警告）；后台每 2 秒或攒满 128 条批量 `InsertRequestLogs`，并在这里调用 `pricing` 算成本，再把花费通知 `auth.KeyLimiter`。每小时按设置 `log_retention_days` 清理旧日志。关闭时先停 HTTP server，再停 logger，确保缓冲里的日志写完。

## 加密

`internal/store/cipher.go`：AES-256-GCM，密钥是 `$DATA_DIR/secret.key`（32 字节，权限 0600，首次启动随机生成）。密钥放在数据库文件旁边但是单独的文件，所以只偷到 `.db` 不够，同时 `docker compose up` 不需要用户做密钥管理。`Encrypt` 输出 `nonce || 密文`，空字符串加密成 nil，所以「没设置」保持「没设置」。解密失败几乎总是 `secret.key` 变了或丢了。

加密存放的东西：

- provider 的 API key（`providers.api_key_enc`）。`store.Provider.APIKey` 带 `json:"-"`，绝不到达浏览器，连编辑它的表单也不行；管理员要查看时走 `POST /api/providers/{id}/secret`。
- Polyglot 自己的 API key 的明文副本（`api_keys.secret_enc`，迁移 0017）。认证走 SHA-256 哈希（`store.HashToken`），请求路径查的还是哈希，这一点没变；密文是为了让运维者能把自己持有的 key 读回来。读回是管理员会话下的 POST（`POST /api/keys/{id}/secret`），绝不是 GET，明文只出现在那个响应体里（WebUI 把它复制到剪贴板而不显示，免得留在截图里）。0017 之前创建的 key 没有密文，会返回 `revealable: false`，那是历史事实，不需要修。
- 运行参数（`settings` 表里 key 为 `runtime` 的一行，见 [configuration.md](configuration.md)），用 `GetSecretSetting` / `SetSecretSetting`。

`POLYGLOT_SECRET_KEY` 已不再是设置项。设置了它的旧部署，所有凭据是用它加密的，所以 `LoadCipher` 最后兼容一次：把它派生出的密钥写进密钥文件，之后以文件为准；原来存在的、不同的密钥文件被改名为 `secret.key.replaced-<时间戳>` 保留，不删除。

其他安全相关：

- API key 的名字唯一（迁移 0018）：名字是列表和 `request_logs.api_key_name` 里区分两个 key 的依据。重名返回 409；创建时没填名字的会自动取一个空闲的默认名。
- 管理员：单管理员（迁移 0016）；密码用 bcrypt；会话是 HttpOnly cookie，加上每个状态变更请求的 double-submit CSRF token（cookie `polyglot_csrf`、头 `X-CSRF-Token`）；会话有效期 30 天。Secure 标志按请求本身判断（TLS，或可信代理说明它是 HTTPS）。
- 每条错误路径在到达客户端或日志之前都要剥掉上游凭据（`gateway.redact`、`api.redactSecret`），有测试。
- provider base URL 要校验（scheme、不能内嵌凭据）；不带着鉴权头跟随跨主机重定向；保留输入大小上限、上游响应上限和超时。
- `trust_proxy_headers` 默认开：`X-Forwarded-For` 决定 `request_logs.client_ip`。代价是明说的：端口不经过代理也能访问到的部署里，调用方可以自己设这个头，决定日志里怎么写他，那种部署应该关掉它。地址只在一个地方解析（`middleware.RealIP`），日志、限流器和 key 来源页读同一个结果，不要再加第二个地方读转发头。

## 请求日志里的内容

- **默认只记元数据**：模型、协议、provider、token 数、耗时、状态、保真度备注、成本等；元数据日志永远不记录请求头；prompt 和补全文本不进默认路径。
- **完整内容记录**是单独的、默认关闭的功能，按团队开关（`team_content_logging` 表，通过 `/api/content-logging` 读写），保留期只能选 3/7/30 天。开启后 `internal/capture` 把每个阶段的真实 payload 增量写成压缩事件流，存在 `$DATA_DIR/log-content/`，长流不会堆在内存里；阶段包括客户端请求、canonical 请求、每次上游尝试的请求和响应、canonical 响应。传输凭据（`Authorization`、cookie、provider 专用 secret 头、URL 里的凭据参数）必须被排除在捕获之外，这个边界不能放松。除此之外任何地方（遥测、错误路径、别的表）都不存 prompt 或补全。
- 外部访问走只读的 log key（`log_keys` 表，`/api/logs/v1/*`），见 [docs/log-api.md](../log-api.md)。

## 团队作用域（`store.Scope`）

团队是**可选的隔离层**：几个人共用同一个网关，各自的 provider、凭据、模型、key、账本和日志互不可见。它存在的理由只有资源归属，不是组织架构、用户系统或 RBAC。

**永远恰好有一个团队**：默认部署里就是 `store.DefaultTeamID`（1，名为 Default），界面上不出现，没有两套代码路径。迁移 0020 种下它，并把之前所有的行折进去。所以 scope 不是「开了团队才有」，是从来就有：每一行可拥有的数据都带非空的 `team_id`，每一条碰这些数据的查询都带团队谓词，只是只有一个团队时看不出来。

实现（`internal/store/scope.go`）：

- `Store.ForTeam(id)` 返回 `*Scope`。所有读写团队数据的方法都挂在 `*Scope` 上，不挂在 `*Store` 上，所以不指定团队就够不着数据，这是编译错误，不是 review 意见。管理 API 里用 `s.team()`（目前固定是默认团队），协议端点里用 `s.callerTeam(r)`（API key 所属的团队），gateway 里用 `g.Store.ForTeam(teamID)`。
- 所有权根是 `providers`、`api_keys`、`request_logs`、`log_keys`，它们有自己的 `team_id` 列。`models`、`model_aliases`、`api_key_models` 没有，它们通过 `provider_id` 外键到达自己的团队，多存一份会在 provider 换团队时和 `providers.team_id` 打架。
- 查询只能通过 scope 里的构造函数：`query`、`queryRow`（自己拥有 `team_id` 的表）、`queryViaProvider`、`queryRowViaProvider`（通过 provider 连接的子表，内部是 INNER JOIN，不能写成 LEFT JOIN，否则没有 provider 的行会漏进别的团队的结果）、`exec`（只接受 `DELETE` 或 `UPDATE SET ...` 两种动词）。构造函数自己写 `WHERE team_id = ?`，调用方传的片段以悬空的 `AND` 开头或直接接 `ORDER BY`；写成了自带 `WHERE` 的完整语句会得到 `WHERE team_id = ? WHERE ...`，SQLite 直接报语法错。也就是忘了谓词是语法错误，不会是泄漏。
- 列里 `team_id` 的默认值是 **0**，不是 1。0 是「丢了团队」的哨兵：忘了带团队的插入落到 `WHERE team_id = 0`，能被找到，而不是悄悄归到默认团队。`providers`、`api_keys`、`log_keys` 各有一个 `BEFORE INSERT` 触发器，`team_id = 0` 时 `RAISE(ABORT, ...)`（用触发器而不是 CHECK，因为 SQLite 对已有数据的表 `ADD COLUMN ... CHECK` 会拒绝，会让新库和升级库的 schema 分叉）。`request_logs` 有列没触发器，它只有 `InsertRequestLogs` 一个写者，在缓冲 flush 的路径上，不给这条路径增加逐行工作；`team_id` 也不是外键，日志是已经发生的事实，删团队不应该改写或级联删除它。
- 跨团队访问返回 `store.ErrNotFound`，永远不返回权限错误：403 等于确认资源存在。
- 请求日志在写入时把团队名和 key 名快照下来（`team_name`、`api_key_name`），所以删除 key 或团队之后历史仍然可读。
- 目前还没有团队的 API 或界面：`Server.team()` 是唯一需要学会「读管理员所属团队」的地方，将来团队可见时在这里改。

新增一张团队拥有的表要做的：直接带 `team_id` 的，加进 `owned` 注册表（`ownedXxx`）并用 `query`/`exec`；挂在 provider 下的子表，用 `queryViaProvider`；要跨团队行为的话在 `scope_test.go` 加用例（`TestAQueryCannotCrossTeams` 是范例）。

价格目录（`price_catalog`、`model_makers`）是全局的，不属于任何团队；模型价格覆盖在 `models` 行上，所以跟着 provider 走。价格相关见 [pricing.md](pricing.md)。
