package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/gemini-media-mcp/internal/media"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// Tool annotation presets.
var (
	// generative: creates new files and costs money; never overwrites.
	generative = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)}
	// polling: safe to repeat; may download a finished result.
	polling = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)}
	// local: read-only, no external calls.
	local = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)}
	// processing: writes new files from local image processing; free, no external calls.
	processing = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
)

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "generate_image",
		Title: "Generate image",
		Description: "Create images from a text prompt, optionally guided by up to 14 reference images (subjects, products, style). " +
			"Default model nb2 (Nano Banana 2.1, ~$0.04 per 1K image); pro for the densest scenes and final renders (~$0.15); nb2-lite for bulk drafts. " +
			"Returns saved files with URIs you can pass to edit_image, generate_video (image) or other tools.",
		Annotations: withTitle(generative, "Generate image"),
	}, s.handleGenerateImage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "edit_image",
		Title: "Edit image",
		Description: "Change an existing image with an instruction (add/remove/replace elements, restyle, relight, change aspect ratio, fix text) while keeping everything else. " +
			"Accepts extra reference images to insert or match. Say what must stay unchanged. Saves a new file; the original is kept.",
		Annotations: withTitle(generative, "Edit image"),
	}, s.handleEditImage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "tile_image",
		Title: "Tile image for upscaling",
		Description: "Upscale step 1: cut an image into crops shaped to the edit model's aspect ratios. longEdge sets the target (1K-16K, default 8K); with grid and imageSize omitted it picks the cheapest plan that covers it (one tile up to about 4K, a 2x2-4x4 grid beyond). regions re-render faces or text in a later pass. Free and local; reports the edit cost. " +
			"Step 2: edit_image each crop as the result says. Step 3: stitch_tiles. Detail is invented, not recovered.",
		Annotations: withTitle(processing, "Tile image for upscaling"),
	}, s.handleTileImage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "stitch_tiles",
		Title: "Stitch upscaled tiles",
		Description: "Upscale step 3: align each edited tile from tile_image with the image, match its broad color, blend the overlaps and save one large PNG (default 8192 px long edge). Free and local. " +
			"Reports tiles it rejected (their area keeps the prior pixels) and returns a preview plus 100% crops of the seams to inspect.",
		Annotations: withTitle(processing, "Stitch upscaled tiles"),
	}, s.handleStitchTiles)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "generate_video",
		Title: "Generate video",
		Description: "Start a video clip with native audio from a text prompt, optionally from a first frame, first+last frames, or reference images. " +
			"Default omni (Gemini Omni Flash: 3-10s, 360p drafts at ~$0.03/s to 4K, up to 10 references, ~$0.10/s at 720p). On Vertex AI the default is Veo lite ($0.05/s at 720p); fast/standard add Veo 4K and ingredients. Veo is leaving the Gemini API (list_models has the date). " +
			"Asynchronous: returns a jobId; then call get_video.",
		Annotations: withTitle(generative, "Generate video"),
	}, s.handleGenerateVideo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_video",
		Title:       "Get video job",
		Description: "Check a video job and wait for it (default up to 45s). When the job completes, the video is downloaded and returned; calling again is safe and returns the same files.",
		Annotations: withTitle(polling, "Get video job"),
	}, s.handleGetVideo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "extend_video",
		Title: "Extend video",
		Description: "Continue a completed clip from its end: omni adds up to 10 s (40 s total, keeping characters and audio coherent); Veo fast/standard add about 7 s (720p, up to 148 s, within 2 days). " +
			"Asynchronous like generate_video.",
		Annotations: withTitle(generative, "Extend video"),
	}, s.handleExtendVideo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "edit_video",
		Title: "Edit video",
		Description: "Change an existing clip with an instruction (add, remove or replace objects, restyle, relight, change the background) with Gemini Omni Flash, keeping everything else. " +
			"Pass the jobId of a finished clip (Omni clips are edited in conversation) or a video file of at most 10 s. Asynchronous like generate_video; about $0.10 per second of video at 720p.",
		Annotations: withTitle(generative, "Edit video"),
	}, s.handleEditVideo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "generate_speech",
		Title: "Generate speech",
		Description: "Turn text into natural speech (WAV) with one voice, or a dialogue between two speakers. Control delivery with style. " +
			"About $0.01-0.02 per minute of audio. For podcasts, voiceovers, narration and dialogue.",
		Annotations: withTitle(generative, "Generate speech"),
	}, s.handleGenerateSpeech)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "generate_music",
		Title: "Generate music",
		Description: "Compose music with Lyria: 30-second clips (clip, $0.04) or full songs of a few minutes with vocals and lyrics (full, $0.08). " +
			"Control genre, mood, instruments, tempo and structure in the prompt; returns lyrics/structure text when available.",
		Annotations: withTitle(generative, "Generate music"),
	}, s.handleGenerateMusic)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "list_models",
		Title: "List models",
		Description: "List current models per media type with aliases, status (GA/preview/deprecated), prices and a short guide to when to use each. " +
			"detail: true adds supported aspect ratios, sizes, durations and voices; live: true checks what your API key can actually call.",
		Annotations: withTitle(&mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(true)}, "List models"),
	}, s.handleListModels)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "estimate_cost",
		Title:       "Estimate cost",
		Description: "Estimate the cost of an image, video, speech or music request before running it, optionally comparing all models; also reports remaining budget and whether approval is needed.",
		Annotations: withTitle(local, "Estimate cost"),
	}, s.handleEstimateCost)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_usage",
		Title:       "Get usage and spend",
		Description: "Report estimated spend for this session, today, this month or all time, broken down by model and tool, with budget remaining, recent calls and running video jobs.",
		Annotations: withTitle(local, "Get usage and spend"),
	}, s.handleGetUsage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_config",
		Title:       "Get configuration",
		Description: "Show the active backend and how it was chosen, output directory, default models, budgets, input-file policy and configuration warnings. Use it to troubleshoot credentials or missing files.",
		Annotations: withTitle(local, "Get configuration"),
	}, s.handleGetConfig)
}

func withTitle(a *mcp.ToolAnnotations, title string) *mcp.ToolAnnotations {
	c := *a
	c.Title = title
	return &c
}

// ---- handlers ----

func (s *Server) handleGenerateImage(ctx context.Context, req *mcp.CallToolRequest, in media.ImageRequest) (*mcp.CallToolResult, *media.ImageResult, error) {
	res, err := s.svc.GenerateImage(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return imageToolResult("Generated", res), res, nil
}

func (s *Server) handleEditImage(ctx context.Context, req *mcp.CallToolRequest, in media.EditImageRequest) (*mcp.CallToolResult, *media.ImageResult, error) {
	res, err := s.svc.EditImage(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return imageToolResult("Edited", res), res, nil
}

func (s *Server) handleTileImage(ctx context.Context, req *mcp.CallToolRequest, in media.TileImageRequest) (*mcp.CallToolResult, *media.TileImageResult, error) {
	res, err := s.svc.TileImage(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	head := fmt.Sprintf("Cut the %dx%d image into %d tiles (pass %d, %s); stitch_tiles will produce %dx%d.",
		res.Image.Width, res.Image.Height, len(res.Tiles), res.Pass, res.Mode, res.Output.Width, res.Output.Height)
	if res.NativeLongEdge > 0 {
		head += fmt.Sprintf(" At %s the tiles carry about %d px of model detail along the long edge.", res.ImageSize, res.NativeLongEdge)
	}
	lines := []string{head}
	if res.PlanNote != "" {
		lines = append(lines, "Plan: "+res.PlanNote+".")
	}
	if res.Reference != nil {
		lines = append(lines, "Reference for every edit: "+res.Reference.URI)
	} else {
		lines = append(lines, "Refinement pass: edit each crop on its own, without referenceImages.")
	}
	for _, t := range res.Tiles {
		lines = append(lines, fmt.Sprintf("Tile %d %s (aspectRatio %s): %s", t.Tile, t.Label, t.AspectRatio, t.Crop.URI))
	}
	lines = append(lines,
		fmt.Sprintf("Editing every tile once with %s at %s: ~$%.2f (%s).", res.Model, res.ImageSize, res.Cost.USD, res.Cost.Breakdown),
		"Job: "+res.Job,
		"Next: "+res.Next)
	lines = append(lines, warningLines(res.Warnings)...)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: joinLines(lines)}}}, res, nil
}

func (s *Server) handleStitchTiles(ctx context.Context, req *mcp.CallToolRequest, in media.StitchTilesRequest) (*mcp.CallToolResult, *media.StitchResult, error) {
	res, err := s.svc.StitchTiles(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	counts := map[string]int{}
	for _, t := range res.Tiles {
		counts[t.Status]++
	}
	lines := []string{
		"Stitched image: " + fileLine(res.File),
		fmt.Sprintf("Pass %d: %d placed, %d rejected, %d unedited.", res.Pass, counts["placed"], counts["rejected"], counts["unedited"]),
	}
	if res.NativeLongEdge > 0 {
		lines = append(lines, fmt.Sprintf("About %d px of model-rendered detail along the long edge.", res.NativeLongEdge))
	}
	for _, t := range res.Tiles {
		switch t.Status {
		case "placed":
			line := fmt.Sprintf("Tile %d %s: placed (match %.2f, shift %.1f,%.1f px, scale %.3f x %.3f)", t.Tile, t.Label, t.Match, t.ShiftX, t.ShiftY, t.ScaleX, t.ScaleY)
			if t.Note != "" {
				line += "; " + t.Note
			}
			lines = append(lines, line)
		case "rejected":
			lines = append(lines, fmt.Sprintf("Tile %d %s: rejected, prior pixels kept: %s", t.Tile, t.Label, t.Note))
		}
	}
	content := []mcp.Content{}
	if len(res.Previews) > 0 {
		lines = append(lines, "Previews: the whole image first, then "+strings.Join(res.Details, "; ")+".")
	}
	lines = append(lines, "Next: "+res.Next)
	lines = append(lines, warningLines(res.Warnings)...)
	content = append(content, &mcp.TextContent{Text: joinLines(lines)}, resourceLink(res.File))
	for _, p := range res.Previews {
		content = append(content, &mcp.ImageContent{Data: p, MIMEType: "image/jpeg"})
	}
	return &mcp.CallToolResult{Content: content}, res, nil
}

func (s *Server) handleGenerateVideo(ctx context.Context, req *mcp.CallToolRequest, in media.VideoRequest) (*mcp.CallToolResult, *media.VideoJob, error) {
	res, err := s.svc.GenerateVideo(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return videoToolResult(res), res, nil
}

func (s *Server) handleGetVideo(ctx context.Context, req *mcp.CallToolRequest, in media.GetVideoRequest) (*mcp.CallToolResult, *media.VideoJob, error) {
	res, err := s.svc.GetVideo(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return videoToolResult(res), res, nil
}

func (s *Server) handleExtendVideo(ctx context.Context, req *mcp.CallToolRequest, in media.ExtendVideoRequest) (*mcp.CallToolResult, *media.VideoJob, error) {
	res, err := s.svc.ExtendVideo(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return videoToolResult(res), res, nil
}

func (s *Server) handleEditVideo(ctx context.Context, req *mcp.CallToolRequest, in media.EditVideoRequest) (*mcp.CallToolResult, *media.VideoJob, error) {
	res, err := s.svc.EditVideo(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return videoToolResult(res), res, nil
}

func (s *Server) handleGenerateSpeech(ctx context.Context, req *mcp.CallToolRequest, in media.SpeechRequest) (*mcp.CallToolResult, *media.SpeechResult, error) {
	res, err := s.svc.GenerateSpeech(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	var voices []string
	for sp, v := range res.Voices {
		voices = append(voices, fmt.Sprintf("%s=%s", sp, v))
	}
	lines := []string{
		fmt.Sprintf("Generated speech: %s", fileLine(res.File)),
		fmt.Sprintf("Model %s, voices %s, %.1fs.", res.Model, strings.Join(voices, ", "), res.DurationSeconds),
		costLine(res.Cost),
	}
	lines = append(lines, warningLines(res.Warnings)...)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: joinLines(lines)},
		resourceLink(res.File),
	}}, res, nil
}

func (s *Server) handleGenerateMusic(ctx context.Context, req *mcp.CallToolRequest, in media.MusicRequest) (*mcp.CallToolResult, *media.MusicResult, error) {
	res, err := s.svc.GenerateMusic(withProgress(ctx, req), in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	lines := []string{
		fmt.Sprintf("Generated music: %s", fileLine(res.File)),
		fmt.Sprintf("Model %s.", res.Model),
		costLine(res.Cost),
	}
	if res.Lyrics != "" {
		lines = append(lines, "", "Lyrics/structure:", res.Lyrics)
	}
	lines = append(lines, warningLines(res.Warnings)...)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: joinLines(lines)},
		resourceLink(res.File),
	}}, res, nil
}

func (s *Server) handleListModels(ctx context.Context, _ *mcp.CallToolRequest, in media.ListModelsRequest) (*mcp.CallToolResult, *media.ListModelsResult, error) {
	res, err := s.svc.ListModels(ctx, in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Models (catalog %s, backend %s):\n", res.CatalogVersion, res.Backend)
	current := ""
	for _, m := range res.Models {
		if m.MediaType != current {
			current = m.MediaType
			fmt.Fprintf(&b, "\n## %s\n", current)
		}
		name := m.ID
		if len(m.Aliases) > 0 {
			name += " (" + strings.Join(m.Aliases, ", ") + ")"
		}
		flags := []string{m.Status}
		if m.Default {
			flags = append(flags, "default")
		}
		if !m.OnBackend {
			flags = append(flags, "not on "+res.Backend)
		}
		if m.Available != nil && !*m.Available {
			flags = append(flags, "NOT AVAILABLE to this key")
		}
		if m.Shutdown != "" {
			flags = append(flags, "shutdown "+m.Shutdown)
		}
		fmt.Fprintf(&b, "- %s [%s] %s. %s\n", name, strings.Join(flags, ", "), m.Price, m.Summary)
	}
	if len(res.Uncatalogued) > 0 {
		fmt.Fprintf(&b, "\nOffered by the API but not in the catalog (usable by raw ID, unvalidated): %s\n", strings.Join(res.Uncatalogued, ", "))
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(&b, "\nWarning: %s", w)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(b.String())}}}, res, nil
}

func (s *Server) handleEstimateCost(ctx context.Context, _ *mcp.CallToolRequest, in media.EstimateRequest) (*mcp.CallToolResult, *media.EstimateResult, error) {
	res, err := s.svc.EstimateCost(ctx, in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	lines := []string{fmt.Sprintf("%s: ~$%.4f (%s)", res.Model, res.Estimate.USD, res.Estimate.Breakdown)}
	for _, a := range res.Alternatives {
		note := ""
		if !a.Supports {
			note = " (does not support these parameters)"
		}
		lines = append(lines, fmt.Sprintf("  %s: ~$%.4f%s", a.Model, a.USD, note))
	}
	if res.NeedsConfirm {
		lines = append(lines, "Above the confirmation threshold: get the user's approval and pass approvedCostUsd.")
	}
	for k, v := range res.Budget {
		lines = append(lines, fmt.Sprintf("Budget remaining (%s): $%.2f", k, v))
	}
	lines = append(lines, warningLines(res.Warnings)...)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: joinLines(lines)}}}, res, nil
}

func (s *Server) handleGetUsage(ctx context.Context, _ *mcp.CallToolRequest, in media.UsageRequest) (*mcp.CallToolResult, *media.UsageResult, error) {
	res, err := s.svc.Usage(ctx, in)
	if err != nil {
		return nil, nil, toolError(err)
	}
	t := res.Summary.Totals
	lines := []string{
		fmt.Sprintf("Estimated spend: session $%.2f, today $%.2f, month $%.2f, all time $%.2f (pending video jobs $%.2f).", t.Session, t.Today, t.Month, t.All, t.Pending),
		fmt.Sprintf("%s: %d billed calls.", res.Period, res.Summary.Calls),
	}
	for model, usd := range res.Summary.ByModel {
		lines = append(lines, fmt.Sprintf("  %s: $%.4f", model, usd))
	}
	for k, v := range res.Summary.Remaining {
		lines = append(lines, fmt.Sprintf("Budget remaining (%s): $%.2f", k, v))
	}
	if len(res.ActiveJobs) > 0 {
		lines = append(lines, fmt.Sprintf("%d video job(s) still running.", len(res.ActiveJobs)))
	}
	lines = append(lines, res.Note)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: joinLines(lines)}}}, res, nil
}

func (s *Server) handleGetConfig(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, *media.InfoResult, error) {
	info := s.svc.Info(s.transport)
	lines := []string{
		fmt.Sprintf("gemini-media-mcp %s", info.Version),
		fmt.Sprintf("Backend: %s (%s) - %s", info.Backend, info.AuthMode, info.BackendReason),
		fmt.Sprintf("Output directory: %s", info.OutputDir),
		fmt.Sprintf("Defaults: image=%v, video=%v, speech=%v (voice %v), music=%v", info.Defaults["image"], info.Defaults["video"], info.Defaults["speech"], info.Defaults["voice"], info.Defaults["music"]),
		fmt.Sprintf("Input files: %s", info.InputPolicy),
		fmt.Sprintf("Catalog: %s", info.CatalogVersion),
	}
	if info.Project != "" {
		lines = append(lines, fmt.Sprintf("Project: %s, location: %s", info.Project, info.Location))
	}
	if info.HTTPAddr != "" {
		lines = append(lines, "Listening on: "+info.HTTPAddr)
	}
	lines = append(lines, warningLines(info.Warnings)...)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: joinLines(lines)}}}, info, nil
}

// ---- formatting ----

func imageToolResult(verb string, res *media.ImageResult) *mcp.CallToolResult {
	var lines []string
	for _, f := range res.Files {
		lines = append(lines, fmt.Sprintf("%s image: %s", verb, fileLine(f)))
	}
	lines = append(lines, fmt.Sprintf("Model %s.", res.Model), costLine(res.Cost))
	if res.Text != "" {
		lines = append(lines, "Model note: "+res.Text)
	}
	if g := res.Grounding; g != nil {
		if len(g.Queries) > 0 {
			lines = append(lines, "Searched: "+strings.Join(g.Queries, "; "))
		}
		for _, src := range g.Sources {
			lines = append(lines, fmt.Sprintf("Source: %s %s", src.Title, src.URI))
		}
	}
	lines = append(lines, warningLines(res.Warnings)...)
	content := []mcp.Content{&mcp.TextContent{Text: joinLines(lines)}}
	for i, f := range res.Files {
		content = append(content, resourceLink(f))
		if i < len(res.Previews) && res.Previews[i] != nil {
			content = append(content, &mcp.ImageContent{Data: res.Previews[i], MIMEType: "image/jpeg"})
		}
	}
	return &mcp.CallToolResult{Content: content}
}

func videoToolResult(v *media.VideoJob) *mcp.CallToolResult {
	lines := []string{fmt.Sprintf("Video job %s: %s (model %s, %ds elapsed).", v.JobID, v.State, v.Model, v.ElapsedSeconds)}
	for _, f := range v.Files {
		lines = append(lines, "Saved: "+fileLine(f))
	}
	if v.Error != "" {
		lines = append(lines, "Error: "+v.Error)
	}
	lines = append(lines, costLine(v.Cost))
	if v.Next != "" {
		lines = append(lines, "Next: "+v.Next)
	}
	lines = append(lines, warningLines(v.Warnings)...)
	content := []mcp.Content{&mcp.TextContent{Text: joinLines(lines)}}
	for _, f := range v.Files {
		content = append(content, resourceLink(f))
	}
	return &mcp.CallToolResult{Content: content}
}

func fileLine(a store.Asset) string {
	s := a.Path
	var meta []string
	if a.Width > 0 {
		meta = append(meta, fmt.Sprintf("%dx%d", a.Width, a.Height))
	}
	if a.DurationSeconds > 0 {
		meta = append(meta, fmt.Sprintf("%.1fs", a.DurationSeconds))
	}
	meta = append(meta, humanBytes(a.Bytes))
	return fmt.Sprintf("%s (%s; %s)", s, strings.Join(meta, ", "), a.URI)
}

func costLine(c media.Cost) string {
	switch {
	case c.Pending:
		return fmt.Sprintf("Cost: ~$%.2f reserved (billed when the job succeeds).", c.EstimatedUSD)
	case c.Basis == "unpriced":
		return "Cost: unknown (model not in the price table)."
	}
	return fmt.Sprintf("Cost: ~$%.4f (%s).", c.USD, c.Basis)
}

func warningLines(ws []string) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, "Warning: "+w)
	}
	return out
}

func resourceLink(a store.Asset) *mcp.ResourceLink {
	size := a.Bytes
	return &mcp.ResourceLink{URI: a.URI, Name: a.Name, MIMEType: a.MIMEType, Size: &size}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
