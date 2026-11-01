package localmeta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	textunicode "golang.org/x/text/encoding/unicode"
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

func TestLectureStructuredVariantsAndLogicalName(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		authors    []string
	}{
		{"030905避暑山庄百年史-王思治", "主讲人简介\n王思治 1938年生。\n内容简介\n避暑山庄百年史。", []string{"王思治"}},
		{"031104大脑的奥秘-沈政", "主讲简介：\n沈 政：大学教授。\n内容简介\n讲座正文。", []string{"沈政"}},
		{"030318物理学百年的回顾-周光召", "人物简介\n周光召院士，物理学家。\n内容简介\n讲座正文。", []string{"周光召"}},
		{"030130中国油气资源的二次创业-刘光鼎", "主讲人简介\n中国科学院院士，刘光鼎先生，1952年毕业。\n内容简介\n讲座正文。", []string{"刘光鼎"}},
		{"030911与健康手拉手-洪昭光", "主讲人简介\n洪昭光教授曾任医院院长。\n内容简介\n讲座正文。", []string{"洪昭光"}},
		{"030106小城镇 大战略-孔祥智", "主讲人简介\n　孔祥智：教授。\n内容简介\n小城镇，大战略\n全文\n讲座。", []string{"孔祥智"}},
		{"030110金融资产管理与发展-刘鸿儒等", "主讲人简介\n刘鸿儒：金融专家\n柏士珍：中国东方资产管理公司总裁\n唐 旭：博士生导师\n金 碚：中国社会科学院研究员\n职务：中国教授\n内容简介\n讲座介绍\n全文\n正文", []string{"刘鸿儒", "柏士珍", "唐旭", "金碚"}},
		{"030114建筑设计中的文化理念-彭一刚", "主讲人简介\n彭一刚，中国科学院院士。\n内容简介\n建筑创作理念。\n全文\n内容。", []string{"彭一刚"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lectureText(tc.name, tc.body)
			if got.Title == "" || strings.Join(got.Authors, "|") != strings.Join(tc.authors, "|") {
				t.Fatal(got)
			}
			if got := lectureText(tc.name, strings.ReplaceAll(strings.ReplaceAll(tc.body, tc.authors[0], "其他人"), "沈 政", "其他人")); got.Title != "" {
				t.Fatal("mismatched profile trusted", got)
			}
			for _, order := range []textunicode.Endianness{textunicode.LittleEndian, textunicode.BigEndian} {
				raw, err := textunicode.UTF16(order, textunicode.UseBOM).NewEncoder().Bytes([]byte(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(t.TempDir(), "temporary-member.txt")
				if err = os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				m := Supplement(context.Background(), file, "TXT", importmeta.Metadata{}, tc.name+".txt")
				if m.Title != got.Title || strings.Join(m.Authors, "|") != strings.Join(got.Authors, "|") {
					t.Fatal(m, got)
				}
			}
		})
	}
	if got := lectureText("030919存在与人生-何光沪", "这是小说正文，没有主讲人资料。《存在与人生》何光沪"); got.Title != "" {
		t.Fatal(got)
	}
}
