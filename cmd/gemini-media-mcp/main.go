// Command gemini-media-mcp is an MCP server for Google's generative media
// models (Nano Banana images, Veo video, Gemini TTS, Lyria music).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/media"
	"github.com/mordor-forge/gemini-media-mcp/internal/server"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
	"github.com/mordor-forge/gemini-media-mcp/internal/version"
)

const usageText = `gemini-media-mcp - MCP server for Google generative media (images, video, speech, music)

Usage:
  gemini-media-mcp [serve] [flags]     Run the MCP server (default: stdio)
  gemini-media-mcp doctor [flags]      Check credentials, backend and model availability
  gemini-media-mcp configure [flags]   Write a config file (API key read from stdin)
  gemini-media-mcp models [flags]      Print the model catalog
  gemini-media-mcp usage [flags]       Print recorded spend
  gemini-media-mcp version             Print the version

Common flags:
  --config PATH       Config file (default: <user config dir>/gemini-media-mcp/config.yaml)
  --transport NAME    stdio (default) or http
  --http-addr ADDR    Listen address for http (default 127.0.0.1:8765)
  --output-dir DIR    Where generated media is saved (default ~/generated_media)
  --backend NAME      auto (default), gemini-api or vertex
  --log-level LEVEL   debug, info (default), warn or error

Environment: GEMINI_API_KEY / GOOGLE_API_KEY, GOOGLE_CLOUD_PROJECT, GOOGLE_CLOUD_LOCATION,
GOOGLE_GENAI_USE_VERTEXAI, MEDIA_OUTPUT_DIR and GEMINI_MEDIA_* (see README).
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gemini-media-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return cmdServe(args, stderr)
	case "doctor":
		return cmdDoctor(args, stdout, stderr)
	case "configure":
		return cmdConfigure(args, stdin, stdout)
	case "models":
		return cmdModels(args, stdout)
	case "usage":
		return cmdUsage(args, stdout)
	case "version", "--version":
		_, _ = fmt.Fprintf(stdout, "%s %s\n", version.Name, version.String())
		return nil
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usageText)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usageText)
}

// newFlagSet binds the common flags to f; call Parse on the returned set.
func newFlagSet(name string, f *config.Flags) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.ConfigFile, "config", "", "")
	fs.StringVar(&f.Transport, "transport", "", "")
	fs.StringVar(&f.HTTPAddr, "http-addr", "", "")
	fs.StringVar(&f.OutputDir, "output-dir", "", "")
	fs.StringVar(&f.Backend, "backend", "", "")
	fs.StringVar(&f.LogLevel, "log-level", "", "")
	return fs
}

func newLogger(level string, w io.Writer) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// app holds the composed application.
type app struct {
	cfg    *config.Config
	auth   *config.Auth
	svc    *media.Service
	store  *store.Store
	ledger *spend.Ledger
	log    *slog.Logger
}

func build(ctx context.Context, flags config.Flags, stderr io.Writer) (*app, error) {
	cfg, err := config.Load(flags, config.OSEnv)
	if err != nil {
		return nil, err
	}
	log := newLogger(cfg.LogLevel, stderr)
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}
	if err := cfg.Prepare(); err != nil {
		return nil, err
	}
	// Credential problems don't stop the server: tools report them to the
	// agent (with setup instructions) instead of the host showing a crash.
	var api google.API
	auth, err := config.ResolveAuth(cfg, config.OSEnv)
	if err == nil {
		var pool *google.Pool
		if pool, err = google.NewPool(ctx, auth, google.Options{Retry: cfg.Retry, RequestTimeout: cfg.RequestTimeout()}); err == nil {
			api = pool
		}
	}
	if err != nil {
		log.Error("no usable Google credentials; generation tools will report setup instructions", "err", err)
		auth, api = config.Unconfigured(err), google.Unconfigured{}
	}
	st, err := store.New(cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	ledger, err := spend.Open(filepath.Join(cfg.StateDir, "usage.jsonl"), spend.Budget{
		SessionUSD: cfg.Budget.SessionUSD, DailyUSD: cfg.Budget.DailyUSD,
		MonthlyUSD: cfg.Budget.MonthlyUSD, ConfirmAboveUSD: cfg.Budget.ConfirmAboveUSD,
	})
	if err != nil {
		return nil, err
	}
	reg, err := jobs.Open(filepath.Join(cfg.StateDir, "jobs"))
	if err != nil {
		return nil, err
	}
	src, err := catalog.NewSource(cfg.CatalogFile, map[string]string{
		catalog.Image: cfg.Defaults.Image, catalog.Video: cfg.Defaults.Video,
		catalog.Speech: cfg.Defaults.Speech, catalog.Music: cfg.Defaults.Music,
	}, log)
	if err != nil {
		return nil, err
	}
	svc := media.New(media.Deps{API: api, Auth: auth, Config: cfg, Catalog: src, Store: st, Jobs: reg, Ledger: ledger, Logger: log})
	log.Info("configured", "version", version.String(), "backend", auth.Backend, "auth", auth.Mode, "reason", auth.Reason, "output", st.Dir())
	return &app{cfg: cfg, auth: auth, svc: svc, store: st, ledger: ledger, log: log}, nil
}

func cmdServe(args []string, stderr io.Writer) error {
	var flags config.Flags
	if err := newFlagSet("serve", &flags).Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usageText)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := build(ctx, flags, stderr)
	if err != nil {
		return err
	}
	srv := server.New(a.svc, a.store, server.Options{Transport: a.cfg.Transport, Logger: a.log})
	if a.cfg.Transport == config.TransportHTTP {
		return srv.RunHTTP(ctx, a.cfg.HTTP)
	}
	err = srv.RunStdio(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	var flags config.Flags
	if err := newFlagSet("doctor", &flags).Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = fmt.Fprintf(stdout, "%s %s\n\n", version.Name, version.String())
	a, err := build(ctx, flags, io.Discard)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "FAIL configuration: %v\n", err)
		return errors.New("setup is incomplete")
	}
	if a.auth.Mode == config.AuthUnconfigured {
		_, _ = fmt.Fprintf(stdout, "FAIL credentials: %s\n", a.auth.Reason)
		return errors.New("setup is incomplete")
	}
	info := a.svc.Info(a.cfg.Transport)
	_, _ = fmt.Fprintf(stdout, "OK   backend: %s (%s) - %s\n", info.Backend, info.AuthMode, info.BackendReason)
	if info.ConfigFile != "" {
		_, _ = fmt.Fprintf(stdout, "OK   config file: %s\n", info.ConfigFile)
	}
	_, _ = fmt.Fprintf(stdout, "OK   output directory: %s\n", info.OutputDir)
	_, _ = fmt.Fprintf(stdout, "OK   state directory: %s\n", info.StateDir)
	for _, w := range info.Warnings {
		_, _ = fmt.Fprintf(stdout, "WARN %s\n", w)
	}
	if a.auth.Mode == config.AuthAPIKey && strings.HasPrefix(a.auth.APIKey, "AIza") {
		_, _ = fmt.Fprintln(stdout, "WARN this is a standard (AIza...) Gemini API key; Google is migrating to authorization keys (AQ...). If calls fail with 401/403, create a new key in AI Studio.")
	}

	res, err := a.svc.ListModels(ctx, media.ListModelsRequest{Live: true})
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "FAIL listing models: %v\n", err)
		return errors.New("model check failed")
	}
	problems := 0
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(stdout, "WARN %s\n", w)
		if strings.Contains(w, "live check failed") {
			problems++
		}
	}
	for _, mt := range []string{catalog.Image, catalog.Video, catalog.Speech, catalog.Music} {
		def, _ := info.Defaults[mt].(string)
		status := "OK  "
		note := ""
		for _, m := range res.Models {
			if m.ID == def && m.Available != nil && !*m.Available {
				status, note = "WARN", " (not listed for this key; the call may fail)"
				problems++
			}
		}
		_, _ = fmt.Fprintf(stdout, "%s default %s model: %s%s\n", status, mt, def, note)
	}
	if len(res.Uncatalogued) > 0 {
		_, _ = fmt.Fprintf(stdout, "INFO media models offered by the API but not in the catalog: %s\n", strings.Join(res.Uncatalogued, ", "))
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	_, _ = fmt.Fprintln(stdout, "\nAll checks passed.")
	return nil
}

func cmdConfigure(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "")
	keyStdin := fs.Bool("api-key-stdin", false, "")
	backend := fs.String("backend", "", "")
	project := fs.String("project", "", "")
	location := fs.String("location", "", "")
	outputDir := fs.String("output-dir", "", "")
	dailyBudget := fs.Float64("daily-budget-usd", -1, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\nflags: --api-key-stdin --backend --project --location --output-dir --daily-budget-usd --config", err)
	}
	if *path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*path = filepath.Join(dir, "gemini-media-mcp", "config.yaml")
	}
	doc := map[string]any{}
	if data, err := os.ReadFile(*path); err == nil {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("existing config %s is invalid: %w", *path, err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	}
	if *keyStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if key := strings.TrimSpace(line); key != "" {
			doc["apiKey"] = key
		}
	}
	set := func(k, v string) {
		if v != "" {
			doc[k] = v
		}
	}
	set("backend", *backend)
	set("project", *project)
	set("location", *location)
	set("outputDir", *outputDir)
	if *dailyBudget >= 0 {
		b, _ := doc["budget"].(map[string]any)
		if b == nil {
			b = map[string]any{}
		}
		b["dailyUsd"] = *dailyBudget
		doc["budget"] = b
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*path, data, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Wrote %s (mode 0600). Run 'gemini-media-mcp doctor' to verify.\n", *path)
	return nil
}

func cmdModels(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mt := fs.String("media-type", "", "")
	asJSON := fs.Bool("json", false, "")
	all := fs.Bool("all", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := catalog.Default()
	models := c.List(*mt, *all, time.Now())
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(models)
	}
	tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "MEDIA\tID\tALIASES\tSTATUS\tPRICE\n")
	for _, m := range models {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.MediaType, m.ID, strings.Join(m.Aliases, ","), m.EffectiveStatus(time.Now()), m.PriceSummary())
	}
	_, _ = fmt.Fprintf(tw, "\ncatalog version %s\n", c.Version)
	return tw.Flush()
}

func cmdUsage(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	period := fs.String("period", "month", "")
	cfgPath := fs.String("config", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(config.Flags{ConfigFile: *cfgPath}, config.OSEnv)
	if err != nil {
		return err
	}
	l, err := spend.Open(filepath.Join(cfg.StateDir, "usage.jsonl"), spend.Budget{})
	if err != nil {
		return err
	}
	s := l.Summarize(*period, 20)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
