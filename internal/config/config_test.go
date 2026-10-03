package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.APIAddr != ":8080" {
		t.Errorf("APIAddr = %q, want \":8080\"", cfg.APIAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.DemoMode {
		t.Error("DemoMode = true, want false by default (it enables the docker.sock chaos controller)")
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("API_ADDR", ":9999")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("DEMO_MODE", "true")
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.APIAddr != ":9999" {
		t.Errorf("APIAddr = %q, want \":9999\"", cfg.APIAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if !cfg.DemoMode {
		t.Error("DemoMode = false, want true")
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", cfg.ShutdownTimeout)
	}
}

func TestLoadMissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no DATABASE_URL, want an error")
	}
}

// Load must report every problem at once, so one restart diagnoses the whole
// misconfiguration instead of revealing it one variable at a time.
func TestLoadReportsAllErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("LOG_LEVEL", "chatty")
	t.Setenv("DEMO_MODE", "yes-please")
	t.Setenv("SHUTDOWN_TIMEOUT", "soon")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded, want an error")
	}
	for _, want := range []string{"DATABASE_URL", "LOG_LEVEL", "DEMO_MODE", "SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}
