package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"path/filepath"
	"strconv"
	"strings"

	// Register decoders for image.DecodeConfig / image.Decode.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ExtFromMIME maps a MIME type to a file extension (without dot).
func ExtFromMIME(mime string) string {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch mt {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/ogg":
		return "ogg"
	case "audio/flac":
		return "flac"
	case "audio/aac":
		return "aac"
	case "audio/l16", "audio/pcm":
		return "pcm"
	}
	switch {
	case strings.HasPrefix(mt, "image/"):
		return "png"
	case strings.HasPrefix(mt, "video/"):
		return "mp4"
	case strings.HasPrefix(mt, "audio/"):
		return "bin"
	}
	return "bin"
}

// MIMEFromExt infers a MIME type from a file name.
func MIMEFromExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	case ".pcm":
		return "audio/L16"
	}
	return "application/octet-stream"
}

// ImageSize returns the pixel dimensions of an encoded image.
func ImageSize(data []byte) (int, int, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// Preview returns a JPEG thumbnail whose longest side is at most maxPx.
// Small images are still re-encoded as JPEG to keep payloads compact.
func Preview(data []byte, maxPx int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, errors.New("empty image")
	}
	if maxPx > 0 && (w > maxPx || h > maxPx) {
		if w >= h {
			h = h * maxPx / w
			w = maxPx
		} else {
			w = w * maxPx / h
			h = maxPx
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PCMFormat describes raw linear PCM audio.
type PCMFormat struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
}

// ParsePCMMIME parses MIME types such as "audio/L16;codec=pcm;rate=24000".
// It reports ok=false when mime is not raw PCM.
func ParsePCMMIME(mime string) (PCMFormat, bool) {
	parts := strings.Split(mime, ";")
	base := strings.ToLower(strings.TrimSpace(parts[0]))
	if base != "audio/l16" && base != "audio/pcm" {
		return PCMFormat{}, false
	}
	f := PCMFormat{SampleRate: 24000, Channels: 1, BitsPerSample: 16}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			continue
		}
		switch strings.ToLower(k) {
		case "rate":
			f.SampleRate = n
		case "channels":
			f.Channels = n
		}
	}
	return f, true
}

// WrapWAV prefixes little-endian PCM samples with a RIFF/WAVE header.
func WrapWAV(pcm []byte, f PCMFormat) ([]byte, error) {
	if f.SampleRate <= 0 || f.Channels <= 0 || f.BitsPerSample <= 0 {
		return nil, fmt.Errorf("invalid PCM format %+v", f)
	}
	if uint64(len(pcm)) > uint64(^uint32(0))-36 {
		return nil, errors.New("audio too large for WAV")
	}
	blockAlign := f.Channels * f.BitsPerSample / 8
	byteRate := f.SampleRate * blockAlign
	var buf bytes.Buffer
	buf.Grow(44 + len(pcm))
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(f.Channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(f.SampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(f.BitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes(), nil
}

// PCMDuration returns the duration in seconds of raw PCM data.
func PCMDuration(n int, f PCMFormat) float64 {
	bps := f.SampleRate * f.Channels * f.BitsPerSample / 8
	if bps <= 0 {
		return 0
	}
	return float64(n) / float64(bps)
}

// MP3Duration returns the duration in seconds of MPEG audio Layer III data by
// counting frames (exact for constant and variable bitrate), or 0 when the
// data is not MP3.
func MP3Duration(b []byte) float64 {
	i := 0
	// Skip an ID3v2 tag: "ID3", version, flags, then a 28-bit syncsafe size.
	if len(b) >= 10 && string(b[:3]) == "ID3" {
		i = 10 + (int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f))
		if b[5]&0x10 != 0 {
			i += 10 // footer
		}
	}
	var samples, rate, frames int
	for i+4 <= len(b) {
		n, spf, sr := mp3Frame(b[i:])
		if n == 0 {
			if frames > 0 && i+128 >= len(b) {
				break // trailing ID3v1 tag or padding
			}
			i++ // resync
			continue
		}
		frame := b[i:min(i+n, len(b))]
		// The first frame of most files is a silent Xing/Info header.
		if frames > 0 || !bytes.Contains(frame, []byte("Xing")) && !bytes.Contains(frame, []byte("Info")) {
			samples += spf
		}
		rate = sr
		frames++
		i += n
	}
	if rate == 0 {
		return 0
	}
	return float64(samples) / float64(rate)
}

// mp3Frame parses a Layer III frame header, returning the frame length in
// bytes, samples per frame and sample rate (all 0 when h is not a header).
func mp3Frame(h []byte) (length, samples, rate int) {
	if len(h) < 4 || h[0] != 0xff || h[1]&0xe0 != 0xe0 {
		return 0, 0, 0
	}
	version := (h[1] >> 3) & 3 // 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5
	layer := (h[1] >> 1) & 3   // 1 = Layer III
	brIdx := int(h[2] >> 4)
	srIdx := int(h[2]>>2) & 3
	pad := int(h[2]>>1) & 1
	if version == 1 || layer != 1 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return 0, 0, 0
	}
	mpeg1 := [15]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	mpeg2 := [15]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	rates := map[byte][3]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
	rate = rates[version][srIdx]
	if version == 3 {
		return 144*mpeg1[brIdx]*1000/rate + pad, 1152, rate
	}
	return 72*mpeg2[brIdx]*1000/rate + pad, 576, rate
}
