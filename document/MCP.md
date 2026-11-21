# BookHarbor MCP 使用指南

BookHarbor MCP 是内置插件，让 Agent 在当前读者的权限范围内查书、查看图书详情、读取阅读记录、推荐书籍，并在明确确认后编辑元数据、推送或导入书籍。它编译进现有 `booksvr` 二进制，与 BookHarbor 运行在同一容器和进程中，不新增 Compose 服务、MCP 镜像或监听端口。

## 1. 先选一种接法

先在 BookHarbor 的「管理 → 插件中心」启用 **BookHarbor MCP**。

| Agent 在哪里运行 | 客户端要填什么 |
| --- | --- |
| 另一台电脑，或客户端只支持 URL | HTTP 地址与个人令牌 |
| 可以执行 BookHarbor 宿主机上的 Docker 命令 | stdio 命令与 BookHarbor 登录用户名 |

### HTTP

1. 用要授权给 Agent 的账号登录。在「个人插件设置 → BookHarbor MCP」打开个人 MCP 配置。管理员也可在插件中心的卡片打开同一页面。
2. 点击「生成我的令牌」，系统为当前账号生成并保存。确认页面显示的是自己的登录账号。
3. 点击「复制 Agent 配置」粘贴进客户端。若客户端使用表单，传输类型选 **Streamable HTTP**，地址复制页面上的 MCP URL，认证选 **Bearer** 并粘贴令牌。也可使用请求头 `Authorization: Bearer 你的令牌`。

再次打开时点击「显示我的令牌」。点击「重新生成」会让旧令牌立即失效。
通用 JSON 配置为：

```json
{
  "mcpServers": {
    "bookharbor": {
      "type": "http",
      "url": "http://192.168.1.96:8850/mcp",
      "headers": {
        "Authorization": "Bearer 替换为页面生成的个人令牌"
      }
    }
  }
}
```

HTTP 连接默认只读。要编辑、推送或导入，在同一条连接打开「允许 Agent 编辑、推送和导入图书」。客户端会先预检，再请你确认。

### stdio

Agent 必须能执行宿主机的 Docker 命令。将 `admin` 改为你的 BookHarbor 登录用户名，将 `bookharbor` 改为实际容器名：

```json
{
  "mcpServers": {
    "bookharbor": {
      "command": "docker",
      "args": ["exec", "-i", "-u", "talebook", "bookharbor", "booksvr", "mcp", "--user", "admin"]
    }
  }
}
```

stdio 使用本地 Docker 执行权限，按用户名解析身份，默认只读。需要编辑、推送或导入时，在 `args` 末尾添加 `"--allow-write"`。标准容器会复用主服务已初始化的书库、权限索引和配置文件。自定义部署可覆盖 `--db`、`--user-db`、`--library-root` 和 `--settings-file`，也可用 `--user-id` 代替用户名。

### 确认连通

在 Agent 中请求「用 BookHarbor 搜索三体」，或者刷新客户端的 MCP 工具列表。连接成功后会看到 `book_search`、`book_detail` 和 `book_recommend`。HTTP 401 时检查令牌是否过期或已重新生成，以及插件是否启用。stdio 报错时检查 Docker 容器名和 BookHarbor 用户名。

设备管理使用 `device_list` 查看当前账号的设备编号，通过 `device_manage` 预检并确认新增、修改或删除。发送时在 `book_send` 的预检参数中提供 `book_id`、`device_id` 和可选 `format`，再按预检返回的操作标识确认。修改和删除需完整设备信息，避免覆盖其他设备。

## 2. 编辑、推送与导入的两阶段确认

`book_update`、`book_send` 和 `book_import` 只在连接开启 `allow_write` 后出现在工具列表中。先预检，以下示例不会推送：

```json
{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"book_send","arguments":{"book_id":1,"device_id":1,"format":"EPUB","phase":"prepare"}}}
```

检查返回的书名、格式、设备与目标后，使用返回的 `operation_id` 执行。`idempotency_key` 由客户端生成，同一读者和工具内必须唯一：

```json
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"book_send","arguments":{"operation_id":"替换为预检返回的标识","phase":"execute","confirm":true,"idempotency_key":"client-generated-unique-key"}}}
```

在线导入使用相同流程，预检参数为：

```json
{"source_key":"legado:1","book_url":"书源返回的书籍外部标识","format":"","clean":true,"phase":"prepare"}
```

这里不接受本地文件路径或任意下载 URL；书源必须来自当前读者可用的既有插件连接。执行成功后，用返回的 `task_id` 调用 `task_status`。任务仅进程内保存，重启后不能查询；任务消失不代表已经回滚。

管理员或私藏书所有者可用 `book_update` 提交元数据更改。预检示例为
`{"book_id":1,"title":"校正后的书名","authors":["作者"],"phase":"prepare"}`。
`title` 必填。可选字段为 `authors`、`tags`、`publisher`、`series`、`isbn`。省略可选字段会保留原值，空数组可清除作者或标签。执行阶段只需要确认参数。编辑沿用站内手工修改的元数据保护。每个操作标识只消费一次，任务结果通过 `task_status` 获取。


## 3. stdio 权限与高级参数

stdio 不需要远程令牌或已有插件连接；本机 Docker 执行权限就是授权边界，但 MCP 插件仍须在插件中心启用。每次请求都会重新检查读者激活状态、阅读权限和目录访问范围。指定读者不会获得该读者以外的权限。

标准容器只需 `booksvr mcp --user 登录用户名`。自定义部署可用 `--user-id` 代替用户名，并覆盖 `--db`、`--user-db`、`--library-root` 和 `--settings-file`。默认只读；只有显式添加 `--allow-write` 才显示写入工具。

客户端每行发送一个 JSON-RPC 对象，服务每行返回一个响应。通知不产生响应。标准输出只承载协议消息，诊断错误写入标准错误。

## 4. 故障排查

- **Unauthorized**：检查插件和连接是否启用、令牌是否正确、远程是否开启，以及实例连接是否关联有效读者。
- **工具不存在**：确认连接具有对应 scope；写工具还需 `allow_write=true`。
- **书籍不存在或不可见**：书籍可能是他人私藏，或不在当前目录访问范围内。
- **没有推送权限**：除了 MCP 写开关，读者还需站点的推送能力与已激活状态。
- **书源不可用**：检查当前读者能否使用该连接，以及书源是否支持下载。
- **操作标识过期**：预检只保留十分钟，重新预检并确认最新摘要。
- **重启后任务不存在**：这是进程内任务的预期行为；请结合书库、设备或插件运行记录核实实际结果，不要直接重试可能已成功的外部写入。

产品规则见 [`spec/插件/MCP.md`](../spec/插件/MCP.md)，插件开发约束见 [`document/PluginGuide.md`](PluginGuide.md)。
