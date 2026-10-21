package userdb

import "database/sql"

type RecommendationPopularity struct {
	IDs    []int64
	Counts map[int64]int64
}

func ListRecommendationPopularity(db *sql.DB) (RecommendationPopularity, error) {
	out := RecommendationPopularity{IDs: []int64{}, Counts: map[int64]int64{}}
	rows, err := db.Query(`SELECT book_id,COALESCE(count_visit,0)+2*COALESCE(count_download,0) AS heat FROM items ORDER BY heat DESC,book_id DESC LIMIT 512`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, heat int64
		if err := rows.Scan(&id, &heat); err != nil {
			return out, err
		}
		out.IDs = append(out.IDs, id)
		out.Counts[id] = heat
	}
	return out, rows.Err()
}
