package httpapi

import (
	"database/sql"
	"os"
	"talebook/booksvr/internal/booktools"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/sourcefile"
)

func requestToolFormatPath(ctx *Context, db *sql.DB, id int64, format, root string) (string, error) {
	if ctx.Access == nil || ctx.Access.Revision() == 0 || ctx.Identity.IsAdmin {
		return booktools.GetFormatPath(db, id, format)
	}
	path, err := requestFormatPath(ctx, db, id, format, root)
	if err != nil {
		return "", err
	}
	if sourcefile.IsReference(path) {
		return "", booktools.NewError("ZIP 内书籍只读，请先导出为普通文件再编辑")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", booktools.NewError("格式文件不是普通文件")
	}
	return path, nil
}

func requestAcquireFormat(ctx *Context, db *sql.DB, id int64, format, root string) (string, func(), error) {
	if ctx.Access == nil || ctx.Access.Revision() == 0 || ctx.Identity.IsAdmin {
		return booktools.AcquireFormatPath(sourceContext(ctx), db, id, format)
	}
	path, err := requestFormatPath(ctx, db, id, format, root)
	if err != nil {
		return "", func() {}, err
	}
	return sourcefile.Acquire(sourceContext(ctx), path)
}

func requestFormatPath(ctx *Context, db *sql.DB, bookID int64, format, root string) (string, error) {
	if ctx != nil && ctx.Access != nil && ctx.Access.Revision() > 0 && !ctx.Identity.IsAdmin {
		file, err := (library.AccessStore{DB: db}).AccessResolveFile(ctx.Request.Context(), *ctx.Access, bookID, format, root)
		return file.Path, err
	}
	return library.FormatAbsPath(db, bookID, format, root)
}

// requestBooksData preserves the unconfigured/admin fast path. Restricted
// requests load only permitted records and formats through the shared index.
func requestBooksData(ctx *Context, db *sql.DB, ids []int64) ([]library.BookData, error) {
	if ctx != nil && ctx.Access != nil && !ctx.Access.Unrestricted() {
		return library.AccessBooksData(ctx.Request.Context(), db, *ctx.Access, ids)
	}
	return library.GetBooksData(db, ids)
}
