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
