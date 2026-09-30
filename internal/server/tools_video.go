package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// OperationInput is the input type for tools that take only an operation ID.
type OperationInput struct {
	OperationID string `json:"operationId" jsonschema:"Operation ID from a previous generate_video, animate_image, or extend_video call"`
}

// registerVideoTools adds generate_video, animate_image, extend_video,
// video_status, and download_video tools to the MCP server.
func (s *Server) registerVideoTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "generate_video",
		Description: "Generate a video from a text prompt using Google's Gemini video models. Model tiers: lite (default/cheapest), fast, standard (highest quality), omni (Gemini Omni — developer API not yet available, returns stub). This is an async operation — use video_status to poll progress and download_video to retrieve the result.",
	}, s.handleGenerateVideo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "animate_image",
		Description: "Animate a still image into a video clip. Provide the path to a source image and a prompt guiding the animation. This is an async operation — use video_status to poll progress and download_video to retrieve the result.",
	}, s.handleAnimateImage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "extend_video",
		Description: "Extend a previously generated video with a continuation prompt. Requires the operation ID from the original generation. This is an async operation — use video_status to poll progress and download_video to retrieve the result.",
	}, s.handleExtendVideo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "video_status",
		Description: "Check the status of an async video generation operation. Returns progress info (pending, processing, complete, or failed).",
	}, s.handleVideoStatus)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "download_video",
		Description: "Download a completed video generation to a local file. Only call this after video_status reports the operation is complete.",
	}, s.handleDownloadVideo)
}

func (s *Server) handleGenerateVideo(ctx context.Context, _ *mcp.CallToolRequest, input provider.VideoRequest) (*mcp.CallToolResult, provider.VideoOperation, error) {
	op, err := s.videos.GenerateVideo(ctx, input)
	if err != nil {
		return nil, provider.VideoOperation{}, fmt.Errorf("generate video: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Video generation started!\n\nOperation ID: %s\nModel: %s\n\nUse video_status to check progress, then download_video to retrieve the file.",
				op.OperationID, op.Model,
			)},
		},
	}, *op, nil
}

func (s *Server) handleAnimateImage(ctx context.Context, _ *mcp.CallToolRequest, input provider.AnimateRequest) (*mcp.CallToolResult, provider.VideoOperation, error) {
	op, err := s.videos.AnimateImage(ctx, input)
	if err != nil {
		return nil, provider.VideoOperation{}, fmt.Errorf("animate image: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Image animation started!\n\nOperation ID: %s\nModel: %s\n\nUse video_status to check progress, then download_video to retrieve the file.",
				op.OperationID, op.Model,
			)},
		},
	}, *op, nil
}

func (s *Server) handleExtendVideo(ctx context.Context, _ *mcp.CallToolRequest, input provider.ExtendRequest) (*mcp.CallToolResult, provider.VideoOperation, error) {
	op, err := s.videos.Extend(ctx, input)
	if err != nil {
		return nil, provider.VideoOperation{}, fmt.Errorf("extend video: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Video extension started!\n\nOperation ID: %s\nModel: %s\n\nUse video_status to check progress, then download_video to retrieve the file.",
				op.OperationID, op.Model,
			)},
		},
	}, *op, nil
}

func (s *Server) handleVideoStatus(ctx context.Context, _ *mcp.CallToolRequest, input OperationInput) (*mcp.CallToolResult, provider.VideoStatus, error) {
	status, err := s.videos.Status(ctx, input.OperationID)
	if err != nil {
		return nil, provider.VideoStatus{}, fmt.Errorf("video status: %w", err)
	}

	text := fmt.Sprintf(
		"Operation: %s\nProgress: %s\nDone: %t",
		status.OperationID, status.Progress, status.Done,
	)
	if status.Error != "" {
		text += fmt.Sprintf("\nError: %s", status.Error)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}, *status, nil
}

func (s *Server) handleDownloadVideo(ctx context.Context, _ *mcp.CallToolRequest, input OperationInput) (*mcp.CallToolResult, provider.VideoResult, error) {
	result, err := s.videos.Download(ctx, input.OperationID)
	if err != nil {
		return nil, provider.VideoResult{}, fmt.Errorf("download video: %w", err)
	}

	lines := []string{
		"Video downloaded!",
		"",
		fmt.Sprintf("Saved to: %s", result.FilePath),
		fmt.Sprintf("Operation: %s", result.OperationID),
	}
	if result.Model != "" {
		lines = append(lines, fmt.Sprintf("Model: %s", result.Model))
	}
	if result.Duration > 0 {
		lines = append(lines, fmt.Sprintf("Duration: %ds", result.Duration))
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: strings.Join(lines, "\n")},
		},
	}, *result, nil
}
