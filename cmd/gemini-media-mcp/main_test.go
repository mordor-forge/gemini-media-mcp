package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/mordor-forge/gemini-media-mcp/internal/config"
)

func TestFlagsAreParsedIntoConfig(t *testing.T) {
	var f config.Flags
	if err := newFlagSet("serve", &f).Parse([]string{"--transport", "http", "--http-addr", "127.0.0.1:9", "--output-dir", "/tmp/x", "--backend", "vertex", "--log-level", "debug"}); err != nil {
		t.Fatal(err)
	}
	if f.Transport != "http" || f.HTTPAddr != "127.0.0.1:9" || f.OutputDir != "/tmp/x" || f.Backend != "vertex" || f.LogLevel != "debug" {
		t.Fatalf("flags not bound: %+v", f)
	}
}

func TestSimpleCommands(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"version"}, nil, &out, &out); err != nil || !strings.Contains(out.String(), "gemini-media-mcp") {
		t.Fatalf("version: %v %q", err, out.String())
	}
	out.Reset()
	if err := run([]string{"help"}, nil, &out, &out); err != nil || !strings.Contains(out.String(), "doctor") {
		t.Fatalf("help: %v", err)
	}
	out.Reset()
	if err := run([]string{"models", "--media-type", "video"}, nil, &out, &out); err != nil || !strings.Contains(out.String(), "veo-3.1-fast-generate-preview") || strings.Contains(out.String(), "gemini-3.1-flash-image") {
		t.Fatalf("models: %v %q", err, out.String())
	}
	if err := run([]string{"bogus"}, nil, &out, &out); err == nil {
		t.Fatal("unknown command should fail")
	}
}

func TestConfigureWritesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "config.yaml")
	var out bytes.Buffer
	err := run([]string{"configure", "--config", path, "--api-key-stdin", "--output-dir", "/tmp/media", "--daily-budget-usd", "5"}, strings.NewReader("AQ.test-key\n"), &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode = %v", info.Mode())
	}
	data, _ := os.ReadFile(path)
	var doc map[string]any
	_ = yaml.Unmarshal(data, &doc)
	if doc["apiKey"] != "AQ.test-key" || doc["outputDir"] != "/tmp/media" || fmt.Sprint(doc["budget"].(map[string]any)["dailyUsd"]) != "5" {
		t.Fatalf("config = %v", doc)
	}
	// Re-running keeps existing values.
	if err := run([]string{"configure", "--config", path, "--backend", "gemini-api"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Flags{ConfigFile: path}, func(string) (string, bool) { return "", false })
	if err != nil || cfg.APIKey != "AQ.test-key" || cfg.Backend != config.BackendGeminiAPI || cfg.Budget.DailyUSD != 5 {
		t.Fatalf("loaded = %+v %v", cfg, err)
	}
}

// An existing world-readable config must not stay readable once it holds a
// key (regression: os.WriteFile only applies the mode on creation).
func TestConfigureTightensExistingConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("outputDir: /tmp/media\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // defeat the umask
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"configure", "--config", path, "--api-key-stdin"}, strings.NewReader("AQ.test-key\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v after storing a key, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "AQ.test-key") || !strings.Contains(string(data), "/tmp/media") {
		t.Fatalf("config = %q", data)
	}
}
