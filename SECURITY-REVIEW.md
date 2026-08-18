# Polyglot 网络安全审查报告

- **日期：** 2026-08-18
- **范围：** 白盒源码审查（认证、管理面、出站请求、存储、前端、部署默认值）+ `govulncheck ./...`
- **未做：** 对正在运行的实例做入侵测试；未读取 `data/` 中的真实密钥或数据库内容
- **原则：** 只报告，不改代码。是否修复由维护者自行衡量。

> **后续处置（2026-08-18）：** 1.1 的首次安装保护和单管理员约束、2.6 的
> Go 安全基线已经实施。2.5 的 bcrypt 行为描述已按实际依赖修正。其余条目仍是
> 待维护者衡量的审查发现。

---

## 总体结论

核心协议网关写得很谨慎。没有看到「拿到一把客户端 API key 就能读内网 / 偷提供商密钥 / 注入 SQL」这类洞。

协议面、密钥存储、日志红线和默认关闭的媒体抓取都站得住。真正会在发布当天被人打的，是**首次安装接管**，以及几处部署默认值。

**建议：先衡量并处理高危项，再公开仓库和镜像。**

---

## 1. 发布前必须衡量（高危）

### 1.1 首次安装可被抢注管理员

**严重度：** 高

**处置：已修复。** 首次安装现在要求一次性安装口令；裸机默认只监听 loopback，
Compose 只向宿主机 loopback 发布端口；新增数据库唯一约束和原子创建方法，并发
setup 最终只能创建一个管理员。

`POST /api/setup` 在「还没有管理员」时对全网开放，且没有安装口令。默认监听 `:3000`（所有网卡），`docker-compose.yml` 又是 `3000:3000`，等于把这个窗口直接挂到局域网甚至公网。

```go
// internal/api/admin.go — handleSetup
n, err := s.store.AdminCount(...)
if n > 0 {
    writeErr(w, http.StatusConflict, "Polyglot has already been set up")
    return
}
// ... 创建管理员并直接签发 session
```

同时 `GET /api/setup` 未认证就会告诉攻击者 `needs_setup: true`。扫描到一台刚 `docker compose up`、还没打开浏览器的机器，抢先 `POST` 一次就能成为唯一管理员：读提供商密钥用途、改路由、建自己的 API key。

还有一个竞态：`admins` 表只约束 `username UNIQUE`，没有「全表只能一行」。两个不同用户名并发 setup，`AdminCount == 0` 检查会双双通过，项目自称的「恰好一个管理员」在并发下不成立。

```sql
-- migrations/0001_init.sql
CREATE TABLE admins (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    ...
);
```

**可选修法（供衡量，未实施）：**

1. 启动时在 stdout / `$DATA_DIR/setup.token` 打印一次性安装口令，setup 必须带上它。
2. 默认只绑 `127.0.0.1:3000`；compose 写成 `"127.0.0.1:3000:3000"`。
3. 用事务 + 单例约束（例如 `CHECK` / 固定 `id=1` / 独立的 `setup_lock` 行）保证最多一个管理员。

这是 WordPress / Jenkins / Grafana 都踩过的发布首日问题。

---

## 2. 应当衡量（中危）

### 2.1 登录 / 安装没有速率限制

`/api/auth/login` 和 `/api/setup` 都不限速。用户名枚举被故意做成同一句错误文案，但校验是短路的：用户不存在时不跑 bcrypt，存在时要跑约 100ms，时间和失败速率都能用来撞弱口令。最低密码长度只有 8，没有锁定。

管理口一旦被猜中，后面所有控制面防护都失效。

**可选修法：** 按 IP + 用户名做失败冷却（例如 5 次后指数退避），失败路径也跑一次空 bcrypt，避免时序差。

### 2.2 `FETCH_REMOTE_MEDIA` 的内网拦截不完整

媒体抓取整体设计是对的：默认关闭、`Dialer.Control` 防 DNS 重绑定、禁止代理、拦截 loopback / RFC1918 / 链路本地 / CGNAT `100.64/10`（含阿里云元数据 `100.100.100.200`）、限制 content-type 和大小。

但 `blocked()` 没覆盖整个 `0.0.0.0/8`，也没覆盖 IPv4-compatible IPv6（`::7f00:1`）。对照当前实现实测：

| 地址 | 是否拦截 |
|---|---|
| `127.0.0.1` / `169.254.169.254` / `10.0.0.5` / `100.100.100.200` | 拦 |
| `0.0.0.1`、`0.0.0.2`、`::ffff:0.0.0.1` | **不拦** |
| `::7f00:1`（兼容形式的 127.0.0.1） | **不拦** |

Docker 跑的是 Linux。在不少 Linux 内核上，往 `0.0.0.0/8` 发起 `connect` 会落到本机。功能默认关闭，所以不是默认攻击面，但文档把「打开后私网一律拒绝」写得很绝对，实际有缝。

**可选修法：** 把 `0.0.0.0/8`、IPv4-mapped/compatible 的回环和私网、以及 `::1/128` 以外的 IPv6 特殊段补进 `blocked()`，并加回归测试。

### 2.3 `BLOCK_PRIVATE_UPSTREAM` 比媒体抓取弱，且有 DNS 重绑定窗口

提供商出站在 `BLOCK_PRIVATE_UPSTREAM=true` 时：

- `isPrivate()` 不拦 CGNAT `100.64/10`（媒体抓取拦了）。
- 先 `LookupIPAddr`，再 `DialContext(hostname)`，DNS 会查第二次。恶意主机名可以第一次返回公网、第二次返回内网。

提供商 URL 是管理员配的，默认威胁模型里这是「操作员自己打自己」。一旦出现 1.1 那种首启接管，攻击者就是管理员，这条就变成内网探测通道。

媒体包用的 `Control` 钩子才是正确写法，这里应对齐。

### 2.4 HTTPS 反代时 Secure Cookie 不会自动打开

`SecureCookies` 只在 `PUBLIC_URL` 以 `https://` 开头，或显式 `SECURE_COOKIES=true` 时打开。README 建议前面挂 Caddy / Nginx / Cloudflare，但很多人不会设这两个变量。结果是：站点是 HTTPS，会话 cookie 仍可在一次明文 HTTP 请求里被带走。

**可选修法：** 识别 `X-Forwarded-Proto: https`（仅当 `TRUST_PROXY_HEADERS` 打开时），或默认在非 loopback 部署打印警告。

### 2.5 bcrypt 超长密码没有输入层校验

`HashPassword` 直接把用户输入交给 `golang.org/x/crypto/bcrypt`。当前版本的
`GenerateFromPassword` 不会静默截断，而是对超过 72 字节的密码返回
`ErrPasswordTooLong`；目前 setup / 改密会把它表现成 500。前端只校验最小长度。

**可选修法：** 在前后端按 UTF-8 字节数拒绝超长密码并返回 400；如果将来改为
预哈希方案，需要版本化密码哈希格式以兼容已有账号。

### 2.6 构建用的 Go 标准库有已知 CVE

**处置：已修复。** Docker、`.go-version` 和 CI 固定到 Go 1.26.6；CI 在 push、PR、
每周计划任务和手动触发时运行固定版本的 `govulncheck`。使用 Go 1.26.6 和
`govulncheck v1.7.0` 复扫结果为 0 个可达漏洞。

审查当时环境是 **Go 1.26.2**。`govulncheck ./...` 报了 11 个标准库漏洞，补丁在 1.26.3–1.26.6。对公网 HTTP 服务更相关的是：

- **GO-2026-6089** — 明文 HTTP/2 前言检查不走 `ReadHeaderTimeout`（慢速头 DoS）
- **GO-2026-4918** — 恶意 `SETTINGS_MAX_FRAME_SIZE` 可让 HTTP/2 传输死循环
- **GO-2026-6218** — `net/url` 路径解析平方复杂度（恶意 URL）

Dockerfile 是 `FROM golang:1.26-alpine`，不钉补丁版本的话，构建日当天拉到什么就是什么。

**可选修法：** 构建钉到 `1.26.6+`（或发布时的最新补丁），CI 里跑 `govulncheck`。

### 2.7 默认 API key 没有并发上限

限额都是可选的。没设 `max_concurrent` 的 key 可以开无限条长连接（上游超时默认 10 分钟）。一把泄露或被滥用的 key 就能把进程和上游额度打满。

**可选修法：** 给一个保守默认并发（例如 32），或在创建 key 的 UI 里强制选一个。

---

## 3. 低危 / 加固项

| 项 | 说明 |
|---|---|
| 缺少 CSP / HSTS / `Permissions-Policy` | 已有 `X-Content-Type-Options`、`X-Frame-Options: DENY`、`Referrer-Policy`。建议补 `Content-Security-Policy`（至少 `default-src 'self'` + 必要的 `style-src`）和反代层 HSTS。 |
| 认证 API 没有 `Cache-Control: no-store` | `GET /v1/models`、`GET /v1beta/models`（Gemini 还支持 `?key=`）以及 `/api/*` 若被中间缓存，可能把模型列表甚至带 key 的 URL 缓存出去。SSE 已经有 `no-cache`。 |
| 登出没有 CSRF | `POST /api/auth/logout` 在 `Admin` 中间件外面。现代浏览器靠 `SameSite=Lax` 能挡大部分，仍可被用来强制登出。 |
| 会话 30 天、无空闲过期 | CSRF cookie 也不绑定 session。对自托管控制台偏松。改密会清掉全部会话，这一点是对的。 |
| `POLYGLOT_SECRET_KEY` 用 SHA-256 派生 | 不是 Argon2/scrypt。环境变量里请放高熵随机数，不要用口令句。 |
| 用户名无最大长度 | 管理 API 体限制 4MiB，极端情况下可写入巨大用户名。 |
| 过期会话只在启动时清理 | `PurgeExpiredSessions` 只在 `main` 里调用一次，长驻进程会留下过期行。 |
| `redact()` 只做明文精确替换 | 短于 8 字符的密钥不处理；URL 编码 / 截断形态不会被擦。提供商密钥请只用 API Key 字段，不要写进 `base_url` 查询串。 |
| 管理接口把内部错误原样返回 | `handleSetup` / 若干 store 错误用 `%v` 回给浏览器，可能带出 SQLite 路径。 |
| 日志搜索的 `LIKE` | 已参数化，无注入；`%` / `_` 会当通配符。仅管理员可用。 |
| 提供商自定义头 | 创建时禁止了 `Authorization` / `x-api-key` / `x-goog-api-key`，但测试/发现接口没有同样校验，也可设置 `Host`、`Transfer-Encoding` 等。管理员能力，建议对齐。 |
| 无 `SECURITY.md` | 公开发布后没有漏洞披露渠道。 |

---

## 4. 已经做得很好的地方

这些值得保留，也说明项目不是「功能堆完再补安全」：

- **客户端 API key** 只存 SHA-256，明文只显示一次；高熵 token 用快速哈希是合适的。
- **管理员密码** 用 bcrypt；改密会使全部会话失效。
- **会话 token** 只存哈希；cookie `HttpOnly` + `SameSite=Lax`；写操作双提交 CSRF。
- **提供商密钥** AES-256-GCM，密钥文件 `0600`；`json:"-"`，不会进浏览器。
- **媒体 SSRF** 默认关闭，用 connect-time `Control` 而不是「先解析再拨号」。
- **`X-Forwarded-For` 默认不信任**；`/metrics` 没有 token 就 404，比较是恒定时间。
- **提供商重定向不跟随**，避免 `Authorization` 跟到别的主机。
- **SQL 全部参数化**；未发现拼接查询。
- **请求日志不存 prompt / completion**；访问日志不记 header 和 query。
- **上游错误会抹掉 API key** 再返回给客户端。
- **无 CORS**，WebUI 和 API 同源。
- **前端没有 `dangerouslySetInnerHTML`**；可见字符串走 React 转义。
- **请求体限制、ReadHeaderTimeout、非 root 容器、静态编译。** 更新检查仅访问公开
  GitHub Tags API，可通过 `UPDATE_CHECK_ENABLED=false` 关闭，不向项目作者上报。
- 协议转换走 canonical，客户端不能把任意 header 转发给上游。

---

## 5. 发布前清单（供衡量，未实施）

按优先级：

1. ✅ **已处理：** 首次安装口令 + 默认 localhost + 单管理员约束。
2. **登录失败限速。**
3. ✅ **已处理：** 构建钉到 Go 1.26.6，CI 加 `govulncheck`。
4. **收紧 `blocked()` / `isPrivate()`**，提供商拨号改成和媒体一样的 `Control` 钩子。
5. compose 不要把 `3000` 暴露到 `0.0.0.0`；README 加一节「公网部署」：反代、`PUBLIC_URL` / `SECURE_COOKIES`、`TRUST_PROXY_HEADERS`、`BLOCK_PRIVATE_UPSTREAM`、`METRICS_TOKEN`、数据目录权限。
6. 增加 `SECURITY.md`（披露邮箱或私有顾问渠道）。
7. 发布镜像前确认 `.gitignore` 已排除 `data/`、`secret.key`、`.env`（审查时已排除）。

---

## 6. 审查覆盖的主要路径

| 区域 | 主要文件 |
|---|---|
| 管理认证 / CSRF / 会话 | `internal/auth/auth.go`, `internal/auth/middleware.go`, `internal/api/admin.go` |
| API key 与配额 | `internal/auth/limits.go`, `internal/store/auth.go` |
| 凭证加密 | `internal/store/cipher.go`, `internal/store/hash.go` |
| HTTP 表面与安全头 | `internal/api/server.go`, `internal/api/http.go` |
| 媒体 SSRF | `internal/media/fetch.go`, `internal/media/fetch_test.go` |
| 提供商出站 / URL 校验 | `internal/provider/client.go`, `internal/provider/provider.go`, `internal/provider/driver.go` |
| 网关错误红行 | `internal/gateway/gateway.go` |
| 配置与 cookie | `internal/config/config.go` |
| 部署 | `Dockerfile`, `docker-compose.yml`, `README.md` |
| 前端 CSRF / XSS 面 | `web/src/lib/api.ts`, `web/src/pages/auth.tsx` |
| 依赖扫描 | `govulncheck ./...`（Go 1.26.2） |

---

*本文件仅供内部衡量，不是对外漏洞通告。是否公开、是否修复、修哪几条，由维护者决定。*
