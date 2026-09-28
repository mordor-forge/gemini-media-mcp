package config

import (
	"errors"
	"fmt"
	"strings"
)

// AuthMode describes how requests are authenticated.
type AuthMode string

// Supported authentication modes.
const (
	// AuthAPIKey uses a Gemini Developer API key (AI Studio).
	AuthAPIKey AuthMode = "gemini-api-key"
	// AuthVertexADC uses Application Default Credentials against a Vertex AI /
	// Gemini Enterprise Agent Platform project.
	AuthVertexADC AuthMode = "vertex-adc"
	// AuthVertexExpress uses a Vertex AI express-mode API key (no project).
	AuthVertexExpress AuthMode = "vertex-express"
	// AuthUnconfigured means no usable credentials: the server still starts so
	// agents can read get_config and relay setup instructions to the user.
	AuthUnconfigured AuthMode = "unconfigured"
)

// Unconfigured returns a placeholder Auth carrying the setup error.
func Unconfigured(err error) *Auth {
	return &Auth{Backend: BackendAuto, Mode: AuthUnconfigured, Reason: err.Error()}
}

// DefaultVertexLocation is used for models whose catalog entry does not name
// a preferred location and no location was configured. It matches the Google
// SDK default; regional-only models (Veo: us-central1) carry their location
// in the catalog.
const DefaultVertexLocation = "global"

// Auth is the resolved backend and credential selection.
type Auth struct {
	Backend  Backend  `json:"backend"`
	Mode     AuthMode `json:"mode"`
	APIKey   string   `json:"-"`
	Project  string   `json:"project,omitempty"`
	Location string   `json:"location,omitempty"`
	// LocationExplicit is true when the user configured a location.
	LocationExplicit bool `json:"locationExplicit,omitempty"`
	// Reason explains, in plain words, why this backend was chosen.
	Reason string `json:"reason"`
}

// ResolveAuth decides the backend from the merged config plus the Google SDK
// switches GOOGLE_GENAI_USE_VERTEXAI / GOOGLE_GENAI_USE_ENTERPRISE.
//
// Precedence:
//  1. An explicit backend (config file, GEMINI_MEDIA_BACKEND or --backend).
//  2. GOOGLE_GENAI_USE_ENTERPRISE, then GOOGLE_GENAI_USE_VERTEXAI (same semantics as the SDKs).
//  3. An API key selects the Gemini API. This deliberately beats a
//     GOOGLE_CLOUD_PROJECT that leaked in from the shell for other tools.
//  4. A project selects Vertex AI with Application Default Credentials.
func ResolveAuth(c *Config, env Env) (*Auth, error) {
	if env == nil {
		env = OSEnv
	}
	backend := c.Backend
	reason := ""
	switch backend {
	case BackendGeminiAPI, BackendVertex:
		reason = fmt.Sprintf("backend %q set explicitly (%s)", backend, sourceOr(c, "backend", "config"))
	default:
		if v, ok := sdkSwitch(env, "GOOGLE_GENAI_USE_ENTERPRISE"); ok {
			backend, reason = pick(v, "GOOGLE_GENAI_USE_ENTERPRISE")
		} else if v, ok := sdkSwitch(env, "GOOGLE_GENAI_USE_VERTEXAI"); ok {
			backend, reason = pick(v, "GOOGLE_GENAI_USE_VERTEXAI")
		} else if c.APIKey != "" {
			backend, reason = BackendGeminiAPI, "an API key is configured"
			if c.Project != "" {
				reason += " (GOOGLE_CLOUD_PROJECT is ignored; set GEMINI_MEDIA_BACKEND=vertex to use Vertex AI)"
			}
		} else if c.Project != "" {
			backend, reason = BackendVertex, "GOOGLE_CLOUD_PROJECT is set and no API key is configured"
		} else {
			return nil, errors.New(noCredentialsHelp)
		}
	}

	a := &Auth{Backend: backend, Reason: reason}
	switch backend {
	case BackendGeminiAPI:
		if c.APIKey == "" {
			return nil, errors.New("the Gemini API backend needs an API key: set GEMINI_API_KEY (or GOOGLE_API_KEY). Get one at https://aistudio.google.com/apikey")
		}
		a.Mode, a.APIKey = AuthAPIKey, c.APIKey
	case BackendVertex:
		switch {
		case c.Project != "":
			a.Mode, a.Project = AuthVertexADC, c.Project
			a.Location = c.Location
			a.LocationExplicit = c.Location != ""
			if a.Location == "" {
				a.Location = DefaultVertexLocation
			}
		case c.APIKey != "":
			// Express mode: API key bound to Vertex, no project/location.
			a.Mode, a.APIKey = AuthVertexExpress, c.APIKey
		default:
			return nil, errors.New("the Vertex AI backend needs GOOGLE_CLOUD_PROJECT (with Application Default Credentials, e.g. `gcloud auth application-default login`) or a Vertex AI express-mode API key in GOOGLE_API_KEY")
		}
	}
	return a, nil
}

const noCredentialsHelp = `no Google credentials configured. Choose one:
  - Gemini API (simplest): export GEMINI_API_KEY=... (https://aistudio.google.com/apikey)
  - Vertex AI: export GOOGLE_CLOUD_PROJECT=... and run 'gcloud auth application-default login'
  - Vertex AI express mode: export GOOGLE_GENAI_USE_VERTEXAI=true GOOGLE_API_KEY=...
Run 'gemini-media-mcp doctor' to check your setup`

func sdkSwitch(env Env, key string) (bool, bool) {
	v, ok := env(key)
	if !ok || strings.TrimSpace(v) == "" {
		return false, false
	}
	b, err := parseBool(v)
	if err != nil {
		return false, false
	}
	return b, true
}

func pick(vertex bool, key string) (Backend, string) {
	if vertex {
		return BackendVertex, key + " is true"
	}
	return BackendGeminiAPI, key + " is false"
}

func sourceOr(c *Config, key, fallback string) string {
	if s, ok := c.Sources[key]; ok {
		return s
	}
	return fallback
}
