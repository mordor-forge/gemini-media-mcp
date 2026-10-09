package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// reloadInterval bounds how often the override file's mtime is checked.
const reloadInterval = 5 * time.Second

// Source serves the embedded catalog merged with an optional override file
// and reloads the override when it changes on disk. A broken override never
// takes the server down: the last good catalog keeps serving and the error
// is reported via Status.
type Source struct {
	base     []byte
	path     string
	defaults map[string]string
	now      func() time.Time
	log      *slog.Logger

	mu        sync.Mutex
	current   *Catalog
	modTime   time.Time
	size      int64
	lastCheck time.Time
	lastErr   error
}

// NewSource builds a source. path may be empty. defaults (from server
// config) override the catalog's defaults per media type.
func NewSource(path string, defaults map[string]string, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Source{base: embedded, path: path, defaults: defaults, now: time.Now, log: log}
	if err := s.reload(true); err != nil {
		// Never fail startup over a bad override: serve the embedded catalog
		// and report the problem via Status (get_config, list_models).
		log.Warn("catalog override ignored; using the built-in catalog", "err", err)
		saved := s.path
		s.path = ""
		if err2 := s.reload(true); err2 != nil {
			return nil, err2 // the embedded catalog itself is broken
		}
		s.path, s.lastErr = saved, err
	}
	return s, nil
}

// Get returns the current catalog, reloading the override if it changed.
func (s *Source) Get() *Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" && s.now().Sub(s.lastCheck) >= reloadInterval {
		s.lastCheck = s.now()
		if info, err := os.Stat(s.path); err == nil && (!info.ModTime().Equal(s.modTime) || info.Size() != s.size) {
			if err := s.reloadLocked(false); err != nil {
				s.log.Warn("catalog override reload failed; keeping previous catalog", "path", s.path, "err", err)
			}
		}
	}
	return s.current
}

// Status reports the override path and the last reload error, if any.
func (s *Source) Status() (path string, lastErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.lastErr
}

func (s *Source) reload(initial bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked(initial)
}

func (s *Source) reloadLocked(initial bool) error {
	var override []byte
	if s.path != "" {
		info, err := os.Stat(s.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// A missing override is fine (the user may create it later).
		case err != nil:
			return s.fail(initial, err)
		default:
			override, err = os.ReadFile(s.path)
			if err != nil {
				return s.fail(initial, err)
			}
			s.modTime, s.size = info.ModTime(), info.Size()
		}
	}
	c, err := Merge(s.base, override)
	if err != nil {
		return s.fail(initial, err)
	}
	for k, v := range s.defaults {
		if v != "" {
			c.setDefault(k, v)
		}
	}
	s.current, s.lastErr = c, nil
	return nil
}

func (s *Source) fail(_ bool, err error) error {
	err = fmt.Errorf("catalog override %s: %w", s.path, err)
	s.lastErr = err
	return err
}

// Merge overlays override YAML onto base YAML. Models are matched by id:
// provided fields replace base fields (maps are merged key by key, lists are
// replaced); unknown ids are appended. Top-level defaults are merged (a
// default set there applies on every backend unless the override also sets
// backendDefaults) and a non-empty version replaces the base version.
func Merge(base, override []byte) (*Catalog, error) {
	c, err := Parse(base)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(override)) == 0 {
		return c, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(override, &root); err != nil {
		return nil, fmt.Errorf("parsing override: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("override must be a YAML mapping")
	}
	top := root.Content[0]
	var backendDefaults *yaml.Node // applied after defaults, which clear them
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i].Value, top.Content[i+1]
		switch key {
		case "version":
			c.Version = val.Value
		case "sources":
			var extra []string
			if err := val.Decode(&extra); err != nil {
				return nil, fmt.Errorf("override sources: %w", err)
			}
			c.Sources = append(c.Sources, extra...)
		case "defaults":
			var d map[string]string
			if err := val.Decode(&d); err != nil {
				return nil, fmt.Errorf("override defaults: %w", err)
			}
			for k, v := range d {
				c.setDefault(k, v)
			}
		case "backendDefaults":
			backendDefaults = val
		case "models":
			if val.Kind != yaml.SequenceNode {
				return nil, errors.New("override models must be a list")
			}
			for _, node := range val.Content {
				if err := mergeModel(c, node); err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("override: unknown top-level key %q", key)
		}
	}
	if backendDefaults != nil {
		var bd map[string]map[string]string
		if err := backendDefaults.Decode(&bd); err != nil {
			return nil, fmt.Errorf("override backendDefaults: %w", err)
		}
		for b, d := range bd {
			if c.BackendDefaults == nil {
				c.BackendDefaults = map[string]map[string]string{}
			}
			if c.BackendDefaults[b] == nil {
				c.BackendDefaults[b] = map[string]string{}
			}
			for k, v := range d {
				c.BackendDefaults[b][k] = v
			}
		}
	}
	if err := c.index(); err != nil {
		return nil, err
	}
	return c, nil
}

func mergeModel(c *Catalog, node *yaml.Node) error {
	var probe struct {
		ID string `yaml:"id"`
	}
	if err := node.Decode(&probe); err != nil || strings.TrimSpace(probe.ID) == "" {
		return fmt.Errorf("override model entry at line %d needs an id", node.Line)
	}
	for i, m := range c.Models {
		if m.ID != probe.ID {
			continue
		}
		clone, err := cloneModel(m)
		if err != nil {
			return err
		}
		if err := node.Decode(clone); err != nil {
			return fmt.Errorf("override model %s: %w", probe.ID, err)
		}
		c.Models[i] = clone
		return nil
	}
	var m Model
	if err := node.Decode(&m); err != nil {
		return fmt.Errorf("override model %s: %w", probe.ID, err)
	}
	if m.Family == "" || m.MediaType == "" {
		fam, mt := InferFamily(strings.ToLower(m.ID))
		if m.Family == "" {
			m.Family = fam
		}
		if m.MediaType == "" {
			m.MediaType = mt
		}
	}
	if m.Family == "" || m.MediaType == "" {
		return fmt.Errorf("override model %s: set family and mediaType", m.ID)
	}
	c.Models = append(c.Models, &m)
	return nil
}

func cloneModel(m *Model) (*Model, error) {
	data, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out Model
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
