package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/version"
)

// maxRequestBody allows inline data: URI inputs (20 MB files, base64-encoded).
const maxRequestBody = 32 << 20

// HTTPHandler returns the Streamable HTTP handler with health check, optional
// bearer-token auth and cross-origin protection.
//
// Stateless mode is the default: it is required by MCP 2026-07-28 and still
// serves older clients (each request gets a temporary session). State that
// must survive across calls (video jobs) lives in explicit handles.
func (s *Server) HTTPHandler(cfg config.HTTP) (http.Handler, error) {
	opts := &mcp.StreamableHTTPOptions{
		Stateless:                    !cfg.Stateful,
		Logger:                       s.log,
		MaxRequestBodyBytes:          maxRequestBody,
		PropagateRequestCancellation: true,
	}
	if cfg.Stateful {
		opts.SessionTimeout = 30 * time.Minute
	}
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, opts)

	// Browsers must not be able to drive a local server from another origin.
	cop := http.NewCrossOriginProtection()
	for _, o := range cfg.AllowedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("allowed origin %q: %w", o, err)
		}
	}
	h = cop.Handler(h)

	if cfg.AuthToken != "" {
		token := []byte(cfg.AuthToken)
		verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
			if subtle.ConstantTimeCompare([]byte(got), token) != 1 {
				return nil, auth.ErrInvalidToken
			}
			return &auth.TokenInfo{UserID: "token", Expiration: time.Now().Add(time.Hour)}, nil
		}
		h = auth.RequireBearerToken(verify, nil)(h)
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Path, h)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "name": version.Name, "version": version.String()})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "%s %s: MCP endpoint at %s (Streamable HTTP)\n", version.Name, version.String(), cfg.Path)
	})
	return mux, nil
}

// RunHTTP serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) RunHTTP(ctx context.Context, cfg config.HTTP) error {
	handler, err := s.HTTPHandler(cfg)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Addr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	s.log.Info("serving MCP over Streamable HTTP", "url", "http://"+ln.Addr().String()+cfg.Path, "stateless", !cfg.Stateful, "auth", cfg.AuthToken != "")

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
