package httpapi

import (
	"database/sql"
	"fmt"
	"net"
	"talebook/booksvr/internal/userdb"
)

// Metadata-only deploys metadata fixes beside an uninterrupted import process.
// It opens existing databases and starts only the leased enrichment worker.
func newMetadataOnlyDispatcher(opts ServeOptions, db, userDB *sql.DB) (*Dispatcher, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		return nil, fmt.Errorf("metadata-only requires a loopback listening address")
	}
	if opts.ReaderOnly || opts.PluginFeaturesOnly {
		return nil, fmt.Errorf("metadata-only cannot be combined with other maintenance modes")
	}
	w := newBookWiring(userDB, db, opts.Settings, opts.PluginRegistry).withSettingsPath(opts.SettingsPath)
	w.libraryRoot = opts.LibraryRoot
	var specs []RouteSpec
	for _, spec := range BookRouteSpecs(w.Config(opts.CDNURL)) {
		if spec.Pattern == rBookEdit || spec.Pattern == rBookRefer || spec.Pattern == rBookDetail {
			specs = append(specs, spec)
		}
	}
	specs = append(specs, RouteSpec{Pattern: rGetPcover, Methods: []string{"GET"}, Handler: ProxyImageHandler(FilesConfig{AllowsImageProxyHost: w.registry.AllowsImageProxyHost})})
	specs = append(specs, RouteSpec{Pattern: "/api/admin/metadata/enrichment", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: MetadataEnrichmentStatus(db, opts, userDB)})
	specs = append(specs, metadataSourceRoutes(db, userDB, opts)...)
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
