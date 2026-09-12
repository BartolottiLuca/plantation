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
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/BartolottiLuca/plantation/internal/config"
)

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

	srv := &http.Server{
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	log.Info("plantation started",
		"http_addr", ln.Addr().String(),
		"tz", cfg.Timezone,
		"digest_hour", cfg.DigestHour,
		"weather_enabled", cfg.WeatherEnabled,
		"tado_enabled", cfg.TadoEnabled,
		"discord_configured", cfg.DiscordWebhook != "",
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

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
	log.Info("plantation stopped")
	return 0
}
