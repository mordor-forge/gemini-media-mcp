package google

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
)

// Blob is a generated media payload.
type Blob struct {
	Data     []byte
	MIMEType string
}

// Result is the normalized content of a GenerateContent response.
type Result struct {
	Media        []Blob
	Text         string
	FinishReason string
	ModelVersion string
	ResponseID   string
	Usage        Usage
	// ModelStatus carries lifecycle signals (Gemini API only), e.g. an
	// upcoming retirement date for a preview model.
	ModelStage     string
	ModelRetiresAt *time.Time
	ModelNotice    string
}

// Usage mirrors usageMetadata with per-modality token counts.
type Usage struct {
	PromptTokens     int            `json:"promptTokens,omitempty"`
	OutputTokens     int            `json:"outputTokens,omitempty"`
	ThoughtsTokens   int            `json:"thoughtsTokens,omitempty"`
	TotalTokens      int            `json:"totalTokens,omitempty"`
	PromptByModality map[string]int `json:"promptByModality,omitempty"`
	OutputByModality map[string]int `json:"outputByModality,omitempty"`
	ServiceTier      string         `json:"serviceTier,omitempty"`
	TrafficType      string         `json:"trafficType,omitempty"`
}

// ParseResponse extracts media, text and metadata. It ignores "thought" parts
// (interim drafts some image models emit) and returns a classified safety
// error when the response was blocked and carries no media.
func ParseResponse(resp *genai.GenerateContentResponse, wantMIMEPrefix string) (*Result, error) {
	if resp == nil {
		return nil, apperr.New(apperr.Unavailable, "the API returned an empty response", "Retry the request.")
	}
	r := &Result{ModelVersion: resp.ModelVersion, ResponseID: resp.ResponseID, Usage: usageFrom(resp.UsageMetadata)}
	if ms := resp.ModelStatus; ms != nil {
		r.ModelStage = string(ms.ModelStage)
		r.ModelNotice = ms.Message
		if !ms.RetirementTime.IsZero() {
			t := ms.RetirementTime
			r.ModelRetiresAt = &t
		}
	}

	var texts []string
	var finishMessages []string
	for _, cand := range resp.Candidates {
		if cand == nil {
			continue
		}
		if r.FinishReason == "" && cand.FinishReason != "" {
			r.FinishReason = string(cand.FinishReason)
		}
		if cand.FinishMessage != "" {
			finishMessages = append(finishMessages, cand.FinishMessage)
		}
		if cand.Content == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			if part == nil || part.Thought {
				continue
			}
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				mt := part.InlineData.MIMEType
				if wantMIMEPrefix == "" || strings.HasPrefix(mt, wantMIMEPrefix) || mt == "" {
					r.Media = append(r.Media, Blob{Data: part.InlineData.Data, MIMEType: mt})
				}
				continue
			}
			if t := strings.TrimSpace(part.Text); t != "" {
				texts = append(texts, t)
			}
		}
	}
	r.Text = strings.Join(texts, "\n")

	if len(r.Media) > 0 {
		return r, nil
	}

	// No media: explain why as precisely as the API allows.
	reason := r.FinishReason
	detail := strings.Join(finishMessages, " ")
	if pf := resp.PromptFeedback; pf != nil && pf.BlockReason != "" {
		reason = string(pf.BlockReason)
		if pf.BlockReasonMessage != "" {
			detail = pf.BlockReasonMessage
		}
	}
	if detail == "" {
		detail = r.Text
	}
	msg := "the model returned no " + strings.TrimSuffix(wantMIMEPrefix, "/")
	if reason != "" {
		msg += " (finish reason " + reason + ")"
	}
	if detail != "" {
		msg += ": " + truncate(detail, 500)
	}
	if isSafetyReason(reason) {
		return r, &apperr.Error{Kind: apperr.Safety, Message: msg, Hint: "Content was blocked by safety filters. Rephrase the prompt (avoid real people, logos, copyrighted characters, violence) or change the input media."}
	}
	return r, &apperr.Error{Kind: apperr.Invalid, Message: msg, Hint: "Make the request explicit about the output (e.g. 'Generate an image of ...'), or retry: generation is stochastic."}
}

var safetyReasons = []string{
	"SAFETY", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "IMAGE_SAFETY",
	"IMAGE_PROHIBITED_CONTENT", "RECITATION", "IMAGE_RECITATION", "JAILBREAK", "MODEL_ARMOR",
}

func isSafetyReason(r string) bool { return slices.Contains(safetyReasons, r) }

func usageFrom(m *genai.GenerateContentResponseUsageMetadata) Usage {
	if m == nil {
		return Usage{}
	}
	u := Usage{
		PromptTokens:   int(m.PromptTokenCount),
		OutputTokens:   int(m.CandidatesTokenCount),
		ThoughtsTokens: int(m.ThoughtsTokenCount),
		TotalTokens:    int(m.TotalTokenCount),
		TrafficType:    string(m.TrafficType),
	}
	u.PromptByModality = modalities(m.PromptTokensDetails)
	u.OutputByModality = modalities(m.CandidatesTokensDetails)
	return u
}

func modalities(details []*genai.ModalityTokenCount) map[string]int {
	if len(details) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, d := range details {
		if d == nil {
			continue
		}
		out[strings.ToLower(string(d.Modality))] += int(d.TokenCount)
	}
	return out
}

// OperationError formats a long-running operation error map.
func OperationError(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	if msg, ok := m["message"].(string); ok && msg != "" {
		if code, ok := m["code"]; ok {
			return fmt.Sprintf("%s (code %v)", msg, code)
		}
		return msg
	}
	return fmt.Sprintf("%v", m)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
