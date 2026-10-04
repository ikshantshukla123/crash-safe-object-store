// Package config loads process configuration from the environment.
//
// Values are read once at startup and passed explicitly to the components that
// need them, rather than being reachable from anywhere via a package-level
// variable, which avoids global mutable state. Each phase adds only the
// settings it actually uses.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Config is the fully resolved configuration for one process.
type Config struct {
	APIAddr         string
	LogLevel        slog.Level
	DemoMode        bool
	DatabaseURL     string
	RedisAddr       string
	ShutdownTimeout time.Duration
}

// Load reads configuration from the environment.
//
// It reports every problem at once instead of failing on the first one, so a
// misconfigured deploy takes one restart to diagnose rather than five.
func Load() (Config, error) {
	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg := Config{
		APIAddr:   envString("API_ADDR", ":8080"),
		RedisAddr: envString("REDIS_ADDR", "localhost:6379"),
	}

	dbURL, err := envRequired("DATABASE_URL")
	fail(err)
	cfg.DatabaseURL = dbURL

	level, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	fail(err)
	cfg.LogLevel = level

	demo, err := envBool("DEMO_MODE", false)
	fail(err)
	cfg.DemoMode = demo

	timeout, err := envDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	fail(err)
	cfg.ShutdownTimeout = timeout

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("load config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envRequired(key string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required but not set", key)
	}
	return v, nil
}

func envBool(key string, def bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean, got %q: %w", key, raw, err)
	}
	return v, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 15s, got %q: %w", key, raw, err)
	}
	return v, nil
}

func envLogLevel(key string, def slog.Level) (slog.Level, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	var level slog.Level
	// UnmarshalText accepts "debug", "info", "warn", "error" case-insensitively.
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%s must be debug, info, warn or error, got %q: %w", key, raw, err)
	}
	return level, nil
}
