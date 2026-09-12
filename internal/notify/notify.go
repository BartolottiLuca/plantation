// Package notify delivers Discord messages through an idempotent outbox.
package notify

import "context"

type Message struct {
	Title       string
	Description string
	URL         string
}

type Notifier interface {
	// SendOnce is idempotent on dedupeKey: a key already claimed is a no-op.
	SendOnce(ctx context.Context, dedupeKey, kind string, msg Message) error
}

type NoopNotifier struct{}

func (*NoopNotifier) SendOnce(context.Context, string, string, Message) error {
	return nil
}

var _ Notifier = (*NoopNotifier)(nil)
