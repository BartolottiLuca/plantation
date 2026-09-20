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
		Description:      y.Description,
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
		Tasks:            convertTasks(y.Tasks),
		CareAdvice:       y.CareAdvice,
		Retired:          y.Retired,
	}
}

func convertTasks(in []speciesTaskYAML) []domain.SpeciesTask {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.SpeciesTask, len(in))
	for i, t := range in {
		out[i] = domain.SpeciesTask{
			Slug:         t.Slug,
			Kind:         domain.TaskKind(t.Kind),
			Label:        t.Label,
			IntervalDays: t.IntervalDays,
			ActiveMonths: intsToMonths(t.ActiveMonths),
		}
	}
	return out
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
