package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"os"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/userdb"
)

func newMCPService(opts ServeOptions, userDB, libraryDB *sql.DB) *mcp.Service {
	service := mcp.NewService(userDB, libraryDB, opts.Settings, opts.PluginRegistry)
	service.LibraryRoot = opts.LibraryRoot
	wireMCPMetadataEditor(service, opts, userDB, libraryDB)
	if opts.accessRuntime != nil {
		service.AccessResolver = mcpAccessResolver(opts.accessRuntime, userDB, libraryDB)
	}
	service.Import = func(callCtx context.Context, principal mcp.Principal, sourceKey, bookURL, format string, clean bool, taskID int64) error {
		request, err := http.NewRequestWithContext(callCtx, http.MethodPost, "/mcp", nil)
		if err != nil {
			return err
		}
		ctx := &Context{Request: request, Identity: Identity{UserID: principal.UserID, IsAdmin: principal.IsAdmin}, DB: userDB, LibraryDB: libraryDB, Access: principal.Access}
		settings := liveSettings(opts.Settings, opts.SettingsPath)
		return NewSaveOnlineWithLibrary(userDB, libraryDB, settings, opts.PluginRegistry)(ctx, sourceKey, bookURL, format, clean, taskID)
	}
	return service
}

// PrepareMCPStdio wires the same ACL projection and import service as HTTP.
func PrepareMCPStdio(ctx context.Context, service *mcp.Service, opts ServeOptions, userDB, libraryDB *sql.DB) (func(), error) {
	// The primary service owns schema migrations and background indexing.
	opts.PluginFeaturesOnly = true
	runtime, stop, err := startAccessRuntime(ctx, opts, libraryDB, userDB)
	if err != nil {
		return nil, err
	}
	service.AccessResolver = mcpAccessResolver(runtime, userDB, libraryDB)
	wireMCPMetadataEditor(service, opts, userDB, libraryDB)
	return stop, nil
}

func mcpAccessResolver(runtime *accessRuntime, userDB, libraryDB *sql.DB) func(context.Context, int64, bool) (*library.AccessScope, error) {
	return func(callCtx context.Context, userID int64, admin bool) (*library.AccessScope, error) {
		request, err := http.NewRequestWithContext(callCtx, http.MethodPost, "/mcp", nil)
		if err != nil {
			return nil, err
		}
		ctx := &Context{Request: request, Identity: Identity{UserID: userID, IsAdmin: admin}, DB: userDB, LibraryDB: libraryDB}
		check, err := runtime.prepare(ctx)
		if err != nil {
			return nil, err
		}
		if check == nil {
			return nil, os.ErrPermission
		}
		if err = check(); err != nil {
			return nil, err
		}
		return ctx.Access, nil
	}
}

func wireMCPMetadataEditor(service *mcp.Service, opts ServeOptions, userDB, libraryDB *sql.DB) {
	wiring := newBookWiring(userDB, libraryDB, opts.Settings, opts.PluginRegistry).withSettingsPath(opts.SettingsPath)
	service.EditMetadata = func(callCtx context.Context, principal mcp.Principal, id int64, patch map[string]any, apply bool) error {
		reader, err := userdb.GetReaderByID(userDB, principal.UserID)
		if err != nil {
			return err
		}
		if reader == nil || !reader.Active || reader.Admin != principal.IsAdmin {
			return &mcp.ToolError{Code: "mcp.permission_denied", Message: "读者状态已变化"}
		}
		request, err := http.NewRequestWithContext(callCtx, http.MethodPost, "/mcp", nil)
		if err != nil {
			return err
		}
		ctx := &Context{Request: request, Identity: Identity{UserID: principal.UserID, IsAdmin: principal.IsAdmin}, DB: userDB, LibraryDB: libraryDB, Access: principal.Access}
		if !reader.Admin {
			owned, err := userdb.IsBookOwner(userDB, id, reader.ID, false)
			if err != nil {
				return err
			}
			if !owned {
				return &mcp.ToolError{Code: "mcp.permission_denied", Message: "只有管理员或私藏书所有者可以编辑"}
			}
		}
		if !apply {
			return nil
		}
		result, err := wiring.bookEdit(ctx, id, patch)
		if err != nil {
			return err
		}
		if result["err"] != "ok" {
			return &mcp.ToolError{Code: "book.edit_failed", Message: "书籍不可用或无法保存"}
		}
		return nil
	}
}
