package google

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
)

// Classify converts SDK/transport errors into an *Error with a hint.
// model is included in hints when relevant; backend is "gemini-api" or "vertex".
func Classify(err error, op, model, backend string) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	prefix := op
	if model != "" {
		prefix = fmt.Sprintf("%s with %s", op, model)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &apperr.Error{Kind: apperr.Canceled, Message: prefix + ": request cancelled", Cause: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &apperr.Error{Kind: apperr.Timeout, Message: prefix + ": timed out", Hint: "Retry, or raise requestTimeoutSeconds. Large 4K images and Pro models can take minutes.", Retryable: true, Cause: err}
	}

	var apiErr genai.APIError
	var apiErrPtr *genai.APIError
	switch {
	case errors.As(err, &apiErr):
	case errors.As(err, &apiErrPtr) && apiErrPtr != nil:
		apiErr = *apiErrPtr
	default:
		return &apperr.Error{Kind: apperr.Unknown, Message: fmt.Sprintf("%s: %v", prefix, err), Cause: err}
	}

	msg := fmt.Sprintf("%s failed (HTTP %d %s): %s", prefix, apiErr.Code, apiErr.Status, strings.TrimSpace(apiErr.Message))
	e := &apperr.Error{Message: msg, Status: apiErr.Code, Cause: err}
	lower := strings.ToLower(apiErr.Message)
	switch {
	case apiErr.Code == 401 || strings.Contains(lower, "api key not valid") || strings.Contains(lower, "api_key_invalid"):
		e.Kind = apperr.Auth
		e.Hint = "The credentials were rejected. Check GEMINI_API_KEY/GOOGLE_API_KEY, or for Vertex AI run `gcloud auth application-default login`. Run `gemini-media-mcp doctor` to diagnose."
	case apiErr.Code == 403:
		e.Kind = apperr.Permission
		if backend == "vertex" {
			e.Hint = "Enable the Vertex AI API (aiplatform.googleapis.com) for the project, check billing, and grant roles/aiplatform.user to the calling identity."
		} else {
			e.Hint = "The key lacks access to this model. Some media models need a paid (billing-enabled) Gemini API project; check https://aistudio.google.com/ usage tier and key restrictions."
		}
	case apiErr.Code == 429:
		e.Kind, e.Retryable = apperr.Quota, true
		e.Hint = "Rate limit or quota exhausted. Wait before retrying (the server already retried with backoff), reduce parallel requests, or request a quota increase."
	case apiErr.Code == 404:
		e.Kind = apperr.NotFound
		e.Hint = fmt.Sprintf("Model %q was not found. It may have been renamed or retired, or it is not offered in this region/backend. Call list_models (live: true) to see what is available, or pass a different model.", model)
	case apiErr.Code == 400 && (strings.Contains(lower, "safety") || strings.Contains(lower, "blocked") || strings.Contains(lower, "responsible ai") || strings.Contains(lower, "prohibited")):
		e.Kind = apperr.Safety
		e.Hint = "The prompt or input media was blocked by safety filters. Rephrase it (avoid real people, brands, violence, or copyrighted characters) and try again."
	case apiErr.Code == 400 && strings.Contains(lower, "location") && strings.Contains(lower, "not supported"):
		e.Kind = apperr.NotFound
		e.Hint = "This model is not served from the configured Vertex AI location. Set a per-model location in the catalog override or GOOGLE_CLOUD_LOCATION (many preview models require 'global')."
	case apiErr.Code == 400:
		e.Kind = apperr.Invalid
		e.Hint = "The API rejected the parameters. Check list_models for the model's supported values."
	case apiErr.Code == 408 || apiErr.Code == 504:
		e.Kind, e.Retryable = apperr.Timeout, true
		e.Hint = "The request timed out upstream. Retry; if it persists use a smaller size/resolution."
	case apiErr.Code >= 500:
		e.Kind, e.Retryable = apperr.Unavailable, true
		e.Hint = "Google's service had a transient error. Retry in a few seconds."
	default:
		e.Kind = apperr.Unknown
	}
	return e
}
