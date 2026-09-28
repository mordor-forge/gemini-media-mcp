// Package config loads the server configuration from layered sources:
// built-in defaults < config file < environment variables < command-line flags.
//
// Every layer is optional. Unknown keys in the config file produce warnings
// instead of hard failures so that an older binary keeps working with a newer
// config file (and vice versa).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Backend identifies which Google API surface serves requests.
type Backend string

// Supported backends. BackendAuto resolves to one of the concrete backends.
const (
	BackendAuto      Backend = "auto"
	BackendGeminiAPI Backend = "gemini-api"
	BackendVertex    Backend = "vertex"
)

// Transport names.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// Config is the fully merged configuration.
type Config struct {
	Backend  Backend `yaml:"backend" json:"backend"`
	APIKey   string  `yaml:"apiKey" json:"-"`
	Project  string  `yaml:"project" json:"project,omitempty"`
	Location string  `yaml:"location" json:"location,omitempty"`

	OutputDir   string   `yaml:"outputDir" json:"outputDir"`
	StateDir    string   `yaml:"stateDir" json:"stateDir"`
	CatalogFile string   `yaml:"catalogFile" json:"catalogFile,omitempty"`
	InputDirs   []string `yaml:"inputDirs" json:"inputDirs,omitempty"`
	// AllowAnyInputPath permits reading input media from anywhere on disk.
	// Nil means "decide by transport": true for stdio, false for HTTP.
	AllowAnyInputPath *bool `yaml:"allowAnyInputPath" json:"allowAnyInputPath,omitempty"`

	InlinePreviews   *bool `yaml:"inlinePreviews" json:"inlinePreviews,omitempty"`
	PreviewMaxPixels int   `yaml:"previewMaxPixels" json:"previewMaxPixels"`

	Defaults ModelDefaults `yaml:"defaults" json:"defaults"`
	Budget   Budget        `yaml:"budget" json:"budget"`
	HTTP     HTTP          `yaml:"http" json:"http"`
	Retry    Retry         `yaml:"retry" json:"retry"`

	Transport             string `yaml:"transport" json:"transport"`
	RequestTimeoutSeconds int    `yaml:"requestTimeoutSeconds" json:"requestTimeoutSeconds"`
	MaxVideoWaitSeconds   int    `yaml:"maxVideoWaitSeconds" json:"maxVideoWaitSeconds"`
	LogLevel              string `yaml:"logLevel" json:"logLevel"`

	// ConfigFile is the path of the file that was loaded, if any.
	ConfigFile string `yaml:"-" json:"configFile,omitempty"`
	// Warnings collects non-fatal problems found while loading.
	Warnings []string `yaml:"-" json:"warnings,omitempty"`
	// Sources records where selected settings came from (default/file/env/flag).
	Sources map[string]string `yaml:"-" json:"sources,omitempty"`
}

// ModelDefaults selects the model (alias or raw ID) used when a tool call omits one.
type ModelDefaults struct {
	Image  string `yaml:"image" json:"image,omitempty"`
	Video  string `yaml:"video" json:"video,omitempty"`
	Speech string `yaml:"speech" json:"speech,omitempty"`
	Music  string `yaml:"music" json:"music,omitempty"`
	Voice  string `yaml:"voice" json:"voice,omitempty"`
}

// Budget caps estimated spend. Zero disables a cap.
type Budget struct {
	SessionUSD      float64 `yaml:"sessionUsd" json:"sessionUsd,omitempty"`
	DailyUSD        float64 `yaml:"dailyUsd" json:"dailyUsd,omitempty"`
	MonthlyUSD      float64 `yaml:"monthlyUsd" json:"monthlyUsd,omitempty"`
	ConfirmAboveUSD float64 `yaml:"confirmAboveUsd" json:"confirmAboveUsd,omitempty"`
}

// HTTP configures the Streamable HTTP transport.
type HTTP struct {
	Addr           string   `yaml:"addr" json:"addr"`
	Path           string   `yaml:"path" json:"path"`
	AuthToken      string   `yaml:"authToken" json:"-"`
	AllowedOrigins []string `yaml:"allowedOrigins" json:"allowedOrigins,omitempty"`
	// Stateful keeps per-client sessions (legacy 2025-11-25 clients only).
	// The default stateless mode is required for protocol 2026-07-28.
	Stateful bool `yaml:"stateful" json:"stateful,omitempty"`
}

// Retry configures SDK-level retries for transient API errors (429/5xx).
type Retry struct {
	Attempts            int     `yaml:"attempts" json:"attempts"`
	InitialDelaySeconds float64 `yaml:"initialDelaySeconds" json:"initialDelaySeconds"`
	MaxDelaySeconds     float64 `yaml:"maxDelaySeconds" json:"maxDelaySeconds"`
}

// Flags carries command-line overrides; empty values mean "not set".
type Flags struct {
	ConfigFile string
	Transport  string
	HTTPAddr   string
	OutputDir  string
	Backend    string
	LogLevel   string
}

// Env abstracts environment lookup so loading is testable.
type Env func(key string) (string, bool)

// OSEnv reads the process environment.
func OSEnv(key string) (string, bool) { return os.LookupEnv(key) }

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Backend:          BackendAuto,
		OutputDir:        defaultOutputDir(),
		StateDir:         defaultStateDir(OSEnv),
		PreviewMaxPixels: 768,
		HTTP: HTTP{
			Addr: "127.0.0.1:8765",
			Path: "/mcp",
		},
		Retry: Retry{
			Attempts:            4,
			InitialDelaySeconds: 1,
			MaxDelaySeconds:     30,
		},
		Transport:             TransportStdio,
		RequestTimeoutSeconds: 600,
		MaxVideoWaitSeconds:   600,
		LogLevel:              "info",
		Sources:               map[string]string{},
	}
}

// Load merges all configuration layers. It does not touch the filesystem
// beyond reading the config file; call Prepare to create directories.
func Load(flags Flags, env Env) (*Config, error) {
	if env == nil {
		env = OSEnv
	}
	cfg := Default()
	cfg.StateDir = defaultStateDir(env)

	path, explicit := configFilePath(flags, env)
	if path != "" {
		found, err := cfg.mergeFile(path)
		switch {
		case err != nil:
			return nil, err
		case !found && explicit:
			return nil, fmt.Errorf("config file %s does not exist", path)
		case found:
			cfg.ConfigFile = path
		}
	}

	if err := cfg.mergeEnv(env); err != nil {
		return nil, err
	}
	cfg.mergeFlags(flags)
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Prepare creates the output and state directories.
func (c *Config) Prepare() error {
	for _, dir := range []string{c.OutputDir, c.StateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}
	return nil
}

// RequestTimeout returns the per-API-request timeout.
func (c *Config) RequestTimeout() time.Duration {
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}

// InlinePreviewsEnabled reports whether tool results embed downscaled image previews.
func (c *Config) InlinePreviewsEnabled() bool {
	return c.InlinePreviews == nil || *c.InlinePreviews
}

// AnyInputPathAllowed reports whether input media may be read from arbitrary paths.
func (c *Config) AnyInputPathAllowed() bool {
	if c.AllowAnyInputPath != nil {
		return *c.AllowAnyInputPath
	}
	return c.Transport != TransportHTTP
}

func (c *Config) setSource(key, source string) {
	if c.Sources == nil {
		c.Sources = map[string]string{}
	}
	c.Sources[key] = source
}

func (c *Config) warnf(format string, args ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

func configFilePath(flags Flags, env Env) (string, bool) {
	if flags.ConfigFile != "" {
		return expandHome(flags.ConfigFile), true
	}
	if v, ok := env("GEMINI_MEDIA_CONFIG"); ok && v != "" {
		return expandHome(v), true
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, "gemini-media-mcp", "config.yaml"), false
}

// mergeEnv applies environment variables. Both the Google SDK conventions and
// GEMINI_MEDIA_* names are honored so existing MCP client configs keep working.
func (c *Config) mergeEnv(env Env) error {
	str := func(target *string, key string, keys ...string) {
		for _, k := range keys {
			if v, ok := env(k); ok && strings.TrimSpace(v) != "" {
				*target = strings.TrimSpace(v)
				c.setSource(key, "env:"+k)
				return
			}
		}
	}
	var errs []error
	num := func(target *float64, key, envKey string) {
		v, ok := env(envKey)
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 {
			errs = append(errs, fmt.Errorf("%s=%q: expected a non-negative number", envKey, v))
			return
		}
		*target = f
		c.setSource(key, "env:"+envKey)
	}
	integer := func(target *int, key, envKey string) {
		v, ok := env(envKey)
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s=%q: expected a non-negative integer", envKey, v))
			return
		}
		*target = n
		c.setSource(key, "env:"+envKey)
	}
	boolean := func(target **bool, key, envKey string) {
		v, ok := env(envKey)
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		b, err := parseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s=%q: %w", envKey, v, err))
			return
		}
		*target = &b
		c.setSource(key, "env:"+envKey)
	}

	var backend string
	str(&backend, "backend", "GEMINI_MEDIA_BACKEND")
	if backend != "" {
		c.Backend = Backend(strings.ToLower(backend))
	}
	// GEMINI_MEDIA_API_KEY lets plugin managers inject a key without
	// shadowing the user's shell variables. Otherwise mirror the Google SDKs,
	// which prefer GOOGLE_API_KEY when both are set.
	str(&c.APIKey, "apiKey", "GEMINI_MEDIA_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY")
	str(&c.Project, "project", "GEMINI_MEDIA_PROJECT", "GOOGLE_CLOUD_PROJECT")
	str(&c.Location, "location", "GEMINI_MEDIA_LOCATION", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_REGION")
	str(&c.OutputDir, "outputDir", "GEMINI_MEDIA_OUTPUT_DIR", "MEDIA_OUTPUT_DIR")
	str(&c.StateDir, "stateDir", "GEMINI_MEDIA_STATE_DIR")
	str(&c.CatalogFile, "catalogFile", "GEMINI_MEDIA_CATALOG")
	str(&c.Transport, "transport", "GEMINI_MEDIA_TRANSPORT")
	str(&c.HTTP.Addr, "http.addr", "GEMINI_MEDIA_HTTP_ADDR")
	str(&c.HTTP.AuthToken, "http.authToken", "GEMINI_MEDIA_HTTP_TOKEN")
	str(&c.LogLevel, "logLevel", "GEMINI_MEDIA_LOG_LEVEL")
	str(&c.Defaults.Image, "defaults.image", "GEMINI_MEDIA_IMAGE_MODEL")
	str(&c.Defaults.Video, "defaults.video", "GEMINI_MEDIA_VIDEO_MODEL")
	str(&c.Defaults.Speech, "defaults.speech", "GEMINI_MEDIA_SPEECH_MODEL")
	str(&c.Defaults.Music, "defaults.music", "GEMINI_MEDIA_MUSIC_MODEL")
	str(&c.Defaults.Voice, "defaults.voice", "GEMINI_MEDIA_VOICE")

	var inputDirs string
	str(&inputDirs, "inputDirs", "GEMINI_MEDIA_INPUT_DIRS")
	if inputDirs != "" {
		c.InputDirs = filepath.SplitList(inputDirs)
	}
	var origins string
	str(&origins, "http.allowedOrigins", "GEMINI_MEDIA_HTTP_ALLOWED_ORIGINS")
	if origins != "" {
		c.HTTP.AllowedOrigins = splitCSV(origins)
	}

	num(&c.Budget.SessionUSD, "budget.sessionUsd", "GEMINI_MEDIA_BUDGET_SESSION_USD")
	num(&c.Budget.DailyUSD, "budget.dailyUsd", "GEMINI_MEDIA_BUDGET_DAILY_USD")
	num(&c.Budget.MonthlyUSD, "budget.monthlyUsd", "GEMINI_MEDIA_BUDGET_MONTHLY_USD")
	num(&c.Budget.ConfirmAboveUSD, "budget.confirmAboveUsd", "GEMINI_MEDIA_CONFIRM_ABOVE_USD")
	integer(&c.RequestTimeoutSeconds, "requestTimeoutSeconds", "GEMINI_MEDIA_REQUEST_TIMEOUT_SECONDS")
	integer(&c.MaxVideoWaitSeconds, "maxVideoWaitSeconds", "GEMINI_MEDIA_MAX_VIDEO_WAIT_SECONDS")
	integer(&c.Retry.Attempts, "retry.attempts", "GEMINI_MEDIA_RETRY_ATTEMPTS")
	integer(&c.PreviewMaxPixels, "previewMaxPixels", "GEMINI_MEDIA_PREVIEW_MAX_PIXELS")
	boolean(&c.InlinePreviews, "inlinePreviews", "GEMINI_MEDIA_INLINE_PREVIEWS")
	boolean(&c.AllowAnyInputPath, "allowAnyInputPath", "GEMINI_MEDIA_ALLOW_ANY_INPUT_PATH")

	var stateful *bool
	boolean(&stateful, "http.stateful", "GEMINI_MEDIA_HTTP_STATEFUL")
	if stateful != nil {
		c.HTTP.Stateful = *stateful
	}

	return errors.Join(errs...)
}

func (c *Config) mergeFlags(f Flags) {
	set := func(target *string, key, v string) {
		if v != "" {
			*target = v
			c.setSource(key, "flag")
		}
	}
	set(&c.Transport, "transport", f.Transport)
	set(&c.HTTP.Addr, "http.addr", f.HTTPAddr)
	set(&c.OutputDir, "outputDir", f.OutputDir)
	set(&c.LogLevel, "logLevel", f.LogLevel)
	if f.Backend != "" {
		c.Backend = Backend(strings.ToLower(f.Backend))
		c.setSource("backend", "flag")
	}
}

func (c *Config) normalize() {
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	if c.Backend == "" {
		c.Backend = BackendAuto
	}
	// Accept a few common spellings so configs survive renames.
	switch c.Backend {
	case "gemini", "geminiapi", "google-ai", "mldev", "ai-studio":
		c.Backend = BackendGeminiAPI
	case "vertexai", "vertex-ai", "enterprise", "gemini-enterprise":
		c.Backend = BackendVertex
	}
	c.OutputDir = expandHome(c.OutputDir)
	c.StateDir = expandHome(c.StateDir)
	c.CatalogFile = expandHome(c.CatalogFile)
	for i, d := range c.InputDirs {
		c.InputDirs[i] = expandHome(strings.TrimSpace(d))
	}
	c.InputDirs = slices.DeleteFunc(c.InputDirs, func(s string) bool { return s == "" })
	if c.HTTP.Path == "" {
		c.HTTP.Path = "/mcp"
	}
	if !strings.HasPrefix(c.HTTP.Path, "/") {
		c.HTTP.Path = "/" + c.HTTP.Path
	}
	if c.PreviewMaxPixels <= 0 {
		c.PreviewMaxPixels = 768
	}
	if c.Retry.Attempts < 1 {
		c.Retry.Attempts = 1
	}
}

func (c *Config) validate() error {
	var errs []error
	switch c.Backend {
	case BackendAuto, BackendGeminiAPI, BackendVertex:
	default:
		errs = append(errs, fmt.Errorf("unknown backend %q (use auto, gemini-api or vertex)", c.Backend))
	}
	switch c.Transport {
	case TransportStdio, TransportHTTP:
	default:
		errs = append(errs, fmt.Errorf("unknown transport %q (use stdio or http)", c.Transport))
	}
	if c.OutputDir == "" {
		errs = append(errs, errors.New("outputDir must not be empty"))
	}
	for _, v := range []float64{c.Budget.SessionUSD, c.Budget.DailyUSD, c.Budget.MonthlyUSD, c.Budget.ConfirmAboveUSD} {
		if v < 0 {
			errs = append(errs, errors.New("budget values must be non-negative"))
			break
		}
	}
	if c.Transport == TransportHTTP && c.HTTP.AuthToken == "" && !isLoopbackAddr(c.HTTP.Addr) {
		errs = append(errs, fmt.Errorf("refusing to serve HTTP on non-loopback address %s without an auth token; set GEMINI_MEDIA_HTTP_TOKEN", c.HTTP.Addr))
	}
	return errors.Join(errs...)
}

func isLoopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, errors.New("expected true/false")
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "generated_media"
	}
	return filepath.Join(home, "generated_media")
}

func defaultStateDir(env Env) string {
	if v, ok := env("XDG_STATE_HOME"); ok && v != "" {
		return filepath.Join(v, "gemini-media-mcp")
	}
	if runtime.GOOS == "linux" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "state", "gemini-media-mcp")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "gemini-media-mcp")
	}
	return filepath.Join(defaultOutputDir(), ".gemini-media-mcp")
}
