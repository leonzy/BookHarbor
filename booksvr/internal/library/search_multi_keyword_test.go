package library

import (
	"testing"
)

func TestSearchMultipleKeywords(t *testing.T) {
	db := newQueryDB(t)
	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"三体 II", []int64{2}},
		{"三体 科幻", []int64{1, 2}},
		{"三体\t\r\nII", []int64{2}},
		{"三体　II", []int64{2}},
		{"三体 NOT II", []int64{1}},
		{"活着 OR 三体 II", []int64{2, 3}},
		{"(活着 OR 三体) II", []int64{2}},
		{"三体 author:刘慈欣", []int64{1, 2}},
		{`"三体 II" 科幻`, []int64{2}},
		{`'三体 II'`, []int64{2}},
		{"三体 不存在", nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := Search(db, tc.query)
			if err != nil || !idsEqual(got, tc.want) {
				t.Fatalf("Search(%q)=%v,%v want %v", tc.query, got, err, tc.want)
			}
		})
	}
	for _, q := range []string{`"未闭合`, "三体 AND", "三体 OR OR 活着", "三体)", "三体 (II", "author:", "unknown:abc"} {
		if _, err := Search(db, q); err == nil {
			t.Errorf("expected syntax error for %q", q)
		}
	}
}
