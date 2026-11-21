package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

func (s *Service) visibleIDs(ctx context.Context, principal Principal, ids []int64) ([]int64, error) {
	private, err := userdb.PrivateBookIDsFor(s.DB, principal.UserID, principal.IsAdmin)
	if err != nil {
		return nil, err
	}
	visible := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !private[id] {
			visible = append(visible, id)
		}
	}
	if principal.Access != nil && !principal.Access.Unrestricted() {
		return library.AccessFilterIDs(ctx, s.LibraryDB, *principal.Access, visible)
	}
	return visible, nil
}

func (s *Service) visibleBook(ctx context.Context, principal Principal, id int64) (*library.BookData, error) {
	if id <= 0 {
		return nil, toolError("book.not_found", "书籍不存在或不可见")
	}
	ids, err := s.visibleIDs(ctx, principal, []int64{id})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, toolError("book.not_found", "书籍不存在或不可见")
	}
	books, err := s.booksData(ctx, principal, ids)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, toolError("book.not_found", "书籍不存在或不可见")
	}
	return &books[0], nil
}

func (s *Service) booksData(ctx context.Context, principal Principal, ids []int64) ([]library.BookData, error) {
	if principal.Access != nil && !principal.Access.Unrestricted() {
		return library.AccessBooksData(ctx, s.LibraryDB, *principal.Access, ids)
	}
	return library.GetBooksData(s.LibraryDB, ids)
}

func bookSummary(book library.BookData) map[string]any {
	formats := append([]string{}, book.Formats...)
	return map[string]any{
		"book_id": book.ID, "title": book.Title, "authors": book.Authors, "tags": book.Tags,
		"publisher": book.Publisher, "series": book.Series, "series_index": book.SeriesIdx,
		"rating": book.Rating, "language": book.Language, "isbn": book.ISBN,
		"formats": formats, "format_sizes": book.FormatSizes,
	}
}

func (s *Service) search(ctx context.Context, principal Principal, args map[string]any) (any, error) {
	query := stringArg(args["query"])
	if query == "" || len([]rune(query)) > 200 {
		return nil, toolError("mcp.invalid_params", "搜索词必填且不能超过 200 字")
	}
	limit, offset, err := pageArgs(args, 20, 50)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var total int
	if principal.Access != nil && !principal.Access.Unrestricted() {
		query := library.AccessBrowseQuery{Search: query, BrowseQuery: library.BrowseQuery{Start: offset, Size: limit}}
		ids, total, err = library.AccessBrowseBooks(ctx, s.LibraryDB, *principal.Access, query)
		if err != nil {
			return nil, err
		}
		offset = min(offset, total)
	} else {
		ids, err = library.Search(s.LibraryDB, query)
		if err != nil {
			return nil, toolError("book.invalid_query", "搜索语法无效")
		}
		ids, err = s.visibleIDs(ctx, principal, ids)
		if err != nil {
			return nil, err
		}
		total = len(ids)
		offset = min(offset, total)
		ids = ids[offset:min(offset+limit, total)]
	}
	books, err := s.booksData(ctx, principal, ids)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(books))
	for _, book := range books {
		items = append(items, bookSummary(book))
	}
	return map[string]any{"books": items, "total": total, "offset": offset, "limit": limit, "has_more": offset+limit < total}, nil
}

func (s *Service) history(ctx context.Context, principal Principal, args map[string]any) (any, error) {
	limit, offset, err := pageArgs(args, 20, 50)
	if err != nil {
		return nil, err
	}
	state := stringArg(args["state"])
	if state == "" {
		state = "all"
	}
	conditions := map[string]string{
		"all": "", "unread": " AND read_state=0", "reading": " AND read_state=1",
		"finished": " AND read_state=2", "shelf": " AND wants=1", "favorite": " AND favorite=1",
	}
	condition, ok := conditions[state]
	if !ok {
		return nil, toolError("mcp.invalid_params", "阅读状态筛选无效")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT book_id,reader_id,COALESCE(favorite,0),COALESCE(favorite_date,''),COALESCE(wants,0),COALESCE(wants_date,''),COALESCE(read_state,0),COALESCE(read_date,''),COALESCE(online_read,0),COALESCE(download,0),COALESCE(progress,'{}'),COALESCE(progress_update_time,'') FROM reading_state WHERE reader_id=?`+condition+` ORDER BY MAX(COALESCE(favorite_date,''),COALESCE(wants_date,''),COALESCE(read_date,''),COALESCE(progress_update_time,'')) DESC,book_id DESC LIMIT 10000`, principal.UserID)
	if err != nil {
		return nil, err
	}
	var states []*userdb.ReadingState
	ids := []int64{}
	for rows.Next() {
		state := &userdb.ReadingState{}
		var progress string
		if err = rows.Scan(&state.BookID, &state.ReaderID, &state.Favorite, &state.FavoriteDate, &state.Wants, &state.WantsDate, &state.ReadState, &state.ReadDate, &state.OnlineRead, &state.Download, &progress, &state.ProgressUpdateTime); err != nil {
			rows.Close()
			return nil, err
		}
		state.Progress = map[string]any{}
		if json.Unmarshal([]byte(progress), &state.Progress) != nil {
			state.Progress = map[string]any{}
		}
		states = append(states, state)
		ids = append(ids, state.BookID)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, closeErr
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	visible, err := s.visibleIDs(ctx, principal, ids)
	if err != nil {
		return nil, err
	}
	allowed := map[int64]bool{}
	for _, id := range visible {
		allowed[id] = true
	}
	filtered := make([]*userdb.ReadingState, 0, len(states))
	visibleIDs := []int64{}
	for _, state := range states {
		if allowed[state.BookID] {
			filtered = append(filtered, state)
			visibleIDs = append(visibleIDs, state.BookID)
		}
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	books, err := s.booksData(ctx, principal, visibleIDs[offset:end])
	if err != nil {
		return nil, err
	}
	byID := map[int64]library.BookData{}
	for _, book := range books {
		byID[book.ID] = book
	}
	items := []map[string]any{}
	for _, state := range filtered[offset:end] {
		book, ok := byID[state.BookID]
		if !ok {
			continue
		}
		items = append(items, map[string]any{
			"book": bookSummary(book), "favorite": state.Favorite != 0, "on_shelf": state.Wants != 0,
			"read_state": state.ReadState, "read_status": []string{"unread", "reading", "finished"}[min(max(int(state.ReadState), 0), 2)],
			"progress": state.Progress, "progress_updated_at": state.ProgressUpdateTime,
			"read_at": state.ReadDate, "favorite_at": state.FavoriteDate, "shelf_at": state.WantsDate,
			"online_read_count": state.OnlineRead, "download_count": state.Download,
		})
	}
	return map[string]any{"history": items, "total": total, "offset": offset, "limit": limit, "has_more": end < total}, nil
}

func (s *Service) recommend(ctx context.Context, principal Principal, args map[string]any) (any, error) {
	request := stringArg(args["request"])
	if request == "" || len([]rune(request)) > 500 {
		return nil, toolError("mcp.invalid_params", "推荐要求必填且不能超过 500 字")
	}
	limit, _, err := pageArgs(map[string]any{"limit": args["limit"]}, 5, 20)
	if err != nil {
		return nil, err
	}
	seedRows, err := s.DB.QueryContext(ctx, `SELECT book_id FROM reading_state WHERE reader_id=? AND (favorite=1 OR wants=1 OR read_state>0) ORDER BY MAX(COALESCE(favorite_date,''),COALESCE(wants_date,''),COALESCE(read_date,''),COALESCE(progress_update_time,'')) DESC LIMIT 100`, principal.UserID)
	if err != nil {
		return nil, err
	}
	seeds := []int64{}
	for seedRows.Next() {
		var id int64
		if err = seedRows.Scan(&id); err != nil {
			seedRows.Close()
			return nil, err
		}
		seeds = append(seeds, id)
	}
	if closeErr := seedRows.Close(); closeErr != nil {
		return nil, closeErr
	}
	if err = seedRows.Err(); err != nil {
		return nil, err
	}
	seeds, err = s.visibleIDs(ctx, principal, seeds)
	if err != nil {
		return nil, err
	}
	candidates, err := library.RecommendationIDs(ctx, s.LibraryDB, seeds)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, seeds...)
	candidates, err = s.visibleIDs(ctx, principal, candidates)
	if err != nil {
		return nil, err
	}
	books, err := library.RecommendationMetadata(ctx, s.LibraryDB, candidates)
	if err != nil {
		return nil, err
	}
	states, err := userdb.ListReadingStates(s.DB, principal.UserID, candidates)
	if err != nil {
		return nil, err
	}
	seedSet := map[int64]bool{}
	for _, id := range seeds {
		seedSet[id] = true
	}
	weights := map[string]float64{}
	for _, book := range books {
		state := states[book.ID]
		if !seedSet[book.ID] || state == nil {
			continue
		}
		weight := 1.0
		if state.ReadState == 2 {
			weight = 2
		}
		if state.Wants != 0 {
			weight = 3
		}
		if state.Favorite != 0 {
			weight = 5
		}
		for _, term := range append(append(append([]string{}, book.Authors...), book.Tags...), book.Series) {
			weights[normalTerm(term)] += weight
		}
	}
	terms := requestTerms(request)
	type ranked struct {
		book    library.BookData
		score   float64
		reasons []string
	}
	rankedBooks := []ranked{}
	for _, book := range books {
		state := states[book.ID]
		if state != nil && (state.ReadState > 0 || len(state.Progress) > 0) {
			continue
		}
		score := float64(book.Rating) * 0.2
		reasons := []string{}
		for _, term := range append(append(append([]string{}, book.Authors...), book.Tags...), book.Series) {
			normalized := normalTerm(term)
			if normalized == "" {
				continue
			}
			if weights[normalized] > 0 {
				score += min(weights[normalized], 8)
				if len(reasons) < 3 {
					reasons = append(reasons, "与你的阅读偏好相符："+term)
				}
			}
			for _, wanted := range terms {
				if strings.Contains(normalized, wanted) || strings.Contains(wanted, normalized) {
					score += 5
					reasons = append([]string{"匹配你的要求：" + term}, reasons...)
					break
				}
			}
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "馆藏评分与阅读记录综合推荐")
		}
		rankedBooks = append(rankedBooks, ranked{book, score, uniqueStrings(reasons)})
	}
	sort.SliceStable(rankedBooks, func(i, j int) bool {
		if rankedBooks[i].score != rankedBooks[j].score {
			return rankedBooks[i].score > rankedBooks[j].score
		}
		return rankedBooks[i].book.ID > rankedBooks[j].book.ID
	})
	if len(rankedBooks) > limit {
		rankedBooks = rankedBooks[:limit]
	}
	ids := []int64{}
	for _, item := range rankedBooks {
		ids = append(ids, item.book.ID)
	}
	full, err := s.booksData(ctx, principal, ids)
	if err != nil {
		return nil, err
	}
	byID := map[int64]library.BookData{}
	for _, book := range full {
		byID[book.ID] = book
	}
	items := []map[string]any{}
	for _, item := range rankedBooks {
		items = append(items, map[string]any{"book": bookSummary(byID[item.book.ID]), "reasons": item.reasons})
	}
	return map[string]any{"request": request, "recommendations": items, "personalized": len(weights) > 0, "generated_at": time.Now().UTC().Format(time.RFC3339)}, nil
}

func normalTerm(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "unknown", "未知", "佚名", "none", "无", "未分类":
		return ""
	}
	return value
}
func requestTerms(request string) []string {
	fields := strings.FieldsFunc(strings.ToLower(request), func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("，。！？、；：,.!?;:", r) })
	terms := []string{}
	for _, field := range fields {
		if len([]rune(field)) >= 2 && !map[string]bool{"推荐": true, "几本": true, "不要": true, "书籍": true, "小说": true, "我想": true, "读过": true, "一些": true}[field] {
			terms = append(terms, field)
		}
	}
	return terms
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}
