package care

import (
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestTaskActiveSnoozeRunsUntilItsDate(t *testing.T) {
	// snoozed_until is the day the task comes back, not the day it goes quiet.
	until := domain.Date{Year: 2026, Month: time.October, Day: 1}
	controls := []TaskControl{{Kind: domain.Water, Enabled: true, SnoozedUntil: &until}}
	tests := []struct {
		today domain.Date
		want  bool
	}{
		{domain.Date{Year: 2026, Month: time.September, Day: 19}, false},
		{domain.Date{Year: 2026, Month: time.September, Day: 30}, false},
		{domain.Date{Year: 2026, Month: time.October, Day: 1}, true},
		{domain.Date{Year: 2026, Month: time.October, Day: 15}, true},
	}
	for _, tc := range tests {
		if got := TaskActive(controls, domain.Water, tc.today); got != tc.want {
			t.Errorf("snoozed until %s, today %s: active = %v, want %v",
				until, tc.today, got, tc.want)
		}
	}
}

func TestTaskActiveDefaults(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	if !TaskActive(nil, domain.Water, today) {
		t.Error("no control row should leave the catalog task enabled")
	}
	off := []TaskControl{{Kind: domain.Prune, Enabled: false}}
	if TaskActive(off, domain.Prune, today) {
		t.Error("disabled task reported active")
	}
	if !TaskActive(off, domain.Water, today) {
		t.Error("a control for one kind must not suppress another")
	}
}

func speciesWithTasks() domain.Species {
	return domain.Species{
		Slug: "test", Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 7, MinIntervalDays: 1, MaxIntervalDays: 90,
		Prune:     &domain.FixedTask{IntervalDays: 90},
		Fertilize: &domain.FixedTask{IntervalDays: 30},
		Repot:     &domain.FixedTask{IntervalDays: 730},
	}
}

func kinds(tasks []ScheduledTask) []domain.TaskKind {
	out := make([]domain.TaskKind, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Kind)
	}
	return out
}

func TestScheduleAllGatesOnControls(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 7, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	sp := speciesWithTasks()
	env := EnvSeries{IndoorDataStale: true}

	all := kinds(ScheduleAll(p, sp, nil, env, nil, today))
	if len(all) != 4 {
		t.Fatalf("kinds = %v, want water+prune+fertilize+repot", all)
	}

	off := []TaskControl{{Kind: domain.Fertilize, Enabled: false}}
	got := kinds(ScheduleAll(p, sp, nil, env, off, today))
	for _, k := range got {
		if k == domain.Fertilize {
			t.Errorf("disabled fertilize still scheduled: %v", got)
		}
	}

	inGround := p
	inGround.InGround = true
	inGround.Location = domain.Outdoor
	for _, k := range kinds(ScheduleAll(inGround, sp, nil, EnvSeries{}, nil, today)) {
		if k == domain.Repot {
			t.Error("in-ground plant scheduled for repotting (SPEC §7.1)")
		}
	}
}

func TestScheduleAllAppliesIntervalOverride(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 7, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	sp := speciesWithTasks()
	seven := 7
	controls := []TaskControl{{Kind: domain.Fertilize, Enabled: true, IntervalDaysOverride: &seven}}
	for _, task := range ScheduleAll(p, sp, nil, EnvSeries{IndoorDataStale: true}, controls, today) {
		if task.Kind != domain.Fertilize {
			continue
		}
		if got := task.Due.On.Sub(today); got != seven {
			t.Fatalf("fertilize due in %d days, want the overridden %d (catalog says 30)", got, seven)
		}
		return
	}
	t.Fatal("fertilize was not scheduled at all")
}
