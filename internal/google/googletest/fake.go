// Package googletest provides an in-memory fake of google.API for tests.
package googletest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
)

// ContentCall records a GenerateContent call.
type ContentCall struct {
	Location, Model string
	Contents        []*genai.Content
	Config          *genai.GenerateContentConfig
}

// VideoCall records a GenerateVideos call.
type VideoCall struct {
	Location, Model string
	Source          *genai.GenerateVideosSource
	Config          *genai.GenerateVideosConfig
}

// Fake implements google.API in memory.
type Fake struct {
	Mu           sync.Mutex
	BackendName  config.Backend
	ContentCalls []ContentCall
	VideoCalls   []VideoCall
	Polls        int
	Downloads    int
	// ContentFn answers GenerateContent (required when content calls are made).
	ContentFn func(call ContentCall) (*genai.GenerateContentResponse, error)
	// OpDoneAfter is the number of polls before operations report done.
	OpDoneAfter int
	// OpResult overrides the finished operation.
	OpResult func(name string) *genai.GenerateVideosOperation
	// Listed is returned by ListModels.
	Listed []*genai.Model
	// DownloadErr, when set, makes DownloadVideo fail.
	DownloadErr error
}

// Backend implements google.API.
func (f *Fake) Backend() config.Backend { return f.BackendName }

// GenerateContent implements google.API.
func (f *Fake) GenerateContent(_ context.Context, location, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	f.Mu.Lock()
	call := ContentCall{location, model, contents, cfg}
	f.ContentCalls = append(f.ContentCalls, call)
	fn := f.ContentFn
	f.Mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("googletest: no ContentFn")
	}
	return fn(call)
}

// GenerateVideos implements google.API.
func (f *Fake) GenerateVideos(_ context.Context, location, model string, src *genai.GenerateVideosSource, cfg *genai.GenerateVideosConfig) (*genai.GenerateVideosOperation, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.VideoCalls = append(f.VideoCalls, VideoCall{location, model, src, cfg})
	return &genai.GenerateVideosOperation{Name: fmt.Sprintf("models/%s/operations/op%d", model, len(f.VideoCalls))}, nil
}

// GetVideosOperation implements google.API.
func (f *Fake) GetVideosOperation(_ context.Context, _ string, op *genai.GenerateVideosOperation) (*genai.GenerateVideosOperation, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Polls++
	if f.Polls <= f.OpDoneAfter {
		return &genai.GenerateVideosOperation{Name: op.Name}, nil
	}
	if f.OpResult != nil {
		return f.OpResult(op.Name), nil
	}
	return &genai.GenerateVideosOperation{Name: op.Name, Done: true, Response: &genai.GenerateVideosResponse{
		GeneratedVideos: []*genai.GeneratedVideo{{Video: &genai.Video{URI: "https://files/" + op.Name, MIMEType: "video/mp4"}}},
	}}, nil
}

// DownloadVideo implements google.API and returns an 8-second fake MP4.
func (f *Fake) DownloadVideo(context.Context, string, *genai.Video) ([]byte, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Downloads++
	if f.DownloadErr != nil {
		return nil, f.DownloadErr
	}
	return MP4(8), nil
}

// ListModels implements google.API.
func (f *Fake) ListModels(context.Context) ([]*genai.Model, error) { return f.Listed, nil }

// MP4 builds a minimal byte stream containing an mvhd box of the given length.
func MP4(seconds uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{0, 0, 0, 108})
	b.WriteString("mvhd")
	b.WriteByte(0)           // version
	b.Write([]byte{0, 0, 0}) // flags
	b.Write(make([]byte, 8)) // creation + modification time
	_ = binary.Write(&b, binary.BigEndian, uint32(1000))
	_ = binary.Write(&b, binary.BigEndian, seconds*1000)
	b.Write(make([]byte, 80))
	return b.Bytes()
}

// ImageResponse returns a successful image response with usage metadata.
func ImageResponse(data []byte) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop, Content: &genai.Content{Parts: []*genai.Part{
			{Text: "A cat."},
			{InlineData: &genai.Blob{Data: data, MIMEType: "image/png"}},
		}}}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 12, CandidatesTokenCount: 1125, TotalTokenCount: 1137,
			CandidatesTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 1120}, {Modality: genai.MediaModalityText, TokenCount: 5}},
		},
	}
}
