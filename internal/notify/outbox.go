package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/BartolottiLuca/plantation/internal/store"
)

// OutboxRepo is the persistence this package needs from
// store.NotificationRepo. Declaring it locally (rather than depending on
// *store.NotificationRepo directly) keeps internal/notify testable with a
// fake and keeps the dependency direction explicit: notify depends on
// store's public API and its Notification value type, never the reverse.
type OutboxRepo interface {
	Claim(ctx context.Context, dedupeKey, kind string) (bool, error)
	MarkSent(ctx context.Context, dedupeKey string) error
	MarkFailed(ctx context.Context, dedupeKey, lastError string) error
	RecordSkipped(ctx context.Context, dedupeKey, kind string) error
	StaleClaims(ctx context.Context, olderThan time.Time) ([]store.Notification, error)
}

// OutboxNotifier implements Notifier per SPEC.md §8's claim-then-send
// protocol: an empty Message is recorded as skipped and never reaches
// Discord; a non-empty one is claimed first, and only the caller that wins
// the claim posts, which is what makes SendOnce safe to call on every tick
// without re-alerting on the same event.
type OutboxNotifier struct {
	repo    OutboxRepo
	client  *discordClient
	nowFunc func() time.Time
}

// OutboxOption configures an OutboxNotifier at construction. Options exist
// so tests can inject a controllable sleep, clock, and HTTP client without
// this package needing internal/care's Clock or any test-only exported
// setters on the zero-value struct.
type OutboxOption func(*OutboxNotifier)

// WithSleep overrides the sleep used before a 429 retry. Defaults to
// time.Sleep.
func WithSleep(sleep func(time.Duration)) OutboxOption {
	return func(n *OutboxNotifier) { n.client.sleep = sleep }
}

// WithNow overrides the clock SweepStale uses to compute its cutoff.
// Defaults to time.Now.
func WithNow(nowFunc func() time.Time) OutboxOption {
	return func(n *OutboxNotifier) { n.nowFunc = nowFunc }
}

// WithHTTPClient overrides the HTTP client used to reach Discord (or, in
// tests, an httptest.Server). Defaults to a client with discordTimeout.
func WithHTTPClient(c *http.Client) OutboxOption {
	return func(n *OutboxNotifier) { n.client.httpClient = c }
}

// NewOutboxNotifier builds a Notifier that posts to webhookURL through repo.
// webhookURL is held only inside the unexported discordClient it builds; it
// is never copied anywhere this package logs or formats an error.
func NewOutboxNotifier(repo OutboxRepo, webhookURL string, opts ...OutboxOption) *OutboxNotifier {
	n := &OutboxNotifier{
		repo:    repo,
		client:  newDiscordClient(webhookURL),
		nowFunc: time.Now,
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

var _ Notifier = (*OutboxNotifier)(nil)

// isEmptyMessage defines "empty" for the skip rule: no title and no
// description. A Message with only a URL is still empty — Discord would
// render nothing for a human to act on, so it is not worth a claim row.
func isEmptyMessage(msg Message) bool {
	return msg.Title == "" && msg.Description == ""
}

// SendOnce implements the four-step protocol from SPEC.md §8: skip empty
// content, claim, POST, mark the outcome. A crash between Claim and the
// POST resolving leaves the row 'claimed' for SweepStale to find later; see
// SweepStale's doc comment for exactly what recovery is possible.
func (n *OutboxNotifier) SendOnce(ctx context.Context, dedupeKey, kind string, msg Message) error {
	if isEmptyMessage(msg) {
		return n.repo.RecordSkipped(ctx, dedupeKey, kind)
	}

	claimed, err := n.repo.Claim(ctx, dedupeKey, kind)
	if err != nil {
		return fmt.Errorf("claiming notification %s: %w", kind, err)
	}
	if !claimed {
		// Already claimed by an earlier call (this tick, a previous tick, or
		// another replica caught by the scheduler's advisory lock): at-most-
		// once means we stop here rather than risk a second Discord post.
		return nil
	}

	if err := n.client.send(ctx, msg); err != nil {
		lastError := err.Error() // already sanitized by discordClient
		if markErr := n.repo.MarkFailed(ctx, dedupeKey, lastError); markErr != nil {
			return fmt.Errorf("marking notification failed (send error: %s): %w", lastError, markErr)
		}
		return fmt.Errorf("sending discord notification: %w", err)
	}

	if err := n.repo.MarkSent(ctx, dedupeKey); err != nil {
		return fmt.Errorf("marking notification sent: %w", err)
	}
	return nil
}

// SweepStale retries rows still 'claimed' after olderThan, per SPEC.md §8's
// "sweep hourly, retry while attempts<3" recovery rule.
//
// THE GAP, stated precisely: store.NotificationRepo.Claim
// (internal/store/notification.go) inserts a row with `body` left at its
// column default (”) — there is no SetBody, and no richer Claim overload,
// to persist the rendered Message at claim time, and this card may not add
// one to internal/store. SendOnce above therefore cannot hand SweepStale
// anything to replay: by the time a claimed row is old enough to count as
// stale (>10 min, per SPEC.md §8), the only process that ever held the
// in-memory Message either already resolved it (MarkSent/MarkFailed, so it
// is no longer 'claimed') or has crashed — and a crash also destroys any
// in-process cache this package could keep, so caching would only create a
// false sense of recoverability without ever actually firing. This method
// therefore does NOT keep one; it works only from what StaleClaims reads
// back from Postgres.
//
// What SweepStale actually does, given that:
//   - decode row.Body as a JSON-encoded Message (the encoding SendOnce
//     WOULD need to persist if a future store change lets it). If that
//     succeeds, retry the POST from the decoded content: MarkSent on
//     success, MarkFailed on failure. This branch is dead code against
//     today's schema (Body is always "") but starts working for free the
//     day C02 or C09 closes the gap, without another change to this
//     package.
//   - if row.Body is empty or not decodable (the realistic case today):
//     there is nothing safe to send, so it calls MarkFailed immediately
//     with a descriptive last_error. This is the concrete form of "increment
//     attempts and eventually MarkFailed rather than crash or hang" against
//     a store API where MarkFailed always finalizes status to 'failed' —
//     there is no lower-severity "record an attempt but stay claimed" call,
//     so one sweep pass is all a body-less row ever gets before it leaves
//     StaleClaims' `status='claimed'` filter for good. This still honours
//     SPEC.md §8's "a failed digest must never block tomorrow's": today's
//     row ends up 'failed' with a clear reason, and the per-day dedupe key
//     means tomorrow's send is unaffected.
//
// Who should close this, and how:
//   - C09 already recomputes the actionable set every tick from the
//     database (SPEC.md §6: "derived state is computed, never stored"), so
//     for digest/frost/heatwave/rain_skip it can simply call SendOnce again
//     with a freshly-rendered Message on a later tick using the SAME
//     dedupeKey. That is safe today ONLY while the row is still 'claimed'
//     or does not exist — Claim's `ON CONFLICT DO NOTHING` makes a repeat
//     call a no-op against an existing row regardless of its status, so a
//     row this method has already marked 'failed' will NOT be retried by a
//     bare re-call to SendOnce either. Either (a) C02 changes Claim (or adds
//     a sibling) to re-return true for a stale 'claimed' or 'failed' row so
//     a fresh SendOnce can actually re-send, or (b) C09 treats "SweepStale
//     just marked this dedupe key failed" as its own signal to compute a new
//     event for the NEXT period only (which is already how the per-day key
//     behaves) and accepts that a same-day resend needs (a).
//   - Simpler alternative: C02 adds a body-at-claim-time write (e.g.
//     `ClaimWithBody(ctx, dedupeKey, kind, body string) (bool, error)`) and
//     SendOnce calls it with json.Marshal(msg); SweepStale's JSON-decode
//     branch above then starts firing for real, with no timing coordination
//     with C09 required.
func (n *OutboxNotifier) SweepStale(ctx context.Context, olderThan time.Duration) error {
	cutoff := n.nowFunc().Add(-olderThan)
	stale, err := n.repo.StaleClaims(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("listing stale claims: %w", err)
	}

	var errs []error
	for _, row := range stale {
		if row.Attempts >= 3 {
			continue
		}
		if err := n.retryStale(ctx, row); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sweeping %d of %d stale claim(s), first error: %w", len(errs), len(stale), errs[0])
	}
	return nil
}

func (n *OutboxNotifier) retryStale(ctx context.Context, row store.Notification) error {
	msg, ok := decodeBody(row.Body)
	if !ok {
		const reason = "stale claim has no recoverable message body (process restarted before send)"
		if err := n.repo.MarkFailed(ctx, row.DedupeKey, reason); err != nil {
			return fmt.Errorf("marking unrecoverable stale claim %q failed: %w", row.Kind, err)
		}
		return nil
	}

	if err := n.client.send(ctx, msg); err != nil {
		if markErr := n.repo.MarkFailed(ctx, row.DedupeKey, err.Error()); markErr != nil {
			return fmt.Errorf("marking retried claim %q failed: %w", row.Kind, markErr)
		}
		return nil
	}
	if err := n.repo.MarkSent(ctx, row.DedupeKey); err != nil {
		return fmt.Errorf("marking retried claim %q sent: %w", row.Kind, err)
	}
	return nil
}

// decodeBody is the JSON encoding SweepStale would need Body to be in, if
// something ever wrote it. Empty Body (today's reality — see SweepStale's
// doc comment) always misses.
func decodeBody(body string) (Message, bool) {
	if body == "" {
		return Message{}, false
	}
	var msg Message
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		return Message{}, false
	}
	if isEmptyMessage(msg) {
		return Message{}, false
	}
	return msg, true
}
