package catalog

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
)

// Resolved is the outcome of resolving a user-supplied model name.
type Resolved struct {
	Model    *Model
	APIID    string
	Known    bool
	Warnings []string
}

// Resolve maps an alias, model ID or empty string (use the default) to a
// callable model for mediaType on backend (an empty name picks the backend's
// default). Retired models are redirected to their replacement, and models
// missing on the backend, or past their shutdown date there, to their
// fallback, with warnings the caller should surface. Unknown IDs are passed through
// when their family can be inferred, so newly launched models work before
// the catalog is updated.
func (c *Catalog) Resolve(name, mediaType, backend string, now time.Time) (*Resolved, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = c.DefaultFor(mediaType, backend)
	}
	m, ok := c.Lookup(name)
	if !ok {
		return c.resolveUnknown(name, mediaType, backend, now)
	}
	if m.MediaType != mediaType {
		return nil, apperr.Invalidf("model %q generates %s, not %s; use the %s tool or one of: %s", name, m.MediaType, mediaType, toolFor(m.MediaType), strings.Join(c.Names(mediaType, backend, now), ", "))
	}

	r := &Resolved{Known: true}
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		if seen[m.ID] {
			return nil, fmt.Errorf("catalog: redirect loop at %s", m.ID)
		}
		seen[m.ID] = true
		if !m.Active(now) {
			next, ok := c.Lookup(m.Replacement)
			if !ok {
				return nil, &apperr.Error{Kind: apperr.NotFound, Message: fmt.Sprintf("model %s was retired on %s", m.ID, m.Shutdown), Hint: "Call list_models to pick a current model."}
			}
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s was retired on %s; using its replacement %s", m.ID, m.Shutdown, next.ID))
			m = next
			continue
		}
		if !m.OfferedOn(backend, now) {
			// A passed backend shutdown redirects like a retirement on that
			// backend: to the fallback, else the replacement.
			ended := m.SupportsBackend(backend)
			_, target := m.redirect(backend, now)
			msg := fmt.Sprintf("%s is not available on %s", m.ID, backend)
			switch {
			case ended && unresolved(backend):
				msg = fmt.Sprintf("%s shut down on every backend (%s)", m.ID, m.BackendSummary(now))
			case ended:
				msg = fmt.Sprintf("%s shut down on %s on %s", m.ID, backendName(backend), m.BackendShutdown[backend])
			}
			next, ok := c.Lookup(target)
			if !ok {
				if !ended {
					msg = fmt.Sprintf("%s is not available on the %s backend", m.ID, backend)
				}
				return nil, &apperr.Error{Kind: apperr.NotFound, Message: "model " + msg + c.elsewhere(m, backend, now), Hint: "Call list_models to see models for this backend."}
			}
			r.Warnings = append(r.Warnings, msg+"; using "+next.ID+c.elsewhere(m, backend, now))
			m = next
			continue
		}
		break
	}
	if m.StatusOn(backend, now) == StatusDeprecated {
		r.Warnings = append(r.Warnings, c.deprecation(m, backend, now))
	}
	r.Model = m
	r.APIID = m.APIID(backend)
	return r, nil
}

// redirect names the field Resolve follows for m on backend at now, and
// its target: the replacement once m is retired; where m is not offered,
// the fallback, else (after a backend shutdown) the replacement. Both are
// empty while m is offered on backend.
func (m *Model) redirect(backend string, now time.Time) (field, target string) {
	switch {
	case !m.Active(now):
		return "replacement", m.Replacement
	case m.OfferedOn(backend, now):
		return "", ""
	case m.Fallback != "" || !m.SupportsBackend(backend):
		return "fallback", m.Fallback
	}
	return "replacement", m.Replacement
}

// deprecation words the warning for a deprecated model on backend.
func (c *Catalog) deprecation(m *Model, backend string, now time.Time) string {
	// Recommend the model requests will reach, following redirects.
	to := c.Successor(m, backend, now)
	if d := m.BackendShutdown[backend]; d != "" {
		msg := fmt.Sprintf("%s is deprecated on %s and shuts down there on %s", m.ID, backendName(backend), d)
		if to != "" {
			msg += "; switch to " + c.label(to)
		}
		return msg + c.elsewhere(m, backend, now)
	}
	msg := m.ID + " is deprecated"
	if m.Shutdown != "" {
		msg += " and shuts down on " + m.Shutdown
	}
	if to != "" {
		msg += "; switch to " + c.label(to)
	}
	return msg
}

// elsewhere names the other backends that still offer m, as a clause to
// append to a warning ("" when there are none).
func (c *Catalog) elsewhere(m *Model, backend string, now time.Time) string {
	if m.BackendShutdown[backend] == "" {
		return ""
	}
	var other []string
	for _, b := range m.backendNames() {
		if b != backend && m.OfferedOn(b, now) {
			other = append(other, fmt.Sprintf("%s on the %s backend (%s)", m.APIID(b), b, backendName(b)))
		}
	}
	if len(other) == 0 {
		return ""
	}
	return ". It stays available as " + strings.Join(other, " and as ")
}

// label is a model ID with its first alias, e.g. "gemini-omni-1.1-flash (omni)".
func (c *Catalog) label(id string) string {
	if m, ok := c.Lookup(id); ok && len(m.Aliases) > 0 {
		return m.ID + " (" + m.Aliases[0] + ")"
	}
	return id
}

// backendName is the product name of a backend for messages.
func backendName(backend string) string {
	switch backend {
	case "gemini-api":
		return "the Gemini API"
	case "vertex":
		return "Gemini Enterprise Agent Platform, formerly Vertex AI"
	}
	return backend
}

func (c *Catalog) resolveUnknown(name, mediaType, backend string, now time.Time) (*Resolved, error) {
	lower := strings.ToLower(strings.TrimPrefix(name, "models/"))
	switch {
	case strings.HasPrefix(lower, "imagen"):
		return nil, &apperr.Error{Kind: apperr.NotFound, Message: fmt.Sprintf("%s: Imagen models were shut down on the Gemini API (August 2026) and discontinued on Vertex AI", name), Hint: "Use gemini-nano-banana-2.1 (alias nb2) or gemini-3-pro-image (alias pro)."}
	}
	family, mt := InferFamily(lower)
	if family == "" {
		return nil, apperr.Invalidf("unknown %s model %q; known models and aliases: %s", mediaType, name, strings.Join(c.Names(mediaType, backend, now), ", "))
	}
	if mt != mediaType {
		return nil, apperr.Invalidf("model %q looks like a %s model; use the %s tool", name, mt, toolFor(mt))
	}
	m := &Model{ID: lower, Family: family, MediaType: mt, Status: StatusUnknown}
	return &Resolved{
		Model:    m,
		APIID:    lower,
		Warnings: []string{fmt.Sprintf("%s is not in the model catalog: parameters are not validated and cost cannot be estimated. Add it to a catalog override file to enable both.", lower)},
	}, nil
}

// InferFamily guesses the adapter family and media type from a model ID.
func InferFamily(id string) (family, mediaType string) {
	switch {
	case strings.Contains(id, "realtime"):
		return "", "" // streaming (Live API) models; this server cannot call them
	case strings.HasPrefix(id, "veo-"):
		return FamilyVeo, Video
	case strings.HasPrefix(id, "gemini-omni"):
		return FamilyOmni, Video
	case strings.HasPrefix(id, "lyria-"):
		return FamilyLyria, Music
	case strings.Contains(id, "tts"):
		return FamilyGeminiTTS, Speech
	case strings.HasPrefix(id, "gemini-") && (strings.Contains(id, "image") || strings.Contains(id, "banana")):
		return FamilyGeminiImage, Image // gemini-*-image and gemini-nano-banana-*
	}
	return "", ""
}

// Names lists the model IDs and aliases of a media type offered on backend.
func (c *Catalog) Names(mediaType, backend string, now time.Time) []string {
	var out []string
	for _, m := range c.List(mediaType, backend, false, now) {
		if !m.OfferedOn(backend, now) {
			continue
		}
		label := m.ID
		if len(m.Aliases) > 0 {
			label += " (" + strings.Join(m.Aliases, ", ") + ")"
		}
		out = append(out, label)
	}
	return out
}

func toolFor(mediaType string) string {
	switch mediaType {
	case Image:
		return "generate_image"
	case Video:
		return "generate_video"
	case Speech:
		return "generate_speech"
	case Music:
		return "generate_music"
	}
	return "matching"
}

// Params is a flat view of request parameters used for validation. Values
// are strings; the empty string means "not set".
type Params map[string]string

// Validate checks params against the model's capabilities and rules.
// It returns a single Invalid error listing every problem, plus warnings
// for backend-specific parameters that will be dropped.
func (m *Model) Validate(p Params, backend string) (dropped []string, err error) {
	if m.Status == StatusUnknown {
		return nil, nil
	}
	cp := m.Capabilities
	var problems []string
	oneOf := func(param string, allowed []string) {
		v := p[param]
		if v == "" || len(allowed) == 0 {
			return
		}
		if !slices.Contains(allowed, v) {
			problems = append(problems, fmt.Sprintf("%s %q is not supported by %s (allowed: %s)", param, v, m.ID, strings.Join(allowed, ", ")))
		}
	}
	oneOf("aspectRatio", cp.AspectRatios)
	oneOf("imageSize", cp.ImageSizes)
	oneOf("resolution", cp.Resolutions)
	oneOf("personGeneration", cp.PersonGeneration)
	oneOf("format", cp.OutputFormats)
	if v := p["durationSeconds"]; v != "" && len(cp.Durations) > 0 {
		n, _ := strconv.Atoi(v)
		if !slices.Contains(cp.Durations, n) {
			problems = append(problems, fmt.Sprintf("durationSeconds %s is not supported by %s (allowed: %s)", v, m.ID, joinInts(cp.Durations)))
		}
	}
	if v := p["referenceImages"]; v != "" {
		n, _ := strconv.Atoi(v)
		switch {
		case cp.MaxReferenceImages == 0:
			problems = append(problems, fmt.Sprintf("%s does not accept reference images", m.ID))
		case n > cp.MaxReferenceImages:
			problems = append(problems, fmt.Sprintf("%s accepts at most %d reference images, got %d", m.ID, cp.MaxReferenceImages, n))
		}
	}
	if p["image"] != "" && m.MediaType == Video && !cp.ImageToVideo {
		problems = append(problems, fmt.Sprintf("%s does not support image-to-video", m.ID))
	}
	if p["lastFrame"] != "" && !cp.LastFrame {
		problems = append(problems, fmt.Sprintf("%s does not support lastFrame", m.ID))
	}
	if p["negativePrompt"] != "" && m.MediaType == Video && !cp.NegativePrompt {
		problems = append(problems, fmt.Sprintf("%s does not support negativePrompt", m.ID))
	}
	if p["googleSearch"] == "true" && !cp.SearchGrounding {
		problems = append(problems, fmt.Sprintf("%s does not support Google Search grounding: drop googleSearch, or pick a model whose capabilities include searchGrounding (list_models with detail: true)", m.ID))
	}
	if v := p["speakers"]; v != "" && cp.MaxSpeakers > 0 {
		if n, _ := strconv.Atoi(v); n > cp.MaxSpeakers {
			problems = append(problems, fmt.Sprintf("%s supports at most %d speakers, got %d", m.ID, cp.MaxSpeakers, n))
		}
	}
	for _, r := range cp.Rules {
		if r.matches(p) && !r.satisfied(p) {
			problems = append(problems, r.Message)
		}
	}
	for _, name := range cp.VertexOnly {
		if backend != "vertex" && p[name] != "" {
			dropped = append(dropped, name)
		}
	}
	for _, name := range cp.GeminiAPIOnly {
		if backend == "vertex" && p[name] != "" && p[name] != "false" {
			dropped = append(dropped, name)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return dropped, &apperr.Error{Kind: apperr.Invalid, Message: strings.Join(problems, "; "), Hint: "Call list_models to see each model's supported values."}
	}
	return dropped, nil
}

func (r Rule) matches(p Params) bool {
	if len(r.When) == 0 {
		return false
	}
	for k, vals := range r.When {
		if !valueMatches(p[k], vals) {
			return false
		}
	}
	return true
}

func (r Rule) satisfied(p Params) bool {
	for k, vals := range r.Require {
		v := p[k]
		if v == "" && !slices.Contains(vals, "") && !slices.Contains(vals, "*") {
			continue // unset values get API defaults, which satisfy the rule
		}
		if !valueMatches(v, vals) {
			return false
		}
	}
	return true
}

func valueMatches(v string, vals []string) bool {
	for _, want := range vals {
		switch {
		case want == "*" && v != "":
			return true
		case want == v:
			return true
		}
	}
	return false
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}
