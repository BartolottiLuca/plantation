package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
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
	// draftSaved: the draft validated and is in the catalog.
	draftSaved
	// draftNeedsReview: problems survived the model's correction round, so the
	// draft cannot be stored as it is and a person has to fix it on the form.
	draftNeedsReview
	// draftExists: a species with the drafted slug is already in the catalog.
	// Nothing is overwritten.
	draftExists
	draftFailed
)

// draftJob is one drafting run. Drafting can take longer than the ~100 s a
// Cloudflare tunnel waits on a silent request, so the request that starts it
// returns at once and the page polls the job instead.
type draftJob struct {
	ID       string
	State    draftState
	Request  species.Request
	Result   species.Result
	Err      error
	Existing *domain.Species // set for draftExists
	Started  time.Time
}

// draftOutcome is what a finished run reports back to the store.
type draftOutcome struct {
	State    draftState
	Result   species.Result
	Err      error
	Existing *domain.Species
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

// start registers a pending job and runs it in the background. run does the
// whole job — drafting and, when the draft is clean, saving it — because the
// request that started it has already returned and nobody is waiting to save.
func (d *draftStore) start(now time.Time, req species.Request, run func(context.Context) draftOutcome) (string, error) {
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
		// The request that started this has already returned, so the job gets its
		// own context; the drafter applies its own overall timeout.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("species draft panicked", "panic", fmt.Sprint(r))
				d.finish(id, draftOutcome{State: draftFailed, Err: fmt.Errorf("drafting panicked: %v", r)})
			}
		}()
		d.finish(id, run(context.Background()))
	}()
	return id, nil
}

func (d *draftStore) finish(id string, out draftOutcome) {
	d.mu.Lock()
	defer d.mu.Unlock()
	job, ok := d.jobs[id]
	if !ok {
		return
	}
	job.State, job.Result, job.Err, job.Existing = out.State, out.Result, out.Err, out.Existing
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
