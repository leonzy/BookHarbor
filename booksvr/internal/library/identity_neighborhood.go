package library

import (
	"fmt"
	"talebook/booksvr/internal/bookidentity"
)

// Explore the bounded title/ISBN neighborhood, including conflicting authors.
// Discarding them before selection hides ambiguous bridges from other entry IDs.
func identityNeighborhood(q identityReader, root IdentityBook) ([]IdentityBook, error) {
	out := []IdentityBook{root}
	seen := map[int64]bool{root.ID: true}
	cache := map[int64]IdentityBook{root.ID: root}
	protected := map[int64]bool{}
	for i := 0; i < len(out); i++ {
		current := out[i]
		ids, err := identityCandidates(q, current.Metadata)
		if err != nil {
			return nil, err
		}
		unit := map[int64]bool{}
		if current.WorkID > 0 {
			rows, err := q.Query(`SELECT id FROM books WHERE work_id=? ORDER BY id LIMIT 65`, current.WorkID)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id int64
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				unit[id] = true
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			b, ok := cache[id]
			if !ok {
				if len(cache) >= 128 {
					return nil, fmt.Errorf("%w: 邻域读取超过128条", ErrIdentityAmbiguous)
				}
				b, err = identityBook(q, id)
				if err != nil {
					return nil, err
				}
				cache[id] = b
				locked, err := identityProtected(q, b)
				if err != nil {
					return nil, err
				}
				protected[id] = locked
			}
			if protected[id] {
				continue
			}
			if !unit[id] && bookidentity.Compare(current.Metadata, b.Metadata).Reason == "title_conflict" {
				continue
			}
			if !unit[id] && b.Metadata.ISBN == "!conflict" {
				continue
			}
			if len(out) >= 64 {
				return nil, fmt.Errorf("%w: 候选超过64条", ErrIdentityAmbiguous)
			}
			out = append(out, b)
			seen[id] = true
		}
	}
	return out, nil
}
