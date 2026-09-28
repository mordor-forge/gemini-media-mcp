// Package google adapts the google.golang.org/genai SDK to the narrow
// interface the media service needs. It owns client construction (Gemini API,
// Vertex AI with ADC, Vertex express mode), per-location client pooling,
// retries and error classification.
package google

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/version"
)

// API is the subset of Google generative media operations used by the server.
// The location argument selects a Vertex AI region and is ignored by the
// Gemini API backend.
type API interface {
	GenerateContent(ctx context.Context, location, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	GenerateVideos(ctx context.Context, location, model string, src *genai.GenerateVideosSource, cfg *genai.GenerateVideosConfig) (*genai.GenerateVideosOperation, error)
	GetVideosOperation(ctx context.Context, location string, op *genai.GenerateVideosOperation) (*genai.GenerateVideosOperation, error)
	DownloadVideo(ctx context.Context, location string, v *genai.Video) ([]byte, error)
	ListModels(ctx context.Context) ([]*genai.Model, error)
	Backend() config.Backend
}

// Options tune client construction.
type Options struct {
	Retry          config.Retry
	RequestTimeout time.Duration
	HTTPClient     *http.Client // optional, for tests
}

// Pool lazily creates one genai client per Vertex location (or a single
// client for the Gemini API / express mode) and implements API.
type Pool struct {
	auth *config.Auth
	opts Options

	mu      sync.Mutex
	clients map[string]*genai.Client
}

var _ API = (*Pool)(nil)

// NewPool validates that a client can be built for the default location.
func NewPool(ctx context.Context, auth *config.Auth, opts Options) (*Pool, error) {
	p := &Pool{auth: auth, opts: opts, clients: map[string]*genai.Client{}}
	if _, err := p.client(ctx, ""); err != nil {
		return nil, err
	}
	return p, nil
}

// Backend reports the resolved backend.
func (p *Pool) Backend() config.Backend { return p.auth.Backend }

func (p *Pool) key(location string) string {
	if p.auth.Mode != config.AuthVertexADC {
		return "" // one endpoint serves everything
	}
	if location == "" {
		return p.auth.Location
	}
	return location
}

func (p *Pool) client(ctx context.Context, location string) (*genai.Client, error) {
	key := p.key(location)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[key]; ok {
		return c, nil
	}
	cc := p.clientConfig(key)
	c, err := genai.NewClient(ctx, cc)
	if err != nil {
		return nil, fmt.Errorf("creating Google GenAI client (%s): %w", p.auth.Mode, err)
	}
	p.clients[key] = c
	return c, nil
}

func (p *Pool) clientConfig(location string) *genai.ClientConfig {
	cc := &genai.ClientConfig{HTTPClient: p.opts.HTTPClient}
	switch p.auth.Mode {
	case config.AuthAPIKey:
		cc.Backend = genai.BackendGeminiAPI
		cc.APIKey = p.auth.APIKey
	case config.AuthVertexExpress:
		cc.Backend = genai.BackendVertexAI
		cc.APIKey = p.auth.APIKey
	case config.AuthVertexADC:
		cc.Backend = genai.BackendVertexAI
		cc.Project = p.auth.Project
		cc.Location = location
	}
	cc.HTTPOptions.Headers = http.Header{"User-Agent": []string{version.Name + "/" + version.String()}}
	if p.opts.RequestTimeout > 0 {
		t := p.opts.RequestTimeout
		cc.HTTPOptions.Timeout = &t
	}
	if p.opts.Retry.Attempts > 1 {
		attempts := int32(p.opts.Retry.Attempts)
		initial := p.opts.Retry.InitialDelaySeconds
		maxDelay := p.opts.Retry.MaxDelaySeconds
		cc.HTTPOptions.RetryOptions = &genai.HTTPRetryOptions{
			Attempts:     &attempts,
			InitialDelay: &initial,
			MaxDelay:     &maxDelay,
			// 408 is excluded on purpose: a timed-out generation may still
			// complete (and bill) server-side, so retrying could double-charge.
			HTTPStatusCodes: []int32{429, 500, 502, 503, 504},
		}
	}
	return cc
}

// GenerateContent implements API.
func (p *Pool) GenerateContent(ctx context.Context, location, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	c, err := p.client(ctx, location)
	if err != nil {
		return nil, err
	}
	return c.Models.GenerateContent(ctx, model, contents, cfg)
}

// GenerateVideos implements API.
func (p *Pool) GenerateVideos(ctx context.Context, location, model string, src *genai.GenerateVideosSource, cfg *genai.GenerateVideosConfig) (*genai.GenerateVideosOperation, error) {
	c, err := p.client(ctx, location)
	if err != nil {
		return nil, err
	}
	return c.Models.GenerateVideosFromSource(ctx, model, src, cfg)
}

// GetVideosOperation implements API.
func (p *Pool) GetVideosOperation(ctx context.Context, location string, op *genai.GenerateVideosOperation) (*genai.GenerateVideosOperation, error) {
	c, err := p.client(ctx, location)
	if err != nil {
		return nil, err
	}
	return c.Operations.GetVideosOperation(ctx, op, nil)
}

// DownloadVideo returns the bytes of a generated video. Vertex AI returns
// bytes inline; the Gemini API returns a file URI that must be downloaded.
func (p *Pool) DownloadVideo(ctx context.Context, location string, v *genai.Video) ([]byte, error) {
	if v == nil {
		return nil, fmt.Errorf("no video in response")
	}
	if len(v.VideoBytes) > 0 {
		return v.VideoBytes, nil
	}
	if v.URI == "" {
		return nil, fmt.Errorf("video has neither bytes nor a URI")
	}
	if p.auth.Backend == config.BackendVertex {
		return nil, fmt.Errorf("video was written to %s; download it with `gcloud storage cp` (Cloud Storage outputs are not fetched by this server)", v.URI)
	}
	c, err := p.client(ctx, location)
	if err != nil {
		return nil, err
	}
	return c.Files.Download(ctx, genai.NewDownloadURIFromVideo(v), nil)
}

// ListModels lists models visible to the credentials (Gemini API: all base
// models; Vertex: publisher models are not enumerable this way, so an
// empty list is returned).
func (p *Pool) ListModels(ctx context.Context) ([]*genai.Model, error) {
	if p.auth.Backend == config.BackendVertex {
		return nil, nil
	}
	c, err := p.client(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []*genai.Model
	for m, err := range c.Models.All(ctx) {
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Unconfigured is the API used when no credentials are available. Every call
// fails; the media service checks readiness first so these are a safety net.
type Unconfigured struct{}

var _ API = Unconfigured{}

var errUnconfigured = fmt.Errorf("no Google credentials configured")

// Backend implements API.
func (Unconfigured) Backend() config.Backend { return config.BackendAuto }

// GenerateContent implements API.
func (Unconfigured) GenerateContent(context.Context, string, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return nil, errUnconfigured
}

// GenerateVideos implements API.
func (Unconfigured) GenerateVideos(context.Context, string, string, *genai.GenerateVideosSource, *genai.GenerateVideosConfig) (*genai.GenerateVideosOperation, error) {
	return nil, errUnconfigured
}

// GetVideosOperation implements API.
func (Unconfigured) GetVideosOperation(context.Context, string, *genai.GenerateVideosOperation) (*genai.GenerateVideosOperation, error) {
	return nil, errUnconfigured
}

// DownloadVideo implements API.
func (Unconfigured) DownloadVideo(context.Context, string, *genai.Video) ([]byte, error) {
	return nil, errUnconfigured
}

// ListModels implements API.
func (Unconfigured) ListModels(context.Context) ([]*genai.Model, error) { return nil, errUnconfigured }
