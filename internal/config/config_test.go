package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mapEnv(m map[string]string) Env {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// noFile points the default config path at an empty temp dir so tests never
// read the developer's real config.
func noFile(t *testing.T) Flags {
	t.Helper()
	isolate(t)
	return Flags{}
}

// isolate redirects every OS-specific user config/home location.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
}

func load(t *testing.T, flags Flags, env map[string]string) *Config {
	t.Helper()
	if _, ok := env["GEMINI_MEDIA_CONFIG"]; !ok && flags.ConfigFile == "" {
		// Isolate from any real user config file.
		isolate(t)
	}
	cfg, err := Load(flags, mapEnv(env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestLoadDefaults(t *testing.T) {
	cfg := load(t, noFile(t), map[string]string{})
	if cfg.Backend != BackendAuto || cfg.Transport != TransportStdio {
		t.Fatalf("unexpected defaults: backend=%s transport=%s", cfg.Backend, cfg.Transport)
	}
	if cfg.HTTP.Addr != "127.0.0.1:8765" || cfg.HTTP.Path != "/mcp" {
		t.Fatalf("unexpected HTTP defaults: %+v", cfg.HTTP)
	}
	if !cfg.InlinePreviewsEnabled() || !cfg.AnyInputPathAllowed() {
		t.Fatal("stdio defaults should enable previews and unrestricted input paths")
	}
}

func TestLoadEnvAliases(t *testing.T) {
	cfg := load(t, noFile(t), map[string]string{
		"GEMINI_API_KEY":                    "gem",
		"GOOGLE_CLOUD_REGION":               "europe-west4",
		"MEDIA_OUTPUT_DIR":                  "/tmp/out",
		"GEMINI_MEDIA_BUDGET_DAILY_USD":     "5.5",
		"GEMINI_MEDIA_INLINE_PREVIEWS":      "false",
		"GEMINI_MEDIA_INPUT_DIRS":           "/a" + string(os.PathListSeparator) + "/b",
		"GEMINI_MEDIA_IMAGE_MODEL":          "pro",
		"GEMINI_MEDIA_HTTP_ALLOWED_ORIGINS": "https://a.example, https://b.example",
	})
	if cfg.APIKey != "gem" || cfg.Location != "europe-west4" || cfg.OutputDir != "/tmp/out" {
		t.Fatalf("env aliases not applied: %+v", cfg)
	}
	if cfg.Budget.DailyUSD != 5.5 || cfg.InlinePreviewsEnabled() {
		t.Fatalf("typed env values not applied: %+v", cfg.Budget)
	}
	if len(cfg.InputDirs) != 2 || cfg.Defaults.Image != "pro" || len(cfg.HTTP.AllowedOrigins) != 2 {
		t.Fatalf("list env values not applied: %+v %+v", cfg.InputDirs, cfg.HTTP.AllowedOrigins)
	}
	if cfg.Sources["location"] != "env:GOOGLE_CLOUD_REGION" {
		t.Fatalf("source not tracked: %v", cfg.Sources)
	}
}

func TestGoogleAPIKeyPreferredOverGeminiAPIKey(t *testing.T) {
	cfg := load(t, noFile(t), map[string]string{"GOOGLE_API_KEY": "g", "GEMINI_API_KEY": "m"})
	if cfg.APIKey != "g" {
		t.Fatalf("APIKey = %q, want GOOGLE_API_KEY value (matches Google SDK precedence)", cfg.APIKey)
	}
}

func TestLoadRejectsBadNumbers(t *testing.T) {
	isolate(t)
	_, err := Load(Flags{}, mapEnv(map[string]string{"GEMINI_MEDIA_BUDGET_DAILY_USD": "lots"}))
	if err == nil || !strings.Contains(err.Error(), "GEMINI_MEDIA_BUDGET_DAILY_USD") {
		t.Fatalf("expected a descriptive error, got %v", err)
	}
}

func TestLoadFileWithUnknownKeysWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
backend: vertex
project: file-project
futureOption: 42
budget:
  dailyUsd: 3
defaults:
  video: fast
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := load(t, Flags{ConfigFile: path}, map[string]string{"GOOGLE_CLOUD_PROJECT": "env-project"})
	if cfg.Backend != BackendVertex || cfg.Budget.DailyUSD != 3 || cfg.Defaults.Video != "fast" {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	if cfg.Project != "env-project" {
		t.Fatalf("env must override file, got %q", cfg.Project)
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(strings.Join(cfg.Warnings, "\n"), "futureOption") {
		t.Fatalf("expected a warning about the unknown key, got %v", cfg.Warnings)
	}
	if cfg.ConfigFile != path {
		t.Fatalf("ConfigFile = %q", cfg.ConfigFile)
	}
}

func TestExplicitMissingConfigFileFails(t *testing.T) {
	_, err := Load(Flags{ConfigFile: filepath.Join(t.TempDir(), "missing.yaml")}, mapEnv(nil))
	if err == nil {
		t.Fatal("expected error for a missing explicit config file")
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	cfg := load(t, Flags{Transport: "http", HTTPAddr: "127.0.0.1:9999"}, map[string]string{"GEMINI_MEDIA_TRANSPORT": "stdio"})
	if cfg.Transport != TransportHTTP || cfg.HTTP.Addr != "127.0.0.1:9999" {
		t.Fatalf("flags not applied: %s %s", cfg.Transport, cfg.HTTP.Addr)
	}
	if cfg.AnyInputPathAllowed() {
		t.Fatal("HTTP transport must restrict input paths by default")
	}
}

func TestHTTPOnPublicAddressRequiresToken(t *testing.T) {
	isolate(t)
	_, err := Load(Flags{Transport: "http", HTTPAddr: "0.0.0.0:8765"}, mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "GEMINI_MEDIA_HTTP_TOKEN") {
		t.Fatalf("expected auth token requirement, got %v", err)
	}
	cfg := load(t, Flags{Transport: "http", HTTPAddr: "0.0.0.0:8765"}, map[string]string{"GEMINI_MEDIA_HTTP_TOKEN": "s3cret"})
	if cfg.HTTP.AuthToken != "s3cret" {
		t.Fatal("token not loaded")
	}
}

func TestBackendSpellingsNormalize(t *testing.T) {
	for in, want := range map[string]Backend{"vertexai": BackendVertex, "gemini": BackendGeminiAPI, "enterprise": BackendVertex} {
		cfg := load(t, noFile(t), map[string]string{"GEMINI_MEDIA_BACKEND": in})
		if cfg.Backend != want {
			t.Errorf("backend %q normalized to %q, want %q", in, cfg.Backend, want)
		}
	}
}

func TestResolveAuth(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		env      map[string]string
		backend  Backend
		mode     AuthMode
		location string
		wantErr  string
	}{
		{name: "api key wins over leaked project", cfg: Config{APIKey: "k", Project: "p"}, backend: BackendGeminiAPI, mode: AuthAPIKey},
		{name: "project only uses ADC", cfg: Config{Project: "p"}, backend: BackendVertex, mode: AuthVertexADC, location: DefaultVertexLocation},
		{name: "explicit location kept", cfg: Config{Project: "p", Location: "global"}, backend: BackendVertex, mode: AuthVertexADC, location: "global"},
		{name: "sdk vertex switch with key is express mode", cfg: Config{APIKey: "k"}, env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"}, backend: BackendVertex, mode: AuthVertexExpress},
		{name: "sdk vertex switch with project uses ADC", cfg: Config{APIKey: "k", Project: "p"}, env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "1"}, backend: BackendVertex, mode: AuthVertexADC, location: DefaultVertexLocation},
		{name: "enterprise switch beats vertex switch", cfg: Config{APIKey: "k"}, env: map[string]string{"GOOGLE_GENAI_USE_ENTERPRISE": "false", "GOOGLE_GENAI_USE_VERTEXAI": "true"}, backend: BackendGeminiAPI, mode: AuthAPIKey},
		{name: "explicit backend beats sdk switch", cfg: Config{Backend: BackendGeminiAPI, APIKey: "k"}, env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"}, backend: BackendGeminiAPI, mode: AuthAPIKey},
		{name: "nothing configured", cfg: Config{}, wantErr: "no Google credentials"},
		{name: "gemini backend without key", cfg: Config{Backend: BackendGeminiAPI, Project: "p"}, wantErr: "needs an API key"},
		{name: "vertex without project or key", cfg: Config{Backend: BackendVertex}, wantErr: "needs GOOGLE_CLOUD_PROJECT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cfg.Backend == "" {
				tt.cfg.Backend = BackendAuto
			}
			a, err := ResolveAuth(&tt.cfg, mapEnv(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if a.Backend != tt.backend || a.Mode != tt.mode || a.Location != tt.location {
				t.Fatalf("got %+v, want backend=%s mode=%s location=%q", a, tt.backend, tt.mode, tt.location)
			}
			if a.Reason == "" {
				t.Fatal("reason must explain the selection")
			}
		})
	}
}
