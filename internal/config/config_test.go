package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	setValidEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.DatabaseMaxConns != 10 || cfg.DatabaseMinConns != 2 {
		t.Fatalf("pool size = %d/%d", cfg.DatabaseMinConns, cfg.DatabaseMaxConns)
	}
	if cfg.DatabaseQueryTimeout != 3*time.Second {
		t.Fatalf("DatabaseQueryTimeout = %s", cfg.DatabaseQueryTimeout)
	}
}

func TestLoadRejectsMissingRequiredValue(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with empty DATABASE_URL")
	}
}

func TestLoadRejectsInvalidPoolRange(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_MIN_CONNS", "11")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with min conns greater than max conns")
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"HTTP_ADDR":                  ":8080",
		"HTTP_READ_TIMEOUT":          "10s",
		"HTTP_READ_HEADER_TIMEOUT":   "5s",
		"HTTP_WRITE_TIMEOUT":         "15s",
		"HTTP_IDLE_TIMEOUT":          "60s",
		"LOG_LEVEL":                  "info",
		"SHUTDOWN_TIMEOUT":           "10s",
		"DATABASE_URL":               "postgres://localhost/tripgo",
		"DATABASE_MAX_CONNS":         "10",
		"DATABASE_MIN_CONNS":         "2",
		"DATABASE_MAX_CONN_LIFETIME": "30m",
		"DATABASE_CONNECT_TIMEOUT":   "5s",
		"DATABASE_QUERY_TIMEOUT":     "3s",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}
