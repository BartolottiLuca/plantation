package web

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

const upcomingHorizonDays = 7

// historyFrom is used when a plant has no acquired_at so SinceDate still
// returns the full log. There is no list-all-events store method.
var historyFrom = domain.Date{Year: 2000, Month: time.January, Day: 1}

// scheduled is one task a plant owes, carried alongside the plant and species
// it belongs to so a template can render both without a second lookup. Slug
// and Label come straight from care.ScheduledTask — the engine is the single
// source of what a task is called, so this package never re-derives a label
// from Due.Kind alone (two tasks can share a kind).
type scheduled struct {
	Plant   domain.Plant
	Species domain.Species
	Slug    string
	Label   string
	Due     care.Due
	Expl    care.Explanation
}

type taskView struct {
	PlantID     uuid.UUID
	PlantName   string
	Slug        string
	Kind        domain.TaskKind
	Label       string
	Summary     string
	DueOn       string
	Status      care.Status
	StatusLabel string
	Preselected bool
}

type explPanel struct {
	Kind            string
	KindLabel       string
	Mode            string
	CapacityMM      string
	DeficitMM       string
	ThresholdMM     string
	DepletionPct    string
	MeanETcMMPerDay string
	EffectiveRainMM string
	Deferred        bool
	DeferredReason  string
	ClampedBy       string
	OutdoorStale    bool
	IndoorDataStale bool
	Summary         string
	EmptyEnv        bool
}

type eventView struct {
	ID     int64
	Kind   string
	DoneOn string
	Source string
	Note   string
}

func (s *Server) activeRows(ctx context.Context) ([]store.PlantWithSpecies, error) {
	rows, err := s.Plants.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing plants: %w", err)
	}
	out := make([]store.PlantWithSpecies, 0, len(rows))
	for _, row := range rows {
		if row.Plant.Active {
			out = append(out, row)
		}
	}
	return out, nil
}

func toScheduled(p domain.Plant, sp domain.Species, tasks []care.ScheduledTask) []scheduled {
	out := make([]scheduled, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, scheduled{
			Plant: p, Species: sp, Slug: t.Slug, Label: t.Label, Due: t.Due, Expl: t.Expl,
		})
	}
	return out
}

func (s *Server) schedulePlant(ctx context.Context, p domain.Plant, sp domain.Species) ([]scheduled, error) {
	if sp.Slug == "" {
		return nil, nil
	}
	events, err := s.Events.LatestByKind(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("latest care events: %w", err)
	}
	today := s.today()
	env := s.envFor(ctx, p, sp)
	params := care.Effective(p, sp)

	controls, err := s.taskControls(ctx, p.ID)
	if err != nil {
		return nil, err
	}

	return toScheduled(p, sp, care.ScheduleAll(params, sp, events, env, controls, today)), nil
}

// taskControls reads the per-plant overrides the digest also applies. Both
// sides go through care.ScheduleAll so the dashboard and the digest cannot
// disagree about what is due (SPEC §6).
func (s *Server) taskControls(ctx context.Context, plantID uuid.UUID) ([]care.TaskControl, error) {
	if s.Tasks == nil {
		return nil, nil
	}
	rows, err := s.Tasks.List(ctx, plantID)
	if err != nil {
		return nil, fmt.Errorf("listing care tasks: %w", err)
	}
	return toControls(rows), nil
}

func toControls(rows []store.CareTask) []care.TaskControl {
	out := make([]care.TaskControl, 0, len(rows))
	for _, t := range rows {
		out = append(out, care.TaskControl{
			Slug:                 t.TaskSlug,
			Enabled:              t.Enabled,
			IntervalDaysOverride: t.IntervalDaysOverride,
			SnoozedUntil:         t.SnoozedUntil,
		})
	}
	return out
}

// scheduleRows schedules a whole list with two queries rather than two per
// plant: the dashboard renders every plant at once, so per-plant reads here are
// one round trip each.
func (s *Server) scheduleRows(ctx context.Context, rows []store.PlantWithSpecies) ([]scheduled, error) {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Plant.ID)
	}
	events, err := s.Events.LatestByKindForPlants(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("latest care events: %w", err)
	}
	controls, err := s.taskControlsForPlants(ctx, ids)
	if err != nil {
		return nil, err
	}

	today := s.today()
	var out []scheduled
	for _, row := range rows {
		if row.Species.Slug == "" {
			continue
		}
		params := care.Effective(row.Plant, row.Species)
		env := s.envFor(ctx, row.Plant, row.Species)
		tasks := care.ScheduleAll(params, row.Species, events[row.Plant.ID], env, controls[row.Plant.ID], today)
		out = append(out, toScheduled(row.Plant, row.Species, tasks)...)
	}
	return out, nil
}

func (s *Server) taskControlsForPlants(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]care.TaskControl, error) {
	out := map[uuid.UUID][]care.TaskControl{}
	if s.Tasks == nil || len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Tasks.ListForPlants(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("listing care tasks: %w", err)
	}
	for id, tasks := range rows {
		out[id] = toControls(tasks)
	}
	return out, nil
}

func (s *Server) groupDashboard(tasks []scheduled) (overdue, dueToday, upcoming []taskView) {
	today := s.today()
	horizon := today.AddDays(upcomingHorizonDays)
	for _, t := range tasks {
		v := taskOf(t)
		switch t.Due.Status {
		case care.StatusOverdue:
			overdue = append(overdue, v)
		case care.StatusDueToday:
			dueToday = append(dueToday, v)
		case care.StatusUpcoming:
			if !horizon.Before(t.Due.On) {
				upcoming = append(upcoming, v)
			}
		}
	}
	sortTasks(overdue)
	sortTasks(dueToday)
	sortTasks(upcoming)
	return overdue, dueToday, upcoming
}

func sortTasks(tasks []taskView) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].DueOn != tasks[j].DueOn {
			return tasks[i].DueOn < tasks[j].DueOn
		}
		if tasks[i].PlantName != tasks[j].PlantName {
			return tasks[i].PlantName < tasks[j].PlantName
		}
		return tasks[i].Slug < tasks[j].Slug
	})
}

func taskOf(t scheduled) taskView {
	return taskView{
		PlantID:     t.Plant.ID,
		PlantName:   t.Plant.Name,
		Slug:        t.Slug,
		Kind:        t.Due.Kind,
		Label:       t.Label,
		Summary:     t.Expl.Summary,
		DueOn:       t.Due.On.String(),
		Status:      t.Due.Status,
		StatusLabel: statusLabel(t.Due.Status),
	}
}

// kindLabel is the fallback display name for a kind with no specific task
// label available — a legacy care-history event, or an event whose task has
// since been removed from the catalog. Anywhere a real domain.SpeciesTask (or
// the water pseudo-task) is in hand, its Label is used instead of this.
func kindLabel(k domain.TaskKind) string {
	switch k {
	case domain.Water:
		return "Water"
	case domain.Prune:
		return "Prune"
	case domain.Pinch:
		return "Pinch"
	case domain.Deadhead:
		return "Deadhead"
	case domain.Fertilize:
		return "Fertilize"
	case domain.TopDress:
		return "Top-dress"
	case domain.Repot:
		return "Repot"
	case domain.Divide:
		return "Divide"
	case domain.Harvest:
		return "Harvest"
	case domain.Mulch:
		return "Mulch"
	case domain.Stake:
		return "Stake"
	case domain.Inspect:
		return "Inspect"
	default:
		return string(k)
	}
}

func statusLabel(st care.Status) string {
	switch st {
	case care.StatusOverdue:
		return "Overdue"
	case care.StatusDueToday:
		return "Due today"
	case care.StatusUpcoming:
		return "Upcoming"
	default:
		return string(st)
	}
}

func waterPanel(tasks []scheduled) explPanel {
	for _, t := range tasks {
		if t.Due.Kind == domain.Water {
			return panelOf(t)
		}
	}
	return explPanel{}
}

func panelOf(t scheduled) explPanel {
	e := t.Expl
	clamped := e.ClampedBy
	if clamped == "" {
		clamped = "none"
	}
	return explPanel{
		Kind:            string(t.Due.Kind),
		KindLabel:       t.Label,
		Mode:            e.Mode,
		CapacityMM:      fmt.Sprintf("%.1f", e.CapacityMM),
		DeficitMM:       fmt.Sprintf("%.1f", e.DeficitMM),
		ThresholdMM:     fmt.Sprintf("%.1f", e.ThresholdMM),
		DepletionPct:    fmt.Sprintf("%.0f%%", e.DepletionPct*100),
		MeanETcMMPerDay: fmt.Sprintf("%.2f", e.MeanETcMMPerDay),
		EffectiveRainMM: fmt.Sprintf("%.1f", e.EffectiveRainMM),
		Deferred:        e.Deferred,
		DeferredReason:  e.DeferredReason,
		ClampedBy:       clamped,
		OutdoorStale:    e.OutdoorStale,
		IndoorDataStale: e.IndoorDataStale,
		Summary:         e.Summary,
		EmptyEnv:        e.Mode == care.ModeBaseInterval,
	}
}

// taskLabel resolves the best available name for a logged event: the species
// task it names by slug, if that task still exists in the catalog, else the
// generic kind label — which is also what a legacy (nil-slug) event gets,
// since there is nothing more specific to show for one.
func taskLabel(sp domain.Species, e domain.CareEvent) string {
	if e.TaskSlug != nil {
		for _, t := range sp.Tasks {
			if t.Slug == *e.TaskSlug {
				return t.Label
			}
		}
	}
	return kindLabel(e.Kind)
}

func (s *Server) history(ctx context.Context, p domain.Plant, sp domain.Species) ([]eventView, error) {
	from := historyFrom
	if p.AcquiredAt != nil && !p.AcquiredAt.IsZero() {
		from = domain.TodayIn(*p.AcquiredAt, s.loc())
	}
	var all []domain.CareEvent
	for _, k := range domain.TaskKinds() {
		ev, err := s.Events.SinceDate(ctx, p.ID, k, from)
		if err != nil {
			return nil, fmt.Errorf("listing %s events: %w", k, err)
		}
		all = append(all, ev...)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].DoneAt.Equal(all[j].DoneAt) {
			return all[i].DoneAt.After(all[j].DoneAt)
		}
		return all[i].ID > all[j].ID
	})
	out := make([]eventView, 0, len(all))
	for _, e := range all {
		if e.VoidedAt != nil {
			continue
		}
		out = append(out, eventView{
			ID:     e.ID,
			Kind:   taskLabel(sp, e),
			DoneOn: domain.TodayIn(e.DoneAt, s.loc()).String(),
			Source: e.Source,
			Note:   e.Note,
		})
	}
	return out, nil
}

func worstStatus(tasks []scheduled) (care.Status, string) {
	var best care.Status
	summary := ""
	rank := func(st care.Status) int {
		switch st {
		case care.StatusOverdue:
			return 3
		case care.StatusDueToday:
			return 2
		case care.StatusUpcoming:
			return 1
		default:
			return 0
		}
	}
	for _, t := range tasks {
		if rank(t.Due.Status) > rank(best) {
			best = t.Due.Status
			summary = t.Expl.Summary
		}
	}
	return best, summary
}

// controlView is one row of the per-plant task settings form.
type controlView struct {
	PlantID      string
	Slug         string
	Label        string
	Enabled      bool
	IntervalDays string // "" when the catalog interval is in force
	CatalogDays  int    // 0 for water, whose interval is physics-derived
	SnoozedUntil string // "" when not snoozed, else YYYY-MM-DD
	Suppressed   bool   // not running today, for whatever reason
}

// taskControlViews lists every task this plant can have, joined with whatever
// control rows exist. It walks sp.Tasks directly — the same list
// care.ScheduleAll walks — so the form can never offer a task the engine
// would not schedule, and two tasks sharing a kind render as two distinct
// rows rather than one.
func (s *Server) taskControlViews(ctx context.Context, p domain.Plant, sp domain.Species) ([]controlView, error) {
	if sp.Slug == "" {
		return nil, nil
	}
	controls, err := s.taskControls(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]care.TaskControl, len(controls))
	for _, c := range controls {
		bySlug[c.Slug] = c
	}

	type entry struct {
		slug, label string
		catalogDays int
	}
	entries := make([]entry, 0, len(sp.Tasks)+1)
	entries = append(entries, entry{slug: domain.WaterSlug, label: "Water"})
	for _, t := range sp.Tasks {
		if t.Kind == domain.Repot && p.InGround {
			continue // SPEC §7.1: an in-ground plant is never repotted.
		}
		entries = append(entries, entry{slug: t.Slug, label: t.Label, catalogDays: t.IntervalDays})
	}

	today := s.today()
	out := make([]controlView, 0, len(entries))
	for _, en := range entries {
		v := controlView{
			PlantID:     p.ID.String(),
			Slug:        en.slug,
			Label:       en.label,
			Enabled:     true,
			CatalogDays: en.catalogDays,
			Suppressed:  !care.TaskActive(controls, en.slug, today),
		}
		if c, ok := bySlug[en.slug]; ok {
			v.Enabled = c.Enabled
			if c.IntervalDaysOverride != nil {
				v.IntervalDays = strconv.Itoa(*c.IntervalDaysOverride)
			}
			if c.SnoozedUntil != nil {
				v.SnoozedUntil = c.SnoozedUntil.String()
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// findTask resolves a slug from a route into the TaskKind that goes on a
// domain.CareEvent / store.CareTask, and reports whether it names a real
// task: the reserved water slug, or one of sp.Tasks. This is the single place
// a slug from a URL is validated against what a species actually offers,
// mirroring how parseKind validated a fixed kind set before task identity
// existed.
func findTask(sp domain.Species, slug string) (kind domain.TaskKind, label string, ok bool) {
	if slug == domain.WaterSlug {
		return domain.Water, "Water", true
	}
	for _, t := range sp.Tasks {
		if t.Slug == slug {
			return t.Kind, t.Label, true
		}
	}
	return "", "", false
}
