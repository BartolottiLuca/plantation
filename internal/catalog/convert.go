package catalog

import (
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// convert assumes y has already passed validate; it never fails.
func convert(slug string, y speciesYAML) domain.Species {
	dormancyFactor := 1.0
	if y.DormancyFactor != nil {
		dormancyFactor = *y.DormancyFactor
	}
	return domain.Species{
		Slug:             slug,
		CommonName:       y.CommonName,
		ScientificName:   y.ScientificName,
		Placement:        domain.Location(y.Placement),
		Kc:               y.Kc,
		Substrate:        domain.SubstrateKind(y.Substrate),
		MAD:              y.MAD,
		BaseIntervalDays: y.BaseIntervalDays,
		MinIntervalDays:  y.MinIntervalDays,
		MaxIntervalDays:  y.MaxIntervalDays,
		DormantMonths:    intsToMonths(y.DormantMonths),
		DormancyFactor:   dormancyFactor,
		MinTempC:         y.MinTempC,
		FrostTender:      y.FrostTender,
		Prune:            convertFixedTask(y.Prune),
		Fertilize:        convertFixedTask(y.Fertilize),
		Repot:            convertFixedTask(y.Repot),
		CareAdvice:       y.CareAdvice,
		Retired:          y.Retired,
	}
}

func convertFixedTask(t *fixedTaskYAML) *domain.FixedTask {
	if t == nil {
		return nil
	}
	return &domain.FixedTask{
		IntervalDays: t.IntervalDays,
		ActiveMonths: intsToMonths(t.ActiveMonths),
	}
}

func intsToMonths(in []int) []time.Month {
	if len(in) == 0 {
		return nil
	}
	out := make([]time.Month, len(in))
	for i, v := range in {
		out[i] = time.Month(v)
	}
	return out
}
