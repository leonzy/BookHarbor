package httpapi

import (
	"math"
	"sort"
	"strings"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
	"time"
)

func recommendationTerms(b library.BookData) [3][]string {
	return [3][]string{b.Authors, b.Tags, {b.Series}}
}
func recommendationTerm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "unknown", "未知", "佚名", "none", "无", "未分类":
		return ""
	}
	return s
}

func personalizedIndex(cfg BookConfig, ctx *Context) (map[string]any, error) {
	private, err := bookPrivateIDs(cfg, ctx)
	if err != nil {
		return nil, err
	}
	count := func(name, key string) int {
		n := toIntSetting(cfg.Settings, key, 12)
		if n < 0 {
			n = 0
		}
		if n > 200 {
			n = 200
		}
		if ctx.Query(name) != "" {
			v := bookIndexCount(ctx, name, n)
			if v < n {
				n = v
			}
		}
		return n
	}
	nRecent, nRec := count("recent", "MAIN_PAGE_RECENT_COUNT"), count("random", "MAIN_PAGE_RANDOM_COUNT")
	// The newest list uses indexed order, rather than sampling from the newest 100.
	recent, total, err := cfg.BrowsePage(ctx, library.BrowseQuery{Excluded: private, Start: 0, Size: nRecent, TimestampSort: true})
	if err != nil {
		return nil, err
	}
	if nRecent == 0 {
		recent = nil
	}
	seeds := []int64{}
	if ctx.Identity.UserID > 0 {
		rows, e := cfg.DB.QueryContext(ctx.Request.Context(), `SELECT book_id FROM reading_state WHERE reader_id=? AND (favorite=1 OR wants=1 OR read_state>0) ORDER BY MAX(COALESCE(favorite_date,''),COALESCE(wants_date,''),COALESCE(read_date,''),COALESCE(progress_update_time,'')) DESC LIMIT 100`, ctx.Identity.UserID)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return nil, e
			}
			if !private[id] {
				seeds = append(seeds, id)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	selected := []int64{}
	if nRec > 0 {
		candidates, e := library.RecommendationIDs(ctx.Request.Context(), cfg.LibraryDB, seeds)
		if e != nil {
			return nil, e
		}
		hot, e := userdb.ListRecommendationPopularity(cfg.DB)
		if e != nil {
			return nil, e
		}
		all := append(append(candidates, seeds...), hot.IDs...)
		filtered := []int64{}
		seen := map[int64]bool{}
		for _, id := range all {
			if !seen[id] && !private[id] {
				seen[id] = true
				filtered = append(filtered, id)
			}
		}
		books, e := library.RecommendationMetadata(ctx.Request.Context(), cfg.LibraryDB, filtered)
		if e != nil {
			return nil, e
		}
		states, e := userdb.ListReadingStates(cfg.DB, ctx.Identity.UserID, filtered)
		if e != nil {
			return nil, e
		}
		selected = rankRecommendations(books, states, hot.Counts, nRec, time.Now().Unix())
	}
	render := func(ids []int64) (any, error) {
		v, e := bookRenderResolvedPage(cfg, ctx, ids, "", len(ids), false)
		if e != nil {
			return nil, e
		}
		return v["books"], nil
	}
	rb, err := render(selected)
	if err != nil {
		return nil, err
	}
	nb, err := render(recent)
	if err != nil {
		return nil, err
	}
	return map[string]any{"err": "ok", "visible_books_count": total, "recommendations_enabled": nRec > 0, "random_books_count": len(selected), "new_books_count": len(recent), "random_books": rb, "new_books": nb}, nil
}

func rankRecommendations(books []library.BookData, states map[int64]*userdb.ReadingState, hot map[int64]int64, n int, now int64) []int64 {
	profiles := [3]map[string]float64{{}, {}, {}}
	weights := [3]float64{10, 8, 12}
	hasProfile := false
	for _, b := range books {
		if s := states[b.ID]; s != nil {
			w := 0.
			if s.ReadState > 0 {
				w = 1
			}
			if s.ReadState == 2 {
				w = 2
			}
			if s.Wants != 0 {
				w = 3
			}
			if s.Favorite != 0 {
				w = 5
			}
			if w > 0 {
				for i, terms := range recommendationTerms(b) {
					for _, v := range terms {
						if t := recommendationTerm(v); t != "" {
							profiles[i][t] += w
							hasProfile = true
						}
					}
				}
			}
		}
	}
	maxes := [3]float64{1, 1, 1}
	for i, p := range profiles {
		for _, v := range p {
			if v > maxes[i] {
				maxes[i] = v
			}
		}
	}
	type candidate struct {
		b           library.BookData
		score, base float64
	}
	pool := []candidate{}
	for _, b := range books {
		if s := states[b.ID]; s != nil && (s.ReadState > 0 || len(s.Progress) > 0) {
			continue
		}
		score := float64(hot[b.ID])
		if hasProfile {
			score = math.Min(2, math.Log1p(float64(hot[b.ID]))/4)
			for i, terms := range recommendationTerms(b) {
				best := 0.
				for _, v := range terms {
					if x := profiles[i][recommendationTerm(v)]; x > best {
						best = x
					}
				}
				score += weights[i] * best / maxes[i]
			}
			score += float64(b.Rating) * .6
			age := math.Max(0, float64(now-b.Timestamp)) / 86400
			score += 1 / (1 + age/90)
		}
		pool = append(pool, candidate{b, score, score})
	}
	result := []int64{}
	authors, series := map[string]int{}, map[string]int{}
	for len(pool) > 0 && len(result) < n {
		sort.SliceStable(pool, func(i, j int) bool {
			a, b := pool[i], pool[j]
			if a.score != b.score {
				return a.score > b.score
			}
			if a.b.Rating != b.b.Rating && !hasProfile {
				return a.b.Rating > b.b.Rating
			}
			if a.b.Timestamp != b.b.Timestamp {
				return a.b.Timestamp > b.b.Timestamp
			}
			return a.b.ID > b.b.ID
		})
		best := pool[0]
		pool = pool[1:]
		result = append(result, best.b.ID)
		if hasProfile {
			for _, v := range best.b.Authors {
				authors[recommendationTerm(v)]++
			}
			if t := recommendationTerm(best.b.Series); t != "" {
				series[t]++
			}
			for i := range pool {
				a := 0
				for _, v := range pool[i].b.Authors {
					if authors[recommendationTerm(v)] > a {
						a = authors[recommendationTerm(v)]
					}
				}
				penalty := float64(a*4 + series[recommendationTerm(pool[i].b.Series)]*6)
				pool[i].score = pool[i].base - penalty
			}
		}
	}
	return result
}
