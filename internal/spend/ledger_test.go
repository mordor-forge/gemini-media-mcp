package spend

import (
	"os"
	"path/filepath"
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
