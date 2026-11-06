package library

import "strings"

const (
	sourceKnown             uint8 = 1
	sourcePreviouslyPresent uint8 = 2
)

// One path table replaces overlapping existence and prior-presence sets.
// A full discovery never reads the cached snapshot, so do not construct it.
func prepareSourceFiles(tracked []sourceTracked, option SourceSyncOptions, limited bool) (map[string]uint8, map[string]sourceDiskFile, map[string]sourceDiskFile) {
	known := map[string]uint8{}
	var cached, transferred map[string]sourceDiskFile
	reuse := limited && !option.FullDiscovery
	for _, t := range tracked {
		flags := known[t.physical] | sourceKnown
		if t.missing == 0 && (t.key != "" || strings.HasPrefix(t.hash, "sha256:")) {
			flags |= sourcePreviouslyPresent
		}
		known[t.physical] = flags
		if t.key == "" || t.missing != 0 {
			continue
		}
		f := sourceDiskFile{t.physical, t.key, t.size, t.mtime}
		if reuse {
			if cached == nil {
				cached = map[string]sourceDiskFile{}
			}
			cached[t.physical] = f
		}
		if len(option.Moves) > 0 {
			if moved := sourceMovedPath(t.physical, option.Moves); moved != "" {
				if transferred == nil {
					transferred = map[string]sourceDiskFile{}
				}
				f.path = moved
				transferred[moved] = f
			}
		}
	}
	return known, cached, transferred
}

// Most baseline entries still exist. Index only keys/sizes actually needed to
// relocate missing sources, including claimed candidates to preserve ambiguity.
func sourceRelocationIndexes(tracked []sourceTracked, files map[string]sourceDiskFile, healthy func(string) bool) (map[string][]sourceDiskFile, map[int64][]sourceDiskFile) {
	keys := map[string]bool{}
	sizes := map[int64]bool{}
	for _, t := range tracked {
		if _, exists := files[t.physical]; exists || !healthy(t.physical) {
			continue
		}
		if t.key != "" {
			keys[t.key] = true
		}
		if strings.HasPrefix(t.hash, "sha256:") {
			sizes[t.size] = true
		}
	}
	byKey := map[string][]sourceDiskFile{}
	bySize := map[int64][]sourceDiskFile{}
	if len(keys) == 0 && len(sizes) == 0 {
		return byKey, bySize
	}
	for _, f := range files {
		if keys[f.key] {
			byKey[f.key] = append(byKey[f.key], f)
		}
		if sizes[f.size] {
			bySize[f.size] = append(bySize[f.size], f)
		}
	}
	return byKey, bySize
}
