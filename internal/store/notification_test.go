package store

import (
	"context"
	"testing"
	"time"
)

func TestClaimReturnsFalseForExistingKey(t *testing.T) {
	repo := NewNotificationRepo(migratedPool(t))
	ctx := context.Background()

	cases := []struct {
		name string
		key  string
		kind string
	}{
		{name: "digest", key: "digest:2026-09-11", kind: "digest"},
		{name: "ops", key: "ops:weather_stale:2026-09-11", kind: "ops"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := repo.Claim(ctx, tc.key, tc.kind)
			if err != nil {
				t.Fatalf("first Claim: %v", err)
			}
			if !first {
				t.Fatal("first Claim = false, want true")
			}
			second, err := repo.Claim(ctx, tc.key, tc.kind)
			if err != nil {
				t.Fatalf("second Claim: %v", err)
			}
			if second {
				t.Fatal("second Claim = true, want false")
			}
		})
	}
}

func TestStaleClaimsAndStatusTransitions(t *testing.T) {
	pool := migratedPool(t)
	repo := NewNotificationRepo(pool)
	ctx := context.Background()

	claimed, err := repo.Claim(ctx, "digest:2026-09-12", "digest")
	if err != nil || !claimed {
		t.Fatalf("Claim: claimed=%v err=%v", claimed, err)
	}
	if err := repo.RecordSkipped(ctx, "digest:2026-09-12", "digest"); err != nil {
		t.Fatalf("RecordSkipped on existing key: %v", err)
	}

	stale, err := repo.StaleClaims(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("StaleClaims: %v", err)
	}
	if len(stale) != 1 || stale[0].DedupeKey != "digest:2026-09-12" {
		t.Fatalf("StaleClaims = %+v, want the claimed digest", stale)
	}

	if err := repo.MarkSent(ctx, "digest:2026-09-12"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	stale, err = repo.StaleClaims(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("StaleClaims after sent: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("StaleClaims after sent = %d, want 0", len(stale))
	}
}
