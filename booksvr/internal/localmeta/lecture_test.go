package localmeta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"talebook/booksvr/internal/importmeta"
)

func TestLectureTXTRequiresFilenameAndBodyEvidence(t *testing.T) {
	const text = "主讲人简介：\n\n　何光沪，1950年生。\n内容简介：\n讲座简介。\n《存在与人生》（全文）\n正文。"
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"030919存在与人生-何光沪", text, true},
		{"030919存在与人生-何光沪", strings.ReplaceAll(text, "《存在与人生》", "《其他讲座》"), false},
		{"030919存在与人生-何光沪", strings.ReplaceAll(text, "何光沪，", "张三，"), false},
		{"030919存在与人生-何光沪", strings.ReplaceAll(text, "主讲人简介：", "人物故事："), false},
		{"030919存在与人生-何光沪", strings.ReplaceAll(text, "何光沪，", "何光沪先生，"), false},
		{"031332存在与人生-何光沪", text, false},
		{"1984-何光沪", text, false},
		{"存在与人生-何光沪", text, false},
		{"三体-黑暗森林", text, false},
	} {
		hint := lectureText(tc.name, tc.text)
		if (hint.Title != "") != tc.valid {
			t.Fatal(tc.name, hint)
		}
	}
	file := filepath.Join(t.TempDir(), "030919存在与人生-何光沪.txt")
	raw, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, initial := range []importmeta.Metadata{{}, {Title: "030919存在与人生-何光沪", Authors: []string{"佚名"}}} {
		got := Supplement(context.Background(), file, "TXT", initial)
		if got.Title != "存在与人生" || len(got.Authors) != 1 || got.Authors[0] != "何光沪" || got.Pubdate != "" {
			t.Fatal(got)
		}
	}
	got := Supplement(context.Background(), file, "TXT", importmeta.Metadata{Title: "明确书名", Authors: []string{"明确作者"}})
	if got.Title != "明确书名" || got.Authors[0] != "明确作者" {
		t.Fatal("existing metadata replaced", got)
	}
}
