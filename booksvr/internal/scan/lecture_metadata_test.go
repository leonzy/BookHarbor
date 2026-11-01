package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"talebook/booksvr/internal/sourcefile"
	"testing"
)

func TestReadMetadataLectureZIPUsesLogicalFilenameAndReleasesScratch(t *testing.T) {
	name := "目录/030919存在与人生-何光沪.txt"
	body := []byte("主讲人简介\n何光沪，教授。\n内容简介\n存在与人生\n全文\n正文。")
	raw := zipTestBytes(t, zipTestEntry{name, body, 0})
	archive := writeZIPFixture(t, t.TempDir(), "collection.zip", raw)
	fp, err := sourcefile.Fingerprint(archive)
	if err != nil {
		t.Fatal(err)
	}
	ref := sourcefile.Encode(sourcefile.Reference{Archive: archive, Fingerprint: fp, Chain: []string{base64.StdEncoding.EncodeToString([]byte(name))}, Name: name, Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: int64(len(body))})
	got, err := ReadMetadata(context.Background(), ref, "TXT")
	if err != nil || got.Title != "存在与人生" || len(got.Authors) != 1 || got.Authors[0] != "何光沪" {
		t.Fatal(got, err)
	}
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("source changed", err)
	}
	// Acquiring again verifies that a released temporary lease can be reused.
	physical, release, err := sourcefile.Acquire(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err = os.Stat(physical); !os.IsNotExist(err) {
		t.Fatal("temporary member retained", err)
	}
}
