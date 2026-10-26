package httpapi

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"talebook/booksvr/internal/userdb"
)

// openExistingReaderDB deliberately does not migrate schemas, start discovery,
// or create a missing database. The importing process owns those responsibilities.
func openExistingReaderDB(path string) (*sql.DB, error) {
	registerCalibreFunctions()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	resource := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	db, err := sql.Open("sqlite", resource.String()+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func newReaderOnlyDispatcher(opts ServeOptions, db, userDB *sql.DB) (*Dispatcher, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		return nil, fmt.Errorf("reader-only requires a loopback listening address")
	}
	wiring := newBookWiring(userDB, db, opts.Settings, nil).withSettingsPath(opts.SettingsPath)
	wiring.libraryRoot = opts.LibraryRoot
	var specs []RouteSpec
	for _, spec := range BookRouteSpecs(wiring.Config(opts.CDNURL)) {
		switch spec.Pattern {
		case rBookRead, rBookReaderBoot, rBookReaderRes:
			specs = append(specs, spec)
		}
	}
	applyCaptchaSettings(specs, CaptchaConfig{CookieSecret: opts.CookieSecret, Settings: opts.CaptchaSettings})
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
