package coverimage

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func TestJPEGFormatsAndBounds(t *testing.T) {
	for name, encode := range map[string]func(*bytes.Buffer) error{
		"png":  func(b *bytes.Buffer) error { return png.Encode(b, image.NewRGBA(image.Rect(0, 0, 100, 200))) },
		"bmp":  func(b *bytes.Buffer) error { return bmp.Encode(b, image.NewRGBA(image.Rect(0, 0, 100, 200))) },
		"tiff": func(b *bytes.Buffer) error { return tiff.Encode(b, image.NewRGBA(image.Rect(0, 0, 100, 200)), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			if err := encode(&b); err != nil {
				t.Fatal(err)
			}
			out, err := JPEG(b.Bytes(), 80)
			if err != nil {
				t.Fatal(err)
			}
			im, err := jpeg.Decode(bytes.NewReader(out))
			if err != nil || im.Bounds().Dx() != 40 || im.Bounds().Dy() != 80 {
				t.Fatal("size changed ratio", err)
			}
		})
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	data := b.Bytes()
	binary.BigEndian.PutUint32(data[16:20], 100000)
	binary.BigEndian.PutUint32(data[20:24], 100000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if _, err := Decode(data); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatal("pixel limit", err)
	}
	if _, err := Decode(make([]byte, (32<<20)+1)); err == nil {
		t.Fatal("compressed limit")
	}
	if _, err := JPEG([]byte("<html>blocked</html>"), 800); err == nil {
		t.Fatal("nonimage accepted")
	}
}
