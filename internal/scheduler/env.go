package scheduler

import (
	"context"
	"errors"
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
)

// PlantEnv builds the care.EnvSeries for one plant. web.Server.Env can point here.
func (l *Loop) PlantEnv(ctx context.Context, p domain.Plant, _ domain.Species) care.EnvSeries {
	today := l.today()
	from := today.AddDays(-envLookbackDays)
	to := today.AddDays(envHorizonDays)

	var env care.EnvSeries
	if l.WeatherEnabled && l.Weather != nil {
		got, err := l.Weather.Series(ctx, from, to)
		if err != nil {
			l.Log.Error("reading weather series", "err", err)
		} else {
			env = got
		}
	}

	if p.Location != domain.Indoor || p.TadoRoomID == nil || *p.TadoRoomID == "" || l.Climate == nil {
		if p.Location == domain.Indoor {
			env.IndoorDataStale = true
		}
		return env
	}

	means, err := l.Climate.DailyMeans(ctx, *p.TadoRoomID, from, to)
	if err != nil {
		l.Log.Error("reading indoor daily means", "err", err)
		env.IndoorDataStale = true
		return env
	}
	env.Indoor = make([]care.IndoorDay, 0, len(means))
	for _, m := range means {
		env.Indoor = append(env.Indoor, care.IndoorDay{
			Date:        m.Date,
			TempC:       m.TempC,
			HumidityPct: m.HumidityPct,
		})
	}

	latest, err := l.Climate.LatestSample(ctx, *p.TadoRoomID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			l.Log.Error("reading latest indoor sample", "err", err)
		}
		env.IndoorDataStale = true
		return env
	}
	if l.now().Sub(latest.ObservedAt) > indoorStaleAfter {
		env.IndoorDataStale = true
	}
	return env
}

func (l *Loop) envRange() (from, to domain.Date) {
	today := l.today()
	return today.AddDays(-envLookbackDays), today.AddDays(envHorizonDays)
}

func (l *Loop) outdoorSeries(ctx context.Context) (care.EnvSeries, error) {
	if l.Weather == nil {
		return care.EnvSeries{}, nil
	}
	from, to := l.envRange()
	got, err := l.Weather.Series(ctx, from, to)
	if err != nil {
		return care.EnvSeries{}, fmt.Errorf("weather series: %w", err)
	}
	return got, nil
}
