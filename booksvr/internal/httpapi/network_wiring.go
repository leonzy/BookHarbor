package httpapi

import (
	"database/sql"

	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/sourcecatalog"
)

// networkLibraryRouteSpecs shares the registered providers across the normal
// server and maintenance listener. A nil runtime registry would create an empty
// registry, even though the database still contains enabled source connections.
func networkLibraryRouteSpecs(opts ServeOptions, db, libraryDB *sql.DB, registry *pluginruntime.PluginRegistry) []RouteSpec {
	settings := func() map[string]any {
		if opts.SettingsPath != "" {
			return LoadSettings(opts.SettingsPath)
		}
		return opts.Settings
	}
	catalog := func(ctx *Context) (*sourcecatalog.Service, error) {
		current := settings()
		return sourcecatalog.NewService(db, current, ctx.Identity.UserID, pluginruntime.NewRuntime(db, current, registry))
	}
	comic := OnlineComicConfig{Settings: map[string]any{"cookie_secret": opts.CookieSecret}, Catalog: catalog}
	save := opts.SaveOnline
	if save == nil {
		save = func(ctx *Context, key, bookURL, format string, clean bool, taskID int64) error {
			return NewSaveOnlineWithLibrary(db, libraryDB, settings(), registry)(ctx, key, bookURL, format, clean, taskID)
		}
	}
	specs := NetworkRouteSpecs(NetworkConfig{
		Settings:           opts.Settings,
		LibraryRoot:        opts.LibraryRoot,
		Catalog:            catalog,
		SaveOnline:         save,
		ComicCoverRewriter: OnlineComicCoverRewriter(comic),
	})
	return append(specs, OnlineComicRouteSpecs(comic)...)
}
