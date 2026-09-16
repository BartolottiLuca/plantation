package notify

import (
	"context"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/store"
)

// fakeRepo is an in-memory OutboxRepo for tests, standing in for
// *store.NotificationRepo per the card's "depend on store only through a
// small local interface" requirement.
type fakeRepo struct {
	mu sync.Mutex

	claimed    map[string]bool
	claimCalls int

	sent    []string
	failed  []failedCall
	skipped []skippedCall

	// stale is returned verbatim by StaleClaims; tests seed it directly to
	// simulate a claim that happened in a different (crashed) process.
	stale []store.Notification
}

type failedCall struct {
	dedupeKey string
	lastError string
}

type skippedCall struct {
	dedupeKey string
	kind      string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{claimed: make(map[string]bool)}
}

func (r *fakeRepo) Claim(_ context.Context, dedupeKey, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimCalls++
	if r.claimed[dedupeKey] {
		return false, nil
	}
	r.claimed[dedupeKey] = true
	return true, nil
}

func (r *fakeRepo) MarkSent(_ context.Context, dedupeKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, dedupeKey)
	return nil
}

func (r *fakeRepo) MarkFailed(_ context.Context, dedupeKey, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, failedCall{dedupeKey: dedupeKey, lastError: lastError})
	return nil
}

func (r *fakeRepo) RecordSkipped(_ context.Context, dedupeKey, kind string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skipped = append(r.skipped, skippedCall{dedupeKey: dedupeKey, kind: kind})
	return nil
}

func (r *fakeRepo) StaleClaims(_ context.Context, _ time.Time) ([]store.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]store.Notification, len(r.stale))
	copy(out, r.stale)
	return out, nil
}

func (r *fakeRepo) sentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *fakeRepo) failedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.failed)
}

func (r *fakeRepo) lastFailedError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failed) == 0 {
		return ""
	}
	return r.failed[len(r.failed)-1].lastError
}

func (r *fakeRepo) skippedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.skipped)
}
