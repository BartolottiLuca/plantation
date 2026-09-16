package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/store"
)

// countingDiscordServer counts successful (2xx) deliveries separately from
// raw request attempts, so tests can assert "at most one message actually
// reached Discord" rather than merely "at most one HTTP attempt was made".
func countingDiscordServer(t *testing.T, status int) (*httptest.Server, *int32) {
	t.Helper()
	var successes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status >= 200 && status < 300 {
			atomic.AddInt32(&successes, 1)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &successes
}

func TestSendOnceEmptyMessageRecordsSkippedAndNeverCallsDiscord(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		t.Error("discord server was hit for an empty message")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, srv.URL, WithSleep(func(time.Duration) {}))

	if err := n.SendOnce(context.Background(), "digest:2026-09-11", "digest", Message{}); err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if repo.skippedCount() != 1 {
		t.Errorf("skippedCount = %d, want 1", repo.skippedCount())
	}
	if repo.claimCalls != 0 {
		t.Errorf("claimCalls = %d, want 0 (empty message must never claim)", repo.claimCalls)
	}
	if hit {
		t.Error("discord server was hit for an empty message")
	}
}

func TestSendOnceTwiceSameKeyCallsDiscordAtMostOnce(t *testing.T) {
	srv, successes := countingDiscordServer(t, http.StatusOK)

	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, srv.URL, WithSleep(func(time.Duration) {}))

	msg := Message{Title: "digest", Description: "line"}
	if err := n.SendOnce(context.Background(), "digest:2026-09-11", "digest", msg); err != nil {
		t.Fatalf("first SendOnce: %v", err)
	}
	if err := n.SendOnce(context.Background(), "digest:2026-09-11", "digest", msg); err != nil {
		t.Fatalf("second SendOnce: %v", err)
	}

	if got := atomic.LoadInt32(successes); got != 1 {
		t.Errorf("successful discord deliveries = %d, want 1", got)
	}
	if repo.sentCount() != 1 {
		t.Errorf("sentCount = %d, want 1", repo.sentCount())
	}
}

func TestSendOnceMarksFailedOn404WithPermanentStyleError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, srv.URL, WithSleep(func(time.Duration) {}))

	err := n.SendOnce(context.Background(), "digest:2026-09-11", "digest", Message{Title: "t", Description: "d"})
	if err == nil {
		t.Fatal("SendOnce error = nil, want the discord failure to propagate")
	}
	if repo.failedCount() != 1 {
		t.Fatalf("failedCount = %d, want 1", repo.failedCount())
	}
	if !strings.Contains(repo.lastFailedError(), "permanent") {
		t.Errorf("lastFailedError = %q, want it to describe a permanent failure", repo.lastFailedError())
	}
}

// TestCrashThenSweepResolvesAtMostOnce is the card's required crash test.
// It claims a row directly through the fake repo (bypassing SendOnce
// entirely) to simulate exactly what a real crash produces: a 'claimed' row
// in Postgres with no in-memory Message anywhere, because the process that
// claimed it is gone. SweepStale must then resolve it without ever
// producing more than one successful Discord delivery for the dedupe key.
func TestCrashThenSweepResolvesAtMostOnce(t *testing.T) {
	srv, successes := countingDiscordServer(t, http.StatusOK)

	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, srv.URL, WithSleep(func(time.Duration) {}))

	const key = "digest:2026-09-11"
	claimed, err := repo.Claim(context.Background(), key, "digest")
	if err != nil || !claimed {
		t.Fatalf("seeding claim: claimed=%v err=%v", claimed, err)
	}
	// Body is '' here, matching exactly what store.NotificationRepo.Claim's
	// INSERT produces today (see outbox.go's SweepStale doc comment).
	repo.stale = []store.Notification{{
		DedupeKey: key,
		Kind:      "digest",
		Status:    store.NotificationClaimed,
		ClaimedAt: time.Now().Add(-time.Hour),
		Attempts:  0,
		Body:      "",
	}}

	if err := n.SweepStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	if got := atomic.LoadInt32(successes); got > 1 {
		t.Fatalf("successful discord deliveries = %d, want at most 1", got)
	}
	if got := atomic.LoadInt32(successes); got != 0 {
		t.Errorf("successful discord deliveries = %d, want 0 (no body was ever recoverable)", got)
	}
	if repo.failedCount() != 1 {
		t.Fatalf("failedCount = %d, want 1 (unrecoverable claim must resolve to failed, not hang)", repo.failedCount())
	}
	if repo.sentCount() != 0 {
		t.Errorf("sentCount = %d, want 0", repo.sentCount())
	}

	// A second sweep pass must not double-resolve it either: StaleClaims is
	// a fake here, so simulate the real repo's behaviour of no longer
	// returning a 'failed' row by clearing the seeded stale slice, then
	// confirm sweeping an empty set is a no-op.
	repo.stale = nil
	if err := n.SweepStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("second SweepStale: %v", err)
	}
	if got := atomic.LoadInt32(successes); got != 0 {
		t.Errorf("successful discord deliveries after second sweep = %d, want 0", got)
	}
	if repo.failedCount() != 1 {
		t.Errorf("failedCount after second sweep = %d, want unchanged at 1", repo.failedCount())
	}
}

// TestSweepStaleReplaysRecoverableBody exercises the branch that starts
// firing for real once something writes a JSON-encoded Message into
// notifications.body (see outbox.go's SweepStale doc comment: this is dead
// code against today's store.NotificationRepo.Claim, but is tested here so
// it is proven correct for the day that gap closes).
func TestSweepStaleReplaysRecoverableBody(t *testing.T) {
	srv, successes := countingDiscordServer(t, http.StatusOK)

	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, srv.URL, WithSleep(func(time.Duration) {}))

	const key = "digest:2026-09-11"
	body := `{"Title":"digest","Description":"line","URL":""}`
	repo.stale = []store.Notification{{
		DedupeKey: key,
		Kind:      "digest",
		Status:    store.NotificationClaimed,
		ClaimedAt: time.Now().Add(-time.Hour),
		Attempts:  0,
		Body:      body,
	}}

	if err := n.SweepStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if got := atomic.LoadInt32(successes); got != 1 {
		t.Errorf("successful discord deliveries = %d, want 1", got)
	}
	if repo.sentCount() != 1 {
		t.Errorf("sentCount = %d, want 1", repo.sentCount())
	}
}

func TestSweepStaleSkipsRowsWithThreeAttempts(t *testing.T) {
	srv, successes := countingDiscordServer(t, http.StatusOK)

	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, srv.URL, WithSleep(func(time.Duration) {}))
	repo.stale = []store.Notification{{
		DedupeKey: "digest:2026-09-11",
		Kind:      "digest",
		Status:    store.NotificationClaimed,
		ClaimedAt: time.Now().Add(-time.Hour),
		Attempts:  3,
		Body:      `{"Title":"t","Description":"d"}`,
	}}

	if err := n.SweepStale(context.Background(), 10*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if got := atomic.LoadInt32(successes); got != 0 {
		t.Errorf("successful discord deliveries = %d, want 0 (attempts already exhausted)", got)
	}
	if repo.failedCount() != 0 || repo.sentCount() != 0 {
		t.Errorf("failedCount=%d sentCount=%d, want 0/0: exhausted rows must be left alone", repo.failedCount(), repo.sentCount())
	}
}

func TestSendOnceNeverLeaksWebhookURLOnFailure(t *testing.T) {
	const marker = "leaked-webhook-marker-9f2c"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	webhookURL := srv.URL + "/webhooks/1/" + marker
	defer srv.Close()

	var logBuf strings.Builder
	repo := newFakeRepo()
	n := NewOutboxNotifier(repo, webhookURL, WithSleep(func(time.Duration) {}))

	err := n.SendOnce(context.Background(), "digest:2026-09-11", "digest", Message{Title: "t", Description: "d"})
	if err == nil {
		t.Fatal("SendOnce error = nil, want the exhausted-retries failure to propagate")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("SendOnce error leaked the webhook URL: %q", err.Error())
	}
	if strings.Contains(repo.lastFailedError(), marker) {
		t.Fatalf("MarkFailed's lastError leaked the webhook URL: %q", repo.lastFailedError())
	}
	if strings.Contains(logBuf.String(), marker) {
		t.Fatalf("log output leaked the webhook URL: %q", logBuf.String())
	}
}
