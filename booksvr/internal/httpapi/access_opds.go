package httpapi

import "talebook/booksvr/internal/library"

// Both book feeds and counts use the same scoped SQL page. Acquisition links
// are built only from the permitted formats, never from global book metadata.
func accessOpdsFeed(cfg OpdsConfig, ctx *Context, q library.AccessBrowseQuery, offset int, pageURL, upURL, id, title string) ([]byte, error) {
	q.Start, q.Size = offset, opdsMaxItems(cfg)
	ids, total, err := library.AccessBrowseBooks(ctx.Request.Context(), cfg.DB, *ctx.Access, q)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, errOpdsNoBooks
	}
	offsets, err := newOpdsOffsets(offset, q.Size, total)
	if err != nil {
		return nil, err
	}
	books, err := requestBooksData(ctx, cfg.DB, ids)
	if err != nil {
		return nil, err
	}
	return opdsPageFeed(cfg, ctx, books, offsets, pageURL, upURL, id, title)
}

func accessOpdsCategories(cfg OpdsConfig, ctx *Context, category, pageURL, upURL string, offset int, prefixes ...string) ([]byte, error) {
	size := opdsMaxItems(cfg)
	if offset < 0 || offset%size != 0 {
		return nil, errOpdsNotFound
	}
	items, total, err := library.AccessCategoryPage(ctx.Request.Context(), cfg.DB, *ctx.Access, category, "", offset/size+1, size, prefixes...)
	if err != nil {
		return nil, err
	}
	offsets, err := newOpdsOffsets(offset, size, total)
	if err != nil {
		return nil, err
	}
	updated := opdsLastModified(cfg)
	ctx.Writer.Header().Set("Content-Type", opdsHTTPContentType)
	b := newOpdsFeed(cfg, "calibre-category-feed:"+category, opdsDefaultFeedTitle(cfg)+" :: By "+category, "", updated).withNavLinks(offsets, pageURL, upURL)
	base := opdsBaseHref(cfg) + "/category/" + opdsHexlify(category)
	for _, item := range items {
		b.addEntry(opdsCatalogEntry(item, category, base, updated, false, false))
	}
	return b.bytes()
}
