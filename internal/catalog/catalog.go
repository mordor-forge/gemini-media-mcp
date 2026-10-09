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
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
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
		// An unknown backend would never match a request, and an unresolved
		// one ("auto") would make StatusOn recurse.
		for _, b := range m.Backends {
			if !knownBackend(b) {
				return fmt.Errorf("catalog: %s: backends lists %q; the backends are gemini-api and vertex", m.ID, b)
			}
		}
		// A date that does not parse would silently keep the model offered.
		if _, ok := parseDate(m.Shutdown); m.Shutdown != "" && !ok {
			return fmt.Errorf("catalog: %s: shutdown %q is not a YYYY-MM-DD date", m.ID, m.Shutdown)
		}
		for backend, d := range m.BackendShutdown {
			bt, ok := parseDate(d)
			if !ok {
				return fmt.Errorf("catalog: %s: backendShutdown %s %q is not a YYYY-MM-DD date", m.ID, backend, d)
			}
			// The global shutdown ends every backend, so a later backend date
			// would be listed but never take effect.
			if gt, ok := m.ShutdownTime(); ok && bt.After(gt) {
				return fmt.Errorf("catalog: %s: backendShutdown %s %s is after the model's shutdown %s; set it on or before %s, or drop it", m.ID, backend, d, m.Shutdown, m.Shutdown)
			}
			// A misspelled backend would never match, keeping the model on.
			if !knownBackend(backend) || !m.SupportsBackend(backend) {
				return fmt.Errorf("catalog: %s: backendShutdown names %q, which is not one of its backends (%s)", m.ID, backend, strings.Join(m.backendNames(), ", "))
			}
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
	// A redirect to a missing model or one of another media type would fail
	// the request after the cutoff, and a fallback must be offered on each
	// backend that switches requests to it.
	for _, m := range c.Models {
		for _, r := range []struct{ field, name string }{{"replacement", m.Replacement}, {"fallback", m.Fallback}} {
			if r.name == "" {
				continue
			}
			to, ok := c.Lookup(r.name)
			switch {
			case !ok:
				return fmt.Errorf("catalog: %s: %s %q is not in the catalog", m.ID, r.field, r.name)
			case to == m:
				return fmt.Errorf("catalog: %s: %s %q is the model itself", m.ID, r.field, r.name)
			case to.MediaType != m.MediaType:
				return fmt.Errorf("catalog: %s: %s %q generates %s, not %s", m.ID, r.field, r.name, to.MediaType, m.MediaType)
			}
		}
		if to, ok := c.Lookup(m.Fallback); ok {
			for _, b := range concreteBackends {
				if (!m.SupportsBackend(b) || m.BackendShutdown[b] != "") && !to.SupportsBackend(b) {
					return fmt.Errorf("catalog: %s: fallback %q is not offered on %s, where requests for %s switch to it; pick a fallback offered there (only %s)", m.ID, m.Fallback, b, m.ID, strings.Join(to.backendNames(), ", "))
				}
			}
		}
	}
	if err := c.checkRedirects(); err != nil {
		return err
	}
	// A default naming no model, a model of another media type or one its
	// backend does not offer would fail every request that relies on it.
	checkDefault := func(backend, mediaType, name string) error {
		scope := ""
		if backend != "" {
			scope = backend + " "
		}
		m, ok := c.Lookup(name)
		switch {
		case name == "":
			return nil
		case !ok:
			return fmt.Errorf("catalog: %sdefault %s model %q is not in the catalog", scope, mediaType, name)
		case m.MediaType != mediaType:
			return fmt.Errorf("catalog: %sdefault %s model %q generates %s", scope, mediaType, name, m.MediaType)
		case !m.SupportsBackend(backend):
			return fmt.Errorf("catalog: %sdefault %s model %q is not offered on %s (only %s)", scope, mediaType, name, backend, strings.Join(m.backendNames(), ", "))
		}
		return nil
	}
	for mediaType, name := range c.Defaults {
		if err := checkDefault("", mediaType, name); err != nil {
			return err
		}
		// It also serves every backend without a default of its own.
		for _, b := range concreteBackends {
			if m, ok := c.Lookup(name); ok && c.BackendDefaults[b][mediaType] == "" && !m.SupportsBackend(b) {
				return fmt.Errorf("catalog: default %s model %q is not offered on %s (only %s); set backendDefaults.%s.%s as well", mediaType, name, b, strings.Join(m.backendNames(), ", "), b, mediaType)
			}
		}
	}
	for backend, d := range c.BackendDefaults {
		if !knownBackend(backend) {
			return fmt.Errorf("catalog: backendDefaults names %q; the backends are gemini-api and vertex", backend)
		}
		for mediaType, name := range d {
			if err := checkDefault(backend, mediaType, name); err != nil {
				return err
			}
		}
	}
	return c.checkDefaults()
}

// checkRedirects makes sure redirects keep requests working: once a model
// name resolves on a backend, every later lifecycle date at which it
// redirects there (to its fallback or replacement) must still resolve, as
// the models along the chain retire in turn. A model that retires without
// a redirect may simply stop resolving.
func (c *Catalog) checkRedirects() error {
	dates := c.lifecycleDates()
	for _, m := range c.Models {
		if m.Fallback == "" && m.Replacement == "" {
			continue
		}
		for _, b := range concreteBackends {
			served := false
			for _, t := range dates {
				_, err := c.Resolve(m.ID, m.MediaType, b, t)
				if err == nil {
					served = true
					continue
				}
				field, target := m.redirect(b, t)
				if !served || target == "" {
					continue
				}
				var ae *apperr.Error
				if errors.As(err, &ae) {
					err = errors.New(ae.Message)
				}
				return fmt.Errorf("catalog: %s: from %s, requests on %s go to its %s %q, which cannot serve them (%v); pick a %s that stays callable on %s", m.ID, t.Format(time.DateOnly), b, field, target, err, field, b)
			}
		}
	}
	return nil
}

// lifecycleDates lists, in order, the zero time (before every date) and
// each distinct shutdown date in the catalog: the moments at which what a
// name resolves to can change.
func (c *Catalog) lifecycleDates() []time.Time {
	seen := map[time.Time]bool{}
	dates := []time.Time{{}}
	add := func(s string) {
		if t, ok := parseDate(s); ok && !seen[t] {
			seen[t] = true
			dates = append(dates, t)
		}
	}
	for _, m := range c.Models {
		add(m.Shutdown)
		for _, d := range m.BackendShutdown {
			add(d)
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	return dates
}

// checkDefaults makes sure a request without a model resolves on every
// backend at every lifecycle date, so a default that is retired, or
// retires with nowhere to go, cannot break them.
func (c *Catalog) checkDefaults() error {
	types := map[string]bool{}
	for mt := range c.Defaults {
		types[mt] = true
	}
	for _, d := range c.BackendDefaults {
		for mt := range d {
			types[mt] = true
		}
	}
	dates := c.lifecycleDates()
	for _, mt := range slices.Sorted(maps.Keys(types)) {
		for _, b := range concreteBackends {
			name := c.DefaultFor(mt, b)
			if name == "" {
				continue
			}
			for _, t := range dates {
				if _, err := c.Resolve("", mt, b, t); err != nil {
					var ae *apperr.Error
					if errors.As(err, &ae) {
						err = errors.New(ae.Message)
					}
					when := ""
					if !t.IsZero() {
						when = " from " + t.Format(time.DateOnly)
					}
					return fmt.Errorf("catalog: the default %s model on %s, %q, cannot serve requests%s (%v); pick a default that stays callable there", mt, b, name, when, err)
				}
			}
		}
	}
	return nil
}

// Successor names the model requests for m on backend go to once m is not
// offered there: now when it already is not, else from the date it ends
// there. It follows redirects as Resolve does, and is m's listed
// replacement when m has no end date there or nothing resolves.
func (c *Catalog) Successor(m *Model, backend string, now time.Time) string {
	at := now
	if m.OfferedOn(backend, now) {
		t, ok := parseDate(m.ShutdownOn(backend))
		if !ok {
			return m.Replacement
		}
		at = t
	}
	if r, err := c.Resolve(m.ID, m.MediaType, backend, at); err == nil && r.Model != m {
		return r.Model.ID
	}
	return m.Replacement
}

// concreteBackends are the backends the catalog describes.
var concreteBackends = []string{"gemini-api", "vertex"}

// knownBackend reports whether b is a backend the catalog describes.
func knownBackend(b string) bool { return slices.Contains(concreteBackends, b) }

// unresolved reports whether backend is not chosen yet (e.g. no credentials).
func unresolved(backend string) bool { return backend == "" || backend == "auto" }

// backendNames lists the backends m is offered on.
func (m *Model) backendNames() []string {
	if len(m.Backends) == 0 {
		return []string{"gemini-api", "vertex"}
	}
	return m.Backends
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
	defaults := map[string]*Model{}
	rank := func(m *Model) int {
		d, ok := defaults[m.MediaType]
		if !ok {
			d = c.EffectiveDefault(m.MediaType, backend, now)
			defaults[m.MediaType] = d
		}
		if d == m {
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

// EffectiveDefault is the model a request for mediaType without a model
// name uses on backend at now: the configured default after lifecycle
// redirects, or nil when that does not resolve to a catalog model.
func (c *Catalog) EffectiveDefault(mediaType, backend string, now time.Time) *Model {
	r, err := c.Resolve("", mediaType, backend, now)
	if err != nil || !r.Known {
		return nil
	}
	return r.Model
}

// IsDefault reports whether m is what a request for its media type without
// a model name uses on backend at now.
func (c *Catalog) IsDefault(m *Model, backend string, now time.Time) bool {
	return c.EffectiveDefault(m.MediaType, backend, now) == m
}

// ShutdownTime parses the shutdown date. Google shuts a model down on that
// date, so it counts as gone from the start of the day (UTC).
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
	return t, true
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
	if t, ok := m.ShutdownTime(); ok && !now.Before(t) {
		return StatusRetired
	}
	return m.Status
}

// StatusOn is EffectiveStatus on one backend: a backend shutdown date makes
// the model deprecated there before the date and retired from it on. With
// no backend resolved ("" or "auto"), the model is retired once every one
// of its backends has ended it.
func (m *Model) StatusOn(backend string, now time.Time) string {
	st := m.EffectiveStatus(now)
	if st == StatusRetired {
		return st
	}
	if unresolved(backend) {
		for _, b := range m.backendNames() {
			if m.StatusOn(b, now) != StatusRetired {
				return st
			}
		}
		return StatusRetired
	}
	if t, ok := parseDate(m.BackendShutdown[backend]); ok {
		if !now.Before(t) {
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

// BackendSummary lists the model's backends with their shutdown dates,
// e.g. "gemini-api until 2026-10-22, vertex" or "gemini-api ended 2026-10-22".
func (m *Model) BackendSummary(now time.Time) string {
	if len(m.Backends) == 0 && len(m.BackendShutdown) == 0 {
		if !m.Active(now) {
			return strings.TrimSpace("ended " + m.Shutdown)
		}
		return "all"
	}
	names := m.backendNames()
	parts := make([]string, 0, len(names))
	for _, b := range names {
		switch d := m.ShutdownOn(b); {
		case !m.OfferedOn(b, now):
			parts = append(parts, strings.TrimSpace(b+" ended "+d))
		case d != "":
			parts = append(parts, b+" until "+d)
		default:
			parts = append(parts, b)
		}
	}
	return strings.Join(parts, ", ")
}

// SupportsBackend reports whether the model is offered on backend.
func (m *Model) SupportsBackend(backend string) bool {
	if unresolved(backend) {
		return true
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
