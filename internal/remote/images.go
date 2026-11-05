package remote

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"sync"

	"golang.org/x/image/draw"

	"github.com/ThiraSoft/neonmux/vt"
)

const maxImagePayload = 900 * 1024 // 900 KiB

type imgCacheKey struct {
	id  uint32
	gen uint64
}

type preparedImageEntry struct {
	data []byte
	w, h int
	err  error
}

var (
	prepMu    sync.Mutex
	prepCache = make(map[imgCacheKey]preparedImageEntry)
)

func getOrPrepareImage(img vt.Image) ([]byte, int, int, error) {
	if img.ID != 0 && img.Gen != 0 {
		prepMu.Lock()
		if entry, ok := prepCache[imgCacheKey{img.ID, img.Gen}]; ok {
			prepMu.Unlock()
			return entry.data, entry.w, entry.h, entry.err
		}
		prepMu.Unlock()
	}

	data, w, h, err := prepareImage(img)

	if img.ID != 0 && img.Gen != 0 {
		prepMu.Lock()
		prepCache[imgCacheKey{img.ID, img.Gen}] = preparedImageEntry{
			data: data,
			w:    w,
			h:    h,
			err:  err,
		}
		prepMu.Unlock()
	}

	return data, w, h, err
}

func decodeRawImage(img vt.Image) (image.Image, error) {
	switch img.Format {
	case 100: // PNG
		return png.Decode(bytes.NewReader(img.Data))
	case 24: // RGB
		rgba := image.NewRGBA(image.Rect(0, 0, img.W, img.H))
		src, dst := img.Data, rgba.Pix
		n := min(len(src)/3, img.W*img.H)
		for i := 0; i < n; i++ {
			dst[4*i] = src[3*i]
			dst[4*i+1] = src[3*i+1]
			dst[4*i+2] = src[3*i+2]
			dst[4*i+3] = 255
		}
		return rgba, nil
	case 32: // RGBA
		rgba := image.NewRGBA(image.Rect(0, 0, img.W, img.H))
		copy(rgba.Pix, img.Data)
		return rgba, nil
	default:
		return nil, image.ErrFormat
	}
}

func isImageOpaque(m image.Image, format uint8) bool {
	if format == 24 {
		return true
	}
	if op, ok := m.(interface{ Opaque() bool }); ok {
		return op.Opaque()
	}
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := m.At(x, y).RGBA()
			if a != 0xffff {
				return false
			}
		}
	}
	return true
}

// prepareImage prepares an image to be sent over the wire to the phone:
// JPEG quality 80 if opaque, PNG otherwise, longest side <= 1280 px,
// halved until fitting under 900 KiB.
func prepareImage(img vt.Image) (data []byte, w, h int, err error) {
	raw, err := decodeRawImage(img)
	if err != nil {
		return nil, 0, 0, err
	}

	origW := img.W
	origH := img.H
	if origW <= 0 || origH <= 0 {
		origW = raw.Bounds().Dx()
		origH = raw.Bounds().Dy()
	}
	if origW <= 0 || origH <= 0 {
		return nil, 0, 0, image.ErrFormat
	}

	w, h = origW, origH
	longSide := max(w, h)
	if longSide > 1280 {
		if w >= h {
			w = 1280
			h = max(1, origH*1280/origW)
		} else {
			h = 1280
			w = max(1, origW*1280/origH)
		}
	}

	opaque := isImageOpaque(raw, img.Format)

	scaleImg := func(src image.Image, targetW, targetH int) image.Image {
		if src.Bounds().Dx() == targetW && src.Bounds().Dy() == targetH {
			return src
		}
		dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
		draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		return dst
	}

	encode := func(m image.Image) ([]byte, error) {
		var buf bytes.Buffer
		if opaque {
			err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 80})
			return buf.Bytes(), err
		}
		err := png.Encode(&buf, m)
		return buf.Bytes(), err
	}

	for {
		scaled := scaleImg(raw, w, h)
		data, err = encode(scaled)
		if err != nil {
			return nil, 0, 0, err
		}
		if len(data) <= maxImagePayload || (w <= 1 && h <= 1) {
			break
		}
		newW := max(1, w/2)
		newH := max(1, h/2)
		if newW == w && newH == h {
			break
		}
		w, h = newW, newH
	}
	return data, w, h, nil
}
