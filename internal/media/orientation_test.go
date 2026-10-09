package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// exifJPEG encodes a 64x32 image (left half red, right half blue) with an
// EXIF orientation tag in the given byte order.
func exifJPEG(t *testing.T, orientation uint16, bo binary.ByteOrder) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := range 32 {
		for x := range 64 {
			c := color.RGBA{220, 20, 20, 255}
			if x >= 32 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	tiff := make([]byte, 26)
	if bo == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	bo.PutUint16(tiff[2:], 42)
	bo.PutUint32(tiff[4:], 8)       // first IFD
	bo.PutUint16(tiff[8:], 1)       // one entry
	bo.PutUint16(tiff[10:], 0x0112) // Orientation
	bo.PutUint16(tiff[12:], 3)      // SHORT
	bo.PutUint32(tiff[14:], 1)      // count
	bo.PutUint16(tiff[18:], orientation)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	data := buf.Bytes()
	return append(append(append([]byte{}, data[:2]...), append(app1, seg...)...), data[2:]...)
}

func isRed(c color.Color) bool {
	r, _, b, _ := c.RGBA()
	return r > 2*b
}

func TestDecodeImageAppliesEXIFOrientation(t *testing.T) {
	cases := []struct {
		o      uint16
		bo     binary.ByteOrder
		w, h   int
		redAt  image.Point // a pixel that must be red
		blueAt image.Point
	}{
		{1, binary.BigEndian, 64, 32, image.Pt(5, 16), image.Pt(58, 16)},
		{3, binary.LittleEndian, 64, 32, image.Pt(58, 16), image.Pt(5, 16)},
		{6, binary.BigEndian, 32, 64, image.Pt(16, 5), image.Pt(16, 58)}, // turned clockwise: left half on top
		{8, binary.LittleEndian, 32, 64, image.Pt(16, 58), image.Pt(16, 5)},
	}
	for _, c := range cases {
		data := exifJPEG(t, c.o, c.bo)
		if got := jpegOrientation(data); got != int(c.o) {
			t.Fatalf("orientation %d read as %d", c.o, got)
		}
		img, err := decodeImage(&store.Input{Data: data, MIMEType: "image/jpeg", Ref: "x.jpg"}, "image", maxPlanPixels)
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != c.w || b.Dy() != c.h {
			t.Fatalf("orientation %d: %v, want %dx%d", c.o, b, c.w, c.h)
		}
		if !isRed(img.At(c.redAt.X, c.redAt.Y)) || isRed(img.At(c.blueAt.X, c.blueAt.Y)) {
			t.Errorf("orientation %d: pixels not where expected", c.o)
		}
	}
	// No or broken EXIF data leaves the image alone.
	for _, b := range [][]byte{nil, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xE1, 0, 200}, []byte("not a jpeg")} {
		if got := jpegOrientation(b); got != 1 {
			t.Errorf("jpegOrientation(%x) = %d", b, got)
		}
	}
}

func TestOrientMatchesAPixelMapAcrossBands(t *testing.T) {
	// A decoded JPEG (YCbCr) taller than one band.
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scene(70, 600, 21), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	src, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*image.YCbCr); !ok {
		t.Fatalf("decoded %T, want *image.YCbCr", src)
	}
	ref := normalizeRGBA(src).(*image.RGBA)
	w, h := 70, 600
	for o := 2; o <= 8; o++ {
		got := orient(src, o).(*image.RGBA)
		for y := range h {
			for x := range w {
				var dx, dy int
				switch o {
				case 2:
					dx, dy = w-1-x, y
				case 3:
					dx, dy = w-1-x, h-1-y
				case 4:
					dx, dy = x, h-1-y
				case 5:
					dx, dy = y, x
				case 6:
					dx, dy = h-1-y, x
				case 7:
					dx, dy = h-1-y, w-1-x
				case 8:
					dx, dy = y, w-1-x
				}
				if got.RGBAAt(dx, dy) != ref.RGBAAt(x, y) {
					t.Fatalf("orientation %d: pixel %d,%d -> %d,%d = %v, want %v", o, x, y, dx, dy, got.RGBAAt(dx, dy), ref.RGBAAt(x, y))
				}
			}
		}
	}
}

func TestDecodeShrunkTurnsAfterShrinking(t *testing.T) {
	// 64x32, left half red, turned clockwise: 32x64 with red on top.
	in := &store.Input{Data: exifJPEG(t, 6, binary.BigEndian), MIMEType: "image/jpeg", Ref: "x.jpg"}
	img, decoded, err := decodeShrunk(in, "original", maxPlanPixels, 16)
	if err != nil || decoded != 64*32 {
		t.Fatal(decoded, err)
	}
	if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 16 {
		t.Fatalf("shrunk to %v, want 8x16 (64/4 x 32/4, turned)", b)
	}
	if !isRed(img.At(4, 2)) || isRed(img.At(4, 13)) {
		t.Fatal("the shrunk image is not turned like the original")
	}
	// Box averages: a flat block stays exactly its color.
	flat := image.NewRGBA(image.Rect(0, 0, 9, 7))
	for i := range flat.Pix {
		flat.Pix[i] = uint8(40 + i%4*50)
	}
	s := shrink(flat, 3)
	if s.Rect.Dx() != 3 || s.Rect.Dy() != 2 || s.RGBAAt(2, 1) != (color.RGBA{40, 90, 140, 190}) {
		t.Fatalf("shrink = %v %v", s.Rect, s.RGBAAt(2, 1))
	}
	// A side shorter than the factor is averaged whole, never shrunk to 0.
	strip := image.NewRGBA(image.Rect(0, 0, 9000, 4))
	for i := range strip.Pix {
		strip.Pix[i] = uint8(40 + i%4*50)
	}
	if s := shrink(strip, 9); s.Rect.Dx() != 1000 || s.Rect.Dy() != 1 || s.RGBAAt(999, 0) != (color.RGBA{40, 90, 140, 190}) {
		t.Fatalf("panorama shrink = %v %v", s.Rect, s.RGBAAt(999, 0))
	}
}
