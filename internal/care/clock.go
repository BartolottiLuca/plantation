package care

import "time"

type Clock interface {
	Now() time.Time
}

type NoopClock struct {
	Instant time.Time
}

func (c *NoopClock) Now() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.Instant
}

var _ Clock = (*NoopClock)(nil)
