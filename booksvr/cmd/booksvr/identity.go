package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"talebook/booksvr/internal/httpapi"
	"talebook/booksvr/internal/library"
)

// Preview opens the actual library read-only. Applying changes is explicit,
// per-ID and additive; the normal background queue handles the rest.
func runBookIdentity(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("book-identity", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("db", "", "书库数据库路径")
	rawIDs := fs.String("ids", "", "预检书号，逗号分隔")
	apply := fs.Bool("apply", false, "应用高置信作品关联，保留书号与文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || *rawIDs == "" {
		return errors.New("book-identity: 必须指定 -db 和 -ids")
	}
	ids := []int64{}
	for _, raw := range strings.Split(*rawIDs, ",") {
		n, e := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if e != nil || n < 1 {
			return errors.New("book-identity: 无效书号")
		}
		ids = append(ids, n)
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	query := u.Query()
	query.Set("mode", "ro")
	if *apply {
		query.Set("mode", "rw")
	}
	query.Add("_pragma", "busy_timeout(3000)")
	u.RawQuery = query.Encode()
	httpapi.RegisterCalibreFunctions()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if *apply {
		if err = library.EnsureIdentitySchema(db); err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(out)
	for _, id := range ids {
		result, e := library.ReconcileBookIdentity(db, id, !*apply)
		if e != nil {
			return e
		}
		if e = encoder.Encode(result); e != nil {
			return e
		}
	}
	return nil
}
