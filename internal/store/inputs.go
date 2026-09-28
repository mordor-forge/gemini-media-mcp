package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMaxInputBytes bounds a single input file. Gemini API inline request
// payloads are limited to about 20 MB in total.
const DefaultMaxInputBytes = 20 << 20

// Input is a loaded input media item.
type Input struct {
	Data     []byte
	MIMEType string
	// Ref is a short human-readable reference (path or URI) for provenance.
	Ref string
}

// InputPolicy restricts where input media can be read from.
type InputPolicy struct {
	// AllowAny disables path restrictions (safe for local stdio use where the
	// agent already has filesystem access).
	AllowAny bool
	// Roots are directories inputs may come from, in addition to the output dir.
	Roots    []string
	MaxBytes int64
}

// ErrInputNotAllowed is returned when a path is outside the permitted roots.
var ErrInputNotAllowed = errors.New("input path is outside the allowed directories")

// LoadInput resolves ref, which may be:
//   - a resource URI from a previous result (gemini-media://files/NAME)
//   - a bare file name of an earlier output (NAME)
//   - a file:// URI or filesystem path (absolute, ~-relative or relative)
//   - a data: URI (data:image/png;base64,....)
//
// Remote http(s) URLs are rejected to avoid server-side request forgery.
func (s *Store) LoadInput(ref string, pol InputPolicy) (*Input, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("empty input reference")
	}
	maxBytes := pol.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxInputBytes
	}

	switch {
	case strings.HasPrefix(ref, "data:"):
		return decodeDataURI(ref, maxBytes)
	case strings.HasPrefix(ref, URIScheme):
		path, data, err := s.Open(ref)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", ref, err)
		}
		return newInput(data, path, ref, maxBytes)
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		return nil, fmt.Errorf("remote URLs are not fetched (%s); download the file first and pass its path", ref)
	}

	path := ref
	if strings.HasPrefix(ref, "file://") {
		u, err := url.Parse(ref)
		if err != nil {
			return nil, fmt.Errorf("invalid file URI %q: %w", ref, err)
		}
		path = u.Path
		// file:///C:/x on Windows parses to /C:/x.
		if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		path = filepath.FromSlash(path)
	}
	path = expandHome(path)

	// A bare name that does not exist relative to the working directory is
	// looked up in the output directory, so agents can chain outputs by name.
	if !filepath.IsAbs(path) && filepath.Base(path) == path {
		if _, err := os.Stat(path); err != nil {
			candidate := filepath.Join(s.dir, path)
			if _, err2 := os.Stat(candidate); err2 == nil {
				path = candidate
			}
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("input %s: %w", ref, err)
	}
	if !pol.AllowAny && !s.allowed(real, pol.Roots) {
		return nil, fmt.Errorf("%w: %s (allowed: output directory%s; configure inputDirs or GEMINI_MEDIA_INPUT_DIRS)", ErrInputNotAllowed, ref, rootsHint(pol.Roots))
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("input %s: %w", ref, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("input %s is not a regular file", ref)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("input %s is %d bytes; the limit is %d", ref, info.Size(), maxBytes)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, fmt.Errorf("reading input %s: %w", ref, err)
	}
	return newInput(data, real, ref, maxBytes)
}

func (s *Store) allowed(path string, roots []string) bool {
	all := append([]string{s.dir}, roots...)
	for _, root := range all {
		r, err := filepath.EvalSymlinks(expandHome(root))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(r, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func rootsHint(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return ", " + strings.Join(roots, ", ")
}

func newInput(data []byte, path, ref string, maxBytes int64) (*Input, error) {
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("input %s is %d bytes; the limit is %d", ref, len(data), maxBytes)
	}
	return &Input{Data: data, MIMEType: SniffMIME(data, path), Ref: ref}, nil
}

func decodeDataURI(ref string, maxBytes int64) (*Input, error) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(ref, "data:"), ",")
	if !ok {
		return nil, errors.New("malformed data URI: missing comma")
	}
	if !strings.HasSuffix(meta, ";base64") {
		return nil, errors.New("data URIs must be base64-encoded (data:<mime>;base64,<data>)")
	}
	if int64(base64.StdEncoding.DecodedLen(len(payload))) > maxBytes {
		return nil, fmt.Errorf("data URI payload exceeds %d bytes", maxBytes)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decoding data URI: %w", err)
	}
	mime := strings.TrimSuffix(meta, ";base64")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return &Input{Data: data, MIMEType: mime, Ref: "data:" + mime}, nil
}

// SniffMIME detects the content type from bytes, falling back to the file
// extension for types the stdlib sniffer does not know (e.g. HEIC).
func SniffMIME(data []byte, name string) string {
	sniffed := http.DetectContentType(data)
	if sniffed != "application/octet-stream" && !strings.HasPrefix(sniffed, "text/plain") {
		return strings.SplitN(sniffed, ";", 2)[0]
	}
	return MIMEFromExt(name)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
