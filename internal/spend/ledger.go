// Package spend records estimated and reconciled costs in an append-only
// JSONL ledger and enforces budget caps before expensive calls.
//
// Google does not return cost with API responses, so every figure here is
// computed from the model catalog's price table and the usage metadata the
// API does return. Authoritative numbers live in Cloud Billing / AI Studio.
package spend

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
)

// Status values for ledger entries.
const (
	StatusPending  = "pending"  // long-running job submitted, not finished
	StatusOK       = "ok"       // completed and (probably) billed
	StatusFailed   = "failed"   // API error or failed job; not billed
	StatusFiltered = "filtered" // blocked by safety filters; typically not billed
)

// Cost basis values explain how CostUSD was derived.
const (
	BasisUsage    = "usage_metadata" // tokens reported by the API x price table
	BasisUnits    = "unit_params"    // seconds/images/requests x price table
	BasisEstimate = "estimate_only"  // no reconciliation possible
	BasisUnpriced = "unpriced"       // model missing from the price table
)

// Entry is one ledger event. Several events may share an ID (a pending video
// job later settles); readers keep the last event per ID.
type Entry struct {
	ID           string         `json:"id"`
	Time         time.Time      `json:"ts"`
	Session      string         `json:"session,omitempty"`
	Tool         string         `json:"tool"`
	Model        string         `json:"model"`
	MediaType    string         `json:"mediaType,omitempty"`
	Backend      string         `json:"backend,omitempty"`
	Status       string         `json:"status"`
	EstimatedUSD float64        `json:"estimatedUsd"`
	CostUSD      float64        `json:"costUsd"`
	Basis        string         `json:"basis"`
	Units        map[string]any `json:"units,omitempty"`
	Usage        any            `json:"usage,omitempty"`
	OperationID  string         `json:"operationId,omitempty"`
	Outputs      []string       `json:"outputs,omitempty"`
	PriceAsOf    string         `json:"priceAsOf,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// Counts reports whether an entry counts toward spend.
func (e Entry) Counts() bool {
	return e.Status == StatusOK || e.Status == StatusPending
}

// Budget caps in USD; zero disables a cap.
type Budget struct {
	SessionUSD      float64
	DailyUSD        float64
	MonthlyUSD      float64
	ConfirmAboveUSD float64
}

// Ledger is safe for concurrent use and shares its file with other server
// processes: new lines written by others are picked up before budget checks.
type Ledger struct {
	path    string
	session string
	budget  Budget
	now     func() time.Time

	mu       sync.Mutex
	offset   int64
	entries  map[string]Entry // last event per ID
	reserved map[string]float64
}

// Open loads (or creates) the ledger at path.
func Open(path string, budget Budget) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating ledger directory: %w", err)
	}
	l := &Ledger{
		path:     path,
		session:  newID(),
		budget:   budget,
		now:      time.Now,
		entries:  map[string]Entry{},
		reserved: map[string]float64{},
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.refreshLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// Path returns the ledger file path.
func (l *Ledger) Path() string { return l.path }

// Session returns this process's session ID.
func (l *Ledger) Session() string { return l.session }

// Budget returns the configured caps.
func (l *Ledger) Budget() Budget { return l.budget }

// refreshLocked reads lines appended since the last read (by any process).
func (l *Ledger) refreshLocked() error {
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opening ledger: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < l.offset { // truncated or rotated
		l.offset = 0
		l.entries = map[string]Entry{}
	}
	if _, err := f.Seek(l.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			l.offset += int64(len(line))
			var e Entry
			if json.Unmarshal(bytes.TrimSpace(line), &e) == nil && e.ID != "" {
				l.entries[e.ID] = e
			}
		}
		if err != nil {
			break // io.EOF or a partial trailing line written concurrently
		}
	}
	return nil
}

func (l *Ledger) appendLocked(e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening ledger: %w", err)
	}
	defer func() { _ = f.Close() }()
	// One write call per line keeps concurrent appenders from interleaving.
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing ledger: %w", err)
	}
	// Re-read (includes our own line and anything others appended).
	return l.refreshLocked()
}

// Reservation holds estimated spend while a call is in flight so concurrent
// calls cannot jointly exceed a cap.
type Reservation struct {
	ID        string
	Estimated float64
	l         *Ledger
	done      bool
}

// Reserve checks budgets and confirmation thresholds, then reserves est.
// approvedUSD is the caller's explicit approval (0 = none).
func (l *Ledger) Reserve(est, approvedUSD float64) (*Reservation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.refreshLocked()

	if t := l.budget.ConfirmAboveUSD; t > 0 && est > t && approvedUSD+1e-9 < est {
		return nil, &apperr.Error{
			Kind:    apperr.Confirm,
			Message: fmt.Sprintf("this call is estimated at $%.2f, above the confirmation threshold of $%.2f", est, t),
			Hint:    fmt.Sprintf("Confirm the cost with the user, then retry with approvedCostUsd: %.2f (or more). Use estimate_cost to compare cheaper options (smaller resolution, shorter duration, a lighter model).", roundUp(est)),
		}
	}

	tot := l.totalsLocked()
	reserved := 0.0
	for _, v := range l.reserved {
		reserved += v
	}
	check := func(name string, cap, spent float64) error {
		if cap <= 0 || spent+reserved+est <= cap+1e-9 {
			return nil
		}
		return &apperr.Error{
			Kind:    apperr.Budget,
			Message: fmt.Sprintf("%s budget exceeded: spent $%.2f + in-flight $%.2f + this call $%.2f > cap $%.2f", name, spent, reserved, est, cap),
			Hint:    "Ask the user to raise the budget (GEMINI_MEDIA_BUDGET_* settings) or pick a cheaper option. Call get_usage for a breakdown.",
		}
	}
	if err := errors.Join(
		check("session", l.budget.SessionUSD, tot.Session),
		check("daily", l.budget.DailyUSD, tot.Today),
		check("monthly", l.budget.MonthlyUSD, tot.Month),
	); err != nil {
		if ce, ok := apperr.As(err); ok {
			return nil, ce
		}
		return nil, err
	}
	r := &Reservation{ID: newID(), Estimated: est, l: l}
	l.reserved[r.ID] = est
	return r, nil
}

// Settle records the final entry for a reservation and releases it.
func (r *Reservation) Settle(e Entry) (Entry, error) {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !r.done {
		delete(l.reserved, r.ID)
		r.done = true
	}
	e.ID = r.ID
	if e.EstimatedUSD == 0 {
		e.EstimatedUSD = r.Estimated
	}
	err := l.recordLocked(&e)
	return e, err
}

// Release drops a reservation without recording anything (e.g. validation
// failed before any API call).
func (r *Reservation) Release() {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !r.done {
		delete(l.reserved, r.ID)
		r.done = true
	}
}

// Record appends an entry (used to update pending jobs by ID).
func (l *Ledger) Record(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.ID == "" {
		e.ID = newID()
	}
	err := l.recordLocked(&e)
	return e, err
}

func (l *Ledger) recordLocked(e *Entry) error {
	if e.Time.IsZero() {
		e.Time = l.now().UTC()
	}
	if e.Session == "" {
		e.Session = l.session
	}
	return l.appendLocked(*e)
}

// Get returns the latest entry for id.
func (l *Ledger) Get(id string) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[id]
	return e, ok
}

// Totals are spend sums in USD.
type Totals struct {
	Session float64 `json:"session"`
	Today   float64 `json:"today"`
	Month   float64 `json:"month"`
	All     float64 `json:"allTime"`
	Pending float64 `json:"pending"`
}

func (l *Ledger) totalsLocked() Totals {
	now := l.now()
	y, m, d := now.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	monthStart := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	var t Totals
	for _, e := range l.entries {
		if !e.Counts() {
			continue
		}
		c := e.CostUSD
		t.All += c
		if e.Status == StatusPending {
			t.Pending += c
		}
		ts := e.Time.In(now.Location())
		if !ts.Before(dayStart) {
			t.Today += c
		}
		if !ts.Before(monthStart) {
			t.Month += c
		}
		if e.Session == l.session {
			t.Session += c
		}
	}
	return t
}

// Summary is a usage report.
type Summary struct {
	Totals    Totals             `json:"totals"`
	ByModel   map[string]float64 `json:"byModel"`
	ByTool    map[string]float64 `json:"byTool"`
	Calls     int                `json:"calls"`
	Recent    []Entry            `json:"recent,omitempty"`
	Remaining map[string]float64 `json:"budgetRemaining,omitempty"`
}

// Summarize reports spend for a period: session, today, month or all.
func (l *Ledger) Summarize(period string, recent int) Summary {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.refreshLocked()
	now := l.now()
	y, m, d := now.Date()
	var since time.Time
	switch period {
	case "today":
		since = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case "month":
		since = time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	}
	s := Summary{Totals: l.totalsLocked(), ByModel: map[string]float64{}, ByTool: map[string]float64{}}
	var list []Entry
	for _, e := range l.entries {
		if period == "session" && e.Session != l.session {
			continue
		}
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		list = append(list, e)
		if e.Counts() {
			s.Calls++
			s.ByModel[e.Model] += e.CostUSD
			s.ByTool[e.Tool] += e.CostUSD
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time.After(list[j].Time) })
	if recent > 0 && len(list) > recent {
		list = list[:recent]
	}
	s.Recent = list
	s.Remaining = map[string]float64{}
	if b := l.budget.SessionUSD; b > 0 {
		s.Remaining["session"] = b - s.Totals.Session
	}
	if b := l.budget.DailyUSD; b > 0 {
		s.Remaining["daily"] = b - s.Totals.Today
	}
	if b := l.budget.MonthlyUSD; b > 0 {
		s.Remaining["monthly"] = b - s.Totals.Month
	}
	return s
}

func roundUp(v float64) float64 {
	return float64(int64(v*100+0.999999)) / 100
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
