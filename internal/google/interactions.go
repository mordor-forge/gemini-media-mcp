package google

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/apierrors"
	"google.golang.org/genai/interactions/models/components"
	ix "google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
	"google.golang.org/genai/interactions/retry"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
)

// MediaPart is an inline image or video sent with an interaction.
type MediaPart struct {
	Kind     string // "image" or "video"
	Data     []byte
	MIMEType string
}

// InteractionRequest is one video interaction with a Gemini Omni model:
// a generation, or an edit or extension of an earlier interaction.
type InteractionRequest struct {
	Model  string
	Prompt string
	// Media is sent before the prompt, in order (first frame, last frame,
	// reference images, then videos).
	Media []MediaPart
	// PreviousID continues a stored interaction (conversational edit or
	// extension of its video).
	PreviousID      string
	AspectRatio     string
	Resolution      string
	DurationSeconds int
	// Task optionally pins video_config.task (text_to_video, image_to_video,
	// reference_to_video, edit, extend); the model infers it otherwise.
	Task string
}

// InteractionResult is a finished interaction.
type InteractionResult struct {
	ID     string
	Status string
	// Video is the generated clip: a Files API URI (delivery "uri") or bytes.
	Video *genai.Video
	Text  string
	Usage Usage
	// Error explains a failed or incomplete interaction.
	Error string
}

// CreateInteraction implements API. Omni interactions are synchronous: the
// call returns when the video exists, which can take several minutes, so
// timeout replaces the client's request timeout for this call.
//
// Generation is billed and not idempotent, so the SDK's default retries
// (408, 409, 5xx and connection errors) are disabled; only 429 and 503,
// which mean the request was not processed, are retried here.
func (p *Pool) CreateInteraction(ctx context.Context, req *InteractionRequest, timeout time.Duration) (*InteractionResult, error) {
	if p.auth.Backend == config.BackendVertex {
		return nil, fmt.Errorf("the Interactions API is not supported on Vertex AI by this server yet")
	}
	c, err := p.client(ctx, "")
	if err != nil {
		return nil, err
	}
	body := operations.NewCreateInteractionRequestBody(interactionBody(req))
	opts := []operations.Option{operations.WithRetries(retry.Config{Strategy: "none"})}
	if timeout > 0 {
		opts = append(opts, operations.WithOperationTimeout(timeout))
	}
	attempts := max(p.opts.Retry.Attempts, 1)
	for attempt := 1; ; attempt++ {
		res, err := c.Interactions.Create(ctx, operations.CreateInteractionRequest{Body: body}, opts...)
		if err == nil {
			if res == nil || res.Interaction == nil {
				return nil, fmt.Errorf("the API returned no interaction")
			}
			return parseInteraction(res.Interaction), nil
		}
		err = interactionError(err)
		var apiErr genai.APIError
		if attempt >= attempts || !errors.As(err, &apiErr) || (apiErr.Code != 429 && apiErr.Code != 503) {
			return nil, err
		}
		delay := time.Duration(p.opts.Retry.InitialDelaySeconds * math.Pow(2, float64(attempt-1)) * float64(time.Second))
		if limit := time.Duration(p.opts.Retry.MaxDelaySeconds * float64(time.Second)); limit > 0 {
			delay = min(delay, limit)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func interactionBody(req *InteractionRequest) ix.CreateModelInteraction {
	var parts []ix.Content
	for _, m := range req.Media {
		data := base64.StdEncoding.EncodeToString(m.Data)
		switch m.Kind {
		case "video":
			mt := ix.VideoContentMimeType(m.MIMEType)
			parts = append(parts, ix.NewContent(ix.VideoContent{Data: &data, MimeType: &mt}))
		default:
			mt := ix.ImageContentMimeType(m.MIMEType)
			parts = append(parts, ix.NewContent(ix.ImageContent{Data: &data, MimeType: &mt}))
		}
	}
	parts = append(parts, ix.NewContent(ix.TextContent{Text: req.Prompt}))

	// URI delivery keeps multi-megabyte videos out of the response body; the
	// file is downloaded through the Files API like Veo output.
	delivery := ix.VideoResponseFormatDeliveryURI
	format := ix.VideoResponseFormat{Delivery: &delivery}
	if req.AspectRatio != "" {
		ar := ix.VideoResponseFormatAspectRatio(req.AspectRatio)
		format.AspectRatio = &ar
	}
	if req.Resolution != "" {
		r := ix.Resolution(strings.ToLower(req.Resolution))
		format.Resolution = &r
	}
	if req.DurationSeconds > 0 {
		d := fmt.Sprintf("%ds", req.DurationSeconds)
		format.Duration = &d
	}
	rf := ix.NewCreateModelInteractionResponseFormat(ix.NewResponseFormat(format))
	input := ix.NewInteractionsInput(parts)
	body := ix.CreateModelInteraction{Model: ix.Model(req.Model), Input: &input, ResponseFormat: &rf}
	if req.PreviousID != "" {
		body.PreviousInteractionID = &req.PreviousID
	}
	if req.Task != "" {
		task := ix.Task(req.Task)
		body.GenerationConfig = &ix.GenerationConfig{VideoConfig: &ix.VideoConfig{Task: &task}}
	}
	return body
}

func parseInteraction(in *ix.Interaction) *InteractionResult {
	r := &InteractionResult{Status: string(in.Status)}
	if in.ID != nil {
		r.ID = *in.ID
	}
	// output_video is an SDK convenience; the REST payload carries the video
	// in the model_output steps, so read both.
	video := in.OutputVideo
	var texts, errs []string
	for _, st := range in.Steps {
		mo := st.ModelOutputStep
		if mo == nil {
			continue
		}
		for _, c := range mo.Content {
			switch {
			case c.VideoContent != nil:
				video = c.VideoContent
			case c.TextContent != nil && strings.TrimSpace(c.TextContent.Text) != "":
				texts = append(texts, strings.TrimSpace(c.TextContent.Text))
			}
		}
		if mo.Error != nil && mo.Error.Message != nil {
			errs = append(errs, *mo.Error.Message)
		}
	}
	if len(texts) == 0 && in.OutputText != nil {
		texts = append(texts, strings.TrimSpace(*in.OutputText))
	}
	r.Text = strings.Join(texts, "\n")
	for _, e := range in.Errors {
		if e.Message != nil {
			errs = append(errs, *e.Message)
		}
	}
	r.Error = strings.Join(errs, "; ")
	if video != nil {
		v := &genai.Video{MIMEType: "video/mp4"}
		if video.MimeType != nil && *video.MimeType != "" {
			v.MIMEType = string(*video.MimeType)
		}
		if video.URI != nil {
			v.URI = *video.URI
		}
		if video.Data != nil {
			if b, err := base64.StdEncoding.DecodeString(*video.Data); err == nil {
				v.VideoBytes = b
			}
		}
		if v.URI != "" || len(v.VideoBytes) > 0 {
			r.Video = v
		}
	}
	if u := in.Usage; u != nil {
		r.Usage = Usage{
			PromptTokens:     deref(u.TotalInputTokens),
			OutputTokens:     deref(u.TotalOutputTokens),
			ThoughtsTokens:   deref(u.TotalThoughtTokens),
			TotalTokens:      deref(u.TotalTokens),
			PromptByModality: modalityTokens(u.InputTokensByModality),
			OutputByModality: modalityTokens(u.OutputTokensByModality),
		}
	}
	return r
}

func modalityTokens(ms []ix.ModalityTokens) map[string]int {
	if len(ms) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, m := range ms {
		if m.Modality != nil {
			out[strings.ToLower(string(*m.Modality))] += deref(m.Tokens)
		}
	}
	return out
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// interactionError converts Interactions SDK errors into genai.APIError, so
// Classify treats them like every other Gemini API error.
func interactionError(err error) error {
	var (
		clientErr *apierrors.CreateInteractionClientError
		serverErr *apierrors.CreateInteractionServerError
		apiErr    *apierrors.APIError
	)
	switch {
	case errors.As(err, &clientErr):
		return toAPIError(clientErr.Error_, clientErr.HTTPMeta, err)
	case errors.As(err, &serverErr):
		return toAPIError(serverErr.Error_, serverErr.HTTPMeta, err)
	case errors.As(err, &apiErr):
		msg := strings.TrimSpace(apiErr.Message)
		if body := strings.TrimSpace(apiErr.Body); body != "" {
			msg += ": " + body
		}
		return genai.APIError{Code: apiErr.StatusCode, Message: msg}
	}
	return err
}

func toAPIError(e ix.Error, meta components.HTTPMetadata, cause error) error {
	out := genai.APIError{Message: cause.Error()}
	if e.Message != nil {
		out.Message = *e.Message
	}
	if e.Code != nil {
		out.Status = *e.Code
	}
	if meta.Response != nil {
		out.Code = meta.Response.StatusCode
	}
	return out
}
