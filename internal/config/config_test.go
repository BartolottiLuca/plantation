package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("PLANTATION_DATABASE_URL", "postgres://plantation:plantation@localhost:5432/plantation")
	t.Setenv("PLANTATION_TZ", "Europe/London")
	t.Setenv("PLANTATION_BASE_URL", "http://localhost:8080")
}

func TestLoadMinimalBoot(t *testing.T) {
	setRequired(t)
	t.Setenv("PLANTATION_HTTP_ADDR", "")
	t.Setenv("PLANTATION_DIGEST_HOUR", "")
	t.Setenv("PLANTATION_LOG_LEVEL", "")
	t.Setenv("PLANTATION_WEATHER_ENABLED", "")
	t.Setenv("PLANTATION_LATITUDE", "")
	t.Setenv("PLANTATION_LONGITUDE", "")
	t.Setenv("PLANTATION_TADO_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("minimal boot: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DigestHour != 9 {
		t.Fatalf("DigestHour = %d, want 9", cfg.DigestHour)
	}
	if cfg.WeatherEnabled {
		t.Fatal("weather should be off when coordinates are absent")
	}
	if !cfg.TadoEnabled {
		t.Fatal("TadoEnabled default is true")
	}
	if cfg.Location == nil || cfg.Location.String() != "Europe/London" {
		t.Fatalf("Location = %v", cfg.Location)
	}
}

func TestLoadWeatherWhenCoordsPresent(t *testing.T) {
	setRequired(t)
	t.Setenv("PLANTATION_LATITUDE", "51.5")
	t.Setenv("PLANTATION_LONGITUDE", "-0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.WeatherEnabled {
		t.Fatal("weather should be on when coordinates are set")
	}
	if cfg.Latitude != 51.5 || cfg.Longitude != -0.1 {
		t.Fatalf("coords = %v,%v", cfg.Latitude, cfg.Longitude)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantSub string
	}{
		{
			name:    "missing database url",
			env:     map[string]string{"PLANTATION_DATABASE_URL": ""},
			wantSub: "PLANTATION_DATABASE_URL",
		},
		{
			name:    "missing timezone",
			env:     map[string]string{"PLANTATION_TZ": ""},
			wantSub: "PLANTATION_TZ",
		},
		{
			name:    "invalid timezone",
			env:     map[string]string{"PLANTATION_TZ": "Not/A/Zone"},
			wantSub: "PLANTATION_TZ",
		},
		{
			name:    "missing base url",
			env:     map[string]string{"PLANTATION_BASE_URL": ""},
			wantSub: "PLANTATION_BASE_URL",
		},
		{
			name:    "relative base url",
			env:     map[string]string{"PLANTATION_BASE_URL": "/plants"},
			wantSub: "PLANTATION_BASE_URL",
		},
		{
			name:    "digest hour too high",
			env:     map[string]string{"PLANTATION_DIGEST_HOUR": "24"},
			wantSub: "PLANTATION_DIGEST_HOUR",
		},
		{
			name:    "digest hour negative",
			env:     map[string]string{"PLANTATION_DIGEST_HOUR": "-1"},
			wantSub: "PLANTATION_DIGEST_HOUR",
		},
		{
			name:    "digest hour not a number",
			env:     map[string]string{"PLANTATION_DIGEST_HOUR": "nine"},
			wantSub: "PLANTATION_DIGEST_HOUR",
		},
		{
			name:    "bad log level",
			env:     map[string]string{"PLANTATION_LOG_LEVEL": "trace"},
			wantSub: "PLANTATION_LOG_LEVEL",
		},
		{
			name: "weather explicitly on without coords",
			env: map[string]string{
				"PLANTATION_WEATHER_ENABLED": "true",
				"PLANTATION_LATITUDE":        "",
				"PLANTATION_LONGITUDE":       "",
			},
			wantSub: "PLANTATION_LATITUDE",
		},
		{
			name: "weather on missing longitude",
			env: map[string]string{
				"PLANTATION_WEATHER_ENABLED": "true",
				"PLANTATION_LATITUDE":        "51.5",
			},
			wantSub: "PLANTATION_LONGITUDE",
		},
		{
			name:    "latitude out of range",
			env:     map[string]string{"PLANTATION_LATITUDE": "100"},
			wantSub: "PLANTATION_LATITUDE",
		},
		{
			name:    "bad weather flag",
			env:     map[string]string{"PLANTATION_WEATHER_ENABLED": "maybe"},
			wantSub: "PLANTATION_WEATHER_ENABLED",
		},
		{
			name:    "bad tado flag",
			env:     map[string]string{"PLANTATION_TADO_ENABLED": "maybe"},
			wantSub: "PLANTATION_TADO_ENABLED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q does not name %q", err, tt.wantSub)
			}
		})
	}
}

func TestLoadResolvesLocationOnce(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	if got := now.In(cfg.Location).Location().String(); got != "Europe/London" {
		t.Fatalf("resolved location = %s", got)
	}
}
