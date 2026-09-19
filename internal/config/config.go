// Package config parses environment variables into a typed Config, validated at boot.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL    string
	HTTPAddr       string
	Timezone       string
	Location       *time.Location
	DigestHour     int
	BaseURL        string
	Latitude       float64
	Longitude      float64
	WeatherEnabled bool
	TadoEnabled    bool
	DiscordWebhook string
	WriteToken     string
	LogLevel       slog.Level
}

func Load() (Config, error) {
	var cfg Config
	var err error

	cfg.DatabaseURL, err = require("PLANTATION_DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	cfg.Timezone, err = require("PLANTATION_TZ")
	if err != nil {
		return Config{}, err
	}
	cfg.Location, err = time.LoadLocation(cfg.Timezone)
	if err != nil {
		return Config{}, fmt.Errorf("PLANTATION_TZ is not a valid IANA timezone %q: %w", cfg.Timezone, err)
	}
	cfg.BaseURL, err = require("PLANTATION_BASE_URL")
	if err != nil {
		return Config{}, err
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Config{}, err
	}

	cfg.HTTPAddr = envOr("PLANTATION_HTTP_ADDR", ":8080")

	cfg.DigestHour, err = envInt("PLANTATION_DIGEST_HOUR", 9, 0, 23)
	if err != nil {
		return Config{}, err
	}

	cfg.LogLevel, err = parseLogLevel(envOr("PLANTATION_LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	cfg.WriteToken = strings.TrimSpace(os.Getenv("PLANTATION_WRITE_TOKEN"))
	cfg.DiscordWebhook = strings.TrimSpace(os.Getenv("PLANTATION_DISCORD_WEBHOOK_URL"))

	cfg.TadoEnabled, err = envBool("PLANTATION_TADO_ENABLED", true)
	if err != nil {
		return Config{}, err
	}

	weather, weatherSet, err := envBoolSet("PLANTATION_WEATHER_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	lat, latSet, err := envFloat("PLANTATION_LATITUDE")
	if err != nil {
		return Config{}, err
	}
	lon, lonSet, err := envFloat("PLANTATION_LONGITUDE")
	if err != nil {
		return Config{}, err
	}
	if latSet {
		if lat < -90 || lat > 90 {
			return Config{}, fmt.Errorf("PLANTATION_LATITUDE must be between -90 and 90, got %v", lat)
		}
		cfg.Latitude = lat
	}
	if lonSet {
		if lon < -180 || lon > 180 {
			return Config{}, fmt.Errorf("PLANTATION_LONGITUDE must be between -180 and 180, got %v", lon)
		}
		cfg.Longitude = lon
	}

	coordsReady := latSet && lonSet
	switch {
	case weather && coordsReady:
		cfg.WeatherEnabled = true
	case weather && weatherSet && !coordsReady:
		missing := missingCoords(latSet, lonSet)
		return Config{}, fmt.Errorf("PLANTATION_WEATHER_ENABLED is true but %s", missing)
	default:
		cfg.WeatherEnabled = false
	}

	return cfg, nil
}

func require(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback, lo, hi int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be an integer %d–%d, got %q", key, lo, hi, raw)
	}
	return n, nil
}

func envBool(key string, fallback bool) (bool, error) {
	v, _, err := envBoolSet(key, fallback)
	return v, err
}

func envBoolSet(key string, fallback bool) (value bool, set bool, err error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, false, nil
	}
	switch strings.ToLower(raw) {
	case "true", "1", "yes":
		return true, true, nil
	case "false", "0", "no":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("%s must be true or false, got %q", key, raw)
	}
}

func envFloat(key string) (float64, bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, true, fmt.Errorf("%s must be a number, got %q", key, raw)
	}
	return f, true, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("PLANTATION_LOG_LEVEL must be debug|info|warn|error, got %q", raw)
	}
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("PLANTATION_BASE_URL must be an absolute URL, got %q", raw)
	}
	return nil
}

func missingCoords(latSet, lonSet bool) string {
	switch {
	case !latSet && !lonSet:
		return "PLANTATION_LATITUDE and PLANTATION_LONGITUDE are not set"
	case !latSet:
		return "PLANTATION_LATITUDE is not set"
	default:
		return "PLANTATION_LONGITUDE is not set"
	}
}
