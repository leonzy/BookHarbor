package localmeta

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"os"
	"os/exec"
	"path/filepath"
	"talebook/booksvr/internal/importmeta"
	"testing"
)

func TestISBNChecksumAndIdentifiers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"urn:isbn:978-7-02-009162-1", "9787020091621"}, {"ISBN: 0-306-40615-2", "0306406152"}, {"0-8044-2957-X", "080442957X"},
		{"9787020091622", ""}, {"B0865KKLFS", ""}, {"urn:uuid:36d4c435-8ad2-4037-a3dc-ca0689ac7285", ""}, {"群言出版社", ""},
	} {
		if got := ISBN(tc.in); got != tc.want {
			t.Errorf("%q: %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestExplicitCopyrightLabelsAndPriority(t *testing.T) {
	m := FromText("书名：微神集 月牙集\n作者：老舍 出版社：人民文学出版社 出版时间：2012年9月 ISBN：9787020091621\n版权所有")
	if m.Title != "微神集 月牙集" || len(m.Authors) != 1 || m.Authors[0] != "老舍" || m.Publisher != "人民文学出版社" || m.ISBN != "9787020091621" {
		t.Fatalf("%+v", m)
	}
	original := importmeta.Metadata{Title: "人工书名", Authors: []string{"原作者"}, ISBN: "9787020121625", Publisher: "原出版社"}
	Merge(&original, m)
	if original.Title != "人工书名" || original.Authors[0] != "原作者" || original.ISBN != "9787020121625" || original.Publisher != "原出版社" {
		t.Fatal(original)
	}
	if m := FromText("这本书的作者是老舍，讨论了2012010112345这个号码。"); m.ISBN != "" || len(m.Authors) > 0 || m.Publisher != "" {
		t.Fatal(m)
	}
}

func TestEpubCopyrightAndTXTReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.epub")
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	for n, s := range map[string]string{"Text/Copyright.xhtml": "<html><body><p>作者：老舍</p><p>出版社：人民文学出版社</p><p>ISBN：9787020091621</p></body></html>", "Text/chapter.xhtml": "<p>作者：无关正文人物</p>"} {
		w, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte(s))
	}
	z.Close()
	f.Close()
	before, _ := os.ReadFile(path)
	m := Supplement(context.Background(), path, "epub", importmeta.Metadata{Authors: []string{"佚名"}})
	if m.ISBN != "9787020091621" || m.Publisher != "人民文学出版社" || m.Authors[0] != "老舍" {
		t.Fatal(m)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source rewritten")
	}
	path = filepath.Join(dir, "小说.txt")
	gb, _ := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("书名：小说\n作者：老舍\n出版社：人民文学出版社\nISBN：9787020091621\n正文"))
	os.WriteFile(path, gb, 0600)
	m = Supplement(context.Background(), path, "txt", importmeta.Metadata{})
	if m.Authors[0] != "老舍" || m.ISBN != "9787020091621" {
		t.Fatal(m)
	}
}

func TestPalmDOCTextAndEncryption(t *testing.T) {
	text := []byte("<p>作者：老舍</p><p>出版社：人民文学出版社</p><p>ISBN：9787020091621</p>")
	b := make([]byte, 110)
	binary.BigEndian.PutUint16(b[76:], 2)
	binary.BigEndian.PutUint32(b[78:], 94)
	binary.BigEndian.PutUint32(b[86:], 110)
	binary.BigEndian.PutUint16(b[94:], 1)
	binary.BigEndian.PutUint16(b[102:], 1)
	b = append(b, text...)
	path := filepath.Join(t.TempDir(), "book.mobi")
	os.WriteFile(path, b, 0600)
	m := Supplement(context.Background(), path, "mobi", importmeta.Metadata{})
	if m.ISBN != "9787020091621" || m.Authors[0] != "老舍" {
		t.Fatal(m)
	}
	binary.BigEndian.PutUint16(b[106:], 1)
	os.WriteFile(path, b, 0600)
	m = Supplement(context.Background(), path, "mobi", importmeta.Metadata{})
	if m.ISBN != "" || m.Authors[0] != "佚名" {
		t.Fatal("encrypted text parsed", m)
	}
}

func TestPublisherISBNMisfiled(t *testing.T) {
	m := Supplement(context.Background(), "absent.epub", "epub", importmeta.Metadata{Publisher: "9787020091621"})
	if m.Publisher != "" || m.ISBN != "9787020091621" {
		t.Fatal(m)
	}
}

func TestPDFPropertiesAndCopyrightText(t *testing.T) {
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		t.Skip("Poppler unavailable on this host; Linux production QA provides it")
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("Poppler text unavailable")
	}
	stream := "BT /F1 12 Tf 20 700 Td (Author: Jane Austen) Tj 0 -20 Td (Publisher: Penguin Classics) Tj 0 -20 Td (ISBN: 9780141439518) Tj ET"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", "<< /Author (Jane Austen) >>"}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, s := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, s)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 6 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	file := filepath.Join(t.TempDir(), "copyright.pdf")
	if err := os.WriteFile(file, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m := Supplement(context.Background(), file, "pdf", importmeta.Metadata{})
	if m.ISBN != "9780141439518" || len(m.Authors) != 1 || m.Authors[0] != "Jane Austen" || m.Publisher != "Penguin Classics" {
		t.Fatal(m)
	}
}
