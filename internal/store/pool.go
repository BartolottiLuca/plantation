package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	poolHealthCheckPeriod = 30 * time.Second
	poolConnectTimeout    = 5 * time.Second
	poolMaxConnLifetime   = time.Hour
	openBackoffStart      = 200 * time.Millisecond
	openBackoffCap        = 15 * time.Second
)

// Open builds a pgxpool and retries Ping with capped exponential backoff until
// the database is reachable or ctx is cancelled. An absent database is not fatal.
// The DSN is never logged.
func Open(ctx context.Context, databaseURL string, log *slog.Logger) (*pgxpool.Pool, error) {
	if log == nil {
		log = slog.Default()
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}
	cfg.HealthCheckPeriod = poolHealthCheckPeriod
	cfg.MaxConnLifetime = poolMaxConnLifetime
	cfg.ConnConfig.ConnectTimeout = poolConnectTimeout

	backoff := openBackoffStart
	for attempt := 1; ; attempt++ {
		pool, err := connectOnce(ctx, cfg)
		if err == nil {
			return pool, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("connecting to database: %w", ctx.Err())
		}
		log.Warn("database connect failed",
			"attempt", attempt,
			"backoff_ms", backoff.Milliseconds(),
			"err", err,
		)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connecting to database: %w", ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > openBackoffCap {
			backoff = openBackoffCap
		}
	}
}

func connectOnce(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, poolConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
