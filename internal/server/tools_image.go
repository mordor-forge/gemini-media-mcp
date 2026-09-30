package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// registerImageTools adds generate_image, edit_image, and compose_images
// tools to the MCP server.
func (s *Server) registerImageTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "generate_image",
		Description: "Generate an image from a text prompt using Google's Gemini image models. Supports optional thinking/reasoning mode for complex compositions.",
	}, s.handleGenerateImage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "edit_image",
		Description: "Edit an existing image using a text prompt. Provide the path to the source image and a description of the desired changes.",
	}, s.handleEditImage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "compose_images",
		Description: "Compose a new image using 1-3 reference images and a text prompt for style/content guidance.",
	}, s.handleComposeImages)
}

func (s *Server) handleGenerateImage(ctx context.Context, _ *mcp.CallToolRequest, input provider.ImageRequest) (*mcp.CallToolResult, provider.ImageResult, error) {
	if input.ThinkingBudget > 8192 {
		return nil, provider.ImageResult{}, fmt.Errorf("thinkingBudget must be <= 8192, got %d", input.ThinkingBudget)
	}
	if input.ThinkingBudget > 0 && !input.Thinking {
		input.Thinking = true
	}

	result, err := s.images.Generate(ctx, input)
	if err != nil {
		return nil, provider.ImageResult{}, fmt.Errorf("generate image: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Image generated!\n\nModel: %s\nSaved to: %s\nType: %s",
				result.Model, result.FilePath, result.MimeType,
			)},
		},
	}, *result, nil
}

func (s *Server) handleEditImage(ctx context.Context, _ *mcp.CallToolRequest, input provider.EditImageRequest) (*mcp.CallToolResult, provider.ImageResult, error) {
	result, err := s.images.Edit(ctx, input)
	if err != nil {
		return nil, provider.ImageResult{}, fmt.Errorf("edit image: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Image edited!\n\nModel: %s\nSaved to: %s\nType: %s",
				result.Model, result.FilePath, result.MimeType,
			)},
		},
	}, *result, nil
}

func (s *Server) handleComposeImages(ctx context.Context, _ *mcp.CallToolRequest, input provider.ComposeRequest) (*mcp.CallToolResult, provider.ImageResult, error) {
	result, err := s.images.Compose(ctx, input)
	if err != nil {
		return nil, provider.ImageResult{}, fmt.Errorf("compose images: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Image composed!\n\nModel: %s\nSaved to: %s\nType: %s",
				result.Model, result.FilePath, result.MimeType,
			)},
		},
	}, *result, nil
}
