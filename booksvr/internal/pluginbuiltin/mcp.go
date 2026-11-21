package pluginbuiltin

import (
	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginproto"
)

// MCPProvider exposes the protocol service through the normal plugin runtime.
type MCPProvider struct{ ManifestDict map[string]any }

func newMCPProvider() *MCPProvider {
	return &MCPProvider{ManifestDict: map[string]any{
		"protocol_version": pluginproto.ProtocolVersion,
		"id":               mcp.PluginID,
		"name":             "BookHarbor MCP",
		"description":      "让获授权的 Agent 查书、推荐阅读、查询阅读记录，并在确认后推送或导入书籍。",
		"version":          "1.0.0",
		"categories":       []any{"integrations"},
		"capabilities":     []any{mcp.Capability},
		"runtime_kind":     "builtin",
		"actions":          []any{"test"},
		"auth_schema":      map[string]any{"type": "object", "required": []any{"access_token"}, "properties": map[string]any{"access_token": map[string]any{"type": "string", "format": "password", "writeOnly": true, "minLength": 32, "description": "Agent 使用的 Bearer 令牌，至少 32 个随机字符"}}},
		"config_schema": map[string]any{"type": "object", "properties": map[string]any{
			"allow_remote": map[string]any{"type": "boolean", "default": false},
			"allow_write":  map[string]any{"type": "boolean", "default": false},
			"user_id":      map[string]any{"type": "integer", "minimum": 1, "description": "仅实例连接使用：Bearer 请求代表的读者"},
		}},
		"permissions":       []any{"books.read", "reading.read", "books.write", "tasks.read"},
		"data_policy":       map[string]any{"stores_full_text": false, "retention": "process_memory", "remote_access_default": false, "write_default": false},
		"compatibility":     map[string]any{"talebook": ">=0.1.0"},
		"homepage":          "https://modelcontextprotocol.io/",
		"license":           "GPL-3.0",
		"ui":                map[string]any{"icon": "mdi-robot-outline", "configuration_mode": "connection", "primary_action": "configure"},
		"connection_owners": []any{"instance", "user"},
	}}
}

func (p *MCPProvider) Manifest() map[string]any           { return p.ManifestDict }
func (p *MCPProvider) InitialEnabled(map[string]any) bool { return false }
func (p *MCPProvider) InitialConfig(map[string]any) map[string]any {
	return map[string]any{"allow_remote": false, "allow_write": false}
}
func (p *MCPProvider) Invoke(method string, args []any, context map[string]any) (any, error) {
	if method != "invoke" {
		return nil, pluginproto.NewUpstreamError("MCP 仅接受协议调用", "other", nil)
	}
	platform, _ := context["platform"].(map[string]any)
	return mcp.InvokeFromPlugin(args, platform)
}
func (p *MCPProvider) SelfCheck(map[string]any) (plugindomain.CheckReport, error) {
	return plugindomain.CheckReport{Healthy: true, Message: "MCP provider 已注册；协议可用性取决于连接和读者权限"}, nil
}

var _ pluginiface.MCPProvider = (*MCPProvider)(nil)
