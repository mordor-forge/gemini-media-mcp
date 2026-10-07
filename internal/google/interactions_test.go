package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
	ix "google.golang.org/genai/interactions/models/interactions"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
)

func fakePool(t *testing.T, h http.HandlerFunc) *Pool {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", srv.URL)
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	auth := &config.Auth{Backend: config.BackendGeminiAPI, Mode: config.AuthAPIKey, APIKey: "test-key"}
	pool, err := NewPool(context.Background(), auth, Options{Retry: config.Retry{Attempts: 3, InitialDelaySeconds: 0.01, MaxDelaySeconds: 0.02}})
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

// TestCreateInteractionAgainstFakeAPI exercises the real Interactions SDK:
// request serialization, video and usage parsing, and file download.
func TestCreateInteractionAgainstFakeAPI(t *testing.T) {
	fileWait = time.Millisecond
	var body map[string]any
	var fileChecks atomic.Int32
	pool := fakePool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("api key header missing on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions":
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("bad body: %v", err)
			}
			_, _ = io.WriteString(w, `{"id":"v1_abc","status":"completed","model":"gemini-omni-1.1-flash","object":"interaction",
			  "steps":[{"type":"user_input","content":[{"type":"text","text":"x"}]},
			    {"type":"thought","content":[{"type":"thought","text":"planning"}]},
			    {"type":"model_output","content":[{"type":"text","text":"Here is your clip."},{"type":"video","mime_type":"video/mp4","uri":"`+"http://"+r.Host+`/v1beta/files/vid1:download?alt=media"}]}],
			  "usage":{"total_input_tokens":1200,"total_output_tokens":34752,"total_tokens":35952,
			    "input_tokens_by_modality":[{"modality":"text","tokens":30},{"modality":"image","tokens":1170}],
			    "output_tokens_by_modality":[{"modality":"video","tokens":34752}]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta/files/vid1":
			state := "PROCESSING"
			if fileChecks.Add(1) > 1 {
				state = "ACTIVE"
			}
			_, _ = io.WriteString(w, `{"name":"files/vid1","state":"`+state+`"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1beta/files/vid1:download"):
			_, _ = io.WriteString(w, "MP4DATA")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	res, err := pool.CreateInteraction(context.Background(), &InteractionRequest{
		Model: "gemini-omni-1.1-flash", Prompt: "<FIRST_FRAME> waves roll in",
		Media:       []MediaPart{{Kind: "image", Data: []byte("PNG"), MIMEType: "image/png"}},
		AspectRatio: "9:16", Resolution: "1080p", DurationSeconds: 6, PreviousID: "v1_prev", Task: "image_to_video",
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if body["model"] != "gemini-omni-1.1-flash" || body["previous_interaction_id"] != "v1_prev" {
		t.Errorf("model/previous id not sent: %v", body)
	}
	input, _ := body["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input = %v", body["input"])
	}
	img := input[0].(map[string]any)
	if img["type"] != "image" || img["mime_type"] != "image/png" || img["data"] != base64.StdEncoding.EncodeToString([]byte("PNG")) {
		t.Errorf("image part = %v", img)
	}
	if txt := input[1].(map[string]any); txt["type"] != "text" || txt["text"] != "<FIRST_FRAME> waves roll in" {
		t.Errorf("text part = %v", txt)
	}
	rf, _ := body["response_format"].(map[string]any)
	if rf["type"] != "video" || rf["delivery"] != "uri" || rf["aspect_ratio"] != "9:16" || rf["resolution"] != "1080p" || rf["duration"] != "6s" {
		t.Errorf("response_format = %v", body["response_format"])
	}
	gc, _ := body["generation_config"].(map[string]any)
	if vc, _ := gc["video_config"].(map[string]any); vc["task"] != "image_to_video" {
		t.Errorf("generation_config = %v", body["generation_config"])
	}

	if res.ID != "v1_abc" || res.Status != "completed" || res.Text != "Here is your clip." {
		t.Errorf("result = %+v", res)
	}
	if res.Video == nil || !strings.HasSuffix(res.Video.URI, "/v1beta/files/vid1:download?alt=media") || res.Video.MIMEType != "video/mp4" {
		t.Fatalf("video = %+v", res.Video)
	}
	if res.Usage.OutputByModality["video"] != 34752 || res.Usage.PromptByModality["image"] != 1170 || res.Usage.PromptTokens != 1200 {
		t.Errorf("usage = %+v", res.Usage)
	}

	data, err := pool.DownloadVideo(context.Background(), "", res.Video)
	if err != nil || string(data) != "MP4DATA" {
		t.Fatalf("download = %q, %v", data, err)
	}
	if fileChecks.Load() != 2 {
		t.Errorf("expected to wait for the file to become ACTIVE, got %d checks", fileChecks.Load())
	}
}

// TestCreateInteractionRetriesOnlyUnprocessedRequests: generation is billed,
// so a 500 must not be retried, while a 429 is.
func TestCreateInteractionRetriesOnlyUnprocessedRequests(t *testing.T) {
	for _, tc := range []struct {
		code      int
		wantCalls int32
		wantKind  apperr.Kind
	}{
		{429, 3, apperr.Quota},
		{500, 1, apperr.Unavailable},
		{400, 1, apperr.Invalid},
	} {
		var calls atomic.Int32
		pool := fakePool(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.code)
			_, _ = io.WriteString(w, `{"error":{"code":"`+http.StatusText(tc.code)+`","message":"nope `+http.StatusText(tc.code)+`"}}`)
		})
		_, err := pool.CreateInteraction(context.Background(), &InteractionRequest{Model: "gemini-omni-1.1-flash", Prompt: "x"}, time.Minute)
		if calls.Load() != tc.wantCalls {
			t.Errorf("HTTP %d: %d calls, want %d", tc.code, calls.Load(), tc.wantCalls)
		}
		var apiErr genai.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != tc.code || !strings.Contains(apiErr.Message, "nope") {
			t.Errorf("HTTP %d: error = %#v", tc.code, err)
		}
		if k := apperr.KindOf(Classify(err, "generating video", "gemini-omni-1.1-flash", "gemini-api")); k != tc.wantKind {
			t.Errorf("HTTP %d: kind = %s, want %s", tc.code, k, tc.wantKind)
		}
	}
}

func TestParseInteractionReportsFailures(t *testing.T) {
	res := parseInteraction(mustInteraction(t, `{"id":"v1_x","status":"failed","errors":[{"code":"SAFETY","message":"blocked by safety filters"}],
	  "steps":[{"type":"model_output","content":[],"error":{"code":3,"message":"no video"}}]}`))
	if res.Status != "failed" || res.Video != nil || !strings.Contains(res.Error, "blocked by safety filters") || !strings.Contains(res.Error, "no video") {
		t.Fatalf("result = %+v", res)
	}
	// Inline delivery (the default when uri is not requested) carries bytes.
	res = parseInteraction(mustInteraction(t, `{"id":"v1_y","status":"completed","output_video":{"type":"video","mime_type":"video/mp4","data":"`+base64.StdEncoding.EncodeToString([]byte("MP4"))+`"}}`))
	if res.Video == nil || string(res.Video.VideoBytes) != "MP4" {
		t.Fatalf("inline video = %+v", res.Video)
	}
}

func mustInteraction(t *testing.T, s string) *ix.Interaction {
	t.Helper()
	var in ix.Interaction
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		t.Fatal(err)
	}
	return &in
}
