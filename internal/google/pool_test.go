package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
)

// TestPoolAgainstFakeGeminiAPI exercises the real genai SDK serialization:
// the speech_metadata / voice injection and SDK retries on 429.
func TestPoolAgainstFakeGeminiAPI(t *testing.T) {
	var calls atomic.Int32
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/models/gemini-3.8-flash-tts:generateContent") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("api key header missing")
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad body: %v", err)
		}
		audio := base64.StdEncoding.EncodeToString([]byte("RIFF....WAVE"))
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"`+audio+`"}}]},"finishReason":"STOP"}],
		  "usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":50,"totalTokenCount":54,"candidatesTokensDetails":[{"modality":"AUDIO","tokenCount":50}]},
		  "modelStatus":{"modelStage":"STABLE","retirementTime":"2027-09-01T00:00:00Z"}}`)
	}))
	defer srv.Close()
	t.Setenv("GOOGLE_GEMINI_BASE_URL", srv.URL)
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	auth := &config.Auth{Backend: config.BackendGeminiAPI, Mode: config.AuthAPIKey, APIKey: "test-key"}
	pool, err := NewPool(context.Background(), auth, Options{Retry: config.Retry{Attempts: 3, InitialDelaySeconds: 0.01, MaxDelaySeconds: 0.02}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &genai.GenerateContentConfig{
		ResponseModalities: []string{"AUDIO"},
		SpeechConfig:       &genai.SpeechConfig{VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: "Kore"}}},
		HTTPOptions:        &genai.HTTPOptions{ExtrasRequestProvider: SpeechPatch([]map[string]any{{"style": "cheerful"}, nil}, "voice_abc")},
	}
	contents := []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Hello!"}, {Text: "Bye."}}}}
	resp, err := pool.GenerateContent(context.Background(), "", "gemini-3.8-flash-tts", contents, cfg)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one retry after 429, got %d calls", calls.Load())
	}

	parts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	md, ok := parts[0].(map[string]any)["speechMetadata"].(map[string]any)
	if !ok || md["style"] != "cheerful" {
		t.Fatalf("speechMetadata not serialized: %v", parts)
	}
	if _, ok := parts[1].(map[string]any)["speechMetadata"]; ok {
		t.Fatal("nil metadata must not be added")
	}
	vc := body["generationConfig"].(map[string]any)["speechConfig"].(map[string]any)["voiceConfig"].(map[string]any)
	if vc["voice"] != "voice_abc" || vc["prebuiltVoiceConfig"] != nil {
		t.Fatalf("voice not patched: %v", vc)
	}

	r, err := ParseResponse(resp, "audio/")
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage.OutputByModality["audio"] != 50 || r.ModelRetiresAt == nil || r.ModelStage != "STABLE" {
		t.Fatalf("parsed = %+v", r)
	}
}
