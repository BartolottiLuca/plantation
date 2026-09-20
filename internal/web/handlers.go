package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

type dashboardData struct {
	page
	NoPlants bool
	Quiet    bool
	Overdue  []taskView
	DueToday []taskView
	Upcoming []taskView
}

type plantListRow struct {
	ID          uuid.UUID
	Name        string
	Place       string
	Location    string
	Status      string
	Summary     string
	Retired     bool
	SpeciesName string
}

type plantListData struct {
	page
	Plants []plantListRow
}

type detailData struct {
	page
	Plant    domain.Plant
	Species  domain.Species
	Retired  bool
	Missing  bool
	EmptyEnv bool
	Water    explPanel
	Tasks    []taskView
	Controls []controlView
	Events   []eventView
	PlantID  string
}

type formData struct {
	page
	Heading       string
	Action        string
	Submit        string
	Form          plantForm
	Species       []domain.Species
	Rooms         []climate.RoomClimate
	UseRoomSelect bool
	AdvancedOpen  bool
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.activeRows(ctx)
	if err != nil {
		s.internal(w, "listing plants", err)
		return
	}
	data := dashboardData{page: s.page(r, "dashboard"), NoPlants: len(rows) == 0}
	if data.NoPlants {
		s.render(w, http.StatusOK, "dashboard.html", data)
		return
	}
	tasks, err := s.scheduleRows(ctx, rows)
	if err != nil {
		s.internal(w, "scheduling dashboard", err)
		return
	}
	data.Overdue, data.DueToday, data.Upcoming = s.groupDashboard(tasks)
	data.Quiet = len(data.Overdue)+len(data.DueToday)+len(data.Upcoming) == 0
	s.render(w, http.StatusOK, "dashboard.html", data)
}

func (s *Server) listPlants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.activeRows(ctx)
	if err != nil {
		s.internal(w, "listing plants", err)
		return
	}
	data := plantListData{page: s.page(r, "plants"), Plants: make([]plantListRow, 0, len(rows))}
	for _, row := range rows {
		tasks, err := s.schedulePlant(ctx, row.Plant, row.Species)
		if err != nil {
			s.internal(w, "scheduling plant", err)
			return
		}
		st, summary := worstStatus(tasks)
		data.Plants = append(data.Plants, plantListRow{
			ID:          row.Plant.ID,
			Name:        row.Plant.Name,
			Place:       row.Plant.Place,
			Location:    string(row.Plant.Location),
			Status:      statusLabel(st),
			Summary:     summary,
			Retired:     row.Species.Retired,
			SpeciesName: row.Species.CommonName,
		})
	}
	s.render(w, http.StatusOK, "plants.html", data)
}

func (s *Server) newPlantGET(w http.ResponseWriter, r *http.Request) {
	s.renderForm(w, r, http.StatusOK, "Add a plant", "/plants/new", "Add plant", blankForm(), "")
}

func (s *Server) newPlantPOST(w http.ResponseWriter, r *http.Request) {
	f := parsePlantForm(r)
	p := domain.Plant{Active: true}
	f.apply(&p)
	s.bindSoleTadoRoom(r.Context(), &p)
	if err := s.checkSpecies(r.Context(), &f, true, ""); err != nil {
		s.internal(w, "loading species", err)
		return
	}
	if !f.ok() {
		s.renderForm(w, r, http.StatusBadRequest, "Add a plant", "/plants/new", "Add plant", f, "")
		return
	}
	p.SpeciesSlug = f.SpeciesSlug
	created, err := s.Plants.Create(r.Context(), p)
	if err != nil {
		s.internal(w, "creating plant", err)
		return
	}
	s.redirect(w, r, "/plants/"+created.ID.String())
}

func (s *Server) plantDetail(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadActive(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sp, missing, err := s.loadSpecies(ctx, p.SpeciesSlug)
	if err != nil {
		s.internal(w, "loading species", err)
		return
	}
	tasks, err := s.schedulePlant(ctx, p, sp)
	if err != nil {
		s.internal(w, "scheduling plant", err)
		return
	}
	events, err := s.history(ctx, p, sp)
	if err != nil {
		s.internal(w, "loading care history", err)
		return
	}
	action := r.URL.Query().Get("action")
	views := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		v := taskOf(t)
		v.Preselected = action != "" && t.Slug == action
		views = append(views, v)
	}
	controls, err := s.taskControlViews(ctx, p, sp)
	if err != nil {
		s.internal(w, "loading task controls", err)
		return
	}
	water := waterPanel(tasks)
	s.render(w, http.StatusOK, "detail.html", detailData{
		page:     s.page(r, "plants"),
		Plant:    p,
		Species:  sp,
		Retired:  sp.Retired,
		Missing:  missing,
		EmptyEnv: water.EmptyEnv,
		Water:    water,
		Tasks:    views,
		Controls: controls,
		Events:   events,
		PlantID:  p.ID.String(),
	})
}

func (s *Server) editPlantGET(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadActive(w, r)
	if !ok {
		return
	}
	s.renderForm(w, r, http.StatusOK, "Edit "+p.Name, "/plants/"+p.ID.String()+"/edit", "Save", formFromPlant(p), p.ID.String())
}

func (s *Server) editPlantPOST(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadActive(w, r)
	if !ok {
		return
	}
	f := parsePlantForm(r)
	f.apply(&p)
	s.bindSoleTadoRoom(r.Context(), &p)
	if err := s.checkSpecies(r.Context(), &f, false, p.SpeciesSlug); err != nil {
		s.internal(w, "loading species", err)
		return
	}
	if !f.ok() {
		s.renderForm(w, r, http.StatusBadRequest, "Edit "+p.Name, "/plants/"+p.ID.String()+"/edit", "Save", f, p.ID.String())
		return
	}
	p.SpeciesSlug = f.SpeciesSlug
	if err := s.Plants.Update(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return
		}
		s.internal(w, "updating plant", err)
		return
	}
	s.redirect(w, r, "/plants/"+p.ID.String())
}

func (s *Server) deletePlant(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePlantID(w, r)
	if !ok {
		return
	}
	if err := s.Plants.Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return
		}
		s.internal(w, "deleting plant", err)
		return
	}
	s.redirect(w, r, "/plants")
}

func (s *Server) logCare(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadActive(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sp, missing, err := s.loadSpecies(ctx, p.SpeciesSlug)
	if err != nil {
		s.internal(w, "loading species", err)
		return
	}
	if missing {
		http.Error(w, "species is missing from the catalog", http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	kind, _, ok := findTask(sp, slug)
	if !ok {
		http.Error(w, "unknown care task", http.StatusBadRequest)
		return
	}
	if _, err := s.Events.Add(ctx, domain.CareEvent{
		PlantID:  p.ID,
		Kind:     kind,
		TaskSlug: &slug,
		DoneAt:   s.Clock.Now(),
		Source:   "web",
	}); err != nil {
		s.internal(w, "logging care event", err)
		return
	}
	if isHTMX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/plants/"+p.ID.String(), http.StatusSeeOther)
}

func (s *Server) voidEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.notFound(w)
		return
	}
	if err := s.Events.Void(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return
		}
		s.internal(w, "voiding care event", err)
		return
	}
	if isHTMX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if pid := r.FormValue("plant_id"); pid != "" {
		http.Redirect(w, r, "/plants/"+pid, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, status int, heading, action, submit string, f plantForm, plantID string) {
	keep := f.SpeciesSlug
	species, err := s.pickerSpecies(r.Context(), keep)
	if err != nil {
		s.internal(w, "listing species", err)
		return
	}
	rooms := s.listRooms(r.Context())
	if id, ok := soleTadoRoom(rooms); ok && f.TadoRoomID == "" {
		f.TadoRoomID = id
	}
	nav := "add"
	if plantID != "" {
		nav = "plants"
	}
	s.render(w, status, "form.html", formData{
		page:          s.page(r, nav),
		Heading:       heading,
		Action:        action,
		Submit:        submit,
		Form:          f,
		Species:       species,
		Rooms:         rooms,
		UseRoomSelect: len(rooms) > 1,
		AdvancedOpen:  f.advancedOpen(),
	})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, dest string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func parsePlantID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) loadActive(w http.ResponseWriter, r *http.Request) (domain.Plant, bool) {
	id, ok := parsePlantID(w, r)
	if !ok {
		return domain.Plant{}, false
	}
	p, err := s.Plants.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return domain.Plant{}, false
		}
		s.internal(w, "loading plant", err)
		return domain.Plant{}, false
	}
	if !p.Active {
		s.notFound(w)
		return domain.Plant{}, false
	}
	return p, true
}

func (s *Server) loadSpecies(ctx context.Context, slug string) (domain.Species, bool, error) {
	sp, err := s.Species.Get(ctx, slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Species{}, true, nil
		}
		return domain.Species{}, false, err
	}
	return sp, false, nil
}

// updateTask writes the per-plant control row the digest and the dashboard
// both read. An empty interval_days or snooze_until clears that override
// rather than leaving a stale one in place.
func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadActive(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sp, missing, err := s.loadSpecies(ctx, p.SpeciesSlug)
	if err != nil {
		s.internal(w, "loading species", err)
		return
	}
	if missing {
		http.Error(w, "species is missing from the catalog", http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	kind, _, ok := findTask(sp, slug)
	if !ok {
		http.Error(w, "unknown care task", http.StatusBadRequest)
		return
	}
	if s.Tasks == nil {
		http.Error(w, "task control is not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}

	t := store.CareTask{
		PlantID:  p.ID,
		TaskSlug: slug,
		Enabled:  r.FormValue("enabled") != "",
	}
	if raw := strings.TrimSpace(r.FormValue("interval_days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 3650 {
			http.Error(w, "interval_days must be a whole number of days from 1 to 3650", http.StatusBadRequest)
			return
		}
		// Water is scheduled from the reservoir model, not an interval; the
		// plant-level base_interval_days_override is its knob (SPEC §7.7).
		if kind != domain.Water {
			t.IntervalDaysOverride = &n
		}
	}
	if raw := strings.TrimSpace(r.FormValue("snooze_until")); raw != "" {
		d, err := parseISODate(raw)
		if err != nil {
			http.Error(w, "snooze_until must be a date as YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		t.SnoozedUntil = &d
	}
	if _, err := s.Tasks.Upsert(ctx, t); err != nil {
		s.internal(w, "saving care task", err)
		return
	}
	s.redirect(w, r, "/plants/"+p.ID.String())
}

func parseISODate(raw string) (domain.Date, error) {
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return domain.Date{}, err
	}
	return domain.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}, nil
}
