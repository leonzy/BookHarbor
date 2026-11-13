package localmeta

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/importmeta"
	"testing"
)

func TestSignedSelfPrefaceSuppliesAuthorOnly(t *testing.T) {
	text := "前言\n《登天的感觉》记录了我的咨询工作。\n写这本书，是我学习时的梦想。\n岳晓东\n1996年冬\n于香港寓所\n"
	if got := prefaceAuthor(text, "登天的感觉"); got != "岳晓东" {
		t.Fatal(got)
	}
	for _, bad := range []string{
		strings.ReplaceAll(text, "前言", "第一章"),
		strings.ReplaceAll(text, "写这本书，是我学习时的梦想。", "我为此书作序。"),
		strings.ReplaceAll(text, "《登天的感觉》", "《另一部书》"),
		strings.ReplaceAll(text, "1996年冬\n于香港寓所", "一个普通故事"),
		text + "\n前言\n《登天的感觉》\n我写这本书\n另一作者\n2000年冬\n于北京寓所\n",
	} {
		if got := prefaceAuthor(bad, "登天的感觉"); got != "" {
			t.Fatalf("unsafe evidence: %q", got)
		}
	}
	if m := FromText(text); m.Pubdate != "" || m.ISBN != "" {
		t.Fatal(m)
	}
}

func TestPalmPrefaceAfterThirtyTwoRecordsAndTrailers(t *testing.T) {
	text := strings.Repeat("<p>front matter</p>", 9000) + "<h1>前言</h1><p>《登天的感觉》记录我的工作。<br/>写这本书，是我学习时的梦想。<br/>岳晓东<br/>1996年冬<br/>于香港寓所</p>"
	count := (len(text) + 4095) / 4096
	offset := 78 + (count+1)*8
	data := make([]byte, offset+260)
	binary.BigEndian.PutUint16(data[76:], uint16(count+1))
	binary.BigEndian.PutUint32(data[78:], uint32(offset))
	binary.BigEndian.PutUint16(data[offset:], 1)
	binary.BigEndian.PutUint16(data[offset+8:], uint16(count))
	copy(data[offset+16:], "MOBI")
	binary.BigEndian.PutUint32(data[offset+20:], 244)
	binary.BigEndian.PutUint32(data[offset+28:], 65001)
	binary.BigEndian.PutUint32(data[offset+240:], 2)
	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint32(data[78+(i+1)*8:], uint32(len(data)))
		end := min((i+1)*4096, len(text))
		data = append(data, []byte(text[i*4096:end])...)
		data = append(data, 255, 130)
	}
	p := filepath.Join(t.TempDir(), "book.azw3")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := Supplement(context.Background(), p, "azw3", importmeta.Metadata{Title: "登天的感觉", Authors: []string{"Unknown"}})
	if len(m.Authors) != 1 || m.Authors[0] != "岳晓东" {
		t.Fatal(m)
	}
	if m.ISBN != "" || m.Pubdate != "" {
		t.Fatal("signature guessed edition", m)
	}
	m = Supplement(context.Background(), p, "azw3", importmeta.Metadata{Title: "登天的感觉", Authors: []string{"人工作者"}})
	if m.Authors[0] != "人工作者" {
		t.Fatal("existing author overwritten", m)
	}
}
func TestPalmTrailerRejectsMalformedLengths(t *testing.T) {
	for _, c := range []struct {
		data  []byte
		flags uint32
	}{{nil, 1}, {[]byte{255}, 2}, {[]byte{255}, 1}, {[]byte{128}, 2}} {
		if _, ok := palmWithoutTrailer(c.data, c.flags); ok {
			t.Fatal(c)
		}
	}
	data := []byte{'A', 'B', 255, 130}
	out, ok := palmWithoutTrailer(data, 2)
	if !ok || string(out) != "AB" {
		t.Fatal(out, ok)
	}
}
