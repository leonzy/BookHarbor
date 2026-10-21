package main

import (
	"flag"
	"io"
	"talebook/booksvr/internal/httpapi"
)

func runBootstrap(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(out)
	db := fs.String("user-db", "/data/books/calibre-webserver.db", "用户数据库")
	settings := fs.String("settings-file", "/data/books/settings/auto.py", "站点配置")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return httpapi.Bootstrap(*db, *settings)
}
