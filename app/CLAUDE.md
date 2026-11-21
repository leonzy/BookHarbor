# CLAUDE.md

本文件为 Claude Code 在 `app/` 目录中工作时提供指引。

## 技术栈

Nuxt 4 + Vue 3 + Vuetify 3 + Pinia + @nuxtjs/i18n，使用 TypeScript。

## 常用命令

```bash
npm ci
npm run dev          # 开发服务器，需连接真实 Docker 后端
npm run build        # SSR 生产构建，输出 .output/
npm run generate     # SPA/静态构建，输出 .output/public/
npm run lint         # eslint 检查
npm run lint:fix     # eslint 自动修复

# 常规单元和组件测试（vitest + happy-dom）
npx vitest run test/components test/composables test/plugins test/stores test/utils
npx vitest run -c test/upgrade.vitest.config.ts

# 构建后浏览器回归；配置自动管理 mock 后端和 SSR 服务
npx playwright test -c playwright.library.config.ts
```

## 架构

### API 调用方式

所有后端请求通过 `plugins/talebook.js` 中的 `backend(url, options)` 函数发起，**不要**直接使用 `fetch` 或 `useFetch`。该函数负责：
- SSR 环境下拼接 `config.public.api_url` 作为服务端地址，浏览器端使用 `window.location.origin`
- 统一处理 413/502 等错误状态码并弹出提示
- 自动转发 cookie 和 X-Forwarded-* 请求头（SSR 场景）

URL 路由在 `nuxt.config.ts` 的 `routeRules` 中将 `/api/**`、`/get/**`、`/read/**` 代理到后端，开发环境默认代理到 `http://127.0.0.1:8080`。

### 全局状态

Pinia 按领域拆分 store。`stores/main.ts`（`useMainStore`）承载站点级状态，包括：
- `user` — 当前登录用户信息（`is_login`、`is_admin`、`nickname` 等）
- `sys` — 站点配置（`socials`、`allow`、`footer` 等），由登录接口一并返回
- `alert` — 全局通知弹窗状态
- `loading` / `nav` — 页面加载状态和导航栏显示状态

独立领域状态放在对应 store：

- `stores/theme.ts` — 明/暗主题及 cookie 持久化。
- `stores/audiobook.ts` — 有声书播放器、播放队列与本地恢复状态。

### 内置阅读器 candle-reader

阅读器产物来自 npm 包 `@talebook/candle-reader`（版本在 `package.json` 精确锁定），不提交进仓库。`modules/candle-reader/` 在每次 Nuxt 启动时把包内 `dist/` 同步到 `public/static/candle-reader/`（已 gitignore），内容与锁定版本一致则跳过。联调本地阅读器时，在 candle-reader 里 `make dist install` 后用 `CANDLE_READER_SKIP_SYNC=1` 启动 Nuxt，避免被锁定版本覆盖。

### 国际化

使用 `@nuxtjs/i18n` 懒加载，语言文件在 `i18n/locales/zh-CN.json` 、`en-US.json` 和 `ru-RU.json`。默认及回退语言均为 `zh-CN`。

**注意**：SPA 静态构建时，`nuxt.config.ts` 的 `prerender:done` hook 会把三种语言补写到带构建哈希的 `_i18n/` 输出目录。修改构建流程时必须保留并验证该逻辑，否则页面会显示原始 i18n key。

### 测试

- **单元测试**：`test/components/`、`test/composables/`、`test/plugins/`、`test/stores/`、`test/utils/`，使用 Vitest + `@nuxt/test-utils`，运行环境为 `happy-dom`
- **E2E 测试**：`test/e2e/`，使用 Playwright 和 `test/mock-server.js`；`playwright.library.config.ts` 自动管理 mock 后端和构建后的 SSR 服务，其他专项配置按文件说明启动依赖

## 测试要求

**每新增一个 feature，必须添加对应的测试用例。** 根据改动类型选择测试层级：

### 组件单元测试（`test/components/`）

适用于新增或修改 `components/` 下的 Vue 组件。使用 vitest + `@vue/test-utils`，需手动挂载 Vuetify：

```typescript
import { describe, it, expect } from 'vitest';
import { mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import MyComponent from '@/components/MyComponent.vue';

const vuetify = createVuetify({ components, directives });

describe('MyComponent.vue', () => {
    it('renders correctly', () => {
        const wrapper = mount(MyComponent, {
            global: { plugins: [vuetify] },
            props: { ... }
        });
        expect(wrapper.text()).toContain('预期文字');
    });
});
```

运行：`npx vitest run test/components/`

### E2E 页面测试（`test/e2e/`）

适用于新增或修改 `pages/` 下的页面。优先在具有自动服务生命周期的 Playwright 配置中加入用例；`playwright.library.config.ts` 会启动 mock server 和 SSR 服务，并按需重置 mock 状态。测试数据在 `test/e2e/mocks/` 下的 JSON 文件中：

```typescript
import { test, expect } from '@playwright/test';

test.describe('My Page', () => {
    test.beforeEach(async ({ request }) => {
        // 重置 mock server 状态
        await request.post('http://127.0.0.1:8080/_test/reset', {
            data: { installed: true }
        });
    });

    test('displays expected content', async ({ page }) => {
        await page.goto('/my-page');
        await expect(page.getByText('预期文字')).toBeVisible();
    });
});
```

运行：`npx playwright test -c playwright.library.config.ts`。该配置要求先执行 `npm run build`，并自动管理 mock 与 SSR 服务；其他专项页面使用对应的 `playwright.*.config.ts`。
