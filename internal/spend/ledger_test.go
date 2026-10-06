package spend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
)

func TestReserveSettleAndTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	l, err := Open(path, Budget{DailyUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Reserve(0.4, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent call that would exceed the cap together with the in-flight one.
	if _, err := l.Reserve(0.7, 0); apperr.KindOf(err) != apperr.Budget {
		t.Fatalf("expected budget error while 0.4 is reserved, got %v", err)
	}
	// Check predicts the same outcome without reserving.
	if msg := l.Check(0.7); !strings.Contains(msg, "daily budget exceeded") || !strings.Contains(msg, "in-flight $0.40") {
		t.Fatalf("Check(0.7) = %q", msg)
	}
	if msg := l.Check(0.5); msg != "" {
		t.Fatalf("Check(0.5) = %q, want admitted", msg)
	}
	e, err := r.Settle(Entry{Tool: "generate_image", Model: "m", Status: StatusOK, CostUSD: 0.35, Basis: BasisUsage})
	if err != nil {
		t.Fatal(err)
	}
	if e.EstimatedUSD != 0.4 || e.ID == "" || e.Session != l.Session() {
		t.Fatalf("settled entry = %+v", e)
	}
	s := l.Summarize("today", 10)
	if s.Totals.Today != 0.35 || s.Totals.Session != 0.35 || s.ByModel["m"] != 0.35 || s.Calls != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if got := s.Remaining["daily"]; got < 0.649 || got > 0.651 {
		t.Fatalf("remaining = %v", got)
	}

	// Failed calls don't count.
	r2, _ := l.Reserve(0.1, 0)
	_, _ = r2.Settle(Entry{Tool: "t", Model: "m", Status: StatusFailed, Error: "boom"})
	if l.Summarize("all", 0).Totals.All != 0.35 {
		t.Fatal("failed entries must not count toward spend")
	}
}

func TestSummaryRoundsMoneyAndBudgetUsesCamelCase(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "usage.jsonl"), Budget{DailyUSD: 0.6})
	if err != nil {
		t.Fatal(err)
	}
	// 0.0407 + 0.2 sums to 0.24070000000000003 in float64.
	for _, c := range []float64{0.0407, 0.2} {
		r, _ := l.Reserve(c, 0)
		_, _ = r.Settle(Entry{Tool: "t", Model: "m", Status: StatusOK, CostUSD: c})
	}
	s := l.Summarize("all", 0)
	if s.Totals.All != 0.2407 || s.ByModel["m"] != 0.2407 || s.Remaining["daily"] != 0.3593 {
		t.Fatalf("summary not rounded: %+v", s)
	}
	b, _ := json.Marshal(l.Budget())
	if string(b) != `{"sessionUsd":0,"dailyUsd":0.6,"monthlyUsd":0,"confirmAboveUsd":0}` {
		t.Fatalf("budget JSON = %s", b)
	}
}

func TestConfirmationThreshold(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "u.jsonl"), Budget{ConfirmAboveUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.Reserve(3.2, 0)
	if apperr.KindOf(err) != apperr.Confirm {
		t.Fatalf("want confirmation error, got %v", err)
	}
	if _, err := l.Reserve(3.2, 2); apperr.KindOf(err) != apperr.Confirm {
		t.Fatal("insufficient approval must still require confirmation")
	}
	r, err := l.Reserve(3.2, 3.2)
	if err != nil {
		t.Fatalf("approved call rejected: %v", err)
	}
	r.Release()
	if _, err := l.Reserve(0.5, 0); err != nil {
		t.Fatalf("cheap call must not need confirmation: %v", err)
	}
}

func TestPendingJobUpdatesAndCrossProcessVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	a, _ := Open(path, Budget{})
	b, _ := Open(path, Budget{MonthlyUSD: 5})

	r, _ := a.Reserve(3.2, 0)
	e, _ := r.Settle(Entry{Tool: "generate_video", Model: "veo", Status: StatusPending, CostUSD: 3.2, Basis: BasisUnits, OperationID: "op"})

	// Process b sees a's pending spend before admitting a new call.
	if _, err := b.Reserve(2, 0); apperr.KindOf(err) != apperr.Budget {
		t.Fatalf("other process's spend not visible: %v", err)
	}
	// The job fails; it stops counting.
	e.Status, e.CostUSD = StatusFailed, 0
	if _, err := a.Record(e); err != nil {
		t.Fatal(err)
	}
	r2, err := b.Reserve(2, 0)
	if err != nil {
		t.Fatalf("failed job should free the budget: %v", err)
	}
	r2.Release()
	got, ok := a.Get(e.ID)
	if !ok || got.Status != StatusFailed {
		t.Fatalf("Get = %+v", got)
	}
	// An entry written by another process is visible to Get without a Reserve first.
	r3, _ := a.Reserve(0.1, 0)
	fromA, _ := r3.Settle(Entry{Tool: "t", Model: "m", Status: StatusPending, CostUSD: 0.1})
	if _, ok := b.Get(fromA.ID); !ok {
		t.Fatal("Get must refresh from disk")
	}
}

func TestLedgerToleratesCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	if err := os.WriteFile(path, []byte("not json\n{\"id\":\"x\",\"ts\":\"2026-01-01T00:00:00Z\",\"tool\":\"t\",\"model\":\"m\",\"status\":\"ok\",\"costUsd\":1,\"basis\":\"unit_params\"}\n{\"partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Summarize("all", 0).Totals.All != 1 {
		t.Fatal("valid line should be loaded despite corrupt neighbours")
	}
}

func TestConcurrentReservationsNeverExceedCap(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "u.jsonl"), Budget{SessionUSD: 1})
	l.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.Reserve(0.3, 0)
			if err != nil {
				return
			}
			mu.Lock()
			admitted++
			mu.Unlock()
			_, _ = r.Settle(Entry{Tool: "t", Model: "m", Status: StatusOK, CostUSD: 0.3})
		}()
	}
	wg.Wait()
	if admitted != 3 {
		t.Fatalf("admitted %d calls of $0.30 under a $1 cap, want 3", admitted)
	}
}

// Two server processes sharing one ledger must not both admit calls that
// only fit the cap individually (regression: reservations were process-local).
func TestReservationsAreSharedAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	a, _ := Open(path, Budget{DailyUSD: 1})
	b, _ := Open(path, Budget{DailyUSD: 1})

	ra, err := a.Reserve(0.6, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(0.6, 0); apperr.KindOf(err) != apperr.Budget {
		t.Fatalf("b admitted $0.60 while a holds $0.60 of a $1 cap: %v", err)
	}
	if got := b.Summarize("today", 10); got.Totals.InFlight != 0.6 || got.Totals.Today != 0 || len(got.Recent) != 0 {
		t.Fatalf("in-flight hold must be reported separately from spend: %+v", got)
	}
	if _, err := ra.Settle(Entry{Tool: "t", Model: "m", Status: StatusOK, CostUSD: 0.6}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(0.6, 0); apperr.KindOf(err) != apperr.Budget {
		t.Fatalf("settled spend must keep counting: %v", err)
	}
	// A released reservation frees the budget for the other process.
	ra2, err := a.Reserve(0.3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(0.3, 0); apperr.KindOf(err) != apperr.Budget {
		t.Fatalf("second hold not visible: %v", err)
	}
	ra2.Release()
	rb, err := b.Reserve(0.3, 0)
	if err != nil {
		t.Fatalf("released hold still counted: %v", err)
	}
	rb.Release()

	// The session cap only counts this process's holds.
	c, _ := Open(path, Budget{SessionUSD: 0.5})
	if _, err := a.Reserve(0.3, 0); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Reserve(0.4, 0)
	if err != nil {
		t.Fatalf("another process's hold must not count against this session: %v", err)
	}
	rc.Release()
}

func TestConcurrentProcessesNeverExceedCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	day := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 12; i++ {
		l, err := Open(path, Budget{DailyUSD: 1}) // one ledger per "process"
		if err != nil {
			t.Fatal(err)
		}
		l.now = func() time.Time { return day }
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.Reserve(0.3, 0)
			if err != nil {
				return
			}
			mu.Lock()
			admitted++
			mu.Unlock()
			if _, err := r.Settle(Entry{Tool: "t", Model: "m", Status: StatusOK, CostUSD: 0.3}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if admitted != 3 {
		t.Fatalf("admitted %d calls of $0.30 under a $1 daily cap across processes, want 3", admitted)
	}
}

func TestAbandonedReservationExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	crashed, _ := Open(path, Budget{DailyUSD: 1})
	crashed.now = func() time.Time { return now }
	if _, err := crashed.Reserve(0.8, 0); err != nil { // never settled
		t.Fatal(err)
	}
	l, _ := Open(path, Budget{DailyUSD: 1})
	l.now = func() time.Time { return now.Add(10 * time.Minute) }
	if _, err := l.Reserve(0.5, 0); apperr.KindOf(err) != apperr.Budget {
		t.Fatalf("live hold ignored: %v", err)
	}
	l.now = func() time.Time { return now.Add(DefaultReservationTTL + time.Minute) }
	if _, err := l.Reserve(0.5, 0); err != nil {
		t.Fatalf("expired hold still blocks the budget: %v", err)
	}
}

// An interrupted write leaves a line without a newline; the next settlement
// must not be glued onto it and lost (regression).
func TestIncompleteTailIsRepairedBeforeAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	valid := `{"id":"x","ts":"2026-09-28T10:00:00Z","tool":"t","model":"m","status":"ok","costUsd":0.25,"basis":"unit_params"}` + "\n"
	if err := os.WriteFile(path, []byte(valid+`{"id":"torn","ts":"2026-09-28T11:00:00Z","tool":"t","mod`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	r, err := l.Reserve(0.6, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(Entry{Tool: "t", Model: "m", Status: StatusOK, CostUSD: 0.6}); err != nil {
		t.Fatal(err)
	}
	if got := l.Summarize("all", 0).Totals.All; got < 0.849 || got > 0.851 {
		t.Fatalf("total = %v, want 0.85 (the settlement was lost)", got)
	}
	// A fresh reader agrees.
	fresh, _ := Open(path, Budget{})
	if got := fresh.Summarize("all", 0).Totals.All; got < 0.849 || got > 0.851 {
		t.Fatalf("fresh reader total = %v, want 0.85", got)
	}
}

// Ledgers written by older versions may already hold a settlement glued onto
// a torn line; it is salvaged.
func TestGluedEntryIsSalvaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	glued := `{"id":"torn","ts":"2026-09-28T11:00:00Z","tool":"t","mod` +
		`{"id":"y","ts":"2026-09-28T11:01:00Z","tool":"t","model":"m","status":"ok","costUsd":0.6,"basis":"usage_metadata"}` + "\n"
	if err := os.WriteFile(path, []byte(glued), 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := Open(path, Budget{})
	if got := l.Summarize("all", 0).Totals.All; got != 0.6 {
		t.Fatalf("total = %v, want the glued $0.60 settlement", got)
	}
}
