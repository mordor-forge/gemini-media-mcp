package gemini

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/gemini-media-mcp/internal/provider"
)

func TestSaveImage(t *testing.T) {
	dir := t.TempDir()
	p := &GeminiProvider{outputDir: dir, modelMap: defaultModelMap()}

	data := []byte("fake-png-data")
	path, err := p.saveImage(data, "png")
	if err != nil {
		t.Fatalf("saveImage: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("saved to %q, want dir %q", path, dir)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	if string(content) != string(data) {
		t.Errorf("file content = %q, want %q", content, data)
	}
}

func TestSaveImage_FilenameFormat(t *testing.T) {
	dir := t.TempDir()
	p := &GeminiProvider{outputDir: dir, modelMap: defaultModelMap()}

	path, err := p.saveImage([]byte("data"), "jpg")
	if err != nil {
		t.Fatalf("saveImage: %v", err)
	}

	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != ".jpg" {
		t.Errorf("extension = %q, want .jpg", ext)
	}
	if base[:6] != "image-" {
		t.Errorf("filename %q does not start with 'image-'", base)
	}
}

func TestGenerate_EmptyPrompt(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Generate(context.Background(), provider.ImageRequest{Prompt: ""})
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestGenerate_RejectsKnownNonImageModel(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Generate(context.Background(), provider.ImageRequest{
		Prompt: "a sunset",
		Model:  "tts",
	})
	if err == nil {
		t.Fatal("expected error for non-image model")
	}
	if err != nil && err.Error() != `model "gemini-2.5-flash-preview-tts" does not support image operations` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEdit_EmptyPrompt(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Edit(context.Background(), provider.EditImageRequest{Prompt: "", ImagePath: "/tmp/img.png"})
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestEdit_EmptyImagePath(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Edit(context.Background(), provider.EditImageRequest{Prompt: "edit", ImagePath: ""})
	if err == nil {
		t.Fatal("expected error for empty imagePath")
	}
}

func TestCompose_TooManyReferences(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Compose(context.Background(), provider.ComposeRequest{
		Prompt:          "compose",
		ReferenceImages: []string{"a.png", "b.png", "c.png", "d.png"},
	})
	if err == nil {
		t.Fatal("expected error for >3 reference images")
	}
}

func TestCompose_NoReferences(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Compose(context.Background(), provider.ComposeRequest{
		Prompt:          "compose",
		ReferenceImages: []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty reference images")
	}
}

func TestCompose_EmptyPrompt(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	_, err := p.Compose(context.Background(), provider.ComposeRequest{
		Prompt:          "",
		ReferenceImages: []string{"a.png"},
	})
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestExtensionFromMIME(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/png", "png"},
		{"image/jpeg", "jpg"},
		{"image/gif", "gif"},
		{"image/webp", "webp"},
		{"image/unknown", "png"},
		{"", "png"},
	}
	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			got := extensionFromMIME(tt.mime)
			if got != tt.want {
				t.Errorf("extensionFromMIME(%q) = %q, want %q", tt.mime, got, tt.want)
			}
		})
	}
}

func TestMimeFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"photo.png", "image/png"},
		{"photo.webp", "image/webp"},
		{"photo.gif", "image/gif"},
		{"photo.bmp", "image/png"},
		{"no-extension", "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := mimeFromPath(tt.path)
			if got != tt.want {
				t.Errorf("mimeFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBuildImageGenerateConfig_Defaults(t *testing.T) {
	config := buildImageGenerateConfig(provider.ImageRequest{})
	if len(config.ResponseModalities) != 2 {
		t.Fatalf("ResponseModalities len = %d, want 2", len(config.ResponseModalities))
	}
	if config.ResponseModalities[0] != "IMAGE" || config.ResponseModalities[1] != "TEXT" {
		t.Fatalf("ResponseModalities = %v, want [IMAGE TEXT]", config.ResponseModalities)
	}
	if config.ImageConfig != nil {
		t.Fatalf("ImageConfig = %#v, want nil", config.ImageConfig)
	}
	if config.ThinkingConfig != nil {
		t.Fatal("ThinkingConfig should be nil when thinking is disabled")
	}
}

func TestBuildImageGenerateConfig_WithAspectRatioAndResolution(t *testing.T) {
	config := buildImageGenerateConfig(provider.ImageRequest{AspectRatio: "16:9", Resolution: "2K"})
	if config.ImageConfig == nil {
		t.Fatal("ImageConfig is nil, want populated config")
	}
	if config.ImageConfig.AspectRatio != "16:9" {
		t.Errorf("AspectRatio = %q, want %q", config.ImageConfig.AspectRatio, "16:9")
	}
	if config.ImageConfig.ImageSize != "2K" {
		t.Errorf("ImageSize = %q, want %q", config.ImageConfig.ImageSize, "2K")
	}
}

func TestBuildImageGenerateConfig_WithAspectRatioOnly(t *testing.T) {
	config := buildImageGenerateConfig(provider.ImageRequest{AspectRatio: "9:16"})
	if config.ImageConfig == nil {
		t.Fatal("ImageConfig is nil, want populated config")
	}
	if config.ImageConfig.AspectRatio != "9:16" {
		t.Errorf("AspectRatio = %q, want %q", config.ImageConfig.AspectRatio, "9:16")
	}
	if config.ImageConfig.ImageSize != "" {
		t.Errorf("ImageSize = %q, want empty", config.ImageConfig.ImageSize)
	}
}

func TestBuildImageGenerateConfig_ThinkingEnabled(t *testing.T) {
	config := buildImageGenerateConfig(provider.ImageRequest{Thinking: true})
	if config.ThinkingConfig == nil {
		t.Fatal("ThinkingConfig is nil, want populated")
	}
	if config.ThinkingConfig.ThinkingBudget == nil {
		t.Fatal("ThinkingBudget is nil, want default 1024")
	}
	if *config.ThinkingConfig.ThinkingBudget != 1024 {
		t.Errorf("ThinkingBudget = %d, want 1024", *config.ThinkingConfig.ThinkingBudget)
	}
}

func TestBuildImageGenerateConfig_ThinkingCustomBudget(t *testing.T) {
	config := buildImageGenerateConfig(provider.ImageRequest{Thinking: true, ThinkingBudget: 4096})
	if config.ThinkingConfig == nil {
		t.Fatal("ThinkingConfig is nil, want populated")
	}
	if *config.ThinkingConfig.ThinkingBudget != 4096 {
		t.Errorf("ThinkingBudget = %d, want 4096", *config.ThinkingConfig.ThinkingBudget)
	}
}

func TestBuildImageGenerateConfig_ThinkingDisabledIgnoresBudget(t *testing.T) {
	config := buildImageGenerateConfig(provider.ImageRequest{Thinking: false, ThinkingBudget: 4096})
	if config.ThinkingConfig != nil {
		t.Fatal("ThinkingConfig should be nil when thinking is disabled")
	}
}
