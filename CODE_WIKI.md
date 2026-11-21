# BookHarbor Code Wiki

本文是当前代码的导航与架构入口，不重复产品领域规则。产品行为以 [`spec/索引.md`](spec/索引.md) 为准，具体改动的历史论证以 `design/` 为准。

## 1. 当前架构

BookHarbor 是面向个人和家庭的自托管书库。Go `booksvr` 提供全部应用 HTTP 接口，Nuxt 4 / Vue 3 提供 Web 界面，Calibre 作为外部格式工具。Python 只用于镜像启动、自检、签名升级和工程脚本，不存在 Python HTTP 后端。

```text
Browser / OPDS / WebDAV / device / MCP Agent
              |
           Nginx
        |           |
 static SPA/SSR     +---- booksvr :8000
                            |
          +-----------------+------------------+
          |                 |                  |
   Calibre metadata.db  calibre-webserver.db  source files
          |                 |                  |
   works/books/formats  users/plugins/tasks  scan/watch/read
```

生产 SPA 由 Nginx 直接托管；SSR 镜像还运行 Nuxt 服务。`/api/`、`/get/`、`/read/`、`/auth/`、`/opds/`、`/books/`、`/mcp` 和媒体路径由 Go 后端处理。开发环境的 Nuxt 代理见 `app/nuxt.config.ts`。

## 2. 技术栈

| 层 | 技术与入口 |
| --- | --- |
| 前端 | Nuxt 4、Vue 3、Vuetify、Pinia、vue-i18n；`app/` |
| 后端 | Go 1.26 module `talebook/booksvr`；`booksvr/` |
| 数据库 | SQLite，驱动 `modernc.org/sqlite` |
| 外部执行 | Calibre CLI、Voicebook、固定版本下载器 |
| 脚本 | Python 3.11+；`docker/`、`scripts/upgrade/` |
| 部署 | Docker、Nginx、Supervisor；`Dockerfile`、`conf/` |

Go module、部分环境变量、Cookie、插件 ID 和 Compose 服务名保留 `talebook` 字样，是兼容契约，不表示仍运行 Talebook Python 服务。

## 3. 目录导航

| 路径 | 职责 |
| --- | --- |
| `app/pages/` | Nuxt 文件路由页面 |
| `app/components/` | 书卡、详情操作、导入、插件、播放器等组件 |
| `app/stores/` | 站点、主题和有声书播放状态 |
| `app/plugins/talebook.js` | 统一 JSON/NDJSON 后端请求和错误信封处理 |
| `app/modules/` | candle-reader 等构建期集成 |
| `booksvr/cmd/booksvr/` | CLI、启动参数、服务装配、维护子命令 |
| `booksvr/internal/httpapi/` | 路由、认证、HTTP 处理器和资源响应 |
| `booksvr/internal/library/` | 书库模型、查询、作品身份、访问范围投影 |
| `booksvr/internal/access/` | 目录/分类/用户组访问策略的纯规则编译 |
| `booksvr/internal/userdb/` | 用户、阅读状态、插件和业务库模型 |
| `booksvr/internal/scan/` | 来源扫描、校验、导入和去重 |
| `booksvr/internal/importdir/` | 导入目录与允许根目录约束 |
| `booksvr/internal/pluginruntime/` | 插件安装、连接、运行、审计与调度 |
| `booksvr/internal/pluginiface/` | 类型化 Provider 能力接口 |
| `booksvr/internal/pluginbuiltin/` | 全部内置插件的唯一装配入口 |
| `booksvr/internal/mcp/` | MCP JSON-RPC、工具、授权、确认和进程内任务 |
| `booksvr/internal/safehttp/` | 外部 HTTP 的地址、并发和资源约束 |
| `resources/` | 阅读模板、内置书源和运行时资源 |
| `docker/` | 容器启动、自检和状态页 |
| `scripts/` | 工程检查、部署、迁移和阅读器同步工具 |
| `tests/` | 镜像管理和签名升级脚本测试 |
| `spec/` | 当前产品规则的唯一领域规格 |
| `design/` | 带 WIP/ACTIVE/SUPERSEDED 生命周期的内部方案 |
| `document/` | 面向使用者和开发者的现行指南及明确标记的历史证据 |

## 4. 后端启动与请求处理

### 启动

`booksvr/cmd/booksvr/main.go` 提供 `bootstrap`、`serve` 和只读检查/修复子命令。容器先通过自检初始化业务数据库与配置，再启动主服务。

`serve` 的参数优先级为命令行、环境变量、默认值。`booksvr/cmd/booksvr/wiring.go` 构造扫描、文件监听、漫画和插件服务，并显式区分：

- `libraryDB`：Calibre 书目、作者、作品和格式；
- `userDB`：账号、阅读状态、扫描记录、插件连接和运行记录。

主服务监听就绪前，启动流程会处理 SQLite 写锁重试；不能将就绪日志当作外部健康检查的替代品。

### 路由和错误

`booksvr/internal/httpapi/routes.go` 是旧 URL 兼容目录和原生作品/目录路由的注册表。正则顺序是契约，具体路由必须排在通配路由之前。

处理器使用 `HandlerFunc` 和请求 `Context`。JSON 接口通常返回 `{"err":"ok", ...}`；认证、安装、权限和业务失败也使用既有错误信封。OPDS、媒体流、WebDAV 和 MCP 可按各自协议使用 HTTP 状态码与响应格式，但不能绕过资源权限。

`app/plugins/talebook.js` 是前端唯一通用请求入口，负责 SSR Cookie/转发头、登录浮窗、安装跳转、413/502 提示和版本变化刷新。新增页面不要绕过它直接调用后端 JSON API。

## 5. 数据模型与权限

### 作品、版本、格式

`booksvr/internal/library/schema.go` 以幂等方式扩展 Calibre 派生库：

- `works`：归一化书名和作者形成的作品聚合；
- `books`：版本层，保留旧书号并增加 `work_id`、`edition_key` 等字段；
- `book_formats`：实际文件路径、大小、时间和哈希。

默认来源导入只登记原文件，不复制源书。复制和移动导入是显式模式。删除、来源缺失、回收站和挂载保护的产品规则见 `spec/书库.md` 与 `spec/书籍.md`。

### 用户和访问范围

权限不是单一的“登录/管理员”判断：

1. 账号能力与激活状态决定能否读、写、上传或管理；
2. 收藏者和公开/私藏决定书籍归属与基础可见性；
3. 用户、用户组、目录和权限分类形成访问范围；
4. 文件来源和派生关系保留权限继承，移动文件不能扩大受众。

纯策略编译位于 `booksvr/internal/access/`；SQL 投影和查询位于 `library` 与 `userdb`；HTTP 层必须对所有入口执行相同授权，包括列表、搜索、封面、下载、阅读、OPDS、WebDAV 和媒体流。

## 6. 扫描、后台任务与插件

### 扫描和导入

`scan` 负责目录遍历、格式与容器校验、元数据提取、查重和导入。完整来源核对使用独立子进程，主服务处理增量事件和文件监听。持久任务、断点和暂停状态保存在业务库中。

不可信 EPUB/ZIP/漫画容器必须遵守路径、加密、条目数、展开大小和压缩比预算。格式扩展名本身不是安全校验。

### 插件

插件开发见 [`document/PluginGuide.md`](document/PluginGuide.md)。核心边界为：

- Manifest 声明身份、能力、配置和权限；
- Provider 实现 `pluginiface` 中的类型接口；
- Connection 保存实例或用户自己的配置与加密凭据；
- Run 保存测试、预览、执行、重试、回滚的审计结果。

所有内置 Provider 在 `pluginbuiltin.Register` 集中注册，启动时校验 Manifest 与能力契约。外部请求通过受限运行时执行，不能把查询、凭据或内部路径泄漏到公开错误中。

MCP 使用 `talebook.combo.mcp` 插件与 `integrations.mcp` 类型化能力。`/mcp` 和 `booksvr mcp` 只是传输入口，共用 `internal/mcp` 的工具、权限与任务实现；写操作通过同一运行时审计。启用和调用示例见 [`document/MCP.md`](document/MCP.md)，产品规则见 [`spec/插件/MCP.md`](spec/插件/MCP.md)。

## 7. 前端和阅读器

Pinia store 按站点、主题和有声书拆分。i18n 语言文件为 `app/i18n/locales/zh-CN.json`、`en-US.json` 和 `ru-RU.json`。

阅读器有不同交付方式：

- candle-reader 由精确版本 npm 包同步到静态目录；
- Readest 是固定上游来源加宿主补丁的导出产物，由 `scripts/readest/` 维护；
- PDF、漫画和旧阅读器的静态资源位于 `app/public/`；
- 阅读格式选择由后端阅读入口决定，前端不能仅凭扩展名自行推断可读性。

SPA 静态构建需要保留 `_i18n` 输出逻辑。修改阅读器产物时必须同步来源清单、版本和许可证，不直接编辑压缩后的第三方 bundle。

## 8. 构建、测试和 CI

常用命令：

```bash
make test-go lint-go
python3 -m pytest tests -q
make lint-py
make check-spec
make check-design
make check-i18n
cd app && npm ci && npm run lint
cd app && npx vitest run test/components test/composables test/plugins test/stores test/utils
cd app && npm run build
cd app && npx playwright test -c playwright.library.config.ts
```

镜像管理测试中的升级信任配置要求 Linux root 环境。前端 Playwright 使用 mock API 做隔离回归，不能替代真实 Docker 后端验收；镜像构建 workflow 另对首页和 `/api/welcome` 做真实容器冒烟。

GitHub Actions 分为：

- `ci.yml`：规格、方案、Go、Python、前端单测、构建、浏览器和依赖检查；
- `build.yml`：SPA/SSR/dev 镜像、多架构发布和容器冒烟；
- `application-upgrade.yml`：固定提交的应用包签名与发布。

修改 workflow 后还必须按 `.github/workflows/AGENTS.md` 使用真实 workflow 和匹配事件执行 `act`；静态 YAML 检查不能替代。

## 9. 文档维护边界

- 安装和基本使用：根目录 README 与 `document/README.*.md`。
- 开发环境：`document/Development.*.md` 和 `CONTRIBUTING.md`。
- 当前代码结构：本文。
- 产品行为：`spec/`，不在其他文档复制一份规则。
- 某次变更的历史：`design/`、`plans/`、`research/` 和明确标记的验收记录。
- 第三方静态包、许可证和上游文档：保留原项目名称与历史归属，不为“改名”改写事实。
