package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
)

const roomListTimeout = 3 * time.Second

type plantForm struct {
	Name          string
	SpeciesSlug   string
	Location      string
	Place         string
	TadoRoomID    string
	Container     string
	PotDiameterMM string
	FExposure     string
	FRain         string
	Kc            string
	MAD           string
	Substrate     string
	BaseInterval  string
	MinInterval   string
	MaxInterval   string
	Errors        map[string]string
}

func blankForm() plantForm {
	return plantForm{
		Location:      string(domain.Indoor),
		Container:     "pot",
		FExposure:     "1.0",
		FRain:         "0.0",
		PotDiameterMM: "180",
		Errors:        map[string]string{},
	}
}

func formFromPlant(p domain.Plant) plantForm {
	f := plantForm{
		Name:          p.Name,
		SpeciesSlug:   p.SpeciesSlug,
		Location:      string(p.Location),
		Place:         p.Place,
		Container:     "pot",
		PotDiameterMM: strconv.Itoa(p.PotDiameterMM),
		FExposure:     fmt.Sprintf("%.1f", p.FExposure),
		FRain:         fmt.Sprintf("%.1f", p.FRain),
		Errors:        map[string]string{},
	}
	if p.InGround {
		f.Container = "ground"
		f.PotDiameterMM = ""
	}
	if p.TadoRoomID != nil {
		f.TadoRoomID = *p.TadoRoomID
	}
	o := p.Overrides
	if o.Kc != nil {
		f.Kc = strconv.FormatFloat(*o.Kc, 'f', -1, 64)
	}
	if o.MAD != nil {
		f.MAD = strconv.FormatFloat(*o.MAD, 'f', -1, 64)
	}
	if o.Substrate != nil {
		f.Substrate = string(*o.Substrate)
	}
	if o.BaseIntervalDays != nil {
		f.BaseInterval = strconv.Itoa(*o.BaseIntervalDays)
	}
	if o.MinIntervalDays != nil {
		f.MinInterval = strconv.Itoa(*o.MinIntervalDays)
	}
	if o.MaxIntervalDays != nil {
		f.MaxInterval = strconv.Itoa(*o.MaxIntervalDays)
	}
	return f
}

func parsePlantForm(r *http.Request) plantForm {
	return plantForm{
		Name:          strings.TrimSpace(r.FormValue("name")),
		SpeciesSlug:   strings.TrimSpace(r.FormValue("species_slug")),
		Location:      strings.TrimSpace(r.FormValue("location")),
		Place:         strings.TrimSpace(r.FormValue("place")),
		TadoRoomID:    strings.TrimSpace(r.FormValue("tado_room_id")),
		Container:     strings.TrimSpace(r.FormValue("container")),
		PotDiameterMM: strings.TrimSpace(r.FormValue("pot_diameter_mm")),
		FExposure:     strings.TrimSpace(r.FormValue("f_exposure")),
		FRain:         strings.TrimSpace(r.FormValue("f_rain")),
		Kc:            strings.TrimSpace(r.FormValue("kc_override")),
		MAD:           strings.TrimSpace(r.FormValue("mad_override")),
		Substrate:     strings.TrimSpace(r.FormValue("substrate_override")),
		BaseInterval:  strings.TrimSpace(r.FormValue("base_interval_days_override")),
		MinInterval:   strings.TrimSpace(r.FormValue("min_interval_days_override")),
		MaxInterval:   strings.TrimSpace(r.FormValue("max_interval_days_override")),
		Errors:        map[string]string{},
	}
}

func (f *plantForm) apply(p *domain.Plant) {
	if f.Name == "" {
		f.Errors["name"] = "Name is required"
	} else if len(f.Name) > 200 {
		f.Errors["name"] = "Name is too long"
	} else {
		p.Name = f.Name
	}

	if len(f.Place) > 200 {
		f.Errors["place"] = "Place is too long"
	} else {
		p.Place = f.Place
	}

	switch f.Location {
	case string(domain.Indoor), string(domain.Outdoor):
		p.Location = domain.Location(f.Location)
	default:
		f.Errors["location"] = "Choose indoor or outdoor"
	}

	switch f.FExposure {
	case "1.0":
		p.FExposure = 1.0
	case "1.3":
		p.FExposure = 1.3
	default:
		f.Errors["f_exposure"] = "Choose a listed exposure"
	}

	switch f.FRain {
	case "0.0":
		p.FRain = 0.0
	case "0.6":
		p.FRain = 0.6
	case "0.9":
		p.FRain = 0.9
	default:
		f.Errors["f_rain"] = "Choose a listed rain exposure"
	}

	container := f.Container
	if container == "" {
		container = "pot"
	}
	switch container {
	case "ground":
		if p.Location != domain.Outdoor {
			f.Errors["container"] = "In the ground is only for outdoor plants"
			break
		}
		p.InGround = true
		p.PotDiameterMM = 0
	case "pot":
		n, err := strconv.Atoi(f.PotDiameterMM)
		if err != nil || n < 40 || n > 2000 {
			f.Errors["pot_diameter_mm"] = "Pot diameter must be 40–2000 mm"
		} else {
			p.PotDiameterMM = n
			p.InGround = false
		}
	default:
		f.Errors["container"] = "Choose a pot or in the ground"
	}

	if f.TadoRoomID == "" {
		p.TadoRoomID = nil
	} else {
		id := f.TadoRoomID
		p.TadoRoomID = &id
	}

	p.Overrides.Kc = optionalFloat(f.Kc, 0.05, 2.5, "kc_override", f.Errors)
	p.Overrides.MAD = optionalFloat(f.MAD, 0.05, 1, "mad_override", f.Errors)
	p.Overrides.BaseIntervalDays = optionalInt(f.BaseInterval, 1, 365, "base_interval_days_override", f.Errors)
	p.Overrides.MinIntervalDays = optionalInt(f.MinInterval, 1, 365, "min_interval_days_override", f.Errors)
	p.Overrides.MaxIntervalDays = optionalInt(f.MaxInterval, 1, 365, "max_interval_days_override", f.Errors)

	switch f.Substrate {
	case "":
		p.Overrides.Substrate = nil
	case string(domain.Peat), string(domain.Cactus), string(domain.Coir):
		sk := domain.SubstrateKind(f.Substrate)
		p.Overrides.Substrate = &sk
	default:
		f.Errors["substrate_override"] = "Choose peat, cactus or coir"
	}
}

func optionalFloat(s string, min, max float64, field string, errs map[string]string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < min || v > max {
		errs[field] = fmt.Sprintf("Must be between %g and %g", min, max)
		return nil
	}
	return &v
}

func optionalInt(s string, min, max int, field string, errs map[string]string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		errs[field] = fmt.Sprintf("Must be between %d and %d", min, max)
		return nil
	}
	return &n
}

func (s *Server) checkSpecies(ctx context.Context, f *plantForm, create bool, previousSlug string) error {
	if f.SpeciesSlug == "" {
		f.Errors["species_slug"] = "Species is required"
		return nil
	}
	sp, err := s.Species.Get(ctx, f.SpeciesSlug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			f.Errors["species_slug"] = "Unknown species"
			return nil
		}
		return fmt.Errorf("getting species: %w", err)
	}
	if sp.Retired && (create || f.SpeciesSlug != previousSlug) {
		f.Errors["species_slug"] = "That species is retired"
	}
	return nil
}

func (f plantForm) ok() bool {
	return len(f.Errors) == 0
}

func (f plantForm) advancedOpen() bool {
	if f.Kc != "" || f.MAD != "" || f.Substrate != "" || f.BaseInterval != "" || f.MinInterval != "" || f.MaxInterval != "" {
		return true
	}
	for _, k := range []string{"kc_override", "mad_override", "substrate_override", "base_interval_days_override", "min_interval_days_override", "max_interval_days_override"} {
		if f.Errors[k] != "" {
			return true
		}
	}
	return false
}

func (s *Server) pickerSpecies(ctx context.Context, keepSlug string) ([]domain.Species, error) {
	all, err := s.Species.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing species: %w", err)
	}
	out := make([]domain.Species, 0, len(all))
	for _, sp := range all {
		if !sp.Retired || sp.Slug == keepSlug {
			out = append(out, sp)
		}
	}
	return out, nil
}

func (s *Server) listRooms(ctx context.Context) []climate.RoomClimate {
	if s.Rooms == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, roomListTimeout)
	defer cancel()
	rooms, err := s.Rooms.Rooms(ctx)
	if err != nil {
		slog.Error("listing rooms", "err", err)
		return nil
	}
	return rooms
}

func soleTadoRoom(rooms []climate.RoomClimate) (string, bool) {
	if len(rooms) != 1 || rooms[0].RoomID == "" {
		return "", false
	}
	return rooms[0].RoomID, true
}

func (s *Server) bindSoleTadoRoom(ctx context.Context, p *domain.Plant) {
	id, ok := soleTadoRoom(s.listRooms(ctx))
	if !ok {
		return
	}
	p.TadoRoomID = &id
}
