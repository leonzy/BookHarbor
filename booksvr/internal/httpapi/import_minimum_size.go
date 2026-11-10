package httpapi

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const defaultImportMinSizeKiB int64 = 10
const maxImportMinSizeKiB int64 = 2147483647

var importSettingsRevision atomic.Uint64

func parseImportMinSize(value any) (int64, error) {
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	case string:
		var e error
		n, e = strconv.ParseFloat(v, 64)
		if e != nil {
			return 0, e
		}
	default:
		return 0, fmt.Errorf("最小导入文件大小必须是非负整数（KiB）")
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > float64(maxImportMinSizeKiB) || math.Trunc(n) != n {
		return 0, fmt.Errorf("最小导入文件大小必须为 0 至 %d 的整数（KiB）", maxImportMinSizeKiB)
	}
	return int64(n), nil
}
func importMinSize(settings map[string]any) int64 {
	if n, e := parseImportMinSize(settings["import_min_file_size_kib"]); e == nil {
		return n
	}
	return defaultImportMinSizeKiB
}

// Cache parsing between files; saves invalidate immediately, external edits within a second.
func NewImportMinimumSizeReader(path string) func() int64 {
	var mu sync.Mutex
	var expires time.Time
	var revision uint64
	var size int64
	return func() int64 {
		mu.Lock()
		defer mu.Unlock()
		current := importSettingsRevision.Load()
		if time.Now().After(expires) || current != revision {
			size = importMinSize(LoadSettings(path)) * 1024
			revision = current
			expires = time.Now().Add(time.Second)
		}
		return size
	}
}
