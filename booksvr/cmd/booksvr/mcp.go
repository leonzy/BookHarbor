package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"talebook/booksvr/internal/httpapi"
	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

func runMCP(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(out)
	dbPath := flags.String("db", mcpPath("BOOKSVR_DB", "/data/books/library/metadata.db"), "Calibre metadata.db 路径")
	userDBPath := flags.String("user-db", os.Getenv("BOOKSVR_USER_DB"), "业务数据库路径")
	userID := flags.Int64("user-id", 0, "读者编号")
	username := flags.String("user", "", "读者用户名")
	allowWrite := flags.Bool("allow-write", false, "允许经确认的书籍编辑、推送与导入")
	settingsFile := flags.String("settings-file", mcpPath("BOOKSVR_SETTINGS_FILE", "/data/books/settings/auto.py"), "站点配置文件")
	libraryRoot := flags.String("library-root", mcpPath("BOOKSVR_LIBRARY_ROOT", "/data/books/library"), "书库根目录")
	cookieSecret := flags.String("cookie-secret", os.Getenv("BOOKSVR_COOKIE_SECRET"), "访问范围校验使用的 Cookie 密钥")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (*userID > 0) == (*username != "") {
		return errors.New("mcp: 必须指定 -user 或大于零的 -user-id，二选一")
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("mcp: 无法访问书库数据库: %w", err)
	}
	if *userDBPath == "" {
		*userDBPath = *dbPath
		if *dbPath == "/data/books/library/metadata.db" {
			*userDBPath = "/data/books/calibre-webserver.db"
		}
	}
	httpapi.RegisterCalibreFunctions()
	libraryDB, err := sql.Open("sqlite", *dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	defer libraryDB.Close()
	userDB := libraryDB
	if *userDBPath != *dbPath {
		if _, err = os.Stat(*userDBPath); err != nil {
			return fmt.Errorf("mcp: 无法访问业务数据库: %w", err)
		}
		userDB, err = sql.Open("sqlite", *userDBPath+"?_pragma=busy_timeout(30000)")
		if err != nil {
			return err
		}
		defer userDB.Close()
	}
	var reader *userdb.Reader
	if *username != "" {
		reader, err = userdb.GetReaderByUsername(userDB, *username)
	} else {
		reader, err = userdb.GetReaderByID(userDB, *userID)
	}
	if err != nil || reader == nil || !reader.Active || !reader.HasPermission("r", true) {
		return errors.New("mcp: 指定读者不存在、未激活或无阅读权限")
	}
	installation, err := userdb.GetPluginInstallationByKey(userDB, mcp.PluginID)
	if err != nil {
		return err
	}
	if installation == nil || !installation.Enabled || installation.Status != "active" {
		return errors.New("mcp: 请先在 BookHarbor 插件中心启用 MCP")
	}
	registry := pluginruntime.NewPluginRegistry()
	if err = pluginbuiltin.Register(registry); err != nil {
		return err
	}
	settings := httpapi.LoadSettings(*settingsFile)
	opts := httpapi.ServeOptions{DBPath: *dbPath, UserDBPath: *userDBPath, Settings: settings, SettingsPath: *settingsFile, LibraryRoot: *libraryRoot, PluginRegistry: registry, CookieSecret: *cookieSecret}
	service := mcp.NewService(userDB, libraryDB, settings, registry)
	service.LibraryRoot = *libraryRoot
	service.Import = func(ctx context.Context, principal mcp.Principal, sourceKey, bookURL, format string, clean bool, taskID int64) error {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/mcp", nil)
		callCtx := &httpapi.Context{Request: request, Identity: httpapi.Identity{UserID: principal.UserID, IsAdmin: principal.IsAdmin}, DB: userDB, LibraryDB: libraryDB, Access: principal.Access}
		return httpapi.NewSaveOnlineWithLibrary(userDB, libraryDB, settings, registry)(callCtx, sourceKey, bookURL, format, clean, taskID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := httpapi.PrepareMCPStdio(ctx, service, opts, userDB, libraryDB)
	if err != nil {
		return fmt.Errorf("mcp: 初始化访问范围失败: %w", err)
	}
	defer stop()
	principal := mcp.Principal{UserID: reader.ID, IsAdmin: reader.Admin, Local: true, AllowWrite: *allowWrite, Scopes: []string{"books.read", "reading.read", "tasks.read"}}
	if *allowWrite {
		principal.Scopes = append(principal.Scopes, "books.write")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request mcp.Request
		if json.Unmarshal([]byte(line), &request) != nil {
			if err = encoder.Encode(&mcp.Response{JSONRPC: "2.0", Error: &mcp.RPCError{Code: -32700, Message: "Parse error"}}); err != nil {
				return err
			}
			continue
		}
		current, err := userdb.GetReaderByID(userDB, principal.UserID)
		if err != nil || current == nil || !current.Active || !current.HasPermission("r", true) {
			return errors.New("mcp: 当前读者不再具备阅读权限")
		}
		installed, err := userdb.GetPluginInstallationByKey(userDB, mcp.PluginID)
		if err != nil {
			return err
		}
		if installed == nil || !installed.Enabled || installed.Status != "active" {
			return errors.New("mcp: 插件已停用")
		}
		principal.IsAdmin = current.Admin
		principal.Access, err = service.AccessResolver(ctx, principal.UserID, principal.IsAdmin)
		if err != nil {
			return fmt.Errorf("mcp: 无法读取当前权限: %w", err)
		}
		response := service.Handle(ctx, principal, request)
		if response != nil {
			if err = encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func mcpPath(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
