package gemini

import (
	"context"
	"testing"
)

func TestListModels_ReturnsAllTiers(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 9 {
		t.Fatalf("got %d models, want 9", len(models))
	}

	tiers := make(map[string]bool)
	for _, m := range models {
		tiers[m.Tier] = true
	}
	for _, want := range []string{"nb2", "pro", "lite", "fast", "standard", "tts", "clip", "full", "omni"} {
		if !tiers[want] {
			t.Errorf("missing tier %q in model list", want)
		}
	}
}

func TestListModels_MediaTypes(t *testing.T) {
	p := &GeminiProvider{modelMap: defaultModelMap()}
	models, _ := p.ListModels(context.Background())

	imageTiers := 0
	videoTiers := 0
	audioTiers := 0
	musicTiers := 0
	for _, m := range models {
		switch m.MediaType {
		case "image":
			imageTiers++
		case "video":
			videoTiers++
		case "audio":
			audioTiers++
		case "music":
			musicTiers++
		}
	}
	if imageTiers != 2 {
		t.Errorf("got %d image tiers, want 2", imageTiers)
	}
	if videoTiers != 4 {
		t.Errorf("got %d video tiers, want 4", videoTiers)
	}
	if audioTiers != 1 {
		t.Errorf("got %d audio tiers, want 1", audioTiers)
	}
	if musicTiers != 2 {
		t.Errorf("got %d music tiers, want 2", musicTiers)
	}
}
