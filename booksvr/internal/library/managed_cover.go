package library

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"talebook/booksvr/internal/coverimage"
)

// ManagedCoverPath keeps artwork separate from external, potentially read-only books.
func ManagedCoverPath(root string, id int64) string {
	return filepath.Join(root, ".talebook-covers", fmt.Sprintf("%d.jpg", id))
}

// StoreCover stores only a bounded JPEG, never an ebook or a source-side file.
// Existing artwork wins; linking the completed temporary file cannot overwrite it.
func StoreCover(db *sql.DB, root string, id int64, data []byte) (bool, error) {
	return storeCover(db, root, id, data, false)
}

// ReplaceCover is reserved for an explicit user selection. It atomically
// replaces managed artwork and never writes beside an indexed source book.
func ReplaceCover(db *sql.DB, root string, id int64, data []byte) (bool, error) {
	return storeCover(db, root, id, data, true)
}

func storeCover(db *sql.DB, root string, id int64, data []byte, replace bool) (bool, error) {
	if id <= 0 || root == "" {
		return false, fmt.Errorf("invalid cover destination")
	}
	encoded, err := coverimage.JPEG(data, 800)
	if err != nil {
		return false, err
	}
	var exists int
	if err := db.QueryRow(`SELECT 1 FROM books WHERE id=?`, id).Scan(&exists); err != nil {
		return false, err
	}
	path := ManagedCoverPath(root, id)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cover-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(encoded); err != nil {
		f.Close()
		return false, err
	}
	if err = f.Chmod(0644); err != nil {
		f.Close()
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	created := true
	if replace {
		if err = os.Rename(f.Name(), path); err != nil {
			return false, err
		}
	} else if err = os.Link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return false, err
		}
		created = false
	}
	// On a busy database the complete image remains usable and a retry repairs the flag.
	_, err = db.Exec(`UPDATE books SET has_cover=1 WHERE id=? AND COALESCE(has_cover,0)=0`, id)
	return created, err
}
