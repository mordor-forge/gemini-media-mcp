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

// GenerateVideo creates a video from a text prompt using the Gemini Veo API.
// Video generation is asynchronous — this returns an operation handle that
// must be polled via Status and retrieved via Download.
func (p *GeminiProvider) GenerateVideo(ctx context.Context, req provider.VideoRequest) (*provider.VideoOperation, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	model := p.resolveModel(req.Model, "lite")

	if model == p.modelMap["omni"] {
		return p.generateOmniVideo(ctx, req)
	}

	if err := p.validateVideoGenerationInput(model, req.AspectRatio, req.Resolution, req.Duration); err != nil {
		return nil, err
	}

	config := buildVideoConfig(req.AspectRatio, req.Resolution, req.Duration)

	op, err := p.client.Models.GenerateVideosFromSource(ctx, model, &genai.GenerateVideosSource{
		Prompt: req.Prompt,
	}, config)
	if err != nil {
		return nil, fmt.Errorf("generating video: %w", err)
	}

	return &provider.VideoOperation{
		OperationID: op.Name,
		Model:       model,
	}, nil
}

// AnimateImage creates a video using an image as the first frame.
// The image is read from disk and sent alongside the text prompt.
func (p *GeminiProvider) AnimateImage(ctx context.Context, req provider.AnimateRequest) (*provider.VideoOperation, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if req.ImagePath == "" {
		return nil, fmt.Errorf("imagePath is required")
	}

	imgBytes, err := os.ReadFile(req.ImagePath)
	if err != nil {
		return nil, fmt.Errorf("reading source image %s: %w", req.ImagePath, err)
	}

	model := p.resolveModel(req.Model, "lite")
	if err := p.validateVideoGenerationInput(model, req.AspectRatio, "", req.Duration); err != nil {
		return nil, err
	}

	config := buildVideoConfig(req.AspectRatio, "", req.Duration)

	op, err := p.client.Models.GenerateVideosFromSource(ctx, model, &genai.GenerateVideosSource{
		Prompt: req.Prompt,
		Image: &genai.Image{
			ImageBytes: imgBytes,
			MIMEType:   mimeFromPath(req.ImagePath),
		},
	}, config)
	if err != nil {
		return nil, fmt.Errorf("animating image: %w", err)
	}

	return &provider.VideoOperation{
		OperationID: op.Name,
		Model:       model,
	}, nil
}

// Extend chains a new video segment onto a previously generated video.
// It retrieves the completed operation to get its video, then uses it as
// the source for a new generation. Lite model does not support extension.
func (p *GeminiProvider) Extend(ctx context.Context, req provider.ExtendRequest) (*provider.VideoOperation, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if req.OperationID == "" {
		return nil, fmt.Errorf("operationId is required")
	}

	// Retrieve the previous operation to get its video output.
	prevOp, err := p.client.Operations.GetVideosOperation(ctx, &genai.GenerateVideosOperation{
		Name: req.OperationID,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("retrieving previous operation %s: %w", req.OperationID, err)
	}
	if !prevOp.Done {
		return nil, fmt.Errorf("previous operation %s is not yet complete", req.OperationID)
	}
	if prevOp.Error != nil {
		return nil, fmt.Errorf("previous operation %s failed: %v", req.OperationID, prevOp.Error)
	}
	if prevOp.Response == nil || len(prevOp.Response.GeneratedVideos) == 0 || prevOp.Response.GeneratedVideos[0].Video == nil {
		return nil, fmt.Errorf("previous operation %s has no video output", req.OperationID)
	}

	prevVideo := prevOp.Response.GeneratedVideos[0].Video

	model, err := p.resolveExtensionModel(prevOp.Name, req.Model)
	if err != nil {
		return nil, err
	}

	op, err := p.client.Models.GenerateVideosFromSource(ctx, model, &genai.GenerateVideosSource{
		Prompt: req.Prompt,
		Video:  prevVideo,
	}, &genai.GenerateVideosConfig{})
	if err != nil {
		return nil, fmt.Errorf("extending video: %w", err)
	}

	return &provider.VideoOperation{
		OperationID: op.Name,
		Model:       model,
	}, nil
}

// Status polls the current state of a video generation operation.
func (p *GeminiProvider) Status(ctx context.Context, operationID string) (*provider.VideoStatus, error) {
	if operationID == "" {
		return nil, fmt.Errorf("operationId is required")
	}

	if isOmniStub(operationID) {
		return omniUnavailableStatus(), nil
	}

	op, err := p.client.Operations.GetVideosOperation(ctx, &genai.GenerateVideosOperation{
		Name: operationID,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("polling operation %s: %w", operationID, err)
	}

	status := &provider.VideoStatus{
		OperationID: operationID,
		Done:        op.Done,
	}

	switch {
	case op.Error != nil:
		status.Progress = "failed"
		status.Error = fmt.Sprintf("%v", op.Error)
	case op.Done:
		status.Progress = "complete"
	default:
		status.Progress = "processing"
	}

	return status, nil
}

// Download retrieves a completed video operation and saves the video to disk.
func (p *GeminiProvider) Download(ctx context.Context, operationID string) (*provider.VideoResult, error) {
	if operationID == "" {
		return nil, fmt.Errorf("operationId is required")
	}

	if isOmniStub(operationID) {
		return nil, omniUnavailableDownloadError()
	}

	op, err := p.client.Operations.GetVideosOperation(ctx, &genai.GenerateVideosOperation{
		Name: operationID,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("retrieving operation %s: %w", operationID, err)
	}

	if !op.Done {
		return nil, fmt.Errorf("operation %s is not yet complete", operationID)
	}
	if op.Error != nil {
		return nil, fmt.Errorf("operation %s failed: %v", operationID, op.Error)
	}
	if op.Response == nil || len(op.Response.GeneratedVideos) == 0 || op.Response.GeneratedVideos[0].Video == nil {
		return nil, fmt.Errorf("operation %s has no video output", operationID)
	}

	video := op.Response.GeneratedVideos[0].Video

	// The API returns a URI, not inline bytes — download via the SDK
	videoBytes := video.VideoBytes
	if len(videoBytes) == 0 && video.URI != "" {
		downloaded, err := p.client.Files.Download(ctx, genai.NewDownloadURIFromVideo(video), nil)
		if err != nil {
			return nil, fmt.Errorf("downloading video from URI: %w", err)
		}
		videoBytes = downloaded
	}
	if len(videoBytes) == 0 {
		return nil, fmt.Errorf("operation %s returned empty video data", operationID)
	}

	filePath, err := p.saveVideo(videoBytes)
	if err != nil {
		return nil, err
	}

	model := modelFromOperationName(op.Name)
	if model == "" {
		model = modelFromOperationName(operationID)
	}

	return &provider.VideoResult{
		FilePath:    filePath,
		OperationID: operationID,
		Model:       model,
	}, nil
}

// saveVideo writes raw video bytes to the output directory with a generated filename.
func (p *GeminiProvider) saveVideo(data []byte) (string, error) {
	filename := generateFilename("video", "mp4")
	filePath := filepath.Join(p.outputDir, filename)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing file %s: %w", filePath, err)
	}
	return filePath, nil
}

// buildVideoConfig creates a GenerateVideosConfig from the optional parameters.
func buildVideoConfig(aspectRatio, resolution string, duration int) *genai.GenerateVideosConfig {
	config := &genai.GenerateVideosConfig{}
	if aspectRatio != "" {
		config.AspectRatio = aspectRatio
	}
	if resolution != "" {
		config.Resolution = resolution
	}
	if duration > 0 {
		d := int32(duration)
		config.DurationSeconds = &d
	}
	return config
}

func (p *GeminiProvider) validateVideoGenerationInput(model, aspectRatio, resolution string, duration int) error {
	if err := p.validateVideoModel(model, true); err != nil {
		return err
	}
	if aspectRatio != "" && aspectRatio != "16:9" && aspectRatio != "9:16" {
		return fmt.Errorf("invalid aspectRatio %q: must be 16:9 or 9:16", aspectRatio)
	}
	if resolution != "" && resolution != "720p" && resolution != "1080p" && resolution != "4k" {
		return fmt.Errorf("invalid resolution %q: must be 720p, 1080p, or 4k", resolution)
	}
	if duration != 0 && duration != 4 && duration != 6 && duration != 8 {
		return fmt.Errorf("invalid duration %d: must be 4, 6, or 8 seconds", duration)
	}
	if resolution == "4k" && model == p.modelMap["lite"] {
		return fmt.Errorf("resolution 4k is not supported by model %q", model)
	}
	return nil
}

func (p *GeminiProvider) validateVideoModel(model string, allowLite bool) error {
	switch model {
	case p.modelMap["lite"]:
		if !allowLite {
			return fmt.Errorf("model %q does not support video extension", model)
		}
	case p.modelMap["fast"], p.modelMap["standard"]:
		return nil
	case p.modelMap["omni"]:
		return nil
	case p.modelMap["nb2"], p.modelMap["pro"], p.modelMap["tts"], p.modelMap["clip"], p.modelMap["full"]:
		return fmt.Errorf("model %q does not support video generation", model)
	}
	return nil
}

func (p *GeminiProvider) resolveExtensionModel(operationID, requestedModel string) (string, error) {
	originalModel := modelFromOperationName(operationID)
	model := originalModel
	if requestedModel != "" {
		model = p.resolveModel(requestedModel, "fast")
	}
	if model == "" {
		return "", fmt.Errorf("could not determine original model from operationId %q", operationID)
	}
	if err := p.validateVideoModel(model, false); err != nil {
		return "", err
	}
	if originalModel != "" && model != originalModel {
		return "", fmt.Errorf("extension model %q must match original model %q", model, originalModel)
	}
	return model, nil
}

func modelFromOperationName(operationName string) string {
	const (
		modelPrefix    = "models/"
		operationSplit = "/operations/"
	)

	if !strings.HasPrefix(operationName, modelPrefix) {
		return ""
	}

	trimmed := strings.TrimPrefix(operationName, modelPrefix)
	if idx := strings.Index(trimmed, operationSplit); idx >= 0 {
		return trimmed[:idx]
	}

	return ""
}
