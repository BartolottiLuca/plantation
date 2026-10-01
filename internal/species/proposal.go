package species

import (
	"math"
	"strings"
	"unicode"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

// The floor and ceiling around the modelled interval. They bound how far the
// weather can move a schedule, not a prediction of it, so generous is correct:
// they are chosen to contain every curated species' own min/base and max/base
// ratio (0.25–0.5 and 2.5–3.5), and a tighter pair would reject real plants.
const (
	minIntervalFactor = 0.25
	maxIntervalFactor = 3.5
)

// ToSpecies converts a drafted proposal into a species record. The three
// interval fields are computed, never taken from the proposal: base is what
// care.ScheduleWater actually produces for these constants at the reference
// point, so the catalog's physics-consistency check passes by construction
// rather than by the model having guessed a compatible number.
//
// It does not validate. A species whose modelled base interval is 1 day cannot
// satisfy min < base with min >= 1; Validate reports that, which is the honest
// outcome for a combination of constants no real plant has.
func ToSpecies(p Proposal) domain.Species {
	base := catalog.ModelledIntervalDays(p.Kc, p.MAD, p.Substrate)
	minDays, maxDays := IntervalBounds(base)

	return domain.Species{
		Slug:             Slugify(p.ScientificName),
		CommonName:       p.CommonName,
		ScientificName:   p.ScientificName,
		Description:      p.Description,
		Kc:               p.Kc,
		Substrate:        p.Substrate,
		MAD:              p.MAD,
		BaseIntervalDays: base,
		MinIntervalDays:  minDays,
		MaxIntervalDays:  maxDays,
		DormantMonths:    p.DormantMonths,
		DormancyFactor:   p.DormancyFactor,
		MinTempC:         p.MinTempC,
		FrostTender:      p.FrostTender,
		Tasks:            p.Tasks,
		CareAdvice:       p.CareAdvice,
	}
}

// Slugify turns a scientific name into the kebab-case identity a species is
// stored under. The rule is mechanical, so code owns it rather than the model.
// Anything that is not an ASCII letter or digit separates words, which drops
// the hybrid sign and cultivar quotes: "Mentha × piperita 'Citrata'" becomes
// "mentha-piperita-citrata".
func Slugify(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
			continue
		}
		pendingHyphen = true
	}
	return b.String()
}

// IntervalBounds returns the min and max interval around base, kept strictly on
// either side of it as Validate requires. The species form uses it to recompute
// intervals when a person edits the constants they derive from. For base 1 the
// lower bound comes out as 0, which Validate then reports.
func IntervalBounds(base int) (minDays, maxDays int) {
	minDays = int(math.Round(float64(base) * minIntervalFactor))
	if minDays < 1 {
		minDays = 1
	}
	if minDays >= base {
		minDays = base - 1
	}
	maxDays = int(math.Round(float64(base) * maxIntervalFactor))
	if maxDays <= base {
		maxDays = base + 1
	}
	return minDays, maxDays
}
