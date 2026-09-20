package care

import (
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func TestTaskActiveSnoozeRunsUntilItsDate(t *testing.T) {
	// snoozed_until is the day the task comes back, not the day it goes quiet.
	until := domain.Date{Year: 2026, Month: time.October, Day: 1}
	controls := []TaskControl{{Slug: domain.WaterSlug, Enabled: true, SnoozedUntil: &until}}
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
		if got := TaskActive(controls, domain.WaterSlug, tc.today); got != tc.want {
			t.Errorf("snoozed until %s, today %s: active = %v, want %v",
				until, tc.today, got, tc.want)
		}
	}
}

func TestTaskActiveDefaults(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	if !TaskActive(nil, domain.WaterSlug, today) {
		t.Error("no control row should leave the catalog task enabled")
	}
	off := []TaskControl{{Slug: "prune", Enabled: false}}
	if TaskActive(off, "prune", today) {
		t.Error("disabled task reported active")
	}
	if !TaskActive(off, domain.WaterSlug, today) {
		t.Error("a control for one slug must not suppress another")
	}
}

func speciesWithTasks() domain.Species {
	return domain.Species{
		Slug: "test", Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 7, MinIntervalDays: 1, MaxIntervalDays: 90,
		Tasks: []domain.SpeciesTask{
			{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 90},
			{Slug: "fertilize", Kind: domain.Fertilize, Label: "Fertilize", IntervalDays: 30},
			{Slug: "repot", Kind: domain.Repot, Label: "Repot", IntervalDays: 730},
		},
	}
}

// lavenderSpecies has two tasks of one kind — exactly the case a species with
// at most one FixedTask per kind could never express before C17/C18.
func lavenderSpecies() domain.Species {
	return domain.Species{
		Slug: "lavandula-angustifolia", Kc: 0.4, Substrate: domain.Cactus, MAD: 0.7,
		BaseIntervalDays: 8, MinIntervalDays: 4, MaxIntervalDays: 21,
		Tasks: []domain.SpeciesTask{
			{Slug: "prune-hard", Kind: domain.Prune, Label: "Hard prune",
				IntervalDays: 365, ActiveMonths: []time.Month{time.March}},
			{Slug: "trim-after-flowering", Kind: domain.Prune, Label: "Trim after flowering",
				IntervalDays: 365, ActiveMonths: []time.Month{time.August}},
		},
	}
}

func slugs(tasks []ScheduledTask) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Slug)
	}
	return out
}

func kinds(tasks []ScheduledTask) []domain.TaskKind {
	out := make([]domain.TaskKind, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Kind)
	}
	return out
}

func lavenderParams() Params {
	return Params{
		Location: domain.Outdoor, Kc: 0.4, Substrate: domain.Cactus, MAD: 0.7,
		BaseIntervalDays: 8, MinIntervalDays: 4, MaxIntervalDays: 21,
		FExposure: 1, FRain: 0.9, InGround: true,
	}
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

	off := []TaskControl{{Slug: "fertilize", Enabled: false}}
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
	controls := []TaskControl{{Slug: "fertilize", Enabled: true, IntervalDaysOverride: &seven}}
	for _, task := range ScheduleAll(p, sp, nil, EnvSeries{IndoorDataStale: true}, controls, today) {
		if task.Slug != "fertilize" {
			continue
		}
		if got := task.Due.On.Sub(today); got != seven {
			t.Fatalf("fertilize due in %d days, want the overridden %d (catalog says 30)", got, seven)
		}
		return
	}
	t.Fatal("fertilize was not scheduled at all")
}

// TestScheduleAllSchedulesTwoTasksOfOneKindIndependently is the engine-level
// proof of C17/C18's whole point: lavender's two prunings must appear as two
// distinct scheduled tasks with their own dates, not collapse into one.
func TestScheduleAllSchedulesTwoTasksOfOneKindIndependently(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.September, Day: 1}
	sp := lavenderSpecies()
	p := lavenderParams()

	hardSlug, trimSlug := "prune-hard", "trim-after-flowering"
	march := time.Date(2026, time.March, 15, 9, 0, 0, 0, time.UTC)
	august := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)
	events := []domain.CareEvent{
		{Kind: domain.Prune, TaskSlug: &hardSlug, DoneAt: march},
		{Kind: domain.Prune, TaskSlug: &trimSlug, DoneAt: august},
	}

	tasks := ScheduleAll(p, sp, events, EnvSeries{}, nil, today)
	byLabel := map[string]ScheduledTask{}
	pruneCount := 0
	for _, task := range tasks {
		if task.Kind != domain.Prune {
			continue // water is always scheduled too; this test only cares about prune
		}
		pruneCount++
		byLabel[task.Slug] = task
	}
	if pruneCount != 2 {
		t.Fatalf("scheduled %v prune tasks, want exactly 2 (prune-hard, trim-after-flowering): %v",
			pruneCount, slugs(tasks))
	}
	if byLabel[hardSlug].Due.Kind != domain.Prune || byLabel[trimSlug].Due.Kind != domain.Prune {
		t.Fatalf("both tasks should report kind=prune: %+v", tasks)
	}
	if byLabel[hardSlug].Label != "Hard prune" || byLabel[trimSlug].Label != "Trim after flowering" {
		t.Errorf("labels not carried through: %+v", tasks)
	}
	// Independent last-done dates: hard prune was 15 March + 365d, the trim
	// was 20 August + 365d — they must not share a due date.
	if byLabel[hardSlug].Due.On == byLabel[trimSlug].Due.On {
		t.Errorf("both prune tasks landed on %s; expected independent dates", byLabel[hardSlug].Due.On)
	}
}

func TestScheduleAllControlsApplyPerSlugNotPerKind(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.September, Day: 1}
	sp := lavenderSpecies()
	p := lavenderParams()

	off := []TaskControl{{Slug: "prune-hard", Enabled: false}}
	var pruneSlugs []string
	for _, task := range ScheduleAll(p, sp, nil, EnvSeries{}, off, today) {
		if task.Kind == domain.Prune {
			pruneSlugs = append(pruneSlugs, task.Slug)
		}
	}
	if len(pruneSlugs) != 1 || pruneSlugs[0] != "trim-after-flowering" {
		t.Fatalf("disabling prune-hard should leave trim-after-flowering scheduled; got prune tasks %v", pruneSlugs)
	}
}

func TestScheduleAllWaterCarriesSlugAndLabel(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.June, Day: 1}
	p := Params{
		Location: domain.Indoor, Kc: 0.7, Substrate: domain.Peat, MAD: 0.5,
		BaseIntervalDays: 7, MinIntervalDays: 1, MaxIntervalDays: 90,
		FExposure: 1, PotDiameterMM: 180,
	}
	tasks := ScheduleAll(p, domain.Species{}, nil, EnvSeries{IndoorDataStale: true}, nil, today)
	if len(tasks) != 1 || tasks[0].Slug != domain.WaterSlug || tasks[0].Kind != domain.Water {
		t.Fatalf("water task = %+v, want slug=%s kind=water", tasks, domain.WaterSlug)
	}
	if tasks[0].Label == "" {
		t.Error("water task has no label")
	}
}
