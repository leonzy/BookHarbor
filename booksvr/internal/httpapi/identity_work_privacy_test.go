package httpapi

import "testing"

func TestIdentityWorkSummaryDoesNotExposePrivateOrigin(t *testing.T) {
	db := newTestDB(t)
	work, a, b := seedWork(t, db)
	if _, e := db.Exec(`UPDATE works SET display_title='私藏备注',display_author='私有作者',title_norm='private',author_norm='private' WHERE id=?`, work); e != nil {
		t.Fatal(e)
	}
	svc := WorkService{DB: db, IsPublic: func(id int64) (bool, error) { return id == a, nil }}
	result, e := svc.WorkDetail(work)
	if e != nil || result.Err != "ok" || result.Work.Title != "三体" || result.Work.Author != "刘慈欣" || result.Work.TitleNorm == "private" || result.Work.VersionCount != 1 {
		t.Fatal(result, e)
	}
	hidden, e := svc.BookVersions(b)
	if e != nil || hidden.Err != ErrBookNotFound || hidden.WorkID != 0 || len(hidden.Versions) > 0 {
		t.Fatal(hidden, e)
	}
	svc.IsPublic = func(int64) (bool, error) { return false, nil }
	empty, e := svc.WorkDetail(work)
	if e != nil || empty.Err != ErrWorkNotFound {
		t.Fatal(empty, e)
	}
}
func TestIdentityUngroupedBookHasEmptyVersions(t *testing.T) {
	db := newTestDB(t)
	db.Exec(`INSERT INTO books(id,title,author_sort) VALUES(100,'单独作品','作者')`)
	svc := WorkService{DB: db}
	result, e := svc.BookVersions(100)
	if e != nil || result.Err != "ok" || result.WorkID != 0 || len(result.Versions) != 0 {
		t.Fatal(result, e)
	}
}
