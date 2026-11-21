# 带子路径的反向代理

BookHarbor 默认部署在 `/`，无需额外路径配置。需要部署在已有站点的 `/team/books/` 等子路径时，必须在**构建镜像时**设置 `TALEBOOK_BASE_PATH`：

```sh
docker build --build-arg TALEBOOK_BASE_PATH=/team/books \
  --target production-spa -t bookharbor:subpath .
docker run -d --name bookharbor-subpath --restart unless-stopped \
  -p 127.0.0.1:39209:80 \
  -v bookharbor-subpath-data:/data bookharbor:subpath
```

路径支持英文、数字、`-` 和 `_` 组成的单级或多级目录。空值或 `/` 表示根路径，尾斜线会归一化；完整 URL、查询参数、片段、编码路径和 `.` / `..` 不接受。

前端路由、静态资源和后端公开 URL 都在构建时确定。**不要只通过 `docker run -e TALEBOOK_BASE_PATH=...` 修改已有镜像**。源码开发时，Nuxt 与 Go 后端也必须使用相同前缀。

Readest 已集成在当前静态产物中，不再需要合并旧 PR #977。更新 Readest 时仍须通过 `scripts/readest/` 的来源清单和同步流程生成带前缀资源，不能直接复用固定根路径的第三方导出。

## Caddy

在现有域名的兜底 `handle` 之前添加：

```caddyfile
books.example.com {
    @bookharbor_root path /team/books
    redir @bookharbor_root /team/books/ 308
    handle_path /team/books/* {
        reverse_proxy 127.0.0.1:39209
    }
    handle {
        reverse_proxy 127.0.0.1:13000
    }
}
```

`handle_path` 移除应用前缀。Caddy 传递原始 Host 和 `X-Forwarded-Proto`，容器内 Nginx 再向 Go 后端传递外部协议。修改已有站点时保留 WebSocket 等既有规则。

## Nginx

将以下配置放入已有 HTTPS `server`：

```nginx
location = /team/books {
    return 308 /team/books/$is_args$args;
}
location ^~ /team/books/ {
    # 尾斜线不可省略：移除 /team/books/ 前缀。
    proxy_pass http://127.0.0.1:39209/;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_redirect off;
    client_max_body_size 100m;
    proxy_buffering off;
}
```

应用生成的跳转、API、下载、封面、阅读资源和 Cookie 已包含公开前缀。不需要 `sub_filter`、`proxy_cookie_path`，也不要把宿主根目录 `/api/` 转发给 BookHarbor。应用不根据任意请求的 `X-Forwarded-Prefix` 改变部署路径。

## 验证与兼容范围

- 首页、登录、详情、直接刷新和新标签页链接均应留在配置前缀下。
- 浏览器网络请求中的 API、`_nuxt`、封面、Readest 和其他阅读资源不应误落到宿主站点根路径。
- 登录与退出使用相同 Cookie Path；改变已有站点前缀后需要重新登录。
- OPDS 地址为 `/team/books/opds/`，WebDAV 地址为 `/team/books/books/`；社交登录服务商需要登记带前缀的回调 URL。
- 管理员自定义 HTML、外部主题硬编码 URL、显式 `static_host` 和 `opds_url_prefix` 需要由管理员同步修改，外部完整 URL 不会自动重写。
- 子路径共享浏览器 origin，不是独立安全边界；需要隔离存储或运行不受信任应用时使用不同域名。

前端路径工具回归：

```bash
cd app
npx vitest run test/utils/base-path.spec.ts
```

真实浏览器验收脚本为 `app/test/scripts/reverse-proxy-live.mjs`，只连接明确指定的隔离测试实例。不要再用旧 `tests/test_base_path.py` 或 `tests/test_reverse_proxy_integration.py`；当前后端回归位于 `booksvr/`，完整代理行为还需通过真实 Docker 镜像验证。
