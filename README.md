<p align="center"><img src="app/public/brand/bookharbor.svg" width="96" alt="BookHarbor"></p>

# BookHarbor

中文 | [English](README_EN.md)

BookHarbor 是面向个人和家庭的自托管电子书库，采用 **Go 后端、Nuxt 4 / Vue 3 前端**，提供书籍管理、在线阅读与阅读设备推送。

本项目基于 [Talebook](https://github.com/talebook/talebook) 独立维护，保留原项目的提交历史与 BSD-2-Clause 许可证。Calibre 作为外部格式处理工具，Python 仅用于镜像管理与 Calibre 相关工具，不再运行 Python HTTP 后端。

## 功能

- 多个来源目录自动扫描，默认仅索引原文件；后台全量导入与 ZIP 书籍导入。
- 文件删除、改名与移动同步，挂载掉线保护，来源缺失状态与回收站。
- 书库、分类、目录树浏览；按文件大小排序，筛选和页码跳转。
- 在线阅读、格式转换、书架、阅读记录、私有书目与多用户权限。
- OPDS、元数据插件及 BOOX、Kindle、WebDAV 等设备或服务连接。
- 中文、英文、俄文界面，浅色和深色主题。

## Docker 部署

镜像：`ghcr.io/leonzy/bookharbor:main`（主分支）、正式版本标签（发布后可用）。仅使用你有权访问和管理的图书。

```bash
git clone https://github.com/leonzy/BookHarbor.git
cd BookHarbor
docker compose up -d
```

首次部署访问 `http://localhost:8080`，完成管理员初始化。`docker-compose.yml` 中的服务名 `talebook` 与 `TALEBOOK_DATA_DIR` 环境变量保留，以兼容已有部署。

已有用户继续将原数据目录挂载到 `/data`，源书籍目录分别挂载为 `/books`、`/books1` 等，再在导入设置填写这些容器内路径。Calibre 书库仍默认位于 `/data/books/library`；不会因项目改名迁移数据库或复制源书籍。自定义站点名称和设置保留。替换镜像前备份数据库和设置，避免两个容器同时写入同一数据目录。

## 站点名称与标识

新安装默认使用 BookHarbor 名称和标识。管理员可在常规设置中修改网站标题、页头与侧栏自定义 HTML；已有站点的自定义设置会保留。需要自定义侧栏图片时，将图片放在持久化数据目录的 `books/logo/` 下，并在侧栏 HTML 中使用 `/logo/文件名`；该目录在容器内为 `/data/books/logo/`，更新镜像不会覆盖已有图片。

## 开发与上游同步

```bash
git remote add upstream https://github.com/talebook/talebook.git
git fetch upstream
git log --oneline main..upstream/master
```

`origin` 指向 BookHarbor；Talebook 为 `upstream`，按需选择性吸收改动。前端改动可以按依赖评估后迁入；后端行为适配到 Go，保留本项目已有修复，不重新引入 Python HTTP 服务。完整开发、发布及兼容约定见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 项目与致谢

- [问题反馈](https://github.com/leonzy/BookHarbor/issues) · [版本发布](https://github.com/leonzy/BookHarbor/releases) · [容器镜像](https://github.com/leonzy/BookHarbor/pkgs/container/bookharbor)
- 原项目：[Talebook](https://github.com/talebook/talebook)，原作者及贡献者版权保留。
- 依赖 [Calibre](https://calibre-ebook.com/)、[candle-reader](https://github.com/talebook/candle-reader) 等独立项目；第三方许可见 [document/licenses](document/licenses)。
- 许可证：[BSD-2-Clause](LICENSE)。
