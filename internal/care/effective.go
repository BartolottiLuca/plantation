package care

import "github.com/BartolottiLuca/plantation/internal/domain"

// Effective is the single COALESCE(plant override, species) resolution.
// An edit to a species cannot silently clobber hand-tuning.
//
// It is also the single cm→mm conversion point: domain.Plant stores pot
// diameter the way a person measures it (centimeters), and Params carries it
// the way the reservoir model needs it (millimeters, matching ET0 and rainfall
// depths) — see SPEC §7.1's D = 0.8 × pot_diameter_mm.
func Effective(p domain.Plant, s domain.Species) Params {
	params := Params{
		Location:         p.Location,
		Kc:               s.Kc,
		Substrate:        s.Substrate,
		MAD:              s.MAD,
		BaseIntervalDays: s.BaseIntervalDays,
		MinIntervalDays:  s.MinIntervalDays,
		MaxIntervalDays:  s.MaxIntervalDays,
		DormancyFactor:   1,
		FExposure:        p.FExposure,
		FRain:            p.FRain,
		InGround:         p.InGround,
		PotDiameterMM:    p.PotDiameterCM * 10,
		AcquiredAt:       p.AcquiredAt,
	}
	// The species' rest period is an indoor fact. Outdoors the same slowdown is
	// already in the weather — a low winter ET0 — and applying it again would
	// under-water the plant (SPEC §7.2).
	if p.Location == domain.Indoor {
		params.DormantMonths = s.DormantMonths
		params.DormancyFactor = s.DormancyFactor
	}
	o := p.Overrides
	if o.Kc != nil {
		params.Kc = *o.Kc
	}
	if o.MAD != nil {
		params.MAD = *o.MAD
	}
	if o.Substrate != nil {
		params.Substrate = *o.Substrate
	}
	if o.BaseIntervalDays != nil {
		params.BaseIntervalDays = *o.BaseIntervalDays
	}
	if o.MinIntervalDays != nil {
		params.MinIntervalDays = *o.MinIntervalDays
	}
	if o.MaxIntervalDays != nil {
		params.MaxIntervalDays = *o.MaxIntervalDays
	}
	return params
}
