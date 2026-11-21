package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/userdb"
)

const mcpPersonalRole = "personal-mcp"

var mcpPersonalTokenMu sync.Mutex

func mcpPersonalToken(cfg PluginConfig, ctx *Context) (map[string]any, error) {
	ctx.Writer.Header().Set("Cache-Control", "no-store")
	ctx.Writer.Header().Set("Pragma", "no-cache")
	reader, err := userdb.GetReaderByID(ctx.DB, ctx.Identity.UserID)
	if err != nil {
		return nil, err
	}
	if reader == nil || !reader.Active || !reader.HasPermission("r", true) {
		return map[string]any{"err": "mcp.permission_denied", "msg": "当前账号没有阅读权限"}, nil
	}
	installation, err := userdb.GetPluginInstallationByKey(ctx.DB, mcp.PluginID)
	if err != nil {
		return nil, err
	}
	enabled := installation != nil && installation.Enabled && installation.Status == "active"
	result := map[string]any{"err": "ok", "username": reader.Username, "enabled": enabled, "configured": false, "allow_write": false}
	if !enabled {
		return result, nil
	}

	write := ctx.Request.Method == http.MethodPost
	var action string
	var allowWrite bool
	if write {
		if !mcpOriginAllowed(ctx.Request) || !strings.HasPrefix(strings.ToLower(ctx.Request.Header.Get("Content-Type")), "application/json") {
			return map[string]any{"err": "mcp.request_forbidden", "msg": "请在站点内操作"}, nil
		}
		req, e := pluginBody(ctx)
		if e != nil {
			return pluginErrorOr(e), nil
		}
		action = pluginStr(req["action"])
		switch action {
		case "reveal", "rotate":
		case "settings":
			var ok bool
			allowWrite, ok = req["allow_write"].(bool)
			if !ok {
				return map[string]any{"err": "params.invalid", "msg": "写入权限设置无效"}, nil
			}
		default:
			return map[string]any{"err": "params.invalid", "msg": "令牌操作无效"}, nil
		}
		mcpPersonalTokenMu.Lock()
		defer mcpPersonalTokenMu.Unlock()
	}
	conns, err := pluginUserInstallationConnections(ctx.DB, installation.ID, reader.ID)
	if err != nil {
		return nil, err
	}
	var conn *userdb.PluginConnection
	for _, c := range conns {
		if c.Role == mcpPersonalRole {
			conn = c
			break
		}
	}
	if conn != nil {
		result["configured"] = conn.Enabled && conn.SecretID.Valid
		result["allow_write"], _ = conn.Config["allow_write"].(bool)
	}
	if !write {
		return result, nil
	}
	rt := cfg.pluginRuntime(ctx)
	if action == "settings" {
		if conn == nil || !conn.Enabled {
			return map[string]any{"err": "mcp.token_missing", "msg": "请先生成令牌"}, nil
		}
		_, err = ctx.DB.ExecContext(ctx.Request.Context(), `UPDATE plugin_connections SET config=json_set(config,'$.allow_write',json(?)),update_time=? WHERE id=? AND owner_type='user' AND owner_id=?`, map[bool]string{true: "true", false: "false"}[allowWrite], nowString(), conn.ID, reader.ID)
		if err != nil {
			return nil, err
		}
		result["allow_write"] = allowWrite
		return result, nil
	}
	if conn != nil && conn.Enabled && action == "reveal" {
		credentials, e := rt.LoadSecrets(conn)
		if e != nil {
			return pluginErrorOr(e), nil
		}
		token, _ := credentials["access_token"].(string)
		if token == "" {
			return map[string]any{"err": "mcp.token_missing", "msg": "令牌无法读取，请重新生成"}, nil
		}
		result["access_token"] = token
		return result, nil
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(secret[:])
	scopes := []any{"books.read", "reading.read", "tasks.read", "books.write"}
	conn, err = rt.SaveConnection(installation.ID, "user", reader.ID, map[string]any{"access_token": token}, "BookHarbor MCP", map[string]any{"allow_remote": true, "allow_write": result["allow_write"]}, scopes, "", mcpPersonalRole)
	if err != nil {
		return pluginErrorOr(err), nil
	}
	if !conn.Enabled {
		if _, err = ctx.DB.ExecContext(ctx.Request.Context(), `UPDATE plugin_connections SET enabled=1 WHERE id=? AND owner_type='user' AND owner_id=?`, conn.ID, reader.ID); err != nil {
			return nil, err
		}
	}
	result["configured"] = true
	result["access_token"] = token
	return result, nil
}
