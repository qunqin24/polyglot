# 前端（WebUI）

React 19、Vite 6、TypeScript（strict）、Tailwind v4、手写的 shadcn 风格组件。包管理器是 pnpm（版本由 `web/package.json` 的 `packageManager` 钉住）。构建产物输出到 `web/dist`，由 `web/embed.go` 的 `go:embed` 嵌进二进制；`web/dist` 里只有 `.gitkeep` 被提交，所以全新 clone 也能 `go build`，只是 WebUI 会显示一个提示页。

界面保持克制：它应该看起来像基础设施，不是密密麻麻的后台管理系统。一个选得好的数字胜过一张图表。

## 常用命令

在仓库根目录：

```bash
make web-deps    # pnpm install --frozen-lockfile
make web-dev     # Vite 开发服务器 :5173（/api、/health 代理到 :3000）
make web-check   # 前端测试 + tsc + eslint
make web         # web-deps + web-check + vite build
```

`pnpm --dir web run check` 等价于 test + typecheck + lint + build。`vite build` 本身不做类型检查，所以类型和 lint 要显式跑（Dockerfile 里也是先 `typecheck`、`lint` 再 `build`）。

## 目录约定

```
web/src/
  main.tsx              入口；Provider 顺序：I18n → Theme → Font → Direction → Session → Router
  routes/               TanStack Router 文件路由，文件很薄，只挂页面组件
    (auth)/sign-in.tsx   _authenticated/*.tsx   docs/$.tsx   __root.tsx
  routeTree.gen.ts      路由插件生成的文件，不手改；eslint 忽略它
  pages/                一个页面一个文件：overview、providers、models、aliases、keys、logs、pricing、settings、auth、docs
  components/
    ui/                 shadcn 风格的基础组件（button、card、dialog、select、sidebar、table…）
    kit/                Polyglot 对基础组件的封装：Dialog/Sheet/ConfirmDialog、Input/Field、useToast、
                        Badge/Select/Table/Th/Td/EmptyState/ErrorBanner、LoadingButton、图表
    layout/             侧边栏、顶栏、页面骨架；侧边栏条目在 layout/data/sidebar-data.ts
    data-table/         基于 TanStack Table 的工具栏、分页、筛选
    provider-create/    新增供应商的对话框
    （其余）            brand-icon、model-icon、model-picker、runtime-settings、content-log-settings 等共用组件
  lib/
    api.ts              类型化的管理 API client
    hooks.ts            useAsync、useInterval、errorMessage、copyToClipboard
    query-cache.ts      useAsync 背后的会话内缓存
    session.tsx         登录态（loading / setup / anonymous / signed-in）
    i18n/               en.ts、zh.ts、index.tsx
    provider-form.ts    供应商预设和表单的默认值
    timezone.ts utils.ts cookies.ts
  context/ hooks/       主题、字体、布局、方向等 Provider；通用 hook
  features/errors/      错误页
  styles/               index.css（入口）、theme.css、polyglot.css
  assets/               brand/（自带的 logo）、custom/（图标组件）
```

约定：

- 页面放 `pages/`，对应的路由文件放 `routes/`，里面只做 `createFileRoute(...)({ component: 页面 })`。新增一个需要登录的页面，路由文件放在 `routes/_authenticated/`，在 `layout/data/sidebar-data.ts` 加导航条目。
- 两空格缩进（`.tsx` 里个别文件用单引号、无分号的 shadcn 原有风格，改动时跟着所在文件走）；组件 `PascalCase`，函数 `camelCase`，页面文件名小写。
- `components/ui/` 是接近原版 shadcn 的原子组件；页面里优先用 `components/kit/` 里的封装（带了 Polyglot 的样式和行为，比如 `Input` 关掉了自动填充、`Select` 把 Radix 的 Select 包成简单的 `options` 接口）。
- 不要重型状态库。数据层就是 `useAsync` 加会话内缓存。

## TypeScript 与 lint

- `tsconfig.json`：`strict`、`noUnusedLocals`、`noUnusedParameters`、`noFallthroughCasesInSwitch` 都开着，要一直开着。路径别名 `@/*` → `src/*`。
- ESLint 9 flat config（`web/eslint.config.js`）：`typescript-eslint` 的 `recommendedTypeChecked`（带类型信息，所以会漏了 `await` 的 promise 之类的问题报出来）、React、React Hooks、React Refresh。下划线开头的未用参数/变量可以豁免。只有这一个 linter，不要加 Biome 或第二个格式化工具。
- 不许用 `any`、`@ts-ignore`、`@ts-expect-error`、`eslint-disable` 压问题，去修根因。不可信的外部数据用 `unknown` 再收窄（`providers` 页的 `parseHeaders` 是范例）。
  - 现有的例外只有这些：生成的 `routeTree.gen.ts`；几个 Provider 文件里 `react-refresh/only-export-components` 的 `eslint-disable-next-line`（`context/*-provider.tsx` 导出 hook）；`tanstack-table.d.ts` 里一处 `no-unused-vars`。新代码不要再加。
- 前端测试是两个 Node 脚本（`web/package.json` 的 `test`）：`scripts/timezone-offset.test.mjs`、`scripts/query-cache.test.mjs`，用 `typescript` 把被测的 `.ts` 现场转译后 import，不需要测试框架。

## i18n

- `web/src/lib/i18n/en.ts` 是键的唯一来源：`export const en = {...}`，`TranslationKey = keyof typeof en`，`Catalog = Record<TranslationKey, string>`。`zh.ts` 的类型是 `Catalog`，所以**少一个键或拼错一个键都是编译错误**。
- 每个用户可见的字符串都走 `t()`，不要在组件里写死文案。`const t = useT()`（只要翻译函数）或 `useI18n()`（还要 locale）。占位符用 `{name}`，`t("key", { name: value })`。
- 键按页面或功能加前缀（`providers.`、`runtime.`、`contentLogs.`、`docs.`…）；新增文案同时加进 `en.ts` 和 `zh.ts`。
- `zh.ts` 顶部注明了术语约定：协议 = protocol、供应商 = provider、别名 = alias、上游 = upstream、转换 = conversion；OpenAI、Anthropic、Gemini 这类协议名和字段名保持英文。
- locale 存在 `localStorage` 的 `polyglot-locale`，首次按 `navigator.languages` 猜（`zh` 开头用中文，其余英文），运行时缺键回退到英文。日期、数字的格式化跟随 locale（`lib/utils.ts` 的 `setFormatLocale`），时区来自实例设置（`setFormatTimeZone`，加载会话前用 UTC）。
- 文档页（`pages/docs.tsx`）不走 `t()`，它直接 `?raw` 导入仓库里的 Markdown：README、`docs/api*.md`、`docs/log-api.md`、`docs/webui-performance.md`，以及 **`AGENTS.md`（作为「开发指南」页）**。改这些文件的路径或链接时，要同步看 `docs.tsx` 里的 `internal` 链接映射。

## API client 与数据加载

- `lib/api.ts` 里导出 `api` 对象，每个管理接口一个方法，请求/响应都有类型（`Provider`、`Model`、`RuntimeSettings`…）。新增接口时先在 Go 里加 handler 和路由，再在这里加类型和方法，保持字段名和后端 JSON 一致（snake_case）。
- 底层 `request<T>(path, init)`：同源 `credentials`；非 GET/HEAD 请求从 cookie 读 CSRF token 放进 `X-CSRF-Token` 头；响应非 2xx 抛 `ApiError(status, message)`，message 取响应体的 `error` 字段；401 会清空全部缓存（`clearQueries`）。成功的写请求会让缓存全部失效（`invalidateQueries`），例外是路径以 `/test`、`/discover`、`/secret` 结尾的（它们不改数据），以及登录/登出/初始化（这三个是整个清空）。
- 读数据用 `useAsync(fn, queryKey)`（`lib/hooks.ts`）：`queryKey` 必须包含影响结果的每个筛选条件，页面之间共用同一个请求，回到页面时先显示上次的结果；查询键里如果带了翻译后的选项，也要把语言放进去。返回 `{ data, loading, error, reload }`。缓存在 `query-cache.ts`：15 秒内算新鲜，不活跃的条目保留 5 分钟，最多 100 条；显式 `reload()` 总是重新请求。会泄露的东西（payload 查看器、显示出来的密钥）不走这个缓存。
- 需要定时刷新用 `useInterval(callback, delayMs)`，页面不可见时跳过。
- 错误显示用 `errorMessage(e)` 加 kit 的 `ErrorBanner` / `useToast`。

## 图标

图标来自 `@lobehub/icons-static-svg`，只用它的纯 SVG 文件（它的 React 版 `@lobehub/icons` 会拖进 antd 和 `@lobehub/ui`，为了几个 logo 引入第二套 UI 框架不值得）。每个图标单独 import（`@lobehub/icons-static-svg/icons/<名字>.svg`，类型声明在 `assets.d.ts`），所以打包进去的只是用到的那些，不是包里的几百个。

两个组件，职责不同：

| | `brand-icon.tsx` 的 `BrandIcon` | `model-icon.tsx` 的 `ModelIcon` |
|---|---|---|
| 表示什么 | 一个**供应商/平台**的标识（OpenAI、OpenRouter、Ollama、OpenCode…） | 一个**模型**的出品方标识（Claude、Qwen、Kimi、GLM…） |
| 键 | `BRANDS` 表的 `BrandName`，由 `provider-form.ts` 的预设 `brand` 字段指定 | `markOf(id, maker)`：先用服务端给的 `maker`（models.dev 的出品方 id）查 `MAKERS`，没有再用模型 id 的正则 `RULES` 兜底 |
| 例子 | 经 Groq 访问 Llama，列表里供应商是 Groq 的标 | 同一个模型，不管谁在卖，都是 Meta 的标 |
| 没有图标时 | 预设必须有 `brand` | 显示模型名首字母 |

`maker` 来自后端的价格目录（见 [pricing.md](pricing.md)），所以模型 id 是目录里没有的（带日期的快照名、本地 Ollama tag、比目录新的模型）时才用 `RULES`。`RULES` 的顺序有意义，先匹配的赢：`deepseek-r1-distill-qwen` 是 DeepSeek 的模型，所以 DeepSeek 排在 Qwen、Llama 前面。

**图标的坑：**

- 没有自己颜色的单色标（OpenAI、Anthropic、Groq、Ollama、OpenRouter…）SVG 里写的是 `fill="currentColor"`，但 `<img>` 是独立文档，会把它解析成黑色，在深色主题上看不见。所以 `mono: true` 的标由 `SvgMark` 用 CSS `mask-image` 画成一个填充 `currentColor` 的块；彩色标直接用 `<img>`。新增图标时要选对 `mono` 还是彩色。
- **带白色主体的彩色标会在浅色背景上消失。** 彩色文件的主体形状如果是白的（为深色底设计的），就不能用它：Kimi 的彩色标把 K 画成 `#fff`，所以 `MAKERS` 里 Kimi 用的是单色文件。新增彩色标前，在浅色和深色两种主题下都看一眼。
- 图标包里没有的品牌，不要借用别人家的标：Command Code 就用了一个自带的终端图形（`assets/brand/terminal.svg`）。
- 这些图标在界面里都是装饰性的（名字就在旁边），所以 `aria-hidden`。

新增一个出品方：在 `model-icon.tsx` 加 import，在 `MAKERS` 里加 `makerId: color(...)` 或 `mono(...)`；maker id 是 models.dev 的 id（可以看 `internal/pricing/snapshot.json` 的 `makers`）。

## 开发时的联调

`make dev` 设 `POLYGLOT_DEV=true`，Go 服务器把 WebUI 请求代理到 Vite（`POLYGLOT_DEV_PROXY`），管理 API 仍由 Go 服务；Vite 自己也把 `/api`、`/health` 代理回 `127.0.0.1:3000`。所以浏览器开 `:3000` 或 `:5173` 都能用。生产构建里没有 Node。

WebUI 在高延迟线路上的加载策略见 [docs/webui-performance.md](../webui-performance.md)（会话内缓存、并发请求合并、HTTP 压缩，以及 `useAsync` 的查询键规则）。
