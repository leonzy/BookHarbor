package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestTxtReaderInitializesAndReadsChapters(t *testing.T) {
	w, db := newWriteFixture(t)
	insertBook(t, db, 1, "测试小说")
	insertItem(t, db, 1, "public", 1)
	path := filepath.Join(t.TempDir(), "novel.txt")
	body := "第一章 开始\n你好<script>alert(1)</script>\n第二章 后续\n再见\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	seedFormat(t, db, 1, "TXT", path)
	id := seedReader(t, db, "txt-reader", "test-password", true, true)
	ctx := adminCtx(id)
	ctx.DB = db
	ctx.Request = httptest.NewRequest(http.MethodGet, rBookTxtInit+"?id=1&test=0", nil)
	resp, err := BookTxtInitHandler(w.Config(""))(ctx)
	if err != nil || resp["err"] != "ok" {
		t.Fatalf("init: %v %v", resp, err)
	}
	chapters := resp["data"].(map[string]any)["content"].([]map[string]any)
	if len(chapters) != 2 || chapters[1]["title"] != "第二章 后续" {
		t.Fatalf("toc: %v", chapters)
	}
	ctx.Request.URL.Path = rBookTxtRead
	ctx.Request.URL.RawQuery = "id=1&start=0&end=" + fmt.Sprint(chapters[0]["end"])
	resp, err = w.bookReadTxt(ctx, 1)
	content, _ := resp["content"].(string)
	if err != nil || !strings.Contains(content, "你好&lt;script&gt;") || strings.Contains(content, "第二章") {
		t.Fatalf("chapter: %v %v", resp, err)
	}
	if _, leaked := resp["raw_text"]; leaked {
		t.Fatal("full text leaked in chapter response")
	}
}

func TestTxtPrintedTOCPointsToLaterBodyAndKeepsEveryByte(t *testing.T) {
	titles := []string{"第一章 开始", "第二章 继续", "第三章 转折", "第四章 结尾"}
	text := "作品简介\n"
	for _, title := range titles {
		text += title + "\n\n"
	}
	text += "目录后的短说明\n"
	for i, title := range titles {
		text += title + " \n"
		if i == 0 {
			text += "第一卷 组织篇\n"
		}
		text += strings.Repeat("真实正文", 30) + "\n"
	}
	chapters := txtChapters(text)
	if len(chapters) != 5 {
		t.Fatal(chapters)
	}
	var restored strings.Builder
	for i, c := range chapters {
		start, end := c["start"].(int), c["end"].(int)
		restored.WriteString(text[start:end])
		if i > 0 && !strings.Contains(text[start:end], "真实正文") {
			t.Fatal("TOC entry still used as chapter", c)
		}
		if i > 0 && start != chapters[i-1]["end"] {
			t.Fatal("non-contiguous offsets")
		}
	}
	if restored.String() != text {
		t.Fatal("source bytes lost")
	}
}

func TestTxtPrintedTOCDoesNotHideRealRepeatedOrShortChapters(t *testing.T) {
	for _, body := range []string{strings.Repeat("真正的章节内容", 30), "短诗"} {
		text := ""
		for n := 0; n < 2; n++ {
			for _, title := range []string{"第一章 开始", "第二章 继续", "第三章 结束"} {
				text += title + "\n" + body + "\n"
			}
		}
		if got := txtPrintedTOC(text); len(got) != 0 {
			t.Fatal("real repeat mistaken for TOC", got)
		}
	}
	late := strings.Repeat("序文", 33000) + "\n第一章\n第二章\n第三章\n"
	for _, title := range []string{"第一章", "第二章", "第三章"} {
		late += title + "\n" + strings.Repeat("正文", 80) + "\n"
	}
	if got := txtPrintedTOC(late); len(got) != 0 {
		t.Fatal("middle-of-book headings treated as front matter", got)
	}
	text := strings.Repeat("第一章\n第二章\n第三章\n", 2)
	if got := txtPrintedTOC(text); len(got) != 0 {
		t.Fatal("empty source invented a body", got)
	}
	if got := txtPrintedTOC("第一章\n\n第一章\n" + strings.Repeat("正文", 80)); len(got) != 0 {
		t.Fatal("isolated repeat removed", got)
	}
}

func TestTxtInitVisibilityAndGuestPermission(t *testing.T) {
	w, db := newWriteFixture(t)
	owner := seedReader(t, db, "txt-owner", "test-password", true, true)
	insertBook(t, db, 1, "私藏小说")
	insertItem(t, db, 1, "private", owner)
	path := filepath.Join(t.TempDir(), "novel.txt")
	if err := os.WriteFile(path, []byte("第一章 开始\n正文"), 0600); err != nil {
		t.Fatal(err)
	}
	seedFormat(t, db, 1, "TXT", path)
	for _, test := range []struct {
		name      string
		user      int64
		guestRead bool
		want      string
	}{
		{"guest disabled", 0, false, "user.need_login"},
		{"private guest", 0, true, "permission"},
		{"owner", owner, false, "ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := w.Config("")
			cfg.AllowGuestRead = test.guestRead
			ctx := &Context{DB: db, Identity: Identity{UserID: test.user}, Request: httptest.NewRequest(http.MethodGet, rBookTxtInit+"?id=1", nil)}
			resp, err := BookTxtInitHandler(cfg)(ctx)
			if err != nil || resp["err"] != test.want {
				t.Fatalf("%v %v", resp, err)
			}
		})
	}
	if _, err := db.Exec(`UPDATE items SET scope='public' WHERE book_id=1`); err != nil {
		t.Fatal(err)
	}
	cfg := w.Config("")
	cfg.AllowGuestRead = true
	ctx := &Context{DB: db, Request: httptest.NewRequest(http.MethodGet, rBookTxtInit+"?id=1", nil)}
	resp, err := BookTxtInitHandler(cfg)(ctx)
	if err != nil || resp["err"] != "ok" {
		t.Fatalf("public guest: %v %v", resp, err)
	}
}
