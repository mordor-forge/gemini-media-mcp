// Package store manages generated media on disk: collision-free naming,
// atomic writes, provenance sidecars, preview thumbnails, and safe loading of
// user-supplied input media.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// URIScheme prefixes media resources served by this server.
const URIScheme = "gemini-media://files/"

// metaDir holds provenance sidecars, hidden from casual directory listings.
const metaDir = ".meta"

// Asset describes a saved media file.
type Asset struct {
	Path     string `json:"path" jsonschema:"Absolute path of the saved file on the server's filesystem"`
	URI      string `json:"uri" jsonschema:"MCP resource URI; pass it back as an input to other tools or read it with resources/read"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType"`
	Bytes    int64  `json:"bytes"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	// DurationSeconds is set for audio/video when known.
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
}

// Provenance is written next to each asset so later edits, audits and cost
// reports can trace how a file was produced.
type Provenance struct {
	Tool        string         `json:"tool"`
	Model       string         `json:"model"`
	Prompt      string         `json:"prompt,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Inputs      []string       `json:"inputs,omitempty"`
	OperationID string         `json:"operationId,omitempty"`
	CostUSD     float64        `json:"costUsd,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	ModelText   string         `json:"modelText,omitempty"`
}

// Store writes into a single output directory.
type Store struct {
	dir string
	now func() time.Time
}

// New returns a Store rooted at dir (created if missing).
func New(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory %s: %w", abs, err)
	}
	return &Store{dir: abs, now: time.Now}, nil
}

// Dir returns the absolute output directory.
func (s *Store) Dir() string { return s.dir }

// Save writes data under a unique name. kind is a short prefix (image, video,
// speech, music); name is an optional caller-chosen base name; ext has no dot.
// Provenance is best-effort.
func (s *Store) Save(kind, name, ext string, data []byte, mimeType string, prov *Provenance) (*Asset, error) {
	return s.save(kind, name, ext, data, mimeType, prov, false)
}

// SaveWithProvenance is Save for an asset whose provenance a later step
// relies on: when the sidecar cannot be written, the asset is removed and
// the error returned.
func (s *Store) SaveWithProvenance(kind, name, ext string, data []byte, mimeType string, prov *Provenance) (*Asset, error) {
	if prov == nil {
		return nil, errors.New("SaveWithProvenance needs a provenance record")
	}
	return s.save(kind, name, ext, data, mimeType, prov, true)
}

func (s *Store) save(kind, name, ext string, data []byte, mimeType string, prov *Provenance, required bool) (*Asset, error) {
	if len(data) == 0 {
		return nil, errors.New("refusing to save empty media")
	}
	ext = strings.TrimPrefix(ext, ".")
	base := slugify(name)
	if base == "" {
		base = fmt.Sprintf("%s-%s-%s", kind, s.now().UTC().Format("20060102-150405"), randomHex(3))
	}

	path, f, err := s.reserve(base, ext)
	if err != nil {
		return nil, err
	}
	// Write to a temp file and rename over the reserved (empty) file so a
	// crash never leaves a truncated asset behind.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	_ = f.Close()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		_ = os.Remove(path)
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		_ = os.Remove(path)
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		_ = os.Remove(path)
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		_ = os.Remove(path)
		return nil, fmt.Errorf("finalizing %s: %w", path, err)
	}

	a := &Asset{
		Path:     path,
		Name:     filepath.Base(path),
		URI:      URIScheme + filepath.Base(path),
		MIMEType: mimeType,
		Bytes:    int64(len(data)),
	}
	if strings.HasPrefix(mimeType, "image/") {
		if w, h, ok := ImageSize(data); ok {
			a.Width, a.Height = w, h
		}
	}
	if prov != nil {
		if prov.CreatedAt.IsZero() {
			prov.CreatedAt = s.now().UTC()
		}
		if err := s.writeProvenance(a.Name, prov); err != nil && required {
			_ = os.Remove(path)
			return nil, fmt.Errorf("recording provenance for %s in %s: %w", a.Name, filepath.Join(s.dir, metaDir), err)
		}
	}
	return a, nil
}

// reserve atomically claims a free file name.
func (s *Store) reserve(base, ext string) (string, *os.File, error) {
	for i := 0; i < 1000; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		path := filepath.Join(s.dir, name+"."+ext)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return path, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("creating %s: %w", path, err)
		}
	}
	return "", nil, fmt.Errorf("could not find a free file name for %q", base)
}

func (s *Store) writeProvenance(name string, p *Provenance) error {
	dir := filepath.Join(s.dir, metaDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644)
}

// Provenance returns the sidecar for an asset in the output directory, if any.
func (s *Store) Provenance(name string) (*Provenance, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, metaDir, filepath.Base(name)+".json"))
	if err != nil {
		return nil, err
	}
	var p Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Open resolves a resource URI or bare file name to a file inside the output
// directory and returns its contents. Symlinks are resolved first and the
// target must still lie inside the output directory, so a link planted there
// cannot expose other files through resources/read or URI inputs.
func (s *Store) Open(uriOrName string) (string, []byte, error) {
	name := strings.TrimPrefix(uriOrName, URIScheme)
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", nil, fmt.Errorf("invalid media name %q", uriOrName)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(s.dir, name))
	if err != nil {
		return "", nil, err
	}
	if !s.allowed(real, nil) {
		return "", nil, fmt.Errorf("%w: %s resolves outside the output directory", ErrInputNotAllowed, uriOrName)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", uriOrName)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return "", nil, err
	}
	return real, data, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Drop a media extension the caller added ("hero.png"), but keep dots
	// that belong to the name ("shootout-gemini-3.1-flash-image").
	if MIMEFromExt(s) != "application/octet-stream" {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-._")
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(b)
}
