//go:build ignore

// Design-only SQL experiment. Uses synthetic data in a newly created temporary
// database; deliberately accepts no database path or production connection.
// Run: go -C booksvr run ../scripts/bench_acl_design.go -n 500000 -rounds 3
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type metric struct {
	Profile int     `json:"profile"`
	Case    string  `json:"case"`
	Mode    string  `json:"mode"`
	Samples int     `json:"samples"`
	Median  float64 `json:"median_ms"`
	Max     float64 `json:"max_ms"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func exec(db *sql.DB, q string, args ...any) { _, err := db.Exec(q, args...); must(err) }

func run(db *sql.DB, q string, args []any) []int64 {
	rows, err := db.Query(q, args...)
	must(err)
	defer rows.Close()
	var result []int64
	for rows.Next() {
		var id int64
		must(rows.Scan(&id))
		result = append(result, id)
	}
	must(rows.Err())
	return result
}

func measure(db *sql.DB, profile int, name, mode, q string, args []any, rounds int) metric {
	run(db, q, args) // warm cache, explicitly not a cold-storage benchmark
	times := []float64{}
	for i := 0; i < rounds; i++ {
		start := time.Now()
		run(db, q, args)
		times = append(times, float64(time.Since(start).Microseconds())/1000)
	}
	sort.Float64s(times)
	return metric{profile, name, mode, rounds, times[len(times)/2], times[len(times)-1]}
}

func main() {
	n := flag.Int("n", 500000, "synthetic book count, 1000..1000000")
	rounds := flag.Int("rounds", 3, "warm samples per query, 1..10")
	flag.Parse()
	if *n < 1000 || *n > 1000000 || *rounds < 1 || *rounds > 10 {
		panic("invalid bounded experiment size")
	}
	runtime.GOMAXPROCS(1)
	dir, err := os.MkdirTemp("", "bookharbor-acl-design-")
	must(err)
	path := filepath.Join(dir, "synthetic.db")
	defer func() {
		// Exact task-created files only, no recursive deletion or external paths.
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(path + suffix)
		}
		_ = os.Remove(dir)
	}()
	db, err := sql.Open("sqlite", path)
	must(err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	exec(db, `PRAGMA journal_mode=DELETE`)
	exec(db, `PRAGMA synchronous=NORMAL`)
	exec(db, `PRAGMA cache_size=-65536`)
	exec(db, `CREATE TABLE books(id INTEGER PRIMARY KEY,ts INTEGER,title TEXT,size INTEGER,author INTEGER)`)
	exec(db, `CREATE INDEX books_time ON books(ts DESC,id DESC)`)
	exec(db, `CREATE INDEX books_title ON books(title,id)`)
	exec(db, `CREATE INDEX books_size ON books(size DESC,id DESC)`)
	exec(db, `CREATE TABLE files(id INTEGER PRIMARY KEY,book INTEGER,format INTEGER,dir INTEGER,policy INTEGER,size INTEGER)`)
	exec(db, `CREATE INDEX files_book ON files(book,policy,size)`)
	exec(db, `CREATE INDEX files_policy ON files(policy,book)`)
	exec(db, `CREATE INDEX files_dir ON files(dir,book)`)
	exec(db, `CREATE TABLE allowed(profile INTEGER,policy INTEGER,PRIMARY KEY(profile,policy)) WITHOUT ROWID`)
	start := time.Now()
	tx, err := db.Begin()
	must(err)
	b, err := tx.Prepare(`INSERT INTO books VALUES(?,?,?,?,?)`)
	must(err)
	f, err := tx.Prepare(`INSERT INTO files VALUES(?,?,?,?,?,?)`)
	must(err)
	for i := 1; i <= *n; i++ {
		size := int64(10000 + (i*8713)%15000000)
		maxSize := size
		if i%4 == 0 {
			maxSize = size + 700000
		}
		_, err = b.Exec(i, (i*7919)%1000000, fmt.Sprintf("书号%07d", (i*2999)%(*n)), maxSize, i%20000)
		must(err)
		_, err = f.Exec(i*2, i, 1, i%1000, i%1000, size)
		must(err)
		if i%4 == 0 {
			policy := (i*17 + 5) % 1000
			_, err = f.Exec(i*2+1, i, 2, policy, policy, maxSize)
			must(err)
		}
	}
	must(b.Close())
	must(f.Close())
	must(tx.Commit())
	for profile, limit := range map[int]int{1: 900, 2: 100, 3: 10} {
		tx, err = db.Begin()
		must(err)
		for policy := 0; policy < limit; policy++ {
			_, err = tx.Exec(`INSERT INTO allowed VALUES(?,?)`, profile, policy)
			must(err)
		}
		must(tx.Commit())
	}
	exec(db, `ANALYZE`)
	seedSeconds := time.Since(start).Seconds()
	var pages, pageSize int64
	must(db.QueryRow(`PRAGMA page_count`).Scan(&pages))
	must(db.QueryRow(`PRAGMA page_size`).Scan(&pageSize))
	baseBytes := pages * pageSize
	exec(db, `CREATE TABLE visible_file(profile INTEGER,file INTEGER,book INTEGER,format INTEGER,dir INTEGER,size INTEGER,PRIMARY KEY(profile,file)) WITHOUT ROWID`)
	exec(db, `CREATE INDEX visible_file_dir ON visible_file(profile,dir,book)`)
	exec(db, `CREATE INDEX visible_file_book ON visible_file(profile,book)`)
	exec(db, `CREATE TABLE visible_book(profile INTEGER,book INTEGER,ts INTEGER,title TEXT,maxsize INTEGER,PRIMARY KEY(profile,book)) WITHOUT ROWID`)
	exec(db, `CREATE INDEX visible_time ON visible_book(profile,ts DESC,book DESC)`)
	exec(db, `CREATE INDEX visible_title ON visible_book(profile,title,book)`)
	exec(db, `CREATE INDEX visible_size ON visible_book(profile,maxsize DESC,book DESC)`)
	exec(db, `CREATE INDEX visible_reverse ON visible_book(book,profile)`)
	exec(db, `CREATE TABLE totals(profile INTEGER PRIMARY KEY,n INTEGER)`)
	build := map[int]map[string]any{}
	for profile := 1; profile <= 3; profile++ {
		start = time.Now()
		exec(db, `INSERT INTO visible_file SELECT ?,f.id,f.book,f.format,f.dir,f.size FROM allowed a JOIN files f ON f.policy=a.policy WHERE a.profile=?`, profile, profile)
		exec(db, `INSERT INTO visible_book SELECT ?,b.id,b.ts,b.title,max(v.size) FROM visible_file v JOIN books b ON b.id=v.book WHERE v.profile=? GROUP BY b.id`, profile, profile)
		exec(db, `INSERT INTO totals SELECT ?,count(*) FROM visible_book WHERE profile=?`, profile, profile)
		var count int64
		must(db.QueryRow(`SELECT n FROM totals WHERE profile=?`, profile).Scan(&count))
		build[profile] = map[string]any{"visible_books": count, "seconds": time.Since(start).Seconds()}
	}
	exec(db, `ANALYZE`)
	must(db.QueryRow(`PRAGMA page_count`).Scan(&pages))
	projectedBytes := pages*pageSize - baseBytes
	results := []metric{}
	results = append(results, measure(db, 0, "newest", "unrestricted", `SELECT id FROM books ORDER BY ts DESC,id DESC LIMIT 50`, nil, *rounds))
	results = append(results, measure(db, 0, "contains", "unrestricted", `SELECT count(*) FROM books WHERE instr(title,'00012')>0`, nil, *rounds))
	for profile := 1; profile <= 3; profile++ {
		gate := `EXISTS(SELECT 1 FROM files f JOIN allowed a ON a.policy=f.policy AND a.profile=? WHERE f.book=b.id)`
		cases := []struct{ name, dynamic, projected string }{
			{"count", `SELECT count(*) FROM books b WHERE ` + gate, `SELECT count(*) FROM visible_book WHERE profile=?`},
			{"count_cached", `SELECT count(*) FROM books b WHERE ` + gate, `SELECT n FROM totals WHERE profile=?`},
			{"newest", `SELECT b.id FROM books b WHERE ` + gate + ` ORDER BY b.ts DESC,b.id DESC LIMIT 50`, `SELECT book FROM visible_book WHERE profile=? ORDER BY ts DESC,book DESC LIMIT 50`},
			{"title", `SELECT b.id FROM books b WHERE ` + gate + ` ORDER BY b.title,b.id LIMIT 50`, `SELECT book FROM visible_book WHERE profile=? ORDER BY title,book LIMIT 50`},
			{"size", `SELECT b.id FROM books b JOIN files f ON f.book=b.id JOIN allowed a ON a.policy=f.policy WHERE a.profile=? GROUP BY b.id ORDER BY max(f.size) DESC,b.id DESC LIMIT 50`, `SELECT book FROM visible_book WHERE profile=? ORDER BY maxsize DESC,book DESC LIMIT 50`},
			{"contains", `SELECT count(*) FROM books b WHERE instr(b.title,'00012')>0 AND ` + gate, `SELECT count(*) FROM visible_book WHERE profile=? AND instr(title,'00012')>0`},
			{"directory", `SELECT DISTINCT f.book FROM files f JOIN allowed a ON a.policy=f.policy WHERE a.profile=? AND f.dir=5 ORDER BY f.book LIMIT 50`, `SELECT DISTINCT book FROM visible_file WHERE profile=? AND dir=5 ORDER BY book LIMIT 50`},
		}
		for _, c := range cases {
			args := []any{profile}
			x, y := run(db, c.dynamic, args), run(db, c.projected, args)
			if !reflect.DeepEqual(x, y) {
				panic(fmt.Sprintf("result mismatch profile %d case %s", profile, c.name))
			}
			results = append(results, measure(db, profile, c.name, "runtime_rules", c.dynamic, args, *rounds))
			results = append(results, measure(db, profile, c.name, "projection", c.projected, args, *rounds))
			if c.name == "contains" {
				q := `SELECT count(*) FROM books b WHERE instr(b.title,'00012')>0 AND EXISTS(SELECT 1 FROM visible_book v WHERE v.profile=? AND v.book=b.id)`
				if !reflect.DeepEqual(x, run(db, q, args)) {
					panic("candidate-first projection mismatch")
				}
				results = append(results, measure(db, profile, c.name, "candidate_index", q, args, *rounds))
			}
		}
	}
	plans := map[string][]string{}
	for name, q := range map[string]string{
		"runtime_count":      `SELECT count(*) FROM books b WHERE EXISTS(SELECT 1 FROM files f JOIN allowed a ON a.policy=f.policy AND a.profile=1 WHERE f.book=b.id)`,
		"projected_size":     `SELECT book FROM visible_book WHERE profile=1 ORDER BY maxsize DESC,book DESC LIMIT 50`,
		"candidate_contains": `SELECT count(*) FROM books b WHERE instr(b.title,'00012')>0 AND EXISTS(SELECT 1 FROM visible_book v WHERE v.profile=1 AND v.book=b.id)`,
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + q)
		must(err)
		for rows.Next() {
			var a, b, c int
			var detail string
			must(rows.Scan(&a, &b, &c, &detail))
			plans[name] = append(plans[name], detail)
		}
		must(rows.Err())
		must(rows.Close())
	}
	var sqliteVersion string
	must(db.QueryRow(`SELECT sqlite_version()`).Scan(&sqliteVersion))
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"books": *n, "sqlite": sqliteVersion, "goos": runtime.GOOS, "goarch": runtime.GOARCH, "gomaxprocs": 1, "cache": "warm local synthetic DB, 64MiB SQLite page cache; not HTTP or NAS cold P95", "seed_seconds": seedSeconds, "base_bytes": baseBytes, "projection_extra_bytes_three_profiles": projectedBytes, "profile_build": build, "go_heap_alloc_bytes": memory.HeapAlloc, "equal_results": true, "measurements": results, "query_plans": plans})
}
