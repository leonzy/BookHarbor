package httpapi

import (
	"strconv"
	"talebook/booksvr/internal/library"
)

// Keep each physical version's ID and URLs. No cross-resource state or paths
// are disclosed until the requester's visibility has been checked for that ID.
func bookIdentityVersions(cfg BookConfig, ctx *Context, id int64) ([]map[string]any, error) {
	ids, err := library.IdentityVersions(cfg.LibraryDB, id)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, other := range ids {
		if cfg.CanViewBook != nil && !cfg.CanViewBook(ctx, other) {
			continue
		}
		b, e := bookGetOne(cfg, other)
		if e != nil {
			return nil, e
		}
		if b == nil {
			continue
		}
		out = append(out, map[string]any{"id": b.ID, "title": b.Title, "authors": b.Authors, "href": cfg.CDNURL + "/book/" + strconv.FormatInt(b.ID, 10), "files": bookFileEntries(cfg, ctx, *b)})
	}
	return out, nil
}

// Cards summarize visible files, while downloads retain their original IDs.
func bookAttachIdentityCards(cfg BookConfig, ctx *Context, cards []map[string]any) error {
	ids := []int64{}
	for _, card := range cards {
		ids = append(ids, int64Of(card["id"]))
	}
	groups, err := library.IdentityCardMembers(cfg.LibraryDB, ids)
	if err != nil {
		return err
	}
	private := map[int64]bool{}
	if cfg.PrivateBookIDs != nil {
		private, err = cfg.PrivateBookIDs(ctx)
		if err != nil {
			return err
		}
	}
	all := []int64{}
	seen := map[int64]bool{}
	for _, members := range groups {
		for _, id := range members {
			if seen[id] || private[id] || (cfg.CanViewBook != nil && !cfg.CanViewBook(ctx, id)) {
				continue
			}
			seen[id] = true
			all = append(all, id)
		}
	}
	books, err := library.IdentityCardFormats(cfg.LibraryDB, all)
	if err != nil {
		return err
	}
	for _, card := range cards {
		members := groups[int64Of(card["id"])]
		if len(members) < 2 {
			continue
		}
		sizes := map[string]int64{}
		formats := []string{}
		n := 0
		for _, id := range members {
			b, ok := books[id]
			if !ok {
				continue
			}
			n++
			for _, format := range b.Formats {
				size := b.FormatSizes[format]
				previous, ok := sizes[format]
				if !ok {
					formats = append(formats, format)
				}
				if !ok || size > previous {
					sizes[format] = size
				}
			}
		}
		card["format_sizes"] = sizes
		card["formats"] = formats
		card["version_count"] = n
	}
	return nil
}
