package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// registerAudioTools adds the generate_audio tool to the MCP server.
func (s *Server) registerAudioTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "generate_audio",
		Description: "Generate speech audio from a text prompt using Google's Gemini TTS. Supports voice selection, language configuration, and multi-speaker dialogue (max 2 speakers).",
	}, s.handleGenerateAudio)
}

func (s *Server) handleGenerateAudio(ctx context.Context, _ *mcp.CallToolRequest, input provider.AudioRequest) (*mcp.CallToolResult, provider.AudioResult, error) {
	if len(input.Speakers) > 2 {
		return nil, provider.AudioResult{}, fmt.Errorf("maximum 2 speakers allowed for multi-speaker TTS, got %d", len(input.Speakers))
	}
	for i, spk := range input.Speakers {
		if spk.Name == "" {
			return nil, provider.AudioResult{}, fmt.Errorf("speakers[%d].name is required", i)
		}
		if spk.VoiceName == "" {
			return nil, provider.AudioResult{}, fmt.Errorf("speakers[%d].voiceName is required", i)
		}
	}

	result, err := s.audio.GenerateAudio(ctx, input)
	if err != nil {
		return nil, provider.AudioResult{}, fmt.Errorf("generate audio: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(
				"Audio generated!\n\nModel: %s\nSaved to: %s\nType: %s",
				result.Model, result.FilePath, result.MimeType,
			)},
		},
	}, *result, nil
}
