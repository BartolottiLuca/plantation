package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

const (
	tickInterval      = 60 * time.Second
	weatherMaxAge     = time.Hour
	sampleMaxAge      = 30 * time.Minute
	indoorStaleAfter  = 3 * time.Hour
	weatherStaleAlert = 24 * time.Hour
	sweepStaleAfter   = 10 * time.Minute
	reauthWarnAfter   = 21 * 24 * time.Hour
	activityTimeout   = 20 * time.Second
	envLookbackDays   = 90
	envHorizonDays    = 16
	heatwaveCelsius   = 32.0
)

// WeatherSource is weather.Service (Refresh + Series). Nil disables weather.
type WeatherSource interface {
	Refresh(ctx context.Context) error
	Series(ctx context.Context, from, to domain.Date) (care.EnvSeries, error)
}

// WeatherAge is *store.WeatherRepo.LastFetchedAt.
type WeatherAge interface {
	LastFetchedAt(ctx context.Context, locationKey string) (time.Time, error)
}

// Sampler is *tado.Sampler.Sample / Status.
type Sampler interface {
	Sample(ctx context.Context) error
	Status(ctx context.Context) climate.LinkStatus
}

// TokenRepo is *store.TadoTokenRepo.Load.
type TokenRepo interface {
	Load(ctx context.Context) (store.TadoToken, error)
}

// TokenRefresher is *tado.Client (NeedsRefresh + Refresh).
type TokenRefresher interface {
	NeedsRefresh(current store.TadoToken, now time.Time) bool
	Refresh(ctx context.Context) error
}

// PlantLister is *store.PlantRepo.List.
type PlantLister interface {
	List(ctx context.Context) ([]store.PlantWithSpecies, error)
}

// EventSource is *store.CareEventRepo.LatestByKind.
type EventSource interface {
	LatestByKind(ctx context.Context, plantID uuid.UUID) ([]domain.CareEvent, error)
}

// TaskLister is *store.CareTaskRepo.List. Nil treats every catalog task as enabled.
type TaskLister interface {
	List(ctx context.Context, plantID uuid.UUID) ([]store.CareTask, error)
}

// ClimateSource is *store.ClimateRepo.
type ClimateSource interface {
	DailyMeans(ctx context.Context, roomID string, from, to domain.Date) ([]store.ClimateDailyMean, error)
	LatestSample(ctx context.Context, roomID string) (store.ClimateSample, error)
}

// Notifier is notify.Notifier. Empty messages become skipped rows.
type Notifier interface {
	SendOnce(ctx context.Context, dedupeKey, kind string, msg notify.Message) error
}

// Sweeper is *notify.OutboxNotifier.SweepStale. Nil skips the sweep.
type Sweeper interface {
	SweepStale(ctx context.Context, olderThan time.Duration) error
}

// Loop is the scheduler. Construct with New; call Run from cmd.
type Loop struct {
	Clock          care.Clock
	Location       *time.Location
	DigestHour     int
	BaseURL        string
	LocationKey    string
	WeatherEnabled bool
	TadoEnabled    bool

	Lock    Locker
	Weather WeatherSource
	WAge    WeatherAge
	Sampler Sampler
	Token   TokenRepo
	Auth    TokenRefresher
	Plants  PlantLister
	Events  EventSource
	Tasks   TaskLister
	Climate ClimateSource
	Notify  Notifier
	Sweep   Sweeper
	Log     *slog.Logger

	// Ticker is the wait between ticks. Tests replace it with a tiny duration
	// or ignore Run and call Tick directly.
	Ticker time.Duration

	state *loopState
}

type loopState struct {
	mu        sync.Mutex
	holding   bool
	lastTick  time.Time
	lostToken bool
}

// New copies opts and fills defaults. Clock is required.
func New(opts Loop) *Loop {
	l := opts
	l.state = &loopState{}
	if l.Location == nil {
		l.Location = time.UTC
	}
	if l.Lock == nil {
		l.Lock = AlwaysLock{}
	}
	if l.Ticker <= 0 {
		l.Ticker = tickInterval
	}
	if l.Log == nil {
		l.Log = slog.Default()
	}
	return &l
}

// HoldsLock reports whether this process currently owns the scheduler lock.
func (l *Loop) HoldsLock() bool {
	if l.state == nil {
		return false
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	return l.state.holding
}

// LastTick is the clock time of the most recent Tick that ran (zero if none).
func (l *Loop) LastTick() time.Time {
	if l.state == nil {
		return time.Time{}
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	return l.state.lastTick
}

// Run acquires the advisory lock, sweeps once, then ticks immediately and
// every Ticker until ctx is cancelled. If the lock is not acquired it logs
// once and returns — HTTP on this replica keeps serving.
func (l *Loop) Run(ctx context.Context) {
	if l.Clock == nil {
		l.Log.Error("scheduler refused to start", "err", "clock is required")
		return
	}
	held, release, err := l.Lock.TryHold(ctx)
	if err != nil {
		l.Log.Error("scheduler lock failed", "err", err)
		return
	}
	if !held {
		l.Log.Info("scheduler lock not acquired; this replica will serve HTTP only")
		return
	}
	l.setHolding(true)
	defer func() {
		release()
		l.setHolding(false)
	}()

	l.runActivity(ctx, "sweep", l.sweep)
	l.Tick(ctx)

	ticker := time.NewTicker(l.Ticker)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Tick(ctx)
		}
	}
}

// Tick evaluates every SPEC.md §9 condition once. A failing activity is
// logged; the others still run. Each activity has its own timeout.
func (l *Loop) Tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := l.now()
	if l.state != nil {
		l.state.mu.Lock()
		l.state.lastTick = now
		l.state.mu.Unlock()
	}

	l.runActivity(ctx, "weather", l.refreshWeather)
	l.runActivity(ctx, "tado_sample", l.sampleTado)
	l.runActivity(ctx, "tado_token", l.refreshToken)
	if l.pastDigestHour(now) {
		l.runActivity(ctx, "digest", l.digestAndAlerts)
	}
	if now.In(l.loc()).Minute() == 0 {
		l.runActivity(ctx, "sweep", l.sweep)
	}
}

func (l *Loop) runActivity(ctx context.Context, name string, fn func(context.Context) error) {
	actx, cancel := context.WithTimeout(ctx, activityTimeout)
	defer cancel()
	if err := fn(actx); err != nil {
		l.Log.Error("scheduler activity failed", "activity", name, "err", err)
	}
}

func (l *Loop) now() time.Time {
	if l.Clock == nil {
		return time.Time{}
	}
	return l.Clock.Now()
}

func (l *Loop) loc() *time.Location {
	if l.Location == nil {
		return time.UTC
	}
	return l.Location
}

func (l *Loop) today() domain.Date {
	return domain.TodayIn(l.now(), l.loc())
}

func (l *Loop) pastDigestHour(now time.Time) bool {
	return now.In(l.loc()).Hour() >= l.DigestHour
}

func (l *Loop) setHolding(v bool) {
	if l.state == nil {
		return
	}
	l.state.mu.Lock()
	l.state.holding = v
	l.state.mu.Unlock()
}
