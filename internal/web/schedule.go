package web

import (
	"context"
	"fmt"
	"sort"
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

var historyKinds = []domain.TaskKind{
	domain.Water, domain.Prune, domain.Fertilize, domain.Repot, domain.Inspect,
}

type scheduled struct {
	Plant   domain.Plant
	Species domain.Species
	Due     care.Due
	Expl    care.Explanation
}

type taskView struct {
	PlantID     uuid.UUID
	PlantName   string
	Kind        domain.TaskKind
	KindLabel   string
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

	var out []scheduled
	due, expl := care.ScheduleWater(params, events, env, today)
	out = append(out, scheduled{Plant: p, Species: sp, Due: due, Expl: expl})

	// CareTask.List is not consulted: this card schedules from the species
	// catalog only (water always; prune/fertilize/repot when the species
	// defines them). Disabled-task rows would need a store query cmd can
	// add later.
	if sp.Prune != nil {
		d, e := care.ScheduleFixed(*sp.Prune, domain.Prune, events, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: d, Expl: e})
	}
	if sp.Fertilize != nil {
		d, e := care.ScheduleFixed(*sp.Fertilize, domain.Fertilize, events, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: d, Expl: e})
	}
	if sp.Repot != nil {
		d, e := care.ScheduleFixed(*sp.Repot, domain.Repot, events, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: d, Expl: e})
	}
	return out, nil
}

func (s *Server) scheduleRows(ctx context.Context, rows []store.PlantWithSpecies) ([]scheduled, error) {
	var out []scheduled
	for _, row := range rows {
		tasks, err := s.schedulePlant(ctx, row.Plant, row.Species)
		if err != nil {
			return nil, err
		}
		out = append(out, tasks...)
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
		return tasks[i].Kind < tasks[j].Kind
	})
}

func taskOf(t scheduled) taskView {
	return taskView{
		PlantID:     t.Plant.ID,
		PlantName:   t.Plant.Name,
		Kind:        t.Due.Kind,
		KindLabel:   kindLabel(t.Due.Kind),
		Summary:     t.Expl.Summary,
		DueOn:       t.Due.On.String(),
		Status:      t.Due.Status,
		StatusLabel: statusLabel(t.Due.Status),
	}
}

func kindLabel(k domain.TaskKind) string {
	switch k {
	case domain.Water:
		return "Water"
	case domain.Prune:
		return "Prune"
	case domain.Fertilize:
		return "Fertilize"
	case domain.Repot:
		return "Repot"
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
		KindLabel:       kindLabel(t.Due.Kind),
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

func (s *Server) history(ctx context.Context, p domain.Plant) ([]eventView, error) {
	from := historyFrom
	if p.AcquiredAt != nil && !p.AcquiredAt.IsZero() {
		from = domain.TodayIn(*p.AcquiredAt, s.loc())
	}
	var all []domain.CareEvent
	for _, k := range historyKinds {
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
			Kind:   kindLabel(e.Kind),
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
