package store

import (
	"bytes"
	"math"
	"testing"
)

// mp3Frames builds n MPEG-1 Layer III frames at 128 kbps / 44.1 kHz
// (417 bytes each without padding).
func mp3Frames(n int, first []byte) []byte {
	var b bytes.Buffer
	for i := range n {
		frame := make([]byte, 417)
		copy(frame, []byte{0xff, 0xfb, 0x90, 0x00})
		if i == 0 && first != nil {
			copy(frame[36:], first)
		}
		b.Write(frame)
	}
	return b.Bytes()
}

func TestMP3Duration(t *testing.T) {
	want := 100 * 1152 / 44100.0
	plain := mp3Frames(100, nil)
	if got := MP3Duration(plain); math.Abs(got-want) > 1e-9 {
		t.Fatalf("plain = %v, want %v", got, want)
	}
	// An ID3v2 tag (size 0x0201 = 257 syncsafe) and a trailing ID3v1 tag are skipped.
	tag := append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 2, 1}, make([]byte, 257)...)
	tagged := append(append(tag, plain...), append([]byte("TAG"), make([]byte, 125)...)...)
	if got := MP3Duration(tagged); math.Abs(got-want) > 1e-9 {
		t.Fatalf("tagged = %v, want %v", got, want)
	}
	// A Xing header frame carries no audio.
	if got := MP3Duration(mp3Frames(101, []byte("Xing"))); math.Abs(got-want) > 1e-9 {
		t.Fatalf("xing = %v, want %v", got, want)
	}
	if MP3Duration([]byte("RIFF....WAVE")) != 0 || MP3Duration(nil) != 0 {
		t.Fatal("non-MP3 data must report 0")
	}
}
