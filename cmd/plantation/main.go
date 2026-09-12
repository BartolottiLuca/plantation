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
	"sync"
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BartolottiLuca/plantation/internal/config"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/BartolottiLuca/plantation/internal/web"
)

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
		fmt.Fprintln(os.Stderr, "commands: serve")
		return 2
	}
	switch args[0] {
	case "serve":
		return serve()
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
