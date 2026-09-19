package web

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

type memDB struct {
	mu      sync.Mutex
	plants  map[uuid.UUID]domain.Plant
	species map[string]domain.Species
	events  []domain.CareEvent
	nextEv  int64
	clock   care.Clock
}

func newMem(clock care.Clock) *memDB {
	return &memDB{
		plants:  map[uuid.UUID]domain.Plant{},
		species: map[string]domain.Species{},
		clock:   clock,
	}
}

func (m *memDB) addSpecies(s domain.Species) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.species[s.Slug] = s
}

func (m *memDB) Create(_ context.Context, p domain.Plant) (domain.Plant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	m.plants[p.ID] = p
	return p, nil
}

func (m *memDB) Get(_ context.Context, id uuid.UUID) (domain.Plant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plants[id]
	if !ok {
		return domain.Plant{}, store.ErrNotFound
	}
	return p, nil
}

func (m *memDB) Update(_ context.Context, p domain.Plant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.plants[p.ID]; !ok {
		return store.ErrNotFound
	}
	m.plants[p.ID] = p
	return nil
}

func (m *memDB) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plants[id]
	if !ok {
		return store.ErrNotFound
	}
	p.Active = false
	m.plants[id] = p
	return nil
}

func (m *memDB) List(context.Context) ([]store.PlantWithSpecies, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.PlantWithSpecies, 0, len(m.plants))
	for _, p := range m.plants {
		sp, ok := m.species[p.SpeciesSlug]
		if !ok {
			continue
		}
		out = append(out, store.PlantWithSpecies{Plant: p, Species: sp})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plant.Name != out[j].Plant.Name {
			return out[i].Plant.Name < out[j].Plant.Name
		}
		return out[i].Plant.ID.String() < out[j].Plant.ID.String()
	})
	return out, nil
}

type memSpecies struct{ db *memDB }

func (m memSpecies) List(context.Context) ([]domain.Species, error) {
	m.db.mu.Lock()
	defer m.db.mu.Unlock()
	out := make([]domain.Species, 0, len(m.db.species))
	for _, s := range m.db.species {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (m memSpecies) Get(_ context.Context, slug string) (domain.Species, error) {
	m.db.mu.Lock()
	defer m.db.mu.Unlock()
	s, ok := m.db.species[slug]
	if !ok {
		return domain.Species{}, store.ErrNotFound
	}
	return s, nil
}

func (m *memDB) Add(_ context.Context, e domain.CareEvent) (domain.CareEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextEv++
	e.ID = m.nextEv
	m.events = append(m.events, e)
	return e, nil
}

func (m *memDB) Void(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.events {
		if m.events[i].ID != id {
			continue
		}
		if m.events[i].VoidedAt != nil {
			return store.ErrNotFound
		}
		now := m.clock.Now()
		m.events[i].VoidedAt = &now
		return nil
	}
	return store.ErrNotFound
}

func (m *memDB) LatestByKind(_ context.Context, plantID uuid.UUID) ([]domain.CareEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	best := map[domain.TaskKind]domain.CareEvent{}
	for _, e := range m.events {
		if e.PlantID != plantID || e.VoidedAt != nil {
			continue
		}
		cur, ok := best[e.Kind]
		if !ok || e.DoneAt.After(cur.DoneAt) || (e.DoneAt.Equal(cur.DoneAt) && e.ID > cur.ID) {
			best[e.Kind] = e
		}
	}
	out := make([]domain.CareEvent, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	return out, nil
}

func (m *memDB) SinceDate(_ context.Context, plantID uuid.UUID, kind domain.TaskKind, from domain.Date) ([]domain.CareEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	start := time.Date(from.Year, from.Month, from.Day, 0, 0, 0, 0, time.UTC)
	var out []domain.CareEvent
	for _, e := range m.events {
		if e.PlantID == plantID && e.Kind == kind && !e.DoneAt.Before(start) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DoneAt.Equal(out[j].DoneAt) {
			return out[i].DoneAt.Before(out[j].DoneAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

type roomList []climate.RoomClimate

func (r roomList) Rooms(context.Context) ([]climate.RoomClimate, error) {
	return r, nil
}

func (m *memDB) LatestByKindForPlants(ctx context.Context, plantIDs []uuid.UUID) (map[uuid.UUID][]domain.CareEvent, error) {
	out := map[uuid.UUID][]domain.CareEvent{}
	for _, id := range plantIDs {
		ev, err := m.LatestByKind(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(ev) > 0 {
			out[id] = ev
		}
	}
	return out, nil
}
