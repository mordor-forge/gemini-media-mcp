package store

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSaveUniqueNamesAndProvenance(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := pngBytes(t, 40, 20)
	a1, err := s.Save("image", "Hero Banner!", "png", data, "image/png", &Provenance{Tool: "generate_image", Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.Save("image", "hero banner", "png", data, "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Name != "hero-banner.png" || a2.Name != "hero-banner-2.png" {
		t.Fatalf("names = %q, %q", a1.Name, a2.Name)
	}
	if a1.Width != 40 || a1.Height != 20 || a1.URI != URIScheme+"hero-banner.png" {
		t.Fatalf("unexpected asset %+v", a1)
	}
	p, err := s.Provenance(a1.Name)
	if err != nil || p.Prompt != "p" || p.CreatedAt.IsZero() {
		t.Fatalf("provenance = %+v, %v", p, err)
	}
	a3, err := s.Save("video", "", "mp4", []byte("x"), "video/mp4", nil)
	if err != nil || !strings.HasPrefix(a3.Name, "video-") || !strings.HasSuffix(a3.Name, ".mp4") {
		t.Fatalf("generated name = %+v, %v", a3, err)
	}
	if _, err := s.Save("image", "", "png", nil, "image/png", nil); err == nil {
		t.Fatal("empty data must be rejected")
	}
	entries, _ := os.ReadDir(s.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestOpenRejectsTraversal(t *testing.T) {
	s, _ := New(t.TempDir())
	for _, bad := range []string{URIScheme + "../etc/passwd", "../x", URIScheme + ".meta", ""} {
		if _, _, err := s.Open(bad); err == nil {
			t.Errorf("Open(%q) should fail", bad)
		}
	}
}

func TestLoadInputPolicy(t *testing.T) {
	out := t.TempDir()
	other := t.TempDir()
	s, _ := New(out)
	img := pngBytes(t, 8, 8)
	a, err := s.Save("image", "in", "png", img, "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(other, "secret.png")
	if err := os.WriteFile(outside, img, 0o600); err != nil {
		t.Fatal(err)
	}

	restricted := InputPolicy{}
	fileURI := "file://" + filepath.ToSlash(a.Path)
	if runtime.GOOS == "windows" {
		fileURI = "file:///" + filepath.ToSlash(a.Path)
	}
	for _, ref := range []string{a.URI, a.Path, a.Name, fileURI} {
		in, err := s.LoadInput(ref, restricted)
		if err != nil {
			t.Fatalf("LoadInput(%q): %v", ref, err)
		}
		if in.MIMEType != "image/png" || !bytes.Equal(in.Data, img) {
			t.Fatalf("LoadInput(%q) = %s", ref, in.MIMEType)
		}
	}
	if _, err := s.LoadInput(outside, restricted); !errors.Is(err, ErrInputNotAllowed) {
		t.Fatalf("outside path must be rejected, got %v", err)
	}
	// Symlink from inside the output dir to outside must not bypass the policy.
	link := filepath.Join(out, "link.png")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := s.LoadInput(link, restricted); !errors.Is(err, ErrInputNotAllowed) {
			t.Fatalf("symlink escape must be rejected, got %v", err)
		}
	}
	if _, err := s.LoadInput(outside, InputPolicy{Roots: []string{other}}); err != nil {
		t.Fatalf("configured root must be allowed: %v", err)
	}
	if _, err := s.LoadInput(outside, InputPolicy{AllowAny: true}); err != nil {
		t.Fatalf("AllowAny must allow: %v", err)
	}
	if _, err := s.LoadInput("https://example.com/x.png", InputPolicy{AllowAny: true}); err == nil {
		t.Fatal("remote URLs must be rejected")
	}
	if _, err := s.LoadInput(a.Path, InputPolicy{MaxBytes: 4}); err == nil {
		t.Fatal("size limit must apply")
	}

	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
	in, err := s.LoadInput(uri, restricted)
	if err != nil || in.MIMEType != "image/png" || !bytes.Equal(in.Data, img) {
		t.Fatalf("data URI: %v", err)
	}
	if _, err := s.LoadInput("data:image/png,notbase64", restricted); err == nil {
		t.Fatal("non-base64 data URI must fail")
	}
}

func TestSniffMIMEPrefersContent(t *testing.T) {
	img := pngBytes(t, 2, 2)
	if got := SniffMIME(img, "photo.jpg"); got != "image/png" {
		t.Fatalf("SniffMIME = %s, want image/png despite .jpg extension", got)
	}
	if got := SniffMIME([]byte{0, 1, 2}, "x.heic"); got != "image/heic" {
		t.Fatalf("SniffMIME fallback = %s", got)
	}
}

func TestPreviewDownscales(t *testing.T) {
	prev, err := Preview(pngBytes(t, 2000, 1000), 500)
	if err != nil {
		t.Fatal(err)
	}
	w, h, ok := ImageSize(prev)
	if !ok || w != 500 || h != 250 {
		t.Fatalf("preview size = %dx%d", w, h)
	}
	if _, err := Preview([]byte("nope"), 100); err == nil {
		t.Fatal("invalid image must fail")
	}
}

func TestWAV(t *testing.T) {
	f, ok := ParsePCMMIME("audio/L16;codec=pcm;rate=24000")
	if !ok || f.SampleRate != 24000 || f.Channels != 1 || f.BitsPerSample != 16 {
		t.Fatalf("ParsePCMMIME = %+v %v", f, ok)
	}
	if _, ok := ParsePCMMIME("audio/mpeg"); ok {
		t.Fatal("mp3 is not PCM")
	}
	pcm := make([]byte, 48000) // 1 second
	wav, err := WrapWAV(pcm, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(wav) != 44+len(pcm) || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("bad WAV header: %q", wav[:44])
	}
	if d := PCMDuration(len(pcm), f); d != 1 {
		t.Fatalf("duration = %v", d)
	}
	if ExtFromMIME("audio/L16;codec=pcm;rate=24000") != "pcm" || ExtFromMIME("image/jpeg") != "jpg" || ExtFromMIME("video/mp4") != "mp4" {
		t.Fatal("ExtFromMIME mapping")
	}
}
