// Package media implements the generation workflows behind the MCP tools:
// model resolution, validation, cost estimation and budget checks, the
// Google API calls, saving outputs with provenance, and spend reconciliation.
package media

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// Deps are the collaborators of a Service.
type Deps struct {
	API     google.API
	Auth    *config.Auth
	Config  *config.Config
	Catalog *catalog.Source
	Store   *store.Store
	Jobs    *jobs.Registry
	Ledger  *spend.Ledger
	Logger  *slog.Logger
}

// Service runs generation workflows. It is safe for concurrent use.
type Service struct {
	api     google.API
	auth    *config.Auth
	cfg     *config.Config
	catalog *catalog.Source
	store   *store.Store
	jobs    *jobs.Registry
	ledger  *spend.Ledger
	log     *slog.Logger
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error

	jobLocks sync.Map // job ID -> chan struct{} (capacity 1): a lock that honors contexts
	fetching sync.Map // job ID -> struct{}: outputs being downloaded in the background
	// omniActive tracks Omni interactions this process is running:
	// job ID -> chan struct{} closed when the job is settled.
	omniActive sync.Map
	// imaging admits one tile_image or stitch_tiles step at a time (each
	// may hold about 1 GB); capacity 1. imagingWaiting counts the calls
	// waiting for it.
	imaging        chan struct{}
	imagingWaiting atomic.Int32
}

// New builds a Service.
func New(d Deps) *Service {
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		api: d.API, auth: d.Auth, cfg: d.Config, catalog: d.Catalog,
		store: d.Store, jobs: d.Jobs, ledger: d.Ledger, log: log,
		now:     time.Now,
		sleep:   sleepCtx,
		imaging: make(chan struct{}, 1),
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Cost is the cost information attached to every generation result.
type Cost struct {
	EstimatedUSD float64 `json:"estimatedUsd" jsonschema:"Estimate computed before the call from the price table"`
	USD          float64 `json:"usd" jsonschema:"Best-known cost in USD; reconciled from token usage when the API reports it"`
	Basis        string  `json:"basis" jsonschema:"How usd was derived: usage_metadata, unit_params, token_estimate or unpriced"`
	Breakdown    string  `json:"breakdown,omitempty"`
	PriceAsOf    string  `json:"priceAsOf,omitempty"`
	Pending      bool    `json:"pending,omitempty" jsonschema:"True while a video job is still running (cost is reserved, not final)"`
}

// ProgressFunc reports progress for long operations.
type ProgressFunc func(message string, progress, total float64)

type progressKey struct{}

// WithProgress attaches a progress reporter to ctx.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func progress(ctx context.Context, msg string, p, total float64) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		fn(msg, p, total)
	}
}

func (s *Service) backend() string { return string(s.auth.Backend) }

// ready fails fast when the server started without usable credentials.
func (s *Service) ready() error {
	if s.auth.Mode != config.AuthUnconfigured {
		return nil
	}
	return &apperr.Error{
		Kind:    apperr.Auth,
		Message: "the server has no usable Google credentials: " + s.auth.Reason,
		Hint:    "Tell the user to set GEMINI_API_KEY (https://aistudio.google.com/apikey) in this MCP server's environment, or run `gemini-media-mcp configure --api-key-stdin`, then restart the server. `gemini-media-mcp doctor` verifies the setup.",
	}
}

// resolve picks the model and Vertex location for a request.
func (s *Service) resolve(name, mediaType string) (*catalog.Resolved, string, []string, error) {
	c := s.catalog.Get()
	r, err := c.Resolve(name, mediaType, s.backend(), s.now())
	if err != nil {
		return nil, "", nil, err
	}
	warnings := append([]string(nil), r.Warnings...)
	location := ""
	if s.auth.Mode == config.AuthVertexADC {
		loc, warn := r.Model.VertexLocation(s.auth.Location, s.auth.LocationExplicit)
		location = loc
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}
	return r, location, warnings, nil
}

// reserve checks budgets for an estimate. Models without price data would
// estimate $0 and slip past every cap, so when any cap or confirmation
// threshold is configured they need an explicit approvedCostUsd, which is
// then reserved (and recorded) as their estimated cost.
func (s *Service) reserve(est *catalog.Estimate, approved float64) (*spend.Reservation, error) {
	if est.Basis == catalog.BasisUnpriced {
		b := s.ledger.Budget()
		if b.ConfirmAboveUSD > 0 || b.SessionUSD > 0 || b.DailyUSD > 0 || b.MonthlyUSD > 0 {
			if approved <= 0 {
				return nil, &apperr.Error{
					Kind:    apperr.Confirm,
					Message: "this model has no price data, so its cost cannot be estimated and budgets cannot protect you",
					Hint:    "Ask the user for the maximum they accept for this call and retry with approvedCostUsd set to it, or add the model's pricing in a catalog override file (GEMINI_MEDIA_CATALOG).",
				}
			}
			est.USD, est.Basis = approved, spend.BasisEstimate
			est.Breakdown = fmt.Sprintf("unpriced model; using the approved maximum $%.2f", approved)
		}
	}
	return s.ledger.Reserve(est.USD, approved)
}

func (s *Service) inputPolicy() store.InputPolicy {
	return store.InputPolicy{AllowAny: s.cfg.AnyInputPathAllowed(), Roots: s.cfg.InputDirs}
}

func (s *Service) loadInputs(refs []string, what string) ([]*store.Input, error) {
	out := make([]*store.Input, 0, len(refs))
	for i, ref := range refs {
		in, err := s.store.LoadInput(ref, s.inputPolicy())
		if err != nil {
			return nil, &apperr.Error{
				Kind:    apperr.Invalid,
				Message: fmt.Sprintf("%s %d: %v", what, i+1, err),
				Hint:    "Pass a local file path, a gemini-media:// URI from an earlier result, or a data: URI; get_config shows which directories may be read.",
				Cause:   err,
			}
		}
		out = append(out, in)
	}
	return out, nil
}

func (s *Service) loadInput(ref, what string) (*store.Input, error) {
	ins, err := s.loadInputs([]string{ref}, what)
	if err != nil {
		return nil, err
	}
	return ins[0], nil
}

func requireImage(in *store.Input, what string) error {
	if !strings.HasPrefix(in.MIMEType, "image/") {
		return apperr.Invalidf("%s %s is %s, not an image", what, in.Ref, in.MIMEType)
	}
	return nil
}

// modelNotices converts response lifecycle signals into warnings.
func modelNotices(r *google.Result) []string {
	if r == nil {
		return nil
	}
	var out []string
	if r.ModelRetiresAt != nil {
		out = append(out, fmt.Sprintf("the API reports this model retires on %s", r.ModelRetiresAt.Format("2006-01-02")))
	}
	if r.ModelNotice != "" {
		out = append(out, r.ModelNotice)
	}
	return out
}

func tokenUsage(u google.Usage) catalog.TokenUsage {
	return catalog.TokenUsage{
		PromptTokens:     u.PromptTokens,
		OutputTokens:     u.OutputTokens,
		ThoughtsTokens:   u.ThoughtsTokens,
		PromptByModality: u.PromptByModality,
		OutputByModality: u.OutputByModality,
	}
}

func addUsage(a, b google.Usage) google.Usage {
	a.PromptTokens += b.PromptTokens
	a.OutputTokens += b.OutputTokens
	a.ThoughtsTokens += b.ThoughtsTokens
	a.TotalTokens += b.TotalTokens
	a.PromptByModality = mergeCounts(a.PromptByModality, b.PromptByModality)
	a.OutputByModality = mergeCounts(a.OutputByModality, b.OutputByModality)
	return a
}

func mergeCounts(a, b map[string]int) map[string]int {
	if len(b) == 0 {
		return a
	}
	if a == nil {
		a = map[string]int{}
	}
	for k, v := range b {
		a[k] += v
	}
	return a
}

// settle finalizes a synchronous call in the ledger and returns its Cost.
func (s *Service) settle(res *spend.Reservation, entry spend.Entry, est catalog.Estimate, actual *catalog.Estimate) Cost {
	cost := Cost{EstimatedUSD: est.USD, USD: est.USD, Basis: est.Basis, Breakdown: est.Breakdown, PriceAsOf: est.PriceAsOf}
	if actual != nil {
		cost.USD, cost.Basis, cost.Breakdown = actual.USD, actual.Basis, actual.Breakdown
	}
	entry.EstimatedUSD = est.USD
	entry.CostUSD = cost.USD
	entry.Basis = cost.Basis
	entry.PriceAsOf = est.PriceAsOf
	entry.Backend = s.backend()
	if _, err := res.Settle(entry); err != nil {
		s.log.Warn("recording spend failed", "err", err)
	}
	return cost
}

// fail records a failed call (not billed) and returns the classified error.
func (s *Service) fail(res *spend.Reservation, entry spend.Entry, err error) error {
	if res != nil {
		entry.Status = spend.StatusFailed
		if apperr.KindOf(err) == apperr.Safety {
			entry.Status = spend.StatusFiltered
		}
		entry.Error = truncate(err.Error(), 300)
		entry.Backend = s.backend()
		if _, lerr := res.Settle(entry); lerr != nil {
			s.log.Warn("recording spend failed", "err", lerr)
		}
	}
	return err
}

// settleUnusable records a call that reached the model but returned no
// usable media (for example finish reason MAX_TOKENS). Google bills the
// tokens a response reports whether or not the requested media came back, so
// billable usage is settled as spent at its token cost. Safety blocks stay
// "filtered" and usage without a token price stays "failed"; the usage is
// kept on the entry for the record either way.
func (s *Service) settleUnusable(res *spend.Reservation, entry spend.Entry, err error, m *catalog.Model, recorded, billable google.Usage, location string, est catalog.Estimate) error {
	if hasTokens(recorded) {
		entry.Usage = recorded
	}
	if hasTokens(billable) {
		tu := tokenUsage(billable)
		if len(tu.OutputByModality) == 0 && m.Pricing.OutputPer1M["text"] > 0 {
			// Nothing came back as media, so the output was text or thinking.
			// (Models without a text price, like TTS, keep their primary
			// output rate: over- rather than under-counting protects budgets.)
			tu.OutputByModality = map[string]int{"text": billable.OutputTokens}
		}
		if actual, ok := m.CostFromUsage(tu, s.backend(), location); ok && actual.USD > 0 {
			entry.Status = spend.StatusOK
			entry.Error = truncate(err.Error(), 300)
			cost := s.settle(res, entry, est, &actual)
			return billedNote(err, cost.USD, actual.Breakdown)
		}
	}
	return s.fail(res, entry, err)
}

func hasTokens(u google.Usage) bool {
	return u.PromptTokens+u.OutputTokens+u.ThoughtsTokens > 0
}

// billedNote tells the agent that a failed call was still charged.
func billedNote(err error, usd float64, breakdown string) error {
	note := fmt.Sprintf(" (the call still consumed %s and is billed about $%.4f)", breakdown, usd)
	if ae, ok := apperr.As(err); ok {
		c := *ae
		c.Message += note
		return &c
	}
	return fmt.Errorf("%w%s", err, note)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func btoa(b bool) string {
	if !b {
		return ""
	}
	return "true"
}

func dropWarning(dropped []string, backend string) []string {
	if len(dropped) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("ignored %s: not supported on the %s backend", strings.Join(dropped, ", "), backend)}
}
