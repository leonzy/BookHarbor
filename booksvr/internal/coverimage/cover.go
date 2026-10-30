// Package coverimage decodes bounded artwork and normalizes it for cover consumers.
package coverimage

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Decode checks compressed size and pixel count before allocating image pixels.
func Decode(data []byte) (image.Image, error) {
	if len(data) > 32<<20 {
		return nil, fmt.Errorf("cover exceeds 32 MiB")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil, fmt.Errorf("cover dimensions exceed limit")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return src, nil
}

// JPEG returns bounded, proportionally scaled JPEG artwork.
func JPEG(data []byte, maxEdge int) ([]byte, error) {
	src, err := Decode(data)
	if err != nil {
		return nil, err
	}
	config := src.Bounds()
	if maxEdge <= 0 {
		return nil, fmt.Errorf("invalid cover size")
	}
	w, h := config.Dx(), config.Dy()
	if w > maxEdge || h > maxEdge {
		if w >= h {
			h = max(1, h*maxEdge/w)
			w = maxEdge
		} else {
			w = max(1, w*maxEdge/h)
			h = maxEdge
		}
	}
	normalized := src
	if w != config.Dx() || h != config.Dy() {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		b := src.Bounds()
		// JPEG's YCbCr fast path avoids allocating a boxed color per pixel.
		// Keep native JPEG buffers when no resize is necessary.
		if jpg, ok := src.(*image.YCbCr); ok {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					c := jpg.YCbCrAt(b.Min.X+x*config.Dx()/w, b.Min.Y+y*config.Dy()/h)
					r, g, blue := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
					dst.SetRGBA(x, y, color.RGBA{R: r, G: g, B: blue, A: 255})
				}
			}
		} else {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					dst.Set(x, y, src.At(b.Min.X+x*config.Dx()/w, b.Min.Y+y*config.Dy()/h))
				}
			}
		}
		normalized = dst
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, normalized, &jpeg.Options{Quality: 75}); err != nil {
		return nil, err
	}
	if out.Len() > 512<<10 {
		return nil, fmt.Errorf("compressed cover exceeds 512 KiB")
	}
	return out.Bytes(), nil
}
