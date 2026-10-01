package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/species"
)

const (
	// A draft that nobody looks at again is dropped after this long. The result is
	// only useful while a person is deciding, and the job holds nothing durable.
	draftTTL = 30 * time.Minute

	// Ten drafts an hour is generous for a household adding the odd plant, and it
	// bounds the bill if a page is refreshed in a loop or someone past the access
	// layer finds the button. Single replica, so an in-process bucket is exact.
	draftsPerHour = 10
)

type draftState int

const (
	draftPending draftState = iota
	draftDone
	draftFailed
)

// draftJob is one drafting run. Drafting can take longer than the ~100 s a
// Cloudflare tunnel waits on a silent request, so the request that starts it
// returns at once and the page polls the job instead.
type draftJob struct {
	ID      string
	State   draftState
	Request species.Request
	Result  species.Result
	Err     error
	Started time.Time
}

type draftStore struct {
	mu   sync.Mutex
	jobs map[string]*draftJob
}

func newDraftStore() *draftStore {
	return &draftStore{jobs: map[string]*draftJob{}}
}

func newDraftID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating draft id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// start registers a pending job and runs the draft in the background.
func (d *draftStore) start(now time.Time, drafter species.Drafter, req species.Request) (string, error) {
	id, err := newDraftID()
	if err != nil {
		return "", err
	}
	job := &draftJob{ID: id, State: draftPending, Request: req, Started: now}

	d.mu.Lock()
	for k, j := range d.jobs {
		if now.Sub(j.Started) > draftTTL {
			delete(d.jobs, k)
		}
	}
	d.jobs[id] = job
	d.mu.Unlock()

	go func() {
		// The request that started this has already returned, so the draft gets its
		// own context; the drafter applies its own overall timeout.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("species draft panicked", "panic", fmt.Sprint(r))
				d.finish(id, species.Result{}, fmt.Errorf("drafting panicked: %v", r))
			}
		}()
		res, err := drafter.Draft(context.Background(), req)
		d.finish(id, res, err)
	}()
	return id, nil
}

func (d *draftStore) finish(id string, res species.Result, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	job, ok := d.jobs[id]
	if !ok {
		return
	}
	job.Result, job.Err = res, err
	job.State = draftDone
	if err != nil {
		job.State = draftFailed
	}
}

// get returns a copy so a caller never races the goroutine that fills the job.
func (d *draftStore) get(id string) (draftJob, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	job, ok := d.jobs[id]
	if !ok {
		return draftJob{}, false
	}
	return *job, true
}

// rateLimiter is a token bucket.
type rateLimiter struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	perHour  float64
	last     time.Time
}

func newRateLimiter(perHour int) *rateLimiter {
	return &rateLimiter{tokens: float64(perHour), capacity: float64(perHour), perHour: float64(perHour)}
}

func (l *rateLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() && now.After(l.last) {
		l.tokens += now.Sub(l.last).Hours() * l.perHour
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
