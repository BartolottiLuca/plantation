package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TadoTokenRepo struct {
	pool *pgxpool.Pool
}

func NewTadoTokenRepo(pool *pgxpool.Pool) *TadoTokenRepo {
	return &TadoTokenRepo{pool: pool}
}

func (r *TadoTokenRepo) Load(ctx context.Context) (TadoToken, error) {
	var tok TadoToken
	err := Retry(ctx, func(ctx context.Context) error {
		got, err := scanTadoToken(r.pool.QueryRow(ctx, tadoTokenSelect+` WHERE id = 1`))
		if err != nil {
			return fmt.Errorf("loading tado token: %w", mapNoRows(err))
		}
		tok = got
		return nil
	})
	return tok, err
}

// WithLock begins a transaction, locks the single tado_token row, and hands
// fn the current values plus a writer bound to that transaction. C06 must
// persist a rotated refresh token through the writer before using the access
// token. There is no unlocked Save — two callers (scheduler and HTTP) would
// otherwise spend the same rotating refresh token.
func (r *TadoTokenRepo) WithLock(ctx context.Context, fn func(ctx context.Context, current TadoToken, w TadoTokenWriter) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("locking tado token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanTadoToken(tx.QueryRow(ctx, tadoTokenSelect+` WHERE id = 1 FOR UPDATE`))
	if err != nil {
		return fmt.Errorf("locking tado token: %w", mapNoRows(err))
	}
	if err := fn(ctx, current, tadoTokenWriter{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing tado token: %w", err)
	}
	return nil
}

const tadoTokenSelect = `
	SELECT access_token, access_expires_at, refresh_token, previous_refresh_token,
		refresh_obtained_at, home_id, state, updated_at
	FROM tado_token`

type tadoTokenWriter struct {
	tx pgx.Tx
}

func (w tadoTokenWriter) Write(ctx context.Context, tok TadoToken) error {
	_, err := w.tx.Exec(ctx, `
		UPDATE tado_token SET
			access_token = $1,
			access_expires_at = $2,
			refresh_token = $3,
			previous_refresh_token = $4,
			refresh_obtained_at = $5,
			home_id = $6,
			state = $7,
			updated_at = now()
		WHERE id = 1`,
		tok.AccessToken, tok.AccessExpiresAt, tok.RefreshToken, tok.PreviousRefreshToken,
		tok.RefreshObtainedAt, tok.HomeID, tok.State,
	)
	if err != nil {
		return fmt.Errorf("writing tado token: %w", err)
	}
	return nil
}

func scanTadoToken(row speciesScanner) (TadoToken, error) {
	var tok TadoToken
	err := row.Scan(
		&tok.AccessToken, &tok.AccessExpiresAt, &tok.RefreshToken, &tok.PreviousRefreshToken,
		&tok.RefreshObtainedAt, &tok.HomeID, &tok.State, &tok.UpdatedAt,
	)
	return tok, err
}
