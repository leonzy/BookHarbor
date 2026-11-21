# BookHarbor 开发指南

中文 | [English](Development.en_US.md)

本文说明当前 Go + Nuxt 代码库的开发方式。旧 `webserver/` Python/Tornado HTTP 服务已删除；如果本地仍存在该目录，它只是旧运行留下的空目录或缓存，不是后端代码入口，也不要向其中添加新后端代码。

## 项目结构

```text
BookHarbor/
  app/          # Nuxt 4 + Vue 3 + Vuetify + Pinia
  booksvr/      # 唯一 Go HTTP 后端及维护 CLI
  tests/        # 镜像管理、启动和签名升级脚本测试
  conf/         # Nginx、Supervisor 与升级信任配置
  docker/       # 容器启动、自检和状态页
  resources/    # 阅读模板、内置书源与运行时资源
  scripts/      # 工程检查、部署、迁移和阅读器工具
  spec/         # 当前领域规则
  design/       # 内部方案与历史决策
  document/     # 使用和开发文档
```

代码导航见 [CODE_WIKI](../CODE_WIKI.md)，贡献和发布约定见 [CONTRIBUTING](../CONTRIBUTING.md)。

## 推荐开发环境

- Linux 或可用的 Docker Desktop 环境；
- Docker Compose；
- Node.js 24；
- Go 版本以 `booksvr/go.mod` 为准；
- Python 3.11+，仅用于镜像与工程脚本。

### 1. 启动真实后端

在仓库根目录执行：

```bash
make dev
curl --fail --silent http://127.0.0.1:8080/api/welcome
```

`make dev` 构建并启动开发镜像。开发 Compose 将前端目录挂载到容器，但 Go 源码不会自动热重载；修改 `booksvr/` 后需重新构建开发镜像并重启容器。不要按旧文档期待 Python 自动重启。

开发数据默认位于 `/tmp/talebook-dev`。它是隔离开发目录，不应指向唯一生产书库。

### 2. 启动 Nuxt

```bash
cd app
npm ci
API_URL=http://127.0.0.1:8080 npm run dev -- --port 3000 --host 127.0.0.1
```

访问 `http://127.0.0.1:3000/`。必须确认后端 `/api/welcome` 可用后再启动前端，不得把 mock API 地址作为本地真实体验环境。

如果热更新异常，停止服务并排查原生文件事件；禁止通过 `CHOKIDAR_USEPOLLING=true` 规避。

## 不启动前端开发服务的后端开发

Go 后端可直接构建和测试：

```bash
make build-go
make test-go
make lint-go
```

生成的二进制位于 `booksvr/bin/booksvr`。直接运行 `serve` 需要现有 Calibre `metadata.db`、业务库和 Cookie 密钥等参数，完整生产装配以 `Dockerfile`、`docker/start.sh` 和 Supervisor 配置为准。不要用旧 `server.py --syncdb` 或 `webserver/main.py` 命令初始化当前系统。

## 测试与质量门禁

### Go

```bash
make test-go
make lint-go
```

后端新增或修改行为时，在对应 `booksvr/internal/<package>/` 添加 `_test.go`。

### Python 镜像脚本

```bash
python3 -m pytest tests -q
make lint-py
```

签名升级执行器要求信任配置由 root 持有，完整测试应在 Linux root 或项目 CI 环境执行。Windows 上缺少 `fcntl` 导致的收集失败不代表 Linux 测试通过。

### 前端

```bash
cd app
npm ci
npm run lint
npx vitest run test/components test/composables test/plugins test/stores test/utils
npx vitest run -c test/upgrade.vitest.config.ts
npm run build
npx playwright install chromium
npx playwright test -c playwright.library.config.ts
```

专项 Playwright 配置用于有声书、封面和其他页面。它们大多使用短生命周期 mock API，只用于隔离界面回归；真实登录、文件、权限和阅读链路还需 Docker 验收。

### 文档

```bash
make check-spec
make check-design
make check-i18n
```

产品行为变化必须同步 `spec/`。重要改动先建立唯一的 WIP 单文件 HTML 方案，验证完成后回写真实结果并转 ACTIVE。历史方案、调研和固定提交的验收记录不应当被改成“当前指南”。

## 常用开发边界

- 前端 API 调用统一经过 `app/plugins/talebook.js`。
- Go 代码区分 Calibre 书目库与 BookHarbor 业务库，不能混用句柄。
- 任何书籍、封面、下载、媒体、OPDS 或 WebDAV 新入口都要执行资源可见性校验。
- Python 配置 `auto.py` / `manual.py` 按数据解析，不作为 Python 服务代码执行。
- 保留 `/data`、旧 API、Go module、Cookie、`TALEBOOK_*` 和插件协议标识的兼容性。
- 第三方静态资源和阅读器产物需要保留来源、固定版本、许可证和同步记录。

## 故障排查

容器启动状态由 `/status.json` 和状态页展示。常见检查：

```bash
docker compose -f docker-compose.dev.yml logs
docker compose -f docker-compose.dev.yml ps
```

重点检查 `/data` 权限、PUID/PGID、Nginx 配置、数据库初始化/迁移和 `booksvr` 日志 `/data/log/talebook.log`。不要根据旧文档查找 `talebook:tornado-8000` 进程；当前应用进程为 Go `booksvr` 及明确的辅助 worker。

仍无法定位时，提交 Issue 应包含版本/提交、部署方式、相关启动状态和脱敏日志，不要上传真实数据库、密钥或私人书籍。
