package media

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// DefaultVoice is used when neither the request nor the config names one.
const DefaultVoice = "Aoede"

// secondVoice is the default for the second speaker in a dialogue.
const secondVoice = "Puck"

// SpeechRequest is the input of generate_speech.
type SpeechRequest struct {
	Text         string         `json:"text,omitempty" jsonschema:"The exact words to speak (single speaker). Newer models read it verbatim, so put delivery directions in style, not here. Inline tags like <sigh> or <short pause> are allowed."`
	Dialogue     []DialogueTurn `json:"dialogue,omitempty" jsonschema:"Alternative to text: a conversation between up to 2 named speakers, one entry per line."`
	Voice        string         `json:"voice,omitempty" jsonschema:"Voice for single-speaker text, e.g. Kore (firm), Puck (upbeat), Charon (informative), Aoede (breezy, default), Zephyr (bright). list_models shows all 30."`
	Speakers     []SpeakerVoice `json:"speakers,omitempty" jsonschema:"Voice per dialogue speaker; unlisted speakers get distinct defaults."`
	Style        string         `json:"style,omitempty" jsonschema:"How to deliver the lines, e.g. 'warm and enthusiastic', 'whispered urgently', 'slow, calm narrator'."`
	LanguageCode string         `json:"languageCode,omitempty" jsonschema:"Optional BCP-47 code (e.g. en-US, it-IT). Usually auto-detected from the text."`
	Model        string         `json:"model,omitempty" jsonschema:"Model ID or alias: tts (default, Gemini 3.8 Flash TTS), tts-lite (cheaper). See list_models."`
	Format       string         `json:"format,omitempty" jsonschema:"wav (default) or pcm (raw 16-bit little-endian)."`
	OutputName   string         `json:"outputName,omitempty" jsonschema:"Optional base file name."`
	// ApprovedCostUSD is only needed above the confirmation threshold.
	ApprovedCostUSD float64 `json:"approvedCostUsd,omitempty" jsonschema:"Only needed above the confirmation threshold."`
}

// DialogueTurn is one line of a dialogue.
type DialogueTurn struct {
	Speaker string `json:"speaker" jsonschema:"Speaker name, e.g. Host or Guest"`
	Text    string `json:"text" jsonschema:"The exact words"`
	Style   string `json:"style,omitempty" jsonschema:"Optional delivery for this line"`
}

// SpeakerVoice assigns a voice to a speaker.
type SpeakerVoice struct {
	Speaker string `json:"speaker"`
	Voice   string `json:"voice"`
}

// SpeechResult is the output of generate_speech.
type SpeechResult struct {
	File            store.Asset       `json:"file"`
	Model           string            `json:"model"`
	Voices          map[string]string `json:"voices"`
	DurationSeconds float64           `json:"durationSeconds,omitempty"`
	Cost            Cost              `json:"cost"`
	Warnings        []string          `json:"warnings,omitempty"`
}

// GenerateSpeech synthesizes speech.
func (s *Service) GenerateSpeech(ctx context.Context, req SpeechRequest) (*SpeechResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(req.Text)
	if (text == "") == (len(req.Dialogue) == 0) {
		return nil, apperr.Invalidf("provide either text or dialogue (not both)")
	}
	format := strings.ToLower(firstNonEmpty(req.Format, "wav"))
	if format != "wav" && format != "pcm" {
		return nil, apperr.Invalidf("format must be wav or pcm")
	}
	r, location, warnings, err := s.resolve(req.Model, catalog.Speech)
	if err != nil {
		return nil, err
	}
	m := r.Model

	// Speakers in order of first appearance.
	var speakers []string
	for i, t := range req.Dialogue {
		if strings.TrimSpace(t.Speaker) == "" || strings.TrimSpace(t.Text) == "" {
			return nil, apperr.Invalidf("dialogue line %d needs speaker and text", i+1)
		}
		if !slices.Contains(speakers, t.Speaker) {
			speakers = append(speakers, t.Speaker)
		}
	}
	if _, err := m.Validate(catalog.Params{"speakers": itoa(len(speakers))}, s.backend()); err != nil {
		return nil, err
	}

	voices := map[string]string{}
	defaultVoice := firstNonEmpty(req.Voice, s.cfg.Defaults.Voice, DefaultVoice)
	if len(req.Dialogue) > 0 {
		assigned := map[string]string{}
		for _, sv := range req.Speakers {
			assigned[sv.Speaker] = sv.Voice
		}
		for i, sp := range speakers {
			v := assigned[sp]
			if v == "" {
				v = defaultVoice
				if i == 1 {
					v = secondVoice
					if strings.EqualFold(voices[speakers[0]], secondVoice) {
						v = "Kore"
					}
				}
			}
			voices[sp] = v
		}
	} else {
		voices[""] = defaultVoice
	}
	metadataStyle := m.Capabilities.SpeechStyle == "metadata"
	customVoice := ""
	for sp, v := range voices {
		canonical, ok := canonicalVoice(m, v)
		switch {
		case ok:
			voices[sp] = canonical
		case metadataStyle && sp == "" && looksLikeVoiceID(v):
			customVoice = v // Extended Voice Library or custom voice ID
		case len(m.Capabilities.Voices) == 0:
			// Unknown model: pass the name through.
		default:
			return nil, apperr.Invalidf("unknown voice %q for %s; choose one of: %s", v, m.ID, strings.Join(m.Capabilities.Voices, ", "))
		}
	}

	parts, meta, spoken := buildSpeechParts(text, req.Dialogue, speakers, req.Style, metadataStyle)
	est := m.EstimateSpeech(spoken)
	res, err := s.reserve(&est, req.ApprovedCostUSD)
	if err != nil {
		return nil, err
	}

	sc := &genai.SpeechConfig{LanguageCode: req.LanguageCode}
	if len(speakers) > 1 {
		mc := &genai.MultiSpeakerVoiceConfig{}
		for _, sp := range speakers {
			mc.SpeakerVoiceConfigs = append(mc.SpeakerVoiceConfigs, &genai.SpeakerVoiceConfig{
				Speaker:     sp,
				VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: voices[sp]}},
			})
		}
		sc.MultiSpeakerVoiceConfig = mc
	} else {
		v := voices[""]
		if len(speakers) == 1 {
			v = voices[speakers[0]]
		}
		sc.VoiceConfig = &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: v}}
	}
	gcfg := &genai.GenerateContentConfig{ResponseModalities: []string{"AUDIO"}, SpeechConfig: sc}
	if hasMeta(meta) || customVoice != "" {
		gcfg.HTTPOptions = &genai.HTTPOptions{ExtrasRequestProvider: google.SpeechPatch(meta, customVoice)}
	}
	contents := []*genai.Content{{Role: string(genai.RoleUser), Parts: parts}}

	entry := spend.Entry{Tool: "generate_speech", Model: m.ID, MediaType: catalog.Speech, Units: map[string]any{"characters": len(spoken), "speakers": max(len(speakers), 1)}}
	progress(ctx, "synthesizing speech with "+m.ID, 0, 1)
	resp, err := s.api.GenerateContent(ctx, location, r.APIID, contents, gcfg)
	if err != nil {
		return nil, s.fail(res, entry, google.Classify(err, "speech generation", m.ID, s.backend()))
	}
	parsed, err := google.ParseResponse(resp, "audio/")
	if err != nil {
		return nil, s.fail(res, entry, err)
	}
	blob := parsed.Media[0]
	data, mime, ext, duration, err := normalizeSpeech(blob, format)
	if err != nil {
		entry.Status, entry.Usage, entry.Error = spend.StatusOK, parsed.Usage, "converting audio: "+err.Error()
		s.settle(res, entry, est, nil)
		return nil, fmt.Errorf("converting audio: %w", err)
	}
	if customVoice != "" {
		voices[""] = customVoice
	}
	prov := &store.Provenance{Tool: "generate_speech", Model: m.ID, Prompt: spoken, Params: map[string]any{"voices": voices, "style": req.Style, "languageCode": req.LanguageCode}}
	asset, err := s.store.Save("speech", req.OutputName, ext, data, mime, prov)
	if err != nil {
		entry.Status, entry.Usage, entry.Error = spend.StatusOK, parsed.Usage, "saving output: "+err.Error()
		cost := s.settle(res, entry, est, nil) // billed even though saving failed
		return nil, fmt.Errorf("the audio was generated and billed (~$%.4f) but could not be saved: %w", cost.USD, err)
	}
	asset.DurationSeconds = duration

	entry.Status = spend.StatusOK
	entry.Usage = parsed.Usage
	entry.Outputs = []string{asset.Path}
	var actual *catalog.Estimate
	if a, ok := m.CostFromUsage(tokenUsage(parsed.Usage), s.backend(), location); ok {
		actual = &a
	} else if duration > 0 && m.Pricing.OutputPer1M["audio"] > 0 {
		tok := duration * float64(max(m.Pricing.AudioTokensPerSecond, 25))
		a := catalog.Estimate{USD: tok * m.Pricing.OutputPer1M["audio"] / 1e6, Basis: catalog.BasisUnits, Breakdown: fmt.Sprintf("%.1fs of audio", duration)}
		actual = &a
	}
	cost := s.settle(res, entry, est, actual)
	if len(speakers) == 0 {
		voices = map[string]string{"narrator": voices[""]}
	}
	return &SpeechResult{
		File: *asset, Model: m.ID, Voices: voices, DurationSeconds: duration, Cost: cost,
		Warnings: dedupe(append(warnings, modelNotices(parsed)...)),
	}, nil
}

// buildSpeechParts renders the request for the model's style convention and
// returns the parts, per-part speech metadata, and the spoken text.
func buildSpeechParts(text string, dialogue []DialogueTurn, speakers []string, style string, metadataStyle bool) ([]*genai.Part, []map[string]any, string) {
	if metadataStyle {
		if len(dialogue) == 0 {
			var meta []map[string]any
			if style != "" {
				meta = []map[string]any{{"style": style}}
			}
			return []*genai.Part{{Text: text}}, meta, text
		}
		parts := make([]*genai.Part, 0, len(dialogue))
		meta := make([]map[string]any, 0, len(dialogue))
		var spoken []string
		for _, t := range dialogue {
			parts = append(parts, &genai.Part{Text: t.Text})
			md := map[string]any{}
			if len(speakers) > 1 {
				md["speaker"] = t.Speaker
			}
			if st := firstNonEmpty(t.Style, style); st != "" {
				md["style"] = st
			}
			if len(md) == 0 {
				md = nil
			}
			meta = append(meta, md)
			spoken = append(spoken, t.Text)
		}
		return parts, meta, strings.Join(spoken, "\n")
	}

	// Legacy models take directions inside the prompt text.
	if len(dialogue) == 0 {
		if style != "" {
			text = fmt.Sprintf("Say in a %s way: %s", style, text)
		}
		return []*genai.Part{{Text: text}}, nil, text
	}
	var b strings.Builder
	fmt.Fprintf(&b, "TTS the following conversation between %s", strings.Join(speakers, " and "))
	if style != "" {
		fmt.Fprintf(&b, " (%s)", style)
	}
	b.WriteString(":\n")
	for _, t := range dialogue {
		if t.Style != "" {
			fmt.Fprintf(&b, "%s: [%s] %s\n", t.Speaker, t.Style, t.Text)
		} else {
			fmt.Fprintf(&b, "%s: %s\n", t.Speaker, t.Text)
		}
	}
	out := strings.TrimSpace(b.String())
	return []*genai.Part{{Text: out}}, nil, out
}

func hasMeta(meta []map[string]any) bool {
	for _, m := range meta {
		if len(m) > 0 {
			return true
		}
	}
	return false
}

func canonicalVoice(m *catalog.Model, v string) (string, bool) {
	for _, known := range m.Capabilities.Voices {
		if strings.EqualFold(known, v) {
			return known, true
		}
	}
	return v, false
}

func looksLikeVoiceID(v string) bool {
	return strings.HasPrefix(v, "voice_") || strings.HasPrefix(v, "voicekey_") || strings.Contains(v, "/") || strings.Contains(v, "-")
}

// normalizeSpeech converts the API audio into the requested container.
// Legacy models return raw PCM (audio/L16); Gemini 3.8 returns a WAV file.
func normalizeSpeech(blob google.Blob, format string) (data []byte, mime, ext string, duration float64, err error) {
	isWAV := len(blob.Data) >= 12 && string(blob.Data[0:4]) == "RIFF" && string(blob.Data[8:12]) == "WAVE"
	pcmFmt, isPCM := store.ParsePCMMIME(blob.MIMEType)
	switch {
	case isWAV && format == "wav":
		return blob.Data, "audio/wav", "wav", wavDuration(blob.Data), nil
	case isWAV && format == "pcm":
		pcm, f, ok := wavPCM(blob.Data)
		if !ok {
			return blob.Data, "audio/wav", "wav", wavDuration(blob.Data), nil
		}
		return pcm, fmt.Sprintf("audio/L16;codec=pcm;rate=%d", f.SampleRate), "pcm", store.PCMDuration(len(pcm), f), nil
	case isPCM && format == "wav":
		wav, err := store.WrapWAV(blob.Data, pcmFmt)
		if err != nil {
			return nil, "", "", 0, err
		}
		return wav, "audio/wav", "wav", store.PCMDuration(len(blob.Data), pcmFmt), nil
	case isPCM:
		return blob.Data, blob.MIMEType, "pcm", store.PCMDuration(len(blob.Data), pcmFmt), nil
	}
	return blob.Data, blob.MIMEType, store.ExtFromMIME(blob.MIMEType), 0, nil
}

// wavPCM extracts the data chunk and format of a PCM WAV file.
func wavPCM(b []byte) ([]byte, store.PCMFormat, bool) {
	var f store.PCMFormat
	var data []byte
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		size := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		body := i + 8
		if size < 0 || body+size > len(b) {
			size = len(b) - body
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				f.Channels = int(binary.LittleEndian.Uint16(b[body+2:]))
				f.SampleRate = int(binary.LittleEndian.Uint32(b[body+4:]))
				f.BitsPerSample = int(binary.LittleEndian.Uint16(b[body+14:]))
			}
		case "data":
			data = b[body : body+size]
		}
		i = body + size + size%2
	}
	return data, f, data != nil && f.SampleRate > 0
}

func wavDuration(b []byte) float64 {
	data, f, ok := wavPCM(b)
	if !ok {
		return 0
	}
	return store.PCMDuration(len(data), f)
}
