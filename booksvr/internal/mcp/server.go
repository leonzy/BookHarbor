package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"talebook/booksvr/internal/background"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

// Service owns protocol dispatch and short-lived write operations.
type Service struct {
	DB             *sql.DB
	LibraryDB      *sql.DB
	Settings       map[string]any
	Registry       *pluginruntime.PluginRegistry
	LibraryRoot    string
	EditMetadata   func(context.Context, Principal, int64, map[string]any, bool) error
	Import         ImportFunc
	Background     *background.Service
	AccessResolver func(context.Context, int64, bool) (*library.AccessScope, error)

	mu         sync.Mutex
	operations map[string]*operation
	tasks      map[string]*task
	idempotent map[string]string
}

func NewService(db, libraryDB *sql.DB, settings map[string]any, registry *pluginruntime.PluginRegistry) *Service {
	if libraryDB == nil {
		libraryDB = db
	}
	return &Service{
		DB: db, LibraryDB: libraryDB, Settings: settings, Registry: registry,
		Background: background.Instance(), operations: map[string]*operation{},
		tasks: map[string]*task{}, idempotent: map[string]string{},
	}
}

func (s *Service) runtime() *pluginruntime.Runtime {
	return pluginruntime.NewRuntime(s.DB, s.Settings, s.Registry)
}

// PluginRuntime returns the normal audited runtime used by transports.
func (s *Service) PluginRuntime() *pluginruntime.Runtime { return s.runtime() }

// Invoke executes a request through the plugin runtime's scopes and audit records.
func (s *Service) Invoke(ctx context.Context, principal Principal, connection *userdb.PluginConnection, request Request) (*Response, error) {
	mode := "read"
	if request.Method == "tools/call" {
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(request.Params, &params)
		if (isWriteTool(params.Name)) && params.Arguments["phase"] == "execute" {
			mode = "write"
		}
	}
	value, err := s.runtime().CallOne(connection, mode, "invoke", []any{request}, pluginruntime.CallOptions{
		RequestedBy: &principal.UserID, RequiredScopes: RequiredScopes(request),
		ContextOverrides: map[string]any{"platform": map[string]any{"mcp_service": s, "mcp_principal": principal, "mcp_context": ctx}},
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	response, _ := value.(*Response)
	return response, nil
}

// Handle executes one request. A nil response is a JSON-RPC notification.
func (s *Service) Handle(ctx context.Context, principal Principal, request Request) *Response {
	response := &Response{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &RPCError{Code: -32600, Message: "Invalid Request"}
		return response
	}
	if request.Method == "notifications/initialized" {
		return nil
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		return nil
	}
	if principal.UserID <= 0 {
		response.Result = failedTool("mcp.unauthorized", "需要有效的读者身份")
		return response
	}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "BookHarbor", "version": "1.0.0"},
			"instructions":    "只查询当前读者可见的数据。推送和导入先 prepare，再使用 operation_id、confirm=true 和幂等键 execute。",
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		available := []map[string]any{}
		for _, tool := range toolDefinitions(principal.AllowWrite) {
			name, _ := tool["name"].(string)
			if authorize(principal, name) == nil {
				available = append(available, tool)
			}
		}
		response.Result = map[string]any{"tools": available}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			response.Error = &RPCError{Code: -32602, Message: "Invalid params"}
			return response
		}
		result, err := s.callTool(ctx, principal, params.Name, params.Arguments)
		if err != nil {
			code, message := publicError(err)
			if code == "mcp.internal" {
				log.Printf("MCP tool failed user=%d tool=%s: %v", principal.UserID, params.Name, err)
			}
			response.Result = failedTool(code, message)
			return response
		}
		response.Result = successfulTool(result)
	default:
		response.Error = &RPCError{Code: -32601, Message: "Method not found"}
	}
	return response
}

func successfulTool(value any) map[string]any {
	encoded, _ := json.MarshalIndent(value, "", "  ")
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(encoded)}},
		"structuredContent": value,
		"isError":           false,
	}
}

func failedTool(code, message string) map[string]any {
	value := map[string]any{"error": map[string]any{"code": code, "message": message}}
	encoded, _ := json.Marshal(value)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(encoded)}},
		"structuredContent": value,
		"isError":           true,
	}
}

func toolDefinitions(writes bool) []map[string]any {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": append([]string{}, required...), "additionalProperties": false}
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(minimum, maximum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
	}
	page := map[string]any{"limit": integer(1, 50), "offset": integer(0, 10000)}
	tools := []map[string]any{
		{"name": "device_list", "description": "列出当前读者的发送设备及编号。", "inputSchema": object(map[string]any{})},
		{"name": "book_detail", "description": "获取一本可见馆藏书的元数据与可用格式。", "inputSchema": object(map[string]any{"book_id": integer(1, 2147483647)}, "book_id")},
		{"name": "book_search", "description": "搜索当前读者可见的馆藏书籍，支持书名、作者、标签及 Calibre 查询语法。", "inputSchema": object(map[string]any{"query": str("搜索词"), "limit": page["limit"], "offset": page["offset"]}, "query")},
		{"name": "reading_history", "description": "获取当前读者的收藏、书架、阅读状态和进度，不能读取其他用户记录。", "inputSchema": object(map[string]any{"state": map[string]any{"type": "string", "enum": []any{"all", "unread", "reading", "finished", "shelf", "favorite"}}, "limit": page["limit"], "offset": page["offset"]})},
		{"name": "book_recommend", "description": "根据自然语言要求、当前读者阅读偏好和可见馆藏给出带理由的推荐。", "inputSchema": object(map[string]any{"request": str("例如：推荐几本轻松的科幻小说，不要我读过的"), "limit": integer(1, 20)}, "request")},
	}
	if writes {
		confirmation := map[string]any{
			"phase":           map[string]any{"type": "string", "enum": []any{"prepare", "execute"}},
			"operation_id":    str("prepare 返回的短期操作标识"),
			"confirm":         map[string]any{"type": "boolean", "description": "execute 必须为 true"},
			"idempotency_key": str("execute 必填；同一读者和工具内唯一"),
		}
		send := map[string]any{"book_id": integer(1, 2147483647), "device_id": integer(1, 2147483647), "device_type": str("kindle、boox 等已配置设备"), "target": str("设备地址或邮箱"), "format": str("可选；指定馆藏格式")}
		for k, v := range confirmation {
			send[k] = v
		}
		importArgs := map[string]any{"source_key": str("已有书源连接的稳定标识"), "book_url": str("该书源返回的书籍外部标识"), "format": str("可选书源格式"), "clean": map[string]any{"type": "boolean"}}
		for k, v := range confirmation {
			importArgs[k] = v
		}
		update := map[string]any{"book_id": integer(1, 2147483647), "title": str("修改后的书名"), "publisher": str("出版社"), "series": str("丛书"), "isbn": str("ISBN"), "authors": map[string]any{"type": "array", "items": str("作者名")}, "tags": map[string]any{"type": "array", "items": str("标签")}}
		for k, v := range confirmation {
			update[k] = v
		}
		device := map[string]any{"action": map[string]any{"type": "string", "enum": []string{"add", "update", "delete"}}, "device_id": integer(1, 2147483647), "name": str("设备名称"), "device_type": str("设备类型"), "ip": str("设备 IP 或主机名"), "port": integer(1, 65535), "schema": str("http 或 https"), "mailbox": str("Kindle 邮箱")}
		for k, v := range confirmation {
			device[k] = v
		}
		tools = append(tools,
			map[string]any{"name": "device_manage", "description": "新增、修改或删除当前读者的设备。修改或删除需 device_id。先预检再确认。", "inputSchema": object(device)},
			map[string]any{"name": "book_update", "description": "修改有权管理的馆藏书。预检传 book_id、title 和要修改的字段，执行只传确认参数。", "inputSchema": object(update)}, map[string]any{"name": "book_send", "description": "把馆藏格式推送到已配置设备。两阶段执行，不能指定文件路径。", "inputSchema": object(send)},
			map[string]any{"name": "book_import", "description": "通过当前读者可用的既有书源连接导入一本书。两阶段执行，不能指定任意 URL。", "inputSchema": object(importArgs)},
		)
	}
	tools = append(tools, map[string]any{"name": "task_status", "description": "查询当前读者创建的 MCP 写入任务。", "inputSchema": object(map[string]any{"task_id": str("写入工具返回的任务标识")}, "task_id")})
	return tools
}

func (s *Service) callTool(ctx context.Context, principal Principal, name string, args map[string]any) (any, error) {
	if err := authorize(principal, name); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	if !isWriteTool(name) && strings.TrimSpace(stringArg(args["phase"])) != "" {
		return nil, toolError("mcp.invalid_params", "该工具不支持写入阶段参数")
	}
	switch name {
	case "device_list":
		return s.devices(principal)
	case "book_detail":
		book, err := s.visibleBook(ctx, principal, int64(intArg(args["book_id"], 0)))
		if err != nil {
			return nil, err
		}
		return bookSummary(*book), nil
	case "book_search":
		return s.search(ctx, principal, args)
	case "reading_history":
		return s.history(ctx, principal, args)
	case "book_recommend":
		return s.recommend(ctx, principal, args)
	case "book_send", "book_import", "book_update", "device_manage":
		return s.write(ctx, principal, name, args)
	case "task_status":
		return s.taskStatus(principal, args)
	default:
		return nil, toolError("mcp.tool_not_found", "工具不存在或未获授权")
	}
}

func isWriteTool(name string) bool {
	switch name {
	case "book_send", "book_import", "book_update", "device_manage":
		return true
	}
	return false
}

func stringArg(value any) string {
	valueString, _ := value.(string)
	return strings.TrimSpace(valueString)
}
func intArg(value any, fallback int) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return fallback
}
func boolArg(value any) bool { result, _ := value.(bool); return result }
func pageArgs(args map[string]any, defaultLimit, maxLimit int) (int, int, error) {
	limit, offset := intArg(args["limit"], defaultLimit), intArg(args["offset"], 0)
	if limit < 1 || limit > maxLimit || offset < 0 || offset > 10000 {
		return 0, 0, toolError("mcp.invalid_params", "分页参数超出允许范围")
	}
	return limit, offset, nil
}

type operation struct {
	ID        string
	Tool      string
	UserID    int64
	Arguments map[string]any
	Summary   map[string]any
	Expires   time.Time
}

type task struct {
	ID           string
	UserID       int64
	Kind         string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	BookID       int64
	BackgroundID int64
	Result       map[string]any
	Error        string
}
