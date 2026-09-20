package store

import (
	"context"
	"errors"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
)

var ErrNotFound = errors.New("store: not found")

const (
	WeatherObserved = "observed"
	WeatherForecast = "forecast"

	TadoUnlinked    = "unlinked"
	TadoLinked      = "linked"
	TadoNeedsReauth = "needs_reauth"

	NotificationClaimed = "claimed"
	NotificationSent    = "sent"
	NotificationSkipped = "skipped"
	NotificationFailed  = "failed"
)

// CareTask is a per-plant enable/snooze row, keyed on the species task's slug
// rather than its kind — a species may have two tasks of one kind (lavender's
// two prunings), and each needs its own row. It is not a domain type.
type CareTask struct {
	ID                   int64
	PlantID              uuid.UUID
	TaskSlug             string
	Enabled              bool
	IntervalDaysOverride *int
	SnoozedUntil         *domain.Date
}

// SpeciesTask is the species_tasks row form of domain.SpeciesTask, plus the
// display ordering that isn't part of the domain type.
type SpeciesTask struct {
	SpeciesSlug  string
	Slug         string
	Kind         domain.TaskKind
	Label        string
	IntervalDays int
	ActiveMonths []time.Month
	SortOrder    int
}

// PlantWithSpecies is a plant list row with its catalog species joined.
type PlantWithSpecies struct {
	Plant   domain.Plant
	Species domain.Species
}

// WeatherDay is the store-local weather cache row. Dates are civil domain.Date.
// Do not import internal/weather from this package.
type WeatherDay struct {
	LocationKey string
	Date        domain.Date
	Kind        string
	ET0MM       *float64
	PrecipMM    *float64
	PrecipProb  *float64
	TMinC       *float64
	TMaxC       *float64
	FetchedAt   time.Time
}

// ClimateSample is one Tado room reading. Do not import internal/climate.
type ClimateSample struct {
	RoomID      string
	ObservedAt  time.Time
	TempC       float64
	HumidityPct float64
}

// ClimateDailyMean is the per-day average of ClimateSample rows.
type ClimateDailyMean struct {
	Date        domain.Date
	TempC       float64
	HumidityPct float64
}

// TadoToken is the single-row token record. Callers must never log token fields.
type TadoToken struct {
	AccessToken          *string
	AccessExpiresAt      *time.Time
	RefreshToken         *string
	PreviousRefreshToken *string
	RefreshObtainedAt    *time.Time
	HomeID               *string
	State                string
	UpdatedAt            time.Time
}

// TadoTokenWriter persists the locked tado_token row. There is no unlocked Save.
type TadoTokenWriter interface {
	Write(ctx context.Context, tok TadoToken) error
}

// Notification is one outbox row.
type Notification struct {
	ID        int64
	DedupeKey string
	Kind      string
	Status    string
	ClaimedAt time.Time
	SentAt    *time.Time
	Attempts  int
	Body      string
	LastError string
}
