package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

func (s *Server) registerResources() {
	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: store.URIScheme + "{name}",
		Name:        "generated-media",
		Title:       "Generated media file",
		Description: "A file produced by this server (image, video, speech or music). Useful when the client cannot read the server's filesystem, e.g. over HTTP.",
	}, s.readMedia)
}

// maxResourceBytes bounds a file resources/read sends. The response carries
// it base64-encoded, a third larger, so the server holds it about three
// times over; larger files (big upscales) are read from the output
// directory instead.
var maxResourceBytes int64 = 64 << 20

func (s *Server) readMedia(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	// Files are read one at a time, so concurrent reads cannot add up.
	select {
	case s.reads <- struct{}{}:
		defer func() { <-s.reads }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	path, data, err := s.store.OpenMax(uri, maxResourceBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		var big *store.TooLargeError
		if errors.As(err, &big) {
			return nil, fmt.Errorf("%s is %d MB, more than resources/read sends (%d MB): read it from the server's output directory (the tool result gives its path), or make a smaller version", uri, big.Size>>20, maxResourceBytes>>20)
		}
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: store.SniffMIME(data, path),
		Blob:     data,
	}}}, nil
}
