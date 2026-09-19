package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

// memTasks is an in-memory CareTaskRepo keyed the way the real unique index is.
type memTasks struct {
	mu   sync.Mutex
	rows map[string]store.CareTask
}

func newMemTasks() *memTasks { return &memTasks{rows: map[string]store.CareTask{}} }

func (m *memTasks) key(id uuid.UUID, k domain.TaskKind) string { return id.String() + "/" + string(k) }

func (m *memTasks) List(_ context.Context, plantID uuid.UUID) ([]store.CareTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.CareTask{}
	for _, t := range m.rows {
		if t.PlantID == plantID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *memTasks) ListForPlants(ctx context.Context, plantIDs []uuid.UUID) (map[uuid.UUID][]store.CareTask, error) {
	out := map[uuid.UUID][]store.CareTask{}
	for _, id := range plantIDs {
		rows, err := m.List(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			out[id] = rows
		}
	}
	return out, nil
}

func (m *memTasks) Upsert(_ context.Context, t store.CareTask) (store.CareTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[m.key(t.PlantID, t.Kind)] = t
	return t, nil
}

func thirstyPlant(t *testing.T, db *memDB) domain.Plant {
	t.Helper()
	acquired := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return mustCreate(t, db, domain.Plant{
		Name: "Overdue Monstera", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterMM: 180, FExposure: 1, Active: true, AcquiredAt: &acquired,
	})
}

// The README promises the dashboard and the digest can never disagree. Both
// read care_tasks through care.ScheduleAll, so a snooze must hide the task
// from the dashboard exactly as it does from the digest.
func TestDashboardHonoursSnooze(t *testing.T) {
	tasks := newMemTasks()
	_, db, mux := testUI(t, func(_ *memDB, s *Server) { s.Tasks = tasks })
	p := thirstyPlant(t, db)

	rec := doGET(t, mux, "/")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Overdue Monstera")

	// testNow is 2026-09-16; snooze past it.
	until := domain.Date{Year: 2026, Month: time.October, Day: 1}
	if _, err := tasks.Upsert(context.Background(), store.CareTask{
		PlantID: p.ID, Kind: domain.Water, Enabled: true, SnoozedUntil: &until,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rec = doGET(t, mux, "/")
	assertStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "Overdue Monstera") {
		t.Error("snoozed task still shown on the dashboard; dashboard and digest disagree")
	}
}

func TestDashboardHonoursDisabledTask(t *testing.T) {
	tasks := newMemTasks()
	_, db, mux := testUI(t, func(_ *memDB, s *Server) { s.Tasks = tasks })
	p := thirstyPlant(t, db)
	if _, err := tasks.Upsert(context.Background(), store.CareTask{
		PlantID: p.ID, Kind: domain.Water, Enabled: false,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	rec := doGET(t, mux, "/")
	if strings.Contains(rec.Body.String(), "Overdue Monstera") {
		t.Error("disabled task still shown on the dashboard")
	}
}

func TestUpdateTaskRoundTrips(t *testing.T) {
	tasks := newMemTasks()
	_, db, mux := testUI(t, func(_ *memDB, s *Server) { s.Tasks = tasks })
	p := thirstyPlant(t, db)

	rec := doPOST(t, mux, "/plants/"+p.ID.String()+"/tasks/fertilize", url.Values{
		"enabled":       {"1"},
		"interval_days": {"14"},
		"snooze_until":  {"2026-10-01"},
	})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("status %d, want a redirect", rec.Code)
	}
	rows, err := tasks.List(context.Background(), p.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
	got := rows[0]
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got.IntervalDaysOverride == nil || *got.IntervalDaysOverride != 14 {
		t.Errorf("IntervalDaysOverride = %v, want 14", got.IntervalDaysOverride)
	}
	if got.SnoozedUntil == nil || got.SnoozedUntil.String() != "2026-10-01" {
		t.Errorf("SnoozedUntil = %v, want 2026-10-01", got.SnoozedUntil)
	}
}

func TestUpdateTaskRejectsBadInput(t *testing.T) {
	tasks := newMemTasks()
	_, db, mux := testUI(t, func(_ *memDB, s *Server) { s.Tasks = tasks })
	p := thirstyPlant(t, db)
	base := "/plants/" + p.ID.String() + "/tasks/"
	for _, tc := range []struct {
		name string
		path string
		form url.Values
	}{
		{"unknown kind", base + "polish", url.Values{"enabled": {"1"}}},
		{"interval zero", base + "prune", url.Values{"enabled": {"1"}, "interval_days": {"0"}}},
		{"interval absurd", base + "prune", url.Values{"enabled": {"1"}, "interval_days": {"9999"}}},
		{"snooze not a date", base + "prune", url.Values{"enabled": {"1"}, "snooze_until": {"soon"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doPOST(t, mux, tc.path, tc.form)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", rec.Code)
			}
		})
	}
}

func TestPlantDetailRendersTaskSettings(t *testing.T) {
	tasks := newMemTasks()
	_, db, mux := testUI(t, func(_ *memDB, s *Server) { s.Tasks = tasks })
	p := thirstyPlant(t, db)
	rec := doGET(t, mux, "/plants/"+p.ID.String())
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec,
		"Task settings",
		"/plants/"+p.ID.String()+"/tasks/water",
		"snooze_until",
		"Watering is scheduled from the water balance",
	)
	assertNoExternalAssets(t, rec)
}

func TestPlantDetailWithoutTaskRepoStillRenders(t *testing.T) {
	// Tasks is an optional seam; a nil repo must degrade, not 500.
	_, db, mux := testUI(t, nil)
	p := thirstyPlant(t, db)
	rec := doGET(t, mux, "/plants/"+p.ID.String())
	assertStatus(t, rec, http.StatusOK)
}

// countingEvents counts round trips so the dashboard's query count can be
// asserted directly rather than inferred.
type countingEvents struct {
	CareEventRepo
	perPlant int
	batch    int
}

func (c *countingEvents) LatestByKind(ctx context.Context, id uuid.UUID) ([]domain.CareEvent, error) {
	c.perPlant++
	return c.CareEventRepo.LatestByKind(ctx, id)
}

func (c *countingEvents) LatestByKindForPlants(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]domain.CareEvent, error) {
	c.batch++
	return c.CareEventRepo.LatestByKindForPlants(ctx, ids)
}

type countingTasks struct {
	CareTaskRepo
	perPlant int
	batch    int
}

func (c *countingTasks) List(ctx context.Context, id uuid.UUID) ([]store.CareTask, error) {
	c.perPlant++
	return c.CareTaskRepo.List(ctx, id)
}

func (c *countingTasks) ListForPlants(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]store.CareTask, error) {
	c.batch++
	return c.CareTaskRepo.ListForPlants(ctx, ids)
}

func TestDashboardQueryCountIsFlatInPlantCount(t *testing.T) {
	var events *countingEvents
	var tasks *countingTasks
	_, db, mux := testUI(t, func(db *memDB, s *Server) {
		events = &countingEvents{CareEventRepo: db}
		tasks = &countingTasks{CareTaskRepo: newMemTasks()}
		s.Events = events
		s.Tasks = tasks
	})
	acquired := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		mustCreate(t, db, domain.Plant{
			Name: "Plant " + strconv.Itoa(i), SpeciesSlug: "monstera-deliciosa",
			Location: domain.Indoor, PotDiameterMM: 180, FExposure: 1,
			Active: true, AcquiredAt: &acquired,
		})
	}

	rec := doGET(t, mux, "/")
	assertStatus(t, rec, http.StatusOK)

	if events.perPlant != 0 || tasks.perPlant != 0 {
		t.Errorf("per-plant reads on the dashboard: %d events, %d tasks (want 0 of each)",
			events.perPlant, tasks.perPlant)
	}
	if events.batch != 1 || tasks.batch != 1 {
		t.Errorf("batch reads = %d events, %d tasks; want exactly 1 of each for 12 plants",
			events.batch, tasks.batch)
	}
}
