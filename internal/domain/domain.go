// Package domain holds the shared nouns. A type is declared here once and
// imported everywhere else. This package may import only the standard library
// and github.com/google/uuid.
package domain

import (
	"time"

	"github.com/google/uuid"
)

type Location string

const (
	Indoor  Location = "indoor"
	Outdoor Location = "outdoor"
)

type TaskKind string

const (
	Water     TaskKind = "water"
	Prune     TaskKind = "prune"
	Fertilize TaskKind = "fertilize"
	Repot     TaskKind = "repot"
	Inspect   TaskKind = "inspect"
)

type SubstrateKind string

const (
	Peat   SubstrateKind = "peat"
	Cactus SubstrateKind = "cactus"
	Coir   SubstrateKind = "coir"
)

type Species struct {
	Slug             string
	CommonName       string
	ScientificName   string
	Placement        Location
	Kc               float64
	Substrate        SubstrateKind
	MAD              float64
	BaseIntervalDays int
	MinIntervalDays  int
	MaxIntervalDays  int
	DormantMonths    []time.Month
	DormancyFactor   float64
	MinTempC         float64
	FrostTender      bool
	Prune            *FixedTask
	Fertilize        *FixedTask
	Repot            *FixedTask
	CareAdvice       string
	Retired          bool
}

type FixedTask struct {
	IntervalDays int
	ActiveMonths []time.Month
}

type Plant struct {
	ID            uuid.UUID
	Name          string
	SpeciesSlug   string
	Location      Location
	Place         string
	TadoRoomID    *string
	PotDiameterMM int
	FExposure     float64
	FRain         float64
	AcquiredAt    *time.Time
	Active        bool
	Notes         string
	Overrides     Overrides
}

// Overrides are nullable mirrors of the species tunables. The effective value
// is always COALESCE(override, species), resolved by care.Effective.
type Overrides struct {
	Kc               *float64
	MAD              *float64
	Substrate        *SubstrateKind
	BaseIntervalDays *int
	MinIntervalDays  *int
	MaxIntervalDays  *int
}

type CareEvent struct {
	ID       int64
	PlantID  uuid.UUID
	Kind     TaskKind
	DoneAt   time.Time
	Note     string
	Source   string
	VoidedAt *time.Time
}
