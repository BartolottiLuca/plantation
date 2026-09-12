package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetryRecoversFromTransientSQLSTATE(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		code      string
		transient bool
	}{
		{name: "serialization_failure", code: "40001", transient: true},
		{name: "admin_shutdown", code: "57P01", transient: true},
		{name: "connection_failure", code: "08006", transient: true},
		{name: "unique_violation", code: "23505", transient: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls int
			err := Retry(context.Background(), func(context.Context) error {
				calls++
				if calls == 1 {
					return &pgconn.PgError{Code: tc.code}
				}
				return nil
			})
			if tc.transient {
				if err != nil {
					t.Fatalf("Retry = %v, want success after transient %s", err, tc.code)
				}
				if calls != 2 {
					t.Fatalf("calls = %d, want 2", calls)
				}
				return
			}
			if err == nil {
				t.Fatal("Retry succeeded for a non-transient error")
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1 (no retry)", calls)
			}
		})
	}
}

func TestRetryStopsOnPermanentError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	err := Retry(context.Background(), func(context.Context) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Retry = %v, want %v", err, want)
	}
}

func TestIsTransient(t *testing.T) {
	t.Parallel()
	if !isTransient(&pgconn.PgError{Code: "08000"}) {
		t.Fatal("08xxx should be transient")
	}
	if isTransient(errors.New("nope")) {
		t.Fatal("plain error should not be transient")
	}
}
