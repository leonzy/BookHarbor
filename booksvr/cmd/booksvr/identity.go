package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"talebook/booksvr/internal/httpapi"
	"talebook/booksvr/internal/library"
	"time"
)

// Preview opens the actual library read-only. Applying changes is explicit,
// per-ID and additive; the normal background queue handles the rest.
func runBookIdentity(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("book-identity", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("db", "", "书库数据库路径")
	rawIDs := fs.String("ids", "", "预检书号，逗号分隔")
	apply := fs.Bool("apply", false, "应用高置信作品关联，保留书号与文件")
	refresh := fs.Bool("refresh-files", false, "按指定书号只读核查来源文件并补缺，需同时指定apply")
	root := fs.String("library-root", "", "解析书库相对文件路径的根目录")
	repair := fs.Bool("repair-authors", false, "执行一批可续跑的作者归组规则核查，需同时指定apply")
	batches := fs.Int("repair-batches", 1, "维护批次数，最多100；批次间休息50毫秒")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || (*rawIDs == "" && !*repair) || (*repair && !*apply) {
		return errors.New("book-identity: 必须指定 -db 和 -ids")
	}
	if *refresh && (!*apply || *rawIDs == "" || *repair) {
		return errors.New("refresh-files必须指定ids、apply，不能与repair-authors混用")
	}
	ids := []int64{}
	for _, raw := range strings.Split(*rawIDs, ",") {
		if *repair && raw == "" {
			continue
		}
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
	if *repair {
		if *batches < 1 || *batches > 100 {
			return errors.New("repair-batches必须在1到100之间")
		}
		owner := fmt.Sprintf("author-rules:%d:%d", os.Getpid(), time.Now().UnixNano())
		n := 0
		for i := 0; i < *batches; i++ {
			processed, e := library.RunIdentityBatch(context.Background(), db, owner, 50)
			if e != nil {
				return e
			}
			n += processed
			if i+1 < *batches {
				time.Sleep(50 * time.Millisecond)
			}
		}
		return encoder.Encode(map[string]any{"processed": n})
	}
	for _, id := range ids {
		if *refresh {
			if err = library.EnsureMetadataSchema(db); err != nil {
				return err
			}
			if err = httpapi.RefreshFileMetadata(context.Background(), db, *root, id); err != nil {
				return err
			}
		}
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
