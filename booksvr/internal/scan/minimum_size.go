package scan

import (
	"fmt"
	"os"
	"talebook/booksvr/internal/userdb"
)

func (s *Service) minimumFileSize() int64 {
	if s.MinFileSize != nil {
		if n := s.MinFileSize(); n > 0 {
			return n
		}
	}
	return 0
}
func (s *Service) belowMinimum(size int64) bool { return size < s.minimumFileSize() }
func (s *Service) dropSmallFile(row *userdb.ScanFile, size int64) (bool, error) {
	minimum := s.minimumFileSize()
	if size >= minimum {
		return false, nil
	}
	row.Status = userdb.ScanStatusDrop
	setRowData(row, "skip_reason", fmt.Sprintf("文件大小 %d 字节，小于最小导入大小 %d KiB", size, minimum/1024), "skip_reason_code", "file.too_small")
	return true, saveScanFile(s.DB, row)
}
func (s *Service) filterImportSize(row *userdb.ScanFile, physical string) (bool, error) {
	info, e := os.Stat(physical)
	if e != nil {
		return false, nil
	} // existing source error handling owns unreadable files
	return s.dropSmallFile(row, info.Size())
}
