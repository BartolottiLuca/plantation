package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

const (
	fleeceMaxMinTempC = 2.0
	fleeceProtectionC = 4.0
)

type scheduled struct {
	Plant   domain.Plant
	Species domain.Species
	Slug    string
	Label   string
	Due     care.Due
	Expl    care.Explanation
}

func (l *Loop) digestAndAlerts(ctx context.Context) error {
	if l.Notify == nil || l.Plants == nil || l.Events == nil {
		return nil
	}
	today := l.today()

	rows, err := l.Plants.List(ctx)
	if err != nil {
		return fmt.Errorf("listing plants: %w", err)
	}

	active := make([]store.PlantWithSpecies, 0, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.Plant.Active {
			active = append(active, row)
			ids = append(ids, row.Plant.ID)
		}
	}
	events, err := l.Events.LatestByKindForPlants(ctx, ids)
	if err != nil {
		return fmt.Errorf("latest care events: %w", err)
	}
	controls, err := l.taskControlsForPlants(ctx, ids)
	if err != nil {
		return fmt.Errorf("listing care tasks: %w", err)
	}

	var tasks []scheduled
	for _, row := range active {
		env := l.PlantEnv(ctx, row.Plant, row.Species)
		params := care.Effective(row.Plant, row.Species)
		for _, t := range care.ScheduleAll(params, row.Species, events[row.Plant.ID], env, controls[row.Plant.ID], today) {
			tasks = append(tasks, scheduled{
				Plant: row.Plant, Species: row.Species, Slug: t.Slug, Label: t.Label, Due: t.Due, Expl: t.Expl,
			})
		}
	}

	l.sendAlerts(ctx, today, rows, tasks)

	var overdue, dueToday []notify.DigestLine
	for _, t := range tasks {
		line := notify.DigestLine{
			PlantID:   t.Plant.ID,
			PlantName: t.Plant.Name,
			Task:      t.Label,
			Summary:   t.Expl.Summary,
		}
		switch t.Due.Status {
		case care.StatusOverdue:
			overdue = append(overdue, line)
		case care.StatusDueToday:
			dueToday = append(dueToday, line)
		}
	}
	msg := notify.FormatDigest(l.BaseURL, overdue, dueToday)
	key := "digest:" + today.String()
	if err := l.Notify.SendOnce(ctx, key, "digest", msg); err != nil {
		l.ops(ctx, today, "digest_failed", "Today's digest could not be delivered.")
		return fmt.Errorf("sending digest: %w", err)
	}
	return nil
}

func (l *Loop) taskControlsForPlants(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]care.TaskControl, error) {
	out := map[uuid.UUID][]care.TaskControl{}
	if l.Tasks == nil || len(ids) == 0 {
		return out, nil
	}
	rows, err := l.Tasks.ListForPlants(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, tasks := range rows {
		out[id] = taskControls(tasks)
	}
	return out, nil
}

func taskControls(tasks []store.CareTask) []care.TaskControl {
	out := make([]care.TaskControl, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, care.TaskControl{
			Slug:                 t.TaskSlug,
			Enabled:              t.Enabled,
			IntervalDaysOverride: t.IntervalDaysOverride,
			SnoozedUntil:         t.SnoozedUntil,
		})
	}
	return out
}

func (l *Loop) sendAlerts(ctx context.Context, today domain.Date, rows []store.PlantWithSpecies, tasks []scheduled) {
	l.frostAlerts(ctx, today, rows)
	l.heatwaveAlert(ctx, today, rows)
	l.rainSkipAlert(ctx, today, tasks)
	l.opsAlerts(ctx, today)
}

func (l *Loop) frostAlerts(ctx context.Context, today domain.Date, rows []store.PlantWithSpecies) {
	series, err := l.outdoorSeries(ctx)
	if err != nil {
		l.Log.Error("frost: weather series", "err", err)
		return
	}
	for _, row := range rows {
		if !row.Plant.Active || row.Plant.Location != domain.Outdoor || !row.Species.FrostTender {
			continue
		}
		var nights []notify.FrostNight
		for _, day := range series.Days {
			if day.Date.Before(today) || day.Observed {
				continue
			}
			if day.TMinC > row.Species.MinTempC {
				continue
			}
			nights = append(nights, notify.FrostNight{Date: day.Date, TempC: day.TMinC})
		}
		if len(nights) == 0 {
			continue
		}
		sort.Slice(nights, func(i, j int) bool {
			return nights[i].Date.Before(nights[j].Date)
		})
		key := fmt.Sprintf("frost:%s:%s", row.Plant.ID, nights[0].Date)
		url := strings.TrimRight(l.BaseURL, "/") + "/plants/" + row.Plant.ID.String()
		msg := notify.FormatFrost(row.Plant.Name, nights, frostAdvice(row, nights), url)
		if err := l.Notify.SendOnce(ctx, key, "frost", msg); err != nil {
			l.Log.Error("sending frost alert", "err", err)
		}
	}
}

func frostAdvice(row store.PlantWithSpecies, nights []notify.FrostNight) notify.FrostAdvice {
	if !row.Plant.InGround {
		return notify.FrostAdviceBringIndoors
	}
	if row.Species.MinTempC > fleeceMaxMinTempC {
		return notify.FrostAdviceLift
	}
	for _, night := range nights {
		if night.TempC < row.Species.MinTempC-fleeceProtectionC {
			return notify.FrostAdviceLift
		}
	}
	return notify.FrostAdviceFleece
}

func (l *Loop) heatwaveAlert(ctx context.Context, today domain.Date, rows []store.PlantWithSpecies) {
	series, err := l.outdoorSeries(ctx)
	if err != nil {
		l.Log.Error("heatwave: weather series", "err", err)
		return
	}
	byDate := map[string]float64{}
	for _, day := range series.Days {
		if day.Date.Before(today) || day.Observed {
			continue
		}
		if day.TMaxC >= heatwaveCelsius {
			if cur, ok := byDate[day.Date.String()]; !ok || day.TMaxC > cur {
				byDate[day.Date.String()] = day.TMaxC
			}
		}
	}
	if len(byDate) == 0 {
		return
	}
	var names []string
	seen := map[string]struct{}{}
	for _, row := range rows {
		if !row.Plant.Active || row.Plant.Location != domain.Outdoor {
			continue
		}
		if _, ok := seen[row.Plant.Name]; ok {
			continue
		}
		seen[row.Plant.Name] = struct{}{}
		names = append(names, row.Plant.Name)
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	for date, tmax := range byDate {
		d, err := parseDate(date)
		if err != nil {
			continue
		}
		key := "heatwave:" + date
		msg := notify.FormatHeatwave(l.BaseURL, names, d, tmax)
		if err := l.Notify.SendOnce(ctx, key, "heatwave", msg); err != nil {
			l.Log.Error("sending heatwave alert", "err", err)
		}
	}
}

func (l *Loop) rainSkipAlert(ctx context.Context, today domain.Date, tasks []scheduled) {
	var names []string
	seen := map[uuid.UUID]struct{}{}
	for _, t := range tasks {
		if t.Due.Kind != domain.Water || !t.Expl.Deferred {
			continue
		}
		if _, ok := seen[t.Plant.ID]; ok {
			continue
		}
		seen[t.Plant.ID] = struct{}{}
		names = append(names, t.Plant.Name)
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	key := "rain_skip:" + today.String()
	msg := notify.FormatRainSkip(l.BaseURL, names, today)
	if err := l.Notify.SendOnce(ctx, key, "rain_skip", msg); err != nil {
		l.Log.Error("sending rain-skip alert", "err", err)
	}
}

func (l *Loop) opsAlerts(ctx context.Context, today domain.Date) {
	if l.WeatherEnabled && l.WAge != nil && l.LocationKey != "" {
		last, err := l.WAge.LastFetchedAt(ctx, l.LocationKey)
		if err != nil {
			l.Log.Error("ops: weather freshness", "err", err)
		} else if last.IsZero() || l.now().Sub(last) > weatherStaleAlert {
			l.ops(ctx, today, "weather_stale", "Weather cache is older than 24 hours; outdoor schedules are degraded.")
		}
	}

	if !l.TadoEnabled {
		return
	}
	st := l.tadoStatus(ctx)
	switch st.State {
	case climate.StateNeedsReauth:
		l.ops(ctx, today, "tado_needs_reauth", "Tado rejected the refresh token. Relink at /settings/tado.")
	}
	if !st.RefreshObtainedAt.IsZero() && l.now().Sub(st.RefreshObtainedAt) >= reauthWarnAfter {
		l.ops(ctx, today, "tado_reauth_soon", "Tado refresh token is older than 21 days. Relink at /settings/tado before it expires.")
	}
	if l.tokenLost() {
		l.ops(ctx, today, "tado_token_lost", "Tado token rotation succeeded at the API but failed to commit. Relink at /settings/tado.")
	}
}

func (l *Loop) ops(ctx context.Context, today domain.Date, reason, detail string) {
	if l.Notify == nil {
		return
	}
	key := "ops:" + reason + ":" + today.String()
	if err := l.Notify.SendOnce(ctx, key, "ops", notify.FormatOps(reason, detail)); err != nil {
		l.Log.Error("sending ops alert", "reason", reason, "err", err)
	}
}

func parseDate(s string) (domain.Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return domain.Date{}, err
	}
	return domain.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}, nil
}
