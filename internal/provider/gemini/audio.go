package gemini

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// GenerateAudio creates speech audio from a text prompt using Gemini's
// GenerateContent API with audio response modality and SpeechConfig.
func (p *GeminiProvider) GenerateAudio(ctx context.Context, req provider.AudioRequest) (*provider.AudioResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	model := p.resolveModel("", "tts")

	voiceName := req.VoiceName
	if voiceName == "" {
		voiceName = "Aoede"
	}

	languageCode := req.LanguageCode
	if languageCode == "" {
		languageCode = "en-US"
	}

	contents := []*genai.Content{
		genai.NewContentFromText(req.Prompt, genai.RoleUser),
	}
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"AUDIO"},
	}

	if len(req.Speakers) > 0 {
		speakerConfigs := make([]*genai.SpeakerVoiceConfig, len(req.Speakers))
		for i, s := range req.Speakers {
			speakerConfigs[i] = &genai.SpeakerVoiceConfig{
				Speaker: s.Name,
				VoiceConfig: &genai.VoiceConfig{
					PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
						VoiceName: s.VoiceName,
					},
				},
			}
		}
		config.SpeechConfig = &genai.SpeechConfig{
			MultiSpeakerVoiceConfig: &genai.MultiSpeakerVoiceConfig{
				SpeakerVoiceConfigs: speakerConfigs,
			},
			LanguageCode: languageCode,
		}
	} else {
		config.SpeechConfig = &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: voiceName,
				},
			},
			LanguageCode: languageCode,
		}
	}

	resp, err := p.client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return nil, fmt.Errorf("generating audio: %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no audio returned by the API")
	}

	blob, ok := extractFirstNonEmptyInlineData(resp)
	if !ok {
		return nil, fmt.Errorf("no audio data found in API response")
	}

	ext := audioExtensionFromMIME(blob.MIMEType)
	filePath, err := p.saveAudio(blob.Data, ext)
	if err != nil {
		return nil, err
	}
	return &provider.AudioResult{
		FilePath: filePath,
		Model:    model,
		MimeType: blob.MIMEType,
	}, nil
}

// saveAudio writes raw audio bytes to the output directory with a generated filename.
func (p *GeminiProvider) saveAudio(data []byte, ext string) (string, error) {
	filename := generateFilename("audio", ext)
	filePath := filepath.Join(p.outputDir, filename)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing file %s: %w", filePath, err)
	}
	return filePath, nil
}

// audioExtensionFromMIME returns the file extension for a given audio MIME type.
// The Gemini TTS API typically returns "audio/L16;codec=pcm;rate=24000" (raw PCM).
func audioExtensionFromMIME(mime string) string {
	switch {
	case mime == "audio/wav" || mime == "audio/x-wav":
		return "wav"
	case mime == "audio/mpeg" || mime == "audio/mp3":
		return "mp3"
	case mime == "audio/ogg":
		return "ogg"
	case mime == "audio/flac":
		return "flac"
	case mime == "audio/aac":
		return "aac"
	case strings.HasPrefix(mime, "audio/L16"):
		return "pcm"
	default:
		return "wav"
	}
}
