package web

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

// PlantRepo is the plant persistence the UI needs. *store.PlantRepo satisfies it.
type PlantRepo interface {
	Create(ctx context.Context, p domain.Plant) (domain.Plant, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Plant, error)
	Update(ctx context.Context, p domain.Plant) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]store.PlantWithSpecies, error)
}

// SpeciesRepo is the catalog the UI reads. *store.SpeciesRepo satisfies it.
type SpeciesRepo interface {
	List(ctx context.Context) ([]domain.Species, error)
	Get(ctx context.Context, slug string) (domain.Species, error)
}

// CareEventRepo is the append-only care log. *store.CareEventRepo satisfies it.
type CareEventRepo interface {
	Add(ctx context.Context, e domain.CareEvent) (domain.CareEvent, error)
	Void(ctx context.Context, id int64) error
	LatestByKind(ctx context.Context, plantID uuid.UUID) ([]domain.CareEvent, error)
	SinceDate(ctx context.Context, plantID uuid.UUID, kind domain.TaskKind, from domain.Date) ([]domain.CareEvent, error)
}

// CareTaskRepo is the per-plant task control: enable, snooze, interval
// override. *store.CareTaskRepo satisfies it. Nil leaves every catalog task
// enabled, which is what a boot without the repo wired used to do implicitly.
type CareTaskRepo interface {
	List(ctx context.Context, plantID uuid.UUID) ([]store.CareTask, error)
	Upsert(ctx context.Context, t store.CareTask) (store.CareTask, error)
}

// RoomLister is the optional Tado room picker. climate.IndoorClimateProvider satisfies it.
type RoomLister interface {
	Rooms(ctx context.Context) ([]climate.RoomClimate, error)
}

// EnvFunc builds the environment series for a plant. Nil means empty EnvSeries.
type EnvFunc func(ctx context.Context, plant domain.Plant, species domain.Species) care.EnvSeries

var (
	_ PlantRepo     = (*store.PlantRepo)(nil)
	_ SpeciesRepo   = (*store.SpeciesRepo)(nil)
	_ CareEventRepo = (*store.CareEventRepo)(nil)
	_ CareTaskRepo  = (*store.CareTaskRepo)(nil)
	_ RoomLister    = (*climate.NoopClimate)(nil)
)

// Server is the plant tracker UI. Health routes stay on RegisterHealth.
type Server struct {
	Plants   PlantRepo
	Species  SpeciesRepo
	Events   CareEventRepo
	Tasks    CareTaskRepo
	Clock    care.Clock
	Location *time.Location
	// WriteToken, when non-empty, is required as Authorization: Bearer on POSTs.
	WriteToken string
	Rooms      RoomLister
	// LastDigestFailed, when non-nil, drives the failed-digest banner.
	// NotificationRepo has no "last digest" query; cmd injects this.
	LastDigestFailed func(context.Context) bool
	Env              EnvFunc

	// Settings (C11). Nil optional seams render a disabled explanation, not a 500.
	Tado              TadoLinker
	Notifier          notify.Notifier
	BaseURL           string
	WeatherAge        WeatherAge
	Weather           WeatherSeries
	LocationKey       string
	Scheduler         SchedulerStat
	CatalogReady      bool
	LastNotifications func(context.Context) (NotificationSnapshot, error)

	pages    map[string]*template.Template
	tadoFlow *tadoFlowState
}

// NewServer copies opts, parses embedded templates, and defaults a nil Location to UTC.
// Clock must be non-nil; cmd passes a wall clock.
func NewServer(opts Server) *Server {
	if opts.Clock == nil {
		panic("web.NewServer: Clock is required")
	}
	s := opts
	if s.Location == nil {
		s.Location = time.UTC
	}
	s.pages = parsePages()
	s.tadoFlow = &tadoFlowState{}
	return &s
}

// Register mounts UI routes only. cmd already calls RegisterHealth.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", noIndex(s.dashboard))
	mux.HandleFunc("GET /plants", noIndex(s.listPlants))
	mux.HandleFunc("GET /plants/new", noIndex(s.newPlantGET))
	mux.HandleFunc("POST /plants/new", s.requireWrite(s.newPlantPOST))
	mux.HandleFunc("GET /plants/{id}", noIndex(s.plantDetail))
	mux.HandleFunc("GET /plants/{id}/edit", noIndex(s.editPlantGET))
	mux.HandleFunc("POST /plants/{id}/edit", s.requireWrite(s.editPlantPOST))
	mux.HandleFunc("POST /plants/{id}/delete", s.requireWrite(s.deletePlant))
	mux.HandleFunc("POST /plants/{id}/care/{kind}", s.requireWrite(s.logCare))
	mux.HandleFunc("POST /plants/{id}/tasks/{kind}", s.requireWrite(s.updateTask))
	mux.HandleFunc("POST /events/{id}/void", s.requireWrite(s.voidEvent))
	s.registerSettings(mux)
	mux.Handle("GET /static/", noIndex(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
	}))
}

func (s *Server) requireWrite(next http.HandlerFunc) http.HandlerFunc {
	return noIndex(func(w http.ResponseWriter, r *http.Request) {
		if s.WriteToken != "" {
			want := "Bearer " + s.WriteToken
			if r.Header.Get("Authorization") != want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	})
}

func (s *Server) loc() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

func (s *Server) today() domain.Date {
	return domain.TodayIn(s.Clock.Now(), s.loc())
}

func (s *Server) digestFailed(ctx context.Context) bool {
	if s.LastDigestFailed == nil {
		return false
	}
	return s.LastDigestFailed(ctx)
}

func (s *Server) envFor(ctx context.Context, p domain.Plant, sp domain.Species) care.EnvSeries {
	if s.Env == nil {
		return care.EnvSeries{}
	}
	return s.Env(ctx, p, sp)
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
