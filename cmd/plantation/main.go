package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/climate/tado"
	"github.com/BartolottiLuca/plantation/internal/config"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/scheduler"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/BartolottiLuca/plantation/internal/weather"
	"github.com/BartolottiLuca/plantation/internal/web"
)

// wallClock is cmd's one clock of record. Packages take a Clock; they
// must not call time.Now themselves (AGENTS.md).
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

var _ care.Clock = wallClock{}
var _ tado.Clock = wallClock{}

// dbGate lets /healthz come up before Postgres. /readyz pings only after Open+Migrate.
type dbGate struct {
	mu   sync.RWMutex
	pool *pgxpool.Pool
}

func (g *dbGate) set(p *pgxpool.Pool) {
	g.mu.Lock()
	g.pool = p
	g.mu.Unlock()
}

func (g *dbGate) get() *pgxpool.Pool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.pool
}

func (g *dbGate) ping(ctx context.Context) error {
	p := g.get()
	if p == nil {
		return errors.New("database not connected")
	}
	return p.Ping(ctx)
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: plantation <command>")
		fmt.Fprintln(os.Stderr, "commands: serve, backfill-weather, send-test-digest")
		return 2
	}
	switch args[0] {
	case "serve":
		return serve()
	case "backfill-weather":
		return backfillWeather(args[1:])
	case "send-test-digest":
		return sendTestDigest()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func serve() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}

	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	log := slog.New(h)
	slog.SetDefault(log)

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		log.Error("listen", "err", err)
		return 1
	}

	mux := http.NewServeMux()
	db := &dbGate{}
	web.RegisterHealth(mux, db.ping)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	go func() {
		pool, err := store.Open(ctx, cfg.DatabaseURL, log)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("database open", "err", err)
			}
			return
		}
		if err := store.Migrate(ctx, pool); err != nil {
			log.Error("migrate", "err", err)
			pool.Close()
			return
		}

		// Species catalog is rebuilt from catalog/species/*.yaml at every
		// boot (AGENTS.md: "no migration, no manual SQL" for adding a
		// species) — a bad YAML file must not bring up a stale catalog
		// silently, so a load or upsert failure is fatal, same as a failed
		// migration.
		species, err := catalog.Load()
		if err != nil {
			log.Error("loading species catalog", "err", err)
			pool.Close()
			return
		}
		if err := catalog.Upsert(ctx, store.NewSpeciesRepo(pool), species); err != nil {
			log.Error("upserting species catalog", "err", err)
			pool.Close()
			return
		}
		log.Info("species catalog upserted", "count", len(species))

		// Mount UI and start the scheduler before flipping /readyz so a
		// ready pod already has routes and the tick is running.
		startRuntime(ctx, mux, pool, cfg, log)
		db.set(pool)
		log.Info("database ready")
	}()

	log.Info("plantation started",
		"version", web.Version,
		"commit", web.Commit,
		"http_addr", ln.Addr().String(),
		"tz", cfg.Timezone,
		"digest_hour", cfg.DigestHour,
		"weather_enabled", cfg.WeatherEnabled,
		"tado_enabled", cfg.TadoEnabled,
		"discord_configured", cfg.DiscordWebhook != "",
	)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Error("http server", "err", err)
			return 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
		return 1
	}
	if p := db.get(); p != nil {
		p.Close()
	}
	log.Info("plantation stopped")
	return 0
}

func startRuntime(ctx context.Context, mux *http.ServeMux, pool *pgxpool.Pool, cfg config.Config, log *slog.Logger) {
	clk := wallClock{}
	plantRepo := store.NewPlantRepo(pool)
	eventRepo := store.NewCareEventRepo(pool)
	climateRepo := store.NewClimateRepo(pool)
	tokenRepo := store.NewTadoTokenRepo(pool)

	var weatherSvc *weather.Service
	var weatherRepo *store.WeatherRepo
	var weatherAge scheduler.WeatherAge
	var locationKey string
	if cfg.WeatherEnabled {
		weatherRepo = store.NewWeatherRepo(pool)
		weatherSvc = weather.NewService(weather.NewClient(cfg.Timezone), weatherRepo, cfg.Latitude, cfg.Longitude, clk, log)
		weatherAge = weatherRepo
		locationKey = fmt.Sprintf("%.3f,%.3f", cfg.Latitude, cfg.Longitude)
	}

	var auth *tado.Client
	var sampler *tado.Sampler
	if cfg.TadoEnabled {
		auth = tado.NewClient(tokenRepo, tado.WithClock(clk))
		sampler = tado.NewSampler(auth, climateRepo, plantRepo)
	}

	var notifier scheduler.Notifier = &notify.NoopNotifier{}
	var uiNotify notify.Notifier
	var sweeper scheduler.Sweeper
	if cfg.DiscordWebhook != "" {
		outbox := notify.NewOutboxNotifier(store.NewNotificationRepo(pool), cfg.DiscordWebhook, notify.WithNow(clk.Now))
		notifier = outbox
		sweeper = outbox
		uiNotify = outbox
	}

	loop := scheduler.New(scheduler.Loop{
		Clock:          clk,
		Location:       cfg.Location,
		DigestHour:     cfg.DigestHour,
		BaseURL:        cfg.BaseURL,
		LocationKey:    locationKey,
		WeatherEnabled: cfg.WeatherEnabled,
		TadoEnabled:    cfg.TadoEnabled,
		Lock:           scheduler.PoolLocker{Pool: pool},
		Weather:        weatherSource(weatherSvc),
		WAge:           weatherAge,
		Sampler:        asSampler(sampler),
		Token:          tokenRepo,
		Auth:           asAuth(auth),
		Plants:         plantRepo,
		Events:         eventRepo,
		Tasks:          store.NewCareTaskRepo(pool),
		Climate:        climateRepo,
		Notify:         notifier,
		Sweep:          sweeper,
		Retain:         store.NewRetentionRepo(pool),
		Log:            log,
	})

	ui := web.NewServer(web.Server{
		Plants:       plantRepo,
		Species:      store.NewSpeciesRepo(pool),
		Events:       eventRepo,
		Clock:        clk,
		Location:     cfg.Location,
		WriteToken:   cfg.WriteToken,
		Rooms:        roomLister(sampler),
		Env:          loop.PlantEnv,
		Tado:         tadoLinker(auth),
		Notifier:     uiNotify,
		BaseURL:      cfg.BaseURL,
		WeatherAge:   webWeatherAge(weatherRepo),
		Weather:      weatherSeries(weatherSvc),
		LocationKey:  locationKey,
		Scheduler:    loop,
		CatalogReady: true, // startRuntime only runs after a successful upsert
	})
	ui.Register(mux)

	go loop.Run(ctx)
}

func weatherSource(svc *weather.Service) scheduler.WeatherSource {
	if svc == nil {
		return nil
	}
	return svc
}

func roomLister(s *tado.Sampler) web.RoomLister {
	if s == nil {
		return nil
	}
	return s
}

func asSampler(s *tado.Sampler) scheduler.Sampler {
	if s == nil {
		return nil
	}
	return s
}

func asAuth(c *tado.Client) scheduler.TokenRefresher {
	if c == nil {
		return nil
	}
	return c
}

func tadoLinker(c *tado.Client) web.TadoLinker {
	if c == nil {
		return nil
	}
	return c
}

func weatherSeries(s *weather.Service) web.WeatherSeries {
	if s == nil {
		return nil
	}
	return s
}

func webWeatherAge(r *store.WeatherRepo) web.WeatherAge {
	if r == nil {
		return nil
	}
	return r
}

// backfillWeather is a one-off wider fetch (default 30 days of history vs.
// Refresh's fixed 7, per SPEC.md §10.1) for recovering the cache after an
// outage longer than a week. It opens and migrates its own pool rather than
// requiring serve to be running, since it is meant to be run by hand.
func backfillWeather(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	if !cfg.WeatherEnabled {
		fmt.Fprintln(os.Stderr, "backfill-weather: weather is not enabled "+
			"(set PLANTATION_WEATHER_ENABLED=true with PLANTATION_LATITUDE/PLANTATION_LONGITUDE)")
		return 1
	}

	wideDays := 30
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n <= 0 {
			fmt.Fprintf(os.Stderr, "backfill-weather: days must be a positive integer, got %q\n", args[0])
			return 2
		}
		wideDays = n
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		log.Error("database open", "err", err)
		return 1
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		log.Error("migrate", "err", err)
		return 1
	}

	client := weather.NewClient(cfg.Timezone)
	repo := store.NewWeatherRepo(pool)
	if err := weather.RunBackfill(ctx, client, repo, cfg.Latitude, cfg.Longitude, wideDays); err != nil {
		log.Error("backfill weather", "err", err)
		return 1
	}
	log.Info("weather backfill complete", "days", wideDays)
	return 0
}

// sendTestDigest sends a single synthetic digest through the real Discord
// outbox (SendOnce's claim-then-send protocol, per SPEC.md §8), so an
// operator can verify PLANTATION_DISCORD_WEBHOOK_URL end-to-end. It needs a
// live pool because OutboxNotifier's claim row lives in Postgres, same as
// the eventual scheduled sends.
func sendTestDigest() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	if cfg.DiscordWebhook == "" {
		fmt.Fprintln(os.Stderr, "send-test-digest: PLANTATION_DISCORD_WEBHOOK_URL is not set")
		return 1
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		log.Error("database open", "err", err)
		return 1
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		log.Error("migrate", "err", err)
		return 1
	}

	notifier := notify.NewOutboxNotifier(store.NewNotificationRepo(pool), cfg.DiscordWebhook)
	// Pass time.Now explicitly rather than relying on RunSendTestDigest's
	// nil-fallback, so the clock read stays visibly owned by cmd (AGENTS.md:
	// "no time.Now() outside cmd").
	if err := notify.RunSendTestDigest(ctx, notifier, cfg.BaseURL, time.Now); err != nil {
		log.Error("send test digest", "err", err)
		return 1
	}
	log.Info("test digest sent")
	return 0
}
