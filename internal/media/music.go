package media

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// MusicRequest is the input of generate_music.
type MusicRequest struct {
	Prompt          string   `json:"prompt" jsonschema:"Describe the music: genre, mood, instruments, tempo, key, production style and song structure. Use [Intro]/[Verse]/[Chorus]/[Bridge]/[Outro] tags or [0:00-0:15] timestamps to control sections."`
	Model           string   `json:"model,omitempty" jsonschema:"clip (default: 30-second MP3, cheapest) or full (complete song up to about 3 minutes, vocals, WAV option). See list_models."`
	Lyrics          string   `json:"lyrics,omitempty" jsonschema:"Optional lyrics with section tags; they are sung in the language they are written in."`
	Instrumental    bool     `json:"instrumental,omitempty" jsonschema:"Ask for no vocals."`
	BPM             int      `json:"bpm,omitempty" jsonschema:"Optional tempo hint in beats per minute."`
	DurationSeconds int      `json:"durationSeconds,omitempty" jsonschema:"Optional target length hint for full songs (clip is always 30s)."`
	Images          []string `json:"images,omitempty" jsonschema:"Optional images (up to 10) whose mood or scene should inspire the music."`
	Format          string   `json:"format,omitempty" jsonschema:"mp3 (default) or wav (full model only)."`
	OutputName      string   `json:"outputName,omitempty" jsonschema:"Optional base file name."`
	ApprovedCostUSD float64  `json:"approvedCostUsd,omitempty" jsonschema:"Only needed above the confirmation threshold."`
}

// MusicResult is the output of generate_music.
type MusicResult struct {
	File     store.Asset `json:"file"`
	Model    string      `json:"model"`
	Lyrics   string      `json:"lyrics,omitempty" jsonschema:"Lyrics or song structure returned by the model"`
	Cost     Cost        `json:"cost"`
	Warnings []string    `json:"warnings,omitempty"`
}

// GenerateMusic creates a music track.
func (s *Service) GenerateMusic(ctx context.Context, req MusicRequest) (*MusicResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Prompt) == "" && strings.TrimSpace(req.Lyrics) == "" {
		return nil, apperr.Invalidf("prompt is required")
	}
	format := strings.ToLower(req.Format)
	r, location, warnings, err := s.resolve(req.Model, catalog.Music)
	if err != nil {
		return nil, err
	}
	m := r.Model
	images, err := s.loadInputs(req.Images, "image")
	if err != nil {
		return nil, err
	}
	for _, in := range images {
		if err := requireImage(in, "image"); err != nil {
			return nil, err
		}
	}
	if len(images) > 0 && !m.Capabilities.ImageInput && m.Status != catalog.StatusUnknown {
		return nil, apperr.Invalidf("%s does not accept images", m.ID)
	}
	if _, err := m.Validate(catalog.Params{"format": format, "referenceImages": itoa(len(images))}, s.backend()); err != nil {
		return nil, err
	}
	if req.DurationSeconds > 0 && m.Capabilities.OutputSeconds > 0 && req.DurationSeconds != m.Capabilities.OutputSeconds {
		warnings = append(warnings, fmt.Sprintf("%s always produces %ds clips; durationSeconds ignored (use model full for longer songs)", m.ID, m.Capabilities.OutputSeconds))
	}

	prompt := musicPrompt(req, m)
	est := m.EstimateMusic(1)
	res, err := s.reserve(&est, req.ApprovedCostUSD)
	if err != nil {
		return nil, err
	}

	parts := imageParts(images, prompt)
	gcfg := &genai.GenerateContentConfig{ResponseModalities: []string{"AUDIO", "TEXT"}}
	if format == "wav" {
		gcfg.ResponseMIMEType = "audio/wav"
	}
	entry := spend.Entry{Tool: "generate_music", Model: m.ID, MediaType: catalog.Music, Units: map[string]any{"requests": 1}}
	progress(ctx, "composing with "+m.ID, 0, 1)
	resp, err := s.api.GenerateContent(ctx, location, r.APIID, []*genai.Content{{Role: string(genai.RoleUser), Parts: parts}}, gcfg)
	if err != nil {
		return nil, s.fail(res, entry, google.Classify(err, "music generation", m.ID, s.backend()))
	}
	parsed, err := google.ParseResponse(resp, "audio/")
	if err != nil {
		var recorded, billable google.Usage
		if parsed != nil {
			recorded = parsed.Usage
			if apperr.KindOf(err) != apperr.Safety {
				billable = parsed.Usage
			}
		}
		return nil, s.settleUnusable(res, entry, err, m, recorded, billable, location, est)
	}
	blob := parsed.Media[0]
	mime := firstNonEmpty(blob.MIMEType, "audio/mpeg")
	asset, err := s.store.Save("music", req.OutputName, store.ExtFromMIME(mime), blob.Data, mime, &store.Provenance{
		Tool: "generate_music", Model: m.ID, Prompt: prompt, Inputs: refsOf(images), ModelText: parsed.Text,
	})
	if err != nil {
		entry.Status, entry.Usage, entry.Error = spend.StatusOK, parsed.Usage, "saving output: "+err.Error()
		cost := s.settle(res, entry, est, nil) // billed even though saving failed
		return nil, fmt.Errorf("the music was generated and billed (~$%.2f) but could not be saved: %w", cost.USD, err)
	}
	entry.Status = spend.StatusOK
	entry.Usage = parsed.Usage
	entry.Outputs = []string{asset.Path}
	cost := s.settle(res, entry, est, nil)
	return &MusicResult{
		File: *asset, Model: m.ID, Lyrics: parsed.Text, Cost: cost,
		Warnings: dedupe(append(warnings, modelNotices(parsed)...)),
	}, nil
}

// musicPrompt folds structured hints into the prompt; Lyria has no
// dedicated fields for tempo, vocals or length.
func musicPrompt(req MusicRequest, m *catalog.Model) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(req.Prompt))
	var hints []string
	if req.BPM > 0 {
		hints = append(hints, fmt.Sprintf("Tempo: %d BPM.", req.BPM))
	}
	if req.Instrumental {
		hints = append(hints, "Instrumental only, no vocals.")
	}
	if req.DurationSeconds > 0 && m.Capabilities.OutputSeconds == 0 {
		hints = append(hints, fmt.Sprintf("Length: about %d seconds.", req.DurationSeconds))
	}
	if len(hints) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(hints, " "))
	}
	if l := strings.TrimSpace(req.Lyrics); l != "" {
		b.WriteString("\n\nLyrics:\n")
		b.WriteString(l)
	}
	return b.String()
}
