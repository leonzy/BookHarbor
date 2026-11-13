package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"talebook/booksvr/internal/access"
)

// InheritAccessSource runs inside the target format registration transaction.
// Source facts and inherited rule IDs are committed before the new format can
// enter any active projection. Independent imports do not call this guard.
func InheritAccessSource(sourceBook int64, sourceFormat string, targetBook int64, targetFormat, root string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		var available bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='acl_format_lineage')`).Scan(&available); err != nil {
			return err
		}
		if !available {
			return nil
		}
		books, err := AccessSourceReader(root)(context.Background(), tx, []int64{sourceBook})
		if err != nil {
			return err
		}
		var revision, version int64
		var raw string
		var policy access.Policy
		err = tx.QueryRow(`SELECT r.id,r.version,r.policy FROM acl_revision r JOIN acl_state s ON s.active_revision=r.id WHERE s.id=1`).Scan(&revision, &version, &raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if err = json.Unmarshal([]byte(raw), &policy); err != nil {
				return err
			}
		}
		parents := []access.FileFacts{}
		classes := []string{}
		if revision > 0 {
			rows, e := tx.Query(`SELECT class_id FROM acl_class_membership WHERE revision=? AND book_id=?`, revision, sourceBook)
			if e != nil {
				return e
			}
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				classes = append(classes, id)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		for _, book := range books {
			for _, file := range book.Files {
				if sourceFormat != "" && !strings.EqualFold(file.Format, sourceFormat) {
					continue
				}
				facts := file.Facts
				facts.Classes = append(facts.Classes, classes...)
				facts.Ready = facts.Root != ""
				if revision > 0 {
					facts.Inherited = append(facts.Inherited, policy.MatchingOriginRules(revision, version, facts)...)
				}
				parents = append(parents, facts)
			}
		}
		if len(parents) == 0 || len(parents) > 32 {
			return ErrAccessNotReady
		}
		data, err := json.Marshal(parents)
		if err != nil {
			return err
		}
		if len(data) > 1<<20 {
			return ErrAccessNotReady
		}
		_, err = tx.Exec(`INSERT INTO acl_format_lineage(book_id,format,facts) VALUES(?,?,?) ON CONFLICT(book_id,format) DO UPDATE SET facts=excluded.facts`, targetBook, strings.ToUpper(targetFormat), string(data))
		return err
	}
}
