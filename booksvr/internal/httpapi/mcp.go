package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/userdb"
)

// MCPConfig wires the protocol service without creating an import cycle.
type MCPConfig struct{ Service *mcp.Service }

func MCPRouteSpecs(cfg MCPConfig) []RouteSpec {
	return []RouteSpec{{Pattern: "/mcp", Methods: []string{http.MethodPost}, Handler: func(ctx *Context) (map[string]any, error) {
		if cfg.Service == nil {
			writeMCPError(ctx.Writer, http.StatusServiceUnavailable, nil, -32603, "MCP service unavailable")
			return nil, nil
		}
		if !mcpOriginAllowed(ctx.Request) {
			writeMCPError(ctx.Writer, http.StatusForbidden, nil, -32001, "Origin not allowed")
			return nil, nil
		}
		if version := ctx.Request.Header.Get("MCP-Protocol-Version"); version != "" && version != mcp.ProtocolVersion && version != "2025-03-26" {
			writeMCPError(ctx.Writer, http.StatusBadRequest, nil, -32600, "Unsupported MCP protocol version")
			return nil, nil
		}
		remote := mcpRemoteRequest(ctx.Request)
		token := ""
		if parts := strings.SplitN(ctx.Request.Header.Get("Authorization"), " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token = strings.TrimSpace(parts[1])
		}
		var principal mcp.Principal
		var connection *userdb.PluginConnection
		var err error
		if token != "" {
			principal, connection, err = cfg.Service.ResolveBearer(token, remote)
		} else if !remote {
			principal, connection, err = cfg.Service.ResolveLocal(ctx.Identity.UserID, ctx.Identity.IsAdmin, ctx.Access)
		} else {
			err = fmt.Errorf("远程 MCP 必须使用 Bearer 认证")
		}
		if err != nil {
			writeMCPError(ctx.Writer, http.StatusUnauthorized, nil, -32001, "Unauthorized")
			return nil, nil
		}
		body := http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1<<20)
		defer body.Close()
		var request mcp.Request
		decoder := json.NewDecoder(body)
		if err = decoder.Decode(&request); err != nil {
			writeMCPError(ctx.Writer, http.StatusBadRequest, nil, -32700, "Parse error")
			return nil, nil
		}
		if decoder.Decode(new(json.RawMessage)) != io.EOF {
			writeMCPError(ctx.Writer, http.StatusBadRequest, nil, -32700, "Expected a single JSON-RPC message")
			return nil, nil
		}
		response, callErr := cfg.Service.Invoke(ctx.Request.Context(), principal, connection, request)
		if callErr != nil {
			writeMCPError(ctx.Writer, http.StatusOK, request.ID, -32603, "Internal error")
			return nil, nil
		}
		if response == nil {
			ctx.Writer.WriteHeader(http.StatusAccepted)
			return nil, nil
		}
		writeMCPResponse(ctx.Writer, http.StatusOK, response)
		return nil, nil
	}}}
}

// Browser requests must originate at the endpoint's own origin. Agent
// clients which do not send Origin continue to use Bearer authentication.
func mcpOriginAllowed(request *http.Request) bool {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Host == "" {
		return false
	}
	if !mcpRemoteRequest(request) {
		host := origin.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return false
		}
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	host, _, _ := net.SplitHostPort(request.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && mcpRemoteRequest(request) {
		if forwarded := request.Header.Get("X-Scheme"); forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
	}
	return origin.Scheme == scheme && strings.EqualFold(origin.Host, request.Host)
}

func mcpRemoteRequest(request *http.Request) bool {
	if request.Header.Get("X-Forwarded-For") != "" || request.Header.Get("X-Real-IP") != "" {
		return true
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func writeMCPError(writer http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeMCPResponse(writer, status, &mcp.Response{JSONRPC: "2.0", ID: id, Error: &mcp.RPCError{Code: code, Message: message}})
}
func writeMCPResponse(writer http.ResponseWriter, status int, response *mcp.Response) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(response)
}
