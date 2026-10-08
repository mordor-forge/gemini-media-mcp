package media

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// Gemini Omni answers each interaction synchronously, after minutes rather
// than seconds. The server runs the call in the background and tracks it as
// a job, so the video tools behave the same as with Veo's operations.
const (
	// omniMinTimeout bounds one interaction; Google's own tooling allows up
	// to 20 minutes for 4K and long extensions.
	omniMinTimeout = 20 * time.Minute
	// omniHeartbeat is how often the running session refreshes the job.
	omniHeartbeat = 20 * time.Second
	// omniStale is when a job whose session stopped refreshing it is
	// reported as interrupted.
	omniStale = 2 * time.Minute
	// maxInlineVideoBytes caps a video sent inline for editing.
	maxInlineVideoBytes = 20 << 20
)

// EditVideoRequest is the input of edit_video.
type EditVideoRequest struct {
	JobID           string   `json:"jobId,omitempty" jsonschema:"A completed video job to edit. Omni clips are edited in conversation (Google keeps the context); other clips are sent as their saved file. Pass jobId or video."`
	Video           string   `json:"video,omitempty" jsonschema:"Or a video file to edit: file path, gemini-media:// URI or data: URI. At most 10 seconds and 20 MB."`
	Prompt          string   `json:"prompt" jsonschema:"The change, stated simply, ending with 'Keep everything else the same.' (e.g. 'Make the phone invisible. Keep everything else the same.')"`
	ReferenceImages []string `json:"referenceImages,omitempty" jsonschema:"Optional images to bring into the clip; refer to them as <IMAGE_REF_0>, <IMAGE_REF_1>... in the prompt."`
	Model           string   `json:"model,omitempty" jsonschema:"Model ID or alias; default omni (the only model that edits videos)."`
	Resolution      string   `json:"resolution,omitempty" jsonschema:"360p, 720p (default), 1080p or 4k (1080p and 4k are upscaled)."`
	WaitSeconds     int      `json:"waitSeconds,omitempty" jsonschema:"Block up to this many seconds for the result (default 0 = return a job handle immediately). Keep under your client's tool timeout."`
	OutputName      string   `json:"outputName,omitempty" jsonschema:"Optional base file name for the saved video. Defaults to the source name plus -edit."`
	ApprovedCostUSD float64  `json:"approvedCostUsd,omitempty" jsonschema:"Only needed above the confirmation threshold: the cost in USD the user approved."`
}

func (s *Service) omniTimeout() time.Duration {
	return max(s.cfg.RequestTimeout(), omniMinTimeout)
}

// generateOmni is generate_video for an Omni model.
func (s *Service) generateOmni(ctx context.Context, req VideoRequest, r *catalog.Resolved, first, last *store.Input, refs []*store.Input, warnings []string) (*VideoJob, error) {
	m := r.Model
	for _, ref := range refs {
		if err := requireImage(ref, "reference image"); err != nil {
			return nil, err
		}
	}
	images := len(refs)
	for _, in := range []*store.Input{first, last} {
		if in != nil {
			images++
		}
	}
	res := strings.ToLower(req.Resolution)
	if _, err := m.Validate(catalog.Params{
		"aspectRatio":     req.AspectRatio,
		"resolution":      res,
		"durationSeconds": itoa(req.DurationSeconds),
		"image":           req.Image,
		"lastFrame":       req.LastFrame,
		"referenceImages": itoa(images),
	}, s.backend()); err != nil {
		return nil, err
	}
	warnings = append(warnings, omniIgnored(req.Seed != nil, req.PersonGeneration != "", req.GenerateAudio != nil && !*req.GenerateAudio)...)

	duration := req.DurationSeconds
	if duration <= 0 {
		duration = max(m.Capabilities.DefaultDuration, 8)
	}
	var media []google.MediaPart
	var inputs []string
	for _, in := range append([]*store.Input{first, last}, refs...) {
		if in != nil {
			media = append(media, google.MediaPart{Kind: "image", Data: in.Data, MIMEType: in.MIMEType})
			inputs = append(inputs, in.Ref)
		}
	}
	ireq := &google.InteractionRequest{
		Model: r.APIID, Prompt: omniPrompt(req.Prompt, first != nil, last != nil, req.NegativePrompt), Media: media,
		AspectRatio: req.AspectRatio, Resolution: res, DurationSeconds: duration,
	}
	est := m.EstimateVideo(firstNonEmpty(res, "720p"), duration, 1, true, s.backend())
	params := map[string]any{
		"aspectRatio": req.AspectRatio, "resolution": res, "durationSeconds": duration,
		"negativePrompt": req.NegativePrompt, "inputs": inputs,
	}
	return s.startOmni(ctx, "generate_video", m, ireq, est, req.Prompt, params, req.OutputName, "", req.ApprovedCostUSD, req.WaitSeconds, warnings)
}

// extendOmni continues a finished Omni clip in the same interaction thread.
func (s *Service) extendOmni(ctx context.Context, req ExtendVideoRequest, parent *jobs.Job, m *catalog.Model) (*VideoJob, error) {
	if parent.Interaction == "" {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("job %s has no Omni interaction to continue", parent.ID), Hint: "Generate the clip again with model omni, or edit its saved file with edit_video."}
	}
	if limit := m.Capabilities.MaxTotalSeconds; limit > 0 && len(parent.Outputs) > 0 && parent.Outputs[0].DurationSeconds >= float64(limit)-0.5 {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("job %s is already %.0f s long; %s extends clips up to %d s", parent.ID, parent.Outputs[0].DurationSeconds, m.ID, limit), Hint: "Start a new clip from its last frame (generate_video with image) to continue the story."}
	}
	warnings := omniIgnored(req.Seed != nil, false, false)
	res, _ := parent.Params["resolution"].(string)
	seconds := max(m.Capabilities.ExtendSeconds, 1)
	ireq := &google.InteractionRequest{
		Model: m.APIID(s.backend()), Prompt: omniPrompt(req.Prompt, false, false, req.NegativePrompt),
		PreviousID: parent.Interaction, Resolution: res,
	}
	est := m.EstimateVideo(firstNonEmpty(res, "720p"), seconds, 1, true, s.backend())
	params := map[string]any{"extends": parent.ID, "resolution": res, "negativePrompt": req.NegativePrompt}
	return s.startOmni(ctx, "extend_video", m, ireq, est, req.Prompt, params, req.OutputName, parent.ID, req.ApprovedCostUSD, req.WaitSeconds, warnings)
}

// EditVideo changes an existing clip with an instruction (Omni).
func (s *Service) EditVideo(ctx context.Context, req EditVideoRequest) (*VideoJob, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, apperr.Invalidf("prompt is required")
	}
	if (strings.TrimSpace(req.JobID) == "") == (strings.TrimSpace(req.Video) == "") {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: "pass exactly one of jobId or video", Hint: "Use jobId for a clip this server generated, or video for a file."}
	}
	if err := s.videoAllowed(); err != nil {
		return nil, err
	}
	r, _, warnings, err := s.resolve(firstNonEmpty(req.Model, "omni"), catalog.Video)
	if err != nil {
		return nil, err
	}
	m := r.Model
	if !m.Capabilities.VideoEdit {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("%s cannot edit videos", m.ID), Hint: "Omit model (edits use omni), or call list_models to see video models."}
	}
	refs, err := s.loadInputs(req.ReferenceImages, "reference image")
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if err := requireImage(ref, "reference image"); err != nil {
			return nil, err
		}
	}
	res := strings.ToLower(req.Resolution)
	if _, err := m.Validate(catalog.Params{"resolution": res, "referenceImages": itoa(len(refs))}, s.backend()); err != nil {
		return nil, err
	}

	ireq := &google.InteractionRequest{Model: r.APIID, Prompt: strings.TrimSpace(req.Prompt)}
	var inputs []string
	for _, ref := range refs {
		ireq.Media = append(ireq.Media, google.MediaPart{Kind: "image", Data: ref.Data, MIMEType: ref.MIMEType})
		inputs = append(inputs, ref.Ref)
	}
	seconds := 0.0
	parentID, outName := "", req.OutputName
	if req.JobID != "" {
		parent, err := s.findJob(req.JobID)
		if err != nil {
			return nil, err
		}
		if parent.State != jobs.StateCompleted || len(parent.Outputs) == 0 {
			return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("job %s has no finished video to edit (%s)", parent.ID, parent.State), Hint: "Call get_video until it completes, then edit it."}
		}
		parentID = parent.ID
		seconds = parent.Outputs[0].DurationSeconds
		if res == "" {
			res, _ = parent.Params["resolution"].(string)
		}
		outName = firstNonEmpty(outName, editName(parent.Outputs[0].Name))
		if parent.Interaction != "" && parent.Model == m.ID {
			ireq.PreviousID = parent.Interaction
		} else {
			data, err := os.ReadFile(parent.Outputs[0].Path)
			if err != nil {
				return nil, &apperr.Error{Kind: apperr.NotFound, Message: fmt.Sprintf("the video of job %s is no longer on disk: %v", parent.ID, err), Hint: "Pass the video file with the video parameter instead."}
			}
			in := &store.Input{Ref: parent.Outputs[0].Path, Data: data, MIMEType: firstNonEmpty(parent.Outputs[0].MIMEType, "video/mp4")}
			if err := s.checkEditVideo(in, m); err != nil {
				return nil, err
			}
			ireq.Media = append(ireq.Media, google.MediaPart{Kind: "video", Data: in.Data, MIMEType: in.MIMEType})
			inputs = append(inputs, in.Ref)
		}
	} else {
		in, err := s.loadInput(req.Video, "video")
		if err != nil {
			return nil, err
		}
		if err := s.checkEditVideo(in, m); err != nil {
			return nil, err
		}
		seconds = MP4Duration(in.Data)
		outName = firstNonEmpty(outName, editName(req.Video))
		ireq.Media = append(ireq.Media, google.MediaPart{Kind: "video", Data: in.Data, MIMEType: in.MIMEType})
		inputs = append(inputs, in.Ref)
	}
	ireq.Resolution = res

	billed := int(math.Ceil(seconds))
	if billed <= 0 {
		billed = max(m.Capabilities.DefaultDuration, 8)
	}
	billed = min(max(billed, 3), max(m.Capabilities.MaxInputVideoSeconds, 10))
	est := m.EstimateVideo(firstNonEmpty(res, "720p"), billed, 1, true, s.backend())
	params := map[string]any{"edits": firstNonEmpty(parentID, req.Video), "resolution": res, "inputs": inputs}
	return s.startOmni(ctx, "edit_video", m, ireq, est, req.Prompt, params, outName, parentID, req.ApprovedCostUSD, req.WaitSeconds, warnings)
}

// checkEditVideo enforces the input limits for a video sent inline.
func (s *Service) checkEditVideo(in *store.Input, m *catalog.Model) error {
	if !strings.HasPrefix(in.MIMEType, "video/") {
		return apperr.Invalidf("video %s is %s, not a video", in.Ref, in.MIMEType)
	}
	trim := "Trim or shrink it first, e.g. ffmpeg -i in.mp4 -t 10 -vf scale=-2:720 -c:v libx264 -c:a aac out.mp4"
	if len(in.Data) > maxInlineVideoBytes {
		return &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("video %s is %d MB; edits accept at most %d MB", in.Ref, len(in.Data)>>20, maxInlineVideoBytes>>20), Hint: trim + "."}
	}
	if limit := m.Capabilities.MaxInputVideoSeconds; limit > 0 {
		if d := MP4Duration(in.Data); d > float64(limit)+0.5 {
			return &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("video %s is %.1f s; %s edits clips of at most %d s", in.Ref, d, m.ID, limit), Hint: trim + "."}
		}
	}
	return nil
}

// omniPrompt binds frames to their roles with Omni's tags and folds a
// negative prompt into the text (Omni has no negative_prompt field).
func omniPrompt(prompt string, first, last bool, negative string) string {
	p := strings.TrimSpace(prompt)
	if first && p == "" {
		p = "Bring this image to life."
	}
	if !strings.Contains(p, "[# Sources") && !strings.Contains(p, "<FIRST_FRAME>") {
		switch {
		case first && last:
			p = "<FIRST_FRAME> <LAST_FRAME> " + p
		case first:
			p = "<FIRST_FRAME> " + p
		}
	}
	if n := strings.TrimSpace(negative); n != "" {
		p += " Avoid: " + strings.TrimSuffix(n, ".") + "."
	}
	return p
}

func omniIgnored(seed, personGeneration, silent bool) []string {
	var w []string
	if seed {
		w = append(w, "seed is ignored by Omni")
	}
	if personGeneration {
		w = append(w, "personGeneration is ignored by Omni")
	}
	if silent {
		w = append(w, "Omni always generates audio; describe the soundtrack you want in the prompt (for example 'ambient sound only, no music')")
	}
	return w
}

// startOmni reserves budget, records the job and runs the interaction in the
// background. Nothing is sent to Google before the job is saved, so a job
// that cannot be tracked is never billed.
func (s *Service) startOmni(ctx context.Context, tool string, m *catalog.Model, ireq *google.InteractionRequest, est catalog.Estimate, prompt string, params map[string]any, outName, parentID string, approved float64, wait int, warnings []string) (*VideoJob, error) {
	res, err := s.reserve(&est, approved)
	if err != nil {
		return nil, err
	}
	entry := spend.Entry{
		Tool: tool, Model: m.ID, MediaType: catalog.Video, Status: spend.StatusPending,
		Units:        map[string]any{"resolution": firstNonEmpty(ireq.Resolution, "720p"), "durationSeconds": params["durationSeconds"]},
		CostUSD:      est.USD,
		Basis:        est.Basis,
		EstimatedUSD: est.USD,
		PriceAsOf:    est.PriceAsOf,
		Backend:      s.backend(),
	}
	settled, err := res.Settle(entry)
	if err != nil {
		s.log.Warn("recording spend failed", "err", err)
	}
	job := &jobs.Job{
		ID: jobs.NewID(), Tool: tool, Model: m.ID, Backend: s.backend(), Prompt: prompt, Params: params,
		OutputName: outName, LedgerID: settled.ID, EstimateUSD: est.USD, State: jobs.StateWorking,
		ParentID: parentID, Worker: s.ledger.Session(),
	}
	if err := s.jobs.Put(job); err != nil {
		s.recordOutcome(job.LedgerID, spend.StatusFailed, nil, "", nil, "could not save the job")
		return nil, fmt.Errorf("saving the video job (nothing was sent to Google): %w", err)
	}
	done := make(chan struct{})
	s.omniActive.Store(job.ID, done)
	worker := *job
	go s.runOmni(context.WithoutCancel(ctx), &worker, ireq, m, done)

	var out *VideoJob
	if wait <= 0 {
		out = s.jobView(job)
	} else {
		out = s.pollJob(ctx, job, nil, wait)
	}
	out.Warnings = append(warnings, out.Warnings...)
	return out, nil
}

// runOmni performs the interaction, downloads the clip and settles the job
// and its spend. It works on its own copy of the job.
func (s *Service) runOmni(ctx context.Context, job *jobs.Job, ireq *google.InteractionRequest, m *catalog.Model, done chan struct{}) {
	defer close(done)
	defer s.omniActive.Delete(job.ID)
	ctx, cancel := context.WithTimeout(ctx, s.omniTimeout())
	defer cancel()

	// Refresh the record while waiting, so other processes can tell a
	// running job from one whose server stopped.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(omniHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := s.jobs.Put(job); err != nil {
					s.log.Warn("refreshing video job failed", "job", job.ID, "err", err)
				}
			}
		}
	}()

	result, err := s.api.CreateInteraction(ctx, ireq, s.omniTimeout())
	var data []byte
	var derr error
	if err == nil && result.Video != nil {
		data, derr = s.downloadOmni(ctx, result)
	}
	close(stop)
	wg.Wait()

	now := s.now().UTC()
	job.CompletedAt = &now
	var usage *google.Usage
	var actual *catalog.Estimate
	if result != nil {
		usage = &result.Usage
		if a, ok := m.CostFromUsage(tokenUsage(result.Usage), s.backend(), ""); ok {
			actual = &a
		}
		if result.ID != "" {
			job.Operation = "interactions/" + result.ID
		}
	}
	status := spend.StatusOK
	switch {
	case err != nil:
		cerr := google.Classify(err, "generating video", m.ID, s.backend())
		job.State, job.Error, status = jobs.StateFailed, cerr.Error(), spend.StatusFailed
		switch apperr.KindOf(cerr) {
		case apperr.Safety:
			job.State, status = jobs.StateFiltered, spend.StatusFiltered
		case apperr.Timeout, apperr.Canceled:
			// The request may still complete (and be billed) at Google.
			job.Billed, status = true, spend.StatusOK
			job.Error = fmt.Sprintf("no answer within %s: Google may still produce (and bill) the clip, but this server stopped waiting", s.omniTimeout())
		}
	case result.Video == nil:
		job.State, status = jobs.StateFailed, spend.StatusFailed
		job.Error = firstNonEmpty(result.Error, fmt.Sprintf("the interaction ended without a video (status %s)", result.Status))
		if isSafetyText(job.Error) {
			job.State, status = jobs.StateFiltered, spend.StatusFiltered
		}
		if hasVideo(ireq.Media) && result.Usage.OutputTokens == 0 {
			job.Error += ". Editing or extending uploaded videos is not available in the EEA, Switzerland, the UK and some US states"
		}
		if actual != nil && actual.USD > 0 && status == spend.StatusFailed {
			status = spend.StatusOK // input tokens were still billed
		}
	case derr != nil:
		cerr := google.Classify(derr, "downloading video", m.ID, s.backend())
		job.State, job.Billed = jobs.StateFailed, true
		job.Error = "the video was generated (and billed) but could not be downloaded: " + cerr.Error()
	default:
		mime := firstNonEmpty(result.Video.MIMEType, "video/mp4")
		asset, serr := s.store.Save("video", job.OutputName, store.ExtFromMIME(mime), data, mime, &store.Provenance{
			Tool: job.Tool, Model: job.Model, Prompt: job.Prompt, Params: job.Params, OperationID: job.Operation,
			ModelText: result.Text, CostUSD: costOr(actual, job.EstimateUSD),
		})
		if serr != nil {
			job.State, job.Billed = jobs.StateFailed, true
			job.Error = "the video was generated (and billed) but could not be saved: " + serr.Error()
			break
		}
		asset.DurationSeconds = MP4Duration(data)
		job.Outputs, job.State, job.Billed = []store.Asset{*asset}, jobs.StateCompleted, true
		job.Interaction = result.ID
	}
	if actual != nil && status == spend.StatusOK {
		job.CostUSD, job.CostBasis = actual.USD, actual.Basis
	}
	// Settle the spend before publishing the finished job: a caller that
	// sees it finished may read usage right away.
	s.recordOutcome(job.LedgerID, status, actual, job.Error, usage, "", assetPaths(job.Outputs)...)
	if err := s.jobs.Put(job); err != nil {
		s.log.Error("saving video job failed", "job", job.ID, "err", err)
	}
}

// downloadOmni fetches the clip, retrying transient failures: the video is
// already billed, so giving up early would waste it.
func (s *Service) downloadOmni(ctx context.Context, result *google.InteractionResult) ([]byte, error) {
	var err error
	for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
		var data []byte
		if data, err = s.api.DownloadVideo(ctx, "", result.Video); err == nil {
			return data, nil
		}
		switch apperr.KindOf(google.Classify(err, "", "", s.backend())) {
		case apperr.NotFound, apperr.Permission, apperr.Invalid:
			return nil, err
		}
		if attempt < maxDownloadAttempts {
			if serr := s.sleep(ctx, time.Duration(attempt)*5*time.Second); serr != nil {
				return nil, err
			}
		}
	}
	return nil, err
}

// recordOutcome finalizes a pending ledger entry. Unbilled outcomes cost $0;
// billed ones keep the estimate unless usage priced them. An entry already
// finalized is left alone: a worker that settled it but stopped before
// saving its job must not lose its cost and output paths to the recovery.
func (s *Service) recordOutcome(ledgerID, status string, actual *catalog.Estimate, errText string, usage *google.Usage, fallbackErr string, outputs ...string) {
	if ledgerID == "" {
		return
	}
	e, ok := s.ledger.Get(ledgerID)
	if !ok || e.Status != spend.StatusPending {
		return
	}
	e.Status = status
	e.Error = truncate(firstNonEmpty(errText, fallbackErr), 300)
	e.Outputs = outputs
	if usage != nil && (usage.PromptTokens > 0 || usage.OutputTokens > 0) {
		e.Usage = usage
	}
	switch {
	case status != spend.StatusOK:
		e.CostUSD = 0
	case actual != nil:
		e.CostUSD, e.Basis = actual.USD, actual.Basis
	}
	if _, err := s.ledger.Record(e); err != nil {
		s.log.Warn("recording spend failed", "err", err)
	}
}

// checkWorkerJob reports an Omni job tracked by a session's background
// worker. done is true when the caller should stop waiting.
func (s *Service) checkWorkerJob(job *jobs.Job) (jobCheck, bool) {
	if _, ok := s.omniActive.Load(job.ID); ok {
		return jobCheck{nil, job}, false
	}
	if s.now().Sub(job.UpdatedAt) < omniStale {
		return jobCheck{nil, job}, false // running in another server process
	}
	// The session running it stopped without finishing: Google may still
	// have produced (and billed) the clip, so the estimate stays spent.
	now := s.now().UTC()
	job.CompletedAt = &now
	job.State, job.Billed = jobs.StateFailed, true
	job.Error = "the server process generating this clip stopped before it finished; Google may still have produced (and billed) it, but it cannot be retrieved"
	if err := s.jobs.Put(job); err != nil {
		s.log.Warn("saving job failed", "job", job.ID, "err", err)
	}
	s.recordOutcome(job.LedgerID, spend.StatusOK, nil, job.Error, nil, "")
	return jobCheck{s.jobView(job), job}, true
}

func hasVideo(media []google.MediaPart) bool {
	for _, p := range media {
		if p.Kind == "video" {
			return true
		}
	}
	return false
}

func isSafetyText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "safety") || strings.Contains(l, "blocked") || strings.Contains(l, "prohibited") || strings.Contains(l, "responsible ai")
}

func costOr(e *catalog.Estimate, fallback float64) float64 {
	if e != nil {
		return e.USD
	}
	return fallback
}
