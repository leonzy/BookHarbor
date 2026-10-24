# BookHarbor 开发与发布

主仓库为 https://github.com/leonzy/BookHarbor ，默认分支为 `main`。原版 Talebook 是上游参考，旧 fork 可作为 `legacy` 保留。保留提交历史与原许可证，不重写已发布历史，不向原项目强行提交 BookHarbor 的独立产品改动。

## 本地验证

遵循根目录 `AGENTS.md` 和各目录规范。后端执行 `make test-go lint-go`，镜像管理脚本执行 `python3 -m pytest tests -q`，前端在 `app` 执行 `npm ci`、`npm run lint`、`npm run generate` 及相应 Vitest / Playwright 用例。行为变化同步更新 `spec`；重要改动先建立 WIP 方案，实际验证后转 ACTIVE。

## 吸收上游

```bash
git remote add upstream https://github.com/talebook/talebook.git
git fetch upstream
git log --oneline main..upstream/master
git switch -c sync/upstream-YYYYMMDD main
```

逐项评估提交和依赖。独立的前端改动可 cherry-pick；耦合 Python 服务的后端行为移植到 `booksvr`，保留本项目的导入、来源同步、权限、分页和性能修复。冲突解决后执行相关真实后端验收。不要把整个 upstream/master 直接覆盖 main。

## 镜像与发布

主分支镜像为 `ghcr.io/leonzy/bookharbor:main`（linux/amd64）；正式版本使用日期版本标签 `vYY.MM.DD`（必要时加后缀），保留旧版本比较兼容性并构建多架构镜像。前端构建与 CI 使用 Node 24 LTS。镜像发布使用仓库的 `GITHUB_TOKEN`，不依赖原项目的 Docker Hub 凭据。工作流的手动验证默认不发布。

应用内签名升级沿用原协议与运行时校验，但下载来源和签名发布身份属于 BookHarbor。维护者已配置 `application-upgrade` 的独立 `UPGRADE_SIGNING_KEY`、`UPGRADE_KEY_ID`，以及 `application-upgrade-publish` 的 `UPGRADE_TRUSTED_KEYS` 和发布审批。私钥保存在源码外的维护目录，不进入代码仓库或镜像。公开信任配置见 `conf/bookharbor-upgrade.json`；启用应用内升级时可将它只读挂载到 `/etc/talebook-upgrade.json`。未配置受信任公钥的部署保持签名升级禁用，可使用 Docker 镜像更新。

## 兼容边界

保持 `/data`、原数据库位置、API、Go module `talebook/booksvr`、`TALEBOOK_*` 环境变量、Cookie 和版本协议名称，避免改名引发数据迁移。站点可自定义名称与 Logo；默认项目品牌为 BookHarbor。第三方包、服务地址和历史文档中的 Talebook 名称仍表示其原项目。
