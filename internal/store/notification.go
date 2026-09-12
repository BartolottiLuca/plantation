package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type NotificationRepo struct {
	pool *pgxpool.Pool
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{pool: pool}
}

func (r *NotificationRepo) Claim(ctx context.Context, dedupeKey, kind string) (bool, error) {
	var claimed bool
	err := Retry(ctx, func(ctx context.Context) error {
		var id int64
		err := r.pool.QueryRow(ctx, `
			INSERT INTO notifications (dedupe_key, kind, status)
			VALUES ($1, $2, 'claimed')
			ON CONFLICT (dedupe_key) DO NOTHING
			RETURNING id`, dedupeKey, kind).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				claimed = false
				return nil
			}
			return fmt.Errorf("claiming notification: %w", err)
		}
		claimed = true
		return nil
	})
	return claimed, err
}

func (r *NotificationRepo) MarkSent(ctx context.Context, dedupeKey string) error {
	return Retry(ctx, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE notifications SET status = 'sent', sent_at = now()
			WHERE dedupe_key = $1`, dedupeKey)
		if err != nil {
			return fmt.Errorf("marking notification sent: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r *NotificationRepo) MarkFailed(ctx context.Context, dedupeKey, lastError string) error {
	return Retry(ctx, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE notifications
			SET status = 'failed', last_error = $2, attempts = attempts + 1
			WHERE dedupe_key = $1`, dedupeKey, lastError)
		if err != nil {
			return fmt.Errorf("marking notification failed: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r *NotificationRepo) RecordSkipped(ctx context.Context, dedupeKey, kind string) error {
	return Retry(ctx, func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `
			INSERT INTO notifications (dedupe_key, kind, status)
			VALUES ($1, $2, 'skipped')
			ON CONFLICT (dedupe_key) DO NOTHING`, dedupeKey, kind)
		if err != nil {
			return fmt.Errorf("recording skipped notification: %w", err)
		}
		return nil
	})
}

func (r *NotificationRepo) StaleClaims(ctx context.Context, olderThan time.Time) ([]Notification, error) {
	var out []Notification
	err := Retry(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT id, dedupe_key, kind, status, claimed_at, sent_at, attempts, body, last_error
			FROM notifications
			WHERE status = 'claimed' AND claimed_at < $1
			ORDER BY claimed_at ASC`, olderThan)
		if err != nil {
			return fmt.Errorf("listing stale claims: %w", err)
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			n, err := scanNotification(rows)
			if err != nil {
				return fmt.Errorf("listing stale claims: %w", err)
			}
			out = append(out, n)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing stale claims: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Notification{}
	}
	return out, nil
}

func scanNotification(row speciesScanner) (Notification, error) {
	var n Notification
	err := row.Scan(
		&n.ID, &n.DedupeKey, &n.Kind, &n.Status, &n.ClaimedAt, &n.SentAt,
		&n.Attempts, &n.Body, &n.LastError,
	)
	return n, err
}
