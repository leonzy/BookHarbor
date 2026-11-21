package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

// ResolveBearer authenticates against encrypted plugin connection credentials.
func (s *Service) ResolveBearer(token string, remote bool) (Principal, *userdb.PluginConnection, error) {
	empty := Principal{}
	if len(token) < 32 || len(token) > 512 {
		return empty, nil, fmt.Errorf("MCP 凭据无效")
	}
	connections, err := s.runtime().ConnectionsFor(Capability, nil)
	if err != nil {
		return empty, nil, err
	}
	userConnections := []*userdb.PluginConnection{}
	rows, err := s.DB.Query(`SELECT c.id FROM plugin_connections c JOIN plugin_installations i ON i.id=c.installation_id JOIN plugin_definitions d ON d.id=i.definition_id WHERE i.enabled=1 AND i.status='active' AND c.enabled=1 AND c.owner_type='user' AND d.plugin_key=? ORDER BY c.id`, PluginID)
	if err != nil {
		return empty, nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return empty, nil, err
		}
		ids = append(ids, id)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return empty, nil, closeErr
	}
	if err = rows.Err(); err != nil {
		return empty, nil, err
	}
	for _, id := range ids {
		connection, e := s.runtime().ConnectionByID(id)
		if e == nil && connection != nil {
			userConnections = append(userConnections, connection)
		}
	}
	connections = append(connections, userConnections...)
	for _, connection := range connections {
		secrets, loadErr := s.runtime().LoadSecrets(connection)
		if loadErr != nil {
			continue
		}
		candidate, _ := secrets["access_token"].(string)
		if len(candidate) != len(token) || subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) != 1 {
			continue
		}
		if remote && !configBool(connection.Config, "allow_remote") {
			return empty, nil, fmt.Errorf("该 MCP 连接不允许远程访问")
		}
		principal, principalErr := s.principalForConnection(connection, 0, false, nil)
		if principalErr != nil {
			return empty, nil, principalErr
		}
		if s.AccessResolver != nil {
			principal.Access, principalErr = s.AccessResolver(context.Background(), principal.UserID, principal.IsAdmin)
			if principalErr != nil {
				return empty, nil, principalErr
			}
		}
		return principal, connection, nil
	}
	return empty, nil, fmt.Errorf("MCP 凭据无效")
}

// ResolveLocal selects the current reader's connection, then an instance connection.
func (s *Service) ResolveLocal(userID int64, admin bool, access *library.AccessScope) (Principal, *userdb.PluginConnection, error) {
	if access == nil && s.AccessResolver != nil {
		resolved, err := s.AccessResolver(context.Background(), userID, admin)
		if err != nil {
			return Principal{}, nil, err
		}
		access = resolved
	}
	connections, err := s.runtime().ConnectionsFor(Capability, &userID)
	if err != nil {
		return Principal{}, nil, err
	}
	for _, connection := range connections {
		if connection.OwnerType == "user" && connection.OwnerID != userID {
			continue
		}
		if connection.OwnerType == "instance" && int64(configNumber(connection.Config["user_id"])) != userID {
			continue
		}
		hasCredentials, credentialErr := s.runtime().ConnectionHasCredentials(connection)
		if credentialErr != nil || !hasCredentials {
			continue
		}
		principal, principalErr := s.principalForConnection(connection, userID, true, access)
		if principalErr == nil {
			principal.IsAdmin = admin
			return principal, connection, nil
		}
	}
	return Principal{}, nil, fmt.Errorf("当前读者没有可用的 MCP 连接")
}

func (s *Service) principalForConnection(connection *userdb.PluginConnection, cookieUserID int64, local bool, access *library.AccessScope) (Principal, error) {
	userID := cookieUserID
	if userID == 0 {
		if connection.OwnerType == "user" {
			userID = connection.OwnerID
		} else {
			userID = int64(configNumber(connection.Config["user_id"]))
		}
	}
	reader, err := userdb.GetReaderByID(s.DB, userID)
	if err != nil || reader == nil || !reader.Active {
		return Principal{}, fmt.Errorf("MCP 连接没有关联到有效读者")
	}
	allowWrite := configBool(connection.Config, "allow_write")
	if !hasPermission(reader.Permission, "r") {
		return Principal{}, fmt.Errorf("当前读者没有阅读权限")
	}
	if allowWrite && !hasPermission(reader.Permission, "p") && !hasPermission(reader.Permission, "s") {
		allowWrite = false
	}
	scopes := make([]string, 0, len(connection.Scopes))
	for _, value := range connection.Scopes {
		if text, ok := value.(string); ok {
			scopes = append(scopes, text)
		}
	}
	return Principal{UserID: userID, IsAdmin: reader.Admin, Access: access, ConnectionID: connection.ID, AllowWrite: allowWrite, Local: local, Scopes: scopes}, nil
}

func configBool(config map[string]any, key string) bool {
	value, ok := config[key].(bool)
	return ok && value
}
func configNumber(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		var parsed int
		_, _ = fmt.Sscanf(strings.TrimSpace(n), "%d", &parsed)
		return parsed
	}
	return 0
}
func hasPermission(permission, operation string) bool {
	if strings.Contains(permission, operation) {
		return true
	}
	if strings.Contains(permission, strings.ToUpper(operation)) {
		return false
	}
	return true
}

// RequiredScopes returns transport/runtime scopes for a JSON-RPC tools/call.
func RequiredScopes(request Request) []string {
	if request.Method != "tools/call" {
		return nil
	}
	var params struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(request.Params, &params) != nil {
		return []string{"books.read"}
	}
	switch params.Name {
	case "reading_history":
		return []string{"reading.read"}
	case "book_recommend":
		return []string{"books.read", "reading.read"}
	case "book_send", "book_import", "book_update", "device_manage":
		return []string{"books.write"}
	case "task_status":
		return []string{"tasks.read"}
	default:
		return []string{"books.read"}
	}
}

func authorize(principal Principal, name string) error {
	encoded, _ := json.Marshal(map[string]any{"name": name})
	required := RequiredScopes(Request{Method: "tools/call", Params: encoded})
	owned := map[string]bool{}
	for _, scope := range principal.Scopes {
		owned[scope] = true
	}
	for _, scope := range required {
		if !owned[scope] {
			return toolError("mcp.scope_denied", "MCP 连接未授予此工具所需权限")
		}
	}
	return nil
}
