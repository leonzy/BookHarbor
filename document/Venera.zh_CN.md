# 在 BookHarbor 阅读 Venera 漫画源

管理员在「书库浏览 → 网络书库」打开 **Venera 漫画源**插件，启用插件，在「全局配置」保存 MangaDex 连接并执行「测试连接」。读者登录后可浏览最新、热门和最近更新，或搜索漫画；打开详情、选择章节后使用本站漫画阅读器翻页。

公开阅读无需 MangaDex 账号。配置支持标题语言、原图或压缩图、搜索内容分级及连接超时。默认搜索 safe 内容；浏览分类沿用官方源行为，不作为内容分级过滤。默认超时 20 秒，网络较慢时可调至 60 秒。未启用、连接不兼容、网络失败或权限不足会显示错误，详情和阅读器提供重试。

可选统计接口失败时，官方脚本可降级显示；必需接口失败仍明确报错。源脚本不能绕过宿主网络策略和资源限制。SPA、SSR 和开发代理均精确处理无尾斜线的 `/read-online-comic`；子路径部署见 [反向代理说明](reverse-proxy.md)。

## 接入机制与兼容范围

Venera 源是客户端 JavaScript 扩展，官方无头模式没有可直接对接的漫画 HTTP 服务。本插件运行固定版本并校验 SHA-256 的官方源脚本，通过受限宿主 API 映射到 BookHarbor 的 `SourceProvider`。

- 当前仅支持官方 **MangaDex v1.2.0**，固定配置提交 `d8a71168a83a6797482a5be9c000989dabf21c08`，SHA-256 为 `c8beb84262da8fa3922ff0392fc2f79f03ee36ae08933d10571172ef6c8ce21d`。首次使用从固定 GitHub 地址获取并校验脚本，缓存在服务进程内，重启后重新获取。
- 服务需要访问固定源脚本地址、`api.mangadex.org`、`uploads.mangadex.org` 和允许的 MangaDex 图片节点。请求必须经过 `safehttp` 的协议、地址、端口、路径、响应大小和超时约束。
- 章节保留官方脚本的卷、翻译语言和条目顺序。单次源操作至多 40 次请求，章节至多 1,000 页，源结果和上游响应有大小限制；超大目录可能超出兼容范围。
- 当前 Go 实现使用同进程 `goja`，通过超时中断和最大调用栈限制执行固定脚本。`goja` 没有硬内存上限 API，因此它不是通用不可信脚本沙箱；不要开放任意脚本上传或替换固定哈希校验。
- 宿主不向源脚本传入本站账号、凭据、文件系统、环境变量或数据库句柄，源网络会话不继承环境代理、认证头或 Cookie。
- 封面和页面使用同站代理，凭证绑定当前读者与源，有效期 15 分钟。每次读取重新检查登录、账号激活、阅读权限、源启用状态和连接 scopes。
- 在线漫画不保存到本地书库，也不持久保存阅读进度。论坛 WebView 登录、收藏、评论、DRM 图片变换和其他 Venera 源尚未支持。

旧 Talebook PR #1073 的客户端导出方向未合并；当前能力是 BookHarbor 服务端接收固定 Venera 源。

## 验证

当前自动化位于 Go 和前端测试目录：

```bash
cd booksvr
go test ./internal/booksource
go test ./internal/httpapi
go test ./internal/pluginbuiltin
```

`booksvr/internal/booksource/venera*_test.go` 覆盖分页、顺序、脚本隔离、网络白名单和本地模拟上游；`network_comic` 与 `network_library` 测试覆盖真实 Go 插件运行时、页面接口和图片权限。前端隔离状态用例位于 `app/test/e2e/venera.spec.ts`，应由短生命周期 Playwright/mock 流程运行，不能当作真实 MangaDex 可用性证明。

旧 `tests/test_venera_source.py`、`tests/test_network_comic.py`、`tests/test_venera_deployment.py` 和 `scripts/venera_live_server.py` 已不适用于当前 Go 后端。真实上游复验需要另行启动隔离 Docker 书库和 Nuxt 页面，使用临时管理员与连接，检查搜索、详情、章节和图片；不要修改生产书库，也不要提交访问文件、Cookie 或临时凭据。

`document/screenshots/tb-236/` 中的报告与截图是固定时间的历史验证证据，不代表当前上游内容、节点或可用性。机制依据为 [Venera 源文档](https://github.com/venera-app/venera/blob/master/doc/comic_source.md) 和 [固定版本 MangaDex 脚本](https://github.com/venera-app/venera-configs/blob/d8a71168a83a6797482a5be9c000989dabf21c08/manga_dex.js)。
