package media

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/version"
)

// ListModelsRequest is the input of list_models.
type ListModelsRequest struct {
	MediaType       string `json:"mediaType,omitempty" jsonschema:"Filter: image, video, speech or music."`
	IncludeInactive bool   `json:"includeInactive,omitempty" jsonschema:"Also list retired models and their replacements."`
	Live            bool   `json:"live,omitempty" jsonschema:"Also query the Gemini API for the models this key can use, flagging unavailable ones and media models missing from the catalog."`
	Detail          bool   `json:"detail,omitempty" jsonschema:"Include full capabilities (aspect ratios, voices, rules)."`
}

// ModelSummary describes a model for agents.
type ModelSummary struct {
	ID           string                `json:"id"`
	Aliases      []string              `json:"aliases,omitempty"`
	Title        string                `json:"title,omitempty"`
	MediaType    string                `json:"mediaType"`
	Status       string                `json:"status"`
	Default      bool                  `json:"default,omitempty"`
	Summary      string                `json:"summary,omitempty"`
	Price        string                `json:"price"`
	Shutdown     string                `json:"shutdown,omitempty"`
	Replacement  string                `json:"replacement,omitempty"`
	OnBackend    bool                  `json:"onBackend" jsonschema:"Offered on the active backend"`
	Available    *bool                 `json:"available,omitempty" jsonschema:"From live check: whether the API lists this model for your key"`
	Capabilities *catalog.Capabilities `json:"capabilities,omitempty"`
	Notes        []string              `json:"notes,omitempty"`
}

// ListModelsResult is the output of list_models.
type ListModelsResult struct {
	CatalogVersion string         `json:"catalogVersion"`
	Backend        string         `json:"backend"`
	Models         []ModelSummary `json:"models"`
	Uncatalogued   []string       `json:"uncatalogued,omitempty" jsonschema:"Media models the API offers that the catalog does not know yet (usable by raw ID)"`
	Warnings       []string       `json:"warnings,omitempty"`
}

// ListModels reports catalog models, optionally cross-checked live.
func (s *Service) ListModels(ctx context.Context, req ListModelsRequest) (*ListModelsResult, error) {
	mt := strings.ToLower(req.MediaType)
	switch mt {
	case "", catalog.Image, catalog.Video, catalog.Speech, catalog.Music:
	case "audio", "tts":
		mt = catalog.Speech
	default:
		return nil, apperr.Invalidf("mediaType must be image, video, speech or music")
	}
	c := s.catalog.Get()
	now := s.now()
	out := &ListModelsResult{CatalogVersion: c.Version, Backend: s.backend()}
	if _, err := s.catalog.Status(); err != nil {
		out.Warnings = append(out.Warnings, "catalog override not applied: "+err.Error())
	}

	var live map[string]bool
	if req.Live && s.auth.Mode == config.AuthUnconfigured {
		out.Warnings = append(out.Warnings, "live check skipped: "+s.ready().Error())
	} else if req.Live {
		models, err := s.api.ListModels(ctx)
		switch {
		case err != nil:
			out.Warnings = append(out.Warnings, "live check failed: "+google.Classify(err, "listing models", "", s.backend()).Error())
		case s.auth.Backend == config.BackendVertex:
			out.Warnings = append(out.Warnings, "live check is only available on the Gemini API backend")
		default:
			live = map[string]bool{}
			for _, m := range models {
				if m != nil {
					live[strings.TrimPrefix(m.Name, "models/")] = true
				}
			}
		}
	}

	backend := s.backend()
	for _, m := range c.List(mt, backend, req.IncludeInactive, now) {
		sum := ModelSummary{
			ID: m.ID, Aliases: m.Aliases, Title: m.Title, MediaType: m.MediaType,
			Status: m.StatusOn(backend, now), Default: c.IsDefault(m, backend, now), Summary: m.Summary,
			Price: m.PriceSummary(), Shutdown: m.ShutdownOn(backend), Replacement: m.Replacement,
			OnBackend: m.OfferedOn(backend, now), Notes: m.Notes,
		}
		// What requests on this backend switch to: past the global shutdown
		// the replacement, as Resolve does; otherwise the backend fallback.
		if m.Fallback != "" && m.Active(now) && (!sum.OnBackend || m.BackendShutdown[backend] != "") {
			sum.Replacement = m.Fallback
		}
		if req.Detail {
			capCopy := m.Capabilities
			sum.Capabilities = &capCopy
		}
		if live != nil && m.Active(now) {
			ok := live[m.APIID(s.backend())]
			sum.Available = &ok
		}
		out.Models = append(out.Models, sum)
	}
	if live != nil {
		for id := range live {
			fam, lmt := catalog.InferFamily(id)
			if fam == "" || (mt != "" && lmt != mt) {
				continue
			}
			if _, known := c.Lookup(id); !known {
				out.Uncatalogued = append(out.Uncatalogued, id)
			}
		}
		sort.Strings(out.Uncatalogued)
	}
	return out, nil
}

// EstimateRequest is the input of estimate_cost.
type EstimateRequest struct {
	MediaType       string `json:"mediaType" jsonschema:"image, video, speech or music"`
	Model           string `json:"model,omitempty" jsonschema:"Model ID or alias (default model when omitted)"`
	Count           int    `json:"count,omitempty" jsonschema:"Number of images/clips/songs (default 1)"`
	ImageSize       string `json:"imageSize,omitempty" jsonschema:"Images: 512, 1K, 2K or 4K"`
	InputImages     int    `json:"inputImages,omitempty" jsonschema:"Images: number of reference/source images"`
	Resolution      string `json:"resolution,omitempty" jsonschema:"Video: 720p, 1080p or 4k (Omni also 360p)"`
	DurationSeconds int    `json:"durationSeconds,omitempty" jsonschema:"Video: clip length (default 8)"`
	GenerateAudio   *bool  `json:"generateAudio,omitempty" jsonschema:"Video on Vertex AI: false for the cheaper silent rate"`
	Text            string `json:"text,omitempty" jsonschema:"Speech: the text to be spoken (length drives cost)"`
	Compare         bool   `json:"compare,omitempty" jsonschema:"Also estimate the same request on every other active model of this media type"`
}

// EstimateResult is the output of estimate_cost.
type EstimateResult struct {
	Model        string             `json:"model"`
	Estimate     catalog.Estimate   `json:"estimate"`
	Alternatives []ModelEstimate    `json:"alternatives,omitempty"`
	Budget       map[string]float64 `json:"budgetRemaining,omitempty"`
	NeedsConfirm bool               `json:"needsApproval" jsonschema:"True when the call would exceed the confirmation threshold and needs approvedCostUsd"`
	OverBudget   bool               `json:"wouldExceedBudget,omitempty" jsonschema:"True when a budget would refuse this call right now (see warnings)"`
	Warnings     []string           `json:"warnings,omitempty" jsonschema:"Lifecycle notices (deprecation, shutdown, replacement) and budget problems"`
	Note         string             `json:"note"`
}

// ModelEstimate pairs a model with an estimate.
type ModelEstimate struct {
	Model    string  `json:"model"`
	USD      float64 `json:"usd"`
	Supports bool    `json:"supports" jsonschema:"Whether the model supports the requested parameters"`
}

// EstimateCost prices a hypothetical request without calling the API.
func (s *Service) EstimateCost(_ context.Context, req EstimateRequest) (*EstimateResult, error) {
	mt := strings.ToLower(req.MediaType)
	if mt == "audio" || mt == "tts" {
		mt = catalog.Speech
	}
	switch mt {
	case catalog.Image, catalog.Video, catalog.Speech, catalog.Music:
	default:
		return nil, apperr.Invalidf("mediaType must be image, video, speech or music")
	}
	r, location, warnings, err := s.resolve(req.Model, mt)
	if err != nil {
		return nil, err
	}
	audio := req.GenerateAudio == nil || *req.GenerateAudio
	size := strings.ToUpper(req.ImageSize)
	estimate := func(m *catalog.Model) (catalog.Estimate, bool) {
		switch mt {
		case catalog.Image:
			_, err := m.Validate(catalog.Params{"imageSize": size, "referenceImages": itoa(req.InputImages)}, s.backend())
			return m.EstimateImage(size, req.Count, 400, req.InputImages, s.backend(), location), err == nil
		case catalog.Video:
			_, err := m.Validate(catalog.Params{"resolution": strings.ToLower(req.Resolution), "durationSeconds": itoa(req.DurationSeconds)}, s.backend())
			return m.EstimateVideo(strings.ToLower(req.Resolution), req.DurationSeconds, req.Count, audio, s.backend()), err == nil
		case catalog.Speech:
			text := req.Text
			if text == "" {
				text = strings.Repeat("word ", 150)
			}
			e := m.EstimateSpeech(text)
			e.USD *= float64(max(req.Count, 1))
			return e, true
		default:
			return m.EstimateMusic(req.Count), true
		}
	}
	est, _ := estimate(r.Model)
	out := &EstimateResult{Model: r.Model.ID, Estimate: est, Warnings: warnings}
	if mt == catalog.Speech && req.Text == "" {
		out.Estimate.Breakdown += " (assumed one minute of speech; pass text for a precise figure)"
	}
	if req.Compare {
		c := s.catalog.Get()
		for _, m := range c.List(mt, s.backend(), false, s.now()) {
			if m.ID == r.Model.ID || !m.OfferedOn(s.backend(), s.now()) {
				continue
			}
			e, ok := estimate(m)
			out.Alternatives = append(out.Alternatives, ModelEstimate{Model: m.ID, USD: e.USD, Supports: ok})
		}
		sort.Slice(out.Alternatives, func(i, j int) bool { return out.Alternatives[i].USD < out.Alternatives[j].USD })
	}
	sum := s.ledger.Summarize("today", 0)
	out.Budget = sum.Remaining
	if t := s.ledger.Budget().ConfirmAboveUSD; t > 0 && est.USD > t {
		out.NeedsConfirm = true
	}
	if msg := s.ledger.Check(est.USD); msg != "" {
		out.OverBudget = true
		out.Warnings = append(out.Warnings, msg+": this call would be refused. Ask the user to raise the budget (GEMINI_MEDIA_BUDGET_* settings) or pick a cheaper option.")
	}
	out.Note = "List-price estimate from the model catalog; actual billing may differ (free tier, batch/flex tiers, regional pricing). Authoritative spend: Google AI Studio or Cloud Billing."
	return out, nil
}

// UsageRequest is the input of get_usage.
type UsageRequest struct {
	Period string `json:"period,omitempty" jsonschema:"session (since this server started), today (default), month or all"`
	Recent int    `json:"recent,omitempty" jsonschema:"How many recent entries to include (default 10, max 100)"`
}

// UsageResult is the output of get_usage.
type UsageResult struct {
	Period     string         `json:"period"`
	Summary    spend.Summary  `json:"summary"`
	Budget     spend.Budget   `json:"budget"`
	LedgerPath string         `json:"ledgerPath"`
	ActiveJobs []jobs.Job     `json:"activeJobs,omitempty"`
	Note       string         `json:"note"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// Usage summarizes recorded spend.
func (s *Service) Usage(_ context.Context, req UsageRequest) (*UsageResult, error) {
	period := strings.ToLower(firstNonEmpty(req.Period, "today"))
	switch period {
	case "session", "today", "month", "all":
	default:
		return nil, apperr.Invalidf("period must be session, today, month or all")
	}
	recent := req.Recent
	if recent <= 0 {
		recent = 10
	}
	recent = min(recent, 100)
	out := &UsageResult{
		Period:     period,
		Summary:    s.ledger.Summarize(period, recent),
		Budget:     s.ledger.Budget(),
		LedgerPath: s.ledger.Path(),
		Note:       "Costs are estimates computed from list prices and the token usage Google reports. Failed and safety-filtered generations are recorded at $0. Check Google AI Studio or Cloud Billing for authoritative charges.",
	}
	for _, j := range s.jobs.List(50) {
		if j.State == jobs.StateWorking {
			out.ActiveJobs = append(out.ActiveJobs, *j)
		}
	}
	return out, nil
}

// InfoResult is the output of get_config.
type InfoResult struct {
	Version        string            `json:"version"`
	Backend        string            `json:"backend"`
	AuthMode       string            `json:"authMode"`
	BackendReason  string            `json:"backendReason"`
	Project        string            `json:"project,omitempty"`
	Location       string            `json:"location,omitempty"`
	Transport      string            `json:"transport"`
	HTTPAddr       string            `json:"httpAddr,omitempty" jsonschema:"Address the HTTP transport listens on"`
	OutputDir      string            `json:"outputDir"`
	StateDir       string            `json:"stateDir"`
	ConfigFile     string            `json:"configFile,omitempty"`
	CatalogVersion string            `json:"catalogVersion"`
	CatalogFile    string            `json:"catalogFile,omitempty"`
	Defaults       map[string]any    `json:"defaults"`
	Budget         spend.Budget      `json:"budget"`
	InputPolicy    string            `json:"inputPolicy"`
	InlinePreviews bool              `json:"inlinePreviews"`
	Sources        map[string]string `json:"settingSources,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
}

// Info reports the effective configuration (no secrets).
func (s *Service) Info(transport string) *InfoResult {
	c := s.catalog.Get()
	defaults := map[string]any{}
	for _, mt := range []string{catalog.Image, catalog.Video, catalog.Speech, catalog.Music} {
		if r, err := c.Resolve("", mt, s.backend(), s.now()); err == nil {
			defaults[mt] = r.Model.ID
		}
	}
	defaults["voice"] = firstNonEmpty(s.cfg.Defaults.Voice, DefaultVoice)
	policy := "any readable file"
	if !s.cfg.AnyInputPathAllowed() {
		policy = "output directory only"
		if len(s.cfg.InputDirs) > 0 {
			policy = "output directory and " + strings.Join(s.cfg.InputDirs, ", ")
		}
	}
	info := &InfoResult{
		Version: version.String(), Backend: s.backend(), AuthMode: string(s.auth.Mode), BackendReason: s.auth.Reason,
		Project: s.auth.Project, Transport: transport, OutputDir: s.store.Dir(), StateDir: s.cfg.StateDir,
		ConfigFile: s.cfg.ConfigFile, CatalogVersion: c.Version, CatalogFile: s.cfg.CatalogFile, Defaults: defaults,
		Budget: s.ledger.Budget(), InputPolicy: policy, InlinePreviews: s.cfg.InlinePreviewsEnabled(),
		Sources: s.cfg.Sources, Warnings: append([]string(nil), s.cfg.Warnings...),
	}
	if transport == config.TransportHTTP {
		info.HTTPAddr = s.cfg.HTTP.Addr
	}
	if s.auth.Mode == config.AuthVertexADC {
		info.Location = s.auth.Location + " (per-model locations from the catalog take precedence)"
	}
	if path, err := s.catalog.Status(); err != nil {
		info.Warnings = append(info.Warnings, fmt.Sprintf("catalog override %s: %v", path, err))
	}
	return info
}
