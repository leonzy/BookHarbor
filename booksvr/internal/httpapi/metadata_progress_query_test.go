package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestMetadataProgressQueriesUseBoundedRetryCandidates(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	for i := int64(2); i <= 5; i++ {
		insertBook(t, w.libraryDB, i, "Book")
	}
	for id, status := range map[int64]string{1: "network_pending", 2: "partial", 3: "protected", 4: "complete", 5: "network_pending", 999: "network_pending"} {
		if err := m.saveJob(id, status, "", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO metadata_source_jobs(book_id,connection_id,attempts,outcome) VALUES(1,1,1,'source_failed'),(1,2,1,'source_failed'),(3,1,1,'source_failed'),(999,1,1,'source_failed')`,
		`INSERT INTO metadata_source_jobs(book_id,connection_id,attempts,last_error) VALUES(2,1,1,'来源请求失败')`,
		`INSERT INTO metadata_source_jobs(book_id,connection_id,attempts,outcome,phase) VALUES(4,1,1,'source_failed','done'),(5,1,0,'source_failed','search'),(5,2,1,'source_failed','done')`,
		`INSERT INTO metadata_enrichment_writes(book_id,field,origin,previous_value,written_value) VALUES(1,'title','network:foo','','a'),(1,'authors','network:bar','','b'),(2,'title','NETWORK:legacy','','c'),(3,'title','file','','d'),(999,'title','network:foo','','e')`,
	} {
		if _, err := w.libraryDB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := ensureMetadataProgressIndexes(w.libraryDB); err != nil {
			t.Fatal(err)
		}
	}
	result, err := MetadataEnrichmentStatus(w.libraryDB, ServeOptions{})(adminCtx(1))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"total_books": 5, "files_checked": 5, "network_retries": 2, "network_applied_books": 2, "written_books": 3} {
		if result[key] != want {
			t.Fatalf("%s: got %v, want %d", key, result[key], want)
		}
	}
	for name, query := range map[string]string{
		"metadata_progress_retry":          metadataRetryCountSQL,
		"metadata_progress_status":         metadataJobsStatusSQL,
		"metadata_progress_network_writes": metadataNetworkWriteCountSQL,
	} {
		rows, err := w.libraryDB.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !strings.Contains(plan, name) {
			t.Fatalf("%s not used: %s (%v)", name, plan, err)
		}
	}
}
