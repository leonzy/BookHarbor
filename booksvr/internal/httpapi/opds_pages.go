package httpapi

import (
	"fmt"
	"strconv"
	"strings"

	"talebook/booksvr/internal/library"
)

const opdsPageBranches = 50

// Page directories are Navigation feeds. Acquisition feeds remain book-only;
// readers that ignore feed-level pagination can use ordinary directory links.
func opdsPageDirectory(cfg OpdsConfig, ctx *Context, query string) ([]byte, error) {
	parts := strings.Split(query, ":")
	if (len(parts) != 1 && len(parts) != 3) || (parts[0] != "newest" && parts[0] != "title") {
		return nil, errOpdsNotFound
	}
	start, width := 0, 0
	if len(parts) == 3 {
		var err error
		start, err = strconv.Atoi(parts[1])
		if err != nil || start < 0 {
			return nil, errOpdsNotFound
		}
		width, err = strconv.Atoi(parts[2])
		if err != nil || !validOpdsPageWidth(width) || start%width != 0 {
			return nil, errOpdsNotFound
		}
	}
	var total int
	var err error
	if ctx.Access != nil && !ctx.Access.Unrestricted() {
		_, total, err = library.AccessBrowseBooks(ctx.Request.Context(), cfg.DB, *ctx.Access, library.AccessBrowseQuery{BrowseQuery: library.BrowseQuery{TimestampSort: true, Size: 1}})
	} else if cfg.ExcludedBookIDs != nil || (cfg.FilterVisibleBookIDs == nil && cfg.CanViewBook == nil) {
		var excluded map[int64]bool
		if cfg.ExcludedBookIDs != nil {
			excluded, err = cfg.ExcludedBookIDs(ctx)
			if err != nil {
				return nil, err
			}
		}
		_, total, err = library.BrowseBooks(ctx.Request.Context(), cfg.DB, library.BrowseQuery{Excluded: excluded, TimestampSort: true, Size: 1})
	} else {
		var ids []int64
		ids, err = opdsAllBookIDs(cfg)
		if err == nil {
			ids, err = opdsVisibleIDs(cfg, ctx, ids)
		}
		total = len(ids)
	}
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, errOpdsNoBooks
	}
	pageSize := opdsMaxItems(cfg)
	pages := 1 + (total-1)/pageSize
	top := opdsPageWidth(pages)
	if !validOpdsPageWidth(top) {
		return nil, errOpdsNotFound
	}
	if width == 0 {
		width = top
	}
	if width > top || start >= pages {
		return nil, errOpdsNotFound
	}
	end := start + min(width, pages-start)
	name := "最新"
	if parts[0] == "title" {
		name = "书名"
	}
	updated := opdsLastModified(cfg)
	pageURL := opdsPageDirectoryURL(cfg, parts[0], start, width, top)
	b := newOpdsFeed(cfg, "urn:bookharbor:opds-pages:"+query, fmt.Sprintf("按页浏览 · %s · 第 %d–%d 页", name, start+1, end), fmt.Sprintf("共 %d 本，%d 页；每页最多 %d 本，请选择页码或页码范围。", total, pages, pageSize), updated)
	b.feed.Links = append(b.feed.Links, atomLinkXML{Rel: "self", Href: pageURL, Type: opdsNavContentType})
	up := opdsBaseHref(cfg) + "/"
	if width < top {
		parent := width * opdsPageBranches
		up = opdsPageDirectoryURL(cfg, parts[0], start/parent*parent, parent, top)
	}
	b.feed.Links = append(b.feed.Links, atomLinkXML{Rel: "up", Href: up, Type: opdsNavContentType})
	step := max(1, width/opdsPageBranches)
	for page := start; page < end; page += step {
		upper := page + min(step, end-page)
		title := fmt.Sprintf("第 %d–%d 页", page+1, upper)
		href := opdsPageDirectoryURL(cfg, parts[0], page, step, top)
		kind := opdsNavContentType
		text := fmt.Sprintf("打开第 %d–%d 页的页码目录", page+1, upper)
		if step == 1 {
			title = fmt.Sprintf("第 %d 页", page+1)
			href = opdsBaseHref(cfg) + "/nav/" + opdsHexlify("O"+parts[0]) + "?offset=" + strconv.Itoa(page*pageSize)
			kind = opdsContentType
			lastBook := total
			if upper <= total/pageSize {
				lastBook = upper * pageSize
			}
			text = fmt.Sprintf("第 %d–%d 本，共 %d 本", page*pageSize+1, lastBook, total)
		}
		b.addEntry(atomEntryXML{Title: title, ID: fmt.Sprintf("urn:bookharbor:opds-page:%s:%d:%d", parts[0], page, step), Updated: opdsUpdated(updated), Content: &atomContentXML{Type: "text", Inner: opdsXMLText(text)}, Links: []atomLinkXML{{Rel: "subsection", Href: href, Type: kind}}})
	}
	ctx.Writer.Header().Set("Content-Type", opdsHTTPContentType)
	ctx.Writer.Header().Set("Last-Modified", lastModifiedHeader(updated.Unix()))
	return b.bytes()
}

func opdsPageWidth(pages int) int {
	width := opdsPageBranches
	for width < pages {
		if width > int(^uint(0)>>1)/opdsPageBranches {
			return int(^uint(0) >> 1)
		}
		width *= opdsPageBranches
	}
	return width
}

func validOpdsPageWidth(width int) bool {
	if width < opdsPageBranches {
		return false
	}
	for width%opdsPageBranches == 0 {
		width /= opdsPageBranches
	}
	return width == 1
}

func opdsPageDirectoryURL(cfg OpdsConfig, order string, start, width, top int) string {
	q := "P" + order
	if start != 0 || width != top {
		q += fmt.Sprintf(":%d:%d", start, width)
	}
	return opdsBaseHref(cfg) + "/nav/" + opdsHexlify(q)
}
