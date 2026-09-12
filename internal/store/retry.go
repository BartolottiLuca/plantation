package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	retryAttempts     = 5
	retryBackoffStart = 50 * time.Millisecond
	retryBackoffCap   = 2 * time.Second
)

// Retry runs op until it succeeds, ctx is cancelled, or the error is not a
// transient SQLSTATE (08xxx connection exception, 57P01 admin_shutdown, 40001
// serialization_failure). Writes in this package are upserts or append-only
// inserts, so a retry is safe.
func Retry(ctx context.Context, op func(context.Context) error) error {
	backoff := retryBackoffStart
	var err error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		err = op(ctx)
		if err == nil || !isTransient(err) {
			return err
		}
		if attempt == retryAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > retryBackoffCap {
			backoff = retryBackoffCap
		}
	}
	return err
}

func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if strings.HasPrefix(pgErr.Code, "08") {
		return true
	}
	switch pgErr.Code {
	case "57P01", "40001":
		return true
	default:
		return false
	}
}
