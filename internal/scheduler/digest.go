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

type scheduled struct {
	Plant   domain.Plant
	Species domain.Species
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

	var tasks []scheduled
	for _, row := range rows {
		if !row.Plant.Active {
			continue
		}
		got, err := l.schedulePlant(ctx, row.Plant, row.Species)
		if err != nil {
			l.Log.Error("scheduling plant", "err", err)
			continue
		}
		tasks = append(tasks, got...)
	}

	l.sendAlerts(ctx, today, rows, tasks)

	var overdue, dueToday []notify.DigestLine
	for _, t := range tasks {
		line := notify.DigestLine{
			PlantID:   t.Plant.ID,
			PlantName: t.Plant.Name,
			Task:      string(t.Due.Kind),
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

func (l *Loop) schedulePlant(ctx context.Context, p domain.Plant, sp domain.Species) ([]scheduled, error) {
	events, err := l.Events.LatestByKind(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("latest care events: %w", err)
	}
	today := l.today()
	env := l.PlantEnv(ctx, p, sp)
	params := care.Effective(p, sp)

	var tasks []store.CareTask
	if l.Tasks != nil {
		tasks, err = l.Tasks.List(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("listing care tasks: %w", err)
		}
	}

	var out []scheduled
	if enabled(tasks, domain.Water, today) {
		due, expl := care.ScheduleWater(params, events, env, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: due, Expl: expl})
	}
	if sp.Prune != nil && enabled(tasks, domain.Prune, today) {
		d, e := care.ScheduleFixed(*sp.Prune, domain.Prune, events, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: d, Expl: e})
	}
	if sp.Fertilize != nil && enabled(tasks, domain.Fertilize, today) {
		d, e := care.ScheduleFixed(*sp.Fertilize, domain.Fertilize, events, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: d, Expl: e})
	}
	if sp.Repot != nil && enabled(tasks, domain.Repot, today) {
		d, e := care.ScheduleFixed(*sp.Repot, domain.Repot, events, today)
		out = append(out, scheduled{Plant: p, Species: sp, Due: d, Expl: e})
	}
	return out, nil
}

func enabled(tasks []store.CareTask, kind domain.TaskKind, today domain.Date) bool {
	for _, t := range tasks {
		if t.Kind != kind {
			continue
		}
		if !t.Enabled {
			return false
		}
		if t.SnoozedUntil != nil && !today.Before(*t.SnoozedUntil) {
			return false
		}
		return true
	}
	return true
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
		for _, day := range series.Days {
			if day.Date.Before(today) || day.Observed {
				continue
			}
			if day.TMinC > row.Species.MinTempC {
				continue
			}
			key := fmt.Sprintf("frost:%s:%s", row.Plant.ID, day.Date)
			url := strings.TrimRight(l.BaseURL, "/") + "/plants/" + row.Plant.ID.String()
			msg := notify.FormatFrost(row.Plant.Name, day.Date, day.TMinC, url)
			if err := l.Notify.SendOnce(ctx, key, "frost", msg); err != nil {
				l.Log.Error("sending frost alert", "err", err)
			}
		}
	}
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
