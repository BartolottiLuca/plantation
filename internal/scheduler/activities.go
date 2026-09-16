package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/climate/tado"
	"github.com/BartolottiLuca/plantation/internal/store"
)

func (l *Loop) refreshWeather(ctx context.Context) error {
	if !l.WeatherEnabled || l.Weather == nil {
		return nil
	}
	if l.WAge != nil && l.LocationKey != "" {
		last, err := l.WAge.LastFetchedAt(ctx, l.LocationKey)
		if err != nil {
			return fmt.Errorf("reading weather freshness: %w", err)
		}
		if !last.IsZero() && l.now().Sub(last) < weatherMaxAge {
			return nil
		}
	}
	return l.Weather.Refresh(ctx)
}

func (l *Loop) sampleTado(ctx context.Context) error {
	if !l.TadoEnabled || l.Sampler == nil {
		return nil
	}
	due, err := l.sampleDue(ctx)
	if err != nil {
		return err
	}
	if !due {
		return nil
	}
	return l.Sampler.Sample(ctx)
}

func (l *Loop) sampleDue(ctx context.Context) (bool, error) {
	if l.Climate == nil || l.Plants == nil {
		return true, nil
	}
	rows, err := l.Plants.List(ctx)
	if err != nil {
		return false, fmt.Errorf("listing plants for climate freshness: %w", err)
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if !row.Plant.Active || row.Plant.TadoRoomID == nil || *row.Plant.TadoRoomID == "" {
			continue
		}
		seen[*row.Plant.TadoRoomID] = struct{}{}
	}
	if len(seen) == 0 {
		return false, nil
	}
	var newest time.Time
	found := false
	for roomID := range seen {
		s, err := l.Climate.LatestSample(ctx, roomID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return false, fmt.Errorf("latest climate sample: %w", err)
		}
		if !found || s.ObservedAt.After(newest) {
			newest = s.ObservedAt
			found = true
		}
	}
	if !found {
		return true, nil
	}
	return l.now().Sub(newest) > sampleMaxAge, nil
}

func (l *Loop) refreshToken(ctx context.Context) error {
	if !l.TadoEnabled || l.Token == nil || l.Auth == nil {
		return nil
	}
	tok, err := l.Token.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading tado token: %w", err)
	}
	if !l.Auth.NeedsRefresh(tok, l.now()) {
		return nil
	}
	if err := l.Auth.Refresh(ctx); err != nil {
		if errors.Is(err, tado.ErrTokenLost) {
			l.setTokenLost(true)
			return nil
		}
		if errors.Is(err, tado.ErrNeedsReauth) {
			return nil
		}
		return err
	}
	return nil
}

func (l *Loop) setTokenLost(v bool) {
	if l.state == nil {
		return
	}
	l.state.mu.Lock()
	l.state.lostToken = v
	l.state.mu.Unlock()
}

func (l *Loop) tokenLost() bool {
	if l.state == nil {
		return false
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	return l.state.lostToken
}

func (l *Loop) sweep(ctx context.Context) error {
	if l.Sweep == nil {
		return nil
	}
	return l.Sweep.SweepStale(ctx, sweepStaleAfter)
}

func (l *Loop) tadoStatus(ctx context.Context) climate.LinkStatus {
	if l.Sampler != nil {
		return l.Sampler.Status(ctx)
	}
	return climate.LinkStatus{State: climate.StateUnlinked}
}
