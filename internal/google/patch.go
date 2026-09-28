package google

import "google.golang.org/genai"

// SpeechPatch returns a request-body hook that adds fields the released Go
// SDK does not model yet (Gemini 3.8 TTS): per-part speechMetadata
// ({speaker, style}) and a non-prebuilt voice ID in speechConfig.voiceConfig.
// meta[i] applies to contents[0].parts[i]; nil entries are skipped.
func SpeechPatch(meta []map[string]any, voice string) genai.ExtrasRequestProvider {
	return func(body map[string]any) map[string]any {
		if contents := asSlice(body["contents"]); len(contents) > 0 {
			if first := asMap(contents[0]); first != nil {
				for i, part := range asSlice(first["parts"]) {
					if i < len(meta) && len(meta[i]) > 0 {
						if pm := asMap(part); pm != nil {
							pm["speechMetadata"] = meta[i]
						}
					}
				}
			}
		}
		if voice != "" {
			gc := ensureMap(body, "generationConfig")
			sc := ensureMap(gc, "speechConfig")
			vc := ensureMap(sc, "voiceConfig")
			delete(vc, "prebuiltVoiceConfig")
			vc["voice"] = voice
		}
		return body
	}
}

func asSlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		out := make([]any, len(s))
		for i := range s {
			out[i] = s[i]
		}
		return out
	}
	return nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if m := asMap(parent[key]); m != nil {
		return m
	}
	m := map[string]any{}
	parent[key] = m
	return m
}
