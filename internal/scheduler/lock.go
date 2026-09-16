package scheduler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Distinct from store's migration lock (0x504C4E54). Session-level: the
// connection that wins must be held until release, or the lock dies with it.
const schedulerLockKey int64 = 0x504C4E53

// Locker is the advisory-lock seam. Tests use a memory lock; cmd passes
// PoolLocker so two replicas degrade to one scheduler.
type Locker interface {
	TryHold(ctx context.Context) (held bool, release func(), err error)
}

// AlwaysLock wins immediately. Tests that are not proving replica behaviour
// use this so they do not need Postgres.
type AlwaysLock struct{}

func (AlwaysLock) TryHold(context.Context) (bool, func(), error) {
	return true, func() {}, nil
}

// NeverLock never wins. Used to prove the HTTP replica path does not tick.
type NeverLock struct{}

func (NeverLock) TryHold(context.Context) (bool, func(), error) {
	return false, func() {}, nil
}

// PoolLocker holds pg_try_advisory_lock on one pool connection for the
// scheduler's lifetime.
type PoolLocker struct {
	Pool *pgxpool.Pool
}

func (l PoolLocker) TryHold(ctx context.Context) (bool, func(), error) {
	if l.Pool == nil {
		return false, func() {}, fmt.Errorf("scheduler lock: nil pool")
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return false, func() {}, fmt.Errorf("acquiring scheduler lock connection: %w", err)
	}
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, schedulerLockKey).Scan(&held); err != nil {
		conn.Release()
		return false, func() {}, fmt.Errorf("trying scheduler advisory lock: %w", err)
	}
	if !held {
		conn.Release()
		return false, func() {}, nil
	}
	return true, func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, schedulerLockKey)
		conn.Release()
	}, nil
}
