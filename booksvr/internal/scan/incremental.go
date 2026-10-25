package scan

import (
	"context"
	"os"
	"strconv"
	"talebook/booksvr/internal/userdb"
)

type incrementalScanKey struct{}

func WithSourceChanges(ctx context.Context) context.Context {
	return context.WithValue(ctx, incrementalScanKey{}, true)
}
func stampSource(row *userdb.ScanFile, info os.FileInfo) {
	if info != nil {
		setRowData(row, "source_size", strconv.FormatInt(info.Size(), 10), "source_mtime", strconv.FormatInt(info.ModTime().UnixNano(), 10))
	}
}
func (s *Service) changedSource(path string, info os.FileInfo) (bool, error) {
	row, err := getScanFileByPath(s.DB, path)
	if err != nil {
		return false, err
	}
	if row == nil {
		return true, nil
	}
	// Indexed/imported books keep their IDs and metadata on restore or change.
	// Source synchronization owns their availability and physical identity.
	if row.Status == userdb.ScanStatusIndexed || row.Status == userdb.ScanStatusImported || row.Status == userdb.ScanStatusDeleteFailed {
		return false, nil
	}
	if row.Data["source_size"] == strconv.FormatInt(info.Size(), 10) && row.Data["source_mtime"] == strconv.FormatInt(info.ModTime().UnixNano(), 10) {
		return false, nil
	}
	// Only explicitly changed paths are reset; unrelated pending/importing rows
	// never participate in this operation. The caller holds the import writer.
	row.Status = userdb.ScanStatusNew
	row.Hash = TempHash(path, info.Size())
	return true, saveScanFile(s.DB, row)
}
