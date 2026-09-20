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

// TaskKind is the curated care vocabulary. It is closed on purpose: the digest
// groups by it and the care history is queried by it, so a typo must not become
// a new kind of care. Widening it is a SPEC §3 amendment.
//
// `inspect` is log-only — it can be recorded and appears in the history, but no
// species task drives it. There is no defensible interval for "have a look".
type TaskKind string

const (
	Water     TaskKind = "water"
	Prune     TaskKind = "prune"
	Pinch     TaskKind = "pinch"
	Deadhead  TaskKind = "deadhead"
	Fertilize TaskKind = "fertilize"
	TopDress  TaskKind = "top_dress"
	Repot     TaskKind = "repot"
	Divide    TaskKind = "divide"
	Harvest   TaskKind = "harvest"
	Mulch     TaskKind = "mulch"
	Stake     TaskKind = "stake"
	Inspect   TaskKind = "inspect"
)

// WaterSlug is reserved. Watering is scheduled from the reservoir model and is
// per-plant, so it never appears in Species.Tasks — but reserving the slug lets
// every task, watering included, be addressed the same way in the URL space.
const WaterSlug = "water"

// TaskKinds returns the vocabulary in display order. Callers that validate or
// render a kind read it from here rather than keeping a second list.
func TaskKinds() []TaskKind {
	return []TaskKind{
		Water, Prune, Pinch, Deadhead, Fertilize, TopDress,
		Repot, Divide, Harvest, Mulch, Stake, Inspect,
	}
}

// Valid reports whether k is in the vocabulary.
func (k TaskKind) Valid() bool {
	for _, known := range TaskKinds() {
		if k == known {
			return true
		}
	}
	return false
}

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
	Description      string // a paragraph on the plant; CareAdvice stays the short practical note
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
	// Tasks is every fixed-interval task this species needs, in display order.
	// Watering is absent by design: it is computed, not declared.
	Tasks      []SpeciesTask
	CareAdvice string
	Retired    bool
}

// SpeciesTask is one declared unit of care. A species may hold several of the
// same Kind — lavender wants a hard prune in spring and a light trim after
// flowering — which is why tasks carry a Slug rather than being keyed by Kind.
//
// Slug is a permanent identity within the species, in the same sense as the
// species slug itself: renaming it orphans every care event that references it.
// It is unique per species, not globally, so two species may both have `feed`.
type SpeciesTask struct {
	Slug         string
	Kind         TaskKind
	Label        string // what the UI and the digest call it
	IntervalDays int
	ActiveMonths []time.Month // empty means all year
}

type Plant struct {
	ID          uuid.UUID
	Name        string
	SpeciesSlug string
	Location    Location
	Place       string
	TadoRoomID  *string
	InGround    bool
	// PotDiameterCM is centimeters, what a person measures with a tape and reads
	// off a label; 0 when InGround. care.Effective converts to millimeters for
	// the reservoir model, which works in the same unit as ET0 and rainfall.
	PotDiameterCM int
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
	ID      int64
	PlantID uuid.UUID
	Kind    TaskKind
	// TaskSlug names the species task this event satisfies. It is nil on every
	// row logged before tasks had identity: backfilling would mean guessing, and
	// for a species with two tasks of one kind the guess is undecidable. The
	// engine treats a nil slug as satisfying any task of the event's Kind.
	TaskSlug *string
	DoneAt   time.Time
	Note     string
	Source   string
	VoidedAt *time.Time
}
