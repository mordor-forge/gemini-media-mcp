package gemini

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// Generate creates an image from a text prompt using Gemini's GenerateContent
// API with image response modality. This is the correct approach for Gemini
// image models (nb2/pro) — the GenerateImages endpoint is Imagen-only.
func (p *GeminiProvider) Generate(ctx context.Context, req provider.ImageRequest) (*provider.ImageResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	model := p.resolveModel(req.Model, "nb2")
	if err := p.validateKnownModel(model, "image operations", p.modelMap["nb2"], p.modelMap["pro"]); err != nil {
		return nil, err
	}

	contents := []*genai.Content{
		genai.NewContentFromText(req.Prompt, genai.RoleUser),
	}
	config := buildImageGenerateConfig(req)

	resp, err := p.client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return nil, fmt.Errorf("generating image: %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no image returned by the API")
	}

	blob, ok := extractFirstNonEmptyInlineData(resp)
	if !ok {
		return nil, fmt.Errorf("no image data found in API response")
	}

	ext := extensionFromMIME(blob.MIMEType)
	filePath, err := p.saveImage(blob.Data, ext)
	if err != nil {
		return nil, err
	}
	return &provider.ImageResult{
		FilePath: filePath,
		Model:    model,
		MimeType: blob.MIMEType,
	}, nil
}

// Edit modifies an existing image based on a text prompt using GenerateContent
// with the source image as inline data alongside the edit instructions.
func (p *GeminiProvider) Edit(ctx context.Context, req provider.EditImageRequest) (*provider.ImageResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if req.ImagePath == "" {
		return nil, fmt.Errorf("imagePath is required")
	}

	model := p.resolveModel(req.Model, "nb2")
	if err := p.validateKnownModel(model, "image operations", p.modelMap["nb2"], p.modelMap["pro"]); err != nil {
		return nil, err
	}

	imgBytes, err := os.ReadFile(req.ImagePath)
	if err != nil {
		return nil, fmt.Errorf("reading source image %s: %w", req.ImagePath, err)
	}

	contents := []*genai.Content{
		{
			Role: string(genai.RoleUser),
			Parts: []*genai.Part{
				{InlineData: &genai.Blob{Data: imgBytes, MIMEType: mimeFromPath(req.ImagePath)}},
				{Text: req.Prompt},
			},
		},
	}
	config := buildImageGenerateConfig(provider.ImageRequest{})

	resp, err := p.client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return nil, fmt.Errorf("editing image: %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no image returned by the API")
	}

	blob, ok := extractFirstNonEmptyInlineData(resp)
	if !ok {
		return nil, fmt.Errorf("no image data found in API response")
	}

	ext := extensionFromMIME(blob.MIMEType)
	filePath, err := p.saveImage(blob.Data, ext)
	if err != nil {
		return nil, err
	}
	return &provider.ImageResult{
		FilePath: filePath,
		Model:    model,
		MimeType: blob.MIMEType,
	}, nil
}

// Compose creates a new image guided by one or more reference images using
// GenerateContent with all reference images as inline data parts.
func (p *GeminiProvider) Compose(ctx context.Context, req provider.ComposeRequest) (*provider.ImageResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(req.ReferenceImages) == 0 {
		return nil, fmt.Errorf("at least one reference image is required")
	}
	if len(req.ReferenceImages) > 3 {
		return nil, fmt.Errorf("maximum 3 reference images allowed, got %d", len(req.ReferenceImages))
	}

	model := p.resolveModel(req.Model, "nb2")
	if err := p.validateKnownModel(model, "image operations", p.modelMap["nb2"], p.modelMap["pro"]); err != nil {
		return nil, err
	}

	// Build parts: reference images first, then the text prompt
	parts := make([]*genai.Part, 0, len(req.ReferenceImages)+1)
	for _, path := range req.ReferenceImages {
		imgBytes, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading reference image %s: %w", path, err)
		}
		parts = append(parts, &genai.Part{
			InlineData: &genai.Blob{Data: imgBytes, MIMEType: mimeFromPath(path)},
		})
	}
	parts = append(parts, &genai.Part{Text: req.Prompt})

	contents := []*genai.Content{{Role: string(genai.RoleUser), Parts: parts}}
	config := buildImageGenerateConfig(provider.ImageRequest{AspectRatio: req.AspectRatio})

	resp, err := p.client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return nil, fmt.Errorf("composing image: %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no image returned by the API")
	}

	blob, ok := extractFirstNonEmptyInlineData(resp)
	if !ok {
		return nil, fmt.Errorf("no image data found in API response")
	}

	ext := extensionFromMIME(blob.MIMEType)
	filePath, err := p.saveImage(blob.Data, ext)
	if err != nil {
		return nil, err
	}
	return &provider.ImageResult{
		FilePath: filePath,
		Model:    model,
		MimeType: blob.MIMEType,
	}, nil
}

// saveImage writes raw image bytes to the output directory with a generated filename.
func (p *GeminiProvider) saveImage(data []byte, ext string) (string, error) {
	filename := generateFilename("image", ext)
	filePath := filepath.Join(p.outputDir, filename)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing file %s: %w", filePath, err)
	}
	return filePath, nil
}

// extensionFromMIME returns the file extension for a given MIME type.
func extensionFromMIME(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}

// mimeFromPath infers the MIME type from a file's extension.
func mimeFromPath(path string) string {
	switch filepath.Ext(path) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func buildImageGenerateConfig(req provider.ImageRequest) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"IMAGE", "TEXT"},
	}

	if req.Thinking {
		budget := int32(1024)
		if req.ThinkingBudget > 0 {
			budget = req.ThinkingBudget
		}
		config.ThinkingConfig = &genai.ThinkingConfig{
			ThinkingBudget: genai.Ptr(budget),
		}
	}

	if req.AspectRatio != "" || req.Resolution != "" {
		config.ImageConfig = &genai.ImageConfig{
			AspectRatio: req.AspectRatio,
			ImageSize:   req.Resolution,
		}
	}
	return config
}
