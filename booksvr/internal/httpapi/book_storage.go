package httpapi

import (
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/sourcefile"
)

func bookStorageLocation(cfg BookConfig, book library.BookData, format string) map[string]any {
	var stored string
	e := cfg.LibraryDB.QueryRow(`SELECT path FROM book_formats WHERE book_id=? AND upper(format)=?`, book.ID, strings.ToUpper(format)).Scan(&stored)
	if e != nil {
		var name string
		if cfg.LibraryDB.QueryRow(`SELECT name FROM data WHERE book=? AND upper(format)=?`, book.ID, strings.ToUpper(format)).Scan(&name) != nil {
			return nil
		}
		stored = filepath.Join(book.Path, name+"."+strings.ToLower(format))
	}
	location := map[string]any{}
	if sourcefile.IsReference(stored) {
		ref, err := sourcefile.Decode(stored)
		if err != nil {
			return nil
		}
		stored = ref.Archive
		location["archive_member"] = strings.Join(ref.Chain, " → ")
	}
	if !filepath.IsAbs(stored) {
		stored = filepath.Join(cfg.LibraryRoot, stored)
	}
	location["path"] = stored
	location["directory"] = filepath.Dir(stored)
	location["filename"] = filepath.Base(stored)
	return location
}
