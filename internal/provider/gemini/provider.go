package gemini

import (
	"context"
	"fmt"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// Compile-time interface satisfaction checks.
var (
	_ provider.ImageGenerator = (*GeminiProvider)(nil)
	_ provider.VideoGenerator = (*GeminiProvider)(nil)
	_ provider.AudioGenerator = (*GeminiProvider)(nil)
	_ provider.MusicGenerator = (*GeminiProvider)(nil)
	_ provider.ModelLister    = (*GeminiProvider)(nil)
)

// GeminiProvider wraps the official genai SDK client and implements
// the provider interfaces (ImageGenerator, VideoGenerator, ModelLister).
type GeminiProvider struct {
	client    *genai.Client
	outputDir string
	modelMap  map[string]string
}

// Config holds the parameters needed to construct a GeminiProvider.
type Config struct {
	APIKey         string
	VertexProject  string
	VertexLocation string
	OutputDir      string
}

// NewFromConfig creates a GeminiProvider from the application-level config.
func NewFromConfig(ctx context.Context, cfg *config.Config) (*GeminiProvider, error) {
	return New(ctx, Config{
		APIKey:         cfg.Provider.APIKey,
		VertexProject:  cfg.Provider.VertexProject,
		VertexLocation: cfg.Provider.VertexLocation,
		OutputDir:      cfg.OutputDir,
	})
}

// New creates a GeminiProvider with the given configuration.
// It selects the Vertex AI or Gemini API backend based on
// which credentials are provided.
func New(ctx context.Context, cfg Config) (*GeminiProvider, error) {
	clientCfg := &genai.ClientConfig{}

	if cfg.APIKey != "" {
		// API key takes precedence — avoids accidentally using Vertex AI
		// when GOOGLE_CLOUD_PROJECT leaks from the shell environment.
		clientCfg.Backend = genai.BackendGeminiAPI
		clientCfg.APIKey = cfg.APIKey
	} else if cfg.VertexProject != "" {
		clientCfg.Backend = genai.BackendVertexAI
		clientCfg.Project = cfg.VertexProject
		clientCfg.Location = cfg.VertexLocation
	} else {
		return nil, fmt.Errorf("no credentials: provide APIKey or VertexProject")
	}

	client, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("creating genai client: %w", err)
	}

	return &GeminiProvider{
		client:    client,
		outputDir: cfg.OutputDir,
		modelMap:  defaultModelMap(),
	}, nil
}

// defaultModelMap returns the mapping from friendly tier names to
// actual Gemini model identifiers.
func defaultModelMap() map[string]string {
	return map[string]string{
		"nb2":      "gemini-3.1-flash-image-preview",
		"pro":      "gemini-3-pro-image-preview",
		"lite":     "veo-3.1-lite-generate-preview",
		"fast":     "veo-3.1-fast-generate-preview",
		"standard": "veo-3.1-generate-preview",
		"tts":      "gemini-2.5-flash-preview-tts",
		"clip":     "lyria-3-clip-preview",
		"full":     "lyria-3-pro-preview",
		"omni":     "gemini-omni-flash",
	}
}

// resolveModel maps a friendly model tier name to its full model ID.
// If model is empty, the fallback is used instead. If the name is
// not in the model map, it is returned as-is (assumed to be a raw ID).
func (p *GeminiProvider) resolveModel(model, fallback string) string {
	if model == "" {
		model = fallback
	}
	if resolved, ok := p.modelMap[model]; ok {
		return resolved
	}
	return model
}

func (p *GeminiProvider) validateKnownModel(model, operation string, allowed ...string) error {
	allowedModels := make(map[string]struct{}, len(allowed))
	for _, allowedModel := range allowed {
		allowedModels[allowedModel] = struct{}{}
	}

	if _, ok := allowedModels[model]; ok {
		return nil
	}

	for _, knownModel := range p.modelMap {
		if model == knownModel {
			return fmt.Errorf("model %q does not support %s", model, operation)
		}
	}

	return nil
}
