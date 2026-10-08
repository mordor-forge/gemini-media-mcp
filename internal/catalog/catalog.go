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
	FamilyOmni        = "omni"         // Interactions API (synchronous video generation and editing)
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
	// BackendDefaults replaces Defaults on one backend (backend -> media
	// type -> model), for media types whose default is not offered there.
	BackendDefaults map[string]map[string]string `yaml:"backendDefaults" json:"backendDefaults,omitempty"`
	Models          []*Model                     `yaml:"models" json:"models"`

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
	// BackendShutdown holds earlier shutdown dates on single backends
	// (backend -> YYYY-MM-DD), e.g. a preview that leaves the Gemini API
	// while its GA version stays on Vertex AI. The model is deprecated on
	// that backend until the date and not offered there after it.
	BackendShutdown map[string]string `yaml:"backendShutdown" json:"backendShutdown,omitempty"`
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
	// MaxTotalSeconds caps the length of a clip grown by repeated extension.
	MaxTotalSeconds int `yaml:"maxTotalSeconds" json:"maxTotalSeconds,omitempty"`
	// VideoEdit marks models that edit existing videos (edit_video).
	VideoEdit bool `yaml:"videoEdit" json:"videoEdit,omitempty"`
	// MaxInputVideoSeconds caps a video sent for editing or extension.
	MaxInputVideoSeconds int      `yaml:"maxInputVideoSeconds" json:"maxInputVideoSeconds,omitempty"`
	NegativePrompt       bool     `yaml:"negativePrompt" json:"negativePrompt,omitempty"`
	Seed                 bool     `yaml:"seed" json:"seed,omitempty"`
	PersonGeneration     []string `yaml:"personGeneration" json:"personGeneration,omitempty"`
	AudioToggle          []string `yaml:"audioToggle" json:"audioToggle,omitempty"`
	MaxSpeakers          int      `yaml:"maxSpeakers" json:"maxSpeakers,omitempty"`
	Voices               []string `yaml:"voices" json:"voices,omitempty"`
	ImageInput           bool     `yaml:"imageInput" json:"imageInput,omitempty"`
	OutputSeconds        int      `yaml:"outputSeconds" json:"outputSeconds,omitempty"`
	MaxOutputSeconds     int      `yaml:"maxOutputSeconds" json:"maxOutputSeconds,omitempty"`
	OutputFormats        []string `yaml:"outputFormats" json:"outputFormats,omitempty"`
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

// List returns models, optionally filtered by media type, sorted with the
// backend's defaults first, then GA before preview, then by ID. Unless
// includeInactive is set, models retired on backend are left out (models
// the backend never offered stay, so callers can say where they run).
func (c *Catalog) List(mediaType, backend string, includeInactive bool, now time.Time) []*Model {
	var out []*Model
	for _, m := range c.Models {
		if mediaType != "" && m.MediaType != mediaType {
			continue
		}
		if !includeInactive && m.StatusOn(backend, now) == StatusRetired {
			continue
		}
		out = append(out, m)
	}
	rank := func(m *Model) int {
		if c.IsDefault(m, backend) {
			return 0
		}
		switch m.StatusOn(backend, now) {
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

// DefaultFor returns the default model name for mediaType on backend.
func (c *Catalog) DefaultFor(mediaType, backend string) string {
	if d := c.BackendDefaults[backend][mediaType]; d != "" {
		return d
	}
	return c.Defaults[mediaType]
}

// setDefault makes name the default for mediaType on every backend.
func (c *Catalog) setDefault(mediaType, name string) {
	if c.Defaults == nil {
		c.Defaults = map[string]string{}
	}
	c.Defaults[mediaType] = name
	for _, d := range c.BackendDefaults {
		delete(d, mediaType)
	}
}

// IsDefault reports whether m is the default for its media type on backend.
func (c *Catalog) IsDefault(m *Model, backend string) bool {
	d, ok := c.Lookup(c.DefaultFor(m.MediaType, backend))
	return ok && d == m
}

// ShutdownTime parses the shutdown date (end of that day, UTC).
func (m *Model) ShutdownTime() (time.Time, bool) {
	return parseDate(m.Shutdown)
}

func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(24*time.Hour - time.Second), true
}

// ShutdownOn returns the shutdown date that applies on backend: the
// backend's own date when set, otherwise the model's.
func (m *Model) ShutdownOn(backend string) string {
	if d := m.BackendShutdown[backend]; d != "" {
		return d
	}
	return m.Shutdown
}

// EffectiveStatus accounts for shutdown dates that have passed.
func (m *Model) EffectiveStatus(now time.Time) string {
	if t, ok := m.ShutdownTime(); ok && now.After(t) {
		return StatusRetired
	}
	return m.Status
}

// StatusOn is EffectiveStatus on one backend: a backend shutdown date makes
// the model deprecated there until the date and retired after it.
func (m *Model) StatusOn(backend string, now time.Time) string {
	st := m.EffectiveStatus(now)
	if t, ok := parseDate(m.BackendShutdown[backend]); ok && st != StatusRetired {
		if now.After(t) {
			return StatusRetired
		}
		return StatusDeprecated
	}
	return st
}

// Active reports whether the model can still be called on some backend.
func (m *Model) Active(now time.Time) bool {
	return m.EffectiveStatus(now) != StatusRetired
}

// OfferedOn reports whether the model can be called on backend at now.
func (m *Model) OfferedOn(backend string, now time.Time) bool {
	return m.SupportsBackend(backend) && m.StatusOn(backend, now) != StatusRetired
}

// BackendSummary lists the backends offering the model, with backend
// shutdown dates, e.g. "gemini-api until 2026-10-22, vertex".
func (m *Model) BackendSummary(now time.Time) string {
	if len(m.Backends) == 0 {
		return "all"
	}
	parts := make([]string, 0, len(m.Backends))
	for _, b := range m.Backends {
		switch d := m.BackendShutdown[b]; {
		case d == "":
			parts = append(parts, b)
		case m.OfferedOn(b, now):
			parts = append(parts, b+" until "+d)
		default:
			parts = append(parts, b+" ended "+d)
		}
	}
	return strings.Join(parts, ", ")
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
