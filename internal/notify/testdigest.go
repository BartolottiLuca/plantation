package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RunSendTestDigest sends a fixed, clearly-labelled test message through
// notifier so an operator can verify the webhook end-to-end from the
// send-test-digest subcommand (or the /settings/test-notification handler
// per SPEC.md §10.3's route table). It does not read plants, care state, or
// the real actionable set — computing that is C09's job, and wiring a real
// data source into this card's binary is explicitly out of scope per the
// card's "Do not" section — so the content is a single synthetic
// DigestLine that says plainly it is a test.
//
// The dedupe key is test:<unix-seconds>, using nowFunc (defaulting to
// time.Now when nil) so a test can assert on the exact key without a real
// clock dependency. Every call gets a fresh key, matching a manual "send a
// test" action: it should always actually send, never get silently
// deduped against a previous test run in the same second.
func RunSendTestDigest(ctx context.Context, notifier Notifier, baseURL string, nowFunc func() time.Time) error {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	msg := FormatDigest(baseURL, nil, []DigestLine{{
		PlantID:   uuid.Nil,
		PlantName: "Test plant",
		Task:      "water",
		Summary:   "This is a test message from plantation send-test-digest; no real plant is due.",
	}})
	msg.Title = "Plantation test digest"

	dedupeKey := fmt.Sprintf("test:%d", nowFunc().Unix())
	return notifier.SendOnce(ctx, dedupeKey, "ops", msg)
}
