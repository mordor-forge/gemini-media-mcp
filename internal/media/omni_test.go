package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/google/googletest"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
)

func TestOmniGenerateEditAndExtend(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	ref := filepath.Join(t.TempDir(), "hero.png")
	if err := os.WriteFile(ref, pngData(t), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := e.svc.GenerateVideo(context.Background(), VideoRequest{
		Prompt: "the hero waves", Model: "omni", Image: ref, ReferenceImages: []string{ref},
		Resolution: "360P", DurationSeconds: 6, NegativePrompt: "text overlays", Seed: ptr(3),
		OutputName: "hero", WaitSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != jobs.StateCompleted || len(job.Files) != 1 || job.Files[0].Name != "hero.mp4" {
		t.Fatalf("job = %+v", job)
	}
	call := e.api.InteractionCalls[0]
	if call.Model != "gemini-omni-1.1-flash" || call.Resolution != "360p" || call.DurationSeconds != 6 || len(call.Media) != 2 {
		t.Fatalf("request = %+v", call)
	}
	if !strings.HasPrefix(call.Prompt, "<FIRST_FRAME> the hero waves") || !strings.HasSuffix(call.Prompt, "Avoid: text overlays.") {
		t.Errorf("prompt = %q", call.Prompt)
	}
	if !strings.Contains(strings.Join(job.Warnings, "\n"), "seed is ignored") {
		t.Errorf("warnings = %v", job.Warnings)
	}
	// 6 s of video at $17.50/M video tokens (5792 tokens/s), reconciled from usage.
	if job.Cost.Basis != catalog.BasisUsage || job.Cost.USD < 0.608 || job.Cost.USD > 0.609 || job.Cost.Pending {
		t.Fatalf("cost = %+v", job.Cost)
	}
	if s := e.ledger.Summarize("all", 5); s.Calls != 1 || s.ByModel["gemini-omni-1.1-flash"] != job.Cost.USD || s.Totals.Pending != 0 {
		t.Fatalf("ledger = %+v", s)
	}
	stored, _ := e.jobs.Get(job.JobID)
	if stored.Interaction != "v1_interaction1" || stored.Operation != "interactions/v1_interaction1" || !strings.Contains(job.Next, "edit_video") {
		t.Fatalf("stored job = %+v, next %q", stored, job.Next)
	}

	// Edits of an Omni clip continue its interaction; no video is re-sent.
	edit, err := e.svc.EditVideo(context.Background(), EditVideoRequest{JobID: job.JobID, Prompt: "Make it night. Keep everything else the same.", WaitSeconds: 30})
	if err != nil || edit.State != jobs.StateCompleted {
		t.Fatalf("edit = %+v %v", edit, err)
	}
	if c := e.api.InteractionCalls[1]; c.PreviousID != "v1_interaction1" || len(c.Media) != 0 || c.Resolution != "360p" {
		t.Fatalf("edit request = %+v", c)
	}
	if edit.Files[0].Name != "hero-edit.mp4" {
		t.Errorf("edit name = %s", edit.Files[0].Name)
	}

	// Extension continues the edited clip.
	ext, err := e.svc.ExtendVideo(context.Background(), ExtendVideoRequest{JobID: edit.JobID, Prompt: "The scene continues.", WaitSeconds: 30})
	if err != nil || ext.State != jobs.StateCompleted {
		t.Fatalf("extend = %+v %v", ext, err)
	}
	if c := e.api.InteractionCalls[2]; c.PreviousID != "v1_interaction2" || c.DurationSeconds != 0 {
		t.Fatalf("extend request = %+v", c)
	}
	if ext.Cost.EstimatedUSD != 0.34 { // 10 s at 360p
		t.Errorf("extension estimate = %+v", ext.Cost)
	}
}

func TestOmniEditsFilesWithinLimits(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	dir := t.TempDir()
	long := filepath.Join(dir, "long.mp4")
	short := filepath.Join(dir, "short.mp4")
	_ = os.WriteFile(long, googletest.MP4(12), 0o600)
	_ = os.WriteFile(short, googletest.MP4(5), 0o600)

	if _, err := e.svc.EditVideo(context.Background(), EditVideoRequest{Video: long, Prompt: "x"}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("a 12 s clip must be refused with a trim hint: %v", err)
	}
	if _, err := e.svc.EditVideo(context.Background(), EditVideoRequest{Prompt: "x"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("jobId or video is required: %v", err)
	}
	if _, err := e.svc.EditVideo(context.Background(), EditVideoRequest{Video: short, Prompt: "x", Model: "lite"}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("Veo cannot edit videos: %v", err)
	}
	res, err := e.svc.EditVideo(context.Background(), EditVideoRequest{Video: short, Prompt: "Add rain. Keep everything else the same.", WaitSeconds: 30})
	if err != nil || res.State != jobs.StateCompleted || res.Files[0].Name != "short-edit.mp4" {
		t.Fatalf("edit = %+v %v", res, err)
	}
	c := e.api.InteractionCalls[0]
	if len(c.Media) != 1 || c.Media[0].Kind != "video" || c.PreviousID != "" {
		t.Fatalf("request = %+v", c)
	}
	if res.Cost.EstimatedUSD != 0.507 { // 5 s at 720p
		t.Errorf("estimate = %+v", res.Cost)
	}

	// A Veo clip is edited by sending its saved file.
	veo, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "waves", Model: "lite", WaitSeconds: 30})
	if err != nil || veo.State != jobs.StateCompleted {
		t.Fatalf("veo = %+v %v", veo, err)
	}
	if _, err := e.svc.EditVideo(context.Background(), EditVideoRequest{JobID: veo.JobID, Prompt: "x", WaitSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	if c := e.api.InteractionCalls[1]; len(c.Media) != 1 || c.Media[0].Kind != "video" || c.PreviousID != "" {
		t.Fatalf("veo clip edit request = %+v", c)
	}
}

func TestOmniFailures(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	ctx := context.Background()

	// A refused request costs nothing.
	e.api.InteractionFn = func(*google.InteractionRequest) (*google.InteractionResult, error) {
		return nil, genai.APIError{Code: 400, Message: "The prompt was blocked by safety filters."}
	}
	job, err := e.svc.GenerateVideo(ctx, VideoRequest{Prompt: "x", Model: "omni", WaitSeconds: 30})
	if err != nil || job.State != jobs.StateFiltered || job.Cost.USD != 0 {
		t.Fatalf("filtered = %+v %v", job, err)
	}

	// No answer in time: Google may still bill, so the estimate stays spent.
	e.api.InteractionFn = func(*google.InteractionRequest) (*google.InteractionResult, error) {
		return nil, context.DeadlineExceeded
	}
	job, err = e.svc.GenerateVideo(ctx, VideoRequest{Prompt: "x", Model: "omni", WaitSeconds: 30})
	if err != nil || job.State != jobs.StateFailed || job.Cost.USD != 0.8112 || !strings.Contains(job.Error, "may still produce") {
		t.Fatalf("timeout = %+v %v", job, err)
	}

	// An uploaded-video edit that returns nothing explains the regional limit.
	e.api.InteractionFn = func(*google.InteractionRequest) (*google.InteractionResult, error) {
		return &google.InteractionResult{ID: "v1_z", Status: "completed"}, nil
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	_ = os.WriteFile(clip, googletest.MP4(4), 0o600)
	job, err = e.svc.EditVideo(ctx, EditVideoRequest{Video: clip, Prompt: "x", WaitSeconds: 30})
	if err != nil || job.State != jobs.StateFailed || job.Cost.USD != 0 || !strings.Contains(job.Error, "EEA") {
		t.Fatalf("empty edit = %+v %v", job, err)
	}

	if s := e.ledger.Summarize("all", 10); s.Calls != 1 || s.Totals.All != 0.8112 {
		t.Fatalf("only the timed-out call counts: %+v", s.Totals)
	}
}

func TestOmniInterruptedWorkerIsReported(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	led := e.ledger
	entry, err := led.Record(spend.Entry{Tool: "generate_video", Model: "gemini-omni-1.1-flash", Status: spend.StatusPending, CostUSD: 0.81, EstimatedUSD: 0.81})
	if err != nil {
		t.Fatal(err)
	}
	j := &jobs.Job{ID: jobs.NewID(), Tool: "generate_video", Model: "gemini-omni-1.1-flash", State: jobs.StateWorking, Worker: "gone", LedgerID: entry.ID, EstimateUSD: 0.81}
	if err := e.jobs.Put(j); err != nil {
		t.Fatal(err)
	}
	// Fresh record of another process: still running.
	e.svc.now = time.Now
	got, err := e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: j.ID, WaitSeconds: ptr(0)})
	if err != nil || got.State != jobs.StateWorking || !strings.Contains(got.Next, "Another server process") {
		t.Fatalf("running elsewhere = %+v %v", got, err)
	}
	// No refresh for longer than omniStale: the worker is gone.
	e.svc.now = func() time.Time { return time.Now().Add(omniStale + time.Minute) }
	got, err = e.svc.GetVideo(context.Background(), GetVideoRequest{JobID: j.ID, WaitSeconds: ptr(0)})
	if err != nil || got.State != jobs.StateFailed || got.Cost.USD != 0.81 || !strings.Contains(got.Error, "stopped before it finished") {
		t.Fatalf("interrupted = %+v %v", got, err)
	}
	if s := led.Summarize("all", 5); s.Totals.Pending != 0 || s.Totals.All != 0.81 {
		t.Fatalf("ledger = %+v", s.Totals)
	}
}

func TestOmniIsGeminiAPIOnly(t *testing.T) {
	e := newEnv(t, &config.Auth{Backend: config.BackendVertex, Mode: config.AuthVertexADC, Project: "p", Location: "us-central1"}, spend.Budget{})
	_, err := e.svc.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", Model: "omni"})
	if apperr.KindOf(err) != apperr.NotFound || len(e.api.InteractionCalls) != 0 {
		t.Fatalf("omni on Vertex = %v", err)
	}
}
