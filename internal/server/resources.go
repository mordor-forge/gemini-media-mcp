package server

import (
	"context"
	"errors"
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

func (s *Server) readMedia(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	path, data, err := s.store.Open(uri)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: store.SniffMIME(data, path),
		Blob:     data,
	}}}, nil
}
