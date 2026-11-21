# BookHarbor 前端

`app/` 是 BookHarbor 的 Nuxt 4 前端，使用 Vue 3、Vuetify、Pinia 和 TypeScript。Go 后端位于 `../booksvr`，Python 不提供应用 HTTP 服务。

## 安装与构建

使用 Node.js 24 和 npm lockfile：

```bash
cd app
npm ci
npm run dev        # 开发服务
npm run build      # SSR 构建，输出 .output/
npm run generate   # SPA 静态构建
npm run preview    # 预览 SSR 构建
npm run lint       # ESLint
```

`API_URL` 默认是 `http://127.0.0.1:8080`。SSR 和静态生产部署通常由仓库根目录的 Docker 镜像组装，不需要在容器内另装一套前端依赖。

## 连接真实后端开发

前端开发必须连接真实 Docker 后端，不能把 mock API 当成本地体验或实际验收环境。

```bash
# 在仓库根目录启动后端
make dev

# 确认后端可用
curl --fail --silent http://127.0.0.1:8080/api/welcome

# 在 app 目录启动前端
API_URL=http://127.0.0.1:8080 npm run dev -- --port 3000 --host 127.0.0.1
```

访问 `http://127.0.0.1:3000/`。Go 源码变化需要重建 dev 镜像；前端目录挂载在容器中，但 Nuxt 开发服务按上述方式在宿主运行。不要使用 Chokidar polling 绕过文件监听问题。

## 代码约定

- JSON 和 NDJSON 请求统一使用 `plugins/talebook.js` 提供的 `$backend` / `$backend_stream`，不要在页面直接请求 `/api`。
- 站点状态在 `stores/main.ts`，主题和有声书分别有独立 store。
- 语言文件位于 `i18n/locales/`，支持中文、英文和俄文。
- candle-reader 使用 `package.json` 中的精确版本，由 `modules/candle-reader/` 同步。
- Readest 和其他固定第三方产物不能手工修改压缩 bundle，按对应来源清单和同步脚本更新。

## 测试

```bash
# 全部常规 Vitest 单元/组件测试
npx vitest run test/components test/composables test/plugins test/stores test/utils

# 使用独立配置的升级组件测试
npx vitest run -c test/upgrade.vitest.config.ts

# 构建后运行 SSR + mock 后端浏览器回归
npm run build
npx playwright install chromium
npx playwright test -c playwright.library.config.ts
```

`playwright.library.config.ts` 会自行启动和停止 mock 后端与 SSR 服务。其他专项 Playwright 配置用于有声书、封面和文档等场景，应按各配置要求运行。

mock 测试只证明隔离界面行为。涉及真实登录、书库、封面、文件、阅读和权限时，还需要 Docker 后端与真实数据验收。完整约束见根目录 `AGENTS.md` 和 `app/CLAUDE.md`。
