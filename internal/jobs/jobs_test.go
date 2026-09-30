package jobs

import (
	"errors"
	"testing"
	"time"
)

func TestRegistryRoundTrip(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := &Job{ID: NewID(), Operation: "projects/p/locations/us-central1/publishers/google/models/veo/operations/1", Model: "veo", State: StateWorking}
	if err := r.Put(j); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(j.ID)
	if err != nil || got.Operation != j.Operation || got.CreatedAt.IsZero() {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	byOp, err := r.FindByOperation(j.Operation)
	if err != nil || byOp.ID != j.ID {
		t.Fatalf("FindByOperation = %+v, %v", byOp, err)
	}
	if _, err := r.Get("job_deadbeefdeadbeef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := r.Get("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatal("malformed ids must not touch the filesystem")
	}
	if err := r.Put(&Job{ID: "bad"}); err == nil {
		t.Fatal("invalid ids must be rejected")
	}
}

func TestListOrderAndPrune(t *testing.T) {
	dir := t.TempDir()
	r, _ := Open(dir)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		r.now = func() time.Time { return base.Add(time.Duration(i) * time.Hour) }
		if err := r.Put(&Job{ID: NewID(), State: StateCompleted}); err != nil {
			t.Fatal(err)
		}
	}
	list := r.List(0)
	if len(list) != 3 || !list[0].CreatedAt.After(list[1].CreatedAt) {
		t.Fatalf("List not newest-first: %+v", list)
	}
	if len(r.List(2)) != 2 {
		t.Fatal("limit ignored")
	}
	r.now = func() time.Time { return base.Add(Retention + 48*time.Hour) }
	r.prune()
	if len(r.List(0)) != 0 {
		t.Fatal("expired jobs should be pruned")
	}
	if !IsJobID(NewID()) || IsJobID("models/veo/operations/x") {
		t.Fatal("IsJobID")
	}
}
