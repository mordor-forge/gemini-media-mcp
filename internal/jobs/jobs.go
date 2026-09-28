// Package jobs persists long-running generation jobs (video) as opaque
// handles so that status checks survive server restarts and never depend on
// parsing provider operation names (which differ between the Gemini API and
// Vertex AI).
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// Job states, aligned with the MCP Tasks extension vocabulary so a Tasks
// adapter can be added once SDKs and clients support it.
const (
	StateWorking   = "working"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateFiltered  = "filtered" // blocked by safety filters
)

// Retention is how long job records are kept on disk.
const Retention = 30 * 24 * time.Hour

// Job is a persisted long-running generation.
type Job struct {
	ID          string         `json:"id"`
	Operation   string         `json:"operation"`
	Tool        string         `json:"tool"`
	Model       string         `json:"model"`
	Location    string         `json:"location,omitempty"`
	Backend     string         `json:"backend"`
	Prompt      string         `json:"prompt,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	OutputName  string         `json:"outputName,omitempty"`
	LedgerID    string         `json:"ledgerId,omitempty"`
	EstimateUSD float64        `json:"estimateUsd,omitempty"`
	State       string         `json:"state"`
	Error       string         `json:"error,omitempty"`
	Outputs     []store.Asset  `json:"outputs,omitempty"`
	ParentID    string         `json:"parentId,omitempty"`
	// DownloadAttempts counts failed downloads of a finished operation.
	DownloadAttempts int        `json:"downloadAttempts,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
	// RemoteExpiresAt is when the provider deletes the generated output
	// (Gemini API keeps videos for two days).
	RemoteExpiresAt *time.Time `json:"remoteExpiresAt,omitempty"`
}

// Done reports whether the job reached a terminal state.
func (j *Job) Done() bool { return j.State != StateWorking }

// Registry stores one JSON file per job, which keeps concurrent server
// processes from clobbering each other.
type Registry struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// Open creates the registry directory and prunes expired records.
func Open(dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating jobs directory: %w", err)
	}
	r := &Registry{dir: dir, now: time.Now}
	r.prune()
	return r, nil
}

// NewID returns an opaque, unguessable job handle.
func NewID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("job_%x", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(b)
}

var idRe = regexp.MustCompile(`^job_[0-9a-f]{8,64}$`)

// IsJobID reports whether s looks like a handle issued by this server.
func IsJobID(s string) bool { return idRe.MatchString(s) }

// Put writes the job atomically.
func (r *Registry) Put(j *Job) error {
	if !IsJobID(j.ID) {
		return fmt.Errorf("invalid job id %q", j.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), r.path(j.ID))
}

// ErrNotFound is returned for unknown handles.
var ErrNotFound = errors.New("job not found")

// Get loads a job by handle.
func (r *Registry) Get(id string) (*Job, error) {
	if !IsJobID(id) {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(r.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("corrupt job record %s: %w", id, err)
	}
	return &j, nil
}

// FindByOperation returns the job tracking a provider operation name.
func (r *Registry) FindByOperation(op string) (*Job, error) {
	for _, j := range r.List(0) {
		if j.Operation == op {
			return j, nil
		}
	}
	return nil, ErrNotFound
}

// List returns jobs newest first (n <= 0 means all).
func (r *Registry) List(n int) []*Job {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	var out []*Job
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		j, err := r.Get(strings.TrimSuffix(name, ".json"))
		if err == nil {
			out = append(out, j)
		}
	}
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].CreatedAt.After(out[k-1].CreatedAt); k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func (r *Registry) path(id string) string { return filepath.Join(r.dir, id+".json") }

func (r *Registry) prune() {
	cutoff := r.now().Add(-Retention)
	for _, j := range r.List(0) {
		if j.UpdatedAt.Before(cutoff) {
			_ = os.Remove(r.path(j.ID))
		}
	}
}
