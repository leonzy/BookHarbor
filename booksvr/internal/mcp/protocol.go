// Package mcp implements BookHarbor's authenticated Model Context Protocol tools.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"talebook/booksvr/internal/library"
)

const (
	ProtocolVersion = "2025-06-18"
	PluginID        = "talebook.combo.mcp"
	Capability      = "integrations.mcp"
)

// Principal is the authenticated reader and the plugin connection used for a call.
type Principal struct {
	UserID       int64
	IsAdmin      bool
	Access       *library.AccessScope
	ConnectionID int64
	AllowWrite   bool
	Local        bool
	Scopes       []string
}

// Request is one JSON-RPC request. Notifications have no ID.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC response, never BookHarbor's legacy err/msg envelope.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolError is a deliberate, safe message returned in an MCP tool result.
type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string { return e.Message }

func toolError(code, message string) error { return &ToolError{Code: code, Message: message} }

func publicError(err error) (string, string) {
	var safe *ToolError
	if errors.As(err, &safe) {
		return safe.Code, safe.Message
	}
	return "mcp.internal", "工具调用失败，请查看服务器日志"
}

// ImportFunc is the existing online-source importer, injected by HTTP/CLI wiring.
type ImportFunc func(ctx context.Context, principal Principal, sourceKey, bookURL, format string, clean bool, backgroundTaskID int64) error

// InvokeFromPlugin preserves typed provider dispatch and runtime auditing.
func InvokeFromPlugin(args []any, platform map[string]any) (any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("MCP 调用参数无效")
	}
	service, ok := platform["mcp_service"].(*Service)
	if !ok || service == nil {
		return nil, fmt.Errorf("MCP 服务未装配")
	}
	principal, ok := platform["mcp_principal"].(Principal)
	if !ok {
		return nil, fmt.Errorf("MCP 身份未装配")
	}
	request, ok := args[0].(Request)
	if !ok {
		return nil, fmt.Errorf("MCP 请求类型无效")
	}
	ctx, _ := platform["mcp_context"].(context.Context)
	if ctx == nil {
		ctx = context.Background()
	}
	return service.Handle(ctx, principal, request), nil
}
