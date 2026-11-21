# BookHarbor 插件开发指南

本文描述当前 Go 插件实现。旧 `webserver/plugins/` Python Provider、`META_PROVIDERS` 和 Python Protocol 开发方式已不适用于本仓库。

产品领域规则以 [插件平台规格](../spec/插件/插件平台.md) 为唯一来源；本文只说明代码接入、契约校验和验证方法。

## 1. 核心对象

| 对象 | 当前实现 | 职责 |
| --- | --- | --- |
| Manifest | `booksvr/internal/pluginproto/` | 声明插件身份、版本、能力、配置、权限和兼容范围 |
| Provider | `booksvr/internal/pluginiface/` | 实现一项或多项类型化能力 |
| Registry | `booksvr/internal/pluginruntime/registry.go` | 启动时校验并保存 Provider |
| Installation | `pluginruntime/install.go` | 保存实例级启用状态 |
| Connection | `pluginruntime/connection.go` | 保存实例或用户配置及加密凭据引用 |
| Run | `pluginruntime/execute.go`、`runtime.go` | 执行动作并保存审计结果 |

插件不是万能 `execute()`。启用、已配置和健康是三个独立状态，Connection 的显示名称也不是查询键。

当前插件全部内置，随 `booksvr` 二进制发布。Manifest 的 `runtime_kind` 属于协议声明，不意味着支持用户上传任意文件或远程安装插件。

## 2. 代码包边界

| 包 | 用途 |
| --- | --- |
| `pluginiface` | Metadata、Annotation、Review、Source、Transform、Push、MCP、ExtraFeature 接口及能力契约 |
| `plugindomain` | 跨插件书籍、分页、书源、批注和工具领域对象 |
| `pluginproto` | `talebook.plugin/v1` Manifest 校验和上游错误 |
| `pluginruntime` | 注册、安装、连接、调度、执行、错误脱敏和运行状态 |
| `pluginssecrets` | 凭据加密与脱敏 |
| `safehttp` | 公网地址检查、超时、主机并发与外部 HTTP 限制 |
| `pluginbuiltin` | 内置 Provider 实现和唯一注册入口 |
| `metabase` / `metasource` | 元数据 Provider 共用行为与具体来源实现 |
| `booksource` / `booksourcesearch` | 书源规则、检索和受控脚本执行 |
| `httpapi` | 管理端和用户端插件 API，不是 Provider 的存放目录 |

Provider 不应直接持有 HTTP 请求上下文或决定管理员策略。数据库写入由运行时的执行和审计边界协调，外部服务失败不能绕过任务状态与错误脱敏。

## 3. 能力与接口

当前能力到接口的权威映射是 `pluginiface.CapabilityInterfaces`，包括：

- `metadata.lookup` → `MetadataProvider`；
- `annotations.import` / `annotations.push` → `AnnotationProvider`；
- `reviews.lookup` / `reviews.import` / 章评 → `ReviewProvider`；
- `sources.search` / `sources.browse` / `sources.acquire` → `SourceProvider`；
- `integrations.tool` → `TransformProvider`；
- `integrations.push` → `PushProvider`；
- `integrations.mcp` → `MCPProvider`，使用指南见 [`document/MCP.md`](MCP.md)；
- 元数据发现、书架、统计、社区、推荐等显式额外能力 → `ExtraFeatureProvider`。

新增能力不能只修改 Manifest。必须先明确领域契约、接口、权限、运行语义、API 和规格归属；未知能力会被注册表拒绝。

## 4. Manifest 契约

`pluginproto.RequiredManifestFields` 是必填字段的权威列表，当前包括：

```text
protocol_version, id, name, version, categories, capabilities,
runtime_kind, actions, auth_schema, config_schema, connection_owners,
permissions, data_policy, compatibility, homepage, license
```

可选字段只有 `description`、`ui`、`download_mode` 和 `extra_features`。未知键、非法 ID、非语义化版本、未知能力和不兼容声明会被拒绝。

关键约束：

- `protocol_version` 使用 `talebook.plugin/v1`；这是稳定协议标识，不因品牌改名。
- `id` 使用小写点分或连字符标识，发布后保持稳定。
- `connection_owners` 只允许 `instance` 和 `user`。
- `actions` 只能使用 `test`、`preview`、`run`、`retry`、`rollback`。
- 书源必须声明与实际 `DownloadMode` 一致的 `download_mode`。
- 声明额外功能时必须实现 `ExtraFeatureProvider`。
- 凭据只进入 `Secrets`，公开序列化不返回密文。

## 5. 新增内置 Provider

### 第一步：先确定产品和权限边界

明确它解决的问题、所有者、所需权限、读取/写入数据、外部主机、超时、是否可重试和是否支持回滚。新增内置插件必须同步 `spec/插件/` 归属；有专属界面的插件还涉及前端与浏览器验证。

### 第二步：实现领域来源与类型接口

在合适的内部包实现具体协议或外部服务客户端，在 `pluginbuiltin` 中完成 Provider 适配。优先复用现有领域对象，不另造一套只用于单个插件的通用模型。

`MetadataProvider` 需要检索、按外部 ID 获取详情和按需下载封面；`SourceProvider` 按下载模式实现搜索、分类、详情、目录、正文或文件下载；`TransformProvider` 区分预览与实际写入。

### 第三步：声明 Manifest 和 CapabilitySet

Manifest 描述声明，CapabilitySet 描述 Go 对象实际实现的接口。`PluginRegistry.Register` 同时校验两者，不能只写其中一个。

启动装配调用 `pluginbuiltin.Register`，其分组顺序为：

```text
combo / source / meta / review / annotation / tool / push
```

在对应 `register*Providers` 函数中加入 Provider。不要在其他包维护第二份隐式注册表，也不要在首次 API 请求时才注册。

### 第四步：接入运行时动作

测试、预览、执行、重试和回滚必须遵守各自语义：

- 测试只验证连接和凭据；
- 预览不写业务数据；
- 执行才提交写入；
- 重试基于持久运行记录，不复制私密输入到公开响应；
- 回滚只撤销可明确追踪的写入。

外部错误应使用 `pluginproto` / `pluginruntime` 的可分类错误。写操作中断不能默认自动重试；只读连接错误也应按契约标记是否可重试。

### 第五步：处理外部资源与安全

所有用户配置 URL 和上游请求必须经过 `safehttp` 或对应专用安全客户端，遵守 HTTPS、公网 IP、端口、重定向、路径、超时、响应大小和主机并发约束。

不得：

- 把本站 Cookie、环境变量、文件系统或数据库句柄暴露给外部脚本；
- 在日志和公开错误中输出查询内容、凭据、内部路径或完整上游响应；
- 根据扩展名信任下载文件；
- 让不可信压缩包绕过条目、路径、展开大小和压缩比预算；
- 用管理员连接读取或替换普通用户的私人连接。

Venera 当前使用同进程 `goja`、超时中断和调用栈限制，不再是旧文档所述的独立 QuickJS 进程。Legado 的字段后处理同样使用受限 `goja`，不支持任意 `java.ajax` 副作用。

## 6. 测试要求

至少覆盖：

1. Manifest 合法、缺字段、未知字段和未知能力；
2. CapabilitySet 与 Provider 实现一致性；
3. 实例/用户连接所有权和越权访问；
4. 测试、预览、执行、重试和回滚的状态及写入差异；
5. 凭据加密、公开序列化和错误脱敏；
6. 超时、限流、部分失败、无效响应和上游中断；
7. 文件/压缩包安全预算及临时文件清理；
8. 实际注册表包含新插件，且 `spec/插件/` 有对应归属。

常用命令：

```bash
cd booksvr
go test ./internal/pluginiface ./internal/pluginproto ./internal/pluginruntime ./internal/pluginbuiltin
go test ./internal/httpapi
go test ./...
```

有前端消费者时还需组件测试、对应 Playwright 场景和真实后端界面审查。真实上游验证必须显式进行，不能用 mock 结果声称外部服务当前可用。

## 7. 评审清单

- 是否把启用、配置和健康混为一谈？
- 是否使用连接显示名而不是稳定 `role` 定位配置？
- 是否增加了未声明的通用执行入口？
- 是否让管理员看到其他用户的私人连接、凭据或运行输入？
- 是否对写操作进行不确定的自动重试？
- 是否绕过 `safehttp`、资源预算或错误脱敏？
- 是否忘记启动注册、持久运行记录、规格归属和对应测试？
