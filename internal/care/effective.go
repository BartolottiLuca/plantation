package care

import "github.com/BartolottiLuca/plantation/internal/domain"

// Effective is the single COALESCE(plant override, species) resolution.
// A YAML edit of the catalog cannot silently clobber hand-tuning.
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
		DormantMonths:    s.DormantMonths,
		DormancyFactor:   s.DormancyFactor,
		FExposure:        p.FExposure,
		FRain:            p.FRain,
		InGround:         p.InGround,
		PotDiameterMM:    p.PotDiameterCM * 10,
		AcquiredAt:       p.AcquiredAt,
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
