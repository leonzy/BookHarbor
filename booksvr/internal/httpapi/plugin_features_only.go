package httpapi

import (
	"database/sql"
	"fmt"
	"net"

	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginruntime"
)

// A bounded maintenance listener allows a feature fix without restarting imports.
// It shares existing connections, encryption and cookie authentication; it does
// not migrate databases or start any import/run workers. Normal feature calls
// retain their existing lazy-install and per-user connection semantics.
func newPluginFeaturesOnlyDispatcher(opts ServeOptions, db, userDB *sql.DB) (*Dispatcher, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		return nil, fmt.Errorf("plugin-features-only requires a loopback listening address")
	}
	if opts.ReaderOnly {
		return nil, fmt.Errorf("plugin-features-only and reader-only are mutually exclusive")
	}
	registry := opts.PluginRegistry
	if registry == nil {
		registry = pluginruntime.NewPluginRegistry()
		if err := pluginbuiltin.Register(registry); err != nil {
			return nil, err
		}
	}
	cfg := PluginConfig{Settings: opts.Settings, Registry: registry}
	server, err := NewServer([]RouteSpec{{
		Pattern: "/api/plugins/([a-z0-9.-]+)/features/([a-z0-9_]+)",
		Methods: []string{"GET", "POST"}, Auth: true,
		Handler: UserPluginFeatureHandler(cfg),
	}, {
		Pattern: "/api/plugins/([a-z0-9.-]+)",
		Methods: []string{"GET", "POST"}, Auth: true,
		Handler: UserPluginStateHandler(cfg),
	}})
	if err != nil {
		return nil, err
	}
	server.DB = userDB
	server.LibraryDB = db
	server.Identify = NewCookieIdentifier(opts.CookieSecret, nil)
	return NewDispatcher(server, opts.TornadoURL, nil, true)
}
