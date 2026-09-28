//go:build e2e

// Live tests against Google's APIs. They cost real money (a few cents per
// run; the video test about $0.20) and only run with:
//
//	GEMINI_MEDIA_E2E=1 GEMINI_API_KEY=... go test -tags=e2e ./internal/media/ -run E2E -v -timeout 15m
//
// Set GEMINI_MEDIA_E2E_VIDEO=1 to include video, and
// GEMINI_MEDIA_E2E_OUTPUT_DIR=<dir> to keep the generated files for review
// (otherwise they go to a temporary directory that is deleted). Vertex AI
// works too (GOOGLE_CLOUD_PROJECT + ADC, or GOOGLE_GENAI_USE_VERTEXAI=true).
package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

func liveService(t *testing.T) *Service {
	t.Helper()
	if os.Getenv("GEMINI_MEDIA_E2E") != "1" {
		t.Skip("set GEMINI_MEDIA_E2E=1 to run live tests (they cost money)")
	}
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	if keep := os.Getenv("GEMINI_MEDIA_E2E_OUTPUT_DIR"); keep != "" {
		outDir = keep
	}
	cfg, err := config.Load(config.Flags{OutputDir: outDir}, config.OSEnv)
	if err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = filepath.Join(dir, "state")
	auth, err := config.ResolveAuth(cfg, config.OSEnv)
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	pool, err := google.NewPool(ctx, auth, google.Options{Retry: cfg.Retry, RequestTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.New(cfg.OutputDir)
	led, _ := spend.Open(filepath.Join(cfg.StateDir, "usage.jsonl"), spend.Budget{DailyUSD: 2})
	reg, _ := jobs.Open(filepath.Join(cfg.StateDir, "jobs"))
	src, _ := catalog.NewSource("", nil, nil)
	return New(Deps{API: pool, Auth: auth, Config: cfg, Catalog: src, Store: st, Jobs: reg, Ledger: led})
}

func TestE2E_ListModelsLive(t *testing.T) {
	s := liveService(t)
	res, err := s.ListModels(context.Background(), ListModelsRequest{Live: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Models {
		if m.Default && m.Available != nil && !*m.Available {
			t.Errorf("default model %s is not available to this key: update internal/catalog/models.yaml", m.ID)
		}
	}
	if len(res.Uncatalogued) > 0 {
		t.Logf("media models missing from the catalog: %v", res.Uncatalogued)
	}
}

func TestE2E_Image(t *testing.T) {
	s := liveService(t)
	res, err := s.GenerateImage(context.Background(), ImageRequest{Prompt: "A flat vector icon of a red paper boat on a white background", Model: "nb2-lite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("image %s cost %+v", res.Files[0].Path, res.Cost)
	edited, err := s.EditImage(context.Background(), EditImageRequest{Image: res.Files[0].URI, Prompt: "Make the boat blue; keep everything else identical"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("edited %s cost %+v", edited.Files[0].Path, edited.Cost)
}

func TestE2E_Speech(t *testing.T) {
	s := liveService(t)
	res, err := s.GenerateSpeech(context.Background(), SpeechRequest{Text: "The harbor lights flickered on, one by one, as the fog rolled in.", Style: "calm, unhurried narrator"})
	if err != nil {
		t.Fatal(err)
	}
	if res.File.MIMEType != "audio/wav" || res.DurationSeconds <= 0 {
		t.Fatalf("unexpected speech result %+v", res)
	}
	dlg, err := s.GenerateSpeech(context.Background(), SpeechRequest{Dialogue: []DialogueTurn{
		{Speaker: "Ana", Text: "Did you hear the thunder last night?"},
		{Speaker: "Ben", Text: "I slept right through it.", Style: "sleepy"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("speech %s (%.1fs), dialogue %s voices %v", res.File.Path, res.DurationSeconds, dlg.File.Path, dlg.Voices)
}

func TestE2E_Music(t *testing.T) {
	s := liveService(t)
	res, err := s.GenerateMusic(context.Background(), MusicRequest{Prompt: "Gentle acoustic guitar loop, warm and calm", Instrumental: true, BPM: 90})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("music %s (%s) lyrics=%q", res.File.Path, res.File.MIMEType, res.Lyrics)
}

func TestE2E_Video(t *testing.T) {
	s := liveService(t)
	if os.Getenv("GEMINI_MEDIA_E2E_VIDEO") != "1" {
		t.Skip("set GEMINI_MEDIA_E2E_VIDEO=1 to run the video test (~$0.20)")
	}
	job, err := s.GenerateVideo(context.Background(), VideoRequest{Prompt: "Gentle ocean waves rolling onto a sandy beach at sunset, soft wave sounds", Model: "lite", DurationSeconds: 4})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Minute)
	for job.State == jobs.StateWorking && time.Now().Before(deadline) {
		wait := 45
		if job, err = s.GetVideo(context.Background(), GetVideoRequest{JobID: job.JobID, WaitSeconds: &wait}); err != nil {
			t.Fatal(err)
		}
	}
	if job.State != jobs.StateCompleted || len(job.Files) == 0 {
		t.Fatalf("video job ended as %+v", job)
	}
	t.Logf("video %s (%.1fs) cost %+v", job.Files[0].Path, job.Files[0].DurationSeconds, job.Cost)
}
