package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

func TestGenerateOmniVideo_ReturnsSentinel(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	op, err := p.generateOmniVideo(context.Background(), provider.VideoRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("generateOmniVideo: %v", err)
	}
	if op.OperationID != omniStatusUnavailable {
		t.Errorf("OperationID = %q, want %q", op.OperationID, omniStatusUnavailable)
	}
	if !strings.Contains(op.Model, "pending") {
		t.Errorf("Model = %q, want to contain 'pending'", op.Model)
	}
}

func TestIsOmniStub(t *testing.T) {
	if !isOmniStub(omniStatusUnavailable) {
		t.Fatal("isOmniStub(omniStatusUnavailable) = false, want true")
	}
	if isOmniStub("models/veo-3.1/operations/op-123") {
		t.Fatal("isOmniStub(real-op) = true, want false")
	}
	if isOmniStub("") {
		t.Fatal("isOmniStub(\"\") = true, want false")
	}
}

func TestGenerateVideo_RoutesToOmni(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	op, err := p.GenerateVideo(context.Background(), provider.VideoRequest{
		Prompt: "test omni video",
		Model:  "omni",
	})
	if err != nil {
		t.Fatalf("GenerateVideo(omni): %v", err)
	}
	if op.OperationID != omniStatusUnavailable {
		t.Errorf("OperationID = %q, want %q", op.OperationID, omniStatusUnavailable)
	}
}

func TestStatus_OmniSentinel(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	status, err := p.Status(context.Background(), omniStatusUnavailable)
	if err != nil {
		t.Fatalf("Status(omni): %v", err)
	}
	if status.Progress != "unavailable" {
		t.Errorf("Progress = %q, want 'unavailable'", status.Progress)
	}
	if !status.Done {
		t.Error("Done = false, want true")
	}
	if status.Error == "" {
		t.Error("Error is empty, want unavailable message")
	}
	if !strings.Contains(status.Error, "not yet available") {
		t.Errorf("Error = %q, want to contain 'not yet available'", status.Error)
	}
}

func TestDownload_OmniSentinel(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Download(context.Background(), omniStatusUnavailable)
	if err == nil {
		t.Fatal("expected error for omni stub download")
	}
	if !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("error = %q, want to contain 'not yet available'", err)
	}
}

func TestValidateVideoModel_AllowsOmni(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	if err := p.validateVideoModel(p.modelMap["omni"], true); err != nil {
		t.Fatalf("validateVideoModel(omni): %v", err)
	}
}
