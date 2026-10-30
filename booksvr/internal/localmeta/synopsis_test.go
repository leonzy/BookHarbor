package localmeta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talebook/booksvr/internal/importmeta"
)

func TestExplicitSynopsisBoundaries(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"下载广告\r\n【内容简介】\r\n　　甲探索星空。\r\n乙同行。\r\n\r\n相关设定\r\n不可当简介", "甲探索星空。\n乙同行。"},
		{"[作品简介]\n一段简介\n第0001章 正文\n不能提取", "一段简介"},
		{"简介：<b>只是文本</b>\n目录\n第一章", "&lt;b&gt;只是文本&lt;/b&gt;"},
		{"第一章 初见\n他拿起书说：\n简介：这是人物台词", ""},
		{"正文没有标签，不能猜简介", ""},
		{"【内容简介】\n" + strings.Repeat("长", 4001), ""},
	} {
		if got := FromText(tc.text).Comments; got != tc.want {
			t.Errorf("synopsis = %q, want %q", got, tc.want)
		}
	}
}

func TestTXTCanonicalFilenameAndMissingSynopsis(t *testing.T) {
	name := "《星海之旅》（校对版全本）作者：作者甲"
	p := filepath.Join(t.TempDir(), name+".txt")
	if err := os.WriteFile(p, []byte("【内容简介】\n一段可靠的简介。\n\n相关设定\n忽略"), 0600); err != nil {
		t.Fatal(err)
	}
	m := Supplement(context.Background(), p, "TXT", importmeta.Metadata{})
	if m.Title != "星海之旅" || len(m.Authors) != 1 || m.Authors[0] != "作者甲" || m.Comments != "一段可靠的简介。" || m.ISBN != "" || m.Publisher != "" {
		t.Fatalf("%+v", m)
	}
	m = Supplement(context.Background(), p, "txt", importmeta.Metadata{Title: "人工书名", ISBN: "9780141439518", Publisher: "出版社", Authors: []string{"作者乙"}})
	if m.Title != "人工书名" || m.Comments != "一段可靠的简介。" || m.Authors[0] != "作者乙" {
		t.Fatalf("%+v", m)
	}
	for _, title := range []string{"《书名》（新版）", "含作者的普通书名", "书名：副标题"} {
		if FilenameTitle(title) != title {
			t.Fatalf("changed legitimate title %q", title)
		}
	}
}
