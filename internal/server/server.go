// Package server exposes the media service as an MCP server over stdio or
// Streamable HTTP.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/media"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
	"github.com/mordor-forge/gemini-media-mcp/internal/version"
)

// Instructions are sent to clients (initialize / server/discover) and are
// often placed in the agent's system prompt, so they stay short.
const Instructions = `Generates images (Nano Banana), video (Veo and Gemini Omni), speech (Gemini TTS) and music (Lyria) with Google models. Outputs are saved on the server; results include the file path and a gemini-media:// URI.

Workflow tips:
- Chain outputs: pass a previous result's uri (or path) as image/referenceImages/images input.
- Video is asynchronous: generate_video, extend_video and edit_video return a jobId; call get_video (waitSeconds ~45) until state is completed. Veo takes 1-3 minutes, Omni 1-5.
- edit_video changes a finished clip or a short video file from an instruction (model omni).
- Upscaling (any target from 1K to 8K and beyond): tile_image plans the crops, edit_image re-renders each, stitch_tiles blends them.
- Every call costs money. Results report cost; estimate_cost compares options before expensive calls (4K images, 1080p/4k or standard-tier video). If a call is rejected for confirmation, ask the user, then retry with approvedCostUsd.
- list_models shows current models, aliases, supported parameters and prices; retired models are redirected automatically with a warning.`

// Options configure the server.
type Options struct {
	Transport string
	Logger    *slog.Logger
}

// Server wires the media service into an MCP server.
type Server struct {
	mcp       *mcp.Server
	svc       *media.Service
	store     *store.Store
	transport string
	log       *slog.Logger
	reads     chan struct{} // one resources/read at a time
}

// New builds the MCP server and registers all tools and resources.
func New(svc *media.Service, st *store.Store, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	impl := &mcp.Implementation{
		Name:       version.Name,
		Title:      "Gemini Media",
		Version:    version.String(),
		WebsiteURL: "https://github.com/mordor-forge/gemini-media-mcp",
	}
	m := mcp.NewServer(impl, &mcp.ServerOptions{
		Instructions: Instructions,
		Logger:       log,
		// Logging is deprecated in MCP 2026-07-28; logs go to stderr instead.
		Capabilities: &mcp.ServerCapabilities{},
	})
	s := &Server{mcp: m, svc: svc, store: st, transport: opts.Transport, log: log, reads: make(chan struct{}, 1)}
	s.registerTools()
	s.registerResources()
	return s
}

// MCP returns the underlying server (for transports and tests).
func (s *Server) MCP() *mcp.Server { return s.mcp }

// RunStdio serves over stdin/stdout until ctx is cancelled or the client disconnects.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// withProgress bridges service progress callbacks to MCP progress notifications.
func withProgress(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil || req.Params == nil || req.Session == nil {
		return ctx
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return ctx
	}
	return media.WithProgress(ctx, func(msg string, progress, total float64) {
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Message: msg, Progress: progress, Total: total,
		})
	})
}

// toolError formats a service error for the agent: category, message and
// the remediation hint. The SDK turns it into an isError tool result.
func toolError(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := apperr.As(err); ok {
		return fmt.Errorf("[%s] %s", ce.Kind, ce.Error())
	}
	return fmt.Errorf("[%s] %s", apperr.Unknown, err.Error())
}

func boolPtr(b bool) *bool { return &b }

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
