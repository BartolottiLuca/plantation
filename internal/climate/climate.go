// Package climate reads indoor temperature and humidity (Tado X).
package climate

import (
	"context"
	"time"
)

const (
	StateUnlinked    = "unlinked"
	StateLinked      = "linked"
	StateNeedsReauth = "needs_reauth"
)

type RoomClimate struct {
	RoomID      string
	Name        string
	TempC       float64
	HumidityPct float64
	ObservedAt  time.Time
}

type LinkStatus struct {
	State             string
	HomeID            string
	AccessExpiresAt   time.Time
	RefreshObtainedAt time.Time
}

type IndoorClimateProvider interface {
	// Rooms returns one current reading per room, or an empty slice when unavailable.
	Rooms(ctx context.Context) ([]RoomClimate, error)
	// Status reports link state for the diagnostics page; never returns an error.
	Status(ctx context.Context) LinkStatus
}

type NoopClimate struct{}

func (*NoopClimate) Rooms(context.Context) ([]RoomClimate, error) {
	return []RoomClimate{}, nil
}

func (*NoopClimate) Status(context.Context) LinkStatus {
	return LinkStatus{State: StateUnlinked}
}

var _ IndoorClimateProvider = (*NoopClimate)(nil)
