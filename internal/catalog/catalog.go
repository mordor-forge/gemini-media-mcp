// Package catalog is the single source of truth for model knowledge: IDs,
// aliases, lifecycle (preview/GA/deprecated/retired), capabilities,
// parameter constraints and prices.
//
// The default catalog is embedded (models.yaml). Users can overlay a local
// override file that is hot-reloaded, so model launches, renames, retirements
// and price changes do not require a new server release.
package catalog

import (
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed models.yaml
var embedded []byte

// Media types.
const (
	Image  = "image"
	Video  = "video"
	Speech = "speech"
	Music  = "music"
)

// Families select the request adapter.
const (
	FamilyGeminiImage = "gemini-image" // generateContent with IMAGE modality
	FamilyVeo         = "veo"          // generateVideos long-running operation
	FamilyGeminiTTS   = "gemini-tts"   // generateContent with AUDIO modality + speechConfig
	FamilyLyria       = "lyria"        // generateContent with AUDIO modality
)

// Lifecycle states.
const (
	StatusGA         = "ga"
	StatusPreview    = "preview"
	StatusDeprecated = "deprecated"
	StatusRetired    = "retired"
	StatusUnknown    = "unknown"
)

// Catalog is a parsed model catalog.
type Catalog struct {
	Version  string            `yaml:"version" json:"version"`
	Sources  []string          `yaml:"sources" json:"sources,omitempty"`
	Defaults map[string]string `yaml:"defaults" json:"defaults"`
	Models   []*Model          `yaml:"models" json:"models"`

	byName map[string]*Model
}

// Model describes one Google model.
type Model struct {
	ID          string   `yaml:"id" json:"id"`
	Aliases     []string `yaml:"aliases" json:"aliases,omitempty"`
	Family      string   `yaml:"family" json:"family"`
	MediaType   string   `yaml:"mediaType" json:"mediaType"`
	Title       string   `yaml:"title" json:"title,omitempty"`
	Summary     string   `yaml:"summary" json:"summary,omitempty"`
	Status      string   `yaml:"status" json:"status"`
	Released    string   `yaml:"released" json:"released,omitempty"`
	Shutdown    string   `yaml:"shutdown" json:"shutdown,omitempty"`
	Replacement string   `yaml:"replacement" json:"replacement,omitempty"`
	// Fallback is used instead when the model is not offered on the active backend.
	Fallback        string       `yaml:"fallback" json:"fallback,omitempty"`
	Backends        []string     `yaml:"backends" json:"backends,omitempty"`
	VertexID        string       `yaml:"vertexId" json:"vertexId,omitempty"`
	VertexLocations []string     `yaml:"vertexLocations" json:"vertexLocations,omitempty"`
	Capabilities    Capabilities `yaml:"capabilities" json:"capabilities"`
	Pricing         Pricing      `yaml:"pricing" json:"pricing"`
	Notes           []string     `yaml:"notes" json:"notes,omitempty"`
}

// Capabilities describe accepted parameters. Empty lists mean "not
// constrained by the catalog" (the API validates).
type Capabilities struct {
	AspectRatios       []string `yaml:"aspectRatios" json:"aspectRatios,omitempty"`
	ImageSizes         []string `yaml:"imageSizes" json:"imageSizes,omitempty"`
	Resolutions        []string `yaml:"resolutions" json:"resolutions,omitempty"`
	Durations          []int    `yaml:"durations" json:"durations,omitempty"`
	DefaultDuration    int      `yaml:"defaultDuration" json:"defaultDuration,omitempty"`
	MaxReferenceImages int      `yaml:"maxReferenceImages" json:"maxReferenceImages,omitempty"`
	Edit               bool     `yaml:"edit" json:"edit,omitempty"`
	SearchGrounding    bool     `yaml:"searchGrounding" json:"searchGrounding,omitempty"`
	ImageToVideo       bool     `yaml:"imageToVideo" json:"imageToVideo,omitempty"`
	LastFrame          bool     `yaml:"lastFrame" json:"lastFrame,omitempty"`
	Extend             bool     `yaml:"extend" json:"extend,omitempty"`
	ExtendSeconds      int      `yaml:"extendSeconds" json:"extendSeconds,omitempty"`
	NegativePrompt     bool     `yaml:"negativePrompt" json:"negativePrompt,omitempty"`
	Seed               bool     `yaml:"seed" json:"seed,omitempty"`
	PersonGeneration   []string `yaml:"personGeneration" json:"personGeneration,omitempty"`
	AudioToggle        []string `yaml:"audioToggle" json:"audioToggle,omitempty"`
	MaxSpeakers        int      `yaml:"maxSpeakers" json:"maxSpeakers,omitempty"`
	Voices             []string `yaml:"voices" json:"voices,omitempty"`
	ImageInput         bool     `yaml:"imageInput" json:"imageInput,omitempty"`
	OutputSeconds      int      `yaml:"outputSeconds" json:"outputSeconds,omitempty"`
	MaxOutputSeconds   int      `yaml:"maxOutputSeconds" json:"maxOutputSeconds,omitempty"`
	OutputFormats      []string `yaml:"outputFormats" json:"outputFormats,omitempty"`
	// SpeechStyle is "metadata" (style sent as speech_metadata, text read
	// verbatim) or "prompt" (style written as directions inside the text).
	SpeechStyle string `yaml:"speechStyle" json:"speechStyle,omitempty"`
	// VertexOnly / GeminiAPIOnly list parameters accepted by only one backend;
	// they are dropped with a warning on the other.
	VertexOnly    []string `yaml:"vertexOnly" json:"vertexOnly,omitempty"`
	GeminiAPIOnly []string `yaml:"geminiApiOnly" json:"geminiApiOnly,omitempty"`
	Rules         []Rule   `yaml:"rules" json:"rules,omitempty"`
}

// Rule expresses a conditional constraint, e.g. "1080p requires 8 seconds".
// When all When conditions match the request, every Require condition must
// hold. Values are compared as strings; "*" in When means "parameter is set".
type Rule struct {
	When    map[string][]string `yaml:"when" json:"when"`
	Require map[string][]string `yaml:"require" json:"require"`
	Message string              `yaml:"message" json:"message"`
}

// Pricing holds list prices in USD. Only the fields relevant to the model's
// billing unit are set.
type Pricing struct {
	AsOf   string `yaml:"asOf" json:"asOf,omitempty"`
	Source string `yaml:"source" json:"source,omitempty"`
	// Token prices, USD per 1M tokens, keyed by modality (text, image, audio).
	InputPer1M  map[string]float64 `yaml:"inputPer1M" json:"inputPer1M,omitempty"`
	OutputPer1M map[string]float64 `yaml:"outputPer1M" json:"outputPer1M,omitempty"`
	// ImageOutputTokens maps an image size (1K, 2K...) to tokens per output image.
	ImageOutputTokens map[string]int `yaml:"imageOutputTokens" json:"imageOutputTokens,omitempty"`
	// InputImageTokens is the token cost of one input/reference image.
	InputImageTokens int `yaml:"inputImageTokens" json:"inputImageTokens,omitempty"`
	// TextOutputTokens is the typical text/thinking output per image request,
	// used for estimates (default 400). Models that think by default need more.
	TextOutputTokens int `yaml:"textOutputTokens" json:"textOutputTokens,omitempty"`
	// PerSecond maps a video resolution to USD per generated second (with audio).
	PerSecond map[string]float64 `yaml:"perSecond" json:"perSecond,omitempty"`
	// PerSecondNoAudio is used when audio generation is disabled (Vertex only).
	PerSecondNoAudio map[string]float64 `yaml:"perSecondNoAudio" json:"perSecondNoAudio,omitempty"`
	// PerRequest is a flat price per successful request (e.g. Lyria).
	PerRequest float64 `yaml:"perRequest" json:"perRequest,omitempty"`
	// AudioTokensPerSecond converts audio duration to output tokens (TTS).
	AudioTokensPerSecond int `yaml:"audioTokensPerSecond" json:"audioTokensPerSecond,omitempty"`
	// VertexRegionalMultiplier applies to Vertex calls outside "global".
	VertexRegionalMultiplier float64 `yaml:"vertexRegionalMultiplier" json:"vertexRegionalMultiplier,omitempty"`
	FreeTier                 bool    `yaml:"freeTier" json:"freeTier,omitempty"`
	BatchDiscount            float64 `yaml:"batchDiscount" json:"batchDiscount,omitempty"`
}

// Priced reports whether any price is known.
func (p Pricing) Priced() bool {
	return len(p.InputPer1M) > 0 || len(p.OutputPer1M) > 0 || len(p.PerSecond) > 0 || p.PerRequest > 0
}

// Parse decodes and indexes catalog YAML.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing catalog: %w", err)
	}
	if err := c.index(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Default returns the embedded catalog.
func Default() *Catalog {
	c, err := Parse(embedded)
	if err != nil {
		panic(err) // guarded by tests
	}
	return c
}

func (c *Catalog) index() error {
	c.byName = map[string]*Model{}
	for _, m := range c.Models {
		if m == nil || m.ID == "" {
			return fmt.Errorf("catalog: model without id")
		}
		if m.Status == "" {
			m.Status = StatusPreview
		}
		for _, name := range append([]string{m.ID}, m.Aliases...) {
			key := strings.ToLower(name)
			if prev, dup := c.byName[key]; dup && prev != m {
				return fmt.Errorf("catalog: name %q used by both %s and %s", name, prev.ID, m.ID)
			}
			c.byName[key] = m
		}
		if m.VertexID != "" {
			if _, dup := c.byName[strings.ToLower(m.VertexID)]; !dup {
				c.byName[strings.ToLower(m.VertexID)] = m
			}
		}
	}
	return nil
}

// Lookup finds a model by ID, alias or Vertex ID (case-insensitive).
func (c *Catalog) Lookup(name string) (*Model, bool) {
	m, ok := c.byName[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "models/")))]
	return m, ok
}

// List returns models, optionally filtered by media type, sorted with
// defaults first, then GA before preview, then by ID.
func (c *Catalog) List(mediaType string, includeInactive bool, now time.Time) []*Model {
	var out []*Model
	for _, m := range c.Models {
		if mediaType != "" && m.MediaType != mediaType {
			continue
		}
		if !includeInactive && !m.Active(now) {
			continue
		}
		out = append(out, m)
	}
	rank := func(m *Model) int {
		if c.IsDefault(m) {
			return 0
		}
		switch m.EffectiveStatus(now) {
		case StatusGA:
			return 1
		case StatusPreview:
			return 2
		case StatusDeprecated:
			return 3
		}
		return 4
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MediaType != out[j].MediaType {
			return out[i].MediaType < out[j].MediaType
		}
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// IsDefault reports whether m is the default for its media type.
func (c *Catalog) IsDefault(m *Model) bool {
	d, ok := c.Lookup(c.Defaults[m.MediaType])
	return ok && d == m
}

// ShutdownTime parses the shutdown date (end of that day, UTC).
func (m *Model) ShutdownTime() (time.Time, bool) {
	if m.Shutdown == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", m.Shutdown)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(24*time.Hour - time.Second), true
}

// EffectiveStatus accounts for shutdown dates that have passed.
func (m *Model) EffectiveStatus(now time.Time) string {
	if t, ok := m.ShutdownTime(); ok && now.After(t) {
		return StatusRetired
	}
	return m.Status
}

// Active reports whether the model can still be called.
func (m *Model) Active(now time.Time) bool {
	return m.EffectiveStatus(now) != StatusRetired
}

// SupportsBackend reports whether the model is offered on backend.
func (m *Model) SupportsBackend(backend string) bool {
	if backend == "" || backend == "auto" {
		return true // backend not resolved yet (e.g. no credentials)
	}
	return len(m.Backends) == 0 || slices.Contains(m.Backends, backend)
}

// APIID returns the model ID to send to the given backend.
func (m *Model) APIID(backend string) string {
	if backend == "vertex" && m.VertexID != "" {
		return m.VertexID
	}
	return m.ID
}

// VertexLocation picks the Vertex location for this model. A configured
// location is used when the model is offered there (or when the catalog does
// not say); otherwise the model's first listed location wins.
func (m *Model) VertexLocation(configured string, explicit bool) (string, string) {
	if len(m.VertexLocations) == 0 {
		return configured, ""
	}
	if slices.Contains(m.VertexLocations, configured) {
		return configured, ""
	}
	loc := m.VertexLocations[0]
	if explicit {
		return loc, fmt.Sprintf("%s is not offered in %s; using %s", m.ID, configured, loc)
	}
	return loc, ""
}
