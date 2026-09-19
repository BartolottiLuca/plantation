package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeNotify struct {
	mu      sync.Mutex
	sent    []string
	kinds   map[string]string
	fail    error
	failKey string
}

func newFakeNotify() *fakeNotify {
	return &fakeNotify{kinds: map[string]string{}}
}

func (f *fakeNotify) SendOnce(_ context.Context, dedupeKey, kind string, msg notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil && (f.failKey == "" || f.failKey == dedupeKey) {
		return f.fail
	}
	if _, ok := f.kinds[dedupeKey]; ok {
		return nil
	}
	if msg.Title == "" && msg.Description == "" {
		f.kinds[dedupeKey] = "skipped"
		f.sent = append(f.sent, dedupeKey)
		return nil
	}
	f.kinds[dedupeKey] = kind
	f.sent = append(f.sent, dedupeKey)
	return nil
}

func (f *fakeNotify) keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, k := range f.sent {
		if len(prefix) == 0 || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out
}

func (f *fakeNotify) digestSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, k := range f.sent {
		if len(k) >= 7 && k[:7] == "digest:" && f.kinds[k] == "digest" {
			out = append(out, k)
		}
	}
	return out
}

func (f *fakeNotify) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.kinds[key]
	return ok
}

type fakeWeather struct {
	mu        sync.Mutex
	series    care.EnvSeries
	refreshN  int
	err       error
	seriesErr error
}

func (w *fakeWeather) Refresh(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refreshN++
	return w.err
}

func (w *fakeWeather) Series(context.Context, domain.Date, domain.Date) (care.EnvSeries, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seriesErr != nil {
		return care.EnvSeries{}, w.seriesErr
	}
	return w.series, nil
}

type fakeAge struct {
	at  time.Time
	err error
}

func (a fakeAge) LastFetchedAt(context.Context, string) (time.Time, error) {
	return a.at, a.err
}

type fakePlants struct {
	rows []store.PlantWithSpecies
	err  error
}

func (p *fakePlants) List(context.Context) ([]store.PlantWithSpecies, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.rows, nil
}

type fakeEvents struct {
	mu      sync.Mutex
	byPlant map[uuid.UUID][]domain.CareEvent
	err     error
}

func (e *fakeEvents) LatestByKind(_ context.Context, plantID uuid.UUID) ([]domain.CareEvent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	return e.byPlant[plantID], nil
}

type fakeSampler struct {
	n      int
	err    error
	status climate.LinkStatus
}

func (s *fakeSampler) Sample(context.Context) error {
	s.n++
	return s.err
}

func (s *fakeSampler) Status(context.Context) climate.LinkStatus {
	if s.status.State == "" {
		return climate.LinkStatus{State: climate.StateUnlinked}
	}
	return s.status
}

type fakeSweep struct {
	n   int
	err error
}

func (s *fakeSweep) SweepStale(context.Context, time.Duration) error {
	s.n++
	return s.err
}

type fakeRetain struct {
	mu     sync.Mutex
	n      int
	before time.Time
	err    error
}

func (r *fakeRetain) PurgeOlderThan(_ context.Context, before time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	r.before = before
	return r.err
}

func testSpecies() domain.Species {
	return domain.Species{
		Slug:             "test-shrub",
		CommonName:       "Test shrub",
		Placement:        domain.Outdoor,
		Kc:               0.8,
		Substrate:        domain.Peat,
		MAD:              0.5,
		BaseIntervalDays: 7,
		MinIntervalDays:  7,
		MaxIntervalDays:  14,
		MinTempC:         2,
		FrostTender:      true,
	}
}

func testPlant(id uuid.UUID, watered time.Time) store.PlantWithSpecies {
	return store.PlantWithSpecies{
		Plant: domain.Plant{
			ID:            id,
			Name:          "Alice",
			SpeciesSlug:   "test-shrub",
			Location:      domain.Outdoor,
			PotDiameterMM: 200,
			FExposure:     1.0,
			FRain:         0.0,
			Active:        true,
			AcquiredAt:    &watered,
		},
		Species: testSpecies(),
	}
}

func (e *fakeEvents) LatestByKindForPlants(_ context.Context, plantIDs []uuid.UUID) (map[uuid.UUID][]domain.CareEvent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	out := map[uuid.UUID][]domain.CareEvent{}
	for _, id := range plantIDs {
		if ev, ok := e.byPlant[id]; ok {
			out[id] = ev
		}
	}
	return out, nil
}
