package gemini

import (
	"context"

	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// ListModels returns a static list of available models with their
// capabilities and indicative pricing. No API call is made.
func (p *GeminiProvider) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{
		{
			ID: p.modelMap["nb2"], Tier: "nb2", MediaType: "image",
			Resolutions:  []string{"1K", "2K", "4K"},
			AspectRatios: []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"},
			PricePerSec:  "$0.067/img",
		},
		{
			ID: p.modelMap["pro"], Tier: "pro", MediaType: "image",
			Resolutions:  []string{"1K", "2K", "4K"},
			AspectRatios: []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"},
			PricePerSec:  "$0.134/img",
		},
		{
			ID: p.modelMap["lite"], Tier: "lite", MediaType: "video",
			Resolutions:  []string{"720p", "1080p"},
			AspectRatios: []string{"16:9", "9:16"},
			PricePerSec:  "$0.05/sec (720p), $0.08/sec (1080p)",
		},
		{
			ID: p.modelMap["fast"], Tier: "fast", MediaType: "video",
			Resolutions:  []string{"720p", "1080p", "4k"},
			AspectRatios: []string{"16:9", "9:16"},
			PricePerSec:  "$0.15/sec (720p/1080p), $0.35/sec (4k)",
		},
		{
			ID: p.modelMap["standard"], Tier: "standard", MediaType: "video",
			Resolutions:  []string{"720p", "1080p", "4k"},
			AspectRatios: []string{"16:9", "9:16"},
			PricePerSec:  "$0.40/sec (720p/1080p), $0.60/sec (4k)",
		},
		{
			ID: p.modelMap["tts"], Tier: "tts", MediaType: "audio",
			Resolutions:  []string{},
			AspectRatios: []string{},
			PricePerSec:  "standard Gemini token pricing",
		},
		{
			ID: p.modelMap["clip"], Tier: "clip", MediaType: "music",
			Resolutions:  []string{},
			AspectRatios: []string{},
			PricePerSec:  "standard Gemini token pricing (~$0.08/song)",
		},
		{
			ID: p.modelMap["full"], Tier: "full", MediaType: "music",
			Resolutions:  []string{},
			AspectRatios: []string{},
			PricePerSec:  "standard Gemini token pricing (~$0.15/song)",
		},
		{
			ID: p.modelMap["omni"] + " (pending)", Tier: "omni", MediaType: "video",
			Resolutions:  []string{},
			AspectRatios: []string{"16:9", "9:16"},
			PricePerSec:  "TBD — developer API not yet available",
		},
	}, nil
}
