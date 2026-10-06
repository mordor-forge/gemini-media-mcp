package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func TestEmbeddedCatalogIsConsistent(t *testing.T) {
	c := Default()
	if c.Version == "" || len(c.Models) == 0 {
		t.Fatal("empty catalog")
	}
	for mt, name := range c.Defaults {
		m, ok := c.Lookup(name)
		if !ok {
			t.Fatalf("default %s=%s not in catalog", mt, name)
		}
		if m.MediaType != mt || !m.Active(now) {
			t.Fatalf("default %s=%s is %s/%s", mt, name, m.MediaType, m.EffectiveStatus(now))
		}
	}
	families := map[string]bool{FamilyGeminiImage: true, FamilyVeo: true, FamilyGeminiTTS: true, FamilyLyria: true}
	for _, m := range c.Models {
		if !families[m.Family] {
			t.Errorf("%s: unknown family %q", m.ID, m.Family)
		}
		for _, ref := range []string{m.Replacement, m.Fallback} {
			if ref == "" {
				continue
			}
			if r, ok := c.Lookup(ref); !ok || r.MediaType != m.MediaType {
				t.Errorf("%s: replacement/fallback %q missing or wrong media type", m.ID, ref)
			}
		}
		if m.Active(now) && !m.Pricing.Priced() {
			t.Errorf("%s: active model without pricing", m.ID)
		}
		if m.Shutdown != "" {
			if _, ok := m.ShutdownTime(); !ok {
				t.Errorf("%s: bad shutdown date %q", m.ID, m.Shutdown)
			}
		}
	}
}

func TestLegacyAliasesStillResolve(t *testing.T) {
	c := Default()
	// Every alias the v0.x server accepted must keep working.
	cases := map[string]string{
		"nb2": Image, "pro": Image, "lite": Video, "fast": Video, "standard": Video,
		"tts": Speech, "clip": Music, "full": Music,
	}
	for alias, mt := range cases {
		r, err := c.Resolve(alias, mt, "gemini-api", now)
		if err != nil {
			t.Errorf("Resolve(%q): %v", alias, err)
			continue
		}
		if !r.Model.Active(now) {
			t.Errorf("alias %q resolved to inactive %s", alias, r.Model.ID)
		}
	}
}

func TestResolveRedirectsRetiredPreviewIDs(t *testing.T) {
	c := Default()
	r, err := c.Resolve("gemini-3-pro-image-preview", Image, "gemini-api", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model.ID != "gemini-3-pro-image" || len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "retired") {
		t.Fatalf("got %s %v", r.Model.ID, r.Warnings)
	}
}

func TestResolveBackendSpecifics(t *testing.T) {
	c := Default()
	r, err := c.Resolve("fast", Video, "vertex", now)
	if err != nil || r.APIID != "veo-3.1-fast-generate-001" {
		t.Fatalf("vertex veo id = %v, %v", r, err)
	}
	loc, warn := r.Model.VertexLocation("global", false)
	if loc != "us-central1" || warn != "" {
		t.Fatalf("veo location = %s %q", loc, warn)
	}
	r, err = c.Resolve("", Speech, "vertex", now)
	if err != nil || r.Model.ID != "gemini-2.5-flash-preview-tts" || r.APIID != "gemini-2.5-flash-tts" {
		t.Fatalf("vertex speech fallback = %+v, %v", r, err)
	}
	r, err = c.Resolve("full", Music, "vertex", now)
	if err != nil || r.Model.ID != "lyria-3-pro-preview" {
		t.Fatalf("vertex music fallback = %+v, %v", r, err)
	}
	img, _ := c.Resolve("nb2", Image, "vertex", now)
	if loc, warn := img.Model.VertexLocation("us-central1", true); loc != "global" || warn == "" {
		t.Fatalf("explicit unsupported location should switch to global with a warning, got %s %q", loc, warn)
	}
}

func TestResolveDeprecatedWarns(t *testing.T) {
	c := Default()
	before := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	r, err := c.Resolve("nano-banana", Image, "gemini-api", before)
	if err != nil || r.Model.ID != "gemini-2.5-flash-image" || len(r.Warnings) == 0 {
		t.Fatalf("deprecated model should resolve with a warning: %+v %v", r, err)
	}
	after := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	r, err = c.Resolve("nano-banana", Image, "gemini-api", after)
	if err != nil || r.Model.ID != "gemini-nano-banana-2.1" {
		t.Fatalf("after shutdown the replacement should be used: %+v %v", r, err)
	}
}

// Nano Banana 2.1 replaced Nano Banana 2 as the default; the old model keeps
// working with a warning until its Gemini API shutdown, then redirects.
func TestNanoBanana21ReplacesNanoBanana2(t *testing.T) {
	c := Default()
	if c.Defaults[Image] != "gemini-nano-banana-2.1" {
		t.Fatalf("default image model = %s", c.Defaults[Image])
	}
	for _, alias := range []string{"nb2", "nb2.1", "nano-banana-2.1", "flash-image"} {
		if r, err := c.Resolve(alias, Image, "gemini-api", now); err != nil || r.Model.ID != "gemini-nano-banana-2.1" || len(r.Warnings) != 0 {
			t.Errorf("Resolve(%q) = %+v %v", alias, r, err)
		}
	}
	before := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"gemini-3.1-flash-image", "nano-banana-2"} {
		r, err := c.Resolve(name, Image, "gemini-api", before)
		if err != nil || r.Model.ID != "gemini-3.1-flash-image" || len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "2026-10-29") {
			t.Errorf("before shutdown %q = %+v %v", name, r, err)
		}
		r, err = c.Resolve(name, Image, "gemini-api", time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
		if err != nil || r.Model.ID != "gemini-nano-banana-2.1" {
			t.Errorf("after shutdown %q = %+v %v", name, r, err)
		}
	}
	for _, retired := range []string{"gemini-3.1-flash-image-preview", "gemini-2.5-flash-image-preview"} {
		if r, err := c.Resolve(retired, Image, "gemini-api", now); err != nil || r.Model.ID != "gemini-nano-banana-2.1" {
			t.Errorf("retired %s = %+v %v", retired, r, err)
		}
	}
	// Future Nano Banana IDs pass through as image models.
	if fam, mt := InferFamily("gemini-nano-banana-2.2"); fam != FamilyGeminiImage || mt != Image {
		t.Errorf("InferFamily(gemini-nano-banana-2.2) = %s %s", fam, mt)
	}
	// Streaming models are not callable here, so live checks do not flag them.
	if fam, _ := InferFamily("lyria-realtime-exp"); fam != "" {
		t.Errorf("InferFamily(lyria-realtime-exp) = %s, want none", fam)
	}
}

func TestResolveUnknownAndMismatch(t *testing.T) {
	c := Default()
	r, err := c.Resolve("veo-4.0-generate-preview", Video, "gemini-api", now)
	if err != nil || r.Known || r.Model.Family != FamilyVeo || len(r.Warnings) == 0 {
		t.Fatalf("unknown veo id should pass through: %+v %v", r, err)
	}
	if _, err := c.Resolve("veo-4.0-generate-preview", Image, "gemini-api", now); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("mismatched media type should fail: %v", err)
	}
	if _, err := c.Resolve("fast", Image, "gemini-api", now); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("alias of another media type should fail: %v", err)
	}
	if _, err := c.Resolve("imagen-4.0-generate-001", Image, "gemini-api", now); apperr.KindOf(err) != apperr.NotFound {
		t.Fatalf("imagen should explain the shutdown: %v", err)
	}
	if _, err := c.Resolve("banana", Image, "gemini-api", now); err == nil || !strings.Contains(err.Error(), "nb2") {
		t.Fatalf("unknown name should list options: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	c := Default()
	fast, _ := c.Lookup("fast")
	lite, _ := c.Lookup("lite")
	if _, err := fast.Validate(Params{"resolution": "4k", "durationSeconds": "4"}, "gemini-api"); err == nil || !strings.Contains(err.Error(), "8") {
		t.Fatalf("4k with 4s must fail: %v", err)
	}
	if _, err := fast.Validate(Params{"resolution": "4k"}, "gemini-api"); err != nil {
		t.Fatalf("4k with default duration is fine: %v", err)
	}
	if _, err := lite.Validate(Params{"resolution": "4k"}, "gemini-api"); err == nil {
		t.Fatal("lite has no 4k")
	}
	if _, err := lite.Validate(Params{"referenceImages": "1"}, "gemini-api"); err == nil {
		t.Fatal("lite has no reference images")
	}
	if _, err := lite.Validate(Params{"lastFrame": "x"}, "gemini-api"); err == nil {
		t.Fatal("lite lastFrame without image must fail")
	}
	if _, err := fast.Validate(Params{"referenceImages": "2", "image": "x"}, "gemini-api"); err == nil {
		t.Fatal("reference images + first frame must fail")
	}
	if _, err := fast.Validate(Params{"lastFrame": "x"}, "gemini-api"); err == nil {
		t.Fatal("lastFrame without image must fail")
	}
	dropped, err := fast.Validate(Params{"seed": "3"}, "gemini-api")
	if err != nil || len(dropped) != 1 || dropped[0] != "seed" {
		t.Fatalf("seed should be dropped on gemini-api: %v %v", dropped, err)
	}
	nb2, _ := c.Lookup("nb2")
	if _, err := nb2.Validate(Params{"aspectRatio": "7:3"}, "gemini-api"); err == nil {
		t.Fatal("bad aspect ratio must fail")
	}
	if _, err := nb2.Validate(Params{"aspectRatio": "8:1", "imageSize": "2K", "referenceImages": "14"}, "gemini-api"); err != nil {
		t.Fatalf("valid NB 2.1 params rejected: %v", err)
	}
	if _, err := nb2.Validate(Params{"imageSize": "512"}, "gemini-api"); err == nil {
		t.Fatal("NB 2.1 has no 512")
	}
	old, _ := c.Lookup("gemini-3.1-flash-image")
	if _, err := old.Validate(Params{"aspectRatio": "8:1", "imageSize": "512", "referenceImages": "14"}, "gemini-api"); err != nil {
		t.Fatalf("valid NB2 params rejected: %v", err)
	}
	pro, _ := c.Lookup("pro")
	if _, err := pro.Validate(Params{"imageSize": "512"}, "gemini-api"); err == nil {
		t.Fatal("pro has no 512")
	}
	tts, _ := c.Lookup("tts")
	if _, err := tts.Validate(Params{"speakers": "3"}, "gemini-api"); err == nil {
		t.Fatal("3 speakers must fail")
	}
}

func TestEstimates(t *testing.T) {
	c := Default()
	nb2, _ := c.Lookup("nb2")
	e := nb2.EstimateImage("1K", 1, 400, 0, "gemini-api", "")
	// 1120 image tokens x $30/M + 900 thinking tokens x $7.50/M + 100 prompt tokens x $1.50/M
	if e.USD < 0.0403 || e.USD > 0.0406 || e.Basis != BasisTokens {
		t.Fatalf("NB 2.1 1K estimate = %+v", e)
	}
	// The breakdown names every part of the figure, so it adds up.
	for _, part := range []string{"1120 image tokens", "($0.0336)", "~900 text/thinking tokens", "($0.0067)", "input ($0.0001"} {
		if !strings.Contains(e.Breakdown, part) {
			t.Errorf("breakdown %q lacks %q", e.Breakdown, part)
		}
	}
	// 4K bills 3780 image tokens ($0.1134), as measured from live usage.
	if v := nb2.EstimateImage("4K", 2, 0, 0, "vertex", "us-central1").USD; v < 2*0.1134*1.1 {
		t.Fatalf("NB 2.1 4K estimate or regional multiplier wrong: %v", v)
	}
	// Models without textOutputTokens keep the 400-token default.
	nbLite, _ := c.Lookup("nb2-lite")
	if nbLite.Pricing.TextOutputTokens != 0 || !strings.Contains(nbLite.EstimateImage("1K", 1, 0, 0, "gemini-api", "").Breakdown, "~400 text/thinking") {
		t.Error("default text allowance not applied to Lite")
	}
	old, _ := c.Lookup("gemini-3.1-flash-image")
	if e := old.EstimateImage("1K", 1, 400, 0, "gemini-api", ""); e.USD < 0.067 || e.USD > 0.07 {
		t.Fatalf("NB2 1K estimate = %+v", e)
	}
	std, _ := c.Lookup("standard")
	if v := std.EstimateVideo("4k", 8, 1, true, "gemini-api").USD; v != 4.8 {
		t.Fatalf("Veo 4k 8s = %v, want 4.80", v)
	}
	if v := std.EstimateVideo("1080p", 8, 1, false, "vertex").USD; v != 1.6 {
		t.Fatalf("Veo no-audio on vertex = %v, want 1.60", v)
	}
	if v := std.EstimateVideo("1080p", 8, 1, false, "gemini-api").USD; v != 3.2 {
		t.Fatalf("Gemini API always bills audio: %v", v)
	}
	lite, _ := c.Lookup("lite")
	if v := lite.EstimateVideo("", 0, 1, true, "gemini-api").USD; v != 0.4 {
		t.Fatalf("lite default estimate = %v, want 0.40 (720p x 8s)", v)
	}
	clip, _ := c.Lookup("clip")
	if clip.EstimateMusic(1).USD != 0.04 {
		t.Fatal("clip price")
	}
	tts, _ := c.Lookup("tts")
	if e := tts.EstimateSpeech(strings.Repeat("word ", 150)); e.USD <= 0 || e.USD > 0.02 {
		t.Fatalf("1 minute of speech = %+v", e)
	}

	got, ok := nb2.CostFromUsage(TokenUsage{PromptTokens: 20, OutputTokens: 1130, OutputByModality: map[string]int{"image": 1120, "text": 10}}, "gemini-api", "")
	if !ok || got.USD < 0.0336 || got.USD > 0.0338 || got.Basis != BasisUsage {
		t.Fatalf("CostFromUsage = %+v", got)
	}
	if _, ok := clip.CostFromUsage(TokenUsage{PromptTokens: 5}, "gemini-api", ""); ok {
		t.Fatal("per-request models are not token priced")
	}
	for _, m := range c.Models {
		if m.Active(now) && m.PriceSummary() == "unknown" {
			t.Errorf("%s: no price summary", m.ID)
		}
	}
}

func TestOverrideMergeAndHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "override.yaml")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`
defaults:
  video: fast
models:
  - id: veo-3.1-fast-generate-preview
    pricing:
      perSecond: {720p: 0.2}
  - id: veo-4.0-generate-preview
    aliases: [veo4]
    status: preview
    pricing:
      perSecond: {720p: 1.0}
`)
	s, err := NewSource(path, map[string]string{"image": "pro"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	fast, _ := c.Lookup("fast")
	if fast.Pricing.PerSecond["720p"] != 0.2 || fast.Pricing.PerSecond["4k"] != 0.30 {
		t.Fatalf("map fields must merge key by key: %v", fast.Pricing.PerSecond)
	}
	if len(fast.Capabilities.Resolutions) == 0 || fast.VertexID == "" {
		t.Fatal("unspecified fields must be kept")
	}
	v4, ok := c.Lookup("veo4")
	if !ok || v4.Family != FamilyVeo || v4.MediaType != Video {
		t.Fatalf("new model should be appended with inferred family: %+v", v4)
	}
	if c.Defaults["video"] != "fast" || c.Defaults["image"] != "pro" {
		t.Fatalf("defaults = %v", c.Defaults)
	}

	// A broken edit keeps the last good catalog and reports the error.
	write("models: [oops")
	s.lastCheck = time.Time{}
	s.modTime = time.Time{}
	if got := s.Get(); got != c {
		t.Fatal("broken override must not replace the catalog")
	}
	if _, err := s.Status(); err == nil {
		t.Fatal("reload error must be reported")
	}

	// A valid edit is picked up.
	write("models:\n  - id: lyria-3-clip-preview\n    pricing: {perRequest: 0.05}\n")
	s.lastCheck = time.Time{}
	s.modTime = time.Time{}
	clip, _ := s.Get().Lookup("clip")
	if clip.Pricing.PerRequest != 0.05 {
		t.Fatalf("hot reload not applied: %v", clip.Pricing.PerRequest)
	}
	if _, err := s.Status(); err != nil {
		t.Fatalf("status should clear after a good reload: %v", err)
	}
}

func TestBrokenOverrideAtStartupFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(path, []byte("models: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSource(path, nil, nil)
	if err != nil {
		t.Fatalf("a broken override must not prevent startup: %v", err)
	}
	if _, ok := s.Get().Lookup("nb2"); !ok {
		t.Fatal("embedded catalog should be served")
	}
	if _, err := s.Status(); err == nil {
		t.Fatal("the override error must be reported")
	}
}

func TestOverrideErrors(t *testing.T) {
	if _, err := Merge(embedded, []byte("unknownKey: 1")); err == nil {
		t.Fatal("unknown top-level key should fail")
	}
	if _, err := Merge(embedded, []byte("models:\n  - aliases: [x]\n")); err == nil {
		t.Fatal("model without id should fail")
	}
	if _, err := Merge(embedded, []byte("models:\n  - id: mystery-model\n")); err == nil {
		t.Fatal("uninferable new model should need family/mediaType")
	}
	if _, err := Merge(embedded, []byte("models:\n  - id: x-image\n    family: gemini-image\n    mediaType: image\n    aliases: [nb2]\n")); err == nil {
		t.Fatal("alias collisions should fail")
	}
}
