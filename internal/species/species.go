// Package species drafts a species record from a short description of a plant.
//
// A model proposes the horticultural constants; this package turns the proposal
// into a domain.Species, computes the fields a model should not be asked for, and
// runs the result through catalog.Validate — the same gate a hand-typed species
// passes. Nothing here writes to the database: a draft is only ever a suggestion
// for a person to review.
package species

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

var (
	// ErrDisabled means no API key is configured. The web layer renders the manual
	// form instead of treating it as a failure.
	ErrDisabled = errors.New("species drafting is not configured")
	// ErrRateLimited means the API rejected the request for rate limiting even
	// after the client's own retries.
	ErrRateLimited = errors.New("species drafting is rate limited")
	// ErrNoQuota means the API account has run out of credit. Retrying does not
	// help; someone has to top it up.
	ErrNoQuota = errors.New("the drafting account has no quota left")
	// ErrRefused means the model declined to produce a record.
	ErrRefused = errors.New("the model declined to draft this species")
	// ErrTruncated means the response hit its token cap before the record was complete.
	ErrTruncated = errors.New("the drafted species was cut off")
)

const (
	maxNameLen  = 120
	maxLabelLen = 500
	maxNotesLen = 1000
)

// Request is what the form collects, and nothing more. The user does not know
// crop coefficients; the user does know what the plant is called. Where it will
// live is deliberately absent: that belongs to the plant, and one species record
// serves a plant indoors and another outdoors.
type Request struct {
	Name      string // what the user typed: common or scientific name
	LabelText string // optional: nursery label, cultivar, anything printed on it
	Notes     string // optional free text
}

// Validate rejects a request that cannot be sent: empty or oversized.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	for _, f := range []struct {
		name  string
		value string
		max   int
	}{
		{"name", r.Name, maxNameLen},
		{"label text", r.LabelText, maxLabelLen},
		{"notes", r.Notes, maxNotesLen},
	} {
		if len([]rune(f.value)) > f.max {
			return fmt.Errorf("%s is longer than %d characters", f.name, f.max)
		}
	}
	return nil
}

// Proposal is what the model returns. It deliberately has no interval fields
// and no slug: both are computed (see ToSpecies), so the model is never asked
// for a number it would have to reverse-engineer from the water-balance model.
type Proposal struct {
	CommonName     string
	ScientificName string
	Description    string
	CareAdvice     string
	Kc             float64
	Substrate      domain.SubstrateKind
	MAD            float64
	DormantMonths  []time.Month
	DormancyFactor float64
	MinTempC       float64
	FrostTender    bool
	Tasks          []domain.SpeciesTask

	// Identification, for the review screen — not stored.
	Confidence   Confidence
	IdentifiedAs string   // the binomial the model believes it described
	Alternatives []string // other plants the description could mean
	Reasoning    string   // why these constants, in a few sentences
}

// Confidence is the model's own read on whether it identified the right plant.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

func (c Confidence) valid() bool {
	return c == ConfidenceHigh || c == ConfidenceMedium || c == ConfidenceLow
}

// Result is a draft plus everything a review screen needs to judge it.
type Result struct {
	Proposal Proposal
	// Species is the proposal converted for storage, with provenance set. It is
	// present even when Problems is not empty, so the form can show the values
	// that failed next to the errors.
	Species domain.Species
	// Problems are the validation errors that survived the model's one correction
	// attempt. Each is a *catalog.FieldError. Empty for a clean draft.
	Problems []error
	Model    string
	Retried  bool
}

// Drafter turns a user's description of a plant into a proposed species record.
type Drafter interface {
	Draft(ctx context.Context, req Request) (Result, error)
}

// NoopDrafter is wired when no API key is configured. Draft always returns
// ErrDisabled, which the web layer renders as "add it manually instead".
type NoopDrafter struct{}

func (NoopDrafter) Draft(context.Context, Request) (Result, error) {
	return Result{}, ErrDisabled
}

var _ Drafter = NoopDrafter{}
