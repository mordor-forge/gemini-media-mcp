package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// DefaultGetVideoWait is how long get_video blocks when waitSeconds is omitted.
// It stays below common MCP client tool timeouts (Codex defaults to 60s).
const DefaultGetVideoWait = 45

// geminiVideoRetention is how long the Gemini API keeps generated videos.
const geminiVideoRetention = 48 * time.Hour

// maxDownloadAttempts bounds retries of a finished job's download.
const maxDownloadAttempts = 3

// VideoRequest is the input of generate_video.
type VideoRequest struct {
	Prompt           string   `json:"prompt" jsonschema:"What happens in the clip: subject and action, camera movement, setting, lighting, style, and sound (dialogue in quotes, sound effects, ambience, music)."`
	Model            string   `json:"model,omitempty" jsonschema:"Model ID or alias: lite (default, cheapest Veo), omni (Gemini Omni Flash: best prompt adherence, editable and extendable to 40 s), fast (Veo 4K, ingredients) or standard (highest Veo quality). See list_models."`
	AspectRatio      string   `json:"aspectRatio,omitempty" jsonschema:"16:9 (default) or 9:16"`
	Resolution       string   `json:"resolution,omitempty" jsonschema:"720p (default), 1080p or 4k (not lite); omni also 360p for fast drafts. Veo 1080p and 4k require 8-second clips."`
	DurationSeconds  int      `json:"durationSeconds,omitempty" jsonschema:"Clip length: Veo 4, 6 or 8 seconds; omni 3-10 seconds (default 8)."`
	Image            string   `json:"image,omitempty" jsonschema:"Optional first frame (image-to-video): file path, gemini-media:// URI or data: URI."`
	LastFrame        string   `json:"lastFrame,omitempty" jsonschema:"Optional last frame; requires image. Veo interpolates between the two."`
	ReferenceImages  []string `json:"referenceImages,omitempty" jsonschema:"Images of characters, objects, products or styles to keep consistent: Veo fast/standard up to 3 (forces 8s, not with image/lastFrame); omni up to 10 including frames (name them <IMAGE_REF_0>... in the prompt)."`
	NegativePrompt   string   `json:"negativePrompt,omitempty" jsonschema:"What to avoid, as plain nouns (e.g. 'text overlays, blur'). For omni it is appended to the prompt."`
	Seed             *int     `json:"seed,omitempty" jsonschema:"Seed for more repeatable results (Vertex AI only; ignored on the Gemini API)."`
	PersonGeneration string   `json:"personGeneration,omitempty" jsonschema:"allow_all, allow_adult or dont_allow. The Gemini API requires allow_all for text-to-video and allow_adult for image-based modes."`
	GenerateAudio    *bool    `json:"generateAudio,omitempty" jsonschema:"Vertex AI only: set false for silent video at a lower price. The Gemini API always generates audio."`
	WaitSeconds      int      `json:"waitSeconds,omitempty" jsonschema:"Block up to this many seconds for the result (default 0 = return a job handle immediately). Keep under your client's tool timeout."`
	OutputName       string   `json:"outputName,omitempty" jsonschema:"Optional base file name for the saved video."`
	ApprovedCostUSD  float64  `json:"approvedCostUsd,omitempty" jsonschema:"Only needed above the confirmation threshold: the cost in USD the user approved."`
}

// ExtendVideoRequest is the input of extend_video.
type ExtendVideoRequest struct {
	JobID           string  `json:"jobId" jsonschema:"Job handle of a completed generate_video or extend_video call (operation names from older versions also work)."`
	Prompt          string  `json:"prompt" jsonschema:"What happens next. Veo adds about 7 seconds (720p) from the last second; omni adds up to 10 seconds using the last 10 seconds as context."`
	NegativePrompt  string  `json:"negativePrompt,omitempty"`
	Seed            *int    `json:"seed,omitempty" jsonschema:"Vertex AI only."`
	WaitSeconds     int     `json:"waitSeconds,omitempty" jsonschema:"Block up to this many seconds for the result (default 0)."`
	OutputName      string  `json:"outputName,omitempty"`
	ApprovedCostUSD float64 `json:"approvedCostUsd,omitempty" jsonschema:"Only needed above the confirmation threshold."`
}

// GetVideoRequest is the input of get_video.
type GetVideoRequest struct {
	JobID       string `json:"jobId" jsonschema:"Job handle returned by generate_video, extend_video or edit_video."`
	WaitSeconds *int   `json:"waitSeconds,omitempty" jsonschema:"Block up to this many seconds for completion (default 45, 0 = check once). The video is downloaded automatically when ready."`
}

// VideoJob is the output of the video tools.
type VideoJob struct {
	JobID           string        `json:"jobId" jsonschema:"Opaque handle; pass it to get_video, extend_video or edit_video"`
	State           string        `json:"state" jsonschema:"working, completed, failed or filtered"`
	Model           string        `json:"model"`
	ElapsedSeconds  int           `json:"elapsedSeconds"`
	Files           []store.Asset `json:"files,omitempty"`
	Error           string        `json:"error,omitempty"`
	Cost            Cost          `json:"cost"`
	Warnings        []string      `json:"warnings,omitempty"`
	Next            string        `json:"next,omitempty" jsonschema:"Suggested next step"`
	RemoteExpiresAt *time.Time    `json:"remoteExpiresAt,omitempty" jsonschema:"When Google deletes the server-side copy; extend before this time"`
}

// GenerateVideo starts a Veo job and optionally waits for it.
func (s *Service) GenerateVideo(ctx context.Context, req VideoRequest) (*VideoJob, error) {
	if strings.TrimSpace(req.Prompt) == "" && req.Image == "" {
		return nil, apperr.Invalidf("prompt is required")
	}
	if err := s.videoAllowed(); err != nil {
		return nil, err
	}
	r, location, warnings, err := s.resolve(req.Model, catalog.Video)
	if err != nil {
		return nil, err
	}
	m := r.Model

	var first, last *store.Input
	if req.Image != "" {
		if first, err = s.loadInput(req.Image, "image"); err != nil {
			return nil, err
		}
		if err := requireImage(first, "image"); err != nil {
			return nil, err
		}
	}
	if req.LastFrame != "" {
		if last, err = s.loadInput(req.LastFrame, "lastFrame"); err != nil {
			return nil, err
		}
		if err := requireImage(last, "lastFrame"); err != nil {
			return nil, err
		}
	}
	refs, err := s.loadInputs(req.ReferenceImages, "reference image")
	if err != nil {
		return nil, err
	}
	if m.Family == catalog.FamilyOmni {
		return s.generateOmni(ctx, req, r, first, last, refs, warnings)
	}

	seed := ""
	if req.Seed != nil {
		seed = fmt.Sprint(*req.Seed)
	}
	audio := ""
	if req.GenerateAudio != nil {
		audio = fmt.Sprint(*req.GenerateAudio)
	}
	dropped, err := m.Validate(catalog.Params{
		"aspectRatio":      req.AspectRatio,
		"resolution":       strings.ToLower(req.Resolution),
		"durationSeconds":  itoa(req.DurationSeconds),
		"image":            req.Image,
		"lastFrame":        req.LastFrame,
		"referenceImages":  itoa(len(refs)),
		"negativePrompt":   req.NegativePrompt,
		"personGeneration": req.PersonGeneration,
		"seed":             seed,
		"generateAudio":    audio,
	}, s.backend())
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, dropWarning(dropped, s.backend())...)

	vcfg := &genai.GenerateVideosConfig{
		AspectRatio:      req.AspectRatio,
		Resolution:       strings.ToLower(req.Resolution),
		NegativePrompt:   req.NegativePrompt,
		PersonGeneration: req.PersonGeneration,
	}
	if req.DurationSeconds > 0 {
		d := int32(req.DurationSeconds)
		vcfg.DurationSeconds = &d
	}
	if req.Seed != nil && !slices.Contains(dropped, "seed") {
		sd := int32(*req.Seed)
		vcfg.Seed = &sd
	}
	withAudio := true
	if req.GenerateAudio != nil && !slices.Contains(dropped, "generateAudio") {
		vcfg.GenerateAudio = req.GenerateAudio
		withAudio = *req.GenerateAudio
	}
	if last != nil {
		vcfg.LastFrame = &genai.Image{ImageBytes: last.Data, MIMEType: last.MIMEType}
	}
	for _, ref := range refs {
		vcfg.ReferenceImages = append(vcfg.ReferenceImages, &genai.VideoGenerationReferenceImage{
			Image:         &genai.Image{ImageBytes: ref.Data, MIMEType: ref.MIMEType},
			ReferenceType: genai.VideoGenerationReferenceTypeAsset,
		})
	}
	src := &genai.GenerateVideosSource{Prompt: req.Prompt}
	if first != nil {
		src.Image = &genai.Image{ImageBytes: first.Data, MIMEType: first.MIMEType}
	}

	est := m.EstimateVideo(strings.ToLower(req.Resolution), req.DurationSeconds, 1, withAudio, s.backend())
	params := map[string]any{
		"aspectRatio": req.AspectRatio, "resolution": req.Resolution, "durationSeconds": req.DurationSeconds,
		"negativePrompt": req.NegativePrompt, "personGeneration": req.PersonGeneration,
	}
	var inputs []string
	for _, in := range append([]*store.Input{first, last}, refs...) {
		if in != nil {
			inputs = append(inputs, in.Ref)
		}
	}
	params["inputs"] = inputs
	return s.startVideo(ctx, "generate_video", m, r.APIID, location, src, vcfg, est, req.Prompt, params, req.OutputName, "", req.ApprovedCostUSD, req.WaitSeconds, warnings)
}

// ExtendVideo continues a completed Veo clip.
func (s *Service) ExtendVideo(ctx context.Context, req ExtendVideoRequest) (*VideoJob, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, apperr.Invalidf("prompt is required")
	}
	if err := s.videoAllowed(); err != nil {
		return nil, err
	}
	parent, err := s.findJob(req.JobID)
	if err != nil {
		return nil, err
	}
	if parent.State != jobs.StateCompleted {
		if parent.State == jobs.StateWorking {
			return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("job %s is still running", parent.ID), Hint: "Call get_video until it completes, then extend it."}
		}
		return nil, apperr.Invalidf("job %s did not complete (%s); it cannot be extended", parent.ID, parent.State)
	}
	if parent.RemoteExpiresAt != nil && s.now().After(*parent.RemoteExpiresAt) {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("the server-side copy of job %s expired on %s", parent.ID, parent.RemoteExpiresAt.Format(time.RFC3339)), Hint: "Generate a new clip (e.g. with generate_video using the last frame as image) instead."}
	}

	c := s.catalog.Get()
	m, ok := c.Lookup(parent.Model)
	if !ok {
		return nil, apperr.Invalidf("model %s of job %s is not in the catalog", parent.Model, parent.ID)
	}
	if !m.Capabilities.Extend {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("%s does not support extension", m.ID), Hint: "Generate the first clip with omni, fast or standard to be able to extend it."}
	}
	if m.Family == catalog.FamilyOmni {
		return s.extendOmni(ctx, req, parent, m)
	}
	if res, _ := parent.Params["resolution"].(string); res != "" && !strings.EqualFold(res, "720p") {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("job %s is %s; Veo can only extend 720p clips", parent.ID, res), Hint: "Generate the base clip at 720p to extend it, or start a new clip from its last frame (generate_video with image)."}
	}
	var warnings []string
	seed := ""
	if req.Seed != nil {
		seed = fmt.Sprint(*req.Seed)
	}
	dropped, err := m.Validate(catalog.Params{"negativePrompt": req.NegativePrompt, "seed": seed}, s.backend())
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, dropWarning(dropped, s.backend())...)

	video, err := s.sourceVideo(ctx, parent)
	if err != nil {
		return nil, err
	}
	vcfg := &genai.GenerateVideosConfig{NegativePrompt: req.NegativePrompt}
	if req.Seed != nil && !slices.Contains(dropped, "seed") {
		sd := int32(*req.Seed)
		vcfg.Seed = &sd
	}
	seconds := max(m.Capabilities.ExtendSeconds, 1)
	// Extension billing granularity is undocumented; estimate a full 8s clip.
	est := m.EstimateVideo("720p", max(seconds, 8), 1, true, s.backend())
	src := &genai.GenerateVideosSource{Prompt: req.Prompt, Video: video}
	params := map[string]any{"extends": parent.ID, "negativePrompt": req.NegativePrompt}
	return s.startVideo(ctx, "extend_video", m, m.APIID(s.backend()), parent.Location, src, vcfg, est, req.Prompt, params, req.OutputName, parent.ID, req.ApprovedCostUSD, req.WaitSeconds, warnings)
}

func (s *Service) videoAllowed() error {
	if err := s.ready(); err != nil {
		return err
	}
	if s.auth.Mode == config.AuthVertexExpress {
		return &apperr.Error{Kind: apperr.Permission, Message: "Vertex AI express mode does not support video generation (Veo long-running operations)", Hint: "Use a Gemini API key, or Vertex AI with GOOGLE_CLOUD_PROJECT and Application Default Credentials."}
	}
	return nil
}

func (s *Service) startVideo(ctx context.Context, tool string, m *catalog.Model, apiID, location string, src *genai.GenerateVideosSource, vcfg *genai.GenerateVideosConfig, est catalog.Estimate, prompt string, params map[string]any, outName, parentID string, approved float64, wait int, warnings []string) (*VideoJob, error) {
	res, err := s.reserve(&est, approved)
	if err != nil {
		return nil, err
	}
	entry := spend.Entry{Tool: tool, Model: m.ID, MediaType: catalog.Video, Units: map[string]any{"resolution": firstNonEmpty(vcfg.Resolution, "720p"), "durationSeconds": params["durationSeconds"]}}

	op, err := s.api.GenerateVideos(ctx, location, apiID, src, vcfg)
	if err != nil {
		return nil, s.fail(res, entry, google.Classify(err, "starting video generation", m.ID, s.backend()))
	}
	if op == nil || op.Name == "" {
		return nil, s.fail(res, entry, apperr.New(apperr.Unavailable, "the API did not return an operation for the video job", "Retry the request."))
	}

	job := &jobs.Job{
		ID: jobs.NewID(), Operation: op.Name, Tool: tool, Model: m.ID, Location: location,
		Backend: s.backend(), Prompt: prompt, Params: params, OutputName: outName,
		EstimateUSD: est.USD, State: jobs.StateWorking, ParentID: parentID,
	}
	entry.Status = spend.StatusPending
	entry.OperationID = op.Name
	entry.CostUSD = est.USD
	entry.Basis = est.Basis
	entry.EstimatedUSD = est.USD
	entry.PriceAsOf = est.PriceAsOf
	entry.Backend = s.backend()
	settled, err := res.Settle(entry)
	if err != nil {
		s.log.Warn("recording spend failed", "err", err)
	}
	job.LedgerID = settled.ID
	if err := s.jobs.Put(job); err != nil {
		// The operation is already running (and billing): never drop the
		// handle. get_video accepts operation names as a fallback.
		s.log.Error("saving video job failed", "job", job.ID, "err", err)
		out := s.jobView(job)
		out.JobID = op.Name
		out.Warnings = append(warnings, "the job record could not be saved ("+err.Error()+"); pass this operation name as jobId to get_video")
		return out, nil
	}

	var out *VideoJob
	if wait <= 0 {
		out = s.jobView(job) // return the handle without an immediate re-poll
	} else {
		out = s.pollJob(ctx, job, op, wait)
	}
	out.Warnings = append(warnings, out.Warnings...)
	return out, nil
}

// GetVideo reports (and, when done, downloads) a video job.
func (s *Service) GetVideo(ctx context.Context, req GetVideoRequest) (*VideoJob, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	job, err := s.findJob(req.JobID)
	if err != nil {
		return nil, err
	}
	wait := DefaultGetVideoWait
	if req.WaitSeconds != nil {
		wait = *req.WaitSeconds
	}
	return s.pollJob(ctx, job, nil, wait), nil
}

var opModelRe = regexp.MustCompile(`models/([^/]+)/operations/`)
var opLocationRe = regexp.MustCompile(`locations/([^/]+)/`)

// findJob accepts job handles and, for compatibility, provider operation names.
func (s *Service) findJob(id string) (*jobs.Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, apperr.Invalidf("jobId is required")
	}
	if jobs.IsJobID(id) {
		j, err := s.jobs.Get(id)
		if errors.Is(err, jobs.ErrNotFound) {
			return nil, &apperr.Error{Kind: apperr.NotFound, Message: fmt.Sprintf("unknown job %s", id), Hint: fmt.Sprintf("Job records are kept for %d days. Start a new generate_video call.", int(jobs.Retention.Hours()/24))}
		}
		return j, err
	}
	if !strings.Contains(id, "operations/") {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("%q is not a job handle", id), Hint: "Pass the jobId returned by generate_video (it starts with job_)."}
	}
	if j, err := s.jobs.FindByOperation(id); err == nil {
		return j, nil
	}
	// An operation started by an older version of this server (or elsewhere).
	model := ""
	if mm := opModelRe.FindStringSubmatch(id); mm != nil {
		model = mm[1]
		if cm, ok := s.catalog.Get().Lookup(model); ok {
			model = cm.ID
		}
	}
	loc := ""
	if lm := opLocationRe.FindStringSubmatch(id); lm != nil {
		loc = lm[1]
	}
	j := &jobs.Job{ID: jobs.NewID(), Operation: id, Tool: "generate_video", Model: model, Location: loc, Backend: s.backend(), State: jobs.StateWorking}
	if err := s.jobs.Put(j); err != nil {
		return nil, err
	}
	return j, nil
}

// lockJob serializes work on one job across concurrent calls. It gives up
// when ctx ends, so a caller's wait also bounds the time spent queueing
// behind another caller (for example one that is downloading the video).
func (s *Service) lockJob(ctx context.Context, id string) (func(), error) {
	v, _ := s.jobLocks.LoadOrStore(id, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pollJob checks the job until it finishes or wait elapses. fresh may carry
// the just-created operation (skips the first status call). It never returns
// an error: the job keeps running server-side, so callers always get a handle.
//
// A positive wait bounds the whole call, upstream status requests and lock
// queueing included. A zero wait checks once, without a deadline.
func (s *Service) pollJob(ctx context.Context, job *jobs.Job, fresh *genai.GenerateVideosOperation, wait int) *VideoJob {
	wait = min(max(wait, 0), s.cfg.MaxVideoWaitSeconds)
	deadline := s.now().Add(time.Duration(wait) * time.Second)
	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(wait)*time.Second)
		defer cancel()
	}
	interval := 5 * time.Second
	skipFetch := fresh != nil && !fresh.Done

	for {
		if !skipFetch {
			view, done := s.checkJob(ctx, job)
			if done {
				return view.VideoJob
			}
			job = view.job
		}
		skipFetch = false
		remaining := deadline.Sub(s.now())
		if remaining <= 0 || ctx.Err() != nil {
			return s.jobView(job)
		}
		elapsed := s.now().Sub(job.CreatedAt).Seconds()
		progress(ctx, fmt.Sprintf("video job %s running for %.0fs", job.ID, elapsed), elapsed, 0)
		if done, ok := s.omniActive.Load(job.ID); ok {
			// This process runs the interaction: wake up as soon as it ends.
			select {
			case <-done.(chan struct{}):
			case <-ctx.Done():
				return s.jobView(job)
			case <-time.After(min(interval, remaining)):
			}
		} else if err := s.sleep(ctx, min(interval, remaining)); err != nil {
			return s.jobView(job) // the wait ended or the client left; the job keeps running
		}
		interval = min(interval+5*time.Second, 15*time.Second)
	}
}

type jobCheck struct {
	*VideoJob
	job *jobs.Job
}

// checkJob polls the operation once and finishes the job when it is done.
// It holds the per-job lock only for this step, so concurrent waiters on the
// same job don't serialize their waits. done reports that the caller should
// stop waiting (finished job, the end of its wait, or an error worth
// surfacing).
func (s *Service) checkJob(ctx context.Context, job *jobs.Job) (jobCheck, bool) {
	unlock, err := s.lockJob(ctx, job.ID)
	if err != nil {
		// The wait ended while another call held the job (usually while
		// downloading it). Report the latest state; nothing is lost.
		if latest, gerr := s.jobs.Get(job.ID); gerr == nil {
			job = latest
		}
		return jobCheck{s.jobView(job), job}, true
	}
	handedOff := false
	defer func() {
		if !handedOff {
			unlock()
		}
	}()
	if latest, err := s.jobs.Get(job.ID); err == nil {
		job = latest // another call may have finished it
	}
	if job.Done() {
		return jobCheck{s.jobView(job), job}, true
	}
	if job.Worker != "" {
		return s.checkWorkerJob(job)
	}
	op, err := s.api.GetVideosOperation(ctx, job.Location, &genai.GenerateVideosOperation{Name: job.Operation})
	if err != nil {
		out := s.jobView(job)
		// A status request cut short by the end of the wait (or a client
		// that left) is not a failure worth reporting.
		if cerr := google.Classify(err, "checking video job", job.Model, s.backend()); ctx.Err() == nil && apperr.KindOf(cerr) != apperr.Canceled {
			out.Warnings = append(out.Warnings, "status check failed: "+truncate(cerr.Error(), 200))
			out.Next = "The job is still tracked. Call get_video again in a little while."
		}
		return jobCheck{out, job}, true
	}
	if !op.Done {
		return jobCheck{nil, job}, false
	}

	// The operation finished (a video that exists is billed): settle it and
	// download its outputs detached from this call, so neither the end of
	// the wait nor a disconnecting client can interrupt the download. The
	// job lock passes to that goroutine, which records the outcome in the
	// job registry for this or any later get_video call.
	fetched := *job
	finished := make(chan struct{})
	s.fetching.Store(job.ID, struct{}{})
	handedOff = true
	go func() {
		defer unlock()
		defer close(finished)
		defer s.fetching.Delete(fetched.ID)
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.downloadTimeout())
		defer cancel()
		s.finishJob(dctx, &fetched, op)
	}()
	select {
	case <-finished:
		return jobCheck{s.jobView(&fetched), &fetched}, fetched.Done()
	case <-ctx.Done():
		return jobCheck{s.jobView(job), job}, true
	}
}

// downloadTimeout bounds one background download of a job's outputs.
func (s *Service) downloadTimeout() time.Duration {
	if d := s.cfg.RequestTimeout(); d > 0 {
		return d
	}
	return 10 * time.Minute
}

// finishJob downloads outputs and settles spend for a finished operation.
func (s *Service) finishJob(ctx context.Context, job *jobs.Job, op *genai.GenerateVideosOperation) {
	now := s.now().UTC()
	job.CompletedAt = &now
	var status string
	switch {
	case len(op.Error) > 0:
		job.State, job.Error, status = jobs.StateFailed, google.OperationError(op.Error), spend.StatusFailed
	case op.Response == nil || len(op.Response.GeneratedVideos) == 0:
		if op.Response != nil && op.Response.RAIMediaFilteredCount > 0 {
			job.State, status = jobs.StateFiltered, spend.StatusFiltered
			job.Error = "blocked by safety filters"
			if len(op.Response.RAIMediaFilteredReasons) > 0 {
				job.Error += ": " + strings.Join(op.Response.RAIMediaFilteredReasons, "; ")
			}
		} else {
			job.State, job.Error, status = jobs.StateFailed, "the operation finished without a video", spend.StatusFailed
		}
	default:
		var assets []store.Asset
		permanent, interrupted := false, false
		for _, gv := range op.Response.GeneratedVideos {
			if gv == nil || gv.Video == nil {
				continue
			}
			data, err := s.api.DownloadVideo(ctx, job.Location, gv.Video)
			if err != nil {
				cerr := google.Classify(err, "downloading video", job.Model, s.backend())
				switch apperr.KindOf(cerr) {
				case apperr.NotFound, apperr.Permission, apperr.Invalid:
					// Expired remote files, Cloud Storage outputs, or requests
					// retrying cannot fix.
					permanent = true
				case apperr.Canceled, apperr.Timeout:
					// Interrupted, not refused: the file is still there.
					interrupted = true
				}
				job.Error = "download failed: " + cerr.Error()
				continue
			}
			mime := firstNonEmpty(gv.Video.MIMEType, "video/mp4")
			asset, err := s.store.Save("video", job.OutputName, store.ExtFromMIME(mime), data, mime, &store.Provenance{
				Tool: job.Tool, Model: job.Model, Prompt: job.Prompt, Params: job.Params, OperationID: job.Operation, CostUSD: job.EstimateUSD,
			})
			if err != nil {
				job.Error = "saving video: " + err.Error()
				continue
			}
			asset.DurationSeconds = MP4Duration(data)
			assets = append(assets, *asset)
		}
		if len(assets) == 0 {
			// Interruptions don't count as attempts: the video is paid for,
			// and the provider's retention (NotFound once it lapses) is
			// what eventually makes a retry pointless.
			if !interrupted || permanent {
				job.DownloadAttempts++
			}
			if job.DownloadAttempts < maxDownloadAttempts && !permanent {
				// Keep the job working so a later get_video retries the download.
				job.CompletedAt = nil
				if err := s.jobs.Put(job); err != nil {
					s.log.Warn("saving job failed", "job", job.ID, "err", err)
				}
				return
			}
			// Generated (and billed) but not retrievable: stop retrying.
			job.State, job.Billed, status = jobs.StateFailed, true, spend.StatusOK
			job.Error = "the video was generated (and billed) but could not be downloaded: " + job.Error
			break
		}
		job.Outputs, job.State, job.Error, job.Billed, status = assets, jobs.StateCompleted, "", true, spend.StatusOK
		if job.Backend == string(config.BackendGeminiAPI) {
			exp := job.CreatedAt.Add(geminiVideoRetention)
			job.RemoteExpiresAt = &exp
		}
	}
	if err := s.jobs.Put(job); err != nil {
		s.log.Warn("saving job failed", "job", job.ID, "err", err)
	}
	if job.LedgerID != "" {
		if e, ok := s.ledger.Get(job.LedgerID); ok {
			e.Status = status
			e.Outputs = assetPaths(job.Outputs)
			e.Error = job.Error
			if status != spend.StatusOK {
				e.CostUSD = 0 // failed and filtered generations are not billed
			}
			if e.Status == spend.StatusOK && job.State == jobs.StateFailed {
				e.Error = job.Error
			}
			if _, err := s.ledger.Record(e); err != nil {
				s.log.Warn("recording spend failed", "err", err)
			}
		}
	}
}

func (s *Service) jobView(job *jobs.Job) *VideoJob {
	end := s.now()
	if job.CompletedAt != nil {
		end = *job.CompletedAt
	}
	v := &VideoJob{
		JobID: job.ID, State: job.State, Model: job.Model, Files: job.Outputs, Error: job.Error,
		ElapsedSeconds: int(end.Sub(job.CreatedAt).Seconds()), RemoteExpiresAt: job.RemoteExpiresAt,
	}
	v.Cost = Cost{EstimatedUSD: job.EstimateUSD, USD: job.EstimateUSD, Basis: catalog.BasisUnits}
	if job.CostBasis != "" {
		v.Cost.USD, v.Cost.Basis = job.CostUSD, job.CostBasis
	}
	m, known := s.catalog.Get().Lookup(job.Model)
	switch job.State {
	case jobs.StateWorking:
		v.Cost.Pending = true
		v.Next = fmt.Sprintf("Call get_video with jobId %s (e.g. waitSeconds 45) until state is completed. Veo usually takes 1-3 minutes.", job.ID)
		if job.Worker != "" {
			v.Next = fmt.Sprintf("Call get_video with jobId %s (e.g. waitSeconds 45) until state is completed. Omni answers in one piece after 1-5 minutes (4K and extensions take longer).", job.ID)
			if _, here := s.omniActive.Load(job.ID); !here && job.Worker != s.ledger.Session() {
				v.Next += " Another server process is generating it; keep it running until the clip is done."
			}
		}
		if _, ok := s.fetching.Load(job.ID); ok {
			v.Next = fmt.Sprintf("The video is ready and being downloaded. Call get_video with jobId %s again in a few seconds.", job.ID)
		}
	case jobs.StateCompleted:
		switch {
		case known && m.Capabilities.VideoEdit:
			v.Next = "Review the video. To change it, call edit_video with this jobId; to continue the shot, call extend_video."
		case known && m.Capabilities.Extend:
			v.Next = "Review the video. To continue the shot, call extend_video with this jobId."
		}
	case jobs.StateFailed, jobs.StateFiltered:
		if job.Billed {
			// Delivery failed, billing did not: keep reporting what was spent.
			v.Next = fmt.Sprintf("Google generated this video and billed about $%.2f, but it could not be retrieved. Tell the user before generating again: a new call is billed again.", v.Cost.USD)
			break
		}
		v.Cost.USD = 0
		v.Next = "Adjust the prompt or inputs and call generate_video again."
	}
	return v
}

// sourceVideo returns the provider-side video for extension.
func (s *Service) sourceVideo(ctx context.Context, job *jobs.Job) (*genai.Video, error) {
	op, err := s.api.GetVideosOperation(ctx, job.Location, &genai.GenerateVideosOperation{Name: job.Operation})
	if err == nil && op.Response != nil && len(op.Response.GeneratedVideos) > 0 && op.Response.GeneratedVideos[0].Video != nil {
		v := op.Response.GeneratedVideos[0].Video
		if v.URI != "" {
			return &genai.Video{URI: v.URI, MIMEType: v.MIMEType}, nil
		}
		if len(v.VideoBytes) > 0 {
			return &genai.Video{VideoBytes: v.VideoBytes, MIMEType: firstNonEmpty(v.MIMEType, "video/mp4")}, nil
		}
	}
	// Vertex AI accepts inline bytes, so fall back to the saved file.
	if job.Backend == string(config.BackendVertex) && len(job.Outputs) > 0 {
		data, rerr := os.ReadFile(job.Outputs[0].Path)
		if rerr == nil {
			return &genai.Video{VideoBytes: data, MIMEType: "video/mp4"}, nil
		}
	}
	if err != nil {
		return nil, google.Classify(err, "loading the source video", job.Model, s.backend())
	}
	return nil, apperr.New(apperr.NotFound, "the source video is no longer available from the API", "Generate a new clip instead.")
}

// MP4Duration reads the duration from an MP4's movie header, or 0.
func MP4Duration(b []byte) float64 {
	i := bytes.Index(b[:min(len(b), 1<<20)], []byte("mvhd"))
	if i < 4 || i+32 > len(b) {
		// mvhd may sit at the end of the file (moov after mdat).
		i = bytes.LastIndex(b, []byte("mvhd"))
		if i < 4 || i+32 > len(b) {
			return 0
		}
	}
	p := b[i+4:]
	version := p[0]
	var timescale uint32
	var duration uint64
	if version == 1 {
		if len(p) < 32 {
			return 0
		}
		timescale = binary.BigEndian.Uint32(p[20:24])
		duration = binary.BigEndian.Uint64(p[24:32])
	} else {
		timescale = binary.BigEndian.Uint32(p[12:16])
		duration = uint64(binary.BigEndian.Uint32(p[16:20]))
	}
	if timescale == 0 {
		return 0
	}
	return float64(duration) / float64(timescale)
}
