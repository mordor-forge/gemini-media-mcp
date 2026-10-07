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
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/filelock"
)

// Status values for ledger entries.
const (
	StatusPending  = "pending"  // long-running job submitted, not finished
	StatusOK       = "ok"       // completed and (probably) billed
	StatusFailed   = "failed"   // API error or failed job; not billed
	StatusFiltered = "filtered" // blocked by safety filters; typically not billed

	// Budget holds, not spend. A reservation is written before an API call
	// so every process sharing the ledger sees it, and is superseded by the
	// call's final entry (same ID) or by a release.
	StatusReserved = "reserved"
	StatusReleased = "released"
)

// DefaultReservationTTL bounds how long an unsettled reservation (from a
// process that crashed mid-call) keeps holding budget.
const DefaultReservationTTL = time.Hour

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
	ExpiresAt    *time.Time     `json:"expiresAt,omitempty"` // reservations only
}

// Counts reports whether an entry counts toward spend.
func (e Entry) Counts() bool {
	return e.Status == StatusOK || e.Status == StatusPending
}

// hold reports whether a reservation entry still holds budget at now.
func (e Entry) hold(now time.Time) bool {
	return e.Status == StatusReserved && e.ExpiresAt != nil && now.Before(*e.ExpiresAt)
}

// bookkeeping reports whether an entry is a budget hold rather than a call.
func (e Entry) bookkeeping() bool {
	return e.Status == StatusReserved || e.Status == StatusReleased
}

// Budget caps in USD; zero disables a cap.
type Budget struct {
	SessionUSD      float64 `json:"sessionUsd"`
	DailyUSD        float64 `json:"dailyUsd"`
	MonthlyUSD      float64 `json:"monthlyUsd"`
	ConfirmAboveUSD float64 `json:"confirmAboveUsd"`
}

// Ledger is safe for concurrent use and shares its file with other server
// processes. Writers (reservations, settlements, job updates) hold an
// exclusive lock on <path>.lock, so a budget check and the reservation it
// admits are atomic across processes; readers pick up new lines lazily.
type Ledger struct {
	path     string
	lockPath string
	session  string
	budget   Budget
	ttl      time.Duration
	now      func() time.Time

	mu      sync.Mutex
	offset  int64
	entries map[string]Entry // last event per ID
}

// Open loads (or creates) the ledger at path.
func Open(path string, budget Budget) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating ledger directory: %w", err)
	}
	l := &Ledger{
		path:     path,
		lockPath: path + ".lock",
		session:  newID(),
		budget:   budget,
		ttl:      DefaultReservationTTL,
		now:      time.Now,
		entries:  map[string]Entry{},
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

// SetReservationTTL sets how long a reservation holds budget if its process
// dies before settling it. It must exceed the longest possible API call
// (request timeout x retry attempts), or a slow call's hold could lapse.
func (l *Ledger) SetReservationTTL(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d > 0 {
		l.ttl = d
	}
}

// lockedLocked runs fn holding the cross-process lock, after catching up
// with lines other processes appended. l.mu must be held.
func (l *Ledger) lockedLocked(fn func() error) error {
	lk, err := filelock.Acquire(l.lockPath)
	if err != nil {
		return fmt.Errorf("locking usage ledger: %w", err)
	}
	defer func() { _ = lk.Unlock() }()
	if err := l.refreshLocked(); err != nil {
		return err
	}
	return fn()
}

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
			if e, ok := parseLine(line); ok {
				l.entries[e.ID] = e
			}
		}
		if err != nil {
			break // io.EOF or a partial trailing line written concurrently
		}
	}
	return nil
}

// entryPrefix starts every marshalled Entry (ID is its first field).
var entryPrefix = []byte(`{"id":`)

// parseLine decodes one ledger line. A line that fails to parse may be an
// interrupted write with a complete entry appended to it by an older version
// (which did not repair tails); the last embedded entry is salvaged.
func parseLine(line []byte) (Entry, bool) {
	line = bytes.TrimSpace(line)
	var e Entry
	if json.Unmarshal(line, &e) == nil && e.ID != "" {
		return e, true
	}
	if i := bytes.LastIndex(line, entryPrefix); i > 0 {
		e = Entry{}
		if json.Unmarshal(line[i:], &e) == nil && e.ID != "" {
			return e, true
		}
	}
	return Entry{}, false
}

// appendLocked writes one entry and reads it back. The caller holds the
// cross-process lock, so a last line without a newline cannot be a write in
// progress: it is the tail of an interrupted one, and is terminated first so
// the new entry starts on a line of its own.
func (l *Ledger) appendLocked(e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening ledger: %w", err)
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	// One write call per line keeps appenders from interleaving.
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing ledger: %w", err)
	}
	// Re-read (includes our own line and anything others appended) and make
	// sure the entry is really there: a recorded cost that readers cannot see
	// would silently disappear from every total.
	if err := l.refreshLocked(); err != nil {
		return err
	}
	if got, ok := l.entries[e.ID]; !ok || got.Status != e.Status || !got.Time.Equal(e.Time) {
		return fmt.Errorf("ledger entry %s was written to %s but cannot be read back", e.ID, l.path)
	}
	return nil
}

// Reservation holds estimated spend while a call is in flight so concurrent
// calls, in this or any other process sharing the ledger, cannot jointly
// exceed a cap.
type Reservation struct {
	ID        string
	Estimated float64
	l         *Ledger
	done      bool
}

// Reserve checks budgets and confirmation thresholds, then reserves est.
// approvedUSD is the caller's explicit approval (0 = none). The check and the
// reservation happen under the ledger's cross-process lock.
func (l *Ledger) Reserve(est, approvedUSD float64) (*Reservation, error) {
	if t := l.budget.ConfirmAboveUSD; t > 0 && est > t && approvedUSD+1e-9 < est {
		return nil, &apperr.Error{
			Kind:    apperr.Confirm,
			Message: fmt.Sprintf("this call is estimated at $%.2f, above the confirmation threshold of $%.2f", est, t),
			Hint:    fmt.Sprintf("Confirm the cost with the user, then retry with approvedCostUsd: %.2f (or more). Use estimate_cost to compare cheaper options (smaller resolution, shorter duration, a lighter model).", roundUp(est)),
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	var r *Reservation
	err := l.lockedLocked(func() error {
		if err := l.overBudgetLocked(est); err != nil {
			return err
		}
		now := l.now().UTC()
		exp := now.Add(l.ttl)
		hold := Entry{ID: newID(), Time: now, Session: l.session, Status: StatusReserved, EstimatedUSD: est, Basis: BasisEstimate, ExpiresAt: &exp}
		if err := l.appendLocked(hold); err != nil {
			return err
		}
		r = &Reservation{ID: hold.ID, Estimated: est, l: l}
		return nil
	})
	if err != nil {
		if ce, ok := apperr.As(err); ok {
			return nil, ce
		}
		return nil, &apperr.Error{
			Kind:    apperr.Unavailable,
			Message: "could not reserve budget in the usage ledger: " + err.Error(),
			Hint:    "Check that the state directory (GEMINI_MEDIA_STATE_DIR) is writable, then retry.",
			Cause:   err,
		}
	}
	return r, nil
}

// overBudgetLocked returns the [budget] error Reserve would give a call
// estimated at est, or nil. The caller holds l.mu with entries refreshed.
func (l *Ledger) overBudgetLocked(est float64) error {
	tot := l.totalsLocked()
	check := func(name string, cap, spent, held float64) error {
		if cap <= 0 || spent+held+est <= cap+1e-9 {
			return nil
		}
		return &apperr.Error{
			Kind:    apperr.Budget,
			Message: fmt.Sprintf("%s budget exceeded: spent $%.2f + in-flight $%.2f + this call $%.2f > cap $%.2f", name, spent, held, est, cap),
			Hint:    "Ask the user to raise the budget (GEMINI_MEDIA_BUDGET_* settings) or pick a cheaper option. Call get_usage for a breakdown.",
		}
	}
	return errors.Join(
		check("session", l.budget.SessionUSD, tot.Session, tot.inFlightSession),
		check("daily", l.budget.DailyUSD, tot.Today, tot.InFlight),
		check("monthly", l.budget.MonthlyUSD, tot.Month, tot.InFlight),
	)
}

// Check reports, without reserving anything, why a call estimated at est
// would be refused by a budget right now ("" when it would be admitted).
func (l *Ledger) Check(est float64) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.refreshLocked()
	err := l.overBudgetLocked(est)
	if ce, ok := apperr.As(err); ok {
		return ce.Message
	}
	return ""
}

// Settle records the final entry for a reservation, which also releases it.
func (r *Reservation) Settle(e Entry) (Entry, error) {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	r.done = true
	e.ID = r.ID
	if e.EstimatedUSD == 0 {
		e.EstimatedUSD = r.Estimated
	}
	err := l.recordLocked(&e)
	return e, err
}

// Release drops a reservation without recording a call (e.g. validation
// failed before any API call). If the release cannot be written the hold
// lapses after the reservation TTL.
func (r *Reservation) Release() {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	_ = l.recordLocked(&Entry{ID: r.ID, Status: StatusReleased, Basis: BasisEstimate})
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
	return l.lockedLocked(func() error { return l.appendLocked(*e) })
}

// Get returns the latest entry for id.
func (l *Ledger) Get(id string) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.refreshLocked() // pick up entries written by other processes
	e, ok := l.entries[id]
	return e, ok
}

// Totals are spend sums in USD.
type Totals struct {
	Session  float64 `json:"session"`
	Today    float64 `json:"today"`
	Month    float64 `json:"month"`
	All      float64 `json:"allTime"`
	Pending  float64 `json:"pending"`
	InFlight float64 `json:"inFlight"` // reserved by calls still running, all processes

	inFlightSession float64 // this process's share of InFlight
}

func (l *Ledger) totalsLocked() Totals {
	now := l.now()
	y, m, d := now.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	monthStart := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	var t Totals
	for _, e := range l.entries {
		if e.hold(now) {
			t.InFlight += e.EstimatedUSD
			if e.Session == l.session {
				t.inFlightSession += e.EstimatedUSD
			}
			continue
		}
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
		if e.bookkeeping() || period == "session" && e.Session != l.session {
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
	// Money is reported to 4 decimals; sums of float costs otherwise show
	// noise such as 0.24070000000000003.
	for _, f := range []*float64{&s.Totals.Session, &s.Totals.Today, &s.Totals.Month, &s.Totals.All, &s.Totals.Pending, &s.Totals.InFlight} {
		*f = round4(*f)
	}
	for _, m := range []map[string]float64{s.ByModel, s.ByTool, s.Remaining} {
		for k, v := range m {
			m[k] = round4(v)
		}
	}
	return s
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

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
