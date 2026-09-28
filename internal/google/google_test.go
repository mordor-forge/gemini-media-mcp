package google

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		kind      apperr.Kind
		retryable bool
	}{
		{"auth", genai.APIError{Code: 400, Message: "API key not valid. Please pass a valid API key."}, apperr.Auth, false},
		{"unauthenticated", genai.APIError{Code: 401, Message: "unauthenticated"}, apperr.Auth, false},
		{"permission", genai.APIError{Code: 403, Message: "denied"}, apperr.Permission, false},
		{"quota", genai.APIError{Code: 429, Message: "Resource exhausted"}, apperr.Quota, true},
		{"not found", genai.APIError{Code: 404, Message: "models/x is not found"}, apperr.NotFound, false},
		{"safety 400", genai.APIError{Code: 400, Message: "The prompt was blocked due to safety"}, apperr.Safety, false},
		{"invalid", genai.APIError{Code: 400, Message: "durationSeconds must be 8"}, apperr.Invalid, false},
		{"server", genai.APIError{Code: 503, Message: "overloaded"}, apperr.Unavailable, true},
		{"pointer", &genai.APIError{Code: 429}, apperr.Quota, true},
		{"wrapped", fmt.Errorf("outer: %w", genai.APIError{Code: 403}), apperr.Permission, false},
		{"deadline", context.DeadlineExceeded, apperr.Timeout, true},
		{"canceled", context.Canceled, apperr.Canceled, false},
		{"other", errors.New("boom"), apperr.Unknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Classify(tt.err, "generating image", "m", "gemini-api")
			ce, ok := apperr.As(err)
			if !ok {
				t.Fatalf("not classified: %v", err)
			}
			if ce.Kind != tt.kind || ce.Retryable != tt.retryable {
				t.Fatalf("got kind=%s retryable=%v, want %s/%v (%v)", ce.Kind, ce.Retryable, tt.kind, tt.retryable, err)
			}
			if ce.Kind != apperr.Unknown && ce.Kind != apperr.Canceled && ce.Hint == "" {
				t.Fatal("classified errors must carry a hint")
			}
		})
	}
	if Classify(nil, "x", "", "") != nil {
		t.Fatal("nil in, nil out")
	}
	already := apperr.New(apperr.Budget, "over", "")
	if Classify(already, "x", "", "") != already {
		t.Fatal("already-classified errors pass through")
	}
}

func TestParseResponseSkipsThoughtsAndCollectsText(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		ModelVersion: "v1",
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{Thought: true, InlineData: &genai.Blob{Data: []byte("draft"), MIMEType: "image/png"}},
				{Text: "Here is your image."},
				nil,
				{InlineData: &genai.Blob{Data: []byte("final"), MIMEType: "image/png"}},
			}},
		}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        10,
			CandidatesTokenCount:    1120,
			TotalTokenCount:         1130,
			CandidatesTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 1120}},
		},
	}
	r, err := ParseResponse(resp, "image/")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Media) != 1 || string(r.Media[0].Data) != "final" {
		t.Fatalf("thought part leaked into media: %+v", r.Media)
	}
	if r.Text != "Here is your image." || r.Usage.OutputByModality["image"] != 1120 {
		t.Fatalf("unexpected result %+v", r)
	}
}

func TestParseResponseExplainsSafetyBlocks(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		PromptFeedback: &genai.GenerateContentResponsePromptFeedback{BlockReason: genai.BlockedReasonProhibitedContent},
	}
	_, err := ParseResponse(resp, "image/")
	if apperr.KindOf(err) != apperr.Safety {
		t.Fatalf("want safety error, got %v", err)
	}

	resp = &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		FinishReason: genai.FinishReasonImageSafety,
		Content:      &genai.Content{Parts: []*genai.Part{{Text: "I can't create that."}}},
	}}}
	_, err = ParseResponse(resp, "image/")
	if apperr.KindOf(err) != apperr.Safety {
		t.Fatalf("want safety error for IMAGE_SAFETY, got %v", err)
	}

	resp = &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		FinishReason: genai.FinishReasonStop,
		Content:      &genai.Content{Parts: []*genai.Part{{Text: "What style would you like?"}}},
	}}}
	_, err = ParseResponse(resp, "image/")
	if apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("want invalid (text-only) error, got %v", err)
	}
}

func TestPoolClientConfig(t *testing.T) {
	p := &Pool{auth: &config.Auth{Backend: config.BackendVertex, Mode: config.AuthVertexADC, Project: "p", Location: "us-central1"}, opts: Options{Retry: config.Retry{Attempts: 3, InitialDelaySeconds: 1, MaxDelaySeconds: 5}}}
	if p.key("") != "us-central1" || p.key("global") != "global" {
		t.Fatalf("ADC pool must key clients by location")
	}
	cc := p.clientConfig("global")
	if cc.Backend != genai.BackendVertexAI || cc.Project != "p" || cc.Location != "global" || cc.APIKey != "" {
		t.Fatalf("unexpected vertex config %+v", cc)
	}
	if cc.HTTPOptions.RetryOptions == nil || *cc.HTTPOptions.RetryOptions.Attempts != 3 {
		t.Fatal("retry options not set")
	}

	express := &Pool{auth: &config.Auth{Backend: config.BackendVertex, Mode: config.AuthVertexExpress, APIKey: "k"}}
	if express.key("europe-west4") != "" {
		t.Fatal("express mode uses a single global client")
	}
	cc = express.clientConfig("")
	if cc.Backend != genai.BackendVertexAI || cc.APIKey != "k" || cc.Project != "" {
		t.Fatalf("unexpected express config %+v", cc)
	}

	gem := &Pool{auth: &config.Auth{Backend: config.BackendGeminiAPI, Mode: config.AuthAPIKey, APIKey: "k"}}
	cc = gem.clientConfig("")
	if cc.Backend != genai.BackendGeminiAPI || cc.HTTPOptions.RetryOptions != nil {
		t.Fatalf("unexpected gemini config %+v", cc)
	}
}
