package notify

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeNotifier records SendOnce calls without touching Discord or a repo.
type fakeNotifier struct {
	dedupeKey string
	kind      string
	msg       Message
	calls     int
}

func (f *fakeNotifier) SendOnce(_ context.Context, dedupeKey, kind string, msg Message) error {
	f.calls++
	f.dedupeKey, f.kind, f.msg = dedupeKey, kind, msg
	return nil
}

func TestRunSendTestDigestUsesTestDedupeKeyAndNonEmptyMessage(t *testing.T) {
	fixed := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	n := &fakeNotifier{}

	if err := RunSendTestDigest(context.Background(), n, "https://example.com", func() time.Time { return fixed }); err != nil {
		t.Fatalf("RunSendTestDigest: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("SendOnce called %d times, want 1", n.calls)
	}
	want := fmt.Sprintf("test:%d", fixed.Unix())
	if n.dedupeKey != want {
		t.Errorf("dedupeKey = %q, want %q", n.dedupeKey, want)
	}
	if isEmptyMessage(n.msg) {
		t.Error("RunSendTestDigest sent an empty message")
	}
}

func TestRunSendTestDigestDefaultsNowFuncWhenNil(t *testing.T) {
	n := &fakeNotifier{}
	before := time.Now().Unix()
	if err := RunSendTestDigest(context.Background(), n, "https://example.com", nil); err != nil {
		t.Fatalf("RunSendTestDigest: %v", err)
	}
	after := time.Now().Unix()
	want := fmt.Sprintf("test:%d", before)
	wantAlt := fmt.Sprintf("test:%d", after)
	if n.dedupeKey != want && n.dedupeKey != wantAlt {
		t.Errorf("dedupeKey = %q, want %q or %q", n.dedupeKey, want, wantAlt)
	}
}
