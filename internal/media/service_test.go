package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google/googletest"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	img.Set(1, 1, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type env struct {
	svc    *Service
	api    *googletest.Fake
	cfg    *config.Config
	ledger *spend.Ledger
	store  *store.Store
	jobs   *jobs.Registry
}

func newEnv(t *testing.T, auth *config.Auth, budget spend.Budget) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.OutputDir = filepath.Join(dir, "out")
	cfg.StateDir = filepath.Join(dir, "state")
	st, err := store.New(cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	led, err := spend.Open(filepath.Join(cfg.StateDir, "usage.jsonl"), budget)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := jobs.Open(filepath.Join(cfg.StateDir, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := catalog.NewSource("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		auth = &config.Auth{Backend: config.BackendGeminiAPI, Mode: config.AuthAPIKey, APIKey: "k"}
	}
	api := &googletest.Fake{BackendName: auth.Backend}
	svc := New(Deps{API: api, Auth: auth, Config: cfg, Catalog: src, Store: st, Jobs: reg, Ledger: led})
	svc.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	svc.sleep = func(context.Context, time.Duration) error { return nil }
	return &env{svc: svc, api: api, cfg: cfg, ledger: led, store: st, jobs: reg}
}

// ---- images ----

func TestGenerateImageEndToEnd(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	data := pngData(t)
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return googletest.ImageResponse(data), nil
	}

	ref := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(ref, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var progressed bool
	ctx := WithProgress(context.Background(), func(string, float64, float64) { progressed = true })
	res, err := e.svc.GenerateImage(ctx, ImageRequest{Prompt: "a cat", AspectRatio: "16:9", ImageSize: "2k", ReferenceImages: []string{ref}, OutputName: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	call := e.api.ContentCalls[0]
	if call.Model != "gemini-nano-banana-2.1" {
		t.Fatalf("default model = %s", call.Model)
	}
	if call.Config.ImageConfig == nil || call.Config.ImageConfig.AspectRatio != "16:9" || call.Config.ImageConfig.ImageSize != "2K" {
		t.Fatalf("image config = %+v", call.Config.ImageConfig)
	}
	if parts := call.Contents[0].Parts; len(parts) != 2 || parts[0].InlineData == nil || parts[1].Text != "a cat" {
		t.Fatalf("parts = %+v", parts)
	}
	if len(res.Files) != 1 || res.Files[0].Name != "cat.png" || res.Files[0].Width != 64 || res.Text != "A cat." {
		t.Fatalf("result = %+v", res)
	}
	if res.Cost.Basis != catalog.BasisUsage || res.Cost.USD < 0.0336 || res.Cost.USD > 0.034 { // NB 2.1: 1120 image tokens x $30/M + prompt/text
		t.Fatalf("cost = %+v", res.Cost)
	}
	if len(res.Previews) != 1 || !progressed {
		t.Fatal("expected a preview and progress notifications")
	}
	p, err := e.store.Provenance("cat.png")
	if err != nil || p.Model != "gemini-nano-banana-2.1" || p.Prompt != "a cat" {
		t.Fatalf("provenance = %+v %v", p, err)
	}
	if s := e.ledger.Summarize("session", 5); s.Calls != 1 || s.ByTool["generate_image"] != res.Cost.USD {
		t.Fatalf("ledger = %+v", s)
	}
}

func TestGenerateImageVariationsPartialFailure(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	data := pngData(t)
	var n int
	var mu sync.Mutex
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 2 {
			return nil, genai.APIError{Code: 503, Message: "overloaded"}
		}
		return googletest.ImageResponse(data), nil
	}
	res, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 2 || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[len(res.Warnings)-1], "1 of 3") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x", Count: 9}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatal("count > 4 must be rejected")
	}
}

func TestGenerateImageSafetyBlockIsClassifiedAndNotBilled(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonImageSafety}}}, nil
	}
	_, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if apperr.KindOf(err) != apperr.Safety {
		t.Fatalf("want safety error, got %v", err)
	}
	s := e.ledger.Summarize("all", 5)
	if s.Totals.All != 0 || len(s.Recent) != 1 || s.Recent[0].Status != spend.StatusFiltered {
		t.Fatalf("ledger = %+v", s)
	}
}

func TestRetiredModelRedirectAndConfirmation(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{ConfirmAboveUSD: 0.2})
	data := pngData(t)
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return googletest.ImageResponse(data), nil
	}
	res, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x", Model: "gemini-3-pro-image-preview"})
	if err != nil {
		t.Fatal(err)
	}
	if e.api.ContentCalls[0].Model != "gemini-3-pro-image" || len(res.Warnings) == 0 {
		t.Fatalf("expected redirect to GA pro model with a warning: %v", res.Warnings)
	}
	calls := len(e.api.ContentCalls)
	_, err = e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x", Model: "pro", ImageSize: "4K"})
	if apperr.KindOf(err) != apperr.Confirm || len(e.api.ContentCalls) != calls {
		t.Fatalf("expensive call must require approval before calling the API: %v", err)
	}
	if _, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x", Model: "pro", ImageSize: "4K", ApprovedCostUSD: 0.3}); err != nil {
		t.Fatalf("approved call failed: %v", err)
	}
}

func TestEditImageDefaultsToSourceModelAndRestrictsInputs(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	data := pngData(t)
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return googletest.ImageResponse(data), nil
	}
	first, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x", Model: "pro"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EditImage(context.Background(), EditImageRequest{Image: first.Files[0].URI, Prompt: "make it blue"}); err != nil {
		t.Fatal(err)
	}
	if got := e.api.ContentCalls[1].Model; got != "gemini-3-pro-image" {
		t.Fatalf("edit should reuse the source model, got %s", got)
	}

	// HTTP-style restricted input policy.
	f := false
	e.cfg.AllowAnyInputPath = &f
	outside := filepath.Join(t.TempDir(), "x.png")
	_ = os.WriteFile(outside, data, 0o600)
	if _, err := e.svc.EditImage(context.Background(), EditImageRequest{Image: outside, Prompt: "p"}); !errors.Is(err, store.ErrInputNotAllowed) {
		t.Fatalf("outside input must be rejected: %v", err)
	}
	if _, err := e.svc.EditImage(context.Background(), EditImageRequest{Image: first.Files[0].Name, Prompt: "p"}); err != nil {
		t.Fatalf("output-dir input by bare name must work: %v", err)
	}
}

// ---- video ----

func TestVideoLifecycle(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.api.OpDoneAfter = 1
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "waves", Model: "fast", Resolution: "720p", Seed: ptr(7)})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != jobs.StateWorking || !job.Cost.Pending || job.Cost.EstimatedUSD != 0.8 || !jobs.IsJobID(job.JobID) {
		t.Fatalf("job = %+v", job)
	}
	vc := e.api.VideoCalls[0]
	if vc.Model != "veo-3.1-fast-generate-preview" || vc.Config.Resolution != "720p" || vc.Config.Seed != nil {
		t.Fatalf("video call = %+v (seed must be dropped on the Gemini API)", vc.Config)
	}
	if !strings.Contains(strings.Join(job.Warnings, " "), "seed") {
		t.Fatalf("expected a dropped-seed warning: %v", job.Warnings)
	}
	if s := e.ledger.Summarize("all", 5); s.Totals.Pending != 0.8 {
		t.Fatalf("pending spend = %+v", s.Totals)
	}

	done, err := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if done.State != jobs.StateCompleted || len(done.Files) != 1 || done.Files[0].DurationSeconds != 8 || done.RemoteExpiresAt == nil {
		t.Fatalf("completed job = %+v", done)
	}
	if s := e.ledger.Summarize("all", 5); s.Totals.All != 0.8 || s.Totals.Pending != 0 {
		t.Fatalf("settled spend = %+v", s.Totals)
	}
	again, _ := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID})
	if e.api.Downloads != 1 || again.Files[0].Path != done.Files[0].Path {
		t.Fatal("completed jobs must not be downloaded twice")
	}

	ext, err := e.svc.ExtendVideo(context.Background(), ExtendVideoRequest{JobID: job.JobID, Prompt: "the wave crashes"})
	if err != nil {
		t.Fatal(err)
	}
	if src := e.api.VideoCalls[1].Source; src.Video == nil || src.Video.URI == "" || len(src.Video.VideoBytes) != 0 {
		t.Fatalf("extension must send the provider URI only: %+v", src.Video)
	}
	if ext.JobID == job.JobID {
		t.Fatal("extension must create a new job")
	}
}

func TestVideoValidationAndLegacyOperations(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	if _, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "lite", Resolution: "4k"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("lite 4k must fail: %v", err)
	}
	if _, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "nb2"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("image model for video must fail: %v", err)
	}
	if len(e.api.VideoCalls) != 0 {
		t.Fatal("invalid requests must not reach the API")
	}
	lite, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "lite", WaitSeconds: 30})
	if err != nil || lite.State != jobs.StateCompleted {
		t.Fatalf("waiting generate should complete: %+v %v", lite, err)
	}
	if _, err := e.svc.ExtendVideo(context.Background(), ExtendVideoRequest{JobID: lite.JobID, Prompt: "more"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("lite cannot be extended: %v", err)
	}
	hd, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "fast", Resolution: "1080p", WaitSeconds: 30})
	if err != nil || hd.State != jobs.StateCompleted {
		t.Fatalf("1080p job: %+v %v", hd, err)
	}
	if _, err := e.svc.ExtendVideo(context.Background(), ExtendVideoRequest{JobID: hd.JobID, Prompt: "more"}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "720p") {
		t.Fatalf("only 720p clips can be extended: %v", err)
	}
	// Operation names from v0.x of this server still work.
	legacy, err := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: "models/veo-3.1-generate-preview/operations/abc", WaitSeconds: ptr(0)})
	if err != nil || legacy.Model != "veo-3.1-generate-preview" || legacy.State != jobs.StateCompleted {
		t.Fatalf("legacy operation = %+v %v", legacy, err)
	}
	if _, err := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: "job_0123456789abcdef"}); apperr.KindOf(err) != apperr.NotFound {
		t.Fatalf("unknown job: %v", err)
	}
}

func TestVideoFilteredIsNotBilled(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.api.OpResult = func(name string) *genai.GenerateVideosOperation {
		return &genai.GenerateVideosOperation{Name: name, Done: true, Response: &genai.GenerateVideosResponse{RAIMediaFilteredCount: 1, RAIMediaFilteredReasons: []string{"celebrity"}}}
	}
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", WaitSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != jobs.StateFiltered || job.Cost.USD != 0 || !strings.Contains(job.Error, "celebrity") {
		t.Fatalf("job = %+v", job)
	}
	if s := e.ledger.Summarize("all", 5); s.Totals.All != 0 {
		t.Fatalf("filtered job billed: %+v", s.Totals)
	}
}

func TestVideoOnVertex(t *testing.T) {
	e := newEnv(t, &config.Auth{Backend: config.BackendVertex, Mode: config.AuthVertexADC, Project: "p", Location: "global"}, spend.Budget{})
	f := false
	if _, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "standard", GenerateAudio: &f, Seed: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	vc := e.api.VideoCalls[0]
	if vc.Model != "veo-3.1-generate-001" || vc.Location != "us-central1" || vc.Config.GenerateAudio == nil || vc.Config.Seed == nil {
		t.Fatalf("vertex call = %s @ %s %+v", vc.Model, vc.Location, vc.Config)
	}

	express := newEnv(t, &config.Auth{Backend: config.BackendVertex, Mode: config.AuthVertexExpress, APIKey: "k"}, spend.Budget{})
	if _, err := express.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"}); apperr.KindOf(err) != apperr.Permission {
		t.Fatalf("express mode video must be rejected: %v", err)
	}
}

// ---- speech & music ----

func TestSpeechLegacyPCMIsWrappedAsWAV(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	pcm := make([]byte, 48000)
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{InlineData: &genai.Blob{Data: pcm, MIMEType: "audio/L16;codec=pcm;rate=24000"}},
		}}}}}, nil
	}
	res, err := e.svc.GenerateSpeech(context.Background(), SpeechRequest{Text: "Hello there", Model: "tts-2.5", Style: "cheerful", Voice: "kore"})
	if err != nil {
		t.Fatal(err)
	}
	call := e.api.ContentCalls[0]
	if call.Config.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName != "Kore" {
		t.Fatal("voice names should be canonicalized")
	}
	if got := call.Contents[0].Parts[0].Text; !strings.Contains(got, "cheerful") || !strings.HasSuffix(got, "Hello there") {
		t.Fatalf("legacy models take style in the text: %q", got)
	}
	if call.Config.HTTPOptions != nil {
		t.Fatal("legacy models must not get speech metadata")
	}
	if res.File.MIMEType != "audio/wav" || !strings.HasSuffix(res.File.Name, ".wav") || res.DurationSeconds != 1 {
		t.Fatalf("result = %+v", res)
	}
	data, _ := os.ReadFile(res.File.Path)
	if string(data[:4]) != "RIFF" {
		t.Fatal("file must be a WAV")
	}
}

func TestSpeech38DialogueUsesMetadata(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	wav, _ := store.WrapWAV(make([]byte, 96000), store.PCMFormat{SampleRate: 24000, Channels: 1, BitsPerSample: 16})
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{InlineData: &genai.Blob{Data: wav, MIMEType: "audio/wav"}},
		}}}}}, nil
	}
	res, err := e.svc.GenerateSpeech(context.Background(), SpeechRequest{
		Dialogue: []DialogueTurn{{Speaker: "Joe", Text: "Hi Jane!", Style: "excited"}, {Speaker: "Jane", Text: "Hey."}},
		Style:    "casual",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := e.api.ContentCalls[0]
	if call.Model != "gemini-3.8-flash-tts" || len(call.Contents[0].Parts) != 2 || call.Contents[0].Parts[0].Text != "Hi Jane!" {
		t.Fatalf("3.8 dialogue must send one verbatim part per line: %+v", call.Contents[0].Parts)
	}
	mc := call.Config.SpeechConfig.MultiSpeakerVoiceConfig
	if mc == nil || len(mc.SpeakerVoiceConfigs) != 2 || res.Voices["Joe"] == res.Voices["Jane"] {
		t.Fatalf("multi-speaker config = %+v voices=%v", mc, res.Voices)
	}
	body := map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "Hi Jane!"}, map[string]any{"text": "Hey."}}}}}
	body = call.Config.HTTPOptions.ExtrasRequestProvider(body)
	parts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	md0 := parts[0].(map[string]any)["speechMetadata"].(map[string]any)
	md1 := parts[1].(map[string]any)["speechMetadata"].(map[string]any)
	if md0["speaker"] != "Joe" || md0["style"] != "excited" || md1["style"] != "casual" {
		t.Fatalf("speech metadata = %v %v", md0, md1)
	}
	if res.DurationSeconds != 2 || res.File.MIMEType != "audio/wav" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := e.svc.GenerateSpeech(context.Background(), SpeechRequest{Dialogue: []DialogueTurn{{Speaker: "A", Text: "1"}, {Speaker: "B", Text: "2"}, {Speaker: "C", Text: "3"}}}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatal("3 speakers must fail")
	}
	if _, err := e.svc.GenerateSpeech(context.Background(), SpeechRequest{Text: "x", Voice: "Nobody"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatal("unknown voice must fail")
	}
}

func TestMusicPromptAndFormat(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Text: "[Verse] la la"},
			{InlineData: &genai.Blob{Data: []byte("ID3fake"), MIMEType: "audio/wav"}},
		}}}}}, nil
	}
	res, err := e.svc.GenerateMusic(context.Background(), MusicRequest{Prompt: "lofi", Model: "full", BPM: 80, Instrumental: true, Format: "wav"})
	if err != nil {
		t.Fatal(err)
	}
	call := e.api.ContentCalls[0]
	if call.Model != "lyria-3.5" || call.Config.ResponseMIMEType != "audio/wav" {
		t.Fatalf("call = %s %+v", call.Model, call.Config)
	}
	if p := call.Contents[0].Parts[0].Text; !strings.Contains(p, "80 BPM") || !strings.Contains(p, "no vocals") {
		t.Fatalf("prompt = %q", p)
	}
	if res.Cost.USD != 0.08 || res.Lyrics != "[Verse] la la" || !strings.HasSuffix(res.File.Name, ".wav") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := e.svc.GenerateMusic(context.Background(), MusicRequest{Prompt: "x", Model: "clip", Format: "wav"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("clip has no wav: %v", err)
	}
}

// ---- info ----

func TestListModelsLiveAndEstimates(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{ConfirmAboveUSD: 1})
	e.api.Listed = []*genai.Model{{Name: "models/gemini-nano-banana-2.1"}, {Name: "models/veo-4.0-generate-preview"}, {Name: "models/gemini-9-pro"}}
	res, err := e.svc.ListModels(context.Background(), ListModelsRequest{Live: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Uncatalogued) != 1 || res.Uncatalogued[0] != "veo-4.0-generate-preview" {
		t.Fatalf("uncatalogued = %v", res.Uncatalogued)
	}
	var nb2, pro *ModelSummary
	for i := range res.Models {
		switch res.Models[i].ID {
		case "gemini-nano-banana-2.1":
			nb2 = &res.Models[i]
		case "gemini-3-pro-image":
			pro = &res.Models[i]
		}
		if res.Models[i].Status == catalog.StatusRetired {
			t.Fatal("retired models hidden by default")
		}
	}
	if nb2 == nil || !*nb2.Available || !nb2.Default || pro == nil || *pro.Available {
		t.Fatalf("availability flags wrong: %+v %+v", nb2, pro)
	}

	est, err := e.svc.EstimateCost(context.Background(), EstimateRequest{MediaType: "video", Model: "standard", Resolution: "4k", Compare: true})
	if err != nil {
		t.Fatal(err)
	}
	if est.Estimate.USD != 4.8 || !est.NeedsConfirm || len(est.Alternatives) != 2 {
		t.Fatalf("estimate = %+v", est)
	}
	for _, a := range est.Alternatives {
		if a.Model == "veo-3.1-lite-generate-preview" && a.Supports {
			t.Fatal("lite does not support 4k")
		}
	}
	info := e.svc.Info("stdio")
	if info.Defaults["image"] != "gemini-nano-banana-2.1" || info.AuthMode != string(config.AuthAPIKey) {
		t.Fatalf("info = %+v", info)
	}
	u, err := e.svc.Usage(context.Background(), UsageRequest{})
	if err != nil || u.Period != "today" {
		t.Fatalf("usage = %+v %v", u, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestUnconfiguredModeExplainsSetup(t *testing.T) {
	e := newEnv(t, config.Unconfigured(errors.New("no Google credentials configured")), spend.Budget{})
	_, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if apperr.KindOf(err) != apperr.Auth || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("want setup instructions, got %v", err)
	}
	if _, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"}); apperr.KindOf(err) != apperr.Auth {
		t.Fatalf("video: %v", err)
	}
	if est, err := e.svc.EstimateCost(context.Background(), EstimateRequest{MediaType: "image"}); err != nil || est.Estimate.USD == 0 {
		t.Fatalf("estimates must work without credentials: %+v %v", est, err)
	}
	res, err := e.svc.ListModels(context.Background(), ListModelsRequest{Live: true})
	if err != nil || len(res.Models) == 0 || len(res.Warnings) == 0 {
		t.Fatalf("list_models must work without credentials: %+v %v", res, err)
	}
	if info := e.svc.Info("stdio"); info.AuthMode != string(config.AuthUnconfigured) {
		t.Fatalf("info = %+v", info)
	}
	if len(e.api.ContentCalls)+len(e.api.VideoCalls) != 0 || e.ledger.Summarize("all", 5).Calls != 0 {
		t.Fatal("no API calls or ledger entries without credentials")
	}
}

func TestUnpricedModelsCannotBypassBudgets(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{DailyUSD: 5})
	_, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "veo-9.0-generate-preview"})
	if apperr.KindOf(err) != apperr.Confirm || len(e.api.VideoCalls) != 0 {
		t.Fatalf("unpriced model under a budget must require approval first: %v", err)
	}
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "veo-9.0-generate-preview", ApprovedCostUSD: 2})
	if err != nil {
		t.Fatal(err)
	}
	if job.Cost.EstimatedUSD != 2 || e.ledger.Summarize("all", 1).Totals.Pending != 2 {
		t.Fatalf("approved maximum should be reserved as the estimate: %+v", job.Cost)
	}
	// Without any budget configured, unpriced models just warn.
	free := newEnv(t, nil, spend.Budget{})
	if _, err := free.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "veo-9.0-generate-preview"}); err != nil {
		t.Fatalf("no budget, no approval needed: %v", err)
	}
}

func TestBilledOutputThatCannotBeSavedIsStillCharged(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	data := pngData(t)
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return googletest.ImageResponse(data), nil
	}
	// Replace the output directory with a file so saving fails.
	if err := os.RemoveAll(e.store.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.store.Dir(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "billed") {
		t.Fatalf("want a billed-but-unsaved error, got %v", err)
	}
	s := e.ledger.Summarize("all", 1)
	if s.Totals.All < 0.0336 || s.Recent[0].Status != spend.StatusOK || s.Recent[0].Error == "" {
		t.Fatalf("the billed call must be recorded as spent: %+v", s)
	}
}

func TestUndownloadableVideoStopsRetryingAndSettles(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.api.DownloadErr = genai.APIError{Code: 404, Message: "file expired"}
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateFailed || !strings.Contains(got.Error, "billed") {
		t.Fatalf("permanent download failure should end the job: %+v", got)
	}
	s := e.ledger.Summarize("all", 1)
	if s.Totals.Pending != 0 || s.Totals.All != 0.4 {
		t.Fatalf("the generated video is billed and no longer pending: %+v", s.Totals)
	}
	// The job reports what was spent, not $0 and "generate again" (regression),
	// now and on later calls.
	again, _ := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
	for _, v := range []*VideoJob{got, again} {
		if v.Cost.USD != 0.4 || v.Cost.Pending || !strings.Contains(v.Next, "billed") || strings.HasPrefix(v.Next, "Adjust the prompt") {
			t.Fatalf("billed undelivered job view: cost=%+v next=%q", v.Cost, v.Next)
		}
	}

	// Transient download errors are retried on later calls, up to a limit.
	e2 := newEnv(t, nil, spend.Budget{})
	e2.api.DownloadErr = genai.APIError{Code: 503, Message: "busy"}
	job, _ = e2.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"})
	for i := 0; i < maxDownloadAttempts; i++ {
		got, _ = e2.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
	}
	if got.State != jobs.StateFailed || e2.api.Downloads != maxDownloadAttempts {
		t.Fatalf("transient failures: state=%s downloads=%d", got.State, e2.api.Downloads)
	}
}

func TestCanceledWaitKeepsTheHandle(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.api.OpDoneAfter = 100
	e.svc.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", WaitSeconds: 60})
	if err != nil || job.JobID == "" || job.State != jobs.StateWorking {
		t.Fatalf("a canceled wait must still return the job handle: %+v %v", job, err)
	}
}

// Canceling the call that downloads a finished (billed) video must not make
// the video unrecoverable (regression).
func TestCanceledDownloadStaysRecoverable(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.api.DownloadHook = func(ctx context.Context) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	got, err := e.svc.GetVideo(ctx, GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateWorking || !strings.Contains(got.Next, "being downloaded") {
		t.Fatalf("a canceled call must leave the job recoverable: %+v", got)
	}
	close(release) // the download carries on without the canceled caller
	got, err = e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateCompleted || len(got.Files) != 1 || e.api.Downloads != 1 {
		t.Fatalf("after cancellation: state=%s files=%d downloads=%d", got.State, len(got.Files), e.api.Downloads)
	}

	// A download that fails because it was canceled or timed out is retried
	// on the next call and does not use up an attempt.
	e2 := newEnv(t, nil, spend.Budget{})
	job, _ = e2.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"})
	for _, interruption := range []error{context.Canceled, context.DeadlineExceeded, context.Canceled, context.DeadlineExceeded} {
		e2.api.DownloadErr = interruption
		got, _ = e2.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
		if got.State != jobs.StateWorking {
			t.Fatalf("interrupted download (%v) ended the job: %+v", interruption, got)
		}
		stored, _ := e2.jobs.Get(job.JobID)
		if stored.DownloadAttempts != 0 {
			t.Fatalf("interruptions must not count as attempts: %d", stored.DownloadAttempts)
		}
	}
	e2.api.DownloadErr = nil
	got, _ = e2.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)})
	if got.State != jobs.StateCompleted {
		t.Fatalf("retry after interruption: %+v", got)
	}
}

// waitSeconds bounds the whole call, including slow status requests and
// queueing behind another call that holds the job (regression).
func TestWaitBoundsUpstreamRequestsAndLocks(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.svc.now, e.svc.sleep = time.Now, sleepCtx
	e.api.OpDoneAfter = 1000
	e.api.PollHook = func(ctx context.Context) error { return sleepCtx(ctx, 3*time.Second) }
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, _ := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(1)})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a 1s wait took %v with a 3s status request", d)
	}
	if got.State != jobs.StateWorking || len(got.Warnings) != 0 {
		t.Fatalf("the end of a wait is not a failure: %+v", got)
	}
	// A zero wait still completes its single check.
	e.api.PollHook = func(ctx context.Context) error { return sleepCtx(ctx, 50*time.Millisecond) }
	polls := e.api.Polls
	if got, _ = e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(0)}); e.api.Polls != polls+1 || len(got.Warnings) != 0 {
		t.Fatalf("zero wait must check once: polls %d->%d %+v", polls, e.api.Polls, got)
	}

	// Queueing behind a call that is downloading the job.
	e.api.PollHook, e.api.OpDoneAfter = nil, 0
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.api.DownloadHook = func(ctx context.Context) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	first := make(chan *VideoJob)
	go func() {
		v, _ := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(30)})
		first <- v
	}()
	<-started
	start = time.Now()
	got, _ = e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: ptr(1)})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a 1s wait queued for %v behind a download", d)
	}
	if got.State != jobs.StateWorking || !strings.Contains(got.Next, "being downloaded") {
		t.Fatalf("queued call: %+v", got)
	}
	close(release)
	if v := <-first; v.State != jobs.StateCompleted {
		t.Fatalf("downloading call: %+v", v)
	}
}

// A response that consumed tokens but returned no image (MAX_TOKENS) is still
// billed: its usage and token cost must reach the ledger (regression).
func TestUsageWithoutMediaIsRecordedAndBilled(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	usage := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 100, ThoughtsTokenCount: 2000, TotalTokenCount: 2110}
	e.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{
			Candidates:    []*genai.Candidate{{FinishReason: genai.FinishReasonMaxTokens, Content: &genai.Content{Parts: []*genai.Part{{Text: "Let me think..."}}}}},
			UsageMetadata: usage,
		}, nil
	}
	_, err := e.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "MAX_TOKENS") || !strings.Contains(err.Error(), "billed") {
		t.Fatalf("want a no-image error that says the call was billed, got %v", err)
	}
	s := e.ledger.Summarize("all", 1)
	got := s.Recent[0]
	if got.Status != spend.StatusOK || got.CostUSD <= 0 || got.Basis != spend.BasisUsage || got.Usage == nil || got.Error == "" {
		t.Fatalf("ledger entry = %+v", got)
	}
	if s.Totals.All != got.CostUSD {
		t.Fatalf("totals = %+v", s.Totals)
	}

	// Safety blocks stay distinct: filtered and not charged, usage kept.
	e2 := newEnv(t, nil, spend.Budget{})
	e2.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonImageSafety}}, UsageMetadata: usage}, nil
	}
	if _, err := e2.svc.GenerateImage(context.Background(), ImageRequest{Prompt: "x"}); apperr.KindOf(err) != apperr.Safety || strings.Contains(err.Error(), "billed") {
		t.Fatalf("want an unbilled safety error, got %v", err)
	}
	s = e2.ledger.Summarize("all", 1)
	if got := s.Recent[0]; got.Status != spend.StatusFiltered || got.CostUSD != 0 || got.Usage == nil || s.Totals.All != 0 {
		t.Fatalf("filtered entry = %+v totals %+v", got, s.Totals)
	}

	// The same holds for speech.
	e3 := newEnv(t, nil, spend.Budget{})
	e3.api.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonMaxTokens}}, UsageMetadata: usage}, nil
	}
	if _, err := e3.svc.GenerateSpeech(context.Background(), SpeechRequest{Text: "Hello"}); err == nil || !strings.Contains(err.Error(), "billed") {
		t.Fatalf("speech: %v", err)
	}
	if got := e3.ledger.Summarize("all", 1).Recent[0]; got.Status != spend.StatusOK || got.CostUSD <= 0 || got.Usage == nil {
		t.Fatalf("speech entry = %+v", got)
	}
}
