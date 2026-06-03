package gemini

import (
	"context"
	"fmt"

	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

// omniStatusUnavailable is a sentinel operation ID that Status() and
// Download() recognize to return the "API not available" message.
const omniStatusUnavailable = "omni-unavailable"

// generateOmniVideo is the Omni video stub. When the Gemini Omni developer
// API becomes available, this will be replaced with a real generateContent
// call using responseModalities: ["VIDEO"].
func (p *GeminiProvider) generateOmniVideo(_ context.Context, _ provider.VideoRequest) (*provider.VideoOperation, error) {
	return &provider.VideoOperation{
		OperationID: omniStatusUnavailable,
		Model:       p.modelMap["omni"] + " (pending)",
	}, nil
}

// isOmniStub returns true if the operation ID is the Omni unavailable sentinel.
func isOmniStub(operationID string) bool {
	return operationID == omniStatusUnavailable
}

// omniUnavailableStatus returns a VideoStatus indicating the Omni API is not yet available.
func omniUnavailableStatus() *provider.VideoStatus {
	return &provider.VideoStatus{
		OperationID: omniStatusUnavailable,
		Done:        true,
		Progress:    "unavailable",
		Error:       "Gemini Omni developer API is not yet available. Use model 'lite', 'fast', or 'standard' (Veo 3.1) for video generation.",
	}
}

// omniUnavailableDownloadError returns the error for attempting to download an Omni stub.
func omniUnavailableDownloadError() error {
	return fmt.Errorf("cannot download: Gemini Omni developer API is not yet available — use model 'lite', 'fast', or 'standard' (Veo 3.1)")
}
