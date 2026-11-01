package httpapi

import (
	"database/sql"
	"fmt"
	"net"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginjobs"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

// Metadata-only deploys metadata fixes beside an uninterrupted import process.
// It opens existing databases without starting importers or directory watchers.
func newMetadataOnlyDispatcher(opts ServeOptions, db, userDB *sql.DB) (*Dispatcher, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		return nil, fmt.Errorf("metadata-only requires a loopback listening address")
	}
	if opts.ReaderOnly || opts.PluginFeaturesOnly {
		return nil, fmt.Errorf("metadata-only cannot be combined with other maintenance modes")
	}
	if err := library.EnsureOpdsClock(db); err != nil {
		return nil, err
	}
	w := newBookWiring(userDB, db, opts.Settings, opts.PluginRegistry).withSettingsPath(opts.SettingsPath)
	w.libraryRoot = opts.LibraryRoot
	opts.PluginRegistry = w.registry
	var specs []RouteSpec
	for _, spec := range BookRouteSpecs(w.Config(opts.CDNURL)) {
		if spec.Pattern == rBookEdit || spec.Pattern == rBookRefer || spec.Pattern == rBookDetail {
			specs = append(specs, spec)
		}
	}
	specs = append(specs, RouteSpec{Pattern: rGetPcover, Methods: []string{"GET"}, Handler: ProxyImageHandler(FilesConfig{AllowsImageProxyHost: w.registry.AllowsImageProxyHost})})
	specs = append(specs, RouteSpec{Pattern: "/api/admin/metadata/enrichment", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: MetadataEnrichmentStatus(db, opts, userDB)})
	specs = append(specs, metadataSourceRoutes(db, userDB, opts)...)
	specs = append(specs, metadataReviewRoutes(db)...)
	executor := pluginjobs.NewExecutor(pluginjobs.Config{DB: userDB, Runtime: pluginruntime.NewRuntime(userDB, opts.Settings, w.registry)})
	pluginConfig := PluginConfig{Settings: opts.Settings, Registry: w.registry, ExecuteRun: func(_ *Context, id int64) error { _, e := executor.Execute(id); return e }}
	// The catalogue and connection controls must see the same native providers as
	// searches and scheduling, while the original import listener stays running.
	for _, spec := range PluginRouteSpecs(pluginConfig) {
		switch spec.Pattern {
		case "/api/admin/plugins", "/api/admin/plugins/installations/([0-9]+)/state", "/api/admin/plugins/connections", "/api/admin/plugins/connections/([0-9]+)/state", "/api/admin/plugins/connections/([0-9]+)/(test|preview|run|retry|rollback)":
			specs = append(specs, spec)
		}
	}
	specs = append(specs,
		RouteSpec{Pattern: "/api/admin/plugins/runs", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: AdminPluginRunsHandler(pluginConfig)},
		RouteSpec{Pattern: "/api/admin/plugins/runs/([0-9]+)", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: AdminPluginRunDetailHandler(pluginConfig)},
		RouteSpec{Pattern: "/api/plugins/runs", Methods: []string{"GET"}, Auth: true, Handler: UserPluginRunsHandler(pluginConfig)},
		RouteSpec{Pattern: "/api/plugins/runs/([0-9]+)", Methods: []string{"GET"}, Auth: true, Handler: UserPluginRunDetailHandler(pluginConfig)},
	)
	specs = append(specs, OpdsRouteSpecs(newOpdsConfig(db, userDB, opts))...)
	specs = append(specs, networkLibraryRouteSpecs(opts, userDB, db, w.registry)...)
	specs = append(specs, RouteSpec{Pattern: "/api/admin/plugins/([a-z0-9.-]+)/metadata/search", Methods: []string{"POST"}, Auth: true, Admin: true, Handler: AdminPluginMetadataSearchHandler(PluginConfig{Settings: opts.Settings, Registry: w.registry})})
	server, err := NewServer(specs)
	if err != nil {
		return nil, err
	}
	server.DB = userDB
	server.LibraryDB = db
	server.Identify = NewCookieIdentifier(opts.CookieSecret, nil)
	server.IsPublic = func(identity Identity, id int64) (bool, error) {
		return userdb.CanViewBook(userDB, id, identity.UserID, identity.IsAdmin)
	}
	return NewDispatcher(server, opts.TornadoURL, nil, true)
}
