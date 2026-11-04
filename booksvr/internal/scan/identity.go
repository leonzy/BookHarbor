package scan

import (
	"database/sql"
	"strings"
	"talebook/booksvr/internal/bookidentity"
	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
)

func identityMetadata(mi importmeta.Metadata) bookidentity.Metadata {
	return bookidentity.Metadata{Title: mi.Title, Authors: mi.Authors, ISBN: mi.ISBN, Publisher: mi.Publisher, Pubdate: mi.Pubdate, Language: mi.Language}
}

func (s *Service) identityImportMatch(mi importmeta.Metadata, format string, userID int64) (int64, bool, error) {
	ids, err := library.MatchIdentityBooks(s.LibraryDB, identityMetadata(mi))
	if err != nil {
		return 0, false, err
	}
	for _, id := range ids {
		// Scope is in the user DB, not in the shared metadata projection. Never
		// attach an incoming source to another user's private book.
		if s.DB != nil && userID > 0 {
			var scope sql.NullString
			var collector sql.NullInt64
			e := s.DB.QueryRow(`SELECT scope,collector_id FROM items WHERE book_id=?`, id).Scan(&scope, &collector)
			if e != nil && e != sql.ErrNoRows {
				return 0, false, e
			}
			if e == nil && scope.String == "private" && collector.Int64 != userID {
				continue
			}
		}
		var occupied bool
		if err = s.LibraryDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM book_formats WHERE book_id=? AND UPPER(format)=?)`, id, strings.ToUpper(format)).Scan(&occupied); err != nil {
			return 0, false, err
		}
		if !occupied {
			return id, false, nil
		}
	}
	// A different physical file in an occupied format is a version, not a
	// duplicate. SHA256 duplicates have already been handled by the scanner.
	return 0, false, nil
}
